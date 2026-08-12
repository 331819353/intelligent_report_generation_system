package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

// deterministicPlanProposal converts a previously validated semantic binding
// and the trusted calendar parse carried by the conversation fact into the
// closed semantic IR contract. Plan construction is a mechanical operation;
// keeping it out of the provider path prevents an otherwise valid question
// from being blocked when a model cannot reproduce a deeply nested JSON
// schema.
func deterministicPlanProposal(request LoopRequest) (cognition.PlanProposal, askdata.EvidenceRef, bool) {
	var binding carriedBindingFact
	var bindingEvidence askdata.EvidenceRef
	rules := rulesFromConversationFacts(request)
	for _, governed := range request.Facts {
		switch governed.Fact.Kind {
		case cognition.FactBindingEvidence:
			var candidate carriedBindingFact
			if json.Unmarshal(governed.Fact.Payload, &candidate) == nil && candidate.ModelVersionID.Validate() == nil && len(candidate.MetricBindings) > 0 {
				binding, bindingEvidence = candidate, governed.Evidence
			}
		}
	}
	if binding.ModelVersionID == "" || bindingEvidence.Validate() != nil {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}

	metrics := make([]ircontract.Metric, 0, len(binding.MetricBindings))
	seenMetrics := map[askdata.ID]bool{}
	for _, item := range binding.MetricBindings {
		if item.MetricVersionID.Validate() != nil || seenMetrics[item.MetricVersionID] {
			continue
		}
		seenMetrics[item.MetricVersionID] = true
		metrics = append(metrics, ircontract.Metric{
			MetricVersionID: item.MetricVersionID,
			Alias:           fmt.Sprintf("metric_%d", len(metrics)+1),
		})
	}
	if len(metrics) == 0 {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}

	groupBy := make([]ircontract.GroupBy, 0, len(binding.DimensionBindings))
	grouped := map[askdata.ID]bool{}
	var timeDimension askdata.ID
	filterDimensions := map[askdata.ID]bool{}
	for _, item := range binding.DimensionBindings {
		if item.DimensionVersionID.Validate() != nil {
			continue
		}
		switch item.Role {
		case cognition.BindingRoleGroupBy:
			if !grouped[item.DimensionVersionID] {
				grouped[item.DimensionVersionID] = true
				groupBy = append(groupBy, ircontract.GroupBy{DimensionVersionID: item.DimensionVersionID})
			}
		case cognition.BindingRoleTime:
			if timeDimension != "" && timeDimension != item.DimensionVersionID {
				return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
			}
			timeDimension = item.DimensionVersionID
		case cognition.BindingRoleFilter:
			filterDimensions[item.DimensionVersionID] = true
		case cognition.BindingRoleSort:
			// A dimension sort is executable only when the dimension is projected.
			if !grouped[item.DimensionVersionID] {
				grouped[item.DimensionVersionID] = true
				groupBy = append(groupBy, ircontract.GroupBy{DimensionVersionID: item.DimensionVersionID})
			}
		}
	}

	membersByDimension := map[askdata.ID][]askdata.ID{}
	for _, item := range binding.MemberBindings {
		if item.DimensionVersionID.Validate() != nil || item.MemberVersionID.Validate() != nil {
			continue
		}
		membersByDimension[item.DimensionVersionID] = appendUniquePlanID(membersByDimension[item.DimensionVersionID], item.MemberVersionID)
	}
	// A provider occasionally labels the only calendar dimension as FILTER even
	// though the trusted rule parse resolved a calendar range. When there is one
	// unambiguous, member-less dimension, the deterministic calendar evidence is
	// authoritative for its role and can safely correct that label.
	if rules != nil && rules.Time != nil && timeDimension == "" {
		calendarCandidates := make([]askdata.ID, 0, len(filterDimensions))
		for dimensionID := range filterDimensions {
			if len(membersByDimension[dimensionID]) == 0 {
				calendarCandidates = append(calendarCandidates, dimensionID)
			}
		}
		if len(calendarCandidates) == 1 {
			timeDimension = calendarCandidates[0]
			delete(filterDimensions, timeDimension)
		}
	}
	filters := make([]ircontract.Filter, 0, len(membersByDimension))
	dimensionIDs := make([]askdata.ID, 0, len(membersByDimension))
	for dimensionID := range membersByDimension {
		dimensionIDs = append(dimensionIDs, dimensionID)
	}
	sort.Slice(dimensionIDs, func(i, j int) bool { return dimensionIDs[i] < dimensionIDs[j] })
	for _, dimensionID := range dimensionIDs {
		filters = append(filters, ircontract.Filter{
			DimensionVersionID: dimensionID,
			Operator:           ircontract.FilterIn,
			MemberVersionIDs:   membersByDimension[dimensionID],
		})
		delete(filterDimensions, dimensionID)
	}
	// Never silently drop a requested filter. Candidate judgment must first
	// resolve it to governed member identities.
	if len(filterDimensions) != 0 {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}

	var timeRange *ircontract.TimeRange
	if rules != nil && rules.Time != nil {
		if timeDimension == "" {
			return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
		}
		grain := ircontract.TimeGrain(rules.Time.Grain)
		timeRange = &ircontract.TimeRange{
			DimensionVersionID: timeDimension,
			Start:              rules.Time.Start, EndExclusive: rules.Time.EndExclusive,
			Timezone: rules.Time.Timezone, RequestedPeriod: string(rules.Time.Expression), Grain: grain,
		}
		for _, grouping := range rules.Groupings {
			if grouping.Grain == nil || grouped[timeDimension] {
				continue
			}
			groupGrain := ircontract.TimeGrain(*grouping.Grain)
			grouped[timeDimension] = true
			groupBy = append(groupBy, ircontract.GroupBy{DimensionVersionID: timeDimension, Grain: &groupGrain})
			break
		}
	}

	var comparison *ircontract.Comparison
	if rules != nil && len(rules.Comparisons) > 1 {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}
	if rules != nil && len(rules.Comparisons) == 1 {
		mapped := ircontract.ComparisonType(rules.Comparisons[0].Type)
		switch mapped {
		case ircontract.ComparisonYearOverYear, ircontract.ComparisonMonthOverMonth, ircontract.ComparisonPeriodOverPeriod:
			comparison = &ircontract.Comparison{Type: mapped, Periods: 1}
		default:
			return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
		}
	}

	limit := 100
	sorts := []ircontract.Sort{}
	if rules != nil && rules.Ranking != nil {
		limit = rules.Ranking.Limit
		sorts = append(sorts, ircontract.Sort{
			TargetType: ircontract.SortTargetMetric, TargetVersionID: metrics[0].MetricVersionID,
			Direction: ircontract.SortDirection(rules.Ranking.Direction), Nulls: ircontract.NullsLast,
			RankBy: ircontract.RankBy(rules.Ranking.RankBy),
		})
	} else if rules != nil && len(rules.Sorts) > 0 {
		sorts = append(sorts, ircontract.Sort{
			TargetType: ircontract.SortTargetMetric, TargetVersionID: metrics[0].MetricVersionID,
			Direction: ircontract.SortDirection(rules.Sorts[0].Direction), Nulls: ircontract.NullsLast,
			RankBy: ircontract.RankByCurrentValue,
		})
	}
	if limit < 1 || limit > ircontract.MaxTopN {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}

	proposal := cognition.PlanProposal{
		SemanticIR: ircontract.SemanticIR{
			IRVersion: ircontract.Version, SemanticReleaseID: request.Run.Release.ReleaseID,
			SemanticContentHash: request.Run.Release.ContentHash, DomainID: request.Run.DomainID,
			ModelVersionID: binding.ModelVersionID, Metrics: metrics, GroupBy: groupBy,
			Filters: filters, TimeRange: timeRange, Comparison: comparison, Sort: sorts, Limit: limit,
			OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieDeterministicCut,
		},
		Confidence: askdata.ConfidenceEvidence{
			Score: 1, Margin: 1, Evidence: []askdata.EvidenceRef{bindingEvidence},
			ReasonCodes: []string{"DETERMINISTIC_VALIDATED_BINDING"},
		},
	}
	if proposal.Validate() != nil || strings.TrimSpace(string(proposal.SemanticIR.SemanticContentHash)) == "" {
		return cognition.PlanProposal{}, askdata.EvidenceRef{}, false
	}
	return proposal, bindingEvidence, true
}

func rulesFromConversationFacts(request LoopRequest) *understanding.RuleParseResult {
	for _, governed := range request.Facts {
		if governed.Fact.Kind != cognition.FactConversation {
			continue
		}
		var conversation struct {
			Question  string                         `json:"question"`
			RuleParse *understanding.RuleParseResult `json:"ruleParse"`
		}
		if json.Unmarshal(governed.Fact.Payload, &conversation) != nil {
			continue
		}
		if conversation.RuleParse != nil {
			return conversation.RuleParse
		}
		normalized, err := understanding.NormalizeQuestion(conversation.Question)
		if err != nil {
			continue
		}
		parser, err := understanding.NewRuleParser(request.Run.CreatedAt, 0)
		if err != nil {
			continue
		}
		rules, err := parser.Parse(normalized)
		if err == nil {
			return &rules
		}
	}
	return nil
}

func appendUniquePlanID(values []askdata.ID, value askdata.ID) []askdata.ID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
