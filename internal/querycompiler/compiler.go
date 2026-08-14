package querycompiler

import (
	"crypto/sha256"
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

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/physicalname"
	"intelligent-report-generation-system/internal/policy"
)

type Dialect string

const (
	MySQL      Dialect = "MYSQL"
	Oracle     Dialect = "ORACLE"
	PostgreSQL Dialect = "POSTGRESQL"
	SQLServer  Dialect = "SQLSERVER"
)

// DialectForSourceType maps the shared data-source catalog to the closest
// compiler dialect. MariaDB and ClickHouse use the MySQL-compatible SQL path;
// PostgreSQL and SQL Server keep their own pagination and placeholder rules.
func DialectForSourceType(sourceType string) (Dialect, bool) {
	switch strings.ToUpper(strings.TrimSpace(sourceType)) {
	case "MYSQL", "MARIADB", "CLICKHOUSE":
		return MySQL, true
	case "POSTGRESQL":
		return PostgreSQL, true
	case "ORACLE":
		return Oracle, true
	case "SQLSERVER":
		return SQLServer, true
	default:
		return "", false
	}
}

func supportedDialect(value Dialect) bool {
	return value == MySQL || value == Oracle || value == PostgreSQL || value == SQLServer
}

// LimitKind selects which already-validated Dataset DSL ceiling applies to a
// compilation. Existing callers default to PREVIEW; governed formal-query
// adapters must opt in to RESULT explicitly and still cannot exceed the
// document's result limit.
type LimitKind string

const (
	LimitPreview LimitKind = "PREVIEW"
	LimitResult  LimitKind = "RESULT"
)

var safeIdentifier = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_$#]{0,127}$`)
var safeMaskCharacter = regexp.MustCompile(`^[\p{L}\p{N}*•]$`)

// TableRef 是从控制库加载的可信物理表白名单。
type TableRef struct {
	NodeID, Schema, Name string
	Columns              map[string]bool
	ColumnTypes          map[string]string
}

// Input 包含编译所需 DSL、物理白名单、运行参数和已匹配权限策略。
type Input struct {
	Document       dataset.Document
	Dialect        Dialect
	Tables         map[string]TableRef
	Parameters     map[string]any
	Scope          policy.UserScope
	RowPolicies    []policy.RowPolicy
	ColumnPolicies []policy.ColumnPolicy
	MaxRows        int
	LimitKind      LimitKind
}

// CompiledQuery 是可直接交给只读 Connector 的参数化查询计划。
type CompiledQuery struct {
	SQL      string
	Args     []any
	MaxRows  int
	PlanHash string
}

type compiler struct {
	input Input
	args  []any
}

// Compile 仅从结构化 DSL 生成 SQL，并对全部标识符和值执行失败关闭校验。
func Compile(input Input) (CompiledQuery, error) {
	if !supportedDialect(input.Dialect) {
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
	if len(input.Document.Nodes) == 0 {
		return CompiledQuery{}, errors.New("secure compiler requires at least one input node")
	}
	if input.Document.Dataset.Type != "SINGLE_SOURCE" &&
		!(input.Dialect == PostgreSQL &&
			input.Document.Dataset.Type == "CROSS_SOURCE") {
		// CROSS_SOURCE plans may only be compiled directly after every logical
		// input has been rebound to a trusted relation in the governed
		// PostgreSQL warehouse. Source-plane cross-source execution must keep
		// using the federated executor so credentials and trust boundaries are
		// never collapsed by caller-authored metadata.
		return CompiledQuery{}, errors.New("secure compiler requires a single-source dataset outside the governed PostgreSQL warehouse")
	}
	limitKind := input.LimitKind
	if limitKind == "" {
		limitKind = LimitPreview
	}
	limitCeiling := input.Document.ExecutionPolicy.PreviewLimit
	if limitKind == LimitResult {
		limitCeiling = input.Document.ExecutionPolicy.ResultLimit
	} else if limitKind != LimitPreview {
		return CompiledQuery{}, errors.New("query limit kind is invalid")
	}
	if input.MaxRows < 1 || input.MaxRows > limitCeiling {
		return CompiledQuery{}, errors.New("query row limit is invalid")
	}
	input.LimitKind = limitKind
	parameters, err := NormalizeParameters(input.Document.Parameters, input.Parameters)
	if err != nil {
		return CompiledQuery{}, err
	}
	input.Parameters = parameters
	// 空参数查询也必须生成非 nil 切片；Connector 的 JSON 合同要求数组，nil 会被
	// 编码成 null 并在真正执行 SQL 之前被远端以 422 拒绝。
	c := &compiler{input: input, args: []any{}}
	// 内层查询只负责数据集语义（Join、计算、聚合）；外层查询再应用行列权限。
	// 这种分层让策略只能看到稳定的输出字段，不能借策略表达式触达物理源列。
	inner, err := c.compileInner()
	if err != nil {
		return CompiledQuery{}, err
	}
	rowSQL, err := c.compileRows()
	if err != nil {
		return CompiledQuery{}, err
	}
	columns, err := c.compileColumns()
	if err != nil {
		return CompiledQuery{}, err
	}
	sql := "SELECT " + strings.Join(columns, ", ") + " FROM (" + inner + ") " + c.quote("secure_base")
	if rowSQL != "" {
		sql += " WHERE " + rowSQL
	}
	order, err := c.compileSorts()
	if err != nil {
		return CompiledQuery{}, err
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	// Connector 会额外读取一行来确认调用方没有把截断结果误当成完整结果。
	// 因此最终查询必须把已经校验过的行数上限下推到数据库；仅传 MaxRows
	// 会让任何超过上限的合法结果被 Connector 以 413 拒绝，发布的一行试跑
	// 也会因此对多行数据集稳定失败。
	if input.Dialect == Oracle {
		sql += " FETCH FIRST " + c.bind(input.MaxRows) + " ROWS ONLY"
	} else if input.Dialect == SQLServer {
		if order == "" {
			sql += " ORDER BY (SELECT NULL)"
		}
		sql += " OFFSET 0 ROWS FETCH NEXT " + c.bind(input.MaxRows) + " ROWS ONLY"
	} else {
		sql += " LIMIT " + c.bind(input.MaxRows)
	}
	// 标识符已通过白名单校验、值已全部绑定，此处再做最终令牌兜底。命中时宁可
	// 拒绝整个计划，也不尝试清理 SQL 后继续执行。
	if strings.ContainsAny(sql, ";\x00") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return CompiledQuery{}, errors.New("compiled query contains a forbidden token")
	}
	planHash, err := compiledPlanHash(input, sql)
	if err != nil {
		return CompiledQuery{}, err
	}
	return CompiledQuery{SQL: sql, Args: c.args, MaxRows: input.MaxRows, PlanHash: planHash}, nil
}

type compiledPlanParameter struct {
	Code       string `json:"code"`
	DataType   string `json:"dataType"`
	MultiValue bool   `json:"multiValue"`
	Required   bool   `json:"required"`
}

type compiledPlanColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type compiledPlanTable struct {
	NodeID  string               `json:"nodeId"`
	Schema  string               `json:"schema"`
	Name    string               `json:"name"`
	Columns []compiledPlanColumn `json:"columns"`
}

// compiledPlanHash fingerprints only executable shape: SQL placeholders,
// limit contract, parameter types and the trusted physical whitelist. Runtime
// values are intentionally excluded so plan artifacts cannot become a side
// channel for question parameters or policy attributes.
func compiledPlanHash(input Input, sql string) (string, error) {
	parameters := make([]compiledPlanParameter, 0, len(input.Document.Parameters))
	for _, parameter := range input.Document.Parameters {
		parameters = append(parameters, compiledPlanParameter{
			Code: parameter.Code, DataType: parameter.DataType,
			MultiValue: parameter.MultiValue, Required: parameter.Required,
		})
	}
	tableIDs := make([]string, 0, len(input.Tables))
	for nodeID := range input.Tables {
		tableIDs = append(tableIDs, nodeID)
	}
	sort.Strings(tableIDs)
	tables := make([]compiledPlanTable, 0, len(tableIDs))
	for _, nodeID := range tableIDs {
		ref := input.Tables[nodeID]
		columnNames := make([]string, 0, len(ref.Columns))
		for column, available := range ref.Columns {
			if available {
				columnNames = append(columnNames, column)
			}
		}
		sort.Strings(columnNames)
		columns := make([]compiledPlanColumn, 0, len(columnNames))
		for _, column := range columnNames {
			columns = append(columns, compiledPlanColumn{Name: column, Type: ref.ColumnTypes[column]})
		}
		tables = append(tables, compiledPlanTable{
			NodeID: ref.NodeID, Schema: ref.Schema, Name: ref.Name, Columns: columns,
		})
	}
	payload, err := json.Marshal(struct {
		Version    string                  `json:"version"`
		Dialect    Dialect                 `json:"dialect"`
		LimitKind  LimitKind               `json:"limitKind"`
		MaxRows    int                     `json:"maxRows"`
		SQL        string                  `json:"sql"`
		Parameters []compiledPlanParameter `json:"parameters"`
		Tables     []compiledPlanTable     `json:"tables"`
	}{"querycompiler-plan-v1", input.Dialect, input.LimitKind, input.MaxRows, sql, parameters, tables})
	if err != nil {
		return "", fmt.Errorf("marshal compiled plan fingerprint: %w", err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:]), nil
}

// NormalizeParameters 按 DSL 参数定义应用默认值和类型约束，并拒绝未声明参数。
func NormalizeParameters(definitions []dataset.Parameter, values map[string]any) (map[string]any, error) {
	// 先按声明表驱动转换，再拒绝剩余键；调用方不能通过附加参数影响占位符顺序，
	// 默认值也会和显式输入经过同一套类型检查。
	if values == nil {
		values = map[string]any{}
	}
	definitionsByCode := make(map[string]dataset.Parameter, len(definitions))
	for _, definition := range definitions {
		definitionsByCode[definition.Code] = definition
	}
	for code := range values {
		if _, ok := definitionsByCode[code]; !ok {
			return nil, fmt.Errorf("parameter %s is not declared by the dataset", code)
		}
	}
	out := make(map[string]any, len(definitions))
	for _, definition := range definitions {
		value, supplied := values[definition.Code]
		if !supplied && definition.DefaultValue != nil {
			value, supplied = definition.DefaultValue, true
		}
		if !supplied {
			if definition.Required {
				return nil, fmt.Errorf("required parameter %s is missing", definition.Code)
			}
			out[definition.Code] = nil
			continue
		}
		converted, err := normalizeParameterValue(definition, value)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", definition.Code, err)
		}
		out[definition.Code] = converted
	}
	return out, nil
}

func normalizeParameterValue(definition dataset.Parameter, value any) (any, error) {
	// 多值参数逐项规范化，避免集合中混入与标量路径不同的 JSON 类型。
	if definition.MultiValue {
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) || reflected.Len() < 1 || reflected.Len() > 1000 {
			return nil, errors.New("multi-value parameter must contain 1 to 1000 values")
		}
		values := make([]any, reflected.Len())
		for i := 0; i < reflected.Len(); i++ {
			converted, err := normalizeScalar(definition.DataType, reflected.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			values[i] = converted
		}
		return values, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() && (reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array) {
		return nil, errors.New("scalar parameter cannot use a collection value")
	}
	return normalizeScalar(definition.DataType, value)
}

func normalizeScalar(dataType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch dataType {
	case "STRING":
		result, ok := value.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		return result, nil
	case "INTEGER":
		return normalizeInteger(value)
	case "DECIMAL":
		return normalizeDecimal(value)
	case "BOOLEAN":
		switch typed := value.(type) {
		case bool:
			return typed, nil
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err == nil {
				return parsed, nil
			}
		}
		return nil, errors.New("must be a boolean")
	case "DATE":
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("must use YYYY-MM-DD")
		}
		parsed, err := time.Parse("2006-01-02", text)
		if err != nil {
			return nil, errors.New("must use YYYY-MM-DD")
		}
		return parsed.Format("2006-01-02"), nil
	case "DATETIME":
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("must be an RFC3339 or SQL datetime string")
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.Format(layout), nil
			}
		}
		return nil, errors.New("must be an RFC3339 or SQL datetime string")
	default:
		return nil, errors.New("uses an unsupported data type")
	}
}

func normalizeInteger(value any) (int64, error) {
	// JSON 数字通常以 float64 或 json.Number 进入服务；只有无小数且未溢出的值
	// 才能收敛为 int64，避免不同入口对同一个参数产生不同解释。
	var number float64
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, errors.New("is outside the supported integer range")
		}
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, errors.New("is outside the supported integer range")
		}
		return int64(typed), nil
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, nil
		}
		return 0, errors.New("must be an integer")
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed, nil
		}
		return 0, errors.New("must be an integer")
	default:
		return 0, errors.New("must be an integer")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number > math.MaxInt64 || number < math.MinInt64 {
		return 0, errors.New("must be an integer")
	}
	return int64(number), nil
}

func normalizeDecimal(value any) (any, error) {
	// 保留 json.Number 的十进制文本直到序列化阶段，避免在主服务内先转 float64
	// 引入不必要的精度损失。
	switch typed := value.(type) {
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return nil, errors.New("must be a decimal number")
		}
		return typed, nil
	case string:
		if _, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err != nil {
			return nil, errors.New("must be a decimal number")
		}
		return strings.TrimSpace(typed), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return value, nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, errors.New("must be a finite decimal number")
		}
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, errors.New("must be a finite decimal number")
		}
		return typed, nil
	default:
		return nil, errors.New("must be a decimal number")
	}
}

func (c *compiler) compileInner() (string, error) {
	aliases := map[string]string{}
	from := ""
	fromArgs := []any{}
	// 选择没有入边的节点作为 Join 根。领域校验只保证忽略方向后的连通性；若存在
	// 方向环或声明无法按 left -> right 扩展，下面的循环会失败而不会重排外连接。
	incoming := map[string]bool{}
	for _, join := range c.input.Document.Joins {
		incoming[join.RightNodeID] = true
	}
	rootID := c.input.Document.Nodes[0].ID
	for _, node := range c.input.Document.Nodes {
		if !incoming[node.ID] {
			rootID = node.ID
			break
		}
	}
	for _, node := range c.input.Document.Nodes {
		ref, ok := c.input.Tables[node.ID]
		if !ok || ref.NodeID != node.ID {
			return "", fmt.Errorf("node %s is not in the physical table whitelist", node.ID)
		}
		if err := c.validateTable(ref); err != nil {
			return "", err
		}
		aliases[node.ID] = node.Alias
		if node.ID == rootID {
			relation, args, err := c.nodeRelation(node, ref)
			if err != nil {
				return "", err
			}
			from = relation + " " + c.quote(node.Alias)
			fromArgs = append(fromArgs, args...)
		}
	}
	joined := map[string]bool{rootID: true}
	remaining := append([]dataset.Join(nil), c.input.Document.Joins...)
	for len(remaining) > 0 {
		progressed := false
		next := make([]dataset.Join, 0, len(remaining))
		for _, join := range remaining {
			// 为保持 LEFT/RIGHT JOIN 方向语义，仅在左节点已接入且右节点尚未接入时编译。
			if !joined[join.LeftNodeID] || joined[join.RightNodeID] {
				next = append(next, join)
				continue
			}
			ref, ok := c.input.Tables[join.RightNodeID]
			if !ok {
				return "", errors.New("join table is not in the physical whitelist")
			}
			if c.input.Dialect == MySQL && join.JoinType == "FULL" {
				return "", errors.New("MySQL does not support FULL JOIN")
			}
			conditions := make([]string, 0, len(join.Conditions))
			for _, condition := range join.Conditions {
				left, err := c.expression(condition.LeftExpression, aliases)
				if err != nil {
					return "", err
				}
				right, err := c.expression(condition.RightExpression, aliases)
				if err != nil {
					return "", err
				}
				op, err := comparison(condition.Operator)
				if err != nil {
					return "", err
				}
				conditions = append(conditions, left+" "+op+" "+right)
			}
			node := c.documentNode(join.RightNodeID)
			relation, args, err := c.nodeRelation(node, ref)
			if err != nil {
				return "", err
			}
			from += " " + join.JoinType + " JOIN " + relation + " " + c.quote(aliases[join.RightNodeID]) + " ON " + strings.Join(conditions, " AND ")
			fromArgs = append(fromArgs, args...)
			joined[join.RightNodeID], progressed = true, true
		}
		if !progressed {
			return "", errors.New("join order cannot be compiled without changing its declared direction")
		}
		remaining = next
	}
	fieldExpressions := map[string]string{}
	fieldIndexes := map[string]int{}
	projections := make([]string, 0, len(c.input.Document.Fields))
	for index, field := range c.input.Document.Fields {
		expression, err := c.expression(field.Expression, aliases)
		if err != nil {
			return "", fmt.Errorf("field %s: %w", field.Code, err)
		}
		fieldExpressions[field.ID] = expression
		fieldIndexes[field.ID] = index
		projections = append(projections, expression+" AS "+c.quote(field.Code))
	}
	// SELECT 表达式的绑定值在 SQL 文本中先于 FROM 派生表，因此关联前分组的
	// sourceFilter 参数必须在投影参数之后、外层 WHERE 参数之前追加。
	c.args = append(c.args, fromArgs...)
	// 节点过滤和 PRE_AGGREGATION 过滤都进入 WHERE，因此一定发生在分组之前。
	// 传统 sourceFilter 仍支持固定值，但值同样只能经 bind 进入 SQL。
	where := make([]string, 0, len(c.input.Document.Filters))
	for _, node := range c.input.Document.Nodes {
		if c.preAggregation(node.ID) != nil {
			continue
		}
		for _, filter := range node.SourceFilters {
			if filter.Expression != nil {
				value, err := c.expression(*filter.Expression, aliases)
				if err != nil {
					return "", err
				}
				where = append(where, value)
				continue
			}
			ref := c.input.Tables[node.ID]
			if !ref.Columns[filter.Field] {
				return "", errors.New("source filter field is not in the whitelist")
			}
			left := c.quote(node.Alias) + "." + c.quote(filter.Field)
			if filter.Operator == "IS_NULL" {
				where = append(where, left+" IS NULL")
				continue
			}
			if filter.Operator == "IS_NOT_NULL" {
				where = append(where, left+" IS NOT NULL")
				continue
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
				where = append(where, "("+left+" "+op+" ("+values+"))")
				continue
			}
			where = append(where, "("+left+" "+op+" "+c.bind(filter.Value)+")")
		}
	}
	for _, filter := range c.input.Document.Filters {
		if filter.Optional && hasNilParameter(filter.Expression, c.input.Parameters) {
			continue
		}
		expression, err := c.expression(filter.Expression, aliases)
		if err != nil {
			return "", err
		}
		where = append(where, expression)
	}
	selectPrefix := "SELECT "
	if c.input.Document.Distinct {
		selectPrefix = "SELECT DISTINCT "
	}
	groups := make([]string, 0, len(c.input.Document.GroupBy))
	groupAliases := make([]string, 0, len(c.input.Document.GroupBy))
	groupExpressions := make(map[string]dataset.Expression, len(c.input.Document.GroupBy))
	if len(c.input.Document.GroupBy) > 0 {
		for _, id := range c.input.Document.GroupBy {
			_, ok := fieldExpressions[id]
			if !ok {
				return "", errors.New("groupBy references an unknown field")
			}
			field := c.input.Document.Fields[fieldIndexes[id]]
			groupAliases = append(groupAliases, c.quote(field.Code))
			groupExpressions[id] = field.Expression
			// GROUP BY occurs after WHERE in SQL text. Compile native grouping
			// expressions again here so any bound literals are appended in the
			// same order as their second appearance in the query.
			if c.input.Document.GroupByMode == dataset.GroupByModeSets {
				continue
			}
			if c.input.Dialect != MySQL ||
				(c.input.Document.GroupByMode != dataset.GroupByModeCube &&
					c.input.Document.GroupByMode != dataset.GroupByModeRollup) {
				value, err := c.expression(field.Expression, aliases)
				if err != nil {
					return "", err
				}
				groups = append(groups, value)
			} else {
				groups = append(groups, fieldExpressions[id])
			}
		}
	}
	groupClause := strings.Join(groups, ", ")
	switch c.input.Document.GroupByMode {
	case dataset.GroupByModeCube:
		if c.input.Dialect != MySQL {
			groupClause = "CUBE (" + groupClause + ")"
		}
	case dataset.GroupByModeRollup:
		if c.input.Dialect == MySQL {
			groupClause = strings.Join(groupAliases, ", ") + " WITH ROLLUP"
		} else {
			groupClause = "ROLLUP (" + groupClause + ")"
		}
	case dataset.GroupByModeSets:
		if c.input.Dialect != MySQL {
			sets := make([]string, 0, len(c.input.Document.GroupingSets))
			for _, groupingSet := range c.input.Document.GroupingSets {
				expressions := make([]string, 0, len(groupingSet))
				for _, id := range groupingSet {
					expression, ok := groupExpressions[id]
					if !ok {
						return "", errors.New("groupingSets references an unknown groupBy field")
					}
					value, err := c.expression(expression, aliases)
					if err != nil {
						return "", err
					}
					expressions = append(expressions, value)
				}
				sets = append(sets, "("+strings.Join(expressions, ", ")+")")
			}
			groupClause = "GROUPING SETS (" + strings.Join(sets, ", ") + ")"
		}
	}
	having := []string{}
	if len(c.input.Document.Having) > 0 {
		for _, filter := range c.input.Document.Having {
			if filter.Optional && hasNilParameter(filter.Expression, c.input.Parameters) {
				continue
			}
			value, err := c.expression(filter.Expression, aliases)
			if err != nil {
				return "", err
			}
			having = append(having, value)
		}
	}
	minimumGroupSize := c.minimumGroupSize()
	if minimumGroupSize > 0 {
		// AGGREGATE_ONLY 的最小分组人数是数据泄露防线，必须与业务 HAVING
		// 同层执行；放到外层会允许小分组先产生敏感聚合结果。
		if len(c.input.Document.GroupBy) == 0 {
			return "", errors.New("minimum group size requires a grouped dataset")
		}
		having = append(having, "COUNT(*) >= "+c.bind(minimumGroupSize))
	}
	buildSelect := func(branchProjections []string, groupClause string) string {
		sql := selectPrefix + strings.Join(branchProjections, ", ") + " FROM " + from
		if len(where) > 0 {
			sql += " WHERE " + strings.Join(where, " AND ")
		}
		if groupClause != "" {
			sql += " GROUP BY " + groupClause
		}
		if len(having) > 0 {
			sql += " HAVING " + strings.Join(having, " AND ")
		}
		return sql
	}
	if c.input.Dialect == MySQL &&
		(c.input.Document.GroupByMode == dataset.GroupByModeCube ||
			c.input.Document.GroupByMode == dataset.GroupByModeSets) {
		// MySQL has ROLLUP but no CUBE or GROUPING SETS. Expand the requested
		// grouping sets as UNION ALL;
		// including the bit mask in branch construction preserves distinct rows
		// when source dimensions themselves contain NULL.
		groupingSelections, err := groupingSelectionsForFields(
			c.input.Document.GroupByMode,
			c.input.Document.GroupBy,
			c.input.Document.GroupingSets,
		)
		if err != nil {
			return "", err
		}
		branches := make([]string, 0, len(groupingSelections))
		for _, selected := range groupingSelections {
			branchProjections := append([]string(nil), projections...)
			activeGroups := make([]string, 0, len(groupAliases))
			for index, fieldID := range c.input.Document.GroupBy {
				if selected[index] {
					activeGroups = append(activeGroups, groupAliases[index])
					continue
				}
				fieldIndex := fieldIndexes[fieldID]
				field := c.input.Document.Fields[fieldIndex]
				branchProjections[fieldIndex] = "CASE WHEN 1 = 1 THEN NULL ELSE " +
					fieldExpressions[fieldID] + " END AS " + c.quote(field.Code)
			}
			branches = append(branches, buildSelect(branchProjections, strings.Join(activeGroups, ", ")))
		}
		templateArgs := append([]any(nil), c.args...)
		c.args = make([]any, 0, len(templateArgs)*len(branches))
		for range branches {
			c.args = append(c.args, templateArgs...)
		}
		return strings.Join(branches, " UNION ALL "), nil
	}
	return buildSelect(projections, groupClause), nil
}

func (c *compiler) documentNode(nodeID string) dataset.Node {
	for _, node := range c.input.Document.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	return dataset.Node{}
}

func (c *compiler) preAggregation(nodeID string) *dataset.PreAggregation {
	for index := range c.input.Document.PreAggregations {
		if c.input.Document.PreAggregations[index].NodeID == nodeID {
			return &c.input.Document.PreAggregations[index]
		}
	}
	return nil
}

func (c *compiler) preAggregationProduces(nodeID, field string) bool {
	item := c.preAggregation(nodeID)
	if item == nil {
		return false
	}
	for _, group := range item.GroupBy {
		if group.Field == field {
			return true
		}
	}
	for _, metric := range item.Metrics {
		if metric.Field == field {
			return true
		}
	}
	return false
}

// nodeRelation 把显式关联前分组编译成受白名单约束的派生表。派生表输出仍沿用
// 原字段名，因此外层 Join 和 FIELD_REF 的结构化引用保持稳定。
func (c *compiler) nodeRelation(node dataset.Node, ref TableRef) (string, []any, error) {
	item := c.preAggregation(node.ID)
	if item == nil {
		return c.tableName(ref), nil, nil
	}
	sub := &compiler{input: c.input, args: []any{}}
	aliases := map[string]string{node.ID: "pre_source"}
	projections := make([]string, 0, len(item.GroupBy)+len(item.Metrics))
	groups := make([]string, 0, len(item.GroupBy))
	groupAliases := make([]string, 0, len(item.GroupBy))
	groupExpressions := make([]dataset.Expression, 0, len(item.GroupBy))
	for _, group := range item.GroupBy {
		expression := dataset.Expression{Type: "FIELD_REF", NodeID: node.ID, Field: group.Field}
		if group.Expression != nil {
			expression = *group.Expression
		}
		if group.Unit != "" {
			// Keep the child in a distinct value before replacing expression.
			// Taking &expression directly in the replacement literal makes
			// Argument point back to the new DATE_TRUNC node itself and causes
			// unbounded recursion during publication validation.
			argument := expression
			expression = dataset.Expression{
				Type: "DATE_TRUNC", Unit: group.Unit, Argument: &argument,
			}
		}
		value, err := sub.expression(expression, aliases)
		if err != nil {
			return "", nil, err
		}
		projections = append(projections, value+" AS "+c.quote(group.Field))
		groups = append(groups, value)
		groupAliases = append(groupAliases, c.quote(group.Field))
		groupExpressions = append(groupExpressions, expression)
	}
	for _, metric := range item.Metrics {
		aggregate := dataset.Expression{Type: "AGGREGATE", Function: metric.Function}
		if !metric.CountRows {
			argument := dataset.Expression{Type: "FIELD_REF", NodeID: node.ID, Field: metric.Field}
			if metric.Expression != nil {
				argument = *metric.Expression
			}
			aggregate.Argument = &argument
		}
		value, err := sub.expression(aggregate, aliases)
		if err != nil {
			return "", nil, err
		}
		projections = append(projections, value+" AS "+c.quote(metric.Field))
	}
	where := []string{}
	for _, filter := range node.SourceFilters {
		value, err := sub.sourceFilterExpression(node, filter, aliases)
		if err != nil {
			return "", nil, err
		}
		where = append(where, value)
	}
	if item.GroupByMode != dataset.GroupByModeSets &&
		(c.input.Dialect != MySQL ||
			(item.GroupByMode != dataset.GroupByModeCube &&
				item.GroupByMode != dataset.GroupByModeRollup)) {
		groups = groups[:0]
		for _, expression := range groupExpressions {
			value, err := sub.expression(expression, aliases)
			if err != nil {
				return "", nil, err
			}
			groups = append(groups, value)
		}
	}
	groupClause := strings.Join(groups, ", ")
	switch item.GroupByMode {
	case dataset.GroupByModeCube:
		if c.input.Dialect != MySQL {
			groupClause = "CUBE (" + groupClause + ")"
		}
	case dataset.GroupByModeRollup:
		if c.input.Dialect == MySQL {
			groupClause = strings.Join(groupAliases, ", ") + " WITH ROLLUP"
		} else {
			groupClause = "ROLLUP (" + groupClause + ")"
		}
	case dataset.GroupByModeSets:
		if c.input.Dialect != MySQL {
			expressionsByField := make(map[string]dataset.Expression, len(item.GroupBy))
			for index, group := range item.GroupBy {
				expressionsByField[group.Field] = groupExpressions[index]
			}
			sets := make([]string, 0, len(item.GroupingSets))
			for _, groupingSet := range item.GroupingSets {
				expressions := make([]string, 0, len(groupingSet))
				for _, field := range groupingSet {
					expression, ok := expressionsByField[field]
					if !ok {
						return "", nil, errors.New("preAggregation groupingSets references an unknown groupBy field")
					}
					value, err := sub.expression(expression, aliases)
					if err != nil {
						return "", nil, err
					}
					expressions = append(expressions, value)
				}
				sets = append(sets, "("+strings.Join(expressions, ", ")+")")
			}
			groupClause = "GROUPING SETS (" + strings.Join(sets, ", ") + ")"
		}
	}
	buildSelect := func(branchProjections []string, groupClause string) string {
		sql := "SELECT " + strings.Join(branchProjections, ", ") + " FROM " + c.tableName(ref) + " " + c.quote("pre_source")
		if len(where) > 0 {
			sql += " WHERE " + strings.Join(where, " AND ")
		}
		if groupClause != "" {
			sql += " GROUP BY " + groupClause
		}
		return sql
	}
	if c.input.Dialect == MySQL &&
		(item.GroupByMode == dataset.GroupByModeCube ||
			item.GroupByMode == dataset.GroupByModeSets) {
		dimensions := make([]string, len(item.GroupBy))
		for index, group := range item.GroupBy {
			dimensions[index] = group.Field
		}
		groupingSelections, err := groupingSelectionsForFields(item.GroupByMode, dimensions, item.GroupingSets)
		if err != nil {
			return "", nil, err
		}
		branches := make([]string, 0, len(groupingSelections))
		for _, selected := range groupingSelections {
			branchProjections := append([]string(nil), projections...)
			activeGroups := make([]string, 0, len(groupAliases))
			for index, group := range item.GroupBy {
				if selected[index] {
					activeGroups = append(activeGroups, groupAliases[index])
					continue
				}
				branchProjections[index] = "CASE WHEN 1 = 1 THEN NULL ELSE " + groups[index] +
					" END AS " + c.quote(group.Field)
			}
			branches = append(branches, buildSelect(branchProjections, strings.Join(activeGroups, ", ")))
		}
		templateArgs := append([]any(nil), sub.args...)
		sub.args = make([]any, 0, len(templateArgs)*len(branches))
		for range branches {
			sub.args = append(sub.args, templateArgs...)
		}
		return "(" + strings.Join(branches, " UNION ALL ") + ")", sub.args, nil
	}
	return "(" + buildSelect(projections, groupClause) + ")", sub.args, nil
}

func groupingSelectionsForFields(mode dataset.GroupByMode, dimensions []string, groupingSets [][]string) ([][]bool, error) {
	if mode == dataset.GroupByModeCube {
		selections := make([][]bool, 0, 1<<len(dimensions))
		for mask := (1 << len(dimensions)) - 1; mask >= 0; mask-- {
			selected := make([]bool, len(dimensions))
			for index := range dimensions {
				selected[index] = mask&(1<<index) != 0
			}
			selections = append(selections, selected)
		}
		return selections, nil
	}
	if mode != dataset.GroupByModeSets {
		selected := make([]bool, len(dimensions))
		for index := range selected {
			selected[index] = true
		}
		return [][]bool{selected}, nil
	}
	indexes := make(map[string]int, len(dimensions))
	for index, dimension := range dimensions {
		indexes[dimension] = index
	}
	selections := make([][]bool, 0, len(groupingSets))
	for _, groupingSet := range groupingSets {
		selected := make([]bool, len(dimensions))
		for _, dimension := range groupingSet {
			index, ok := indexes[dimension]
			if !ok {
				return nil, errors.New("groupingSets references an unknown groupBy field")
			}
			selected[index] = true
		}
		selections = append(selections, selected)
	}
	return selections, nil
}

func (c *compiler) sourceFilterExpression(node dataset.Node, filter dataset.SourceFilter, aliases map[string]string) (string, error) {
	if filter.Expression != nil {
		return c.expression(*filter.Expression, aliases)
	}
	ref := c.input.Tables[node.ID]
	if !ref.Columns[filter.Field] {
		return "", errors.New("source filter field is not in the whitelist")
	}
	left := c.quote(aliases[node.ID]) + "." + c.quote(filter.Field)
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

func (c *compiler) minimumGroupSize() int {
	// 多个受限列共享同一个分组结果，因此采用所有生效策略中的最大下限。
	result := 0
	seen := map[string]struct{}{}
	for _, item := range c.input.ColumnPolicies {
		// 列策略已按优先级排序，与投影阶段一致，同一字段只采用第一条规则。
		if _, exists := seen[item.FieldCode]; exists {
			continue
		}
		seen[item.FieldCode] = struct{}{}
		if item.PolicyType == "AGGREGATE_ONLY" && item.MinimumGroupSize > result {
			result = item.MinimumGroupSize
		}
	}
	return result
}

func (c *compiler) expression(expression dataset.Expression, aliases map[string]string) (string, error) {
	// 递归编译器只接受 Validate 定义的节点种类：标识符来自物理白名单并引用，
	// 所有字面量和参数进入 args。任何表达式都不能直接携带 SQL 片段。
	switch expression.Type {
	case "FIELD_REF":
		ref, ok := c.input.Tables[expression.NodeID]
		if !ok || !ref.Columns[expression.Field] && !c.preAggregationProduces(expression.NodeID, expression.Field) {
			return "", errors.New("field is not in the source whitelist")
		}
		return c.quote(aliases[expression.NodeID]) + "." + c.quote(expression.Field), nil
	case "PARAM_REF":
		value, ok := c.input.Parameters[expression.Code]
		if !ok {
			return "", fmt.Errorf("required parameter %s is missing", expression.Code)
		}
		return c.bind(value), nil
	case "LITERAL":
		return c.bind(expression.Value), nil
	case "AGGREGATE":
		argument := "*"
		var err error
		if expression.Argument != nil {
			argument, err = c.expression(*expression.Argument, aliases)
			if err != nil {
				return "", err
			}
		}
		function := expression.Function
		if function == "COUNT_DISTINCT" {
			return "COUNT(DISTINCT " + argument + ")", nil
		}
		if function == "PERIOD_END" || function == "PERIOD_BEGIN" {
			if c.input.Dialect != PostgreSQL || len(expression.OrderBy) != 1 {
				return "", errors.New("ordered period aggregate requires PostgreSQL and one time field")
			}
			order, err := c.expression(expression.OrderBy[0].Expression, aliases)
			if err != nil {
				return "", err
			}
			direction := expression.OrderBy[0].Direction
			expected := "ASC"
			if function == "PERIOD_END" {
				expected = "DESC"
			}
			if direction != expected {
				return "", errors.New("ordered period aggregate direction is invalid")
			}
			return "(ARRAY_AGG(" + argument + " ORDER BY " + order + " " + direction + " NULLS LAST))[1]", nil
		}
		if !oneOf(function, "SUM", "AVG", "MIN", "MAX", "COUNT") {
			return "", errors.New("unsupported aggregate")
		}
		return function + "(" + argument + ")", nil
	case "NULLIF":
		if len(expression.Arguments) != 2 {
			return "", errors.New("NULLIF requires two arguments")
		}
		left, err := c.expression(expression.Arguments[0], aliases)
		if err != nil {
			return "", err
		}
		right, err := c.expression(expression.Arguments[1], aliases)
		if err != nil {
			return "", err
		}
		return "NULLIF(" + left + "," + right + ")", nil
	case "WINDOW":
		if !oneOf(expression.Function, "ROW_NUMBER", "RANK", "DENSE_RANK", "SUM", "AVG", "COUNT", "MIN", "MAX") {
			return "", errors.New("unsupported window function")
		}
		if len(expression.PartitionBy) == 0 || len(expression.OrderBy) == 0 {
			return "", errors.New("window function requires PARTITION BY and ORDER BY")
		}
		partitions := make([]string, 0, len(expression.PartitionBy))
		for _, partition := range expression.PartitionBy {
			value, err := c.expression(partition, aliases)
			if err != nil {
				return "", err
			}
			partitions = append(partitions, value)
		}
		orders := make([]string, 0, len(expression.OrderBy))
		for _, item := range expression.OrderBy {
			value, err := c.expression(item.Expression, aliases)
			if err != nil {
				return "", err
			}
			if !oneOf(item.Direction, "ASC", "DESC") {
				return "", errors.New("unsupported window order direction")
			}
			orders = append(orders, value+" "+item.Direction)
		}
		call := expression.Function + "()"
		frame := ""
		if oneOf(expression.Function, "SUM", "AVG", "COUNT", "MIN", "MAX") {
			if expression.Argument == nil {
				return "", errors.New("aggregate window function requires an argument")
			}
			argument, err := c.expression(*expression.Argument, aliases)
			if err != nil {
				return "", err
			}
			call = expression.Function + "(" + argument + ")"
			// 明确整分区窗口，避免带 ORDER BY 时数据库采用默认“截至当前行”
			// frame 而把组内聚合悄悄变成累计值。
			frame = " ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING"
		}
		return call + " OVER (PARTITION BY " + strings.Join(partitions, ", ") + " ORDER BY " + strings.Join(orders, ", ") + frame + ")", nil
	case "CURRENT_DATE":
		switch c.input.Dialect {
		case Oracle:
			return "TRUNC(CURRENT_DATE)", nil
		case PostgreSQL:
			return "CURRENT_DATE", nil
		default:
			return "CURRENT_DATE()", nil
		}
	case "DATE_DIFF":
		if len(expression.Arguments) != 2 {
			return "", errors.New("DATE_DIFF requires start and end arguments")
		}
		start, err := c.expression(expression.Arguments[0], aliases)
		if err != nil {
			return "", err
		}
		end, err := c.expression(expression.Arguments[1], aliases)
		if err != nil {
			return "", err
		}
		if expression.Unit == "DAY" {
			switch c.input.Dialect {
			case Oracle:
				return "(TRUNC(" + end + ") - TRUNC(" + start + "))", nil
			case PostgreSQL:
				return "(CAST(" + end + " AS DATE) - CAST(" + start + " AS DATE))", nil
			default:
				return "DATEDIFF(" + end + ", " + start + ")", nil
			}
		}
		startYear, err := c.datePartExpression("YEAR", start)
		if err != nil {
			return "", err
		}
		endYear, err := c.datePartExpression("YEAR", end)
		if err != nil {
			return "", err
		}
		if expression.Unit == "YEAR" {
			return "((" + endYear + ") - (" + startYear + "))", nil
		}
		if expression.Unit != "MONTH" {
			return "", errors.New("unsupported date difference unit")
		}
		startMonth, err := c.datePartExpression("MONTH", start)
		if err != nil {
			return "", err
		}
		endMonth, err := c.datePartExpression("MONTH", end)
		if err != nil {
			return "", err
		}
		return "(((" + endYear + ") - (" + startYear + ")) * 12 + ((" + endMonth + ") - (" + startMonth + ")))", nil
	case "DATE_EXTRACT":
		if expression.Argument == nil {
			return "", errors.New("DATE_EXTRACT requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		return c.datePartExpression(expression.Unit, argument)
	case "DATE_START", "DATE_END":
		if expression.Argument == nil {
			return "", errors.New(expression.Type + " requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		if expression.Type == "DATE_START" {
			return c.dateBoundaryStartExpression(expression.Unit, argument)
		}
		return c.dateBoundaryEndExpression(expression.Unit, argument)
	case "DATE_TRUNC":
		if expression.Argument == nil {
			return "", errors.New("DATE_TRUNC requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		if c.input.Dialect == Oracle {
			return "TRUNC(" + argument + ", '" + map[string]string{"DAY": "DD", "WEEK": "IW", "MONTH": "MM", "QUARTER": "Q", "YEAR": "YYYY"}[expression.Unit] + "')", nil
		}
		if c.input.Dialect == PostgreSQL {
			unit := map[string]string{"DAY": "day", "WEEK": "week", "MONTH": "month", "QUARTER": "quarter", "YEAR": "year"}[expression.Unit]
			if unit == "" {
				return "", errors.New("unsupported date truncation unit")
			}
			return "DATE_TRUNC('" + unit + "', " + argument + ")", nil
		}
		if expression.Unit == "QUARTER" {
			return "STR_TO_DATE(CONCAT(YEAR(" + argument + "),'-',LPAD((QUARTER(" + argument + ")-1)*3+1,2,'0'),'-01'),'%%Y-%%m-%%d')", nil
		}
		format := map[string]string{"DAY": "%%Y-%%m-%%d", "WEEK": "%%x-%%v", "MONTH": "%%Y-%%m-01"}[expression.Unit]
		if expression.Unit == "YEAR" {
			format = "%%Y-01-01"
		}
		if format == "" {
			return "", errors.New("unsupported date truncation unit")
		}
		return "DATE_FORMAT(" + argument + ", '" + format + "')", nil
	case "DATE_FORMAT":
		if expression.Argument == nil {
			return "", errors.New("DATE_FORMAT requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		if c.input.Dialect == Oracle || c.input.Dialect == PostgreSQL {
			format := map[string]string{"YEAR": "YYYY", "MONTH": "YYYYMM", "DAY": "YYYYMMDD"}[expression.Unit]
			if format != "" {
				return "TO_CHAR(" + argument + ", '" + format + "')", nil
			}
			if expression.Unit == "QUARTER" {
				return "TO_CHAR(" + argument + ", 'YYYY') || 'Q' || TO_CHAR(" + argument + ", 'Q')", nil
			}
			return "", errors.New("unsupported date format unit")
		}
		format := map[string]string{"YEAR": "%%Y", "MONTH": "%%Y%%m", "DAY": "%%Y%%m%%d"}[expression.Unit]
		if format != "" {
			return "DATE_FORMAT(" + argument + ", '" + format + "')", nil
		}
		if expression.Unit == "QUARTER" {
			return "CONCAT(DATE_FORMAT(" + argument + ", '%%Y'), 'Q', QUARTER(" + argument + "))", nil
		}
		return "", errors.New("unsupported date format unit")
	case "CAST":
		if expression.Argument == nil {
			return "", errors.New("CAST requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		types := map[Dialect]map[string]string{
			MySQL:      {"STRING": "CHAR", "INTEGER": "SIGNED", "DECIMAL": "DECIMAL(38,10)", "BOOLEAN": "UNSIGNED", "DATE": "DATE", "DATETIME": "DATETIME"},
			Oracle:     {"STRING": "VARCHAR2(4000)", "INTEGER": "NUMBER(38)", "DECIMAL": "NUMBER", "BOOLEAN": "NUMBER(1)", "DATE": "DATE", "DATETIME": "TIMESTAMP"},
			PostgreSQL: {"STRING": "TEXT", "INTEGER": "BIGINT", "DECIMAL": "NUMERIC(38,10)", "BOOLEAN": "BOOLEAN", "DATE": "DATE", "DATETIME": "TIMESTAMP"},
		}
		target := types[c.input.Dialect][expression.TargetType]
		if target == "" {
			return "", errors.New("unsupported CAST target")
		}
		return "CAST(" + argument + " AS " + target + ")", nil
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
		if len(expression.Arguments) < 2 {
			return "", errors.New("arithmetic expression requires two arguments")
		}
		parts := []string{}
		for _, arg := range expression.Arguments {
			value, err := c.expression(arg, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		return "(" + strings.Join(parts, " "+map[string]string{"ADD": "+", "SUBTRACT": "-", "MULTIPLY": "*", "DIVIDE": "/"}[expression.Type]+" ") + ")", nil
	case "ABS", "FLOOR", "CEIL":
		if expression.Argument == nil {
			return "", errors.New(expression.Type + " requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		return expression.Type + "(" + argument + ")", nil
	case "ROUND":
		if len(expression.Arguments) != 2 {
			return "", errors.New("ROUND requires a value and precision")
		}
		parts := make([]string, 0, 2)
		for _, argument := range expression.Arguments {
			value, err := c.expression(argument, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		return "ROUND(" + strings.Join(parts, ",") + ")", nil
	case "CONCAT", "COALESCE":
		parts := []string{}
		for _, arg := range expression.Arguments {
			value, err := c.expression(arg, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		if expression.Type == "COALESCE" {
			return "COALESCE(" + strings.Join(parts, ",") + ")", nil
		}
		if c.input.Dialect == MySQL {
			return "CONCAT(" + strings.Join(parts, ",") + ")", nil
		}
		return "(" + strings.Join(parts, " || ") + ")", nil
	case "TRIM", "UPPER", "LOWER":
		if expression.Argument == nil {
			return "", errors.New(expression.Type + " requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		return expression.Type + "(" + argument + ")", nil
	case "SUBSTRING":
		if len(expression.Arguments) != 3 {
			return "", errors.New("SUBSTRING requires text, start and length")
		}
		parts := make([]string, 0, 3)
		for _, argument := range expression.Arguments {
			value, err := c.expression(argument, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		function := "SUBSTRING"
		if c.input.Dialect == Oracle {
			function = "SUBSTR"
		}
		return function + "(" + strings.Join(parts, ",") + ")", nil
	case "REPLACE":
		if len(expression.Arguments) != 3 {
			return "", errors.New("REPLACE requires text, search and replacement")
		}
		parts := make([]string, 0, 3)
		for _, argument := range expression.Arguments {
			value, err := c.expression(argument, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		return "REPLACE(" + strings.Join(parts, ",") + ")", nil
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "LIKE", "CONTAINS", "NOT_CONTAINS", "IN", "NOT_IN":
		if expression.Left == nil || expression.Right == nil {
			return "", errors.New("comparison requires both operands")
		}
		left, err := c.expression(*expression.Left, aliases)
		if err != nil {
			return "", err
		}
		if expression.Type == "IN" || expression.Type == "NOT_IN" {
			if expression.Right.Type == "ARRAY" {
				if len(expression.Right.Arguments) == 0 {
					return "", errors.New("IN array cannot be empty")
				}
				values := make([]string, 0, len(expression.Right.Arguments))
				for _, item := range expression.Right.Arguments {
					compiled, err := c.expression(item, aliases)
					if err != nil {
						return "", err
					}
					values = append(values, compiled)
				}
				op, _ := comparison(expression.Type)
				return "(" + left + " " + op + " (" + strings.Join(values, ",") + "))", nil
			}
			var raw any
			switch expression.Right.Type {
			case "PARAM_REF":
				var ok bool
				raw, ok = c.input.Parameters[expression.Right.Code]
				if !ok {
					return "", errors.New("IN parameter is missing")
				}
			case "LITERAL":
				raw = expression.Right.Value
			default:
				return "", errors.New("IN requires a literal or parameter collection")
			}
			values, err := c.bindCollection(raw)
			if err != nil {
				return "", err
			}
			op, _ := comparison(expression.Type)
			return "(" + left + " " + op + " (" + values + "))", nil
		}
		right, err := c.expression(*expression.Right, aliases)
		if err != nil {
			return "", err
		}
		if expression.Type == "CONTAINS" {
			if c.input.Dialect == PostgreSQL {
				return "(STRPOS(" + left + "," + right + ") > 0)", nil
			}
			return "(INSTR(" + left + "," + right + ") > 0)", nil
		}
		if expression.Type == "NOT_CONTAINS" {
			if c.input.Dialect == PostgreSQL {
				return "(STRPOS(" + left + "," + right + ") = 0)", nil
			}
			return "(INSTR(" + left + "," + right + ") = 0)", nil
		}
		op, err := comparison(expression.Type)
		if err != nil {
			return "", err
		}
		return "(" + left + " " + op + " " + right + ")", nil
	case "BETWEEN":
		if expression.Left == nil || expression.Lower == nil || expression.Upper == nil {
			return "", errors.New("BETWEEN requires value and bounds")
		}
		left, err := c.expression(*expression.Left, aliases)
		if err != nil {
			return "", err
		}
		lower, err := c.expression(*expression.Lower, aliases)
		if err != nil {
			return "", err
		}
		upper, err := c.expression(*expression.Upper, aliases)
		if err != nil {
			return "", err
		}
		return "(" + left + " BETWEEN " + lower + " AND " + upper + ")", nil
	case "IS_NULL", "IS_NOT_NULL", "NOT":
		if expression.Argument == nil {
			return "", errors.New(expression.Type + " requires an argument")
		}
		argument, err := c.expression(*expression.Argument, aliases)
		if err != nil {
			return "", err
		}
		if expression.Type == "NOT" {
			return "(NOT " + argument + ")", nil
		}
		return "(" + argument + " " + strings.ReplaceAll(expression.Type, "_", " ") + ")", nil
	case "AND", "OR":
		if len(expression.Arguments) < 2 {
			return "", errors.New("logical expression requires two arguments")
		}
		parts := []string{}
		for _, arg := range expression.Arguments {
			value, err := c.expression(arg, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		return "(" + strings.Join(parts, " "+expression.Type+" ") + ")", nil
	case "CASE":
		parts := make([]string, 0, len(expression.Whens)+1)
		for _, branch := range expression.Whens {
			when, err := c.expression(branch.When, aliases)
			if err != nil {
				return "", err
			}
			then, err := c.expression(branch.Then, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, "WHEN "+when+" THEN "+then)
		}
		if expression.Else != nil {
			value, err := c.expression(*expression.Else, aliases)
			if err != nil {
				return "", err
			}
			parts = append(parts, "ELSE "+value)
		}
		return "(CASE " + strings.Join(parts, " ") + " END)", nil
	default:
		return "", fmt.Errorf("expression %s is not supported by the secure compiler", expression.Type)
	}
}

func (c *compiler) datePartExpression(unit, argument string) (string, error) {
	switch c.input.Dialect {
	case Oracle:
		switch unit {
		case "YEAR", "MONTH", "DAY":
			return "EXTRACT(" + unit + " FROM " + argument + ")", nil
		case "QUARTER":
			return "TO_NUMBER(TO_CHAR(" + argument + ", 'Q'))", nil
		case "WEEK":
			return "TO_NUMBER(TO_CHAR(" + argument + ", 'IW'))", nil
		case "WEEKDAY":
			return "(TRUNC(" + argument + ") - TRUNC(" + argument + ", 'IW') + 1)", nil
		case "DAY_OF_YEAR":
			return "TO_NUMBER(TO_CHAR(" + argument + ", 'DDD'))", nil
		}
	case PostgreSQL:
		part := map[string]string{
			"YEAR": "YEAR", "QUARTER": "QUARTER", "MONTH": "MONTH", "WEEK": "WEEK",
			"DAY": "DAY", "WEEKDAY": "ISODOW", "DAY_OF_YEAR": "DOY",
		}[unit]
		if part != "" {
			return "EXTRACT(" + part + " FROM " + argument + ")", nil
		}
	default:
		function := map[string]string{
			"YEAR": "YEAR", "QUARTER": "QUARTER", "MONTH": "MONTH", "DAY": "DAY", "DAY_OF_YEAR": "DAYOFYEAR",
		}[unit]
		if function != "" {
			return function + "(" + argument + ")", nil
		}
		if unit == "WEEK" {
			return "WEEK(" + argument + ", 3)", nil
		}
		if unit == "WEEKDAY" {
			return "(WEEKDAY(" + argument + ") + 1)", nil
		}
	}
	return "", errors.New("unsupported date extraction unit")
}

func (c *compiler) dateBoundaryStartExpression(unit, argument string) (string, error) {
	switch c.input.Dialect {
	case Oracle:
		format := map[string]string{"WEEK": "IW", "MONTH": "MM", "QUARTER": "Q", "YEAR": "YYYY"}[unit]
		if format != "" {
			return "TRUNC(" + argument + ", '" + format + "')", nil
		}
	case PostgreSQL:
		part := map[string]string{"WEEK": "week", "MONTH": "month", "QUARTER": "quarter", "YEAR": "year"}[unit]
		if part != "" {
			return "CAST(DATE_TRUNC('" + part + "', " + argument + ") AS DATE)", nil
		}
	default:
		switch unit {
		case "WEEK":
			return "DATE_SUB(DATE(" + argument + "), INTERVAL WEEKDAY(" + argument + ") DAY)", nil
		case "MONTH":
			return "DATE_SUB(DATE(" + argument + "), INTERVAL (DAYOFMONTH(" + argument + ") - 1) DAY)", nil
		case "QUARTER":
			return "DATE_ADD(MAKEDATE(YEAR(" + argument + "), 1), INTERVAL ((QUARTER(" + argument + ") - 1) * 3) MONTH)", nil
		case "YEAR":
			return "MAKEDATE(YEAR(" + argument + "), 1)", nil
		}
	}
	return "", errors.New("unsupported date boundary unit")
}

func (c *compiler) dateBoundaryEndExpression(unit, argument string) (string, error) {
	switch c.input.Dialect {
	case Oracle:
		switch unit {
		case "WEEK":
			return "(TRUNC(" + argument + ", 'IW') + 6)", nil
		case "MONTH":
			return "(ADD_MONTHS(TRUNC(" + argument + ", 'MM'), 1) - 1)", nil
		case "QUARTER":
			return "(ADD_MONTHS(TRUNC(" + argument + ", 'Q'), 3) - 1)", nil
		case "YEAR":
			return "(ADD_MONTHS(TRUNC(" + argument + ", 'YYYY'), 12) - 1)", nil
		}
	case PostgreSQL:
		part := map[string]string{"WEEK": "week", "MONTH": "month", "QUARTER": "quarter", "YEAR": "year"}[unit]
		interval := map[string]string{"WEEK": "1 week", "MONTH": "1 month", "QUARTER": "3 months", "YEAR": "1 year"}[unit]
		if part != "" {
			return "CAST(DATE_TRUNC('" + part + "', " + argument + ") + INTERVAL '" + interval + "' - INTERVAL '1 day' AS DATE)", nil
		}
	default:
		switch unit {
		case "WEEK":
			return "DATE_ADD(DATE_SUB(DATE(" + argument + "), INTERVAL WEEKDAY(" + argument + ") DAY), INTERVAL 6 DAY)", nil
		case "MONTH":
			return "LAST_DAY(" + argument + ")", nil
		case "QUARTER":
			return "LAST_DAY(DATE_ADD(MAKEDATE(YEAR(" + argument + "), 1), INTERVAL (QUARTER(" + argument + ") * 3 - 1) MONTH))", nil
		case "YEAR":
			return "DATE(CONCAT(YEAR(" + argument + "), '-12-31'))", nil
		}
	}
	return "", errors.New("unsupported date boundary unit")
}

func (c *compiler) compileRows() (string, error) {
	if len(c.input.RowPolicies) == 0 {
		return "", nil
	}
	allowed := map[string]bool{}
	for _, field := range c.input.Document.Fields {
		allowed[field.Code] = true
	}
	policies := append([]policy.RowPolicy(nil), c.input.RowPolicies...)
	sort.SliceStable(policies, func(i, j int) bool { return policies[i].Priority < policies[j].Priority })
	allowAND, allowOR, denies := []string{}, []string{}, []string{}
	for _, item := range policies {
		value, err := c.policyExpression(item.Expression, allowed)
		if err != nil {
			return "", err
		}
		if item.Effect == "DENY" || item.CombineMode == "DENY_OVERRIDE" {
			denies = append(denies, value)
		} else if item.CombineMode == "AND" {
			allowAND = append(allowAND, value)
		} else if item.CombineMode == "OR" {
			allowOR = append(allowOR, value)
		} else {
			return "", fmt.Errorf("row policy %s uses an unsupported combine mode", item.ID)
		}
	}
	// 最终语义为：全部 AND 规则命中、OR 规则至少一条命中，并且没有 DENY 命中。
	// DENY 独立取反后再相交，确保它不会被任何允许规则覆盖。
	parts := []string{}
	if len(allowAND) > 0 {
		parts = append(parts, "("+strings.Join(allowAND, " AND ")+")")
	}
	if len(allowOR) > 0 {
		parts = append(parts, "("+strings.Join(allowOR, " OR ")+")")
	}
	if len(denies) > 0 {
		parts = append(parts, "NOT ("+strings.Join(denies, " OR ")+")")
	}
	return strings.Join(parts, " AND "), nil
}

func (c *compiler) policyExpression(expression policy.Expression, allowed map[string]bool) (string, error) {
	// 行策略运行在 secure_base 外层，只允许引用数据集输出字段和当前用户属性。
	// 缺失属性直接报错而不是当作 NULL，避免配置错误意外放宽访问范围。
	switch expression.Type {
	case "FIELD_REF":
		if !allowed[expression.FieldCode] {
			return "", errors.New("row policy references a non-output field")
		}
		return c.quote("secure_base") + "." + c.quote(expression.FieldCode), nil
	case "USER_ATTRIBUTE_REF":
		value, ok := c.input.Scope.Attributes[expression.Attribute]
		if !ok {
			return "", errors.New("row policy user attribute is missing")
		}
		return c.bind(value), nil
	case "LITERAL":
		return c.bind(expression.Value), nil
	case "EQUALS", "NOT_EQUALS", "IN":
		if expression.Left == nil || expression.Right == nil {
			return "", errors.New("invalid row policy")
		}
		left, err := c.policyExpression(*expression.Left, allowed)
		if err != nil {
			return "", err
		}
		if expression.Type == "IN" {
			var raw any
			switch expression.Right.Type {
			case "LITERAL":
				raw = expression.Right.Value
			case "USER_ATTRIBUTE_REF":
				raw = c.input.Scope.Attributes[expression.Right.Attribute]
			default:
				return "", errors.New("row policy IN requires a collection")
			}
			values, err := c.bindCollection(raw)
			if err != nil {
				return "", err
			}
			return "(" + left + " IN (" + values + "))", nil
		}
		right, err := c.policyExpression(*expression.Right, allowed)
		if err != nil {
			return "", err
		}
		return "(" + left + " " + map[string]string{"EQUALS": "=", "NOT_EQUALS": "<>"}[expression.Type] + " " + right + ")", nil
	case "AND", "OR":
		parts := []string{}
		for _, child := range expression.Children {
			value, err := c.policyExpression(child, allowed)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
		if len(parts) < 2 {
			return "", errors.New("invalid logical row policy")
		}
		return "(" + strings.Join(parts, " "+expression.Type+" ") + ")", nil
	default:
		return "", errors.New("unsupported row policy expression")
	}
}

func (c *compiler) compileColumns() ([]string, error) {
	// PolicyStore 按优先级返回规则；同一字段只采用第一条，保持与行策略及文件
	// 执行器一致。脱敏在最外层投影执行，排序也只能基于脱敏后的输出别名。
	policies := map[string]policy.ColumnPolicy{}
	for _, item := range c.input.ColumnPolicies {
		if _, exists := policies[item.FieldCode]; !exists {
			policies[item.FieldCode] = item
		}
	}
	out := []string{}
	for _, field := range c.input.Document.Fields {
		base := c.quote("secure_base") + "." + c.quote(field.Code)
		projection := base
		if item, ok := policies[field.Code]; ok {
			switch item.PolicyType {
			case "ALLOW":
			case "DENY":
				return nil, fmt.Errorf("column %s is denied", field.Code)
			case "NULLIFY":
				projection = "NULL"
			case "HASH":
				if c.input.Dialect == MySQL {
					projection = "SHA2(CAST(" + base + " AS CHAR), 256)"
				} else if c.input.Dialect == PostgreSQL {
					projection = "ENCODE(DIGEST(CAST(" + base + " AS TEXT), 'sha256'), 'hex')"
				} else {
					projection = "STANDARD_HASH(TO_CHAR(" + base + "), 'SHA256')"
				}
			case "MASK":
				if item.MaskRule.Type != "KEEP_PREFIX_SUFFIX" || item.MaskRule.PrefixLength < 0 || item.MaskRule.SuffixLength < 0 {
					return nil, fmt.Errorf("column %s has an invalid mask rule", field.Code)
				}
				if item.MaskRule.MaskChar != "" && !safeMaskCharacter.MatchString(item.MaskRule.MaskChar) {
					return nil, fmt.Errorf("column %s has an unsafe mask character", field.Code)
				}
				projection = c.mask(base, item.MaskRule)
			case "AGGREGATE_ONLY":
				aggregation := field.Expression.Function
				allowed := false
				for _, value := range item.AllowedAggregations {
					allowed = allowed || strings.EqualFold(value, aggregation)
				}
				if aggregation == "" || !allowed {
					return nil, fmt.Errorf("column %s requires an allowed aggregation", field.Code)
				}
			default:
				return nil, errors.New("unsupported column policy")
			}
		}
		out = append(out, projection+" AS "+c.quote(field.Code))
	}
	return out, nil
}

func (c *compiler) compileSorts() (string, error) {
	// Oracle 原生支持 NULLS FIRST/LAST；MySQL 通过额外的 IS NULL 排序键模拟，
	// 从而让两种方言在同一 DSL 下得到一致的空值顺序。
	fields := map[string]string{}
	for _, field := range c.input.Document.Fields {
		fields[field.ID] = field.Code
	}
	parts := make([]string, 0, len(c.input.Document.Sorts)*2)
	for _, item := range c.input.Document.Sorts {
		code, ok := fields[item.FieldID]
		if !ok {
			return "", errors.New("sort references an unknown field")
		}
		quoted := c.quote(code)
		if c.input.Dialect == MySQL && item.Nulls != "" {
			nullDirection := "ASC"
			if item.Nulls == "FIRST" {
				nullDirection = "DESC"
			}
			parts = append(parts, "("+quoted+" IS NULL) "+nullDirection)
		}
		part := quoted + " " + item.Direction
		if (c.input.Dialect == Oracle || c.input.Dialect == PostgreSQL) && item.Nulls != "" {
			part += " NULLS " + item.Nulls
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", "), nil
}

func (c *compiler) mask(base string, rule policy.MaskRule) string {
	maskChar := rule.MaskChar
	if maskChar == "" {
		maskChar = "*"
	}
	if c.input.Dialect == MySQL {
		return fmt.Sprintf("CONCAT(LEFT(%s,%d),REPEAT('%s',GREATEST(CHAR_LENGTH(%s)-%d,0)),RIGHT(%s,%d))", base, rule.PrefixLength, strings.ReplaceAll(maskChar, "'", "''"), base, rule.PrefixLength+rule.SuffixLength, base, rule.SuffixLength)
	}
	if c.input.Dialect == PostgreSQL {
		return fmt.Sprintf("CONCAT(LEFT(CAST(%s AS TEXT),%d),REPEAT('%s',GREATEST(LENGTH(CAST(%s AS TEXT))-%d,0)),RIGHT(CAST(%s AS TEXT),%d))", base, rule.PrefixLength, strings.ReplaceAll(maskChar, "'", "''"), base, rule.PrefixLength+rule.SuffixLength, base, rule.SuffixLength)
	}
	return fmt.Sprintf("SUBSTR(%s,1,%d)||RPAD('%s',GREATEST(LENGTH(%s)-%d,0),'%s')||SUBSTR(%s,-%d)", base, rule.PrefixLength, strings.ReplaceAll(maskChar, "'", "''"), base, rule.PrefixLength+rule.SuffixLength, strings.ReplaceAll(maskChar, "'", "''"), base, rule.SuffixLength)
}

// bind 记录绑定值并返回当前方言的占位符。占位符次序必须与递归遍历次序一致。
func (c *compiler) bind(value any) string {
	c.args = append(c.args, value)
	if c.input.Dialect == Oracle {
		return fmt.Sprintf(":%d", len(c.args))
	}
	if c.input.Dialect == PostgreSQL {
		return fmt.Sprintf("$%d", len(c.args))
	}
	return "%s"
}

// bindCollection 将 IN 集合展开为独立占位符，并限制长度以控制 SQL 大小和驱动负载。
func (c *compiler) bindCollection(value any) (string, error) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) || reflected.Len() < 1 || reflected.Len() > 1000 {
		return "", errors.New("collection parameter must contain 1 to 1000 values")
	}
	values := make([]string, reflected.Len())
	for i := 0; i < reflected.Len(); i++ {
		values[i] = c.bind(reflected.Index(i).Interface())
	}
	return strings.Join(values, ","), nil
}

// quote 只负责方言引用形式和 Oracle 大写折叠；调用前标识符必须已经过白名单校验。
func (c *compiler) quote(value string) string {
	if c.input.Dialect == MySQL {
		return "`" + value + "`"
	}
	if c.input.Dialect == PostgreSQL || c.input.Dialect == SQLServer {
		return `"` + value + `"`
	}
	return `"` + strings.ToUpper(value) + `"`
}

// tableName 组合已校验的 schema 与表名，不接受 DSL 或用户输入的任意字符串。
func (c *compiler) tableName(ref TableRef) string {
	if ref.Schema == "" {
		return c.quote(ref.Name)
	}
	return c.quote(ref.Schema) + "." + c.quote(ref.Name)
}

// validateTable 在任何物理名称进入 SQL 前执行最后一次标识符校验。
func (c *compiler) validateTable(ref TableRef) error {
	if !safeIdentifier.MatchString(ref.Name) || (ref.Schema != "" && !safeIdentifier.MatchString(ref.Schema)) {
		return errors.New("physical table identifier is invalid")
	}
	if c.input.Dialect == PostgreSQL && (len(ref.Name) > 63 || len(ref.Schema) > 63) {
		return errors.New("PostgreSQL physical identifiers cannot exceed 63 bytes")
	}
	for name := range ref.Columns {
		if !physicalname.ValidColumn(name) {
			return errors.New("physical column identifier is invalid")
		}
		if c.input.Dialect == PostgreSQL && len(name) > 63 {
			return errors.New("PostgreSQL physical identifiers cannot exceed 63 bytes")
		}
	}
	return nil
}

// PostgreSQL silently truncates identifiers after 63 bytes. Rejecting aliases
// and output names before compilation prevents two distinct DSL identifiers
// from collapsing to the same physical name (especially with multibyte text).
func validatePostgreSQLDocumentIdentifiers(document dataset.Document) error {
	for _, node := range document.Nodes {
		if len(node.Alias) > 63 {
			return errors.New("PostgreSQL node aliases cannot exceed 63 bytes")
		}
		for _, field := range node.Projection {
			if len(field) > 63 {
				return errors.New("PostgreSQL projected identifiers cannot exceed 63 bytes")
			}
		}
	}
	for _, field := range document.Fields {
		if len(field.Code) > 63 {
			return errors.New("PostgreSQL output identifiers cannot exceed 63 bytes")
		}
	}
	return nil
}

// comparison 把 DSL 枚举映射为固定 SQL 令牌，避免操作符通过字符串透传。
func comparison(value string) (string, error) {
	operators := map[string]string{"EQUALS": "=", "NOT_EQUALS": "<>", "GT": ">", "GTE": ">=", "LT": "<", "LTE": "<=", "LIKE": "LIKE", "IN": "IN", "NOT_IN": "NOT IN"}
	result, ok := operators[value]
	if !ok {
		return "", errors.New("unsupported comparison")
	}
	return result, nil
}
func oneOf(value string, values ...string) bool {
	for _, item := range values {
		if value == item {
			return true
		}
	}
	return false
}

func hasNilParameter(expression dataset.Expression, parameters map[string]any) bool {
	// optional 过滤器只要任一深层参数为空就整体跳过；递归遍历必须覆盖 CASE
	// 和所有一元、二元、列表子表达式，不能只检查过滤器根节点。
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
	for _, child := range expression.PartitionBy {
		if hasNilParameter(child, parameters) {
			return true
		}
	}
	for _, item := range expression.OrderBy {
		if hasNilParameter(item.Expression, parameters) {
			return true
		}
	}
	return false
}
