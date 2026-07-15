package federation

import (
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/queryruntime"
)

type aggregateFieldUse struct {
	functions map[string]bool
	blocked   bool
}

type sourceAggregationPlan struct {
	Document    dataset.Document
	Projections map[string]map[string]querycompiler.ScanAggregateProjection
}

// planSourceAggregations 只为可证明处于所有 Join“多”侧的数据库节点生成预聚合。
// SUM/MIN/MAX 可直接归并；COUNT 改为部分计数求和，AVG 改为 SUM/COUNT 加权归并。
func planSourceAggregations(document dataset.Document, resolved queryruntime.ResolvedPlan, columnPolicies []policy.ColumnPolicy) sourceAggregationPlan {
	fallback := sourceAggregationPlan{Document: document}
	fieldsByCode := map[string]dataset.Field{}
	for _, field := range document.Fields {
		fieldsByCode[field.Code] = field
	}
	for _, item := range columnPolicies {
		if item.MinimumGroupSize > 0 {
			// 最小分组人数必须基于 Join 后原始行数，预聚合会破坏该计数。
			return fallback
		}
		if item.PolicyType == "AGGREGATE_ONLY" {
			field, exists := fieldsByCode[item.FieldCode]
			if exists && (expressionHasAggregateFunction(field.Expression, "COUNT") || expressionHasAggregateFunction(field.Expression, "AVG")) {
				// COUNT/AVG 会改写为组合表达式，不能绕过策略对直接聚合函数的校验。
				return fallback
			}
		}
	}
	grouped := map[string]bool{}
	for _, fieldID := range document.GroupBy {
		grouped[fieldID] = true
	}
	for _, field := range document.Fields {
		if !expressionHasAggregate(field.Expression) && !grouped[field.ID] {
			// 非标准的“聚合加未分组明细”依赖首行语义，不做顺序可能变化的下推。
			return fallback
		}
	}

	uses := map[string]*aggregateFieldUse{}
	unsupported := false
	inspect := func(expression dataset.Expression) {}
	inspect = func(expression dataset.Expression) {
		if expression.Type == "AGGREGATE" {
			function := strings.ToUpper(expression.Function)
			if function == "COUNT" && expression.Argument == nil {
				// COUNT(*) 无法归属单个节点，外连接下也不能用某一侧部分计数代替。
				unsupported = true
				return
			}
			if expression.Argument != nil && expression.Argument.Type == "FIELD_REF" && isComposableAggregate(function) {
				key := fieldUseKey(expression.Argument.NodeID, expression.Argument.Field)
				if uses[key] == nil {
					uses[key] = &aggregateFieldUse{functions: map[string]bool{}}
				}
				uses[key].functions[function] = true
				return
			}
			if function == "COUNT" || function == "AVG" {
				// 复杂参数的重复次数不能由单列部分状态还原，整条计划回退。
				unsupported = true
			}
			if expression.Argument != nil {
				markAggregateFieldsBlocked(*expression.Argument, uses)
			}
			return
		}
		if expression.Type == "FIELD_REF" {
			key := fieldUseKey(expression.NodeID, expression.Field)
			if uses[key] == nil {
				uses[key] = &aggregateFieldUse{functions: map[string]bool{}}
			}
			uses[key].blocked = true
		}
		visitAggregationChildren(expression, inspect)
	}
	forEachDocumentExpression(document, inspect)
	if unsupported {
		return fallback
	}

	eligible := map[string]bool{}
	for key, use := range uses {
		if use.blocked || len(use.functions) == 0 {
			continue
		}
		nodeID, _, ok := splitFieldUseKey(key)
		if !ok || !nodeIsSafeManySide(nodeID, document.Joins) {
			continue
		}
		node, exists := resolved.Nodes[nodeID]
		if !exists || node.SourceType == datasource.TypeExcel {
			continue
		}
		eligible[key] = true
	}
	if len(eligible) == 0 {
		return fallback
	}

	transformed := cloneAggregationDocument(document)
	projections := map[string]map[string]querycompiler.ScanAggregateProjection{}
	aliases := map[string]string{}
	additional := map[string][]string{}
	existing := map[string]map[string]bool{}
	for _, node := range transformed.Nodes {
		existing[node.ID] = map[string]bool{}
		for _, name := range node.Projection {
			existing[node.ID][name] = true
		}
	}
	sequence := 0
	ensureProjection := func(nodeID, sourceField, function string) string {
		stateKey := nodeID + "\x00" + sourceField + "\x00" + function
		if alias := aliases[stateKey]; alias != "" {
			return alias
		}
		for {
			sequence++
			alias := fmt.Sprintf("partial_%s_%d", strings.ToLower(function), sequence)
			if existing[nodeID][alias] {
				continue
			}
			existing[nodeID][alias] = true
			aliases[stateKey] = alias
			additional[nodeID] = append(additional[nodeID], alias)
			if projections[nodeID] == nil {
				projections[nodeID] = map[string]querycompiler.ScanAggregateProjection{}
			}
			projections[nodeID][alias] = querycompiler.ScanAggregateProjection{SourceField: sourceField, Function: function}
			return alias
		}
	}
	rewrite := func(expression dataset.Expression) dataset.Expression { return expression }
	rewrite = func(expression dataset.Expression) dataset.Expression {
		if expression.Type == "AGGREGATE" && expression.Argument != nil && expression.Argument.Type == "FIELD_REF" {
			argument := *expression.Argument
			if eligible[fieldUseKey(argument.NodeID, argument.Field)] {
				return rewritePartialAggregate(expression.Function, argument, ensureProjection)
			}
		}
		return rewriteAggregationChildren(expression, rewrite)
	}
	for index := range transformed.Fields {
		transformed.Fields[index].Expression = rewrite(transformed.Fields[index].Expression)
	}
	for index := range transformed.Having {
		transformed.Having[index].Expression = rewrite(transformed.Having[index].Expression)
	}
	for index := range transformed.Nodes {
		node := &transformed.Nodes[index]
		projection := make([]string, 0, len(node.Projection)+len(additional[node.ID]))
		for _, name := range node.Projection {
			if !eligible[fieldUseKey(node.ID, name)] {
				projection = append(projection, name)
			}
		}
		projection = append(projection, additional[node.ID]...)
		node.Projection = projection
	}
	if err := dataset.Validate(transformed); err != nil {
		// 内部改写不应扩大可执行语法；若校验器无法证明其合法性则回退原计划。
		return fallback
	}
	return sourceAggregationPlan{Document: transformed, Projections: projections}
}

func rewritePartialAggregate(function string, argument dataset.Expression, ensure func(string, string, string) string) dataset.Expression {
	field := func(alias string) dataset.Expression {
		return dataset.Expression{Type: "FIELD_REF", NodeID: argument.NodeID, Field: alias}
	}
	aggregate := func(name, alias string) dataset.Expression {
		value := field(alias)
		return dataset.Expression{Type: "AGGREGATE", Function: name, Argument: &value}
	}
	function = strings.ToUpper(function)
	switch function {
	case "SUM", "MIN", "MAX":
		alias := ensure(argument.NodeID, argument.Field, function)
		return aggregate(function, alias)
	case "COUNT":
		alias := ensure(argument.NodeID, argument.Field, "COUNT")
		sum := aggregate("SUM", alias)
		zero := dataset.Expression{Type: "LITERAL", Value: int64(0)}
		coalesced := dataset.Expression{Type: "COALESCE", Arguments: []dataset.Expression{sum, zero}}
		return dataset.Expression{Type: "CAST", TargetType: "INTEGER", Argument: &coalesced}
	case "AVG":
		sumAlias := ensure(argument.NodeID, argument.Field, "SUM")
		countAlias := ensure(argument.NodeID, argument.Field, "COUNT")
		sumValue := aggregate("SUM", sumAlias)
		countValue := aggregate("SUM", countAlias)
		zero := dataset.Expression{Type: "LITERAL", Value: int64(0)}
		condition := dataset.Expression{Type: "EQUALS", Left: expressionPointer(countValue), Right: &zero}
		quotient := dataset.Expression{Type: "DIVIDE", Arguments: []dataset.Expression{sumValue, countValue}}
		return dataset.Expression{Type: "CASE", Whens: []dataset.CaseBranch{{When: condition, Then: dataset.Expression{Type: "LITERAL", Value: nil}}}, Else: &quotient}
	default:
		return dataset.Expression{Type: "AGGREGATE", Function: function, Argument: &argument}
	}
}

func expressionPointer(expression dataset.Expression) *dataset.Expression { return &expression }

func rewriteAggregationChildren(expression dataset.Expression, rewrite func(dataset.Expression) dataset.Expression) dataset.Expression {
	result := expression
	if expression.Argument != nil {
		value := rewrite(*expression.Argument)
		result.Argument = &value
	}
	if expression.Left != nil {
		value := rewrite(*expression.Left)
		result.Left = &value
	}
	if expression.Right != nil {
		value := rewrite(*expression.Right)
		result.Right = &value
	}
	if expression.Lower != nil {
		value := rewrite(*expression.Lower)
		result.Lower = &value
	}
	if expression.Upper != nil {
		value := rewrite(*expression.Upper)
		result.Upper = &value
	}
	if expression.Else != nil {
		value := rewrite(*expression.Else)
		result.Else = &value
	}
	result.Arguments = make([]dataset.Expression, len(expression.Arguments))
	for index, child := range expression.Arguments {
		result.Arguments[index] = rewrite(child)
	}
	result.Whens = make([]dataset.CaseBranch, len(expression.Whens))
	for index, branch := range expression.Whens {
		result.Whens[index] = dataset.CaseBranch{When: rewrite(branch.When), Then: rewrite(branch.Then)}
	}
	return result
}

func cloneAggregationDocument(document dataset.Document) dataset.Document {
	result := document
	result.Nodes = append([]dataset.Node(nil), document.Nodes...)
	for index := range result.Nodes {
		result.Nodes[index].Projection = append([]string(nil), document.Nodes[index].Projection...)
	}
	result.Fields = append([]dataset.Field(nil), document.Fields...)
	result.Having = append([]dataset.Filter(nil), document.Having...)
	return result
}

func isComposableAggregate(function string) bool {
	return function == "SUM" || function == "MIN" || function == "MAX" || function == "COUNT" || function == "AVG"
}

func nodeIsSafeManySide(nodeID string, joins []dataset.Join) bool {
	participates := false
	for _, join := range joins {
		switch nodeID {
		case join.LeftNodeID:
			participates = true
			if join.Cardinality != "MANY_TO_ONE" {
				return false
			}
		case join.RightNodeID:
			participates = true
			if join.Cardinality != "ONE_TO_MANY" {
				return false
			}
		}
	}
	return participates
}

func fieldUseKey(nodeID, field string) string { return nodeID + "\x00" + field }

func splitFieldUseKey(key string) (string, string, bool) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func markAggregateFieldsBlocked(expression dataset.Expression, uses map[string]*aggregateFieldUse) {
	if expression.Type == "FIELD_REF" {
		key := fieldUseKey(expression.NodeID, expression.Field)
		if uses[key] == nil {
			uses[key] = &aggregateFieldUse{functions: map[string]bool{}}
		}
		uses[key].blocked = true
	}
	visitAggregationChildren(expression, func(child dataset.Expression) { markAggregateFieldsBlocked(child, uses) })
}

func expressionHasAggregate(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" {
		return true
	}
	found := false
	visitAggregationChildren(expression, func(child dataset.Expression) { found = found || expressionHasAggregate(child) })
	return found
}

func expressionHasAggregateFunction(expression dataset.Expression, function string) bool {
	if expression.Type == "AGGREGATE" && strings.EqualFold(expression.Function, function) {
		return true
	}
	found := false
	visitAggregationChildren(expression, func(child dataset.Expression) {
		found = found || expressionHasAggregateFunction(child, function)
	})
	return found
}

func forEachDocumentExpression(document dataset.Document, visit func(dataset.Expression)) {
	for _, node := range document.Nodes {
		for _, filter := range node.SourceFilters {
			if filter.Expression != nil {
				visit(*filter.Expression)
			} else if filter.Field != "" {
				visit(dataset.Expression{Type: "FIELD_REF", NodeID: node.ID, Field: filter.Field})
			}
		}
	}
	for _, join := range document.Joins {
		for _, condition := range join.Conditions {
			visit(condition.LeftExpression)
			visit(condition.RightExpression)
		}
	}
	for _, field := range document.Fields {
		visit(field.Expression)
	}
	for _, filter := range document.Filters {
		visit(filter.Expression)
	}
	for _, filter := range document.Having {
		visit(filter.Expression)
	}
}

func visitAggregationChildren(expression dataset.Expression, visit func(dataset.Expression)) {
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			visit(*child)
		}
	}
	for _, child := range expression.Arguments {
		visit(child)
	}
	for _, branch := range expression.Whens {
		visit(branch.When)
		visit(branch.Then)
	}
}
