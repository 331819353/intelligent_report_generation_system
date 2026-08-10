package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/validator"
)

var ErrAnswerVerification = errors.New("answer verification runner failed")

type AnswerTransitionStore interface {
	Transition(context.Context, TransitionRequest) (TransitionResult, error)
}

type AnswerRunRequest struct {
	Scope       askdata.PolicyScope
	DomainID    askdata.ID
	Run         Run
	Input       answer.CompositionInput
	Outcome     validator.Outcome
	EvidenceIDs []askdata.ID
}

type AnswerRunResult struct {
	Run        Run
	Artifact   answer.AnswerArtifact
	Reports    []answer.VerifyReport
	Degraded   bool
	EventCodes []string
}

// AnswerVerificationRunner owns the mandatory RESULT_VERIFYING ->
// ANSWER_VERIFYING -> ANSWERED boundary. Failed prose is represented only by
// stable reason codes/hashes in events; the terminal artifact is either
// verified or the structured L1 fallback.
type AnswerVerificationRunner struct {
	store    AnswerTransitionStore
	composer answer.NarrativeComposer
	verifier *answer.Verifier
	metrics  *answer.NarrativeMetrics
}

func NewAnswerVerificationRunner(
	store AnswerTransitionStore,
	composer answer.NarrativeComposer,
	verifier *answer.Verifier,
) (*AnswerVerificationRunner, error) {
	if store == nil || composer == nil || verifier == nil {
		return nil, ErrAnswerVerification
	}
	return &AnswerVerificationRunner{store: store, composer: composer, verifier: verifier}, nil
}

func (runner *AnswerVerificationRunner) SetNarrativeMetrics(metrics *answer.NarrativeMetrics) {
	if runner != nil {
		runner.metrics = metrics
	}
}

func (runner *AnswerVerificationRunner) Run(
	ctx context.Context,
	request AnswerRunRequest,
) (AnswerRunResult, error) {
	result := AnswerRunResult{Reports: []answer.VerifyReport{}, EventCodes: []string{}}
	if runner == nil || ctx == nil || request.Run.State != StateResultVerifying ||
		request.Run.Validate() != nil || request.DomainID != request.Run.DomainID ||
		!answerScopeMatchesRun(request.Run, request.Scope) ||
		!answerInputMatchesRun(request.Run, request.Input) {
		return result, ErrAnswerVerification
	}
	entered, err := runner.store.Transition(ctx, TransitionRequest{
		Scope: request.Scope, DomainID: request.DomainID, RunID: request.Run.ID,
		ExpectedVersion: request.Run.RecordVersion, TargetState: StateAnswerVerifying,
		Usage: request.Run.Usage,
		Event: TransitionEventInput{Stage: string(StateAnswerVerifying), Status: EventStarted, Code: "ANSWER_VERIFYING"},
	})
	if err != nil {
		return result, fmt.Errorf("%w: enter stage: %v", ErrAnswerVerification, err)
	}
	current := entered.Run
	result.EventCodes = append(result.EventCodes, "ANSWER_VERIFYING")

	remaining := answerModelCallsRemaining(current.Usage, current.Limits)
	input := request.Input
	input.LLMCallsRemaining = remaining
	input.InterpretationEnabled = answer.DefaultAskDataInterpretationEnabled
	composed, err := answer.ComposeAndVerify(ctx, runner.composer, runner.verifier, input)
	if err != nil {
		return result, fmt.Errorf("%w: compose: %v", ErrAnswerVerification, err)
	}
	result.Artifact, result.Reports, result.Degraded = composed.Artifact, composed.Reports, composed.Degraded
	if failureStore, ok := runner.store.(narrativeFailureSampleStore); ok {
		if err := failureStore.RecordNarrativeFailureSamples(
			ctx, request.Scope, request.Run, answer.NarrativeRunAskData, composed.FailureSamples,
		); err != nil {
			return result, fmt.Errorf("%w: persist failure samples: %v", ErrAnswerVerification, err)
		}
	}

	// Persist each failed attempt before completion so retry/degradation is
	// visible and resumable from the durable event stream.
	for index, report := range composed.Reports {
		last := index == len(composed.Reports)-1
		if report.Passed && last {
			break
		}
		usage, usageErr := chargedAnswerUsage(current.Usage, current.Limits)
		if usageErr != nil {
			break
		}
		details, detailsErr := verificationEventDetails(report)
		if detailsErr != nil {
			return result, detailsErr
		}
		checkpoint, transitionErr := runner.store.Transition(ctx, TransitionRequest{
			Scope: request.Scope, DomainID: request.DomainID, RunID: current.ID,
			ExpectedVersion: current.RecordVersion, TargetState: StateAnswerVerifying,
			Usage: usage,
			Event: TransitionEventInput{
				Stage: string(StateAnswerVerifying), Status: EventFailed,
				Code: "ANSWER_VERIFICATION_FAILED", Details: details,
			},
		})
		if transitionErr != nil {
			return result, fmt.Errorf("%w: record failure: %v", ErrAnswerVerification, transitionErr)
		}
		current = checkpoint.Run
		result.EventCodes = append(result.EventCodes, "ANSWER_VERIFICATION_FAILED")
		if last {
			break
		}
	}

	// Charge the final successful model call when it was not represented by a
	// failed-attempt checkpoint. A zero-attempt fallback consumes no LLM call.
	charged := 0
	for _, code := range result.EventCodes {
		if code == "ANSWER_VERIFICATION_FAILED" {
			charged++
		}
	}
	if len(composed.Reports) > charged {
		usage, usageErr := chargedAnswerUsage(current.Usage, current.Limits)
		if usageErr != nil {
			return result, usageErr
		}
		current.Usage = usage
	}
	// A bounded answer attempt that cannot use the full retry allowance is a
	// budget degradation, not an implicit third outcome. Persist the exhausted
	// bit on the terminal run while still returning the safe L1 artifact.
	if composed.Degraded && remaining < 2 {
		current.Usage.Exhausted = true
	}
	answerPayload, err := composed.Artifact.MarshalCanonical()
	if err != nil {
		return result, fmt.Errorf("%w: terminal artifact: %v", ErrAnswerVerification, err)
	}
	outcome := request.Outcome
	if outcome.Validate() != nil {
		// Older callers without QUERY-011 wiring remain safe and exportable only
		// as a complete answer. Production pipelines should always pass their
		// validator-produced outcome explicitly.
		outcome = validator.DetermineOutcome(validator.OutcomeContext{})
	}
	payload, err := json.Marshal(struct {
		Answer  json.RawMessage   `json:"answer"`
		Outcome validator.Outcome `json:"outcome"`
	}{Answer: answerPayload, Outcome: outcome})
	if err != nil {
		return result, fmt.Errorf("%w: terminal envelope: %v", ErrAnswerVerification, err)
	}
	code := "ANSWER_VERIFIED"
	if composed.Degraded {
		code = "ANSWER_DEGRADED"
	}
	completed, err := runner.store.Transition(ctx, TransitionRequest{
		Scope: request.Scope, DomainID: request.DomainID, RunID: current.ID,
		ExpectedVersion: current.RecordVersion, TargetState: StateAnswered, Usage: current.Usage,
		Completion: &CompletionArtifactInput{
			Code: code, Type: ArtifactAnswer, SchemaVersion: answer.SchemaVersion,
			EvidenceIDs: append([]askdata.ID(nil), request.EvidenceIDs...), Payload: payload,
		},
		Event: TransitionEventInput{
			Stage: string(StateAnswered), Status: EventSucceeded, Code: code,
			Details: mustCanonicalAudit(map[string]any{
				"attempts":          composed.Artifact.Verification.Attempts,
				"narrativeDegraded": composed.Degraded,
			}),
		},
	})
	if err != nil {
		return result, fmt.Errorf("%w: complete: %v", ErrAnswerVerification, err)
	}
	result.Run = completed.Run
	result.EventCodes = append(result.EventCodes, code)
	if runner.metrics != nil {
		_, _ = runner.metrics.Record(answer.NarrativeMetricInput{
			DomainID: request.DomainID, RunType: answer.NarrativeRunAskData,
			Reports: composed.Reports, Degraded: composed.Degraded,
		})
	}
	return result, nil
}

func answerScopeMatchesRun(run Run, scope askdata.PolicyScope) bool {
	if scope.Validate() != nil || scope.TenantID != run.TenantID ||
		scope.ActorID != run.ActorID || scope.Release != run.Release ||
		scope.PolicyHash != run.PolicyScopeHash {
		return false
	}
	for _, domainID := range scope.DomainIDs {
		if domainID == run.DomainID {
			return true
		}
	}
	return false
}

func answerInputMatchesRun(run Run, input answer.CompositionInput) bool {
	return input.Artifact.RunID == run.ID &&
		input.Artifact.Provenance.SemanticReleaseID == run.Release.ReleaseID &&
		input.Artifact.Provenance.ResultHash == run.Hashes.Result &&
		input.Result.ReferenceHash == run.Hashes.Result &&
		input.Binding.SemanticReleaseID == run.Release.ReleaseID
}

func chargedAnswerUsage(current BudgetUsage, limits BudgetLimits) (BudgetUsage, error) {
	if answerModelCallsRemaining(current, limits) < 1 {
		return BudgetUsage{}, ErrLoopBudgetExhausted
	}
	next := current
	next.LLMCallsUsed++
	next.StepCount++
	return next, nil
}

func answerModelCallsRemaining(usage BudgetUsage, limits BudgetLimits) int {
	if usage.ElapsedMS >= limits.MaxDurationMS {
		return 0
	}
	remaining := limits.MaxLLMCalls - usage.LLMCallsUsed
	if steps := limits.MaxSteps - usage.StepCount; steps < remaining {
		remaining = steps
	}
	if remaining < 0 {
		return 0
	}
	if remaining > 2 {
		return 2
	}
	return remaining
}

func verificationEventDetails(report answer.VerifyReport) (json.RawMessage, error) {
	codes := make([]string, 0, len(report.Failures))
	for _, failure := range report.Failures {
		codes = append(codes, string(failure.Reason))
	}
	sort.Strings(codes)
	return json.Marshal(map[string]any{
		"failureCodes":          codes,
		"failureCount":          len(report.Failures),
		"verifierVersion":       report.VerifierVersion,
		"policyWordlistVersion": report.PolicyWordlistVersion,
	})
}
