package compiler

import (
	"fmt"

	"intelligent-report-generation-system/internal/askdata/registry"
)

func compilePreAggregateJoin(request JoinCompileRequest) (string, error) {
	condition, _ := decodeRelationshipJoinAST(request.Relationship.JoinAST)
	if !validJoinIdentifier(request.Right.Alias + "_pre") {
		return "", ErrInvalidJoinContract
	}
	right, err := compileGroupedSource(request.Right, []string{condition.RightFieldID})
	if err != nil {
		return "", err
	}
	return "WITH " + quoteJoinIdentifier(request.Right.Alias+"_pre") + " AS (" + right + ") " +
		"SELECT * FROM " + sourceRelation(request.Left) + " " +
		joinKeyword(request.Relationship.JoinType) + " " +
		quoteJoinIdentifier(request.Right.Alias+"_pre") + " AS " + quoteJoinIdentifier(request.Right.Alias) +
		" ON " + qualified(request.Left.Alias, condition.LeftFieldID) + " = " +
		qualified(request.Right.Alias, condition.RightFieldID), nil
}

func compileGroupedSource(source JoinSource, requiredKeys []string) (string, error) {
	for _, key := range requiredKeys {
		if !validJoinIdentifier(key) {
			return "", ErrInvalidJoinContract
		}
	}
	groups := orderedUnique(requiredKeys, source.GroupBy)
	projections := make([]string, 0, len(groups)+len(source.Measures))
	groupExpressions := make([]string, 0, len(groups))
	innerAlias := source.Alias + "_source"
	if !validJoinIdentifier(innerAlias) {
		return "", ErrInvalidJoinContract
	}
	inner := source
	inner.Alias = innerAlias
	for _, column := range groups {
		expression := qualified(innerAlias, column)
		projections = append(projections, expression+" AS "+quoteJoinIdentifier(column))
		groupExpressions = append(groupExpressions, expression)
	}
	for _, measure := range source.Measures {
		function, err := aggregateSQL(measure.Function)
		if err != nil {
			return "", err
		}
		expression := qualified(innerAlias, measure.Column)
		if measure.Function == registry.AggregationCountDistinct {
			expression = "DISTINCT " + expression
		}
		projections = append(projections,
			function+"("+expression+") AS "+quoteJoinIdentifier(measure.Alias))
	}
	if len(projections) == 0 {
		return "", ErrInvalidJoinContract
	}
	if len(source.Measures) == 0 {
		return "SELECT DISTINCT " + comma(projections) + " FROM " + sourceRelation(inner), nil
	}
	return "SELECT " + comma(projections) + " FROM " + sourceRelation(inner) +
		" GROUP BY " + comma(groupExpressions), nil
}

func aggregateSQL(value registry.Aggregation) (string, error) {
	switch value {
	case registry.AggregationSum:
		return "SUM", nil
	case registry.AggregationAverage:
		return "AVG", nil
	case registry.AggregationMinimum:
		return "MIN", nil
	case registry.AggregationMaximum:
		return "MAX", nil
	case registry.AggregationCount, registry.AggregationCountDistinct:
		return "COUNT", nil
	default:
		return "", fmt.Errorf("%w: aggregation", ErrInvalidJoinContract)
	}
}
