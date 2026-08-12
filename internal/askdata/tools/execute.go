package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"

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
	artifact, semanticIR, validation, ok := binding.plans.getValidated(input.PlanHash)
	if !ok {
		// Either the plan is unknown to this run or it has not been validated.
		// Both are invalid invocations: execution never validates implicitly.
		return output, toolhost.ErrInvalidInvocation
	}
	execution, err := binding.services.Executor.Execute(ctx, validator.ExecutionRequest{
		RunID: string(binding.run.RunID), Query: artifact, Validation: validation,
	})
	if err != nil {
		// The Tool Host deliberately returns a bounded public error, but the
		// worker still needs the internal failure class to diagnose broken
		// warehouse/materialization paths. Do not log SQL, arguments or rows.
		slog.ErrorContext(ctx, "execute AskData query plan",
			"run_id", binding.run.RunID,
			"plan_hash", input.PlanHash,
			"error", err,
		)
		return output, err
	}

	contract, contractErr := validator.NormalizeResultColumns(artifact, execution, nil)
	columns := make([]toolhost.ResultColumnSummary, 0)
	metrics := make([]toolhost.ResultMetricSummary, 0)
	rowCount := 0
	if contractErr == nil {
		for _, plan := range contract.Plans {
			if plan.Role != compiler.QueryRoleCurrent {
				continue
			}
			rows, exists := execution.Rows(compiler.QueryRoleCurrent)
			if !exists {
				contractErr = fmt.Errorf("current query result is missing")
				break
			}
			rowCount = len(rows)
			columns, metrics, contractErr = summarizeCurrentResult(plan, rows)
			break
		}
	}

	payload, err := json.Marshal(execution.Artifact)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindQueryResult, binding.run.DomainID, payload)
	if contractErr != nil {
		return output, contractErr
	}
	binding.results.put(executionEntry{
		planHash: artifact.PlanHash, artifact: artifact, semanticIR: semanticIR,
		validation: validation, execution: execution, contract: contract,
		resultHash:  execution.Artifact.ResultHash,
		evidenceIDs: []askdata.ID{evidence.EvidenceID},
	})
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
	output.QueryScanBytes = estimatedQueryScanBytes(validation)
	return output, nil
}

// estimatedQueryScanBytes derives a stable, strictly positive lower-bound
// estimate from the trusted EXPLAIN summaries already validated for this run.
// PostgreSQL does not expose actual heap bytes for a SELECT without adding a
// second privileged query; eight bytes per estimated row keeps quota accounting
// useful while remaining conservative and free of client/model input.
func estimatedQueryScanBytes(validation validator.ValidationArtifact) int64 {
	var total int64
	for _, plan := range validation.Plans {
		rows := plan.Explain.MaxNodeRows
		if plan.Explain.MaxSequentialRows > rows {
			rows = plan.Explain.MaxSequentialRows
		}
		if rows < 1 {
			rows = 1
		}
		if rows > (1<<50)/8 || total > (1<<50)-(rows*8) {
			return 1 << 50
		}
		total += rows * 8
	}
	if total < 1 {
		return 1
	}
	return total
}

func executionVerdict(rowCount int) string {
	// Empty data is represented independently by NoDataConfirmed. PASS means
	// the governed execution itself completed and crossed its deterministic
	// validation boundary; it does not claim that the result contains rows.
	return "PASS"
}

// summarizeCurrentResult produces the bounded, row-free result evidence that
// the cognition loop may inspect. Every visible column is included (metrics
// included), while metric aggregates are computed with exact rationals so a
// DECIMAL never crosses float64 merely to build a summary.
func summarizeCurrentResult(
	plan validator.ResultPlanContract,
	rows [][]any,
) ([]toolhost.ResultColumnSummary, []toolhost.ResultMetricSummary, error) {
	columns := make([]toolhost.ResultColumnSummary, len(plan.Columns))
	metrics := make([]toolhost.ResultMetricSummary, 0)
	if len(plan.Columns) == 0 {
		return nil, nil, fmt.Errorf("current query columns are missing")
	}
	for rowIndex, row := range rows {
		if len(row) != len(plan.Columns) {
			return nil, nil, fmt.Errorf("current query row %d has an invalid shape", rowIndex)
		}
	}
	for columnIndex, column := range plan.Columns {
		distinct := make(map[string]struct{}, len(rows))
		nullCount := 0
		metric := toolhost.ResultMetricSummary{Code: column.Name}
		var minimum, maximum, sum *big.Rat
		for _, row := range rows {
			value := row[columnIndex]
			if value == nil {
				nullCount++
				continue
			}
			canonical, err := json.Marshal(value)
			if err != nil {
				return nil, nil, err
			}
			distinct[string(canonical)] = struct{}{}
			if column.Role != "METRIC" {
				continue
			}
			numeric, ok := exactResultNumber(value)
			if !ok {
				return nil, nil, fmt.Errorf("metric %s contains a non-numeric value", column.Name)
			}
			if minimum == nil || numeric.Cmp(minimum) < 0 {
				minimum = new(big.Rat).Set(numeric)
			}
			if maximum == nil || numeric.Cmp(maximum) > 0 {
				maximum = new(big.Rat).Set(numeric)
			}
			if sum == nil {
				sum = new(big.Rat)
			}
			sum.Add(sum, numeric)
		}
		columns[columnIndex] = toolhost.ResultColumnSummary{
			Code: column.Name, CanonicalType: column.Role,
			NullCount: nullCount, DistinctCount: len(distinct),
		}
		if column.Role == "METRIC" {
			metric.NullCount = nullCount
			metric.NonNullCount = len(rows) - nullCount
			if minimum != nil {
				metric.Minimum = minimum.RatString()
				metric.Maximum = maximum.RatString()
				metric.Sum = sum.RatString()
			}
			metrics = append(metrics, metric)
		}
	}
	return columns, metrics, nil
}

func exactResultNumber(value any) (*big.Rat, bool) {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case json.Number:
		text = typed.String()
	case int:
		text = strconv.FormatInt(int64(typed), 10)
	case int8:
		text = strconv.FormatInt(int64(typed), 10)
	case int16:
		text = strconv.FormatInt(int64(typed), 10)
	case int32:
		text = strconv.FormatInt(int64(typed), 10)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case uint:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		text = strconv.FormatUint(typed, 10)
	case float32:
		text = strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return nil, false
	}
	number, ok := new(big.Rat).SetString(text)
	return number, ok
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
	}{left.resultHash, right.resultHash})
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindQueryResult, binding.run.DomainID, payload)
	equivalent := left.resultHash == right.resultHash
	differenceCount := 0
	if !equivalent {
		differenceCount = 1
	}
	output.Result = toolhost.CompareCandidateResultsResult{
		LeftResultHash: left.resultHash, RightResultHash: right.resultHash, Equivalent: equivalent,
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
