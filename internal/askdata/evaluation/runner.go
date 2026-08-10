package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/testfixture"
)

const FixtureRegressionVersion = "fixture-regression-v1"

var (
	ErrInvalidFixtureRunner = errors.New("fixture regression runner is invalid")
	regressionCodePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	fixtureYearPattern      = regexp.MustCompile(`([0-9]{4})年`)
)

// FailureStage identifies the first deterministic boundary at which a case
// diverged from its gold contract. The values are intentionally stable because
// later evaluation gates aggregate them without inspecting free-form errors.
type FailureStage string

const (
	FailureStageIntent     FailureStage = "INTENT"
	FailureStageRecall     FailureStage = "RECALL"
	FailureStageBinding    FailureStage = "BINDING"
	FailureStageGraph      FailureStage = "GRAPH"
	FailureStageIR         FailureStage = "IR"
	FailureStagePlan       FailureStage = "PLAN"
	FailureStageExecution  FailureStage = "EXECUTION"
	FailureStageValidation FailureStage = "VALIDATION"
	FailureStageSecurity   FailureStage = "SECURITY"
)

var fixtureFailureStages = []FailureStage{
	FailureStageIntent,
	FailureStageRecall,
	FailureStageBinding,
	FailureStageGraph,
	FailureStageIR,
	FailureStagePlan,
	FailureStageExecution,
	FailureStageValidation,
	FailureStageSecurity,
}

type FixtureStageFailure struct {
	Stage FailureStage
	Code  string
}

func (failure *FixtureStageFailure) Error() string {
	if failure == nil {
		return "fixture stage failed"
	}
	return fmt.Sprintf("fixture stage %s failed with %s", failure.Stage, failure.Code)
}

func newFixtureStageFailure(stage FailureStage, code string) error {
	return &FixtureStageFailure{Stage: stage, Code: code}
}

// FixtureActual is the bounded, row-free outcome returned by an in-memory
// fixture pipeline. Result rows exist only long enough for EVAL-001 comparison
// and are never copied into FixtureCaseReport.
type FixtureActual struct {
	Disposition   testfixture.Disposition
	ReasonCode    string
	DecisionStage FailureStage
	IR            *ir.SemanticIR
	Result        *ResultSet
	SensitiveLeak bool
}

// FixturePipeline is deliberately smaller than the production Orchestrator.
// Implementations can replay deterministic adapters or inject a stage failure
// without requiring a model provider, registry database or warehouse.
type FixturePipeline interface {
	Execute(context.Context, testfixture.Set, testfixture.Question) (FixtureActual, error)
}

type FixtureRunner struct {
	pipeline FixturePipeline
}

func NewFixtureRunner(pipeline FixturePipeline) (*FixtureRunner, error) {
	if pipeline == nil {
		return nil, ErrInvalidFixtureRunner
	}
	return &FixtureRunner{pipeline: pipeline}, nil
}

type FixtureCaseReport struct {
	CaseID              askdata.ID               `json:"caseId"`
	Scenario            testfixture.ScenarioCode `json:"scenario"`
	ExpectedDisposition testfixture.Disposition  `json:"expectedDisposition"`
	ActualDisposition   testfixture.Disposition  `json:"actualDisposition,omitempty"`
	ExpectedReasonCode  string                   `json:"expectedReasonCode,omitempty"`
	ActualReasonCode    string                   `json:"actualReasonCode,omitempty"`
	Passed              bool                     `json:"passed"`
	FailureStage        FailureStage             `json:"failureStage,omitempty"`
	FailureCode         string                   `json:"failureCode,omitempty"`
	ExpectedIRHash      askdata.ContentHash      `json:"expectedIrHash,omitempty"`
	ActualIRHash        askdata.ContentHash      `json:"actualIrHash,omitempty"`
	ExpectedResultHash  askdata.ContentHash      `json:"expectedResultHash,omitempty"`
	ActualResultHash    askdata.ContentHash      `json:"actualResultHash,omitempty"`
	ComparisonHash      askdata.ContentHash      `json:"comparisonHash,omitempty"`
	Differences         []Difference             `json:"differences"`
}

type FixtureStageSummary struct {
	Stage         FailureStage `json:"stage"`
	Failures      int          `json:"failures"`
	FailedCaseIDs []askdata.ID `json:"failedCaseIds"`
}

type FixtureRegressionReport struct {
	SchemaVersion  string                `json:"schemaVersion"`
	FixtureVersion string                `json:"fixtureVersion"`
	Release        askdata.ReleaseRef    `json:"release"`
	TotalCases     int                   `json:"totalCases"`
	PassedCases    int                   `json:"passedCases"`
	FailedCases    int                   `json:"failedCases"`
	Cases          []FixtureCaseReport   `json:"cases"`
	Stages         []FixtureStageSummary `json:"stages"`
	ContentHash    askdata.ContentHash   `json:"contentHash"`
}

// Run evaluates a synthetic fixture in stable case-ID order. Invalid fixture
// structure is a runner error; a valid case that diverges produces a report
// with FailedCases > 0 so the CLI can return a regression exit status.
func (runner *FixtureRunner) Run(ctx context.Context, fixture testfixture.Set) (FixtureRegressionReport, error) {
	if runner == nil || runner.pipeline == nil || ctx == nil {
		return FixtureRegressionReport{}, ErrInvalidFixtureRunner
	}
	if err := fixture.Validate(); err != nil {
		return FixtureRegressionReport{}, fmt.Errorf("%w: %v", ErrInvalidFixtureRunner, err)
	}
	questions := append([]testfixture.Question(nil), fixture.Questions...)
	sort.Slice(questions, func(left, right int) bool {
		return questions[left].QuestionID < questions[right].QuestionID
	})
	if err := validateFixtureCaseIdentities(questions); err != nil {
		return FixtureRegressionReport{}, err
	}
	goldResults, err := indexFixtureResults(fixture.Results)
	if err != nil {
		return FixtureRegressionReport{}, err
	}
	for _, question := range questions {
		if question.ExpectedDisposition == testfixture.DispositionDirect {
			if _, exists := goldResults[question.QuestionID]; !exists {
				return FixtureRegressionReport{}, fmt.Errorf("%w: direct case %s has no gold result", ErrInvalidFixtureRunner, question.QuestionID)
			}
		}
	}

	report := FixtureRegressionReport{
		SchemaVersion:  FixtureRegressionVersion,
		FixtureVersion: fixture.FixtureVersion,
		Release:        fixture.Release,
		TotalCases:     len(questions),
		Cases:          make([]FixtureCaseReport, 0, len(questions)),
		Stages:         emptyFixtureStageSummaries(),
	}
	for _, question := range questions {
		if err := ctx.Err(); err != nil {
			return FixtureRegressionReport{}, err
		}
		actual, executionErr := runner.pipeline.Execute(ctx, fixture, question)
		if err := ctx.Err(); err != nil {
			return FixtureRegressionReport{}, err
		}
		caseReport := evaluateFixtureCase(question, goldResults[question.QuestionID], actual, executionErr)
		report.Cases = append(report.Cases, caseReport)
		if caseReport.Passed {
			report.PassedCases++
			continue
		}
		report.FailedCases++
		addFixtureStageFailure(report.Stages, caseReport.FailureStage, caseReport.CaseID)
	}
	report.ContentHash, err = fixtureReportHash(report)
	if err != nil {
		return FixtureRegressionReport{}, err
	}
	if err := report.Validate(); err != nil {
		return FixtureRegressionReport{}, err
	}
	return report, nil
}

func (report FixtureRegressionReport) Validate() error {
	if report.SchemaVersion != FixtureRegressionVersion || report.FixtureVersion == "" ||
		report.Release.Validate() != nil || report.Cases == nil || report.Stages == nil ||
		report.TotalCases != len(report.Cases) || report.PassedCases+report.FailedCases != report.TotalCases {
		return ErrInvalidFixtureRunner
	}
	passed, failed := 0, 0
	previousID := askdata.ID("")
	stageCounts := make(map[FailureStage]int, len(fixtureFailureStages))
	for index, result := range report.Cases {
		if result.CaseID.Validate() != nil || (index > 0 && result.CaseID <= previousID) ||
			result.Differences == nil || len(result.Differences) > MaxDifferences {
			return ErrInvalidFixtureRunner
		}
		previousID = result.CaseID
		if result.Passed {
			passed++
			if result.FailureStage != "" || result.FailureCode != "" {
				return ErrInvalidFixtureRunner
			}
		} else {
			failed++
			if !validFailureStage(result.FailureStage) || !regressionCodePattern.MatchString(result.FailureCode) {
				return ErrInvalidFixtureRunner
			}
			stageCounts[result.FailureStage]++
		}
	}
	if passed != report.PassedCases || failed != report.FailedCases || len(report.Stages) != len(fixtureFailureStages) {
		return ErrInvalidFixtureRunner
	}
	for index, summary := range report.Stages {
		if summary.Stage != fixtureFailureStages[index] || summary.Failures != len(summary.FailedCaseIDs) ||
			summary.Failures != stageCounts[summary.Stage] {
			return ErrInvalidFixtureRunner
		}
		for caseIndex, caseID := range summary.FailedCaseIDs {
			if caseID.Validate() != nil || (caseIndex > 0 && caseID <= summary.FailedCaseIDs[caseIndex-1]) {
				return ErrInvalidFixtureRunner
			}
		}
	}
	if report.ContentHash.Validate() != nil {
		return ErrInvalidFixtureRunner
	}
	expected, err := fixtureReportHash(report)
	if err != nil || expected != report.ContentHash {
		return ErrInvalidFixtureRunner
	}
	return nil
}

func evaluateFixtureCase(
	question testfixture.Question,
	gold testfixture.Result,
	actual FixtureActual,
	executionErr error,
) FixtureCaseReport {
	report := FixtureCaseReport{
		CaseID: question.QuestionID, Scenario: question.Scenario,
		ExpectedDisposition: question.ExpectedDisposition,
		ExpectedReasonCode:  question.ExpectedReasonCode,
		ActualDisposition:   actual.Disposition, ActualReasonCode: actual.ReasonCode,
		Differences: []Difference{},
	}
	if executionErr != nil {
		var stageFailure *FixtureStageFailure
		if errors.As(executionErr, &stageFailure) && validFailureStage(stageFailure.Stage) &&
			regressionCodePattern.MatchString(stageFailure.Code) {
			return failFixtureCase(report, stageFailure.Stage, stageFailure.Code)
		}
		return failFixtureCase(report, FailureStageValidation, "PIPELINE_FAILED")
	}
	if err := validateFixtureActual(actual); err != nil {
		return failFixtureCase(report, FailureStageValidation, "PIPELINE_OUTCOME_INVALID")
	}
	if actual.SensitiveLeak {
		return failFixtureCase(report, FailureStageSecurity, "SENSITIVE_DATA_LEAK")
	}
	if actual.Disposition != question.ExpectedDisposition {
		return failFixtureCase(report, actual.DecisionStage, "DISPOSITION_MISMATCH")
	}
	if actual.ReasonCode != question.ExpectedReasonCode {
		return failFixtureCase(report, actual.DecisionStage, "REASON_CODE_MISMATCH")
	}
	if question.ExpectedDisposition != testfixture.DispositionDirect {
		report.Passed = true
		return report
	}
	if question.ExpectedIR == nil {
		return failFixtureCase(report, FailureStageIR, "GOLD_IR_MISSING")
	}
	if actual.IR == nil {
		return failFixtureCase(report, FailureStageIR, "ACTUAL_IR_MISSING")
	}
	if actual.Result == nil {
		return failFixtureCase(report, FailureStageExecution, "ACTUAL_RESULT_MISSING")
	}
	schema, expectedResult := fixtureComparisonInput(gold)
	comparison, err := Compare(ComparisonRequest{
		Schema:         schema,
		Expected:       Artifact{IR: *question.ExpectedIR, Result: expectedResult},
		Actual:         Artifact{IR: *actual.IR, Result: *actual.Result},
		FloatTolerance: DefaultFloatTolerance(),
	})
	if err != nil {
		if _, _, _, irErr := ir.Canonicalize(*actual.IR); irErr != nil {
			return failFixtureCase(report, FailureStageIR, "ACTUAL_IR_INVALID")
		}
		return failFixtureCase(report, FailureStageExecution, "ACTUAL_RESULT_INVALID")
	}
	report.ExpectedIRHash, report.ActualIRHash = comparison.ExpectedIRHash, comparison.ActualIRHash
	report.ExpectedResultHash, report.ActualResultHash = comparison.ExpectedResultHash, comparison.ActualResultHash
	report.Differences = append([]Difference{}, comparison.Differences...)
	comparisonRaw, err := json.Marshal(comparison)
	if err != nil {
		return failFixtureCase(report, FailureStageValidation, "COMPARISON_ENCODING_FAILED")
	}
	report.ComparisonHash = askdata.HashBytes(comparisonRaw)
	if !comparison.IREquivalent {
		return failFixtureCase(report, FailureStageIR, "IR_STRICT_MISMATCH")
	}
	if !comparison.ResultEquivalent {
		return failFixtureCase(report, FailureStageValidation, "RESULT_NOT_EQUIVALENT")
	}
	report.Passed = true
	return report
}

func validateFixtureActual(actual FixtureActual) error {
	if !validDisposition(actual.Disposition) || !validFailureStage(actual.DecisionStage) {
		return ErrInvalidFixtureRunner
	}
	if actual.ReasonCode != "" && !regressionCodePattern.MatchString(actual.ReasonCode) {
		return ErrInvalidFixtureRunner
	}
	return nil
}

func failFixtureCase(report FixtureCaseReport, stage FailureStage, code string) FixtureCaseReport {
	if !validFailureStage(stage) {
		stage = FailureStageValidation
	}
	if !regressionCodePattern.MatchString(code) {
		code = "PIPELINE_FAILED"
	}
	report.Passed, report.FailureStage, report.FailureCode = false, stage, code
	return report
}

func fixtureComparisonInput(result testfixture.Result) (ResultSchema, ResultSet) {
	columns := make([]Column, len(result.Columns))
	for index, name := range result.Columns {
		columns[index] = Column{Name: name, Type: ScalarString, Key: index == 0}
	}
	rows := make([][]any, len(result.Rows))
	for rowIndex, row := range result.Rows {
		rows[rowIndex] = make([]any, len(row))
		for columnIndex, value := range row {
			rows[rowIndex][columnIndex] = value
		}
	}
	return ResultSchema{Columns: columns}, ResultSet{Columns: append([]string(nil), result.Columns...), Rows: rows}
}

func validateFixtureCaseIdentities(questions []testfixture.Question) error {
	for index, question := range questions {
		if index > 0 && question.QuestionID == questions[index-1].QuestionID {
			return fmt.Errorf("%w: duplicate question %s", ErrInvalidFixtureRunner, question.QuestionID)
		}
	}
	return nil
}

func indexFixtureResults(results []testfixture.Result) (map[askdata.ID]testfixture.Result, error) {
	indexed := make(map[askdata.ID]testfixture.Result, len(results))
	for _, result := range results {
		if _, duplicate := indexed[result.QuestionID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate result for %s", ErrInvalidFixtureRunner, result.QuestionID)
		}
		indexed[result.QuestionID] = result
	}
	return indexed, nil
}

func emptyFixtureStageSummaries() []FixtureStageSummary {
	result := make([]FixtureStageSummary, len(fixtureFailureStages))
	for index, stage := range fixtureFailureStages {
		result[index] = FixtureStageSummary{Stage: stage, FailedCaseIDs: []askdata.ID{}}
	}
	return result
}

func addFixtureStageFailure(summaries []FixtureStageSummary, stage FailureStage, caseID askdata.ID) {
	for index := range summaries {
		if summaries[index].Stage == stage {
			summaries[index].Failures++
			summaries[index].FailedCaseIDs = append(summaries[index].FailedCaseIDs, caseID)
			return
		}
	}
}

func fixtureReportHash(report FixtureRegressionReport) (askdata.ContentHash, error) {
	payload := struct {
		SchemaVersion  string                `json:"schemaVersion"`
		FixtureVersion string                `json:"fixtureVersion"`
		Release        askdata.ReleaseRef    `json:"release"`
		TotalCases     int                   `json:"totalCases"`
		PassedCases    int                   `json:"passedCases"`
		FailedCases    int                   `json:"failedCases"`
		Cases          []FixtureCaseReport   `json:"cases"`
		Stages         []FixtureStageSummary `json:"stages"`
	}{
		report.SchemaVersion, report.FixtureVersion, report.Release,
		report.TotalCases, report.PassedCases, report.FailedCases,
		report.Cases, report.Stages,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(raw), nil
}

func validFailureStage(stage FailureStage) bool {
	for _, allowed := range fixtureFailureStages {
		if stage == allowed {
			return true
		}
	}
	return false
}

func validDisposition(value testfixture.Disposition) bool {
	switch value {
	case testfixture.DispositionDirect, testfixture.DispositionClarify,
		testfixture.DispositionRefuse, testfixture.DispositionNoData:
		return true
	default:
		return false
	}
}

// DeterministicFixturePipeline is an executable synthetic baseline. It uses
// only the supplied fixture assets and controlled lexical/rule decisions; it
// never calls a model provider, PostgreSQL, a graph server or a warehouse.
type DeterministicFixturePipeline struct {
	ReferenceYear int
}

func NewDeterministicFixturePipeline() *DeterministicFixturePipeline {
	return &DeterministicFixturePipeline{ReferenceYear: 2026}
}

type fixtureMetricMatch struct {
	Metric     testfixture.Metric
	DomainID   askdata.ID
	Score      int
	Authorized bool
}

type fixtureMemberMatch struct {
	Member    testfixture.Member
	Dimension testfixture.Dimension
	Score     int
}

func (pipeline *DeterministicFixturePipeline) Execute(
	ctx context.Context,
	fixture testfixture.Set,
	question testfixture.Question,
) (FixtureActual, error) {
	if err := ctx.Err(); err != nil {
		return FixtureActual{}, err
	}
	if pipeline == nil || pipeline.ReferenceYear < 1970 || pipeline.ReferenceYear > 9998 ||
		strings.TrimSpace(question.Text) == "" || !utf8.ValidString(question.Text) {
		return FixtureActual{}, newFixtureStageFailure(FailureStageIntent, "INTENT_INVALID")
	}
	user, found := fixtureUser(fixture, question)
	if !found {
		return FixtureActual{}, newFixtureStageFailure(FailureStageSecurity, "ACTOR_SCOPE_MISSING")
	}
	domains := make(map[askdata.ID]bool, len(user.DomainIDs))
	for _, domainID := range user.DomainIDs {
		domains[domainID] = true
	}
	matches := fixtureMetricMatches(fixture, question.Text, domains)
	if len(matches) == 0 {
		return FixtureActual{}, newFixtureStageFailure(FailureStageRecall, "METRIC_RECALL_EMPTY")
	}
	bestGlobal, bestAuthorized := bestFixtureMetricScores(matches)
	if bestAuthorized == 0 || bestGlobal > bestAuthorized {
		return FixtureActual{
			Disposition: testfixture.DispositionRefuse,
			ReasonCode:  "SEMANTIC_OBJECT_FORBIDDEN", DecisionStage: FailureStageSecurity,
		}, nil
	}
	metric, ok := selectFixtureMetric(matches, bestAuthorized)
	if !ok {
		return FixtureActual{}, newFixtureStageFailure(FailureStageBinding, "METRIC_BINDING_AMBIGUOUS")
	}

	members := fixtureMemberMatches(fixture, question.Text, metric.Metric.ModelVersionID)
	member, memberDecision := selectFixtureMember(members)
	switch memberDecision {
	case "MEMBER_EXPIRED":
		return FixtureActual{
			Disposition: testfixture.DispositionClarify,
			ReasonCode:  memberDecision, DecisionStage: FailureStageBinding,
		}, nil
	case "MEMBER_DIMENSION_AMBIGUOUS":
		return FixtureActual{
			Disposition: testfixture.DispositionClarify,
			ReasonCode:  memberDecision, DecisionStage: FailureStageBinding,
		}, nil
	}

	if strings.Contains(question.Text, "商品") {
		unsafe, graphErr := fixtureProductFanout(fixture, metric.Metric.ModelVersionID)
		if graphErr != nil {
			return FixtureActual{}, graphErr
		}
		if unsafe {
			return FixtureActual{
				Disposition: testfixture.DispositionClarify,
				ReasonCode:  "JOIN_FANOUT_UNSAFE", DecisionStage: FailureStageGraph,
			}, nil
		}
	}

	semanticIR, err := pipeline.buildFixtureIR(fixture, question, metric.Metric, member)
	if err != nil {
		return FixtureActual{}, err
	}
	if err := semanticIR.Validate(); err != nil {
		return FixtureActual{}, newFixtureStageFailure(FailureStagePlan, "PLAN_INVALID")
	}
	result, ok := fixtureResult(fixture, question.QuestionID)
	if !ok {
		return FixtureActual{}, newFixtureStageFailure(FailureStageExecution, "FIXTURE_RESULT_MISSING")
	}
	_, actualResult := fixtureComparisonInput(result)
	if len(actualResult.Rows) == 0 {
		return FixtureActual{
			Disposition: testfixture.DispositionNoData,
			ReasonCode:  "TIME_RANGE_NO_DATA", DecisionStage: FailureStageExecution,
			IR: &semanticIR, Result: &actualResult,
		}, nil
	}
	return FixtureActual{
		Disposition:   testfixture.DispositionDirect,
		DecisionStage: FailureStageValidation,
		IR:            &semanticIR, Result: &actualResult,
	}, nil
}

func (pipeline *DeterministicFixturePipeline) buildFixtureIR(
	fixture testfixture.Set,
	question testfixture.Question,
	metric testfixture.Metric,
	member *fixtureMemberMatch,
) (ir.SemanticIR, error) {
	domainID, ok := fixtureModelDomain(fixture, metric.ModelVersionID)
	if !ok {
		return ir.SemanticIR{}, newFixtureStageFailure(FailureStageIR, "MODEL_DOMAIN_MISSING")
	}
	value := ir.SemanticIR{
		IRVersion:           ir.Version,
		SemanticReleaseID:   fixture.Release.ReleaseID,
		SemanticContentHash: fixture.Release.ContentHash,
		DomainID:            domainID,
		ModelVersionID:      metric.ModelVersionID,
		Metrics:             []ir.Metric{{MetricVersionID: metric.Version.VersionID, Alias: fixtureMetricAlias(metric)}},
		GroupBy:             []ir.GroupBy{}, Filters: []ir.Filter{}, Sort: []ir.Sort{{
			TargetType: ir.SortTargetMetric, TargetVersionID: metric.Version.VersionID,
			Direction: ir.SortDescending, Nulls: ir.NullsLast, RankBy: ir.RankByCurrentValue,
		}},
		Limit: 500, OtherPolicy: ir.OtherNone, TieBreaking: ir.TieIncludeAll,
	}
	if strings.Contains(question.Text, "按月") {
		dimension, ok := fixtureDimensionByName(fixture, metric.ModelVersionID, "统计月")
		if !ok {
			return ir.SemanticIR{}, newFixtureStageFailure(FailureStageIR, "MONTH_DIMENSION_MISSING")
		}
		month := ir.TimeGrainMonth
		value.GroupBy = append(value.GroupBy, ir.GroupBy{DimensionVersionID: dimension.Version.VersionID, Grain: &month})
	}
	if member != nil {
		value.Filters = append(value.Filters, ir.Filter{
			DimensionVersionID: member.Dimension.Version.VersionID,
			Operator:           ir.FilterIn,
			MemberVersionIDs:   []askdata.ID{member.Member.Version.VersionID},
		})
	}
	year, hasYear := fixtureQuestionYear(question.Text, pipeline.ReferenceYear)
	if hasYear {
		timeDimension, ok := fixtureDimensionByName(fixture, metric.ModelVersionID, "下单日期")
		if !ok {
			return ir.SemanticIR{}, newFixtureStageFailure(FailureStageIR, "TIME_DIMENSION_MISSING")
		}
		value.TimeRange = &ir.TimeRange{
			DimensionVersionID: timeDimension.Version.VersionID,
			Start:              fmt.Sprintf("%04d-01-01", year),
			EndExclusive:       fmt.Sprintf("%04d-01-01", year+1),
			Timezone:           "Asia/Shanghai",
		}
	}
	return ir.Normalize(value), nil
}

func fixtureModelDomain(fixture testfixture.Set, modelVersionID askdata.ID) (askdata.ID, bool) {
	for _, model := range fixture.Models {
		if model.Version.VersionID == modelVersionID {
			return model.DomainID, true
		}
	}
	return "", false
}

func fixtureUser(fixture testfixture.Set, question testfixture.Question) (testfixture.User, bool) {
	for _, user := range fixture.Users {
		if user.ActorID == question.ActorID && user.TenantID == question.TenantID {
			return user, true
		}
	}
	return testfixture.User{}, false
}

func fixtureMetricMatches(fixture testfixture.Set, text string, domains map[askdata.ID]bool) []fixtureMetricMatch {
	models := make(map[askdata.ID]testfixture.SemanticModel, len(fixture.Models))
	for _, model := range fixture.Models {
		models[model.Version.VersionID] = model
	}
	matches := make([]fixtureMetricMatch, 0)
	for _, metric := range fixture.Metrics {
		score := fixtureLexicalScore(text, append([]string{metric.Name}, metric.Aliases...)...)
		if score == 0 {
			continue
		}
		model, exists := models[metric.ModelVersionID]
		if !exists {
			continue
		}
		matches = append(matches, fixtureMetricMatch{
			Metric: metric, DomainID: model.DomainID, Score: score,
			Authorized: domains[model.DomainID],
		})
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].Score != matches[right].Score {
			return matches[left].Score > matches[right].Score
		}
		return matches[left].Metric.Version.VersionID < matches[right].Metric.Version.VersionID
	})
	return matches
}

func bestFixtureMetricScores(matches []fixtureMetricMatch) (int, int) {
	global, authorized := 0, 0
	for _, match := range matches {
		if match.Score > global {
			global = match.Score
		}
		if match.Authorized && match.Score > authorized {
			authorized = match.Score
		}
	}
	return global, authorized
}

func selectFixtureMetric(matches []fixtureMetricMatch, score int) (fixtureMetricMatch, bool) {
	selected := make([]fixtureMetricMatch, 0)
	for _, match := range matches {
		if match.Authorized && match.Score == score {
			selected = append(selected, match)
		}
	}
	if len(selected) != 1 {
		return fixtureMetricMatch{}, false
	}
	return selected[0], true
}

func fixtureMemberMatches(fixture testfixture.Set, text string, modelID askdata.ID) []fixtureMemberMatch {
	dimensions := make(map[askdata.ID]testfixture.Dimension, len(fixture.Dimensions))
	for _, dimension := range fixture.Dimensions {
		if dimension.ModelVersionID == modelID {
			dimensions[dimension.Version.VersionID] = dimension
		}
	}
	result := make([]fixtureMemberMatch, 0)
	for _, member := range fixture.Members {
		dimension, exists := dimensions[member.DimensionVersionID]
		if !exists {
			continue
		}
		score := fixtureLexicalScore(text, append([]string{member.Label}, member.Aliases...)...)
		if score > 0 {
			result = append(result, fixtureMemberMatch{Member: member, Dimension: dimension, Score: score})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score != result[right].Score {
			return result[left].Score > result[right].Score
		}
		return result[left].Member.Version.VersionID < result[right].Member.Version.VersionID
	})
	return result
}

func selectFixtureMember(matches []fixtureMemberMatch) (*fixtureMemberMatch, string) {
	if len(matches) == 0 {
		return nil, ""
	}
	best := matches[0].Score
	top := make([]fixtureMemberMatch, 0)
	for _, match := range matches {
		if match.Score != best {
			break
		}
		top = append(top, match)
	}
	for _, match := range top {
		if match.Member.Status == testfixture.MemberExpired {
			return nil, "MEMBER_EXPIRED"
		}
	}
	dimensions := make(map[askdata.ID]struct{}, len(top))
	for _, match := range top {
		dimensions[match.Dimension.Version.VersionID] = struct{}{}
	}
	if len(dimensions) != 1 || len(top) != 1 {
		return nil, "MEMBER_DIMENSION_AMBIGUOUS"
	}
	selected := top[0]
	return &selected, ""
}

func fixtureProductFanout(fixture testfixture.Set, sourceModelID askdata.ID) (bool, error) {
	var productModel askdata.ID
	for _, model := range fixture.Models {
		if strings.Contains(model.Name, "明细") {
			if productModel != "" && productModel != model.Version.VersionID {
				return false, newFixtureStageFailure(FailureStageGraph, "GRAPH_TARGET_AMBIGUOUS")
			}
			productModel = model.Version.VersionID
		}
	}
	if productModel == "" {
		return false, newFixtureStageFailure(FailureStageRecall, "PRODUCT_MODEL_MISSING")
	}
	for _, relationship := range fixture.Relationships {
		if relationship.FromModelVersionID == sourceModelID && relationship.ToModelVersionID == productModel {
			if !relationship.Certified {
				return false, newFixtureStageFailure(FailureStageGraph, "GRAPH_RELATIONSHIP_UNCERTIFIED")
			}
			return relationship.FanoutRisk, nil
		}
	}
	return false, newFixtureStageFailure(FailureStageGraph, "GRAPH_PATH_MISSING")
}

func fixtureDimensionByName(fixture testfixture.Set, modelID askdata.ID, name string) (testfixture.Dimension, bool) {
	var matches []testfixture.Dimension
	for _, dimension := range fixture.Dimensions {
		if dimension.ModelVersionID == modelID && dimension.Name == name {
			matches = append(matches, dimension)
		}
	}
	sort.Slice(matches, func(left, right int) bool {
		return matches[left].Version.VersionID < matches[right].Version.VersionID
	})
	if len(matches) != 1 {
		return testfixture.Dimension{}, false
	}
	return matches[0], true
}

func fixtureResult(fixture testfixture.Set, questionID askdata.ID) (testfixture.Result, bool) {
	var matches []testfixture.Result
	for _, result := range fixture.Results {
		if result.QuestionID == questionID {
			matches = append(matches, result)
		}
	}
	if len(matches) != 1 {
		return testfixture.Result{}, false
	}
	return matches[0], true
}

func fixtureMetricAlias(metric testfixture.Metric) string {
	objectID := string(metric.Version.ObjectID)
	switch {
	case strings.Contains(objectID, "net-amount"):
		return "net_sales"
	case strings.Contains(objectID, "order-count"):
		return "order_count"
	default:
		builder := strings.Builder{}
		for _, character := range strings.ToLower(objectID) {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
				builder.WriteRune(character)
			} else if builder.Len() > 0 {
				builder.WriteByte('_')
			}
		}
		alias := strings.Trim(builder.String(), "_")
		if alias == "" || alias[0] < 'a' || alias[0] > 'z' {
			return "metric_value"
		}
		if len(alias) > 64 {
			alias = alias[:64]
		}
		return alias
	}
}

func fixtureQuestionYear(text string, referenceYear int) (int, bool) {
	if strings.Contains(text, "今年") {
		return referenceYear, true
	}
	matches := fixtureYearPattern.FindStringSubmatch(text)
	if len(matches) != 2 {
		return 0, false
	}
	year, err := strconv.Atoi(matches[1])
	if err != nil || year < 1 || year > 9998 {
		return 0, false
	}
	return year, true
}

func fixtureLexicalScore(text string, terms ...string) int {
	best := 0
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" || !strings.Contains(text, term) {
			continue
		}
		if length := utf8.RuneCountInString(term); length > best {
			best = length
		}
	}
	return best
}
