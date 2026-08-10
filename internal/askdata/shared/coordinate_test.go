package shared

import (
	"reflect"
	"testing"
)

func TestRowKeyRoundTripPreservesGovernedGroupingOrder(t *testing.T) {
	parts := []RowKeyPart{{Key: "region", Value: "华东|直营"}, {Key: "month", Value: "2026-08"}}
	encoded, err := FormatRowKey(parts)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "region=%E5%8D%8E%E4%B8%9C%7C%E7%9B%B4%E8%90%A5|month=2026-08" {
		t.Fatalf("encoded row key = %q", encoded)
	}
	decoded, err := ParseRowKey(encoded)
	if err != nil || !reflect.DeepEqual(decoded, parts) {
		t.Fatalf("ParseRowKey() = %#v, %v", decoded, err)
	}
}

func TestRowKeyRejectsAmbiguousAndNonCanonicalCoordinates(t *testing.T) {
	for _, value := range []string{"region=east|region=west", "region=华东", "region=%e5%8d%8e", "region=east=west", ""} {
		if _, err := ParseRowKey(value); err == nil {
			t.Fatalf("ParseRowKey(%q) accepted invalid coordinate", value)
		}
	}
}

func TestValidateCitationsUsesUnicodeOffsetsAndRejectsOverlap(t *testing.T) {
	rowKey, err := FormatRowKey([]RowKeyPart{{Key: "region", Value: "east"}})
	if err != nil {
		t.Fatal(err)
	}
	text := "华东销售额为128万元"
	ref := CellRef{RowKey: rowKey, ColumnKey: "sales_amount"}
	citations := []Citation{
		NewContractCitation(TextSpan{Start: 0, End: 5}, "metric:sales@v5"),
		NewResultCellCitation(TextSpan{Start: 6, End: 11}, ref),
	}
	if err := ValidateCitations(text, citations); err != nil {
		t.Fatalf("ValidateCitations() error = %v", err)
	}
	citations[1].TextSpan.Start = 4
	if err := ValidateCitations(text, citations); err == nil {
		t.Fatal("overlapping citations were accepted")
	}
}
