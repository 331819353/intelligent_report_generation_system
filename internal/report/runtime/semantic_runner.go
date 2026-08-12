package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/validator"
	"intelligent-report-generation-system/internal/report/store"
)

// SemanticRuntimeRunner rehydrates an immutable AskData plan under the
// viewer's current roles, repeats coverage and EXPLAIN validation, then uses
// the formal semantic executor. Report filter FieldRefs point to Dataset data
// contexts; certified semantic member filters remain inside the pinned IR.
type SemanticRuntimeRunner struct {
	artifacts  SemanticArtifactSource
	scopes     ViewerScopeResolver
	rehydrator *askcompiler.PinnedArtifactRehydrator
	coverage   *validator.CoverageControl
	validator  *validator.Validator
	executor   *validator.Executor
}

func NewSemanticRuntimeRunner(
	artifacts SemanticArtifactSource,
	scopes ViewerScopeResolver,
	rehydrator *askcompiler.PinnedArtifactRehydrator,
	coverage *validator.CoverageControl,
	planValidator *validator.Validator,
	planExecutor *validator.Executor,
) (*SemanticRuntimeRunner, error) {
	if artifacts == nil || scopes == nil || rehydrator == nil || coverage == nil ||
		planValidator == nil || planExecutor == nil {
		return nil, errors.New("semantic report runtime dependencies are incomplete")
	}
	return &SemanticRuntimeRunner{
		artifacts: artifacts, scopes: scopes, rehydrator: rehydrator,
		coverage: coverage, validator: planValidator, executor: planExecutor,
	}, nil
}

func (runner *SemanticRuntimeRunner) CompileAndExecuteSemanticIR(
	ctx context.Context,
	request SemanticExecutionRequest,
) (QueryResult, error) {
	identity, ok := ctx.Value(viewerIdentityKey{}).(store.Identity)
	if runner == nil || runner.artifacts == nil || runner.scopes == nil ||
		runner.rehydrator == nil || runner.coverage == nil || runner.validator == nil || runner.executor == nil ||
		!ok || identity.Validate() != nil || request.ReportID.Validate() != nil ||
		request.ReportVersionID.Validate() != nil ||
		(request.SourceRunID == "") == (request.CompilationArtifactID == "") ||
		(request.SourceRunID != "" && request.SourceRunID.Validate() != nil) ||
		(request.CompilationArtifactID != "" && request.CompilationArtifactID.Validate() != nil) ||
		request.ReleaseID.Validate() != nil || request.ContentHash.Validate() != nil ||
		request.FixedPlanHash.Validate() != nil || request.IR.Validate() != nil ||
		request.IR.DomainID != identity.DomainID || request.Limit < 1 {
		return QueryResult{}, errors.New("semantic report runtime request is invalid")
	}
	var persisted askcompiler.QueryArtifact
	var err error
	if request.CompilationArtifactID != "" {
		source, ok := runner.artifacts.(SemanticCompilationSource)
		if !ok {
			return QueryResult{}, NewError(
				"REPORT_SEMANTIC_ARTIFACT_INVALID", "semantic report compilation is unavailable", nil,
			)
		}
		persisted, err = source.LoadCompilationArtifact(
			ctx, identity, request.ReportVersionID, request.CompilationArtifactID, request.FixedPlanHash,
		)
	} else {
		var snapshot askcompiler.ReportQuerySnapshot
		snapshot, err = runner.artifacts.LoadQueryArtifact(
			ctx, identity, request.ReportVersionID, request.SourceRunID, request.FixedPlanHash,
		)
		if err == nil {
			if !reflect.DeepEqual(snapshot.ResolvedTimeSpec, request.ResolvedTimeSpec) {
				return QueryResult{}, NewError(
					"REPORT_SEMANTIC_ARTIFACT_INVALID", "semantic report time contract does not match its source artifact", nil,
				)
			}
			release := askdata.ReleaseRef{ReleaseID: request.ReleaseID, ContentHash: request.ContentHash}
			var scope askdata.PolicyScope
			scope, err = runner.scopes.ResolveViewerScope(ctx, identity, release)
			if err == nil {
				persisted, err = runner.rehydrator.RehydrateSnapshot(ctx, scope, request.IR, request.FixedPlanHash, snapshot)
			}
			if err == nil {
				return runner.executeLiveSemanticIR(ctx, identity, request, persisted)
			}
		}
	}
	if err != nil {
		return QueryResult{}, err
	}
	return runner.executePersistedSemanticIR(ctx, identity, request, persisted)
}

// ExecuteCompiledSemanticIR executes a preview-only compiler artifact without
// persisting it. The same current-viewer scope rebuild, deterministic
// rehydration, coverage check, EXPLAIN validation and governed execution used
// by published reports still apply.
func (runner *SemanticRuntimeRunner) ExecuteCompiledSemanticIR(
	ctx context.Context,
	request SemanticExecutionRequest,
	persisted askcompiler.QueryArtifact,
) (QueryResult, error) {
	identity, ok := ctx.Value(viewerIdentityKey{}).(store.Identity)
	if runner == nil || runner.scopes == nil || runner.rehydrator == nil || runner.coverage == nil ||
		runner.validator == nil || runner.executor == nil || !ok || identity.Validate() != nil ||
		request.ReportID.Validate() != nil || request.ReportVersionID.Validate() != nil ||
		request.SourceRunID != "" || request.CompilationArtifactID != "" ||
		request.ReleaseID.Validate() != nil || request.ContentHash.Validate() != nil ||
		request.FixedPlanHash.Validate() != nil || request.IR.Validate() != nil ||
		request.IR.DomainID != identity.DomainID || request.Limit < 1 ||
		persisted.Validate() != nil || persisted.PlanHash != request.FixedPlanHash {
		return QueryResult{}, errors.New("semantic report preview request is invalid")
	}
	return runner.executePersistedSemanticIR(ctx, identity, request, persisted)
}

func (runner *SemanticRuntimeRunner) executePersistedSemanticIR(
	ctx context.Context,
	identity store.Identity,
	request SemanticExecutionRequest,
	persisted askcompiler.QueryArtifact,
) (QueryResult, error) {
	if !reflect.DeepEqual(persisted.ResolvedTimeSpec, request.ResolvedTimeSpec) {
		return QueryResult{}, NewError(
			"REPORT_SEMANTIC_ARTIFACT_INVALID", "semantic report time contract does not match its source artifact", nil,
		)
	}
	release := askdata.ReleaseRef{ReleaseID: request.ReleaseID, ContentHash: request.ContentHash}
	scope, err := runner.scopes.ResolveViewerScope(ctx, identity, release)
	if err != nil {
		return QueryResult{}, err
	}
	live, err := runner.rehydrator.Rehydrate(ctx, scope, request.IR, request.FixedPlanHash, persisted)
	if err != nil {
		return QueryResult{}, NewError("NO_PERMISSION", "semantic report plan cannot be resolved for this viewer", err)
	}

	return runner.executeLiveSemanticIR(ctx, identity, request, live)
}

func (runner *SemanticRuntimeRunner) executeLiveSemanticIR(
	ctx context.Context,
	identity store.Identity,
	request SemanticExecutionRequest,
	live askcompiler.QueryArtifact,
) (QueryResult, error) {
	partial := false
	var validation validator.ValidationArtifact
	var err error
	if live.ResolvedTimeSpec != nil {
		materializationIDs, err := semanticMaterializationIDs(live)
		if err != nil {
			return QueryResult{}, err
		}
		verdict, err := runner.coverage.Evaluate(
			ctx, string(identity.TenantID), materializationIDs, *live.ResolvedTimeSpec,
		)
		if err != nil {
			return QueryResult{}, NewError(
				"REPORT_SEMANTIC_COVERAGE_UNAVAILABLE", "semantic report data coverage is unavailable", err,
			)
		}
		if verdict.Relation == validator.CoverageNone {
			return emptySemanticResult(), nil
		}
		partial = verdict.Relation == validator.CoverageTruncated
		validation, err = runner.validator.ValidateCovered(ctx, live, verdict)
		if err != nil {
			return QueryResult{}, NewError(
				"REPORT_SEMANTIC_PLAN_STALE", "semantic report plan requires republishing", err,
			)
		}
	} else {
		validation, err = runner.validator.Validate(ctx, live)
		if err != nil {
			return QueryResult{}, NewError(
				"REPORT_SEMANTIC_VALIDATION_FAILED", "semantic report plan validation failed", err,
			)
		}
	}
	executed, err := runner.executor.Execute(ctx, validator.ExecutionRequest{
		RunID: uuid.NewString(), Query: live, Validation: validation,
	})
	if err != nil {
		return QueryResult{}, NewError(
			"REPORT_SEMANTIC_EXECUTION_FAILED", "semantic report query execution failed", err,
		)
	}
	return semanticQueryResult(executed, request.Limit, partial)
}

func semanticMaterializationIDs(artifact askcompiler.QueryArtifact) ([]string, error) {
	seen := make(map[string]struct{}, len(artifact.Plans))
	result := make([]string, 0, len(artifact.Plans))
	for _, plan := range artifact.Plans {
		id := string(plan.Source.MaterializationID)
		if plan.Source.MaterializationID.Validate() != nil {
			return nil, errors.New("semantic report materialization is invalid")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, errors.New("semantic report has no materialization")
	}
	return result, nil
}

func emptySemanticResult() QueryResult {
	raw := []byte(`{"plans":[]}`)
	return QueryResult{Columns: []string{}, Rows: [][]any{}, Plans: []QueryPlanResult{}, Hash: askdata.HashBytes(raw)}
}

func semanticQueryResult(
	executed validator.ExecutionResult,
	limit int,
	partial bool,
) (QueryResult, error) {
	if executed.Artifact.Validate() != nil || limit < 1 {
		return QueryResult{}, errors.New("semantic report execution result is invalid")
	}
	result := QueryResult{Columns: []string{}, Rows: [][]any{}, Plans: []QueryPlanResult{}, Partial: partial}
	for _, plan := range executed.Artifact.Plans {
		rows, exists := executed.Rows(plan.Role)
		if !exists {
			return QueryResult{}, errors.New("semantic report execution rows are missing")
		}
		if len(rows) >= limit {
			result.Partial = true
		}
		if len(rows) > limit {
			rows = rows[:limit]
		}
		columns := make([]string, len(plan.Columns))
		for index, column := range plan.Columns {
			columns[index] = column.Name
		}
		planResult := QueryPlanResult{
			Role: string(plan.Role), Columns: columns, Rows: rows,
		}
		result.Plans = append(result.Plans, planResult)
		if plan.Role == askcompiler.QueryRoleCurrent {
			result.Columns = append([]string(nil), columns...)
			result.Rows = cloneQueryRows(rows)
		}
	}
	if len(result.Plans) == 0 {
		return QueryResult{}, fmt.Errorf("semantic report execution contains no plans")
	}
	raw, err := json.Marshal(struct {
		Plans   []QueryPlanResult `json:"plans"`
		Partial bool              `json:"partial"`
	}{result.Plans, result.Partial})
	if err != nil {
		return QueryResult{}, err
	}
	result.Hash = askdata.HashBytes(raw)
	return result, nil
}

func cloneQueryRows(rows [][]any) [][]any {
	result := make([][]any, len(rows))
	for index := range rows {
		result[index] = append([]any(nil), rows[index]...)
	}
	return result
}
