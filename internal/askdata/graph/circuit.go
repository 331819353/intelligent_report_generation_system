package graph

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultGraphFailureThreshold        = 3
	DefaultGraphCircuitOpenDuration     = 30 * time.Second
	DefaultGraphDegradedAlertPermillion = 50_000
	graphRateScale                      = 1_000_000
)

var ErrGraphCircuitOpen = errors.New("graph circuit breaker is open")

type CircuitBreaker struct {
	mu               sync.Mutex
	failureThreshold int
	openDuration     time.Duration
	now              func() time.Time
	consecutive      int
	openUntil        time.Time
}

func NewCircuitBreaker(failureThreshold int, openDuration time.Duration) (*CircuitBreaker, error) {
	return newCircuitBreaker(failureThreshold, openDuration, time.Now)
}

func newCircuitBreaker(
	failureThreshold int,
	openDuration time.Duration,
	now func() time.Time,
) (*CircuitBreaker, error) {
	if failureThreshold < 1 || failureThreshold > 100 || openDuration <= 0 || now == nil {
		return nil, errors.New("graph circuit breaker configuration is invalid")
	}
	return &CircuitBreaker{
		failureThreshold: failureThreshold, openDuration: openDuration, now: now,
	}, nil
}

func (breaker *CircuitBreaker) AllowPrimary() bool {
	if breaker == nil {
		return true
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.openUntil.IsZero() {
		return true
	}
	if breaker.now().Before(breaker.openUntil) {
		return false
	}
	breaker.openUntil = time.Time{}
	breaker.consecutive = 0
	return true
}

func (breaker *CircuitBreaker) RecordSuccess() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	breaker.consecutive = 0
	breaker.openUntil = time.Time{}
	breaker.mu.Unlock()
}

func (breaker *CircuitBreaker) RecordFailure() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if !breaker.openUntil.IsZero() && breaker.now().Before(breaker.openUntil) {
		return
	}
	breaker.consecutive++
	if breaker.consecutive >= breaker.failureThreshold {
		breaker.openUntil = breaker.now().Add(breaker.openDuration)
	}
}

type GraphDegradationRate struct {
	Name           string `json:"name"`
	Total          uint64 `json:"total"`
	Degraded       uint64 `json:"degraded"`
	RatePermillion uint64 `json:"ratePermillion"`
	Alerting       bool   `json:"alerting"`
}

type GraphDegradationMetrics struct {
	total          atomic.Uint64
	degraded       atomic.Uint64
	alerting       atomic.Bool
	alertThreshold uint64
	alert          func(GraphDegradationRate)
}

func NewGraphDegradationMetrics(
	alertThresholdPermillion uint64,
	alert func(GraphDegradationRate),
) (*GraphDegradationMetrics, error) {
	if alertThresholdPermillion > graphRateScale {
		return nil, errors.New("graph degraded alert threshold is invalid")
	}
	return &GraphDegradationMetrics{
		alertThreshold: alertThresholdPermillion,
		alert:          alert,
	}, nil
}

func (metrics *GraphDegradationMetrics) Observe(degraded bool) {
	if metrics == nil {
		return
	}
	metrics.total.Add(1)
	if degraded {
		metrics.degraded.Add(1)
	}
	snapshot := metrics.Snapshot()
	shouldAlert := snapshot.Total > 0 && snapshot.RatePermillion > metrics.alertThreshold
	previous := metrics.alerting.Swap(shouldAlert)
	if shouldAlert && !previous && metrics.alert != nil {
		snapshot.Alerting = true
		metrics.alert(snapshot)
	}
}

func (metrics *GraphDegradationMetrics) Snapshot() GraphDegradationRate {
	if metrics == nil {
		return GraphDegradationRate{Name: "graph_degraded_rate"}
	}
	total, degraded := metrics.total.Load(), metrics.degraded.Load()
	rate := uint64(0)
	if total > 0 {
		rate = degraded * graphRateScale / total
	}
	return GraphDegradationRate{
		Name: "graph_degraded_rate", Total: total, Degraded: degraded,
		RatePermillion: rate, Alerting: metrics.alerting.Load(),
	}
}
