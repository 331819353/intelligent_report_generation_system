package compiler

import (
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/dataset"
)

// AdditivityShape is the aggregation-only projection of a compiled plan. It
// carries no physical source, no SQL, no parameter values and no plan hash, so
// it is not a way to reach data — the only path to execution remains a
// QueryArtifact produced by Adapt from a replay-validated binding.
type AdditivityShape struct {
	Expression          *dataset.Expression
	PreAggregationCount int
	CompileErrorCode    string
}

// InspectAdditivity compiles the resolved contracts through the very same
// compileResolvedArtifact the query path uses, then reports how the named
// metric was aggregated. It exists so the governed additivity suite asserts on
// the compiler rather than on a second implementation of its rules; a suite
// that re-derives the answer it is checking measures nothing.
//
// A governed aggregation refusal — mixed units, a re-aggregated distinct, a
// semi-additive metric with no time window — is returned as a CompileErrorCode
// rather than an error, because refusing is precisely the behaviour under test.
func InspectAdditivity(
	semanticIR ir.SemanticIR,
	resolution Resolution,
	metricVersionID askdata.ID,
) (AdditivityShape, error) {
	artifact, err := compileResolvedArtifact(semanticIR, resolution, nil)
	if err != nil {
		var planError *AggregationPlanError
		if errors.As(err, &planError) {
			return AdditivityShape{CompileErrorCode: planError.Code}, nil
		}
		return AdditivityShape{}, err
	}
	alias := ""
	for _, metric := range semanticIR.Metrics {
		if metric.MetricVersionID == metricVersionID {
			alias = metric.Alias
		}
	}
	if alias == "" || len(artifact.Plans) == 0 {
		return AdditivityShape{}, fmt.Errorf(
			"%w: metric %s is not selected by this query", ErrInvalidAdaptRequest, metricVersionID,
		)
	}
	document := artifact.Plans[0].Document
	shape := AdditivityShape{}
	for _, preAggregation := range document.PreAggregations {
		shape.PreAggregationCount += len(preAggregation.Metrics)
	}
	for _, field := range document.Fields {
		if field.Code != alias || field.Role != "MEASURE" {
			continue
		}
		expression := cloneDatasetExpression(field.Expression)
		shape.Expression = &expression
		return shape, nil
	}
	return AdditivityShape{}, fmt.Errorf(
		"%w: metric %s produced no output measure", ErrInvalidQueryPlan, metricVersionID,
	)
}
