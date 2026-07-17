package metadataai

import (
	"context"
	"errors"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type serviceStore struct {
	input      CompletionInput
	failedCode string
	saveCalled bool
	saveErr    error
	createdJob Job
}

func (s *serviceStore) LoadInput(context.Context, string, string) (CompletionInput, error) {
	return s.input, nil
}
func (s *serviceStore) CreateJob(_ context.Context, _, _ string, job Job) (Job, error) {
	s.createdJob = job
	job.ID = "job-1"
	return job, nil
}
func (s *serviceStore) FailJob(_ context.Context, _, _ string, job Job, code string) (Job, error) {
	s.failedCode = code
	job.Status = "FAILED"
	job.ErrorCode = code
	return job, nil
}
func (s *serviceStore) SaveResult(_ context.Context, _, _ string, job Job, _ CompletionInput, _ ProviderResult, _ float64) (Job, []Suggestion, error) {
	s.saveCalled = true
	if s.saveErr != nil {
		return job, nil, s.saveErr
	}
	job.Status = "SUCCEEDED"
	return job, []Suggestion{}, nil
}
func (*serviceStore) ListSuggestions(context.Context, string, string, string, int) ([]Suggestion, error) {
	return nil, nil
}
func (*serviceStore) DecideSuggestion(context.Context, string, string, string, string) (Suggestion, error) {
	return Suggestion{}, nil
}

type serviceProvider struct {
	output CompletionOutput
	err    error
	wait   bool
	input  *CompletionInput
}

func (serviceProvider) Name() string     { return "test" }
func (serviceProvider) Model() string    { return "test-model" }
func (serviceProvider) Configured() bool { return true }
func (p serviceProvider) Complete(ctx context.Context, _, _ string, input CompletionInput) (ProviderResult, error) {
	if p.input != nil {
		*p.input = input
	}
	if p.wait {
		<-ctx.Done()
		return ProviderResult{}, ctx.Err()
	}
	return ProviderResult{Output: p.output}, p.err
}

func TestGenerateWithSamplesForwardsAtMostThreeRowsToProvider(t *testing.T) {
	input, output := validCompletion()
	input.StructureHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &serviceStore{input: input}
	var captured CompletionInput
	service := NewService(store, serviceProvider{output: output, input: &captured}, time.Second, 0.8)
	samples := []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}}

	if _, err := service.GenerateWithSamples(context.Background(), "tenant", "actor", "table-1", samples); err != nil {
		t.Fatal(err)
	}
	if len(captured.SampleRows) != 3 || captured.SampleRows[0]["id"] != 1 || captured.SampleRows[2]["id"] != 3 {
		t.Fatalf("sample rows=%#v", captured.SampleRows)
	}
	if store.createdJob.StructureHash != input.StructureHash {
		t.Fatalf("job structure hash=%q", store.createdJob.StructureHash)
	}
}

func TestCompleteTableRejectsStaleExpectedStructureBeforeProviderCall(t *testing.T) {
	input, output := validCompletion()
	input.StructureHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &serviceStore{input: input}
	var captured CompletionInput
	service := NewService(store, serviceProvider{output: output, input: &captured}, time.Second, 0.8)

	err := service.CompleteTable(context.Background(), "tenant", "actor", "table-1", nil,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "item-1", "worker-1", 1)
	if !errors.Is(err, ErrStructureChanged) {
		t.Fatalf("error=%v", err)
	}
	if store.createdJob.TableID != "" || captured.Table.ID != "" {
		t.Fatal("stale structure reached job creation or provider")
	}
}

func TestGenerateRejectsInvalidOutputBeforeFormalPersistence(t *testing.T) {
	input, output := validCompletion()
	output.Columns[0].TargetID = "hallucinated"
	store := &serviceStore{input: input}
	service := NewService(store, serviceProvider{output: output}, time.Second, 0.8)
	result, err := service.Generate(context.Background(), "tenant", "actor", "table-1")
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error=%v", err)
	}
	if store.saveCalled || store.failedCode != "INVALID_OUTPUT" || result.Job.Status != "FAILED" {
		t.Fatalf("saveCalled=%v failedCode=%s result=%#v", store.saveCalled, store.failedCode, result)
	}
}

func TestGenerateRecordsTimeoutWithoutSavingSuggestions(t *testing.T) {
	input, _ := validCompletion()
	store := &serviceStore{input: input}
	service := NewService(store, serviceProvider{wait: true}, 5*time.Millisecond, 0.8)
	_, err := service.Generate(context.Background(), "tenant", "actor", "table-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if store.saveCalled || store.failedCode != "TIMEOUT" {
		t.Fatalf("saveCalled=%v failedCode=%s", store.saveCalled, store.failedCode)
	}
}

func TestGenerateConvertsPersistenceFailureToFailedJob(t *testing.T) {
	input, output := validCompletion()
	store := &serviceStore{input: input, saveErr: errors.New("database unavailable")}
	service := NewService(store, serviceProvider{output: output}, time.Second, 0.8)
	result, err := service.Generate(context.Background(), "tenant", "actor", "table-1")
	if err == nil || store.failedCode != "PERSISTENCE_ERROR" || result.Job.Status != "FAILED" {
		t.Fatalf("error=%v failedCode=%s result=%#v", err, store.failedCode, result)
	}
}

func TestGenerateRecordsTenantQuotaFailureWithoutSavingSuggestions(t *testing.T) {
	input, _ := validCompletion()
	store := &serviceStore{input: input}
	service := NewService(store, serviceProvider{err: aiplatform.ErrQuotaExceeded}, time.Second, 0.8)
	result, err := service.Generate(context.Background(), "tenant", "actor", "table-1")
	if !errors.Is(err, aiplatform.ErrQuotaExceeded) || store.saveCalled || store.failedCode != "QUOTA_EXCEEDED" || result.Job.Status != "FAILED" {
		t.Fatalf("error=%v failedCode=%s result=%#v", err, store.failedCode, result)
	}
}

func TestDecideSuggestionRejectsUnknownDecision(t *testing.T) {
	service := NewService(&serviceStore{}, serviceProvider{}, time.Second, 0.8)
	if _, err := service.DecideSuggestion(context.Background(), "tenant", "actor", "suggestion", "MAYBE"); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("error=%v", err)
	}
}
