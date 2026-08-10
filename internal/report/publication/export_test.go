package publication

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
)

type fixedExportRows struct{}

func (fixedExportRows) Rows(context.Context, ExportClaim) (ExportRows, error) {
	return ExportRows{Columns: []string{"region", "amount"}, Rows: [][]any{{"East", "12.50"}, {"West", "7.25"}}}, nil
}

func TestTabularExportGeneratorIncludesPinnedFooter(t *testing.T) {
	footer := ExportFooter{ReportVersion: 7, AsOf: "2026-08-09T10:00:00+08:00",
		Filters: map[string]any{"region": "East"}, ExportedAt: "2026-08-09T10:01:00Z",
		ExportedBy: askdata.ID("00000000-0000-0000-0000-000000000011")}
	generator := TabularExportGenerator{Source: fixedExportRows{}}

	csvArtifact, err := generator.Generate(context.Background(), ExportClaim{ExportJob: ExportJob{Format: ExportCSV}}, footer)
	if err != nil || csvArtifact.Validate(ExportCSV) != nil {
		t.Fatalf("CSV export error = %v, artifact = %#v", err, csvArtifact)
	}
	rows, err := csv.NewReader(bytes.NewReader(csvArtifact.Bytes)).ReadAll()
	if err != nil || len(rows) != 8 || rows[3][0] != "# reportVersion" || rows[7][1] != string(footer.ExportedBy) {
		t.Fatalf("CSV rows = %#v, error = %v", rows, err)
	}

	xlsxArtifact, err := generator.Generate(context.Background(), ExportClaim{ExportJob: ExportJob{Format: ExportXLSX}}, footer)
	if err != nil || xlsxArtifact.Validate(ExportXLSX) != nil {
		t.Fatalf("XLSX export error = %v, artifact = %#v", err, xlsxArtifact)
	}
	reader, err := zip.NewReader(bytes.NewReader(xlsxArtifact.Bytes), int64(len(xlsxArtifact.Bytes)))
	if err != nil || len(reader.File) == 0 {
		t.Fatalf("XLSX is not a ZIP workbook: %v", err)
	}
}

func TestHTTPDocumentExportGeneratorPinsIdentityAndDisablesLazyLoading(t *testing.T) {
	wanted := []byte("%PDF-1.7\ncontrolled")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer renderer-secret" {
			t.Error("renderer token is missing")
		}
		body, _ := io.ReadAll(request.Body)
		var input map[string]any
		if json.Unmarshal(body, &input) != nil || input["disableLazyLoading"] != true || input["requestedBy"] == "" || input["reportVersionId"] == "" {
			t.Errorf("renderer input = %s", body)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(wanted)
	}))
	defer server.Close()
	generator, err := NewHTTPDocumentExportGenerator(server.URL, "renderer-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	footer := ExportFooter{ReportVersion: 2, AsOf: "2026-08-09T10:00:00+08:00",
		Filters: map[string]any{}, ExportedAt: time.Now().UTC().Format(time.RFC3339),
		ExportedBy: askdata.ID("00000000-0000-0000-0000-000000000011")}
	claim := ExportClaim{ExportJob: ExportJob{Format: ExportPDF,
		ReportID:        askdata.ID("00000000-0000-0000-0000-000000000021"),
		ReportVersionID: askdata.ID("00000000-0000-0000-0000-000000000022"),
		RequestedBy:     footer.ExportedBy}}
	artifact, err := generator.Generate(context.Background(), claim, footer)
	if err != nil || artifact.Validate(ExportPDF) != nil || !bytes.Equal(artifact.Bytes, wanted) {
		t.Fatalf("PDF export = %#v, error = %v", artifact, err)
	}
}

func TestExportArtifactRejectsTampering(t *testing.T) {
	artifact := ExportArtifact{Bytes: []byte("png"), ContentType: "image/png", Extension: "png",
		Footer: ExportFooter{ReportVersion: 1, AsOf: "now", ExportedAt: "now",
			ExportedBy: askdata.ID("00000000-0000-0000-0000-000000000011")}}
	artifact.Seal()
	artifact.Bytes[0] = 'P'
	if artifact.Validate(ExportPNG) == nil {
		t.Fatal("tampered export artifact was accepted")
	}
}

func TestFlattenExportResultsPreservesEveryComponentAndComparisonPlan(t *testing.T) {
	plan := reportruntime.ExecutionPlan{Components: []reportruntime.ComponentPlan{
		{PageID: "page_b", ComponentID: "component_b"},
		{PageID: "page_a", ComponentID: "component_a"},
	}}
	results := []reportruntime.ComponentResult{
		{ComponentID: "component_b", State: reportruntime.StatePartial, Result: &reportruntime.QueryResult{
			Columns: []string{"region", "sales"}, Rows: [][]any{{"East", 12}}, Partial: true,
		}},
		{ComponentID: "component_a", State: reportruntime.StateReady, Result: &reportruntime.QueryResult{
			Plans: []reportruntime.QueryPlanResult{
				{Role: "CURRENT", Columns: []string{"sales"}, Rows: [][]any{{20}}},
				{Role: "BASELINE", Columns: []string{"sales"}, Rows: [][]any{{18}}},
			},
		}},
	}
	flattened, err := flattenExportResults(plan, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(flattened.Columns) != 7 || len(flattened.Rows) != 4 ||
		flattened.Rows[0][1] != askdata.ID("component_a") || flattened.Rows[0][2] != "CURRENT" ||
		flattened.Rows[1][2] != "BASELINE" || flattened.Rows[2][1] != askdata.ID("component_b") ||
		flattened.Rows[2][6] != true {
		t.Fatalf("flattened export = %#v", flattened)
	}
}

func TestFlattenExportResultsFailsClosedOnComponentError(t *testing.T) {
	_, err := flattenExportResults(
		reportruntime.ExecutionPlan{Components: []reportruntime.ComponentPlan{{
			PageID: "page_a", ComponentID: "component_a",
		}}},
		[]reportruntime.ComponentResult{{
			ComponentID: "component_a", State: reportruntime.StateNoPermission,
		}},
	)
	if err == nil {
		t.Fatal("permission failure was silently exported")
	}
}
