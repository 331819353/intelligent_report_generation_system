package metriccandidate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

// Service 编排候选读取、人工拒绝及“接受后创建或迭代草稿”的安全边界。
type Service struct {
	store   Store
	metrics MetricCreator
}

func NewService(store Store, metrics MetricCreator) *Service {
	return &Service{store: store, metrics: metrics}
}

func (s *Service) List(ctx context.Context, tenantID string, filter ListFilter) ([]Candidate, int, error) {
	if tenantID == "" || filter.Limit < 1 || filter.Limit > 200 || filter.Offset < 0 ||
		(filter.DatasetID != "" && !canonicalUUID(filter.DatasetID)) || !validCandidateStatusFilter(filter.Status) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.List(ctx, tenantID, filter)
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (Candidate, error) {
	if tenantID == "" || !canonicalUUID(id) {
		return Candidate{}, ErrNotFound
	}
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) Reject(ctx context.Context, tenantID, actorID, id string, input RejectInput) (Candidate, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 || !validDecisionReason(input.Reason) {
		return Candidate{}, ErrInvalidRequest
	}
	return s.store.Reject(ctx, tenantID, actorID, id, input)
}

func (s *Service) Accept(ctx context.Context, tenantID, actorID, id string, input AcceptInput) (AcceptResult, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 || s.metrics == nil {
		return AcceptResult{}, ErrInvalidRequest
	}
	candidate, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return AcceptResult{}, err
	}
	if candidate.Status == CandidateStatusBlocked || len(candidate.BlockReasons) > 0 {
		return AcceptResult{}, ErrBlocked
	}
	if candidate.Status != CandidateStatusReady && candidate.Status != CandidateStatusNeedsReview && candidate.Status != CandidateStatusAccepted {
		return AcceptResult{}, ErrNotReviewable
	}
	if candidate.Status != CandidateStatusAccepted && candidate.Version != input.ExpectedVersion {
		return AcceptResult{}, ErrConflict
	}
	enrichedDefinition, err := acceptedCandidateDefinition(candidate)
	if err != nil {
		return AcceptResult{}, ErrInvalidRequest
	}
	prepared, err := metric.Prepare(enrichedDefinition)
	if err != nil || prepared.Definition.DatasetID != candidate.DatasetID ||
		prepared.Definition.DatasetVersionID != candidate.DatasetVersionID ||
		prepared.Definition.Metric.Code != candidate.Code {
		return AcceptResult{}, ErrInvalidRequest
	}
	record, err := s.metrics.CreateFromCandidate(ctx, tenantID, actorID, candidate.ID, input.ExpectedVersion, metric.CreateInput{Definition: enrichedDefinition})
	if err != nil {
		if errors.Is(err, metric.ErrOriginCandidateConflict) {
			return AcceptResult{}, ErrConflict
		}
		if errors.Is(err, metric.ErrOriginCandidateUnavailable) {
			return AcceptResult{}, ErrNotReviewable
		}
		return AcceptResult{}, err
	}
	accepted, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return AcceptResult{}, err
	}
	if accepted.Status != CandidateStatusAccepted || accepted.AcceptedMetricID != record.ID {
		return AcceptResult{}, ErrConflict
	}
	return AcceptResult{Candidate: accepted, Metric: record}, nil
}

func acceptedCandidateDefinition(candidate Candidate) (json.RawMessage, error) {
	var definition metric.Definition
	decoder := json.NewDecoder(strings.NewReader(string(candidate.ProposedDefinition)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(candidate.Semantic.Name)
	if name == "" {
		name = strings.TrimSpace(candidate.Name)
	}
	description := strings.TrimSpace(candidate.Semantic.Description)
	if description == "" {
		description = strings.TrimSpace(candidate.Description)
	}
	if name == "" || description == "" {
		return nil, ErrInvalidRequest
	}
	definition.Metric.Name = name
	definition.Metric.Description = description
	return json.Marshal(definition)
}

func validCandidateStatusFilter(value string) bool {
	switch CandidateStatus(value) {
	case "", CandidateStatusReady, CandidateStatusNeedsReview, CandidateStatusBlocked, CandidateStatusAccepted, CandidateStatusRejected:
		return true
	default:
		return false
	}
}

func validDecisionReason(value string) bool {
	if len([]rune(value)) < 1 || len([]rune(value)) > 1000 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t' {
			return false
		}
	}
	return true
}

type JobStore interface {
	ListJobTenantIDs(context.Context) ([]string, error)
	ClaimJob(context.Context, string, string, time.Duration) (*JobClaim, error)
	LoadExactDatasetVersion(context.Context, JobClaim) (LoadedDatasetVersion, error)
	FinishJob(context.Context, JobClaim, string, ExtractionResult) error
	FailJob(context.Context, JobClaim, string, string, string) error
}

type LoadedDatasetVersion struct {
	Version               dataset.VersionRecord
	DependencyUnavailable bool
}

// Worker 运行纯规则提取器。LLM 不在此路径上，因此数据集发布不依赖模型可用性。
type Worker struct {
	store    JobStore
	enricher *Enricher
}

func NewWorker(store JobStore, enrichers ...*Enricher) *Worker {
	worker := &Worker{store: store}
	if len(enrichers) > 0 {
		worker.enricher = enrichers[0]
	}
	return worker
}

func (w *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	return w.store.ListJobTenantIDs(ctx)
}

func (w *Worker) ProcessNext(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	claim, err := w.store.ClaimJob(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	var result ExtractionResult
	if len(claim.PreparedResult) > 0 && string(claim.PreparedResult) != "null" {
		err = json.Unmarshal(claim.PreparedResult, &result)
		if err == nil && (result.DatasetID != claim.DatasetID ||
			result.DatasetVersionID != claim.DatasetVersionID || result.DSLHash != claim.DSLHash) {
			err = ErrInvalidRequest
		}
		if err == nil {
			err = w.store.FinishJob(ctx, *claim, workerID, result)
		}
	} else {
		var loaded LoadedDatasetVersion
		loaded, err = w.store.LoadExactDatasetVersion(ctx, *claim)
		version := loaded.Version
		if err == nil {
			result, err = Extract(version)
			if err == nil {
				if loaded.DependencyUnavailable {
					result = blockUnavailableDatasetCandidates(result)
				}
				if w.enricher != nil {
					var enrichmentErr error
					result, enrichmentErr = w.enricher.Enrich(ctx, claim.TenantID, claim.RequestedBy, version, result)
					if enrichmentErr != nil {
						result.Warnings = append(result.Warnings, "LLM 语义补全暂不可用，本次已使用规则生成的口径、血缘和标签，后续可重试补全。")
					}
				} else {
					result = attachDefaultSemantics(version, result)
				}
				err = w.store.FinishJob(ctx, *claim, workerID, result)
			}
		}
	}
	if err == nil {
		return true, nil
	}
	failErr := w.store.FailJob(ctx, *claim, workerID, "METRIC_EXTRACTION_FAILED", err.Error())
	if failErr != nil {
		return true, errors.Join(err, failErr)
	}
	return true, err
}

func blockUnavailableDatasetCandidates(result ExtractionResult) ExtractionResult {
	for index := range result.Candidates {
		result.Candidates[index].Status = CandidateStatusBlocked
		if !containsString(result.Candidates[index].BlockReasons, BlockReasonDatasetUnavailable) {
			result.Candidates[index].BlockReasons = append(result.Candidates[index].BlockReasons, BlockReasonDatasetUnavailable)
		}
		if !containsString(result.Candidates[index].Warnings, "数据集发布快照的运行依赖当前不可用，修复依赖并重新发布前不能接受该候选。") {
			result.Candidates[index].Warnings = append(result.Candidates[index].Warnings, "数据集发布快照的运行依赖当前不可用，修复依赖并重新发布前不能接受该候选。")
		}
	}
	result.Status = TaskStatusPartial
	result.Warnings = append(result.Warnings, "数据集发布快照的运行依赖当前不可用；已保留事实候选并全部标记为阻塞。")
	return result
}

func extractionBlockedByUnavailable(result ExtractionResult) bool {
	if len(result.Candidates) == 0 {
		return false
	}
	for _, candidate := range result.Candidates {
		if candidate.Status != CandidateStatusBlocked || !containsString(candidate.BlockReasons, BlockReasonDatasetUnavailable) {
			return false
		}
	}
	return true
}
