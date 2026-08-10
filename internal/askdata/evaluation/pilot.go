package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidPilot = errors.New("pilot lifecycle is invalid")

type PilotStage string

const (
	PilotReady      PilotStage = "READY"
	PilotShadow     PilotStage = "SHADOW"
	PilotCanary5    PilotStage = "CANARY_5"
	PilotCanary20   PilotStage = "CANARY_20"
	PilotCanary50   PilotStage = "CANARY_50"
	PilotAccepted95 PilotStage = "ACCEPTED_95"
	PilotStopped    PilotStage = "STOPPED"
)

type PilotIdentity struct {
	TenantID           askdata.ID
	DomainID           askdata.ID
	ControlReleaseID   askdata.ID
	CandidateReleaseID askdata.ID
	EvaluationSetID    askdata.ID
	EvaluationSetHash  askdata.ContentHash
	ReleaseHash        askdata.ContentHash
	ModelVersion       string
	EmbeddingVersion   string
}

type PilotAcceptanceFacts struct {
	SealedCases                 int
	StrictAccuracy              float64
	WilsonLowerBound            float64
	DirectCoverage              float64
	ClarificationRefusalRate    float64
	P0Accuracy                  float64
	SecurityAccuracy            float64
	SensitiveLeaks              int
	CanarySignificantRegression bool
}

type PilotAdvanceEvidence struct {
	FromStage            PilotStage
	StageSampleCount     int
	MinimumStageSamples  int
	StageStartedAt       time.Time
	ObservedThrough      time.Time
	MinimumStageDuration time.Duration
	OfflineOnlineAligned bool
	SecurityPassed       bool
	AutomaticStop        AutomaticStopDecision
	GateReceiptHash      askdata.ContentHash
	SemanticApprovalHash askdata.ContentHash
	DataApprovalHash     askdata.ContentHash
	ManualApprovalHash   askdata.ContentHash
	Acceptance           PilotAcceptanceFacts
}

type PilotAdvanceReceipt struct {
	Identity           PilotIdentity       `json:"identity"`
	FromStage          PilotStage          `json:"fromStage"`
	ToStage            PilotStage          `json:"toStage"`
	StageSampleCount   int                 `json:"stageSampleCount"`
	ObservedThrough    time.Time           `json:"observedThrough"`
	GateReceiptHash    askdata.ContentHash `json:"gateReceiptHash"`
	ManualApprovalHash askdata.ContentHash `json:"manualApprovalHash"`
	ReceiptHash        askdata.ContentHash `json:"receiptHash"`
}

// AdvancePilot validates one manually approved expansion. Automated code may
// call StopPilot, but cannot use this function without a new approval receipt.
func AdvancePilot(identity PilotIdentity, evidence PilotAdvanceEvidence) (PilotAdvanceReceipt, error) {
	if validatePilotIdentity(identity) != nil || validatePilotAdvanceEvidence(evidence) != nil {
		return PilotAdvanceReceipt{}, ErrInvalidPilot
	}
	next, valid := nextPilotStage(evidence.FromStage)
	if !valid || evidence.AutomaticStop.Stop || len(evidence.AutomaticStop.Codes) != 0 ||
		!evidence.SecurityPassed || evidence.StageSampleCount < evidence.MinimumStageSamples ||
		evidence.ObservedThrough.Sub(evidence.StageStartedAt) < evidence.MinimumStageDuration {
		return PilotAdvanceReceipt{}, ErrInvalidPilot
	}
	if evidence.FromStage == PilotShadow && !evidence.OfflineOnlineAligned {
		return PilotAdvanceReceipt{}, ErrInvalidPilot
	}
	if next == PilotAccepted95 && !pilotAcceptancePassed(evidence.Acceptance) {
		return PilotAdvanceReceipt{}, ErrInvalidPilot
	}
	receipt := PilotAdvanceReceipt{
		Identity: identity, FromStage: evidence.FromStage, ToStage: next,
		StageSampleCount: evidence.StageSampleCount, ObservedThrough: evidence.ObservedThrough.UTC(),
		GateReceiptHash: evidence.GateReceiptHash, ManualApprovalHash: evidence.ManualApprovalHash,
	}
	receiptHash, err := hashPilotValue(struct {
		SchemaVersion string               `json:"schemaVersion"`
		Receipt       PilotAdvanceReceipt  `json:"receipt"`
		Acceptance    PilotAcceptanceFacts `json:"acceptance"`
		Semantic      askdata.ContentHash  `json:"semanticApprovalHash"`
		Data          askdata.ContentHash  `json:"dataApprovalHash"`
	}{"askdata-pilot-advance-v1", receipt, evidence.Acceptance, evidence.SemanticApprovalHash, evidence.DataApprovalHash})
	if err != nil {
		return PilotAdvanceReceipt{}, ErrInvalidPilot
	}
	receipt.ReceiptHash = receiptHash
	return receipt, nil
}

type PilotStopReceipt struct {
	Identity    PilotIdentity       `json:"identity"`
	FromStage   PilotStage          `json:"fromStage"`
	Codes       []string            `json:"codes"`
	StoppedAt   time.Time           `json:"stoppedAt"`
	ReceiptHash askdata.ContentHash `json:"receiptHash"`
}

func StopPilot(identity PilotIdentity, stage PilotStage, decision AutomaticStopDecision, stoppedAt time.Time) (PilotStopReceipt, error) {
	if validatePilotIdentity(identity) != nil || stage == PilotReady || stage == PilotAccepted95 || stage == PilotStopped ||
		!decision.Stop || len(decision.Codes) == 0 || stoppedAt.IsZero() {
		return PilotStopReceipt{}, ErrInvalidPilot
	}
	codes := append([]string(nil), decision.Codes...)
	sort.Strings(codes)
	for index, code := range codes {
		if !regressionCodePattern.MatchString(code) || index > 0 && code == codes[index-1] {
			return PilotStopReceipt{}, ErrInvalidPilot
		}
	}
	receipt := PilotStopReceipt{Identity: identity, FromStage: stage, Codes: codes, StoppedAt: stoppedAt.UTC()}
	receiptHash, err := hashPilotValue(struct {
		SchemaVersion string           `json:"schemaVersion"`
		Receipt       PilotStopReceipt `json:"receipt"`
	}{"askdata-pilot-stop-v1", receipt})
	if err != nil {
		return PilotStopReceipt{}, ErrInvalidPilot
	}
	receipt.ReceiptHash = receiptHash
	return receipt, nil
}

type ShadowExecutionRequest struct {
	TenantID           askdata.ID
	DomainID           askdata.ID
	SourceRunID        askdata.ID
	CandidateReleaseID askdata.ID
	CandidateHash      askdata.ContentHash
	PolicyScopeHash    askdata.ContentHash
	WarehouseHash      askdata.ContentHash
}

type ShadowCandidateOutcome struct {
	Observation  ExperimentObservation
	FailureStage FailureStage
	FailureCode  string
}

type ShadowCandidateExecutor interface {
	ExecuteShadow(context.Context, ShadowExecutionRequest) (ShadowCandidateOutcome, error)
}

type ShadowObservationSink interface {
	AppendShadowObservation(context.Context, ShadowExecutionRequest, ShadowCandidateOutcome) error
}

// RunShadow executes only stable handles and discards the candidate artifact.
// It has no production result or SQL output in its return contract.
func RunShadow(ctx context.Context, executor ShadowCandidateExecutor, sink ShadowObservationSink, request ShadowExecutionRequest) error {
	if ctx == nil || executor == nil || sink == nil || validateShadowExecutionRequest(request) != nil {
		return ErrInvalidPilot
	}
	outcome, err := executor.ExecuteShadow(ctx, request)
	if err != nil {
		outcome = ShadowCandidateOutcome{
			Observation: ExperimentObservation{
				ReleaseID: request.CandidateReleaseID, DomainID: request.DomainID,
				Role: "SHADOW_WORKER", Cohort: CohortShadow,
			},
			FailureStage: FailureStageExecution, FailureCode: "SHADOW_EXECUTION_ERROR",
		}
	}
	if validateExperimentObservation(outcome.Observation) != nil || outcome.Observation.Cohort != CohortShadow ||
		outcome.Observation.ReleaseID != request.CandidateReleaseID || outcome.Observation.DomainID != request.DomainID ||
		(outcome.FailureCode != "" && (!validFailureStage(outcome.FailureStage) || !regressionCodePattern.MatchString(outcome.FailureCode))) {
		return ErrInvalidPilot
	}
	return sink.AppendShadowObservation(ctx, request, outcome)
}

type DomainReplicationPlan struct {
	SourceDomainID          askdata.ID
	TargetDomainID          askdata.ID
	TargetOwnerID           askdata.ID
	ImportManifestHash      askdata.ContentHash
	GovernanceHash          askdata.ContentHash
	TargetReleaseID         askdata.ID
	TargetReleaseHash       askdata.ContentHash
	TargetEvaluationSet     askdata.ID
	TargetEvaluationHash    askdata.ContentHash
	FreshEvaluationRequired bool
	InheritedAccuracyClaim  bool
}

func ValidateDomainReplicationPlan(plan DomainReplicationPlan) error {
	if plan.SourceDomainID.Validate() != nil || plan.TargetDomainID.Validate() != nil ||
		plan.SourceDomainID == plan.TargetDomainID || plan.TargetOwnerID.Validate() != nil ||
		plan.ImportManifestHash.Validate() != nil || plan.GovernanceHash.Validate() != nil ||
		plan.TargetReleaseID.Validate() != nil || plan.TargetReleaseHash.Validate() != nil ||
		plan.TargetEvaluationSet.Validate() != nil || plan.TargetEvaluationHash.Validate() != nil ||
		!plan.FreshEvaluationRequired || plan.InheritedAccuracyClaim {
		return ErrInvalidPilot
	}
	return nil
}

func validatePilotIdentity(identity PilotIdentity) error {
	for _, id := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ControlReleaseID, identity.CandidateReleaseID, identity.EvaluationSetID} {
		if id.Validate() != nil {
			return ErrInvalidPilot
		}
	}
	if identity.ControlReleaseID == identity.CandidateReleaseID || identity.EvaluationSetHash.Validate() != nil ||
		identity.ReleaseHash.Validate() != nil || identity.ModelVersion == "" || len(identity.ModelVersion) > 128 ||
		identity.EmbeddingVersion == "" || len(identity.EmbeddingVersion) > 128 {
		return ErrInvalidPilot
	}
	return nil
}

func validatePilotAdvanceEvidence(evidence PilotAdvanceEvidence) error {
	if evidence.GateReceiptHash.Validate() != nil || evidence.SemanticApprovalHash.Validate() != nil ||
		evidence.DataApprovalHash.Validate() != nil || evidence.ManualApprovalHash.Validate() != nil ||
		evidence.SemanticApprovalHash == evidence.DataApprovalHash || evidence.StageSampleCount < 1 ||
		evidence.MinimumStageSamples < 1 || evidence.MinimumStageDuration < 0 ||
		evidence.StageStartedAt.IsZero() || evidence.ObservedThrough.IsZero() ||
		evidence.ObservedThrough.Before(evidence.StageStartedAt) {
		return ErrInvalidPilot
	}
	return nil
}

func nextPilotStage(stage PilotStage) (PilotStage, bool) {
	switch stage {
	case PilotReady:
		return PilotShadow, true
	case PilotShadow:
		return PilotCanary5, true
	case PilotCanary5:
		return PilotCanary20, true
	case PilotCanary20:
		return PilotCanary50, true
	case PilotCanary50:
		return PilotAccepted95, true
	default:
		return "", false
	}
}

func pilotAcceptancePassed(facts PilotAcceptanceFacts) bool {
	return facts.SealedCases >= 2000 && facts.StrictAccuracy >= .96 && facts.WilsonLowerBound >= .95 &&
		facts.DirectCoverage >= .85 && facts.ClarificationRefusalRate >= .95 &&
		facts.P0Accuracy == 1 && facts.SecurityAccuracy == 1 && facts.SensitiveLeaks == 0 &&
		!facts.CanarySignificantRegression
}

func validateShadowExecutionRequest(request ShadowExecutionRequest) error {
	for _, id := range []askdata.ID{request.TenantID, request.DomainID, request.SourceRunID, request.CandidateReleaseID} {
		if id.Validate() != nil {
			return ErrInvalidPilot
		}
	}
	if request.CandidateHash.Validate() != nil || request.PolicyScopeHash.Validate() != nil || request.WarehouseHash.Validate() != nil {
		return ErrInvalidPilot
	}
	return nil
}

func hashPilotValue(value any) (askdata.ContentHash, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(encoded), nil
}
