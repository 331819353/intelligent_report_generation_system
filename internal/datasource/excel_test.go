package datasource

import (
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
