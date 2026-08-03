package semanticqa

import "strings"

func evidenceIDForValue(kind, value string) string {
	return strings.TrimSpace(kind) + ":" + hashText(
		strings.ToLower(strings.TrimSpace(value)),
	)
}

func metricSemanticEvidenceIDs(
	query string,
	candidates []semanticMetricEvidence,
) []string {
	result := []string{evidenceIDForValue("metric-semantic-search", query)}
	for _, candidate := range candidates {
		identity := candidate.Code
		if strings.TrimSpace(identity) == "" {
			identity = candidate.Name
		}
		result = appendUniqueString(
			result, evidenceIDForValue("metric-semantic", identity),
		)
	}
	return result
}

func metricCatalogEvidenceIDs(
	query string,
	candidates []recallCandidate,
) []string {
	result := []string{evidenceIDForValue("metric-catalog-search", query)}
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" ||
			strings.TrimSpace(candidate.Code) == "" {
			continue
		}
		result = appendUniqueString(
			result, evidenceIDForValue("metric", candidate.Code),
		)
	}
	return result
}

func dimensionSemanticEvidenceIDs(
	metricCode, query string,
	result dimensionSemanticToolResult,
) []string {
	ids := []string{evidenceIDForValue(
		"dimension-semantic-search", metricCode+"\x00"+query,
	)}
	for _, candidate := range result.AvailableDimensions {
		if strings.TrimSpace(candidate.DimensionCode) == "" {
			continue
		}
		ids = appendUniqueString(
			ids, evidenceIDForValue("dimension", candidate.DimensionCode),
		)
	}
	return ids
}

func dimensionDecisionEvidenceIDs(
	metricCode, query string,
	lookups []QueryDimensionValueLookupTrace,
) []string {
	ids := []string{evidenceIDForValue(
		"dimension-decision-search", metricCode+"\x00"+query,
	)}
	for _, lookup := range lookups {
		for _, candidate := range lookup.DecisionCandidates {
			if strings.TrimSpace(candidate.DecisionID) == "" {
				continue
			}
			ids = appendUniqueString(
				ids, "dimension-decision:"+candidate.DecisionID,
			)
		}
	}
	return ids
}
