package filequery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
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

func TestEvaluateCubeProducesDetailSubtotalsAndGrandTotal(t *testing.T) {
	input := fileInput(t)
	input.RowPolicies, input.ColumnPolicies = nil, nil
	input.Document.Sorts = nil
	input.Document.Fields = append(input.Document.Fields, dataset.Field{
		ID: "field_order_status", Code: "order_status", Name: "订单状态", Role: "DIMENSION",
		Expression:    dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"},
		CanonicalType: "STRING", Nullable: true,
	})
	input.Document.GroupBy = append(input.Document.GroupBy, "field_order_status")
	input.Document.GroupByMode = dataset.GroupByModeCube

	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 6 {
		t.Fatalf("CUBE row count=%d rows=%#v", result.RowCount, result.Rows)
	}
	var detail, monthSubtotal, statusSubtotal, grandTotal int
	for _, row := range result.Rows {
		month, revenue, status := row[0], row[1], row[2]
		switch {
		case month != nil && status != nil:
			detail++
		case month != nil && status == nil:
			monthSubtotal++
		case month == nil && status != nil:
			statusSubtotal++
			if revenue != 50.0 {
				t.Fatalf("status subtotal=%#v", row)
			}
		default:
			grandTotal++
			if revenue != 50.0 {
				t.Fatalf("grand total=%#v", row)
			}
		}
	}
	if detail != 2 || monthSubtotal != 2 || statusSubtotal != 1 || grandTotal != 1 {
		t.Fatalf("unexpected CUBE levels detail=%d month=%d status=%d grand=%d rows=%#v", detail, monthSubtotal, statusSubtotal, grandTotal, result.Rows)
	}
}

func TestEvaluateRollupAndCustomGroupingSets(t *testing.T) {
	groupedInput := func(mode dataset.GroupByMode) Input {
		input := fileInput(t)
		input.RowPolicies, input.ColumnPolicies = nil, nil
		input.Document.Sorts = nil
		input.Document.Fields = append(input.Document.Fields, dataset.Field{
			ID: "field_order_status", Code: "order_status", Name: "订单状态", Role: "DIMENSION",
			Expression:    dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"},
			CanonicalType: "STRING", Nullable: true,
		})
		input.Document.GroupBy = append(input.Document.GroupBy, "field_order_status")
		input.Document.GroupByMode = mode
		return input
	}

	rollupInput := groupedInput(dataset.GroupByModeRollup)
	rollup, err := Evaluate(context.Background(), rollupInput)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.RowCount != 5 {
		t.Fatalf("ROLLUP row count=%d rows=%#v", rollup.RowCount, rollup.Rows)
	}
	for _, row := range rollup.Rows {
		if row[0] == nil && row[2] != nil {
			t.Fatalf("ROLLUP must not produce status-only subtotal: %#v", row)
		}
	}

	setsInput := groupedInput(dataset.GroupByModeSets)
	setsInput.Document.GroupingSets = [][]string{
		{"field_stat_month", "field_order_status"},
		{"field_order_status"},
		{},
	}
	groupingSets, err := Evaluate(context.Background(), setsInput)
	if err != nil {
		t.Fatal(err)
	}
	if groupingSets.RowCount != 4 {
		t.Fatalf("GROUPING SETS row count=%d rows=%#v", groupingSets.RowCount, groupingSets.Rows)
	}
	var detail, statusOnly, grandTotal int
	for _, row := range groupingSets.Rows {
		switch {
		case row[0] != nil && row[2] != nil:
			detail++
		case row[0] == nil && row[2] != nil:
			statusOnly++
		case row[0] == nil && row[2] == nil:
			grandTotal++
		default:
			t.Fatalf("unexpected GROUPING SETS level: %#v", row)
		}
	}
	if detail != 2 || statusOnly != 1 || grandTotal != 1 {
		t.Fatalf("unexpected GROUPING SETS levels detail=%d status=%d grand=%d rows=%#v", detail, statusOnly, grandTotal, groupingSets.Rows)
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

	document.PreAggregations[0].Metrics = []dataset.PreAggregationMetric{{Field: "customer_count", Function: "COUNT", CountRows: true}}
	document.Fields[1].Expression = dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_count"}
	input.Document = document
	result, err = Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0][1] != int64(2) || result.Rows[2][1] != int64(1) {
		t.Fatalf("pre-join COUNT(*) result=%#v", result)
	}

	document.Nodes[0].Projection = append(document.Nodes[0].Projection, "region")
	document.PreAggregations[0].GroupBy = append(document.PreAggregations[0].GroupBy, dataset.PreAggregationGroup{Field: "region"})
	document.PreAggregations[0].GroupByMode = dataset.GroupByModeCube
	document.Fields = []dataset.Field{
		{ID: "field_customer_id", Code: "customer_id", Name: "客户ID", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, CanonicalType: "INTEGER", Nullable: true},
		{ID: "field_region", Code: "region", Name: "地区", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "region"}, CanonicalType: "STRING", Nullable: true},
		{ID: "field_customer_count", Code: "customer_count", Name: "客户数", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_count"}, CanonicalType: "INTEGER", Nullable: false},
		{ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}, CanonicalType: "DECIMAL", Nullable: true},
	}
	input.Document = document
	input.Tables["customers"] = querycompiler.TableRef{NodeID: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true, "region": true}}
	input.NodeTables["customers"] = NodeTableData{
		Columns: []string{"customer_id", "customer_name", "region"},
		Rows:    [][]any{{int64(1), "张三", "华东"}, {int64(1), "李四", "华东"}, {int64(2), "王五", "华南"}},
	}
	result, err = Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 9 {
		t.Fatalf("pre-join CUBE row count=%d rows=%#v", result.RowCount, result.Rows)
	}
	var detail, customerSubtotal, regionSubtotal, grandTotal int
	for _, row := range result.Rows {
		switch {
		case row[0] != nil && row[1] != nil:
			detail++
		case row[0] != nil:
			customerSubtotal++
		case row[1] != nil:
			regionSubtotal++
		default:
			grandTotal++
		}
	}
	if detail != 3 || customerSubtotal != 3 || regionSubtotal != 2 || grandTotal != 1 {
		t.Fatalf("pre-join CUBE levels detail=%d customer=%d region=%d grand=%d rows=%#v", detail, customerSubtotal, regionSubtotal, grandTotal, result.Rows)
	}

	document.PreAggregations[0].GroupByMode = dataset.GroupByModeRollup
	document.PreAggregations[0].GroupingSets = nil
	input.Document = document
	result, err = Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 7 {
		t.Fatalf("pre-join ROLLUP row count=%d rows=%#v", result.RowCount, result.Rows)
	}
	for _, row := range result.Rows {
		if row[0] == nil && row[1] != nil {
			t.Fatalf("pre-join ROLLUP must not produce region-only subtotal: %#v", row)
		}
	}

	document.PreAggregations[0].GroupByMode = dataset.GroupByModeSets
	document.PreAggregations[0].GroupingSets = [][]string{{"customer_id", "region"}, {"region"}, {}}
	input.Document = document
	result, err = Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 6 {
		t.Fatalf("pre-join GROUPING SETS row count=%d rows=%#v", result.RowCount, result.Rows)
	}
	for _, row := range result.Rows {
		if row[0] != nil && row[1] == nil {
			t.Fatalf("pre-join GROUPING SETS must not produce customer-only subtotal: %#v", row)
		}
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

func TestParseCellAcceptsExcelCurrencyAndPercentageFormatting(t *testing.T) {
	amount, err := parseCell("¥16,320.00", "DECIMAL")
	if err != nil || amount != 16320.0 {
		t.Fatalf("amount=%#v err=%v", amount, err)
	}
	ratio, err := parseCell("65.1%", "DECIMAL")
	if err != nil || math.Abs(ratio.(float64)-0.651) > 1e-12 {
		t.Fatalf("ratio=%#v err=%v", ratio, err)
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

func TestDateCalculationUsesCalendarDifferencesPartsAndBoundaries(t *testing.T) {
	start := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "start_date"}
	end := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "end_date"}
	row := sourceRow{
		"orders.start_date": "2024-02-29T23:30:00+08:00",
		"orders.end_date":   "2026-07-05T01:00:00+08:00",
	}
	for unit, expected := range map[string]int64{"YEAR": 2, "MONTH": 29, "DAY": 857} {
		value, err := evaluateExpression(dataset.Expression{Type: "DATE_DIFF", Unit: unit, Arguments: []dataset.Expression{start, end}}, row, nil, nil)
		if err != nil || value != expected {
			t.Fatalf("DATE_DIFF unit=%s value=%#v err=%v", unit, value, err)
		}
	}
	for unit, expected := range map[string]int64{
		"YEAR": 2024, "QUARTER": 1, "MONTH": 2, "WEEK": 9, "DAY": 29, "WEEKDAY": 4, "DAY_OF_YEAR": 60,
	} {
		value, err := evaluateExpression(dataset.Expression{Type: "DATE_EXTRACT", Unit: unit, Argument: &start}, row, nil, nil)
		if err != nil || value != expected {
			t.Fatalf("DATE_EXTRACT unit=%s value=%#v err=%v", unit, value, err)
		}
	}
	for _, test := range []struct {
		operation, unit, expected string
	}{
		{operation: "DATE_START", unit: "WEEK", expected: "2024-02-26"},
		{operation: "DATE_END", unit: "WEEK", expected: "2024-03-03"},
		{operation: "DATE_START", unit: "MONTH", expected: "2024-02-01"},
		{operation: "DATE_END", unit: "MONTH", expected: "2024-02-29"},
		{operation: "DATE_START", unit: "QUARTER", expected: "2024-01-01"},
		{operation: "DATE_END", unit: "QUARTER", expected: "2024-03-31"},
		{operation: "DATE_START", unit: "YEAR", expected: "2024-01-01"},
		{operation: "DATE_END", unit: "YEAR", expected: "2024-12-31"},
	} {
		value, err := evaluateExpression(dataset.Expression{Type: test.operation, Unit: test.unit, Argument: &start}, row, nil, nil)
		if err != nil || value != test.expected {
			t.Fatalf("%s unit=%s value=%#v err=%v", test.operation, test.unit, value, err)
		}
	}
	current, err := evaluateExpression(dataset.Expression{Type: "CURRENT_DATE"}, nil, nil, nil)
	if err != nil || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(fmt.Sprint(current)) {
		t.Fatalf("CURRENT_DATE value=%#v err=%v", current, err)
	}
}

func TestDateCalculationPreservesNullAndNegativeDirection(t *testing.T) {
	start := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "start_date"}
	end := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "end_date"}
	value, err := evaluateExpression(dataset.Expression{Type: "DATE_DIFF", Unit: "DAY", Arguments: []dataset.Expression{start, end}}, sourceRow{
		"orders.start_date": "2026-01-02",
		"orders.end_date":   "2025-12-31",
	}, nil, nil)
	if err != nil || value != int64(-2) {
		t.Fatalf("negative DATE_DIFF value=%#v err=%v", value, err)
	}
	nullValue, err := evaluateExpression(dataset.Expression{Type: "DATE_END", Unit: "MONTH", Argument: &start}, sourceRow{"orders.start_date": nil}, nil, nil)
	if err != nil || nullValue != nil {
		t.Fatalf("DATE_END NULL value=%#v err=%v", nullValue, err)
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

func TestEvaluateWindowRankPartitionsAndOrdersRows(t *testing.T) {
	input := fileInput(t)
	input.RowPolicies, input.ColumnPolicies = nil, nil
	input.Parameters = nil
	input.Document.Dataset.Layer = dataset.LayerDWD
	input.Document.Parameters = nil
	input.Document.Filters = nil
	input.Document.Nodes[0].SourceFilters = nil
	input.Document.GroupBy = nil
	input.Document.Having = nil
	input.Document.Sorts = nil
	input.Document.FactContract = nil
	input.Document.AnalysisContract = nil
	status := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"}
	amount := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_amount"}
	input.Document.Fields = []dataset.Field{
		{ID: "field_status", Code: "status", Name: "状态", Role: "DIMENSION", Expression: status, CanonicalType: "STRING"},
		{ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE", Expression: amount, CanonicalType: "DECIMAL"},
		{
			ID: "field_rank", Code: "status_rank", Name: "状态内排名", Role: "ATTRIBUTE", CanonicalType: "INTEGER",
			Expression: dataset.Expression{
				Type: "WINDOW", Function: "RANK",
				PartitionBy: []dataset.Expression{status},
				OrderBy:     []dataset.WindowOrder{{Expression: amount, Direction: "DESC"}},
			},
		},
		{
			ID: "field_partition_sum", Code: "status_amount_sum", Name: "状态内金额合计", Role: "MEASURE", CanonicalType: "DECIMAL",
			Expression: dataset.Expression{
				Type: "WINDOW", Function: "SUM", Argument: &amount,
				PartitionBy: []dataset.Expression{status},
				OrderBy:     []dataset.WindowOrder{{Expression: amount, Direction: "DESC"}},
			},
		},
	}
	input.Document.OutputGrain = dataset.OutputGrain{Description: "每行代表一笔订单", KeyFields: []string{"status_rank"}}

	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	wantRanks := []any{int64(3), int64(4), int64(2), int64(1), int64(1)}
	if result.RowCount != 5 {
		t.Fatalf("row count=%d rows=%#v", result.RowCount, result.Rows)
	}
	for index, row := range result.Rows {
		if row[2] != wantRanks[index] {
			t.Fatalf("row %d rank=%#v want=%#v rows=%#v", index, row[2], wantRanks[index], result.Rows)
		}
		wantSum := 150.0
		if index == 4 {
			wantSum = 999.0
		}
		if row[3] != wantSum {
			t.Fatalf("row %d window sum=%#v want=%#v rows=%#v", index, row[3], wantSum, result.Rows)
		}
	}
}
