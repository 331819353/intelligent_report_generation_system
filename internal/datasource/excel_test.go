package datasource

import "testing"

func TestInferColumnTypeKeepsExcelCodeAsText(t *testing.T) {
	if got := inferColumnType("SKU_ID", []string{"1001", "1002"}); got != "TEXT" {
		t.Fatalf("Excel code type = %q, want TEXT", got)
	}
}
