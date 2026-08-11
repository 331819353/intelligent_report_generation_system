package operation

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestGuardAIAllowsInitialBlankReportGenerationOnly(t *testing.T) {
	definition := guardDefinition(t)
	definition.Pages[0].Sections = []report.Section{}
	definition.Components = []report.Component{}
	pageID := definition.Pages[0].ID
	runID := askdata.ID("ai-run-create")
	replacement := definition
	replacement.Pages = append([]report.Page(nil), definition.Pages...)
	replacement.Pages[0].Sections = []report.Section{{ID: "ai_section", Name: "AI section", Order: 1, Blocks: []report.Block{}}}
	bundle := Bundle{
		SchemaVersion: SchemaVersion, ReportID: definition.Metadata.ID, BaseRevision: 0,
		Source: SourceAI, AIRunID: &runID, Scope: &Scope{PageID: &pageID},
		Operations: []Operation{{Op: ReportCreate, TargetID: definition.Metadata.ID, Payload: &ReportCreatePayload{Definition: replacement}}},
	}
	if err := GuardAI(bundle, &definition); err != nil {
		t.Fatalf("initial blank generation rejected: %v", err)
	}
	definition.Pages[0].Sections = replacement.Pages[0].Sections
	if err := GuardAI(bundle, &definition); err == nil {
		t.Fatal("REPORT_CREATE was accepted for a non-blank report")
	}
}

func guardDefinition(t *testing.T) report.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := report.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
