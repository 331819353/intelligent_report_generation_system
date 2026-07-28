package datasetai

import (
	"context"
	"strings"
	"time"
)

const (
	ProgressStageContext    = "CONTEXT"
	ProgressStageCatalog    = "CATALOG"
	ProgressStageIntent     = "INTENT"
	ProgressStagePlanner    = "PLANNER"
	ProgressStageValidation = "VALIDATION"
	ProgressStageRepair     = "REPAIR"
	ProgressStageComplete   = "COMPLETE"

	ProgressStatusRunning   = "RUNNING"
	ProgressStatusSucceeded = "SUCCEEDED"
	ProgressStatusWarn      = "WARN"
)

// PlanProgressEvent is a safe, server-generated lifecycle event. It deliberately contains
// no prompt text, asset identifiers, provider payloads, credentials, or raw validation detail.
type PlanProgressEvent struct {
	Timestamp string `json:"timestamp"`
	Stage     string `json:"stage"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

type planProgressReporter func(PlanProgressEvent)
type planProgressContextKey struct{}

func withPlanProgressReporter(ctx context.Context, reporter planProgressReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, planProgressContextKey{}, reporter)
}

func reportPlanProgress(ctx context.Context, stage, status, message string) {
	reporter, _ := ctx.Value(planProgressContextKey{}).(planProgressReporter)
	message = strings.TrimSpace(message)
	if reporter == nil || message == "" {
		return
	}
	reporter(PlanProgressEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Stage:     stage,
		Status:    status,
		Message:   message,
	})
}
