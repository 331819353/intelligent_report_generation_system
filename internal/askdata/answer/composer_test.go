package answer

import (
	"context"
	"testing"
)

type scriptedComposer struct {
	narratives []NarrativeLayer
	requests   []ComposeRequest
}

func (composer *scriptedComposer) Compose(_ context.Context, request ComposeRequest) (NarrativeLayer, error) {
	composer.requests = append(composer.requests, request)
	index := len(composer.requests) - 1
	if index >= len(composer.narratives) {
		index = len(composer.narratives) - 1
	}
	return composer.narratives[index], nil
}

func TestToStructuredErasesRejectedNarrative(t *testing.T) {
	fixture := baseVerifierFixture(t)
	artifact := fixture.artifact
	degraded, err := ToStructured(artifact, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !degraded.Layers.Narrative.Empty() || !degraded.Verification.Degraded ||
		degraded.Verification.Passed || degraded.Verification.Attempts != 2 ||
		DegradedNarrativeHint == "" {
		t.Fatalf("unexpected structured fallback: %#v", degraded)
	}
}

func TestComposeAndVerifyRetriesWithStructuredFailuresThenPasses(t *testing.T) {
	fixture := baseVerifierFixture(t)
	artifact, resultEvidence, binding, timeSpec := fixture.artifact, fixture.result, fixture.binding, fixture.timeSpec
	valid := narrativeArtifact(t, fixture, "128万元", resultCitation("128万元")(fixture)).Layers.Narrative
	invalid := narrativeArtifact(t, fixture, "999元", resultCitation("999元")(fixture)).Layers.Narrative
	composer := &scriptedComposer{narratives: []NarrativeLayer{invalid, valid}}
	verifier, err := NewVerifier(DefaultReleaseVerifierPolicy(false))
	if err != nil {
		t.Fatal(err)
	}
	composed, err := ComposeAndVerify(context.Background(), composer, verifier, CompositionInput{
		Artifact: artifact, Result: resultEvidence, Binding: binding, TimeSpec: timeSpec,
		LLMCallsRemaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Degraded || !composed.Retried || len(composed.Reports) != 2 ||
		len(composer.requests) != 2 || len(composer.requests[1].RetryFailures) == 0 ||
		!composed.Artifact.Verification.Passed {
		t.Fatalf("unexpected verified retry: %#v requests=%#v", composed, composer.requests)
	}
	for _, failure := range composer.requests[1].RetryFailures {
		if failure.Text != "" || failure.Span.Start != 0 || failure.Span.End != 0 {
			t.Fatalf("rejected prose leaked into retry input: %#v", failure)
		}
	}
}

func TestComposeAndVerifyBudgetOrRepeatedFailureReturnsOnlyL1(t *testing.T) {
	fixture := baseVerifierFixture(t)
	artifact, resultEvidence, binding, timeSpec := fixture.artifact, fixture.result, fixture.binding, fixture.timeSpec
	invalid := narrativeArtifact(t, fixture, "999元", resultCitation("999元")(fixture)).Layers.Narrative
	composer := &scriptedComposer{narratives: []NarrativeLayer{invalid}}
	verifier, _ := NewVerifier(DefaultReleaseVerifierPolicy(false))
	composed, err := ComposeAndVerify(context.Background(), composer, verifier, CompositionInput{
		Artifact: artifact, Result: resultEvidence, Binding: binding, TimeSpec: timeSpec,
		LLMCallsRemaining: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !composed.Degraded || composed.Hint == "" || !composed.Artifact.Layers.Narrative.Empty() ||
		composed.Artifact.Verification.Attempts != 1 || len(composer.requests) != 1 {
		t.Fatalf("unverified text crossed fallback boundary: %#v", composed)
	}
}

func TestComposeAndVerifyAskDataInterpretationDefaultsOff(t *testing.T) {
	if DefaultAskDataInterpretationEnabled {
		t.Fatal("Ask Data interpretation must be opt-in")
	}
	fixture := baseVerifierFixture(t)
	valid := narrativeArtifact(t, fixture, "128万元", resultCitation("128万元")(fixture)).Layers.Narrative
	composer := &scriptedComposer{narratives: []NarrativeLayer{valid}}
	verifier, _ := NewVerifier(DefaultReleaseVerifierPolicy(false))
	_, err := ComposeAndVerify(context.Background(), composer, verifier, CompositionInput{
		Artifact: fixture.artifact, Result: fixture.result, Binding: fixture.binding,
		TimeSpec: fixture.timeSpec, InterpretationEnabled: DefaultAskDataInterpretationEnabled,
		LLMCallsRemaining: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(composer.requests) != 1 || composer.requests[0].InterpretationEnabled {
		t.Fatalf("Ask Data unexpectedly enabled L3: %#v", composer.requests)
	}
}
