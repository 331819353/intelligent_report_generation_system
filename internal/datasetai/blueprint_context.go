package datasetai

import (
	"fmt"
	"strings"
)

// promptBlueprintContext is the confirmed blueprint as the planner sees it: only
// AUTO_CONFIRMED and USER_CONFIRMED stages, with alternatives and confidences
// stripped — the planner receives settled decisions, not options. After planning,
// validateBlueprintCompliance proves the graph realises each of them.
type promptBlueprintContext struct {
	Grain      *GrainDecision        `json:"grain,omitempty"`
	Metrics    []promptMetricContext `json:"metrics,omitempty"`
	Joins      []promptJoinContext   `json:"joins,omitempty"`
	Transforms []TransformDecision   `json:"transforms,omitempty"`
	Filters    []FilterDecision      `json:"filters,omitempty"`
	Outputs    []OutputDecision      `json:"outputs,omitempty"`
}

type promptMetricContext struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Definition string         `json:"definition,omitempty"`
	Binding    *MetricBinding `json:"binding,omitempty"`
}

type promptJoinContext struct {
	ID           string    `json:"id"`
	LeftTableID  string    `json:"leftTableId"`
	RightTableID string    `json:"rightTableId"`
	JoinType     string    `json:"joinType"`
	Keys         []JoinKey `json:"keys"`
	Cardinality  string    `json:"cardinality,omitempty"`
}

func stageConfirmed(decision StageDecision) bool {
	return decision.Status == StageStatusAutoConfirmed || decision.Status == StageStatusUserConfirmed
}

// confirmedBlueprintContext projects the session's confirmed stages into planner
// context. It returns nil when nothing is confirmed yet.
func confirmedBlueprintContext(state ModelingSessionState) *promptBlueprintContext {
	if state.Blueprint == nil || state.Blueprint.Phase == BlueprintPhaseBusiness {
		return nil
	}
	result := promptBlueprintContext{}
	bindings := map[string]MetricBinding{}
	populated := false
	for _, decision := range state.Blueprint.Stages {
		if !stageConfirmed(decision) || decision.Stage != StageMetricBinding {
			continue
		}
		for _, binding := range decision.Bindings {
			bindings[binding.MetricID] = binding
		}
	}
	for _, decision := range state.Blueprint.Stages {
		if !stageConfirmed(decision) {
			continue
		}
		switch decision.Stage {
		case StageGrain:
			if decision.Grain != nil {
				grain := *decision.Grain
				result.Grain = &grain
				populated = true
			}
		case StageMetricDefinition:
			for _, metric := range decision.Metrics {
				item := promptMetricContext{ID: metric.ID, Name: metric.Name, Definition: metric.Definition}
				if binding, ok := bindings[metric.ID]; ok {
					copied := binding
					item.Binding = &copied
				}
				result.Metrics = append(result.Metrics, item)
				populated = true
			}
		case StageJoin:
			for _, join := range decision.Joins {
				result.Joins = append(result.Joins, promptJoinContext{
					ID: join.ID, LeftTableID: join.LeftTableID, RightTableID: join.RightTableID,
					JoinType: join.JoinType, Keys: append([]JoinKey(nil), join.Keys...), Cardinality: join.Cardinality,
				})
				populated = true
			}
		case StageTransform:
			result.Transforms = append(result.Transforms, decision.Transforms...)
			populated = populated || len(decision.Transforms) > 0
		case StageFilter:
			result.Filters = append(result.Filters, decision.Filters...)
			populated = populated || len(decision.Filters) > 0
		case StageOutput:
			result.Outputs = append(result.Outputs, decision.Outputs...)
			populated = populated || len(decision.Outputs) > 0
		}
	}
	if !populated {
		return nil
	}
	return &result
}

// validateBlueprintCompliance proves a CREATE proposal realises every confirmed
// blueprint decision: each join exists between the same tables on the same keys
// with the same type; each bound metric is aggregated, passed through, or derived
// exactly as confirmed; each transform uses the confirmed operation and physical
// inputs; each filter condition exists; each output code reaches END. Anything else is a BLUEPRINT_VIOLATION,
// which the repair round can fix because the message names the missing item.
func validateBlueprintCompliance(plan GraphPlan, blueprint *promptBlueprintContext) error {
	if blueprint == nil {
		return nil
	}
	nodeTable := map[string]string{}
	for _, node := range plan.Nodes {
		nodeTable[node.ID] = node.TableID
	}
	transforms := map[string]PlanTransform{}
	for _, transform := range plan.Transforms {
		transforms[transform.ID] = transform
	}
	// origin resolves a plan field key to the physical (tableId, column) it derives
	// from, following transform outputs to their first physical input.
	var origin func(nodeID, column string, depth int) (FieldRef, bool)
	origin = func(nodeID, column string, depth int) (FieldRef, bool) {
		if depth > 8 {
			return FieldRef{}, false
		}
		if tableID, ok := nodeTable[nodeID]; ok {
			return FieldRef{TableID: tableID, Column: column}, true
		}
		transform, ok := transforms[nodeID]
		if !ok {
			return FieldRef{}, false
		}
		for _, rule := range transform.Rules {
			if rule.Output.ID != column {
				continue
			}
			for _, inputKey := range rule.InputKeys {
				parts := strings.SplitN(inputKey, ".", 2)
				if len(parts) != 2 {
					continue
				}
				if resolved, ok := origin(parts[0], parts[1], depth+1); ok {
					return resolved, true
				}
			}
		}
		return FieldRef{}, false
	}
	violation := func(format string, args ...any) error {
		return invalidOutputWithReason(InvalidOutputReasonBlueprint, "plan does not realize the confirmed blueprint: "+fmt.Sprintf(format, args...))
	}

	for _, expected := range blueprint.Joins {
		matched := false
		for _, join := range plan.Joins {
			if len(join.Conditions) == 0 || !strings.EqualFold(join.JoinType, expected.JoinType) && expected.JoinType != "" {
				continue
			}
			pairs := map[string]bool{}
			for _, condition := range join.Conditions {
				left, leftOK := origin(condition.LeftNodeID, condition.LeftColumn, 0)
				right, rightOK := origin(condition.RightNodeID, condition.RightColumn, 0)
				if !leftOK || !rightOK {
					continue
				}
				pairs[left.TableID+"."+left.Column+"="+right.TableID+"."+right.Column] = true
				pairs[right.TableID+"."+right.Column+"="+left.TableID+"."+left.Column] = true
			}
			all := true
			for _, key := range expected.Keys {
				if !pairs[expected.LeftTableID+"."+key.LeftColumn+"="+expected.RightTableID+"."+key.RightColumn] {
					all = false
					break
				}
			}
			if all {
				matched = true
				break
			}
		}
		if !matched {
			return violation("confirmed join %s (%s ↔ %s, %s) is missing or uses different keys", expected.ID, expected.LeftTableID, expected.RightTableID, expected.JoinType)
		}
	}

	for _, metric := range blueprint.Metrics {
		if metric.Binding == nil {
			continue
		}
		expected := *metric.Binding
		mode := firstNonEmpty(expected.Mode, MetricBindingModeAggregate)
		matched := false
		switch mode {
		case MetricBindingModeAggregate:
			for _, group := range plan.Groups {
				for _, item := range group.Metrics {
					if item.Aggregation != expected.Aggregation {
						continue
					}
					resolved, ok := origin(item.NodeID, item.Column, 0)
					if ok && resolved.TableID == expected.TableID && resolved.Column == expected.Column {
						matched = true
					}
				}
			}
		case MetricBindingModePassthrough:
			for _, output := range plan.End.Outputs {
				if !metricOutputMatches(blueprint.Outputs, metric.ID, output.Code) {
					continue
				}
				// A pass-through output retains the physical node id. A transform
				// output is not accepted merely because it has the same first origin.
				if tableID, ok := nodeTable[output.NodeID]; ok && tableID == expected.TableID && output.Column == expected.Column {
					matched = true
				}
			}
		case MetricBindingModeDerived:
			for _, output := range plan.End.Outputs {
				if !metricOutputMatches(blueprint.Outputs, metric.ID, output.Code) {
					continue
				}
				transform, ok := transforms[output.NodeID]
				if !ok || transform.ComponentType != "NUMBER_ARITHMETIC" {
					continue
				}
				for _, rule := range transform.Rules {
					if rule.Output.ID != output.Column || rule.Operation != expected.Operation || len(rule.InputKeys) != len(expected.Inputs) {
						continue
					}
					all := true
					for index, inputKey := range rule.InputKeys {
						parts := strings.SplitN(inputKey, ".", 2)
						if len(parts) != 2 {
							all = false
							break
						}
						resolved, ok := origin(parts[0], parts[1], 0)
						if !ok || resolved != expected.Inputs[index] {
							all = false
							break
						}
					}
					if all {
						matched = true
					}
				}
			}
		}
		if !matched {
			switch mode {
			case MetricBindingModePassthrough:
				return violation("confirmed ADS metric %s must pass through %s.%s to its END output", metric.ID, expected.TableID, expected.Column)
			case MetricBindingModeDerived:
				return violation("confirmed ADS metric %s must use %s on its ordered inputs and reach its END output", metric.ID, expected.Operation)
			default:
				return violation("confirmed metric %s must be %s(%s.%s) in a GROUP", metric.ID, expected.Aggregation, expected.TableID, expected.Column)
			}
		}
	}

	for _, expected := range blueprint.Transforms {
		matched := false
		for _, transform := range plan.Transforms {
			if transform.ComponentType != expected.ComponentType {
				continue
			}
			for _, rule := range transform.Rules {
				if expected.Operation != "" && rule.Operation != expected.Operation {
					continue
				}
				actual := []FieldRef{}
				for _, inputKey := range rule.InputKeys {
					parts := strings.SplitN(inputKey, ".", 2)
					if len(parts) != 2 {
						continue
					}
					if resolved, ok := origin(parts[0], parts[1], 0); ok {
						actual = append(actual, resolved)
					}
				}
				if sameOrderedFieldRefs(actual, expected.Inputs) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return violation("confirmed transform %s/%s on the selected inputs is missing", expected.ComponentType, expected.Operation)
		}
	}

	for _, expected := range blueprint.Filters {
		matched := false
		for _, transform := range plan.Transforms {
			if transform.ComponentType != "FILTER" {
				continue
			}
			for _, condition := range transform.Conditions {
				parts := strings.SplitN(condition.InputKey, ".", 2)
				if len(parts) != 2 || condition.Operator != expected.Operator {
					continue
				}
				resolved, ok := origin(parts[0], parts[1], 0)
				if !ok || resolved.TableID != expected.TableID || resolved.Column != expected.Column {
					continue
				}
				if oneOf(expected.Operator, "IS_NULL", "IS_NOT_NULL") || strings.EqualFold(condition.Value, expected.Value) {
					matched = true
				}
			}
		}
		if !matched {
			return violation("confirmed filter %s.%s %s %q is missing; express it with a FILTER transform", expected.TableID, expected.Column, expected.Operator, expected.Value)
		}
	}

	if len(blueprint.Outputs) > 0 {
		codes := map[string]bool{}
		for _, output := range plan.End.Outputs {
			codes[strings.ToLower(output.Code)] = true
		}
		for _, expected := range blueprint.Outputs {
			if !codes[strings.ToLower(expected.Code)] {
				return violation("confirmed output %s (%s) is missing from END", expected.Code, expected.Name)
			}
		}
	}
	return nil
}

// validateModelKindPlan closes the gap between a complete, structurally valid
// graph and the semantic shape promised by the selected warehouse layer.
func metricOutputMatches(outputs []OutputDecision, metricID, code string) bool {
	for _, output := range outputs {
		if output.MetricID == metricID && strings.EqualFold(output.Code, code) {
			return true
		}
	}
	return false
}

func sameOrderedFieldRefs(left, right []FieldRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateModelKindPlan(plan GraphPlan, modelKind string, blueprint *promptBlueprintContext) error {
	switch strings.ToUpper(strings.TrimSpace(modelKind)) {
	case "DIM", "DWD":
		if len(plan.Groups) > 0 {
			return invalidOutputWithReason(InvalidOutputReasonGroup, fmt.Sprintf("%s is a non-aggregating model and cannot contain GROUP components", modelKind))
		}
	case "DWS":
		if len(plan.Groups) == 0 {
			return invalidOutputWithReason(InvalidOutputReasonGroup, fmt.Sprintf("%s requires at least one GROUP component to realize its metric bindings", modelKind))
		}
	case "ADS":
		requiresGroup := false
		if blueprint != nil {
			for _, metric := range blueprint.Metrics {
				if metric.Binding != nil && firstNonEmpty(metric.Binding.Mode, MetricBindingModeAggregate) == MetricBindingModeAggregate {
					requiresGroup = true
					break
				}
			}
		}
		if requiresGroup && len(plan.Groups) == 0 {
			return invalidOutputWithReason(InvalidOutputReasonGroup, "ADS contains aggregate metric bindings and requires at least one GROUP component")
		}
	}
	return nil
}
