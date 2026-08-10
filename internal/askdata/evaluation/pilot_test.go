package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPilotRequiresManualReceiptAndNeverSkipsCanaryStage(t *testing.T) {
	identity := pilotIdentity()
	evidence := pilotEvidence(PilotCanary5)
	receipt, err := AdvancePilot(identity, evidence)
	if err != nil || receipt.ToStage != PilotCanary20 || receipt.ReceiptHash.Validate() != nil {
		t.Fatalf("advance receipt=%+v err=%v", receipt, err)
	}
	evidence.ManualApprovalHash = ""
	if _, err := AdvancePilot(identity, evidence); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("automatic expansion without a manual receipt must fail")
	}
	evidence = pilotEvidence(PilotStopped)
	if _, err := AdvancePilot(identity, evidence); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("stopped pilot must not advance")
	}
}

func TestPilotAcceptanceUsesEvery95PercentGate(t *testing.T) {
	evidence := pilotEvidence(PilotCanary50)
	evidence.Acceptance = PilotAcceptanceFacts{
		SealedCases: 2000, StrictAccuracy: .96, WilsonLowerBound: .95,
		DirectCoverage: .85, ClarificationRefusalRate: .95,
		P0Accuracy: 1, SecurityAccuracy: 1,
	}
	receipt, err := AdvancePilot(pilotIdentity(), evidence)
	if err != nil || receipt.ToStage != PilotAccepted95 {
		t.Fatalf("acceptance receipt=%+v err=%v", receipt, err)
	}
	evidence.Acceptance.WilsonLowerBound = .949999
	if _, err := AdvancePilot(pilotIdentity(), evidence); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("sub-threshold Wilson bound must fail")
	}
	evidence.Acceptance.WilsonLowerBound = .95
	evidence.Acceptance.CanarySignificantRegression = true
	if _, err := AdvancePilot(pilotIdentity(), evidence); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("significant canary regression must fail acceptance")
	}
}

func TestAutomaticStopIsOneWayAndStable(t *testing.T) {
	decision := AutomaticStopDecision{Stop: true, Codes: []string{"CANARY_SECURITY_REGRESSION"}}
	receipt, err := StopPilot(pilotIdentity(), PilotCanary20, decision, time.Now())
	if err != nil || receipt.ReceiptHash.Validate() != nil || receipt.FromStage != PilotCanary20 {
		t.Fatalf("stop receipt=%+v err=%v", receipt, err)
	}
	if _, err := StopPilot(pilotIdentity(), PilotAccepted95, decision, time.Now()); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("accepted pilot cannot be rewritten to stopped")
	}
}

type shadowExecutorFixture struct{ returnedError bool }

func (fixture shadowExecutorFixture) ExecuteShadow(_ context.Context, request ShadowExecutionRequest) (ShadowCandidateOutcome, error) {
	if fixture.returnedError {
		return ShadowCandidateOutcome{}, errors.New("provider down")
	}
	return ShadowCandidateOutcome{Observation: ExperimentObservation{
		ReleaseID: request.CandidateReleaseID, DomainID: request.DomainID,
		Role: "SHADOW_WORKER", Cohort: CohortShadow, Accurate: true,
		SecurityPassed: true, Latency: time.Second, CostMicros: 100,
	}}, nil
}

type shadowSinkFixture struct {
	called  bool
	outcome ShadowCandidateOutcome
}

func (fixture *shadowSinkFixture) AppendShadowObservation(_ context.Context, _ ShadowExecutionRequest, outcome ShadowCandidateOutcome) error {
	fixture.called, fixture.outcome = true, outcome
	return nil
}

func TestShadowContractPersistsOnlyObservationAndFailsClosed(t *testing.T) {
	request := ShadowExecutionRequest{
		TenantID: "tenant-a", DomainID: "domain-a", SourceRunID: "run-a",
		CandidateReleaseID: "release-candidate", CandidateHash: hashOf("candidate"),
		PolicyScopeHash: hashOf("policy"), WarehouseHash: hashOf("warehouse"),
	}
	sink := &shadowSinkFixture{}
	if err := RunShadow(context.Background(), shadowExecutorFixture{}, sink, request); err != nil || !sink.called || !sink.outcome.Observation.Accurate {
		t.Fatalf("shadow result=%+v err=%v", sink, err)
	}
	sink = &shadowSinkFixture{}
	if err := RunShadow(context.Background(), shadowExecutorFixture{returnedError: true}, sink, request); err != nil ||
		sink.outcome.FailureCode != "SHADOW_EXECUTION_ERROR" || sink.outcome.Observation.Accurate {
		t.Fatalf("shadow failure result=%+v err=%v", sink, err)
	}
}

func TestSecondDomainMustReevaluateAndCannotInheritAccuracy(t *testing.T) {
	plan := DomainReplicationPlan{
		SourceDomainID: "domain-a", TargetDomainID: "domain-b", TargetOwnerID: "owner-b",
		ImportManifestHash: hashOf("import"), GovernanceHash: hashOf("governance"),
		TargetReleaseID: "release-b", TargetReleaseHash: hashOf("release-b"),
		TargetEvaluationSet: "set-b", TargetEvaluationHash: hashOf("set-b"),
		FreshEvaluationRequired: true,
	}
	if err := ValidateDomainReplicationPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.InheritedAccuracyClaim = true
	if err := ValidateDomainReplicationPlan(plan); !errors.Is(err, ErrInvalidPilot) {
		t.Fatal("accuracy inheritance must fail")
	}
}

func pilotIdentity() PilotIdentity {
	return PilotIdentity{
		TenantID: "tenant-a", DomainID: "domain-a", ControlReleaseID: "release-control",
		CandidateReleaseID: "release-candidate", EvaluationSetID: "set-a",
		EvaluationSetHash: hashOf("set"), ReleaseHash: hashOf("release"),
		ModelVersion: "model-v1", EmbeddingVersion: "embedding-v1",
	}
}

func pilotEvidence(stage PilotStage) PilotAdvanceEvidence {
	start := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	return PilotAdvanceEvidence{
		FromStage: stage, StageSampleCount: 100, MinimumStageSamples: 50,
		StageStartedAt: start, ObservedThrough: start.Add(24 * time.Hour), MinimumStageDuration: time.Hour,
		OfflineOnlineAligned: true, SecurityPassed: true,
		GateReceiptHash: hashOf("gate"), SemanticApprovalHash: hashOf("semantic"),
		DataApprovalHash: hashOf("data"), ManualApprovalHash: hashOf("manual"),
	}
}

var _ ShadowCandidateExecutor = shadowExecutorFixture{}
var _ ShadowObservationSink = (*shadowSinkFixture)(nil)
