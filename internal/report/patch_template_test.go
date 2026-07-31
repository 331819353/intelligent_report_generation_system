package report

import (
	"encoding/json"
	"os"
	"testing"

	"intelligent-report-generation-system/internal/reportjson"
)

func TestTemplateUpdateHasDedicatedSemanticBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../api/examples/report-json-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := reportjson.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	before := prepared.Document
	after := before
	after.Template = testReportTemplate()
	if err := reportjson.Validate(after); err != nil {
		t.Fatalf("template contract rejected: %v", err)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportjson.Prepare(encoded); err != nil {
		t.Fatalf("template did not survive strict decode: %v", err)
	}

	if err := validateChangeSemantics(before, after, DraftChange{OperationType: "TEMPLATE_UPDATE"}); err != nil {
		t.Fatalf("template update rejected: %v", err)
	}
	if err := validateChangeSemantics(after, before, DraftChange{
		OperationType: "UNDO",
		Target:        ChangeTarget{ReferencedOperationID: "template-operation"},
	}); err != nil {
		t.Fatalf("template undo rejected: %v", err)
	}

	changedReport := after
	changedReport.Report.Name = "不应混入模板修改"
	if err := validateChangeSemantics(before, changedReport, DraftChange{OperationType: "TEMPLATE_UPDATE"}); err == nil {
		t.Fatal("template update accepted unrelated report metadata")
	}
}

func testReportTemplate() *reportjson.ReportTemplate {
	return &reportjson.ReportTemplate{
		ID:            "template_business_light",
		Name:          "商务浅色",
		PromptContext: "使用克制、专业、清晰的经营分析风格。",
		Typography: reportjson.TemplateTypography{
			FontFamily: "SYSTEM",
			Title:      reportjson.TemplateTitleText{FontSize: 18, Color: "#172033", FontWeight: 700},
			Body:       reportjson.TemplateBodyText{FontSize: 12, Color: "#344054"},
		},
		Palette: reportjson.TemplatePalette{Primary: "#2864DC", Accent: "#0E7490", Muted: "#667085"},
		Canvas:  reportjson.TemplateCanvas{BackgroundColor: "#F8FAFC", GridColor: "#DFE6EE"},
		Block:   reportjson.TemplateBlock{BackgroundColor: "#FFFFFF", BorderColor: "#E1E7EF", BorderRadius: 14, Padding: 2, Shadow: "SOFT"},
	}
}
