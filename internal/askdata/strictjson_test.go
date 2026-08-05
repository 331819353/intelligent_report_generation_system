package askdata

import (
	"strings"
	"testing"
)

func TestDecodeStrictJSON(t *testing.T) {
	type document struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "valid", raw: `{"name":"sales"}`},
		{name: "unknown", raw: `{"name":"sales","sql":"select 1"}`, want: "unknown field"},
		{name: "duplicate", raw: `{"name":"sales","name":"finance"}`, want: "duplicate key"},
		{name: "trailing", raw: `{"name":"sales"} {"name":"finance"}`, want: "trailing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var decoded document
			err := DecodeStrictJSON([]byte(test.raw), &decoded)
			if test.want == "" && err != nil {
				t.Fatalf("DecodeStrictJSON() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("DecodeStrictJSON() error = %v, want substring %q", err, test.want)
			}
		})
	}
}
