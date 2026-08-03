package queryruntime

import "testing"

func TestValidateReadOnlyExplainNodeAcceptsBoundedSelectPlan(t *testing.T) {
	plan := map[string]any{
		"Node Type": "Limit",
		"Plans": []any{map[string]any{
			"Node Type": "Aggregate",
			"Plans":     []any{map[string]any{"Node Type": "Seq Scan"}},
		}},
	}
	nodes := 0
	if err := validateReadOnlyExplainNode(plan, &nodes); err != nil {
		t.Fatalf("read-only EXPLAIN plan rejected: %v", err)
	}
	if nodes != 3 {
		t.Fatalf("unexpected node count: %d", nodes)
	}
}

func TestValidateReadOnlyExplainNodeRejectsExecutableFunction(t *testing.T) {
	plan := map[string]any{"Node Type": "Function Scan"}
	nodes := 0
	if err := validateReadOnlyExplainNode(plan, &nodes); err == nil {
		t.Fatal("function scans must not pass the semantic query preflight")
	}
}

func TestCollectExplainRelationsRequiresSchemaQualifiedRelations(t *testing.T) {
	relations := map[string]bool{}
	plan := map[string]any{
		"Node Type": "Seq Scan", "Schema": "warehouse_dws",
		"Relation Name": "metric_snapshot",
	}
	if err := collectExplainRelations(plan, relations); err != nil ||
		!relations["warehouse_dws\x00metric_snapshot"] {
		t.Fatalf("schema-qualified EXPLAIN relation not collected: %v %#v", err, relations)
	}
	if err := collectExplainRelations(map[string]any{
		"Node Type": "Seq Scan", "Relation Name": "metric_snapshot",
	}, map[string]bool{}); err == nil {
		t.Fatal("an unqualified EXPLAIN relation must be rejected")
	}
}
