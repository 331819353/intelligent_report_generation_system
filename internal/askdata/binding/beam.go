package binding

import (
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

type selectedCandidate struct {
	mention MentionRef
	set     MentionCandidateSet
	option  CandidateOption
}

type beamState struct {
	metrics    []MetricBinding
	dimensions []DimensionBinding
	members    []MemberBinding
	selections []selectedCandidate
	models     map[askdata.ID]struct{}
	identity   string
}

func bindNormalized(request Request, state requestState) (Result, error) {
	beams := []beamState{{models: map[askdata.ID]struct{}{}}}
	for _, mention := range orderedMentions(state.mentions) {
		set := state.sets[mention]
		value := state.mentions[mention]
		expanded := make([]beamState, 0, len(beams)*len(set.Candidates))
		for _, beam := range beams {
			for _, option := range set.Candidates {
				if option.Gate.Verdict == search.GateBlock {
					continue
				}
				next := expandMention(request, state, beam, set, value, option)
				expanded = append(expanded, next...)
			}
		}
		beams = pruneBeams(expanded, request.Config.BeamWidth)
		if len(beams) == 0 {
			break
		}
	}

	bundles := make([]Bundle, 0, len(beams))
	for _, beam := range beams {
		paths := compatiblePaths(request.GraphResolution.Plan, beam.models)
		for _, path := range paths {
			bundle, err := bundleFromBeam(request, state, beam, path)
			if err != nil {
				return Result{}, err
			}
			bundles = append(bundles, bundle)
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].Score.Total != bundles[j].Score.Total {
			return bundles[i].Score.Total > bundles[j].Score.Total
		}
		return bundles[i].BundleHash < bundles[j].BundleHash
	})
	bundles = deduplicateBundles(bundles)
	if len(bundles) > request.Config.TopBundles {
		bundles = bundles[:request.Config.TopBundles]
	}
	result := Result{
		Version: BindingResultVersion, Scope: request.GraphRequest.Scope,
		DomainID:          request.GraphRequest.DomainID,
		UnderstandingHash: request.UnderstandingResult.ResultHash,
		GraphPlanHash:     request.GraphResolution.Plan.PlanHash,
		Bundles:           bundles, BlockedCandidates: state.blocked, NoMatch: len(bundles) == 0,
	}
	return finalizeResult(result)
}

func orderedMentions(values map[MentionRef]mentionValue) []MentionRef {
	result := make([]MentionRef, 0, len(values))
	for mention := range values {
		result = append(result, mention)
	}
	kindOrder := map[MentionKind]int{MentionMetric: 0, MentionDimension: 1, MentionMember: 2}
	sort.Slice(result, func(i, j int) bool {
		if kindOrder[result[i].Kind] != kindOrder[result[j].Kind] {
			return kindOrder[result[i].Kind] < kindOrder[result[j].Kind]
		}
		if result[i].Origin != result[j].Origin {
			return result[i].Origin < result[j].Origin
		}
		return result[i].Index < result[j].Index
	})
	return result
}

func expandMention(
	request Request,
	state requestState,
	beam beamState,
	set MentionCandidateSet,
	value mentionValue,
	option CandidateOption,
) []beamState {
	graphPlan := request.GraphResolution.Plan
	evidence := candidateEvidence(set, option, state.graphEvidence)
	selection := selectedCandidate{mention: set.Mention, set: set, option: option}
	switch set.Mention.Kind {
	case MentionMetric:
		result := []beamState{}
		for _, binding := range graphPlan.MetricModels {
			if binding.MetricVersionID != option.Candidate.ObjectVersionID {
				continue
			}
			next := cloneBeam(beam)
			next.metrics = append(next.metrics, MetricBinding{
				Mention: set.Mention, MetricVersionID: binding.MetricVersionID,
				ModelVersionID:  binding.ModelVersionID,
				AggregationHint: value.metric.AggregationHint, EvidenceRefs: evidence,
			})
			next.selections = append(next.selections, selection)
			next.models[binding.ModelVersionID] = struct{}{}
			if len(next.models) > 1 && len(compatiblePaths(graphPlan, next.models)) == 0 {
				continue
			}
			next.identity += fmt.Sprintf("|%s=%s@%s", mentionIdentity(set.Mention), binding.MetricVersionID, binding.ModelVersionID)
			result = append(result, next)
		}
		return result
	case MentionDimension:
		if !dimensionCompatible(graphPlan, option.Candidate.ObjectVersionID, beam.models) {
			return nil
		}
		next := cloneBeam(beam)
		mention := set.Mention
		next.dimensions = append(next.dimensions, DimensionBinding{
			Mention: &mention, DimensionVersionID: option.Candidate.ObjectVersionID,
			Role: value.dimension.Role, Grain: cloneGrain(value.dimension.Grain), EvidenceRefs: evidence,
		})
		next.selections = append(next.selections, selection)
		next.identity += fmt.Sprintf("|%s=%s", mentionIdentity(set.Mention), option.Candidate.ObjectVersionID)
		return []beamState{next}
	case MentionMember:
		parent := *option.ParentDimensionVersionID
		if !memberOwnedBy(graphPlan, option.Candidate.ObjectVersionID, parent) ||
			!dimensionCompatible(graphPlan, parent, beam.models) {
			return nil
		}
		next := cloneBeam(beam)
		next.members = append(next.members, MemberBinding{
			Mention: set.Mention, MemberVersionID: option.Candidate.ObjectVersionID,
			DimensionVersionID: parent, OperatorHint: value.member.OperatorHint,
			EvidenceRefs: evidence,
		})
		if !hasFilterDimension(next.dimensions, parent) {
			next.dimensions = append(next.dimensions, DimensionBinding{
				DimensionVersionID: parent, Role: understanding.DimensionRoleFilter,
				EvidenceRefs: evidence,
			})
		}
		next.selections = append(next.selections, selection)
		next.identity += fmt.Sprintf("|%s=%s@%s", mentionIdentity(set.Mention), option.Candidate.ObjectVersionID, parent)
		return []beamState{next}
	default:
		return nil
	}
}

func cloneBeam(value beamState) beamState {
	result := beamState{
		metrics:    append([]MetricBinding(nil), value.metrics...),
		dimensions: append([]DimensionBinding(nil), value.dimensions...),
		members:    append([]MemberBinding(nil), value.members...),
		selections: append([]selectedCandidate(nil), value.selections...),
		models:     make(map[askdata.ID]struct{}, len(value.models)), identity: value.identity,
	}
	for model := range value.models {
		result.models[model] = struct{}{}
	}
	return result
}

func pruneBeams(values []beamState, limit int) []beamState {
	sort.Slice(values, func(i, j int) bool {
		left, right := partialSelectionScore(values[i].selections), partialSelectionScore(values[j].selections)
		if left != right {
			return left > right
		}
		return values[i].identity < values[j].identity
	})
	result := make([]beamState, 0, minInt(len(values), limit))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, duplicate := seen[value.identity]; duplicate {
			continue
		}
		seen[value.identity] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

// A multi-model bundle must be justified by one allowed simple path whose
// vertices cover every selected metric model. The path may include an
// authorized intermediate model from the GraphPlan.
func compatiblePaths(plan graph.GraphPlan, selected map[askdata.ID]struct{}) []*graph.JoinPath {
	if len(selected) == 0 {
		return nil
	}
	if len(selected) == 1 {
		return []*graph.JoinPath{nil}
	}
	result := []*graph.JoinPath{}
	for index := range plan.JoinPaths {
		path := plan.JoinPaths[index]
		if !path.Allowed || !pathCoversModels(path, selected) {
			continue
		}
		copy := path
		copy.Steps = append([]graph.JoinStep(nil), path.Steps...)
		copy.RiskCodes = append([]graph.JoinRiskCode(nil), path.RiskCodes...)
		result = append(result, &copy)
	}
	return result
}

func pathCoversModels(path graph.JoinPath, selected map[askdata.ID]struct{}) bool {
	covered := map[askdata.ID]struct{}{}
	for _, step := range path.Steps {
		covered[step.FromModelVersionID] = struct{}{}
		covered[step.ToModelVersionID] = struct{}{}
	}
	for model := range selected {
		if _, exists := covered[model]; !exists {
			return false
		}
	}
	return true
}

func bundleFromBeam(
	request Request,
	state requestState,
	beam beamState,
	path *graph.JoinPath,
) (Bundle, error) {
	models := make([]askdata.ID, 0, len(beam.models)+graph.MaxJoinHops)
	for model := range beam.models {
		models = append(models, model)
	}
	if path != nil {
		for _, step := range path.Steps {
			models = append(models, step.FromModelVersionID, step.ToModelVersionID)
		}
	}
	models = normalizeIDs(models)
	metrics := append([]MetricBinding(nil), beam.metrics...)
	dimensions := append([]DimensionBinding(nil), beam.dimensions...)
	members := append([]MemberBinding(nil), beam.members...)
	normalizeBindings(metrics, dimensions, members)
	evidence := []askdata.EvidenceRef{state.graphEvidence}
	for _, metric := range metrics {
		evidence = append(evidence, metric.EvidenceRefs...)
	}
	for _, dimension := range dimensions {
		evidence = append(evidence, dimension.EvidenceRefs...)
	}
	for _, member := range members {
		evidence = append(evidence, member.EvidenceRefs...)
	}
	bundle := Bundle{
		MetricBindings: metrics, DimensionBindings: dimensions, MemberBindings: members,
		Time: cloneTimeBinding(state.time), ModelVersionIDs: models, GraphPath: path,
		GraphSource: request.GraphResolution.Source, GraphDegraded: request.GraphResolution.Degraded,
		GraphDegradationReason: request.GraphResolution.DegradationReason,
		Score:                  scoreSelections(beam.selections, path), EvidenceRefs: normalizeEvidenceRefs(evidence),
	}
	return finalizeBundle(bundle)
}

func candidateEvidence(
	set MentionCandidateSet,
	option CandidateOption,
	graphEvidence askdata.EvidenceRef,
) []askdata.EvidenceRef {
	values := []askdata.EvidenceRef{set.Evidence, graphEvidence}
	for _, source := range option.Candidate.Evidence {
		values = append(values, source.Evidence)
	}
	values = append(values, option.Gate.EvidenceRefs...)
	values = append(values, option.FeatureEvidenceRefs...)
	return normalizeEvidenceRefs(values)
}

func dimensionCompatible(plan graph.GraphPlan, dimension askdata.ID, models map[askdata.ID]struct{}) bool {
	for _, value := range plan.CompatibleDimensions {
		if value.DimensionVersionID == dimension {
			if _, selected := models[value.ModelVersionID]; selected {
				return true
			}
		}
	}
	return false
}

func memberOwnedBy(plan graph.GraphPlan, member, dimension askdata.ID) bool {
	for _, value := range plan.MemberOwnerships {
		if value.MemberVersionID == member && value.DimensionVersionID == dimension && value.Status == graph.MemberStatusActive {
			return true
		}
	}
	return false
}

func hasFilterDimension(values []DimensionBinding, dimension askdata.ID) bool {
	for _, value := range values {
		if value.DimensionVersionID == dimension && value.Role == understanding.DimensionRoleFilter {
			return true
		}
	}
	return false
}

func normalizeBindings(metrics []MetricBinding, dimensions []DimensionBinding, members []MemberBinding) {
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].Mention != metrics[j].Mention {
			return mentionRefLess(metrics[i].Mention, metrics[j].Mention)
		}
		if metrics[i].MetricVersionID != metrics[j].MetricVersionID {
			return metrics[i].MetricVersionID < metrics[j].MetricVersionID
		}
		return metrics[i].ModelVersionID < metrics[j].ModelVersionID
	})
	sort.Slice(dimensions, func(i, j int) bool {
		if dimensions[i].Role != dimensions[j].Role {
			return dimensions[i].Role < dimensions[j].Role
		}
		if dimensions[i].DimensionVersionID != dimensions[j].DimensionVersionID {
			return dimensions[i].DimensionVersionID < dimensions[j].DimensionVersionID
		}
		return dimensionMentionKey(dimensions[i].Mention) < dimensionMentionKey(dimensions[j].Mention)
	})
	sort.Slice(members, func(i, j int) bool {
		if members[i].Mention != members[j].Mention {
			return mentionRefLess(members[i].Mention, members[j].Mention)
		}
		return members[i].MemberVersionID < members[j].MemberVersionID
	})
}

func dimensionMentionKey(value *MentionRef) string {
	if value == nil {
		return ""
	}
	return mentionIdentity(*value)
}

func normalizeIDs(values []askdata.ID) []askdata.ID {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) == 0 {
		return []askdata.ID{}
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] != values[write-1] {
			values[write] = values[read]
			write++
		}
	}
	return values[:write]
}

func deduplicateBundles(values []Bundle) []Bundle {
	result := make([]Bundle, 0, len(values))
	seen := map[askdata.ContentHash]struct{}{}
	for _, value := range values {
		if _, duplicate := seen[value.BundleHash]; duplicate {
			continue
		}
		seen[value.BundleHash] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneGrain(value *understanding.TimeGrain) *understanding.TimeGrain {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTimeBinding(value *TimeBinding) *TimeBinding {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func mentionIdentity(value MentionRef) string {
	return strings.Join([]string{string(value.Origin), string(value.Kind), fmt.Sprint(value.Index)}, ":")
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
