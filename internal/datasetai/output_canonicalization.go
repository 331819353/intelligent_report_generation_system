package datasetai

import "strings"

type availablePlanField struct {
	transformOutput bool
	outputID        string
	name            string
	code            string
	nodeID          string
	column          string
}

// canonicalizeEndOutputKeys repairs only unambiguous key-shape mistakes. It never invents an
// output or rewires the graph: the chosen key must already be produced by end.input. This keeps
// the fail-closed boundary while avoiding a second model failure for nodeId.outputId or
// groupId.resultName spellings when the transform output metadata identifies one exact field.
func canonicalizeEndOutputKeys(plan GraphPlan) GraphPlan {
	available := availableFieldsAtInput(plan, plan.End.Input, map[string]bool{}, map[string]map[string]availablePlanField{})
	if len(available) == 0 {
		return plan
	}
	for index := range plan.End.Outputs {
		output := &plan.End.Outputs[index]
		if field, exists := available[output.Key]; exists {
			canonicalizePlanOutputLineage(output, field)
			continue
		}
		if key := uniqueTransformOutputKey(available, *output); key != "" {
			output.Key = key
			canonicalizePlanOutputLineage(output, available[key])
			continue
		}
		physicalKey := fieldKey(output.NodeID, output.Column)
		if field, exists := available[physicalKey]; exists {
			output.Key = physicalKey
			canonicalizePlanOutputLineage(output, field)
		}
	}
	return plan
}

func canonicalizePlanOutputLineage(output *PlanOutput, field availablePlanField) {
	if output == nil || field.nodeID == "" || field.column == "" {
		return
	}
	output.NodeID = field.nodeID
	output.Column = field.column
}

func uniqueTransformOutputKey(available map[string]availablePlanField, output PlanOutput) string {
	exact := make([]string, 0, 1)
	for key, field := range available {
		if field.transformOutput && field.name == output.Name && field.code == output.Code {
			exact = append(exact, key)
		}
	}
	if len(exact) == 1 {
		return exact[0]
	}
	suffix := output.Key
	if separator := strings.LastIndex(suffix, "."); separator >= 0 {
		suffix = suffix[separator+1:]
	}
	matched := make([]string, 0, 1)
	for key, field := range available {
		if !field.transformOutput || field.outputID != suffix {
			continue
		}
		if field.name == output.Name || field.code == output.Code {
			matched = append(matched, key)
		}
	}
	if len(matched) == 1 {
		return matched[0]
	}
	return ""
}

func availableFieldsAtInput(plan GraphPlan, input PlanInput, visiting map[string]bool, memo map[string]map[string]availablePlanField) map[string]availablePlanField {
	componentKey := input.Kind + ":" + input.ID
	if cached, exists := memo[componentKey]; exists {
		return cloneAvailablePlanFields(cached)
	}
	if visiting[componentKey] {
		return map[string]availablePlanField{}
	}
	visiting[componentKey] = true
	defer delete(visiting, componentKey)

	result := map[string]availablePlanField{}
	switch input.Kind {
	case "NODE":
		for _, node := range plan.Nodes {
			if node.ID != input.ID {
				continue
			}
			for _, column := range node.SelectedColumns {
				result[fieldKey(node.ID, column)] = availablePlanField{nodeID: node.ID, column: column}
			}
			break
		}
	case "JOIN":
		for _, join := range plan.Joins {
			if join.ID != input.ID {
				continue
			}
			mergeAvailablePlanFields(result, availableFieldsAtInput(plan, join.Left, visiting, memo))
			mergeAvailablePlanFields(result, availableFieldsAtInput(plan, join.Right, visiting, memo))
			break
		}
	case "GROUP":
		for _, group := range plan.Groups {
			if group.ID != input.ID {
				continue
			}
			upstream := availableFieldsAtInput(plan, group.Input, visiting, memo)
			for _, dimension := range group.Dimensions {
				key := fieldKey(dimension.NodeID, dimension.Column)
				result[key] = upstream[key]
			}
			for _, metric := range group.Metrics {
				key := fieldKey(metric.NodeID, metric.Column)
				result[key] = upstream[key]
			}
			break
		}
	case "TRANSFORM":
		for _, transform := range plan.Transforms {
			if transform.ID != input.ID {
				continue
			}
			mergeAvailablePlanFields(result, availableFieldsAtInput(plan, transform.Input, visiting, memo))
			for _, rule := range transform.Rules {
				if rule.ReplaceSourceKey != "" {
					delete(result, rule.ReplaceSourceKey)
				}
				lineage := availablePlanField{}
				if len(rule.InputKeys) > 0 {
					lineage = result[rule.InputKeys[0]]
				}
				result[fieldKey(transform.ID, rule.Output.ID)] = availablePlanField{
					transformOutput: true,
					outputID:        rule.Output.ID,
					name:            rule.Output.Name,
					code:            rule.Output.Code,
					nodeID:          lineage.nodeID,
					column:          lineage.column,
				}
			}
			break
		}
	}
	memo[componentKey] = cloneAvailablePlanFields(result)
	return result
}

func mergeAvailablePlanFields(target, source map[string]availablePlanField) {
	for key, field := range source {
		target[key] = field
	}
}

func cloneAvailablePlanFields(source map[string]availablePlanField) map[string]availablePlanField {
	result := make(map[string]availablePlanField, len(source))
	mergeAvailablePlanFields(result, source)
	return result
}
