package metriccandidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

type enrichmentInvokerStub struct {
	err         error
	description string
	inspect     func(enrichmentInput)
}

func (s enrichmentInvokerStub) Configured() bool { return true }
func (s enrichmentInvokerStub) Model() string    { return "chat-model" }
func (s enrichmentInvokerStub) Invoke(_ context.Context, invocation aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	if s.err != nil {
		return aiplatform.InvocationResult{}, s.err
	}
	var input enrichmentInput
	if err := json.Unmarshal([]byte(invocation.Request.Messages[1].Parts[0].Text), &input); err != nil {
		return aiplatform.InvocationResult{}, err
	}
	if s.inspect != nil {
		s.inspect(input)
	}
	output := enrichmentOutput{Items: make([]enrichmentItem, 0, len(input.Candidates))}
	for _, candidate := range input.Candidates {
		description := s.description
		if description == "" {
			description = "用于经营分析的指标说明"
		}
		output.Items = append(output.Items, enrichmentItem{
			Fingerprint: candidate.Fingerprint, Name: candidate.Name + "指标",
			Description: description, Caliber: candidate.DeterministicRule,
			PeriodDescription: "按月", LineageSummary: "来自已发布数据集的逻辑字段",
			Tags: []string{"销售", "月度", "经营分析"},
		})
	}
	raw, _ := json.Marshal(output)
	return aiplatform.InvocationResult{RequestID: "55555555-5555-4555-8555-555555555555", ProviderResult: aiplatform.ProviderResult{Content: raw}}, nil
}

type semanticContextLoaderStub struct {
	loadFn func(context.Context, string, []SemanticContextRequest) ([]SemanticTableContext, error)
}

func (loader semanticContextLoaderStub) LoadMetricSemanticContext(
	ctx context.Context,
	tenantID string,
	requests []SemanticContextRequest,
) ([]SemanticTableContext, error) {
	return loader.loadFn(ctx, tenantID, requests)
}

func TestEnricherImprovesWordingWithoutChangingMetricFacts(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	base, err := Extract(version)
	if err != nil {
		t.Fatal(err)
	}
	definitions := make([]string, len(base.Candidates))
	fingerprints := make([]string, len(base.Candidates))
	for index, candidate := range base.Candidates {
		raw, _ := json.Marshal(candidate.Definition)
		definitions[index], fingerprints[index] = string(raw), candidate.Fingerprint
	}
	result, err := NewEnricher(enrichmentInvokerStub{}, time.Second).Enrich(
		context.Background(), testTenantID, testActorID, version, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, candidate := range result.Candidates {
		raw, _ := json.Marshal(candidate.Definition)
		if string(raw) != definitions[index] || candidate.Fingerprint != fingerprints[index] {
			t.Fatalf("LLM changed executable metric facts at index %d", index)
		}
		if candidate.Semantic.Source != "HYBRID" || candidate.Semantic.Model != "chat-model" ||
			candidate.Semantic.RequestID == "" || !containsString(candidate.Semantic.Tags, "经营分析") {
			t.Fatalf("semantic enrichment missing at index %d: %#v", index, candidate.Semantic)
		}
		if !reflect.DeepEqual(candidate.Semantic.Lineage.DimensionFieldIDs, []string{"field_region", "field_order_date"}) {
			t.Fatalf("authoritative lineage changed: %#v", candidate.Semantic.Lineage)
		}
	}
}

func TestEnricherFallsBackToRuleSemantics(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	base, _ := Extract(version)
	providerErr := errors.New("provider failed")
	result, err := NewEnricher(enrichmentInvokerStub{err: providerErr}, time.Second).Enrich(
		context.Background(), testTenantID, testActorID, version, base,
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v", err)
	}
	for _, candidate := range result.Candidates {
		if candidate.Semantic.Source != "RULE_FALLBACK" || candidate.Semantic.Caliber == "" || candidate.Semantic.Lineage.DatasetVersionID != version.ID {
			t.Fatalf("fallback semantic metadata = %#v", candidate.Semantic)
		}
	}
}

func TestEnricherReceivesReviewedMetadataActualAggregationAndLockedUnit(t *testing.T) {
	version := publishedDatasetVersion(t, monthlyPaymentsDocument())
	base, err := Extract(version)
	if err != nil {
		t.Fatal(err)
	}
	loader := semanticContextLoaderStub{loadFn: func(
		_ context.Context,
		tenantID string,
		requests []SemanticContextRequest,
	) ([]SemanticTableContext, error) {
		if tenantID != testTenantID || len(requests) != 1 ||
			requests[0].TableID != "44444444-4444-4444-8444-444444444444" {
			t.Fatalf("context requests = (%q, %#v)", tenantID, requests)
		}
		return []SemanticTableContext{{
			TableID: requests[0].TableID, Name: "订单支付事实表",
			Description: "记录订单支付事实。",
			Columns: []SemanticColumnContext{{
				Code: "paid_at", Name: "支付完成时间", Description: "支付完成的精确时间点",
			}},
		}}, nil
	}}
	invoker := enrichmentInvokerStub{
		description: "统计每位客户付款订单的数量。",
		inspect: func(input enrichmentInput) {
			if len(input.SourceTables) != 1 || input.SourceTables[0].Name != "订单支付事实表" {
				t.Fatalf("source metadata missing: %#v", input.SourceTables)
			}
			found := false
			for _, candidate := range input.Candidates {
				if candidate.SourceFieldCode != "order_count" {
					continue
				}
				found = true
				if candidate.Aggregation != "COUNT_DISTINCT" || candidate.Unit != "笔" ||
					candidate.SourceFieldName != "订单数量" {
					t.Fatalf("locked candidate facts = %#v", candidate)
				}
			}
			if !found {
				t.Fatalf("order candidate missing: %#v", input.Candidates)
			}
		},
	}
	result, err := NewEnricher(invoker, time.Second, loader).Enrich(
		context.Background(), testTenantID, testActorID, version, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range result.Candidates {
		if candidate.Semantic.Source != "HYBRID" || candidate.Semantic.Description != "统计每位客户付款订单的数量。" {
			t.Fatalf("semantic enrichment = %#v", candidate.Semantic)
		}
	}
}

func TestEnricherRejectsTechnicalDescriptionAndKeepsBusinessFallback(t *testing.T) {
	version := publishedDatasetVersion(t, monthlyPaymentsDocument())
	base, err := Extract(version)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewEnricher(enrichmentInvokerStub{
		description: "由数据集的 DAG 按 COUNT_DISTINCT 计算输出。",
	}, time.Second).Enrich(context.Background(), testTenantID, testActorID, version, base)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
	for _, candidate := range result.Candidates {
		if candidate.Semantic.Source != "RULE_FALLBACK" ||
			strings.Contains(candidate.Semantic.Description, "DAG") ||
			strings.Contains(candidate.Semantic.Description, "COUNT_DISTINCT") {
			t.Fatalf("unprofessional wording escaped the quality gate: %#v", candidate.Semantic)
		}
	}
}

func TestEnricherBatchesEveryCandidateInsteadOfSkippingLargeDatasets(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	base, err := Extract(version)
	if err != nil {
		t.Fatal(err)
	}
	template := base.Candidates[0]
	base.Candidates = make([]CandidateDraft, maxEnrichmentCandidates+1)
	for index := range base.Candidates {
		base.Candidates[index] = template
		base.Candidates[index].Fingerprint = fmt.Sprintf("%064x", index+1)
	}
	batchSizes := []int{}
	invoker := enrichmentInvokerStub{inspect: func(input enrichmentInput) {
		batchSizes = append(batchSizes, len(input.Candidates))
	}}
	result, err := NewEnricher(invoker, time.Second).Enrich(
		context.Background(), testTenantID, testActorID, version, base,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batchSizes, []int{maxEnrichmentCandidates, 1}) {
		t.Fatalf("batch sizes = %v", batchSizes)
	}
	for index, candidate := range result.Candidates {
		if candidate.Semantic.Source != "HYBRID" {
			t.Fatalf("candidate %d was not enriched: %#v", index, candidate.Semantic)
		}
	}
}

func TestRuleSemanticsPreserveDatasetFilterScopeWithoutExposingValues(t *testing.T) {
	document := candidateDatasetDocument()
	document.Nodes[0].SourceFilters = []dataset.SourceFilter{{Field: "channel", Operator: "EQUALS", Value: "PAID"}}
	document.Filters = []dataset.Filter{{
		ID: "optional_region", Stage: "PRE_AGGREGATION", Optional: true,
		Expression: dataset.Expression{Type: "EQUALS", Left: ptrExpression(fieldRef("region")), Right: ptrExpression(dataset.Expression{Type: "PARAM_REF", Code: "region"})},
	}}
	document.Parameters = []dataset.Parameter{{Code: "region", Name: "地区", DataType: "STRING"}}
	version := publishedDatasetVersion(t, document)
	base, err := Extract(version)
	if err != nil {
		t.Fatal(err)
	}
	result := attachDefaultSemantics(version, base)
	if len(result.Candidates) == 0 || !strings.Contains(result.Candidates[0].Semantic.Caliber, "1 个固定过滤条件") ||
		!strings.Contains(result.Candidates[0].Semantic.Caliber, "1 个运行时可选过滤条件") || strings.Contains(result.Candidates[0].Semantic.Caliber, "PAID") {
		t.Fatalf("filter scope was not preserved safely: %#v", result.Candidates)
	}
}

func ptrExpression(value dataset.Expression) *dataset.Expression { return &value }
