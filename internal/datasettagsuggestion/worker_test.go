package datasettagsuggestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
)

type fakeStore struct {
	claim             *Claim
	input             Input
	loadErr           error
	completion        Completion
	skipCode          string
	failCode          string
	retryable         bool
	claimCalls        int
	heartbeatCalls    int
	heartbeatErr      error
	heartbeatErrAfter int
	completeCalls     int
	completeErr       error
}

func (store *fakeStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{uuid.NewString()}, nil
}
func (store *fakeStore) ClaimNext(
	context.Context, string, string, time.Duration,
) (*Claim, error) {
	store.claimCalls++
	return store.claim, nil
}
func (store *fakeStore) Heartbeat(
	context.Context, Claim, string, time.Duration,
) error {
	store.heartbeatCalls++
	if store.heartbeatErr != nil &&
		(store.heartbeatErrAfter == 0 || store.heartbeatCalls >= store.heartbeatErrAfter) {
		return store.heartbeatErr
	}
	return nil
}
func (store *fakeStore) LoadInput(context.Context, Claim, string) (Input, error) {
	return store.input, store.loadErr
}
func (store *fakeStore) Complete(
	_ context.Context, _ Claim, _ string, completion Completion,
) error {
	store.completeCalls++
	store.completion = completion
	return store.completeErr
}
func (store *fakeStore) Skip(
	_ context.Context, _ Claim, _ string, code string,
) error {
	store.skipCode = code
	return nil
}
func (store *fakeStore) Fail(
	_ context.Context, _ Claim, _ string, code string, retryable bool,
) error {
	store.failCode = code
	store.retryable = retryable
	return nil
}

func TestWorkerCompletesSuggestionAndLeavesApprovalToManagement(t *testing.T) {
	tag := TaxonomyTag{
		ID: uuid.NewString(), Code: "fact_table", Name: "事实表",
		Category: "TABLE_FUNCTION",
	}
	claim := testClaim()
	store := &fakeStore{claim: &claim, input: testInput(tag)}
	content, _ := json.Marshal(map[string]any{"items": []map[string]any{{
		"tagId": tag.ID, "confidence": 0.91, "rationale": "元数据支持",
	}}})
	worker := NewWorker(store, NewGenerator(
		&fakeInvoker{configured: true, content: content}, time.Second,
	))
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "worker-1", time.Minute,
	)
	if err != nil || !processed || len(store.completion.Suggestions) != 1 ||
		store.skipCode != "" || store.failCode != "" {
		t.Fatalf(
			"processed=%v completion=%+v skip=%q fail=%q err=%v",
			processed, store.completion, store.skipCode, store.failCode, err,
		)
	}
}

type blockingInvoker struct {
	started  chan struct{}
	canceled chan struct{}
}

func (invoker *blockingInvoker) Configured() bool { return true }

func (invoker *blockingInvoker) Invoke(
	ctx context.Context,
	_ aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	close(invoker.started)
	<-ctx.Done()
	close(invoker.canceled)
	return aiplatform.InvocationResult{}, ctx.Err()
}

func TestWorkerHeartbeatLossCancelsModelAndForbidsTerminalWrite(t *testing.T) {
	claim := testClaim()
	tag := TaxonomyTag{
		ID: uuid.NewString(), Code: "fact_table", Name: "事实表",
		Category: "TABLE_FUNCTION",
	}
	store := &fakeStore{
		claim: &claim, input: testInput(tag),
		heartbeatErr: ErrLeaseLost, heartbeatErrAfter: 2,
	}
	invoker := &blockingInvoker{
		started: make(chan struct{}), canceled: make(chan struct{}),
	}
	worker := NewWorker(store, NewGenerator(invoker, time.Minute))
	worker.heartbeatInterval = func(time.Duration) time.Duration {
		return time.Millisecond
	}

	type result struct {
		processed bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		processed, err := worker.ProcessNext(
			context.Background(), claim.TenantID, "worker-1", time.Second,
		)
		done <- result{processed: processed, err: err}
	}()

	select {
	case <-invoker.started:
	case <-time.After(time.Second):
		t.Fatal("model call did not start")
	}
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after heartbeat loss")
	}
	if !got.processed || !errors.Is(got.err, ErrLeaseLost) {
		t.Fatalf("processed=%v err=%v", got.processed, got.err)
	}
	select {
	case <-invoker.canceled:
	default:
		t.Fatal("heartbeat loss did not cancel the model context")
	}
	if store.heartbeatCalls < 2 || store.completeCalls != 0 ||
		store.failCode != "" || store.skipCode != "" {
		t.Fatalf(
			"heartbeats=%d complete=%d fail=%q skip=%q",
			store.heartbeatCalls, store.completeCalls, store.failCode, store.skipCode,
		)
	}
}

func TestHeartbeatSQLUsesOwnerTokenAndClaimVersionFences(t *testing.T) {
	for _, fragment := range []string{
		"status='RUNNING'",
		"lease_owner=$3",
		"lease_token=$4::uuid",
		"attempt=$5",
		"dataset_version_id=$6::uuid",
		"lease_expires_at>clock_timestamp()",
	} {
		if !strings.Contains(heartbeatJobSQL, fragment) {
			t.Fatalf("heartbeat SQL is missing %q", fragment)
		}
	}
}

func TestCompletionSemanticGovernanceGateMatchesDatabaseTriggerScope(t *testing.T) {
	for _, fragment := range []string{
		"pg_advisory_xact_lock",
		"hashtextextended",
		"semantic-governance-write:",
		"platform.current_tenant_id()",
	} {
		if !strings.Contains(semanticGovernanceWriteGateSQL, fragment) {
			t.Fatalf("completion governance gate SQL is missing %q", fragment)
		}
	}
}

func TestWorkerSkipsChangedVersionBeforeCallingModel(t *testing.T) {
	claim := testClaim()
	invoker := &fakeInvoker{configured: true}
	store := &fakeStore{claim: &claim, loadErr: ErrSubjectChanged}
	processed, err := NewWorker(
		store, NewGenerator(invoker, time.Second),
	).ProcessNext(context.Background(), claim.TenantID, "worker-1", time.Minute)
	if err != nil || !processed || store.skipCode != "SUBJECT_CHANGED" ||
		invoker.invocation.Purpose != "" {
		t.Fatalf(
			"processed=%v skip=%q invocation=%+v err=%v",
			processed, store.skipCode, invoker.invocation, err,
		)
	}
}

func TestWorkerLeavesPendingJobsUnclaimedWhenProviderIsNotConfigured(t *testing.T) {
	claim := testClaim()
	store := &fakeStore{claim: &claim}
	processed, err := NewWorker(
		store, NewGenerator(&fakeInvoker{}, time.Second),
	).ProcessNext(context.Background(), claim.TenantID, "worker-1", time.Minute)
	if err != nil || processed || store.claimCalls != 0 {
		t.Fatalf("processed=%v claimCalls=%d err=%v", processed, store.claimCalls, err)
	}
}

func TestWorkerClassifiesRetryableProviderFailure(t *testing.T) {
	claim := testClaim()
	tag := TaxonomyTag{
		ID: uuid.NewString(), Code: "fact_table", Name: "事实表",
		Category: "TABLE_FUNCTION",
	}
	store := &fakeStore{claim: &claim, input: testInput(tag)}
	providerErr := &aiplatform.ProviderError{
		Code: aiplatform.ErrorCodeRateLimited, Retryable: true,
	}
	processed, err := NewWorker(store, NewGenerator(
		&fakeInvoker{configured: true, err: providerErr}, time.Second,
	)).ProcessNext(context.Background(), claim.TenantID, "worker-1", time.Minute)
	if !processed || !errors.Is(err, providerErr) ||
		store.failCode != string(aiplatform.ErrorCodeRateLimited) ||
		!store.retryable {
		t.Fatalf(
			"processed=%v fail=%q retryable=%v err=%v",
			processed, store.failCode, store.retryable, err,
		)
	}
}

var _ Store = (*fakeStore)(nil)
