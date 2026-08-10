package validator

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/evaluation"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

func TestEvaluateResultRulesAcceptsValidatedMetricResult(t *testing.T) {
	request := resultRuleFixture(t, decimalResultRows("10.5"))
	artifact, err := EvaluateResultRules(request)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Passed || artifact.NoDataConfirmed || artifact.RequiresAnomalyAnalysis {
		t.Fatalf("unexpected rule artifact: %#v", artifact)
	}
	for _, code := range []string{
		"RESULT_SCHEMA_VALID", "RESULT_ROWS_UNIQUE", "RESULT_KEYS_UNIQUE", "RESULT_NULL_POLICY",
		"RESULT_FANOUT", "DIVISION_BY_ZERO", "DATA_FRESHNESS", "TIME_COVERAGE", "QUALITY_STATUS",
	} {
		check := resultRuleCheck(t, artifact, code)
		if !check.Passed {
			t.Fatalf("%s did not pass: %#v", code, check)
		}
	}
}

func TestNormalizeResultColumnsCarriesPinnedAdditivityAndExactTotal(t *testing.T) {
	query, ctx := liveQueryArtifactWithAdditivity(
		t, "dws_sales_orders",
		json.RawMessage(`{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"}`),
		registry.NonAdditive,
	)
	request := resultRuleFixtureForQuery(t, query, ctx, decimalResultRows("0.1"))
	withoutTotal, err := NormalizeResultColumns(query, request.Execution, nil)
	if err != nil {
		t.Fatal(err)
	}
	column := withoutTotal.Plans[0].Columns[0]
	if column.Role != "METRIC" || column.MetricVersionID != "metric-sales-v1" ||
		column.Additivity != registry.NonAdditive || !column.TotalsNotSummable ||
		column.RecomputedTotal != nil || column.Unit != "CNY" || column.DisplayPrecision != 4 {
		t.Fatalf("unexpected normalized result column: %#v", column)
	}

	exact := "0.300000000000000000000000000000000001"
	withTotal, err := NormalizeResultColumns(query, request.Execution, RecomputedTotalValues{
		compiler.QueryRoleCurrent: {"metric-sales-v1": exact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if total := withTotal.Plans[0].Columns[0].RecomputedTotal; total == nil || *total != exact {
		t.Fatalf("exact recomputed total = %v", total)
	}
}

func TestPlanRecomputedTotalsChargesBudgetAndFailsClosed(t *testing.T) {
	query, _ := liveQueryArtifactWithAdditivity(
		t, "dws_sales_orders",
		json.RawMessage(`{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"}`),
		registry.NonAdditive,
	)
	planned, err := PlanRecomputedTotals(query, true, ValidationQueryBudget{MaxQueries: 3, UsedQueries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Queries) != 1 || planned.ValidationQueriesUsed != 3 || planned.BudgetExhausted {
		t.Fatalf("unexpected total validation plan: %#v", planned)
	}
	validationQuery := planned.Queries[0]
	if len(validationQuery.Plan.Document.GroupBy) != 0 || len(validationQuery.Plan.Document.Fields) != 1 {
		t.Fatalf("total query retained display grain: %#v", validationQuery.Plan.Document)
	}
	compiled, live := validationQuery.Plan.CompiledQuery()
	if !live || strings.Contains(compiled.SQL, " GROUP BY ") {
		t.Fatalf("total query is not executable and ungrouped: live=%v sql=%s", live, compiled.SQL)
	}
	values, err := ParseRecomputedTotalRow(validationQuery,
		[]ExecutionColumn{{Name: validationQuery.ColumnNames[0], DataTypeOID: pgtype.NumericOID}},
		[][]any{{"0.300000000000000000000000000000000001"}},
	)
	if err != nil || values["metric-sales-v1"] != "0.300000000000000000000000000000000001" {
		t.Fatalf("parsed total = %#v, %v", values, err)
	}

	exhausted, err := PlanRecomputedTotals(query, true, ValidationQueryBudget{MaxQueries: 3, UsedQueries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(exhausted.Queries) != 0 || !exhausted.BudgetExhausted || exhausted.ValidationQueriesUsed != 3 {
		t.Fatalf("exhausted budget did not hide unsafe totals: %#v", exhausted)
	}
	if _, err := ParseRecomputedTotalRow(validationQuery,
		[]ExecutionColumn{{Name: validationQuery.ColumnNames[0], DataTypeOID: pgtype.NumericOID}},
		[][]any{{float64(0.3)}},
	); !errors.Is(err, ErrInvalidResultContract) {
		t.Fatalf("floating recomputed total error = %v", err)
	}
}

func TestEvaluateResultRulesRejectsFanoutAndDuplicateRows(t *testing.T) {
	request := resultRuleFixture(t, decimalResultRows("10", "10"))
	artifact, err := EvaluateResultRules(request)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Passed || !artifact.RequiresAnomalyAnalysis ||
		resultRuleCheck(t, artifact, "RESULT_ROWS_UNIQUE").Passed ||
		resultRuleCheck(t, artifact, "RESULT_KEYS_UNIQUE").Passed ||
		resultRuleCheck(t, artifact, "RESULT_FANOUT").Passed {
		t.Fatalf("duplicate fanout result was accepted: %#v", artifact)
	}
}

func TestEvaluateResultRulesDistinguishesConfirmedNoData(t *testing.T) {
	request := resultRuleFixture(t, [][]any{})
	request.Evidence.Empty = []EmptyResultEvidence{{
		Role: compiler.QueryRoleCurrent, MembersExist: true, TimeHasData: false, PermissionPruned: false,
		Evidence: resultEvidenceRef("empty-current", askdata.EvidenceKindDataQuality),
	}}
	artifact, err := EvaluateResultRules(request)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Passed || !artifact.NoDataConfirmed || !artifact.RequiresAnomalyAnalysis ||
		!resultRuleCheck(t, artifact, "EMPTY_RESULT_CURRENT").Passed {
		t.Fatalf("confirmed no-data result was not preserved: %#v", artifact)
	}

	permissionPruned := resultRuleFixture(t, [][]any{})
	permissionPruned.Evidence.Empty = []EmptyResultEvidence{{
		Role: compiler.QueryRoleCurrent, MembersExist: true, TimeHasData: false, PermissionPruned: true,
		Evidence: resultEvidenceRef("empty-permission", askdata.EvidenceKindDataQuality),
	}}
	rejected, err := EvaluateResultRules(permissionPruned)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Passed || rejected.NoDataConfirmed || resultRuleCheck(t, rejected, "EMPTY_RESULT_CURRENT").Passed {
		t.Fatalf("permission-pruned empty result was reported as no data: %#v", rejected)
	}
}

func TestEvaluateResultRulesRequiresExactWarehouseType(t *testing.T) {
	request := resultRuleFixture(t, decimalResultRows("12.5"))
	// The artifact hash includes per-plan metadata. Rebuild through the execution
	// boundary so this fixture is valid except for DECIMAL -> float8 compatibility.
	executionRequest, _ := validatedExecutionRequest(t)
	executionRequest.Query = request.Query
	columnName := request.Query.Plans[0].Document.Fields[0].Code
	result, err := buildExecutionResult(executionRequest, []executedPlan{testExecutedPlan(
		t, compiler.QueryRoleCurrent,
		[]ExecutionColumn{{Name: columnName, DataTypeOID: pgtype.Float8OID}},
		[][]any{{12.5}},
	)})
	if err != nil {
		t.Fatal(err)
	}
	request.Execution = result
	artifact, err := EvaluateResultRules(request)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Passed || resultRuleCheck(t, artifact, "RESULT_SCHEMA_VALID").Passed {
		t.Fatalf("inexact float warehouse type was accepted for DECIMAL: %#v", artifact)
	}
}

func TestEvaluateResultRulesChecksNullDivisionFreshnessAndQuality(t *testing.T) {
	nullRequest := resultRuleFixture(t, [][]any{{nil}})
	nullArtifact, err := EvaluateResultRules(nullRequest)
	if err != nil {
		t.Fatal(err)
	}
	if nullArtifact.Passed || resultRuleCheck(t, nullArtifact, "RESULT_NULL_POLICY").Passed {
		t.Fatalf("NULL result key was accepted: %#v", nullArtifact)
	}

	divisionQuery, divisionContext := liveQueryArtifactForSourceAndMetricFormula(
		t, "dws_sales_orders",
		json.RawMessage(`{"arguments":[{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"},{"type":"LITERAL","value":2}],"type":"DIVIDE"}`),
	)
	divisionRequest := resultRuleFixtureForQuery(t, divisionQuery, divisionContext, decimalResultRows("5"))
	divisionRequest.Evidence.Division = &DivisionEvidence{
		ZeroDenominatorCount: 1,
		Evidence:             resultEvidenceRef("division-zero-count", askdata.EvidenceKindDataQuality),
	}
	divisionArtifact, err := EvaluateResultRules(divisionRequest)
	if err != nil {
		t.Fatal(err)
	}
	divisionCheck := resultRuleCheck(t, divisionArtifact, "DIVISION_BY_ZERO")
	if divisionArtifact.Passed || divisionCheck.Passed || divisionCheck.Count != 1 {
		t.Fatalf("division-by-zero evidence was accepted: %#v", divisionArtifact)
	}

	stale := resultRuleFixture(t, decimalResultRows("10"))
	stale.Evidence.Freshness.DataAsOf = "2026-08-05T00:00:00Z"
	stale.Evidence.Quality.Status = QualityFail
	staleArtifact, err := EvaluateResultRules(stale)
	if err != nil {
		t.Fatal(err)
	}
	if staleArtifact.Passed || resultRuleCheck(t, staleArtifact, "DATA_FRESHNESS").Passed ||
		resultRuleCheck(t, staleArtifact, "QUALITY_STATUS").Passed {
		t.Fatalf("stale or failed-quality evidence was accepted: %#v", staleArtifact)
	}
}

func TestCoverageContainsRequestedRange(t *testing.T) {
	requested := ir.TimeRange{
		DimensionVersionID: "dimension-order-date-v1", Start: "2026-01-01",
		EndExclusive: "2026-08-01", Timezone: "Asia/Shanghai",
	}
	if !coverageContains(CoverageEvidence{Start: "2025-12-01", EndExclusive: "2026-09-01"}, requested) {
		t.Fatal("covering evidence was rejected")
	}
	if coverageContains(CoverageEvidence{Start: "2026-02-01", EndExclusive: "2026-09-01"}, requested) {
		t.Fatal("partial time coverage was accepted")
	}
}

func TestResultVerifierPreventsLLMFromOverridingRules(t *testing.T) {
	rules := resultRuleFixture(t, decimalResultRows("10", "20"))
	reviewer := &resultReviewerFixture{resultSource: askdata.ID(rules.Execution.Artifact.RunID), verdict: cognition.VerificationPass}
	verifier, err := NewResultVerifier(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := verifier.Verify(context.Background(), VerificationRequest{
		Rules: rules, Conversation: resultConversationFact(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.RuleArtifact.Passed || artifact.FinalVerdict != FinalVerificationRetry ||
		!artifact.RuleOverridePrevented || artifact.Anomaly == nil {
		t.Fatalf("LLM overrode deterministic rules: %#v", artifact)
	}
	if len(reviewer.stages) != 2 || reviewer.stages[0] != cognition.StageAnomalyAnalysis ||
		reviewer.stages[1] != cognition.StageResultVerification {
		t.Fatalf("unexpected cognition stages: %#v", reviewer.stages)
	}
}

func TestResultVerifierUsesSanitizedSummaryAndRejectsInventedEvidence(t *testing.T) {
	rules := resultRuleFixture(t, decimalResultRows("30.75"))
	reviewer := &resultReviewerFixture{resultSource: askdata.ID(rules.Execution.Artifact.RunID), verdict: cognition.VerificationPass}
	verifier, err := NewResultVerifier(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := verifier.Verify(context.Background(), VerificationRequest{
		Rules: rules, Conversation: resultConversationFact(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.FinalVerdict != FinalVerificationPass || artifact.Anomaly != nil || len(reviewer.stages) != 1 {
		t.Fatalf("unexpected verification result: %#v stages=%#v", artifact, reviewer.stages)
	}
	if strings.Contains(reviewer.prompts[0], `"rows"`) || strings.Contains(reviewer.prompts[0], `"sql"`) ||
		strings.Contains(reviewer.prompts[0], `"args"`) {
		t.Fatalf("result verifier prompt leaked an executable or row-level payload: %s", reviewer.prompts[0])
	}

	inventing := &resultReviewerFixture{
		resultSource: askdata.ID(rules.Execution.Artifact.RunID), verdict: cognition.VerificationPass, inventEvidence: true,
	}
	blockedVerifier, err := NewResultVerifier(inventing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockedVerifier.Verify(context.Background(), VerificationRequest{
		Rules: rules, Conversation: resultConversationFact(t),
	}); !errors.Is(err, ErrLLMResultVerification) {
		t.Fatalf("invented evidence error = %v", err)
	}
}

func TestSummarizeTrendFlagsAbnormalComparison(t *testing.T) {
	artifact, _ := liveQueryArtifact(t)
	columnName := artifact.Plans[0].Document.Fields[0].Code
	current := normalizedMetricResult(t, columnName, "10")
	baseline := normalizedMetricResult(t, columnName, "1")
	baselinePlan := artifact.Plans[0]
	baselinePlan.Role = compiler.QueryRoleBaseline
	artifact.Plans = append(artifact.Plans, baselinePlan)
	artifact.Comparison = &compiler.ComparisonContract{Type: ir.ComparisonPeriodOverPeriod, Periods: 1}
	trend := summarizeTrend(ResultRuleRequest{Query: artifact}, map[compiler.QueryRole]evaluation.NormalizedResult{
		compiler.QueryRoleCurrent: current, compiler.QueryRoleBaseline: baseline,
	}, 5)
	if !trend.Available || !trend.Anomalous || trend.ComparedMetrics != 1 ||
		trend.IncreasedMetrics != 1 || trend.MaxRelativeChange != 9 {
		t.Fatalf("unexpected abnormal trend: %#v", trend)
	}
}

type resultReviewerFixture struct {
	resultSource   askdata.ID
	verdict        cognition.VerificationVerdict
	inventEvidence bool
	stages         []cognition.Stage
	prompts        []string
}

func (reviewer *resultReviewerFixture) Execute(
	_ context.Context,
	request cognition.RoundRequest,
) (cognition.RoundResult, error) {
	reviewer.stages = append(reviewer.stages, request.Stage)
	if len(request.Messages) != 2 || len(request.Messages[1].Parts) != 1 {
		return cognition.RoundResult{}, errors.New("unexpected verification prompt")
	}
	prompt := request.Messages[1].Parts[0].Text
	reviewer.prompts = append(reviewer.prompts, prompt)
	var envelope struct {
		Facts []struct {
			EvidenceID  askdata.ID          `json:"evidenceId"`
			Kind        cognition.FactKind  `json:"kind"`
			ContentHash askdata.ContentHash `json:"contentHash"`
		} `json:"untrustedFacts"`
	}
	if err := json.Unmarshal([]byte(prompt), &envelope); err != nil {
		return cognition.RoundResult{}, err
	}
	var evidence askdata.EvidenceRef
	for _, fact := range envelope.Facts {
		if fact.Kind == cognition.FactQueryResultSummary {
			evidence = askdata.EvidenceRef{
				EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindQueryResult,
				SourceID: reviewer.resultSource, ContentHash: fact.ContentHash,
			}
			break
		}
	}
	if reviewer.inventEvidence {
		evidence.EvidenceID = "invented-result-evidence"
	}
	action := cognition.Action{
		SchemaVersion: cognition.SchemaVersion, Stage: request.Stage,
		DecisionSummary: "基于已给定的结果摘要和规则证据完成核验。", EvidenceRefs: []askdata.EvidenceRef{evidence},
	}
	switch request.Stage {
	case cognition.StageAnomalyAnalysis:
		action.Action = cognition.ActionAnalyzeAnomaly
		action.AnomalyAnalysis = &cognition.AnomalyAnalysis{
			Category: cognition.AnomalyData, Summary: "结果形状或数据证据存在异常，需要按受限流程复核。",
			RecommendedAction: cognition.RecommendRetryValidate, EvidenceRefs: []askdata.EvidenceRef{evidence},
		}
	case cognition.StageResultVerification:
		action.Action = cognition.ActionVerifyResult
		action.Verification = &cognition.Verification{
			Verdict: reviewer.verdict, Summary: "结果摘要与原问题的指标口径一致。",
			Checks: []cognition.VerificationCheck{{
				Code: "RESULT_ANSWERS_QUESTION", Passed: true, EvidenceRefs: []askdata.EvidenceRef{evidence},
			}},
		}
	default:
		return cognition.RoundResult{}, errors.New("unexpected cognition stage")
	}
	return cognition.RoundResult{Action: action}, nil
}

func resultRuleFixture(t *testing.T, rows [][]any) ResultRuleRequest {
	t.Helper()
	query, ctx := liveQueryArtifact(t)
	return resultRuleFixtureForQuery(t, query, ctx, rows)
}

func resultRuleFixtureForQuery(
	t *testing.T,
	query compiler.QueryArtifact,
	ctx context.Context,
	rows [][]any,
) ResultRuleRequest {
	t.Helper()
	validator, err := NewValidator(&recordingExplainer{raw: safeExplainJSON()}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	validation, err := validator.Validate(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	executionRequest := ExecutionRequest{RunID: newExecutionRunID(), Query: query, Validation: validation}
	columnName := executionRequest.Query.Plans[0].Document.Fields[0].Code
	result, err := buildExecutionResult(executionRequest, []executedPlan{testExecutedPlan(
		t, compiler.QueryRoleCurrent,
		[]ExecutionColumn{{Name: columnName, DataTypeOID: pgtype.NumericOID}}, rows,
	)})
	if err != nil {
		t.Fatal(err)
	}
	buildRequest, err := testfixture.SemanticMetricBuildRequest()
	if err != nil {
		t.Fatal(err)
	}
	buildArtifact, err := ir.Build(buildRequest)
	if err != nil {
		t.Fatal(err)
	}
	if buildArtifact.IRHash != executionRequest.Query.IRHash {
		t.Fatal("semantic IR fixture does not match query artifact")
	}
	return ResultRuleRequest{
		Query: executionRequest.Query, IR: buildArtifact.IR, Execution: result,
		Evidence: ResultEvidence{
			Freshness: FreshnessEvidence{
				DataAsOf: "2026-08-06T00:00:00Z", ObservedAt: "2026-08-06T00:05:00Z", MaxAgeSeconds: 3600,
				Evidence: resultEvidenceRef("freshness-current", askdata.EvidenceKindDataQuality),
			},
			Quality: QualityEvidence{
				Status: QualityPass, Evidence: resultEvidenceRef("quality-current", askdata.EvidenceKindDataQuality),
				Checks: []QualityCheckEvidence{{
					Code: "ROW_COUNT_SANITY", Severity: RuleBlocking, Passed: true,
					Evidence: resultEvidenceRef("quality-row-count", askdata.EvidenceKindDataQuality),
				}},
			},
		},
	}
}

func newExecutionRunID() string {
	return "123e4567-e89b-42d3-a456-426614174000"
}

func decimalResultRows(values ...string) [][]any {
	rows := make([][]any, len(values))
	for index, value := range values {
		negative := strings.HasPrefix(value, "-")
		unsigned := strings.TrimPrefix(value, "-")
		parts := strings.Split(unsigned, ".")
		if len(parts) > 2 || len(parts) == 0 {
			panic("invalid decimal test value")
		}
		fraction := ""
		if len(parts) == 2 {
			fraction = parts[1]
		}
		integer, ok := new(big.Int).SetString(parts[0]+fraction, 10)
		if !ok {
			panic("invalid decimal test value")
		}
		if negative {
			integer.Neg(integer)
		}
		rows[index] = []any{pgtype.Numeric{Int: integer, Exp: int32(-len(fraction)), Valid: true}}
	}
	return rows
}

func resultEvidenceRef(id askdata.ID, kind askdata.EvidenceKind) askdata.EvidenceRef {
	return askdata.EvidenceRef{
		EvidenceID: id, Kind: kind, SourceID: "metric-sales-v1",
		ContentHash: askdata.HashBytes([]byte("result-evidence:" + string(id))),
	}
}

func resultRuleCheck(t *testing.T, artifact RuleArtifact, code string) RuleCheck {
	t.Helper()
	for _, check := range artifact.Checks {
		if check.Code == code {
			return check
		}
	}
	t.Fatalf("rule check %s not found in %#v", code, artifact.Checks)
	return RuleCheck{}
}

func resultConversationFact(t *testing.T) cognition.PromptFact {
	t.Helper()
	fact, err := cognition.NewPromptFact(
		"conversation-result-verification", cognition.FactConversation,
		json.RawMessage(`{"question":"销售额是多少？"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	return fact
}

func normalizedMetricResult(t *testing.T, columnName, value string) evaluation.NormalizedResult {
	t.Helper()
	result, err := evaluation.NormalizeResult(evaluation.ResultSchema{Columns: []evaluation.Column{{
		Name: columnName, Type: evaluation.ScalarDecimal,
	}}}, evaluation.ResultSet{Columns: []string{columnName}, Rows: [][]any{{value}}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
