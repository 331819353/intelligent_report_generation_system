package querycompiler

import (
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func joinProbeInput(dialect Dialect) JoinProbeInput {
	return JoinProbeInput{
		Document: dataset.Document{
			DSLVersion: dataset.DSLVersion,
			Dataset:    dataset.Descriptor{Code: "customer_orders", Name: "客户订单", Type: "SINGLE_SOURCE"},
			Nodes: []dataset.Node{
				{
					ID: "orders", Type: "TABLE", DataSourceID: "source-main", TableID: "table-orders", Alias: "o",
					Projection:    []string{"customer_id", "tenant_id", "status"},
					SourceFilters: []dataset.SourceFilter{{Field: "status", Operator: "EQUALS", Value: "PAID"}},
				},
				{
					ID: "customers", Type: "TABLE", DataSourceID: "source-main", TableID: "table-customers", Alias: "c",
					Projection:    []string{"id", "tenant_id", "name", "state"},
					SourceFilters: []dataset.SourceFilter{{Field: "state", Operator: "EQUALS", Value: "ACTIVE"}},
				},
			},
			Joins: []dataset.Join{{
				ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "INNER", Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
				Conditions: []dataset.JoinCondition{
					{
						LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "customer_id"},
						Operator:        "EQUALS",
						RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "id"},
					},
					{
						LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "tenant_id"},
						Operator:        "EQUALS",
						RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "tenant_id"},
					},
				},
			}},
			Fields: []dataset.Field{{
				ID: "field_customer_name", Code: "customer_name", Name: "客户名称", Role: "DIMENSION", CanonicalType: "STRING",
				Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "name"},
			}},
			Filters: []dataset.Filter{{
				ID: "filter_tenant", Stage: "PRE_AGGREGATION",
				Expression: dataset.Expression{
					Type:  "EQUALS",
					Left:  &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "tenant_id"},
					Right: &dataset.Expression{Type: "PARAM_REF", Code: "tenant_code"},
				},
			}},
			GroupBy:    []string{},
			Having:     []dataset.Filter{},
			Sorts:      []dataset.Sort{},
			Parameters: []dataset.Parameter{{Code: "tenant_code", Name: "租户编码", DataType: "STRING", Required: true}},
			OutputGrain: dataset.OutputGrain{
				Description: "每行一个客户", KeyFields: []string{"customer_name"},
			},
			ExecutionPolicy: dataset.ExecutionPolicy{
				Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000,
				Materialization: dataset.MaterializationPolicy{Enabled: false},
			},
		},
		Dialect: dialect,
		Tables: map[string]TableRef{
			"orders": {
				NodeID: "orders", Schema: "sales", Name: "orders",
				Columns: map[string]bool{"customer_id": true, "tenant_id": true, "status": true},
			},
			"customers": {
				NodeID: "customers", Schema: "crm", Name: "customers",
				Columns: map[string]bool{"id": true, "tenant_id": true, "name": true, "state": true},
			},
		},
		Parameters: map[string]any{"tenant_code": "tenant-a'; DROP TABLE audit; --"},
	}
}

func TestCompileJoinProbesGeneratesAggregateStatisticsForMySQLAndOracle(t *testing.T) {
	tests := []struct {
		name        string
		dialect     Dialect
		placeholder string
		leftTable   string
		rightTable  string
		firstKey    string
		secondKey   string
		statistics  []string
		oracleDual  bool
	}{
		{
			name: "MySQL", dialect: MySQL, placeholder: "%s", leftTable: "`sales`.`orders`", rightTable: "`crm`.`customers`",
			firstKey: "l.`probe_key_0` = r.`probe_key_0`", secondKey: "l.`probe_key_1` = r.`probe_key_1`",
			statistics: []string{"`left_duplicate_keys`", "`right_duplicate_keys`", "`left_max_multiplicity`", "`right_max_multiplicity`", "`fanout_keys`"},
		},
		{
			name: "Oracle", dialect: Oracle, placeholder: ":1", leftTable: `"SALES"."ORDERS"`, rightTable: `"CRM"."CUSTOMERS"`,
			firstKey: `l."PROBE_KEY_0" = r."PROBE_KEY_0"`, secondKey: `l."PROBE_KEY_1" = r."PROBE_KEY_1"`,
			statistics: []string{`"LEFT_DUPLICATE_KEYS"`, `"RIGHT_DUPLICATE_KEYS"`, `"LEFT_MAX_MULTIPLICITY"`, `"RIGHT_MAX_MULTIPLICITY"`, `"FANOUT_KEYS"`},
			oracleDual: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := joinProbeInput(test.dialect)
			compiled, err := CompileJoinProbes(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled) != 1 || compiled[0].JoinID != "orders_customers" {
				t.Fatalf("unexpected probes: %#v", compiled)
			}
			query := compiled[0].Query
			if query.MaxRows != 1 {
				t.Fatalf("聚合探测必须最多返回一行，实际为 %d", query.MaxRows)
			}
			for _, fragment := range []string{test.leftTable, test.rightTable, test.firstKey, test.secondKey, "GROUP BY"} {
				if !strings.Contains(query.SQL, fragment) {
					t.Fatalf("SQL 缺少 %q: %s", fragment, query.SQL)
				}
			}
			if strings.Count(query.SQL, test.placeholder) == 0 {
				t.Fatalf("SQL 未使用方言占位符 %q: %s", test.placeholder, query.SQL)
			}
			if test.dialect == Oracle && (!strings.Contains(query.SQL, ":2") || !strings.Contains(query.SQL, ":3")) {
				t.Fatalf("Oracle 绑定编号不连续: %s", query.SQL)
			}
			if strings.HasSuffix(query.SQL, " FROM DUAL") != test.oracleDual {
				t.Fatalf("Oracle DUAL 处理不正确: %s", query.SQL)
			}

			// 外层 SELECT 只能暴露五个聚合统计字段，不能返回业务键或探测键。
			outerIndex := strings.LastIndex(query.SQL, ") SELECT ")
			if outerIndex < 0 {
				t.Fatalf("未找到聚合探测的外层 SELECT: %s", query.SQL)
			}
			outerSelect := query.SQL[outerIndex+len(") SELECT "):]
			if strings.Count(outerSelect, " AS ") != len(test.statistics) {
				t.Fatalf("外层返回列数量不正确: %s", outerSelect)
			}
			for _, column := range test.statistics {
				if strings.Count(outerSelect, " AS "+column) != 1 {
					t.Fatalf("外层缺少唯一统计列 %s: %s", column, outerSelect)
				}
			}
			for _, businessColumn := range []string{"customer_id", "tenant_id", "id", "name", "status", "state"} {
				if strings.Contains(strings.ToLower(outerSelect), businessColumn) {
					t.Fatalf("外层泄露业务列 %q: %s", businessColumn, outerSelect)
				}
			}

			wantArgs := []any{"PAID", "tenant-a'; DROP TABLE audit; --", "ACTIVE"}
			if !reflect.DeepEqual(query.Args, wantArgs) {
				t.Fatalf("绑定参数顺序不正确: %#v", query.Args)
			}
			for _, value := range wantArgs {
				if strings.Contains(query.SQL, value.(string)) {
					t.Fatalf("参数值进入 SQL: %s", query.SQL)
				}
			}
			if strings.Contains(query.SQL, "DROP TABLE") || strings.Contains(query.SQL, "--") {
				t.Fatalf("带注入特征的参数污染 SQL: %s", query.SQL)
			}
		})
	}
}

func TestCompileJoinProbesRejectsUntrustedPhysicalWhitelist(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*JoinProbeInput)
		errFragment string
	}{
		{
			name: "节点缺失", errFragment: "node is not in the physical whitelist",
			mutate: func(input *JoinProbeInput) { delete(input.Tables, "customers") },
		},
		{
			name: "节点映射不一致", errFragment: "node is not in the physical whitelist",
			mutate: func(input *JoinProbeInput) {
				ref := input.Tables["customers"]
				ref.NodeID = "orders"
				input.Tables["customers"] = ref
			},
		},
		{
			name: "关联列缺失", errFragment: "expression is not a whitelisted field",
			mutate: func(input *JoinProbeInput) { delete(input.Tables["customers"].Columns, "id") },
		},
		{
			name: "物理表标识非法", errFragment: "physical table identifier is invalid",
			mutate: func(input *JoinProbeInput) {
				ref := input.Tables["customers"]
				ref.Name = "customers;DROP"
				input.Tables["customers"] = ref
			},
		},
		{
			name: "物理列标识非法", errFragment: "physical column identifier is invalid",
			mutate: func(input *JoinProbeInput) { input.Tables["customers"].Columns["unsafe;column"] = true },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := joinProbeInput(MySQL)
			test.mutate(&input)
			_, err := CompileJoinProbes(input)
			if err == nil || !strings.Contains(err.Error(), test.errFragment) {
				t.Fatalf("expected error containing %q, got %v", test.errFragment, err)
			}
		})
	}
}

func TestCompileJoinProbesRejectsNonEqualityJoin(t *testing.T) {
	input := joinProbeInput(MySQL)
	input.Document.Joins[0].Conditions[0].Operator = "GT"
	_, err := CompileJoinProbes(input)
	if err == nil || !strings.Contains(err.Error(), "only supports equality conditions") {
		t.Fatalf("非等值 Join 未被拒绝: %v", err)
	}
}

func TestCompileJoinProbesRejectsNonSingleSourceDataset(t *testing.T) {
	input := joinProbeInput(MySQL)
	input.Document.Dataset.Type = "CROSS_SOURCE"
	input.Document.Nodes[1].DataSourceID = "source-customer"
	_, err := CompileJoinProbes(input)
	if err == nil || !strings.Contains(err.Error(), "requires a single-source dataset") {
		t.Fatalf("非单源数据集未被拒绝: %v", err)
	}
}
