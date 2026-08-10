package compiler

import (
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

type ComponentIndex struct {
	ComponentID      askdata.ID         `json:"componentId"`
	ComponentType    string             `json:"componentType"`
	ComponentVersion string             `json:"componentVersion"`
	PageID           askdata.ID         `json:"pageId"`
	SectionID        askdata.ID         `json:"sectionId"`
	BlockID          askdata.ID         `json:"blockId"`
	SlotID           askdata.ID         `json:"slotId"`
	BindingMode      report.BindingMode `json:"bindingMode,omitempty"`
}

type DependencyIndex struct {
	DependencyType string       `json:"dependencyType"`
	DependencyID   string       `json:"dependencyId"`
	ComponentIDs   []askdata.ID `json:"componentIds"`
}

type Indexes struct {
	Components   []ComponentIndex  `json:"components"`
	Dependencies []DependencyIndex `json:"dependencies"`
}

// BuildIndexes derives the complete queryable index projection from a Report
// Definition. It has no storage side effects and therefore supports repair and
// consistency checks using the exact same implementation as normal writes.
func BuildIndexes(definition report.ReportDefinition) Indexes {
	componentsByID := make(map[askdata.ID]report.Component, len(definition.Components))
	for _, component := range definition.Components {
		componentsByID[component.ID] = component
	}
	contextByID := make(map[askdata.ID]report.DataContext, len(definition.DataContexts))
	for _, dataContext := range definition.DataContexts {
		contextByID[dataContext.ID] = dataContext
	}
	result := Indexes{Components: []ComponentIndex{}, Dependencies: []DependencyIndex{}}
	dependencyComponents := map[string]map[askdata.ID]struct{}{}
	addDependency := func(dependencyType, dependencyID string, componentID askdata.ID) {
		if strings.TrimSpace(dependencyID) == "" {
			return
		}
		key := dependencyType + "\x00" + dependencyID
		if dependencyComponents[key] == nil {
			dependencyComponents[key] = map[askdata.ID]struct{}{}
		}
		if componentID != "" {
			dependencyComponents[key][componentID] = struct{}{}
		}
	}

	addDependency("REPORT_TEMPLATE", string(definition.TemplateRef.ReportTemplateID)+"@"+definition.TemplateRef.ReportTemplateVersion, "")
	addDependency("STRUCTURE_TEMPLATE", definition.TemplateRef.StructureTemplateVersion, "")
	addDependency("LAYOUT_TEMPLATE", definition.TemplateRef.LayoutTemplateVersion, "")
	addDependency("NARRATIVE_TEMPLATE", definition.TemplateRef.NarrativeTemplateVersion, "")
	addDependency("THEME", string(definition.ThemeRef.ThemeID)+"@"+definition.ThemeRef.Version, "")
	for _, reference := range definition.Provenance.AnalysisMethodVersions {
		addDependency("ANALYSIS_METHOD", reference.AnalysisMethod+"@"+reference.Version, "")
	}
	for _, version := range definition.Provenance.PromptVersions {
		addDependency("PROMPT_VERSION", version, "")
	}
	for _, policy := range definition.Provenance.ModelPolicies {
		addDependency("MODEL_POLICY", policy, "")
	}
	// Data contexts and report-level filters are definition dependencies even
	// when no currently placed component consumes them. Keeping them in the
	// projection makes impact analysis complete and lets repair rebuild the
	// exact same rows from the immutable definition.
	for _, dataContext := range definition.DataContexts {
		addDependency("DATASET_VERSION", string(dataContext.DatasetVersionID), "")
	}

	for _, page := range definition.Pages {
		for _, section := range page.Sections {
			for _, block := range section.Blocks {
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						component, exists := componentsByID[slot.ComponentID]
						if !exists {
							continue
						}
						bindingMode := report.BindingMode("")
						if component.DataBinding != nil {
							bindingMode = component.DataBinding.BindingMode
						}
						result.Components = append(result.Components, ComponentIndex{
							ComponentID: component.ID, ComponentType: component.TemplateRef.Type,
							ComponentVersion: component.TemplateRef.Version, PageID: page.ID,
							SectionID: section.ID, BlockID: block.ID, SlotID: slot.ID, BindingMode: bindingMode,
						})
						addDependency("COMPONENT_TEMPLATE", component.TemplateRef.Type+"@"+component.TemplateRef.Version, component.ID)
						if component.DataBinding == nil {
							continue
						}
						switch component.DataBinding.BindingMode {
						case report.BindingDatasetField:
							if component.DataBinding.DataContextID != nil {
								if dataContext, found := contextByID[*component.DataBinding.DataContextID]; found {
									addDependency("DATASET_VERSION", string(dataContext.DatasetVersionID), component.ID)
								}
							}
						case report.BindingSemanticIR:
							if component.DataBinding.SemanticQueryRef == nil {
								continue
							}
							reference := component.DataBinding.SemanticQueryRef
							addDependency("SEMANTIC_RELEASE", string(reference.SemanticReleaseID), component.ID)
							if reference.DatasetVersionID != nil {
								addDependency("DATASET_VERSION", string(*reference.DatasetVersionID), component.ID)
							}
							for _, metric := range reference.SemanticIR.Metrics {
								addDependency("METRIC_VERSION", string(metric.MetricVersionID), component.ID)
							}
							for _, group := range reference.SemanticIR.GroupBy {
								addDependency("DIMENSION_VERSION", string(group.DimensionVersionID), component.ID)
							}
							for _, filter := range reference.SemanticIR.Filters {
								addDependency("DIMENSION_VERSION", string(filter.DimensionVersionID), component.ID)
								for _, memberID := range filter.MemberVersionIDs {
									addDependency("MEMBER_VERSION", string(memberID), component.ID)
								}
							}
							if reference.SemanticIR.TimeRange != nil {
								addDependency("DIMENSION_VERSION", string(reference.SemanticIR.TimeRange.DimensionVersionID), component.ID)
							}
						}
					}
				}
			}
		}
	}

	sort.Slice(result.Components, func(i, j int) bool { return result.Components[i].ComponentID < result.Components[j].ComponentID })
	keys := make([]string, 0, len(dependencyComponents))
	for key := range dependencyComponents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		componentIDs := make([]askdata.ID, 0, len(dependencyComponents[key]))
		for componentID := range dependencyComponents[key] {
			componentIDs = append(componentIDs, componentID)
		}
		sort.Slice(componentIDs, func(i, j int) bool { return componentIDs[i] < componentIDs[j] })
		result.Dependencies = append(result.Dependencies, DependencyIndex{DependencyType: parts[0], DependencyID: parts[1], ComponentIDs: componentIDs})
	}
	return result
}
