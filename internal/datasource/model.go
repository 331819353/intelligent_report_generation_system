package datasource

import (
	"context"
	"errors"
)

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

type Source struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenantId"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Type         Type           `json:"type"`
	Status       Status         `json:"status"`
	Config       map[string]any `json:"config"`
	SecretRef    string         `json:"secretRef,omitempty"`
	FileAssetID  string         `json:"fileAssetId,omitempty"`
	Version      int64          `json:"version"`
	RuntimeQuota Quota          `json:"-"`
}
type Quota struct {
	MaxDataSources, MaxConnectionsPerSource, MaxConcurrentQueries int
	MaxExcelFileBytes                                             int64
}
type TestResult struct {
	ServerVersion string `json:"serverVersion"`
	LatencyMS     int64  `json:"latencyMs"`
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
type Repository interface {
	Count(context.Context, string) (int, error)
	Create(context.Context, Source) (Source, error)
	List(context.Context, string) ([]Source, error)
	Get(context.Context, string, string) (Source, error)
	Update(context.Context, Source) (Source, error)
	ApplyMetadata(context.Context, Source, SyncResult) error
	Audit(context.Context, string, string, string, string, any) error
	UpdateStatus(context.Context, string, string, Status, string) error
	Quota(context.Context, string) (Quota, error)
}

// Validate 校验数据源标识、类型、连接配置与生命周期状态。
func (s Source) Validate() error {
	if s.TenantID == "" || s.Code == "" || s.Name == "" {
		return errors.New("tenant, code and name are required")
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
