package datasettagsuggestion

import (
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

func datasetFields(document dataset.Document) ([]FieldContext, error) {
	if len(document.Fields) > MaxDatasetFields {
		return nil, ErrInputLimit
	}
	fields := make([]FieldContext, 0, len(document.Fields))
	for _, field := range document.Fields {
		fields = append(fields, FieldContext{
			ID: field.ID, Code: field.Code, Name: field.Name,
			Description: field.Description, Role: field.Role,
			CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
			Aggregation: field.Aggregation, Nullable: field.Nullable,
			Expression: expressionShape(field.Expression, 0),
		})
	}
	return fields, nil
}

func dagContext(document dataset.Document) DAGContext {
	nodes := make([]NodeContext, 0, len(document.Nodes))
	for _, node := range document.Nodes {
		nodes = append(nodes, NodeContext{
			ID: node.ID, Type: node.Type, Alias: node.Alias,
			Projection: append([]string(nil), node.Projection...),
		})
	}
	joins := make([]JoinContext, 0, len(document.Joins))
	for _, join := range document.Joins {
		refs := make([]string, 0, len(join.Conditions))
		for _, condition := range join.Conditions {
			refs = append(refs, fmt.Sprintf(
				"%s.%s %s %s.%s",
				condition.LeftExpression.NodeID, condition.LeftExpression.Field,
				condition.Operator,
				condition.RightExpression.NodeID, condition.RightExpression.Field,
			))
		}
		joins = append(joins, JoinContext{
			ID: join.ID, LeftNodeID: join.LeftNodeID, RightNodeID: join.RightNodeID,
			JoinType: join.JoinType, Cardinality: join.Cardinality,
			ConditionRefs: refs,
		})
	}
	hasTransforms := len(document.Filters) > 0 || len(document.Having) > 0 ||
		len(document.PreAggregations) > 0
	for _, field := range document.Fields {
		if field.Expression.Type != "FIELD_REF" {
			hasTransforms = true
			break
		}
	}
	return DAGContext{
		Nodes: nodes, Joins: joins,
		GroupBy:       append([]string(nil), document.GroupBy...),
		OutputGrain:   document.OutputGrain.Description,
		OutputKeys:    append([]string(nil), document.OutputGrain.KeyFields...),
		HasTransforms: hasTransforms,
	}
}

// expressionShape keeps operators, functions and field references while
// deliberately omitting literal values and parameter defaults.
func expressionShape(expression dataset.Expression, depth int) string {
	if depth > 32 {
		return "NESTED_EXPRESSION"
	}
	switch expression.Type {
	case "FIELD_REF":
		return strings.Trim(expression.NodeID+"."+expression.Field, ".")
	case "PARAM_REF":
		return "PARAM(" + expression.Code + ")"
	case "LITERAL":
		return "LITERAL"
	}
	parts := []string{expression.Type}
	for _, value := range []string{
		expression.Function, expression.Unit, expression.TargetType,
	} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	children := []dataset.Expression{}
	if expression.Argument != nil {
		children = append(children, *expression.Argument)
	}
	children = append(children, expression.Arguments...)
	for _, pointer := range []*dataset.Expression{
		expression.Left, expression.Right, expression.Lower, expression.Upper,
	} {
		if pointer != nil {
			children = append(children, *pointer)
		}
	}
	for _, branch := range expression.Whens {
		children = append(children, branch.When, branch.Then)
	}
	if expression.Else != nil {
		children = append(children, *expression.Else)
	}
	for _, child := range children {
		parts = append(parts, expressionShape(child, depth+1))
	}
	return strings.Join(parts, "(") + strings.Repeat(")", max(len(parts)-1, 0))
}

func projectedColumns(document dataset.Document, tableID string) []string {
	seen := map[string]bool{}
	for _, node := range document.Nodes {
		if node.Type != "TABLE" || node.TableID != tableID {
			continue
		}
		for _, column := range node.Projection {
			seen[column] = true
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
