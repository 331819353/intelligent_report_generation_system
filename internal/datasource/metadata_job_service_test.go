package datasource

import "testing"

func TestMetadataSampleRowLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		columnCount int
		want        int
	}{
		{name: "unknown", columnCount: 0, want: 10},
		{name: "one column", columnCount: 1, want: 10},
		{name: "eight columns", columnCount: 8, want: 9},
		{name: "fifteen columns", columnCount: 15, want: 8},
		{name: "twenty two columns", columnCount: 22, want: 7},
		{name: "twenty nine columns", columnCount: 29, want: 6},
		{name: "thirty six columns", columnCount: 36, want: 5},
		{name: "forty three columns", columnCount: 43, want: 4},
		{name: "fifty columns", columnCount: 50, want: 3},
		{name: "more than fifty columns", columnCount: 51, want: 3},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := metadataSampleRowLimit(test.columnCount); got != test.want {
				t.Fatalf(
					"metadataSampleRowLimit(%d) = %d, want %d",
					test.columnCount, got, test.want,
				)
			}
		})
	}
}
