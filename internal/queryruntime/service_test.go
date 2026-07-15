package queryruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

type fakeDatasets struct{ record dataset.Record }

func (f fakeDatasets) Get(context.Context, string, string) (dataset.Record, error) {
	return f.record, nil
}

type fakeSources struct {
	source  datasource.Source
	sources map[string]datasource.Source
}

func (f fakeSources) Get(_ context.Context, _, sourceID string) (datasource.Source, error) {
	if source, ok := f.sources[sourceID]; ok {
		return source, nil
	}
	return f.source, nil
}
func (fakeSources) Quota(context.Context, string) (datasource.Quota, error) {
	return datasource.Quota{MaxConnectionsPerSource: 2, MaxConcurrentQueries: 3, MaxExcelFileBytes: 1 << 20}, nil
}

type fakePolicies struct{}

func (fakePolicies) Load(_ context.Context, tenantID, actorID, _, _ string) (policy.UserScope, []policy.RowPolicy, []policy.ColumnPolicy, error) {
	return policy.UserScope{TenantID: tenantID, UserID: actorID, Attributes: map[string]any{}}, nil, nil, nil
}

type fakeRuntimeStore struct {
	mu       sync.Mutex
	resolved ResolvedPlan
	run      RunRecord
	status   string
	error    string
	warnings []datasource.QueryWarning
	stats    []datasource.QuerySourceStat
}

func (f *fakeRuntimeStore) Resolve(context.Context, string, dataset.Document) (ResolvedPlan, error) {
	return f.resolved, nil
}
func (f *fakeRuntimeStore) Start(_ context.Context, run RunRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run, f.status = run, "RUNNING"
	return nil
}
func (f *fakeRuntimeStore) Finish(_ context.Context, _, _ string, status string, _ int, _ int64, errorCode string, warnings []datasource.QueryWarning, stats []datasource.QuerySourceStat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 模拟数据库 WHERE status='RUNNING'，确保取消状态不会被原请求覆盖。
	if f.status == "RUNNING" {
		f.status, f.error, f.warnings = status, errorCode, append([]datasource.QueryWarning(nil), warnings...)
		f.stats = append([]datasource.QuerySourceStat(nil), stats...)
	}
	return nil
}
func (f *fakeRuntimeStore) CancellableSources(_ context.Context, _, _, _, queryID string) ([]RunSourceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != "RUNNING" {
		return nil, dataset.ErrQueryNotFound
	}
	if len(f.run.Sources) > 0 {
		return append([]RunSourceRecord(nil), f.run.Sources...), nil
	}
	return []RunSourceRecord{{SourceID: f.resolved.SourceID, SourceType: f.resolved.SourceType, SubqueryID: queryID}}, nil
}

type fakeConnector struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
	query     func(context.Context, string, []any, int) (datasource.QueryResult, error)
}

type fakeFileExecutor struct {
	called    bool
	versionID string
	params    map[string]any
	result    datasource.QueryResult
	err       error
}

type fakeFederatedExecutor struct {
	called     bool
	queryID    string
	parameters map[string]any
	sources    map[string]datasource.Source
	result     datasource.QueryResult
	err        error
}

func (f *fakeFederatedExecutor) Execute(_ context.Context, queryID string, _ dataset.Document, _ ResolvedPlan, sources map[string]datasource.Source, parameters map[string]any, _ policy.UserScope, _ []policy.RowPolicy, _ []policy.ColumnPolicy, _ int) (datasource.QueryResult, error) {
	f.called, f.queryID, f.parameters, f.sources = true, queryID, parameters, sources
	return f.result, f.err
}

func (*fakeFederatedExecutor) Cancel(context.Context, string) (bool, error) { return true, nil }

func (f *fakeFileExecutor) Execute(_ context.Context, _ datasource.Source, _ string, _ dataset.Document, _ map[string]querycompiler.TableRef, versionID string, parameters map[string]any, _ policy.UserScope, _ []policy.RowPolicy, _ []policy.ColumnPolicy, _ int) (datasource.QueryResult, error) {
	f.called, f.versionID, f.params = true, versionID, parameters
	return f.result, f.err
}

func (*fakeFileExecutor) Cancel(context.Context, string) (bool, error) { return true, nil }

func (f *fakeConnector) Query(ctx context.Context, _ datasource.Source, _ string, sql string, args []any, maxRows int) (datasource.QueryResult, error) {
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	return f.query(ctx, sql, args, maxRows)
}
func (f *fakeConnector) Cancel(context.Context, string) (bool, error) {
	if f.cancelled != nil {
		select {
		case <-f.cancelled:
		default:
			close(f.cancelled)
		}
	}
	return true, nil
}

func runtimeFixture(t *testing.T, connector QueryConnector) (*Service, *fakeRuntimeStore) {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"timeoutMs": 5000`), []byte(`"timeoutMs": 100`), 1)
	store := &fakeRuntimeStore{resolved: ResolvedPlan{
		SourceID: "source-1", SourceType: datasource.TypeMySQL,
		Tables: map[string]querycompiler.TableRef{"orders": {NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"order_date": true, "order_amount": true, "order_status": true}}},
	}}
	service := NewService(
		fakeDatasets{record: dataset.Record{ID: "dataset-1", DraftVersionID: "version-1", DSL: raw}},
		fakeSources{source: datasource.Source{ID: "source-1", Type: datasource.TypeMySQL, Status: datasource.StatusActive}},
		fakePolicies{}, store, map[datasource.Type]QueryConnector{datasource.TypeMySQL: connector},
	)
	return service, store
}

func TestPreviewCompilesParametersAndCompletesAudit(t *testing.T) {
	connector := &fakeConnector{query: func(_ context.Context, sql string, args []any, maxRows int) (datasource.QueryResult, error) {
		if maxRows != 25 || len(args) != 2 || contains(sql, "2026-01-01") {
			t.Fatalf("unsafe query sql=%s args=%#v maxRows=%d", sql, args, maxRows)
		}
		return datasource.QueryResult{Columns: []string{"stat_month", "revenue"}, Rows: [][]any{{"2026-01-01", 12}}, RowCount: 1, DurationMS: 9}, nil
	}}
	service, store := runtimeFixture(t, connector)
	result, err := service.Preview(context.Background(), "tenant-1", "actor-1", "dataset-1", dataset.PreviewInput{Parameters: map[string]any{"start_date": "2026-01-01"}, MaxRows: 25})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.QueryID == "" || store.status != "SUCCEEDED" {
		t.Fatalf("result=%#v status=%s", result, store.status)
	}
	if len(store.run.PlanHash) != 64 || len(store.run.ParameterHash) != 64 {
		t.Fatalf("audit hashes are missing: %#v", store.run)
	}
}

func TestPreviewExecutesFixedFileVersionAndCompletesAudit(t *testing.T) {
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"alias": "o"`), []byte(`"fileVersionId": "33333333-3333-4333-8333-333333333333", "alias": "o"`), 1)
	store := &fakeRuntimeStore{resolved: ResolvedPlan{
		SourceID: "source-file", SourceType: datasource.TypeExcel, FileVersionID: "33333333-3333-4333-8333-333333333333",
		Tables: map[string]querycompiler.TableRef{"orders": {NodeID: "orders", Name: "orders", Columns: map[string]bool{"order_date": true, "order_amount": true, "order_status": true}}},
	}}
	files := &fakeFileExecutor{result: datasource.QueryResult{Columns: []string{"stat_month", "revenue"}, Rows: [][]any{{"2026-01-01", 20.0}}, RowCount: 1, DurationMS: 7}}
	service := NewService(
		fakeDatasets{record: dataset.Record{ID: "dataset-1", DraftVersionID: "version-1", PlanHash: "logical-plan", DSL: raw}},
		fakeSources{source: datasource.Source{ID: "source-file", TenantID: "tenant-1", FileAssetID: "asset-1", Type: datasource.TypeExcel, Status: datasource.StatusActive}},
		fakePolicies{}, store, nil, files,
	)
	result, err := service.Preview(context.Background(), "tenant-1", "actor-1", "dataset-1", dataset.PreviewInput{Parameters: map[string]any{"start_date": "2026-01-01"}, MaxRows: 25})
	if err != nil {
		t.Fatal(err)
	}
	if !files.called || files.versionID != store.resolved.FileVersionID || files.params["start_date"] != "2026-01-01" {
		t.Fatalf("file executor=%#v", files)
	}
	if result.RowCount != 1 || store.status != "SUCCEEDED" || len(store.run.PlanHash) != 64 || len(store.run.ParameterHash) != 64 {
		t.Fatalf("result=%#v run=%#v status=%s", result, store.run, store.status)
	}
}

func TestPreviewExecutesFederatedPlanAndAuditsEverySource(t *testing.T) {
	document := crossSourceRuntimeDocument()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	queryID := "7d309f00-912b-43da-b8ef-acf7984e0158"
	ordersTable := querycompiler.TableRef{NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"customer_id": true, "amount": true}}
	customersTable := querycompiler.TableRef{NodeID: "customers", Schema: "crm", Name: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true}}
	store := &fakeRuntimeStore{resolved: ResolvedPlan{
		SourceID: "source-orders", SourceType: datasource.TypeOracle,
		Tables: map[string]querycompiler.TableRef{"orders": ordersTable, "customers": customersTable},
		Nodes: map[string]ResolvedNode{
			"orders":    {NodeID: "orders", SourceID: "source-orders", SourceType: datasource.TypeOracle, SourceVersion: 4, Watermark: "2026-07-15T08:00:00Z", Table: ordersTable},
			"customers": {NodeID: "customers", SourceID: "source-customers", SourceType: datasource.TypeMySQL, SourceVersion: 7, Watermark: "2026-07-15T08:01:00Z", Table: customersTable},
		},
	}}
	federated := &fakeFederatedExecutor{result: datasource.QueryResult{
		Columns: []string{"customer_name", "revenue"}, Rows: [][]any{{"A", 20.0}}, RowCount: 1, DurationMS: 8,
		Warnings: []datasource.QueryWarning{{Code: "JOIN_FANOUT_RISK", Message: "关联结果可能发生扇出。", JoinID: "orders_customers", EstimatedRows: 4}},
		SourceStats: []datasource.QuerySourceStat{
			{NodeID: "customers", SubqueryID: FederatedSubqueryID(queryID, "customers"), RowCount: 1, DurationMS: 3, Status: "SUCCEEDED"},
			{NodeID: "orders", SubqueryID: FederatedSubqueryID(queryID, "orders"), RowCount: 2, DurationMS: 5, Status: "SUCCEEDED"},
		},
	}}
	service := NewService(
		fakeDatasets{record: dataset.Record{ID: "dataset-1", DraftVersionID: "version-1", PlanHash: "logical-plan", DSL: raw}},
		fakeSources{sources: map[string]datasource.Source{
			"source-orders":    {ID: "source-orders", Type: datasource.TypeOracle, Status: datasource.StatusActive},
			"source-customers": {ID: "source-customers", Type: datasource.TypeMySQL, Status: datasource.StatusActive},
		}},
		fakePolicies{}, store, nil,
	)
	service.SetFederatedExecutor(federated)
	result, err := service.Preview(context.Background(), "tenant-1", "actor-1", "dataset-1", dataset.PreviewInput{QueryID: queryID, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !federated.called || federated.queryID != queryID || len(federated.sources) != 2 || result.RowCount != 1 || store.status != "SUCCEEDED" {
		t.Fatalf("executor=%#v result=%#v status=%s", federated, result, store.status)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "JOIN_FANOUT_RISK" || result.Warnings[0].EstimatedRows != 4 {
		t.Fatalf("preview warnings=%#v", result.Warnings)
	}
	if len(store.warnings) != 1 || store.warnings[0].JoinID != "orders_customers" {
		t.Fatalf("audit warnings=%#v", store.warnings)
	}
	if len(store.stats) != 2 || store.stats[0].NodeID != "customers" || store.stats[1].RowCount != 2 {
		t.Fatalf("source stats=%#v", store.stats)
	}
	if len(store.run.Sources) != 2 || len(store.run.PlanHash) != 64 || len(store.run.ParameterHash) != 64 {
		t.Fatalf("run=%#v", store.run)
	}
	for _, source := range store.run.Sources {
		if source.SubqueryID != FederatedSubqueryID(queryID, source.NodeID) || source.SourceVersion == 0 || source.Watermark == "" {
			t.Fatalf("source audit=%#v", source)
		}
	}
}

func TestResolvedRunSourcesDoesNotDuplicateSingleSourceQueryID(t *testing.T) {
	resolved := ResolvedPlan{Nodes: map[string]ResolvedNode{
		"orders":    {NodeID: "orders", SourceID: "source-1", SourceType: datasource.TypeMySQL, SourceVersion: 1},
		"customers": {NodeID: "customers", SourceID: "source-1", SourceType: datasource.TypeMySQL, SourceVersion: 1},
	}}
	queryID := "7d309f00-912b-43da-b8ef-acf7984e0158"
	if sources := resolvedRunSources(queryID, "SINGLE_SOURCE", resolved); len(sources) != 0 {
		t.Fatalf("single-source audit should use query_runs fallback: %#v", sources)
	}
	sources := resolvedRunSources(queryID, "CROSS_SOURCE", resolved)
	if len(sources) != 2 || sources[0].SubqueryID == sources[1].SubqueryID {
		t.Fatalf("federated subquery audit=%#v", sources)
	}
}

func TestPreviewTimesOutAndRequestsConnectorCancellation(t *testing.T) {
	cancelled := make(chan struct{})
	connector := &fakeConnector{cancelled: cancelled, query: func(ctx context.Context, _ string, _ []any, _ int) (datasource.QueryResult, error) {
		<-ctx.Done()
		return datasource.QueryResult{}, ctx.Err()
	}}
	service, store := runtimeFixture(t, connector)
	_, err := service.Preview(context.Background(), "tenant-1", "actor-1", "dataset-1", dataset.PreviewInput{Parameters: map[string]any{"start_date": "2026-01-01"}})
	if !errors.Is(err, dataset.ErrPreviewTimeout) || store.status != "TIMEOUT" || store.error != "QUERY_TIMEOUT" {
		t.Fatalf("err=%v status=%s code=%s", err, store.status, store.error)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("connector cancellation was not requested")
	}
}

func TestCancelRequiresRunningOwnedQueryAndPreservesCancelledStatus(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	connector := &fakeConnector{started: started, cancelled: cancelled, query: func(context.Context, string, []any, int) (datasource.QueryResult, error) {
		<-cancelled
		return datasource.QueryResult{}, errors.New("cancelled")
	}}
	service, store := runtimeFixture(t, connector)
	queryID := "d7567ac1-dd36-4d16-aac4-65d48d491d74"
	finished := make(chan error, 1)
	go func() {
		_, err := service.Preview(context.Background(), "tenant-1", "actor-1", "dataset-1", dataset.PreviewInput{QueryID: queryID, Parameters: map[string]any{"start_date": "2026-01-01"}})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preview did not start")
	}
	if err := service.Cancel(context.Background(), "tenant-1", "actor-1", "dataset-1", queryID); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, dataset.ErrPreviewFailed) {
		t.Fatalf("preview error = %v", err)
	}
	if store.status != "CANCELLED" || store.error != "QUERY_CANCELLED" {
		t.Fatalf("status=%s code=%s", store.status, store.error)
	}
	if err := service.Cancel(context.Background(), "tenant-1", "actor-1", "dataset-1", queryID); !errors.Is(err, dataset.ErrQueryNotFound) {
		t.Fatalf("second cancel error = %v", err)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func crossSourceRuntimeDocument() dataset.Document {
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "customer_revenue", Name: "客户收入", Type: "CROSS_SOURCE"},
		Nodes: []dataset.Node{
			{ID: "orders", Type: "TABLE", DataSourceID: "source-orders", TableID: "table-orders", Alias: "o", Projection: []string{"customer_id", "amount"}, SourceFilters: []dataset.SourceFilter{}},
			{ID: "customers", Type: "TABLE", DataSourceID: "source-customers", TableID: "table-customers", Alias: "c", Projection: []string{"customer_id", "customer_name"}, SourceFilters: []dataset.SourceFilter{}},
		},
		Joins: []dataset.Join{{
			ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "INNER", Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
			Conditions: []dataset.JoinCondition{{
				LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "customer_id"},
				Operator:        "EQUALS",
				RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"},
			}},
		}},
		Fields: []dataset.Field{
			{ID: "field_customer", Code: "customer_name", Name: "客户", Role: "DIMENSION", CanonicalType: "STRING", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_name"}},
			{ID: "field_revenue", Code: "revenue", Name: "收入", Role: "MEASURE", CanonicalType: "DECIMAL", Expression: dataset.Expression{Type: "AGGREGATE", Function: "SUM", Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}}},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{"field_customer"}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{Description: "每行一个客户", KeyFields: []string{"customer_name"}},
		ExecutionPolicy: dataset.ExecutionPolicy{Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000,
			Materialization: dataset.MaterializationPolicy{Enabled: false}},
	}
}
