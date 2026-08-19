package reporthttp

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
)

func TestBlankReportTemplateSections(t *testing.T) {
	sections := blankReportTemplateSections()
	if len(sections) != 1 || len(sections[0].Blocks) != 4 {
		t.Fatalf("expected one section with four template blocks, got %#v", sections)
	}

	wantTitles := []string{"关键结论概览", "订单趋势分析", "销售结构分析", "订单明细数据"}
	wantTypes := []reportmodel.BlockType{reportmodel.BlockContent, reportmodel.BlockChart, reportmodel.BlockChart, reportmodel.BlockTable}
	for index, block := range sections[0].Blocks {
		if block.Title != wantTitles[index] || block.Type != wantTypes[index] {
			t.Fatalf("block %d = %q/%s, want %q/%s", index, block.Title, block.Type, wantTitles[index], wantTypes[index])
		}
		if len(block.Zones) != 1 || len(block.Zones[0].Slots) != 1 {
			t.Fatalf("block %d should expose exactly one fillable slot", index)
		}
		slot := block.Zones[0].Slots[0]
		if slot.ComponentID != "" || slot.CardKind != reportTemplateSlotKind {
			t.Fatalf("block %d slot should be an empty report template slot: %#v", index, slot)
		}
	}

	left := sections[0].Blocks[1].Layout.Desktop
	right := sections[0].Blocks[2].Layout.Desktop
	if left.X != 0 || left.W != 12 || right.X != 12 || right.W != 12 || left.Y != right.Y {
		t.Fatalf("chart blocks should form an equal two-column row: left=%#v right=%#v", left, right)
	}

	definition := newReportDefinition(
		askdata.ID("11111111-1111-4111-8111-111111111111"), "模板测试", "",
		reportmodel.DataContext{
			ID:               askdata.ID("22222222-2222-4222-8222-222222222222"),
			DatasetID:        askdata.ID("33333333-3333-4333-8333-333333333333"),
			DatasetVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"),
			Alias:            "测试数据", DefaultParameters: []reportmodel.DefaultParameter{},
			QueryPolicy: reportmodel.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
		}, reportmodel.CreatedManually,
	)
	definition.Pages[0].Sections = sections
	if err := definition.Validate(); err != nil {
		t.Fatalf("blank report display template must be a valid Report Definition: %v", err)
	}
}
