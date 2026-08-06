package queryruntime

import "testing"

func TestSemanticQuestionAuditContractsDoNotAcceptUnsafeShapes(t *testing.T) {
	run := SemanticQuestionRun{
		RunID: "14e15896-31e9-4b12-8fa8-ff0841464b4d", RunType: RunTypeSemanticQuestion,
		TenantID: "tenant", DomainID: "domain", ActorID: "actor",
		QueryPlanHash: semanticTestHash('1'), ValidationHash: semanticTestHash('2'),
		PlanCount: 2, MaxRows: 10000, TimeoutMS: 25000, MaxExplainCost: 42,
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	run.RunType = "PREVIEW"
	if run.Validate() == nil {
		t.Fatal("semantic audit accepted a non-semantic run type")
	}
	completion := SemanticQuestionCompletion{
		RunID: "14e15896-31e9-4b12-8fa8-ff0841464b4d", TenantID: "tenant",
		Status: SemanticQuestionSucceeded, ResultHash: semanticTestHash('3'), RowCount: 2,
	}
	if err := completion.Validate(); err != nil {
		t.Fatal(err)
	}
	completion.ErrorCode = "QUERY_FAILED"
	if completion.Validate() == nil {
		t.Fatal("successful semantic audit accepted an error code")
	}
}

func semanticTestHash(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
