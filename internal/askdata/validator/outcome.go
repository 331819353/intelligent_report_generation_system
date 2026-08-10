package validator

import (
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var ErrInvalidOutcome = errors.New("query outcome is invalid")

const (
	OutcomeSchemaVersion = "query-outcome-v1"
	maxOutcomeMetrics    = 16
	maxOutcomePlans      = 6
	maxOutcomeSources    = 16
	maxOutcomeMembers    = 1_000_000
	maxQualityChecks     = 64
)

// OutcomeStatus is the single routing status. Quality warnings remain
// orthogonal: a PARTIAL outcome can also carry QualityWarnings evidence.
type OutcomeStatus string

const (
	OutcomeAnswered       OutcomeStatus = "ANSWERED"
	OutcomePartial        OutcomeStatus = "PARTIAL"
	OutcomeQualityWarning OutcomeStatus = "QUALITY_WARNING"
)

// MetricAuthorizationEvidence deliberately contains counts only. Metric
// names and IDs that the actor cannot access must never cross this boundary.
type MetricAuthorizationEvidence struct {
	RequestedCount  int `json:"requestedCount"`
	AuthorizedCount int `json:"authorizedCount"`
}

type FailedPlanEvidence struct {
	PlanID      askdata.ID `json:"planId"`
	FailureCode string     `json:"failureCode"`
}

type BundleOutcomeEvidence struct {
	TotalPlans  int                  `json:"totalPlans"`
	FailedPlans []FailedPlanEvidence `json:"failedPlans"`
}

type RowLimitEvidence struct {
	Limit        int  `json:"limit"`
	ReturnedRows int  `json:"returnedRows"`
	Truncated    bool `json:"truncated"`
}

type MemberPolicyEvidence struct {
	EvaluatedCount int `json:"evaluatedCount"`
	FilteredCount  int `json:"filteredCount"`
}

type MultiSourceOutcomeEvidence struct {
	TotalSources int          `json:"totalSources"`
	TimedOut     []askdata.ID `json:"timedOut"`
}

type QualityWarningEvidence struct {
	Code     string              `json:"code"`
	Evidence askdata.EvidenceRef `json:"evidence"`
}

// OutcomeContext contains only facts produced by trusted validators,
// authorization filters and execution adapters. A nil optional fact means
// that the corresponding condition did not participate in this result.
type OutcomeContext struct {
	Coverage            *CoverageVerdict             `json:"coverage,omitempty"`
	MetricAuthorization *MetricAuthorizationEvidence `json:"metricAuthorization,omitempty"`
	Bundle              *BundleOutcomeEvidence       `json:"bundle,omitempty"`
	RowLimit            *RowLimitEvidence            `json:"rowLimit,omitempty"`
	MemberPolicy        *MemberPolicyEvidence        `json:"memberPolicy,omitempty"`
	MultiSource         *MultiSourceOutcomeEvidence  `json:"multiSource,omitempty"`
	Quality             *QualityEvidence             `json:"quality,omitempty"`
}

// OutcomeEvidence is the safe, browser-facing explanation for P1-P6 and Q1.
// Empty collections are emitted as [] so the same facts always hash to the
// same replay representation.
type OutcomeEvidence struct {
	TimeRangeTruncated          bool                     `json:"timeRangeTruncated"`
	MetricsFilteredByPermission int                      `json:"metricsFilteredByPermission"`
	FailedPlans                 []FailedPlanEvidence     `json:"failedPlans"`
	RowLimitApplied             bool                     `json:"rowLimitApplied"`
	MembersFilteredByPolicy     int                      `json:"membersFilteredByPolicy"`
	SourcesTimedOut             []askdata.ID             `json:"sourcesTimedOut"`
	QualityWarnings             []QualityWarningEvidence `json:"qualityWarnings"`
}

type Outcome struct {
	SchemaVersion string              `json:"schemaVersion"`
	Status        OutcomeStatus       `json:"status"`
	Evidence      OutcomeEvidence     `json:"evidence"`
	OutcomeHash   askdata.ContentHash `json:"outcomeHash"`
}

// DetermineOutcome evaluates P1-P6, then Q1, accumulating every applicable
// marker. Invalid or contradictory input fails closed as an invalid zero
// Outcome, consistent with EvaluateCoverage's sealed verdict boundary.
func DetermineOutcome(ctx OutcomeContext) Outcome {
	if validateOutcomeContext(ctx) != nil {
		return Outcome{}
	}
	outcome := Outcome{
		SchemaVersion: OutcomeSchemaVersion,
		Status:        OutcomeAnswered,
		Evidence: OutcomeEvidence{
			FailedPlans:     []FailedPlanEvidence{},
			SourcesTimedOut: []askdata.ID{},
			QualityWarnings: []QualityWarningEvidence{},
		},
	}

	// P1: trusted time-coverage truncation.
	if ctx.Coverage != nil && ctx.Coverage.Relation == CoverageTruncated {
		outcome.Evidence.TimeRangeTruncated = true
	}
	// P2: a non-empty authorized subset of a multi-metric request.
	if ctx.MetricAuthorization != nil {
		outcome.Evidence.MetricsFilteredByPermission =
			ctx.MetricAuthorization.RequestedCount - ctx.MetricAuthorization.AuthorizedCount
	}
	// P3: a non-empty failed subset of a bundle.
	if ctx.Bundle != nil {
		outcome.Evidence.FailedPlans = append(
			outcome.Evidence.FailedPlans, ctx.Bundle.FailedPlans...,
		)
		sort.Slice(outcome.Evidence.FailedPlans, func(left, right int) bool {
			return outcome.Evidence.FailedPlans[left].PlanID < outcome.Evidence.FailedPlans[right].PlanID
		})
	}
	// P4: execution explicitly proved row-limit truncation.
	if ctx.RowLimit != nil {
		outcome.Evidence.RowLimitApplied = ctx.RowLimit.Truncated
	}
	// P5: a non-empty allowed subset after row-policy filtering.
	if ctx.MemberPolicy != nil {
		outcome.Evidence.MembersFilteredByPolicy = ctx.MemberPolicy.FilteredCount
	}
	// P6: a non-empty responsive subset of a multi-source request.
	if ctx.MultiSource != nil {
		outcome.Evidence.SourcesTimedOut = append(
			outcome.Evidence.SourcesTimedOut, ctx.MultiSource.TimedOut...,
		)
		sort.Slice(outcome.Evidence.SourcesTimedOut, func(left, right int) bool {
			return outcome.Evidence.SourcesTimedOut[left] < outcome.Evidence.SourcesTimedOut[right]
		})
	}

	partial := outcome.Evidence.TimeRangeTruncated ||
		outcome.Evidence.MetricsFilteredByPermission > 0 ||
		len(outcome.Evidence.FailedPlans) > 0 ||
		outcome.Evidence.RowLimitApplied ||
		outcome.Evidence.MembersFilteredByPolicy > 0 ||
		len(outcome.Evidence.SourcesTimedOut) > 0

	// Q1: only failed, non-blocking WARNING rules are projected. The full
	// evidence references remain safe, governed pointers rather than messages.
	if ctx.Quality != nil && ctx.Quality.Status == QualityWarning {
		for _, check := range ctx.Quality.Checks {
			if !check.Passed && check.Severity == RuleWarning {
				outcome.Evidence.QualityWarnings = append(outcome.Evidence.QualityWarnings,
					QualityWarningEvidence{Code: check.Code, Evidence: check.Evidence})
			}
		}
		sort.Slice(outcome.Evidence.QualityWarnings, func(left, right int) bool {
			return outcome.Evidence.QualityWarnings[left].Code < outcome.Evidence.QualityWarnings[right].Code
		})
	}

	switch {
	case partial:
		outcome.Status = OutcomePartial
	case len(outcome.Evidence.QualityWarnings) > 0:
		outcome.Status = OutcomeQualityWarning
	}
	outcome.OutcomeHash = outcomeHash(outcome)
	if outcome.Validate() != nil {
		return Outcome{}
	}
	return outcome
}

func (outcome Outcome) Validate() error {
	if outcome.SchemaVersion != OutcomeSchemaVersion || outcome.OutcomeHash.Validate() != nil ||
		(outcome.Status != OutcomeAnswered && outcome.Status != OutcomePartial &&
			outcome.Status != OutcomeQualityWarning) ||
		outcome.Evidence.MetricsFilteredByPermission < 0 ||
		outcome.Evidence.MetricsFilteredByPermission >= maxOutcomeMetrics ||
		outcome.Evidence.MembersFilteredByPolicy < 0 ||
		outcome.Evidence.MembersFilteredByPolicy >= maxOutcomeMembers ||
		outcome.Evidence.FailedPlans == nil || outcome.Evidence.SourcesTimedOut == nil ||
		outcome.Evidence.QualityWarnings == nil ||
		len(outcome.Evidence.FailedPlans) >= maxOutcomePlans ||
		len(outcome.Evidence.SourcesTimedOut) >= maxOutcomeSources ||
		len(outcome.Evidence.QualityWarnings) > maxQualityChecks {
		return ErrInvalidOutcome
	}
	for index, failed := range outcome.Evidence.FailedPlans {
		if failed.PlanID.Validate() != nil || !stableRuleCode(failed.FailureCode) ||
			(index > 0 && outcome.Evidence.FailedPlans[index-1].PlanID >= failed.PlanID) {
			return ErrInvalidOutcome
		}
	}
	for index, sourceID := range outcome.Evidence.SourcesTimedOut {
		if sourceID.Validate() != nil ||
			(index > 0 && outcome.Evidence.SourcesTimedOut[index-1] >= sourceID) {
			return ErrInvalidOutcome
		}
	}
	for index, warning := range outcome.Evidence.QualityWarnings {
		if !stableRuleCode(warning.Code) || warning.Evidence.Validate() != nil ||
			(index > 0 && outcome.Evidence.QualityWarnings[index-1].Code >= warning.Code) {
			return ErrInvalidOutcome
		}
	}
	partial := outcome.Evidence.TimeRangeTruncated ||
		outcome.Evidence.MetricsFilteredByPermission > 0 ||
		len(outcome.Evidence.FailedPlans) > 0 ||
		outcome.Evidence.RowLimitApplied ||
		outcome.Evidence.MembersFilteredByPolicy > 0 ||
		len(outcome.Evidence.SourcesTimedOut) > 0
	qualityWarning := len(outcome.Evidence.QualityWarnings) > 0
	if (outcome.Status == OutcomePartial) != partial ||
		(outcome.Status == OutcomeQualityWarning) != (!partial && qualityWarning) ||
		(outcome.Status == OutcomeAnswered) != (!partial && !qualityWarning) ||
		outcomeHash(outcome) != outcome.OutcomeHash {
		return ErrInvalidOutcome
	}
	return nil
}

func validateOutcomeContext(ctx OutcomeContext) error {
	if ctx.Coverage != nil && (ctx.Coverage.Validate() != nil || ctx.Coverage.Relation == CoverageNone) {
		return ErrInvalidOutcome
	}
	if value := ctx.MetricAuthorization; value != nil {
		if value.RequestedCount < 2 || value.RequestedCount > maxOutcomeMetrics ||
			value.AuthorizedCount < 1 || value.AuthorizedCount >= value.RequestedCount {
			return ErrInvalidOutcome
		}
	}
	if value := ctx.Bundle; value != nil {
		if value.TotalPlans < 2 || value.TotalPlans > maxOutcomePlans ||
			len(value.FailedPlans) < 1 || len(value.FailedPlans) >= value.TotalPlans {
			return ErrInvalidOutcome
		}
		seen := make(map[askdata.ID]bool, len(value.FailedPlans))
		for _, failed := range value.FailedPlans {
			if failed.PlanID.Validate() != nil || !stableRuleCode(failed.FailureCode) || seen[failed.PlanID] {
				return ErrInvalidOutcome
			}
			seen[failed.PlanID] = true
		}
	}
	if value := ctx.RowLimit; value != nil {
		if value.Limit < 1 || value.Limit > 20_000 || value.ReturnedRows < 0 ||
			value.ReturnedRows > value.Limit || !value.Truncated || value.ReturnedRows != value.Limit {
			return ErrInvalidOutcome
		}
	}
	if value := ctx.MemberPolicy; value != nil {
		if value.EvaluatedCount < 2 || value.EvaluatedCount > maxOutcomeMembers ||
			value.FilteredCount < 1 || value.FilteredCount >= value.EvaluatedCount {
			return ErrInvalidOutcome
		}
	}
	if value := ctx.MultiSource; value != nil {
		if value.TotalSources < 2 || value.TotalSources > maxOutcomeSources ||
			len(value.TimedOut) < 1 || len(value.TimedOut) >= value.TotalSources {
			return ErrInvalidOutcome
		}
		seen := make(map[askdata.ID]bool, len(value.TimedOut))
		for _, sourceID := range value.TimedOut {
			if sourceID.Validate() != nil || seen[sourceID] {
				return ErrInvalidOutcome
			}
			seen[sourceID] = true
		}
	}
	return validateOutcomeQuality(ctx.Quality)
}

func validateOutcomeQuality(quality *QualityEvidence) error {
	if quality == nil {
		return nil
	}
	if (quality.Status != QualityPass && quality.Status != QualityWarning) ||
		quality.Evidence.Validate() != nil || len(quality.Checks) > maxQualityChecks {
		return ErrInvalidOutcome
	}
	seen := make(map[string]bool, len(quality.Checks))
	warnings := 0
	for _, check := range quality.Checks {
		if !stableRuleCode(check.Code) || !ruleSeverityValid(check.Severity) ||
			check.Evidence.Validate() != nil || seen[check.Code] ||
			(!check.Passed && check.Severity == RuleBlocking) {
			return ErrInvalidOutcome
		}
		seen[check.Code] = true
		if !check.Passed && check.Severity == RuleWarning {
			warnings++
		}
	}
	if (quality.Status == QualityWarning) != (warnings > 0) {
		return ErrInvalidOutcome
	}
	return nil
}

func outcomeHash(outcome Outcome) askdata.ContentHash {
	copy := outcome
	copy.OutcomeHash = ""
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return ""
	}
	return askdata.HashBytes(payload)
}
