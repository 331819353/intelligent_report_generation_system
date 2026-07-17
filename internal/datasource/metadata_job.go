package datasource

import (
	"context"
	"errors"
	"time"
)

var (
	ErrMetadataJobNotFound = errors.New("metadata job not found")
	ErrMetadataJobActive   = errors.New("a metadata job is already active for this data source")
)

type MetadataJobKind string
type MetadataRefreshMode string

const (
	MetadataJobImport  MetadataJobKind = "IMPORT"
	MetadataJobRefresh MetadataJobKind = "REFRESH"

	MetadataRefreshFull        MetadataRefreshMode = "FULL"
	MetadataRefreshIncremental MetadataRefreshMode = "INCREMENTAL"
)

// MetadataJob 是页面轮询使用的批任务摘要；进度只来自已落库的逐表终态。
type MetadataJob struct {
	ID           string              `json:"id"`
	DataSourceID string              `json:"dataSourceId"`
	Kind         MetadataJobKind     `json:"kind"`
	Mode         MetadataRefreshMode `json:"mode"`
	Status       string              `json:"status"`
	Stage        string              `json:"stage"`
	Total        int                 `json:"total"`
	Completed    int                 `json:"completed"`
	Succeeded    int                 `json:"succeeded"`
	Skipped      int                 `json:"skipped"`
	Failed       int                 `json:"failed"`
	CurrentTable string              `json:"currentTable"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	ErrorMessage string              `json:"errorMessage,omitempty"`
	CreatedAt    string              `json:"createdAt"`
	StartedAt    string              `json:"startedAt,omitempty"`
	CompletedAt  string              `json:"completedAt,omitempty"`
}

type metadataJobRequest struct {
	TenantID         string
	DataSourceID     string
	RequestedBy      string
	Kind             MetadataJobKind
	Mode             MetadataRefreshMode
	SourceConfigHash string
	Tables           []TableSelection
}

type metadataJobClaim struct {
	MetadataJob
	TenantID         string
	RequestedBy      string
	SourceConfigHash string
}

type metadataJobItem struct {
	ID                       string
	CatalogName              string
	SchemaName               string
	TableName                string
	TableID                  string
	PreviousStructureHash    string
	PreviousEnrichmentStatus string
	Status                   string
}

type metadataJobItemUpdate struct {
	Status       string
	Stage        string
	TableID      string
	ErrorCode    string
	ErrorMessage string
}

// MetadataJobRepository 把 HTTP 提交、worker 租约和页面进度统一落在 PostgreSQL 中。
type MetadataJobRepository interface {
	EnqueueMetadataJob(context.Context, metadataJobRequest) (MetadataJob, error)
	GetMetadataJob(context.Context, string, string, string) (MetadataJob, error)
	LatestActiveMetadataJob(context.Context, string, string) (*MetadataJob, error)
	ListMetadataJobTenantIDs(context.Context) ([]string, error)
	ClaimMetadataJob(context.Context, string, string, time.Duration) (*metadataJobClaim, error)
	ListMetadataJobItems(context.Context, string, string) ([]metadataJobItem, error)
	IsMetadataTableEnriched(context.Context, string, string, string) (bool, error)
	IsMetadataJobItemCompleted(context.Context, string, string, string, string) (bool, error)
	HeartbeatMetadataJob(context.Context, string, string, string, time.Duration) error
	UpdateMetadataJobStage(context.Context, string, string, string, string, time.Duration) error
	UpdateMetadataJobItem(context.Context, string, string, string, string, metadataJobItemUpdate, time.Duration) error
	FinishMetadataJob(context.Context, string, string, string) (MetadataJob, error)
	FailMetadataJob(context.Context, string, string, string, string, string) error
}
