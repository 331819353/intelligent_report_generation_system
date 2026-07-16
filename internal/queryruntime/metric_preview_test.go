package queryruntime

import (
	"context"
	"encoding/json"
	"errors"
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

func TestPreviewMetricFailsClosedForUnsupportedExecutionBoundary(t *testing.T) {
	singleSource := metricSingleSourceDocument(t)
	tests := []struct {
		name     string
		document dataset.Document
		resolved ResolvedPlan
		policies metricPreviewPolicies
	}{
		{
			name: "跨源数据集", document: crossSourceRuntimeDocument(),
			resolved: ResolvedPlan{SourceID: "source-1", SourceType: datasource.TypeMySQL},
		},
		{
			name: "Excel 数据集", document: singleSource,
			resolved: ResolvedPlan{SourceID: "source-file", SourceType: datasource.TypeExcel},
		},
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
