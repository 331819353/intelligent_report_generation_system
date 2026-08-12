package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	AdditivityMissing                  = "ADDITIVITY_MISSING"
	AdditivityInvalid                  = "ADDITIVITY_INVALID"
	AdditivityUnitMissing              = "ADDITIVITY_UNIT_MISSING"
	AdditivityCurrencyMissing          = "ADDITIVITY_CURRENCY_MISSING"
	SemiAdditiveTimeAggregationMissing = "SEMI_ADDITIVE_TIME_AGG_MISSING"
	NonAdditiveRestrictionInvalid      = "NON_ADDITIVE_RESTRICTION_INVALID"
)

var ErrAdditivityInvalid = errors.New("additivity contract is invalid")

type AdditivityError struct {
	Code            string `json:"code"`
	ObjectVersionID string `json:"objectVersionId"`
}

func (issue *AdditivityError) Error() string {
	return fmt.Sprintf("%s: %s", issue.Code, issue.ObjectVersionID)
}

func (issue *AdditivityError) Unwrap() error { return ErrAdditivityInvalid }

type additivityContract struct {
	id                          string
	additivity                  Additivity
	semiAdditiveTimeAggregation SemiAdditiveTimeAggregation
	aggregationRestriction      AggregationRestriction
	unit                        string
	currency                    string
}

// ValidateAdditivity is the metric certification gate. It deliberately reads
// only the confirmed fact fields; AdditivitySuggestion is never consulted.
func ValidateAdditivity(metric MetricVersion) error {
	return validateAdditivityContract(additivityContract{
		id: metric.ID, additivity: metric.Additivity,
		semiAdditiveTimeAggregation: metric.SemiAdditiveTimeAggregation,
		aggregationRestriction:      metric.AggregationRestriction,
		unit:                        metric.Unit, currency: metric.Currency,
	})
}

func ValidateMeasureAdditivity(measure Measure) error {
	return validateAdditivityContract(additivityContract{
		id: measure.ID, additivity: measure.Additivity,
		semiAdditiveTimeAggregation: measure.SemiAdditiveTimeAggregation,
		aggregationRestriction:      measure.AggregationRestriction,
		unit:                        measure.Unit, currency: measure.Currency,
	})
}

func validateAdditivityContract(contract additivityContract) error {
	if contract.additivity == "" {
		return &AdditivityError{Code: AdditivityMissing, ObjectVersionID: contract.id}
	}
	if !validAdditivity(contract.additivity) {
		return &AdditivityError{Code: AdditivityInvalid, ObjectVersionID: contract.id}
	}
	if strings.TrimSpace(contract.unit) == "" {
		return &AdditivityError{Code: AdditivityUnitMissing, ObjectVersionID: contract.id}
	}
	switch contract.additivity {
	case SemiAdditive:
		if contract.semiAdditiveTimeAggregation == "" {
			return &AdditivityError{Code: SemiAdditiveTimeAggregationMissing, ObjectVersionID: contract.id}
		}
	case NonAdditive:
		if contract.aggregationRestriction != PostAggregate {
			return &AdditivityError{Code: NonAdditiveRestrictionInvalid, ObjectVersionID: contract.id}
		}
	}
	if strings.EqualFold(strings.TrimSpace(contract.unit), "CURRENCY") && strings.TrimSpace(contract.currency) == "" {
		return &AdditivityError{Code: AdditivityCurrencyMissing, ObjectVersionID: contract.id}
	}
	return nil
}

// CertifyMetric is the application-level state-transition gate. Repository or
// HTTP adapters must call it before persisting CERTIFIED; the database repeats
// the same requirements for direct SQL paths.
func CertifyMetric(metric MetricVersion) (MetricVersion, error) {
	if metric.Status != VersionStatusDraft {
		return MetricVersion{}, errors.New("only DRAFT metric versions can be certified")
	}
	applyAdditivityDefaultsToMetric(&metric)
	if err := metric.Validate(); err != nil {
		return MetricVersion{}, err
	}
	if err := ValidateAdditivity(metric); err != nil {
		return MetricVersion{}, err
	}
	metric.Status = VersionStatusCertified
	return metric, nil
}

func CertifyMeasure(measure Measure) (Measure, error) {
	if measure.Status != VersionStatusDraft {
		return Measure{}, errors.New("only DRAFT measures can be certified")
	}
	applyAdditivityDefaultsToMeasure(&measure)
	if err := measure.Validate(); err != nil {
		return Measure{}, err
	}
	if err := ValidateMeasureAdditivity(measure); err != nil {
		return Measure{}, err
	}
	measure.Status = VersionStatusCertified
	return measure, nil
}

func applyAdditivityDefaultsToMetric(metric *MetricVersion) {
	if metric.ZeroDenominatorPolicy == "" {
		metric.ZeroDenominatorPolicy = ZeroDenominatorNull
	}
}

func applyAdditivityDefaultsToMeasure(measure *Measure) {
	if measure.ZeroDenominatorPolicy == "" {
		measure.ZeroDenominatorPolicy = ZeroDenominatorNull
	}
}

func confirmMetricAdditivity(current *MetricVersion, actorID string, previous *MetricVersion) {
	if current.Additivity == "" {
		current.AdditivityConfirmedBy, current.AdditivityConfirmedAt = "", nil
		return
	}
	// Once an owner submits the authoritative additivity fact, the advisory
	// suggestion is no longer part of the draft contract. Keeping both would
	// also violate the audited suggestion/rule pairing enforced by PostgreSQL.
	current.AdditivitySuggestion = ""
	changed := previous == nil || current.Additivity != previous.Additivity ||
		current.SemiAdditiveTimeAggregation != previous.SemiAdditiveTimeAggregation ||
		current.AggregationRestriction != previous.AggregationRestriction ||
		!slices.Equal(current.NonAdditiveDimensions, previous.NonAdditiveDimensions) ||
		current.Unit != previous.Unit || current.Currency != previous.Currency ||
		current.ZeroDenominatorPolicy != previous.ZeroDenominatorPolicy ||
		current.DisplayPrecision != previous.DisplayPrecision
	if !changed {
		current.AdditivityConfirmedBy = previous.AdditivityConfirmedBy
		current.AdditivityConfirmedAt = previous.AdditivityConfirmedAt
		return
	}
	now := time.Now().UTC()
	current.AdditivityConfirmedBy, current.AdditivityConfirmedAt = actorID, &now
}

func confirmMeasureAdditivity(current *Measure, actorID string, previous *Measure) {
	if current.Additivity == "" {
		current.AdditivityConfirmedBy, current.AdditivityConfirmedAt = "", nil
		return
	}
	current.AdditivitySuggestion = ""
	changed := previous == nil || current.Additivity != previous.Additivity ||
		current.SemiAdditiveTimeAggregation != previous.SemiAdditiveTimeAggregation ||
		current.AggregationRestriction != previous.AggregationRestriction ||
		!slices.Equal(current.NonAdditiveDimensions, previous.NonAdditiveDimensions) ||
		current.Unit != previous.Unit || current.Currency != previous.Currency ||
		current.ZeroDenominatorPolicy != previous.ZeroDenominatorPolicy ||
		current.DisplayPrecision != previous.DisplayPrecision
	if !changed {
		current.AdditivityConfirmedBy = previous.AdditivityConfirmedBy
		current.AdditivityConfirmedAt = previous.AdditivityConfirmedAt
		return
	}
	now := time.Now().UTC()
	current.AdditivityConfirmedBy, current.AdditivityConfirmedAt = actorID, &now
}
