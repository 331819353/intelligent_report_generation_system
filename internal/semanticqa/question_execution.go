package semanticqa

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

// executeQueryPlanCore is the lifecycle-free execution adapter used by the
// Question Orchestrator. The legacy execute endpoint wraps the same governed
// store/runtime boundary with its own compatibility lifecycle.
func (service *Service) executeQueryPlanCore(
	ctx context.Context,
	tenantID, actorID, id string,
	input ExecuteQueryPlanInput,
) (QueryPlanExecution, error) {
	if service == nil || service.store == nil || service.metricExecutor == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(id) != nil ||
		uuid.Validate(input.ExpectedGraphGenerationID) != nil ||
		!validHash(input.ExpectedPathHash) || uuid.Validate(input.QueryID) != nil ||
		input.MaxRows < 1 || input.MaxRows > 500 {
		return QueryPlanExecution{}, ErrInvalidRequest
	}
	if input.Parameters == nil {
		input.Parameters = map[string]any{}
	}
	plan, binding, err := service.store.PrepareQueryPlanExecution(
		ctx, tenantID, id,
		input.ExpectedGraphGenerationID, input.ExpectedPathHash,
	)
	if err != nil {
		return QueryPlanExecution{}, err
	}
	dimensionFields := []string{}
	if binding.DimensionFieldID != "" {
		dimensionFields = append(dimensionFields, binding.DimensionFieldID)
	}
	if plan.Intent == "TREND" && binding.TimeFieldID != "" &&
		binding.TimeFieldID != binding.DimensionFieldID {
		dimensionFields = append(dimensionFields, binding.TimeFieldID)
	}
	maxRows := input.MaxRows
	if binding.TopN > 0 && maxRows > binding.TopN {
		maxRows = binding.TopN
	}
	executeRange := func(
		queryID string,
		timeRange *QueryTimeRange,
	) (dataset.PreviewResult, error) {
		dimensionFilters := []metric.DimensionFilter{}
		for _, memberFilter := range binding.MemberFilters {
			operator := "EQUALS"
			value := any(memberFilter.MemberKey)
			if len(memberFilter.MemberKeys) > 1 {
				operator = "IN"
				value = memberFilter.MemberKeys
			}
			dimensionFilters = append(dimensionFilters, metric.DimensionFilter{
				FieldID: memberFilter.FieldID, Operator: operator, Value: value,
			})
		}
		if timeRange != nil {
			dimensionFilters = append(dimensionFilters,
				metric.DimensionFilter{
					FieldID: binding.TimeFieldID, Operator: "GTE", Value: timeRange.Start,
				},
				metric.DimensionFilter{
					FieldID: binding.TimeFieldID, Operator: "LT", Value: timeRange.EndExclusive,
				},
			)
		}
		return service.metricExecutor.PreviewVersion(
			ctx, tenantID, actorID, plan.SelectedMetricID,
			plan.SelectedMetricVersionID,
			metric.PreviewInput{
				QueryID: queryID, Parameters: input.Parameters,
				DimensionFieldIDs:   dimensionFields,
				DimensionFilters:    dimensionFilters,
				MetricSortDirection: binding.SortDirection,
				MaxRows:             maxRows,
			},
		)
	}
	var baselineResult *dataset.PreviewResult
	if binding.ComparisonRange != nil {
		baselineQueryID := uuid.NewSHA1(
			uuid.MustParse(input.QueryID), []byte("semantic-comparison-baseline"),
		).String()
		value, executeErr := executeRange(baselineQueryID, binding.ComparisonRange)
		if executeErr != nil {
			_, _ = service.store.FinishQueryPlanExecution(
				ctx, tenantID, id, baselineQueryID,
				"METRIC_COMPARISON_EXECUTION_FAILED", false,
				input.ExpectedGraphGenerationID, 0, 0,
			)
			return QueryPlanExecution{}, executeErr
		}
		baselineResult = &value
	}
	result, executeErr := executeRange(input.QueryID, binding.TimeRange)
	if executeErr != nil {
		_, _ = service.store.FinishQueryPlanExecution(
			ctx, tenantID, id, input.QueryID,
			"METRIC_EXECUTION_FAILED", false,
			input.ExpectedGraphGenerationID, 0, 0,
		)
		return QueryPlanExecution{}, executeErr
	}
	answerEvidence, evidenceErr := buildAnswerEvidence(
		plan, result, baselineResult, time.Now().UTC(),
	)
	if evidenceErr != nil {
		_, _ = service.store.FinishQueryPlanExecution(
			ctx, tenantID, id, input.QueryID,
			"RESULT_EVIDENCE_HASH_FAILED", false,
			input.ExpectedGraphGenerationID, 0, 0,
		)
		return QueryPlanExecution{}, fmt.Errorf(
			"%w: result evidence hash: %v", ErrUnprovenPath, evidenceErr,
		)
	}
	plan, err = service.store.FinishQueryPlanExecution(
		ctx, tenantID, id, result.QueryID, "", true,
		input.ExpectedGraphGenerationID, result.DurationMS, result.RowCount,
	)
	if err != nil {
		return QueryPlanExecution{}, err
	}
	execution := QueryPlanExecution{
		QueryPlan: plan, Result: result, Evidence: answerEvidence,
	}
	execution.Evidence.Lineage = append([]QueryEvidence(nil), plan.Evidence...)
	if baselineResult != nil && binding.TimeRange != nil && binding.ComparisonRange != nil {
		execution.Comparison = &QueryComparisonExecution{
			Mode:          binding.ComparisonMode,
			CurrentRange:  *binding.TimeRange,
			BaselineRange: *binding.ComparisonRange,
			Baseline:      *baselineResult,
		}
	}
	return execution, nil
}
