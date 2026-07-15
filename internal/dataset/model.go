package dataset

import (
	"encoding/json"
	"errors"
)

const DSLVersion = "1.0"

var (
	ErrNotFound           = errors.New("dataset not found")
	ErrConflict           = errors.New("dataset version conflict")
	ErrAlreadyExists      = errors.New("dataset code already exists")
	ErrInvalidDocument    = errors.New("dataset document is invalid")
	ErrPreviewInvalid     = errors.New("dataset preview request is invalid")
	ErrPreviewFailed      = errors.New("dataset preview failed")
	ErrPreviewTimeout     = errors.New("dataset preview timed out")
	ErrPreviewUnsupported = errors.New("dataset preview source is unsupported")
	ErrQueryNotFound      = errors.New("query run not found")
	ErrQueryConflict      = errors.New("query run already exists")
)

// Document 是数据集 DSL V1 的完整、可版本化定义。
type Document struct {
	DSLVersion      string          `json:"dslVersion"`
	Dataset         Descriptor      `json:"dataset"`
	Nodes           []Node          `json:"nodes"`
	Joins           []Join          `json:"joins"`
	Fields          []Field         `json:"fields"`
	Filters         []Filter        `json:"filters"`
	GroupBy         []string        `json:"groupBy"`
	Having          []Filter        `json:"having"`
	Sorts           []Sort          `json:"sorts"`
	Parameters      []Parameter     `json:"parameters"`
	OutputGrain     OutputGrain     `json:"outputGrain"`
	ExecutionPolicy ExecutionPolicy `json:"executionPolicy"`
}

// Descriptor 保存 DSL 内可移植的数据集基本信息。
type Descriptor struct {
	Code        string       `json:"code"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Type        string       `json:"type"`
	Grain       *OutputGrain `json:"grain,omitempty"`
}

// Node 描述物理表、已发布数据集或只读 SQL 节点。
type Node struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	DataSourceID     string         `json:"datasourceId,omitempty"`
	TableID          string         `json:"tableId,omitempty"`
	DatasetVersionID string         `json:"datasetVersionId,omitempty"`
	FileVersionID    string         `json:"fileVersionId,omitempty"`
	Alias            string         `json:"alias"`
	Projection       []string       `json:"projection"`
	SourceFilters    []SourceFilter `json:"sourceFilters"`
}

// SourceFilter 描述可安全下推到单个节点的简单过滤条件。
type SourceFilter struct {
	Field      string      `json:"field,omitempty"`
	Operator   string      `json:"operator,omitempty"`
	Value      any         `json:"value,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
}

// Join 描述两个节点之间的关联及声明基数。
type Join struct {
	ID              string          `json:"id"`
	LeftNodeID      string          `json:"leftNodeId"`
	RightNodeID     string          `json:"rightNodeId"`
	JoinType        string          `json:"joinType"`
	Cardinality     string          `json:"cardinality"`
	Conditions      []JoinCondition `json:"conditions"`
	ManualConfirmed bool            `json:"manualConfirmed"`
}

// JoinCondition 保存 Join 两侧表达式，禁止保存拼接后的 SQL。
type JoinCondition struct {
	LeftExpression  Expression `json:"leftExpression"`
	Operator        string     `json:"operator"`
	RightExpression Expression `json:"rightExpression"`
}

// Field 描述数据集输出字段及其语义角色。
type Field struct {
	ID            string     `json:"id"`
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Description   string     `json:"description,omitempty"`
	Role          string     `json:"role"`
	Expression    Expression `json:"expression"`
	CanonicalType string     `json:"canonicalType"`
	SemanticType  string     `json:"semanticType,omitempty"`
	Aggregation   string     `json:"aggregation,omitempty"`
	Format        string     `json:"format,omitempty"`
	Unit          string     `json:"unit,omitempty"`
	Nullable      bool       `json:"nullable"`
	Visible       *bool      `json:"visible,omitempty"`
}

// Filter 描述聚合前或聚合后的布尔表达式。
type Filter struct {
	ID         string     `json:"id"`
	Stage      string     `json:"stage"`
	Optional   bool       `json:"optional"`
	Expression Expression `json:"expression"`
}

// Sort 描述结果字段的稳定排序方向和空值位置。
type Sort struct {
	FieldID   string `json:"fieldId"`
	Direction string `json:"direction"`
	Nulls     string `json:"nulls,omitempty"`
}

// Parameter 描述运行时参数，不包含任何拼接 SQL 的能力。
type Parameter struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DataType     string `json:"dataType"`
	MultiValue   bool   `json:"multiValue"`
	Required     bool   `json:"required"`
	DefaultValue any    `json:"defaultValue,omitempty"`
}

// OutputGrain 明确数据集每一行所代表的业务粒度。
type OutputGrain struct {
	Description      string   `json:"description"`
	KeyFields        []string `json:"keyFields"`
	TimeField        string   `json:"timeField,omitempty"`
	DefaultTimeGrain string   `json:"defaultTimeGrain,omitempty"`
}

// ExecutionPolicy 保存与 SQL 方言无关的执行限额。
type ExecutionPolicy struct {
	Mode            string                `json:"mode"`
	TimeoutMS       int                   `json:"timeoutMs"`
	PreviewLimit    int                   `json:"previewLimit"`
	ResultLimit     int                   `json:"resultLimit"`
	CacheTTLSeconds int                   `json:"cacheTtlSeconds"`
	Materialization MaterializationPolicy `json:"materialization"`
}

// MaterializationPolicy 保存后续物化执行器需要的声明信息。
type MaterializationPolicy struct {
	Enabled     bool   `json:"enabled"`
	RefreshMode string `json:"refreshMode,omitempty"`
	Cron        string `json:"cron,omitempty"`
}

// Expression 是受白名单约束的递归表达式树。
type Expression struct {
	Type       string       `json:"type"`
	NodeID     string       `json:"nodeId,omitempty"`
	Field      string       `json:"field,omitempty"`
	Code       string       `json:"code,omitempty"`
	Function   string       `json:"function,omitempty"`
	Unit       string       `json:"unit,omitempty"`
	TargetType string       `json:"targetType,omitempty"`
	Value      any          `json:"value,omitempty"`
	Argument   *Expression  `json:"argument,omitempty"`
	Arguments  []Expression `json:"arguments,omitempty"`
	Left       *Expression  `json:"left,omitempty"`
	Right      *Expression  `json:"right,omitempty"`
	Lower      *Expression  `json:"lower,omitempty"`
	Upper      *Expression  `json:"upper,omitempty"`
	Whens      []CaseBranch `json:"whens,omitempty"`
	Else       *Expression  `json:"else,omitempty"`
}

// CaseBranch 描述 CASE 表达式的一条条件和返回值分支。
type CaseBranch struct {
	When Expression `json:"when"`
	Then Expression `json:"then"`
}

// ValidationIssue 提供可直接定位到 DSL 字段的校验错误。
type ValidationIssue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ValidationError 聚合全部可发现的 DSL 错误，便于设计器一次展示。
type ValidationError struct {
	Issues []ValidationIssue `json:"details"`
}

func (e *ValidationError) Error() string { return "dataset DSL validation failed" }

// Prepared 是完成迁移、规范化、校验和逻辑计划派生后的保存对象。
type Prepared struct {
	Document        Document
	DSLJSON         json.RawMessage
	DSLHash         string
	LogicalPlan     LogicalPlan
	LogicalPlanJSON json.RawMessage
	PlanHash        string
}

// LogicalPlan 是可确定性再生成、但不包含具体 SQL 的逻辑计划。
type LogicalPlan struct {
	DSLVersion     string      `json:"dslVersion"`
	Steps          []PlanStep  `json:"steps"`
	OutputFields   []string    `json:"outputFields"`
	ParameterCodes []string    `json:"parameterCodes"`
	OutputGrain    OutputGrain `json:"outputGrain"`
}

// PlanStep 描述逻辑计划中的扫描、关联、过滤、聚合和排序步骤。
type PlanStep struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Inputs []string `json:"inputs,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// Record 是 API 返回的数据集及当前草稿快照。
type Record struct {
	ID             string          `json:"id"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Version        int64           `json:"version"`
	DraftVersionID string          `json:"draftVersionId"`
	DSLVersion     string          `json:"dslVersion"`
	DSLHash        string          `json:"dslHash"`
	PlanHash       string          `json:"planHash"`
	DSL            json.RawMessage `json:"dsl"`
	LogicalPlan    json.RawMessage `json:"logicalPlan"`
	CreatedAt      string          `json:"createdAt"`
	UpdatedAt      string          `json:"updatedAt"`
}

// Summary 是数据集目录使用的轻量摘要，不返回完整 DSL。
type Summary struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Version     int64  `json:"version"`
	DSLHash     string `json:"dslHash"`
	UpdatedAt   string `json:"updatedAt"`
}

// CreateInput 是创建数据集草稿的请求。
type CreateInput struct {
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	DSL         json.RawMessage `json:"dsl"`
}

// UpdateInput 是带乐观锁的数据集草稿更新请求。
type UpdateInput struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ExpectedVersion int64           `json:"expectedVersion"`
	DSL             json.RawMessage `json:"dsl"`
}

// PreviewInput 包含受 DSL 定义约束的参数、行数上限和客户端预生成查询标识。
type PreviewInput struct {
	QueryID    string         `json:"queryId,omitempty"`
	Parameters map[string]any `json:"parameters"`
	MaxRows    int            `json:"maxRows,omitempty"`
}

// PreviewResult 返回小样本数据和运行摘要，不暴露生成 SQL。
type PreviewResult struct {
	QueryID    string           `json:"queryId"`
	Columns    []string         `json:"columns"`
	Rows       [][]any          `json:"rows"`
	RowCount   int              `json:"rowCount"`
	DurationMS int64            `json:"durationMs"`
	Warnings   []PreviewWarning `json:"warnings,omitempty"`
}

// PreviewWarning 向设计器返回不含源数据值的 Join 语义与性能风险。
type PreviewWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	JoinID        string `json:"joinId,omitempty"`
	EstimatedRows int    `json:"estimatedRows,omitempty"`
}
