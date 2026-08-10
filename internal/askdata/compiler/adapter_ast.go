package compiler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
)

type semanticAST struct {
	Type             string          `json:"type"`
	FieldID          askdata.ID      `json:"fieldId,omitempty"`
	MeasureVersionID askdata.ID      `json:"measureVersionId,omitempty"`
	MeasureID        askdata.ID      `json:"measureId,omitempty"`
	TargetType       string          `json:"targetType,omitempty"`
	Value            json.RawMessage `json:"value,omitempty"`
	Argument         *semanticAST    `json:"argument,omitempty"`
	Arguments        []semanticAST   `json:"arguments,omitempty"`
	Left             *semanticAST    `json:"left,omitempty"`
	Right            *semanticAST    `json:"right,omitempty"`
	Lower            *semanticAST    `json:"lower,omitempty"`
	Upper            *semanticAST    `json:"upper,omitempty"`
	present          map[string]bool
}

var semanticASTKeys = map[string]bool{
	"type": true, "fieldId": true, "measureVersionId": true, "measureId": true,
	"targetType": true, "value": true, "argument": true, "arguments": true,
	"left": true, "right": true, "lower": true, "upper": true,
}

func (node *semanticAST) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key := range fields {
		if !semanticASTKeys[key] {
			return fmt.Errorf("semantic AST contains unknown field %q", key)
		}
	}
	type plain semanticAST
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*node = semanticAST(decoded)
	node.present = make(map[string]bool, len(fields))
	for key := range fields {
		node.present[key] = true
	}
	return nil
}

type astMode uint8

const (
	astMeasure astMode = iota + 1
	astMetric
	astFilter
)

type astTranslator struct {
	fields            map[askdata.ID]FieldContract
	measures          map[askdata.ID]dataset.Expression
	measureVersions   map[askdata.ID]askdata.ID
	referencedFields  map[askdata.ID]struct{}
	referencedMeasure map[askdata.ID]struct{}
	zeroPolicy        registry.ZeroDenominatorPolicy
}

type compiledMeasureInput struct {
	contract MeasureContract
	scalar   dataset.Expression
}

func compileMetricExpression(
	metric MetricContract,
	fields map[askdata.ID]FieldContract,
) (dataset.Expression, string, error) {
	inputs, err := compileMetricMeasureInputs(metric, fields)
	if err != nil {
		return dataset.Expression{}, "", err
	}
	measureExpressions := make(map[askdata.ID]dataset.Expression, len(inputs)*2)
	measureTypes := make(map[askdata.ID]string, len(inputs)*2)
	measureVersions := make(map[askdata.ID]askdata.ID, len(inputs)*2)
	for _, input := range inputs {
		argument := input.scalar
		aggregated := dataset.Expression{
			Type: "AGGREGATE", Function: string(input.contract.Aggregation), Argument: &argument,
		}
		if err := registerMeasureExpression(
			input.contract, aggregated, measureExpressions, measureTypes, measureVersions,
		); err != nil {
			return dataset.Expression{}, "", err
		}
	}
	return compileMetricFormula(metric, measureExpressions, measureTypes, measureVersions)
}

func compileMetricExpressionPreAggregated(
	metric MetricContract,
	fields map[askdata.ID]FieldContract,
	timeField FieldContract,
) (dataset.Expression, string, []dataset.PreAggregationMetric, error) {
	inputs, err := compileMetricMeasureInputs(metric, fields)
	if err != nil {
		return dataset.Expression{}, "", nil, err
	}
	measureExpressions := make(map[askdata.ID]dataset.Expression, len(inputs)*2)
	measureTypes := make(map[askdata.ID]string, len(inputs)*2)
	measureVersions := make(map[askdata.ID]askdata.ID, len(inputs)*2)
	preAggregations := make([]dataset.PreAggregationMetric, 0, len(inputs))
	for _, input := range inputs {
		fieldCode := preAggregatedMeasureField(metric.MetricVersionID, input.contract.MeasureVersionID)
		scalar := input.scalar
		preAggregations = append(preAggregations, dataset.PreAggregationMetric{
			Field: fieldCode, Function: string(input.contract.Aggregation), Expression: &scalar,
		})
		var outer dataset.Expression
		if metric.Additivity == registry.SemiAdditive {
			outer, err = semiAdditiveMeasureExpression(metric, fieldCode, timeField)
		} else {
			outer, err = reaggregateMeasureExpression(metric, input.contract, fieldCode)
		}
		if err != nil {
			return dataset.Expression{}, "", nil, err
		}
		if err := registerMeasureExpression(
			input.contract, outer, measureExpressions, measureTypes, measureVersions,
		); err != nil {
			return dataset.Expression{}, "", nil, err
		}
	}
	expression, canonicalType, err := compileMetricFormula(metric, measureExpressions, measureTypes, measureVersions)
	return expression, canonicalType, preAggregations, err
}

func compileMetricMeasureInputs(
	metric MetricContract,
	fields map[askdata.ID]FieldContract,
) ([]compiledMeasureInput, error) {
	defaultFilter, err := decodeSemanticAST(metric.DefaultFilterAST)
	if err != nil {
		return nil, fmt.Errorf("default filter AST: %w", err)
	}
	var predicate *dataset.Expression
	if defaultFilter.Type != "TRUE" {
		translator := astTranslator{fields: fields, referencedFields: map[askdata.ID]struct{}{}}
		converted, err := translator.translate(defaultFilter, astFilter, 0)
		if err != nil {
			return nil, fmt.Errorf("default filter AST: %w", err)
		}
		if !semanticBooleanType(defaultFilter.Type) {
			return nil, errors.New("default filter root must be boolean")
		}
		predicate = &converted
	}

	inputs := make([]compiledMeasureInput, 0, len(metric.Measures))
	for _, measure := range metric.Measures {
		formula, err := decodeSemanticAST(measure.FormulaAST)
		if err != nil {
			return nil, fmt.Errorf("measure %s formula AST: %w", measure.MeasureVersionID, err)
		}
		translator := astTranslator{fields: fields, referencedFields: map[askdata.ID]struct{}{}}
		scalar, err := translator.translate(formula, astMeasure, 0)
		if err != nil {
			return nil, fmt.Errorf("measure %s formula AST: %w", measure.MeasureVersionID, err)
		}
		if len(translator.referencedFields) == 0 || semanticBooleanType(formula.Type) || formula.Type == "ARRAY" {
			return nil, fmt.Errorf("measure %s formula must reference a numeric model field", measure.MeasureVersionID)
		}
		if measure.Aggregation != registry.AggregationCount &&
			measure.Aggregation != registry.AggregationCountDistinct &&
			!semanticNumericExpression(formula, fields) {
			return nil, fmt.Errorf("measure %s formula is not provably numeric", measure.MeasureVersionID)
		}
		if predicate != nil {
			condition := cloneDatasetExpression(*predicate)
			value := scalar
			scalar = dataset.Expression{
				Type: "CASE", Whens: []dataset.CaseBranch{{When: condition, Then: value}},
				Else: &dataset.Expression{Type: "LITERAL", Value: nil},
			}
		}
		inputs = append(inputs, compiledMeasureInput{contract: measure, scalar: scalar})
	}
	return inputs, nil
}

func registerMeasureExpression(
	measure MeasureContract,
	expression dataset.Expression,
	expressions map[askdata.ID]dataset.Expression,
	types map[askdata.ID]string,
	versions map[askdata.ID]askdata.ID,
) error {
	for _, reference := range []askdata.ID{measure.MeasureID, measure.MeasureVersionID} {
		if existing, duplicate := versions[reference]; duplicate && existing != measure.MeasureVersionID {
			return fmt.Errorf("measure reference %s is ambiguous", reference)
		}
		expressions[reference] = expression
		types[reference] = string(measure.DataType)
		versions[reference] = measure.MeasureVersionID
	}
	return nil
}

func compileMetricFormula(
	metric MetricContract,
	measureExpressions map[askdata.ID]dataset.Expression,
	measureTypes map[askdata.ID]string,
	measureVersions map[askdata.ID]askdata.ID,
) (dataset.Expression, string, error) {
	formula, err := decodeSemanticAST(metric.FormulaAST)
	if err != nil {
		return dataset.Expression{}, "", fmt.Errorf("metric formula AST: %w", err)
	}
	translator := astTranslator{
		measures: measureExpressions, measureVersions: measureVersions,
		referencedMeasure: map[askdata.ID]struct{}{}, zeroPolicy: metric.ZeroDenominatorPolicy,
	}
	expression, err := translator.translate(formula, astMetric, 0)
	if err != nil {
		return dataset.Expression{}, "", fmt.Errorf("metric formula AST: %w", err)
	}
	if len(translator.referencedMeasure) != len(metric.Measures) {
		return dataset.Expression{}, "", errors.New("metric formula must reference its exact declared measure set")
	}
	for _, measure := range metric.Measures {
		if _, exists := translator.referencedMeasure[measure.MeasureVersionID]; !exists {
			return dataset.Expression{}, "", fmt.Errorf("metric formula does not reference measure %s", measure.MeasureVersionID)
		}
	}
	zeroDenominatorMustRemainNull := metric.ZeroDenominatorPolicy == registry.ZeroDenominatorNull &&
		semanticASTContainsType(formula, "DIVIDE")
	if metric.NullPolicy == "ZERO" && !zeroDenominatorMustRemainNull {
		expression = dataset.Expression{Type: "COALESCE", Arguments: []dataset.Expression{
			expression, {Type: "LITERAL", Value: float64(0)},
		}}
	}
	canonicalType := metricExpressionType(formula, measureTypes)
	return expression, canonicalType, nil
}

func semanticASTContainsType(node semanticAST, target string) bool {
	if node.Type == target {
		return true
	}
	for _, child := range []*semanticAST{node.Argument, node.Left, node.Right, node.Lower, node.Upper} {
		if child != nil && semanticASTContainsType(*child, target) {
			return true
		}
	}
	for _, child := range node.Arguments {
		if semanticASTContainsType(child, target) {
			return true
		}
	}
	return false
}

func decodeSemanticAST(raw json.RawMessage) (semanticAST, error) {
	var result semanticAST
	if err := askdata.DecodeStrictJSON(raw, &result); err != nil {
		return semanticAST{}, err
	}
	if err := validateSemanticAST(result, 0); err != nil {
		return semanticAST{}, err
	}
	return result, nil
}

func validateSemanticAST(node semanticAST, depth int) error {
	if depth > 48 {
		return errors.New("semantic AST exceeds maximum depth")
	}
	exact := func(required []string, optional ...string) error {
		allowed := make(map[string]bool, len(required)+len(optional))
		for _, key := range required {
			allowed[key] = true
			if !node.present[key] {
				return fmt.Errorf("%s requires %s", node.Type, key)
			}
		}
		for _, key := range optional {
			allowed[key] = true
		}
		for key := range node.present {
			if !allowed[key] {
				return fmt.Errorf("%s does not allow %s", node.Type, key)
			}
		}
		return nil
	}
	if !node.present["type"] || node.Type == "" {
		return errors.New("semantic AST type is required")
	}
	var children []*semanticAST
	switch node.Type {
	case "FIELD_REF":
		if err := exact([]string{"type", "fieldId"}); err != nil {
			return err
		}
		if node.FieldID.Validate() != nil {
			return errors.New("FIELD_REF fieldId is invalid")
		}
	case "MEASURE_REF":
		if node.present["measureVersionId"] == node.present["measureId"] {
			return errors.New("MEASURE_REF requires exactly one measure identifier")
		}
		if err := exact([]string{"type"}, "measureVersionId", "measureId"); err != nil {
			return err
		}
		if node.measureReference().Validate() != nil {
			return errors.New("MEASURE_REF identifier is invalid")
		}
	case "LITERAL":
		if err := exact([]string{"type", "value"}); err != nil {
			return err
		}
		if _, err := decodeLiteral(node.Value); err != nil {
			return err
		}
	case "TRUE", "FALSE":
		if err := exact([]string{"type"}); err != nil {
			return err
		}
	case "ARRAY":
		if err := exact([]string{"type", "arguments"}); err != nil {
			return err
		}
		if len(node.Arguments) < 1 || len(node.Arguments) > 1000 {
			return errors.New("ARRAY must contain 1 to 1000 items")
		}
		for index := range node.Arguments {
			children = append(children, &node.Arguments[index])
			if node.Arguments[index].Type != "LITERAL" {
				return errors.New("ARRAY accepts only governed literal items")
			}
		}
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE", "COALESCE", "AND", "OR":
		if err := exact([]string{"type", "arguments"}); err != nil {
			return err
		}
		if len(node.Arguments) < 2 || len(node.Arguments) > 32 {
			return fmt.Errorf("%s must contain 2 to 32 arguments", node.Type)
		}
		for index := range node.Arguments {
			children = append(children, &node.Arguments[index])
			if (node.Type == "AND" || node.Type == "OR") && !semanticBooleanType(node.Arguments[index].Type) {
				return fmt.Errorf("%s accepts only boolean arguments", node.Type)
			}
		}
	case "ABS", "FLOOR", "CEIL", "NOT", "IS_NULL", "IS_NOT_NULL":
		if err := exact([]string{"type", "argument"}); err != nil {
			return err
		}
		if node.Argument == nil {
			return fmt.Errorf("%s argument is required", node.Type)
		}
		if node.Type == "NOT" && !semanticBooleanType(node.Argument.Type) {
			return errors.New("NOT requires a boolean argument")
		}
		children = append(children, node.Argument)
	case "ROUND":
		if err := exact([]string{"type", "arguments"}); err != nil {
			return err
		}
		if len(node.Arguments) != 2 {
			return errors.New("ROUND requires value and precision")
		}
		children = append(children, &node.Arguments[0], &node.Arguments[1])
	case "CAST":
		if err := exact([]string{"type", "targetType", "argument"}); err != nil {
			return err
		}
		if !validASTTargetType(node.TargetType) || node.Argument == nil {
			return errors.New("CAST targetType or argument is invalid")
		}
		children = append(children, node.Argument)
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "IN", "NOT_IN":
		if err := exact([]string{"type", "left", "right"}); err != nil {
			return err
		}
		if node.Left == nil || node.Right == nil {
			return fmt.Errorf("%s requires both operands", node.Type)
		}
		if (node.Type == "IN" || node.Type == "NOT_IN") && node.Right.Type != "ARRAY" {
			return fmt.Errorf("%s right operand must be ARRAY", node.Type)
		}
		children = append(children, node.Left, node.Right)
	case "BETWEEN":
		if err := exact([]string{"type", "left", "lower", "upper"}); err != nil {
			return err
		}
		if node.Left == nil || node.Lower == nil || node.Upper == nil {
			return errors.New("BETWEEN requires value and bounds")
		}
		children = append(children, node.Left, node.Lower, node.Upper)
	default:
		return fmt.Errorf("unsupported semantic AST type %q", node.Type)
	}
	for _, child := range children {
		if err := validateSemanticAST(*child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (translator *astTranslator) translate(node semanticAST, mode astMode, depth int) (dataset.Expression, error) {
	if depth > 48 {
		return dataset.Expression{}, errors.New("semantic AST exceeds maximum depth")
	}
	switch node.Type {
	case "FIELD_REF":
		if mode == astMetric {
			return dataset.Expression{}, errors.New("metric formula cannot bypass declared measures")
		}
		field, exists := translator.fields[node.FieldID]
		if !exists {
			return dataset.Expression{}, fmt.Errorf("field %s is not in the semantic model", node.FieldID)
		}
		translator.referencedFields[node.FieldID] = struct{}{}
		return fieldReference(field), nil
	case "MEASURE_REF":
		if mode != astMetric {
			return dataset.Expression{}, errors.New("measure references are allowed only in metric formulas")
		}
		id := node.measureReference()
		expression, exists := translator.measures[id]
		if !exists {
			return dataset.Expression{}, fmt.Errorf("measure %s is not declared by the metric", id)
		}
		translator.referencedMeasure[translator.measureVersions[id]] = struct{}{}
		return cloneDatasetExpression(expression), nil
	case "LITERAL":
		value, err := decodeLiteral(node.Value)
		if err != nil {
			return dataset.Expression{}, err
		}
		return dataset.Expression{Type: "LITERAL", Value: value}, nil
	case "TRUE", "FALSE":
		if mode != astFilter {
			return dataset.Expression{}, fmt.Errorf("%s is allowed only in a default filter", node.Type)
		}
		left := dataset.Expression{Type: "LITERAL", Value: float64(1)}
		rightValue := float64(1)
		if node.Type == "FALSE" {
			rightValue = 0
		}
		right := dataset.Expression{Type: "LITERAL", Value: rightValue}
		return dataset.Expression{Type: "EQUALS", Left: &left, Right: &right}, nil
	case "ARRAY":
		if mode != astFilter {
			return dataset.Expression{}, errors.New("ARRAY is allowed only in a default filter")
		}
		arguments, err := translator.translateArguments(node.Arguments, mode, depth)
		return dataset.Expression{Type: "ARRAY", Arguments: arguments}, err
	case "ADD", "SUBTRACT", "MULTIPLY", "COALESCE":
		arguments, err := translator.translateArguments(node.Arguments, mode, depth)
		return dataset.Expression{Type: node.Type, Arguments: arguments}, err
	case "DIVIDE":
		arguments, err := translator.translateArguments(node.Arguments, mode, depth)
		if err != nil {
			return dataset.Expression{}, err
		}
		if mode == astMetric {
			for index := 1; index < len(arguments); index++ {
				arguments[index] = dataset.Expression{Type: "NULLIF", Arguments: []dataset.Expression{
					arguments[index], {Type: "LITERAL", Value: float64(0)},
				}}
			}
		}
		result := dataset.Expression{Type: "DIVIDE", Arguments: arguments}
		if mode == astMetric && translator.zeroPolicy == registry.ZeroDenominatorZero {
			result = dataset.Expression{Type: "COALESCE", Arguments: []dataset.Expression{
				result, {Type: "LITERAL", Value: float64(0)},
			}}
		}
		return result, nil
	case "ABS", "FLOOR", "CEIL":
		argument, err := translator.translate(*node.Argument, mode, depth+1)
		return dataset.Expression{Type: node.Type, Argument: &argument}, err
	case "ROUND":
		arguments, err := translator.translateArguments(node.Arguments, mode, depth)
		return dataset.Expression{Type: "ROUND", Arguments: arguments}, err
	case "CAST":
		argument, err := translator.translate(*node.Argument, mode, depth+1)
		return dataset.Expression{Type: "CAST", TargetType: node.TargetType, Argument: &argument}, err
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "IN", "NOT_IN":
		if mode != astFilter {
			return dataset.Expression{}, fmt.Errorf("%s is allowed only in a default filter", node.Type)
		}
		left, err := translator.translate(*node.Left, mode, depth+1)
		if err != nil {
			return dataset.Expression{}, err
		}
		right, err := translator.translate(*node.Right, mode, depth+1)
		return dataset.Expression{Type: node.Type, Left: &left, Right: &right}, err
	case "BETWEEN":
		if mode != astFilter {
			return dataset.Expression{}, errors.New("BETWEEN is allowed only in a default filter")
		}
		left, err := translator.translate(*node.Left, mode, depth+1)
		if err != nil {
			return dataset.Expression{}, err
		}
		lower, err := translator.translate(*node.Lower, mode, depth+1)
		if err != nil {
			return dataset.Expression{}, err
		}
		upper, err := translator.translate(*node.Upper, mode, depth+1)
		return dataset.Expression{Type: "BETWEEN", Left: &left, Lower: &lower, Upper: &upper}, err
	case "IS_NULL", "IS_NOT_NULL", "NOT":
		if mode != astFilter {
			return dataset.Expression{}, fmt.Errorf("%s is allowed only in a default filter", node.Type)
		}
		argument, err := translator.translate(*node.Argument, mode, depth+1)
		return dataset.Expression{Type: node.Type, Argument: &argument}, err
	case "AND", "OR":
		if mode != astFilter {
			return dataset.Expression{}, fmt.Errorf("%s is allowed only in a default filter", node.Type)
		}
		arguments, err := translator.translateArguments(node.Arguments, mode, depth)
		return dataset.Expression{Type: node.Type, Arguments: arguments}, err
	default:
		return dataset.Expression{}, fmt.Errorf("unsupported semantic AST type %q", node.Type)
	}
}

func (translator *astTranslator) translateArguments(nodes []semanticAST, mode astMode, depth int) ([]dataset.Expression, error) {
	result := make([]dataset.Expression, 0, len(nodes))
	for _, node := range nodes {
		converted, err := translator.translate(node, mode, depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (node semanticAST) measureReference() askdata.ID {
	if node.MeasureVersionID != "" {
		return node.MeasureVersionID
	}
	return node.MeasureID
}

func decodeLiteral(raw json.RawMessage) (any, error) {
	if raw == nil {
		return nil, errors.New("LITERAL value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("LITERAL value is invalid")
	}
	switch typed := value.(type) {
	case nil, bool:
		return typed, nil
	case string:
		if len(typed) > 4096 || !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return nil, errors.New("LITERAL string is invalid")
		}
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, errors.New("LITERAL number must be finite")
		}
		return typed, nil
	default:
		return nil, errors.New("LITERAL must be a scalar JSON value")
	}
}

func semanticBooleanType(value string) bool {
	switch value {
	case "TRUE", "FALSE", "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE",
		"IN", "NOT_IN", "BETWEEN", "IS_NULL", "IS_NOT_NULL", "NOT", "AND", "OR":
		return true
	default:
		return false
	}
}

func validASTTargetType(value string) bool {
	switch value {
	case "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME":
		return true
	default:
		return false
	}
}

func metricExpressionType(node semanticAST, measureTypes map[askdata.ID]string) string {
	switch node.Type {
	case "MEASURE_REF":
		if value := measureTypes[node.measureReference()]; value == string(registry.NumericInteger) {
			return "INTEGER"
		}
		return "DECIMAL"
	case "CAST":
		if node.TargetType == "INTEGER" || node.TargetType == "DECIMAL" {
			return node.TargetType
		}
	case "FLOOR", "CEIL":
		return "INTEGER"
	}
	return "DECIMAL"
}

func semanticNumericExpression(node semanticAST, fields map[askdata.ID]FieldContract) bool {
	switch node.Type {
	case "FIELD_REF":
		field, exists := fields[node.FieldID]
		return exists && (field.CanonicalType == "INTEGER" || field.CanonicalType == "DECIMAL")
	case "LITERAL":
		value, err := decodeLiteral(node.Value)
		if err != nil {
			return false
		}
		_, numeric := value.(float64)
		return numeric
	case "CAST":
		return node.TargetType == "INTEGER" || node.TargetType == "DECIMAL"
	case "ABS", "FLOOR", "CEIL":
		return node.Argument != nil && semanticNumericExpression(*node.Argument, fields)
	case "ROUND":
		return len(node.Arguments) == 2 && semanticNumericExpression(node.Arguments[0], fields)
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE", "COALESCE":
		if len(node.Arguments) < 2 {
			return false
		}
		for _, argument := range node.Arguments {
			if !semanticNumericExpression(argument, fields) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func cloneDatasetExpression(value dataset.Expression) dataset.Expression {
	raw, _ := json.Marshal(value)
	var result dataset.Expression
	_ = json.Unmarshal(raw, &result)
	return result
}
