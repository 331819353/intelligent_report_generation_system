package metadataai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

var (
	ErrNotFound            = errors.New("metadata AI resource not found")
	ErrConflict            = errors.New("metadata AI resource conflict")
	ErrInvalidTargetScope  = errors.New("metadata AI target scope is invalid")
	ErrStructureChanged    = errors.New("metadata table structure changed during AI completion")
	ErrProcessingLeaseLost = errors.New("metadata processing lease was lost")
	ErrSourceChanged       = errors.New("metadata source changed during AI completion")
)

type Store interface {
	LoadInput(context.Context, string, string) (CompletionInput, error)
	CreateJob(context.Context, string, string, Job) (Job, error)
	FailJob(context.Context, string, string, Job, string) (Job, error)
	SaveResult(context.Context, string, string, Job, CompletionInput, ProviderResult, float64) (Job, []Suggestion, error)
	ListSuggestions(context.Context, string, string, string, int) ([]Suggestion, error)
	DecideSuggestion(context.Context, string, string, string, string) (Suggestion, error)
}

type Service struct {
	store     Store
	provider  Provider
	timeout   time.Duration
	threshold float64
	now       func() time.Time
}

const maxMetadataSampleRows = 10

type metadataCompletionFailure struct {
	code  string
	cause error
}

func (e metadataCompletionFailure) Error() string { return e.cause.Error() }
func (e metadataCompletionFailure) Unwrap() error { return e.cause }

// MetadataCompletionFailureCode exposes only a stable local category to the data-source
// worker. Raw provider output and database details remain confined to server-side logs.
func (e metadataCompletionFailure) MetadataCompletionFailureCode() string { return e.code }

type GenerateResult struct {
	Job         Job          `json:"job"`
	Suggestions []Suggestion `json:"suggestions"`
}

// NewService 创建元数据智能补全编排服务，并设置调用超时和自动应用阈值。
func NewService(store Store, provider Provider, timeout time.Duration, confidenceThreshold float64) *Service {
	return &Service{store: store, provider: provider, timeout: timeout, threshold: confidenceThreshold, now: time.Now}
}

// Generate 创建任务、调用模型、校验结构化结果并持久化建议。
func (s *Service) Generate(ctx context.Context, tenantID, actorID, tableID string) (GenerateResult, error) {
	return s.generate(ctx, tenantID, actorID, tableID, nil, true, nil, "", "", "", 0)
}

// GenerateWithSamples 在不持久化样本行的前提下，将最多十行数据加入本次元数据完善输入。
func (s *Service) GenerateWithSamples(ctx context.Context, tenantID, actorID, tableID string, samples []map[string]any) (GenerateResult, error) {
	if len(samples) > maxMetadataSampleRows {
		samples = samples[:maxMetadataSampleRows]
	}
	return s.generate(ctx, tenantID, actorID, tableID, samples, true, nil, "", "", "", 0)
}

// CompleteTable 使用 worker 已持久化的结构哈希和目标范围作为并发栅栏；nil 字段集合表示全量活动字段。
func (s *Service) CompleteTable(ctx context.Context, tenantID, actorID, tableID string, samples []map[string]any, targetTable bool, targetColumnIDs []string, expectedStructureHash, processingItemID, processingWorkerID string, processingSourceVersion int64) error {
	if len(samples) > maxMetadataSampleRows {
		samples = samples[:maxMetadataSampleRows]
	}
	_, err := s.generate(ctx, tenantID, actorID, tableID, samples, targetTable, targetColumnIDs, expectedStructureHash, processingItemID, processingWorkerID, processingSourceVersion)
	if err == nil {
		return nil
	}
	return metadataCompletionFailure{code: metadataCompletionFailureCode(err), cause: err}
}

func metadataCompletionFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrSourceChanged):
		return "SOURCE_CHANGED"
	case errors.Is(err, ErrStructureChanged):
		return "STRUCTURE_CHANGED"
	case errors.Is(err, ErrProcessingLeaseLost):
		return "PROCESSING_LEASE_LOST"
	case errors.Is(err, ErrProviderUnavailable):
		return "PROVIDER_UNAVAILABLE"
	case errors.Is(err, ErrInvalidOutput):
		return "INVALID_OUTPUT"
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		return "TENANT_AI_FORBIDDEN"
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		return "QUOTA_EXCEEDED"
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	default:
		return "COMPLETION_FAILED"
	}
}

func (s *Service) generate(ctx context.Context, tenantID, actorID, tableID string, samples []map[string]any, targetTable bool, targetColumnIDs []string, expectedStructureHash, processingItemID, processingWorkerID string, processingSourceVersion int64) (GenerateResult, error) {
	if s.provider == nil || !s.provider.Configured() {
		return GenerateResult{}, ErrProviderUnavailable
	}
	input, err := s.store.LoadInput(ctx, tenantID, tableID)
	if err != nil {
		return GenerateResult{}, err
	}
	if expectedStructureHash != "" && input.StructureHash != expectedStructureHash {
		return GenerateResult{}, ErrStructureChanged
	}
	input, err = scopeCompletionInput(input, targetTable, targetColumnIDs)
	if err != nil {
		return GenerateResult{}, err
	}
	input.SampleRows = samples
	hash, err := inputHash(input)
	if err != nil {
		return GenerateResult{}, err
	}
	job, err := s.store.CreateJob(ctx, tenantID, actorID, Job{
		TableID:                 tableID,
		StructureHash:           input.StructureHash,
		ProcessingItemID:        processingItemID,
		ProcessingWorkerID:      processingWorkerID,
		ProcessingSourceVersion: processingSourceVersion,
		Provider:                s.provider.Name(),
		Model:                   s.provider.Model(),
		PromptVersion:           PromptVersion,
		InputHash:               hash,
		Status:                  "RUNNING",
	})
	if err != nil {
		return GenerateResult{}, err
	}
	// 模型调用使用独立超时，任务失败仍需回到原请求上下文记录最终状态。
	started := s.now()
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	providerResult, callErr := s.provider.Complete(callCtx, tenantID, actorID, input)
	cancel()
	job.LatencyMS = s.now().Sub(started).Milliseconds()
	if callErr != nil {
		code := "PROVIDER_ERROR"
		switch {
		case errors.Is(callErr, aiplatform.ErrTenantAIForbidden):
			code = "TENANT_AI_FORBIDDEN"
		case errors.Is(callErr, aiplatform.ErrQuotaExceeded):
			code = "QUOTA_EXCEEDED"
		case errors.Is(callErr, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded):
			code = "TIMEOUT"
		case errors.Is(callErr, ErrInvalidOutput):
			code = "INVALID_OUTPUT"
		}
		job, callErr = s.recordFailure(ctx, tenantID, actorID, job, code, callErr)
		return GenerateResult{Job: job, Suggestions: []Suggestion{}}, callErr
	}
	job.Model = firstNonBlank(providerResult.Model, job.Model)
	job.ModelVersion = providerResult.ModelVersion
	job.PromptTokens = providerResult.Usage.PromptTokens
	job.CompletionTokens = providerResult.Usage.CompletionTokens
	job.TotalTokens = providerResult.Usage.TotalTokens
	providerResult.Output = normalizeOutputForInput(input, providerResult.Output)
	// 不信任外部模型输出；在任何数据库写入前再次执行领域级校验。
	if err := ValidateOutput(input, providerResult.Output); err != nil {
		job, err = s.recordFailure(ctx, tenantID, actorID, job, "INVALID_OUTPUT", err)
		return GenerateResult{Job: job, Suggestions: []Suggestion{}}, err
	}
	job, suggestions, err := s.store.SaveResult(ctx, tenantID, actorID, job, input, providerResult, s.threshold)
	if err != nil {
		code := "PERSISTENCE_ERROR"
		if errors.Is(err, ErrStructureChanged) {
			code = "STRUCTURE_CHANGED"
		} else if errors.Is(err, ErrProcessingLeaseLost) {
			code = "PROCESSING_LEASE_LOST"
		} else if errors.Is(err, ErrSourceChanged) {
			code = "SOURCE_CHANGED"
		}
		job, err = s.recordFailure(ctx, tenantID, actorID, job, code, err)
		return GenerateResult{Job: job, Suggestions: []Suggestion{}}, err
	}
	return GenerateResult{Job: job, Suggestions: suggestions}, nil
}

// scopeCompletionInput 按稳定字段 ID 收缩模型输出目标；顺序仍沿用技术字段顺序以保持输入哈希稳定。
func scopeCompletionInput(input CompletionInput, targetTable bool, targetColumnIDs []string) (CompletionInput, error) {
	input.TargetTable = targetTable
	if targetColumnIDs == nil {
		return input, nil
	}
	requested := make(map[string]struct{}, len(targetColumnIDs))
	for _, id := range targetColumnIDs {
		if id == "" {
			return CompletionInput{}, ErrInvalidTargetScope
		}
		if _, exists := requested[id]; exists {
			return CompletionInput{}, ErrInvalidTargetScope
		}
		requested[id] = struct{}{}
	}
	columns := make([]Target, 0, len(requested))
	for _, column := range input.Columns {
		if _, exists := requested[column.ID]; exists {
			columns = append(columns, column)
			delete(requested, column.ID)
		}
	}
	if len(requested) != 0 || (!targetTable && len(columns) == 0) {
		return CompletionInput{}, ErrInvalidTargetScope
	}
	input.Columns = columns
	return input, nil
}

// ListSuggestions 按任务与状态筛选智能补全建议。
func (s *Service) ListSuggestions(ctx context.Context, tenantID, jobID, status string, limit int) ([]Suggestion, error) {
	return s.store.ListSuggestions(ctx, tenantID, jobID, status, limit)
}

// DecideSuggestion 接受或拒绝待人工确认的建议。
func (s *Service) DecideSuggestion(ctx context.Context, tenantID, actorID, suggestionID, decision string) (Suggestion, error) {
	if decision != "ACCEPT" && decision != "REJECT" {
		return Suggestion{}, ErrInvalidDecision
	}
	return s.store.DecideSuggestion(ctx, tenantID, actorID, suggestionID, decision)
}

// recordFailure 脱离已取消的请求上下文，在短超时内尽力持久化任务失败状态。
func (s *Service) recordFailure(ctx context.Context, tenantID, actorID string, job Job, code string, cause error) (Job, error) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	failed, err := s.store.FailJob(persistCtx, tenantID, actorID, job, code)
	if err != nil {
		return job, errors.Join(cause, err)
	}
	return failed, cause
}

// inputHash 标识模型输入版本，便于审计和结果复现。
func inputHash(input CompletionInput) (string, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
