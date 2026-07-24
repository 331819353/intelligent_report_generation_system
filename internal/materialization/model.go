// Package materialization owns the immutable build plan, frozen input snapshots,
// worker leases and atomic activation metadata for PostgreSQL materializations.
//
// The package deliberately has no raw SQL field or execution method. A warehouse
// executor must compile the published dataset DSL into parameterized statements;
// callers cannot smuggle arbitrary client SQL through this control-plane contract.
package materialization

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const PlanVersion = "1.0"

var (
	ErrInvalidRequest      = errors.New("materialization request is invalid")
	ErrNotFound            = errors.New("materialization build was not found")
	ErrConflict            = errors.New("materialization state conflict")
	ErrIdempotencyConflict = errors.New("materialization idempotency key conflict")
	ErrLeaseLost           = errors.New("materialization worker lease was lost")
	ErrInvalidTransition   = errors.New("materialization state transition is invalid")
	ErrQualityGateFailed   = errors.New("materialization quality gate failed")
	ErrCorruptPlan         = errors.New("stored materialization plan is corrupt")
)

type Layer string

const (
	LayerODS Layer = "ODS"
	LayerDWD Layer = "DWD"
	LayerDWS Layer = "DWS"
)

type RunMode string

const (
	RunModeFull        RunMode = "FULL"
	RunModeIncremental RunMode = "INCREMENTAL"
	RunModeBackfill    RunMode = "BACKFILL"
)

type RunStatus string

const (
	RunQueued    RunStatus = "QUEUED"
	RunRunning   RunStatus = "RUNNING"
	RunSucceeded RunStatus = "SUCCEEDED"
	RunFailed    RunStatus = "FAILED"
	RunCancelled RunStatus = "CANCELLED"
)

type NodeStatus string

const (
	NodePending   NodeStatus = "PENDING"
	NodeRunning   NodeStatus = "RUNNING"
	NodeSucceeded NodeStatus = "SUCCEEDED"
	NodeFailed    NodeStatus = "FAILED"
	NodeSkipped   NodeStatus = "SKIPPED"
)

type NodeKind string

const (
	NodeExtract     NodeKind = "EXTRACT"
	NodeStage       NodeKind = "STAGE"
	NodeProject     NodeKind = "PROJECT"
	NodeFilter      NodeKind = "FILTER"
	NodeJoin        NodeKind = "JOIN"
	NodeAggregate   NodeKind = "AGGREGATE"
	NodeMaterialize NodeKind = "MATERIALIZE"
)

type ExecutionEngine string

const (
	EngineSourceDB ExecutionEngine = "SOURCE_DB"
	EnginePostgres ExecutionEngine = "POSTGRES"
)

type InputType string

const (
	InputSourceTable     InputType = "SOURCE_TABLE"
	InputFileVersion     InputType = "FILE_VERSION"
	InputDatasetVersion  InputType = "DATASET_VERSION"
	InputMaterialization InputType = "MATERIALIZATION"
)

// BuildPlan is a safe execution topology. It references frozen inputs by ordinal
// and published dataset identities; executable SQL is intentionally absent.
type BuildPlan struct {
	Version          string     `json:"version"`
	DatasetID        string     `json:"datasetId"`
	DatasetVersionID string     `json:"datasetVersionId"`
	Layer            Layer      `json:"layer"`
	Mode             RunMode    `json:"mode"`
	Nodes            []PlanNode `json:"nodes"`
	Target           TargetPlan `json:"target"`
}

type PlanNode struct {
	ID            string          `json:"id"`
	Kind          NodeKind        `json:"kind"`
	Engine        ExecutionEngine `json:"engine"`
	DependsOn     []string        `json:"dependsOn,omitempty"`
	InputOrdinals []int           `json:"inputOrdinals,omitempty"`
}

type TargetPlan struct {
	Storage        string `json:"storage"`
	AtomicPublish  bool   `json:"atomicPublish"`
	RelationKind   string `json:"relationKind"`
	RefreshMode    string `json:"refreshMode"`
	StableViewName bool   `json:"stableViewName"`
}

// InputSnapshot freezes the exact upstream identity and its schema/content
// fingerprints before work begins. SnapshotJSON may hold bounded watermark
// metadata, never source rows, credentials or executable statements.
type InputSnapshot struct {
	Ordinal             int             `json:"ordinal"`
	Type                InputType       `json:"type"`
	Layer               string          `json:"layer"`
	DataSourceID        string          `json:"dataSourceId,omitempty"`
	DataSourceVersionID string          `json:"dataSourceVersionId,omitempty"`
	MetadataTableID     string          `json:"metadataTableId,omitempty"`
	FileVersionID       string          `json:"fileVersionId,omitempty"`
	DatasetID           string          `json:"datasetId,omitempty"`
	DatasetVersionID    string          `json:"datasetVersionId,omitempty"`
	MaterializationID   string          `json:"materializationId,omitempty"`
	SourceVersion       string          `json:"sourceVersion"`
	SchemaHash          string          `json:"schemaHash"`
	SnapshotHash        string          `json:"snapshotHash"`
	SnapshotJSON        json.RawMessage `json:"snapshot,omitempty"`
	RowCount            *int64          `json:"rowCount,omitempty"`
}

type RegisterRequest struct {
	Plan         BuildPlan
	Inputs       []InputSnapshot
	PartitionKey string
	MaxAttempts  int
}

type PreparedRequest struct {
	RegisterRequest
	PlanJSON          []byte
	PlanHash          string
	InputSnapshotHash string
	RequestHash       string
	IdempotencyKey    string
}

type Run struct {
	ID                string
	TenantID          string
	DatasetID         string
	DatasetVersionID  string
	Layer             Layer
	Mode              RunMode
	Status            RunStatus
	PlanHash          string
	InputSnapshotHash string
	RequestHash       string
	IdempotencyKey    string
	PartitionKey      string
	RequestedBy       string
	Attempt           int
	MaxAttempts       int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	ErrorCode         string
	ErrorMessage      string
}

type Claim struct {
	Run
	Plan           BuildPlan
	Inputs         []InputSnapshot
	WorkerID       string
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type NodeResult struct {
	Status          NodeStatus
	InputRowCount   *int64
	OutputRowCount  *int64
	OutputSizeBytes *int64
	ErrorCode       string
	ErrorMessage    string
}

type QualitySeverity string

const (
	QualityInfo    QualitySeverity = "INFO"
	QualityWarning QualitySeverity = "WARNING"
	QualityError   QualitySeverity = "ERROR"
)

type QualityStatus string

const (
	QualityPassed  QualityStatus = "PASSED"
	QualityFailed  QualityStatus = "FAILED"
	QualitySkipped QualityStatus = "SKIPPED"
)

type QualityResult struct {
	NodeID             string
	RuleCode           string
	RuleVersion        string
	RuleDefinitionHash string
	Scope              string
	FieldID            string
	Severity           QualitySeverity
	Status             QualityStatus
	Expectation        json.RawMessage
	Observed           json.RawMessage
	Message            string
}

type PhysicalIdentifier struct {
	Schema          string
	Name            string
	PublishedSchema string
	PublishedName   string
}

type Activation struct {
	Physical     PhysicalIdentifier
	RelationKind string
	SchemaHash   string
	SnapshotHash string
	RowCount     int64
	SizeBytes    int64
	Watermark    json.RawMessage
	Quality      []QualityResult
}

type Materialization struct {
	ID               string
	TenantID         string
	DatasetID        string
	DatasetVersionID string
	BuildRunID       string
	Layer            Layer
	Status           string
	Physical         PhysicalIdentifier
	SchemaHash       string
	SnapshotHash     string
	RowCount         int64
	SizeBytes        int64
	ActivatedAt      time.Time
}

// Repository is intentionally expressed in domain objects rather than SQL
// fragments. Every mutation after Claim is fenced by a random lease token.
type Repository interface {
	Register(context.Context, string, string, RegisterRequest) (Run, bool, error)
	Claim(context.Context, string, string, time.Duration) (*Claim, error)
	Heartbeat(context.Context, Claim, time.Duration) (Claim, error)
	StartNode(context.Context, Claim, string) error
	FinishNode(context.Context, Claim, string, NodeResult) error
	Fail(context.Context, Claim, string, string, []QualityResult) error
	Activate(context.Context, Claim, Activation) (Materialization, error)
}
