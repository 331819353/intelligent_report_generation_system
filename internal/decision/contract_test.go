package decision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
)

func TestDecisionSharedFixturesAgainstJSONSchemas(t *testing.T) {
	cases := []struct {
		name, schema, valid, invalid string
	}{
		{"decision", "decision-v1.schema.json", "decision.valid.json", "decision.invalid.json"},
		{"evidence", "decision-evidence-v1.schema.json", "evidence.valid.json", "evidence.invalid.json"},
		{"action", "decision-action-v1.schema.json", "action.valid.json", "action.invalid.json"},
		{"outcome-review", "decision-outcome-review-v1.schema.json", "outcome-review.valid.json", "outcome-review.invalid.json"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			schema, err := os.ReadFile(filepath.Join("..", "..", "api", "schemas", testCase.schema))
			if err != nil {
				t.Fatal(err)
			}
			valid, err := os.ReadFile(filepath.Join("..", "..", "api", "fixtures", "decision", testCase.valid))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ai.ValidateStructuredOutput(ai.JSONSchema{Name: strings.ReplaceAll(testCase.name, "-", "_") + "_v1", Schema: schema}, valid); err != nil {
				t.Fatalf("valid shared fixture rejected: %v", err)
			}
			assertDecisionFixtureRedlines(t, valid)
			invalid, err := os.ReadFile(filepath.Join("..", "..", "api", "fixtures", "decision", testCase.invalid))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ai.ValidateStructuredOutput(ai.JSONSchema{Name: strings.ReplaceAll(testCase.name, "-", "_") + "_v1", Schema: schema}, invalid); err == nil {
				t.Fatal("invalid shared fixture passed its JSON Schema")
			}
		})
	}
}

func assertDecisionFixtureRedlines(t *testing.T, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"sql": {}, "rawsql": {}, "rows": {}, "resultrows": {}, "chainofthought": {}, "reasoning": {},
	}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				if _, blocked := forbidden[normalized]; blocked {
					t.Fatalf("valid fixture contains forbidden field %q", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
}
