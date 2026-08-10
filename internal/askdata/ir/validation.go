package ir

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

type buildSelection struct {
	Bundle binding.Bundle
	Model  askdata.ID
}

func validateBuildRequest(request BuildRequest) (buildSelection, error) {
	if err := request.BundleHash.Validate(); err != nil {
		return buildSelection{}, fmt.Errorf("%w: bundleHash: %v", ErrInvalidBuildRequest, err)
	}
	if err := request.BindingResult.ValidateAgainst(request.BindingRequest); err != nil {
		return buildSelection{}, fmt.Errorf("%w: binding replay: %v", ErrInvalidBuildRequest, err)
	}
	var selected *binding.Bundle
	for index := range request.BindingResult.Bundles {
		if request.BindingResult.Bundles[index].BundleHash != request.BundleHash {
			continue
		}
		if selected != nil {
			return buildSelection{}, fmt.Errorf("%w: duplicate bundleHash", ErrInvalidBuildRequest)
		}
		copy := request.BindingResult.Bundles[index]
		selected = &copy
	}
	if selected == nil {
		return buildSelection{}, fmt.Errorf("%w: selected bundle is absent", ErrInvalidBuildRequest)
	}
	// Semantic IR v1 freezes a singular modelVersionId. Silently discarding a
	// certified join path would change query semantics, so multi-model bundles
	// remain blocked until a versioned IR contract adds model/path support.
	model, err := validateIRModelShape(*selected)
	if err != nil {
		return buildSelection{}, fmt.Errorf("%w: %v", ErrInvalidBuildRequest, err)
	}
	if err := validateBundleSemantics(*selected, request.BindingRequest.GraphResolution.Plan, model); err != nil {
		return buildSelection{}, fmt.Errorf("%w: %v", ErrInvalidBuildRequest, err)
	}
	if err := validateTimeResolution(request, *selected); err != nil {
		return buildSelection{}, fmt.Errorf("%w: %v", ErrInvalidBuildRequest, err)
	}
	return buildSelection{Bundle: *selected, Model: model}, nil
}

func validateIRModelShape(bundle binding.Bundle) (askdata.ID, error) {
	if len(bundle.ModelVersionIDs) != 1 || bundle.GraphPath != nil {
		return "", errors.New("Semantic IR v1 cannot represent a multi-model bundle")
	}
	return bundle.ModelVersionIDs[0], nil
}

func validateBundleSemantics(bundle binding.Bundle, plan graph.GraphPlan, model askdata.ID) error {
	if err := model.Validate(); err != nil {
		return fmt.Errorf("modelVersionId: %w", err)
	}
	for index, metric := range bundle.MetricBindings {
		if metric.ModelVersionID != model || !metricCompatible(plan, metric.MetricVersionID, model) {
			return fmt.Errorf("metricBindings[%d] is not compatible with the selected model", index)
		}
	}
	if len(bundle.MetricBindings) == 0 {
		return errors.New("at least one metric binding is required")
	}
	filterDimensions := map[askdata.ID]struct{}{}
	for index, dimension := range bundle.DimensionBindings {
		if !dimensionCompatibleWithModel(plan, dimension.DimensionVersionID, model) {
			return fmt.Errorf("dimensionBindings[%d] is not compatible with the selected model", index)
		}
		if dimension.Role == understanding.DimensionRoleFilter {
			filterDimensions[dimension.DimensionVersionID] = struct{}{}
		}
	}
	for index, member := range bundle.MemberBindings {
		if _, exists := filterDimensions[member.DimensionVersionID]; !exists {
			return fmt.Errorf("memberBindings[%d] has no FILTER dimension", index)
		}
		if !activeMemberOwnedBy(plan, member.MemberVersionID, member.DimensionVersionID) {
			return fmt.Errorf("memberBindings[%d] ownership is not ACTIVE", index)
		}
	}
	return nil
}

func validateTimeResolution(request BuildRequest, bundle binding.Bundle) error {
	if bundle.Time == nil {
		if request.InheritedTimeResolution != nil {
			return errors.New("inherited time proof was supplied without a time binding")
		}
		return nil
	}
	switch bundle.Time.Origin {
	case understanding.EvidenceOriginCurrent:
		if request.InheritedTimeResolution != nil {
			return errors.New("inherited time proof cannot replace current deterministic time")
		}
		resolved := request.BindingRequest.UnderstandingRequest.ContextRequest.Rules.Time
		if resolved == nil || !reflect.DeepEqual(resolved.Understanding(), bundle.Time.Value) {
			return errors.New("current time is not backed by the replayed deterministic rule result")
		}
	case understanding.EvidenceOriginInherited:
		proof := request.InheritedTimeResolution
		previous := request.BindingRequest.UnderstandingResult.Context.PreviousSnapshotHash
		inherited := request.BindingRequest.UnderstandingResult.Context.Inherited
		if proof == nil || previous == nil || inherited == nil {
			return errors.New("inherited time requires a prior snapshot resolution proof")
		}
		if proof.PreviousSnapshotHash != *previous || proof.Evidence.Kind != askdata.EvidenceKindRule ||
			proof.Evidence.SourceID != askdata.ID("context-snapshot:"+string(*previous)) {
			return errors.New("inherited time proof is bound to the wrong snapshot")
		}
		if err := proof.Resolved.Validate(inherited.Question); err != nil {
			return fmt.Errorf("inherited resolved time: %w", err)
		}
		if !reflect.DeepEqual(proof.Resolved.Understanding(), bundle.Time.Value) {
			return errors.New("inherited time proof does not match the binding")
		}
		payload, err := inheritedTimePayload(proof.PreviousSnapshotHash, proof.Resolved)
		if err != nil {
			return err
		}
		expectedHash := askdata.HashBytes(payload)
		if proof.ResolutionHash != expectedHash || proof.Evidence.ContentHash != expectedHash ||
			proof.Evidence.EvidenceID != askdata.ID("resolved-time:"+string(expectedHash)) ||
			proof.Evidence.Validate() != nil {
			return errors.New("inherited time proof hash or evidence is invalid")
		}
	default:
		return errors.New("time binding origin is invalid")
	}
	return nil
}

func buildSemanticIR(request BuildRequest, selection buildSelection) (SemanticIR, error) {
	bundle := selection.Bundle
	metrics := buildMetrics(bundle.MetricBindings)
	groupBy, filterDimensions, timeDimension, sortDimensions, err := buildDimensions(bundle.DimensionBindings)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: dimensions: %v", ErrInvalidBuildRequest, err)
	}
	filters, err := buildFilters(bundle.MemberBindings, filterDimensions)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: filters: %v", ErrInvalidBuildRequest, err)
	}
	timeRange, err := buildTimeRange(request, bundle, timeDimension)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: timeRange: %v", ErrInvalidBuildRequest, err)
	}
	comparison, err := buildComparison(request, timeRange)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: comparison: %v", ErrInvalidBuildRequest, err)
	}
	sorts, err := buildSort(request, bundle, sortDimensions, comparison)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: sort: %v", ErrInvalidBuildRequest, err)
	}
	limit, err := effectiveLimit(request.BindingRequest.UnderstandingResult)
	if err != nil {
		return SemanticIR{}, fmt.Errorf("%w: limit: %v", ErrInvalidBuildRequest, err)
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	return SemanticIR{
		IRVersion: Version, SemanticReleaseID: request.BindingResult.Scope.Release.ReleaseID,
		SemanticContentHash: request.BindingResult.Scope.Release.ContentHash,
		DomainID:            request.BindingResult.DomainID,
		ModelVersionID:      selection.Model, Metrics: metrics, GroupBy: groupBy, Filters: filters,
		TimeRange: timeRange, Comparison: comparison, Sort: sorts, Limit: limit,
		OtherPolicy: OtherNone, TieBreaking: TieIncludeAll,
	}, nil
}

func buildMetrics(values []binding.MetricBinding) []Metric {
	ids := map[askdata.ID]struct{}{}
	for _, value := range values {
		ids[value.MetricVersionID] = struct{}{}
	}
	result := make([]Metric, 0, len(ids))
	for id := range ids {
		hash := askdata.HashBytes([]byte(id))
		result = append(result, Metric{MetricVersionID: id, Alias: "metric_" + string(hash[:56])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].MetricVersionID < result[j].MetricVersionID })
	return result
}

func buildDimensions(values []binding.DimensionBinding) (
	[]GroupBy,
	map[askdata.ID]struct{},
	*askdata.ID,
	[]binding.DimensionBinding,
	error,
) {
	groups := map[askdata.ID]GroupBy{}
	filters := map[askdata.ID]struct{}{}
	var timeDimension *askdata.ID
	var sorts []binding.DimensionBinding
	for _, value := range values {
		switch value.Role {
		case understanding.DimensionRoleGroupBy:
			grain, err := convertGrain(value.Grain)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			group := GroupBy{DimensionVersionID: value.DimensionVersionID, Grain: grain}
			if previous, exists := groups[value.DimensionVersionID]; exists && !reflect.DeepEqual(previous, group) {
				return nil, nil, nil, nil, fmt.Errorf("dimension %s has conflicting grains", value.DimensionVersionID)
			}
			groups[value.DimensionVersionID] = group
		case understanding.DimensionRoleFilter:
			filters[value.DimensionVersionID] = struct{}{}
		case understanding.DimensionRoleTime:
			if timeDimension != nil && *timeDimension != value.DimensionVersionID {
				return nil, nil, nil, nil, errors.New("multiple time dimensions are not representable")
			}
			copy := value.DimensionVersionID
			timeDimension = &copy
		case understanding.DimensionRoleSort:
			if value.Mention == nil {
				return nil, nil, nil, nil, errors.New("an implied SORT dimension is invalid")
			}
			// Semantic IR v1 permits a dimension sort only when that dimension
			// is projected in groupBy. A SORT mention therefore implies the
			// same stable dimension at its natural grain; an explicit GROUP_BY
			// binding for the dimension, if present, retains its chosen grain.
			if _, exists := groups[value.DimensionVersionID]; !exists {
				groups[value.DimensionVersionID] = GroupBy{DimensionVersionID: value.DimensionVersionID}
			}
			sorts = append(sorts, value)
		default:
			return nil, nil, nil, nil, fmt.Errorf("unsupported dimension role %q", value.Role)
		}
	}
	result := make([]GroupBy, 0, len(groups))
	for _, value := range groups {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DimensionVersionID < result[j].DimensionVersionID })
	return result, filters, timeDimension, sorts, nil
}

type filterFamily struct {
	dimension askdata.ID
	negative  bool
	forceSet  bool
	members   map[askdata.ID]struct{}
}

func buildFilters(values []binding.MemberBinding, dimensions map[askdata.ID]struct{}) ([]Filter, error) {
	families := map[string]*filterFamily{}
	usedDimensions := map[askdata.ID]struct{}{}
	for _, value := range values {
		negative, forceSet, err := operatorFamily(value.OperatorHint)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s\x00%t", value.DimensionVersionID, negative)
		family := families[key]
		if family == nil {
			family = &filterFamily{dimension: value.DimensionVersionID, negative: negative, members: map[askdata.ID]struct{}{}}
			families[key] = family
		}
		family.forceSet = family.forceSet || forceSet
		family.members[value.MemberVersionID] = struct{}{}
		usedDimensions[value.DimensionVersionID] = struct{}{}
	}
	for dimension := range dimensions {
		if _, exists := usedDimensions[dimension]; !exists {
			return nil, fmt.Errorf("FILTER dimension %s has no bound member", dimension)
		}
	}
	result := make([]Filter, 0, len(families))
	for _, family := range families {
		members := make([]askdata.ID, 0, len(family.members))
		for member := range family.members {
			members = append(members, member)
		}
		sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
		operator := FilterEquals
		if family.negative {
			operator = FilterNotEquals
		}
		if family.forceSet || len(members) > 1 {
			operator = FilterIn
			if family.negative {
				operator = FilterNotIn
			}
		}
		result = append(result, Filter{DimensionVersionID: family.dimension, Operator: operator, MemberVersionIDs: members})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DimensionVersionID != result[j].DimensionVersionID {
			return result[i].DimensionVersionID < result[j].DimensionVersionID
		}
		return result[i].Operator < result[j].Operator
	})
	return result, nil
}

func buildTimeRange(
	request BuildRequest,
	bundle binding.Bundle,
	dimension *askdata.ID,
) (*TimeRange, error) {
	if bundle.Time == nil {
		if dimension != nil {
			return nil, errors.New("TIME dimension requires a resolved time binding")
		}
		return nil, nil
	}
	if dimension == nil {
		return nil, errors.New("resolved time requires exactly one TIME dimension")
	}
	var resolved understanding.ResolvedTime
	if bundle.Time.Origin == understanding.EvidenceOriginCurrent {
		resolved = *request.BindingRequest.UnderstandingRequest.ContextRequest.Rules.Time
	} else {
		resolved = request.InheritedTimeResolution.Resolved
	}
	return &TimeRange{
		DimensionVersionID: *dimension, Start: resolved.Start,
		EndExclusive: resolved.EndExclusive, Timezone: resolved.Timezone,
		RequestedPeriod: string(resolved.Expression), Grain: TimeGrain(resolved.Grain),
	}, nil
}

func buildComparison(request BuildRequest, timeRange *TimeRange) (*Comparison, error) {
	values, err := effectiveComparisons(request.BindingRequest.UnderstandingResult)
	if err != nil || len(values) == 0 {
		return nil, err
	}
	if len(values) != 1 || timeRange == nil {
		return nil, errors.New("exactly one comparison and a time range are required")
	}
	var comparisonType ComparisonType
	switch values[0].Type {
	case understanding.ComparisonYearOverYear:
		comparisonType = ComparisonYearOverYear
	case understanding.ComparisonMonthOverMonth:
		comparisonType = ComparisonMonthOverMonth
	case understanding.ComparisonPeriodOverPeriod:
		comparisonType = ComparisonPeriodOverPeriod
	default:
		return nil, fmt.Errorf("comparison %q is not representable in Semantic IR v1", values[0].Type)
	}
	return &Comparison{Type: comparisonType, Periods: 1}, nil
}

func convertGrain(value *understanding.TimeGrain) (*TimeGrain, error) {
	if value == nil {
		return nil, nil
	}
	converted := TimeGrain(*value)
	if !validBuildTimeGrain(converted) {
		return nil, fmt.Errorf("unsupported time grain %q", *value)
	}
	return &converted, nil
}

func validBuildTimeGrain(value TimeGrain) bool {
	switch value {
	case TimeGrainDay, TimeGrainWeek, TimeGrainMonth, TimeGrainQuarter, TimeGrainYear:
		return true
	default:
		return false
	}
}

func operatorFamily(value understanding.ValueOperatorHint) (negative, forceSet bool, err error) {
	switch value {
	case understanding.ValueOperatorDefault, understanding.ValueOperatorEquals:
		return false, false, nil
	case understanding.ValueOperatorIn:
		return false, true, nil
	case understanding.ValueOperatorNotEquals:
		return true, false, nil
	case understanding.ValueOperatorNotIn:
		return true, true, nil
	default:
		return false, false, fmt.Errorf("unsupported member operator %q", value)
	}
}

type sortCandidate struct {
	origin     understanding.EvidenceOrigin
	targetType SortTargetType
	versionID  askdata.ID
	text       string
}

func buildSort(
	request BuildRequest,
	bundle binding.Bundle,
	sortDimensions []binding.DimensionBinding,
	comparison *Comparison,
) ([]Sort, error) {
	orderings, origin, err := effectiveOrderings(request.BindingRequest.UnderstandingResult)
	if err != nil {
		return nil, err
	}
	if len(orderings) == 0 {
		if len(sortDimensions) != 0 {
			return nil, errors.New("SORT dimension has no ordering intent")
		}
		return []Sort{}, nil
	}
	source := request.BindingRequest.UnderstandingResult.Current
	if origin == understanding.EvidenceOriginInherited {
		source = *request.BindingRequest.UnderstandingResult.Context.Inherited
	}
	candidates := make([]sortCandidate, 0, len(bundle.MetricBindings)+len(sortDimensions))
	for _, metric := range bundle.MetricBindings {
		mention, ok := sourceMetricMention(source, metric.Mention, origin)
		if ok {
			candidates = append(candidates, sortCandidate{origin, SortTargetMetric, metric.MetricVersionID, mention.Text})
		}
	}
	for _, dimension := range sortDimensions {
		mention, ok := sourceDimensionMention(source, *dimension.Mention, origin)
		if ok {
			candidates = append(candidates, sortCandidate{origin, SortTargetDimension, dimension.DimensionVersionID, mention.Text})
		}
	}
	result := make([]Sort, 0, len(orderings))
	seen := map[string]struct{}{}
	for _, ordering := range orderings {
		candidate, err := chooseSortCandidate(ordering.TargetText, candidates)
		if err != nil {
			return nil, err
		}
		key := string(candidate.targetType) + "\x00" + string(candidate.versionID)
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("ordering repeats the same target")
		}
		seen[key] = struct{}{}
		direction := SortDirection(ordering.Direction)
		rankBy := RankBy(ordering.RankBy)
		if rankBy == "" && comparison == nil {
			rankBy = RankByCurrentValue
		}
		result = append(result, Sort{
			TargetType: candidate.targetType, TargetVersionID: candidate.versionID,
			Direction: direction, Nulls: NullsLast, RankBy: rankBy,
		})
	}
	return result, nil
}

func chooseSortCandidate(target string, candidates []sortCandidate) (sortCandidate, error) {
	normalizedTarget := normalizeTargetText(target)
	matches := make([]sortCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if normalizeTargetText(candidate.text) == normalizedTarget {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return sortCandidate{}, fmt.Errorf("ordering target %q is ambiguous", target)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return sortCandidate{}, fmt.Errorf("ordering target %q is not uniquely bound", target)
}

func sourceMetricMention(
	value understanding.QuestionUnderstanding,
	ref binding.MentionRef,
	origin understanding.EvidenceOrigin,
) (understanding.MetricMention, bool) {
	if ref.Origin != origin || ref.Kind != binding.MentionMetric || ref.Index < 0 || ref.Index >= len(value.MetricMentions) {
		return understanding.MetricMention{}, false
	}
	return value.MetricMentions[ref.Index], true
}

func sourceDimensionMention(
	value understanding.QuestionUnderstanding,
	ref binding.MentionRef,
	origin understanding.EvidenceOrigin,
) (understanding.DimensionMention, bool) {
	if ref.Origin != origin || ref.Kind != binding.MentionDimension || ref.Index < 0 || ref.Index >= len(value.DimensionMentions) {
		return understanding.DimensionMention{}, false
	}
	return value.DimensionMentions[ref.Index], true
}

func effectiveComparisons(result understanding.UnderstandingResult) ([]understanding.ComparisonMention, error) {
	current := result.Current.Comparisons
	var inherited []understanding.ComparisonMention
	if result.Context.Inherited != nil {
		inherited = result.Context.Inherited.Comparisons
	}
	if len(current) != 0 && len(inherited) != 0 {
		return nil, errors.New("current and inherited comparisons both survived precedence")
	}
	if len(current) != 0 {
		return current, nil
	}
	return inherited, nil
}

func effectiveOrderings(result understanding.UnderstandingResult) (
	[]understanding.OrderingMention,
	understanding.EvidenceOrigin,
	error,
) {
	current := result.Current.Ordering
	var inherited []understanding.OrderingMention
	if result.Context.Inherited != nil {
		inherited = result.Context.Inherited.Ordering
	}
	if len(current) != 0 && len(inherited) != 0 {
		return nil, "", errors.New("current and inherited ordering both survived precedence")
	}
	if len(current) != 0 {
		return current, understanding.EvidenceOriginCurrent, nil
	}
	return inherited, understanding.EvidenceOriginInherited, nil
}

func effectiveLimit(result understanding.UnderstandingResult) (int, error) {
	current := result.Current.Limit
	var inherited *int
	if result.Context.Inherited != nil {
		inherited = result.Context.Inherited.Limit
	}
	if current != nil && inherited != nil {
		return 0, errors.New("current and inherited limits both survived precedence")
	}
	if current != nil {
		return *current, nil
	}
	if inherited != nil {
		return *inherited, nil
	}
	return 0, nil
}

func normalizeTargetText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), ""))
}

func metricCompatible(plan graph.GraphPlan, metric, model askdata.ID) bool {
	for _, value := range plan.MetricModels {
		if value.MetricVersionID == metric && value.ModelVersionID == model {
			return true
		}
	}
	return false
}

func dimensionCompatibleWithModel(plan graph.GraphPlan, dimension, model askdata.ID) bool {
	for _, value := range plan.CompatibleDimensions {
		if value.DimensionVersionID == dimension && value.ModelVersionID == model {
			return true
		}
	}
	return false
}

func activeMemberOwnedBy(plan graph.GraphPlan, member, dimension askdata.ID) bool {
	for _, value := range plan.MemberOwnerships {
		if value.MemberVersionID == member && value.DimensionVersionID == dimension && value.Status == graph.MemberStatusActive {
			return true
		}
	}
	return false
}
