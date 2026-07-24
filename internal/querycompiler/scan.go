package querycompiler

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

// ScanInput 描述跨源执行前单个数据库节点的安全扫描计划。
type ScanInput struct {
	Document             dataset.Document
	NodeID               string
	Dialect              Dialect
	Table                TableRef
	Parameters           map[string]any
	MaxRows              int
	AggregateProjections map[string]ScanAggregateProjection
}

// ScanAggregateProjection 描述由可信联邦计划器生成的单列部分聚合投影。
type ScanAggregateProjection struct {
	SourceField string
	Function    string
}

// CompileScan 下推当前节点投影、源过滤、可证明为单节点的前置过滤及受控预聚合。
func CompileScan(input ScanInput) (CompiledQuery, error) {
	return compileScan(input, 100_000, true)
}

// CompileExtractionScan 为跨源构建生成不带 SQL LIMIT 的安全源端扫描。
// 总行数由流式 Connector 在读取过程中强制执行；这样超过上限时会显式失败，
// 不会把恰好达到 LIMIT 的截断结果误认为完整物化。
func CompileExtractionScan(input ScanInput) (CompiledQuery, error) {
	return compileScan(input, 5_000_000, false)
}

func compileScan(input ScanInput, maximumRows int, sqlLimit bool) (CompiledQuery, error) {
	if input.Dialect != MySQL && input.Dialect != Oracle && input.Dialect != PostgreSQL {
		return CompiledQuery{}, errors.New("unsupported query dialect")
	}
	if err := dataset.Validate(input.Document); err != nil {
		return CompiledQuery{}, fmt.Errorf("invalid dataset document: %w", err)
	}
	if input.Dialect == PostgreSQL {
		if err := validatePostgreSQLDocumentIdentifiers(input.Document); err != nil {
			return CompiledQuery{}, err
		}
	}
	if input.MaxRows < 1 || input.MaxRows > maximumRows {
		return CompiledQuery{}, errors.New("source scan row limit is invalid")
	}
	var node *dataset.Node
	for index := range input.Document.Nodes {
		if input.Document.Nodes[index].ID == input.NodeID {
			node = &input.Document.Nodes[index]
			break
		}
	}
	if node == nil || node.Type != "TABLE" || input.Table.NodeID != node.ID {
		return CompiledQuery{}, errors.New("source scan node is invalid")
	}
	parameters, err := NormalizeParameters(input.Document.Parameters, input.Parameters)
	if err != nil {
		return CompiledQuery{}, err
	}
	c := &compiler{input: Input{
		Document: input.Document, Dialect: input.Dialect, Tables: map[string]TableRef{node.ID: input.Table}, Parameters: parameters,
	}}
	if err := c.validateTable(input.Table); err != nil {
		return CompiledQuery{}, err
	}
	alias := node.Alias
	aliases := map[string]string{node.ID: alias}
	projections := make([]string, 0, len(node.Projection))
	groups := make([]string, 0, len(node.Projection))
	for _, name := range node.Projection {
		aggregate, aggregated := input.AggregateProjections[name]
		if aggregated {
			if !input.Table.Columns[aggregate.SourceField] {
				return CompiledQuery{}, errors.New("source scan aggregate field is not in the whitelist")
			}
			function := strings.ToUpper(aggregate.Function)
			if function != "SUM" && function != "MIN" && function != "MAX" && function != "COUNT" {
				return CompiledQuery{}, errors.New("source scan aggregate is unsupported")
			}
			column := c.quote(alias) + "." + c.quote(aggregate.SourceField)
			projections = append(projections, function+"("+column+") AS "+c.quote(name))
			continue
		}
		if !input.Table.Columns[name] {
			return CompiledQuery{}, fmt.Errorf("column %s is not in the source whitelist", name)
		}
		column := c.quote(alias) + "." + c.quote(name)
		projections = append(projections, column+" AS "+c.quote(name))
		if len(input.AggregateProjections) > 0 {
			groups = append(groups, column)
		}
	}
	for name := range input.AggregateProjections {
		found := false
		for _, projected := range node.Projection {
			found = found || projected == name
		}
		if !found {
			return CompiledQuery{}, errors.New("source scan aggregate field is not projected")
		}
	}
	where := []string{}
	for _, filter := range node.SourceFilters {
		value, err := c.scanSourceFilter(*node, filter, aliases)
		if err != nil {
			return CompiledQuery{}, err
		}
		where = append(where, value)
	}
	for _, filter := range input.Document.Filters {
		if filter.Optional && hasNilParameter(filter.Expression, parameters) {
			continue
		}
		if !expressionOnlyReferencesNode(filter.Expression, node.ID) {
			continue
		}
		value, err := c.expression(filter.Expression, aliases)
		if err != nil {
			return CompiledQuery{}, err
		}
		where = append(where, value)
	}
	sql := "SELECT " + strings.Join(projections, ", ") + " FROM " + c.tableName(input.Table) + " " + c.quote(alias)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	if len(groups) > 0 {
		sql += " GROUP BY " + strings.Join(groups, ", ")
	}
	if sqlLimit {
		// 预览多取一行，让联邦执行器发现源端截断，不能把不完整 Join 静默
		// 当成完整结果；构建流则由 Connector 逐批检测上限。
		if input.Dialect == Oracle {
			sql += " FETCH FIRST " + c.bind(input.MaxRows) + " ROWS ONLY"
		} else {
			sql += " LIMIT " + c.bind(input.MaxRows)
		}
	}
	if strings.ContainsAny(sql, ";\x00") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return CompiledQuery{}, errors.New("compiled source scan contains a forbidden token")
	}
	return CompiledQuery{SQL: sql, Args: c.args, MaxRows: input.MaxRows}, nil
}

func (c *compiler) scanSourceFilter(node dataset.Node, filter dataset.SourceFilter, aliases map[string]string) (string, error) {
	if filter.Expression != nil {
		return c.expression(*filter.Expression, aliases)
	}
	ref := c.input.Tables[node.ID]
	if !ref.Columns[filter.Field] {
		return "", errors.New("source filter field is not in the whitelist")
	}
	left := c.quote(node.Alias) + "." + c.quote(filter.Field)
	if filter.Operator == "IS_NULL" {
		return left + " IS NULL", nil
	}
	if filter.Operator == "IS_NOT_NULL" {
		return left + " IS NOT NULL", nil
	}
	op, err := comparison(filter.Operator)
	if err != nil {
		return "", err
	}
	if filter.Operator == "IN" || filter.Operator == "NOT_IN" {
		values, err := c.bindCollection(filter.Value)
		if err != nil {
			return "", err
		}
		return "(" + left + " " + op + " (" + values + "))", nil
	}
	return "(" + left + " " + op + " " + c.bind(filter.Value) + ")", nil
}

func expressionOnlyReferencesNode(expression dataset.Expression, nodeID string) bool {
	found := false
	valid := true
	visitExpression(expression, func(item dataset.Expression) {
		if item.Type == "AGGREGATE" {
			valid = false
		}
		if item.Type == "FIELD_REF" {
			found = true
			valid = valid && item.NodeID == nodeID
		}
	})
	return found && valid
}

func visitExpression(expression dataset.Expression, visit func(dataset.Expression)) {
	visit(expression)
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			visitExpression(*child, visit)
		}
	}
	for _, child := range expression.Arguments {
		visitExpression(child, visit)
	}
	for _, branch := range expression.Whens {
		visitExpression(branch.When, visit)
		visitExpression(branch.Then, visit)
	}
}
