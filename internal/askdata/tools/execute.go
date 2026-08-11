package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/validator"
)

// validateSemanticBundle checks that every object a binding proposes is present
// in the release pinned to this run.
//
// It is a containment check, not a scoring model: an object the release does
// not contain is reported as missing so the binder can re-bind or clarify,
// rather than being quietly dropped from the query.
func (binding *Binding) validateSemanticBundle(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.SemanticBundleInput,
) (toolhost.ToolOutput[toolhost.ValidateSemanticBundleResult], error) {
	var output toolhost.ToolOutput[toolhost.ValidateSemanticBundleResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Reader == nil {
		return output, ErrToolUnavailable
	}
	requested := make([]askdata.ID, 0)
	requested = append(requested, input.ModelVersionIDs...)
	requested = append(requested, input.MetricVersionIDs...)
	requested = append(requested, input.DimensionVersionIDs...)
	requested = append(requested, input.MemberVersionIDs...)

	rows, err := binding.services.Reader.Contracts(
		ctx, binding.run.Scope, binding.run.DomainID, plainIDs(requested),
	)
	if err != nil {
		return output, err
	}
	present := make(map[askdata.ID]bool, len(rows))
	for _, row := range rows {
		present[askdata.ID(row.ObjectVersionID)] = true
	}
	missing := make([]askdata.ID, 0)
	seen := map[askdata.ID]bool{}
	for _, id := range requested {
		if seen[id] || present[id] {
			continue
		}
		seen[id] = true
		missing = append(missing, id)
	}

	payload, err := json.Marshal(struct {
		Requested []askdata.ID `json:"requested"`
		Missing   []askdata.ID `json:"missing"`
	}{requested, missing})
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindPolicy, binding.run.DomainID, payload)
	confidence := 1_000_000
	if len(requested) > 0 {
		confidence = (len(requested) - len(missing)) * 1_000_000 / len(requested)
	}
	output.Result = toolhost.ValidateSemanticBundleResult{
		Valid: len(missing) == 0, MissingObjectVersionIDs: missing,
		Conflicts: []toolhost.BundleConflict{}, ConfidencePermillion: confidence,
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}

// executeQueryPlan runs a plan that was compiled and validated in this run.
//
// Both the compiled artifact and its validation artifact must come from this
// run's cache: executing a plan that was never validated, or one validated
// under a different policy scope, must be impossible from here.
func (binding *Binding) executeQueryPlan(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.ExecuteQueryPlanInput,
) (toolhost.ToolOutput[toolhost.ExecuteQueryPlanResult], error) {
	var output toolhost.ToolOutput[toolhost.ExecuteQueryPlanResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Executor == nil {
		return output, ErrToolUnavailable
	}
	artifact, validation, ok := binding.plans.getValidated(input.PlanHash)
	if !ok {
		// Either the plan is unknown to this run or it has not been validated.
		// Both are invalid invocations: execution never validates implicitly.
		return output, toolhost.ErrInvalidInvocation
	}
	execution, err := binding.services.Executor.Execute(ctx, validator.ExecutionRequest{
		RunID: string(binding.run.RunID), Query: artifact, Validation: validation,
	})
	if err != nil {
		return output, err
	}

	contract, contractErr := validator.NormalizeResultColumns(artifact, execution, nil)
	columns := make([]toolhost.ResultColumnSummary, 0)
	metrics := make([]toolhost.ResultMetricSummary, 0)
	if contractErr == nil {
		for _, plan := range contract.Plans {
			if plan.Role != compiler.QueryRoleCurrent {
				continue
			}
			for _, column := range plan.Columns {
				if column.Role == "METRIC" {
					metrics = append(metrics, toolhost.ResultMetricSummary{Code: column.Name})
					continue
				}
				columns = append(columns, toolhost.ResultColumnSummary{
					Code: column.Name, CanonicalType: column.Role,
				})
			}
		}
	}

	rowCount := execution.Artifact.TotalRows
	payload, err := json.Marshal(execution.Artifact)
	if err != nil {
		return output, err
	}
	binding.results.put(artifact.PlanHash, execution.Artifact.ResultHash)
	evidence := binding.evidence(askdata.EvidenceKindQueryResult, binding.run.DomainID, payload)
	// An empty result is a confirmed fact about the governed data, not a
	// failure: the answer layer must be able to say "no rows" with evidence.
	output.Result = toolhost.ExecuteQueryPlanResult{
		ResultHash: execution.Artifact.ResultHash, VerificationHash: validation.ValidationHash,
		Verdict: executionVerdict(rowCount), NoDataConfirmed: rowCount == 0,
		RowCount: rowCount, Columns: columns, Metrics: metrics,
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}

func executionVerdict(rowCount int) string {
	if rowCount == 0 {
		return "NO_DATA"
	}
	return "OK"
}

// executeValidationQuery is not yet backed by a governed validation-query
// runner. The compiler emits CURRENT/BASELINE plans only; the bounded
// member-existence, coverage and cardinality probes described by the tool
// contract have no compiler role or executor path today.
//
// It reports unavailability rather than returning a fabricated "covered"
// verdict, because a false coverage claim would let the answer layer assert
// completeness it never checked.
func (binding *Binding) executeValidationQuery(
	_ context.Context,
	authorization toolhost.AuthorizationContext,
	_ toolhost.ExecuteValidationQueryInput,
) (toolhost.ToolOutput[toolhost.ExecuteValidationQueryResult], error) {
	var output toolhost.ToolOutput[toolhost.ExecuteValidationQueryResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	return output, fmt.Errorf("%w: governed validation queries are not implemented", ErrToolUnavailable)
}

// probeJoinCardinality is not yet backed by a governed probe. Fanout safety is
// currently decided statically from the relationship contract's declared
// cardinality and fanout policy during graph resolution; there is no bounded
// warehouse probe to measure it.
//
// It reports unavailability rather than returning Safe=true, which would assert
// a fanout guarantee nothing measured.
func (binding *Binding) probeJoinCardinality(
	_ context.Context,
	authorization toolhost.AuthorizationContext,
	_ toolhost.ProbeJoinCardinalityInput,
) (toolhost.ToolOutput[toolhost.ProbeJoinCardinalityResult], error) {
	var output toolhost.ToolOutput[toolhost.ProbeJoinCardinalityResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	return output, fmt.Errorf("%w: governed join cardinality probes are not implemented", ErrToolUnavailable)
}

// compareCandidateResults compares two results produced in this run.
//
// Comparison is by result hash only: equal hashes prove equivalence, unequal
// hashes prove difference. Per-metric deltas would require holding both result
// row sets in memory across tool calls, which this boundary deliberately does
// not do, so Differences stays empty rather than being estimated.
func (binding *Binding) compareCandidateResults(
	_ context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.CompareCandidateResultsInput,
) (toolhost.ToolOutput[toolhost.CompareCandidateResultsResult], error) {
	var output toolhost.ToolOutput[toolhost.CompareCandidateResultsResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	left, leftOK := binding.results.get(input.LeftPlanHash)
	right, rightOK := binding.results.get(input.RightPlanHash)
	if !leftOK || !rightOK {
		return output, toolhost.ErrInvalidInvocation
	}
	payload, err := json.Marshal(struct {
		Left  askdata.ContentHash `json:"left"`
		Right askdata.ContentHash `json:"right"`
	}{left, right})
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindQueryResult, binding.run.DomainID, payload)
	equivalent := left == right
	differenceCount := 0
	if !equivalent {
		differenceCount = 1
	}
	output.Result = toolhost.CompareCandidateResultsResult{
		LeftResultHash: left, RightResultHash: right, Equivalent: equivalent,
		DifferenceCount: differenceCount,
		Differences:     []toolhost.MetricDifferenceSummary{},
		EvidenceIDs:     []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}

// requestClarification records a targeted clarification for the user.
//
// The handler is deliberately pure: it validates and echoes the conflict the
// model raised. Persisting the clarification and moving the run into
// CLARIFICATION_REQUIRED is the orchestrator's job, because only the
// orchestrator owns run state.
func (binding *Binding) requestClarification(
	_ context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.RequestClarificationInput,
) (toolhost.ToolOutput[toolhost.RequestClarificationResult], error) {
	var output toolhost.ToolOutput[toolhost.RequestClarificationResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if input.ConflictCode == "" || input.Question == "" ||
		len(input.Options) < 2 || len(input.Options) > toolhost.MaxClarificationOptions {
		// A clarification with fewer than two options is not a choice.
		return output, toolhost.ErrInvalidInvocation
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindPolicy, binding.run.DomainID, payload)
	output.Result = toolhost.RequestClarificationResult{
		ConflictCode: input.ConflictCode, Question: input.Question,
		Options: input.Options, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}
