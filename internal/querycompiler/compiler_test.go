package querycompiler

import (
	"os"
	"reflect"
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

func TestPhysicalIdentifierWhitelistSupportsUnicodeWithoutAllowingSQLSyntax(t *testing.T) {
	for _, value := range []string{"订单编号", "客户ID", "amount_金额", "字段$1"} {
		if !safeIdentifier.MatchString(value) {
			t.Fatalf("safe identifier rejected: %q", value)
		}
	}
	for _, value := range []string{"订单 金额", "订单金额`", "订单金额;DROP", "schema.column", "1号字段"} {
		if safeIdentifier.MatchString(value) {
			t.Fatalf("unsafe identifier accepted: %q", value)
		}
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
	if len(compiled.Args) != 4 || compiled.Args[3] != 100 || compiled.MaxRows != 100 || !strings.HasSuffix(compiled.SQL, "LIMIT %s") {
		t.Fatalf("args=%#v maxRows=%d", compiled.Args, compiled.MaxRows)
	}
}

func TestCompileWithoutBusinessParametersStillBindsRowLimit(t *testing.T) {
	input := compilerInput(t)
	input.Document.Parameters = nil
	input.Document.Filters = nil
	input.Document.Nodes[0].SourceFilters = nil
	input.Parameters = nil
	input.RowPolicies = nil
	input.ColumnPolicies = nil
	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Args == nil || len(compiled.Args) != 1 || compiled.Args[0] != 100 || !strings.HasSuffix(compiled.SQL, "LIMIT %s") {
		t.Fatalf("无业务参数查询仍须绑定服务端行数上限: sql=%s args=%#v", compiled.SQL, compiled.Args)
	}
}

func TestTextExpressionsCompileForMySQLAndOracle(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"}
	substring := dataset.Expression{Type: "SUBSTRING", Arguments: []dataset.Expression{{Type: "TRIM", Argument: &field}, {Type: "LITERAL", Value: 2}, {Type: "LITERAL", Value: 3}}}
	replace := dataset.Expression{Type: "REPLACE", Arguments: []dataset.Expression{{Type: "UPPER", Argument: &substring}, {Type: "LITERAL", Value: "OLD"}, {Type: "LITERAL", Value: "NEW"}}}

	for _, test := range []struct {
		name, want string
		dialect    Dialect
	}{
		{name: "mysql", dialect: MySQL, want: "REPLACE(UPPER(SUBSTRING(TRIM(`o`.`order_status`),%s,%s)),%s,%s)"},
		{name: "oracle", dialect: Oracle, want: `REPLACE(UPPER(SUBSTR(TRIM("O"."ORDER_STATUS"),:1,:2)),:3,:4)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiler := compiler{input: Input{Dialect: test.dialect, Tables: map[string]TableRef{"orders": {NodeID: "orders", Columns: map[string]bool{"order_status": true}}}}}
			compiled, err := compiler.expression(replace, map[string]string{"orders": "o"})
			if err != nil {
				t.Fatal(err)
			}
			if compiled != test.want || !reflect.DeepEqual(compiler.args, []any{2, 3, "OLD", "NEW"}) {
				t.Fatalf("compiled=%s args=%#v", compiled, compiler.args)
			}
		})
	}
}

func TestDateFormatCompilesExactCodesForMySQLAndOracle(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}
	for _, test := range []struct {
		name, unit, mysql, oracle string
	}{
		{name: "year", unit: "YEAR", mysql: "DATE_FORMAT(`o`.`order_date`, '%%Y')", oracle: `TO_CHAR("O"."ORDER_DATE", 'YYYY')`},
		{name: "month", unit: "MONTH", mysql: "DATE_FORMAT(`o`.`order_date`, '%%Y%%m')", oracle: `TO_CHAR("O"."ORDER_DATE", 'YYYYMM')`},
		{name: "quarter", unit: "QUARTER", mysql: "CONCAT(DATE_FORMAT(`o`.`order_date`, '%%Y'), 'Q', QUARTER(`o`.`order_date`))", oracle: `TO_CHAR("O"."ORDER_DATE", 'YYYY') || 'Q' || TO_CHAR("O"."ORDER_DATE", 'Q')`},
		{name: "day", unit: "DAY", mysql: "DATE_FORMAT(`o`.`order_date`, '%%Y%%m%%d')", oracle: `TO_CHAR("O"."ORDER_DATE", 'YYYYMMDD')`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, dialect := range []Dialect{MySQL, Oracle} {
				compiler := compiler{input: Input{Dialect: dialect, Tables: map[string]TableRef{"orders": {NodeID: "orders", Columns: map[string]bool{"order_date": true}}}}}
				compiled, err := compiler.expression(dataset.Expression{Type: "DATE_FORMAT", Unit: test.unit, Argument: &field}, map[string]string{"orders": "o"})
				if err != nil {
					t.Fatal(err)
				}
				want := test.mysql
				if dialect == Oracle {
					want = test.oracle
				}
				if compiled != want {
					t.Fatalf("dialect=%s compiled=%s want=%s", dialect, compiled, want)
				}
			}
		})
	}
}

func TestNumericAndContainsExpressionsCompileForMySQLAndOracle(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_amount"}
	for _, test := range []struct {
		name       string
		expression dataset.Expression
		mysql      string
		oracle     string
		wantArgs   []any
	}{
		{name: "round", expression: dataset.Expression{Type: "ROUND", Arguments: []dataset.Expression{field, {Type: "LITERAL", Value: 2}}}, mysql: "ROUND(`o`.`order_amount`,%s)", oracle: `ROUND("O"."ORDER_AMOUNT",:1)`, wantArgs: []any{2}},
		{name: "absolute", expression: dataset.Expression{Type: "ABS", Argument: &field}, mysql: "ABS(`o`.`order_amount`)", oracle: `ABS("O"."ORDER_AMOUNT")`},
		{name: "contains", expression: dataset.Expression{Type: "CONTAINS", Left: &field, Right: &dataset.Expression{Type: "LITERAL", Value: "12"}}, mysql: "(INSTR(`o`.`order_amount`,%s) > 0)", oracle: `(INSTR("O"."ORDER_AMOUNT",:1) > 0)`, wantArgs: []any{"12"}},
		{name: "not_contains", expression: dataset.Expression{Type: "NOT_CONTAINS", Left: &field, Right: &dataset.Expression{Type: "LITERAL", Value: "12"}}, mysql: "(INSTR(`o`.`order_amount`,%s) = 0)", oracle: `(INSTR("O"."ORDER_AMOUNT",:1) = 0)`, wantArgs: []any{"12"}},
		{name: "in_mixed_array", expression: dataset.Expression{Type: "IN", Left: &field, Right: &dataset.Expression{Type: "ARRAY", Arguments: []dataset.Expression{{Type: "LITERAL", Value: 12}, {Type: "FIELD_REF", NodeID: "orders", Field: "other_amount"}}}}, mysql: "(`o`.`order_amount` IN (%s,`o`.`other_amount`))", oracle: `("O"."ORDER_AMOUNT" IN (:1,"O"."OTHER_AMOUNT"))`, wantArgs: []any{12}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, dialect := range []Dialect{MySQL, Oracle} {
				compiler := compiler{input: Input{Dialect: dialect, Tables: map[string]TableRef{"orders": {NodeID: "orders", Columns: map[string]bool{"order_amount": true, "other_amount": true}}}}}
				compiled, err := compiler.expression(test.expression, map[string]string{"orders": "o"})
				if err != nil {
					t.Fatal(err)
				}
				want := test.mysql
				if dialect == Oracle {
					want = test.oracle
				}
				if compiled != want || !reflect.DeepEqual(compiler.args, test.wantArgs) {
					t.Fatalf("dialect=%s compiled=%s args=%#v want=%s/%#v", dialect, compiled, compiler.args, want, test.wantArgs)
				}
			}
		})
	}
}

func TestCompileAllowsProjectionAroundGroupedMetric(t *testing.T) {
	input := compilerInput(t)
	input.RowPolicies, input.ColumnPolicies = nil, nil
	aggregate := input.Document.Fields[1].Expression
	input.Document.Fields[1].Expression = dataset.Expression{Type: "CAST", TargetType: "STRING", Argument: &aggregate}
	input.Document.Fields[1].CanonicalType = "STRING"

	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "CAST(SUM(`o`.`order_amount`) AS CHAR) AS `revenue`") || !strings.Contains(compiled.SQL, "GROUP BY") {
		t.Fatalf("post-group projection SQL=%s", compiled.SQL)
	}
}

func TestCompileBuildsGroupedDerivedTableBeforeJoin(t *testing.T) {
	document := dataset.Document{
		DSLVersion: "1.0", Dataset: dataset.Descriptor{Code: "group_then_join", Name: "先分组后关联", Type: "SINGLE_SOURCE"},
		Nodes: []dataset.Node{
			{ID: "customers", Type: "TABLE", DataSourceID: "source-a", TableID: "table-a", Alias: "c", Projection: []string{"customer_id", "customer_name"}, SourceFilters: []dataset.SourceFilter{}},
			{ID: "orders", Type: "TABLE", DataSourceID: "source-a", TableID: "table-b", Alias: "o", Projection: []string{"customer_id", "amount"}, SourceFilters: []dataset.SourceFilter{}},
		},
		Joins:           []dataset.Join{{ID: "join_1", LeftNodeID: "customers", RightNodeID: "orders", JoinType: "LEFT", Cardinality: "UNKNOWN", ManualConfirmed: true, Conditions: []dataset.JoinCondition{{LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, Operator: "EQUALS", RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "customer_id"}}}}},
		PreAggregations: []dataset.PreAggregation{{ID: "group_1", NodeID: "customers", JoinID: "join_1", JoinSide: "LEFT", GroupBy: []dataset.PreAggregationGroup{{Field: "customer_id"}}, Metrics: []dataset.PreAggregationMetric{{Field: "customer_name", Function: "COUNT_DISTINCT"}}}},
		Fields: []dataset.Field{
			{ID: "field_customer_id", Code: "customer_id", Name: "客户ID", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, CanonicalType: "INTEGER"},
			{ID: "field_customer_count", Code: "customer_count", Name: "客户数", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}, CanonicalType: "INTEGER"},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain:     dataset.OutputGrain{Description: "每行一个客户", KeyFields: []string{"customer_id"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000, Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
	input := Input{Document: document, Dialect: MySQL, MaxRows: 10, Tables: map[string]TableRef{
		"customers": {NodeID: "customers", Schema: "sales", Name: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true}},
		"orders":    {NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"customer_id": true, "amount": true}},
	}}

	compiled, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "COUNT(DISTINCT `pre_source`.`customer_name`) AS `customer_name`") || !strings.Contains(compiled.SQL, "GROUP BY `pre_source`.`customer_id`) `c` LEFT JOIN") {
		t.Fatalf("pre-join aggregation SQL=%s", compiled.SQL)
	}

	upperRegion := dataset.Expression{Type: "UPPER", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}}
	customerCount := dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}
	document.PreAggregations = []dataset.PreAggregation{{
		ID: "group_1", NodeID: "customers", JoinID: "join_1", JoinSide: "LEFT",
		GroupBy: []dataset.PreAggregationGroup{
			{Field: "customer_id"},
			{Field: "region_upper", Expression: &upperRegion},
		},
		Metrics: []dataset.PreAggregationMetric{{Field: "customer_count", Function: "COUNT", Expression: &customerCount}},
	}}
	document.Fields = []dataset.Field{
		{ID: "field_region_upper", Code: "region_upper", Name: "大写地区", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "region_upper"}, CanonicalType: "STRING"},
		{ID: "field_customer_count", Code: "customer_count", Name: "客户数", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_count"}, CanonicalType: "INTEGER"},
	}
	document.OutputGrain = dataset.OutputGrain{Description: "每行一个大写地区", KeyFields: []string{"region_upper"}}
	input.Document = document
	compiled, err = Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "UPPER(`pre_source`.`customer_name`) AS `region_upper`") || !strings.Contains(compiled.SQL, "COUNT(`pre_source`.`customer_name`) AS `customer_count`") || !strings.Contains(compiled.SQL, "`c`.`region_upper` AS `region_upper`") {
		t.Fatalf("transformed pre-join aggregation SQL=%s", compiled.SQL)
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
	if len(compiled.Args) != 6 || compiled.Args[2] != 5 || compiled.Args[5] != 100 {
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
	if !strings.Contains(compiled.SQL, ":1") || strings.Contains(compiled.SQL, "%s") || !strings.HasSuffix(compiled.SQL, "FETCH FIRST :4 ROWS ONLY") || len(compiled.Args) != 4 || compiled.Args[3] != 100 {
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
	if len(compiled.Args) != 3 || compiled.Args[2] != 100 || !strings.HasSuffix(compiled.SQL, "LIMIT %s") {
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
