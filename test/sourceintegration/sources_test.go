//go:build sourceintegration

package sourceintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	runtimefederation "intelligent-report-generation-system/internal/federation"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/queryruntime"
	spikefederation "intelligent-report-generation-system/internal/spike/federation"
)

type result struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"rowCount"`
}

func TestPythonConnectorMySQLOracleAndGoJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mysql := query(t, ctx, map[string]any{"source_type": "MYSQL", "host": "mysql", "port": 3306, "database": "report_source", "username": "report_reader", "password": "local_mysql_reader_password"}, "SELECT customer_id,customer_name,region_code FROM customers ORDER BY customer_id", nil, 100)
	oracle := query(t, ctx, map[string]any{"source_type": "ORACLE", "host": "oracle", "port": 1521, "database": "FREEPDB1", "username": "report_reader", "password": "local_oracle_reader_password"}, "SELECT order_id,customer_id,amount FROM orders ORDER BY order_id", nil, 100)
	customers := make([]spikefederation.Row, 0, len(mysql.Rows))
	for _, row := range mysql.Rows {
		customers = append(customers, spikefederation.Row{"customer_id": row[0], "customer_name": row[1], "region_code": row[2]})
	}
	orders := make([]spikefederation.Row, 0, len(oracle.Rows))
	for _, row := range oracle.Rows {
		orders = append(orders, spikefederation.Row{"order_id": row[0], "customer_id": row[1], "amount": row[2]})
	}
	joined, err := spikefederation.HashJoin(orders, customers, "customer_id", "customer_id", 100)
	if err != nil || len(joined) != 3 {
		t.Fatalf("cross-source join rows=%d err=%v", len(joined), err)
	}
}

func TestFormalFederatedExecutorQueriesMySQLAndOracle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("SOURCE_IT_MYSQL_SECRET", `{"host":"mysql","port":"3306","database":"report_source","username":"report_reader","password":"local_mysql_reader_password"}`)
	t.Setenv("SOURCE_IT_ORACLE_SECRET", `{"host":"oracle","port":"1521","database":"FREEPDB1","username":"report_reader","password":"local_oracle_reader_password"}`)
	connectorURL := env("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090")
	connectorToken := env("CONNECTOR_INTERNAL_TOKEN", "local_connector_token_change_me")
	mysql := datasource.NewPythonConnector(datasource.TypeMySQL, connectorURL, connectorToken, datasource.EnvSecretResolver{})
	oracle := datasource.NewPythonConnector(datasource.TypeOracle, connectorURL, connectorToken, datasource.EnvSecretResolver{})
	executor := runtimefederation.NewExecutor(map[datasource.Type]queryruntime.QueryConnector{
		datasource.TypeMySQL: mysql, datasource.TypeOracle: oracle,
	}, nil)

	orderTable := querycompiler.TableRef{
		NodeID: "orders", Schema: "REPORT_READER", Name: "ORDERS",
		Columns: map[string]bool{"CUSTOMER_ID": true, "AMOUNT": true}, ColumnTypes: map[string]string{"CUSTOMER_ID": "NUMBER", "AMOUNT": "DECIMAL"},
	}
	customerTable := querycompiler.TableRef{
		NodeID: "customers", Schema: "report_source", Name: "customers",
		Columns: map[string]bool{"customer_id": true, "region_code": true}, ColumnTypes: map[string]string{"customer_id": "NUMBER", "region_code": "STRING"},
	}
	plan := queryruntime.ResolvedPlan{
		SourceID: "source-oracle", SourceType: datasource.TypeOracle,
		Tables: map[string]querycompiler.TableRef{"orders": orderTable, "customers": customerTable},
		Nodes: map[string]queryruntime.ResolvedNode{
			"orders":    {NodeID: "orders", SourceID: "source-oracle", SourceType: datasource.TypeOracle, SourceVersion: 1, Table: orderTable},
			"customers": {NodeID: "customers", SourceID: "source-mysql", SourceType: datasource.TypeMySQL, SourceVersion: 1, Table: customerTable},
		},
	}
	quota := datasource.Quota{MaxConnectionsPerSource: 2, MaxConcurrentQueries: 4}
	sources := map[string]datasource.Source{
		"source-oracle": {ID: "source-oracle", TenantID: "source-it", Code: "orders", Name: "订单", Type: datasource.TypeOracle, Status: datasource.StatusActive, SecretRef: "env://SOURCE_IT_ORACLE_SECRET", RuntimeQuota: quota},
		"source-mysql":  {ID: "source-mysql", TenantID: "source-it", Code: "customers", Name: "客户", Type: datasource.TypeMySQL, Status: datasource.StatusActive, SecretRef: "env://SOURCE_IT_MYSQL_SECRET", RuntimeQuota: quota},
	}
	result, err := executor.Execute(ctx, "ad9f1efe-f4bf-41f3-ae01-e43af76ea778", formalFederatedDocument(), plan, sources, map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[0][0] != "CN-SH" || result.Rows[0][1] != 1500.75 || result.Rows[0][2] != int64(2) || result.Rows[0][3] != 750.375 || result.Rows[1][0] != "CN-BJ" || result.Rows[1][1] != 800.0 || result.Rows[1][2] != int64(1) || result.Rows[1][3] != 800.0 || len(result.Warnings) != 0 {
		t.Fatalf("formal federated result=%#v", result)
	}
	if len(result.SourceStats) != 2 || result.SourceStats[0].NodeID != "customers" || result.SourceStats[0].RowCount != 3 || result.SourceStats[1].NodeID != "orders" || result.SourceStats[1].RowCount != 2 {
		t.Fatalf("formal federated source stats=%#v", result.SourceStats)
	}
	for _, stat := range result.SourceStats {
		if stat.Status != "SUCCEEDED" || stat.SubqueryID == "" || stat.DurationMS < 0 {
			t.Fatalf("formal federated source stat=%#v", stat)
		}
	}
}

func TestSecureCompilerQueriesMySQLAndOracle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cases := []struct {
		name, schema, table, field string
		dialect                    querycompiler.Dialect
		connection                 map[string]any
	}{
		{name: "mysql", schema: "report_source", table: "customers", field: "customer_id", dialect: querycompiler.MySQL, connection: map[string]any{"source_type": "MYSQL", "host": "mysql", "port": 3306, "database": "report_source", "username": "report_reader", "password": "local_mysql_reader_password"}},
		{name: "oracle", schema: "REPORT_READER", table: "ORDERS", field: "ORDER_ID", dialect: querycompiler.Oracle, connection: map[string]any{"source_type": "ORACLE", "host": "oracle", "port": 1521, "database": "FREEPDB1", "username": "report_reader", "password": "local_oracle_reader_password"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			document := executableDocument(item.field)
			compiled, err := querycompiler.Compile(querycompiler.Input{
				Document: document, Dialect: item.dialect, MaxRows: 10, Parameters: map[string]any{"minimum_id": 1},
				Tables: map[string]querycompiler.TableRef{"source": {NodeID: "source", Schema: item.schema, Name: item.table, Columns: map[string]bool{item.field: true}}},
				Scope:  policy.UserScope{TenantID: "source-test", UserID: "source-test", Attributes: map[string]any{}},
			})
			if err != nil {
				t.Fatal(err)
			}
			out := query(t, ctx, item.connection, compiled.SQL, compiled.Args, compiled.MaxRows)
			if out.RowCount < 1 || out.RowCount > compiled.MaxRows {
				t.Fatalf("row count=%d max=%d", out.RowCount, compiled.MaxRows)
			}
		})
	}
}

func executableDocument(field string) dataset.Document {
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "secure_source", Name: "安全源查询", Type: "SINGLE_SOURCE"},
		Nodes:      []dataset.Node{{ID: "source", Type: "TABLE", DataSourceID: "source-id", TableID: "table-id", Alias: "s", Projection: []string{field}, SourceFilters: []dataset.SourceFilter{}}},
		Joins:      []dataset.Join{},
		Fields: []dataset.Field{{ID: "field_source_id", Code: "source_id", Name: "源标识", Role: "IDENTIFIER", CanonicalType: "INTEGER", Nullable: false,
			Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "source", Field: field}}},
		Filters: []dataset.Filter{{ID: "minimum_filter", Stage: "PRE_AGGREGATION", Expression: dataset.Expression{Type: "GTE", Left: &dataset.Expression{Type: "FIELD_REF", NodeID: "source", Field: field}, Right: &dataset.Expression{Type: "PARAM_REF", Code: "minimum_id"}}}},
		GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{{FieldID: "field_source_id", Direction: "ASC"}},
		Parameters:  []dataset.Parameter{{Code: "minimum_id", Name: "最小标识", DataType: "INTEGER", Required: true}},
		OutputGrain: dataset.OutputGrain{Description: "每行代表一条源记录", KeyFields: []string{"source_id"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 10, ResultLimit: 100,
			Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
}

func formalFederatedDocument() dataset.Document {
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "customer_revenue", Name: "客户收入", Type: "CROSS_SOURCE"},
		Nodes: []dataset.Node{
			{ID: "orders", Type: "TABLE", DataSourceID: "source-oracle", TableID: "table-orders", Alias: "o", Projection: []string{"CUSTOMER_ID", "AMOUNT"}, SourceFilters: []dataset.SourceFilter{}},
			{ID: "customers", Type: "TABLE", DataSourceID: "source-mysql", TableID: "table-customers", Alias: "c", Projection: []string{"customer_id", "region_code"}, SourceFilters: []dataset.SourceFilter{}},
		},
		Joins: []dataset.Join{{
			ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "INNER", Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "CUSTOMER_ID"},
				Operator:        "EQUALS",
				RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"},
			}},
		}},
		Fields: []dataset.Field{
			{ID: "field_region", Code: "region_code", Name: "区域", Role: "DIMENSION", CanonicalType: "STRING", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "region_code"}},
			{ID: "field_revenue", Code: "revenue", Name: "收入", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: dataset.Expression{Type: "AGGREGATE", Function: "SUM", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "AMOUNT"}}},
			{ID: "field_orders", Code: "order_count", Name: "订单数", Role: "MEASURE", CanonicalType: "INTEGER", Expression: dataset.Expression{Type: "AGGREGATE", Function: "COUNT", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "AMOUNT"}}},
			{ID: "field_average", Code: "average_amount", Name: "平均金额", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: dataset.Expression{Type: "AGGREGATE", Function: "AVG", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "AMOUNT"}}},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{"field_region"}, Having: []dataset.Filter{},
		Sorts:       []dataset.Sort{{FieldID: "field_revenue", Direction: "DESC"}},
		Parameters:  []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{Description: "每行一个区域", KeyFields: []string{"region_code"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 10000, PreviewLimit: 20, ResultLimit: 100,
			Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
}

func query(t *testing.T, ctx context.Context, connection map[string]any, sql string, parameters []any, maxRows int) result {
	t.Helper()
	if parameters == nil {
		parameters = []any{}
	}
	payload, _ := json.Marshal(map[string]any{"connection": connection, "sql": sql, "parameters": parameters, "max_rows": maxRows})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, env("CONNECTOR_SERVICE_URL", "http://127.0.0.1:8090")+"/v1/query", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Connector-Token", env("CONNECTOR_INTERNAL_TOKEN", "local_connector_token_change_me"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("connector query status=%s", response.Status)
	}
	var out result
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.RowCount == 0 {
		t.Fatal(fmt.Errorf("connector returned no rows"))
	}
	return out
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
