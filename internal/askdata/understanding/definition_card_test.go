package understanding

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type definitionBackendSpy struct {
	registryCalls  int
	dataQueryCalls int
	contract       MetricDefinitionContract
	err            error
}

func (backend *definitionBackendSpy) GetMetricDefinition(_ context.Context, _ askdata.ID) (MetricDefinitionContract, error) {
	backend.registryCalls++
	return backend.contract, backend.err
}

// ExecuteDataQuery deliberately is not part of MetricDefinitionRegistry. The
// concrete spy exposes it to prove the definition service never reaches it.
func (backend *definitionBackendSpy) ExecuteDataQuery(context.Context) error {
	backend.dataQueryCalls++
	return nil
}

func TestDefinitionCardShortPathReadsRegistryAndIssuesZeroDataQueries(t *testing.T) {
	metricVersionID := askdata.ID("metric-sales-v3")
	backend := &definitionBackendSpy{contract: MetricDefinitionContract{
		MetricVersionID: metricVersionID,
		Name:            "销售额",
		Definition:      "已支付且未取消订单的含税金额，扣除已确认退款。",
		Formula:         "已支付订单金额 - 已确认退款金额",
		Unit:            "CNY",
		OwnerID:         "owner-finance-data",
		SemanticVersion: "2026.08.1",
		Status:          "CERTIFIED",
		EvidenceRefs: []askdata.EvidenceRef{{
			EvidenceID: "evidence:metric-sales-v3", Kind: askdata.EvidenceKindSemanticContract,
			SourceID: metricVersionID, ContentHash: askdata.HashBytes([]byte("metric-sales-v3")),
		}},
	}}
	classifier, err := NewScopeClassifier(DefaultScopeLexicon(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDefinitionCardService(classifier, backend)
	if err != nil {
		t.Fatal(err)
	}
	understanding := understandingForScopeExample(t, scopeExample{
		question: "销售额怎么算", typeWanted: QuestionTypeDefinition, metrics: []string{"销售额"},
	})
	card, verdict, err := service.Render(context.Background(), understanding, metricVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Type != QuestionTypeDefinition || verdict.Outcome != ScopeOutcomeDefinition ||
		backend.registryCalls != 1 || backend.dataQueryCalls != 0 || card.DataQueryIssued || card.MaxLLMCalls != 1 {
		t.Fatalf("definition short path = card:%#v verdict:%#v registry:%d query:%d",
			card, verdict, backend.registryCalls, backend.dataQueryCalls)
	}
	if card.Name != "销售额" || card.Formula == "" || len(card.EvidenceRefs) != 1 {
		t.Fatalf("definition card = %#v", card)
	}
	if err := card.Validate(); err != nil {
		t.Fatalf("definition card validation: %v", err)
	}
}

func TestDefinitionCardRejectsNonDefinitionAndUntrustedContracts(t *testing.T) {
	metricVersionID := askdata.ID("metric-sales-v3")
	backend := &definitionBackendSpy{contract: MetricDefinitionContract{
		MetricVersionID: metricVersionID,
		Name:            "销售额",
		Definition:      "已支付订单金额。",
		Formula:         "SELECT secret FROM raw_orders",
		Unit:            "CNY",
		OwnerID:         "owner-finance-data",
		SemanticVersion: "2026.08.1",
		Status:          "CERTIFIED",
		EvidenceRefs: []askdata.EvidenceRef{{
			EvidenceID: "evidence:metric-sales-v3", Kind: askdata.EvidenceKindSemanticContract,
			SourceID: metricVersionID, ContentHash: askdata.HashBytes([]byte("metric-sales-v3")),
		}},
	}}
	classifier, _ := NewScopeClassifier(DefaultScopeLexicon(), nil, false)
	service, _ := NewDefinitionCardService(classifier, backend)
	if _, _, err := service.Render(context.Background(), understandingForScopeExample(t, scopeExample{
		question: "销售额是多少", typeWanted: QuestionTypeMetricLookup, metrics: []string{"销售额"},
	}), metricVersionID); !errors.Is(err, ErrQuestionNotDefinition) {
		t.Fatalf("non-definition error = %v", err)
	}
	if backend.registryCalls != 0 || backend.dataQueryCalls != 0 {
		t.Fatalf("non-definition reached backend: %#v", backend)
	}
	if _, _, err := service.Render(context.Background(), understandingForScopeExample(t, scopeExample{
		question: "销售额怎么算", typeWanted: QuestionTypeDefinition, metrics: []string{"销售额"},
	}), metricVersionID); !errors.Is(err, ErrDefinitionCardInvalid) {
		t.Fatalf("unsafe contract error = %v", err)
	}
	if backend.dataQueryCalls != 0 {
		t.Fatalf("unsafe contract issued query: %d", backend.dataQueryCalls)
	}
}

func TestDefinitionCardRegistryMustReturnPinnedMetricVersion(t *testing.T) {
	backend := &definitionBackendSpy{contract: MetricDefinitionContract{MetricVersionID: "metric-other-v1"}}
	classifier, _ := NewScopeClassifier(DefaultScopeLexicon(), nil, false)
	service, _ := NewDefinitionCardService(classifier, backend)
	_, _, err := service.Render(context.Background(), understandingForScopeExample(t, scopeExample{
		question: "销售额怎么算", typeWanted: QuestionTypeDefinition, metrics: []string{"销售额"},
	}), "metric-sales-v3")
	if !errors.Is(err, ErrDefinitionCardInvalid) {
		t.Fatalf("mismatched metric version error = %v", err)
	}
}
