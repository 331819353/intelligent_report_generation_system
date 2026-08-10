package datarequest

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidRequest      = errors.New("data request is invalid")
	ErrNotFound            = errors.New("data request was not found")
	ErrSourceRunNotFound   = errors.New("source question run was not found")
	ErrApproverUnavailable = errors.New("data request approver is unavailable")
	ErrInvalidTransition   = errors.New("data request transition is invalid")
	ErrPermissionDenied    = errors.New("data request transition is not permitted")
	ErrVersionConflict     = errors.New("data request version changed")
)

type State string

const (
	StateDraft      State = "DRAFT"
	StateSubmitted  State = "SUBMITTED"
	StateApproved   State = "APPROVED"
	StateRejected   State = "REJECTED"
	StateInProgress State = "IN_PROGRESS"
	StateDelivered  State = "DELIVERED"
	StateClosed     State = "CLOSED"
)

type DeliveryType string

const (
	DeliveryExistingReport DeliveryType = "EXISTING_REPORT"
	DeliveryNewDataset     DeliveryType = "NEW_DATASET"
	DeliveryOneTimeExport  DeliveryType = "ONE_TIME_EXPORT"
)

type Sensitivity string

const (
	SensitivityPublic       Sensitivity = "PUBLIC"
	SensitivityInternal     Sensitivity = "INTERNAL"
	SensitivityConfidential Sensitivity = "CONFIDENTIAL"
	SensitivityRestricted   Sensitivity = "RESTRICTED"
)

type Identity struct {
	TenantID string
	DomainID string
	ActorID  string
}

func (identity Identity) Valid() bool {
	return uuid.Validate(identity.TenantID) == nil && uuid.Validate(identity.DomainID) == nil &&
		uuid.Validate(identity.ActorID) == nil
}

type TimeRange struct {
	Start        time.Time `json:"start"`
	EndExclusive time.Time `json:"endExclusive"`
	Timezone     string    `json:"timezone"`
	Grain        string    `json:"grain,omitempty"`
}

type ParsedContext struct {
	MetricIDs    []string   `json:"metricIds,omitempty"`
	DimensionIDs []string   `json:"dimensionIds,omitempty"`
	MemberIDs    []string   `json:"memberIds,omitempty"`
	TimeRange    *TimeRange `json:"timeRange,omitempty"`
}

func (context ParsedContext) Empty() bool {
	return len(context.MetricIDs) == 0 && len(context.DimensionIDs) == 0 &&
		len(context.MemberIDs) == 0 && context.TimeRange == nil
}

func (context ParsedContext) Normalize() (ParsedContext, error) {
	metricIDs, err := normalizedUUIDs(context.MetricIDs, 20)
	if err != nil {
		return ParsedContext{}, err
	}
	dimensionIDs, err := normalizedUUIDs(context.DimensionIDs, 30)
	if err != nil {
		return ParsedContext{}, err
	}
	memberIDs, err := normalizedUUIDs(context.MemberIDs, 50)
	if err != nil {
		return ParsedContext{}, err
	}
	normalized := ParsedContext{
		MetricIDs: metricIDs, DimensionIDs: dimensionIDs, MemberIDs: memberIDs,
	}
	if context.TimeRange != nil {
		value := *context.TimeRange
		value.Timezone = strings.TrimSpace(value.Timezone)
		value.Grain = strings.ToUpper(strings.TrimSpace(value.Grain))
		if value.Start.IsZero() || value.EndExclusive.IsZero() ||
			!value.EndExclusive.After(value.Start) ||
			value.EndExclusive.Sub(value.Start) > 10*365*24*time.Hour ||
			value.Timezone == "" || len(value.Timezone) > 64 ||
			!allowedGrain(value.Grain) {
			return ParsedContext{}, ErrInvalidRequest
		}
		if _, err := time.LoadLocation(value.Timezone); err != nil {
			return ParsedContext{}, ErrInvalidRequest
		}
		value.Start = value.Start.UTC()
		value.EndExclusive = value.EndExclusive.UTC()
		normalized.TimeRange = &value
	}
	return normalized, nil
}

type FieldRef struct {
	DatasetVersionID string `json:"datasetVersionId"`
	FieldID          string `json:"fieldId"`
}

func normalizeFields(fields []FieldRef) ([]FieldRef, error) {
	if len(fields) < 1 || len(fields) > 100 {
		return nil, ErrInvalidRequest
	}
	result := append([]FieldRef(nil), fields...)
	for index := range result {
		result[index].DatasetVersionID = strings.TrimSpace(result[index].DatasetVersionID)
		result[index].FieldID = strings.TrimSpace(result[index].FieldID)
		if uuid.Validate(result[index].DatasetVersionID) != nil ||
			!boundedText(result[index].FieldID, 1, 128) {
			return nil, ErrInvalidRequest
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].DatasetVersionID == result[right].DatasetVersionID {
			return result[left].FieldID < result[right].FieldID
		}
		return result[left].DatasetVersionID < result[right].DatasetVersionID
	})
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, ErrInvalidRequest
		}
	}
	return result, nil
}

type CreateInput struct {
	SourceQuestionRunID string        `json:"sourceQuestionRunId,omitempty"`
	RequestText         string        `json:"requestText"`
	ParsedContext       ParsedContext `json:"parsedContext"`
	BusinessPurpose     string        `json:"businessPurpose"`
	RequiredFields      []FieldRef    `json:"requiredFields"`
	SLADueAt            time.Time     `json:"slaDueAt"`
}

type CreateCommand struct {
	ID                  string
	SourceQuestionRunID string
	RequestText         string
	ParsedContext       ParsedContext
	BusinessPurpose     string
	RequiredFields      []FieldRef
	SLADueAt            time.Time
	CreatedAt           time.Time
}

type TransitionInput struct {
	ToState              State        `json:"toState"`
	Note                 string       `json:"note"`
	SecurityCosignUserID string       `json:"securityCosignUserId,omitempty"`
	AssigneeUserID       string       `json:"assigneeUserId,omitempty"`
	DeliveryType         DeliveryType `json:"deliveryType,omitempty"`
	DeliveryRef          string       `json:"deliveryRef,omitempty"`
	RecordVersion        int64        `json:"recordVersion"`
}

type Request struct {
	ID                   string        `json:"id"`
	TenantID             string        `json:"-"`
	DomainID             string        `json:"domainId"`
	RequesterUserID      string        `json:"requesterUserId"`
	SourceQuestionRunID  string        `json:"sourceQuestionRunId,omitempty"`
	RequestText          string        `json:"requestText"`
	ParsedContext        ParsedContext `json:"parsedContext"`
	BusinessPurpose      string        `json:"businessPurpose"`
	RequiredFields       []FieldRef    `json:"requiredFields"`
	SensitivityLevel     Sensitivity   `json:"sensitivityLevel"`
	State                State         `json:"state"`
	ApproverUserIDs      []string      `json:"approverUserIds"`
	SecurityCosignUserID string        `json:"securityCosignUserId,omitempty"`
	AssigneeUserID       string        `json:"assigneeUserId,omitempty"`
	SLADueAt             time.Time     `json:"slaDueAt"`
	DeliveryType         DeliveryType  `json:"deliveryType,omitempty"`
	DeliveryRef          string        `json:"deliveryRef,omitempty"`
	StatusNote           string        `json:"statusNote,omitempty"`
	RecordVersion        int64         `json:"recordVersion"`
	CreatedAt            time.Time     `json:"createdAt"`
	UpdatedAt            time.Time     `json:"updatedAt"`
	SubmittedAt          *time.Time    `json:"submittedAt,omitempty"`
	ApprovedAt           *time.Time    `json:"approvedAt,omitempty"`
	RejectedAt           *time.Time    `json:"rejectedAt,omitempty"`
	StartedAt            *time.Time    `json:"startedAt,omitempty"`
	DeliveredAt          *time.Time    `json:"deliveredAt,omitempty"`
	ClosedAt             *time.Time    `json:"closedAt,omitempty"`
	Events               []Event       `json:"events,omitempty"`
}

type Event struct {
	ID          string       `json:"id"`
	RequestID   string       `json:"requestId"`
	EventType   string       `json:"eventType"`
	AuditNo     int64        `json:"auditNo"`
	SequenceNo  int64        `json:"sequenceNo"`
	FromState   State        `json:"fromState,omitempty"`
	ToState     State        `json:"toState"`
	ActorUserID string       `json:"actorUserId"`
	Note        string       `json:"note,omitempty"`
	Details     EventDetails `json:"details"`
	CreatedAt   time.Time    `json:"createdAt"`
}

type EventDetails struct {
	SensitivityLevel     Sensitivity `json:"sensitivityLevel"`
	SecurityCosignUserID string      `json:"securityCosignUserId,omitempty"`
	ExportJobID          string      `json:"exportJobId,omitempty"`
	DownloadNo           int         `json:"downloadNo,omitempty"`
	ExpiresAt            *time.Time  `json:"expiresAt,omitempty"`
}

func ValidTransition(from, to State) bool {
	switch from {
	case StateDraft:
		return to == StateSubmitted
	case StateSubmitted:
		return to == StateApproved || to == StateRejected
	case StateApproved:
		return to == StateInProgress
	case StateInProgress:
		return to == StateDelivered
	case StateDelivered:
		return to == StateClosed
	default:
		return false
	}
}

func normalizedUUIDs(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, ErrInvalidRequest
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
		if uuid.Validate(result[index]) != nil {
			return nil, ErrInvalidRequest
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, ErrInvalidRequest
		}
	}
	return result, nil
}

func boundedText(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func allowedGrain(value string) bool {
	switch value {
	case "", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR", "FISCAL_MONTH", "FISCAL_QUARTER", "FISCAL_YEAR":
		return true
	default:
		return false
	}
}
