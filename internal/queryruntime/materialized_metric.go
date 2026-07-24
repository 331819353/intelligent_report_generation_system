package queryruntime

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

const materializedMetricNodeID = "materialized_root"

type materializedFieldReplacement struct {
	field dataset.Field
}

// materializedMetricDocument turns a DWS metric plan into a query over the
// DWS version's governed output relation. It never exposes the stable view name
// in the DSL. Additive aggregate outputs are rolled up from their materialized
// columns; non-decomposable aggregates fail closed instead of producing a
// plausible but incorrect metric.
func materializedMetricDocument(
	original dataset.Document,
	derived dataset.Document,
	versionID string,
) (dataset.Document, error) {
	if original.Dataset.Layer != dataset.LayerDWS ||
		derived.Dataset.Layer != dataset.LayerDWS ||
		versionID == "" ||
		len(original.Parameters) != 0 {
		return dataset.Document{}, errors.New("DWS materialized metric contract is unsupported")
	}

	replacements := make(map[string]materializedFieldReplacement, len(original.Fields))
	projection := make([]string, 0, len(original.Fields))
	for _, field := range original.Fields {
		key, err := expressionKey(field.Expression)
		if err != nil {
			return dataset.Document{}, err
		}
		if existing, found := replacements[key]; found && existing.field.Code != field.Code {
			return dataset.Document{}, errors.New("ambiguous DWS output expression")
		}
		replacements[key] = materializedFieldReplacement{field: field}
		projection = append(projection, field.Code)
	}

	for index := range derived.Fields {
		expression, err := rewriteMaterializedExpression(
			derived.Fields[index].Expression,
			replacements,
		)
		if err != nil {
			return dataset.Document{}, err
		}
		derived.Fields[index].Expression = expression
	}
	if err := rejectNonMaterializedFieldReferences(derived.Fields); err != nil {
		return dataset.Document{}, err
	}

	derived.Dataset.Type = "SINGLE_SOURCE"
	derived.Nodes = []dataset.Node{{
		ID: materializedMetricNodeID, Type: "DATASET",
		DatasetVersionID: versionID, Alias: materializedMetricNodeID,
		Projection: projection, SourceFilters: []dataset.SourceFilter{},
	}}
	derived.Joins = []dataset.Join{}
	derived.PreAggregations = []dataset.PreAggregation{}
	derived.Filters = []dataset.Filter{}
	derived.Having = []dataset.Filter{}
	derived.Parameters = []dataset.Parameter{}
	derived.Designer = nil
	if err := dataset.Validate(derived); err != nil {
		return dataset.Document{}, err
	}
	return derived, nil
}

func rewriteMaterializedExpression(
	expression dataset.Expression,
	replacements map[string]materializedFieldReplacement,
) (dataset.Expression, error) {
	key, err := expressionKey(expression)
	if err != nil {
		return dataset.Expression{}, err
	}
	if replacement, found := replacements[key]; found {
		return materializedFieldReference(replacement.field)
	}

	rewritePointer := func(value **dataset.Expression) error {
		if *value == nil {
			return nil
		}
		rewritten, err := rewriteMaterializedExpression(**value, replacements)
		if err != nil {
			return err
		}
		*value = &rewritten
		return nil
	}
	for _, pointer := range []**dataset.Expression{
		&expression.Argument, &expression.Left, &expression.Right,
		&expression.Lower, &expression.Upper, &expression.Else,
	} {
		if err := rewritePointer(pointer); err != nil {
			return dataset.Expression{}, err
		}
	}
	for index := range expression.Arguments {
		rewritten, err := rewriteMaterializedExpression(
			expression.Arguments[index], replacements,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.Arguments[index] = rewritten
	}
	for index := range expression.Whens {
		when, err := rewriteMaterializedExpression(
			expression.Whens[index].When, replacements,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		then, err := rewriteMaterializedExpression(
			expression.Whens[index].Then, replacements,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.Whens[index].When = when
		expression.Whens[index].Then = then
	}
	return expression, nil
}

func materializedFieldReference(field dataset.Field) (dataset.Expression, error) {
	reference := dataset.Expression{
		Type: "FIELD_REF", NodeID: materializedMetricNodeID, Field: field.Code,
	}
	if field.Role != "MEASURE" {
		return reference, nil
	}
	if field.Expression.Type != "AGGREGATE" {
		return dataset.Expression{}, errors.New("calculated DWS measure is not safely roll-up capable")
	}
	function := strings.ToUpper(field.Expression.Function)
	switch function {
	case "SUM", "MIN", "MAX":
		// These aggregates are associative over their already materialized
		// outputs and can therefore be rolled up to a coarser requested grain.
	case "COUNT":
		// Counts roll up by summing partition counts, not by counting rows in
		// the DWS result.
		function = "SUM"
	default:
		return dataset.Expression{}, errors.New("DWS measure uses a non-decomposable aggregate")
	}
	return dataset.Expression{
		Type: "AGGREGATE", Function: function, Argument: &reference,
	}, nil
}

func expressionKey(expression dataset.Expression) (string, error) {
	raw, err := json.Marshal(expression)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func rejectNonMaterializedFieldReferences(fields []dataset.Field) error {
	var visit func(dataset.Expression) error
	visit = func(expression dataset.Expression) error {
		if expression.Type == "FIELD_REF" &&
			(expression.NodeID != materializedMetricNodeID || expression.Field == "") {
			return errors.New("metric expression escaped the DWS output contract")
		}
		for _, child := range []*dataset.Expression{
			expression.Argument, expression.Left, expression.Right,
			expression.Lower, expression.Upper, expression.Else,
		} {
			if child != nil {
				if err := visit(*child); err != nil {
					return err
				}
			}
		}
		for _, child := range expression.Arguments {
			if err := visit(child); err != nil {
				return err
			}
		}
		for _, branch := range expression.Whens {
			if err := visit(branch.When); err != nil {
				return err
			}
			if err := visit(branch.Then); err != nil {
				return err
			}
		}
		return nil
	}
	for _, field := range fields {
		if reflect.ValueOf(field.Expression).IsZero() {
			return errors.New("metric output field has no expression")
		}
		if err := visit(field.Expression); err != nil {
			return err
		}
	}
	return nil
}
