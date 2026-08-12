// Package decision implements the M20 decision-support bounded context. It
// records governed analysis evidence, approvals, actions and outcome reviews;
// it deliberately has no adapter for executing external business transactions.
package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/askdata"
)

const SchemaVersion = "1.0"

var (
	ErrInvalid           = errors.New("decision input is invalid")
	ErrNotFound          = errors.New("decision was not found")
	ErrForbidden         = errors.New("decision action is forbidden")
	ErrConflict          = errors.New("decision record changed concurrently")
	ErrIllegalTransition = errors.New("decision state transition is not allowed")
	ErrEvidenceInvalid   = errors.New("decision evidence is not a verified immutable artifact")
	ErrPolicyUnavailable = errors.New("decision approval policy is not configured")
	ErrSelfApproval      = errors.New("decision submitter cannot approve their own decision")
	ErrOutcomeBlocked    = errors.New("decision outcome review cannot proceed")
)

type Identity struct {
	TenantID askdata.ID
	DomainID askdata.ID
	ActorID  askdata.ID
}

func (identity Identity) Validate() error {
	for _, value := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ActorID} {
		parsed, err := uuid.Parse(string(value))
		if err != nil || parsed.String() != string(value) {
			return ErrInvalid
		}
	}
	return nil
}

type Status string

const (
	StatusDraft       Status = "DRAFT"
	StatusInReview    Status = "IN_REVIEW"
	StatusApproved    Status = "APPROVED"
	StatusRejected    Status = "REJECTED"
	StatusInExecution Status = "IN_EXECUTION"
	StatusReviewDue   Status = "REVIEW_DUE"
	StatusClosed      Status = "CLOSED"
	StatusReopened    Status = "REOPENED"
	StatusCanceled    Status = "CANCELED"
)

type EvidenceMode string

const (
	EvidencePlatformVerified EvidenceMode = "PLATFORM_VERIFIED"
	EvidenceManual           EvidenceMode = "MANUAL_WITHOUT_PLATFORM_EVIDENCE"
)

type SourceType string

const (
	SourceAnswerArtifact  SourceType = "ANSWER_ARTIFACT"
	SourceReportVersion   SourceType = "REPORT_VERSION"
	SourceInsightArtifact SourceType = "INSIGHT_ARTIFACT"
)

type ActionStatus string

const (
	ActionTODO     ActionStatus = "TODO"
	ActionDoing    ActionStatus = "DOING"
	ActionBlocked  ActionStatus = "BLOCKED"
	ActionDone     ActionStatus = "DONE"
	ActionCanceled ActionStatus = "CANCELED"
)

type ReviewStatus string

const (
	ReviewPending      ReviewStatus = "PENDING"
	ReviewGenerated    ReviewStatus = "GENERATED"
	ReviewConfirmed    ReviewStatus = "CONFIRMED"
	ReviewInconclusive ReviewStatus = "INCONCLUSIVE"
)

type Conclusion string

const (
	ConclusionAchieved     Conclusion = "ACHIEVED"
	ConclusionPartial      Conclusion = "PARTIALLY_ACHIEVED"
	ConclusionNotAchieved  Conclusion = "NOT_ACHIEVED"
	ConclusionInconclusive Conclusion = "INCONCLUSIVE"
)

type Decision struct {
	SchemaVersion     string       `json:"schemaVersion"`
	ID                askdata.ID   `json:"id"`
	OwnerUserID       askdata.ID   `json:"ownerUserId"`
	CreatedBy         askdata.ID   `json:"createdBy"`
	Title             string       `json:"title"`
	Question          string       `json:"question"`
	Decision          string       `json:"decision"`
	ExpectedEffect    string       `json:"expectedEffect"`
	Risks             []string     `json:"risks"`
	Status            Status       `json:"status"`
	EvidenceMode      EvidenceMode `json:"evidenceMode"`
	ApprovalPolicyID  string       `json:"approvalPolicyId"`
	RequiredApprovals int          `json:"requiredApprovals"`
	ReviewAt          time.Time    `json:"reviewAt"`
	TerminalReason    string       `json:"terminalReason,omitempty"`
	RecordVersion     int64        `json:"recordVersion"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
}

type Option struct {
	ID          askdata.ID `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Selected    bool       `json:"selected"`
}

type Evidence struct {
	SchemaVersion       string              `json:"schemaVersion"`
	ID                  askdata.ID          `json:"id"`
	SourceType          SourceType          `json:"sourceType"`
	SourceID            askdata.ID          `json:"sourceId"`
	SourceHash          askdata.ContentHash `json:"sourceHash"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticReleaseHash askdata.ContentHash `json:"semanticReleaseHash"`
	DataSnapshotID      askdata.ID          `json:"dataSnapshotId,omitempty"`
	AsOf                time.Time           `json:"asOf"`
	PolicyScopeHash     askdata.ContentHash `json:"policyScopeHash"`
	Summary             string              `json:"summary"`
	Verified            bool                `json:"verified"`
	CreatedAt           time.Time           `json:"createdAt"`
}

type Approval struct {
	ID             askdata.ID `json:"id"`
	ApproverUserID askdata.ID `json:"approverUserId"`
	SequenceNo     int        `json:"sequenceNo"`
	Status         string     `json:"status"`
	Comment        string     `json:"comment"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
}

type Action struct {
	SchemaVersion      string       `json:"schemaVersion"`
	ID                 askdata.ID   `json:"id"`
	DecisionID         askdata.ID   `json:"decisionId"`
	Title              string       `json:"title"`
	Description        string       `json:"description"`
	AssigneeUserID     askdata.ID   `json:"assigneeUserId"`
	DueAt              time.Time    `json:"dueAt"`
	Status             ActionStatus `json:"status"`
	BlockReason        string       `json:"blockReason,omitempty"`
	CompletionEvidence string       `json:"completionEvidence,omitempty"`
	DeliverableRefs    []string     `json:"deliverableRefs"`
	RecordVersion      int64        `json:"recordVersion"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

type TargetDirection string

const (
	DirectionIncrease TargetDirection = "INCREASE"
	DirectionDecrease TargetDirection = "DECREASE"
	DirectionAtLeast  TargetDirection = "AT_LEAST"
	DirectionAtMost   TargetDirection = "AT_MOST"
	DirectionRange    TargetDirection = "RANGE"
)

type OutcomeMetric struct {
	ID                     askdata.ID          `json:"id"`
	DecisionID             askdata.ID          `json:"decisionId"`
	MetricVersionID        string              `json:"metricVersionId"`
	SemanticIR             json.RawMessage     `json:"semanticIR,omitempty"`
	SemanticIRHash         askdata.ContentHash `json:"semanticIRHash"`
	SemanticReleaseID      askdata.ID          `json:"semanticReleaseId"`
	SemanticReleaseHash    askdata.ContentHash `json:"semanticReleaseHash"`
	BaselineValue          string              `json:"baselineValue"`
	TargetDirection        TargetDirection     `json:"targetDirection"`
	TargetValue            *string             `json:"targetValue,omitempty"`
	TargetUpperValue       *string             `json:"targetUpperValue,omitempty"`
	ReviewAt               time.Time           `json:"reviewAt"`
	AttributionNote        string              `json:"attributionNote"`
	CurrentValue           *string             `json:"currentValue,omitempty"`
	CurrentResultHash      askdata.ContentHash `json:"currentResultHash,omitempty"`
	CurrentPolicyScopeHash askdata.ContentHash `json:"currentPolicyScopeHash,omitempty"`
	CurrentAsOf            *time.Time          `json:"currentAsOf,omitempty"`
	Drifted                bool                `json:"drifted"`
	RefreshStatus          string              `json:"refreshStatus"`
	RecordVersion          int64               `json:"recordVersion"`
}

type OutcomeReview struct {
	SchemaVersion string       `json:"schemaVersion"`
	ID            askdata.ID   `json:"id"`
	DecisionID    askdata.ID   `json:"decisionId"`
	Status        ReviewStatus `json:"status"`
	Conclusion    Conclusion   `json:"conclusion,omitempty"`
	Notes         string       `json:"notes"`
	GeneratedAt   *time.Time   `json:"generatedAt,omitempty"`
	ConfirmedBy   askdata.ID   `json:"confirmedBy,omitempty"`
	ConfirmedAt   *time.Time   `json:"confirmedAt,omitempty"`
	RecordVersion int64        `json:"recordVersion"`
}

type Event struct {
	ID            askdata.ID     `json:"id"`
	EventNo       int64          `json:"eventNo"`
	EventType     string         `json:"eventType"`
	ActorUserID   askdata.ID     `json:"actorUserId"`
	FromStatus    string         `json:"fromStatus"`
	ToStatus      string         `json:"toStatus"`
	Reason        string         `json:"reason"`
	Payload       map[string]any `json:"payload"`
	RecordVersion int64          `json:"recordVersion"`
	CreatedAt     time.Time      `json:"createdAt"`
}

type Aggregate struct {
	Decision  Decision        `json:"decision"`
	Options   []Option        `json:"options"`
	Evidence  []Evidence      `json:"evidence"`
	Approvals []Approval      `json:"approvals"`
	Actions   []Action        `json:"actions"`
	Metrics   []OutcomeMetric `json:"outcomeMetrics"`
	Review    *OutcomeReview  `json:"outcomeReview,omitempty"`
	Events    []Event         `json:"events,omitempty"`
}

type CreateInput struct {
	OwnerUserID      askdata.ID      `json:"ownerUserId"`
	Title            string          `json:"title"`
	Question         string          `json:"question"`
	Decision         string          `json:"decision"`
	ExpectedEffect   string          `json:"expectedEffect"`
	Risks            []string        `json:"risks"`
	EvidenceMode     EvidenceMode    `json:"evidenceMode"`
	ApprovalPolicyID string          `json:"approvalPolicyId"`
	ReviewAt         time.Time       `json:"reviewAt"`
	Options          []OptionInput   `json:"options"`
	Evidence         []EvidenceInput `json:"evidence"`
}

type OptionInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Selected    bool   `json:"selected"`
}

type EvidenceInput struct {
	SourceType          SourceType          `json:"sourceType"`
	SourceID            askdata.ID          `json:"sourceId"`
	SourceHash          askdata.ContentHash `json:"sourceHash"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticReleaseHash askdata.ContentHash `json:"semanticReleaseHash"`
	DataSnapshotID      askdata.ID          `json:"dataSnapshotId"`
	AsOf                time.Time           `json:"asOf"`
	PolicyScopeHash     askdata.ContentHash `json:"policyScopeHash"`
	Summary             string              `json:"summary"`
}

type UpdateInput struct {
	ExpectedVersion int64     `json:"expectedVersion"`
	Title           string    `json:"title"`
	Question        string    `json:"question"`
	Decision        string    `json:"decision"`
	ExpectedEffect  string    `json:"expectedEffect"`
	Risks           []string  `json:"risks"`
	ReviewAt        time.Time `json:"reviewAt"`
}

type CreateActionInput struct {
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	AssigneeUserID  askdata.ID `json:"assigneeUserId"`
	DueAt           time.Time  `json:"dueAt"`
	DeliverableRefs []string   `json:"deliverableRefs"`
}

type TransitionActionInput struct {
	ExpectedVersion    int64        `json:"expectedVersion"`
	Target             ActionStatus `json:"target"`
	Reason             string       `json:"reason"`
	CompletionEvidence string       `json:"completionEvidence"`
}

type AddMetricInput struct {
	MetricVersionID     string              `json:"metricVersionId"`
	SemanticIR          json.RawMessage     `json:"semanticIR"`
	SemanticIRHash      askdata.ContentHash `json:"semanticIRHash"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticReleaseHash askdata.ContentHash `json:"semanticReleaseHash"`
	BaselineValue       string              `json:"baselineValue"`
	TargetDirection     TargetDirection     `json:"targetDirection"`
	TargetValue         *string             `json:"targetValue"`
	TargetUpperValue    *string             `json:"targetUpperValue"`
	ReviewAt            time.Time           `json:"reviewAt"`
	AttributionNote     string              `json:"attributionNote"`
}

type ConfirmOutcomeInput struct {
	ExpectedVersion int64      `json:"expectedVersion"`
	Conclusion      Conclusion `json:"conclusion"`
	Notes           string     `json:"notes"`
}

func (input CreateInput) Validate(now time.Time) error {
	if !validUUID(input.OwnerUserID) || !validText(input.Title, 1, 256) ||
		!validText(input.Question, 1, 4096) || len(input.Decision) > 8192 ||
		len(input.ExpectedEffect) > 4096 || !validText(input.ApprovalPolicyID, 1, 128) ||
		input.ReviewAt.IsZero() || input.ReviewAt.Before(now.Add(-time.Minute)) || len(input.Options) > 32 || len(input.Risks) > 64 {
		return ErrInvalid
	}
	for _, risk := range input.Risks {
		if !validText(risk, 1, 1024) {
			return ErrInvalid
		}
	}
	selected := 0
	for _, option := range input.Options {
		if !validText(option.Title, 1, 256) || len(option.Description) > 4096 {
			return ErrInvalid
		}
		if option.Selected {
			selected++
		}
	}
	if selected > 1 {
		return ErrInvalid
	}
	switch input.EvidenceMode {
	case EvidencePlatformVerified:
		if len(input.Evidence) == 0 || len(input.Evidence) > 32 {
			return ErrEvidenceInvalid
		}
	case EvidenceManual:
		if len(input.Evidence) != 0 {
			return ErrEvidenceInvalid
		}
	default:
		return ErrInvalid
	}
	for _, evidence := range input.Evidence {
		if evidence.Validate() != nil {
			return ErrEvidenceInvalid
		}
	}
	return nil
}

func (input EvidenceInput) Validate() error {
	if input.SourceType != SourceAnswerArtifact && input.SourceType != SourceReportVersion && input.SourceType != SourceInsightArtifact ||
		!validUUID(input.SourceID) || input.SourceHash.Validate() != nil || !validUUID(input.SemanticReleaseID) ||
		input.SemanticReleaseHash.Validate() != nil || input.PolicyScopeHash.Validate() != nil || input.AsOf.IsZero() ||
		!validText(input.Summary, 1, 2048) || (input.DataSnapshotID != "" && !validUUID(input.DataSnapshotID)) {
		return ErrEvidenceInvalid
	}
	return nil
}

func validUUID(value askdata.ID) bool {
	parsed, err := uuid.Parse(string(value))
	return err == nil && parsed.String() == string(value)
}

func validText(value string, minimum, maximum int) bool {
	if value != strings.TrimSpace(value) || len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func actionTransitionAllowed(from, to ActionStatus) bool {
	if from == to {
		return false
	}
	switch from {
	case ActionTODO:
		return to == ActionDoing || to == ActionCanceled
	case ActionDoing:
		return to == ActionBlocked || to == ActionDone || to == ActionCanceled
	case ActionBlocked:
		return to == ActionDoing || to == ActionCanceled
	case ActionDone, ActionCanceled:
		return to == ActionDoing
	default:
		return false
	}
}

func statusTransitionAllowed(from, to Status) bool {
	if from == to {
		return false
	}
	switch from {
	case StatusDraft:
		return to == StatusInReview || to == StatusCanceled
	case StatusInReview:
		return to == StatusApproved || to == StatusRejected || to == StatusCanceled
	case StatusApproved:
		return to == StatusInExecution || to == StatusCanceled
	case StatusInExecution, StatusReopened:
		return to == StatusReviewDue || to == StatusCanceled
	case StatusReviewDue:
		return to == StatusClosed || to == StatusReopened || to == StatusCanceled
	case StatusRejected:
		return to == StatusReopened
	case StatusClosed:
		return to == StatusReopened
	default:
		return false
	}
}

func validateNoForbiddenJSON(raw json.RawMessage) error {
	var object map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil {
		return ErrInvalid
	}
	for _, key := range []string{"sql", "rawSQL", "rawSql", "rows", "resultRows", "chainOfThought", "reasoning"} {
		if _, exists := object[key]; exists {
			return fmt.Errorf("%w: forbidden field %s", ErrInvalid, key)
		}
	}
	return nil
}
