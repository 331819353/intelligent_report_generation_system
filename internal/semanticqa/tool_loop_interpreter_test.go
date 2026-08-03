package semanticqa

import (
	"context"
	"encoding/json"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

func TestSemanticMetricToolExecutorRequiresSemanticSearchBeforeCatalog(t *testing.T) {
	executor := &semanticMetricToolExecutor{}
	arguments, _ := json.Marshal(semanticMetricSearchInput{Query: "投诉数量"})
	if _, err := executor.ExecuteTool(
		context.Background(),
		aiplatform.ToolExecution{Name: "search_metrics", Arguments: arguments},
	); err == nil {
		t.Fatal("catalog search must not run before metric semantic retrieval")
	}
	selection, _ := json.Marshal(semanticMetricSelectionInput{
		Intent: "METRIC", NeedsClarification: true,
	})
	if _, err := executor.ExecuteTool(
		context.Background(),
		aiplatform.ToolExecution{
			Name: "submit_metric_selection", Arguments: selection,
		},
	); err == nil {
		t.Fatal("selection must not terminate before both retrieval stages")
	}
}

func TestSemanticMetricToolExecutorBuildsAugmentedQuestionFromCatalogQuery(
	t *testing.T,
) {
	executor := &semanticMetricToolExecutor{
		question:          "帮我看看客诉情况",
		latestMetricQuery: "客诉情况；指标语义=投诉数量",
	}
	want := "帮我看看客诉情况【指标语义补充：客诉情况；指标语义=投诉数量】"
	if got := executor.augmentedQuestion(); got != want {
		t.Fatalf("unexpected augmented question: %q", got)
	}
}

func TestSemanticMetricToolExecutorMergesPrimaryAndSupplementCandidates(
	t *testing.T,
) {
	executor := &semanticMetricToolExecutor{
		candidates: map[string]recallCandidate{},
		order:      []string{},
	}
	executor.addCandidates([]recallCandidate{
		{
			SubjectType: "METRIC", Code: "complaint_rate",
			Label: "投诉处理及时率", Score: 0.72,
		},
		{
			SubjectType: "DIMENSION", Code: "city",
			Label: "城市", Score: 0.99,
		},
	})
	executor.addCandidates([]recallCandidate{
		{
			SubjectType: "METRIC", Code: "complaint_rate",
			Label: "投诉处理及时率", Score: 0.91,
		},
		{
			SubjectType: "METRIC", Code: "complaint_count",
			Label: "投诉数量", Score: 0.83,
		},
	})

	candidates := executor.orderedCandidates()
	if len(candidates) != 2 ||
		candidates[0].Code != "complaint_rate" ||
		candidates[0].Score != 0.91 ||
		candidates[1].Code != "complaint_count" {
		t.Fatalf(
			"expected a metric-only union with the best score retained: %#v",
			candidates,
		)
	}
}
