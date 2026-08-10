package answer

import (
	"errors"
	"math"
	"regexp"
	"sort"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	NarrativeReleaseFailureMaximum = 0.02
	NarrativeDegradedAlertMinimum  = 0.05
)

var metricFailureCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type NarrativeRunType string

const (
	NarrativeRunAskData NarrativeRunType = "ASKDATA"
	NarrativeRunReport  NarrativeRunType = "REPORT"
)

type NarrativeMetricInput struct {
	DomainID askdata.ID
	RunType  NarrativeRunType
	Reports  []VerifyReport
	Degraded bool
}

type NarrativeMetricSnapshot struct {
	GeneratedRuns           uint64
	TerminalFailureRuns     uint64
	DegradedRuns            uint64
	RetriedRuns             uint64
	VerificationFailureRate float64
	DegradedRate            float64
	RetryRate               float64
	DegradedAlert           bool
	FailureByCode           map[VerifyCode]uint64
}

type narrativeMetricKey struct {
	domainID askdata.ID
	runType  NarrativeRunType
}

// NarrativeMetrics owns the shared AskData/Report denominator. It exposes
// gauges for the governed rates and a cumulative code distribution. A process
// restart resets the in-memory Prometheus window; durable release gates still
// recompute their facts from evaluation_runs in PostgreSQL.
type NarrativeMetrics struct {
	mu     sync.Mutex
	values map[narrativeMetricKey]NarrativeMetricSnapshot

	failureRate   *prometheus.GaugeVec
	degradedRate  *prometheus.GaugeVec
	retryRate     *prometheus.GaugeVec
	failureByCode *prometheus.GaugeVec
}

func NewNarrativeMetrics(registerer prometheus.Registerer) (*NarrativeMetrics, error) {
	if registerer == nil {
		return nil, errors.New("narrative metric registerer is required")
	}
	labels := []string{"domain_id", "run_type"}
	metrics := &NarrativeMetrics{
		values: map[narrativeMetricKey]NarrativeMetricSnapshot{},
		failureRate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "answer_verification_failure_rate",
			Help: "Runs still failing after two narrative verification attempts divided by runs that generated narrative.",
		}, labels),
		degradedRate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "narrative_degraded_rate",
			Help: "Narrative-degraded runs divided by runs that generated narrative.",
		}, labels),
		retryRate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "answer_verification_retry_rate",
			Help: "Narrative runs with a regeneration attempt divided by runs that generated narrative.",
		}, labels),
		failureByCode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "answer_failure_by_code",
			Help: "Cumulative narrative verification failures by stable verifier code.",
		}, []string{"domain_id", "failure_code", "run_type"}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.failureRate, metrics.degradedRate, metrics.retryRate, metrics.failureByCode,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (metrics *NarrativeMetrics) Record(input NarrativeMetricInput) (NarrativeMetricSnapshot, error) {
	if metrics == nil || input.DomainID.Validate() != nil ||
		(input.RunType != NarrativeRunAskData && input.RunType != NarrativeRunReport) {
		return NarrativeMetricSnapshot{}, errors.New("narrative metric input is invalid")
	}
	for _, report := range input.Reports {
		for _, failure := range report.Failures {
			if !metricFailureCodePattern.MatchString(string(failure.Reason)) {
				return NarrativeMetricSnapshot{}, errors.New("narrative failure code is invalid")
			}
		}
	}
	key := narrativeMetricKey{domainID: input.DomainID, runType: input.RunType}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	value := metrics.values[key]
	if value.FailureByCode == nil {
		value.FailureByCode = map[VerifyCode]uint64{}
	}
	if len(input.Reports) > 0 {
		value.GeneratedRuns++
		if len(input.Reports) > 1 {
			value.RetriedRuns++
		}
		if input.Degraded {
			value.DegradedRuns++
		}
		if input.Degraded && len(input.Reports) == 2 && !input.Reports[1].Passed {
			value.TerminalFailureRuns++
		}
	}
	for _, report := range input.Reports {
		for _, failure := range report.Failures {
			value.FailureByCode[failure.Reason]++
		}
	}
	value.VerificationFailureRate = safeMetricRate(value.TerminalFailureRuns, value.GeneratedRuns)
	value.DegradedRate = safeMetricRate(value.DegradedRuns, value.GeneratedRuns)
	value.RetryRate = safeMetricRate(value.RetriedRuns, value.GeneratedRuns)
	value.DegradedAlert = value.DegradedRate > NarrativeDegradedAlertMinimum
	metrics.values[key] = value
	labels := []string{string(input.DomainID), string(input.RunType)}
	metrics.failureRate.WithLabelValues(labels...).Set(value.VerificationFailureRate)
	metrics.degradedRate.WithLabelValues(labels...).Set(value.DegradedRate)
	metrics.retryRate.WithLabelValues(labels...).Set(value.RetryRate)
	for code, count := range value.FailureByCode {
		metrics.failureByCode.WithLabelValues(string(input.DomainID), string(code), string(input.RunType)).Set(float64(count))
	}
	return cloneNarrativeMetricSnapshot(value), nil
}

func (metrics *NarrativeMetrics) Snapshot(
	domainID askdata.ID, runType NarrativeRunType,
) (NarrativeMetricSnapshot, error) {
	if metrics == nil || domainID.Validate() != nil ||
		(runType != NarrativeRunAskData && runType != NarrativeRunReport) {
		return NarrativeMetricSnapshot{}, errors.New("narrative metric scope is invalid")
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	value := metrics.values[narrativeMetricKey{domainID: domainID, runType: runType}]
	if value.FailureByCode == nil {
		value.FailureByCode = map[VerifyCode]uint64{}
	}
	return cloneNarrativeMetricSnapshot(value), nil
}

func safeMetricRate(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	value := float64(numerator) / float64(denominator)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func cloneNarrativeMetricSnapshot(value NarrativeMetricSnapshot) NarrativeMetricSnapshot {
	result := value
	result.FailureByCode = make(map[VerifyCode]uint64, len(value.FailureByCode))
	codes := make([]string, 0, len(value.FailureByCode))
	for code := range value.FailureByCode {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)
	for _, code := range codes {
		result.FailureByCode[VerifyCode(code)] = value.FailureByCode[VerifyCode(code)]
	}
	return result
}
