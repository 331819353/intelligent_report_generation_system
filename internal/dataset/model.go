package dataset

import (
	"encoding/json"
	"errors"
)

const DSLVersion = "1.0"

// GroupByMode 描述分组组件如何组合多个维度。空值与 STANDARD 等价，
// 以保持历史 DSL 的 JSON 形状和哈希稳定。
type GroupByMode string

const (
	GroupByModeStandard GroupByMode = "STANDARD"
	GroupByModeCube     GroupByMode = "CUBE"
	GroupByModeRollup   GroupByMode = "ROLLUP"
	GroupByModeSets     GroupByMode = "GROUPING_SETS"
)

// Layer 是数据集的粒度合同：它只回答“这个数据集的一行代表什么”，不再规定
// 数据集必须由哪一层加工而来。层级与血缘方式（Lineage）正交：
//
//	ODS  贴源粒度：与物理源表逐行一致，不改变源表行粒度。
//	DIM  实体粒度：一行一个业务实体，必须声明实体粒度与业务键，不做业务聚合。
//	DWD  明细粒度：一行一个业务事实/事件，不去重、不聚合，可关联维度。
//	DWS  汇总粒度：一行一个汇总统计粒度，必须声明粒度键并至少携带一个度量。
//	ADS  应用粒度：面向消费场景的输出粒度，必须声明粒度键；语义合同 1.0 需绑定消费合同。
//
// 它与 SINGLE_SOURCE/CROSS_SOURCE 同样正交：后者只描述执行时涉及的数据源数量。
type Layer string

const (
	LayerODS Layer = "ODS"
	LayerDIM Layer = "DIM"
	LayerDWD Layer = "DWD"
	LayerDWS Layer = "DWS"
	LayerADS Layer = "ADS"
)

// Lineage 是数据集的血缘方式，由 DSL 拓扑推导，不单独持久化：
//
//	SOURCE   源表直落：恰好一个物理 TABLE 节点、无 Join。物理表（含导入表、既有
//	         宽表）按声明层级直接进入数仓，保持源表既有粒度；五个层级都可以直落。
//	MODELED  分层加工：全部节点引用已发布数据集版本，上游层级必须满足
//	         ODS→DIM/DWD→DWS→ADS 的方向约束（见 ValidateLayerDependencies）。
//
// 完整链路仍然是标准建模路径；直落只是把“进入数仓的层级”交给声明者决定，
// 层级本身的粒度合同对两种血缘一视同仁。
type Lineage string

const (
	LineageSource  Lineage = "SOURCE"
	LineageModeled Lineage = "MODELED"
)

// SourceMode 是遗留兼容标记。早期版本只允许 ODS 直落，源端已汇总的物理表需要
// 服务端分类器写入 PRE_AGGREGATED 才能以 DWS 直落；现在任何单表源直落都是
// 一等血缘，该标记不再承担校验职责。已发布 DSL 继续原样解码与编码以保持
// 内容哈希稳定；新文档不再写入。
type SourceMode string

// SourceModePreAggregated 仅为解码历史 DSL 保留，语义等价于 SOURCE 血缘的 DWS。
const SourceModePreAggregated SourceMode = "PRE_AGGREGATED"

// PublicationOrigin 是数据库持久化的不可变发布来源。调用方不能在请求或
// PublishPlan 中提供它；各服务端提交路径在进入 package-private publishTx 时固定。
type PublicationOrigin string

const (
	PublicationOriginUnpublished            PublicationOrigin = "UNPUBLISHED"
	PublicationOriginDirect                 PublicationOrigin = "DIRECT"
	PublicationOriginHumanApproval          PublicationOrigin = "HUMAN_APPROVAL"
	PublicationOriginSystemMappedDefault    PublicationOrigin = "SYSTEM_MAPPED_DEFAULT"
	PublicationOriginSystemMappedRefresh    PublicationOrigin = "SYSTEM_MAPPED_REFRESH"
	PublicationOriginSystemMappedRegenerate PublicationOrigin = "SYSTEM_MAPPED_REGENERATE"
	PublicationOriginLegacy                 PublicationOrigin = "LEGACY"
)

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
	ErrLayerDependencyUnavailable = errors.New("dataset layer dependency is unavailable")
	ErrLLMTriggerUnavailable      = errors.New("dataset LLM trigger is unavailable")
	ErrLLMTriggerScopeInvalid     = errors.New("dataset LLM trigger scope is invalid")
	ErrSemanticNamingUnavailable  = errors.New("dataset semantic naming is unavailable")
	ErrSemanticNamingInvalid      = errors.New("dataset semantic naming output is invalid")
)

// Document 是数据集 DSL V1 的完整、可版本化定义。
type Document struct {
	DSLVersion       string            `json:"dslVersion"`
	Dataset          Descriptor        `json:"dataset"`
	Nodes            []Node            `json:"nodes"`
	Joins            []Join            `json:"joins"`
	Transforms       []Transform       `json:"transforms,omitempty"`
	PreAggregations  []PreAggregation  `json:"preAggregations,omitempty"`
	FactContract     *FactContract     `json:"factContract,omitempty"`
	AnalysisContract *AnalysisContract `json:"analysisContract,omitempty"`
	Fields           []Field           `json:"fields"`
	Distinct         bool              `json:"distinct,omitempty"`
	Filters          []Filter          `json:"filters"`
	GroupBy          []string          `json:"groupBy"`
	GroupByMode      GroupByMode       `json:"groupByMode,omitempty"`
	GroupingSets     [][]string        `json:"groupingSets,omitempty"`
	Having           []Filter          `json:"having"`
	Sorts            []Sort            `json:"sorts"`
	Parameters       []Parameter       `json:"parameters"`
	OutputGrain      OutputGrain       `json:"outputGrain"`
	ExecutionPolicy  ExecutionPolicy   `json:"executionPolicy"`
	// Designer 保存不参与查询执行的画布元数据，例如组件位置、连线和展示名称。
	// 使用开放对象让设计器可以向后兼容地扩展交互信息；领域校验仍会约束版本、
	// 组件身份以及坐标，避免把无效画布写入不可变修订。
	Designer map[string]any `json:"designer,omitempty"`

	// layerInferred 只标记输入兼容路径，不进入 JSON。历史 DSL 的正文和哈希必须
	// 保持稳定，层级会单独持久化到 datasets/dataset_versions。
	layerInferred  bool
	layerSpecified bool
	inferredLayer  Layer
	sourcePreview  bool
}

// MarshalJSON 保留历史 DSL 的“未声明 layer”形状。旧调用方常见的
// DecodeAndNormalize -> 修改 DAG -> Marshal 流程会据新 DAG 重新推断层级，而不会把
// 上一次兼容推断误升级成用户显式声明；新文档的显式 layer 则正常进入正文和哈希。
func (document Document) MarshalJSON() ([]byte, error) {
	type documentJSON Document
	copy := document
	if copy.layerInferred && copy.Dataset.Layer == copy.inferredLayer {
		copy.Dataset.Layer = ""
	}
	return json.Marshal(documentJSON(copy))
}

// PreAggregation 描述一个发生在 Join 槽位之前的显式分组组件。
// Join 仍引用原始节点 ID，JoinID 与 JoinSide 用于保存画布上的准确连接拓扑。
type PreAggregation struct {
	ID           string                 `json:"id"`
	NodeID       string                 `json:"nodeId"`
	JoinID       string                 `json:"joinId"`
	JoinSide     string                 `json:"joinSide"`
	GroupBy      []PreAggregationGroup  `json:"groupBy"`
	GroupByMode  GroupByMode            `json:"groupByMode,omitempty"`
	GroupingSets [][]string             `json:"groupingSets,omitempty"`
	Metrics      []PreAggregationMetric `json:"metrics"`
}

// PreAggregationGroup 描述关联前分组的维度字段及可选日期粒度。
type PreAggregationGroup struct {
	Field      string      `json:"field"`
	Unit       string      `json:"unit,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
}

// PreAggregationMetric 描述关联前产生的指标；结果继续使用原字段名供 Join 引用。
type PreAggregationMetric struct {
	Field      string      `json:"field"`
	Function   string      `json:"function"`
	CountRows  bool        `json:"countRows,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
}

// Descriptor 保存 DSL 内可移植的数据集基本信息。
type Descriptor struct {
	Code                    string       `json:"code"`
	Name                    string       `json:"name"`
	Description             string       `json:"description,omitempty"`
	Domain                  string       `json:"domain,omitempty"`
	Subject                 string       `json:"subject,omitempty"`
	Type                    string       `json:"type"`
	Layer                   Layer        `json:"layer,omitempty"`
	// SourceMode 只为历史 DSL 保留（见 SourceMode 类型说明）；血缘方式请用 Document.Lineage()。
	SourceMode SourceMode `json:"sourceMode,omitempty"`
	SemanticContractVersion string       `json:"semanticContractVersion,omitempty"`
	ConsumerContractID      string       `json:"consumerContractId,omitempty"`
	Grain                   *OutputGrain `json:"grain,omitempty"`
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

// Join 描述两个节点之间的关联。关联基数由 JoinType 自动推导：
// INNER=ONE_TO_ONE、LEFT=MANY_TO_ONE、RIGHT=ONE_TO_MANY、FULL=MANY_TO_MANY。
// 旧关系语义字段仅保留用于兼容历史 DSL 的严格解码，规范化后不会继续下传。
type Join struct {
	ID               string                `json:"id"`
	LeftNodeID       string                `json:"leftNodeId"`
	RightNodeID      string                `json:"rightNodeId"`
	JoinType         string                `json:"joinType"`
	Cardinality      string                `json:"cardinality"`
	RelationshipType string                `json:"relationshipType,omitempty"`
	RelationshipRole string                `json:"relationshipRole,omitempty"`
	FanoutPolicy     string                `json:"fanoutPolicy,omitempty"`
	Bridge           *BridgeContract       `json:"bridge,omitempty"`
	Temporal         *TemporalJoinContract `json:"temporal,omitempty"`
	Conditions       []JoinCondition       `json:"conditions"`
	ManualConfirmed  bool                  `json:"manualConfirmed"`
}

// BridgeContract 仅用于解码并迁移旧版 DSL。
type BridgeContract struct {
	BridgeNodeID          string `json:"bridgeNodeId"`
	RelationshipTypeField string `json:"relationshipTypeField,omitempty"`
	AllocationWeightField string `json:"allocationWeightField,omitempty"`
	PrimaryFlagField      string `json:"primaryFlagField,omitempty"`
	ValidFromField        string `json:"validFromField,omitempty"`
	ValidToField          string `json:"validToField,omitempty"`
}

// TemporalJoinContract 仅用于解码并迁移旧版 DSL。
type TemporalJoinContract struct {
	EventNodeID      string `json:"eventNodeId"`
	EventTimeField   string `json:"eventTimeField"`
	ValidityNodeID   string `json:"validityNodeId"`
	ValidFromField   string `json:"validFromField"`
	ValidToField     string `json:"validToField"`
	ValidToInclusive bool   `json:"validToInclusive"`
}

// FactContract 固定 DWD 的业务动作、事实粒度、事件时间和原子度量可加性。
type FactContract struct {
	BusinessAction string                  `json:"businessAction"`
	GrainKeyFields []string                `json:"grainKeyFields"`
	EventTimeField string                  `json:"eventTimeField,omitempty"`
	AtomicMeasures []AtomicMeasureContract `json:"atomicMeasures"`
}

type AtomicMeasureContract struct {
	Field              string `json:"field"`
	Additivity         string `json:"additivity"`
	ValueBehavior      string `json:"valueBehavior,omitempty"`
	DefaultAggregation string `json:"defaultAggregation,omitempty"`
	TimeAggregation    string `json:"timeAggregation,omitempty"`
	Unit               string `json:"unit,omitempty"`
	Currency           string `json:"currency,omitempty"`
	NullPolicy         string `json:"nullPolicy"`
}

// AnalysisContract 描述 DWS 的市场通用分析意图和多事实共同粒度。它只保存
// 逻辑语义；是否物化仍由独立构建策略决定。
type AnalysisContract struct {
	Intent              string                    `json:"intent"`
	InputMode           string                    `json:"inputMode"`
	CommonGrainFields   []string                  `json:"commonGrainFields"`
	ConformedDimensions []string                  `json:"conformedDimensions"`
	TimeField           string                    `json:"timeField,omitempty"`
	TimeGrain           string                    `json:"timeGrain,omitempty"`
	Measures            []AnalysisMeasureContract `json:"measures"`
}

type AnalysisMeasureContract struct {
	Field           string   `json:"field"`
	SourceNodeIDs   []string `json:"sourceNodeIds"`
	Aggregation     string   `json:"aggregation"`
	Additivity      string   `json:"additivity"`
	ValueBehavior   string   `json:"valueBehavior,omitempty"`
	TimeAggregation string   `json:"timeAggregation,omitempty"`
	Unit            string   `json:"unit,omitempty"`
	Currency        string   `json:"currency,omitempty"`
}

// JoinCondition 保存 Join 两侧表达式，禁止保存拼接后的 SQL。
type JoinCondition struct {
	LeftExpression  Expression `json:"leftExpression"`
	Operator        string     `json:"operator"`
	RightExpression Expression `json:"rightExpression"`
}

// Transform 是执行 DSL 中可审计的字段处理组件。Designer 仍保存坐标等
// 交互信息；这里保存组件身份、拓扑和每条规则的规范表达式。
type Transform struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Family        string          `json:"family"`
	ComponentType string          `json:"componentType"`
	Input         TransformInput  `json:"input"`
	Rules         []TransformRule `json:"rules"`
}

type TransformInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TransformRule struct {
	ID               string          `json:"id"`
	Operation        string          `json:"operation"`
	InputKeys        []string        `json:"inputKeys"`
	Output           TransformOutput `json:"output"`
	Expression       Expression      `json:"expression"`
	ReplaceSourceKey string          `json:"replaceSourceKey,omitempty"`
}

type TransformOutput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Code          string `json:"code"`
	CanonicalType string `json:"canonicalType"`
}

// Field 描述数据集输出字段及其语义角色。
type Field struct {
	ID               string     `json:"id"`
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	Role             string     `json:"role"`
	Expression       Expression `json:"expression"`
	CanonicalType    string     `json:"canonicalType"`
	SemanticType     string     `json:"semanticType,omitempty"`
	SensitivityLevel string     `json:"sensitivityLevel,omitempty"`
	Aggregation      string     `json:"aggregation,omitempty"`
	Format           string     `json:"format,omitempty"`
	Unit             string     `json:"unit,omitempty"`
	Nullable         bool       `json:"nullable"`
	Visible          *bool      `json:"visible,omitempty"`
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
	Type        string        `json:"type"`
	NodeID      string        `json:"nodeId,omitempty"`
	Field       string        `json:"field,omitempty"`
	Code        string        `json:"code,omitempty"`
	Function    string        `json:"function,omitempty"`
	Unit        string        `json:"unit,omitempty"`
	TargetType  string        `json:"targetType,omitempty"`
	Value       any           `json:"value,omitempty"`
	Argument    *Expression   `json:"argument,omitempty"`
	Arguments   []Expression  `json:"arguments,omitempty"`
	Left        *Expression   `json:"left,omitempty"`
	Right       *Expression   `json:"right,omitempty"`
	Lower       *Expression   `json:"lower,omitempty"`
	Upper       *Expression   `json:"upper,omitempty"`
	Whens       []CaseBranch  `json:"whens,omitempty"`
	Else        *Expression   `json:"else,omitempty"`
	PartitionBy []Expression  `json:"partitionBy,omitempty"`
	OrderBy     []WindowOrder `json:"orderBy,omitempty"`
}

// CaseBranch 描述 CASE 表达式的一条条件和返回值分支。
type CaseBranch struct {
	When Expression `json:"when"`
	Then Expression `json:"then"`
}

// WindowOrder 描述窗口函数 OVER 子句中的结构化排序项。
type WindowOrder struct {
	Expression Expression `json:"expression"`
	Direction  string     `json:"direction"`
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
	SemanticNaming  *SemanticNamingEvidence
}

// SemanticNamingEvidence carries the audited save-time LLM decision into the
// dataset transaction. It is not part of the DSL and therefore never changes a
// logical plan or schema hash by itself.
type SemanticNamingEvidence struct {
	AIRequestID   string
	PromptVersion string
	Tags          []SemanticTagSuggestion
}

// SemanticTagSuggestion is a controlled taxonomy choice made together with
// DWD/DWS/ADS business-table and output-field naming.
type SemanticTagSuggestion struct {
	TagID      string
	TagCode    string
	TagName    string
	Category   string
	Confidence float64
	Rationale  string
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
	DomainID                  string          `json:"domainId"`
	SharingScope              string          `json:"sharingScope"`
	OwnerUserID               string          `json:"ownerUserId"`
	Type                      string          `json:"type"`
	Layer                     Layer           `json:"layer"`
	Tags                      []string        `json:"tags"`
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
	ID                        string   `json:"id"`
	OriginTableID             string   `json:"originTableId,omitempty"`
	OriginTableName           string   `json:"originTableName,omitempty"`
	OriginDataSourceName      string   `json:"originDataSourceName,omitempty"`
	Code                      string   `json:"code"`
	Name                      string   `json:"name"`
	Description               string   `json:"description"`
	DomainID                  string   `json:"domainId"`
	SharingScope              string   `json:"sharingScope"`
	OwnerUserID               string   `json:"ownerUserId"`
	Type                      string   `json:"type"`
	Layer                     Layer    `json:"layer"`
	Tags                      []string `json:"tags"`
	Status                    string   `json:"status"`
	Version                   int64    `json:"version"`
	DSLHash                   string   `json:"dslHash"`
	CurrentPublishedVersionID string   `json:"currentPublishedVersionId,omitempty"`
	UpdatedAt                 string   `json:"updatedAt"`
}

// VersionRecord 是按精确版本 ID 加载的不可变发布快照。
type VersionRecord struct {
	ID                   string            `json:"id"`
	DatasetID            string            `json:"datasetId"`
	DatasetRecordVersion int64             `json:"datasetRecordVersion"`
	DraftVersionID       string            `json:"draftVersionId"`
	DraftRecordVersion   int64             `json:"draftRecordVersion"`
	VersionNo            int               `json:"versionNo"`
	Status               string            `json:"status"`
	PublicationOrigin    PublicationOrigin `json:"publicationOrigin"`
	Layer                Layer             `json:"layer"`
	DSLVersion           string            `json:"dslVersion"`
	DSLHash              string            `json:"dslHash"`
	PlanHash             string            `json:"planHash"`
	DSL                  json.RawMessage   `json:"dsl"`
	LogicalPlan          json.RawMessage   `json:"logicalPlan"`
	PublishedAt          string            `json:"publishedAt"`
	PublishedBy          string            `json:"publishedBy"`
}

// VersionSummary 是版本目录使用的轻量发布快照。
type VersionSummary struct {
	ID                 string            `json:"id"`
	DatasetID          string            `json:"datasetId"`
	VersionNo          int               `json:"versionNo"`
	Status             string            `json:"status"`
	PublicationOrigin  PublicationOrigin `json:"publicationOrigin"`
	Layer              Layer             `json:"layer"`
	DSLVersion         string            `json:"dslVersion"`
	DSLHash            string            `json:"dslHash"`
	PlanHash           string            `json:"planHash"`
	DraftRecordVersion int64             `json:"draftRecordVersion"`
	PublishedAt        string            `json:"publishedAt"`
	PublishedBy        string            `json:"publishedBy"`
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
	Layer       Layer           `json:"layer,omitempty"`
	DSL         json.RawMessage `json:"dsl"`
}

// UpdateInput 是带乐观锁的数据集草稿更新请求。
type UpdateInput struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	ExpectedVersion int64           `json:"expectedVersion"`
	DSL             json.RawMessage `json:"dsl"`
}

// MetadataUpdateInput 只允许修改数据集与输出字段的业务元信息。调用方不提交
// DSL，服务端基于当前草稿打补丁，从接口边界保证 DAG、字段编码和逻辑类型不变。
type MetadataUpdateInput struct {
	Name            string                `json:"name"`
	Description     string                `json:"description"`
	Subject         string                `json:"subject"`
	ExpectedVersion int64                 `json:"expectedVersion"`
	Fields          []FieldMetadataUpdate `json:"fields"`
}

type FieldMetadataUpdate struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Role         string `json:"role"`
	SemanticType string `json:"semanticType"`
	Nullable     bool   `json:"nullable"`
	Visible      bool   `json:"visible"`
}

// LifecycleInput 使用数据集聚合版本保护停用、恢复和删除操作，避免覆盖并发保存或发布。
type LifecycleInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

// LifecycleImpact is the deletion/disable preflight projection. It exposes
// only aggregate usage evidence so callers can make a safe lifecycle decision
// without learning identifiers they cannot otherwise read.
type LifecycleImpact struct {
	DatasetID                     string   `json:"datasetId"`
	Status                        string   `json:"status"`
	DownstreamDraftReferences     int      `json:"downstreamDraftReferences"`
	DownstreamPublishedReferences int      `json:"downstreamPublishedReferences"`
	ActiveQueryRuns               int      `json:"activeQueryRuns"`
	ActiveBuildRuns               int      `json:"activeBuildRuns"`
	Materializations              int      `json:"materializations"`
	CanDisable                    bool     `json:"canDisable"`
	CanRestore                    bool     `json:"canRestore"`
	CanDelete                     bool     `json:"canDelete"`
	Blockers                      []string `json:"blockers"`
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
	ReservedPublishedVersionID string
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

// CandidatePreviewInput 用于新建画布在尚无持久化数据集身份时执行受控小样本。
// 候选不会保存为数据集；运行时使用独立审计记录，避免伪造数据集版本身份。
type CandidatePreviewInput struct {
	QueryID    string          `json:"queryId,omitempty"`
	DSL        json.RawMessage `json:"dsl"`
	Parameters map[string]any  `json:"parameters"`
	MaxRows    int             `json:"maxRows,omitempty"`
}

// PreviewResult 返回小样本数据和运行摘要，不暴露生成 SQL。
type PreviewResult struct {
	QueryID        string           `json:"queryId"`
	Columns        []string         `json:"columns"`
	ColumnMetadata []PreviewColumn  `json:"columnMetadata"`
	Rows           [][]any          `json:"rows"`
	RowCount       int              `json:"rowCount"`
	DurationMS     int64            `json:"durationMs"`
	Warnings       []PreviewWarning `json:"warnings,omitempty"`
}

// PreviewColumn 为稳定技术列编码补充可供界面和下游理解的业务语义。
// 数组顺序与 PreviewResult.Columns 及每一行的值严格一致。
type PreviewColumn struct {
	FieldID             string `json:"fieldId,omitempty"`
	Code                string `json:"code"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	PhysicalName        string `json:"physicalName,omitempty"`
	CanonicalType       string `json:"canonicalType,omitempty"`
	SemanticType        string `json:"semanticType,omitempty"`
	Role                string `json:"role,omitempty"`
	Nullable            bool   `json:"nullable"`
	GroupingPlaceholder string `json:"groupingPlaceholder,omitempty"`
}

// DraftPreviewResult 标识实际生成样本的规范候选，供编辑器丢弃过期 DAG 的响应。
type DraftPreviewResult struct {
	PreviewResult
	DSLHash     string `json:"dslHash"`
	PlanHash    string `json:"planHash"`
	BaseVersion int64  `json:"baseVersion"`
}

// CandidatePreviewResult 返回未保存候选的规范摘要，供前端丢弃过期响应。
type CandidatePreviewResult struct {
	PreviewResult
	DSLHash  string `json:"dslHash"`
	PlanHash string `json:"planHash"`
}

// PreviewWarning 向设计器返回不含源数据值的 Join 语义与性能风险。
type PreviewWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	JoinID        string `json:"joinId,omitempty"`
	EstimatedRows int    `json:"estimatedRows,omitempty"`
}
