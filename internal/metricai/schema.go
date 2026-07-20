package metricai

import "sort"

// proposalOutputSchema constrains the provider before the stronger cross-reference checks in
// validateProposal. Dataset/version pairing and evidence completeness remain local invariants.
func proposalOutputSchema(retrieval RetrievalContext) map[string]any {
	targetDatasetIDs := []string{""}
	targetDatasetVersionIDs := []string{""}
	evidenceDatasetIDs := []string{""}
	evidenceDatasetVersionIDs := []string{""}
	evidenceSourceIDs := []string{}
	publishedDatasetIDs := make([]string, 0, len(retrieval.Datasets))
	publishedDatasetVersionIDs := make([]string, 0, len(retrieval.Datasets))
	publishedFieldIDs := make([]string, 0, len(retrieval.Fields))
	metricVersionIDs := []string{""}
	for _, dataset := range retrieval.Datasets {
		evidenceDatasetIDs = append(evidenceDatasetIDs, dataset.ID)
		evidenceDatasetVersionIDs = append(evidenceDatasetVersionIDs, dataset.VersionID)
		evidenceSourceIDs = append(evidenceSourceIDs, dataset.ID)
		if !dataset.Aggregated {
			targetDatasetIDs = append(targetDatasetIDs, dataset.ID)
			targetDatasetVersionIDs = append(targetDatasetVersionIDs, dataset.VersionID)
			publishedDatasetIDs = append(publishedDatasetIDs, dataset.ID)
			publishedDatasetVersionIDs = append(publishedDatasetVersionIDs, dataset.VersionID)
		}
	}
	for _, dataset := range retrieval.ModifiableDraftDatasets {
		evidenceDatasetIDs = append(evidenceDatasetIDs, dataset.ID)
		evidenceDatasetVersionIDs = append(evidenceDatasetVersionIDs, dataset.VersionID)
		evidenceSourceIDs = append(evidenceSourceIDs, dataset.ID)
		if dataset.Manageable && !dataset.Aggregated {
			targetDatasetIDs = append(targetDatasetIDs, dataset.ID)
			targetDatasetVersionIDs = append(targetDatasetVersionIDs, dataset.VersionID)
		}
	}
	for _, field := range retrieval.Fields {
		publishedFieldIDs = append(publishedFieldIDs, field.ID)
		evidenceSourceIDs = append(evidenceSourceIDs, field.ID)
	}
	for _, field := range retrieval.ModifiableDraftFields {
		evidenceSourceIDs = append(evidenceSourceIDs, field.ID)
	}
	for _, existing := range retrieval.ExistingMetrics {
		metricVersionIDs = append(metricVersionIDs, existing.VersionID)
		evidenceSourceIDs = append(evidenceSourceIDs, existing.VersionID)
	}
	targetDatasetIDs = uniqueSorted(targetDatasetIDs)
	targetDatasetVersionIDs = uniqueSorted(targetDatasetVersionIDs)
	evidenceDatasetIDs = uniqueSorted(evidenceDatasetIDs)
	evidenceDatasetVersionIDs = uniqueSorted(evidenceDatasetVersionIDs)
	evidenceSourceIDs = uniqueSorted(evidenceSourceIDs)
	publishedDatasetIDs = uniqueSorted(publishedDatasetIDs)
	publishedDatasetVersionIDs = uniqueSorted(publishedDatasetVersionIDs)
	publishedFieldIDs = uniqueSorted(publishedFieldIDs)
	metricVersionIDs = uniqueSorted(metricVersionIDs)

	evidenceSourceID := map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
	evidenceMaxItems := 64
	if len(evidenceSourceIDs) == 0 {
		// Keep the item schema valid while making evidence impossible when retrieval is empty.
		evidenceSourceIDs = []string{"__NO_AUTHORIZED_SOURCE__"}
		evidenceMaxItems = 0
	}
	evidenceSourceID["enum"] = evidenceSourceIDs
	evidence := strictObject(
		[]string{"sourceType", "sourceId", "datasetId", "datasetVersionId", "reason"},
		map[string]any{
			"sourceType":       map[string]any{"type": "string", "enum": []string{"DATASET", "FIELD", "METRIC"}},
			"sourceId":         evidenceSourceID,
			"datasetId":        enumString(evidenceDatasetIDs),
			"datasetVersionId": enumString(evidenceDatasetVersionIDs),
			"reason":           map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		},
	)

	root := strictObject(
		[]string{
			"schemaVersion", "strategy", "summary", "targetDatasetId", "targetDatasetVersionId",
			"reuseMetricVersionId", "retrievalEvidence", "candidateMetricDefinition",
			"datasetInstruction", "clarificationQuestions", "assumptions", "warnings",
		},
		map[string]any{
			"schemaVersion":          map[string]any{"type": "string", "const": SchemaVersion},
			"strategy":               map[string]any{"type": "string", "enum": []string{StrategyReuseMetric, StrategyCreateOnDataset, StrategyCreateDataset, StrategyModifyDataset, StrategyDataGap, StrategyNeedsClarification}},
			"summary":                map[string]any{"type": "string", "minLength": 1, "maxLength": 240},
			"targetDatasetId":        enumString(targetDatasetIDs),
			"targetDatasetVersionId": enumString(targetDatasetVersionIDs),
			"reuseMetricVersionId":   enumString(metricVersionIDs),
			"retrievalEvidence":      map[string]any{"type": "array", "maxItems": evidenceMaxItems, "items": evidence},
			"candidateMetricDefinition": map[string]any{"anyOf": []any{
				map[string]any{"$ref": "#/$defs/metricDefinition"},
				map[string]any{"type": "null"},
			}},
			"datasetInstruction":     map[string]any{"type": "string", "maxLength": 2000},
			"clarificationQuestions": textArray(8, 500),
			"assumptions":            textArray(12, 500),
			"warnings":               textArray(12, 500),
		},
	)
	root["$defs"] = map[string]any{
		"expression":       expressionSchema(publishedFieldIDs),
		"metricDefinition": metricDefinitionSchema(publishedDatasetIDs, publishedDatasetVersionIDs),
	}
	return root
}

func metricDefinitionSchema(datasetIDs, datasetVersionIDs []string) map[string]any {
	datasetID := map[string]any{"type": "string", "minLength": 1}
	if len(datasetIDs) > 0 {
		datasetID["enum"] = datasetIDs
	}
	datasetVersionID := map[string]any{"type": "string", "minLength": 1}
	if len(datasetVersionIDs) > 0 {
		datasetVersionID["enum"] = datasetVersionIDs
	}
	dimension := strictObject(
		[]string{"fieldId", "name", "hierarchyFieldIds", "sortDirection", "nullLabel"},
		map[string]any{
			"fieldId":           map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"name":              map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"hierarchyFieldIds": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}},
			"sortDirection":     map[string]any{"type": "string", "enum": []string{"ASC", "DESC"}},
			"nullLabel":         map[string]any{"type": "string", "maxLength": 100},
		},
	)
	return strictObject(
		[]string{
			"schemaVersion", "metric", "datasetId", "datasetVersionId", "expression", "aggregation",
			"unit", "numberFormat", "timeFieldId", "timeGrain", "additivity",
			"nonAdditiveDimensionFieldIds", "allowedDimensions", "decimalScale", "roundingMode",
			"nullHandling", "divisionByZero",
		},
		map[string]any{
			"schemaVersion": map[string]any{"type": "string", "const": "1.0"},
			"metric": strictObject(
				[]string{"code", "name", "description", "type"},
				map[string]any{
					"code":        map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_]{0,63}$"},
					"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
					"description": map[string]any{"type": "string", "maxLength": 2000},
					"type":        map[string]any{"type": "string", "const": "ATOMIC"},
				},
			),
			"datasetId":                    datasetID,
			"datasetVersionId":             datasetVersionID,
			"expression":                   map[string]any{"$ref": "#/$defs/expression"},
			"aggregation":                  map[string]any{"type": "string", "enum": []string{"SUM", "AVG", "MIN", "MAX", "COUNT", "COUNT_DISTINCT"}},
			"unit":                         map[string]any{"type": "string", "maxLength": 32},
			"numberFormat":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
			"timeFieldId":                  map[string]any{"type": "string", "maxLength": 200},
			"timeGrain":                    map[string]any{"type": "string", "enum": []string{"NONE", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR"}},
			"additivity":                   map[string]any{"type": "string", "enum": []string{"ADDITIVE", "SEMI_ADDITIVE", "NON_ADDITIVE"}},
			"nonAdditiveDimensionFieldIds": map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}},
			"allowedDimensions":            map[string]any{"type": "array", "maxItems": 64, "items": dimension},
			"decimalScale":                 map[string]any{"type": "integer", "minimum": 0, "maximum": 12},
			"roundingMode":                 map[string]any{"type": "string", "const": "HALF_UP"},
			"nullHandling":                 map[string]any{"type": "string", "const": "IGNORE"},
			"divisionByZero":               map[string]any{"type": "string", "const": "NULL"},
		},
	)
}

func expressionSchema(fieldIDs []string) map[string]any {
	fieldID := map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
	if len(fieldIDs) > 0 {
		fieldID["enum"] = fieldIDs
	}
	return map[string]any{"oneOf": []any{
		strictObject([]string{"type", "fieldId"}, map[string]any{
			"type": map[string]any{"type": "string", "const": "FIELD_REF"}, "fieldId": fieldID,
		}),
		strictObject([]string{"type", "value"}, map[string]any{
			"type":  map[string]any{"type": "string", "const": "LITERAL"},
			"value": map[string]any{"type": "string", "pattern": "^-?(?:0|[1-9][0-9]*)(?:\\.[0-9]+)?$", "maxLength": 64},
		}),
		strictObject([]string{"type", "arguments"}, map[string]any{
			"type":      map[string]any{"type": "string", "enum": []string{"ADD", "SUBTRACT", "MULTIPLY", "DIVIDE"}},
			"arguments": map[string]any{"type": "array", "minItems": 2, "maxItems": 2, "items": map[string]any{"$ref": "#/$defs/expression"}},
		}),
	}}
}

func strictObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": properties,
	}
}

func textArray(maxItems, maxLength int) map[string]any {
	return map[string]any{
		"type": "array", "maxItems": maxItems,
		"items": map[string]any{"type": "string", "minLength": 1, "maxLength": maxLength},
	}
}

func enumString(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
