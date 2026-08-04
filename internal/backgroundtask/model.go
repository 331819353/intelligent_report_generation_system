package backgroundtask

import (
	"context"
	"errors"
	"time"
)

const (
	ViewActive = "ACTIVE"
	ViewRecent = "RECENT"
	ViewAll    = "ALL"
)

var (
	ErrInvalidRequest = errors.New("invalid background task request")
	ErrNotFound       = errors.New("background task was not found")
	ErrNotActive      = errors.New("background task is no longer active")
	ErrNotCancellable = errors.New("background task cannot be cancelled")
	ErrNotRetryable   = errors.New("background task cannot be retried")
)

type Task struct {
	ID                   string     `json:"id"`
	Kind                 string     `json:"kind"`
	KindLabel            string     `json:"kindLabel"`
	Name                 string     `json:"name"`
	Description          string     `json:"description"`
	Status               string     `json:"status"`
	SourceStatus         string     `json:"sourceStatus"`
	ResourceType         string     `json:"resourceType"`
	ResourceID           string     `json:"resourceId"`
	Processed            *int64     `json:"processed,omitempty"`
	Total                *int64     `json:"total,omitempty"`
	ProgressPercent      *int       `json:"progressPercent,omitempty"`
	ProgressText         string     `json:"progressText"`
	Attempt              int        `json:"attempt"`
	MaxAttempts          int        `json:"maxAttempts"`
	CanCancel            bool       `json:"canCancel"`
	CancelDisabledReason string     `json:"cancelDisabledReason,omitempty"`
	CanRetry             bool       `json:"canRetry"`
	RetryDisabledReason  string     `json:"retryDisabledReason,omitempty"`
	ErrorCode            string     `json:"errorCode,omitempty"`
	ErrorMessage         string     `json:"errorMessage,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	CompletedAt          *time.Time `json:"completedAt,omitempty"`
}

type Page struct {
	Items       []Task    `json:"items"`
	ActiveCount int       `json:"activeCount"`
	GeneratedAt time.Time `json:"generatedAt"`
}

type Store interface {
	List(context.Context, string, string, int, bool) (Page, error)
	Find(context.Context, string, string, string, bool) (Task, error)
	Cancel(context.Context, string, string, string, string, bool) error
	Retry(context.Context, string, string, string, string, bool) error
}
