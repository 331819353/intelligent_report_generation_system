package publication

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
)

type staticDocumentRows struct{ rows ExportRows }

func (source staticDocumentRows) Rows(context.Context, ExportClaim) (ExportRows, error) {
	return source.rows, nil
}

func TestRuntimeDocumentExportGeneratorProducesPortablePNGAndPDF(t *testing.T) {
	definition := documentExportDefinition(t)
	componentID := string(definition.Components[0].ID)
	pageID := string(definition.Pages[0].ID)
	source := staticDocumentRows{rows: ExportRows{
		Columns: []string{"page_id", "component_id", "role", "row_no", "column_name", "value", "partial"},
		Rows: [][]any{
			{pageID, componentID, "DATA", 0, "月份", "2026-07", false},
			{pageID, componentID, "DATA", 0, "销售额", 1280000, false},
			{pageID, componentID, "DATA", 1, "月份", "2026-08", false},
			{pageID, componentID, "DATA", 1, "销售额", 1360000, false},
		},
	}}
	fonts, err := LoadDocumentFontSet("")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewRuntimeDocumentExportGenerator(source, fonts)
	if err != nil {
		t.Fatal(err)
	}
	footer := ExportFooter{
		ReportVersion: 1,
		AsOf:          "2026-08-12T10:00:00+08:00",
		Filters:       map[string]any{"区域": "全国"},
		ExportedAt:    "2026-08-12T02:00:00Z",
		ExportedBy:    askdata.ID("00000000-0000-4000-8000-000000000001"),
	}
	for _, format := range []ExportFormat{ExportPNG, ExportPDF} {
		t.Run(string(format), func(t *testing.T) {
			artifact, generateErr := generator.Generate(context.Background(), ExportClaim{
				ExportJob: ExportJob{Format: format}, Definition: definition,
			}, footer)
			if generateErr != nil {
				t.Fatal(generateErr)
			}
			if validateErr := artifact.Validate(format); validateErr != nil {
				t.Fatal(validateErr)
			}
			if format == ExportPNG {
				config, decodeErr := png.DecodeConfig(bytes.NewReader(artifact.Bytes))
				if decodeErr != nil || config.Width != documentCanvasWidth || config.Height < 500 {
					t.Fatalf("PNG config = %#v, err = %v", config, decodeErr)
				}
				return
			}
			if !bytes.HasPrefix(artifact.Bytes, []byte("%PDF-1.4")) ||
				!bytes.HasSuffix(artifact.Bytes, []byte("%%EOF\n")) ||
				!bytes.Contains(artifact.Bytes, []byte("/MediaBox [0 0 842.00 595.00]")) {
				t.Fatal("PDF is not a self-contained A4 landscape document")
			}
		})
	}
}

func TestLoadDocumentFontSetRejectsMissingExplicitFont(t *testing.T) {
	if _, err := LoadDocumentFontSet(filepath.Join(t.TempDir(), "missing-font.ttc")); err == nil {
		t.Fatal("LoadDocumentFontSet() succeeded for missing explicit font")
	}
}

func documentExportDefinition(t *testing.T) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve document export fixture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Components) == 0 || len(definition.Pages) == 0 || strings.TrimSpace(string(definition.Components[0].ID)) == "" {
		t.Fatal("document export fixture has no renderable component")
	}
	return definition
}
