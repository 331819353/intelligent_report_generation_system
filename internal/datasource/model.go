package datasource

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalidConfiguration = errors.New("invalid data source configuration")
	ErrQuotaExceeded        = errors.New("tenant data source quota exceeded")
	ErrCodeConflict         = errors.New("data source code already exists")
	ErrTestRequired         = errors.New("a successful connection test is required for the current data source version")
	ErrTestExpired          = errors.New("the successful connection test for the current data source version has expired")
	ErrSourceVersionChanged = errors.New("data source configuration changed during the operation")
	ErrVersionConflict      = errors.New("data source was modified by another request")
	ErrVersioningRequired   = errors.New("data source versioned publication is not supported by the repository")
	ErrReviewPending        = errors.New("data source publication review is pending")
	ErrReviewRejected       = errors.New("data source publication review was rejected")
)

var dataSourceCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

type Type string

const (
	TypeMySQL  Type = "MYSQL"
	TypeOracle Type = "ORACLE"
	TypeExcel  Type = "EXCEL"
)

type Status string

const (
	StatusDraft    Status = "DRAFT"
	StatusActive   Status = "ACTIVE"
	StatusDisabled Status = "DISABLED"
	StatusSyncing  Status = "SYNCING"
	StatusError    Status = "ERROR"
	StatusDeleting Status = "DELETING"
	StatusDeleted  Status = "DELETED"
)

type ValidationStatus string

const (
	ValidationUntested ValidationStatus = "UNTESTED"
	ValidationPassed   ValidationStatus = "PASSED"
	ValidationFailed   ValidationStatus = "FAILED"
)

type PublicationStatus string

const (
	PublicationUnpublished PublicationStatus = "UNPUBLISHED"
	PublicationPublished   PublicationStatus = "PUBLISHED"
)

type ReviewStatus string

const (
	ReviewNotSubmitted ReviewStatus = "NOT_SUBMITTED"
	ReviewPending      ReviewStatus = "PENDING"
	ReviewApproved     ReviewStatus = "APPROVED"
	ReviewRejected     ReviewStatus = "REJECTED"
	ReviewWithdrawn    ReviewStatus = "WITHDRAWN"
)

type Visibility string

const (
	VisibilityPrivate      Visibility = "PRIVATE"
	VisibilityTenantPublic Visibility = "TENANT_PUBLIC"
)

type Source struct {
	ID          string         `json:"id"`
	TenantID    string         `json:"tenantId"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	OwnerID     string         `json:"ownerId,omitempty"`
	Visibility  Visibility     `json:"visibility"`
	Type        Type           `json:"type"`
	Status      Status         `json:"status"`
	Config      map[string]any `json:"config"`
	// SecretRef 只在服务端解析，任何数据源响应都不得把加密值或外部密钥引用返回浏览器。
	SecretRef              string            `json:"-"`
	FileAssetID            string            `json:"fileAssetId,omitempty"`
	FileVersionID          string            `json:"fileVersionId,omitempty"`
	ConfigVersionID        string            `json:"configVersionId,omitempty"`
	PublishedVersionID     string            `json:"publishedVersionId,omitempty"`
	ConfigVersion          int64             `json:"configVersion"`
	PublishedConfigVersion int64             `json:"publishedConfigVersion,omitempty"`
	ConfigHash             string            `json:"configHash,omitempty"`
	ValidationStatus       ValidationStatus  `json:"validationStatus"`
	PublicationStatus      PublicationStatus `json:"publicationStatus"`
	HasUnpublishedChanges  bool              `json:"hasUnpublishedChanges"`
	LastTestedAt           *time.Time        `json:"lastTestedAt,omitempty"`
	TestExpiresAt          *time.Time        `json:"testExpiresAt,omitempty"`
	ReviewStatus           ReviewStatus      `json:"reviewStatus"`
	ReviewRequestID        string            `json:"reviewRequestId,omitempty"`
	ReviewRequestVersion   int64             `json:"reviewRequestVersion,omitempty"`
	ReviewNote             string            `json:"reviewNote,omitempty"`
	ReviewRequesterID      string            `json:"reviewRequesterId,omitempty"`
	ReviewReviewerID       string            `json:"reviewReviewerId,omitempty"`
	ReviewSubmittedAt      *time.Time        `json:"reviewSubmittedAt,omitempty"`
	ReviewReviewedAt       *time.Time        `json:"reviewReviewedAt,omitempty"`
	CreatedBy              string            `json:"createdBy,omitempty"`
	UpdatedBy              string            `json:"updatedBy,omitempty"`
	CreatedAt              time.Time         `json:"createdAt"`
	UpdatedAt              time.Time         `json:"updatedAt"`
	Version                int64             `json:"version"`
	RuntimeQuota           Quota             `json:"-"`
}
type Quota struct {
	MaxDataSources, MaxConnectionsPerSource, MaxConcurrentQueries int
	MaxExcelFileBytes                                             int64
}
type TestResult struct {
	ServerVersion   string     `json:"serverVersion"`
	LatencyMS       int64      `json:"latencyMs"`
	ConfigVersionID string     `json:"configVersionId,omitempty"`
	ConfigHash      string     `json:"configHash,omitempty"`
	TestedAt        *time.Time `json:"testedAt,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
}

type ConnectionTestRun struct {
	ID            string
	DataSourceID  string
	ConfigVersion string
	ConfigHash    string
	Status        ValidationStatus
	ServerVersion string
	LatencyMS     int64
	ErrorMessage  string
	StartedAt     time.Time
	CompletedAt   time.Time
	ExpiresAt     *time.Time
}
type QueryResult struct {
	Columns     []string          `json:"columns"`
	Rows        [][]any           `json:"rows"`
	RowCount    int               `json:"rowCount"`
	DurationMS  int64             `json:"durationMs"`
	Warnings    []QueryWarning    `json:"warnings,omitempty"`
	SourceStats []QuerySourceStat `json:"-"`
}

// QueryWarning 是查询成功但存在语义或性能风险时返回的结构化提示。
type QueryWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	JoinID        string `json:"joinId,omitempty"`
	EstimatedRows int    `json:"estimatedRows,omitempty"`
}

// QuerySourceStat 是可信网关采集的跨源节点运行摘要，不接受远端 Connector 注入。
type QuerySourceStat struct {
	NodeID     string
	SubqueryID string
	RowCount   int
	DurationMS int64
	Status     string
}
type SyncResult struct {
	Assets       int             `json:"assets"`
	Watermark    string          `json:"watermark"`
	SnapshotHash string          `json:"snapshotHash"`
	Tables       []MetadataTable `json:"tables,omitempty"`
}
type SampleResult struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"rowCount"`
}
type TableSelection struct {
	CatalogName            string `json:"catalogName"`
	SchemaName             string `json:"schemaName"`
	TableName              string `json:"tableName"`
	TableID                string `json:"-"`
	StructureHash          string `json:"-"`
	LatestEnrichmentStatus string `json:"-"`
}
type MetadataCompletionColumn struct {
	ID   string
	Name string
}
type ManagedMetadataApplyResult struct {
	TableID        string
	Managed        bool
	TablePending   bool
	PendingColumns []MetadataCompletionColumn
}
type ImportedTable struct {
	ID      string           `json:"id"`
	Table   MetadataTable    `json:"table"`
	Samples []map[string]any `json:"-"`
}
type TableRefreshItem struct {
	ID        string `json:"id,omitempty"`
	TableName string `json:"tableName"`
	Status    string `json:"status"`
	Stage     string `json:"stage"`
	Code      string `json:"code,omitempty"`
	Cause     error  `json:"-"`
}
type TableRefreshResult struct {
	Status           string             `json:"status"`
	Total            int                `json:"total"`
	Succeeded        int                `json:"succeeded"`
	TechnicalUpdated int                `json:"technicalUpdated"`
	Failed           int                `json:"failed"`
	Items            []TableRefreshItem `json:"items"`
}
type MetadataTable struct {
	CatalogName       string               `json:"catalogName"`
	SchemaName        string               `json:"schemaName"`
	Name              string               `json:"name"`
	Type              string               `json:"type"`
	SourceComment     string               `json:"sourceComment"`
	EstimatedRowCount *int64               `json:"estimatedRowCount"`
	PrimaryKeyColumns []string             `json:"primaryKeyColumns"`
	Constraints       []MetadataConstraint `json:"constraints"`
	Indexes           []MetadataIndex      `json:"indexes"`
	Columns           []MetadataColumn     `json:"columns"`
}
type MetadataColumn struct {
	Name            string  `json:"name"`
	OrdinalPosition int     `json:"ordinalPosition"`
	SourceComment   string  `json:"sourceComment"`
	NativeType      string  `json:"nativeType"`
	CanonicalType   string  `json:"canonicalType"`
	Length          *int64  `json:"length"`
	Precision       *int    `json:"precision"`
	Scale           *int    `json:"scale"`
	Nullable        bool    `json:"nullable"`
	DefaultValue    *string `json:"defaultValue"`
	PrimaryKey      bool    `json:"primaryKey"`
	ForeignKey      bool    `json:"foreignKey"`
	Unique          bool    `json:"unique"`
}
type MetadataConstraint struct {
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Columns           []string `json:"columns"`
	ReferencedTable   *string  `json:"referencedTable"`
	ReferencedColumns []string `json:"referencedColumns"`
}
type MetadataIndex struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
}
type Connector interface {
	Type() Type
	Test(context.Context, Source) (TestResult, error)
	Sync(context.Context, Source) (SyncResult, error)
	Close(context.Context, Source) error
}
type MetadataSampler interface {
	Sample(context.Context, Source, MetadataTable, int) (SampleResult, error)
}
type FileStructureInspector interface {
	Inspect(context.Context, Source) (ExcelWorkbookInspection, error)
}
type TableCompleter interface {
	// targetTable=false 且 targetColumnIDs 非空时只完善指定变化字段；nil 字段集合表示处理全部活动字段。
	CompleteTable(context.Context, string, string, string, []map[string]any, bool, []string, string, string, string, int64) error
}
type Repository interface {
	Count(context.Context, string) (int, error)
	Create(context.Context, Source) (Source, error)
	List(context.Context, string) ([]Source, error)
	Get(context.Context, string, string) (Source, error)
	Update(context.Context, Source) (Source, error)
	ApplyMetadata(context.Context, Source, SyncResult) error
	ApplySelectedMetadata(context.Context, Source, SyncResult) (map[string]string, error)
	ListActiveTableSelections(context.Context, string, string) ([]TableSelection, error)
	Audit(context.Context, string, string, string, string, any) error
	UpdateStatus(context.Context, string, string, Status, string) error
	Quota(context.Context, string) (Quota, error)
}

// VersionedRepository 是兼容旧仓储的增量能力。管理页读取草稿版本，运行时沿用
// Repository.Get 返回的已发布版本；测试和发布均绑定精确版本及配置摘要。
type VersionedRepository interface {
	GetDraft(context.Context, string, string) (Source, error)
	RecordConnectionTest(context.Context, string, string, ConnectionTestRun) (ConnectionTestRun, error)
	Publish(context.Context, string, string, string, string, string, time.Time) (Source, error)
}

// Validate 校验数据源标识、类型、连接配置与生命周期状态。
func (s Source) Validate() error {
	if s.TenantID == "" || s.Code == "" || s.Name == "" {
		return errors.New("tenant, code and name are required")
	}
	if s.Visibility != "" && s.Visibility != VisibilityPrivate && s.Visibility != VisibilityTenantPublic {
		return errors.New("unsupported data source visibility")
	}
	if !dataSourceCodePattern.MatchString(s.Code) {
		return errors.New("data source code must start with an ASCII letter and contain only ASCII letters, digits, and underscores (maximum 128 characters)")
	}
	switch s.Type {
	case TypeExcel:
		if s.FileAssetID == "" || s.SecretRef != "" {
			return errors.New("excel source requires file asset only")
		}
	case TypeMySQL, TypeOracle:
		if s.SecretRef == "" || s.FileAssetID != "" {
			return errors.New("database source requires secret reference only")
		}
	default:
		return errors.New("unsupported data source type")
	}
	return nil
}
