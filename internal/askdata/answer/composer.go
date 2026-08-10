package answer

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

var (
	ErrInvalidComposition = errors.New("answer composition input is invalid")
	ErrCompositionFailed  = errors.New("answer composition failed")
)

// Ask Data never enables advisory interpretation by default. Report insight
// generation may opt in explicitly, but the same verifier still evaluates the
// complete generated text before it can enter an artifact.
const DefaultAskDataInterpretationEnabled = false

// ComposeRequest is the complete and bounded model input for one narrative
// attempt. RetryFailures is structured verifier output; rejected prose is not
// echoed back through this contract.
type ComposeRequest struct {
	Attempt               int
	Structured            StructuredLayer
	Provenance            Provenance
	RetryFailures         []VerifyFailure
	InterpretationEnabled bool
}

// NarrativeComposer is implemented by the provider-neutral cognition layer.
// It returns prose and citations only; it cannot alter L1 values or provenance.
type NarrativeComposer interface {
	Compose(context.Context, ComposeRequest) (NarrativeLayer, error)
}

type CompositionInput struct {
	Artifact              AnswerArtifact
	Result                ResultEvidence
	Binding               BindingEvidence
	TimeSpec              compiler.ResolvedTimeSpec
	InterpretationEnabled bool
	LLMCallsRemaining     int
}

type CompositionResult struct {
	Artifact       AnswerArtifact
	Reports        []VerifyReport
	FailureSamples []NarrativeFailureSample
	Retried        bool
	Degraded       bool
	Hint           string
}

// NarrativeFailureSample is the only rejected-text persistence contract. It
// deliberately has no prose field: a full-narrative SHA-256, stable failure
// code/span and released semantic IDs are enough for clustering and audit.
type NarrativeFailureSample struct {
	Attempt             int                 `json:"attempt"`
	FailureCode         VerifyCode          `json:"failureCode"`
	FailureSpan         shared.TextSpan     `json:"failureSpan"`
	RejectedTextHash    askdata.ContentHash `json:"rejectedTextHash"`
	MetricVersionIDs    []askdata.ID        `json:"metricVersionIds"`
	DimensionVersionIDs []askdata.ID        `json:"dimensionVersionIds"`
}

// ComposeAndVerify implements the two-attempt L2/L3 trust boundary. L3 is off
// unless explicitly enabled in the request. A failed candidate is never
// returned: after the final failure (or budget exhaustion) only L1 survives.
func ComposeAndVerify(
	ctx context.Context,
	composer NarrativeComposer,
	verifier *Verifier,
	input CompositionInput,
) (CompositionResult, error) {
	result := CompositionResult{Reports: []VerifyReport{}, FailureSamples: []NarrativeFailureSample{}}
	if ctx == nil || composer == nil || verifier == nil || input.LLMCallsRemaining < 0 ||
		input.LLMCallsRemaining > 2 || input.Artifact.SchemaVersion != SchemaVersion ||
		input.Artifact.Verification.VerifierVersion == "" ||
		input.Artifact.Verification.PolicyWordlistVersion == "" {
		return result, ErrInvalidComposition
	}
	base := input.Artifact.Normalize()
	// Validate the immutable L1/provenance boundary before invoking a model.
	probe, err := ToStructured(base, 0)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidComposition, err)
	}
	base = probe

	maxAttempts := input.LLMCallsRemaining
	var failures []VerifyFailure
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		narrative, err := composer.Compose(ctx, ComposeRequest{
			Attempt: attempt, Structured: base.Layers.Structured,
			Provenance:            base.Provenance,
			RetryFailures:         retryFailureHints(failures),
			InterpretationEnabled: input.InterpretationEnabled,
		})
		if err != nil {
			return result, fmt.Errorf("%w: %v", ErrCompositionFailed, err)
		}
		candidate := base
		candidate.Layers.Narrative = narrative
		candidate.Verification.Attempts = attempt
		candidate.Verification.Passed = true
		candidate.Verification.Degraded = false
		candidate = candidate.Normalize()
		if err := candidate.Validate(); err != nil {
			failures = []VerifyFailure{{Reason: AnswerExternalFact, Expected: []string{"schema-valid narrative and citations"}}}
			report := VerifyReport{
				Passed:                false,
				VerifierVersion:       candidate.Verification.VerifierVersion,
				PolicyWordlistVersion: candidate.Verification.PolicyWordlistVersion,
				Failures:              cloneFailures(failures),
			}
			result.Reports = append(result.Reports, report)
			result.FailureSamples = append(result.FailureSamples,
				failureSamplesFor(report, narrative, input.Binding, attempt)...)
			continue
		}
		report := verifier.Verify(candidate, input.Result, input.Binding, input.TimeSpec)
		result.Reports = append(result.Reports, report)
		if report.Passed {
			result.Artifact = candidate
			result.Retried = attempt > 1
			return result, nil
		}
		result.FailureSamples = append(result.FailureSamples,
			failureSamplesFor(report, narrative, input.Binding, attempt)...)
		failures = cloneFailures(report.Failures)
	}

	fallback, err := ToStructured(base, maxAttempts)
	if err != nil {
		return result, err
	}
	result.Artifact = fallback
	result.Retried = maxAttempts > 1
	result.Degraded = true
	result.Hint = DegradedNarrativeHint
	return result, nil
}

func failureSamplesFor(
	report VerifyReport, narrative NarrativeLayer, binding BindingEvidence, attempt int,
) []NarrativeFailureSample {
	if report.Passed || len(report.Failures) == 0 {
		return nil
	}
	metrics := []askdata.ID{}
	dimensions := []askdata.ID{}
	for _, object := range binding.Normalize().Objects {
		if !object.Bound {
			continue
		}
		switch object.Kind {
		case ObjectMetric:
			metrics = append(metrics, object.ObjectID)
		case ObjectDimension:
			dimensions = append(dimensions, object.ObjectID)
		}
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i] < metrics[j] })
	sort.Slice(dimensions, func(i, j int) bool { return dimensions[i] < dimensions[j] })
	textHash := askdata.HashBytes([]byte(narrative.CanonicalText()))
	result := make([]NarrativeFailureSample, 0, len(report.Failures))
	for _, failure := range report.Failures {
		result = append(result, NarrativeFailureSample{
			Attempt: attempt, FailureCode: failure.Reason, FailureSpan: failure.Span,
			RejectedTextHash:    textHash,
			MetricVersionIDs:    append([]askdata.ID(nil), metrics...),
			DimensionVersionIDs: append([]askdata.ID(nil), dimensions...),
		})
	}
	return result
}

func cloneFailures(values []VerifyFailure) []VerifyFailure {
	result := make([]VerifyFailure, len(values))
	for index := range values {
		result[index] = values[index]
		result[index].Expected = append([]string(nil), values[index].Expected...)
	}
	return result
}

// retryFailureHints deliberately omit the rejected prose and its now-useless
// source span. The retry receives only governed error codes and expected
// evidence, so unverified text cannot be persisted or echoed as model context.
func retryFailureHints(values []VerifyFailure) []VerifyFailure {
	result := cloneFailures(values)
	for index := range result {
		result[index].Text = ""
		result[index].Span = shared.TextSpan{}
	}
	return result
}
