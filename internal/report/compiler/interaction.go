package compiler

import (
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

// ValidateInteractions verifies manifest capabilities and prevents recursive
// interaction propagation. Structural reference checks happen earlier in the
// Report Definition validator.
func ValidateInteractions(definition report.ReportDefinition, registry *template.Registry) ValidationIssues {
	components := make(map[askdata.ID]report.Component, len(definition.Components))
	for _, component := range definition.Components {
		components[component.ID] = component
	}
	issues := ValidationIssues{}
	graph := make(map[askdata.ID][]askdata.ID)
	for index, interaction := range definition.Interactions {
		path := fmt.Sprintf("interactions[%d]", index)
		source, sourceExists := components[interaction.SourceComponentID]
		if !sourceExists {
			issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_TARGET_MISSING", Path: path + ".sourceComponentId", Message: "source component does not exist"})
			continue
		}
		eventKind := requiredEventKind(interaction.Event)
		manifest, exists := registry.Get(source.TemplateRef.Type, source.TemplateRef.Version)
		if eventKind == "" || !exists || !supportsInteraction(manifest, eventKind) {
			issues = append(issues, ValidationIssue{
				Code: "REPORT_INTERACTION_EVENT_UNSUPPORTED", Path: path + ".event",
				Message: fmt.Sprintf("component %q does not support event %s", source.ID, interaction.Event),
			})
		}
		actionKind := requiredActionKind(interaction.Action)
		for _, targetID := range interaction.TargetComponentIDs {
			target, targetExists := components[targetID]
			if !targetExists {
				issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_TARGET_MISSING", Path: path + ".targetComponentIds", Message: fmt.Sprintf("target component %q does not exist", targetID)})
				continue
			}
			graph[source.ID] = append(graph[source.ID], target.ID)
			targetManifest, targetManifestExists := registry.Get(target.TemplateRef.Type, target.TemplateRef.Version)
			if actionKind == "" || !targetManifestExists || !supportsInteraction(targetManifest, actionKind) {
				issues = append(issues, ValidationIssue{
					Code: "REPORT_INTERACTION_ACTION_UNSUPPORTED", Path: path + ".action",
					Message: fmt.Sprintf("target component %q does not support action %s", target.ID, interaction.Action),
				})
			}
			if !bindingsCompatible(source.DataBinding, target.DataBinding) && len(interaction.FieldMappings) != 0 {
				issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_FIELD_INCOMPATIBLE", Path: path + ".fieldMappings", Message: "cross-context field mappings require compatible bindings"})
			}
			// A pinned semantic query executes against a fixed plan hash and cannot
			// be narrowed at runtime. An interaction that would have to narrow one is
			// rejected here rather than silently doing nothing when a viewer clicks.
			if narrowsTarget(interaction.Action) && target.DataBinding != nil &&
				target.DataBinding.BindingMode == report.BindingSemanticIR {
				issues = append(issues, ValidationIssue{
					Code: "REPORT_INTERACTION_TARGET_PINNED", Path: path + ".targetComponentIds",
					Message: fmt.Sprintf("component %q is bound to a pinned semantic query and cannot be filtered by an interaction", target.ID),
				})
			}
		}
	}
	if hasInteractionCycle(graph) {
		issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_CYCLE", Path: "interactions", Message: "interaction graph contains a cycle"})
	}
	return issues
}

func requiredEventKind(event report.InteractionEvent) template.InteractionKind {
	switch event {
	case report.InteractionBrush:
		return template.InteractionBrush
	case report.InteractionZoom:
		return template.InteractionZoom
	case report.InteractionClick, report.InteractionSelect:
		return template.InteractionClickFilter
	default:
		return ""
	}
}

func requiredActionKind(action report.InteractionAction) template.InteractionKind {
	switch action {
	case report.InteractionFilter, report.InteractionHighlight:
		return template.InteractionClickFilter
	case report.InteractionDrillDown:
		return template.InteractionDrillDown
	case report.InteractionNavigatePage:
		// Navigation has no target component; the page reference is validated
		// in the structural stage.
		return template.InteractionClickFilter
	default:
		return ""
	}
}

// narrowsTarget reports whether an action changes the target's query rather
// than only its presentation.
func narrowsTarget(action report.InteractionAction) bool {
	return action == report.InteractionFilter || action == report.InteractionDrillDown
}

func supportsInteraction(manifest template.Manifest, required template.InteractionKind) bool {
	for _, candidate := range manifest.SupportedInteractions {
		if candidate == required {
			return true
		}
	}
	return false
}

func bindingsCompatible(left, right *report.DataBinding) bool {
	if left == nil || right == nil || left.BindingMode != right.BindingMode {
		return false
	}
	switch left.BindingMode {
	case report.BindingDatasetField:
		return left.DataContextID != nil && right.DataContextID != nil && *left.DataContextID == *right.DataContextID
	case report.BindingSemanticIR:
		return left.SemanticQueryRef != nil && right.SemanticQueryRef != nil && left.SemanticQueryRef.SemanticReleaseID == right.SemanticQueryRef.SemanticReleaseID
	default:
		return false
	}
}

func hasInteractionCycle(graph map[askdata.ID][]askdata.ID) bool {
	const (
		unseen = iota
		visiting
		done
	)
	colors := map[askdata.ID]int{}
	var visit func(askdata.ID) bool
	visit = func(node askdata.ID) bool {
		switch colors[node] {
		case visiting:
			return true
		case done:
			return false
		}
		colors[node] = visiting
		for _, target := range graph[node] {
			if visit(target) {
				return true
			}
		}
		colors[node] = done
		return false
	}
	for node := range graph {
		if colors[node] == unseen && visit(node) {
			return true
		}
	}
	return false
}
