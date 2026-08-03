package semanticqa

import (
	"context"
	"strings"
	"time"
)

const (
	QueryProgressStageRequest             = "REQUEST"
	QueryProgressStageContext             = "CONTEXT"
	QueryProgressStageMetricSemantic      = "METRIC_SEMANTIC"
	QueryProgressStageMetricCatalog       = "METRIC_CATALOG"
	QueryProgressStageMetricSelection     = "METRIC_SELECTION"
	QueryProgressStageDimensionEnrichment = "DIMENSION_ENRICHMENT"
	QueryProgressStageDimensionSemantic   = "DIMENSION_SEMANTIC"
	QueryProgressStageDimensionDecision   = "DIMENSION_DECISION"
	QueryProgressStageDimensionSelection  = "DIMENSION_SELECTION"
	QueryProgressStagePlan                = "PLAN"
	QueryProgressStageOrchestration       = "ORCHESTRATION"
	QueryProgressStageSQLGuard            = "SQL_GUARD"
	QueryProgressStageExecution           = "EXECUTION"
	QueryProgressStageResultVerification  = "RESULT_VERIFICATION"
	QueryProgressStageAnswer              = "ANSWER"
	QueryProgressStageComplete            = "COMPLETE"

	QueryProgressStatusRunning   = "RUNNING"
	QueryProgressStatusSucceeded = "SUCCEEDED"
	QueryProgressStatusWarn      = "WARN"
)

// QueryTurnProgressEvent is a safe, server-generated lifecycle event for the
// conversational planning request. It intentionally excludes the raw question,
// tool arguments, candidate identifiers, prompts, SQL and provider payloads.
type QueryTurnProgressEvent struct {
	Timestamp  string `json:"timestamp"`
	QuestionID string `json:"questionId,omitempty"`
	Stage      string `json:"stage"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

type queryTurnProgressReporter func(QueryTurnProgressEvent)
type queryTurnProgressContextKey struct{}

func withQueryTurnProgressReporter(
	ctx context.Context,
	reporter queryTurnProgressReporter,
) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, queryTurnProgressContextKey{}, reporter)
}

func reportQueryTurnProgress(ctx context.Context, stage, status, message string) {
	reportQuestionProgress(ctx, "", stage, status, message)
}

func reportQuestionProgress(
	ctx context.Context,
	questionID, stage, status, message string,
) {
	reporter, _ := ctx.Value(queryTurnProgressContextKey{}).(queryTurnProgressReporter)
	message = strings.TrimSpace(message)
	if reporter == nil || message == "" {
		return
	}
	reporter(QueryTurnProgressEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		QuestionID: questionID,
		Stage:      stage,
		Status:     status,
		Message:    message,
	})
}
