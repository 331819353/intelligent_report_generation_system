package datasource

import "testing"

func TestParseCSVOptions(t *testing.T) {
	options, err := parseCSVOptions(map[string]any{"csvOptions": map[string]any{
		"encoding":         "GBK",
		"delimiter":        "TAB",
		"quote":            "'",
		"lazyQuotes":       true,
		"trimLeadingSpace": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if options.Encoding != "GBK" || options.Delimiter != '\t' || options.Quote != '\'' || !options.LazyQuotes || !options.TrimLeadingSpace {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseCSVOptionsRejectsInvalidDialect(t *testing.T) {
	for _, config := range []map[string]any{
		{"csvOptions": "invalid"},
		{"csvOptions": map[string]any{"delimiter": "||"}},
		{"csvOptions": map[string]any{"quote": ""}},
	} {
		if _, err := parseCSVOptions(config); err == nil {
			t.Fatal("expected invalid CSV dialect error")
		}
	}
}
