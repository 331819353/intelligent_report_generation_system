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
	field          dataset.Field
	rollupFunction string
}

// materializedMetricDocument turns a warehouse metric plan into a query over
// the exact dataset version's governed output relation. It never exposes the
// stable view name in the DSL. DIM/DWD fields are aggregated from their
// materialized detail columns; already aggregated DWS/ADS outputs are only
// rolled up when their aggregate is safely decomposable.
func materializedMetricDocument(
	original dataset.Document,
	derived dataset.Document,
	versionID string,
) (dataset.Document, error) {
	if original.Dataset.Layer != dataset.LayerDIM &&
		original.Dataset.Layer != dataset.LayerDWD &&
		original.Dataset.Layer != dataset.LayerDWS &&
		original.Dataset.Layer != dataset.LayerADS ||
		derived.Dataset.Layer != dataset.LayerDWS ||
		versionID == "" ||
		len(original.Parameters) != 0 {
		return dataset.Document{}, errors.New("warehouse materialized metric contract is unsupported")
	}

	replacements := make(map[string]materializedFieldReplacement, len(original.Fields))
	preAggregationRollups := make(map[string]string)
	for _, item := range original.PreAggregations {
		for _, metric := range item.Metrics {
			preAggregationRollups[item.NodeID+"\x00"+metric.Field] =
				strings.ToUpper(metric.Function)
		}
	}
	projection := make([]string, 0, len(original.Fields))
	for _, field := range original.Fields {
		key, err := expressionKey(field.Expression)
		if err != nil {
			return dataset.Document{}, err
		}
		if existing, found := replacements[key]; found && existing.field.Code != field.Code {
			return dataset.Document{}, errors.New("ambiguous DWS output expression")
		}
		rollupFunction := ""
		if field.Expression.Type == "FIELD_REF" {
			rollupFunction = preAggregationRollups[field.Expression.NodeID+"\x00"+field.Expression.Field]
		}
		replacements[key] = materializedFieldReplacement{
			field: field, rollupFunction: rollupFunction,
		}
		projection = append(projection, field.Code)
	}
	exactMaterializedGrain := sameStringSet(
		original.GroupBy, derived.GroupBy,
	)

	for index := range derived.Fields {
		expression, err := rewriteMaterializedExpressionForLayer(
			derived.Fields[index].Expression,
			replacements,
			original.Dataset.Layer,
			exactMaterializedGrain,
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
	derived.Distinct = false
	derived.Filters = append(
		[]dataset.Filter(nil),
		derived.Filters[len(original.Filters):]...,
	)
	for index := range derived.Filters {
		rewritten, err := rewriteMaterializedExpressionForLayer(
			derived.Filters[index].Expression,
			replacements,
			original.Dataset.Layer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Document{}, err
		}
		derived.Filters[index].Expression = rewritten
	}
	derived.Having = []dataset.Filter{}
	derived.Parameters = append(
		[]dataset.Parameter(nil),
		derived.Parameters[len(original.Parameters):]...,
	)
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
	return rewriteMaterializedExpressionForLayer(
		expression, replacements, dataset.LayerDWS, false,
	)
}

func rewriteMaterializedExpressionForLayer(
	expression dataset.Expression,
	replacements map[string]materializedFieldReplacement,
	sourceLayer dataset.Layer,
	exactMaterializedGrain bool,
) (dataset.Expression, error) {
	key, err := expressionKey(expression)
	if err != nil {
		return dataset.Expression{}, err
	}
	if replacement, found := replacements[key]; found {
		return materializedFieldReference(
			replacement.field, sourceLayer, exactMaterializedGrain,
		)
	}
	// 多事实 DWS 的输出度量通常是关联前预聚合的别名，因此原字段表达式是
	// FIELD_REF，指标定义再在外层声明 SUM/MIN/MAX。直接递归会把两层都改写
	// 为聚合并产生 SUM(SUM(...))。在这里一次性校验并生成正确的单层 roll-up。
	if expression.Type == "AGGREGATE" && expression.Argument != nil {
		argumentKey, argumentErr := expressionKey(*expression.Argument)
		if argumentErr != nil {
			return dataset.Expression{}, argumentErr
		}
		if replacement, found := replacements[argumentKey]; found &&
			replacement.rollupFunction != "" {
			return materializedPreAggregationReference(
				replacement, strings.ToUpper(expression.Function),
			)
		}
	}

	rewritePointer := func(value **dataset.Expression) error {
		if *value == nil {
			return nil
		}
		rewritten, err := rewriteMaterializedExpressionForLayer(
			**value, replacements, sourceLayer, exactMaterializedGrain,
		)
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
		rewritten, err := rewriteMaterializedExpressionForLayer(
			expression.Arguments[index], replacements, sourceLayer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.Arguments[index] = rewritten
	}
	for index := range expression.Whens {
		when, err := rewriteMaterializedExpressionForLayer(
			expression.Whens[index].When, replacements, sourceLayer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		then, err := rewriteMaterializedExpressionForLayer(
			expression.Whens[index].Then, replacements, sourceLayer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.Whens[index].When = when
		expression.Whens[index].Then = then
	}
	for index := range expression.PartitionBy {
		rewritten, err := rewriteMaterializedExpressionForLayer(
			expression.PartitionBy[index], replacements, sourceLayer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.PartitionBy[index] = rewritten
	}
	for index := range expression.OrderBy {
		rewritten, err := rewriteMaterializedExpressionForLayer(
			expression.OrderBy[index].Expression, replacements, sourceLayer,
			exactMaterializedGrain,
		)
		if err != nil {
			return dataset.Expression{}, err
		}
		expression.OrderBy[index].Expression = rewritten
	}
	return expression, nil
}

func materializedFieldReference(
	field dataset.Field,
	sourceLayer dataset.Layer,
	exactMaterializedGrain bool,
) (dataset.Expression, error) {
	reference := dataset.Expression{
		Type: "FIELD_REF", NodeID: materializedMetricNodeID, Field: field.Code,
	}
	if sourceLayer == dataset.LayerDIM || sourceLayer == dataset.LayerDWD {
		return reference, nil
	}
	if field.Role != "MEASURE" {
		return reference, nil
	}
	if exactMaterializedGrain {
		// The governed materialization has one row per complete published
		// groupBy key. MAX returns that frozen cell without attempting to
		// roll up a non-decomposable COUNT_DISTINCT or ratio.
		return dataset.Expression{
			Type: "AGGREGATE", Function: "MAX", Argument: &reference,
		}, nil
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

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, value := range left {
		values[value]++
	}
	for _, value := range right {
		if values[value] == 0 {
			return false
		}
		values[value]--
	}
	return true
}

func materializedPreAggregationReference(
	replacement materializedFieldReplacement,
	requestedFunction string,
) (dataset.Expression, error) {
	expectedFunction := strings.ToUpper(replacement.rollupFunction)
	switch expectedFunction {
	case "SUM":
	case "COUNT":
		expectedFunction = "SUM"
	case "MIN", "MAX":
	default:
		return dataset.Expression{}, errors.New(
			"DWS pre-aggregation measure is not safely roll-up capable",
		)
	}
	if requestedFunction != expectedFunction {
		return dataset.Expression{}, errors.New(
			"DWS metric aggregation does not match its pre-aggregation contract",
		)
	}
	reference := dataset.Expression{
		Type: "FIELD_REF", NodeID: materializedMetricNodeID,
		Field: replacement.field.Code,
	}
	return dataset.Expression{
		Type: "AGGREGATE", Function: expectedFunction, Argument: &reference,
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
		for _, child := range expression.PartitionBy {
			if err := visit(child); err != nil {
				return err
			}
		}
		for _, item := range expression.OrderBy {
			if err := visit(item.Expression); err != nil {
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
