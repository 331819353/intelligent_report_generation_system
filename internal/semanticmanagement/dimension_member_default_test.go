package semanticmanagement

import "testing"

func TestReservedDimensionDefaultValueBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    string
		reserved bool
	}{
		{name: "text placeholder", value: "UNKNOWN", reserved: true},
		{name: "normalized text placeholder", value: "ＵＮＫＮＯＷＮ", reserved: true},
		{name: "numeric placeholder", value: "999999999", reserved: true},
		{name: "decimal numeric placeholder", value: "999999999.000", reserved: true},
		{name: "date placeholder", value: "1970-01-01", reserved: true},
		{name: "timestamp placeholder", value: "1970-01-01 00:00:00", reserved: true},
		{name: "boolean false remains valid", value: "False", reserved: false},
		{name: "boolean true remains valid", value: "True", reserved: false},
		{name: "business value", value: "在职", reserved: false},
		{name: "similar numeric business code", value: "9999999991", reserved: false},
		{name: "same date non-midnight", value: "1970-01-01 08:00:00", reserved: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isReservedDimensionDefaultValue(test.value); got != test.reserved {
				t.Fatalf("isReservedDimensionDefaultValue(%q)=%v, want %v",
					test.value, got, test.reserved)
			}
		})
	}
}
