package compiler

import (
	"encoding/json"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ValidationIssues []ValidationIssue

func (issues ValidationIssues) Error() string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Code+" "+issue.Path+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

// ValidateDefinition keeps validation stages ordered. A stage may accumulate
// all of its own findings, but later stages do not run after an earlier stage
// fails because their assumptions would no longer be trustworthy.
func ValidateDefinition(definition report.ReportDefinition, registry *template.Registry) ValidationIssues {
	if issues := validateUniqueIDs(definition); len(issues) != 0 {
		return issues
	}
	if issues := validateReferences(definition); len(issues) != 0 {
		return issues
	}
	if layout := ValidateLayout(definition); len(layout) != 0 {
		issues := make(ValidationIssues, len(layout))
		for index, item := range layout {
			issues[index] = ValidationIssue{Code: item.Code, Path: item.Path, Message: item.Message}
		}
		return issues
	}
	if err := definition.Validate(); err != nil {
		return ValidationIssues{{Code: "REPORT_DEFINITION_INVALID", Path: "$", Message: err.Error()}}
	}
	if registry == nil {
		var err error
		registry, err = defaultRegistry()
		if err != nil {
			return ValidationIssues{{Code: "REPORT_MANIFEST_REGISTRY_UNAVAILABLE", Path: "components", Message: err.Error()}}
		}
	}
	sizes := componentRenderSizes(definition)
	issues := ValidationIssues{}
	for index, component := range definition.Components {
		size := sizes[string(component.ID)]
		if size.W == 0 {
			size.W, size.H = 1, 1
		}
		if err := registry.ValidateComponentSchema(component, size.W, size.H); err != nil {
			issues = append(issues, ValidationIssue{Code: "REPORT_COMPONENT_INVALID", Path: fmt.Sprintf("components[%d]", index), Message: err.Error()})
		}
		if _, err := json.Marshal(component.Options); err != nil {
			issues = append(issues, ValidationIssue{Code: "REPORT_COMPONENT_OPTIONS_INVALID", Path: fmt.Sprintf("components[%d].options", index), Message: err.Error()})
		}
	}
	if len(issues) != 0 {
		return issues
	}
	for index, component := range definition.Components {
		if err := registry.ValidateComponentData(component); err != nil {
			issues = append(issues, ValidationIssue{
				Code: "REPORT_COMPONENT_DATA_INVALID", Path: fmt.Sprintf("components[%d].dataBinding", index),
				Message: err.Error(),
			})
		}
	}
	if len(issues) != 0 {
		return issues
	}
	issues = append(issues, ValidateInteractions(definition, registry)...)
	return issues
}

func validateUniqueIDs(definition report.ReportDefinition) ValidationIssues {
	issues := ValidationIssues{}
	contexts := map[askdata.ID]string{}
	components := map[askdata.ID]string{}
	pages := map[askdata.ID]string{}
	sections := map[askdata.ID]string{}
	blocks := map[askdata.ID]string{}
	zones := map[askdata.ID]string{}
	slots := map[askdata.ID]string{}
	filters := map[askdata.ID]string{}
	interactions := map[askdata.ID]string{}
	add := func(namespace map[askdata.ID]string, id askdata.ID, path string) {
		if previous, exists := namespace[id]; exists {
			issues = append(issues, ValidationIssue{
				Code: "REPORT_ID_DUPLICATE", Path: path,
				Message: fmt.Sprintf("ID %q duplicates %s", id, previous),
			})
			return
		}
		namespace[id] = path
	}
	for index, context := range definition.DataContexts {
		add(contexts, context.ID, fmt.Sprintf("dataContexts[%d].id", index))
	}
	for index, component := range definition.Components {
		add(components, component.ID, fmt.Sprintf("components[%d].id", index))
	}
	for pageIndex, page := range definition.Pages {
		add(pages, page.ID, fmt.Sprintf("pages[%d].id", pageIndex))
		for sectionIndex, section := range page.Sections {
			sectionPath := fmt.Sprintf("pages[%d].sections[%d]", pageIndex, sectionIndex)
			add(sections, section.ID, sectionPath+".id")
			for blockIndex, block := range section.Blocks {
				blockPath := fmt.Sprintf("%s.blocks[%d]", sectionPath, blockIndex)
				add(blocks, block.ID, blockPath+".id")
				for zoneIndex, zone := range block.Zones {
					zonePath := fmt.Sprintf("%s.zones[%d]", blockPath, zoneIndex)
					add(zones, zone.ID, zonePath+".id")
					for slotIndex, slot := range zone.Slots {
						add(slots, slot.ID, fmt.Sprintf("%s.slots[%d].id", zonePath, slotIndex))
					}
				}
			}
		}
	}
	for index, filter := range definition.GlobalFilters {
		add(filters, filter.ID, fmt.Sprintf("globalFilters[%d].id", index))
	}
	for index, interaction := range definition.Interactions {
		add(interactions, interaction.ID, fmt.Sprintf("interactions[%d].id", index))
	}
	return issues
}

func validateReferences(definition report.ReportDefinition) ValidationIssues {
	issues := ValidationIssues{}
	contexts := map[askdata.ID]struct{}{}
	components := map[askdata.ID]struct{}{}
	pages := map[askdata.ID]struct{}{}
	sections := map[askdata.ID]struct{}{}
	blocks := map[askdata.ID]struct{}{}
	placements := map[askdata.ID]int{}
	for _, context := range definition.DataContexts {
		contexts[context.ID] = struct{}{}
	}
	for _, component := range definition.Components {
		components[component.ID] = struct{}{}
	}
	for _, page := range definition.Pages {
		pages[page.ID] = struct{}{}
		for _, section := range page.Sections {
			sections[section.ID] = struct{}{}
			for _, block := range section.Blocks {
				blocks[block.ID] = struct{}{}
			}
		}
	}
	for pageIndex, page := range definition.Pages {
		for sectionIndex, section := range page.Sections {
			for blockIndex, block := range section.Blocks {
				for zoneIndex, zone := range block.Zones {
					for slotIndex, slot := range zone.Slots {
						if slot.ComponentID == "" {
							continue
						}
						path := fmt.Sprintf("pages[%d].sections[%d].blocks[%d].zones[%d].slots[%d].componentId",
							pageIndex, sectionIndex, blockIndex, zoneIndex, slotIndex)
						if _, exists := components[slot.ComponentID]; !exists {
							issues = append(issues, missingReference(path, "component", slot.ComponentID))
							continue
						}
						placements[slot.ComponentID]++
					}
				}
			}
		}
	}
	for index, component := range definition.Components {
		if placements[component.ID] != 1 {
			issues = append(issues, ValidationIssue{
				Code: "REPORT_COMPONENT_PLACEMENT_INVALID", Path: fmt.Sprintf("components[%d].id", index),
				Message: fmt.Sprintf("component %q must be placed exactly once; found %d", component.ID, placements[component.ID]),
			})
		}
		if component.DataBinding != nil && component.DataBinding.DataContextID != nil {
			if _, exists := contexts[*component.DataBinding.DataContextID]; !exists {
				issues = append(issues, missingReference(
					fmt.Sprintf("components[%d].dataBinding.dataContextId", index),
					"data context", *component.DataBinding.DataContextID,
				))
			}
		}
	}
	for index, filter := range definition.GlobalFilters {
		path := fmt.Sprintf("globalFilters[%d]", index)
		if _, exists := contexts[filter.FieldRef.DataContextID]; !exists {
			issues = append(issues, missingReference(path+".fieldRef.dataContextId", "data context", filter.FieldRef.DataContextID))
		}
		var targets map[askdata.ID]struct{}
		switch filter.Scope.Type {
		case report.FilterScopePage:
			targets = pages
		case report.FilterScopeSection:
			targets = sections
		case report.FilterScopeBlock:
			targets = blocks
		case report.FilterScopeComponent:
			targets = components
		}
		seen := map[askdata.ID]struct{}{}
		for targetIndex, targetID := range filter.Scope.TargetIDs {
			targetPath := fmt.Sprintf("%s.scope.targetIds[%d]", path, targetIndex)
			if _, duplicate := seen[targetID]; duplicate {
				issues = append(issues, ValidationIssue{Code: "REPORT_REFERENCE_DUPLICATE", Path: targetPath, Message: fmt.Sprintf("target %q is duplicated", targetID)})
			}
			seen[targetID] = struct{}{}
			if targets != nil {
				if _, exists := targets[targetID]; !exists {
					issues = append(issues, missingReference(targetPath, "scope target", targetID))
				}
			}
		}
	}
	for index, interaction := range definition.Interactions {
		path := fmt.Sprintf("interactions[%d]", index)
		if _, exists := components[interaction.SourceComponentID]; !exists {
			issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_TARGET_MISSING", Path: path + ".sourceComponentId", Message: fmt.Sprintf("source component %q does not exist", interaction.SourceComponentID)})
		}
		seen := map[askdata.ID]struct{}{}
		for targetIndex, targetID := range interaction.TargetComponentIDs {
			targetPath := fmt.Sprintf("%s.targetComponentIds[%d]", path, targetIndex)
			if _, duplicate := seen[targetID]; duplicate {
				issues = append(issues, ValidationIssue{Code: "REPORT_REFERENCE_DUPLICATE", Path: targetPath, Message: fmt.Sprintf("target %q is duplicated", targetID)})
			}
			seen[targetID] = struct{}{}
			if _, exists := components[targetID]; !exists {
				issues = append(issues, ValidationIssue{Code: "REPORT_INTERACTION_TARGET_MISSING", Path: targetPath, Message: fmt.Sprintf("target component %q does not exist", targetID)})
			}
		}
		if interaction.TargetPageID != nil {
			if _, exists := pages[*interaction.TargetPageID]; !exists {
				issues = append(issues, missingReference(path+".targetPageId", "page", *interaction.TargetPageID))
			}
		}
	}
	return issues
}

func missingReference(path, kind string, id askdata.ID) ValidationIssue {
	return ValidationIssue{
		Code: "REPORT_REFERENCE_MISSING", Path: path,
		Message: fmt.Sprintf("%s %q does not exist", kind, id),
	}
}

type gridSize struct{ W, H int }

func componentRenderSizes(definition report.ReportDefinition) map[string]gridSize {
	result := map[string]gridSize{}
	for _, page := range definition.Pages {
		for _, section := range page.Sections {
			for _, block := range section.Blocks {
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						if slot.ComponentID == "" {
							continue
						}
						current := result[string(slot.ComponentID)]
						width, height := block.Layout.Desktop.W, block.Layout.Desktop.H
						if width*height > current.W*current.H {
							result[string(slot.ComponentID)] = gridSize{width, height}
						}
					}
				}
			}
		}
	}
	return result
}
