// Package metriccandidate derives reviewable metric drafts from immutable dataset versions.
// It is deliberately persistence-free: callers decide how candidates are stored, reviewed,
// or materialized through the metric service.
package metriccandidate

import (
	"context"
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/metric"
)

var (
	ErrNotFound       = errors.New("metric candidate not found")
	ErrConflict       = errors.New("metric candidate version conflict")
	ErrInvalidRequest = errors.New("metric candidate request is invalid")
	ErrNotReviewable  = errors.New("metric candidate is not reviewable")
	ErrBlocked        = errors.New("metric candidate is blocked")
)

// CandidateStatus describes whether a deterministic candidate can move to review or is
// known to be incompatible with the current metric execution boundary.
type CandidateStatus string

const (
	CandidateStatusReady       CandidateStatus = "READY"
	CandidateStatusNeedsReview CandidateStatus = "NEEDS_REVIEW"
	CandidateStatusBlocked     CandidateStatus = "BLOCKED"
	CandidateStatusAccepted    CandidateStatus = "ACCEPTED"
	CandidateStatusRejected    CandidateStatus = "REJECTED"
)

// TaskStatus is the stable lifecycle vocabulary for a caller-managed extraction task.
// Extract is synchronous and returns SUCCEEDED or PARTIAL; the remaining values let an
// asynchronous orchestrator expose a consistent domain status without changing this package.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "PENDING"
	TaskStatusRunning   TaskStatus = "RUNNING"
	TaskStatusSucceeded TaskStatus = "SUCCEEDED"
	TaskStatusPartial   TaskStatus = "PARTIAL"
	TaskStatusFailed    TaskStatus = "FAILED"
)

// Confidence states how directly the proposed metric aggregation follows from the dataset.
type Confidence string

const (
	ConfidenceHigh   Confidence = "HIGH"
	ConfidenceMedium Confidence = "MEDIUM"
	ConfidenceLow    Confidence = "LOW"
)

// Evidence records a normalized source fact used to derive a candidate. Path always points
// into the exact dataset version or its immutable envelope; Value is display-safe metadata,
// never sampled business data.
type Evidence struct {
	Code  string `json:"code"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

// CandidateDraft is a deterministic, locally valid metric definition plus provenance and
// current execution-boundary findings. BLOCKED candidates still carry a valid Definition so
// they can be inspected and revisited after the execution model is extended.
type CandidateDraft struct {
	DatasetID        string            `json:"datasetId"`
	DatasetVersionID string            `json:"datasetVersionId"`
	SourceFieldID    string            `json:"sourceFieldId"`
	SourceFieldCode  string            `json:"sourceFieldCode"`
	Status           CandidateStatus   `json:"status"`
	Confidence       Confidence        `json:"confidence"`
	Definition       metric.Definition `json:"definition"`
	DefinitionHash   string            `json:"definitionHash"`
	Fingerprint      string            `json:"fingerprint"`
	Evidence         []Evidence        `json:"evidence"`
	Warnings         []string          `json:"warnings"`
	BlockReasons     []string          `json:"blockReasons"`
}

// ExtractionResult is the complete deterministic output for one exact dataset version.
// PARTIAL means at least one candidate needs review or is blocked; it is not a task failure.
type ExtractionResult struct {
	Status           TaskStatus       `json:"status"`
	DatasetID        string           `json:"datasetId"`
	DatasetVersionID string           `json:"datasetVersionId"`
	DSLHash          string           `json:"dslHash"`
	Candidates       []CandidateDraft `json:"candidates"`
	Warnings         []string         `json:"warnings"`
}

// CandidateEvidence 是 API 与持久层使用的审计证据结构。它把规则引擎的证据
// 代码、精确 DSL 路径和值分别呈现为属性、来源和说明。
type CandidateEvidence struct {
	Property string `json:"property"`
	Source   string `json:"source"`
	Detail   string `json:"detail"`
}

// Candidate 是候选审核目录中的持久记录。ProposedDefinition 已由规则引擎和
// metric.Prepare 校验，但在接受前不会进入正式指标目录。
type Candidate struct {
	ID                 string              `json:"id"`
	DatasetID          string              `json:"datasetId"`
	DatasetVersionID   string              `json:"datasetVersionId"`
	DSLHash            string              `json:"dslHash"`
	Name               string              `json:"name"`
	Code               string              `json:"code"`
	Description        string              `json:"description"`
	Status             CandidateStatus     `json:"status"`
	Method             string              `json:"method"`
	Confidence         float64             `json:"confidence"`
	ProposedDefinition json.RawMessage     `json:"proposedDefinition"`
	SourceFieldIDs     []string            `json:"sourceFieldIds"`
	Evidence           []CandidateEvidence `json:"evidence"`
	Assumptions        []string            `json:"assumptions"`
	Warnings           []string            `json:"warnings"`
	BlockReasons       []string            `json:"blockReasons"`
	Fingerprint        string              `json:"fingerprint"`
	Version            int64               `json:"version"`
	AcceptedMetricID   string              `json:"acceptedMetricId,omitempty"`
	DecisionReason     string              `json:"decisionReason,omitempty"`
	CreatedAt          string              `json:"createdAt"`
	UpdatedAt          string              `json:"updatedAt"`
}

type ListFilter struct {
	Status    string
	DatasetID string
	Limit     int
	Offset    int
}

type RejectInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

type AcceptInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type AcceptResult struct {
	Candidate Candidate     `json:"candidate"`
	Metric    metric.Record `json:"metric"`
}

// Store 是人工审核 API 所需的最小持久化边界。
type Store interface {
	List(context.Context, string, ListFilter) ([]Candidate, int, error)
	Get(context.Context, string, string) (Candidate, error)
	Reject(context.Context, string, string, string, RejectInput) (Candidate, error)
}

// MetricCreator 只允许把已审核候选物化为草稿；它不暴露发布能力。
type MetricCreator interface {
	CreateFromCandidate(context.Context, string, string, string, int64, metric.CreateInput) (metric.Record, error)
}
