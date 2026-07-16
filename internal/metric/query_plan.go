package metric

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/dataset"
)

type validatedDefinition struct {
	prepared           Prepared
	datasetVersion     dataset.VersionRecord
	datasetDocument    dataset.Document
	dependencies       map[string]Prepared
	duplicateSensitive bool
}

const (
	maxExpandedMetricNodes = 2048
	maxExpandedMetricDepth = 64
)

type expandedMetricExpression struct {
	expression dataset.Expression
	nodes      int
	depth      int
}

// buildQueryCandidate 将逻辑指标字段引用展开为精确数据集版本上的受控派生 DSL。
func buildQueryCandidate(metricID, metricVersionID string, validated validatedDefinition, requestedDimensions []string) (QueryCandidate, error) {
	definition := validated.prepared.Definition
	fieldsByID := make(map[string]dataset.Field, len(validated.datasetDocument.Fields))
	for _, field := range validated.datasetDocument.Fields {
		fieldsByID[field.ID] = field
	}
	allowed := make(map[string]Dimension, len(definition.AllowedDimensions))
	for _, dimension := range definition.AllowedDimensions {
		allowed[dimension.FieldID] = dimension
	}
	seen := map[string]bool{}
	selectedFields := make([]dataset.Field, 0, len(requestedDimensions)+1)
	groupBy := make([]string, 0, len(requestedDimensions))
	sorts := make([]dataset.Sort, 0, len(requestedDimensions))
	grainCodes := make([]string, 0, len(requestedDimensions))
	timeFieldCode, defaultTimeGrain := "", ""
	for index, fieldID := range requestedDimensions {
		dimension, exists := allowed[fieldID]
		field, fieldExists := fieldsByID[fieldID]
		if !exists || !fieldExists || seen[fieldID] {
			return QueryCandidate{}, invalid(fmt.Sprintf("dimensionFieldIds[%d]", index), "METRIC_PREVIEW_DIMENSION_INVALID", "试算维度不在指标允许范围内或发生重复")
		}
		seen[fieldID] = true
		if fieldID == definition.TimeFieldID {
			argument := cloneDatasetExpression(field.Expression)
			field.Expression = dataset.Expression{Type: "DATE_TRUNC", Unit: definition.TimeGrain, Argument: &argument}
			timeFieldCode, defaultTimeGrain = field.Code, definition.TimeGrain
		}
		field.Role, field.Aggregation = "DIMENSION", ""
		selectedFields = append(selectedFields, field)
		groupBy = append(groupBy, field.ID)
		grainCodes = append(grainCodes, field.Code)
		sorts = append(sorts, dataset.Sort{FieldID: field.ID, Direction: dimension.SortDirection})
	}

	metricExpression, err := expandMetricExpression(definition, fieldsByID, validated.dependencies, map[string]bool{})
	if err != nil {
		return QueryCandidate{}, err
	}
	metricFieldID := uniqueMetricFieldID(fieldsByID)
	metricType := "DECIMAL"
	if definition.Aggregation == "COUNT" || definition.Aggregation == "COUNT_DISTINCT" {
		metricType = "INTEGER"
	}
	selectedFields = append(selectedFields, dataset.Field{
		ID: metricFieldID, Code: definition.Metric.Code, Name: definition.Metric.Name, Role: "MEASURE",
		Expression: metricExpression, CanonicalType: metricType, Format: definition.NumberFormat,
		Unit: definition.Unit, Nullable: true,
	})
	if len(grainCodes) == 0 {
		grainCodes = []string{definition.Metric.Code}
	}
	document := validated.datasetDocument
	document.Fields = selectedFields
	document.GroupBy = groupBy
	document.Having = []dataset.Filter{}
	document.Sorts = sorts
	document.OutputGrain = dataset.OutputGrain{
		Description: "指标 " + definition.Metric.Name + " 的试算粒度",
		KeyFields:   grainCodes, TimeField: timeFieldCode, DefaultTimeGrain: defaultTimeGrain,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return QueryCandidate{}, ErrInvalidDefinition
	}
	preparedDataset, err := dataset.Prepare(raw)
	if err != nil {
		return QueryCandidate{}, invalid("expression", "METRIC_QUERY_PLAN_INVALID", "指标定义无法生成安全查询计划")
	}
	return QueryCandidate{
		MetricID: metricID, MetricVersionID: metricVersionID,
		DatasetID: definition.DatasetID, DatasetVersionID: definition.DatasetVersionID,
		DSL: preparedDataset.DSLJSON, PlanHash: preparedDataset.PlanHash,
	}, nil
}

func expandMetricExpression(definition Definition, fields map[string]dataset.Field, dependencies map[string]Prepared, visiting map[string]bool) (dataset.Expression, error) {
	expanded, err := expandMetricDefinition(definition, fields, dependencies, visiting)
	if err != nil {
		return dataset.Expression{}, err
	}
	return expanded.expression, nil
}

func expandMetricDefinition(definition Definition, fields map[string]dataset.Field, dependencies map[string]Prepared, visiting map[string]bool) (expandedMetricExpression, error) {
	expanded, err := expandExpressionNode(definition.Expression, fields, dependencies, visiting)
	if err != nil {
		return expandedMetricExpression{}, err
	}
	if definition.Aggregation == "NONE" {
		return expanded, nil
	}
	argument := expanded.expression
	expanded.expression = dataset.Expression{Type: "AGGREGATE", Function: definition.Aggregation, Argument: &argument}
	expanded.nodes++
	expanded.depth++
	if err := validateExpandedMetricBudget(expanded); err != nil {
		return expandedMetricExpression{}, err
	}
	return expanded, nil
}

func expandExpressionNode(expression Expression, fields map[string]dataset.Field, dependencies map[string]Prepared, visiting map[string]bool) (expandedMetricExpression, error) {
	switch expression.Type {
	case "FIELD_REF":
		field, exists := fields[expression.FieldID]
		if !exists {
			return expandedMetricExpression{}, invalid("expression.fieldId", "METRIC_FIELD_NOT_FOUND", "指标字段不属于指定数据集版本")
		}
		nodes, depth := datasetExpressionComplexity(field.Expression)
		expanded := expandedMetricExpression{expression: cloneDatasetExpression(field.Expression), nodes: nodes, depth: depth}
		if err := validateExpandedMetricBudget(expanded); err != nil {
			return expandedMetricExpression{}, err
		}
		return expanded, nil
	case "METRIC_REF":
		dependency, exists := dependencies[expression.MetricVersionID]
		if !exists || visiting[expression.MetricVersionID] {
			return expandedMetricExpression{}, invalid("expression.metricVersionId", "METRIC_REFERENCE_CYCLE", "指标依赖不存在或形成引用循环")
		}
		visiting[expression.MetricVersionID] = true
		result, err := expandMetricDefinition(dependency.Definition, fields, dependencies, visiting)
		delete(visiting, expression.MetricVersionID)
		return result, err
	case "LITERAL":
		literal := dataset.Expression{Type: "LITERAL", Value: expression.Value}
		return expandedMetricExpression{
			expression: dataset.Expression{Type: "CAST", TargetType: "DECIMAL", Argument: &literal},
			nodes:      2, depth: 2,
		}, nil
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
		arguments := make([]dataset.Expression, 0, len(expression.Arguments))
		expandedArguments := make([]expandedMetricExpression, 0, len(expression.Arguments))
		nodes, depth := 1, 1
		for _, argument := range expression.Arguments {
			expanded, err := expandExpressionNode(argument, fields, dependencies, visiting)
			if err != nil {
				return expandedMetricExpression{}, err
			}
			expandedArguments = append(expandedArguments, expanded)
			arguments = append(arguments, expanded.expression)
			nodes += expanded.nodes
			depth = max(depth, expanded.depth+1)
		}
		operation := dataset.Expression{Type: expression.Type, Arguments: arguments}
		if expression.Type != "DIVIDE" || len(arguments) != 2 {
			expanded := expandedMetricExpression{expression: operation, nodes: nodes, depth: depth}
			if err := validateExpandedMetricBudget(expanded); err != nil {
				return expandedMetricExpression{}, err
			}
			return expanded, nil
		}
		// 除零语义在所有数据库方言中固定为 NULL，不能依赖源库的隐式行为。
		zeroLiteral := dataset.Expression{Type: "LITERAL", Value: "0"}
		zero := dataset.Expression{Type: "CAST", TargetType: "DECIMAL", Argument: &zeroLiteral}
		denominator := cloneDatasetExpression(arguments[1])
		condition := dataset.Expression{Type: "EQUALS", Left: &denominator, Right: &zero}
		nullValue := dataset.Expression{Type: "LITERAL", Value: nil}
		expanded := expandedMetricExpression{
			expression: dataset.Expression{
				Type: "CASE", Whens: []dataset.CaseBranch{{When: condition, Then: nullValue}}, Else: &operation,
			},
			// CASE、EQUALS、零值 CAST/LITERAL、NULL 和原运算共增加 6 个节点，
			// 分母在条件和实际除法中出现两次，必须按最终序列化规模重复计费。
			nodes: 6 + expandedArguments[0].nodes + 2*expandedArguments[1].nodes,
			depth: 1 + max(1+max(expandedArguments[1].depth, 2), 1+max(expandedArguments[0].depth, expandedArguments[1].depth)),
		}
		if err := validateExpandedMetricBudget(expanded); err != nil {
			return expandedMetricExpression{}, err
		}
		return expanded, nil
	default:
		return expandedMetricExpression{}, invalid("expression.type", "METRIC_EXPRESSION_TYPE_UNSUPPORTED", "表达式类型不受支持")
	}
}

func validateExpandedMetricBudget(expanded expandedMetricExpression) error {
	if expanded.nodes > maxExpandedMetricNodes || expanded.depth > maxExpandedMetricDepth {
		return invalid("expression", "METRIC_EXPANDED_EXPRESSION_COMPLEXITY_EXCEEDED", "跨指标展开后的表达式不能超过 2048 个节点或 64 层")
	}
	return nil
}

func datasetExpressionComplexity(expression dataset.Expression) (nodes, depth int) {
	nodes, depth = 1, 1
	visit := func(child *dataset.Expression) {
		if child == nil {
			return
		}
		childNodes, childDepth := datasetExpressionComplexity(*child)
		nodes += childNodes
		depth = max(depth, childDepth+1)
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		visit(child)
	}
	for index := range expression.Arguments {
		visit(&expression.Arguments[index])
	}
	for index := range expression.Whens {
		visit(&expression.Whens[index].When)
		visit(&expression.Whens[index].Then)
	}
	return nodes, depth
}

func uniqueMetricFieldID(fields map[string]dataset.Field) string {
	for index := 1; ; index++ {
		candidate := "metric_value"
		if index > 1 {
			candidate = fmt.Sprintf("metric_value_%d", index)
		}
		if _, exists := fields[candidate]; !exists {
			return candidate
		}
	}
}

func cloneDatasetExpression(expression dataset.Expression) dataset.Expression {
	raw, _ := json.Marshal(expression)
	var result dataset.Expression
	_ = json.Unmarshal(raw, &result)
	return result
}

func expressionContainsAggregate(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" {
		return true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil && expressionContainsAggregate(*child) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if expressionContainsAggregate(child) {
			return true
		}
	}
	for _, branch := range expression.Whens {
		if expressionContainsAggregate(branch.When) || expressionContainsAggregate(branch.Then) {
			return true
		}
	}
	return false
}

func expressionNodeIDs(expression dataset.Expression, target map[string]bool) {
	if expression.Type == "FIELD_REF" {
		target[expression.NodeID] = true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			expressionNodeIDs(*child, target)
		}
	}
	for _, child := range expression.Arguments {
		expressionNodeIDs(child, target)
	}
	for _, branch := range expression.Whens {
		expressionNodeIDs(branch.When, target)
		expressionNodeIDs(branch.Then, target)
	}
}
