package filequery

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

func fileInput(t *testing.T) Input {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Document.Nodes[0].FileVersionID = "file-version-1"
	return Input{
		Document: prepared.Document,
		Tables: map[string]querycompiler.TableRef{
			"orders": {NodeID: "orders", Name: "orders", Columns: map[string]bool{"order_date": true, "order_amount": true, "order_status": true}},
		},
		FileTables: []datasource.FileTableData{{
			Name: "orders", Columns: []string{"order_date", "order_amount", "order_status"},
			Types: map[string]string{"order_date": "DATE", "order_amount": "DECIMAL", "order_status": "STRING"},
			Rows: [][]string{
				{"2026-01-05", "12", "VALID"},
				{"2026-01-20", "8", "VALID"},
				{"2026-02-03", "30", "VALID"},
				{"2025-12-01", "100", "VALID"},
				{"2026-01-09", "999", "CANCELLED"},
			},
		}},
		Parameters: map[string]any{"start_date": "2026-01-01"},
		Scope:      policy.UserScope{TenantID: "tenant-1", UserID: "user-1", Attributes: map[string]any{"month": "2026-01-01"}},
		RowPolicies: []policy.RowPolicy{{
			ID: "month", Effect: "ALLOW", CombineMode: "AND",
			Expression: policy.Expression{Type: "EQUALS", Left: &policy.Expression{Type: "FIELD_REF", FieldCode: "stat_month"}, Right: &policy.Expression{Type: "USER_ATTRIBUTE_REF", Attribute: "month"}},
		}},
		ColumnPolicies: []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "NULLIFY"}},
		MaxRows:        100,
	}
}

func TestEvaluateAppliesFiltersAggregationPoliciesAndSort(t *testing.T) {
	result, err := Evaluate(context.Background(), fileInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.Rows[0][0] != "2026-01-01" || result.Rows[0][1] != nil {
		t.Fatalf("unexpected protected result: %#v", result)
	}
}

func TestEvaluateAllowsProjectionAroundGroupedMetric(t *testing.T) {
	input := fileInput(t)
	input.RowPolicies, input.ColumnPolicies = nil, nil
	aggregate := input.Document.Fields[1].Expression
	input.Document.Fields[1].Expression = dataset.Expression{Type: "CAST", TargetType: "STRING", Argument: &aggregate}
	input.Document.Fields[1].CanonicalType = "STRING"

	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[0][1] != "20" || result.Rows[1][1] != "30" {
		t.Fatalf("post-group projection result=%#v", result)
	}
}

func TestEvaluateGroupsSlotOneBeforeJoining(t *testing.T) {
	document := dataset.Document{
		DSLVersion: "1.0", Dataset: dataset.Descriptor{Code: "group_then_join", Name: "先分组后关联", Type: "CROSS_SOURCE"},
		Nodes: []dataset.Node{
			{ID: "customers", Type: "TABLE", DataSourceID: "source-a", TableID: "table-a", Alias: "c", Projection: []string{"customer_id", "customer_name"}, SourceFilters: []dataset.SourceFilter{}},
			{ID: "orders", Type: "TABLE", DataSourceID: "source-b", TableID: "table-b", Alias: "o", Projection: []string{"customer_id", "amount"}, SourceFilters: []dataset.SourceFilter{}},
		},
		Joins:           []dataset.Join{{ID: "join_1", LeftNodeID: "customers", RightNodeID: "orders", JoinType: "LEFT", Cardinality: "UNKNOWN", ManualConfirmed: true, Conditions: []dataset.JoinCondition{{LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, Operator: "EQUALS", RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "customer_id"}}}}},
		PreAggregations: []dataset.PreAggregation{{ID: "group_1", NodeID: "customers", JoinID: "join_1", JoinSide: "LEFT", GroupBy: []dataset.PreAggregationGroup{{Field: "customer_id"}}, Metrics: []dataset.PreAggregationMetric{{Field: "customer_name", Function: "COUNT_DISTINCT"}}}},
		Fields: []dataset.Field{
			{ID: "field_customer_id", Code: "customer_id", Name: "客户ID", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, CanonicalType: "INTEGER", Nullable: false},
			{ID: "field_customer_count", Code: "customer_count", Name: "客户数", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}, CanonicalType: "INTEGER", Nullable: false},
			{ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}, CanonicalType: "DECIMAL", Nullable: true},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain:     dataset.OutputGrain{Description: "每行一个客户订单", KeyFields: []string{"customer_id"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000, Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
	input := Input{Document: document, Tables: map[string]querycompiler.TableRef{
		"customers": {NodeID: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true}},
		"orders":    {NodeID: "orders", Columns: map[string]bool{"customer_id": true, "amount": true}},
	}, NodeTables: map[string]NodeTableData{
		"customers": {Columns: []string{"customer_id", "customer_name"}, Rows: [][]any{{int64(1), "张三"}, {int64(1), "李四"}, {int64(2), "王五"}}},
		"orders":    {Columns: []string{"customer_id", "amount"}, Rows: [][]any{{int64(1), int64(10)}, {int64(1), int64(20)}, {int64(2), int64(30)}}},
	}, MaxRows: 100}

	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 3 || result.Rows[0][0] != int64(1) || result.Rows[0][1] != int64(2) || result.Rows[2][1] != int64(1) {
		t.Fatalf("pre-join aggregation result=%#v", result)
	}
}

func TestEvaluateSkipsAbsentOptionalFilterAndAppliesResultLimit(t *testing.T) {
	input := fileInput(t)
	input.Document.Parameters[0].Required = false
	input.Document.Filters[0].Optional = true
	input.Parameters["start_date"] = nil
	input.RowPolicies, input.ColumnPolicies = nil, nil
	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 3 || result.Rows[0][0] != "2025-12-01" || result.Rows[2][0] != "2026-02-01" {
		t.Fatalf("optional filter result=%#v", result)
	}
	input.MaxRows = 2
	result, err = Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || len(result.Rows) != 2 || result.Rows[0][0] != "2025-12-01" || result.Rows[1][0] != "2026-01-01" {
		t.Fatalf("最终结果应在完整排序后截取前两行: %#v", result)
	}
}

func TestEvaluateRejectsVersionSchemaDriftAndUnsafeMask(t *testing.T) {
	input := fileInput(t)
	input.FileTables[0].Columns = []string{"order_date", "order_amount"}
	if _, err := Evaluate(context.Background(), input); err == nil {
		t.Fatal("固定版本缺少投影字段时仍然执行")
	}
	input = fileInput(t)
	input.ColumnPolicies = []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "MASK", MaskRule: policy.MaskRule{Type: "KEEP_PREFIX_SUFFIX", MaskChar: `';--`}}}
	if _, err := Evaluate(context.Background(), input); err == nil {
		t.Fatal("不安全的脱敏字符未被拒绝")
	}
}

func TestEvaluateUsesOnlyHighestPriorityColumnPolicy(t *testing.T) {
	input := fileInput(t)
	input.ColumnPolicies = []policy.ColumnPolicy{
		{FieldCode: "revenue", PolicyType: "ALLOW"},
		{FieldCode: "revenue", PolicyType: "MASK", MaskRule: policy.MaskRule{Type: "KEEP_PREFIX_SUFFIX", MaskChar: `';--`}},
	}
	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0][1] != 20.0 {
		t.Fatalf("低优先级列策略错误生效: %#v", result.Rows)
	}
}

func TestComparisonKeepsSQLNullSemantics(t *testing.T) {
	nilValue := dataset.Expression{Type: "LITERAL", Value: nil}
	collection := dataset.Expression{Type: "LITERAL", Value: []any{"A", nil}}
	notIn := dataset.Expression{Type: "NOT_IN", Left: &dataset.Expression{Type: "LITERAL", Value: "B"}, Right: &collection}
	value, err := evaluateExpression(notIn, nil, nil, nil)
	if err != nil || value != false {
		t.Fatalf("NOT IN with NULL value=%#v err=%v", value, err)
	}
	equals := dataset.Expression{Type: "EQUALS", Left: &nilValue, Right: &nilValue}
	value, err = evaluateExpression(equals, nil, nil, nil)
	if err != nil || value != false {
		t.Fatalf("NULL equals NULL value=%#v err=%v", value, err)
	}
}

func TestTextExpressionsUseUnicodeCharacterPositions(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "source", Field: "name"}
	substring := dataset.Expression{Type: "SUBSTRING", Arguments: []dataset.Expression{{Type: "TRIM", Argument: &field}, {Type: "LITERAL", Value: 2}, {Type: "LITERAL", Value: 3}}}
	value, err := evaluateExpression(substring, sourceRow{"source.name": " 甲乙Ab丙 "}, nil, nil)
	if err != nil || value != "乙Ab" {
		t.Fatalf("substring value=%#v err=%v", value, err)
	}
	replace := dataset.Expression{Type: "REPLACE", Arguments: []dataset.Expression{{Type: "LOWER", Argument: &substring}, {Type: "LITERAL", Value: "ab"}, {Type: "LITERAL", Value: "xy"}}}
	value, err = evaluateExpression(replace, sourceRow{"source.name": " 甲乙Ab丙 "}, nil, nil)
	if err != nil || value != "乙xy" {
		t.Fatalf("replace value=%#v err=%v", value, err)
	}
	nilValue, err := evaluateExpression(dataset.Expression{Type: "UPPER", Argument: &dataset.Expression{Type: "LITERAL", Value: nil}}, nil, nil, nil)
	if err != nil || nilValue != nil {
		t.Fatalf("upper NULL value=%#v err=%v", nilValue, err)
	}
}

func TestDateTruncAcceptsConnectorTimestampWithFractionalSecondsAndNoTimezone(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "created_at"}
	for unit, expected := range map[string]string{
		"YEAR": "2026-01-01", "MONTH": "2026-07-01", "QUARTER": "2026-07-01", "DAY": "2026-07-15",
	} {
		value, err := evaluateExpression(dataset.Expression{Type: "DATE_TRUNC", Unit: unit, Argument: &field}, sourceRow{
			"orders.created_at": "2026-07-15T01:36:12.393392",
		}, nil, nil)
		if err != nil || value != expected {
			t.Fatalf("unit=%s value=%#v err=%v", unit, value, err)
		}
	}
}

func TestDateFormatProducesExactCalendarCodes(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "created_at"}
	for unit, expected := range map[string]string{
		"YEAR": "2026", "MONTH": "202607", "QUARTER": "2026Q3", "DAY": "20260715",
	} {
		value, err := evaluateExpression(dataset.Expression{Type: "DATE_FORMAT", Unit: unit, Argument: &field}, sourceRow{
			"orders.created_at": "2026-07-15T01:36:12.393392",
		}, nil, nil)
		if err != nil || value != expected {
			t.Fatalf("unit=%s value=%#v err=%v", unit, value, err)
		}
	}
	nullValue, err := evaluateExpression(dataset.Expression{Type: "DATE_FORMAT", Unit: "MONTH", Argument: &field}, sourceRow{"orders.created_at": nil}, nil, nil)
	if err != nil || nullValue != nil {
		t.Fatalf("DATE_FORMAT NULL value=%#v err=%v", nullValue, err)
	}
}

func TestNumericAndContainsExpressionsProduceExpectedValues(t *testing.T) {
	field := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}
	row := sourceRow{"orders.amount": -12.345}
	for _, test := range []struct {
		name       string
		expression dataset.Expression
		expected   any
	}{
		{name: "round", expression: dataset.Expression{Type: "ROUND", Arguments: []dataset.Expression{field, {Type: "LITERAL", Value: 2}}}, expected: -12.35},
		{name: "absolute", expression: dataset.Expression{Type: "ABS", Argument: &field}, expected: 12.345},
		{name: "floor", expression: dataset.Expression{Type: "FLOOR", Argument: &field}, expected: -13.0},
		{name: "ceil", expression: dataset.Expression{Type: "CEIL", Argument: &field}, expected: -12.0},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := evaluateExpression(test.expression, row, nil, nil)
			if err != nil || value != test.expected {
				t.Fatalf("value=%#v err=%v want=%#v", value, err, test.expected)
			}
		})
	}

	textField := dataset.Expression{Type: "CAST", TargetType: "STRING", Argument: &field}
	needle := dataset.Expression{Type: "LITERAL", Value: "12.3"}
	for operator, expected := range map[string]bool{"CONTAINS": true, "NOT_CONTAINS": false} {
		value, err := evaluateExpression(dataset.Expression{Type: operator, Left: &textField, Right: &needle}, row, nil, nil)
		if err != nil || value != expected {
			t.Fatalf("operator=%s value=%#v err=%v", operator, value, err)
		}
	}

	inValues := dataset.Expression{Type: "ARRAY", Arguments: []dataset.Expression{{Type: "LITERAL", Value: -10.0}, {Type: "FIELD_REF", NodeID: "orders", Field: "other_amount"}}}
	inValue, err := evaluateExpression(dataset.Expression{Type: "IN", Left: &field, Right: &inValues}, sourceRow{"orders.amount": -12.345, "orders.other_amount": -12.345}, nil, nil)
	if err != nil || inValue != true {
		t.Fatalf("mixed IN value=%#v err=%v", inValue, err)
	}

	nullValue, err := evaluateExpression(dataset.Expression{Type: "ROUND", Arguments: []dataset.Expression{field, {Type: "LITERAL", Value: 2}}}, sourceRow{"orders.amount": nil}, nil, nil)
	if err != nil || nullValue != nil {
		t.Fatalf("ROUND NULL value=%#v err=%v", nullValue, err)
	}
}

func TestEvaluateHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Evaluate(ctx, fileInput(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestHashInnerJoinBuildsIndexFromSmallerInput(t *testing.T) {
	join := dataset.Join{JoinType: "INNER", Conditions: []dataset.JoinCondition{{
		LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "left", Field: "id"}, Operator: "EQUALS",
		RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "right", Field: "id"},
	}}}
	left := []sourceRow{{"left.id": int64(1), "left.name": "L1"}, {"left.id": int64(2), "left.name": "L2"}}
	right := []sourceRow{{"right.id": int64(2), "right.name": "R2a"}, {"right.id": int64(1), "right.name": "R1"}, {"right.id": int64(2), "right.name": "R2b"}}
	rows, err := hashJoin(context.Background(), left, right, join)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["left.name"] != "L2" || rows[1]["left.name"] != "L1" || rows[2]["right.name"] != "R2b" {
		t.Fatalf("join rows=%#v", rows)
	}
}

func TestHashLeftJoinDoesNotSwapDeclaredSides(t *testing.T) {
	join := dataset.Join{JoinType: "LEFT", Conditions: []dataset.JoinCondition{{
		LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "left", Field: "id"}, Operator: "EQUALS",
		RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "right", Field: "id"},
	}}}
	left := []sourceRow{{"left.id": int64(1), "left.name": "L1"}, {"left.id": int64(3), "left.name": "L3"}}
	right := []sourceRow{{"right.id": int64(2)}, {"right.id": int64(1)}, {"right.id": int64(2)}}
	rows, err := hashJoin(context.Background(), left, right, join)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["left.name"] != "L1" || rows[1]["left.name"] != "L3" || rows[1]["right.id"] != nil {
		t.Fatalf("left join rows=%#v", rows)
	}
}

func TestAggregateSumPreservesIntegersAndRejectsOverflow(t *testing.T) {
	expression := dataset.Expression{Type: "AGGREGATE", Function: "SUM", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "source", Field: "value"}}
	value, err := aggregate(expression, []sourceRow{{"source.value": int64(2)}, {"source.value": int64(3)}}, nil)
	if err != nil || value != int64(5) {
		t.Fatalf("integer sum=%#v err=%v", value, err)
	}
	_, err = aggregate(expression, []sourceRow{{"source.value": int64(math.MaxInt64)}, {"source.value": int64(1)}}, nil)
	if err == nil {
		t.Fatal("integer SUM overflow was not rejected")
	}
}
