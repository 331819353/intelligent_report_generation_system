package evaluation

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidExperiment = errors.New("shadow/canary evaluation is invalid")

type ExperimentMode string

const (
	ExperimentShadow ExperimentMode = "SHADOW"
	ExperimentCanary ExperimentMode = "CANARY"
)

type ExperimentCohort string

const (
	CohortControl   ExperimentCohort = "CONTROL"
	CohortCandidate ExperimentCohort = "CANDIDATE"
	CohortShadow    ExperimentCohort = "SHADOW"
)

type ExperimentConfig struct {
	Mode               ExperimentMode
	TenantID           askdata.ID
	DomainID           askdata.ID
	ControlReleaseID   askdata.ID
	CandidateReleaseID askdata.ID
	CanaryPercent      int
	SaltHash           askdata.ContentHash
	Enabled            bool
}

type ExperimentSubject struct {
	TenantID askdata.ID
	DomainID askdata.ID
	ActorID  askdata.ID
	Role     string
}

type ExperimentRoute struct {
	UserFacingReleaseID askdata.ID
	EvaluationReleaseID askdata.ID
	Cohort              ExperimentCohort
	ExposeEvaluation    bool
}

func RouteExperiment(config ExperimentConfig, subject ExperimentSubject) (ExperimentRoute, error) {
	if err := validateExperimentConfig(config); err != nil || subject.TenantID != config.TenantID ||
		subject.DomainID != config.DomainID || subject.ActorID.Validate() != nil || !stableExperimentRole(subject.Role) {
		return ExperimentRoute{}, ErrInvalidExperiment
	}
	if !config.Enabled {
		return ExperimentRoute{UserFacingReleaseID: config.ControlReleaseID, EvaluationReleaseID: config.ControlReleaseID, Cohort: CohortControl}, nil
	}
	switch config.Mode {
	case ExperimentShadow:
		return ExperimentRoute{
			UserFacingReleaseID: config.ControlReleaseID, EvaluationReleaseID: config.CandidateReleaseID,
			Cohort: CohortShadow, ExposeEvaluation: false,
		}, nil
	case ExperimentCanary:
		bucket := experimentBucket(config, subject)
		if bucket < config.CanaryPercent {
			return ExperimentRoute{
				UserFacingReleaseID: config.CandidateReleaseID, EvaluationReleaseID: config.CandidateReleaseID,
				Cohort: CohortCandidate, ExposeEvaluation: true,
			}, nil
		}
		return ExperimentRoute{
			UserFacingReleaseID: config.ControlReleaseID, EvaluationReleaseID: config.ControlReleaseID,
			Cohort: CohortControl, ExposeEvaluation: true,
		}, nil
	default:
		return ExperimentRoute{}, ErrInvalidExperiment
	}
}

type ExperimentObservation struct {
	ReleaseID      askdata.ID
	DomainID       askdata.ID
	Role           string
	Cohort         ExperimentCohort
	Accurate       bool
	Clarification  bool
	SecurityPassed bool
	SensitiveLeak  bool
	Latency        time.Duration
	CostMicros     int64
}

type ExperimentSummary struct {
	ReleaseID         askdata.ID       `json:"releaseId"`
	DomainID          askdata.ID       `json:"domainId"`
	Role              string           `json:"role"`
	Cohort            ExperimentCohort `json:"cohort"`
	Count             int              `json:"count"`
	Accuracy          float64          `json:"accuracy"`
	ClarificationRate float64          `json:"clarificationRate"`
	SecurityRate      float64          `json:"securityRate"`
	SensitiveLeaks    int              `json:"sensitiveLeaks"`
	LatencyP95        time.Duration    `json:"latencyP95"`
	AverageCostMicros float64          `json:"averageCostMicros"`
}

func SummarizeExperiments(observations []ExperimentObservation) ([]ExperimentSummary, error) {
	if len(observations) < 1 || len(observations) > 1_000_000 {
		return nil, ErrInvalidExperiment
	}
	type key struct {
		release askdata.ID
		domain  askdata.ID
		role    string
		cohort  ExperimentCohort
	}
	groups := map[key][]ExperimentObservation{}
	for _, observation := range observations {
		if err := validateExperimentObservation(observation); err != nil {
			return nil, err
		}
		group := key{observation.ReleaseID, observation.DomainID, observation.Role, observation.Cohort}
		groups[group] = append(groups[group], observation)
	}
	result := make([]ExperimentSummary, 0, len(groups))
	for group, values := range groups {
		accurate, clarification, security, leaks := 0, 0, 0, 0
		cost := int64(0)
		latencies := make([]time.Duration, len(values))
		for index, value := range values {
			if value.Accurate {
				accurate++
			}
			if value.Clarification {
				clarification++
			}
			if value.SecurityPassed {
				security++
			}
			if value.SensitiveLeak {
				leaks++
			}
			cost += value.CostMicros
			latencies[index] = value.Latency
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		result = append(result, ExperimentSummary{
			ReleaseID: group.release, DomainID: group.domain, Role: group.role, Cohort: group.cohort,
			Count: len(values), Accuracy: rate(accurate, len(values)),
			ClarificationRate: rate(clarification, len(values)), SecurityRate: rate(security, len(values)),
			SensitiveLeaks: leaks, LatencyP95: nearestRankDuration(latencies, .95),
			AverageCostMicros: float64(cost) / float64(len(values)),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.DomainID != right.DomainID {
			return left.DomainID < right.DomainID
		}
		if left.ReleaseID != right.ReleaseID {
			return left.ReleaseID < right.ReleaseID
		}
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		return left.Cohort < right.Cohort
	})
	return result, nil
}

type AutomaticStopThresholds struct {
	MinimumSamples               int
	MaximumAccuracyDrop          float64
	MaximumClarificationIncrease float64
	MaximumLatencyP95Ratio       float64
	MaximumCostIncreaseRatio     float64
}

type AutomaticStopDecision struct {
	Stop  bool
	Codes []string
}

// EvaluateAutomaticStop can only stop an experiment. It intentionally has no
// transition that increases CanaryPercent.
func EvaluateAutomaticStop(control, candidate ExperimentSummary, thresholds AutomaticStopThresholds) (AutomaticStopDecision, error) {
	if control.DomainID != candidate.DomainID || control.Role != candidate.Role || control.Cohort != CohortControl ||
		candidate.Cohort != CohortCandidate || thresholds.MinimumSamples < 1 ||
		thresholds.MaximumAccuracyDrop < 0 || thresholds.MaximumAccuracyDrop > 1 ||
		thresholds.MaximumClarificationIncrease < 0 || thresholds.MaximumClarificationIncrease > 1 ||
		thresholds.MaximumLatencyP95Ratio < 1 || thresholds.MaximumCostIncreaseRatio < 1 {
		return AutomaticStopDecision{}, ErrInvalidExperiment
	}
	decision := AutomaticStopDecision{Codes: []string{}}
	if candidate.SensitiveLeaks > 0 || candidate.SecurityRate < 1 {
		decision.Codes = append(decision.Codes, "CANARY_SECURITY_REGRESSION")
	}
	if candidate.Count >= thresholds.MinimumSamples && control.Count >= thresholds.MinimumSamples {
		if control.Accuracy-candidate.Accuracy > thresholds.MaximumAccuracyDrop {
			decision.Codes = append(decision.Codes, "CANARY_ACCURACY_REGRESSION")
		}
		if candidate.ClarificationRate-control.ClarificationRate > thresholds.MaximumClarificationIncrease {
			decision.Codes = append(decision.Codes, "CANARY_CLARIFICATION_REGRESSION")
		}
		if control.LatencyP95 > 0 && float64(candidate.LatencyP95)/float64(control.LatencyP95) > thresholds.MaximumLatencyP95Ratio {
			decision.Codes = append(decision.Codes, "CANARY_LATENCY_REGRESSION")
		}
		if control.AverageCostMicros > 0 && candidate.AverageCostMicros/control.AverageCostMicros > thresholds.MaximumCostIncreaseRatio {
			decision.Codes = append(decision.Codes, "CANARY_COST_REGRESSION")
		}
	}
	sort.Strings(decision.Codes)
	decision.Stop = len(decision.Codes) != 0
	return decision, nil
}

type ExperimentRecorder struct {
	runs             *prometheus.CounterVec
	accurate         *prometheus.CounterVec
	clarifications   *prometheus.CounterVec
	securityFailures *prometheus.CounterVec
	leaks            *prometheus.CounterVec
	latency          *prometheus.HistogramVec
	cost             *prometheus.HistogramVec
}

func NewExperimentRecorder(registerer prometheus.Registerer) (*ExperimentRecorder, error) {
	if registerer == nil {
		return nil, ErrInvalidExperiment
	}
	labels := []string{"release_id", "domain_id", "role", "cohort"}
	recorder := &ExperimentRecorder{
		runs:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "askdata_experiment_runs_total", Help: "Shadow/canary observations."}, labels),
		accurate:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "askdata_experiment_accurate_total", Help: "Strictly accurate shadow/canary observations."}, labels),
		clarifications:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "askdata_experiment_clarifications_total", Help: "Shadow/canary clarifications."}, labels),
		securityFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "askdata_experiment_security_failures_total", Help: "Shadow/canary security failures."}, labels),
		leaks:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "askdata_experiment_sensitive_leaks_total", Help: "Shadow/canary sensitive leaks."}, labels),
		latency:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "askdata_experiment_latency_seconds", Help: "Shadow/canary latency.", Buckets: prometheus.DefBuckets}, labels),
		cost:             prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "askdata_experiment_cost_micros", Help: "Shadow/canary cost in micros.", Buckets: prometheus.ExponentialBuckets(100, 2, 16)}, labels),
	}
	for _, collector := range []prometheus.Collector{recorder.runs, recorder.accurate, recorder.clarifications, recorder.securityFailures, recorder.leaks, recorder.latency, recorder.cost} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return recorder, nil
}

func (recorder *ExperimentRecorder) Observe(observation ExperimentObservation) error {
	if recorder == nil || validateExperimentObservation(observation) != nil {
		return ErrInvalidExperiment
	}
	labels := []string{string(observation.ReleaseID), string(observation.DomainID), observation.Role, string(observation.Cohort)}
	recorder.runs.WithLabelValues(labels...).Inc()
	if observation.Accurate {
		recorder.accurate.WithLabelValues(labels...).Inc()
	}
	if observation.Clarification {
		recorder.clarifications.WithLabelValues(labels...).Inc()
	}
	if !observation.SecurityPassed {
		recorder.securityFailures.WithLabelValues(labels...).Inc()
	}
	if observation.SensitiveLeak {
		recorder.leaks.WithLabelValues(labels...).Inc()
	}
	recorder.latency.WithLabelValues(labels...).Observe(observation.Latency.Seconds())
	recorder.cost.WithLabelValues(labels...).Observe(float64(observation.CostMicros))
	return nil
}

func validateExperimentConfig(config ExperimentConfig) error {
	if config.TenantID.Validate() != nil || config.DomainID.Validate() != nil || config.ControlReleaseID.Validate() != nil ||
		config.CandidateReleaseID.Validate() != nil || config.ControlReleaseID == config.CandidateReleaseID || config.SaltHash.Validate() != nil ||
		(config.Mode != ExperimentShadow && config.Mode != ExperimentCanary) {
		return ErrInvalidExperiment
	}
	if config.Mode == ExperimentCanary && config.CanaryPercent != 5 && config.CanaryPercent != 20 && config.CanaryPercent != 50 {
		return ErrInvalidExperiment
	}
	if config.Mode == ExperimentShadow && config.CanaryPercent != 0 {
		return ErrInvalidExperiment
	}
	return nil
}

func validateExperimentObservation(observation ExperimentObservation) error {
	if observation.ReleaseID.Validate() != nil || observation.DomainID.Validate() != nil || !stableExperimentRole(observation.Role) ||
		(observation.Cohort != CohortControl && observation.Cohort != CohortCandidate && observation.Cohort != CohortShadow) ||
		observation.Latency < 0 || observation.Latency > 10*time.Minute || observation.CostMicros < 0 ||
		observation.SensitiveLeak && observation.SecurityPassed {
		return ErrInvalidExperiment
	}
	return nil
}

func experimentBucket(config ExperimentConfig, subject ExperimentSubject) int {
	payload := strings.Join([]string{string(config.SaltHash), string(subject.TenantID), string(subject.DomainID), string(subject.ActorID), subject.Role, string(config.CandidateReleaseID)}, "|")
	hash := sha256.Sum256([]byte(payload))
	return int(binary.BigEndian.Uint64(hash[:8]) % 100)
}

func stableExperimentRole(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func nearestRankDuration(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 || quantile <= 0 || quantile > 1 || math.IsNaN(quantile) {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(values)))) - 1
	return values[index]
}
