// Package semanticcatalog rebuilds deterministic semantic documents and embeds
// them from the transactional semantic_change_outbox.
package semanticcatalog

import (
	"context"
	"errors"
	"time"
)

const (
	DocumentVersion    = "semantic-catalog-document-v1"
	MaxBatchSize       = 32
	VectorDimensions   = 2560
	maxDocumentBytes   = 240 << 10
	maxBatchInputBytes = 240 << 10
	maxWorkerIDLength  = 128
)

var (
	ErrInvalidRequest = errors.New("semantic catalog request is invalid")
	ErrLeaseLost      = errors.New("semantic catalog event lease was lost")
	ErrSubjectChanged = errors.New("semantic catalog subject changed")
)

const (
	SubjectTag             = "TAG"
	SubjectDatasetVersion  = "DATASET_VERSION"
	SubjectDatasetField    = "DATASET_FIELD"
	SubjectDimension       = "DIMENSION"
	SubjectDimensionMember = "DIMENSION_MEMBER"
	SubjectMetricVersion   = "METRIC_VERSION"
	SubjectDocument        = "SEMANTIC_DOCUMENT"
)

const (
	EventRebuild = "REBUILD"
	EventDelete  = "DELETE"
)

// Claim is the immutable fencing identity returned by a successful outbox claim.
// Every subsequent write must match all four fencing values: event ID, owner,
// lease token and event version.
type Claim struct {
	ID           string
	TenantID     string
	SubjectType  string
	SubjectRef   string
	EventKind    string
	EventVersion int64
	Attempt      int
	MaxAttempts  int
	LeaseToken   string
}

// Subject identifies one semantic_documents row without carrying arbitrary SQL,
// source samples, credentials or mutable source configuration.
type Subject struct {
	Type                   string
	TagID                  string
	DatasetID              string
	DatasetVersionID       string
	DatasetFieldID         string
	DimensionID            string
	DimensionMemberID      string
	MetricID               string
	MetricVersionID        string
	MetricDatasetVersionID string
}

// Work is a bounded, deterministic snapshot prepared for one claimed event.
// InputHash binds a later completion to the exact text that was embedded.
type Work struct {
	Claim
	Subject
	DocumentID string
	Text       string
	InputHash  string
	Missing    bool
	Current    bool
	// Member values remain searchable through the tenant-local exact index and
	// aliases, but are never sent to a remote embedding provider.
	EmbeddingSuppressed bool
}

// Store owns tenant-scoped outbox claims, deterministic document preparation and
// fenced state transitions.
type Store interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimBatch(context.Context, string, string, time.Duration, int, bool) ([]Claim, error)
	Heartbeat(context.Context, Claim, string, time.Duration) error
	Prepare(context.Context, Claim, string, string) (Work, error)
	ApplyDocument(context.Context, Work, string) error
	Acknowledge(context.Context, Work, string) error
	CompleteEmbedding(context.Context, Work, string, string, []float32) error
	Fail(context.Context, Claim, string, string) error
}
