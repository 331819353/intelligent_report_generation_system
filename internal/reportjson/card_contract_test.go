package reportjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestCardDSLExampleMatchesServerContract(t *testing.T) {
	payload, err := os.ReadFile("../../api/examples/report-1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(payload); err != nil {
		t.Fatal(err)
	}
}

func TestCardDSLProducesIndependentCanonicalArtifact(t *testing.T) {
	document := cardTestDocument("DRAFT")
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"canvas"`)) || bytes.Contains(payload, []byte(`"pages"`)) {
		t.Fatalf("card DSL leaked legacy structures: %s", payload)
	}
	prepared, err := Prepare(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Document.IsCardDSL() || prepared.Document.Report.Name != prepared.Document.Report.Title {
		t.Fatalf("card DSL was not normalized: %#v", prepared.Document.Report)
	}
}

func TestCardDSLDraftAllowsIncompleteBindingButPublishedArtifactRejectsIt(t *testing.T) {
	draft := cardTestDocument("DRAFT")
	draft.Cards = append(draft.Cards, Card{
		ID: "chart", Type: "CHART", CardVersion: "1.0.0",
		Layout:       map[string]Grid{"lg": {X: 0, Y: 10, W: 6, H: 20}, "md": {X: 0, Y: 10, W: 12, H: 20}, "sm": {X: 0, Y: 10, W: 12, H: 20}},
		Appearance:   CardAppearance{Title: "未完成图表"},
		Binding:      CardBinding{Metrics: []MetricBinding{}, Dimensions: []DimensionBinding{}, GlobalFilterBindings: []GlobalFilterBinding{}, Filters: []CardFilter{}, Sort: []CardSort{}},
		Interactions: []CardInteraction{},
	})
	payload, _ := json.Marshal(draft)
	if _, err := Prepare(payload); err != nil {
		t.Fatalf("draft should preserve incomplete editor work: %v", err)
	}
	draft.Report.Status = "PUBLISHED"
	payload, _ = json.Marshal(draft)
	_, err := Prepare(payload)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("published artifact accepted incomplete binding: %v", err)
	}
}

func cardTestDocument(status string) Document {
	return Document{
		SchemaURL: cardSchemaURL, SchemaVersion: CardSchemaVersion,
		Report:        Report{Code: "sales_report", Name: "销售报告", Title: "销售报告", Type: "REPORT", Status: status, ThemeID: "business-light", Language: "zh-CN", Timezone: "Asia/Shanghai", Visibility: "PRIVATE", DefaultRefreshPolicy: "CACHE"},
		Layout:        &ResponsiveLayout{Columns: 12, RowHeight: 8, Margin: 12, Breakpoints: map[string]int{"lg": 1200, "md": 768, "sm": 0}},
		GlobalFilters: []GlobalFilter{},
		Cards: []Card{{
			ID: "title", Type: "TITLE", CardVersion: "1.0.0",
			Layout:       map[string]Grid{"lg": {X: 0, Y: 0, W: 12, H: 8}, "md": {X: 0, Y: 0, W: 12, H: 8}, "sm": {X: 0, Y: 0, W: 12, H: 8}},
			Appearance:   CardAppearance{Title: "报告标题"},
			Binding:      CardBinding{Metrics: []MetricBinding{}, Dimensions: []DimensionBinding{}, GlobalFilterBindings: []GlobalFilterBinding{}, Filters: []CardFilter{}, Sort: []CardSort{}},
			Interactions: []CardInteraction{},
		}},
	}
}
