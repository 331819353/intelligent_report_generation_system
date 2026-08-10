package compiler

import (
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	AdditivityMissingCode                  = "ADDITIVITY_MISSING"
	SemiAdditiveTimeAggregationMissingCode = "SEMI_ADDITIVE_TIME_AGG_MISSING"
	SemiAdditiveTimeDimensionMissingCode   = "SEMI_ADDITIVE_TIME_DIMENSION_MISSING"
	NonAdditiveSumAttemptCode              = "NON_ADDITIVE_SUM_ATTEMPT"
	IncompatibleUnitCode                   = "INCOMPATIBLE_UNIT"
	NonAdditiveDimensionCollapsedCode      = "NON_ADDITIVE_DIMENSION_COLLAPSED"
)

var ErrInvalidAggregationPlan = errors.New("metric aggregation plan is invalid")

// AggregationPlanError is safe to return through the orchestrator. Details
// contain only governed IDs and unit labels, never SQL or physical columns.
type AggregationPlanError struct {
	Code               string     `json:"code"`
	MetricVersionID    askdata.ID `json:"metricVersionId,omitempty"`
	DimensionVersionID askdata.ID `json:"dimensionVersionId,omitempty"`
	Units              []string   `json:"units,omitempty"`
	Currencies         []string   `json:"currencies,omitempty"`
}

func (failure *AggregationPlanError) Error() string {
	return fmt.Sprintf("%s: metric=%s dimension=%s", failure.Code, failure.MetricVersionID, failure.DimensionVersionID)
}

func (failure *AggregationPlanError) Unwrap() error { return ErrInvalidAggregationPlan }

type MetricPlan struct {
	MetricVersionID   askdata.ID
	Additivity        registry.Additivity
	UseTimeReduction  bool
	TotalsNotSummable bool
}

type AggregationPlanner struct{}

func (AggregationPlanner) PlanMetric(metric MetricContract, query ir.SemanticIR) (MetricPlan, error) {
	plan := MetricPlan{MetricVersionID: metric.MetricVersionID, Additivity: metric.Additivity}
	if metric.Additivity == "" {
		return MetricPlan{}, aggregationFailure(AdditivityMissingCode, metric.MetricVersionID)
	}
	grouped := make(map[askdata.ID]struct{}, len(query.GroupBy))
	for _, group := range query.GroupBy {
		grouped[group.DimensionVersionID] = struct{}{}
	}
	switch metric.Additivity {
	case registry.FullyAdditive:
	case registry.SemiAdditive:
		if metric.SemiAdditiveTimeAggregation == "" {
			return MetricPlan{}, aggregationFailure(SemiAdditiveTimeAggregationMissingCode, metric.MetricVersionID)
		}
		if metric.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodEnd &&
			metric.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodBegin &&
			metric.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodAverage {
			return MetricPlan{}, aggregationFailure(SemiAdditiveTimeAggregationMissingCode, metric.MetricVersionID)
		}
		if query.TimeRange == nil {
			return MetricPlan{}, aggregationFailure(SemiAdditiveTimeDimensionMissingCode, metric.MetricVersionID)
		}
		plan.UseTimeReduction, plan.TotalsNotSummable = true, true
	case registry.NonAdditive:
		if metric.AggregationRestriction != registry.PostAggregate {
			return MetricPlan{}, aggregationFailure(NonAdditiveSumAttemptCode, metric.MetricVersionID)
		}
		for _, dimensionID := range metric.NonAdditiveDimensions {
			governedID := askdata.ID(dimensionID)
			if _, exists := grouped[governedID]; !exists {
				return MetricPlan{}, &AggregationPlanError{
					Code: NonAdditiveDimensionCollapsedCode, MetricVersionID: metric.MetricVersionID,
					DimensionVersionID: governedID,
				}
			}
		}
		plan.TotalsNotSummable = true
	default:
		return MetricPlan{}, aggregationFailure(AdditivityMissingCode, metric.MetricVersionID)
	}
	return plan, nil
}

func PlanMetric(metric MetricContract, query ir.SemanticIR) (MetricPlan, error) {
	return (AggregationPlanner{}).PlanMetric(metric, query)
}

func planMetrics(metrics []MetricContract, query ir.SemanticIR) ([]MetricPlan, error) {
	plans := make([]MetricPlan, 0, len(metrics))
	for _, metric := range metrics {
		plan, err := PlanMetric(metric, query)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := CheckUnitCompatibility(metrics); err != nil {
		return nil, err
	}
	return plans, nil
}

func aggregationFailure(code string, metricID askdata.ID) error {
	return &AggregationPlanError{Code: code, MetricVersionID: metricID}
}

func metricAggregationContracts(
	metrics []MetricContract,
	plans []MetricPlan,
	selected []ir.Metric,
) []MetricAggregationContract {
	byID := make(map[askdata.ID]MetricPlan, len(plans))
	for _, plan := range plans {
		byID[plan.MetricVersionID] = plan
	}
	aliasByID := make(map[askdata.ID]string, len(selected))
	for _, metric := range selected {
		aliasByID[metric.MetricVersionID] = metric.Alias
	}
	result := make([]MetricAggregationContract, 0, len(metrics))
	for _, metric := range metrics {
		plan := byID[metric.MetricVersionID]
		result = append(result, MetricAggregationContract{
			MetricVersionID: metric.MetricVersionID, ResultColumnName: aliasByID[metric.MetricVersionID],
			Additivity:                  metric.Additivity,
			SemiAdditiveTimeAggregation: metric.SemiAdditiveTimeAggregation,
			AggregationRestriction:      metric.AggregationRestriction,
			NonAdditiveDimensions:       append([]string(nil), metric.NonAdditiveDimensions...),
			TotalsNotSummable:           plan.TotalsNotSummable,
			Unit:                        metric.Unit, Currency: metric.Currency,
			DisplayPrecision:      metric.DisplayPrecision,
			ZeroDenominatorPolicy: metric.ZeroDenominatorPolicy,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].MetricVersionID < result[j].MetricVersionID })
	return result
}

func validateMetricAggregationContracts(values []MetricAggregationContract) error {
	if len(values) < 1 || len(values) > ir.MaxMetrics {
		return ErrInvalidQueryPlan
	}
	previous := askdata.ID("")
	metrics := make([]MetricContract, 0, len(values))
	seenColumnNames := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.MetricVersionID.Validate() != nil || (previous != "" && value.MetricVersionID <= previous) ||
			!trustedOutputNamePattern.MatchString(value.ResultColumnName) ||
			(value.Additivity != registry.FullyAdditive && value.Additivity != registry.SemiAdditive &&
				value.Additivity != registry.NonAdditive) ||
			value.TotalsNotSummable != (value.Additivity != registry.FullyAdditive) ||
			value.DisplayPrecision < 0 || value.DisplayPrecision > 12 ||
			(value.ZeroDenominatorPolicy != registry.ZeroDenominatorNull &&
				value.ZeroDenominatorPolicy != registry.ZeroDenominatorZero) {
			return ErrInvalidQueryPlan
		}
		if _, duplicate := seenColumnNames[value.ResultColumnName]; duplicate {
			return ErrInvalidQueryPlan
		}
		seenColumnNames[value.ResultColumnName] = struct{}{}
		if value.Additivity == registry.SemiAdditive &&
			value.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodEnd &&
			value.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodBegin &&
			value.SemiAdditiveTimeAggregation != registry.SemiAdditivePeriodAverage {
			return ErrInvalidQueryPlan
		}
		if value.Additivity == registry.NonAdditive && value.AggregationRestriction != registry.PostAggregate {
			return ErrInvalidQueryPlan
		}
		seenDimensions := make(map[string]struct{}, len(value.NonAdditiveDimensions))
		for _, raw := range value.NonAdditiveDimensions {
			if askdata.ID(raw).Validate() != nil {
				return ErrInvalidQueryPlan
			}
			if _, duplicate := seenDimensions[raw]; duplicate {
				return ErrInvalidQueryPlan
			}
			seenDimensions[raw] = struct{}{}
		}
		metrics = append(metrics, MetricContract{
			MetricVersionID: value.MetricVersionID, Unit: value.Unit, Currency: value.Currency,
		})
		previous = value.MetricVersionID
	}
	if err := CheckUnitCompatibility(metrics); err != nil {
		return ErrInvalidQueryPlan
	}
	return nil
}
