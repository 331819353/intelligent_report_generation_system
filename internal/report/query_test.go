package report

import (
	"context"
	"sync"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/reportjson"
)

type fakeMetricQueryExecutor struct {
	mu     sync.Mutex
	calls  int
	inputs []metric.PreviewInput
}

func (executor *fakeMetricQueryExecutor) PreviewVersion(_ context.Context, _, _, _, _ string, input metric.PreviewInput) (dataset.PreviewResult, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls++
	executor.inputs = append(executor.inputs, input)
	return dataset.PreviewResult{
		Columns:        []string{"region", "gmv"},
		ColumnMetadata: []dataset.PreviewColumn{{FieldID: "region", Code: "region", Name: "区域", Role: "DIMENSION"}, {FieldID: "gmv", Code: "gmv", Name: "销售额", Role: "METRIC"}},
		Rows:           [][]any{{"华东", 1200.0}}, RowCount: 1, Warnings: []dataset.PreviewWarning{},
	}, nil
}

func TestQueryBatchCompilesTrustedFiltersAndCachesExactMetricVersion(t *testing.T) {
	executor := &fakeMetricQueryExecutor{}
	runtime := newReportQueryRuntime()
	runtime.executor = executor
	document := queryTestDocument()
	input := ReportQueryBatchInput{CardIDs: []string{"ranking"}, Filters: map[string]any{"region-filter": "华东"}, InteractionContext: map[string]ReportInteractionContext{}}
	versions := map[string]string{"metric-gmv": "metric-version-7"}

	first, err := runtime.executeBatch(context.Background(), "tenant-a", "actor-a", document, versions, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.executeBatch(context.Background(), "tenant-a", "actor-a", document, versions, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Results[0].Status != "SUCCESS" || first.Results[0].CacheHit || !second.Results[0].CacheHit {
		t.Fatalf("unexpected cache states: first=%#v second=%#v", first.Results[0], second.Results[0])
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 1 {
		t.Fatalf("expected one source query, got %d", executor.calls)
	}
	filters := executor.inputs[0].DimensionFilters
	if len(filters) != 1 || filters[0].FieldID != "region" || filters[0].Operator != "EQUALS" || filters[0].Value != "华东" {
		t.Fatalf("trusted filter was not compiled: %#v", filters)
	}
}

func TestQueryBatchDerivesDrillDimensionsFromTrustedDefinition(t *testing.T) {
	executor := &fakeMetricQueryExecutor{}
	runtime := newReportQueryRuntime()
	runtime.executor = executor
	document := queryTestDocument()
	document.Cards[0].Interactions = []reportjson.CardInteraction{{
		ID: "drill-region", Event: "data.click",
		Action: reportjson.InteractionAction{Type: "drillDown", PathID: "geo", ToDimension: "city"},
	}}
	input := ReportQueryBatchInput{
		CardIDs: []string{"ranking"}, Filters: map[string]any{},
		InteractionContext: map[string]ReportInteractionContext{
			"ranking": {SourceCardID: "ranking", InteractionID: "drill-region", Value: "华东"},
		},
	}
	result, err := runtime.executeBatch(context.Background(), "tenant-a", "actor-a", document, map[string]string{"metric-gmv": "metric-version-7"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Status != "SUCCESS" {
		t.Fatalf("unexpected result %#v", result.Results[0])
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	got := executor.inputs[0]
	if len(got.DimensionFieldIDs) != 1 || got.DimensionFieldIDs[0] != "city" {
		t.Fatalf("drill grouping was not derived from DSL: %#v", got.DimensionFieldIDs)
	}
	if len(got.DimensionFilters) != 1 || got.DimensionFilters[0].FieldID != "region" || got.DimensionFilters[0].Value != "华东" {
		t.Fatalf("drill filter was not derived from source card: %#v", got.DimensionFilters)
	}
}

func queryTestDocument() reportjson.Document {
	return reportjson.Document{
		SchemaVersion: reportjson.CardSchemaVersion,
		GlobalFilters: []reportjson.GlobalFilter{{ID: "region-filter", Label: "区域", Type: "SELECT", Source: reportjson.FilterSource{SemanticModelID: "sales", DimensionID: "region"}, Operator: "equals"}},
		Cards: []reportjson.Card{{
			ID: "ranking", Type: "RANKING", CardVersion: "1.0.0",
			Binding: reportjson.CardBinding{
				SemanticModelID: "sales", Metrics: []reportjson.MetricBinding{{ID: "metric-gmv", Role: "value"}}, Dimensions: []reportjson.DimensionBinding{{ID: "region", Role: "category"}},
				GlobalFilterBindings: []reportjson.GlobalFilterBinding{{FilterID: "region-filter", TargetDimensionID: "region"}}, Filters: []reportjson.CardFilter{}, Sort: []reportjson.CardSort{{Field: "metric-gmv", Direction: "desc"}}, Limit: 10,
			},
		}},
	}
}
