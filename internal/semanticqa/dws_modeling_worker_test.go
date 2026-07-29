package semanticqa

import (
	"context"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

type recordingDWSInvoker struct {
	invocations []aiplatform.Invocation
}

func (invoker *recordingDWSInvoker) Configured() bool {
	return true
}

func (invoker *recordingDWSInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.invocations = append(invoker.invocations, invocation)
	return aiplatform.InvocationResult{
		RequestID: "request",
		ProviderResult: aiplatform.ProviderResult{
			Content: []byte(`{"templateCodes":["TREND"]}`),
		},
	}, nil
}

func TestDWSSelectorUsesPerFactPromptForSingleDWD(t *testing.T) {
	invoker := &recordingDWSInvoker{}
	selector := NewOrchestratedDWSAnalysisSelector(invoker)
	fact := dwsPlanningAsset{
		Record: dataset.Record{
			ID: "fact_dataset", Code: "dwd_order", Name: "订单明细",
		},
		VersionID: "fact_version",
		Document: dataset.Document{
			Fields: []dataset.Field{{
				Code: "order_date", Role: "TIME", CanonicalType: "DATE",
			}},
			FactContract: &dataset.FactContract{
				BusinessAction: "下单", EventTimeField: "order_date",
			},
		},
	}
	selected, _, err := selector.Select(
		context.Background(), "tenant", "actor", "scope",
		dwsModelingScope{
			GroupKey:   "single-dwd:fact_dataset",
			DomainCode: "operations", SubjectCode: "order",
			SubjectName: "订单分析",
		},
		[]dwsPlanningAsset{fact}, nil, []string{"TREND"},
	)
	if err != nil {
		t.Fatalf("select single DWD: %v", err)
	}
	if len(selected) != 1 || selected[0] != "TREND" {
		t.Fatalf("selected templates = %#v", selected)
	}
	if len(invoker.invocations) != 1 ||
		invoker.invocations[0].PromptVersion != dwsSingleFactPlanningVersion {
		t.Fatalf("invocations = %#v", invoker.invocations)
	}
}

func TestDWSSelectorKeepsExplicitMultiFactPrompt(t *testing.T) {
	invoker := &recordingDWSInvoker{}
	selector := NewOrchestratedDWSAnalysisSelector(invoker)
	facts := []dwsPlanningAsset{
		{
			Record:    dataset.Record{ID: "fact_a", Code: "dwd_a", Name: "事实 A"},
			VersionID: "version_a",
		},
		{
			Record:    dataset.Record{ID: "fact_b", Code: "dwd_b", Name: "事实 B"},
			VersionID: "version_b",
		},
	}
	_, _, err := selector.Select(
		context.Background(), "tenant", "actor", "scope",
		dwsModelingScope{GroupKey: "explicit-multi-fact"},
		facts, nil, []string{"MULTI_FACT_COMPARISON"},
	)
	if err != nil {
		t.Fatalf("select explicit multi-fact DWS: %v", err)
	}
	if len(invoker.invocations) != 1 ||
		invoker.invocations[0].PromptVersion !=
			dwsGroupedFactPlanningVersion {
		t.Fatalf("invocations = %#v", invoker.invocations)
	}
}
