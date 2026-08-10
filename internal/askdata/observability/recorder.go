// Package observability provides the only telemetry vocabulary for AskData.
// It accepts stable IDs, hashes, counts and closed enums; raw questions,
// parameters, result rows, prompts and model reasoning have no input fields.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"intelligent-report-generation-system/internal/askdata"
)

type Stage string

const (
	StageAuthorize         Stage = "authorize"
	StageNormalize         Stage = "normalize"
	StageUnderstand        Stage = "understand"
	StageRetrieveMetric    Stage = "retrieve_metric"
	StageRetrieveDimension Stage = "retrieve_dimension"
	StageLookupMember      Stage = "lookup_member"
	StageGraphResolve      Stage = "graph_resolve"
	StageBind              Stage = "bind"
	StageCompile           Stage = "compile"
	StageExplain           Stage = "explain"
	StageExecute           Stage = "execute"
	StageVerify            Stage = "verify"
	StageRender            Stage = "render"
)

var allStages = map[Stage]struct{}{
	StageAuthorize: {}, StageNormalize: {}, StageUnderstand: {}, StageRetrieveMetric: {},
	StageRetrieveDimension: {}, StageLookupMember: {}, StageGraphResolve: {}, StageBind: {},
	StageCompile: {}, StageExplain: {}, StageExecute: {}, StageVerify: {}, StageRender: {},
}

var stableCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type RunMetadata struct {
	TenantID    askdata.ID
	ActorID     askdata.ID
	DomainID    askdata.ID
	RunID       askdata.ID
	ReleaseID   askdata.ID
	ReleaseHash askdata.ContentHash
}

func (metadata RunMetadata) Validate() error {
	for label, id := range map[string]askdata.ID{
		"tenantId": metadata.TenantID, "actorId": metadata.ActorID,
		"domainId": metadata.DomainID, "runId": metadata.RunID,
		"releaseId": metadata.ReleaseID,
	} {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	if err := metadata.ReleaseHash.Validate(); err != nil {
		return fmt.Errorf("releaseHash: %w", err)
	}
	return nil
}

type StageMetadata struct {
	CandidateCount int
	ObjectCount    int
	ScoreMinimum   float64
	ScoreMaximum   float64
	ArtifactHash   askdata.ContentHash
	Version        string
}

func (metadata StageMetadata) Validate() error {
	if metadata.CandidateCount < 0 || metadata.CandidateCount > 1_000_000 ||
		metadata.ObjectCount < 0 || metadata.ObjectCount > 1_000_000 ||
		metadata.ScoreMinimum < 0 || metadata.ScoreMinimum > 1 ||
		metadata.ScoreMaximum < 0 || metadata.ScoreMaximum > 1 ||
		metadata.ScoreMinimum > metadata.ScoreMaximum {
		return errors.New("stage metadata is outside safe bounds")
	}
	if metadata.ArtifactHash != "" && metadata.ArtifactHash.Validate() != nil {
		return errors.New("artifact hash is invalid")
	}
	version := strings.TrimSpace(metadata.Version)
	if version != metadata.Version || len(version) > 128 || strings.ContainsAny(version, "\r\n\t") {
		return errors.New("version is invalid")
	}
	return nil
}

type StageResult struct {
	Status    string
	ErrorCode string
}

func (result StageResult) Validate() error {
	if result.Status != "OK" && result.Status != "ERROR" && result.Status != "DEGRADED" && result.Status != "BLOCKED" {
		return errors.New("stage result status is invalid")
	}
	if result.ErrorCode != "" && !stableCodePattern.MatchString(result.ErrorCode) {
		return errors.New("stage result error code is invalid")
	}
	if result.Status == "OK" && result.ErrorCode != "" {
		return errors.New("successful stage cannot have an error code")
	}
	return nil
}

type StageEnd func(StageResult) error

type RunOutcome struct {
	RunType    string
	Outcome    string
	Latency    time.Duration
	LLMCalls   int
	ToolCalls  int
	CostMicros int64
}

type QualitySnapshot struct {
	DomainID                  askdata.ID
	ReleaseID                 askdata.ID
	Model                     string
	StrictAccuracy            float64
	DirectAnswerCoverage      float64
	ClarificationRate         float64
	MetricBindingAccuracy     float64
	DimensionBindingF1        float64
	MemberBindingF1           float64
	RetrievalRecallAtK        float64
	GraphPlanFailureRate      float64
	SQLValidationFailureRate  float64
	ResultValidationRetryRate float64
	ReleaseProjectionLag      time.Duration
}

type Recorder struct {
	tracer trace.Tracer
	logger *slog.Logger

	stageLatency    *prometheus.HistogramVec
	stageErrors     *prometheus.CounterVec
	questionLatency *prometheus.HistogramVec
	llmCalls        *prometheus.HistogramVec
	toolCalls       *prometheus.HistogramVec
	costPerAnswer   *prometheus.HistogramVec

	strictAccuracy        *prometheus.GaugeVec
	directCoverage        *prometheus.GaugeVec
	clarificationRate     *prometheus.GaugeVec
	metricBindingAccuracy *prometheus.GaugeVec
	dimensionBindingF1    *prometheus.GaugeVec
	memberBindingF1       *prometheus.GaugeVec
	retrievalRecall       *prometheus.GaugeVec
	graphFailureRate      *prometheus.GaugeVec
	sqlFailureRate        *prometheus.GaugeVec
	resultRetryRate       *prometheus.GaugeVec
	projectionLag         *prometheus.GaugeVec
}

func NewRecorder(
	tracerProvider trace.TracerProvider,
	registerer prometheus.Registerer,
	logger *slog.Logger,
) (*Recorder, error) {
	if tracerProvider == nil || registerer == nil || logger == nil {
		return nil, errors.New("AskData observability dependencies are required")
	}
	labels := []string{"domain_id", "release_id", "model"}
	recorder := &Recorder{
		tracer: tracerProvider.Tracer("intelligent-report-generation-system/askdata"), logger: logger,
		stageLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "askdata_stage_latency_seconds", Help: "AskData governed stage latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"stage", "domain_id"}),
		stageErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "askdata_stage_errors_total", Help: "AskData governed stage failures by stable code.",
		}, []string{"stage", "domain_id", "error_code"}),
		questionLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "askdata_question_latency_seconds", Help: "End-to-end AskData question latency.",
			Buckets: []float64{.1, .25, .5, 1, 2, 3, 5, 8, 12, 18, 25, 30},
		}, []string{"domain_id", "run_type", "outcome"}),
		llmCalls: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "askdata_llm_calls_per_question", Help: "LLM calls used by a completed question.",
			Buckets: prometheus.LinearBuckets(0, 1, 9),
		}, []string{"domain_id", "run_type"}),
		toolCalls: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "askdata_tool_calls_per_question", Help: "Tool calls used by a completed question.",
			Buckets: prometheus.LinearBuckets(0, 2, 9),
		}, []string{"domain_id", "run_type"}),
		costPerAnswer: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "askdata_cost_per_answer", Help: "AI cost per answer in micros.",
			Buckets: prometheus.ExponentialBuckets(100, 2, 16),
		}, []string{"domain_id", "run_type"}),
		strictAccuracy:        qualityGauge("askdata_e2e_strict_accuracy", "Strict end-to-end accuracy.", labels),
		directCoverage:        qualityGauge("askdata_direct_answer_coverage", "Direct answer coverage.", labels),
		clarificationRate:     qualityGauge("askdata_clarification_rate", "Clarification rate.", labels),
		metricBindingAccuracy: qualityGauge("askdata_metric_binding_accuracy", "Metric binding accuracy.", labels),
		dimensionBindingF1:    qualityGauge("askdata_dimension_binding_f1", "Dimension binding F1.", labels),
		memberBindingF1:       qualityGauge("askdata_member_binding_f1", "Member binding F1.", labels),
		retrievalRecall:       qualityGauge("askdata_retrieval_recall_at_k", "Retrieval recall at governed K.", labels),
		graphFailureRate:      qualityGauge("askdata_graph_plan_failure_rate", "Graph plan failure rate.", labels),
		sqlFailureRate:        qualityGauge("askdata_sql_validation_failure_rate", "SQL validation failure rate.", labels),
		resultRetryRate:       qualityGauge("askdata_result_validation_retry_rate", "Result validation retry rate.", labels),
		projectionLag:         qualityGauge("askdata_release_projection_lag_seconds", "Release projection lag seconds.", labels),
	}
	collectors := []prometheus.Collector{
		recorder.stageLatency, recorder.stageErrors, recorder.questionLatency,
		recorder.llmCalls, recorder.toolCalls, recorder.costPerAnswer,
		recorder.strictAccuracy, recorder.directCoverage, recorder.clarificationRate,
		recorder.metricBindingAccuracy, recorder.dimensionBindingF1, recorder.memberBindingF1,
		recorder.retrievalRecall, recorder.graphFailureRate, recorder.sqlFailureRate,
		recorder.resultRetryRate, recorder.projectionLag,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register AskData metric: %w", err)
		}
	}
	return recorder, nil
}

func qualityGauge(name, help string, labels []string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
}

func (recorder *Recorder) StartStage(
	ctx context.Context,
	run RunMetadata,
	stage Stage,
	metadata StageMetadata,
) (context.Context, StageEnd, error) {
	if recorder == nil || recorder.tracer == nil || recorder.logger == nil || ctx == nil {
		return ctx, nil, errors.New("AskData observability recorder is not configured")
	}
	if _, ok := allStages[stage]; !ok {
		return ctx, nil, errors.New("AskData observability stage is invalid")
	}
	if err := run.Validate(); err != nil {
		return ctx, nil, err
	}
	if err := metadata.Validate(); err != nil {
		return ctx, nil, err
	}
	attributes := []attribute.KeyValue{
		attribute.String("askdata.tenant_id", string(run.TenantID)),
		attribute.String("askdata.actor_id", string(run.ActorID)),
		attribute.String("askdata.domain_id", string(run.DomainID)),
		attribute.String("askdata.run_id", string(run.RunID)),
		attribute.String("askdata.release_id", string(run.ReleaseID)),
		attribute.String("askdata.release_hash", string(run.ReleaseHash)),
		attribute.Int("askdata.candidate_count", metadata.CandidateCount),
		attribute.Int("askdata.object_count", metadata.ObjectCount),
		attribute.Float64("askdata.score_minimum", metadata.ScoreMinimum),
		attribute.Float64("askdata.score_maximum", metadata.ScoreMaximum),
	}
	if metadata.ArtifactHash != "" {
		attributes = append(attributes, attribute.String("askdata.artifact_hash", string(metadata.ArtifactHash)))
	}
	if metadata.Version != "" {
		attributes = append(attributes, attribute.String("askdata.version", metadata.Version))
	}
	spanContext, span := recorder.tracer.Start(ctx, "askdata."+string(stage), trace.WithAttributes(attributes...))
	started := time.Now()
	recorder.logger.LogAttrs(spanContext, slog.LevelInfo, "askdata stage started",
		slog.String("stage", string(stage)), slog.String("run_id", string(run.RunID)),
		slog.String("domain_id", string(run.DomainID)),
		slog.Int("candidate_count", metadata.CandidateCount), slog.Int("object_count", metadata.ObjectCount))
	ended := false
	return spanContext, func(result StageResult) error {
		if ended {
			return errors.New("AskData observability stage already ended")
		}
		if err := result.Validate(); err != nil {
			return err
		}
		ended = true
		duration := time.Since(started)
		recorder.stageLatency.WithLabelValues(string(stage), string(run.DomainID)).Observe(duration.Seconds())
		span.SetAttributes(attribute.String("askdata.status", result.Status))
		if result.ErrorCode != "" {
			span.SetAttributes(attribute.String("error.type", result.ErrorCode))
			span.SetStatus(codes.Error, result.ErrorCode)
			recorder.stageErrors.WithLabelValues(string(stage), string(run.DomainID), result.ErrorCode).Inc()
		} else {
			span.SetStatus(codes.Ok, result.Status)
		}
		span.End()
		recorder.logger.LogAttrs(spanContext, slog.LevelInfo, "askdata stage finished",
			slog.String("stage", string(stage)), slog.String("run_id", string(run.RunID)),
			slog.String("domain_id", string(run.DomainID)), slog.String("status", result.Status),
			slog.String("error_code", result.ErrorCode), slog.Int64("duration_ms", duration.Milliseconds()))
		return nil
	}, nil
}

func (recorder *Recorder) RecordRun(run RunMetadata, outcome RunOutcome) error {
	if recorder == nil || run.Validate() != nil || outcome.Latency < 0 || outcome.Latency > 10*time.Minute ||
		outcome.LLMCalls < 0 || outcome.LLMCalls > 100 || outcome.ToolCalls < 0 || outcome.ToolCalls > 1000 ||
		outcome.CostMicros < 0 || !stableCodePattern.MatchString(outcome.RunType) ||
		!stableCodePattern.MatchString(outcome.Outcome) {
		return errors.New("AskData run observation is invalid")
	}
	domain, runType := string(run.DomainID), outcome.RunType
	recorder.questionLatency.WithLabelValues(domain, runType, outcome.Outcome).Observe(outcome.Latency.Seconds())
	recorder.llmCalls.WithLabelValues(domain, runType).Observe(float64(outcome.LLMCalls))
	recorder.toolCalls.WithLabelValues(domain, runType).Observe(float64(outcome.ToolCalls))
	recorder.costPerAnswer.WithLabelValues(domain, runType).Observe(float64(outcome.CostMicros))
	return nil
}

func (recorder *Recorder) RecordQuality(snapshot QualitySnapshot) error {
	if recorder == nil || snapshot.DomainID.Validate() != nil || snapshot.ReleaseID.Validate() != nil ||
		!safeModelLabel(snapshot.Model) || snapshot.ReleaseProjectionLag < 0 ||
		snapshot.ReleaseProjectionLag > 365*24*time.Hour {
		return errors.New("AskData quality observation is invalid")
	}
	values := []float64{
		snapshot.StrictAccuracy, snapshot.DirectAnswerCoverage, snapshot.ClarificationRate,
		snapshot.MetricBindingAccuracy, snapshot.DimensionBindingF1, snapshot.MemberBindingF1,
		snapshot.RetrievalRecallAtK, snapshot.GraphPlanFailureRate,
		snapshot.SQLValidationFailureRate, snapshot.ResultValidationRetryRate,
	}
	for _, value := range values {
		if value < 0 || value > 1 {
			return errors.New("AskData quality rate is outside [0,1]")
		}
	}
	labels := []string{string(snapshot.DomainID), string(snapshot.ReleaseID), snapshot.Model}
	recorder.strictAccuracy.WithLabelValues(labels...).Set(snapshot.StrictAccuracy)
	recorder.directCoverage.WithLabelValues(labels...).Set(snapshot.DirectAnswerCoverage)
	recorder.clarificationRate.WithLabelValues(labels...).Set(snapshot.ClarificationRate)
	recorder.metricBindingAccuracy.WithLabelValues(labels...).Set(snapshot.MetricBindingAccuracy)
	recorder.dimensionBindingF1.WithLabelValues(labels...).Set(snapshot.DimensionBindingF1)
	recorder.memberBindingF1.WithLabelValues(labels...).Set(snapshot.MemberBindingF1)
	recorder.retrievalRecall.WithLabelValues(labels...).Set(snapshot.RetrievalRecallAtK)
	recorder.graphFailureRate.WithLabelValues(labels...).Set(snapshot.GraphPlanFailureRate)
	recorder.sqlFailureRate.WithLabelValues(labels...).Set(snapshot.SQLValidationFailureRate)
	recorder.resultRetryRate.WithLabelValues(labels...).Set(snapshot.ResultValidationRetryRate)
	recorder.projectionLag.WithLabelValues(labels...).Set(snapshot.ReleaseProjectionLag.Seconds())
	return nil
}

func safeModelLabel(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\t{}[]\"")
}

func Stages() []Stage {
	return []Stage{
		StageAuthorize, StageNormalize, StageUnderstand, StageRetrieveMetric,
		StageRetrieveDimension, StageLookupMember, StageGraphResolve, StageBind,
		StageCompile, StageExplain, StageExecute, StageVerify, StageRender,
	}
}
