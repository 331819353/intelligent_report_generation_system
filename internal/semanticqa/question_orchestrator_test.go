package semanticqa

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
)

func readyQuestionPlan(metricCode string, generation int64) QueryPlan {
	metricID := uuid.NewString()
	metricVersionID := uuid.NewString()
	dimensionID := uuid.NewString()
	datasetVersionID := uuid.NewString()
	return QueryPlan{
		ID: uuid.NewString(), GraphGenerationID: uuid.NewString(),
		GraphGeneration: generation, Status: "READY", Confidence: 0.99,
		PathHash:         strings.Repeat("a", 64),
		SelectedMetricID: metricID, SelectedMetricVersionID: metricVersionID,
		SelectedDatasetVersionID:  datasetVersionID,
		SelectedMaterializationID: uuid.NewString(),
		Conditions: QueryConditionDocument{
			MetricCode: metricCode, MetricVersionID: metricVersionID,
			DatasetVersionID: datasetVersionID,
			Dimensions: []QueryDimensionClause{{
				DimensionCode: "region", DimensionID: dimensionID,
				MemberKeys: []string{"EAST"},
			}},
		},
		Evidence: []QueryEvidence{
			{SubjectType: "METRIC", SubjectRef: metricVersionID, Label: "支付GMV", EvidenceHash: strings.Repeat("b", 64)},
			{SubjectType: "DIMENSION", SubjectRef: dimensionID, Label: "销售区域", EvidenceHash: strings.Repeat("c", 64)},
		},
	}
}

func TestBuildQuestionContractsCreatesVersionedSemanticIR(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	turn := QueryTurnPlan{Intent: "RANKING", Plans: []QueryPlan{plan}}
	intent, ir, graph, err := buildQuestionContracts(
		turn, "semantic-graph-7", defaultQuestionBudgets(),
	)
	if err != nil {
		t.Fatalf("build contracts: %v", err)
	}
	if intent.ExecutionPath != QuestionRouteSemantic ||
		ir.SchemaVersion != "1.0" || ir.Mode != "semantic" ||
		len(ir.Metrics) != 1 || ir.Metrics[0].MetricVersionID != plan.SelectedMetricVersionID ||
		len(ir.Filters) != 1 || ir.Filters[0].ValueIDs[0] != "EAST" {
		t.Fatalf("unexpected contracts: intent=%+v ir=%+v", intent, ir)
	}
	if graph.GenerationID != plan.GraphGenerationID || len(graph.QueryPlanIDs) != 1 {
		t.Fatalf("unexpected execution graph: %+v", graph)
	}
}

func TestBuildQuestionContractsPreservesGroupingDimensionWithoutFilter(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	plan.Conditions.Dimensions[0].MemberKey = ""
	plan.Conditions.Dimensions[0].MemberKeys = nil
	turn := QueryTurnPlan{Intent: "RANKING", Plans: []QueryPlan{plan}}

	intent, ir, _, err := buildQuestionContracts(
		turn, "semantic-graph-7", defaultQuestionBudgets(),
	)
	if err != nil {
		t.Fatalf("build contracts: %v", err)
	}
	if len(intent.Dimensions) != 1 || intent.Dimensions[0].Code != "region" {
		t.Fatalf("grouping dimension missing from intent: %+v", intent)
	}
	if len(ir.Dimensions) != 1 ||
		ir.Dimensions[0] != plan.Conditions.Dimensions[0].DimensionID {
		t.Fatalf("grouping dimension missing from semantic IR: %+v", ir)
	}
	if len(ir.Filters) != 0 {
		t.Fatalf("grouping-only dimension must not create a member filter: %+v", ir)
	}
}

func TestBuildQuestionContractsRejectsMixedSemanticVersions(t *testing.T) {
	first := readyQuestionPlan("paid_gmv", 7)
	second := readyQuestionPlan("order_count", 8)
	_, _, _, err := buildQuestionContracts(
		QueryTurnPlan{Intent: "METRIC", Plans: []QueryPlan{first, second}},
		"semantic-graph-7", defaultQuestionBudgets(),
	)
	if err == nil {
		t.Fatal("mixed graph generations must be rejected")
	}
}

func TestSQLGuardRejectsNonReadyPlan(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	plan.Status = "GAP"
	decision := validateSemanticExecution(
		[]QueryPlan{plan},
		SemanticQueryIR{SchemaVersion: "1.0", Mode: "semantic"},
		defaultQuestionBudgets(),
	)
	if decision.Status != "BLOCKED" {
		t.Fatalf("expected blocked SQL Guard, got %+v", decision)
	}
}

func TestResultVerifierRejectsMalformedRows(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	queryID := uuid.NewString()
	execution := QueryPlanExecution{
		QueryPlan: plan,
		Result: dataset.PreviewResult{
			QueryID: queryID, Columns: []string{"region", "paid_gmv"},
			ColumnMetadata: []dataset.PreviewColumn{{Code: "region"}, {Code: "paid_gmv"}},
			Rows:           [][]any{{"EAST"}}, RowCount: 1,
		},
		Evidence: AnswerEvidence{
			QueryTraceID: queryID, QueryPlanHash: strings.Repeat("d", 64),
			ResultHash: strings.Repeat("e", 64), SemanticVersion: "semantic-graph-7",
			ExecutionRevalidated: true, PermissionDecision: "PASS",
		},
	}
	verification := verifyQuestionResults(
		[]QueryPlan{plan}, []QueryPlanExecution{execution}, defaultQuestionBudgets(),
	)
	if verification.Status != "BLOCKED" || verification.TrustLevel != "D" {
		t.Fatalf("malformed row must be blocked: %+v", verification)
	}
}

func TestResultVerifierRecomputesEvidenceHashes(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	queryID := uuid.NewString()
	result := dataset.PreviewResult{
		QueryID: queryID, Columns: []string{"paid_gmv"},
		ColumnMetadata: []dataset.PreviewColumn{{Code: "paid_gmv"}},
		Rows:           [][]any{{123.45}}, RowCount: 1,
	}
	executed := plan
	executed.Status = "EXECUTED"
	evidence, err := buildAnswerEvidence(executed, result, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	execution := QueryPlanExecution{QueryPlan: executed, Result: result, Evidence: evidence}
	verification := verifyQuestionResults(
		[]QueryPlan{plan}, []QueryPlanExecution{execution}, defaultQuestionBudgets(),
	)
	if verification.Status != "PASS" {
		t.Fatalf("valid evidence must pass: %+v", verification)
	}
	execution.Evidence.ResultHash = strings.Repeat("f", 64)
	verification = verifyQuestionResults(
		[]QueryPlan{plan}, []QueryPlanExecution{execution}, defaultQuestionBudgets(),
	)
	if verification.Status != "BLOCKED" {
		t.Fatalf("tampered result hash must be blocked: %+v", verification)
	}
}

func TestNarratorUsesOnlyVerifiedResultSlots(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	execution := QueryPlanExecution{
		QueryPlan: plan,
		Result: dataset.PreviewResult{
			Columns: []string{"paid_gmv"}, Rows: [][]any{{123.45}}, RowCount: 1,
		},
	}
	answer := renderVerifiedQuestionAnswer([]QueryPlanExecution{execution}, QuestionDisplay{})
	if !strings.Contains(answer.Text, "123.45") || answer.Chart.Type != "KPI" {
		t.Fatalf("unexpected deterministic answer: %+v", answer)
	}
}

func TestQuestionToolRegistryEnforcesStateAndClosedSchema(t *testing.T) {
	definition, ok := defaultQuestionToolRegistry.Definition("execute_query_plan")
	if !ok || len(definition.Parameters) == 0 {
		t.Fatal("execute_query_plan must have a registered schema")
	}
	if !defaultQuestionToolRegistry.Allowed("execute_query_plan", QuestionStateExecuting) ||
		defaultQuestionToolRegistry.Allowed("execute_query_plan", QuestionStateContextReady) {
		t.Fatal("warehouse execution tool must only be allowed during EXECUTING")
	}
	if defaultQuestionToolRegistry.Contains("run_arbitrary_sql") {
		t.Fatal("arbitrary SQL must never appear in the Question Tool Registry")
	}
}

func TestQuestionToolRegistryContainsCompleteCanonicalSurface(t *testing.T) {
	canonical := []string{
		"search_semantic_objects", "get_semantic_contracts",
		"lookup_dimension_values", "get_certified_examples",
		"validate_semantic_bundle", "get_data_quality_status",
		"compile_semantic_query", "validate_query_plan", "explain_query_plan",
		"probe_join_cardinality", "execute_query_plan",
		"execute_validation_query", "compare_candidate_results",
		"request_clarification",
	}
	for _, name := range canonical {
		if !defaultQuestionToolRegistry.Contains(name) {
			t.Fatalf("canonical Tool Host contract missing %s", name)
		}
	}
}

func TestGovernedResultVerifierPinsSemanticContentHashAndTypes(t *testing.T) {
	plan := readyQuestionPlan("paid_gmv", 7)
	queryID := uuid.NewString()
	result := dataset.PreviewResult{
		QueryID: queryID, Columns: []string{"paid_gmv"},
		ColumnMetadata: []dataset.PreviewColumn{{
			FieldID: "paid_gmv", Code: "paid_gmv", CanonicalType: "DECIMAL",
		}},
		Rows: [][]any{{123.45}}, RowCount: 1,
	}
	executed := plan
	executed.Status = "EXECUTED"
	evidence, err := buildAnswerEvidence(executed, result, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	evidence.SemanticVersion = "release-v1"
	evidence.SemanticContentHash = strings.Repeat("1", 64)
	execution := QueryPlanExecution{QueryPlan: executed, Result: result, Evidence: evidence}
	verification := verifyQuestionResultsForSemanticSnapshot(
		[]QueryPlan{plan}, []QueryPlanExecution{execution}, defaultQuestionBudgets(),
		"release-v1", strings.Repeat("1", 64),
	)
	if verification.Status != "PASS" {
		t.Fatalf("governed result must pass: %+v", verification)
	}
	execution.Evidence.SemanticContentHash = strings.Repeat("2", 64)
	verification = verifyQuestionResultsForSemanticSnapshot(
		[]QueryPlan{plan}, []QueryPlanExecution{execution}, defaultQuestionBudgets(),
		"release-v1", strings.Repeat("1", 64),
	)
	if verification.Status != "BLOCKED" {
		t.Fatalf("semantic content drift must be blocked: %+v", verification)
	}
}

func TestQuestionRouterUsesSemanticPathAndFailClosedLongTailPolicy(t *testing.T) {
	decision := routeQuestion(QueryTurnPlan{
		State: QuestionStatePlanReady,
		Plans: []QueryPlan{readyQuestionPlan("paid_gmv", 7)},
	})
	if decision.Selected != QuestionRouteSemantic {
		t.Fatalf("ready semantic plan must use path A: %+v", decision)
	}
	var textSQL QuestionRouteCapability
	for _, capability := range decision.Capabilities {
		if capability.Route == QuestionRouteGovernedTextSQL {
			textSQL = capability
		}
	}
	if textSQL.Enabled || textSQL.ReasonCode == "" {
		t.Fatalf("path B must fail closed without a reliable AST adapter: %+v", textSQL)
	}
	clarify := routeQuestion(QueryTurnPlan{
		State:         QuestionStateClarificationRequired,
		Clarification: &QueryClarification{Type: "METRIC", Message: "choose"},
	})
	if clarify.Selected != QuestionRouteClarifyOrRefuse {
		t.Fatalf("clarification must use path C: %+v", clarify)
	}
}
