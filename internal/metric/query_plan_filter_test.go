package metric

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestAppendDimensionFiltersSupportsExcelExclusions(t *testing.T) {
	field := dataset.Field{
		ID: "field_city", Code: "city", Name: "城市",
		Role: "DIMENSION", CanonicalType: "STRING",
		Expression: dataset.Expression{
			Type: "FIELD_REF", NodeID: "source", Field: "city",
		},
	}
	fields := map[string]dataset.Field{field.ID: field}
	allowed := map[string]Dimension{
		field.ID: {FieldID: field.ID, Name: field.Name, SortDirection: "ASC"},
	}
	document := dataset.Document{}

	bindings, parameters, err := appendDimensionFilters(
		&document,
		fields,
		allowed,
		[]DimensionFilter{
			{FieldID: field.ID, Operator: "NOT_EQUALS", Value: "Beijing"},
			{FieldID: field.ID, Operator: "NOT_IN", Value: []string{"Shanghai", "Hangzhou"}},
		},
	)
	if err != nil {
		t.Fatalf("append Excel exclusion filters: %v", err)
	}
	if len(bindings) != 2 || len(document.Filters) != 2 {
		t.Fatalf("unexpected filter count: bindings=%d filters=%d", len(bindings), len(document.Filters))
	}
	if document.Filters[0].Expression.Type != "NOT_EQUALS" ||
		document.Filters[0].Expression.Right == nil ||
		document.Filters[0].Expression.Right.Type != "PARAM_REF" {
		t.Fatalf("NOT_EQUALS was not compiled as a bound scalar filter: %#v", document.Filters[0])
	}
	if document.Filters[1].Expression.Type != "NOT_IN" ||
		document.Filters[1].Expression.Right == nil ||
		document.Filters[1].Expression.Right.Type != "ARRAY" ||
		len(document.Filters[1].Expression.Right.Arguments) != 2 {
		t.Fatalf("NOT_IN was not compiled as a bound set filter: %#v", document.Filters[1])
	}
	if len(parameters) != 3 || len(document.Parameters) != 3 {
		t.Fatalf("unexpected bound parameter count: values=%d definitions=%d", len(parameters), len(document.Parameters))
	}
}
