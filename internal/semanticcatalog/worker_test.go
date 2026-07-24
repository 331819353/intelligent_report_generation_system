package semanticcatalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/embedding"
)

const (
	testTenantID  = "11111111-1111-4111-8111-111111111111"
	testTagEvent  = "22222222-2222-4222-8222-222222222222"
	testTagID     = "33333333-3333-4333-8333-333333333333"
	testDocEvent  = "44444444-4444-4444-8444-444444444444"
	testDocument  = "55555555-5555-4555-8555-555555555555"
	testLeaseOne  = "66666666-6666-4666-8666-666666666666"
	testLeaseTwo  = "77777777-7777-4777-8777-777777777777"
	testWorkerID  = "semantic-worker-test"
	testInputHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakeStore struct {
	queue           []Claim
	prepared        map[string]Work
	applied         []Work
	acknowledged    []Work
	completed       []Work
	failures        []Claim
	heartbeats      []Claim
	heartbeatErr    error
	heartbeatErrAt  int
	applyErr        error
	enqueueDocument bool
	retryFailures   bool
}

func (store *fakeStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{testTenantID}, nil
}

func (store *fakeStore) ClaimBatch(
	_ context.Context,
	_ string,
	_ string,
	_ time.Duration,
	limit int,
	includeEmbedding bool,
) ([]Claim, error) {
	claims := []Claim{}
	remaining := []Claim{}
	for _, claim := range store.queue {
		if len(claims) >= limit || (!includeEmbedding && claim.SubjectType == SubjectDocument) {
			remaining = append(remaining, claim)
			continue
		}
		claims = append(claims, claim)
	}
	store.queue = remaining
	return claims, nil
}

func (store *fakeStore) Heartbeat(
	_ context.Context,
	claim Claim,
	_ string,
	_ time.Duration,
) error {
	store.heartbeats = append(store.heartbeats, claim)
	if store.heartbeatErr != nil &&
		(store.heartbeatErrAt == 0 || len(store.heartbeats) >= store.heartbeatErrAt) {
		return store.heartbeatErr
	}
	return nil
}

func (store *fakeStore) Prepare(
	_ context.Context,
	claim Claim,
	_ string,
	_ string,
) (Work, error) {
	work, ok := store.prepared[claim.ID]
	if !ok {
		return Work{}, errors.New("missing prepared work")
	}
	work.Claim = claim
	return work, nil
}

func (store *fakeStore) ApplyDocument(_ context.Context, work Work, _ string) error {
	if store.applyErr != nil {
		return store.applyErr
	}
	store.applied = append(store.applied, work)
	if store.enqueueDocument {
		claim := documentClaim(1, testLeaseTwo)
		store.queue = append(store.queue, claim)
		store.prepared[claim.ID] = Work{
			Claim:      claim,
			DocumentID: testDocument,
			Text:       work.Text,
			InputHash:  work.InputHash,
		}
		store.enqueueDocument = false
	}
	return nil
}

func (store *fakeStore) Acknowledge(_ context.Context, work Work, _ string) error {
	store.acknowledged = append(store.acknowledged, work)
	return nil
}

func (store *fakeStore) CompleteEmbedding(
	_ context.Context,
	work Work,
	_ string,
	_ string,
	_ []float32,
) error {
	store.completed = append(store.completed, work)
	return nil
}

func (store *fakeStore) Fail(_ context.Context, claim Claim, _ string, _ string) error {
	store.failures = append(store.failures, claim)
	if store.retryFailures && claim.Attempt < claim.MaxAttempts {
		retry := claim
		retry.Attempt++
		retry.LeaseToken = testLeaseTwo
		store.queue = append(store.queue, retry)
		store.prepared[retry.ID] = Work{
			Claim: retry, DocumentID: testDocument,
			Text:      "对象类型：SEMANTIC_DOCUMENT\n标签名称：销售",
			InputHash: testInputHash,
		}
	}
	return nil
}

type fakeProvider struct {
	failuresLeft int
	calls        [][]string
}

func (provider *fakeProvider) Configured() bool { return true }
func (provider *fakeProvider) Model() string    { return "semantic-test-model" }
func (provider *fakeProvider) Dimensions() int  { return VectorDimensions }
func (provider *fakeProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.calls = append(provider.calls, append([]string(nil), inputs...))
	if provider.failuresLeft > 0 {
		provider.failuresLeft--
		return nil, embedding.ErrUnavailable
	}
	vectors := make([][]float32, len(inputs))
	for index := range inputs {
		vectors[index] = make([]float32, VectorDimensions)
		vectors[index][0] = float32(index + 1)
	}
	return vectors, nil
}

type blockingProvider struct {
	started  chan struct{}
	canceled chan struct{}
}

func (provider *blockingProvider) Configured() bool { return true }
func (provider *blockingProvider) Model() string    { return "semantic-test-model" }
func (provider *blockingProvider) Dimensions() int  { return VectorDimensions }
func (provider *blockingProvider) Embed(
	ctx context.Context,
	_ []string,
) ([][]float32, error) {
	close(provider.started)
	<-ctx.Done()
	close(provider.canceled)
	return nil, ctx.Err()
}

func TestTagEditRebuildsDocumentThenEmbedsIt(t *testing.T) {
	tagClaim := Claim{
		ID: testTagEvent, TenantID: testTenantID, SubjectType: SubjectTag,
		SubjectRef: testTagID, EventKind: EventRebuild, EventVersion: 2,
		Attempt: 1, MaxAttempts: 3, LeaseToken: testLeaseOne,
	}
	store := &fakeStore{
		queue: []Claim{tagClaim},
		prepared: map[string]Work{
			testTagEvent: {
				Claim: tagClaim, Subject: Subject{Type: SubjectTag, TagID: testTagID},
				Text: "对象类型：TAG\n标签名称：销售", InputHash: testInputHash,
			},
		},
		enqueueDocument: true,
	}
	provider := &fakeProvider{}
	worker := NewWorker(store, provider)

	count, err := worker.ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)
	if err != nil || count != 1 {
		t.Fatalf("rebuild tick failed: count=%d err=%v", count, err)
	}
	if len(store.applied) != 1 || len(provider.calls) != 0 {
		t.Fatalf("tag rebuild did not stay asynchronous: applied=%d calls=%d", len(store.applied), len(provider.calls))
	}

	count, err = worker.ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)
	if err != nil || count != 1 {
		t.Fatalf("embedding tick failed: count=%d err=%v", count, err)
	}
	if len(provider.calls) != 1 || len(provider.calls[0]) != 1 || len(store.completed) != 1 {
		t.Fatalf("rebuilt document was not embedded exactly once: calls=%v complete=%d", provider.calls, len(store.completed))
	}
	if store.completed[0].InputHash != testInputHash {
		t.Fatal("embedding completion was not fenced to the rebuilt input hash")
	}
}

func TestEmbeddingHeartbeatLossCancelsProviderAndForbidsTerminalWrite(t *testing.T) {
	claim := documentClaim(1, testLeaseOne)
	store := &fakeStore{
		queue: []Claim{claim},
		prepared: map[string]Work{
			claim.ID: {
				Claim: claim, DocumentID: testDocument,
				Text: "对象类型：TAG\n标签名称：销售", InputHash: testInputHash,
			},
		},
		heartbeatErr:   ErrLeaseLost,
		heartbeatErrAt: 2,
	}
	provider := &blockingProvider{
		started: make(chan struct{}), canceled: make(chan struct{}),
	}
	worker := NewWorker(store, provider)
	worker.heartbeatInterval = func(time.Duration) time.Duration {
		return time.Millisecond
	}

	type result struct {
		count int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		count, err := worker.ProcessNext(
			context.Background(), testTenantID, testWorkerID, time.Second,
		)
		done <- result{count: count, err: err}
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("embedding call did not start")
	}
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after heartbeat loss")
	}
	if got.count != 1 || !errors.Is(got.err, ErrLeaseLost) {
		t.Fatalf("count=%d err=%v", got.count, got.err)
	}
	select {
	case <-provider.canceled:
	default:
		t.Fatal("heartbeat loss did not cancel the provider context")
	}
	if len(store.heartbeats) < 2 || len(store.completed) != 0 ||
		len(store.failures) != 0 || len(store.acknowledged) != 0 {
		t.Fatalf(
			"heartbeats=%d completed=%d failures=%d acknowledged=%d",
			len(store.heartbeats), len(store.completed), len(store.failures),
			len(store.acknowledged),
		)
	}
}

func TestLeaseFenceRejectsStaleWorker(t *testing.T) {
	tagClaim := Claim{
		ID: testTagEvent, TenantID: testTenantID, SubjectType: SubjectTag,
		SubjectRef: testTagID, EventKind: EventRebuild, EventVersion: 7,
		Attempt: 1, MaxAttempts: 3, LeaseToken: testLeaseOne,
	}
	store := &fakeStore{
		queue: []Claim{tagClaim},
		prepared: map[string]Work{
			testTagEvent: {
				Claim: tagClaim, Subject: Subject{Type: SubjectTag, TagID: testTagID},
				Text: "对象类型：TAG\n标签名称：旧名称", InputHash: testInputHash,
			},
		},
		applyErr: ErrLeaseLost,
	}
	count, err := NewWorker(store, &fakeProvider{}).ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)
	if count != 1 || !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale lease was not surfaced: count=%d err=%v", count, err)
	}
	if len(store.applied) != 0 || len(store.completed) != 0 {
		t.Fatal("stale worker mutated semantic state")
	}
}

func TestProviderFailureIsRetriedWithoutLosingEvent(t *testing.T) {
	claim := documentClaim(1, testLeaseOne)
	store := &fakeStore{
		queue: []Claim{claim},
		prepared: map[string]Work{
			claim.ID: {
				Claim: claim, DocumentID: testDocument,
				Text: "对象类型：TAG\n标签名称：销售", InputHash: testInputHash,
			},
		},
		retryFailures: true,
	}
	provider := &fakeProvider{failuresLeft: 1}
	worker := NewWorker(store, provider)

	count, err := worker.ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)
	if count != 1 || !errors.Is(err, embedding.ErrUnavailable) {
		t.Fatalf("provider failure was not reported: count=%d err=%v", count, err)
	}
	if len(store.failures) != 1 || len(store.queue) != 1 || len(store.completed) != 0 {
		t.Fatalf("failed event was lost instead of retried: failures=%d queue=%d complete=%d",
			len(store.failures), len(store.queue), len(store.completed))
	}

	count, err = worker.ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)
	if err != nil || count != 1 || len(store.completed) != 1 {
		t.Fatalf("retry did not complete: count=%d err=%v complete=%d", count, err, len(store.completed))
	}
}

func TestDimensionMemberDocumentNeverLeavesTenantLocalIndex(t *testing.T) {
	claim := documentClaim(1, testLeaseOne)
	store := &fakeStore{
		queue: []Claim{claim},
		prepared: map[string]Work{
			claim.ID: {
				Claim: claim, DocumentID: testDocument,
				Text:      "对象类型：DIMENSION_MEMBER\n成员键：13800138000",
				InputHash: testInputHash, EmbeddingSuppressed: true,
			},
		},
	}
	provider := &fakeProvider{}

	count, err := NewWorker(store, provider).ProcessNext(
		context.Background(), testTenantID, testWorkerID, time.Minute,
	)

	if err != nil || count != 1 || len(provider.calls) != 0 ||
		len(store.completed) != 0 || len(store.acknowledged) != 1 {
		t.Fatalf(
			"member document escaped suppression: count=%d err=%v calls=%d completed=%d acknowledged=%d",
			count, err, len(provider.calls), len(store.completed), len(store.acknowledged),
		)
	}
}

func TestClaimAndCompletionSQLUseRequiredFences(t *testing.T) {
	for _, fragment := range []string{
		"FOR UPDATE SKIP LOCKED",
		"lease_token=gen_random_uuid()",
		"attempt<max_attempts",
		"subject_type<>'SEMANTIC_DOCUMENT'",
	} {
		if !strings.Contains(claimBatchSQL, fragment) {
			t.Fatalf("claim SQL is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"status='RUNNING'",
		"lease_owner=$2",
		"lease_token::text=$3",
		"event_version=$4",
		"lease_expires_at>now()",
	} {
		if !strings.Contains(verifyLeaseForUpdateSQL, fragment) {
			t.Fatalf("completion fence is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"status='RUNNING'",
		"lease_owner=$3",
		"lease_token::text=$4",
		"event_version=$5",
		"attempt=$6",
		"lease_expires_at>clock_timestamp()",
	} {
		if !strings.Contains(heartbeatEventSQL, fragment) {
			t.Fatalf("heartbeat fence is missing %q", fragment)
		}
	}
}

func TestEmbeddingBatchIsBoundedByCountAndInputBytes(t *testing.T) {
	documents := make([]Work, MaxBatchSize+1)
	for index := range documents {
		documents[index].Text = "短文档"
	}
	end, inputs := embeddingBatch(documents, 0)
	if end != MaxBatchSize || len(inputs) != MaxBatchSize {
		t.Fatalf("batch count was not capped: end=%d inputs=%d", end, len(inputs))
	}

	documents = []Work{
		{Text: strings.Repeat("a", maxBatchInputBytes-10)},
		{Text: strings.Repeat("b", 20)},
	}
	end, inputs = embeddingBatch(documents, 0)
	if end != 1 || len(inputs) != 1 {
		t.Fatalf("batch input bytes were not capped: end=%d inputs=%d", end, len(inputs))
	}
}

func documentClaim(attempt int, token string) Claim {
	return Claim{
		ID: testDocEvent, TenantID: testTenantID, SubjectType: SubjectDocument,
		SubjectRef: testDocument, EventKind: EventRebuild, EventVersion: 1,
		Attempt: attempt, MaxAttempts: 3, LeaseToken: token,
	}
}
