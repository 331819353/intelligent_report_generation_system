package orchestrator

import (
	"encoding/json"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// deterministicBindingFromEvidence accepts only the narrow happy path: one
// certified metric, one certified model and, when a calendar range is present,
// one time dimension selected from governed retrieval and contract results.
// Ambiguous or unresolved questions remain model-driven.
func deterministicBindingFromEvidence(
	request LoopRequest,
	result LoopResult,
) (cognition.BindingProposal, []askdata.EvidenceRef, bool) {
	rules := rulesFromConversationFacts(request)
	for _, governed := range request.Facts {
		switch governed.Fact.Kind {
		case cognition.FactRuleParse:
			var proposal cognition.UnderstandingProposal
			if json.Unmarshal(governed.Fact.Payload, &proposal) == nil && len(proposal.UnresolvedSpans) > 0 {
				return cognition.BindingProposal{}, nil, false
			}
		}
	}

	scores := map[askdata.ID]float64{}
	var contracts []toolhost.SemanticContractSummary
	evidence := []askdata.EvidenceRef{}
	for _, execution := range result.ToolExecutions {
		switch execution.Response.Tool {
		case toolhost.ToolSearchSemanticObjects:
			var search toolhost.SearchSemanticObjectsResult
			if askdata.DecodeStrictJSON(execution.Response.Result, &search) != nil {
				continue
			}
			for _, candidate := range search.Candidates {
				if candidate.Score > scores[candidate.ObjectVersionID] {
					scores[candidate.ObjectVersionID] = candidate.Score
				}
			}
			evidence = appendUniqueEvidenceRefs(evidence, execution.Response.EvidenceRefs...)
		case toolhost.ToolGetSemanticContracts:
			var contractResult toolhost.GetSemanticContractsResult
			if askdata.DecodeStrictJSON(execution.Response.Result, &contractResult) != nil {
				continue
			}
			contracts = append(contracts, contractResult.Contracts...)
			evidence = appendUniqueEvidenceRefs(evidence, execution.Response.EvidenceRefs...)
		}
	}
	if len(scores) == 0 || len(contracts) == 0 || len(evidence) == 0 {
		return cognition.BindingProposal{}, nil, false
	}

	metric, metricOK := strongestContract(contracts, scores, toolhost.ObjectTypeMetric, nil)
	model, modelOK := strongestContract(contracts, scores, toolhost.ObjectTypeModel, nil)
	if !metricOK || !modelOK {
		return cognition.BindingProposal{}, nil, false
	}
	dimensions := []cognition.DimensionBinding{}
	if rules != nil && rules.Time != nil {
		timeDimension, ok := strongestContract(contracts, scores, toolhost.ObjectTypeDimension, func(contract toolhost.SemanticContractSummary) bool {
			name := strings.ToLower(contract.Name)
			return strings.TrimSpace(contract.Grain) != "" || strings.Contains(name, "日期") ||
				strings.Contains(name, "时间") || strings.Contains(name, "date") || strings.Contains(name, "time")
		})
		if !ok {
			return cognition.BindingProposal{}, nil, false
		}
		dimensions = append(dimensions, cognition.DimensionBinding{
			MentionIndex: 0, DimensionVersionID: timeDimension.ObjectVersionID, Role: cognition.BindingRoleTime,
		})
	}
	proposal := cognition.BindingProposal{
		ModelVersionID:    model.ObjectVersionID,
		MetricBindings:    []cognition.MetricBinding{{MentionIndex: 0, MetricVersionID: metric.ObjectVersionID}},
		DimensionBindings: dimensions, MemberBindings: []cognition.MemberBinding{},
		Confidence: askdata.ConfidenceEvidence{
			Score: 1, Margin: 1, Evidence: evidence,
			ReasonCodes: []string{"DETERMINISTIC_CERTIFIED_MATCH"},
		},
	}
	if proposal.Validate() != nil {
		return cognition.BindingProposal{}, nil, false
	}
	return proposal, evidence, true
}

func strongestContract(
	contracts []toolhost.SemanticContractSummary,
	scores map[askdata.ID]float64,
	objectType toolhost.ObjectType,
	accept func(toolhost.SemanticContractSummary) bool,
) (toolhost.SemanticContractSummary, bool) {
	var selected toolhost.SemanticContractSummary
	selectedScore := -1.0
	for _, contract := range contracts {
		if contract.ObjectType != objectType || contract.ObjectVersionID.Validate() != nil ||
			(accept != nil && !accept(contract)) {
			continue
		}
		score, exists := scores[contract.ObjectVersionID]
		if !exists {
			continue
		}
		if score > selectedScore || score == selectedScore && contract.ObjectVersionID < selected.ObjectVersionID {
			selected, selectedScore = contract, score
		}
	}
	return selected, selectedScore >= 0
}

func appendUniqueEvidenceRefs(values []askdata.EvidenceRef, additions ...askdata.EvidenceRef) []askdata.EvidenceRef {
	seen := make(map[askdata.ID]bool, len(values))
	for _, value := range values {
		seen[value.EvidenceID] = true
	}
	for _, value := range additions {
		if value.Validate() == nil && !seen[value.EvidenceID] && len(values) < askdata.MaxConfidenceProofs {
			seen[value.EvidenceID] = true
			values = append(values, value)
		}
	}
	return values
}

func deterministicContractCandidateIDs(
	request LoopRequest,
	execution toolhost.Execution,
) ([]askdata.ID, bool) {
	for _, governed := range request.Facts {
		if governed.Fact.Kind != cognition.FactRuleParse {
			continue
		}
		var proposal cognition.UnderstandingProposal
		if json.Unmarshal(governed.Fact.Payload, &proposal) == nil && len(proposal.UnresolvedSpans) > 0 {
			return nil, false
		}
	}
	var search toolhost.SearchSemanticObjectsResult
	if execution.Response.Tool != toolhost.ToolSearchSemanticObjects ||
		askdata.DecodeStrictJSON(execution.Response.Result, &search) != nil {
		return nil, false
	}
	hasMetric, hasModel, hasDimension := false, false, false
	ids := make([]askdata.ID, 0, len(search.Candidates))
	seen := map[askdata.ID]bool{}
	for _, candidate := range search.Candidates {
		switch candidate.ObjectType {
		case toolhost.ObjectTypeMetric:
			hasMetric = true
		case toolhost.ObjectTypeModel:
			hasModel = true
		case toolhost.ObjectTypeDimension:
			hasDimension = true
		default:
			continue
		}
		if candidate.ObjectVersionID.Validate() == nil && !seen[candidate.ObjectVersionID] {
			seen[candidate.ObjectVersionID] = true
			ids = append(ids, candidate.ObjectVersionID)
		}
	}
	rules := rulesFromConversationFacts(request)
	if !hasMetric || !hasModel || rules != nil && rules.Time != nil && !hasDimension || len(ids) == 0 {
		return nil, false
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, true
}
