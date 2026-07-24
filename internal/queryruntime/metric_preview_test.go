package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

type metricPreviewPolicies struct {
	rows    []policy.RowPolicy
	columns []policy.ColumnPolicy
}

func (f metricPreviewPolicies) Load(_ context.Context, tenantID, actorID, _, _ string) (policy.UserScope, []policy.RowPolicy, []policy.ColumnPolicy, error) {
	return policy.UserScope{TenantID: tenantID, UserID: actorID, Attributes: map[string]any{}}, f.rows, f.columns, nil
}

func (metricPreviewPolicies) ValidateDefinitions(context.Context, string, string, string, []string) error {
	return nil
}

func TestPreviewMetricExecutesDerivedExactVersionAndAuditsMetricIdentity(t *testing.T) {
	original := singleSourceJoinRuntimeDocument()
	derived := derivedMetricDocument(t, original)
	connectorCalls := 0
	connector := &fakeConnector{query: func(_ context.Context, sql string, _ []any, maxRows int) (datasource.QueryResult, error) {
		connectorCalls++
		if maxRows != 7 || !contains(sql, "metric_revenue") {
			t.Fatalf("指标派生 SQL 或结果上限异常: sql=%s maxRows=%d", sql, maxRows)
		}
		return datasource.QueryResult{Columns: []string{"metric_revenue"}, Rows: [][]any{{"120.50"}}, RowCount: 1}, nil
	}}
	service, store := metricPreviewRuntimeFixture(t, original, metricJoinResolvedPlan(), metricPreviewPolicies{}, connector)
	candidate := metricQueryCandidate(t, "dataset-version-1", derived)

	result, err := service.PreviewMetric(context.Background(), "tenant-1", "actor-1", candidate, dataset.PreviewInput{MaxRows: 7}, false)
	if err != nil {
		t.Fatal(err)
	}
	if connectorCalls != 1 || result.RowCount != 1 || store.status != "SUCCEEDED" {
		t.Fatalf("calls=%d result=%#v status=%s", connectorCalls, result, store.status)
	}
	if store.resolveVersionCalls != 1 || store.resolvedDatasetID != "dataset-1" || store.resolvedVersionID != "dataset-version-1" {
		t.Fatalf("精确数据集版本解析异常: store=%#v", store)
	}
	if store.run.DatasetID != "dataset-1" || store.run.DatasetVersionID != "dataset-version-1" ||
		store.run.MetricID != "metric-1" || store.run.MetricVersionID != "metric-version-1" || store.run.RunType != "PREVIEW" {
		t.Fatalf("指标查询审计身份不完整: %#v", store.run)
	}
}

func TestPreviewMetricRejectsTamperedSourceEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dataset.Document)
	}{
		{name: "节点", mutate: func(document *dataset.Document) { document.Nodes[0].Alias = "orders_changed" }},
		{name: "投影", mutate: func(document *dataset.Document) {
			document.Nodes[0].Projection = append(document.Nodes[0].Projection, "order_date")
		}},
		{name: "Join", mutate: func(document *dataset.Document) { document.Joins[0].JoinType = "LEFT" }},
		{name: "过滤", mutate: func(document *dataset.Document) {
			left := dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}
			right := dataset.Expression{Type: "LITERAL", Value: "0"}
			document.Filters = append(document.Filters, dataset.Filter{
				ID: "positive_amount", Stage: "PRE_AGGREGATION",
				Expression: dataset.Expression{Type: "GT", Left: &left, Right: &right},
			})
		}},
		{name: "参数", mutate: func(document *dataset.Document) {
			document.Parameters = append(document.Parameters, dataset.Parameter{Code: "region", Name: "地区", DataType: "STRING"})
		}},
		{name: "执行限额", mutate: func(document *dataset.Document) { document.ExecutionPolicy.PreviewLimit++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := singleSourceJoinRuntimeDocument()
			derived := derivedMetricDocument(t, original)
			test.mutate(&derived)
			connectorCalled := false
			service, store := metricPreviewRuntimeFixture(t, original, metricJoinResolvedPlan(), metricPreviewPolicies{}, &fakeConnector{query: func(context.Context, string, []any, int) (datasource.QueryResult, error) {
				connectorCalled = true
				return datasource.QueryResult{}, nil
			}})

			_, err := service.PreviewMetric(context.Background(), "tenant-1", "actor-1", metricQueryCandidate(t, "dataset-version-1", derived), dataset.PreviewInput{MaxRows: 1}, false)
			if !errors.Is(err, dataset.ErrPreviewInvalid) || connectorCalled || store.resolveVersionCalls != 0 || store.run.ID != "" {
				t.Fatalf("error=%v connectorCalled=%v resolveCalls=%d run=%#v", err, connectorCalled, store.resolveVersionCalls, store.run)
			}
		})
	}
}

func TestPreviewMetricFailsClosedForUnsupportedPolicyBoundary(t *testing.T) {
	singleSource := metricSingleSourceDocument(t)
	tests := []struct {
		name     string
		document dataset.Document
		resolved ResolvedPlan
		policies metricPreviewPolicies
	}{
		{
			name: "行级策略", document: singleSource, resolved: metricSingleSourceResolvedPlan(),
			policies: metricPreviewPolicies{rows: []policy.RowPolicy{{ID: "row-policy"}}},
		},
		{
			name: "列级策略", document: singleSource, resolved: metricSingleSourceResolvedPlan(),
			policies: metricPreviewPolicies{columns: []policy.ColumnPolicy{{FieldCode: "revenue", PolicyType: "DENY"}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connectorCalled := false
			service, store := metricPreviewRuntimeFixture(t, test.document, test.resolved, test.policies, &fakeConnector{query: func(context.Context, string, []any, int) (datasource.QueryResult, error) {
				connectorCalled = true
				return datasource.QueryResult{}, nil
			}})
			candidate := metricQueryCandidate(t, "dataset-version-1", derivedMetricDocument(t, test.document))

			_, err := service.PreviewMetric(context.Background(), "tenant-1", "actor-1", candidate, dataset.PreviewInput{
				Parameters: map[string]any{"start_date": "2026-01-01"}, MaxRows: 1,
			}, false)
			if !errors.Is(err, dataset.ErrPreviewUnsupported) || connectorCalled || store.run.ID != "" {
				t.Fatalf("error=%v connectorCalled=%v run=%#v", err, connectorCalled, store.run)
			}
		})
	}
}

func TestPreviewMetricExecutesCrossSourceExactVersionThroughFederation(t *testing.T) {
	original := crossSourceRuntimeDocument()
	derived := derivedMetricDocument(t, original)
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	ordersTable := querycompiler.TableRef{
		NodeID: "orders", Schema: "sales", Name: "orders",
		Columns: map[string]bool{"customer_id": true, "amount": true},
	}
	customersTable := querycompiler.TableRef{
		NodeID: "customers", Schema: "crm", Name: "customers",
		Columns: map[string]bool{"customer_id": true, "customer_name": true},
	}
	store := &fakeRuntimeStore{resolved: ResolvedPlan{
		SourceID: "source-orders", SourceType: datasource.TypeOracle,
		Tables: map[string]querycompiler.TableRef{
			"orders": ordersTable, "customers": customersTable,
		},
		Nodes: map[string]ResolvedNode{
			"orders": {
				NodeID: "orders", SourceID: "source-orders", SourceType: datasource.TypeOracle,
				SourceVersion: 2, Watermark: "2026-07-23T08:00:00Z", Table: ordersTable,
			},
			"customers": {
				NodeID: "customers", SourceID: "source-customers", SourceType: datasource.TypeMySQL,
				SourceVersion: 3, Watermark: "2026-07-23T08:01:00Z", Table: customersTable,
			},
		},
	}}
	federated := &fakeFederatedExecutor{result: datasource.QueryResult{
		Columns: []string{"metric_revenue"}, Rows: [][]any{{"120.50"}}, RowCount: 1,
	}}
	service := NewService(
		fakeDatasets{record: dataset.Record{
			ID: "dataset-1", DraftVersionID: "dataset-version-1", PlanHash: "dataset-plan", DSL: raw,
		}},
		fakeSources{sources: map[string]datasource.Source{
			"source-orders": {
				ID: "source-orders", Type: datasource.TypeOracle, Status: datasource.StatusActive,
			},
			"source-customers": {
				ID: "source-customers", Type: datasource.TypeMySQL, Status: datasource.StatusActive,
			},
		}},
		metricPreviewPolicies{}, store, nil,
	)
	service.SetFederatedExecutor(federated)
	candidate := metricQueryCandidate(t, "dataset-version-1", derived)

	result, err := service.PreviewMetric(
		context.Background(), "tenant-1", "actor-1", candidate,
		dataset.PreviewInput{MaxRows: 1}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !federated.called || len(federated.sources) != 2 || result.RowCount != 1 ||
		store.resolveVersionCalls != 1 || store.status != "SUCCEEDED" {
		t.Fatalf("federated=%#v result=%#v store=%#v", federated, result, store)
	}
	if store.run.RunType != "VALIDATION" || store.run.MetricID != candidate.MetricID ||
		store.run.MetricVersionID != candidate.MetricVersionID || len(store.run.Sources) != 2 {
		t.Fatalf("metric federated audit=%#v", store.run)
	}
}

func TestPreviewMetricValidationUsesPublicationRunType(t *testing.T) {
	original := metricSingleSourceDocument(t)
	connectorCalls := 0
	service, store := metricPreviewRuntimeFixture(t, original, metricSingleSourceResolvedPlan(), metricPreviewPolicies{}, &fakeConnector{query: func(_ context.Context, _ string, _ []any, maxRows int) (datasource.QueryResult, error) {
		connectorCalls++
		if maxRows != 1 {
			t.Fatalf("发布试算必须限制为一行，实际为 %d", maxRows)
		}
		return datasource.QueryResult{Columns: []string{"metric_revenue"}, Rows: [][]any{{"120.50"}}, RowCount: 1}, nil
	}})
	candidate := metricQueryCandidate(t, "dataset-version-1", derivedMetricDocument(t, original))

	result, err := service.PreviewMetric(context.Background(), "tenant-1", "actor-1", candidate, dataset.PreviewInput{
		Parameters: map[string]any{"start_date": "2026-01-01"}, MaxRows: 1,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if connectorCalls != 1 || result.RowCount != 1 || store.run.RunType != "VALIDATION" ||
		store.run.MetricID != candidate.MetricID || store.run.MetricVersionID != candidate.MetricVersionID {
		t.Fatalf("calls=%d result=%#v run=%#v", connectorCalls, result, store.run)
	}
}

func TestPreviewDWSMetricReadsExactActiveMaterialization(t *testing.T) {
	original := metricSingleSourceDocument(t)
	original.Parameters = []dataset.Parameter{}
	original.Filters = []dataset.Filter{}
	for index := range original.Nodes {
		original.Nodes[index].SourceFilters = []dataset.SourceFilter{}
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	binding := ResolvedMaterialization{
		NodeID:    materializedMetricNodeID,
		DatasetID: "dataset-1", DatasetVersionID: "dataset-version-1",
		MaterializationID: "33333333-3333-4333-8333-333333333333",
		Layer:             "DWS", PublishedSchema: "warehouse_published",
		PublishedName: "dws_t111111111111_d222222222222",
		SchemaHash:    strings.Repeat("a", 64), SnapshotHash: strings.Repeat("b", 64),
	}
	store := &fakeRuntimeStore{resolved: ResolvedPlan{
		Engine: ExecutionPostgreSQL,
		Tables: map[string]querycompiler.TableRef{
			materializedMetricNodeID: {
				NodeID: materializedMetricNodeID,
				Schema: binding.PublishedSchema, Name: binding.PublishedName,
				Columns: map[string]bool{"stat_month": true, "revenue": true},
			},
		},
		Materializations: []ResolvedMaterialization{binding},
	}}
	warehouse := &fakeWarehouseExecutor{result: datasource.QueryResult{
		Columns: []string{"metric_revenue"},
		Rows:    [][]any{{"120.50"}}, RowCount: 1,
	}}
	service := NewService(
		fakeDatasets{record: dataset.Record{
			ID: "dataset-1", DraftVersionID: "dataset-version-1",
			Layer: dataset.LayerDWS, PlanHash: "dataset-plan", DSL: raw,
		}},
		fakeSources{}, metricPreviewPolicies{}, store, nil,
	)
	service.SetWarehouseExecutor(warehouse)

	candidate := metricQueryCandidate(
		t, "dataset-version-1", derivedMetricDocument(t, original),
	)
	result, err := service.PreviewMetric(
		context.Background(), "tenant-1", "actor-1", candidate,
		dataset.PreviewInput{MaxRows: 1}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !warehouse.called ||
		store.resolveVersionCalls != 0 || store.resolveMaterializedCalls != 1 ||
		store.run.ExecutionEngine != ExecutionPostgreSQL ||
		len(store.run.Materializations) != 1 {
		t.Fatalf("result=%#v warehouse=%#v store=%#v", result, warehouse, store)
	}
	document := warehouse.document
	if len(document.Nodes) != 1 ||
		document.Nodes[0].DatasetVersionID != "dataset-version-1" ||
		len(document.Fields) != 1 ||
		document.Fields[0].Expression.Type != "AGGREGATE" ||
		document.Fields[0].Expression.Function != "SUM" ||
		document.Fields[0].Expression.Argument == nil ||
		document.Fields[0].Expression.Argument.NodeID != materializedMetricNodeID ||
		document.Fields[0].Expression.Argument.Field != "revenue" {
		t.Fatalf("materialized metric document=%#v", document)
	}
}

func TestPreviewDWSMetricRejectsNonDecomposableMaterializedMeasure(t *testing.T) {
	original := metricSingleSourceDocument(t)
	original.Parameters = []dataset.Parameter{}
	original.Filters = []dataset.Filter{}
	for index := range original.Nodes {
		original.Nodes[index].SourceFilters = []dataset.SourceFilter{}
	}
	original.Fields[1].Expression.Function = "AVG"
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeRuntimeStore{}
	service := NewService(
		fakeDatasets{record: dataset.Record{
			ID: "dataset-1", DraftVersionID: "dataset-version-1",
			Layer: dataset.LayerDWS, DSL: raw,
		}},
		fakeSources{}, metricPreviewPolicies{}, store, nil,
	)
	candidate := metricQueryCandidate(
		t, "dataset-version-1", derivedMetricDocument(t, original),
	)
	_, err = service.PreviewMetric(
		context.Background(), "tenant-1", "actor-1", candidate,
		dataset.PreviewInput{MaxRows: 1}, false,
	)
	if !errors.Is(err, dataset.ErrPreviewUnsupported) ||
		store.resolveMaterializedCalls != 0 || store.run.ID != "" {
		t.Fatalf("error=%v store=%#v", err, store)
	}
}

func metricPreviewRuntimeFixture(t *testing.T, document dataset.Document, resolved ResolvedPlan, policies PolicyStore, connector QueryConnector) (*Service, *fakeRuntimeStore) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeRuntimeStore{resolved: resolved}
	connectors := map[datasource.Type]QueryConnector{}
	if connector != nil {
		connectors[datasource.TypeMySQL] = connector
		connectors[datasource.TypeOracle] = connector
	}
	service := NewService(
		fakeDatasets{record: dataset.Record{ID: "dataset-1", DraftVersionID: "dataset-version-1", PlanHash: "dataset-plan", DSL: raw}},
		fakeSources{source: datasource.Source{ID: resolved.SourceID, Type: resolved.SourceType, Status: datasource.StatusActive}},
		policies, store, connectors,
	)
	return service, store
}

func metricQueryCandidate(t *testing.T, datasetVersionID string, document dataset.Document) metric.QueryCandidate {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return metric.QueryCandidate{
		MetricID: "metric-1", MetricVersionID: "metric-version-1", DatasetID: "dataset-1", DatasetVersionID: datasetVersionID,
		DSL: prepared.DSLJSON, PlanHash: prepared.PlanHash,
	}
}

func derivedMetricDocument(t *testing.T, original dataset.Document) dataset.Document {
	t.Helper()
	// 派生计划必须拥有独立切片，避免测试篡改候选时同步改写作为可信基线的原文档。
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := dataset.DecodeAndNormalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	var metricField dataset.Field
	for _, field := range derived.Fields {
		if field.Role == "MEASURE" {
			metricField = field
			break
		}
	}
	if metricField.ID == "" {
		t.Fatal("测试数据集缺少可派生的度量字段")
	}
	metricField.ID, metricField.Code, metricField.Name = "metric_value", "metric_revenue", "指标收入"
	derived.Fields = []dataset.Field{metricField}
	derived.GroupBy = []string{}
	derived.Having = []dataset.Filter{}
	derived.Sorts = []dataset.Sort{}
	derived.OutputGrain = dataset.OutputGrain{Description: "指标试算结果", KeyFields: []string{metricField.Code}}
	return derived
}

func metricSingleSourceDocument(t *testing.T) dataset.Document {
	t.Helper()
	document, err := dataset.DecodeAndNormalize(storeDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func metricSingleSourceResolvedPlan() ResolvedPlan {
	return ResolvedPlan{
		SourceID: "source-1", SourceType: datasource.TypeMySQL,
		Tables: map[string]querycompiler.TableRef{
			"orders": {NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{
				"order_date": true, "order_amount": true, "order_status": true,
			}},
		},
	}
}

func metricJoinResolvedPlan() ResolvedPlan {
	return ResolvedPlan{
		SourceID: "source-1", SourceType: datasource.TypeMySQL,
		Tables: map[string]querycompiler.TableRef{
			"orders":    {NodeID: "orders", Schema: "sales", Name: "orders", Columns: map[string]bool{"customer_id": true, "amount": true}},
			"customers": {NodeID: "customers", Schema: "sales", Name: "customers", Columns: map[string]bool{"customer_id": true, "customer_name": true}},
		},
	}
}
