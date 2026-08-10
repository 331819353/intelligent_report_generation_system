package compiler

import (
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
)

func semiAdditiveMeasureExpression(
	metric MetricContract,
	fieldCode string,
	timeField FieldContract,
) (dataset.Expression, error) {
	argument := dataset.Expression{Type: "FIELD_REF", NodeID: "semantic_model", Field: fieldCode}
	switch metric.SemiAdditiveTimeAggregation {
	case registry.SemiAdditivePeriodAverage:
		return dataset.Expression{Type: "AGGREGATE", Function: "AVG", Argument: &argument}, nil
	case registry.SemiAdditivePeriodEnd, registry.SemiAdditivePeriodBegin:
		direction := "DESC"
		function := "PERIOD_END"
		if metric.SemiAdditiveTimeAggregation == registry.SemiAdditivePeriodBegin {
			direction, function = "ASC", "PERIOD_BEGIN"
		}
		return dataset.Expression{
			Type: "AGGREGATE", Function: function, Argument: &argument,
			OrderBy: []dataset.WindowOrder{{Expression: fieldReference(timeField), Direction: direction}},
		}, nil
	default:
		return dataset.Expression{}, aggregationFailure(SemiAdditiveTimeAggregationMissingCode, metric.MetricVersionID)
	}
}

func reaggregateMeasureExpression(metric MetricContract, measure MeasureContract, fieldCode string) (dataset.Expression, error) {
	argument := dataset.Expression{Type: "FIELD_REF", NodeID: "semantic_model", Field: fieldCode}
	function := string(measure.Aggregation)
	switch measure.Aggregation {
	case registry.AggregationSum, registry.AggregationCount:
		function = "SUM"
	case registry.AggregationMinimum:
		function = "MIN"
	case registry.AggregationMaximum:
		function = "MAX"
	case registry.AggregationAverage, registry.AggregationCountDistinct:
		return dataset.Expression{}, &AggregationPlanError{
			Code: NonAdditiveSumAttemptCode, MetricVersionID: metric.MetricVersionID,
		}
	default:
		return dataset.Expression{}, fmt.Errorf("%w: unsupported measure aggregation %s", ErrInvalidAggregationPlan, measure.Aggregation)
	}
	return dataset.Expression{Type: "AGGREGATE", Function: function, Argument: &argument}, nil
}

func preAggregatedMeasureField(metricID, measureID askdata.ID) string {
	return stableDatasetIdentifier("am", askdata.ID(string(metricID)+":"+string(measureID)))
}
