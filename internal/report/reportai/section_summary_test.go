package reportai

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestBuildSectionSummaryRequestUsesEverySubsectionAndExcludesItsOwnSummary(t *testing.T) {
	sectionID := askdata.ID("10000000-0000-4000-8000-000000000001")
	firstBlockID := askdata.ID("10000000-0000-4000-8000-000000000002")
	secondBlockID := askdata.ID("10000000-0000-4000-8000-000000000003")
	firstComponentID := askdata.ID("10000000-0000-4000-8000-000000000004")
	secondComponentID := askdata.ID("10000000-0000-4000-8000-000000000005")
	angleSummaryID := askdata.ID("10000000-0000-4000-8000-000000000006")
	contextID := askdata.ID("10000000-0000-4000-8000-000000000007")
	filterID := askdata.ID("10000000-0000-4000-8000-000000000009")
	definition := report.ReportDefinition{
		Pages: []report.Page{{ID: askdata.ID("10000000-0000-4000-8000-000000000008"), Sections: []report.Section{{
			ID: sectionID, Name: "经营增长", Question: "增长来自哪里？", Blocks: []report.Block{
				{ID: angleSummaryID, CardKind: "LAYOUT_ANGLE_INSIGHT", Zones: []report.Zone{{Slots: []report.Slot{{ComponentID: angleSummaryID}}}}},
				{ID: firstBlockID, Title: "收入趋势", CardKind: "LAYOUT_SUBSECTION_CONCLUSION_TOP", Zones: []report.Zone{{Slots: []report.Slot{{CardKind: "FRAME_EVIDENCE", ComponentID: firstComponentID}}}}},
				{ID: secondBlockID, Title: "渠道结构", CardKind: "LAYOUT_SUBSECTION_CONCLUSION_LEFT", Zones: []report.Zone{{Slots: []report.Slot{{CardKind: "FRAME_CONCLUSION", ComponentID: secondComponentID}}}}},
			},
		}}}},
		GlobalFilters: []report.GlobalFilter{{ID: filterID, Label: "渠道范围", FieldRef: report.FieldReference{Field: "channel"}}},
		Components: []report.Component{
			{ID: angleSummaryID, TemplateRef: report.ComponentTemplateReference{Type: "rich-text", Version: "1.0.0"}, Options: report.ComponentOptions{RichText: "旧汇总不能成为自己的来源"}},
			{ID: firstComponentID, TemplateRef: report.ComponentTemplateReference{Type: "line-trend", Version: "1.0.0"},
				DataBinding: &report.DataBinding{BindingMode: report.BindingDatasetField, DataContextID: &contextID,
					Dimensions: []report.FieldBinding{{Role: report.RoleTime, Field: "order_date", Label: "订单日期"}},
					Measures:   []report.FieldBinding{{Role: report.RoleValue, Field: "revenue", Label: "收入", Aggregation: "SUM"}},
					FilterPolicy: &report.ComponentFilterPolicy{
						GlobalMappings: []report.GlobalFilterMapping{{FilterID: filterID, Field: "sales_channel"}},
						LocalFilters:   []report.LocalFilter{{Field: "channel", Operator: "EQUALS", Value: "online"}},
					}},
				Options: report.ComponentOptions{Title: "收入变化"}},
			{ID: secondComponentID, TemplateRef: report.ComponentTemplateReference{Type: "analysis-long-form-conclusion", Version: "1.0.0"},
				Options: report.ComponentOptions{Title: "渠道结论", RichText: "线上渠道是现有小节已经写明的结论。"}},
		},
	}

	result, err := BuildSectionSummaryRequest(definition, sectionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Subsections) != 2 || result.Subsections[0].ID != firstBlockID || result.Subsections[1].ID != secondBlockID {
		t.Fatalf("unexpected subsections: %#v", result.Subsections)
	}
	if result.Subsections[0].Weight != 50 || result.Subsections[1].Weight != 50 || result.AnalysisApproach.HowToAnalyze == "" {
		t.Fatalf("unexpected default angle configuration: %#v", result)
	}
	first := result.Subsections[0].Components[0]
	if first.Role != "EVIDENCE" || first.Dimensions[0] != "订单日期" || first.Measures[0] != "收入（SUM）" ||
		first.Filters[0] != "全局 渠道范围 → sales_channel" || first.Filters[1] != "channel EQUALS online" {
		t.Fatalf("unexpected first component: %#v", first)
	}
	if result.Subsections[1].Components[0].Narrative == "" {
		t.Fatal("existing subsection narrative must be available to the summary model")
	}
}

func TestBuildSectionSummaryRequestUsesOnlyConfiguredSubsectionsAndWeights(t *testing.T) {
	sectionID := askdata.ID("20000000-0000-4000-8000-000000000001")
	firstID := askdata.ID("20000000-0000-4000-8000-000000000002")
	secondID := askdata.ID("20000000-0000-4000-8000-000000000003")
	definition := report.ReportDefinition{Pages: []report.Page{{Sections: []report.Section{{
		ID: sectionID, Name: "经营质量", Blocks: []report.Block{
			{ID: firstID, Title: "收入", CardKind: "LAYOUT_SUBSECTION_CONCLUSION_TOP"},
			{ID: secondID, Title: "风险", CardKind: "LAYOUT_SUBSECTION_CONCLUSION_LEFT"},
		},
	}}}}}
	config := report.AngleInsightConfig{
		AnalysisApproach: report.AngleInsightApproach{
			HowToAnalyze: "优先识别风险", AnalyzeWhat: "风险证据", DoNotAnalyze: "收入趋势", OutputExample: "风险：……",
		},
		AnalysisItems: []report.AngleInsightItem{{SubsectionID: secondID, Weight: 100}},
	}
	result, err := BuildSectionSummaryRequestWithConfig(definition, sectionID, &config)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Subsections) != 1 || result.Subsections[0].ID != secondID || result.Subsections[0].Weight != 100 {
		t.Fatalf("unexpected selected subsections: %#v", result.Subsections)
	}
	if result.AnalysisApproach.AnalyzeWhat != "风险证据" {
		t.Fatalf("unexpected analysis approach: %#v", result.AnalysisApproach)
	}
}

func TestSectionSummaryContentProducesReadableRichText(t *testing.T) {
	content, err := ValidateSectionSummaryContent(SectionSummaryContent{
		Summary: "综合结论", Findings: []string{"发现"}, Risks: []string{"风险"}, Actions: []string{"行动"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "综合结论\n核心发现 1：发现\n风险提示 1：风险\n建议动作 1：行动"
	if content.RichText() != want {
		t.Fatalf("RichText() = %q, want %q", content.RichText(), want)
	}
}
