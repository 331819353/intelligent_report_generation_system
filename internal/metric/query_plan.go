package metric

import (
	"crypto/sha256"
	"encoding/hex"
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
	maxSemanticSetValues   = 128
)

type expandedMetricExpression struct {
	expression dataset.Expression
	nodes      int
	depth      int
}

// buildQueryCandidate 将逻辑指标字段引用展开为精确数据集版本上的受控派生 DSL。
func buildQueryCandidate(
	metricID, metricVersionID string,
	validated validatedDefinition,
	requestedDimensions []string,
	requestedFilters []DimensionFilter,
	metricSortDirection string,
) (QueryCandidate, map[string]any, error) {
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
			return QueryCandidate{}, nil, invalid(fmt.Sprintf("dimensionFieldIds[%d]", index), "METRIC_PREVIEW_DIMENSION_INVALID", "试算维度不在指标允许范围内或发生重复")
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
		return QueryCandidate{}, nil, err
	}
	metricFieldID := uniqueMetricFieldID(fieldsByID)
	metricOutputCode := portableMetricOutputCode(definition.Metric.Code)
	metricType := "DECIMAL"
	if definition.Aggregation == "COUNT" || definition.Aggregation == "COUNT_DISTINCT" {
		metricType = "INTEGER"
	}
	selectedFields = append(selectedFields, dataset.Field{
		ID: metricFieldID, Code: metricOutputCode, Name: definition.Metric.Name, Role: "MEASURE",
		Expression: metricExpression, CanonicalType: metricType, Format: definition.NumberFormat,
		Unit: definition.Unit, Nullable: true,
	})
	if metricSortDirection != "" {
		if metricSortDirection != "ASC" && metricSortDirection != "DESC" {
			return QueryCandidate{}, nil, invalid(
				"metricSortDirection", "METRIC_PREVIEW_SORT_INVALID",
				"指标排序方向仅支持 ASC 或 DESC",
			)
		}
		sorts = append(
			[]dataset.Sort{{FieldID: metricFieldID, Direction: metricSortDirection}},
			sorts...,
		)
	}
	if len(grainCodes) == 0 {
		grainCodes = []string{metricOutputCode}
	}
	document := validated.datasetDocument
	// 指标查询是从精确数据集版本派生出的临时分析计划，而不是该版本本身。
	// 保留源节点、Join、预聚合和执行策略作为访问边界，但不能继续携带源
	// DIM/DWD/DWS 的整表层级合同：派生计划只投影请求维度和一个指标，
	// 原合同引用的其余字段会让合法指标无法通过 DSL 校验。省略 layer 后，
	// dataset.Prepare 会根据派生计划中的聚合语义将其确定为 DWS。
	document.Dataset.Layer = ""
	document.Dataset.SemanticContractVersion = ""
	document.Dataset.ConsumerContractID = ""
	document.FactContract = nil
	document.AnalysisContract = nil
	// DIM 的 DISTINCT 已经属于精确发布版本的物化语义。指标派生计划随后会由
	// queryruntime 改写为读取该版本的 ACTIVE 物化表，因此外层聚合不能继续
	// 携带 DISTINCT；否则推断出的 DWS 计划既无法通过层级校验，也会把去重错误
	// 地施加到最终聚合结果。
	document.Distinct = false
	document.Fields = selectedFields
	document.GroupBy = groupBy
	// The caller's selected dimensions define one ordinary preview grain.
	// A source DWS may itself have CUBE/ROLLUP/GROUPING_SETS materialization
	// semantics, but carrying those sets into this reduced projection can
	// reference omitted fields and would return several grains in one preview.
	document.GroupByMode = ""
	document.GroupingSets = nil
	document.Having = []dataset.Filter{}
	document.Sorts = sorts
	document.OutputGrain = dataset.OutputGrain{
		Description: "指标 " + definition.Metric.Name + " 的试算粒度",
		KeyFields:   grainCodes, TimeField: timeFieldCode, DefaultTimeGrain: defaultTimeGrain,
	}
	filterBindings, boundParameters, err := appendDimensionFilters(
		&document, fieldsByID, allowed, requestedFilters,
	)
	if err != nil {
		return QueryCandidate{}, nil, err
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return QueryCandidate{}, nil, ErrInvalidDefinition
	}
	preparedDataset, err := dataset.Prepare(raw)
	if err != nil {
		return QueryCandidate{}, nil, invalid("expression", "METRIC_QUERY_PLAN_INVALID", "指标定义无法生成安全查询计划")
	}
	return QueryCandidate{
		MetricID: metricID, MetricVersionID: metricVersionID,
		DatasetID: definition.DatasetID, DatasetVersionID: definition.DatasetVersionID,
		DSL: preparedDataset.DSLJSON, PlanHash: preparedDataset.PlanHash,
		FilterBindings: filterBindings,
	}, boundParameters, nil
}

// portableMetricOutputCode 将逻辑指标编码映射为 PostgreSQL 可完整保留的输出列名。
// 指标编码的历史合同允许 64 个 ASCII 字符，而 PostgreSQL 标识符上限为 63
// 字节；派生查询必须显式缩短并带摘要，不能依赖数据库静默截断。
func portableMetricOutputCode(code string) string {
	if len(code) <= 63 {
		return code
	}
	digest := sha256.Sum256([]byte(code))
	suffix := "_" + hex.EncodeToString(digest[:])[:12]
	return code[:63-len(suffix)] + suffix
}

func appendDimensionFilters(
	document *dataset.Document,
	fieldsByID map[string]dataset.Field,
	allowed map[string]Dimension,
	requested []DimensionFilter,
) ([]QueryFilterBinding, map[string]any, error) {
	if len(requested) > 16 {
		return nil, nil, invalid("dimensionFilters", "METRIC_PREVIEW_FILTER_LIMIT_EXCEEDED", "维度过滤不能超过 16 个")
	}
	usedParameters := make(map[string]bool, len(document.Parameters)+len(requested))
	for _, parameter := range document.Parameters {
		usedParameters[parameter.Code] = true
	}
	usedFilterIDs := make(map[string]bool, len(document.Filters)+len(requested))
	for _, filter := range document.Filters {
		usedFilterIDs[filter.ID] = true
	}
	seenBindings := map[string]bool{}
	bindings := make([]QueryFilterBinding, 0, len(requested))
	parameters := make(map[string]any, len(requested))
	for index, requestedFilter := range requested {
		field, fieldExists := fieldsByID[requestedFilter.FieldID]
		_, allowedField := allowed[requestedFilter.FieldID]
		operator := requestedFilter.Operator
		bindingKey := requestedFilter.FieldID + "\x00" + operator
		if !fieldExists || !allowedField || seenBindings[bindingKey] {
			return nil, nil, invalid(
				fmt.Sprintf("dimensionFilters[%d].fieldId", index),
				"METRIC_PREVIEW_FILTER_DIMENSION_INVALID",
				"过滤字段不在指标允许维度内或同一操作发生重复",
			)
		}
		setValues, setValuesValid := dimensionFilterSetValues(
			requestedFilter.Value,
		)
		setOperator := operator == "IN" || operator == "NOT_IN"
		if !oneOf(operator, "EQUALS", "NOT_EQUALS", "IN", "NOT_IN", "GTE", "LT") ||
			requestedFilter.Value == nil ||
			(setOperator && !setValuesValid) ||
			(!oneOf(operator, "EQUALS", "NOT_EQUALS", "IN", "NOT_IN") &&
				!oneOf(field.CanonicalType, "DATE", "DATETIME")) {
			return nil, nil, invalid(
				fmt.Sprintf("dimensionFilters[%d]", index),
				"METRIC_PREVIEW_FILTER_UNSUPPORTED",
				"维度过滤仅支持非空的 EQUALS/NOT_EQUALS/IN/NOT_IN，时间字段额外支持 GTE 和 LT",
			)
		}
		seenBindings[bindingKey] = true
		filterID := uniqueDimensionFilterID(usedFilterIDs, index)
		usedFilterIDs[filterID] = true
		left := cloneDatasetExpression(field.Expression)
		parameterCodes := []string{}
		var right dataset.Expression
		if setOperator {
			right = dataset.Expression{Type: "ARRAY", Arguments: []dataset.Expression{}}
			for valueIndex, value := range setValues {
				parameterCode := uniqueDimensionFilterParameter(
					usedParameters, index*maxSemanticSetValues+valueIndex,
				)
				usedParameters[parameterCode] = true
				document.Parameters = append(document.Parameters, dataset.Parameter{
					Code: parameterCode, Name: "语义维度集合过滤",
					DataType: field.CanonicalType, Required: true,
				})
				right.Arguments = append(right.Arguments, dataset.Expression{
					Type: "PARAM_REF", Code: parameterCode,
				})
				parameterCodes = append(parameterCodes, parameterCode)
				parameters[parameterCode] = value
			}
		} else {
			parameterCode := uniqueDimensionFilterParameter(
				usedParameters, index,
			)
			usedParameters[parameterCode] = true
			document.Parameters = append(document.Parameters, dataset.Parameter{
				Code: parameterCode, Name: "语义维度过滤",
				DataType: field.CanonicalType, Required: true,
			})
			right = dataset.Expression{Type: "PARAM_REF", Code: parameterCode}
			parameterCodes = append(parameterCodes, parameterCode)
			parameters[parameterCode] = requestedFilter.Value
		}
		document.Filters = append(document.Filters, dataset.Filter{
			ID:    filterID,
			Stage: "PRE_AGGREGATION", Optional: false,
			Expression: dataset.Expression{
				Type: operator, Left: &left, Right: &right,
			},
		})
		bindings = append(bindings, QueryFilterBinding{
			FieldID:        requestedFilter.FieldID,
			FilterID:       filterID,
			ParameterCode:  parameterCodes[0],
			ParameterCodes: parameterCodes,
			DataType:       field.CanonicalType,
			Operator:       operator,
		})
	}
	return bindings, parameters, nil
}

func dimensionFilterSetValues(value any) ([]any, bool) {
	var result []any
	switch values := value.(type) {
	case []string:
		result = make([]any, 0, len(values))
		for _, item := range values {
			result = append(result, item)
		}
	case []any:
		result = append([]any(nil), values...)
	default:
		return nil, false
	}
	if len(result) < 2 || len(result) > maxSemanticSetValues {
		return nil, false
	}
	for _, item := range result {
		if item == nil {
			return nil, false
		}
	}
	return result, true
}

func uniqueDimensionFilterID(used map[string]bool, index int) string {
	for suffix := index + 1; ; suffix++ {
		candidate := fmt.Sprintf("semantic_dimension_filter_%d", suffix)
		if !used[candidate] {
			return candidate
		}
	}
}

func uniqueDimensionFilterParameter(used map[string]bool, index int) string {
	for suffix := index + 1; ; suffix++ {
		candidate := fmt.Sprintf("semantic_dimension_filter_%d", suffix)
		if !used[candidate] {
			return candidate
		}
	}
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
