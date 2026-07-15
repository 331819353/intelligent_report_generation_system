package federation

import "intelligent-report-generation-system/internal/dataset"

func pruneNodeProjections(document dataset.Document) dataset.Document {
	// Document 按值传递但切片仍共享底层数组，先复制节点及投影，避免预览优化
	// 意外改写调用方持有的规范 DSL。
	document.Nodes = append([]dataset.Node(nil), document.Nodes...)
	for index := range document.Nodes {
		document.Nodes[index].Projection = append([]string(nil), document.Nodes[index].Projection...)
	}
	required := map[string]map[string]bool{}
	for _, node := range document.Nodes {
		required[node.ID] = map[string]bool{}
		for _, filter := range node.SourceFilters {
			if filter.Expression != nil {
				collectExpressionFields(*filter.Expression, required)
			} else {
				required[node.ID][filter.Field] = true
			}
		}
	}
	for _, join := range document.Joins {
		for _, condition := range join.Conditions {
			collectExpressionFields(condition.LeftExpression, required)
			collectExpressionFields(condition.RightExpression, required)
		}
	}
	for _, field := range document.Fields {
		collectExpressionFields(field.Expression, required)
	}
	for _, filter := range document.Filters {
		collectExpressionFields(filter.Expression, required)
	}
	for _, filter := range document.Having {
		collectExpressionFields(filter.Expression, required)
	}
	for index := range document.Nodes {
		node := &document.Nodes[index]
		projection := make([]string, 0, len(node.Projection))
		for _, name := range node.Projection {
			if required[node.ID][name] {
				projection = append(projection, name)
			}
		}
		node.Projection = projection
	}
	return document
}

func collectExpressionFields(expression dataset.Expression, required map[string]map[string]bool) {
	if expression.Type == "FIELD_REF" && required[expression.NodeID] != nil {
		required[expression.NodeID][expression.Field] = true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			collectExpressionFields(*child, required)
		}
	}
	for _, child := range expression.Arguments {
		collectExpressionFields(child, required)
	}
	for _, branch := range expression.Whens {
		collectExpressionFields(branch.When, required)
		collectExpressionFields(branch.Then, required)
	}
}
