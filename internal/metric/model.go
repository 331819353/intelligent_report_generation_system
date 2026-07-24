package metric

import (
	"context"
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/dataset"
)

const DefinitionVersion = "1.0"

var (
	ErrNotFound                   = errors.New("metric not found")
	ErrVersionNotFound            = errors.New("metric version not found")
	ErrVersionUnavailable         = errors.New("metric version is unavailable")
	ErrAlreadyExists              = errors.New("metric code already exists")
	ErrConflict                   = errors.New("metric version conflict")
	ErrForbidden                  = errors.New("metric operation is forbidden")
	ErrInvalidDefinition          = errors.New("metric definition is invalid")
	ErrInvalidTransition          = errors.New("metric version transition is invalid")
	ErrVersionInUse               = errors.New("metric version is used by an active published dependency")
	ErrInUse                      = errors.New("metric is still in use")
	ErrIdempotencyConflict        = errors.New("metric idempotency key conflict")
	ErrPreviewUnavailable         = errors.New("metric preview is unavailable")
	ErrPreviewFailed              = errors.New("metric preview failed")
	ErrOriginCandidateConflict    = errors.New("origin metric candidate version conflict")
	ErrOriginCandidateUnavailable = errors.New("origin metric candidate is unavailable")
)

// Definition 是指标草稿和发布版本共同使用的唯一事实来源。
type Definition struct {
	SchemaVersion                string      `json:"schemaVersion"`
	Metric                       Descriptor  `json:"metric"`
	DatasetID                    string      `json:"datasetId"`
	DatasetVersionID             string      `json:"datasetVersionId"`
	Expression                   Expression  `json:"expression"`
	Aggregation                  string      `json:"aggregation"`
	Unit                         string      `json:"unit"`
	NumberFormat                 string      `json:"numberFormat"`
	TimeFieldID                  string      `json:"timeFieldId,omitempty"`
	TimeGrain                    string      `json:"timeGrain"`
	Additivity                   string      `json:"additivity"`
	NonAdditiveDimensionFieldIDs []string    `json:"nonAdditiveDimensionFieldIds"`
	AllowedDimensions            []Dimension `json:"allowedDimensions"`
	DecimalScale                 int         `json:"decimalScale"`
	RoundingMode                 string      `json:"roundingMode"`
	NullHandling                 string      `json:"nullHandling"`
	DivisionByZero               string      `json:"divisionByZero"`
}

// Descriptor 保存指标主对象的可移植基本信息。
type Descriptor struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// Expression 是只允许字段、精确指标版本、十进制常量和四则运算的表达式树。
type Expression struct {
	Type            string       `json:"type"`
	FieldID         string       `json:"fieldId,omitempty"`
	MetricVersionID string       `json:"metricVersionId,omitempty"`
	Value           any          `json:"value,omitempty"`
	Arguments       []Expression `json:"arguments,omitempty"`
}

// Dimension 保存某个指标版本允许使用的数据集维度及展示语义。
type Dimension struct {
	FieldID           string   `json:"fieldId"`
	Name              string   `json:"name"`
	HierarchyFieldIDs []string `json:"hierarchyFieldIds"`
	SortDirection     string   `json:"sortDirection"`
	NullLabel         string   `json:"nullLabel"`
}

// ValidationIssue 提供稳定代码和可直接定位的定义路径。
type ValidationIssue struct {
	Path   string `json:"path"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// ValidationError 聚合一次可发现的全部指标定义问题。
type ValidationError struct {
	Issues []ValidationIssue `json:"details"`
}

func (e *ValidationError) Error() string { return ErrInvalidDefinition.Error() }
func (e *ValidationError) Unwrap() error { return ErrInvalidDefinition }

// Prepared 是完成严格解码、规范化和确定性哈希后的指标定义。
type Prepared struct {
	Definition           Definition
	DefinitionJSON       json.RawMessage
	DefinitionHash       string
	DimensionFieldIDs    []string
	DependencyVersionIDs []string
}

// Record 返回指标主对象及其当前可变草稿。
type Record struct {
	ID                        string          `json:"id"`
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
	DatasetID                 string          `json:"datasetId"`
	DatasetVersionID          string          `json:"datasetVersionId"`
	DefinitionHash            string          `json:"definitionHash"`
	Definition                json.RawMessage `json:"definition"`
	CreatedAt                 string          `json:"createdAt"`
	UpdatedAt                 string          `json:"updatedAt"`
}

// Summary 是指标目录的轻量记录。
type Summary struct {
	ID                        string `json:"id"`
	Code                      string `json:"code"`
	Name                      string `json:"name"`
	Description               string `json:"description"`
	Type                      string `json:"type"`
	Status                    string `json:"status"`
	Version                   int64  `json:"version"`
	DatasetID                 string `json:"datasetId"`
	DatasetVersionID          string `json:"datasetVersionId"`
	CurrentPublishedVersionID string `json:"currentPublishedVersionId,omitempty"`
	UpdatedAt                 string `json:"updatedAt"`
}

// VersionRecord 是按精确版本 ID 读取的不可变指标快照。
type VersionRecord struct {
	ID                  string          `json:"id"`
	MetricID            string          `json:"metricId"`
	MetricRecordVersion int64           `json:"metricRecordVersion"`
	DraftVersionID      string          `json:"draftVersionId"`
	DraftRecordVersion  int64           `json:"draftRecordVersion"`
	VersionNo           int             `json:"versionNo"`
	Status              string          `json:"status"`
	DatasetID           string          `json:"datasetId"`
	DatasetVersionID    string          `json:"datasetVersionId"`
	DefinitionHash      string          `json:"definitionHash"`
	Definition          json.RawMessage `json:"definition"`
	PublishedAt         string          `json:"publishedAt"`
	PublishedBy         string          `json:"publishedBy"`
}

// VersionSummary 是版本目录使用的不可变摘要。
type VersionSummary struct {
	ID                 string `json:"id"`
	MetricID           string `json:"metricId"`
	VersionNo          int    `json:"versionNo"`
	Status             string `json:"status"`
	DatasetID          string `json:"datasetId"`
	DatasetVersionID   string `json:"datasetVersionId"`
	DefinitionHash     string `json:"definitionHash"`
	DraftRecordVersion int64  `json:"draftRecordVersion"`
	PublishedAt        string `json:"publishedAt"`
	PublishedBy        string `json:"publishedBy"`
}

// VersionUsage 只返回聚合计数，避免通过占用接口枚举无权访问的下游对象。
type VersionUsage struct {
	ReportDraftReferences         int `json:"reportDraftReferences"`
	DownstreamDraftReferences     int `json:"downstreamDraftReferences"`
	DownstreamPublishedReferences int `json:"downstreamPublishedReferences"`
	ActiveQueryRuns               int `json:"activeQueryRuns"`
}

type CreateInput struct {
	Definition json.RawMessage `json:"definition"`
}

// UpdateInput 同时锁定主对象版本、草稿修订和定义摘要。
type UpdateInput struct {
	ExpectedVersion            int64           `json:"expectedVersion"`
	ExpectedDraftRecordVersion int64           `json:"expectedDraftRecordVersion"`
	ExpectedDefinitionHash     string          `json:"expectedDefinitionHash"`
	Definition                 json.RawMessage `json:"definition"`
}

// DeleteInput 使用主对象乐观锁，避免把用户刚刚更新的指标误删。
type DeleteInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

// PublishInput 固定发布所依据的草稿事实和试算参数。
type PublishInput struct {
	DraftVersionID             string         `json:"draftVersionId"`
	ExpectedVersion            int64          `json:"expectedVersion"`
	ExpectedDraftRecordVersion int64          `json:"expectedDraftRecordVersion"`
	ExpectedDefinitionHash     string         `json:"expectedDefinitionHash"`
	ValidationParameters       map[string]any `json:"validationParameters"`
}

// PreviewInput 只允许选择定义声明的维度并传入数据集参数。
type PreviewInput struct {
	QueryID           string         `json:"queryId,omitempty"`
	Parameters        map[string]any `json:"parameters"`
	DimensionFieldIDs []string       `json:"dimensionFieldIds"`
	MaxRows           int            `json:"maxRows,omitempty"`
}

type VersionTransitionInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	ExpectedStatus  string `json:"expectedStatus"`
	TargetStatus    string `json:"targetStatus"`
}

// PublishPlan 保存仓储发布事务需要的规范快照和幂等身份。
type PublishPlan struct {
	IdempotencyKey             string
	RequestHash                string
	ExpectedVersion            int64
	DraftVersionID             string
	ExpectedDraftRecordVersion int64
	ExpectedDefinitionHash     string
	Prepared                   Prepared
}

// QueryCandidate 是交给统一查询运行时的服务端派生数据集计划。
type QueryCandidate struct {
	MetricID         string
	MetricVersionID  string
	DatasetID        string
	DatasetVersionID string
	DSL              json.RawMessage
	PlanHash         string
}

// Store 定义指标草稿、不可变版本和精确依赖解析的持久化边界。
type Store interface {
	Create(context.Context, string, string, Prepared) (Record, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, string, int, int) ([]Summary, int, error)
	Update(context.Context, string, string, string, UpdateInput, Prepared) (Record, error)
	Delete(context.Context, string, string, string, DeleteInput) error
	GetDatasetVersion(context.Context, string, string, string) (dataset.VersionRecord, error)
	GetVersionByID(context.Context, string, string) (VersionRecord, error)
	ReplayPublication(context.Context, string, string, string, string, string) (VersionRecord, bool, error)
	Publish(context.Context, string, string, string, PublishPlan) (VersionRecord, error)
	GetVersion(context.Context, string, string, string) (VersionRecord, error)
	ListVersions(context.Context, string, string, int, int) ([]VersionSummary, int, error)
	GetVersionUsage(context.Context, string, string, string) (VersionUsage, error)
	TransitionVersion(context.Context, string, string, string, string, VersionTransitionInput) (VersionRecord, error)
}

// Previewer 确保草稿试算和发布试算复用同一受控查询执行器。
type Previewer interface {
	PreviewMetric(context.Context, string, string, QueryCandidate, dataset.PreviewInput, bool) (dataset.PreviewResult, error)
	Cancel(context.Context, string, string, string, string) error
}
