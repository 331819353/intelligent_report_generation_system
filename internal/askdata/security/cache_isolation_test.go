package security_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/policy"
)

func TestAskDataCacheKeyBindsEveryMutableSecurityBoundary(t *testing.T) {
	baseScope := cacheIsolationScope(t, "tenant-a", "actor-a", "domain-sales", "analyst", "release-v1", "release-hash-v1")
	base := policy.AskDataCacheKeyInput{
		Scope: baseScope, IRHash: askdata.HashBytes([]byte("ir-v1")),
		SnapshotHash:  askdata.HashBytes([]byte("snapshot-v1")),
		FreshnessHash: askdata.HashBytes([]byte("freshness-v1")), EngineVersion: "engine-v1",
	}
	want, err := policy.BuildAskDataCacheKey(base)
	if err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		name  string
		input policy.AskDataCacheKeyInput
	}{
		{"tenant", base}, {"actor", base}, {"domain", base}, {"role", base},
		{"release id", base}, {"release hash", base}, {"IR", base}, {"snapshot", base},
		{"freshness", base}, {"engine", base},
	}
	variants[0].input.Scope = cacheIsolationScope(t, "tenant-b", "actor-a", "domain-sales", "analyst", "release-v1", "release-hash-v1")
	variants[1].input.Scope = cacheIsolationScope(t, "tenant-a", "actor-b", "domain-sales", "analyst", "release-v1", "release-hash-v1")
	variants[2].input.Scope = cacheIsolationScope(t, "tenant-a", "actor-a", "domain-finance", "analyst", "release-v1", "release-hash-v1")
	variants[3].input.Scope = cacheIsolationScope(t, "tenant-a", "actor-a", "domain-sales", "viewer", "release-v1", "release-hash-v1")
	variants[4].input.Scope = cacheIsolationScope(t, "tenant-a", "actor-a", "domain-sales", "analyst", "release-v2", "release-hash-v1")
	variants[5].input.Scope = cacheIsolationScope(t, "tenant-a", "actor-a", "domain-sales", "analyst", "release-v1", "release-hash-v2")
	variants[6].input.IRHash = askdata.HashBytes([]byte("ir-v2"))
	variants[7].input.SnapshotHash = askdata.HashBytes([]byte("snapshot-v2"))
	variants[8].input.FreshnessHash = askdata.HashBytes([]byte("freshness-v2"))
	variants[9].input.EngineVersion = "engine-v2"
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			got, err := policy.BuildAskDataCacheKey(variant.input)
			if err != nil || got == want {
				t.Fatalf("BuildAskDataCacheKey() = %q, %v; base=%q", got, err, want)
			}
		})
	}

	invalid := []policy.AskDataCacheKeyInput{base, base, base, base, base}
	invalid[0].Scope = askdata.PolicyScope{}
	invalid[1].IRHash = ""
	invalid[2].SnapshotHash = ""
	invalid[3].FreshnessHash = ""
	invalid[4].EngineVersion = " engine-v1 "
	for index, input := range invalid {
		if key, err := policy.BuildAskDataCacheKey(input); err == nil || key != "" {
			t.Fatalf("invalid[%d] cache key = %q, %v", index, key, err)
		}
	}
}

func TestAskDataCacheKeysNeverCrossHitUnderConcurrentTenants(t *testing.T) {
	const tenants = 64
	inputs := make([]policy.AskDataCacheKeyInput, tenants)
	for index := range inputs {
		inputs[index] = policy.AskDataCacheKeyInput{
			Scope: cacheIsolationScope(
				t, askdata.ID(fmt.Sprintf("tenant-%03d", index)), askdata.ID(fmt.Sprintf("actor-%03d", index)),
				"domain-sales", "analyst", "release-shared", "release-shared-hash",
			),
			IRHash: askdata.HashBytes([]byte("shared-ir")), SnapshotHash: askdata.HashBytes([]byte("shared-snapshot")),
			FreshnessHash: askdata.HashBytes([]byte("shared-freshness")), EngineVersion: "engine-v1",
		}
	}

	var entries sync.Map
	errorsFound := make(chan error, tenants*8)
	var wait sync.WaitGroup
	for repetition := 0; repetition < 8; repetition++ {
		for index, input := range inputs {
			wait.Add(1)
			go func(index int, input policy.AskDataCacheKeyInput) {
				defer wait.Done()
				key, err := policy.BuildAskDataCacheKey(input)
				if err != nil {
					errorsFound <- err
					return
				}
				tenant := string(input.Scope.TenantID)
				if previous, loaded := entries.LoadOrStore(key, tenant); loaded && previous != tenant {
					errorsFound <- fmt.Errorf("tenant %s collided with %v at index %d", tenant, previous, index)
				}
			}(index, input)
		}
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	count := 0
	entries.Range(func(_, _ any) bool { count++; return true })
	if count != tenants {
		t.Fatalf("isolated cache key count = %d, want %d", count, tenants)
	}
}

type graphFailurePrimary struct{ calls atomic.Int64 }

func (primary *graphFailurePrimary) Resolve(context.Context, graph.PlanRequest) (graph.GraphPlan, error) {
	primary.calls.Add(1)
	return graph.GraphPlan{}, errors.New("injected graph outage")
}

type poisonedGraphCache struct {
	plan  graph.GraphPlan
	calls atomic.Int64
}

func (cache *poisonedGraphCache) Load(context.Context, graph.PlanRequest) (graph.GraphPlan, bool, error) {
	cache.calls.Add(1)
	return cache.plan, true, nil
}

type poisonedGraphFallback struct {
	plan  graph.GraphPlan
	calls atomic.Int64
}

func (fallback *poisonedGraphFallback) Resolve(context.Context, graph.PlanRequest) (graph.GraphPlan, error) {
	fallback.calls.Add(1)
	return fallback.plan, nil
}

func TestGraphFailureCannotReplayAnotherTenantCacheOrFallback(t *testing.T) {
	requestA, planA := graphIsolationFixture(t, "tenant-a", "actor-a")
	requestB, _ := graphIsolationFixture(t, "tenant-b", "actor-b")
	primary := &graphFailurePrimary{}
	cache := &poisonedGraphCache{plan: planA}
	fallback := &poisonedGraphFallback{plan: planA}
	resolver, err := graph.NewResolver(primary, cache, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if resolution, err := resolver.Resolve(context.Background(), requestA); err != nil ||
		resolution.Plan.Scope.TenantID != requestA.Scope.TenantID {
		t.Fatalf("own-tenant certified cache = %#v, %v", resolution, err)
	}

	const attempts = 64
	errorsFound := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resolution, err := resolver.Resolve(context.Background(), requestB)
			if !errors.Is(err, graph.ErrGraphResolutionUnavailable) {
				errorsFound <- fmt.Errorf("cross-tenant resolution = %#v, %v", resolution, err)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if cache.calls.Load() < attempts+1 || fallback.calls.Load() < attempts {
		t.Fatalf("failure paths were not exercised: primary/cache/fallback=%d/%d/%d",
			primary.calls.Load(), cache.calls.Load(), fallback.calls.Load())
	}
}

type tenantScopedRetrievalStore struct{}

func (tenantScopedRetrievalStore) Exact(
	_ context.Context, scope askdata.PolicyScope, _ string, _ []search.ObjectType, _ int,
) ([]search.RawHit, error) {
	return []search.RawHit{tenantRetrievalHit(scope, 1)}, nil
}

func (tenantScopedRetrievalStore) Lexical(
	_ context.Context, scope askdata.PolicyScope, _ string, _ []search.ObjectType, _ int,
) ([]search.RawHit, error) {
	return []search.RawHit{tenantRetrievalHit(scope, 1.2)}, nil
}

func (tenantScopedRetrievalStore) Vector(
	context.Context, askdata.PolicyScope, []float32, string, []search.ObjectType, int,
) ([]search.RawHit, error) {
	return []search.RawHit{{
		ObjectType: search.ObjectMetric, ObjectVersionID: "metric-foreign-tenant-v1",
		InputHash: askdata.HashBytes([]byte("foreign tenant vector")), Score: 1,
	}}, errors.New("injected vector outage")
}

func tenantRetrievalHit(scope askdata.PolicyScope, score float64) search.RawHit {
	id := askdata.ID("metric-" + string(scope.TenantID) + "-v1")
	return search.RawHit{
		ObjectType: search.ObjectMetric, ObjectVersionID: id,
		InputHash: askdata.HashBytes([]byte("document:" + string(scope.TenantID))), Score: score,
	}
}

func TestVectorFailureKeepsExactLexicalFallbackInsideConcurrentTenantScope(t *testing.T) {
	retriever, err := search.NewRetriever(tenantScopedRetrievalStore{}, search.DefaultRankConfig())
	if err != nil {
		t.Fatal(err)
	}
	const tenants = 64
	errorsFound := make(chan error, tenants)
	var wait sync.WaitGroup
	for index := 0; index < tenants; index++ {
		scope := cacheIsolationScope(
			t, askdata.ID(fmt.Sprintf("tenant-search-%03d", index)), askdata.ID(fmt.Sprintf("actor-search-%03d", index)),
			"domain-sales", "analyst", "release-shared", "release-shared-hash",
		)
		wait.Add(1)
		go func(scope askdata.PolicyScope) {
			defer wait.Done()
			result, err := retriever.Retrieve(context.Background(), search.RetrievalRequest{
				Scope: scope, Mention: "销售额", ObjectTypes: []search.ObjectType{search.ObjectMetric},
				Embedding: make([]float32, search.SearchEmbeddingDimension), EmbeddingModel: "embedding-v1",
			})
			expected := tenantRetrievalHit(scope, 1).ObjectVersionID
			if err != nil || !result.Degraded || result.DegradedReason != "VECTOR_RETRIEVAL_FAILED" ||
				len(result.Candidates) != 1 || result.Candidates[0].ObjectVersionID != expected ||
				result.Candidates[0].ObjectVersionID == "metric-foreign-tenant-v1" ||
				len(result.Candidates[0].Evidence) != 2 {
				errorsFound <- fmt.Errorf("tenant %s retrieval = %#v, %v", scope.TenantID, result, err)
			}
		}(scope)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

type failingAIProvider struct {
	name, model string
	calls       atomic.Int64
}

func (provider *failingAIProvider) Name() string     { return provider.name }
func (provider *failingAIProvider) Model() string    { return provider.model }
func (provider *failingAIProvider) Configured() bool { return true }
func (provider *failingAIProvider) Complete(context.Context, ai.ProviderRequest) (ai.ProviderResult, error) {
	provider.calls.Add(1)
	return ai.ProviderResult{}, &ai.ProviderError{
		Code: ai.ErrorCodeProviderUnavailable, Message: "model unavailable", Retryable: false,
	}
}

type tenantAuditStore struct {
	sequence         atomic.Int64
	mu               sync.Mutex
	tenantByRequest  map[string]string
	starts, failures int
}

func newTenantAuditStore() *tenantAuditStore {
	return &tenantAuditStore{tenantByRequest: map[string]string{}}
}

func (store *tenantAuditStore) Start(_ context.Context, request ai.StartRequest) (ai.RequestRecord, error) {
	id := fmt.Sprintf("request-%d", store.sequence.Add(1))
	store.mu.Lock()
	store.tenantByRequest[id] = request.TenantID
	store.starts++
	store.mu.Unlock()
	return ai.RequestRecord{ID: id}, nil
}

func (store *tenantAuditStore) Complete(_ context.Context, tenantID, requestID string, _ ai.CompletionRecord) error {
	return store.validateTenant(tenantID, requestID)
}

func (store *tenantAuditStore) Fail(_ context.Context, tenantID, requestID string, _ ai.FailureRecord) error {
	if err := store.validateTenant(tenantID, requestID); err != nil {
		return err
	}
	store.mu.Lock()
	store.failures++
	store.mu.Unlock()
	return nil
}

func (store *tenantAuditStore) validateTenant(tenantID, requestID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if stored, exists := store.tenantByRequest[requestID]; !exists || stored != tenantID {
		return fmt.Errorf("AI audit tenant mismatch: request=%s stored=%s got=%s", requestID, stored, tenantID)
	}
	return nil
}

func TestModelFailureAndExplicitFallbackKeepIndependentTenantAudit(t *testing.T) {
	primary := &failingAIProvider{name: "primary", model: "model-primary"}
	fallback := &failingAIProvider{name: "fallback", model: "model-fallback"}
	store := newTenantAuditStore()
	service, err := ai.NewService(store, ai.NewPrimaryFallbackProvider(primary, fallback), ai.ServiceOptions{
		Timeout: time.Second, AttemptTimeout: time.Second, MaxAttempts: 1,
		BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Millisecond, MaxInputBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ai.ProviderRequest{
		Messages: []ai.Message{{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "安全测试"}}}},
		ResponseSchema: ai.JSONSchema{Name: "security_result", Schema: json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}`,
		)},
		MaxOutputTokens: 32,
	}
	const tenants = 64
	errorsFound := make(chan error, tenants)
	var wait sync.WaitGroup
	for index := 0; index < tenants; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			preferred := ""
			if index%2 == 1 {
				preferred = "model-fallback"
			}
			_, err := service.Invoke(context.Background(), ai.Invocation{
				TenantID: fmt.Sprintf("tenant-ai-%03d", index), ActorID: fmt.Sprintf("actor-ai-%03d", index),
				Purpose: ai.PurposeSemanticQuestion, PromptVersion: "security-v1",
				ResourceType: "question", ResourceID: fmt.Sprintf("question-%03d", index),
				PreferredModel: preferred, Request: request,
			})
			if err == nil {
				errorsFound <- fmt.Errorf("tenant %d model failure was accepted", index)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	store.mu.Lock()
	starts, failures := store.starts, store.failures
	store.mu.Unlock()
	if starts != tenants || failures != tenants || primary.calls.Load() != tenants/2 || fallback.calls.Load() != tenants/2 {
		t.Fatalf("model failure accounting = starts:%d failures:%d primary:%d fallback:%d",
			starts, failures, primary.calls.Load(), fallback.calls.Load())
	}
}

func cacheIsolationScope(
	t *testing.T,
	tenantID, actorID, domainID, roleID, releaseID askdata.ID,
	releaseHashSeed string,
) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{roleID},
		askdata.ReleaseRef{ReleaseID: releaseID, ContentHash: askdata.HashBytes([]byte(releaseHashSeed))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func graphIsolationFixture(t *testing.T, tenantID, actorID askdata.ID) (graph.PlanRequest, graph.GraphPlan) {
	t.Helper()
	scope := cacheIsolationScope(
		t, tenantID, actorID, "domain-sales", "analyst", "release-shared", "release-shared-hash",
	)
	model := graph.ObjectVersionRef{ObjectID: "model-orders", VersionID: "model-orders-v1", Version: 1}
	metric := graph.ObjectVersionRef{ObjectID: "metric-sales", VersionID: "metric-sales-v1", Version: 1}
	request := graph.PlanRequest{
		Scope: scope, DomainID: "domain-sales", MetricRefs: []graph.ObjectVersionRef{metric},
		ModelRefs: []graph.ObjectVersionRef{model}, DimensionRefs: []graph.ObjectVersionRef{},
		MemberRefs: []graph.ObjectVersionRef{}, MaxJoinHops: 3, MaxPaths: 16,
	}
	plan, err := graph.NewGraphPlan(
		request, []graph.ObjectVersionRef{model},
		[]graph.MetricModelBinding{{MetricVersionID: metric.VersionID, ModelVersionID: model.VersionID}},
		[]graph.DimensionCompatibility{}, []graph.MemberOwnership{}, []graph.JoinPath{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request, plan
}
