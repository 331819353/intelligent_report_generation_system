package dataset

import (
	"encoding/json"
	"errors"
)

const DSLVersion = "1.0"

var (
	ErrNotFound                   = errors.New("dataset not found")
	ErrVersionNotFound            = errors.New("dataset version not found")
	ErrRevisionNotFound           = errors.New("dataset draft revision not found")
	ErrVersionRollbackUnavailable = errors.New("dataset version rollback is unavailable")
	ErrVersionUnavailable         = errors.New("dataset version is unavailable")
	ErrConflict                   = errors.New("dataset version conflict")
	ErrAlreadyExists              = errors.New("dataset code already exists")
	ErrIdempotencyConflict        = errors.New("dataset idempotency key conflict")
	ErrPublishUnavailable         = errors.New("dataset publication validator is unavailable")
	ErrPublishValidation          = errors.New("dataset publication validation failed")
	ErrForbidden                  = errors.New("dataset operation is forbidden")
	ErrInvalidTransition          = errors.New("dataset version transition is invalid")
	ErrInUse                      = errors.New("dataset is still in use")
	ErrInvalidDocument            = errors.New("dataset document is invalid")
	ErrPreviewInvalid             = errors.New("dataset preview request is invalid")
	ErrPreviewFailed              = errors.New("dataset preview failed")
	ErrPreviewTimeout             = errors.New("dataset preview timed out")
	ErrPreviewUnsupported         = errors.New("dataset preview source is unsupported")
	ErrQueryNotFound              = errors.New("query run not found")
	ErrQueryConflict              = errors.New("query run already exists")
)

// Document 是数据集 DSL V1 的完整、可版本化定义。
type Document struct {
	DSLVersion      string           `json:"dslVersion"`
	Dataset         Descriptor       `json:"dataset"`
	Nodes           []Node           `json:"nodes"`
	Joins           []Join           `json:"joins"`
	PreAggregations []PreAggregation `json:"preAggregations,omitempty"`
	Fields          []Field          `json:"fields"`
	Filters         []Filter         `json:"filters"`
	GroupBy         []string         `json:"groupBy"`
	Having          []Filter         `json:"having"`
	Sorts           []Sort           `json:"sorts"`
	Parameters      []Parameter      `json:"parameters"`
	OutputGrain     OutputGrain      `json:"outputGrain"`
	ExecutionPolicy ExecutionPolicy  `json:"executionPolicy"`
	// Designer 保存不参与查询执行的画布元数据，例如组件位置、连线和展示名称。
	// 使用开放对象让设计器可以向后兼容地扩展交互信息；领域校验仍会约束版本、
	// 组件身份以及坐标，避免把无效画布写入不可变修订。
	Designer map[string]any `json:"designer,omitempty"`
}

// PreAggregation 描述一个发生在 Join 槽位之前的显式分组组件。
// Join 仍引用原始节点 ID，JoinID 与 JoinSide 用于保存画布上的准确连接拓扑。
type PreAggregation struct {
	ID       string                 `json:"id"`
	NodeID   string                 `json:"nodeId"`
	JoinID   string                 `json:"joinId"`
	JoinSide string                 `json:"joinSide"`
	GroupBy  []PreAggregationGroup  `json:"groupBy"`
	Metrics  []PreAggregationMetric `json:"metrics"`
}

// PreAggregationGroup 描述关联前分组的维度字段及可选日期粒度。
type PreAggregationGroup struct {
	Field string `json:"field"`
	Unit  string `json:"unit,omitempty"`
}

// PreAggregationMetric 描述关联前产生的指标；结果继续使用原字段名供 Join 引用。
type PreAggregationMetric struct {
	Field    string `json:"field"`
	Function string `json:"function"`
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

// Join 描述两个节点之间的关联；无法在设计期确认基数时使用 UNKNOWN，执行优化必须保守降级。
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

// PublicationIssue 描述发布前校验失败的稳定代码和 DSL 路径，不包含源数据或 SQL。
type PublicationIssue struct {
	Path   string `json:"path"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// PublicationValidationError 聚合发布试跑、依赖和策略校验发现的问题。
type PublicationValidationError struct {
	Issues []PublicationIssue `json:"details"`
}

func (e *PublicationValidationError) Error() string { return ErrPublishValidation.Error() }

// Unwrap 让 HTTP 层可以用 errors.Is 识别发布校验错误。
func (e *PublicationValidationError) Unwrap() error { return ErrPublishValidation }

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
	ID                        string          `json:"id"`
	OriginTableID             string          `json:"originTableId,omitempty"`
	Code                      string          `json:"code"`
	Name                      string          `json:"name"`
	Description               string          `json:"description"`
	Type                      string          `json:"type"`
	Status                    string          `json:"status"`
	Version                   int64           `json:"version"`
	DraftVersionID            string          `json:"draftVersionId"`
	DraftVersionNo            int             `json:"draftVersionNo"`
	DraftRecordVersion        int64           `json:"draftRecordVersion"`
	CurrentPublishedVersionID string          `json:"currentPublishedVersionId,omitempty"`
	DSLVersion                string          `json:"dslVersion"`
	DSLHash                   string          `json:"dslHash"`
	PlanHash                  string          `json:"planHash"`
	DSL                       json.RawMessage `json:"dsl"`
	LogicalPlan               json.RawMessage `json:"logicalPlan"`
	CreatedAt                 string          `json:"createdAt"`
	UpdatedAt                 string          `json:"updatedAt"`
}

// Summary 是数据集目录使用的轻量摘要，不返回完整 DSL。
type Summary struct {
	ID                        string `json:"id"`
	OriginTableID             string `json:"originTableId,omitempty"`
	Code                      string `json:"code"`
	Name                      string `json:"name"`
	Description               string `json:"description"`
	Type                      string `json:"type"`
	Status                    string `json:"status"`
	Version                   int64  `json:"version"`
	DSLHash                   string `json:"dslHash"`
	CurrentPublishedVersionID string `json:"currentPublishedVersionId,omitempty"`
	UpdatedAt                 string `json:"updatedAt"`
}

// VersionRecord 是按精确版本 ID 加载的不可变发布快照。
type VersionRecord struct {
	ID                   string          `json:"id"`
	DatasetID            string          `json:"datasetId"`
	DatasetRecordVersion int64           `json:"datasetRecordVersion"`
	DraftVersionID       string          `json:"draftVersionId"`
	DraftRecordVersion   int64           `json:"draftRecordVersion"`
	VersionNo            int             `json:"versionNo"`
	Status               string          `json:"status"`
	DSLVersion           string          `json:"dslVersion"`
	DSLHash              string          `json:"dslHash"`
	PlanHash             string          `json:"planHash"`
	DSL                  json.RawMessage `json:"dsl"`
	LogicalPlan          json.RawMessage `json:"logicalPlan"`
	PublishedAt          string          `json:"publishedAt"`
	PublishedBy          string          `json:"publishedBy"`
}

// VersionSummary 是版本目录使用的轻量发布快照。
type VersionSummary struct {
	ID                 string `json:"id"`
	DatasetID          string `json:"datasetId"`
	VersionNo          int    `json:"versionNo"`
	Status             string `json:"status"`
	DSLVersion         string `json:"dslVersion"`
	DSLHash            string `json:"dslHash"`
	PlanHash           string `json:"planHash"`
	DraftRecordVersion int64  `json:"draftRecordVersion"`
	PublishedAt        string `json:"publishedAt"`
	PublishedBy        string `json:"publishedBy"`
}

// RevisionSummary 是草稿历史目录中的不可变快照摘要。VersionNo 使用产生该
// 快照时的数据集聚合版本号，因此发布和生命周期操作会留下有意义的编号间隙。
type RevisionSummary struct {
	ID                 string `json:"id"`
	DatasetID          string `json:"datasetId"`
	VersionNo          int64  `json:"versionNo"`
	OperationType      string `json:"operationType"`
	SourceRevisionID   string `json:"sourceRevisionId,omitempty"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	Type               string `json:"type"`
	DraftVersionID     string `json:"draftVersionId"`
	DraftRecordVersion int64  `json:"draftRecordVersion"`
	DSLVersion         string `json:"dslVersion"`
	DSLHash            string `json:"dslHash"`
	PlanHash           string `json:"planHash"`
	CreatedAt          string `json:"createdAt"`
	CreatedBy          string `json:"createdBy"`
}

// RevisionRecord 增加完整 DSL 与逻辑计划，供查看和回滚到精确草稿修订。
type RevisionRecord struct {
	RevisionSummary
	DSL         json.RawMessage `json:"dsl"`
	LogicalPlan json.RawMessage `json:"logicalPlan"`
}

// RevisionPage 是草稿历史目录的稳定分页响应。
type RevisionPage struct {
	Items  []RevisionSummary `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// VersionUsage 汇总精确发布版本当前可见的引用和运行占用，不暴露下游资源标识。
type VersionUsage struct {
	ReportDraftReferences         int `json:"reportDraftReferences"`
	DownstreamDraftReferences     int `json:"downstreamDraftReferences"`
	DownstreamPublishedReferences int `json:"downstreamPublishedReferences"`
	ActiveQueryRuns               int `json:"activeQueryRuns"`
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

// LifecycleInput 使用数据集聚合版本保护停用、恢复和删除操作，避免覆盖并发保存或发布。
type LifecycleInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

// PublishInput 绑定一个确定的草稿修订和发布试跑参数。
type PublishInput struct {
	DraftVersionID             string         `json:"draftVersionId"`
	ExpectedVersion            int64          `json:"expectedVersion"`
	ExpectedDraftRecordVersion int64          `json:"expectedDraftRecordVersion"`
	ExpectedDSLHash            string         `json:"expectedDslHash"`
	ValidationParameters       map[string]any `json:"validationParameters"`
}

// PublicationCandidate 是交给查询运行时试跑的只读草稿快照。
type PublicationCandidate struct {
	DatasetID          string
	DraftVersionID     string
	DraftRecordVersion int64
	DSLHash            string
	PlanHash           string
	DSL                json.RawMessage
	Parameters         map[string]any
}

// PublishPlan 保存发布事务所需的规范内容和幂等身份。
type PublishPlan struct {
	IdempotencyKey             string
	RequestHash                string
	ExpectedVersion            int64
	DraftVersionID             string
	ExpectedDraftRecordVersion int64
	ExpectedDSLHash            string
	Prepared                   Prepared
}

// VersionTransitionInput 只允许受控地把发布版本单向转为失效或废弃。
type VersionTransitionInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	ExpectedStatus  string `json:"expectedStatus"`
	TargetStatus    string `json:"targetStatus"`
}

// RollbackRevisionInput 以数据集聚合版本保护历史恢复，避免覆盖并发保存、发布
// 或生命周期操作。目标修订由 URL 中的精确 revisionId 决定。
type RollbackRevisionInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

// PreviewInput 包含受 DSL 定义约束的参数、行数上限和客户端预生成查询标识。
type PreviewInput struct {
	QueryID    string         `json:"queryId,omitempty"`
	Parameters map[string]any `json:"parameters"`
	MaxRows    int            `json:"maxRows,omitempty"`
}

// DraftPreviewInput 携带已有数据集在客户端物化出的完整候选 DSL。
// ExpectedVersion 把候选绑定到已加载的持久化基线；执行不会更新草稿或创建修订。
type DraftPreviewInput struct {
	QueryID         string          `json:"queryId,omitempty"`
	ExpectedVersion int64           `json:"expectedVersion"`
	DSL             json.RawMessage `json:"dsl"`
	Parameters      map[string]any  `json:"parameters"`
	MaxRows         int             `json:"maxRows,omitempty"`
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

// DraftPreviewResult 标识实际生成样本的规范候选，供编辑器丢弃过期 DAG 的响应。
type DraftPreviewResult struct {
	PreviewResult
	DSLHash     string `json:"dslHash"`
	PlanHash    string `json:"planHash"`
	BaseVersion int64  `json:"baseVersion"`
}

// PreviewWarning 向设计器返回不含源数据值的 Join 语义与性能风险。
type PreviewWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	JoinID        string `json:"joinId,omitempty"`
	EstimatedRows int    `json:"estimatedRows,omitempty"`
}
