package semanticqa

import (
	"context"
	"testing"
)

func TestQueryTurnProgressReporterPreservesServerLifecycleEvents(t *testing.T) {
	events := []QueryTurnProgressEvent{}
	ctx := withQueryTurnProgressReporter(
		context.Background(), func(event QueryTurnProgressEvent) {
			events = append(events, event)
		},
	)
	reportQueryTurnProgress(
		ctx, QueryProgressStageMetricCatalog,
		QueryProgressStatusRunning, " 正在检索指标清单 ",
	)
	if len(events) != 1 {
		t.Fatalf("expected one progress event, got %d", len(events))
	}
	if events[0].Stage != QueryProgressStageMetricCatalog ||
		events[0].Status != QueryProgressStatusRunning ||
		events[0].Message != "正在检索指标清单" ||
		events[0].Timestamp == "" {
		t.Fatalf("unexpected progress event: %#v", events[0])
	}
}

func TestQueryTurnProgressReporterIgnoresEmptyMessages(t *testing.T) {
	called := false
	ctx := withQueryTurnProgressReporter(
		context.Background(), func(QueryTurnProgressEvent) { called = true },
	)
	reportQueryTurnProgress(
		ctx, QueryProgressStagePlan, QueryProgressStatusRunning, "  ",
	)
	if called {
		t.Fatal("empty lifecycle messages must not produce stream frames")
	}
}

func TestQuestionProgressIncludesStableQuestionID(t *testing.T) {
	var event QueryTurnProgressEvent
	ctx := withQueryTurnProgressReporter(
		context.Background(), func(item QueryTurnProgressEvent) { event = item },
	)
	reportQuestionProgress(
		ctx, "10000000-0000-4000-8000-000000000001",
		QueryProgressStageSQLGuard, QueryProgressStatusSucceeded,
		"查询门禁已通过",
	)
	if event.QuestionID != "10000000-0000-4000-8000-000000000001" ||
		event.Stage != QueryProgressStageSQLGuard {
		t.Fatalf("unexpected question progress event: %#v", event)
	}
}
