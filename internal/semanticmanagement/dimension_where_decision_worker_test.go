package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type dimensionWhereAIStub struct {
	content json.RawMessage
	model   string
	err     error
}

func (s dimensionWhereAIStub) Configured() bool { return true }
func (s dimensionWhereAIStub) Model() string {
	if s.model == "" {
		return "MiniMax-M2"
	}
	return s.model
}
func (s dimensionWhereAIStub) Invoke(
	context.Context,
	aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	if s.err != nil {
		return aiplatform.InvocationResult{}, s.err
	}
	return aiplatform.InvocationResult{
		ProviderResult: aiplatform.ProviderResult{
			Content: s.content,
			Model:   s.model,
		},
	}, nil
}

type dimensionWhereAISequenceStub struct {
	contents    []json.RawMessage
	invocations []aiplatform.Invocation
}

func (*dimensionWhereAISequenceStub) Configured() bool { return true }
func (*dimensionWhereAISequenceStub) Model() string {
	return "MiniMax-M2,deepseek-v3"
}
func (s *dimensionWhereAISequenceStub) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	s.invocations = append(s.invocations, invocation)
	index := len(s.invocations) - 1
	return aiplatform.InvocationResult{
		ProviderResult: aiplatform.ProviderResult{
			Content: s.contents[index],
			Model:   invocation.PreferredModel,
		},
	}, nil
}

func policyClaim(samples ...string) DimensionWherePolicyClaim {
	return DimensionWherePolicyClaim{
		ID: "policy-1", TenantID: "550e8400-e29b-41d4-a716-446655440000",
		ActorID:              "550e8400-e29b-41d4-a716-446655440001",
		DimensionID:          "550e8400-e29b-41d4-a716-446655440002",
		DimensionFieldName:   "key_talent",
		DimensionDescription: "标识员工是否被评定为关键人才",
		MetricCode:           "employee_total_count",
		MetricFieldID:        "field_employee_total_count",
		TableSchema:          "warehouse_published",
		TableName:            "dws_employee",
		SampleValues:         samples,
		Attempt:              1, LeaseOwner: "worker-1",
		LeaseToken:     "550e8400-e29b-41d4-a716-446655440003",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}
}

func TestDimensionWherePolicyDesignerUsesContainsOnlyForDelimitedValues(
	t *testing.T,
) {
	designer := NewOrchestratedDimensionWherePolicyDesigner(
		dimensionWhereAIStub{
			model: "MiniMax-M2",
			content: json.RawMessage(`{
				"dimensionFieldName":"key_talent",
				"operator":"CONTAINS",
				"reason":"样本是逗号分隔的多标签集合",
				"confidence":0.97
			}`),
		},
		time.Second,
	)
	decision, err := designer.Design(
		context.Background(),
		policyClaim("关键人才,技术专家", "后备干部,科技人才"),
	)
	if err != nil || decision.PredicateOperator != "CONTAINS" ||
		decision.LLMModel != "MiniMax-M2" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	_, err = designer.Design(
		context.Background(),
		policyClaim("在岗", "离职"),
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected invalid CONTAINS evidence, got %v", err)
	}
}

func TestDimensionWherePolicyDesignerRepairsWithConfiguredFallback(
	t *testing.T,
) {
	ai := &dimensionWhereAISequenceStub{
		contents: []json.RawMessage{
			json.RawMessage(`{
				"dimensionFieldName":"invented_field",
				"operator":"EQUALS",
				"reason":"原子枚举值",
				"confidence":0.98
			}`),
			json.RawMessage(`{
				"dimensionFieldName":"key_talent",
				"operator":"EQUALS",
				"reason":"样本为原子枚举值",
				"confidence":0.99
			}`),
		},
	}
	designer := NewOrchestratedDimensionWherePolicyDesigner(
		ai, time.Second,
	)
	decision, err := designer.Design(
		context.Background(),
		policyClaim("关键人才", "科技人才"),
	)
	if err != nil || decision.PredicateOperator != "EQUALS" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if len(ai.invocations) != 2 ||
		ai.invocations[1].PreferredModel != "deepseek-v3" {
		t.Fatalf("invocations=%+v", ai.invocations)
	}
}

type dimensionDecisionStoreStub struct {
	claim        *DimensionWherePolicyClaim
	completed    bool
	failed       bool
	materialized int64
}

func (s *dimensionDecisionStoreStub) ListDimensionDecisionTenantIDs(
	context.Context,
) ([]string, error) {
	return []string{"550e8400-e29b-41d4-a716-446655440000"}, nil
}
func (s *dimensionDecisionStoreStub) ReconcileDimensionWherePolicies(
	context.Context, string,
) (int64, error) {
	return 0, nil
}
func (s *dimensionDecisionStoreStub) ClaimDimensionWherePolicy(
	context.Context, string, string, time.Duration,
) (*DimensionWherePolicyClaim, error) {
	claim := s.claim
	s.claim = nil
	return claim, nil
}
func (s *dimensionDecisionStoreStub) CompleteDimensionWherePolicy(
	context.Context,
	DimensionWherePolicyClaim,
	DimensionWherePolicyDecision,
) error {
	s.completed = true
	return nil
}
func (s *dimensionDecisionStoreStub) FailDimensionWherePolicy(
	context.Context,
	DimensionWherePolicyClaim,
	string,
) error {
	s.failed = true
	return nil
}
func (s *dimensionDecisionStoreStub) MaterializeDimensionWhereDecisions(
	context.Context, string, int,
) (int64, error) {
	return s.materialized, nil
}
func (s *dimensionDecisionStoreStub) CleanupDimensionWhereDecisions(
	context.Context, string,
) (int64, error) {
	return 0, nil
}
func (s *dimensionDecisionStoreStub) DimensionWhereDecisionBuildProgress(
	context.Context, string,
) (DimensionWhereDecisionBuildProgress, error) {
	return DimensionWhereDecisionBuildProgress{}, nil
}

type policyDesignerStub struct {
	decision DimensionWherePolicyDecision
	err      error
}

func (s policyDesignerStub) Design(
	context.Context,
	DimensionWherePolicyClaim,
) (DimensionWherePolicyDecision, error) {
	return s.decision, s.err
}

func TestDimensionWhereDecisionWorkerCompletesPolicyBeforeMaterializing(
	t *testing.T,
) {
	claim := policyClaim("在岗", "离职")
	store := &dimensionDecisionStoreStub{claim: &claim}
	worker := NewDimensionWhereDecisionWorker(
		store,
		policyDesignerStub{decision: DimensionWherePolicyDecision{
			PredicateOperator: "EQUALS",
			LLMModel:          "MiniMax-M2",
			LLMReason:         "原子枚举值",
			Confidence:        0.99,
		}},
	)
	processed, err := worker.ProcessNext(
		context.Background(),
		"550e8400-e29b-41d4-a716-446655440000",
		"worker-1", time.Minute,
	)
	if err != nil || !processed || !store.completed || store.failed {
		t.Fatalf(
			"processed=%v err=%v completed=%v failed=%v",
			processed, err, store.completed, store.failed,
		)
	}
}
