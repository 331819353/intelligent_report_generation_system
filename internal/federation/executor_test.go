package federation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/queryruntime"
)

type fakeConnector struct {
	mu        sync.Mutex
	queries   []string
	result    datasource.QueryResult
	err       error
	wait      bool
	started   chan struct{}
	cancelled []string
}

func (f *fakeConnector) Query(ctx context.Context, _ datasource.Source, _ string, sql string, _ []any, _ int) (datasource.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, sql)
	f.mu.Unlock()
	if f.wait {
		if f.started != nil {
			select {
			case f.started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return datasource.QueryResult{}, ctx.Err()
	}
	if f.err != nil {
		return datasource.QueryResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeConnector) Cancel(_ context.Context, queryID string) (bool, error) {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, queryID)
	f.mu.Unlock()
	return true, nil
}

type fakeVersionReader struct {
	versions map[string]datasource.FileVersion
	tables   map[string][]datasource.FileTableData
}

func (f fakeVersionReader) ReadVersionTables(_ context.Context, _, versionID string, _ int64) (datasource.FileVersion, []datasource.FileTableData, error) {
	version, ok := f.versions[versionID]
	if !ok {
		return datasource.FileVersion{}, nil, errors.New("unknown file version")
	}
	return version, f.tables[versionID], nil
}

func crossDocument() dataset.Document {
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "customer_revenue", Name: "客户收入", Type: "CROSS_SOURCE"},
		Nodes: []dataset.Node{
			{ID: "orders", Type: "TABLE", DataSourceID: "source-orders", TableID: "table-orders", Alias: "o", Projection: []string{"customer_id", "amount", "unused_order"}, SourceFilters: []dataset.SourceFilter{}},
			{ID: "customers", Type: "TABLE", DataSourceID: "source-customers", TableID: "table-customers", Alias: "c", Projection: []string{"customer_id", "customer_name", "unused_customer"}, SourceFilters: []dataset.SourceFilter{}},
		},
		Joins: []dataset.Join{{
			ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "INNER", Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "customer_id"}, Operator: "EQUALS",
				RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"},
			}},
		}},
		Fields: []dataset.Field{
			{ID: "field_customer", Code: "customer_name", Name: "客户", Role: "DIMENSION", CanonicalType: "STRING", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}},
			{ID: "field_revenue", Code: "revenue", Name: "收入", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: dataset.Expression{Type: "AGGREGATE", Function: "SUM", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}}},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{"field_customer"}, Having: []dataset.Filter{},
		Sorts:       []dataset.Sort{{FieldID: "field_revenue", Direction: "DESC"}},
		Parameters:  []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{Description: "每行一个客户", KeyFields: []string{"customer_name"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000,
			Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
}

func crossPlan(orderType, customerType datasource.Type) queryruntime.ResolvedPlan {
	orderTable := querycompiler.TableRef{NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"customer_id": true, "amount": true, "unused_order": true}, ColumnTypes: map[string]string{"customer_id": "NUMBER", "amount": "DECIMAL", "unused_order": "TEXT"}}
	customerTable := querycompiler.TableRef{NodeID: "customers", Schema: "crm", Name: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true, "unused_customer": true}, ColumnTypes: map[string]string{"customer_id": "NUMBER", "customer_name": "TEXT", "unused_customer": "TEXT"}}
	return queryruntime.ResolvedPlan{
		SourceID: "source-orders", SourceType: orderType,
		Tables: map[string]querycompiler.TableRef{"orders": orderTable, "customers": customerTable},
		Nodes: map[string]queryruntime.ResolvedNode{
			"orders":    {NodeID: "orders", SourceID: "source-orders", SourceType: orderType, SourceVersion: 1, FileVersionID: fileVersion(orderType, "orders"), Table: orderTable},
			"customers": {NodeID: "customers", SourceID: "source-customers", SourceType: customerType, SourceVersion: 1, FileVersionID: fileVersion(customerType, "customers"), Table: customerTable},
		},
	}
}

func fileVersion(sourceType datasource.Type, suffix string) string {
	if sourceType == datasource.TypeExcel {
		return "version-" + suffix
	}
	return ""
}

func crossSources(orderType, customerType datasource.Type) map[string]datasource.Source {
	return map[string]datasource.Source{
		"source-orders":    {ID: "source-orders", TenantID: "tenant-1", Type: orderType, Status: datasource.StatusActive, FileAssetID: "asset-orders", RuntimeQuota: datasource.Quota{MaxExcelFileBytes: 1 << 20}},
		"source-customers": {ID: "source-customers", TenantID: "tenant-1", Type: customerType, Status: datasource.StatusActive, FileAssetID: "asset-customers", RuntimeQuota: datasource.Quota{MaxExcelFileBytes: 1 << 20}},
	}
}

func fileReader() fakeVersionReader {
	return fakeVersionReader{
		versions: map[string]datasource.FileVersion{
			"version-orders":    {FileAsset: datasource.FileAsset{ID: "asset-orders", VersionID: "version-orders"}},
			"version-customers": {FileAsset: datasource.FileAsset{ID: "asset-customers", VersionID: "version-customers"}},
		},
		tables: map[string][]datasource.FileTableData{
			"version-orders":    {{Name: "orders", Columns: []string{"customer_id", "amount", "unused_order"}, Types: map[string]string{"customer_id": "NUMBER", "amount": "DECIMAL", "unused_order": "TEXT"}, Rows: [][]string{{"1", "12.5", "x"}, {"1", "7.5", "y"}, {"2", "3", "z"}}}},
			"version-customers": {{Name: "customers", Columns: []string{"customer_id", "customer_name", "unused_customer"}, Types: map[string]string{"customer_id": "NUMBER", "customer_name": "TEXT", "unused_customer": "TEXT"}, Rows: [][]string{{"1", "A", "x"}, {"2", "B", "y"}}}},
		},
	}
}

func TestExecutorJoinsDatabaseSourcesAndAppliesPolicies(t *testing.T) {
	// 数据库多侧已按 Join 键返回部分和，网关仍需完成跨源 Join 和最终归并。
	orders := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "partial_sum_1"}, Rows: [][]any{{1.0, 20.0}, {2.0, 3.0}}, RowCount: 2}}
	customers := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "customer_name"}, Rows: [][]any{{1.0, "A"}, {2.0, "B"}}, RowCount: 2}}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	result, err := executor.Execute(context.Background(), "79d39206-c899-40f8-866c-5ad933b035f2", crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{},
		policy.UserScope{Attributes: map[string]any{"customer": "A"}},
		[]policy.RowPolicy{{ID: "customer", Effect: "ALLOW", CombineMode: "AND", Expression: policy.Expression{Type: "EQUALS", Left: &policy.Expression{Type: "FIELD_REF", FieldCode: "customer_name"}, Right: &policy.Expression{Type: "USER_ATTRIBUTE_REF", Attribute: "customer"}}}},
		[]policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "NULLIFY"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.Rows[0][0] != "A" || result.Rows[0][1] != nil {
		t.Fatalf("result=%#v", result)
	}
	if len(result.SourceStats) != 2 || result.SourceStats[0].NodeID != "customers" || result.SourceStats[0].RowCount != 2 || result.SourceStats[1].NodeID != "orders" || result.SourceStats[1].RowCount != 2 {
		t.Fatalf("source stats=%#v", result.SourceStats)
	}
	for _, stat := range result.SourceStats {
		if stat.Status != "SUCCEEDED" || stat.SubqueryID == "" || stat.DurationMS < 0 {
			t.Fatalf("source stat=%#v", stat)
		}
	}
	if strings.Contains(orders.queries[0], "unused_order") || strings.Contains(customers.queries[0], "unused_customer") {
		t.Fatalf("projection was not pruned: %s / %s", orders.queries[0], customers.queries[0])
	}
	if !strings.Contains(orders.queries[0], `SUM("O"."AMOUNT") AS "PARTIAL_SUM_1"`) || !strings.Contains(orders.queries[0], `GROUP BY "O"."CUSTOMER_ID"`) || strings.Contains(customers.queries[0], "GROUP BY") {
		t.Fatalf("aggregate pushdown SQL is invalid: %s / %s", orders.queries[0], customers.queries[0])
	}
}

func TestExecutorSupportsDatabaseExcelAndExcelExcel(t *testing.T) {
	cases := []struct {
		name                    string
		orderType, customerType datasource.Type
	}{
		{name: "database_excel", orderType: datasource.TypeOracle, customerType: datasource.TypeExcel},
		{name: "excel_excel", orderType: datasource.TypeExcel, customerType: datasource.TypeExcel},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			orders := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "partial_sum_1"}, Rows: [][]any{{1.0, 20.0}, {2.0, 3.0}}, RowCount: 2}}
			executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders}, fileReader())
			result, err := executor.Execute(context.Background(), "79d39206-c899-40f8-866c-5ad933b035f2", crossDocument(), crossPlan(item.orderType, item.customerType), crossSources(item.orderType, item.customerType), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			if result.RowCount != 2 || result.Rows[0][0] != "A" || result.Rows[0][1] != 20.0 {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestExecutorMergesPartialCountAndWeightedAverage(t *testing.T) {
	document := crossDocument()
	document.Fields = append(document.Fields, dataset.Field{
		ID: "field_count", Code: "order_count", Name: "订单数", Role: "MEASURE", CanonicalType: "INTEGER",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "COUNT", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}},
	}, dataset.Field{
		ID: "field_average", Code: "average_amount", Name: "平均金额", Role: "MEASURE", CanonicalType: "DECIMAL",
		Expression: dataset.Expression{Type: "AGGREGATE", Function: "AVG", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}},
	})
	orders := &fakeConnector{result: datasource.QueryResult{
		Columns: []string{"customer_id", "partial_sum_1", "partial_count_2"},
		Rows:    [][]any{{1.0, 20.0, 2.0}, {2.0, nil, 0.0}}, RowCount: 2,
	}}
	customers := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "customer_name"}, Rows: [][]any{{1.0, "A"}, {2.0, "B"}}, RowCount: 2}}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	result, err := executor.Execute(context.Background(), "79d39206-c899-40f8-866c-5ad933b035f2", document, crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[0][2] != int64(2) || result.Rows[0][3] != 10.0 || result.Rows[1][2] != int64(0) || result.Rows[1][3] != nil {
		t.Fatalf("partial aggregate result=%#v", result)
	}
	if !strings.Contains(orders.queries[0], `COUNT("O"."AMOUNT") AS "PARTIAL_COUNT_2"`) {
		t.Fatalf("partial aggregate SQL=%s", orders.queries[0])
	}
}

func TestExecutorCancelsAllDatabaseSubqueries(t *testing.T) {
	started := make(chan struct{}, 2)
	orders := &fakeConnector{wait: true, started: started}
	customers := &fakeConnector{wait: true, started: started}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	queryID := "79d39206-c899-40f8-866c-5ad933b035f2"
	finished := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), queryID, crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
		finished <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("database subquery did not start")
		}
	}
	cancelled, err := executor.Cancel(context.Background(), queryID)
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error=%v", err)
	}
	if len(orders.cancelled) != 1 || len(customers.cancelled) != 1 {
		t.Fatalf("cancelled orders=%#v customers=%#v", orders.cancelled, customers.cancelled)
	}
}

func TestExecutorCancelsRemoteSubqueriesWhenParentTimesOut(t *testing.T) {
	started := make(chan struct{}, 2)
	orders := &fakeConnector{wait: true, started: started}
	customers := &fakeConnector{wait: true, started: started}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := executor.Execute(ctx, "79d39206-c899-40f8-866c-5ad933b035f2", crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execute error=%v", err)
	}
	if len(orders.cancelled) != 1 || len(customers.cancelled) != 1 {
		t.Fatalf("cancelled orders=%#v customers=%#v", orders.cancelled, customers.cancelled)
	}
}

func TestExecutorFailsClosedWhenSourceRowsExceedLimit(t *testing.T) {
	rows := make([][]any, sourceRowLimit(10)+1)
	for index := range rows {
		rows[index] = []any{1.0, 1.0}
	}
	orders := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "amount"}, Rows: rows, RowCount: len(rows)}}
	customers := &fakeConnector{result: datasource.QueryResult{Columns: []string{"customer_id", "customer_name"}, Rows: [][]any{{1.0, "A"}}, RowCount: 1}}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	_, err := executor.Execute(context.Background(), "79d39206-c899-40f8-866c-5ad933b035f2", crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
	if !errors.Is(err, ErrSourceRowLimit) {
		t.Fatalf("execute error=%v, want ErrSourceRowLimit", err)
	}
}

func TestExecutorReturnsFailedAndCancelledSourceStats(t *testing.T) {
	started := make(chan struct{}, 1)
	orders := &fakeConnector{err: errors.New("source query failed")}
	customers := &fakeConnector{wait: true, started: started}
	executor := NewExecutor(map[datasource.Type]queryruntime.QueryConnector{datasource.TypeOracle: orders, datasource.TypeMySQL: customers}, fileReader())
	result, err := executor.Execute(context.Background(), "79d39206-c899-40f8-866c-5ad933b035f2", crossDocument(), crossPlan(datasource.TypeOracle, datasource.TypeMySQL), crossSources(datasource.TypeOracle, datasource.TypeMySQL), map[string]any{}, policy.UserScope{Attributes: map[string]any{}}, nil, nil, 10)
	if err == nil || len(result.SourceStats) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	statuses := map[string]string{}
	for _, stat := range result.SourceStats {
		statuses[stat.NodeID] = stat.Status
	}
	if statuses["orders"] != "FAILED" || statuses["customers"] != "CANCELLED" {
		t.Fatalf("source statuses=%#v", statuses)
	}
}
