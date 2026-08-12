package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type answerRunnerComposer struct {
	narratives []answer.NarrativeLayer
	requests   []answer.ComposeRequest
}

func (composer *answerRunnerComposer) Compose(_ context.Context, request answer.ComposeRequest) (answer.NarrativeLayer, error) {
	composer.requests = append(composer.requests, request)
	index := len(composer.requests) - 1
	if index >= len(composer.narratives) {
		index = len(composer.narratives) - 1
	}
	return composer.narratives[index], nil
}

type answerRunnerStore struct {
	run      Run
	requests []TransitionRequest
}

func (store *answerRunnerStore) Transition(_ context.Context, request TransitionRequest) (TransitionResult, error) {
	store.requests = append(store.requests, request)
	var completion *CompletionRef
	if request.Completion != nil {
		completion = &CompletionRef{
			Code: request.Completion.Code, ArtifactType: request.Completion.Type,
			ArtifactHash: askdata.HashBytes(request.Completion.Payload),
		}
	}
	next, err := Apply(store.run, Transition{
		ExpectedVersion: request.ExpectedVersion, TargetState: request.TargetState,
		Usage: request.Usage, Hashes: request.Hashes, Completion: completion,
	})
	if err != nil {
		return TransitionResult{}, err
	}
	store.run = next
	return TransitionResult{Run: next, Event: Event{
		State: next.State, Code: request.Event.Code, Details: request.Event.Details,
	}}, nil
}

type answerRunnerFixture struct {
	run      Run
	scope    askdata.PolicyScope
	input    answer.CompositionInput
	valid    answer.NarrativeLayer
	invalid  answer.NarrativeLayer
	verifier *answer.Verifier
}

func newAnswerRunnerFixture(t *testing.T) answerRunnerFixture {
	t.Helper()
	run := validRun(StateResultVerifying)
	run.Hashes = completeHashes()
	scope, err := askdata.NewPolicyScope(
		run.TenantID, run.ActorID, []askdata.ID{run.DomainID}, []askdata.ID{"role:analyst"}, run.Release,
	)
	if err != nil {
		t.Fatal(err)
	}
	run.PolicyScopeHash = scope.PolicyHash
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "month", Value: "2026-08"}})
	if err != nil {
		t.Fatal(err)
	}
	cellRef := shared.CellRef{RowKey: rowKey, ColumnKey: "sales_amount"}
	policy := answer.DefaultReleaseVerifierPolicy(false)
	artifact := answer.AnswerArtifact{
		SchemaVersion: answer.SchemaVersion, RunID: run.ID,
		Layers: answer.AnswerLayers{
			Structured: answer.StructuredLayer{
				Headline: &answer.MetricValue{
					MetricVersionID: "metric:sales@v1", Value: "128", Unit: "CNY",
					Label: "销售额", ColumnKey: "sales_amount",
				},
				Cards: []answer.MetricValue{}, TableRef: "result:artifact:v1",
			},
			Narrative: answer.NarrativeLayer{Findings: []string{}, Citations: []shared.Citation{}},
		},
		Verification: answer.Verification{
			VerifierVersion: policy.VerifierVersion, PolicyWordlistVersion: policy.PolicyWordlistVersion,
			Attempts: 0, Passed: false, Degraded: true,
		},
		Provenance: answer.Provenance{
			PromptVersion: "answer-v1", ModelPolicy: "narrative-strict",
			EvidenceHash: askdata.HashBytes([]byte("evidence")), ResultHash: run.Hashes.Result,
			SemanticReleaseID: run.Release.ReleaseID, ChartRuleVersion: "chart-rules-v1",
		},
	}
	resultEvidence := answer.ResultEvidence{
		Version: answer.ResultEvidenceVersion, ReferenceHash: run.Hashes.Result,
		Cells: []answer.ResultCell{{
			Ref: cellRef, MetricVersionID: "metric:sales@v1", Value: "128",
			ValueKind: answer.ValueNumber, Unit: "CNY", Currency: "CNY", DisplayPrecision: 0,
		}}, Derivations: []answer.DerivationEvidence{},
	}.Normalize()
	binding := answer.BindingEvidence{
		Source:  answer.BindingSourceSemanticRelease,
		Version: answer.BindingEvidenceVersion, SemanticReleaseID: run.Release.ReleaseID,
		Objects: []answer.ObjectEvidence{{
			ObjectID: "metric:sales@v1", Kind: answer.ObjectMetric, Bound: true, Names: []string{"销售额"},
		}},
	}.Normalize()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	timeSpec := compiler.ResolvedTimeSpec{
		RequestedPeriod: "CURRENT_MONTH", Grain: "MONTH", PolicyApplied: "MTD", PolicySource: "TIME_CONTRACT",
		ResolvedStart:               time.Date(2026, 8, 1, 0, 0, 0, 0, location),
		ResolvedEndExclusive:        time.Date(2026, 8, 7, 0, 0, 0, 0, location),
		DataAvailableThrough:        time.Date(2026, 8, 6, 10, 30, 0, 0, location),
		TruncatedByDataAvailability: true, Timezone: location.String(),
	}
	validText, invalidText := "128元", "999元"
	valid := answer.NarrativeLayer{
		Summary: validText, Findings: []string{},
		Citations: []shared.Citation{shared.NewResultCellCitation(
			shared.TextSpan{Start: 0, End: len([]rune(validText))}, cellRef,
		)},
	}
	invalid := answer.NarrativeLayer{
		Summary: invalidText, Findings: []string{},
		Citations: []shared.Citation{shared.NewResultCellCitation(
			shared.TextSpan{Start: 0, End: len([]rune(invalidText))}, cellRef,
		)},
	}
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		t.Fatal(err)
	}
	return answerRunnerFixture{
		run: run, scope: scope, verifier: verifier, valid: valid, invalid: invalid,
		input: answer.CompositionInput{
			Artifact: artifact, Result: resultEvidence, Binding: binding, TimeSpec: timeSpec,
			InterpretationEnabled: true,
		},
	}
}

func TestAnswerVerificationRunnerPassesAndForcesAskDataL3Off(t *testing.T) {
	fixture := newAnswerRunnerFixture(t)
	store := &answerRunnerStore{run: fixture.run}
	composer := &answerRunnerComposer{narratives: []answer.NarrativeLayer{fixture.valid}}
	runner, err := NewAnswerVerificationRunner(store, composer, fixture.verifier)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), AnswerRunRequest{
		Scope: fixture.scope, DomainID: fixture.run.DomainID, Run: fixture.run,
		Input: fixture.input, EvidenceIDs: []askdata.ID{"evidence:sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != StateAnswered || result.Degraded || !result.Artifact.Verification.Passed ||
		result.Artifact.Verification.Attempts != 1 || result.Run.Usage.LLMCallsUsed != 1 ||
		len(composer.requests) != 1 || composer.requests[0].InterpretationEnabled ||
		strings.Join(result.EventCodes, ",") != "ANSWER_VERIFYING,ANSWER_VERIFIED" {
		t.Fatalf("unexpected verified answer result: %#v requests=%#v", result, composer.requests)
	}
}

func TestAnswerVerificationRunnerRetriesWithoutRejectedProseThenPasses(t *testing.T) {
	fixture := newAnswerRunnerFixture(t)
	store := &answerRunnerStore{run: fixture.run}
	composer := &answerRunnerComposer{narratives: []answer.NarrativeLayer{fixture.invalid, fixture.valid}}
	runner, _ := NewAnswerVerificationRunner(store, composer, fixture.verifier)
	result, err := runner.Run(context.Background(), AnswerRunRequest{
		Scope: fixture.scope, DomainID: fixture.run.DomainID, Run: fixture.run, Input: fixture.input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Degraded || result.Artifact.Verification.Attempts != 2 ||
		result.Run.Usage.LLMCallsUsed != 2 || len(composer.requests) != 2 ||
		len(composer.requests[1].RetryFailures) == 0 ||
		composer.requests[1].RetryFailures[0].Text != "" {
		t.Fatalf("unexpected retry result: %#v requests=%#v", result, composer.requests)
	}
	if strings.Join(result.EventCodes, ",") != "ANSWER_VERIFYING,ANSWER_VERIFICATION_FAILED,ANSWER_VERIFIED" {
		t.Fatalf("event codes = %#v", result.EventCodes)
	}
}

func TestAnswerVerificationRunnerRepeatedFailureReturnsAuditableL1(t *testing.T) {
	fixture := newAnswerRunnerFixture(t)
	store := &answerRunnerStore{run: fixture.run}
	composer := &answerRunnerComposer{narratives: []answer.NarrativeLayer{fixture.invalid}}
	runner, _ := NewAnswerVerificationRunner(store, composer, fixture.verifier)
	result, err := runner.Run(context.Background(), AnswerRunRequest{
		Scope: fixture.scope, DomainID: fixture.run.DomainID, Run: fixture.run, Input: fixture.input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Degraded || !result.Artifact.Layers.Narrative.Empty() ||
		result.Artifact.Verification.Passed || result.Artifact.Verification.Attempts != 2 ||
		result.Run.CompletionCode != "ANSWER_DEGRADED" || result.Run.State != StateAnswered ||
		result.Run.Usage.LLMCallsUsed != 2 {
		t.Fatalf("unverified prose crossed terminal boundary: %#v", result)
	}
	for _, request := range store.requests {
		if strings.Contains(string(request.Event.Details), "999") {
			t.Fatalf("rejected prose persisted in event details: %s", request.Event.Details)
		}
		if len(request.Event.Details) > 0 {
			var details map[string]any
			if json.Unmarshal(request.Event.Details, &details) != nil {
				t.Fatalf("invalid event details: %s", request.Event.Details)
			}
		}
	}
}

func TestAnswerVerificationRunnerBudgetExhaustionSkipsOrBoundsRegeneration(t *testing.T) {
	for _, test := range []struct {
		name             string
		usage            func(BudgetLimits) BudgetUsage
		wantModelCalls   int
		wantAttempts     int
		wantFailureEvent bool
	}{
		{
			name: "llm budget already exhausted",
			usage: func(limits BudgetLimits) BudgetUsage {
				return BudgetUsage{LLMCallsUsed: limits.MaxLLMCalls}
			},
		},
		{
			name: "step budget already exhausted",
			usage: func(limits BudgetLimits) BudgetUsage {
				return BudgetUsage{StepCount: limits.MaxSteps}
			},
		},
		{
			name: "only one answer call remains",
			usage: func(limits BudgetLimits) BudgetUsage {
				return BudgetUsage{LLMCallsUsed: limits.MaxLLMCalls - 1}
			},
			wantModelCalls: 1, wantAttempts: 1, wantFailureEvent: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAnswerRunnerFixture(t)
			fixture.run.Usage = test.usage(fixture.run.Limits)
			store := &answerRunnerStore{run: fixture.run}
			composer := &answerRunnerComposer{narratives: []answer.NarrativeLayer{fixture.invalid}}
			runner, _ := NewAnswerVerificationRunner(store, composer, fixture.verifier)
			result, err := runner.Run(context.Background(), AnswerRunRequest{
				Scope: fixture.scope, DomainID: fixture.run.DomainID, Run: fixture.run, Input: fixture.input,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.State != StateAnswered || !result.Degraded ||
				!result.Artifact.Layers.Narrative.Empty() || !result.Run.Usage.Exhausted ||
				result.Artifact.Verification.Attempts != test.wantAttempts ||
				len(composer.requests) != test.wantModelCalls {
				t.Fatalf("unexpected budget fallback: %#v requests=%#v", result, composer.requests)
			}
			hasFailureEvent := false
			for _, code := range result.EventCodes {
				hasFailureEvent = hasFailureEvent || code == "ANSWER_VERIFICATION_FAILED"
			}
			if hasFailureEvent != test.wantFailureEvent {
				t.Fatalf("failure event=%v codes=%#v", hasFailureEvent, result.EventCodes)
			}
		})
	}
}

func TestAnswerVerificationRunnerRejectsCrossRunEvidence(t *testing.T) {
	fixture := newAnswerRunnerFixture(t)
	fixture.input.Result.ReferenceHash = askdata.HashBytes([]byte("other result"))
	store := &answerRunnerStore{run: fixture.run}
	composer := &answerRunnerComposer{narratives: []answer.NarrativeLayer{fixture.valid}}
	runner, _ := NewAnswerVerificationRunner(store, composer, fixture.verifier)
	if _, err := runner.Run(context.Background(), AnswerRunRequest{
		Scope: fixture.scope, DomainID: fixture.run.DomainID, Run: fixture.run, Input: fixture.input,
	}); err != ErrAnswerVerification || len(store.requests) != 0 || len(composer.requests) != 0 {
		t.Fatalf("cross-run evidence error=%v store=%d compose=%d", err, len(store.requests), len(composer.requests))
	}
}
