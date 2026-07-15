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

func TestEvaluateSkipsAbsentOptionalFilterAndEnforcesResultLimit(t *testing.T) {
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
	if _, err := Evaluate(context.Background(), input); err == nil {
		t.Fatal("超过文件预览行数上限的结果未被拒绝")
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
