package querycompiler

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

const joinProbeMultiplicity = "probe_multiplicity"

// JoinProbeInput 描述同一数据库内 Join 键基数探测所需的可信输入。
type JoinProbeInput struct {
	Document   dataset.Document
	Dialect    Dialect
	Tables     map[string]TableRef
	Parameters map[string]any
}

// JoinProbeQuery 是单条 Join 的聚合探测查询，不返回任何业务键值。
type JoinProbeQuery struct {
	JoinID string
	Query  CompiledQuery
}

// CompileJoinProbes 为每条等值 Join 生成一行聚合统计查询。
func CompileJoinProbes(input JoinProbeInput) ([]JoinProbeQuery, error) {
	if input.Dialect != MySQL && input.Dialect != Oracle {
		return nil, errors.New("unsupported query dialect")
	}
	if err := dataset.Validate(input.Document); err != nil {
		return nil, errors.New("invalid dataset document")
	}
	if input.Document.Dataset.Type != "SINGLE_SOURCE" {
		return nil, errors.New("join probe requires a single-source dataset")
	}
	parameters, err := NormalizeParameters(input.Document.Parameters, input.Parameters)
	if err != nil {
		return nil, err
	}
	result := make([]JoinProbeQuery, 0, len(input.Document.Joins))
	for _, join := range input.Document.Joins {
		compiler := &compiler{input: Input{
			Document: input.Document, Dialect: input.Dialect, Tables: input.Tables, Parameters: parameters,
		}}
		query, err := compiler.compileJoinProbe(join)
		if err != nil {
			return nil, err
		}
		result = append(result, JoinProbeQuery{JoinID: join.ID, Query: query})
	}
	return result, nil
}

func (c *compiler) compileJoinProbe(join dataset.Join) (CompiledQuery, error) {
	if len(join.Conditions) == 0 {
		return CompiledQuery{}, errors.New("join probe requires at least one condition")
	}
	for _, condition := range join.Conditions {
		if condition.Operator != "EQUALS" {
			return CompiledQuery{}, errors.New("join probe only supports equality conditions")
		}
	}
	leftRows, keyAliases, err := c.compileJoinProbeSide(join.LeftNodeID, join.Conditions, true)
	if err != nil {
		return CompiledQuery{}, err
	}
	rightRows, rightKeyAliases, err := c.compileJoinProbeSide(join.RightNodeID, join.Conditions, false)
	if err != nil {
		return CompiledQuery{}, err
	}
	if len(keyAliases) != len(rightKeyAliases) {
		return CompiledQuery{}, errors.New("join probe key count is inconsistent")
	}

	quotedKeys := make([]string, len(keyAliases))
	nonNull := make([]string, len(keyAliases))
	joinPredicates := make([]string, len(keyAliases))
	for index, key := range keyAliases {
		quotedKeys[index] = c.quote(key)
		nonNull[index] = c.quote(key) + " IS NOT NULL"
		joinPredicates[index] = "l." + c.quote(key) + " = r." + c.quote(key)
	}
	keyList := strings.Join(quotedKeys, ", ")
	multiplicity := c.quote(joinProbeMultiplicity)
	leftRowsName, rightRowsName := c.quote("probe_left_rows"), c.quote("probe_right_rows")
	leftKeysName, rightKeysName := c.quote("probe_left_keys"), c.quote("probe_right_keys")
	leftKeyAggregation := "SELECT " + keyList + ", COUNT(*) AS " + multiplicity + " FROM " + leftRowsName +
		" WHERE " + strings.Join(nonNull, " AND ") + " GROUP BY " + keyList
	rightKeyAggregation := "SELECT " + keyList + ", COUNT(*) AS " + multiplicity + " FROM " + rightRowsName +
		" WHERE " + strings.Join(nonNull, " AND ") + " GROUP BY " + keyList

	leftDuplicate := "(SELECT COUNT(*) FROM " + leftKeysName + " WHERE " + multiplicity + " > 1)"
	rightDuplicate := "(SELECT COUNT(*) FROM " + rightKeysName + " WHERE " + multiplicity + " > 1)"
	leftMaximum := "COALESCE((SELECT MAX(" + multiplicity + ") FROM " + leftKeysName + "),0)"
	rightMaximum := "COALESCE((SELECT MAX(" + multiplicity + ") FROM " + rightKeysName + "),0)"
	fanout := "(SELECT COUNT(*) FROM " + leftKeysName + " l JOIN " + rightKeysName + " r ON " +
		strings.Join(joinPredicates, " AND ") + " WHERE l." + multiplicity + " > 1 AND r." + multiplicity + " > 1)"
	sql := "WITH " + leftRowsName + " AS (" + leftRows + "), " + rightRowsName + " AS (" + rightRows +
		"), " + leftKeysName + " AS (" + leftKeyAggregation + "), " + rightKeysName + " AS (" + rightKeyAggregation + ") SELECT " +
		leftDuplicate + " AS " + c.quote("left_duplicate_keys") + ", " +
		rightDuplicate + " AS " + c.quote("right_duplicate_keys") + ", " +
		leftMaximum + " AS " + c.quote("left_max_multiplicity") + ", " +
		rightMaximum + " AS " + c.quote("right_max_multiplicity") + ", " +
		fanout + " AS " + c.quote("fanout_keys")
	if c.input.Dialect == Oracle {
		sql += " FROM DUAL"
	}
	if strings.ContainsAny(sql, ";\x00") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return CompiledQuery{}, errors.New("compiled join probe contains a forbidden token")
	}
	return CompiledQuery{SQL: sql, Args: c.args, MaxRows: 1}, nil
}

func (c *compiler) compileJoinProbeSide(nodeID string, conditions []dataset.JoinCondition, left bool) (string, []string, error) {
	var node *dataset.Node
	for index := range c.input.Document.Nodes {
		if c.input.Document.Nodes[index].ID == nodeID {
			node = &c.input.Document.Nodes[index]
			break
		}
	}
	ref, exists := c.input.Tables[nodeID]
	if node == nil || node.Type != "TABLE" || !exists || ref.NodeID != nodeID {
		return "", nil, errors.New("join probe node is not in the physical whitelist")
	}
	if err := c.validateTable(ref); err != nil {
		return "", nil, err
	}
	aliases := map[string]string{node.ID: node.Alias}
	keys := make([]string, 0, len(conditions))
	projections := make([]string, 0, len(conditions))
	for index, condition := range conditions {
		expression := condition.RightExpression
		if left {
			expression = condition.LeftExpression
		}
		if expression.Type != "FIELD_REF" || expression.NodeID != nodeID || !ref.Columns[expression.Field] {
			return "", nil, errors.New("join probe expression is not a whitelisted field")
		}
		value, err := c.expression(expression, aliases)
		if err != nil {
			return "", nil, err
		}
		key := fmt.Sprintf("probe_key_%d", index)
		keys = append(keys, key)
		projections = append(projections, value+" AS "+c.quote(key))
	}
	where, err := c.compileJoinProbeFilters(*node, aliases)
	if err != nil {
		return "", nil, err
	}
	sql := "SELECT " + strings.Join(projections, ", ") + " FROM " + c.tableName(ref) + " " + c.quote(node.Alias)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	return sql, keys, nil
}

func (c *compiler) compileJoinProbeFilters(node dataset.Node, aliases map[string]string) ([]string, error) {
	where := make([]string, 0, len(node.SourceFilters)+len(c.input.Document.Filters))
	for _, filter := range node.SourceFilters {
		value, err := c.scanSourceFilter(node, filter, aliases)
		if err != nil {
			return nil, err
		}
		where = append(where, value)
	}
	for _, filter := range c.input.Document.Filters {
		if filter.Optional && hasNilParameter(filter.Expression, c.input.Parameters) {
			continue
		}
		if !expressionOnlyReferencesNode(filter.Expression, node.ID) {
			continue
		}
		value, err := c.expression(filter.Expression, aliases)
		if err != nil {
			return nil, err
		}
		where = append(where, value)
	}
	return where, nil
}
