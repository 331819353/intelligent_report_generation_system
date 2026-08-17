package datasetai

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// MODIFY re-entry (docs/10 §2.3): a modification turn does not regenerate the
// blueprint, but it must not leave it stale either. The stage payloads that a
// graph determines — joins, metric bindings, transforms, filters, outputs — are
// derived deterministically from the current graph and from the proposal; the
// stages whose derivation changed are the ones the modification touched. Those
// updates ride on the proposal record and are applied to the blueprint only when
// the user applies the proposal, so a discarded proposal changes nothing. Grain
// cannot be derived from a graph, so a change of grouping dimensions reopens it
// for the user instead of guessing.

const DecisionSourceDerived = "DERIVED"

// planStageDecisions derives the graph-determined stage payloads of a plan.
// Physical origins are resolved through transforms so a metric bound to a
// derived column still reports the column it comes from.
func planStageDecisions(plan GraphPlan) map[string]StageDecision {
	nodeTable := map[string]string{}
	for _, node := range plan.Nodes {
		nodeTable[node.ID] = node.TableID
	}
	transforms := map[string]PlanTransform{}
	for _, transform := range plan.Transforms {
		transforms[transform.ID] = transform
	}
	groups := map[string]PlanGroup{}
	for _, group := range plan.Groups {
		groups[group.ID] = group
	}
	joins := map[string]PlanJoin{}
	for _, join := range plan.Joins {
		joins[join.ID] = join
	}
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
	keyOrigin := func(key string) (FieldRef, bool) {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			return FieldRef{}, false
		}
		return origin(parts[0], parts[1], 0)
	}
	// reaches reports whether walking upstream from input passes through the
	// component (kind, id).
	var reaches func(input PlanInput, kind, id string, depth int) bool
	reaches = func(input PlanInput, kind, id string, depth int) bool {
		if depth > 64 {
			return false
		}
		if input.Kind == kind && input.ID == id {
			return true
		}
		switch input.Kind {
		case "JOIN":
			join, ok := joins[input.ID]
			return ok && (reaches(join.Left, kind, id, depth+1) || reaches(join.Right, kind, id, depth+1))
		case "GROUP":
			group, ok := groups[input.ID]
			return ok && reaches(group.Input, kind, id, depth+1)
		case "TRANSFORM":
			transform, ok := transforms[input.ID]
			return ok && reaches(transform.Input, kind, id, depth+1)
		}
		return false
	}
	now := time.Time{}
	derived := func(stage string) StageDecision {
		return StageDecision{Stage: stage, Status: StageStatusUserConfirmed, Source: DecisionSourceDerived, Confidence: 1, DecidedAt: now}
	}
	result := map[string]StageDecision{}

	joinDecision := derived(StageJoin)
	for _, join := range plan.Joins {
		if len(join.Conditions) == 0 {
			continue
		}
		left, leftOK := origin(join.Conditions[0].LeftNodeID, join.Conditions[0].LeftColumn, 0)
		right, rightOK := origin(join.Conditions[0].RightNodeID, join.Conditions[0].RightColumn, 0)
		if !leftOK || !rightOK {
			continue
		}
		item := JoinDecision{ID: join.ID, LeftTableID: left.TableID, RightTableID: right.TableID, JoinType: strings.ToUpper(join.JoinType), Provenance: DecisionSourceDerived, Cardinality: "UNKNOWN"}
		if item.JoinType != "INNER" && item.JoinType != "LEFT" {
			item.JoinType = "LEFT"
		}
		for _, condition := range join.Conditions {
			leftField, leftOK := origin(condition.LeftNodeID, condition.LeftColumn, 0)
			rightField, rightOK := origin(condition.RightNodeID, condition.RightColumn, 0)
			if leftOK && rightOK {
				item.Keys = append(item.Keys, JoinKey{LeftColumn: leftField.Column, RightColumn: rightField.Column})
			}
		}
		if len(item.Keys) > 0 {
			joinDecision.Joins = append(joinDecision.Joins, item)
		}
	}
	result[StageJoin] = joinDecision

	metricNames := map[string]string{}
	for _, output := range plan.End.Outputs {
		key := output.Key
		if key == "" {
			key = fieldKey(output.NodeID, output.Column)
		}
		metricNames[key] = output.Name
	}
	definitions := derived(StageMetricDefinition)
	bindings := derived(StageMetricBinding)
	metricIDs := map[string]bool{}
	for _, group := range plan.Groups {
		for _, metric := range group.Metrics {
			resolved, ok := origin(metric.NodeID, metric.Column, 0)
			if !ok {
				continue
			}
			id := sanitizeIdentifier("m_" + resolved.Column + "_" + strings.ToLower(metric.Aggregation))
			if metricIDs[id] {
				continue
			}
			metricIDs[id] = true
			name := metricNames[fieldKey(metric.NodeID, metric.Column)]
			if name == "" {
				name = resolved.Column
			}
			definitions.Metrics = append(definitions.Metrics, MetricDefinition{ID: id, Name: name, Definition: fmt.Sprintf("%s(%s)", metric.Aggregation, resolved.Column), Origin: MetricOriginNew})
			bindings.Bindings = append(bindings.Bindings, MetricBinding{MetricID: id, Mode: MetricBindingModeAggregate, TableID: resolved.TableID, Column: resolved.Column, Aggregation: metric.Aggregation, Distinct: metric.Aggregation == "COUNT_DISTINCT"})
		}
	}
	result[StageMetricDefinition] = definitions
	result[StageMetricBinding] = bindings

	transformDecision := derived(StageTransform)
	filterDecision := derived(StageFilter)
	for _, transform := range plan.Transforms {
		if transform.ComponentType == "FILTER" {
			for _, condition := range transform.Conditions {
				field, ok := keyOrigin(condition.InputKey)
				if !ok {
					continue
				}
				filterDecision.Filters = append(filterDecision.Filters, FilterDecision{TableID: field.TableID, Column: field.Column, Operator: condition.Operator, Value: condition.Value, ValueMode: firstNonEmpty(condition.ValueMode, "LITERAL")})
			}
			continue
		}
		placement := TransformPlacementAfterGroup
		for _, group := range plan.Groups {
			if reaches(group.Input, "TRANSFORM", transform.ID, 0) {
				placement = TransformPlacementBeforeGroup
				break
			}
		}
		for _, rule := range transform.Rules {
			inputs := []FieldRef{}
			for _, inputKey := range rule.InputKeys {
				field, ok := keyOrigin(inputKey)
				if !ok {
					continue
				}
				inputs = append(inputs, field)
			}
			if len(inputs) == 0 {
				continue
			}
			transformDecision.Transforms = append(transformDecision.Transforms, TransformDecision{
				ComponentType: transform.ComponentType, Operation: rule.Operation, Inputs: inputs,
				Description: transform.Name, Placement: placement,
			})
		}
	}
	result[StageTransform] = transformDecision
	result[StageFilter] = filterDecision

	outputDecision := derived(StageOutput)
	metricByField := map[string]string{}
	for _, group := range plan.Groups {
		for _, metric := range group.Metrics {
			if resolved, ok := origin(metric.NodeID, metric.Column, 0); ok {
				metricByField[fieldKey(metric.NodeID, metric.Column)] = sanitizeIdentifier("m_" + resolved.Column + "_" + strings.ToLower(metric.Aggregation))
			}
		}
	}
	for _, output := range plan.End.Outputs {
		key := output.Key
		if key == "" {
			key = fieldKey(output.NodeID, output.Column)
		}
		item := OutputDecision{Name: output.Name, Code: output.Code}
		if metricID, ok := metricByField[key]; ok {
			item.MetricID = metricID
		} else if field, ok := keyOrigin(key); ok {
			item.Source = &FieldRef{TableID: field.TableID, Column: field.Column}
		} else {
			continue
		}
		outputDecision.Outputs = append(outputDecision.Outputs, item)
	}
	result[StageOutput] = outputDecision
	return result
}

// planGrainSignature is the set of grouping dimensions (by physical origin); a
// change means the output grain changed and GRAIN must be re-confirmed.
func planGrainSignature(plan GraphPlan) string {
	nodeTable := map[string]string{}
	for _, node := range plan.Nodes {
		nodeTable[node.ID] = node.TableID
	}
	keys := []string{}
	for _, group := range plan.Groups {
		for _, dimension := range group.Dimensions {
			keys = append(keys, nodeTable[dimension.NodeID]+"."+dimension.Column)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// deriveBlueprintResync compares the graph-determined stages of the current
// canvas with the proposal and returns the stage decisions to write when the
// proposal is applied plus the stages that must be reopened.
func deriveBlueprintResync(current, proposal GraphPlan, blueprint *ModelingBlueprint) ([]StageDecision, []string) {
	if blueprint == nil {
		return nil, nil
	}
	before := planStageDecisions(current)
	after := planStageDecisions(proposal)
	updates := []StageDecision{}
	for _, stage := range []string{StageJoin, StageMetricDefinition, StageMetricBinding, StageTransform, StageFilter, StageOutput} {
		if reflect.DeepEqual(stagePayload(before[stage]), stagePayload(after[stage])) {
			continue
		}
		updates = append(updates, after[stage])
	}
	reopen := []string{}
	if planGrainSignature(current) != planGrainSignature(proposal) {
		reopen = append(reopen, StageGrain)
	}
	return updates, reopen
}

func stagePayload(decision StageDecision) any {
	switch decision.Stage {
	case StageJoin:
		return decision.Joins
	case StageMetricDefinition:
		return decision.Metrics
	case StageMetricBinding:
		return decision.Bindings
	case StageTransform:
		return decision.Transforms
	case StageFilter:
		return decision.Filters
	case StageOutput:
		return decision.Outputs
	case StageGrain:
		return decision.Grain
	}
	return nil
}

// ApplyProposalStageUpdates writes an applied proposal's derived stage decisions
// into the blueprint and reopens the stages the modification invalidated. Stages
// the kind does not apply are left SKIPPED; a stage whose derivation became empty
// is recorded as SKIPPED by derivation.
func (state *ModelingSessionState) ApplyProposalStageUpdates(updates []StageDecision, reopen []string, summary string, now time.Time) {
	if state.Blueprint == nil {
		return
	}
	timestamp := now.UTC()
	reason := "由已应用的修改方案同步"
	if strings.TrimSpace(summary) != "" {
		reason = "由已应用的修改方案「" + truncateRunes(strings.TrimSpace(summary), 80) + "」同步"
	}
	for _, update := range updates {
		if !StageApplicable(state.ModelKind, update.Stage) {
			continue
		}
		for index := range state.Blueprint.Stages {
			target := &state.Blueprint.Stages[index]
			if target.Stage != update.Stage {
				continue
			}
			target.Grain, target.Metrics, target.Joins, target.Bindings = update.Grain, update.Metrics, update.Joins, update.Bindings
			target.Transforms, target.Filters, target.Outputs = update.Transforms, update.Filters, update.Outputs
			empty := stagePayload(update) == nil || reflect.ValueOf(stagePayload(update)).Len() == 0
			if empty && !stageRequiredFor(state.ModelKind, update.Stage, len(state.ScopeTableIDs())) {
				target.Status = StageStatusSkipped
			} else {
				target.Status = StageStatusUserConfirmed
			}
			target.Source = DecisionSourceDerived
			target.Confidence = 1
			target.NeedsUserConfirmation = false
			target.Reason = reason
			target.DecidedAt = timestamp
		}
	}
	for _, stage := range reopen {
		for index := range state.Blueprint.Stages {
			target := &state.Blueprint.Stages[index]
			if target.Stage != stage || target.Status == StageStatusSkipped {
				continue
			}
			target.Status = StageStatusProposed
			target.NeedsUserConfirmation = true
			target.Reason = "修改方案改变了分组维度，请重新确认" + StageLabel(stage)
			target.DecidedAt = timestamp
		}
	}
}

func sanitizeIdentifier(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteRune('_')
		}
	}
	result := builder.String()
	if result == "" || !(result[0] >= 'a' && result[0] <= 'z' || result[0] >= 'A' && result[0] <= 'Z') {
		result = "m_" + result
	}
	if len(result) > 128 {
		result = result[:128]
	}
	return result
}
