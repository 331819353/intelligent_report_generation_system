package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/validator"
)

const BundleRunResultVersion = "bundle-run-result-v1"

type BundleOutcome string

const (
	BundleOutcomeAnswered BundleOutcome = "ANSWERED"
	BundleOutcomePartial  BundleOutcome = "PARTIAL"
)

type BundlePlanStatus string

const (
	BundlePlanSucceeded BundlePlanStatus = "SUCCEEDED"
	BundlePlanFailed    BundlePlanStatus = "FAILED"
	BundlePlanTimedOut  BundlePlanStatus = "TIMED_OUT"
)

type BundlePlanFailureCode string

const (
	BundleFailureCompile      BundlePlanFailureCode = "PLAN_COMPILE_FAILED"
	BundleFailureValidate     BundlePlanFailureCode = "PLAN_VALIDATION_FAILED"
	BundleFailureExecute      BundlePlanFailureCode = "PLAN_EXECUTION_FAILED"
	BundleFailureUnauthorized BundlePlanFailureCode = "PLAN_UNAUTHORIZED"
	BundleFailureTimeout      BundlePlanFailureCode = "PLAN_TIMEOUT"
	BundleFailureCanceled     BundlePlanFailureCode = "PLAN_CANCELED"
	BundleFailureUnknown      BundlePlanFailureCode = "PLAN_FAILED"
)

// BundlePlanFailure exposes only a stable public code. The wrapped error is
// available to trusted callers and logs, but is never copied into BundleRunResult.
type BundlePlanFailure struct {
	Code BundlePlanFailureCode
	Err  error
}

func (failure *BundlePlanFailure) Error() string {
	if failure == nil {
		return string(BundleFailureUnknown)
	}
	return string(failure.Code)
}

func (failure *BundlePlanFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Err
}

type BundlePlanArtifact struct {
	QueryPlanHash  askdata.ContentHash `json:"queryPlanHash"`
	ValidationHash askdata.ContentHash `json:"validationHash"`
	ResultHash     askdata.ContentHash `json:"resultHash"`
	RowCount       int                 `json:"rowCount"`
}

func (artifact BundlePlanArtifact) Validate() error {
	if artifact.QueryPlanHash.Validate() != nil || artifact.ValidationHash.Validate() != nil ||
		artifact.ResultHash.Validate() != nil || artifact.RowCount < 0 || artifact.RowCount > 20_000 {
		return fmt.Errorf("%w: bundle plan artifact", ErrInvalidRun)
	}
	return nil
}

type BundlePlanExecutionRequest struct {
	RunID         string
	Scope         askdata.PolicyScope
	SharedContext compiler.BundleSharedContext
	Plan          compiler.BundlePlan
}

// BundlePlanProcessor is deliberately one all-or-nothing operation per item:
// a successful return certifies that compile, Plan Validation and execution
// all completed for that exact SemanticIR.
type BundlePlanProcessor interface {
	CompileValidateExecute(context.Context, BundlePlanExecutionRequest) (BundlePlanArtifact, error)
}

type BundlePlanCompiler interface {
	CompileBundlePlan(
		context.Context,
		compiler.BundlePlanCompileRequest,
	) (compiler.QueryArtifact, *validator.CoverageVerdict, error)
}

type BundlePlanValidator interface {
	ValidateCovered(
		context.Context,
		compiler.QueryArtifact,
		validator.CoverageVerdict,
	) (validator.ValidationArtifact, error)
}

type BundlePlanExecutor interface {
	Execute(context.Context, validator.ExecutionRequest) (validator.ExecutionResult, error)
}

// BundlePipelineProcessor connects QUERY-009 to the existing compiler,
// validator and executor contracts without weakening any of their checks.
type BundlePipelineProcessor struct {
	compiler  BundlePlanCompiler
	validator BundlePlanValidator
	executor  BundlePlanExecutor
}

func NewBundlePipelineProcessor(
	planCompiler BundlePlanCompiler,
	planValidator BundlePlanValidator,
	planExecutor BundlePlanExecutor,
) (*BundlePipelineProcessor, error) {
	if planCompiler == nil || planValidator == nil || planExecutor == nil {
		return nil, fmt.Errorf("%w: bundle pipeline dependencies", ErrInvalidRun)
	}
	return &BundlePipelineProcessor{
		compiler: planCompiler, validator: planValidator, executor: planExecutor,
	}, nil
}

func (processor *BundlePipelineProcessor) CompileValidateExecute(
	ctx context.Context,
	request BundlePlanExecutionRequest,
) (BundlePlanArtifact, error) {
	if processor == nil || processor.compiler == nil || processor.validator == nil || processor.executor == nil ||
		ctx == nil || request.Scope.Validate() != nil || request.Plan.IRHash.Validate() != nil {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureCompile, Err: ErrInvalidRun}
	}
	query, coverage, err := processor.compiler.CompileBundlePlan(ctx, compiler.BundlePlanCompileRequest{
		Scope: request.Scope, SharedContext: request.SharedContext, Plan: request.Plan,
	})
	if err != nil {
		return BundlePlanArtifact{}, preserveBundlePlanFailure(err, BundleFailureCompile)
	}
	if query.Validate() != nil || !reflect.DeepEqual(query.Scope, request.Scope) ||
		query.DomainID != request.SharedContext.DomainID || query.IRHash != request.Plan.IRHash ||
		query.ResolvedTimeSpec == nil || !reflect.DeepEqual(*query.ResolvedTimeSpec, request.SharedContext.ResolvedTimeSpec) {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureCompile, Err: ErrInvalidRun}
	}

	if coverage == nil {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureValidate, Err: validator.ErrInvalidCoverage}
	}
	validation, err := processor.validator.ValidateCovered(ctx, query, *coverage)
	if err != nil {
		return BundlePlanArtifact{}, preserveBundlePlanFailure(err, BundleFailureValidate)
	}
	if validation.Validate() != nil || validation.QueryArtifactPlanHash != query.PlanHash ||
		!reflect.DeepEqual(validation.Scope, request.Scope) {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureValidate, Err: ErrInvalidRun}
	}

	planRunID, err := bundlePlanRunID(request.RunID, request.Plan.PlanID)
	if err != nil {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureExecute, Err: err}
	}
	execution, err := processor.executor.Execute(ctx, validator.ExecutionRequest{
		RunID: planRunID, Query: query, Validation: validation,
	})
	if err != nil {
		return BundlePlanArtifact{}, preserveBundlePlanFailure(err, BundleFailureExecute)
	}
	if execution.Artifact.Validate() != nil || execution.Artifact.QueryArtifactPlanHash != query.PlanHash ||
		execution.Artifact.ValidationHash != validation.ValidationHash {
		return BundlePlanArtifact{}, &BundlePlanFailure{Code: BundleFailureExecute, Err: ErrInvalidRun}
	}
	return BundlePlanArtifact{
		QueryPlanHash: query.PlanHash, ValidationHash: validation.ValidationHash,
		ResultHash: execution.Artifact.ResultHash, RowCount: execution.Artifact.TotalRows,
	}, nil
}

type BundlePlanResult struct {
	PlanID      askdata.ID            `json:"planId"`
	Role        string                `json:"role"`
	ChartType   string                `json:"chartType"`
	Status      BundlePlanStatus      `json:"status"`
	FailureCode BundlePlanFailureCode `json:"failureCode,omitempty"`
	Artifact    *BundlePlanArtifact   `json:"artifact,omitempty"`
}

type BundleRunResult struct {
	Version           string              `json:"version"`
	RunID             string              `json:"runId"`
	RunType           RunType             `json:"runType"`
	BundleHash        askdata.ContentHash `json:"bundleHash"`
	Outcome           BundleOutcome       `json:"outcome"`
	Plans             []BundlePlanResult  `json:"plans"`
	SucceededPlans    int                 `json:"succeededPlans"`
	FailedPlans       int                 `json:"failedPlans"`
	TimedOutPlans     int                 `json:"timedOutPlans"`
	BudgetConsumption BudgetConsumption   `json:"budgetConsumption"`
	ResultHash        askdata.ContentHash `json:"resultHash"`
}

type BundleRunRequest struct {
	RunID  string
	Bundle compiler.QueryPlanBundle
}

type bundleBudgetResolver interface {
	Resolve(askdata.ID, RunBudgetClass) (RunBudget, error)
}

type BundleRunner struct {
	processor BundlePlanProcessor
	budgets   bundleBudgetResolver
}

func NewBundleRunner(processor BundlePlanProcessor, budgets *BudgetCatalog) (*BundleRunner, error) {
	if processor == nil {
		return nil, fmt.Errorf("%w: bundle processor", ErrInvalidRun)
	}
	runner := &BundleRunner{processor: processor}
	if budgets != nil {
		runner.budgets = budgets
	}
	return runner, nil
}

// Run executes plans independently and preserves their source order. A plan
// error never cancels a sibling; the shared context deadline is the only
// fan-out cancellation boundary.
func (runner *BundleRunner) Run(ctx context.Context, request BundleRunRequest) (BundleRunResult, error) {
	if runner == nil || runner.processor == nil || ctx == nil || request.Bundle.Validate() != nil ||
		uuid.Validate(request.RunID) != nil {
		return BundleRunResult{}, fmt.Errorf("%w: bundle run request", ErrInvalidRun)
	}
	budget, err := runner.resolveBudget(request.Bundle.SharedContext.DomainID)
	if err != nil || len(request.Bundle.Plans) > budget.MaxPrimaryQueries ||
		request.Bundle.MaxConcurrentPlans > budget.MaxConcurrentPlans {
		return BundleRunResult{}, fmt.Errorf("%w: bundle run budget", ErrInvalidRun)
	}
	started := time.Now()
	runContext, cancel := context.WithTimeout(ctx, budget.HardTimeout)
	defer cancel()

	results := make([]BundlePlanResult, len(request.Bundle.Plans))
	group := errgroup.Group{}
	group.SetLimit(min(request.Bundle.MaxConcurrentPlans, budget.MaxConcurrentPlans))
	for index, plan := range request.Bundle.Plans {
		index, plan := index, plan
		group.Go(func() error {
			artifact, processErr := runner.processor.CompileValidateExecute(runContext, BundlePlanExecutionRequest{
				RunID: request.RunID, Scope: request.Bundle.Scope,
				SharedContext: request.Bundle.SharedContext, Plan: plan,
			})
			result := BundlePlanResult{PlanID: plan.PlanID, Role: plan.Role, ChartType: plan.ChartType}
			if processErr == nil && artifact.Validate() == nil {
				result.Status, result.Artifact = BundlePlanSucceeded, &artifact
			} else {
				if processErr == nil {
					processErr = &BundlePlanFailure{Code: BundleFailureExecute, Err: ErrInvalidRun}
				}
				result.Status, result.FailureCode = classifyBundlePlanFailure(processErr)
			}
			results[index] = result
			return nil
		})
	}
	_ = group.Wait()

	result := BundleRunResult{
		Version: BundleRunResultVersion, RunID: request.RunID, RunType: RunTypeBundle,
		BundleHash: request.Bundle.BundleHash, Plans: results,
	}
	for _, plan := range results {
		switch plan.Status {
		case BundlePlanSucceeded:
			result.SucceededPlans++
		case BundlePlanTimedOut:
			result.TimedOutPlans++
			result.FailedPlans++
		default:
			result.FailedPlans++
		}
	}
	if result.FailedPlans == 0 {
		result.Outcome = BundleOutcomeAnswered
	} else {
		result.Outcome = BundleOutcomePartial
	}
	elapsed := time.Since(started)
	usage := RunBudgetUsage{
		PrimaryQueriesUsed: len(request.Bundle.Plans), ElapsedMS: elapsed.Milliseconds(),
	}
	result.BudgetConsumption, err = SnapshotBudgetConsumption(budget, usage)
	if err != nil {
		return BundleRunResult{}, err
	}
	result.ResultHash, err = bundleRunResultHash(result)
	if err != nil || result.Validate() != nil {
		return BundleRunResult{}, fmt.Errorf("%w: bundle run result", ErrInvalidRun)
	}
	return result, nil
}

func (result BundleRunResult) Validate() error {
	if result.Version != BundleRunResultVersion || uuid.Validate(result.RunID) != nil ||
		result.RunType != RunTypeBundle || result.BundleHash.Validate() != nil ||
		result.ResultHash.Validate() != nil || len(result.Plans) < 1 || len(result.Plans) > compiler.MaxBundlePlans ||
		(result.Outcome != BundleOutcomeAnswered && result.Outcome != BundleOutcomePartial) {
		return fmt.Errorf("%w: bundle run result", ErrInvalidRun)
	}
	succeeded, failed, timedOut := 0, 0, 0
	for index, plan := range result.Plans {
		if plan.PlanID != askdata.ID(fmt.Sprintf("p%d", index+1)) ||
			(plan.Role != registry.KPIBundleRoleHeadline && plan.Role != registry.KPIBundleRoleTrend &&
				plan.Role != registry.KPIBundleRoleBreakdown) ||
			!registry.IsRegisteredComponentType(plan.ChartType) {
			return fmt.Errorf("%w: bundle plan result", ErrInvalidRun)
		}
		switch plan.Status {
		case BundlePlanSucceeded:
			if plan.FailureCode != "" || plan.Artifact == nil || plan.Artifact.Validate() != nil {
				return fmt.Errorf("%w: successful bundle plan", ErrInvalidRun)
			}
			succeeded++
		case BundlePlanFailed:
			if !validBundleFailureCode(plan.FailureCode) || plan.FailureCode == BundleFailureTimeout || plan.Artifact != nil {
				return fmt.Errorf("%w: failed bundle plan", ErrInvalidRun)
			}
			failed++
		case BundlePlanTimedOut:
			if plan.FailureCode != BundleFailureTimeout || plan.Artifact != nil {
				return fmt.Errorf("%w: timed-out bundle plan", ErrInvalidRun)
			}
			failed++
			timedOut++
		default:
			return fmt.Errorf("%w: bundle plan status", ErrInvalidRun)
		}
	}
	if succeeded != result.SucceededPlans || failed != result.FailedPlans || timedOut != result.TimedOutPlans ||
		(result.Outcome == BundleOutcomeAnswered) != (failed == 0) ||
		result.BudgetConsumption.RunType != RunTypeBundle ||
		result.BudgetConsumption.Usage.PrimaryQueriesUsed != len(result.Plans) {
		return fmt.Errorf("%w: bundle result totals", ErrInvalidRun)
	}
	expected, err := bundleRunResultHash(result)
	if err != nil || expected != result.ResultHash {
		return fmt.Errorf("%w: bundle result hash", ErrInvalidRun)
	}
	return nil
}

func (runner *BundleRunner) resolveBudget(domainID askdata.ID) (RunBudget, error) {
	if runner.budgets != nil {
		return runner.budgets.Resolve(domainID, BudgetClassBundle)
	}
	return DefaultRunBudget(BudgetClassBundle)
}

func preserveBundlePlanFailure(err error, fallback BundlePlanFailureCode) error {
	var failure *BundlePlanFailure
	if errors.As(err, &failure) && validBundleFailureCode(failure.Code) {
		return failure
	}
	return &BundlePlanFailure{Code: fallback, Err: err}
}

func classifyBundlePlanFailure(err error) (BundlePlanStatus, BundlePlanFailureCode) {
	if errors.Is(err, context.DeadlineExceeded) {
		return BundlePlanTimedOut, BundleFailureTimeout
	}
	if errors.Is(err, context.Canceled) {
		return BundlePlanFailed, BundleFailureCanceled
	}
	var failure *BundlePlanFailure
	if errors.As(err, &failure) && validBundleFailureCode(failure.Code) {
		if failure.Code == BundleFailureTimeout {
			return BundlePlanTimedOut, BundleFailureTimeout
		}
		return BundlePlanFailed, failure.Code
	}
	return BundlePlanFailed, BundleFailureUnknown
}

func validBundleFailureCode(code BundlePlanFailureCode) bool {
	switch code {
	case BundleFailureCompile, BundleFailureValidate, BundleFailureExecute,
		BundleFailureUnauthorized, BundleFailureTimeout, BundleFailureCanceled, BundleFailureUnknown:
		return true
	default:
		return false
	}
}

func bundlePlanRunID(runID string, planID askdata.ID) (string, error) {
	namespace, err := uuid.Parse(runID)
	if err != nil || planID.Validate() != nil {
		return "", ErrInvalidRun
	}
	return uuid.NewSHA1(namespace, []byte(planID)).String(), nil
}

func bundleRunResultHash(result BundleRunResult) (askdata.ContentHash, error) {
	payload := struct {
		Version           string              `json:"version"`
		RunID             string              `json:"runId"`
		RunType           RunType             `json:"runType"`
		BundleHash        askdata.ContentHash `json:"bundleHash"`
		Outcome           BundleOutcome       `json:"outcome"`
		Plans             []BundlePlanResult  `json:"plans"`
		SucceededPlans    int                 `json:"succeededPlans"`
		FailedPlans       int                 `json:"failedPlans"`
		TimedOutPlans     int                 `json:"timedOutPlans"`
		BudgetConsumption BudgetConsumption   `json:"budgetConsumption"`
	}{
		result.Version, result.RunID, result.RunType, result.BundleHash, result.Outcome,
		result.Plans, result.SucceededPlans, result.FailedPlans, result.TimedOutPlans,
		result.BudgetConsumption,
	}
	hash, _, err := registry.CanonicalContentHash(payload)
	return hash, err
}
