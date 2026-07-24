package datasetai

import "strings"

// validateCreatePlanHints closes the gap between a syntactically valid graph and the structured
// computation requested by the metric-authoring handoff. It deliberately consumes PlanHints
// instead of matching natural-language phrases, so equivalent Chinese, English, or domain-specific
// wording follows the same rule.
func validateCreatePlanHints(plan GraphPlan, hints *PlanHints) error {
	if hints == nil || hints.Aggregation == "" && hints.TimeGrain == "" {
		return nil
	}
	if len(plan.Groups) == 0 {
		return invalidOutputWithReason(InvalidOutputReasonGroup, "CREATE hints require aggregation or a time grain, but the plan has no GROUP component")
	}

	nodes := make(map[string]PlanNode, len(plan.Nodes))
	for _, node := range plan.Nodes {
		nodes[node.ID] = node
	}
	transforms := make(map[string]PlanTransform, len(plan.Transforms))
	for _, transform := range plan.Transforms {
		transforms[transform.ID] = transform
	}

	if hints.Aggregation != "" && !hintedMetricIsProduced(plan, *hints, nodes, transforms) {
		return invalidOutputWithReason(
			InvalidOutputReasonGroup,
			"CREATE hints require a final GROUP metric with aggregation "+hints.Aggregation+" bound to one of the hinted measure fields",
		)
	}
	if len(hints.DimensionFields) > 0 && !hintedDimensionIsProduced(plan, hints.DimensionFields, nodes, transforms) {
		return invalidOutputWithReason(
			InvalidOutputReasonGroup,
			"CREATE hints require at least one hinted dimension field to be used and exposed by GROUP",
		)
	}
	if hints.TimeGrain != "" && !hintedTimeGrainIsProduced(plan, *hints, nodes, transforms) {
		return invalidOutputWithReason(
			InvalidOutputReasonTransform,
			"CREATE hints require a consumed DATE_FORMAT output with time grain "+hints.TimeGrain+" to be used and exposed as a GROUP dimension",
		)
	}
	return nil
}

func hintedMetricIsProduced(plan GraphPlan, hints PlanHints, nodes map[string]PlanNode, transforms map[string]PlanTransform) bool {
	for _, group := range plan.Groups {
		for _, metric := range group.Metrics {
			if metric.Aggregation != hints.Aggregation {
				continue
			}
			key := fieldKey(metric.NodeID, metric.Column)
			if len(hints.MeasureFields) > 0 && !fieldDescendsFromAnyHint(key, hints.MeasureFields, nodes, transforms, map[string]bool{}) {
				continue
			}
			if planExposesKey(plan, key) {
				return true
			}
		}
	}
	return false
}

func hintedDimensionIsProduced(plan GraphPlan, hints []PlanFieldHint, nodes map[string]PlanNode, transforms map[string]PlanTransform) bool {
	for _, group := range plan.Groups {
		for _, dimension := range group.Dimensions {
			key := fieldKey(dimension.NodeID, dimension.Column)
			if fieldDescendsFromAnyHint(key, hints, nodes, transforms, map[string]bool{}) && planExposesKey(plan, key) {
				return true
			}
		}
	}
	return false
}

func hintedTimeGrainIsProduced(plan GraphPlan, hints PlanHints, nodes map[string]PlanNode, transforms map[string]PlanTransform) bool {
	groupDimensions := map[string]bool{}
	for _, group := range plan.Groups {
		for _, dimension := range group.Dimensions {
			groupDimensions[fieldKey(dimension.NodeID, dimension.Column)] = true
		}
	}
	timeHints := []PlanFieldHint{}
	if hints.TimeField != nil {
		timeHints = append(timeHints, *hints.TimeField)
	}
	for _, transform := range plan.Transforms {
		if transform.ComponentType != "DATE_FORMAT" {
			continue
		}
		for _, rule := range transform.Rules {
			outputKey := fieldKey(transform.ID, rule.Output.ID)
			if rule.Unit != hints.TimeGrain || !groupDimensions[outputKey] || !planExposesKey(plan, outputKey) {
				continue
			}
			if len(timeHints) == 0 {
				return true
			}
			for _, inputKey := range rule.InputKeys {
				if fieldDescendsFromAnyHint(inputKey, timeHints, nodes, transforms, map[string]bool{}) {
					return true
				}
			}
		}
	}
	return false
}

func fieldDescendsFromAnyHint(key string, hints []PlanFieldHint, nodes map[string]PlanNode, transforms map[string]PlanTransform, visiting map[string]bool) bool {
	componentID, column, ok := splitPlanFieldKey(key)
	if !ok || visiting[key] {
		return false
	}
	if node, exists := nodes[componentID]; exists {
		for _, hint := range hints {
			if node.TableID == hint.TableID && column == hint.Column {
				return true
			}
		}
		return false
	}
	transform, exists := transforms[componentID]
	if !exists {
		return false
	}
	visiting[key] = true
	defer delete(visiting, key)
	for _, rule := range transform.Rules {
		if rule.Output.ID != column {
			continue
		}
		for _, inputKey := range rule.InputKeys {
			if fieldDescendsFromAnyHint(inputKey, hints, nodes, transforms, visiting) {
				return true
			}
		}
		if rule.ReplaceSourceKey != "" && fieldDescendsFromAnyHint(rule.ReplaceSourceKey, hints, nodes, transforms, visiting) {
			return true
		}
		for _, value := range rule.ConditionValues {
			if value.Mode == "FIELD" && fieldDescendsFromAnyHint(value.Value, hints, nodes, transforms, visiting) {
				return true
			}
		}
	}
	return false
}

func splitPlanFieldKey(key string) (string, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(key), ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func planExposesKey(plan GraphPlan, key string) bool {
	for _, output := range plan.End.Outputs {
		outputKey := output.Key
		if outputKey == "" {
			outputKey = fieldKey(output.NodeID, output.Column)
		}
		if outputKey == key {
			return true
		}
	}
	return false
}
