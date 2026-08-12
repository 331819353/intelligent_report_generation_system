package orchestrator

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// factsForStage applies the cognition package's single visibility matrix to
// cross-stage facts. This keeps the worker from duplicating prompt policy or
// handing, for example, raw candidate sets to result verification.
func factsForStage(stage cognition.Stage, values []GovernedFact) []GovernedFact {
	result := make([]GovernedFact, 0, len(values))
	seen := make(map[askdata.ID]bool, len(values))
	for _, value := range values {
		if !cognition.FactAllowedAtStage(stage, value.Fact.Kind) || seen[value.Fact.EvidenceID] {
			continue
		}
		seen[value.Fact.EvidenceID] = true
		result = append(result, value)
	}
	return result
}

// outcomeFacts turns only validated, sanitized loop outputs into new governed
// facts. Tool result JSON has already crossed Tool Host's closed result schema;
// decision payloads have already crossed the cognition action schema. The
// model therefore receives durable identities and hashes rather than an
// untrusted reconstruction by the worker.
func outcomeFacts(run Run, result LoopResult) ([]GovernedFact, error) {
	facts := make([]GovernedFact, 0, len(result.ToolExecutions)+1)
	for _, execution := range result.ToolExecutions {
		if execution.Validate() != nil || execution.Response.Status != toolhost.ResponseSuccess {
			continue
		}
		kind, evidenceKind, ok := toolFactKind(execution.Response.Tool)
		if !ok {
			continue
		}
		fact, err := governedOutcomeFact(
			run, execution.Response.CallID, kind, evidenceKind,
			execution.Response.ResultHash, execution.Response.Result,
		)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	if result.Decision.ActionHash == "" {
		return facts, nil
	}
	payload, kind, evidenceKind, ok, err := decisionFactPayload(result.Decision.Action)
	if err != nil {
		return nil, err
	}
	if !ok {
		return facts, nil
	}
	fact, err := governedOutcomeFact(
		run, run.ID, kind, evidenceKind, result.Decision.ActionHash, payload,
	)
	if err != nil {
		return nil, err
	}
	return append(facts, fact), nil
}

func governedOutcomeFact(
	run Run,
	sourceID askdata.ID,
	kind cognition.FactKind,
	evidenceKind askdata.EvidenceKind,
	identity askdata.ContentHash,
	payload json.RawMessage,
) (GovernedFact, error) {
	if run.Validate() != nil || sourceID.Validate() != nil || identity.Validate() != nil {
		return GovernedFact{}, fmt.Errorf("%w: cross-stage fact identity is invalid", ErrInvalidRun)
	}
	evidenceID := askdata.ID(askdata.HashBytes([]byte(
		"question-stage-fact-v1\x00" + string(run.ID) + "\x00" + string(kind) + "\x00" + string(identity),
	)))
	fact, err := cognition.NewPromptFact(evidenceID, kind, payload)
	if err != nil {
		return GovernedFact{}, fmt.Errorf("%w: cross-stage fact payload is invalid", ErrInvalidRun)
	}
	return GovernedFact{
		Fact: fact,
		Evidence: askdata.EvidenceRef{
			EvidenceID: evidenceID, Kind: evidenceKind,
			SourceID: sourceID, ContentHash: fact.ContentHash,
		},
	}, nil
}

func toolFactKind(tool toolhost.ToolName) (cognition.FactKind, askdata.EvidenceKind, bool) {
	switch tool {
	case toolhost.ToolSearchSemanticObjects:
		return cognition.FactCandidateSet, askdata.EvidenceKindCandidateSet, true
	case toolhost.ToolGetSemanticContracts:
		return cognition.FactSemanticContract, askdata.EvidenceKindSemanticContract, true
	case toolhost.ToolLookupDimensionValues:
		return cognition.FactDimensionProfile, askdata.EvidenceKindDimensionProfile, true
	case toolhost.ToolGetCertifiedExamples:
		return cognition.FactCertifiedExample, askdata.EvidenceKindCertifiedExample, true
	case toolhost.ToolResolveGraphPlan:
		return cognition.FactGraphEvidence, askdata.EvidenceKindGraphPath, true
	case toolhost.ToolValidateSemanticBundle:
		return cognition.FactBindingEvidence, askdata.EvidenceKindPolicy, true
	case toolhost.ToolGetDataQualityStatus, toolhost.ToolProbeJoinCardinality,
		toolhost.ToolExecuteValidationQuery, toolhost.ToolCompareCandidateResults:
		return cognition.FactQualityEvidence, askdata.EvidenceKindDataQuality, true
	case toolhost.ToolCompileSemanticQuery, toolhost.ToolValidateQueryPlan:
		return cognition.FactPlanEvidence, askdata.EvidenceKindQueryPlan, true
	case toolhost.ToolExecuteQueryPlan:
		return cognition.FactQueryResultSummary, askdata.EvidenceKindQueryResult, true
	default:
		return "", "", false
	}
}

func decisionFactPayload(
	action cognition.Action,
) (json.RawMessage, cognition.FactKind, askdata.EvidenceKind, bool, error) {
	var value any
	var kind cognition.FactKind
	var evidenceKind askdata.EvidenceKind
	switch action.Action {
	case cognition.ActionProposeUnderstanding:
		value, kind, evidenceKind = action.Understanding, cognition.FactRuleParse, askdata.EvidenceKindRule
	case cognition.ActionProposeBinding:
		// Cross-stage facts are new, governed evidence in their own right. Do not
		// embed the previous stage's evidenceRefs in their prompt payload: models
		// otherwise tend to cite those nested IDs even though the current stage is
		// intentionally allowed to cite only the outer fact evidenceId. The full
		// proposal, including its provenance, remains in the immutable run audit.
		value, kind, evidenceKind = bindingFactPayload(*action.BindingProposal), cognition.FactBindingEvidence, askdata.EvidenceKindRule
	case cognition.ActionProposePlan:
		value, kind, evidenceKind = planFactPayload(*action.PlanProposal), cognition.FactPlanEvidence, askdata.EvidenceKindQueryPlan
	case cognition.ActionAnalyzeAnomaly:
		value, kind, evidenceKind = anomalyFactPayload(*action.AnomalyAnalysis), cognition.FactQualityEvidence, askdata.EvidenceKindDataQuality
	case cognition.ActionVerifyResult:
		value, kind, evidenceKind = verificationFactPayload(*action.Verification), cognition.FactQualityEvidence, askdata.EvidenceKindDataQuality
	default:
		return nil, "", "", false, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, "", "", false, err
	}
	return payload, kind, evidenceKind, true, nil
}

type carriedConfidence struct {
	Score       float64  `json:"score"`
	Margin      float64  `json:"margin"`
	ReasonCodes []string `json:"reasonCodes"`
}

type carriedBindingFact struct {
	ModelVersionID    askdata.ID                   `json:"modelVersionId"`
	MetricBindings    []cognition.MetricBinding    `json:"metricBindings"`
	DimensionBindings []cognition.DimensionBinding `json:"dimensionBindings"`
	MemberBindings    []cognition.MemberBinding    `json:"memberBindings"`
	Confidence        carriedConfidence            `json:"confidence"`
}

func bindingFactPayload(proposal cognition.BindingProposal) carriedBindingFact {
	return carriedBindingFact{
		ModelVersionID:    proposal.ModelVersionID,
		MetricBindings:    append([]cognition.MetricBinding(nil), proposal.MetricBindings...),
		DimensionBindings: append([]cognition.DimensionBinding(nil), proposal.DimensionBindings...),
		MemberBindings:    append([]cognition.MemberBinding(nil), proposal.MemberBindings...),
		Confidence: carriedConfidence{
			Score: proposal.Confidence.Score, Margin: proposal.Confidence.Margin,
			ReasonCodes: append([]string(nil), proposal.Confidence.ReasonCodes...),
		},
	}
}

type carriedPlanFact struct {
	SemanticIR any               `json:"semanticIr"`
	Confidence carriedConfidence `json:"confidence"`
}

func planFactPayload(proposal cognition.PlanProposal) carriedPlanFact {
	return carriedPlanFact{
		SemanticIR: proposal.SemanticIR,
		Confidence: carriedConfidence{
			Score: proposal.Confidence.Score, Margin: proposal.Confidence.Margin,
			ReasonCodes: append([]string(nil), proposal.Confidence.ReasonCodes...),
		},
	}
}

type carriedAnomalyFact struct {
	Category          cognition.AnomalyCategory   `json:"category"`
	Summary           string                      `json:"summary"`
	RecommendedAction cognition.RecommendedAction `json:"recommendedAction"`
}

func anomalyFactPayload(analysis cognition.AnomalyAnalysis) carriedAnomalyFact {
	return carriedAnomalyFact{
		Category: analysis.Category, Summary: analysis.Summary,
		RecommendedAction: analysis.RecommendedAction,
	}
}

type carriedVerificationCheck struct {
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
}

type carriedVerificationFact struct {
	Verdict cognition.VerificationVerdict `json:"verdict"`
	Summary string                        `json:"summary"`
	Checks  []carriedVerificationCheck    `json:"checks"`
}

func verificationFactPayload(verification cognition.Verification) carriedVerificationFact {
	checks := make([]carriedVerificationCheck, len(verification.Checks))
	for index, check := range verification.Checks {
		checks[index] = carriedVerificationCheck{Code: check.Code, Passed: check.Passed}
	}
	return carriedVerificationFact{
		Verdict: verification.Verdict, Summary: verification.Summary, Checks: checks,
	}
}
