package filequery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

// MaxIntermediateRows 是内存 Join/分组允许保留的最大中间行数。
const MaxIntermediateRows = 200000

var safeMaskCharacter = regexp.MustCompile(`^[\p{L}\p{N}*•]$`)

var (
	ErrQueryAlreadyActive    = errors.New("file query ID is already active")
	ErrFileVersionMismatch   = errors.New("file version does not belong to the data source")
	ErrUnsupportedExpression = errors.New("file query expression is unsupported")
)

// Input 是文件 DSL 求值所需的固定版本数据、参数和安全策略。
type Input struct {
	Document       dataset.Document
	Tables         map[string]querycompiler.TableRef
	FileTables     []datasource.FileTableData
	NodeTables     map[string]NodeTableData
	Parameters     map[string]any
	Scope          policy.UserScope
	RowPolicies    []policy.RowPolicy
	ColumnPolicies []policy.ColumnPolicy
	MaxRows        int
}

// NodeTableData 是已由数据库或固定文件版本规范化后的节点级二维数据。
type NodeTableData struct {
	Columns []string
	Rows    [][]any
}

type sourceRow map[string]any
type outputRow struct {
	values map[string]any
}

// Evaluate 在内存上执行 DSL；所有循环都会响应超时和人工取消。
func Evaluate(ctx context.Context, input Input) (datasource.QueryResult, error) {
	if err := dataset.Validate(input.Document); err != nil {
		return datasource.QueryResult{}, err
	}
	if input.Document.Dataset.Type != "SINGLE_SOURCE" && input.Document.Dataset.Type != "CROSS_SOURCE" || input.MaxRows < 1 || input.MaxRows > input.Document.ExecutionPolicy.PreviewLimit {
		return datasource.QueryResult{}, errors.New("invalid file preview limits or dataset type")
	}
	// 执行顺序刻意与 SQL 编译器一致：扫描与源过滤 -> Join -> WHERE -> 分组 ->
	// HAVING -> 输出表达式 -> 行列权限 -> 排序。改变顺序会导致文件预览与数据库
	// 预览产生不同结果，尤其可能让权限策略在错误的粒度上执行。
	rowsByNode, err := loadNodes(ctx, input)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	rows, err := joinNodes(ctx, input.Document, rowsByNode)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	filtered := make([]sourceRow, 0, len(rows))
	for index, row := range rows {
		if err := checkContext(ctx, index); err != nil {
			return datasource.QueryResult{}, err
		}
		keep := true
		for _, filter := range input.Document.Filters {
			if filter.Optional && hasNilParameter(filter.Expression, input.Parameters) {
				continue
			}
			value, err := evaluateExpression(filter.Expression, row, nil, input.Parameters)
			if err != nil {
				return datasource.QueryResult{}, err
			}
			matched, ok := value.(bool)
			if !ok || !matched {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, row)
		}
	}
	groups, err := groupRows(ctx, input.Document, filtered, input.Parameters)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	// 在生成任何聚合输出前先校验列策略，并用最严格的最小分组人数过滤组。
	minimumGroupSize, err := validateColumnPolicies(input.Document, input.ColumnPolicies)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	output := make([]outputRow, 0, len(groups))
	for index, group := range groups {
		if err := checkContext(ctx, index); err != nil {
			return datasource.QueryResult{}, err
		}
		if len(group) < minimumGroupSize {
			continue
		}
		first := sourceRow{}
		if len(group) > 0 {
			first = group[0]
		}
		keep := true
		for _, filter := range input.Document.Having {
			if filter.Optional && hasNilParameter(filter.Expression, input.Parameters) {
				continue
			}
			value, err := evaluateExpression(filter.Expression, first, group, input.Parameters)
			if err != nil {
				return datasource.QueryResult{}, err
			}
			matched, ok := value.(bool)
			if !ok || !matched {
				keep = false
				break
			}
		}
		if !keep {
			continue
		}
		values := make(map[string]any, len(input.Document.Fields))
		for _, field := range input.Document.Fields {
			value, err := evaluateExpression(field.Expression, first, group, input.Parameters)
			if err != nil {
				return datasource.QueryResult{}, fmt.Errorf("field %s: %w", field.Code, err)
			}
			values[field.Code] = value
		}
		// 行策略引用的是数据集输出字段，因此必须在字段表达式求值后执行；列脱敏
		// 随后执行，确保策略仍基于原始值判断而响应中只出现处理后的值。
		allowed, err := evaluateRowPolicies(input.RowPolicies, input.Scope, values)
		if err != nil {
			return datasource.QueryResult{}, err
		}
		if !allowed {
			continue
		}
		if err := applyColumnPolicies(values, input.ColumnPolicies); err != nil {
			return datasource.QueryResult{}, err
		}
		output = append(output, outputRow{values: values})
	}
	if err := sortOutput(input.Document, output); err != nil {
		return datasource.QueryResult{}, err
	}
	if len(output) > input.MaxRows {
		return datasource.QueryResult{}, errors.New("file query row limit exceeded")
	}
	columns := make([]string, len(input.Document.Fields))
	resultRows := make([][]any, len(output))
	for index, field := range input.Document.Fields {
		columns[index] = field.Code
	}
	for rowIndex, row := range output {
		resultRows[rowIndex] = make([]any, len(columns))
		for columnIndex, code := range columns {
			resultRows[rowIndex][columnIndex] = row.values[code]
		}
	}
	return datasource.QueryResult{Columns: columns, Rows: resultRows, RowCount: len(resultRows)}, nil
}

func loadNodes(ctx context.Context, input Input) (map[string][]sourceRow, error) {
	// FileTables 已由固定 fileVersionId 读取，但仍要通过 Resolve 返回的表/列白名单
	// 二次约束。DSL 只能读取 projection 中声明且当前版本真实存在的列。
	fileTables := make(map[string]datasource.FileTableData, len(input.FileTables))
	for _, table := range input.FileTables {
		fileTables[table.Name] = table
	}
	result := make(map[string][]sourceRow, len(input.Document.Nodes))
	for _, node := range input.Document.Nodes {
		ref, ok := input.Tables[node.ID]
		if !ok {
			return nil, errors.New("file node is not in the physical whitelist")
		}
		if table, exists := input.NodeTables[node.ID]; exists {
			rows, err := loadPreparedNode(ctx, node, table, input.Parameters)
			if err != nil {
				return nil, err
			}
			result[node.ID] = rows
			continue
		}
		table, ok := fileTables[ref.Name]
		if !ok {
			return nil, fmt.Errorf("worksheet %s is not present in the fixed file version", ref.Name)
		}
		columnIndexes := make(map[string]int, len(table.Columns))
		for index, name := range table.Columns {
			columnIndexes[name] = index
		}
		for _, name := range node.Projection {
			if _, ok := columnIndexes[name]; !ok {
				return nil, fmt.Errorf("worksheet %s does not contain projected column %s", ref.Name, name)
			}
		}
		rows := make([]sourceRow, 0, len(table.Rows))
		for rowIndex, raw := range table.Rows {
			if err := checkContext(ctx, rowIndex); err != nil {
				return nil, err
			}
			row := sourceRow{}
			// 单元格先按同步阶段推断的规范类型转换，后续比较、聚合和排序都使用
			// 类型化值，避免字符串字典序与数据库数值语义不一致。
			for _, name := range node.Projection {
				index := columnIndexes[name]
				value := ""
				if index < len(raw) {
					value = raw[index]
				}
				parsed, err := parseCell(value, table.Types[name])
				if err != nil {
					return nil, fmt.Errorf("worksheet %s row %d column %s: %w", ref.Name, rowIndex+1, name, err)
				}
				row[node.ID+"."+name] = parsed
			}
			keep, err := evaluateSourceFilters(node, row, input.Parameters)
			if err != nil {
				return nil, err
			}
			if keep {
				rows = append(rows, row)
			}
		}
		result[node.ID] = rows
	}
	return result, nil
}

func loadPreparedNode(ctx context.Context, node dataset.Node, table NodeTableData, parameters map[string]any) ([]sourceRow, error) {
	columnIndexes := make(map[string]int, len(table.Columns))
	for index, name := range table.Columns {
		if _, exists := columnIndexes[name]; exists {
			return nil, fmt.Errorf("node %s returned duplicate column %s", node.ID, name)
		}
		columnIndexes[name] = index
	}
	for _, name := range node.Projection {
		if _, exists := columnIndexes[name]; !exists {
			return nil, fmt.Errorf("node %s does not contain projected column %s", node.ID, name)
		}
	}
	rows := make([]sourceRow, 0, len(table.Rows))
	for rowIndex, raw := range table.Rows {
		if err := checkContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		row := sourceRow{}
		for _, name := range node.Projection {
			index := columnIndexes[name]
			if index >= len(raw) {
				return nil, fmt.Errorf("node %s row %d has an invalid shape", node.ID, rowIndex+1)
			}
			row[node.ID+"."+name] = raw[index]
		}
		keep, err := evaluateSourceFilters(node, row, parameters)
		if err != nil {
			return nil, err
		}
		if keep {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// NodeTableFromFile 将固定文件工作表按规范类型转换为联邦执行输入。
func NodeTableFromFile(table datasource.FileTableData, projection []string) (NodeTableData, error) {
	columnIndexes := make(map[string]int, len(table.Columns))
	for index, name := range table.Columns {
		columnIndexes[name] = index
	}
	result := NodeTableData{Columns: append([]string(nil), projection...), Rows: make([][]any, len(table.Rows))}
	for _, name := range projection {
		if _, exists := columnIndexes[name]; !exists {
			return NodeTableData{}, fmt.Errorf("worksheet %s does not contain projected column %s", table.Name, name)
		}
	}
	for rowIndex, raw := range table.Rows {
		result.Rows[rowIndex] = make([]any, len(projection))
		for columnIndex, name := range projection {
			index := columnIndexes[name]
			value := ""
			if index < len(raw) {
				value = raw[index]
			}
			parsed, err := parseCell(value, table.Types[name])
			if err != nil {
				return NodeTableData{}, fmt.Errorf("worksheet %s row %d column %s: %w", table.Name, rowIndex+1, name, err)
			}
			result.Rows[rowIndex][columnIndex] = parsed
		}
	}
	return result, nil
}

func evaluateSourceFilters(node dataset.Node, row sourceRow, parameters map[string]any) (bool, error) {
	// 兼容旧的 field/operator/value 过滤表示，并统一改写成表达式树求值，避免维护
	// 两套比较语义。sourceFilter 已在领域层限制为当前节点字段。
	for _, filter := range node.SourceFilters {
		var expression dataset.Expression
		if filter.Expression != nil {
			expression = *filter.Expression
		} else if filter.Operator == "IS_NULL" || filter.Operator == "IS_NOT_NULL" {
			expression = dataset.Expression{Type: filter.Operator, Argument: &dataset.Expression{Type: "FIELD_REF", NodeID: node.ID, Field: filter.Field}}
		} else {
			expression = dataset.Expression{Type: filter.Operator, Left: &dataset.Expression{Type: "FIELD_REF", NodeID: node.ID, Field: filter.Field}, Right: &dataset.Expression{Type: "LITERAL", Value: filter.Value}}
		}
		value, err := evaluateExpression(expression, row, nil, parameters)
		if err != nil {
			return false, err
		}
		matched, ok := value.(bool)
		if !ok || !matched {
			return false, nil
		}
	}
	return true, nil
}

func joinNodes(ctx context.Context, document dataset.Document, rowsByNode map[string][]sourceRow) ([]sourceRow, error) {
	if len(document.Nodes) == 1 {
		return rowsByNode[document.Nodes[0].ID], nil
	}
	// 与 SQL 编译器相同，Join 以 left -> right 有向边扩展。领域层只保证忽略方向
	// 后连通；方向环会在无法继续扩展时失败，不能交换两侧来让图“可跑”。
	incoming := map[string]bool{}
	for _, join := range document.Joins {
		incoming[join.RightNodeID] = true
	}
	rootID := document.Nodes[0].ID
	for _, node := range document.Nodes {
		if !incoming[node.ID] {
			rootID = node.ID
			break
		}
	}
	combined := cloneRows(rowsByNode[rootID])
	joined := map[string]bool{rootID: true}
	remaining := append([]dataset.Join(nil), document.Joins...)
	for len(remaining) > 0 {
		progressed := false
		next := make([]dataset.Join, 0, len(remaining))
		for _, join := range remaining {
			if !joined[join.LeftNodeID] || joined[join.RightNodeID] {
				next = append(next, join)
				continue
			}
			var err error
			combined, err = hashJoin(ctx, combined, rowsByNode[join.RightNodeID], join)
			if err != nil {
				return nil, err
			}
			if len(combined) > MaxIntermediateRows {
				return nil, errors.New("file query intermediate row limit exceeded")
			}
			joined[join.RightNodeID], progressed = true, true
		}
		if !progressed {
			return nil, errors.New("file join order cannot preserve the declared direction")
		}
		remaining = next
	}
	return combined, nil
}

func hashJoin(ctx context.Context, leftRows, rightRows []sourceRow, join dataset.Join) ([]sourceRow, error) {
	// 文件预览目前只实现等值哈希 Join。INNER Join 可安全交换哈希构建侧，选择
	// 较小输入降低索引内存；外连接仍固定右侧建表以保持声明方向和补 NULL 语义。
	for _, condition := range join.Conditions {
		if condition.Operator != "EQUALS" {
			return nil, errors.New("file preview currently supports equality joins only")
		}
	}
	if join.JoinType == "INNER" && len(leftRows) < len(rightRows) {
		return hashInnerJoinBuildLeft(ctx, leftRows, rightRows, join)
	}
	index := map[string][]int{}
	for rowIndex, row := range rightRows {
		key, ok, err := joinKey(join.Conditions, row, false)
		if err != nil {
			return nil, err
		}
		if ok {
			index[key] = append(index[key], rowIndex)
		}
	}
	result := make([]sourceRow, 0)
	matchedRight := make([]bool, len(rightRows))
	for rowIndex, left := range leftRows {
		if err := checkContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := joinKey(join.Conditions, left, true)
		if err != nil {
			return nil, err
		}
		matches := index[key]
		if !ok {
			matches = nil
		}
		if len(matches) == 0 && (join.JoinType == "LEFT" || join.JoinType == "FULL") {
			result = append(result, cloneRow(left))
		}
		for _, rightIndex := range matches {
			matchedRight[rightIndex] = true
			result = append(result, mergeRows(left, rightRows[rightIndex]))
			if len(result) > MaxIntermediateRows {
				return nil, errors.New("file query intermediate row limit exceeded")
			}
		}
	}
	if join.JoinType == "RIGHT" || join.JoinType == "FULL" {
		for index, row := range rightRows {
			if !matchedRight[index] {
				result = append(result, cloneRow(row))
			}
		}
	}
	return result, nil
}

func hashInnerJoinBuildLeft(ctx context.Context, leftRows, rightRows []sourceRow, join dataset.Join) ([]sourceRow, error) {
	index := map[string][]int{}
	for rowIndex, row := range leftRows {
		if err := checkContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := joinKey(join.Conditions, row, true)
		if err != nil {
			return nil, err
		}
		if ok {
			index[key] = append(index[key], rowIndex)
		}
	}
	result := make([]sourceRow, 0)
	for rowIndex, right := range rightRows {
		if err := checkContext(ctx, rowIndex); err != nil {
			return nil, err
		}
		key, ok, err := joinKey(join.Conditions, right, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, leftIndex := range index[key] {
			result = append(result, mergeRows(leftRows[leftIndex], right))
			if len(result) > MaxIntermediateRows {
				return nil, errors.New("file query intermediate row limit exceeded")
			}
		}
	}
	return result, nil
}

func joinKey(conditions []dataset.JoinCondition, row sourceRow, left bool) (string, bool, error) {
	// 复合键序列化为 JSON，保留值类型和条件顺序，避免简单字符串拼接产生歧义。
	// 任一分量为 NULL 时不生成键，以对齐 SQL 中 NULL 不与 NULL 相等的规则。
	values := make([]any, 0, len(conditions))
	for _, condition := range conditions {
		expression := condition.RightExpression
		if left {
			expression = condition.LeftExpression
		}
		value, err := evaluateExpression(expression, row, nil, nil)
		if err != nil {
			return "", false, err
		}
		if value == nil {
			return "", false, nil
		}
		values = append(values, value)
	}
	payload, err := json.Marshal(values)
	return string(payload), true, err
}

func groupRows(ctx context.Context, document dataset.Document, rows []sourceRow, parameters map[string]any) ([][]sourceRow, error) {
	// 无显式 groupBy 时有两种语义：存在聚合表达式则全部输入形成一个组；纯明细
	// 查询则每行独立成组。显式分组使用首次出现顺序，保证未排序结果仍可复现。
	hasAggregate := false
	fieldsByID := map[string]dataset.Field{}
	for _, field := range document.Fields {
		fieldsByID[field.ID] = field
		hasAggregate = hasAggregate || expressionContains(field.Expression, "AGGREGATE")
	}
	if len(document.GroupBy) == 0 {
		if hasAggregate {
			return [][]sourceRow{rows}, nil
		}
		groups := make([][]sourceRow, len(rows))
		for index, row := range rows {
			groups[index] = []sourceRow{row}
		}
		return groups, nil
	}
	groupsByKey := map[string][]sourceRow{}
	order := []string{}
	for index, row := range rows {
		if err := checkContext(ctx, index); err != nil {
			return nil, err
		}
		values := make([]any, 0, len(document.GroupBy))
		for _, fieldID := range document.GroupBy {
			field, ok := fieldsByID[fieldID]
			if !ok {
				return nil, errors.New("groupBy references an unknown field")
			}
			value, err := evaluateExpression(field.Expression, row, nil, parameters)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		payload, _ := json.Marshal(values)
		key := string(payload)
		if _, exists := groupsByKey[key]; !exists {
			order = append(order, key)
		}
		groupsByKey[key] = append(groupsByKey[key], row)
	}
	groups := make([][]sourceRow, 0, len(order))
	for _, key := range order {
		groups = append(groups, groupsByKey[key])
	}
	return groups, nil
}

func evaluateExpression(expression dataset.Expression, row sourceRow, group []sourceRow, parameters map[string]any) (any, error) {
	// 该解释器与 querycompiler.expression 共享 DSL 语义但不共享实现。新增表达式时
	// 必须同时更新校验器、SQL 编译器和这里，并用跨执行器测试确认 NULL/类型行为。
	switch expression.Type {
	case "FIELD_REF":
		return row[expression.NodeID+"."+expression.Field], nil
	case "PARAM_REF":
		return parameters[expression.Code], nil
	case "LITERAL":
		return expression.Value, nil
	case "AGGREGATE":
		return aggregate(expression, group, parameters)
	case "DATE_TRUNC":
		if expression.Argument == nil {
			return nil, errors.New("DATE_TRUNC requires an argument")
		}
		value, err := evaluateExpression(*expression.Argument, row, group, parameters)
		if err != nil || value == nil {
			return value, err
		}
		parsed, ok := parseTime(value)
		if !ok {
			return nil, errors.New("DATE_TRUNC requires a date value")
		}
		switch expression.Unit {
		case "DAY":
			return parsed.Format("2006-01-02"), nil
		case "WEEK":
			weekday := (int(parsed.Weekday()) + 6) % 7
			return parsed.AddDate(0, 0, -weekday).Format("2006-01-02"), nil
		case "MONTH":
			return time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, parsed.Location()).Format("2006-01-02"), nil
		case "QUARTER":
			month := time.Month((int(parsed.Month())-1)/3*3 + 1)
			return time.Date(parsed.Year(), month, 1, 0, 0, 0, 0, parsed.Location()).Format("2006-01-02"), nil
		case "YEAR":
			return fmt.Sprintf("%04d-01-01", parsed.Year()), nil
		}
		return nil, ErrUnsupportedExpression
	case "CAST":
		if expression.Argument == nil {
			return nil, errors.New("CAST requires an argument")
		}
		value, err := evaluateExpression(*expression.Argument, row, group, parameters)
		if err != nil {
			return nil, err
		}
		return castValue(value, expression.TargetType)
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
		return arithmetic(expression, row, group, parameters)
	case "CONCAT", "COALESCE":
		values := make([]any, 0, len(expression.Arguments))
		for _, argument := range expression.Arguments {
			value, err := evaluateExpression(argument, row, group, parameters)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if expression.Type == "COALESCE" {
			for _, value := range values {
				if value != nil {
					return value, nil
				}
			}
			return nil, nil
		}
		parts := make([]string, len(values))
		for index, value := range values {
			parts[index] = fmt.Sprint(value)
		}
		return strings.Join(parts, ""), nil
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "LIKE", "IN", "NOT_IN":
		return evaluateComparison(expression, row, group, parameters)
	case "BETWEEN":
		if expression.Left == nil || expression.Lower == nil || expression.Upper == nil {
			return nil, errors.New("BETWEEN requires value and bounds")
		}
		value, err := evaluateExpression(*expression.Left, row, group, parameters)
		if err != nil {
			return nil, err
		}
		lower, err := evaluateExpression(*expression.Lower, row, group, parameters)
		if err != nil {
			return nil, err
		}
		upper, err := evaluateExpression(*expression.Upper, row, group, parameters)
		if err != nil {
			return nil, err
		}
		if value == nil || lower == nil || upper == nil {
			return false, nil
		}
		lowerComparison, lowerComparable := compare(value, lower)
		upperComparison, upperComparable := compare(value, upper)
		return lowerComparable && upperComparable && lowerComparison >= 0 && upperComparison <= 0, nil
	case "IS_NULL", "IS_NOT_NULL", "NOT":
		if expression.Argument == nil {
			return nil, errors.New(expression.Type + " requires an argument")
		}
		value, err := evaluateExpression(*expression.Argument, row, group, parameters)
		if err != nil {
			return nil, err
		}
		if expression.Type == "IS_NULL" {
			return value == nil, nil
		}
		if expression.Type == "IS_NOT_NULL" {
			return value != nil, nil
		}
		boolean, ok := value.(bool)
		if !ok {
			return nil, errors.New("NOT requires a boolean argument")
		}
		return !boolean, nil
	case "AND", "OR":
		result := expression.Type == "AND"
		for _, argument := range expression.Arguments {
			value, err := evaluateExpression(argument, row, group, parameters)
			if err != nil {
				return nil, err
			}
			boolean, ok := value.(bool)
			if !ok {
				return nil, errors.New("logical expression requires boolean values")
			}
			if expression.Type == "AND" {
				result = result && boolean
			} else {
				result = result || boolean
			}
		}
		return result, nil
	case "CASE":
		for _, branch := range expression.Whens {
			value, err := evaluateExpression(branch.When, row, group, parameters)
			if err != nil {
				return nil, err
			}
			if matched, ok := value.(bool); ok && matched {
				return evaluateExpression(branch.Then, row, group, parameters)
			}
		}
		if expression.Else != nil {
			return evaluateExpression(*expression.Else, row, group, parameters)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedExpression, expression.Type)
	}
}

func aggregate(expression dataset.Expression, group []sourceRow, parameters map[string]any) (any, error) {
	// 除 COUNT(*) 外，聚合函数忽略 NULL，与数据库聚合语义保持一致；空集合上的
	// SUM/AVG/MIN/MAX 返回 NULL，而 COUNT 返回 0。
	if expression.Function == "COUNT" && expression.Argument == nil {
		return int64(len(group)), nil
	}
	values := make([]any, 0, len(group))
	for _, row := range group {
		value, err := evaluateExpression(*expression.Argument, row, nil, parameters)
		if err != nil {
			return nil, err
		}
		if value != nil {
			values = append(values, value)
		}
	}
	switch expression.Function {
	case "COUNT":
		return int64(len(values)), nil
	case "COUNT_DISTINCT":
		seen := map[string]bool{}
		for _, value := range values {
			payload, _ := json.Marshal(value)
			seen[string(payload)] = true
		}
		return int64(len(seen)), nil
	case "SUM", "AVG":
		if len(values) == 0 {
			return nil, nil
		}
		if expression.Function == "SUM" {
			if total, ok, err := sumIntegers(values); err != nil {
				return nil, err
			} else if ok {
				return total, nil
			}
		}
		total := float64(0)
		for _, value := range values {
			number, ok := toFloat(value)
			if !ok {
				return nil, errors.New("SUM/AVG requires numeric values")
			}
			total += number
		}
		if expression.Function == "AVG" {
			total /= float64(len(values))
		}
		return total, nil
	case "MIN", "MAX":
		if len(values) == 0 {
			return nil, nil
		}
		result := values[0]
		for _, value := range values[1:] {
			comparison, ok := compare(value, result)
			if !ok {
				return nil, errors.New("MIN/MAX values cannot be compared")
			}
			if expression.Function == "MIN" && comparison < 0 || expression.Function == "MAX" && comparison > 0 {
				result = value
			}
		}
		return result, nil
	}
	return nil, ErrUnsupportedExpression
}

func sumIntegers(values []any) (int64, bool, error) {
	total := int64(0)
	for _, value := range values {
		var current int64
		switch typed := value.(type) {
		case int:
			current = int64(typed)
		case int64:
			current = typed
		default:
			// json.Number 可能来自值恰为整数外观的 DECIMAL，不能仅凭文本猜成整数。
			return 0, false, nil
		}
		if current > 0 && total > math.MaxInt64-current || current < 0 && total < math.MinInt64-current {
			return 0, true, errors.New("integer SUM exceeds the supported range")
		}
		total += current
	}
	return total, true, nil
}

func arithmetic(expression dataset.Expression, row sourceRow, group []sourceRow, parameters map[string]any) (any, error) {
	// 算术统一提升为 float64，按参数声明顺序从左到右计算，SUBTRACT/DIVIDE
	// 因而不是可交换操作；除零直接失败而不是产生 Inf。
	values := make([]float64, len(expression.Arguments))
	for index, argument := range expression.Arguments {
		value, err := evaluateExpression(argument, row, group, parameters)
		if err != nil {
			return nil, err
		}
		number, ok := toFloat(value)
		if !ok {
			return nil, errors.New("arithmetic expression requires numeric values")
		}
		values[index] = number
	}
	result := values[0]
	for _, value := range values[1:] {
		switch expression.Type {
		case "ADD":
			result += value
		case "SUBTRACT":
			result -= value
		case "MULTIPLY":
			result *= value
		case "DIVIDE":
			if value == 0 {
				return nil, errors.New("division by zero")
			}
			result /= value
		}
	}
	return result, nil
}

func evaluateComparison(expression dataset.Expression, row sourceRow, group []sourceRow, parameters map[string]any) (bool, error) {
	if expression.Left == nil || expression.Right == nil {
		return false, errors.New("comparison requires both operands")
	}
	left, err := evaluateExpression(*expression.Left, row, group, parameters)
	if err != nil {
		return false, err
	}
	right, err := evaluateExpression(*expression.Right, row, group, parameters)
	if err != nil {
		return false, err
	}
	if expression.Type == "IN" || expression.Type == "NOT_IN" {
		if left == nil {
			return false, nil
		}
		matched, err := collectionContains(right, left)
		if err != nil {
			return false, err
		}
		// SQL 的 x NOT IN (..., NULL) 结果为 UNKNOWN，不应被当作 true 保留。
		if expression.Type == "NOT_IN" && !matched && collectionHasNil(right) {
			return false, nil
		}
		if expression.Type == "NOT_IN" {
			matched = !matched
		}
		return matched, nil
	}
	// 与 SQL 的三值逻辑保持一致：空值参与普通比较时不命中。
	if left == nil || right == nil {
		return false, nil
	}
	if expression.Type == "LIKE" {
		pattern := regexp.QuoteMeta(fmt.Sprint(right))
		pattern = strings.ReplaceAll(strings.ReplaceAll(pattern, "%", ".*"), "_", ".")
		return regexp.MatchString("^(?s:"+pattern+")$", fmt.Sprint(left))
	}
	comparison, comparable := compare(left, right)
	if !comparable {
		return false, nil
	}
	switch expression.Type {
	case "EQUALS":
		return comparison == 0, nil
	case "NOT_EQUALS":
		return comparison != 0, nil
	case "GT":
		return comparison > 0, nil
	case "GTE":
		return comparison >= 0, nil
	case "LT":
		return comparison < 0, nil
	case "LTE":
		return comparison <= 0, nil
	}
	return false, ErrUnsupportedExpression
}

func validateColumnPolicies(document dataset.Document, policies []policy.ColumnPolicy) (int, error) {
	// 先验证策略能否安全应用，再返回所有生效 AGGREGATE_ONLY 规则中的最大分组
	// 下限。策略仓储按优先级排序，因此同字段只处理第一条。
	fields := map[string]dataset.Field{}
	for _, field := range document.Fields {
		fields[field.Code] = field
	}
	minimum := 0
	seen := map[string]struct{}{}
	for _, item := range policies {
		// 策略仓储已按优先级排序，同一字段仅采用第一条命中的规则。
		if _, exists := seen[item.FieldCode]; exists {
			continue
		}
		seen[item.FieldCode] = struct{}{}
		field, exists := fields[item.FieldCode]
		if !exists {
			continue
		}
		switch item.PolicyType {
		case "ALLOW", "NULLIFY", "HASH":
		case "DENY":
			return 0, fmt.Errorf("column %s is denied", item.FieldCode)
		case "MASK":
			if item.MaskRule.Type != "KEEP_PREFIX_SUFFIX" || item.MaskRule.PrefixLength < 0 || item.MaskRule.SuffixLength < 0 || item.MaskRule.MaskChar != "" && !safeMaskCharacter.MatchString(item.MaskRule.MaskChar) {
				return 0, fmt.Errorf("column %s has an invalid mask rule", item.FieldCode)
			}
		case "AGGREGATE_ONLY":
			allowed := false
			for _, aggregation := range item.AllowedAggregations {
				allowed = allowed || strings.EqualFold(aggregation, field.Expression.Function)
			}
			if !allowed || field.Expression.Type != "AGGREGATE" {
				return 0, fmt.Errorf("column %s requires an allowed aggregation", item.FieldCode)
			}
			minimum = max(minimum, item.MinimumGroupSize)
		default:
			return 0, errors.New("unsupported column policy")
		}
	}
	if minimum > 0 && len(document.GroupBy) == 0 {
		return 0, errors.New("minimum group size requires a grouped dataset")
	}
	return minimum, nil
}

func applyColumnPolicies(values map[string]any, policies []policy.ColumnPolicy) error {
	// 变换直接作用于待返回的输出 map，不修改源行或分组；行策略已经在调用前
	// 完成，因此 HASH/MASK/NULLIFY 不会改变权限判断依据。
	seen := map[string]bool{}
	for _, item := range policies {
		if seen[item.FieldCode] {
			continue
		}
		seen[item.FieldCode] = true
		value, exists := values[item.FieldCode]
		if !exists {
			continue
		}
		switch item.PolicyType {
		case "ALLOW", "AGGREGATE_ONLY":
		case "DENY":
			return fmt.Errorf("column %s is denied", item.FieldCode)
		case "NULLIFY":
			values[item.FieldCode] = nil
		case "HASH":
			if value != nil {
				sum := sha256.Sum256([]byte(fmt.Sprint(value)))
				values[item.FieldCode] = hex.EncodeToString(sum[:])
			}
		case "MASK":
			if value != nil {
				values[item.FieldCode] = maskValue(fmt.Sprint(value), item.MaskRule)
			}
		}
	}
	return nil
}

func evaluateRowPolicies(policies []policy.RowPolicy, scope policy.UserScope, values map[string]any) (bool, error) {
	if len(policies) == 0 {
		return true, nil
	}
	ordered := append([]policy.RowPolicy(nil), policies...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	allowAND, allowOR, denies := []bool{}, []bool{}, []bool{}
	for _, item := range ordered {
		value, err := evaluatePolicyExpression(item.Expression, scope.Attributes, values)
		if err != nil {
			return false, err
		}
		if item.Effect == "DENY" || item.CombineMode == "DENY_OVERRIDE" {
			denies = append(denies, value)
		} else if item.CombineMode == "AND" {
			allowAND = append(allowAND, value)
		} else if item.CombineMode == "OR" {
			allowOR = append(allowOR, value)
		} else {
			return false, errors.New("unsupported row policy combine mode")
		}
	}
	// 组合规则与数据库编译器一致：AND 全部通过、OR 至少一个通过、DENY 任一
	// 命中即拒绝。三组最终取交集，使拒绝规则拥有覆盖优先级。
	for _, value := range allowAND {
		if !value {
			return false, nil
		}
	}
	if len(allowOR) > 0 {
		matched := false
		for _, value := range allowOR {
			matched = matched || value
		}
		if !matched {
			return false, nil
		}
	}
	for _, value := range denies {
		if value {
			return false, nil
		}
	}
	return true, nil
}

func evaluatePolicyExpression(expression policy.Expression, attributes, values map[string]any) (bool, error) {
	// 策略值域被限制为输出字段、用户属性和字面量；缺失字段/属性属于配置错误，
	// 必须失败关闭，不能按 nil 继续比较。
	resolve := func(item *policy.Expression) (any, error) {
		switch item.Type {
		case "FIELD_REF":
			value, ok := values[item.FieldCode]
			if !ok {
				return nil, errors.New("row policy references a non-output field")
			}
			return value, nil
		case "USER_ATTRIBUTE_REF":
			value, ok := attributes[item.Attribute]
			if !ok {
				return nil, errors.New("row policy user attribute is missing")
			}
			return value, nil
		case "LITERAL":
			return item.Value, nil
		}
		return nil, errors.New("unsupported row policy value")
	}
	switch expression.Type {
	case "EQUALS", "NOT_EQUALS", "IN":
		if expression.Left == nil || expression.Right == nil {
			return false, errors.New("invalid row policy expression")
		}
		left, err := resolve(expression.Left)
		if err != nil {
			return false, err
		}
		right, err := resolve(expression.Right)
		if err != nil {
			return false, err
		}
		if expression.Type == "IN" {
			return collectionContains(right, left)
		}
		if left == nil || right == nil {
			return false, nil
		}
		comparison, ok := compare(left, right)
		if !ok {
			return false, nil
		}
		if expression.Type == "EQUALS" {
			return comparison == 0, nil
		}
		return comparison != 0, nil
	case "AND", "OR":
		if len(expression.Children) < 2 {
			return false, errors.New("invalid logical row policy")
		}
		result := expression.Type == "AND"
		for _, child := range expression.Children {
			value, err := evaluatePolicyExpression(child, attributes, values)
			if err != nil {
				return false, err
			}
			if expression.Type == "AND" {
				result = result && value
			} else {
				result = result || value
			}
		}
		return result, nil
	}
	return false, errors.New("unsupported row policy expression")
}

func sortOutput(document dataset.Document, rows []outputRow) error {
	// 多字段排序按 DSL 声明顺序逐项比较；稳定排序保留完全相等行的原始顺序。
	// NULL 位置由 nulls 显式控制，未指定时文件执行器固定放在末尾，不受方向反转。
	fields := map[string]string{}
	for _, field := range document.Fields {
		fields[field.ID] = field.Code
	}
	var sortErr error
	sort.SliceStable(rows, func(i, j int) bool {
		for _, item := range document.Sorts {
			code := fields[item.FieldID]
			left, right := rows[i].values[code], rows[j].values[code]
			if left == nil || right == nil {
				if left == nil && right == nil {
					continue
				}
				nullsFirst := item.Nulls == "FIRST"
				if item.Nulls == "" {
					nullsFirst = false
				}
				return left == nil && nullsFirst || right == nil && !nullsFirst
			}
			comparison, ok := compare(left, right)
			if !ok {
				sortErr = errors.New("sort values cannot be compared")
				return false
			}
			if comparison == 0 {
				continue
			}
			if item.Direction == "DESC" {
				return comparison > 0
			}
			return comparison < 0
		}
		return false
	})
	return sortErr
}

func parseCell(value, canonicalType string) (any, error) {
	// 空字符串视为 NULL；非空值必须严格匹配同步时记录的规范类型，解析失败时
	// 返回带工作表坐标的错误，而不是悄悄降级成字符串。
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	switch canonicalType {
	case "NUMBER", "INTEGER":
		return strconv.ParseInt(value, 10, 64)
	case "DECIMAL":
		return strconv.ParseFloat(value, 64)
	case "BOOLEAN":
		switch strings.ToLower(value) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		}
		return nil, errors.New("invalid boolean")
	case "DATE":
		for _, layout := range []string{"2006-01-02", "2006/01/02", "01/02/2006"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Format("2006-01-02"), nil
			}
		}
		return nil, errors.New("invalid date")
	case "DATETIME":
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "01/02/06 15:04", "2006/01/02 15:04:05"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Format(time.RFC3339), nil
			}
		}
		return nil, errors.New("invalid datetime")
	}
	return value, nil
}

func compare(left, right any) (int, bool) {
	// 比较器只在明确可比时返回 true：数值跨具体类型比较，其余值要求同类语义。
	// 调用方把不可比视为“不命中”，避免 fmt.Sprint 带来的偶然相等。
	if left == nil || right == nil {
		if left == nil && right == nil {
			return 0, true
		}
		return 0, false
	}
	if leftNumber, ok := toFloat(left); ok {
		if rightNumber, ok := toFloat(right); ok {
			switch {
			case leftNumber < rightNumber:
				return -1, true
			case leftNumber > rightNumber:
				return 1, true
			default:
				return 0, true
			}
		}
	}
	leftText, rightText := fmt.Sprint(left), fmt.Sprint(right)
	return strings.Compare(leftText, rightText), true
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	}
	return 0, false
}

func castValue(value any, target string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch target {
	case "STRING":
		return fmt.Sprint(value), nil
	case "INTEGER":
		number, ok := toFloat(value)
		if !ok || math.Trunc(number) != number {
			return nil, errors.New("value cannot be cast to integer")
		}
		return int64(number), nil
	case "DECIMAL":
		number, ok := toFloat(value)
		if !ok {
			return nil, errors.New("value cannot be cast to decimal")
		}
		return number, nil
	case "BOOLEAN":
		if boolean, ok := value.(bool); ok {
			return boolean, nil
		}
		parsed, err := strconv.ParseBool(fmt.Sprint(value))
		return parsed, err
	case "DATE", "DATETIME":
		parsed, ok := parseTime(value)
		if !ok {
			return nil, errors.New("value cannot be cast to date")
		}
		if target == "DATE" {
			return parsed.Format("2006-01-02"), nil
		}
		return parsed.Format(time.RFC3339), nil
	}
	return nil, ErrUnsupportedExpression
}

func parseTime(value any) (time.Time, bool) {
	if parsed, ok := value.(time.Time); ok {
		return parsed, true
	}
	text := fmt.Sprint(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006/01/02", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func collectionContains(collection, target any) (bool, error) {
	// SQL 中空值不会命中 IN 集合，避免 Go 的 nil 等值语义产生偏差。
	if target == nil {
		return false, nil
	}
	value := reflect.ValueOf(collection)
	if !value.IsValid() || value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return false, errors.New("IN requires a collection")
	}
	for index := 0; index < value.Len(); index++ {
		comparison, ok := compare(target, value.Index(index).Interface())
		if ok && comparison == 0 {
			return true, nil
		}
	}
	return false, nil
}

func collectionHasNil(collection any) bool {
	// 单独检查集合中的 nil 是为了复现 NOT IN 的 SQL 三值逻辑。
	value := reflect.ValueOf(collection)
	if !value.IsValid() || value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return false
	}
	for index := 0; index < value.Len(); index++ {
		item := value.Index(index)
		if item.Kind() == reflect.Interface && item.IsNil() || item.Kind() == reflect.Pointer && item.IsNil() {
			return true
		}
	}
	return false
}

func hasNilParameter(expression dataset.Expression, parameters map[string]any) bool {
	// optional 过滤器的参数可能嵌在深层表达式中，必须递归覆盖全部子节点。
	if expression.Type == "PARAM_REF" {
		return parameters[expression.Code] == nil
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil && hasNilParameter(*child, parameters) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if hasNilParameter(child, parameters) {
			return true
		}
	}
	for _, branch := range expression.Whens {
		if hasNilParameter(branch.When, parameters) || hasNilParameter(branch.Then, parameters) {
			return true
		}
	}
	return false
}

func expressionContains(expression dataset.Expression, kind string) bool {
	// 用于判断无 groupBy 的查询是否仍包含聚合；遍历规则需与表达式模型同步。
	if expression.Type == kind {
		return true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil && expressionContains(*child, kind) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if expressionContains(child, kind) {
			return true
		}
	}
	return false
}

func maskValue(value string, rule policy.MaskRule) string {
	// 按 rune 而不是字节截取，防止中文或 emoji 被切成无效 UTF-8。前后保留段
	// 重叠时后缀会收缩，确保结果不会重复原文字符。
	characters := []rune(value)
	prefix := min(rule.PrefixLength, len(characters))
	suffix := min(rule.SuffixLength, max(len(characters)-prefix, 0))
	mask := rule.MaskChar
	if mask == "" || !utf8.ValidString(mask) {
		mask = "*"
	}
	return string(characters[:prefix]) + strings.Repeat(mask, max(len(characters)-prefix-suffix, 0)) + string(characters[len(characters)-suffix:])
}

func checkContext(ctx context.Context, index int) error {
	// 每 128 行轮询一次，在取消响应速度和热循环开销之间折中。所有可能处理大量
	// 行的循环都应调用此函数，新增循环时不能遗漏。
	if index%128 != 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneRow(row sourceRow) sourceRow {
	// Join 过程中始终复制 map，避免合并右行时污染仍会参与后续匹配的左侧输入。
	result := make(sourceRow, len(row))
	for key, value := range row {
		result[key] = value
	}
	return result
}
func cloneRows(rows []sourceRow) []sourceRow {
	result := make([]sourceRow, len(rows))
	for index, row := range rows {
		result[index] = cloneRow(row)
	}
	return result
}
func mergeRows(left, right sourceRow) sourceRow {
	result := cloneRow(left)
	for key, value := range right {
		result[key] = value
	}
	return result
}
