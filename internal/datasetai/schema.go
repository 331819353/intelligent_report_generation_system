package datasetai

func changeIntentOutputSchema(catalog []CatalogTable) map[string]any {
	identifier := map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_]{0,127}$"}
	tableIDs := make([]string, 0, len(catalog))
	seenTableIDs := map[string]bool{}
	for _, table := range catalog {
		if !seenTableIDs[table.ID] {
			seenTableIDs[table.ID] = true
			tableIDs = append(tableIDs, table.ID)
		}
	}
	kind := map[string]any{"type": "string", "enum": []string{"DATASET", "NODE", "JOIN", "GROUP", "END"}}
	input := strictObject([]string{"kind", "id"}, map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"NODE", "JOIN", "GROUP"}},
		"id":   identifier,
	})
	componentRef := strictObject([]string{"componentKind", "componentId"}, map[string]any{
		"componentKind": kind,
		"componentId":   identifier,
	})
	binding := strictObject([]string{"nodeId", "tableId", "column"}, map[string]any{
		"nodeId":  identifier,
		"tableId": map[string]any{"type": "string", "enum": tableIDs},
		"column":  map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
	})
	inputChange := strictObject([]string{"field", "from", "to"}, map[string]any{
		"field": map[string]any{"type": "string", "enum": []string{"left", "right", "input"}},
		"from":  input,
		"to":    input,
	})
	operation := strictObject([]string{"action", "componentKind", "componentId", "componentName", "fields", "inputChanges", "description"}, map[string]any{
		"action":        map[string]any{"type": "string", "enum": []string{"ADD", "UPDATE", "REMOVE"}},
		"componentKind": kind,
		"componentId":   identifier,
		"componentName": map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"fields": map[string]any{
			"type": "array", "maxItems": 8,
			"items": map[string]any{"type": "string", "enum": []string{
				"name", "description", "tableId", "alias", "selectedColumns",
				"left", "right", "joinType", "conditions", "input", "dimensions", "metrics", "outputs",
			}},
		},
		"inputChanges": map[string]any{"type": "array", "maxItems": 3, "items": inputChange},
		"description":  map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
	})
	groupUse := strictObject([]string{"groupId", "role", "grouping", "aggregation"}, map[string]any{
		"groupId":     identifier,
		"role":        map[string]any{"type": "string", "enum": []string{"DIMENSION", "METRIC"}},
		"grouping":    map[string]any{"type": "string", "enum": []string{"", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR"}},
		"aggregation": map[string]any{"type": "string", "enum": []string{"", "SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX"}},
	})
	joinUse := strictObject([]string{"joinId", "side", "peer"}, map[string]any{
		"joinId": identifier,
		"side":   map[string]any{"type": "string", "enum": []string{"LEFT", "RIGHT"}},
		"peer":   binding,
	})
	outputUse := strictObject([]string{"endId", "name", "code"}, map[string]any{
		"endId": map[string]any{"type": "string", "const": "end_1"},
		"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"code":  identifier,
	})
	fieldChange := strictObject([]string{"field", "selectionAction", "purpose", "groupUses", "joinUses", "outputUses"}, map[string]any{
		"field":           binding,
		"selectionAction": map[string]any{"type": "string", "enum": []string{"ADD", "KEEP", "REMOVE"}},
		"purpose":         map[string]any{"type": "string", "enum": []string{"FINAL_OUTPUT", "INTERNAL_ONLY", "SELECTED_ONLY"}},
		"groupUses":       map[string]any{"type": "array", "maxItems": 32, "items": groupUse},
		"joinUses":        map[string]any{"type": "array", "maxItems": 32, "items": joinUse},
		"outputUses":      map[string]any{"type": "array", "maxItems": 1, "items": outputUse},
	})
	changeSet := strictObject([]string{"operations", "fieldChanges"}, map[string]any{
		"operations":   map[string]any{"type": "array", "maxItems": 64, "items": operation},
		"fieldChanges": map[string]any{"type": "array", "maxItems": maxFieldChanges, "items": fieldChange},
	})
	return strictObject([]string{"status", "question", "candidates", "changeSet"}, map[string]any{
		"status":     map[string]any{"type": "string", "enum": []string{"READY", "CLARIFY"}},
		"question":   map[string]any{"type": "string", "maxLength": 500},
		"candidates": map[string]any{"type": "array", "maxItems": 32, "items": componentRef},
		"changeSet":  changeSet,
	})
}

func proposalOutputSchema(catalog []CatalogTable) map[string]any {
	identifier := map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_]{0,127}$"}
	shortText := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	allColumns := make([]string, 0, catalogColumnCount(catalog))
	seenColumns := map[string]bool{}
	for _, table := range catalog {
		for _, column := range table.Columns {
			if !seenColumns[column.Name] {
				seenColumns[column.Name] = true
				allColumns = append(allColumns, column.Name)
			}
		}
	}
	column := map[string]any{"type": "string", "enum": allColumns}
	input := strictObject([]string{"kind", "id"}, map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"NODE", "JOIN", "GROUP"}},
		"id":   identifier,
	})
	bindingProperties := map[string]any{
		"nodeId": identifier,
		"column": column,
	}
	nodeAlternatives := make([]any, 0, len(catalog))
	seenTables := map[string]bool{}
	for _, table := range catalog {
		if seenTables[table.ID] || len(table.Columns) == 0 {
			continue
		}
		seenTables[table.ID] = true
		columns := make([]string, 0, len(table.Columns))
		for _, catalogColumn := range table.Columns {
			columns = append(columns, catalogColumn.Name)
		}
		nodeAlternatives = append(nodeAlternatives, strictObject([]string{"id", "tableId", "alias", "selectedColumns"}, map[string]any{
			"id":      identifier,
			"tableId": map[string]any{"type": "string", "const": table.ID},
			"alias":   identifier,
			"selectedColumns": map[string]any{
				// deepseek-v3 的严格 Schema 语法不支持 uniqueItems；重复字段仍由
				// validateProposal 拒绝，避免为了供应商兼容性放宽可信边界。
				"type": "array", "minItems": 1, "maxItems": 512,
				"items": map[string]any{"type": "string", "enum": columns},
			},
		}))
	}
	node := map[string]any{"oneOf": nodeAlternatives}
	joinCondition := strictObject([]string{"leftNodeId", "leftColumn", "rightNodeId", "rightColumn"}, map[string]any{
		"leftNodeId":  identifier,
		"leftColumn":  column,
		"rightNodeId": identifier,
		"rightColumn": column,
	})
	join := strictObject([]string{"id", "name", "left", "right", "joinType", "conditions"}, map[string]any{
		"id":       identifier,
		"name":     map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"left":     input,
		"right":    input,
		"joinType": map[string]any{"type": "string", "enum": []string{"INNER", "LEFT"}},
		"conditions": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 16, "items": joinCondition,
		},
	})
	dimensionProperties := cloneSchemaMap(bindingProperties)
	dimensionProperties["grouping"] = map[string]any{"type": "string", "enum": []string{"", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR"}}
	dimension := strictObject([]string{"nodeId", "column", "grouping"}, dimensionProperties)
	metricProperties := cloneSchemaMap(bindingProperties)
	metricProperties["aggregation"] = map[string]any{"type": "string", "enum": []string{"SUM", "AVG", "COUNT", "COUNT_DISTINCT", "MIN", "MAX"}}
	metric := strictObject([]string{"nodeId", "column", "aggregation"}, metricProperties)
	group := strictObject([]string{"id", "name", "input", "dimensions", "metrics"}, map[string]any{
		"id":    identifier,
		"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"input": input,
		"dimensions": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 128, "items": dimension,
		},
		"metrics": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 128, "items": metric,
		},
	})
	outputProperties := cloneSchemaMap(bindingProperties)
	outputProperties["name"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
	outputProperties["code"] = identifier
	output := strictObject([]string{"nodeId", "column", "name", "code"}, outputProperties)
	end := strictObject([]string{"name", "input", "outputs"}, map[string]any{
		"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"input": input,
		"outputs": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 512, "items": output,
		},
	})
	plan := strictObject([]string{"dataset", "nodes", "joins", "groups", "end"}, map[string]any{
		"dataset": strictObject([]string{"name", "description"}, map[string]any{
			"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"description": shortText(2000),
		}),
		"nodes":  map[string]any{"type": "array", "minItems": 1, "maxItems": maxPlanNodes, "items": node},
		"joins":  map[string]any{"type": "array", "maxItems": maxPlanComponents, "items": join},
		"groups": map[string]any{"type": "array", "maxItems": maxPlanComponents, "items": group},
		"end":    end,
	})
	return strictObject([]string{"schemaVersion", "mode", "summary", "assumptions", "warnings", "plan"}, map[string]any{
		"schemaVersion": map[string]any{"type": "string", "const": SchemaVersion},
		"mode":          map[string]any{"type": "string", "enum": []string{"CREATE", "MODIFY"}},
		"summary":       map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		"assumptions": map[string]any{
			"type": "array", "maxItems": 12, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		},
		"warnings": map[string]any{
			"type": "array", "maxItems": 12, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
		},
		"plan": plan,
	})
}

func strictObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           properties,
	}
}

func cloneSchemaMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+1)
	for key, item := range value {
		result[key] = item
	}
	return result
}
