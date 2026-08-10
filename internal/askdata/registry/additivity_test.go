package registry

import (
	"errors"
	"testing"
)

func TestValidateAdditivityReturnsStableFieldCodes(t *testing.T) {
	base := MetricVersion{VersionIdentity: validVersionIdentity(), Additivity: FullyAdditive, Unit: "COUNT"}
	tests := []struct {
		name string
		edit func(*MetricVersion)
		code string
	}{
		{name: "missing", edit: func(value *MetricVersion) { value.Additivity = "" }, code: AdditivityMissing},
		{name: "unit", edit: func(value *MetricVersion) { value.Unit = "" }, code: AdditivityUnitMissing},
		{name: "currency", edit: func(value *MetricVersion) { value.Unit, value.Currency = "CURRENCY", "" }, code: AdditivityCurrencyMissing},
		{name: "semi time aggregation", edit: func(value *MetricVersion) { value.Additivity = SemiAdditive }, code: SemiAdditiveTimeAggregationMissing},
		{name: "non additive restriction", edit: func(value *MetricVersion) { value.Additivity = NonAdditive }, code: NonAdditiveRestrictionInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.edit(&value)
			err := ValidateAdditivity(value)
			var issue *AdditivityError
			if !errors.As(err, &issue) || issue.Code != test.code || issue.ObjectVersionID != value.ID {
				t.Fatalf("error = %#v, want code=%s id=%s", err, test.code, value.ID)
			}
		})
	}
}

func TestCertificationIgnoresSuggestionAndAcceptsCompleteFact(t *testing.T) {
	metric := MetricVersion{
		VersionIdentity: validVersionIdentity(), MetricID: validationObject,
		SemanticModelVersionID: validationRow,
		FormulaAST:             []byte(`{"type":"MEASURE_REF","measureId":"m"}`),
		DefaultFiltersAST:      []byte(`{"type":"TRUE"}`), TimeGrain: "MONTH",
		NullPolicy: "PRESERVE", MeasureVersionIDs: []string{validationObject},
		AdditivitySuggestion: FullyAdditive, Unit: "COUNT",
	}
	if _, err := CertifyMetric(metric); !additivityErrorHasCode(err, AdditivityMissing) {
		t.Fatalf("suggestion bypassed certification: %v", err)
	}
	metric.Additivity = FullyAdditive
	certified, err := CertifyMetric(metric)
	if err != nil {
		t.Fatal(err)
	}
	if certified.Status != VersionStatusCertified || certified.ZeroDenominatorPolicy != ZeroDenominatorNull {
		t.Fatalf("certified metric = %#v", certified)
	}
}

func TestReleaseValidationRejectsEveryMissingMetric(t *testing.T) {
	first := releaseObject(ReleaseObjectMetric,
		"11111111-1111-4111-8111-111111111111", "21111111-1111-4111-8111-111111111111",
		`{"type":"METRIC","unit":"COUNT"}`)
	second := releaseObject(ReleaseObjectMetric,
		"12222222-2222-4222-8222-222222222222", "22222222-2222-4222-8222-222222222222",
		`{"type":"METRIC","unit":"COUNT","additivitySuggestion":"FULLY_ADDITIVE"}`)
	_, err := BuildReleaseManifest([]ReleaseObject{second, first})
	var failure *ReleaseAdditivityError
	if !errors.As(err, &failure) || len(failure.Issues) != 2 ||
		failure.Issues[0].ObjectVersionID != first.ObjectVersionID ||
		failure.Issues[1].ObjectVersionID != second.ObjectVersionID {
		t.Fatalf("release additivity failure = %#v", err)
	}
	for _, issue := range failure.Issues {
		if issue.Code != AdditivityMissing {
			t.Fatalf("release issue = %#v", issue)
		}
	}
}

func additivityErrorHasCode(err error, code string) bool {
	var issue *AdditivityError
	return errors.As(err, &issue) && issue.Code == code
}
