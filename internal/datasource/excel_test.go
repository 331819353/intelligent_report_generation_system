package datasource

import (
	"fmt"
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

func TestInspectWorkbookRejectsRowsWiderThanValidatedHeader(t *testing.T) {
	book := spikeexcel.Workbook{Sheets: []spikeexcel.Sheet{{Name: "Bad", Rows: [][]string{{"id"}, {"1", "unexpected"}}}}}
	if _, _, _, err := inspectWorkbook(book, nil); err == nil || !strings.Contains(err.Error(), "more columns") {
		t.Fatalf("error = %v", err)
	}
}
