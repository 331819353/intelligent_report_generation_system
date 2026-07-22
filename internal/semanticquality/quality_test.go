package semanticquality

import "testing"

func TestCompatible(t *testing.T) {
	tests := []struct {
		canonical string
		semantic  string
		want      bool
	}{
		{"DECIMAL", "AMOUNT", true},
		{"INTEGER", "QUANTITY", true},
		{"STRING", "PERCENTAGE", false},
		{"DATE", "DATE", true},
		{"TIMESTAMP", "TIME", true},
		{"DATE", "DATETIME", false},
		{"BOOLEAN", "BOOLEAN", true},
		{"STRING", "BOOLEAN", false},
		{"STRING", "REGION", true},
		{"STRING", "", true},
	}
	for _, test := range tests {
		if got := Compatible(test.canonical, test.semantic); got != test.want {
			t.Fatalf("Compatible(%q,%q)=%v want %v", test.canonical, test.semantic, got, test.want)
		}
	}
}
