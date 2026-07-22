package datasource

import (
	"fmt"
	"math"
	"strings"
	"testing"

	spikeexcel "intelligent-report-generation-system/internal/spike/excel"
)

func TestInferWorkbookTypesHeadersSelectionAndOverrides(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{
		{Name: "Sales", Rows: [][]string{{"id", "amount", "active", "date", "id", ""}, {"1", "12.50", "true", "2026-07-15", "A", "x"}, {"2", "3.25", "false", "2026-07-16", "B", "y"}}},
		{Name: "Ignored", Rows: [][]string{{"value"}, {"1"}}},
	}}
	tables, err := inferWorkbook(book, map[string]any{
		"headerRow": float64(1), "selectedSheets": []any{"Sales"},
		"columnOverrides": map[string]any{"Sales.amount": "TEXT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || len(tables[0].Columns) != 6 {
		t.Fatalf("unexpected tables: %#v", tables)
	}
	wantNames := []string{"id", "amount", "active", "date", "id_2", "column_6"}
	wantTypes := []string{"NUMBER", "TEXT", "BOOLEAN", "DATE", "TEXT", "TEXT"}
	for index, column := range tables[0].Columns {
		if column.Name != wantNames[index] || column.CanonicalType != wantTypes[index] {
			t.Fatalf("column %d: %#v", index, column)
		}
	}
}

func TestInferWorkbookRejectsMissingHeaderAndEmptySelection(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{{Name: "Sheet1", Rows: [][]string{{"a"}}}}}
	if _, err := inferWorkbook(book, map[string]any{"headerRow": float64(2)}); err == nil {
		t.Fatal("expected missing header error")
	}
	if _, err := inferWorkbook(book, map[string]any{"selectedSheets": []any{"Other"}}); err == nil {
		t.Fatal("expected empty selection error")
	}
}

func TestInspectWorkbookLocksIndependentSheetPlansFromFirstTenRows(t *testing.T) {
	rows := [][]string{{"report title"}, {}, {"id", "amount"}}
	for index := 1; index <= 11; index++ {
		rows = append(rows, []string{fmt.Sprintf("%d", index), fmt.Sprintf("%d.50", index)})
	}
	// The eleventh value is intentionally incompatible. The upload inspection contract
	// determines the parse type from the first ten data rows, not from an unbounded scan.
	rows[len(rows)-1][1] = "late text"
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{
		{Name: "Sales", Rows: rows},
		{Name: "Customers", Rows: [][]string{{"customer_id", "region"}, {"1", "East"}}},
	}}
	metadata, inspection, plans, err := inspectWorkbook(book, map[string]any{"sheetPlans": map[string]any{
		"Sales": map[string]any{"headerRow": 3.0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || metadata[0].Columns[1].CanonicalType != "DECIMAL" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if inspection.SampleLimit != 10 || len(inspection.Sheets[0].Rows) != 10 || inspection.Sheets[0].HeaderRow != 3 || inspection.Sheets[1].HeaderRow != 1 {
		t.Fatalf("inspection = %#v", inspection)
	}
	if sales, ok := plans["Sales"].(map[string]any); !ok || sales["headerRow"] != 3 {
		t.Fatalf("plans = %#v", plans)
	}
}

func TestInspectWorkbookDetectsLeafHeaderAfterTitlesAndMergedGroups(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{{Name: "销售订单", Rows: [][]string{
		{"销售订单明细"},
		{"示例数据｜金额列由数量 × 单价自动计算｜表头共两层"},
		{"订单信息", "", "客户信息", "", "商品信息", "", "交易金额"},
		{"订单编号", "订单日期", "客户名称", "区域", "商品名称", "品类", "数量", "单价", "订单金额"},
		{"SO-001", "2026-07-01", "华东智造有限公司", "华东", "工业传感器", "传感器", "24", "680", "16320"},
	}}}}
	metadata, inspection, plans, err := inspectWorkbook(book, map[string]any{"skipEmptyRows": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 || len(metadata[0].Columns) != 9 || metadata[0].Columns[0].Name != "订单编号" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(inspection.Sheets) != 1 || inspection.Sheets[0].HeaderRow != 4 || len(inspection.Sheets[0].Rows) != 1 {
		t.Fatalf("inspection = %#v", inspection)
	}
	plan, ok := plans["销售订单"].(map[string]any)
	if !ok || plan["headerRow"] != 4 {
		t.Fatalf("plans = %#v", plans)
	}
}

func TestSheetHeadersBackfillsVerticalMergeAndInfersFormattedNumbers(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{{Name: "库存", Rows: [][]string{
		{"库存台账"},
		{"商品信息", "", "", "仓库", "库存金额", "回款率"},
		{"SKU", "商品名称", "品类", "", "金额", "完成率"},
		{"SKU-1", "工业传感器", "传感器", "上海一仓", "¥16,320.00", "65.1%"},
	}}}}
	metadata, inspection, _, err := inspectWorkbook(book, map[string]any{"headerRow": 3})
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"SKU", "商品名称", "品类", "仓库", "金额", "完成率"}
	for index, want := range wantNames {
		if metadata[0].Columns[index].Name != want {
			t.Fatalf("column %d = %#v", index, metadata[0].Columns[index])
		}
	}
	if metadata[0].Columns[4].CanonicalType != "DECIMAL" || metadata[0].Columns[5].CanonicalType != "DECIMAL" {
		t.Fatalf("columns = %#v", metadata[0].Columns)
	}
	if inspection.Sheets[0].Columns[3].Name != "仓库" {
		t.Fatalf("inspection = %#v", inspection)
	}
	if amount, ok := ParseSpreadsheetNumber("(￥1,234.50)"); !ok || amount != -1234.5 {
		t.Fatalf("amount=%v ok=%v", amount, ok)
	}
	if ratio, ok := ParseSpreadsheetNumber("65.1%"); !ok || math.Abs(ratio-0.651) > 1e-12 {
		t.Fatalf("ratio=%v ok=%v", ratio, ok)
	}
}

func TestInspectWorkbookRejectsRowsWiderThanValidatedHeader(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{{Name: "Bad", Rows: [][]string{{"id"}, {"1", "unexpected"}}}}}
	if _, _, _, err := inspectWorkbook(book, nil); err == nil || !strings.Contains(err.Error(), "more columns") {
		t.Fatalf("error = %v", err)
	}
}
