package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"intelligent-report-generation-system/internal/askdata"
)

func TestRecorderEmitsEveryGovernedSpanAndNoSensitivePayload(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	registry := prometheus.NewRegistry()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	recorder, err := NewRecorder(provider, registry, logger)
	if err != nil {
		t.Fatal(err)
	}
	run := validRunMetadata()
	for _, stage := range Stages() {
		_, end, err := recorder.StartStage(context.Background(), run, stage, StageMetadata{
			CandidateCount: 2, ObjectCount: 1, ScoreMinimum: .5, ScoreMaximum: .9,
			ArtifactHash: askdata.HashBytes([]byte(stage)), Version: "v1",
		})
		if err != nil {
			t.Fatalf("start %s: %v", stage, err)
		}
		result := StageResult{Status: "OK"}
		if stage == StageVerify {
			result = StageResult{Status: "DEGRADED", ErrorCode: "RESULT_RETRY_REQUIRED"}
		}
		if err := end(result); err != nil {
			t.Fatalf("end %s: %v", stage, err)
		}
	}
	if got := len(spanRecorder.Ended()); got != len(Stages()) {
		t.Fatalf("ended spans = %d, want %d", got, len(Stages()))
	}
	for _, span := range spanRecorder.Ended() {
		if !strings.HasPrefix(span.Name(), "askdata.") {
			t.Fatalf("span name = %q", span.Name())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), "customer secret") || strings.Contains(attr.Value.Emit(), "销售额原问句") {
				t.Fatalf("span leaked sensitive payload: %+v", attr)
			}
		}
	}
	if strings.Contains(logs.String(), "customer secret") || strings.Contains(logs.String(), "销售额原问句") {
		t.Fatalf("logs leaked sensitive payload: %s", logs.String())
	}
	if count := testutil.ToFloat64(recorder.stageErrors.WithLabelValues(
		string(StageVerify), string(run.DomainID), "RESULT_RETRY_REQUIRED",
	)); count != 1 {
		t.Fatalf("verify error count = %v", count)
	}
}

func TestRecorderExportsCoreQualityCostAndLatencyMetrics(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	defer func() { _ = provider.Shutdown(context.Background()) }()
	registry := prometheus.NewRegistry()
	recorder, err := NewRecorder(provider, registry, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	run := validRunMetadata()
	if err := recorder.RecordRun(run, RunOutcome{
		RunType: "SINGLE_QUERY", Outcome: "ANSWERED", Latency: 2 * time.Second,
		LLMCalls: 2, ToolCalls: 4, CostMicros: 1200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordQuality(QualitySnapshot{
		DomainID: run.DomainID, ReleaseID: run.ReleaseID, Model: "fixture-model",
		StrictAccuracy: .97, DirectAnswerCoverage: .88, ClarificationRate: .08,
		MetricBindingAccuracy: .98, DimensionBindingF1: .97, MemberBindingF1: .96,
		RetrievalRecallAtK: .995, GraphPlanFailureRate: .01,
		SQLValidationFailureRate: .02, ResultValidationRetryRate: .03,
		ReleaseProjectionLag: 3 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	metrics, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, family := range metrics {
		names[family.GetName()] = true
	}
	for _, name := range []string{
		"askdata_e2e_strict_accuracy", "askdata_direct_answer_coverage",
		"askdata_clarification_rate", "askdata_metric_binding_accuracy",
		"askdata_dimension_binding_f1", "askdata_member_binding_f1",
		"askdata_retrieval_recall_at_k", "askdata_graph_plan_failure_rate",
		"askdata_sql_validation_failure_rate", "askdata_result_validation_retry_rate",
		"askdata_release_projection_lag_seconds", "askdata_question_latency_seconds",
		"askdata_llm_calls_per_question", "askdata_tool_calls_per_question", "askdata_cost_per_answer",
	} {
		if !names[name] {
			t.Fatalf("metric %s was not gathered", name)
		}
	}
}

func TestRecorderRejectsFreeFormTelemetry(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	defer func() { _ = provider.Shutdown(context.Background()) }()
	recorder, err := NewRecorder(provider, prometheus.NewRegistry(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := recorder.StartStage(context.Background(), validRunMetadata(), "raw-question", StageMetadata{}); err == nil {
		t.Fatal("free-form stage was accepted")
	}
	if err := recorder.RecordRun(validRunMetadata(), RunOutcome{
		RunType: "question=销售额原问句", Outcome: "ANSWERED", Latency: time.Second,
	}); err == nil {
		t.Fatal("free-form run label was accepted")
	}
}

func validRunMetadata() RunMetadata {
	return RunMetadata{
		TenantID: "tenant-1", ActorID: "actor-1", DomainID: "sales", RunID: "run-1",
		ReleaseID: "release-1", ReleaseHash: askdata.HashBytes([]byte("release")),
	}
}
