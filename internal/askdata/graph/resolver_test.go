package graph

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type primaryPlannerStub struct {
	plan  GraphPlan
	err   error
	calls int
}

func (stub *primaryPlannerStub) Resolve(context.Context, PlanRequest) (GraphPlan, error) {
	stub.calls++
	return stub.plan, stub.err
}

type certifiedCacheStub struct {
	plan  GraphPlan
	hit   bool
	err   error
	calls int
}

func (stub *certifiedCacheStub) Load(context.Context, PlanRequest) (GraphPlan, bool, error) {
	stub.calls++
	return stub.plan, stub.hit, stub.err
}

type fallbackPlannerStub struct {
	plan  GraphPlan
	err   error
	calls int
}

func (stub *fallbackPlannerStub) Resolve(context.Context, PlanRequest) (GraphPlan, error) {
	stub.calls++
	return stub.plan, stub.err
}

func TestResolverUsesNebulaWithoutTouchingDegradedPaths(t *testing.T) {
	request, plan := resolverFixture(t)
	primary := &primaryPlannerStub{plan: plan}
	cache := &certifiedCacheStub{}
	fallback := &fallbackPlannerStub{}
	resolver, err := NewResolver(primary, cache, fallback)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != ResolutionSourceNebula || resolution.Degraded ||
		resolution.Plan.PlanHash != plan.PlanHash || primary.calls != 1 ||
		cache.calls != 0 || fallback.calls != 0 {
		t.Fatalf("unexpected primary resolution: %#v, calls=%d/%d/%d", resolution, primary.calls, cache.calls, fallback.calls)
	}
}

func TestResolverReplaysOnlyExactCertifiedCacheOnNebulaFailure(t *testing.T) {
	request, plan := resolverFixture(t)
	primary := &primaryPlannerStub{err: ErrGraphQueryFailed}
	cache := &certifiedCacheStub{plan: plan, hit: true}
	fallback := &fallbackPlannerStub{err: errors.New("must not run")}
	resolver, _ := NewResolver(primary, cache, fallback)
	resolution, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != ResolutionSourceCertifiedCache || !resolution.Degraded ||
		resolution.DegradationReason != DegradationNebulaUnavailable || fallback.calls != 0 {
		t.Fatalf("unexpected cache resolution: %#v", resolution)
	}

	cache.plan.PlanHash = askdata.HashBytes([]byte("tampered"))
	fallback.plan, fallback.err = plan, nil
	resolution, err = resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != ResolutionSourcePostgresFallback || fallback.calls != 1 {
		t.Fatalf("tampered cache was replayed: %#v", resolution)
	}
}

func TestResolverHandlesInjectedNebulaTransportFailure(t *testing.T) {
	request, plan := resolverFixture(t)
	client, err := NewClient(fakeNebulaExecutor{err: errors.New("injected graphd outage")})
	if err != nil {
		t.Fatal(err)
	}
	cache := &certifiedCacheStub{plan: plan, hit: true}
	fallback := &fallbackPlannerStub{err: errors.New("must not run")}
	resolver, err := NewResolver(client, cache, fallback)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != ResolutionSourceCertifiedCache ||
		resolution.DegradationReason != DegradationNebulaUnavailable || fallback.calls != 0 {
		t.Fatalf("injected Nebula outage did not use certified cache: %#v", resolution)
	}
}

func TestResolverFallsBackWhenNebulaReturnsNoMetricRelationship(t *testing.T) {
	request, plan := resolverFixture(t)
	empty, err := NewGraphPlan(request, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	primary := &primaryPlannerStub{plan: empty}
	cache := &certifiedCacheStub{}
	fallback := &fallbackPlannerStub{plan: plan}
	resolver, _ := NewResolver(primary, cache, fallback)
	resolution, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != ResolutionSourcePostgresFallback ||
		resolution.DegradationReason != DegradationNebulaResultIncomplete ||
		cache.calls != 1 || fallback.calls != 1 {
		t.Fatalf("unexpected incomplete-result fallback: %#v", resolution)
	}
}

func TestResolverFailsClosedWhenEveryCertifiedPathFails(t *testing.T) {
	request, _ := resolverFixture(t)
	primary := &primaryPlannerStub{err: errors.New("nebula down")}
	cache := &certifiedCacheStub{err: ErrCertifiedCacheInvalid}
	fallback := &fallbackPlannerStub{err: ErrFallbackLimitExceeded}
	resolver, _ := NewResolver(primary, cache, fallback)
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrGraphResolutionUnavailable) ||
		!errors.Is(err, ErrCertifiedCacheInvalid) || !errors.Is(err, ErrFallbackLimitExceeded) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolverDoesNotTurnCancellationIntoFallback(t *testing.T) {
	request, _ := resolverFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary := &primaryPlannerStub{err: context.Canceled}
	cache := &certifiedCacheStub{}
	fallback := &fallbackPlannerStub{}
	resolver, _ := NewResolver(primary, cache, fallback)
	if _, err := resolver.Resolve(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if primary.calls != 0 || cache.calls != 0 || fallback.calls != 0 {
		t.Fatalf("canceled request touched planners: %d/%d/%d", primary.calls, cache.calls, fallback.calls)
	}
}

func TestResolutionRejectsAnotherRequestEvenWithAValidPlan(t *testing.T) {
	request, plan := resolverFixture(t)
	other := request
	other.MaxJoinHops = 1
	if err := (Resolution{Plan: plan, Source: ResolutionSourceNebula}).Validate(other); err == nil {
		t.Fatal("Resolution.Validate accepted a plan for another exact request")
	}
}

func resolverFixture(t *testing.T) (PlanRequest, GraphPlan) {
	t.Helper()
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-orders-lines@v1",
		FromModelVersionID: request.ModelRefs[0].VersionID,
		ToModelVersionID:   request.ModelRefs[1].VersionID,
		Direction:          TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewGraphPlan(
		request, request.ModelRefs,
		[]MetricModelBinding{
			{MetricVersionID: request.MetricRefs[0].VersionID, ModelVersionID: request.ModelRefs[0].VersionID},
			{MetricVersionID: request.MetricRefs[1].VersionID, ModelVersionID: request.ModelRefs[1].VersionID},
		},
		nil, nil, []JoinPath{path},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request, plan
}
