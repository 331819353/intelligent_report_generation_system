package materialization

import (
	"context"
	"time"
)

const (
	DefaultBuildPageLimit = 50
	MaxBuildPageLimit     = 100
)

// CreateBuildInput is the complete public write contract for registering a
// materialization. Execution plans, input snapshots, SQL and physical
// identifiers are deliberately absent and are always derived by the server.
type CreateBuildInput struct {
	Mode         RunMode `json:"mode,omitempty"`
	PartitionKey string  `json:"partitionKey,omitempty"`
	MaxAttempts  *int    `json:"maxAttempts,omitempty"`
}

type RegisterCurrentRequest struct {
	Mode         RunMode
	PartitionKey string
	MaxAttempts  int
}

// BuildNode is a safe control-plane projection of one persisted node run. It
// contains no SQL and no physical relation identity.
type BuildNode struct {
	ID              string     `json:"id"`
	Kind            NodeKind   `json:"kind"`
	Engine          string     `json:"engine"`
	Status          NodeStatus `json:"status"`
	Attempt         int        `json:"attempt"`
	InputRowCount   *int64     `json:"inputRowCount,omitempty"`
	OutputRowCount  *int64     `json:"outputRowCount,omitempty"`
	OutputSizeBytes *int64     `json:"outputSizeBytes,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
}

// BuildInput exposes only the immutable identities and hashes frozen by the
// server. Internal snapshot JSON and source/warehouse physical names remain
// private to the worker.
type BuildInput struct {
	Ordinal             int       `json:"ordinal"`
	Type                InputType `json:"type"`
	Layer               string    `json:"layer"`
	DataSourceID        string    `json:"dataSourceId,omitempty"`
	DataSourceVersionID string    `json:"dataSourceVersionId,omitempty"`
	MetadataTableID     string    `json:"metadataTableId,omitempty"`
	FileVersionID       string    `json:"fileVersionId,omitempty"`
	DatasetID           string    `json:"datasetId,omitempty"`
	DatasetVersionID    string    `json:"datasetVersionId,omitempty"`
	MaterializationID   string    `json:"materializationId,omitempty"`
	SourceVersion       string    `json:"sourceVersion"`
	SchemaHash          string    `json:"schemaHash"`
	SnapshotHash        string    `json:"snapshotHash"`
	RowCount            *int64    `json:"rowCount,omitempty"`
}

type BuildMaterialization struct {
	ID               string     `json:"id"`
	DatasetVersionID string     `json:"datasetVersionId"`
	Layer            Layer      `json:"layer"`
	Status           string     `json:"status"`
	SchemaHash       string     `json:"schemaHash"`
	SnapshotHash     string     `json:"snapshotHash"`
	RowCount         *int64     `json:"rowCount,omitempty"`
	SizeBytes        *int64     `json:"sizeBytes,omitempty"`
	ActivatedAt      *time.Time `json:"activatedAt,omitempty"`
}

type Build struct {
	ID                string     `json:"id"`
	DatasetID         string     `json:"datasetId"`
	DatasetVersionID  string     `json:"datasetVersionId"`
	Layer             Layer      `json:"layer"`
	Mode              RunMode    `json:"mode"`
	Status            RunStatus  `json:"status"`
	PlanHash          string     `json:"planHash"`
	InputSnapshotHash string     `json:"inputSnapshotHash"`
	PartitionKey      string     `json:"partitionKey"`
	RequestedBy       string     `json:"requestedBy"`
	Attempt           int        `json:"attempt"`
	MaxAttempts       int        `json:"maxAttempts"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	ErrorCode         string     `json:"errorCode,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
}

type BuildDetail struct {
	Build
	Inputs          []BuildInput          `json:"inputs"`
	Nodes           []BuildNode           `json:"nodes"`
	Materialization *BuildMaterialization `json:"materialization,omitempty"`
}

type BuildPage struct {
	Items  []Build `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

type ControlStore interface {
	RegisterCurrent(
		context.Context,
		string,
		string,
		string,
		RegisterCurrentRequest,
	) (Run, bool, error)
	ListBuilds(context.Context, string, string, int, int) ([]Run, int, error)
	GetBuild(context.Context, string, string, string) (BuildDetail, error)
	CancelQueued(context.Context, string, string, string, string) (Run, error)
}

func buildFromRun(run Run) Build {
	return Build{
		ID: run.ID, DatasetID: run.DatasetID,
		DatasetVersionID: run.DatasetVersionID,
		Layer:            run.Layer, Mode: run.Mode, Status: run.Status,
		PlanHash: run.PlanHash, InputSnapshotHash: run.InputSnapshotHash,
		PartitionKey: run.PartitionKey, RequestedBy: run.RequestedBy,
		Attempt: run.Attempt, MaxAttempts: run.MaxAttempts,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
	}
}
