package querycompiler

import (
	"strings"
	"testing"
)

func TestCompileMaterializationBuildsUnboundedPostgreSQLSelect(t *testing.T) {
	input := compilerInput(t)
	input.Document.ExecutionPolicy.Materialization.Enabled = true
	input.Document.ExecutionPolicy.Materialization.RefreshMode = "MANUAL"
	compiled, err := CompileMaterialization(MaterializationInput{
		Document: input.Document, Tables: input.Tables, Parameters: input.Parameters,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, " LIMIT ") || strings.Contains(compiled.SQL, "secure_base") {
		t.Fatalf("warehouse build unexpectedly contains preview wrappers: %s", compiled.SQL)
	}
	for _, expected := range []string{
		`FROM "sales"."orders" "o"`,
		`DATE_TRUNC('month', "o"."order_date")`,
		`SUM("o"."order_amount")`,
		`"o"."order_status" = $1`,
		`"o"."order_date" >= $2`,
	} {
		if !strings.Contains(compiled.SQL, expected) {
			t.Fatalf("warehouse query missing %q: %s", expected, compiled.SQL)
		}
	}
	if len(compiled.Args) != 2 {
		t.Fatalf("args=%#v", compiled.Args)
	}
}

func TestCompileMaterializationRequiresExplicitPolicy(t *testing.T) {
	input := compilerInput(t)
	if _, err := CompileMaterialization(MaterializationInput{
		Document: input.Document, Tables: input.Tables, Parameters: input.Parameters,
	}); err == nil {
		t.Fatal("disabled materialization was accepted")
	}
}

func TestCompileMaterializationRejectsExtraPhysicalWhitelistEntries(t *testing.T) {
	input := compilerInput(t)
	input.Document.ExecutionPolicy.Materialization.Enabled = true
	input.Document.ExecutionPolicy.Materialization.RefreshMode = "MANUAL"
	input.Tables["unreferenced"] = TableRef{
		NodeID: "unreferenced", Schema: "warehouse_ods", Name: "unreferenced",
		Columns: map[string]bool{"id": true},
	}
	if _, err := CompileMaterialization(MaterializationInput{
		Document: input.Document, Tables: input.Tables, Parameters: input.Parameters,
	}); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("err=%v", err)
	}
}
