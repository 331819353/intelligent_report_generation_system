package querycompiler

import (
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/policy"
)

func compilerInput(t *testing.T) Input {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Document: prepared.Document, Dialect: MySQL, Parameters: map[string]any{"start_date": "2026-01-01"}, MaxRows: 100,
		Tables:         map[string]TableRef{"orders": {NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"order_date": true, "order_amount": true, "order_status": true}}},
		Scope:          policy.UserScope{TenantID: "tenant-1", UserID: "user-1", Attributes: map[string]any{"month": "2026-01-01"}},
		RowPolicies:    []policy.RowPolicy{{ID: "row-1", Effect: "ALLOW", CombineMode: "AND", Expression: policy.Expression{Type: "EQUALS", Left: &policy.Expression{Type: "FIELD_REF", FieldCode: "stat_month"}, Right: &policy.Expression{Type: "USER_ATTRIBUTE_REF", Attribute: "month"}}}},
		ColumnPolicies: []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "NULLIFY"}},
	}
}

func TestCompileBindsValuesAndInjectsPolicies(t *testing.T) {
	compiled, err := Compile(compilerInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "2026-01-01") || !strings.Contains(compiled.SQL, "%s") {
		t.Fatalf("query is not parameterized: %s", compiled.SQL)
	}
	if !strings.Contains(compiled.SQL, "secure_base") || !strings.Contains(compiled.SQL, "NULL AS `revenue`") {
		t.Fatalf("security wrappers are missing: %s", compiled.SQL)
	}
	if len(compiled.Args) != 3 || compiled.MaxRows != 100 {
		t.Fatalf("args=%#v maxRows=%d", compiled.Args, compiled.MaxRows)
	}
}

func TestCompileRejectsMissingParameterAndUnknownColumn(t *testing.T) {
	input := compilerInput(t)
	input.Parameters = map[string]any{}
	if _, err := Compile(input); err == nil {
		t.Fatal("missing parameter was accepted")
	}
	input = compilerInput(t)
	delete(input.Tables["orders"].Columns, "order_amount")
	if _, err := Compile(input); err == nil {
		t.Fatal("unknown source column was accepted")
	}
}

func TestCompileRejectsIdentifierInjectionAndDeniedColumn(t *testing.T) {
	input := compilerInput(t)
	ref := input.Tables["orders"]
	ref.Name = "orders;DROP_TABLE"
	input.Tables["orders"] = ref
	if _, err := Compile(input); err == nil {
		t.Fatal("malicious table identifier was accepted")
	}
	input = compilerInput(t)
	input.ColumnPolicies = []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "DENY"}}
	if _, err := Compile(input); err == nil {
		t.Fatal("denied column was accepted")
	}
	input = compilerInput(t)
	input.ColumnPolicies = []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "MASK", MaskRule: policy.MaskRule{Type: "KEEP_PREFIX_SUFFIX", MaskChar: `\';--`}}}
	if _, err := Compile(input); err == nil {
		t.Fatal("unsafe mask character was accepted")
	}
}

func TestCompileKeepsMaliciousValuesOutOfGeneratedSQL(t *testing.T) {
	input := compilerInput(t)
	input.Parameters["start_date"] = "2026-01-01'; DROP TABLE users; --"
	if _, err := Compile(input); err == nil {
		t.Fatal("invalid DATE parameter was accepted")
	}
	input.Parameters["start_date"] = "2026-01-01"
	input.Document.Nodes[0].SourceFilters[0].Value = "VALID'; DELETE FROM orders; /*"
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "DELETE") || strings.Contains(compiled.SQL, "/*") {
		t.Fatalf("literal escaped parameter binding: %s", compiled.SQL)
	}
}

func TestCompileAppliesSortORPoliciesAndMinimumGroupSize(t *testing.T) {
	input := compilerInput(t)
	input.RowPolicies = []policy.RowPolicy{
		{ID: "row-a", Effect: "ALLOW", CombineMode: "OR", Expression: policy.Expression{Type: "EQUALS", Left: &policy.Expression{Type: "FIELD_REF", FieldCode: "stat_month"}, Right: &policy.Expression{Type: "LITERAL", Value: "2026-01-01"}}},
		{ID: "row-b", Effect: "ALLOW", CombineMode: "OR", Expression: policy.Expression{Type: "EQUALS", Left: &policy.Expression{Type: "FIELD_REF", FieldCode: "stat_month"}, Right: &policy.Expression{Type: "LITERAL", Value: "2026-02-01"}}},
	}
	input.ColumnPolicies = []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}, MinimumGroupSize: 5}}
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "COUNT(*) >= %s") || !strings.Contains(compiled.SQL, " OR ") || !strings.Contains(compiled.SQL, "ORDER BY `stat_month` ASC") {
		t.Fatalf("policy or sort compilation is incomplete: %s", compiled.SQL)
	}
	if len(compiled.Args) != 5 || compiled.Args[2] != 5 {
		t.Fatalf("unexpected argument order: %#v", compiled.Args)
	}
}

func TestCompileUsesOnlyHighestPriorityColumnPolicyForGroupMinimum(t *testing.T) {
	input := compilerInput(t)
	input.ColumnPolicies = []policy.ColumnPolicy{
		{FieldCode: "revenue", PolicyType: "ALLOW"},
		{FieldCode: "revenue", PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}, MinimumGroupSize: 50},
	}
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "COUNT(*) >=") {
		t.Fatalf("低优先级列策略错误影响计划: %s", compiled.SQL)
	}
}

func TestNormalizeParametersRejectsUnknownAndWrongShapes(t *testing.T) {
	definitions := []dataset.Parameter{{Code: "ids", DataType: "INTEGER", MultiValue: true, Required: true}}
	if _, err := NormalizeParameters(definitions, map[string]any{"ids": []any{"1", 2.0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeParameters(definitions, map[string]any{"ids": "1"}); err == nil {
		t.Fatal("scalar value was accepted for a multi-value parameter")
	}
	if _, err := NormalizeParameters(definitions, map[string]any{"ids": []any{1}, "extra": "x"}); err == nil {
		t.Fatal("undeclared parameter was accepted")
	}
}

func TestOracleUsesNumberedBindVariables(t *testing.T) {
	input := compilerInput(t)
	input.Dialect = Oracle
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, ":1") || strings.Contains(compiled.SQL, "%s") {
		t.Fatalf("unexpected Oracle placeholders: %s", compiled.SQL)
	}
}

func TestCompileSkipsOptionalFilterWhenParameterIsAbsent(t *testing.T) {
	input := compilerInput(t)
	input.Document.Parameters[0].Required = false
	input.Document.Filters[0].Optional = true
	input.Parameters = map[string]any{}
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	// 仅保留源过滤值和行策略属性，不应绑定缺失的可选参数。
	if len(compiled.Args) != 2 {
		t.Fatalf("optional filter args=%#v sql=%s", compiled.Args, compiled.SQL)
	}
}

func TestCompileScanPushesNodeFiltersAndAppliesBoundLimit(t *testing.T) {
	input := compilerInput(t)
	compiled, err := CompileScan(ScanInput{
		Document: input.Document, NodeID: "orders", Dialect: MySQL, Table: input.Tables["orders"],
		Parameters: input.Parameters, MaxRows: 101,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "`o`.`order_status` = %s") || !strings.Contains(compiled.SQL, "`o`.`order_date` >= %s") || !strings.HasSuffix(compiled.SQL, "LIMIT %s") {
		t.Fatalf("source pushdown is incomplete: %s", compiled.SQL)
	}
	if len(compiled.Args) != 3 || compiled.Args[2] != 101 {
		t.Fatalf("scan args=%#v", compiled.Args)
	}
}

func TestCompileScanPushesSafeAggregateProjections(t *testing.T) {
	input := compilerInput(t)
	compiled, err := CompileScan(ScanInput{
		Document: input.Document, NodeID: "orders", Dialect: MySQL, Table: input.Tables["orders"],
		Parameters: input.Parameters, MaxRows: 101, AggregateProjections: map[string]ScanAggregateProjection{
			"order_amount": {SourceField: "order_amount", Function: "SUM"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "SUM(`o`.`order_amount`) AS `order_amount`") || !strings.Contains(compiled.SQL, "GROUP BY `o`.`order_date`, `o`.`order_status`") || !strings.HasSuffix(compiled.SQL, "LIMIT %s") {
		t.Fatalf("aggregate pushdown is incomplete: %s", compiled.SQL)
	}
}

func TestCompileScanSupportsSyntheticCountProjection(t *testing.T) {
	input := compilerInput(t)
	input.Document.Nodes[0].Projection = append(input.Document.Nodes[0].Projection, "partial_count_1")
	compiled, err := CompileScan(ScanInput{
		Document: input.Document, NodeID: "orders", Dialect: Oracle, Table: input.Tables["orders"],
		Parameters: input.Parameters, MaxRows: 101, AggregateProjections: map[string]ScanAggregateProjection{
			"partial_count_1": {SourceField: "order_amount", Function: "COUNT"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, `COUNT("O"."ORDER_AMOUNT") AS "PARTIAL_COUNT_1"`) || !strings.Contains(compiled.SQL, `GROUP BY "O"."ORDER_DATE", "O"."ORDER_AMOUNT", "O"."ORDER_STATUS"`) {
		t.Fatalf("synthetic count pushdown is incomplete: %s", compiled.SQL)
	}
}

func TestCompileScanRejectsUntrustedAggregateShape(t *testing.T) {
	input := compilerInput(t)
	for _, aggregates := range []map[string]ScanAggregateProjection{
		{"order_amount": {SourceField: "order_amount", Function: "MEDIAN"}},
		{"missing": {SourceField: "order_amount", Function: "SUM"}},
	} {
		if _, err := CompileScan(ScanInput{
			Document: input.Document, NodeID: "orders", Dialect: Oracle, Table: input.Tables["orders"],
			Parameters: input.Parameters, MaxRows: 101, AggregateProjections: aggregates,
		}); err == nil {
			t.Fatalf("unsafe aggregate was accepted: %#v", aggregates)
		}
	}
}

func TestExpressionOnlyReferencesCurrentScanNode(t *testing.T) {
	expression := dataset.Expression{Type: "EQUALS", Left: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "id"}, Right: &dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "id"}}
	if expressionOnlyReferencesNode(expression, "orders") {
		t.Fatal("跨节点过滤器被错误识别为可下推")
	}
	aggregate := dataset.Expression{Type: "GT", Left: &dataset.Expression{Type: "AGGREGATE", Function: "COUNT"}, Right: &dataset.Expression{Type: "LITERAL", Value: 1}}
	if expressionOnlyReferencesNode(aggregate, "orders") {
		t.Fatal("聚合过滤器被错误识别为可下推")
	}
}
