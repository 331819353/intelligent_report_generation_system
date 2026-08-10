package graph

import (
	"context"
	"testing"
	"time"
)

func TestResolverCircuitBreakerSkipsNebulaWhileOpen(t *testing.T) {
	request, plan := resolverFixture(t)
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	breaker, err := newCircuitBreaker(3, 30*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewGraphDegradationMetrics(DefaultGraphDegradedAlertPermillion, nil)
	if err != nil {
		t.Fatal(err)
	}
	primary := &primaryPlannerStub{err: ErrGraphQueryFailed}
	cache := &certifiedCacheStub{}
	fallback := &fallbackPlannerStub{plan: plan}
	resolver, err := NewResolverWithOptions(primary, cache, fallback, ResolverOptions{
		Circuit: breaker, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		resolution, resolveErr := resolver.Resolve(context.Background(), request)
		if resolveErr != nil || resolution.DegradationReason != DegradationNebulaUnavailable {
			t.Fatalf("failure %d resolution = %#v, %v", index+1, resolution, resolveErr)
		}
	}
	resolution, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 3 || resolution.DegradationReason != DegradationNebulaCircuitOpen {
		t.Fatalf("open circuit called primary %d times, resolution=%#v", primary.calls, resolution)
	}

	now = now.Add(31 * time.Second)
	if _, err := resolver.Resolve(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 4 {
		t.Fatalf("expired circuit did not retry primary, calls=%d", primary.calls)
	}
	snapshot := resolver.MetricsSnapshot()
	if snapshot.Name != "graph_degraded_rate" || snapshot.Total != 5 || snapshot.Degraded != 5 ||
		snapshot.RatePermillion != 1_000_000 || !snapshot.Alerting {
		t.Fatalf("degradation metrics = %#v", snapshot)
	}
}

func TestGraphDegradedRateAlertsOnlyAboveFivePercent(t *testing.T) {
	alerts := make([]GraphDegradationRate, 0, 1)
	metrics, err := NewGraphDegradationMetrics(50_000, func(rate GraphDegradationRate) {
		alerts = append(alerts, rate)
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 19; index++ {
		metrics.Observe(false)
	}
	metrics.Observe(true)
	if snapshot := metrics.Snapshot(); snapshot.RatePermillion != 50_000 || snapshot.Alerting || len(alerts) != 0 {
		t.Fatalf("five percent should not alert: %#v, alerts=%d", snapshot, len(alerts))
	}
	metrics.Observe(true)
	if snapshot := metrics.Snapshot(); !snapshot.Alerting || snapshot.RatePermillion <= 50_000 || len(alerts) != 1 {
		t.Fatalf("above five percent did not alert: %#v, alerts=%d", snapshot, len(alerts))
	}
}

func TestCircuitBreakerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewCircuitBreaker(0, time.Second); err == nil {
		t.Fatal("zero failure threshold was accepted")
	}
	if _, err := NewGraphDegradationMetrics(1_000_001, nil); err == nil {
		t.Fatal("invalid degradation rate threshold was accepted")
	}
}
