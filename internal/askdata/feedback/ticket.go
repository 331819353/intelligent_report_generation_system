package feedback

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

var (
	ErrInvalid           = errors.New("invalid feedback ticket")
	ErrNotFound          = errors.New("feedback ticket not found")
	ErrConflict          = errors.New("feedback ticket conflict")
	ErrIllegalTransition = errors.New("illegal feedback ticket transition")
)

type IssueType string

const (
	IssueMetric        IssueType = "METRIC"
	IssueDimension     IssueType = "DIMENSION"
	IssueMember        IssueType = "MEMBER"
	IssueTime          IssueType = "TIME"
	IssueComparison    IssueType = "COMPARISON"
	IssueResult        IssueType = "RESULT"
	IssueNarrative     IssueType = "NARRATIVE"
	IssueUnderstanding IssueType = "UNDERSTANDING"
	IssuePermission    IssueType = "PERMISSION"
	IssueOther         IssueType = "OTHER"
)

type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
)

type Stage string

const (
	StageUnderstanding Stage = "UNDERSTANDING"
	StageRetrieval     Stage = "RETRIEVAL"
	StageBinding       Stage = "BINDING"
	StageGraph         Stage = "GRAPH"
	StageCompile       Stage = "COMPILE"
	StageExecution     Stage = "EXECUTION"
	StageData          Stage = "DATA"
	StageNarrative     Stage = "NARRATIVE"
)

type Status string

const (
	StatusNew         Status = "NEW"
	StatusTriaged     Status = "TRIAGED"
	StatusAccepted    Status = "ACCEPTED"
	StatusRejected    Status = "REJECTED"
	StatusFixProposed Status = "FIX_PROPOSED"
	StatusFixApproved Status = "FIX_APPROVED"
	StatusInRelease   Status = "IN_RELEASE"
	StatusVerified    Status = "VERIFIED"
	StatusClosed      Status = "CLOSED"
)

type Identity struct {
	TenantID, DomainID, ActorID askdata.ID
}

func (identity Identity) Validate() error {
	for _, id := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ActorID} {
		if id.Validate() != nil {
			return ErrInvalid
		}
	}
	return nil
}

type Ticket struct {
	ID                     askdata.ID `json:"id"`
	TenantID               askdata.ID `json:"tenantId"`
	DomainID               askdata.ID `json:"domainId"`
	QueryFeedbackID        askdata.ID `json:"queryFeedbackId"`
	QuestionRunID          askdata.ID `json:"questionRunId"`
	ReporterUserID         askdata.ID `json:"reporterUserId"`
	IssueType              IssueType  `json:"issueType"`
	Severity               Severity   `json:"severity"`
	SuggestedStage         Stage      `json:"suggestedStage"`
	AttributedStage        Stage      `json:"attributedStage,omitempty"`
	Status                 Status     `json:"status"`
	OwnerUserID            askdata.ID `json:"ownerUserId,omitempty"`
	SLADueAt               time.Time  `json:"slaDueAt"`
	LinkedReleaseID        askdata.ID `json:"linkedReleaseId,omitempty"`
	LinkedEvaluationCaseID askdata.ID `json:"linkedEvaluationCaseId,omitempty"`
	ResolutionNote         string     `json:"resolutionNote,omitempty"`
	UserResponse           string     `json:"userResponse,omitempty"`
	FixCandidateType       string     `json:"fixCandidateType,omitempty"`
	FixCandidateID         askdata.ID `json:"fixCandidateId,omitempty"`
	RecordVersion          int64      `json:"recordVersion"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	ClosedAt               *time.Time `json:"closedAt,omitempty"`
}

type Event struct {
	ID          askdata.ID     `json:"id"`
	TicketID    askdata.ID     `json:"ticketId"`
	ActorUserID askdata.ID     `json:"actorUserId"`
	EventNo     int64          `json:"eventNo"`
	FromStatus  Status         `json:"fromStatus,omitempty"`
	ToStatus    Status         `json:"toStatus"`
	Details     map[string]any `json:"details"`
	CreatedAt   time.Time      `json:"createdAt"`
}

var transitions = map[Status]map[Status]bool{
	StatusNew:         {StatusTriaged: true},
	StatusTriaged:     {StatusAccepted: true, StatusRejected: true},
	StatusAccepted:    {StatusFixProposed: true},
	StatusFixProposed: {StatusFixApproved: true},
	StatusFixApproved: {StatusInRelease: true},
	StatusInRelease:   {StatusVerified: true},
	StatusVerified:    {StatusClosed: true},
}

func CanTransition(from, to Status) bool { return transitions[from][to] }

// SLADueAt applies the governed P0 elapsed-hours and P1/P2 working-day rules.
// Weekends are skipped; public-holiday calendars can be supplied by callers.
func SLADueAt(created time.Time, severity Severity, holidays map[string]struct{}) (time.Time, error) {
	created = created.UTC()
	if severity == SeverityP0 {
		return created.Add(4 * time.Hour), nil
	}
	days := 0
	switch severity {
	case SeverityP1:
		days = 1
	case SeverityP2:
		days = 3
	default:
		return time.Time{}, ErrInvalid
	}
	due := created
	for days > 0 {
		due = due.AddDate(0, 0, 1)
		_, holiday := holidays[due.Format("2006-01-02")]
		if due.Weekday() != time.Saturday && due.Weekday() != time.Sunday && !holiday {
			days--
		}
	}
	return due, nil
}

func (ticket Ticket) Overdue(now time.Time) bool {
	return ticket.Status != StatusRejected && ticket.Status != StatusClosed && now.After(ticket.SLADueAt)
}

type TransitionInput struct {
	ExpectedVersion                                         int64
	TargetStatus                                            Status
	Severity                                                Severity
	AttributedStage                                         Stage
	OwnerUserID                                             askdata.ID
	ResolutionNote, UserResponse                            string
	FixCandidateType                                        string
	FixCandidateID, LinkedReleaseID, LinkedEvaluationCaseID askdata.ID
}

func (input TransitionInput) Validate(current Ticket) error {
	if input.ExpectedVersion != current.RecordVersion || !CanTransition(current.Status, input.TargetStatus) {
		return ErrIllegalTransition
	}
	owner := firstID(input.OwnerUserID, current.OwnerUserID)
	if input.TargetStatus != StatusRejected && input.TargetStatus != StatusTriaged && owner == "" {
		return fmt.Errorf("%w: owner is required", ErrInvalid)
	}
	if input.TargetStatus == StatusTriaged && input.OwnerUserID.Validate() != nil {
		return fmt.Errorf("%w: triage owner is required", ErrInvalid)
	}
	if input.TargetStatus == StatusRejected && (strings.TrimSpace(input.ResolutionNote) == "" || strings.TrimSpace(input.UserResponse) == "") {
		return fmt.Errorf("%w: rejection explanation and user response are required", ErrInvalid)
	}
	if input.TargetStatus == StatusFixProposed && (strings.TrimSpace(input.FixCandidateType) == "" || input.FixCandidateID.Validate() != nil) {
		return fmt.Errorf("%w: a DRAFT fix candidate is required", ErrInvalid)
	}
	if input.TargetStatus == StatusInRelease && input.LinkedReleaseID.Validate() != nil {
		return fmt.Errorf("%w: release is required", ErrInvalid)
	}
	if input.TargetStatus == StatusVerified && input.LinkedEvaluationCaseID.Validate() != nil {
		return fmt.Errorf("%w: development regression case is required", ErrInvalid)
	}
	return nil
}

func firstID(values ...askdata.ID) askdata.ID {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ClosureRate computes CLOSED/(all-REJECTED) for a prefiltered rolling window.
func ClosureRate(statuses []Status) float64 {
	denominator, closed := 0, 0
	for _, status := range statuses {
		if status == StatusRejected {
			continue
		}
		denominator++
		if status == StatusClosed {
			closed++
		}
	}
	if denominator == 0 {
		return 0
	}
	return float64(closed) / float64(denominator)
}
