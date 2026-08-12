package support

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalid   = errors.New("support ticket is invalid")
	ErrNotFound  = errors.New("support ticket was not found")
	ErrConflict  = errors.New("support ticket changed concurrently")
	ErrForbidden = errors.New("support ticket access is forbidden")
)

type Identity struct {
	TenantID string
	DomainID string
	ActorID  string
}

func (identity Identity) Valid() bool {
	return uuid.Validate(identity.TenantID) == nil && uuid.Validate(identity.DomainID) == nil && uuid.Validate(identity.ActorID) == nil
}

type CreateInput struct {
	ClientRequestID string `json:"clientRequestId"`
	Category        string `json:"category"`
	Priority        string `json:"priority"`
	Subject         string `json:"subject"`
	Description     string `json:"description"`
	PageURL         string `json:"pageUrl"`
	ErrorCode       string `json:"errorCode"`
}

func (input *CreateInput) normalize() error {
	input.Category = strings.ToUpper(strings.TrimSpace(input.Category))
	input.Priority = strings.ToUpper(strings.TrimSpace(input.Priority))
	input.Subject = strings.TrimSpace(input.Subject)
	input.Description = strings.TrimSpace(input.Description)
	input.PageURL = strings.TrimSpace(input.PageURL)
	input.ErrorCode = strings.ToUpper(strings.TrimSpace(input.ErrorCode))
	if uuid.Validate(input.ClientRequestID) != nil || !oneOf(input.Category, "QUESTION", "DATA", "REPORT", "ACCESS", "SYSTEM", "OTHER") ||
		!oneOf(input.Priority, "NORMAL", "HIGH", "URGENT") || !bounded(input.Subject, 4, 120) || !bounded(input.Description, 10, 4000) ||
		len(input.PageURL) > 1000 || len(input.ErrorCode) > 127 || strings.ContainsAny(input.Subject+input.Description, "\x00") {
		return ErrInvalid
	}
	if input.PageURL != "" {
		parsed, err := url.Parse(input.PageURL)
		if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") {
			return ErrInvalid
		}
	}
	for _, char := range input.ErrorCode {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return ErrInvalid
		}
	}
	return nil
}

type TransitionInput struct {
	Status         string `json:"status"`
	ResolutionNote string `json:"resolutionNote"`
	RecordVersion  int64  `json:"recordVersion"`
}

func (input *TransitionInput) normalize() error {
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	input.ResolutionNote = strings.TrimSpace(input.ResolutionNote)
	if !oneOf(input.Status, "IN_PROGRESS", "RESOLVED", "CLOSED") || input.RecordVersion < 1 || len(input.ResolutionNote) > 2000 {
		return ErrInvalid
	}
	if (input.Status == "RESOLVED" || input.Status == "CLOSED") && !bounded(input.ResolutionNote, 4, 2000) {
		return ErrInvalid
	}
	return nil
}

type Ticket struct {
	ID             string     `json:"id"`
	Category       string     `json:"category"`
	Priority       string     `json:"priority"`
	Subject        string     `json:"subject"`
	Description    string     `json:"description"`
	PageURL        string     `json:"pageUrl"`
	ErrorCode      string     `json:"errorCode"`
	Status         string     `json:"status"`
	ResolutionNote string     `json:"resolutionNote"`
	ReporterUserID string     `json:"reporterUserId"`
	ReporterName   string     `json:"reporterName"`
	AssigneeUserID string     `json:"assigneeUserId,omitempty"`
	AssigneeName   string     `json:"assigneeName,omitempty"`
	RecordVersion  int64      `json:"recordVersion"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
}

func bounded(value string, minimum, maximum int) bool {
	length := len([]rune(value))
	return length >= minimum && length <= maximum
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
