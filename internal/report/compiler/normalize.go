package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tiendc/go-deepcopy"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

// Normalize performs the frozen Report V1 normalization order and returns the
// canonical UTF-8 JSON bytes plus their SHA-256 hash. Published definitions
// are values; this function never mutates the caller's object.
func Normalize(definition report.ReportDefinition) ([]byte, string, error) {
	return NormalizeWithRegistry(definition, nil)
}

func NormalizeWithRegistry(definition report.ReportDefinition, registry *template.Registry) ([]byte, string, error) {
	normalized, err := cloneDefinition(definition)
	if err != nil {
		return nil, "", err
	}
	normalizeStringsAndEnums(&normalized)
	if err := applyDefaults(&normalized); err != nil {
		return nil, "", err
	}
	if registry == nil {
		registry, err = defaultRegistry()
		if err != nil {
			return nil, "", fmt.Errorf("load component manifest registry: %w", err)
		}
	}
	if err := applyComponentDefaults(&normalized, registry); err != nil {
		return nil, "", err
	}
	normalizeCollections(&normalized)
	sortSemanticCollections(&normalized)
	if validation := ValidateDefinition(normalized, registry); len(validation) != 0 {
		return nil, "", validation
	}
	canonical, err := marshalCanonicalDefinition(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("encode canonical report definition: %w", err)
	}
	if len(canonical) > report.MaxDefinitionBytes {
		return nil, "", fmt.Errorf("canonical report definition exceeds %d bytes", report.MaxDefinitionBytes)
	}
	return canonical, string(askdata.HashBytes(canonical)), nil
}

func marshalCanonicalDefinition(definition report.ReportDefinition) ([]byte, error) {
	raw, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("encode report definition: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode report definition for canonical ordering: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical report definition: %w", err)
	}
	return canonical, nil
}

func cloneDefinition(definition report.ReportDefinition) (report.ReportDefinition, error) {
	var result report.ReportDefinition
	if err := deepcopy.Copy(&result, &definition); err != nil {
		return report.ReportDefinition{}, fmt.Errorf("clone report definition: %w", err)
	}
	return result, nil
}

func normalizeStringsAndEnums(definition *report.ReportDefinition) {
	definition.SchemaVersion = strings.TrimSpace(definition.SchemaVersion)
	definition.Metadata.Code = strings.TrimSpace(definition.Metadata.Code)
	definition.Metadata.Name = strings.TrimSpace(definition.Metadata.Name)
	definition.Metadata.Description = strings.TrimSpace(definition.Metadata.Description)
	definition.Metadata.Locale = strings.TrimSpace(definition.Metadata.Locale)
	definition.Metadata.ReportType = report.ReportType(strings.ToUpper(strings.TrimSpace(string(definition.Metadata.ReportType))))
	definition.TemplateRef.ReportTemplateVersion = strings.TrimSpace(definition.TemplateRef.ReportTemplateVersion)
	definition.TemplateRef.StructureTemplateVersion = strings.TrimSpace(definition.TemplateRef.StructureTemplateVersion)
	definition.TemplateRef.LayoutTemplateVersion = strings.TrimSpace(definition.TemplateRef.LayoutTemplateVersion)
	definition.TemplateRef.NarrativeTemplateVersion = strings.TrimSpace(definition.TemplateRef.NarrativeTemplateVersion)
	definition.ThemeRef.Version = strings.TrimSpace(definition.ThemeRef.Version)
	for contextIndex := range definition.DataContexts {
		context := &definition.DataContexts[contextIndex]
		context.Alias = strings.TrimSpace(context.Alias)
		for parameterIndex := range context.DefaultParameters {
			parameter := &context.DefaultParameters[parameterIndex]
			parameter.Name = strings.TrimSpace(parameter.Name)
			parameter.Type = report.ParameterType(strings.ToUpper(strings.TrimSpace(string(parameter.Type))))
			if parameter.StringValue != nil {
				value := strings.TrimSpace(*parameter.StringValue)
				parameter.StringValue = &value
			}
		}
	}
	for filterIndex := range definition.GlobalFilters {
		filter := &definition.GlobalFilters[filterIndex]
		filter.Type = report.FilterType(strings.ToUpper(strings.TrimSpace(string(filter.Type))))
		if filter.Type == report.FilterSelect {
			filter.Type = report.FilterSingleSelect
		}
		filter.FieldRef.Field = strings.TrimSpace(filter.FieldRef.Field)
		filter.Scope.Type = report.FilterScopeType(strings.ToUpper(strings.TrimSpace(string(filter.Scope.Type))))
	}
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		page.Name = strings.TrimSpace(page.Name)
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			section.Name = strings.TrimSpace(section.Name)
			for blockIndex := range section.Blocks {
				block := &section.Blocks[blockIndex]
				block.Type = report.BlockType(strings.ToUpper(strings.TrimSpace(string(block.Type))))
				block.Layout.Mobile.HeightMode = report.MobileHeightMode(strings.ToUpper(strings.TrimSpace(string(block.Layout.Mobile.HeightMode))))
				block.Layout.Mobile.SlotMode = report.MobileSlotMode(strings.ToUpper(strings.TrimSpace(string(block.Layout.Mobile.SlotMode))))
				for zoneIndex := range block.Zones {
					zone := &block.Zones[zoneIndex]
					zone.Type = report.ZoneType(strings.ToUpper(strings.TrimSpace(string(zone.Type))))
					zone.Layout.HeightMode = report.ZoneHeightMode(strings.ToUpper(strings.TrimSpace(string(zone.Layout.HeightMode))))
					zone.Layout.Overflow = report.OverflowPolicy(strings.ToUpper(strings.TrimSpace(string(zone.Layout.Overflow))))
				}
			}
		}
	}
	for componentIndex := range definition.Components {
		component := &definition.Components[componentIndex]
		component.TemplateRef.Type = strings.ToLower(strings.TrimSpace(component.TemplateRef.Type))
		component.TemplateRef.Version = strings.TrimSpace(component.TemplateRef.Version)
		component.Options.Title = strings.TrimSpace(component.Options.Title)
		component.Options.Subtitle = strings.TrimSpace(component.Options.Subtitle)
		component.Options.ColorPaletteRef = strings.TrimSpace(component.Options.ColorPaletteRef)
		component.Options.NumberFormat = strings.TrimSpace(component.Options.NumberFormat)
		component.Options.InsightRole = strings.TrimSpace(component.Options.InsightRole)
		component.Options.Orientation = report.Orientation(strings.ToUpper(strings.TrimSpace(string(component.Options.Orientation))))
		component.Options.NullPolicy = report.NullPolicy(strings.ToUpper(strings.TrimSpace(string(component.Options.NullPolicy))))
		component.Options.MobileLegendMode = report.MobileLegendMode(strings.ToUpper(strings.TrimSpace(string(component.Options.MobileLegendMode))))
		component.Options.RichText = report.SanitizeRichText(component.Options.RichText)
		if component.DataBinding != nil {
			component.DataBinding.BindingMode = report.BindingMode(strings.ToUpper(strings.TrimSpace(string(component.DataBinding.BindingMode))))
			for index := range component.DataBinding.Dimensions {
				component.DataBinding.Dimensions[index].Role = report.BindingRole(strings.ToUpper(strings.TrimSpace(string(component.DataBinding.Dimensions[index].Role))))
				component.DataBinding.Dimensions[index].Field = strings.TrimSpace(component.DataBinding.Dimensions[index].Field)
			}
			for index := range component.DataBinding.Measures {
				component.DataBinding.Measures[index].Role = report.BindingRole(strings.ToUpper(strings.TrimSpace(string(component.DataBinding.Measures[index].Role))))
				component.DataBinding.Measures[index].Field = strings.TrimSpace(component.DataBinding.Measures[index].Field)
			}
		}
	}
	for interactionIndex := range definition.Interactions {
		interaction := &definition.Interactions[interactionIndex]
		interaction.Event = report.InteractionEvent(strings.ToUpper(strings.TrimSpace(string(interaction.Event))))
		interaction.Action = report.InteractionAction(strings.ToUpper(strings.TrimSpace(string(interaction.Action))))
		for mappingIndex := range interaction.FieldMappings {
			interaction.FieldMappings[mappingIndex].SourceField = strings.TrimSpace(interaction.FieldMappings[mappingIndex].SourceField)
			interaction.FieldMappings[mappingIndex].TargetField = strings.TrimSpace(interaction.FieldMappings[mappingIndex].TargetField)
		}
	}
	definition.RuntimePolicy.RefreshMode = report.RefreshMode(strings.ToUpper(strings.TrimSpace(string(definition.RuntimePolicy.RefreshMode))))
	definition.RuntimePolicy.FailureMode = report.FailureMode(strings.ToUpper(strings.TrimSpace(string(definition.RuntimePolicy.FailureMode))))
	definition.Provenance.CreatedFrom = report.CreatedFrom(strings.ToUpper(strings.TrimSpace(string(definition.Provenance.CreatedFrom))))
	for index := range definition.Provenance.AnalysisMethodVersions {
		definition.Provenance.AnalysisMethodVersions[index].AnalysisMethod = strings.TrimSpace(definition.Provenance.AnalysisMethodVersions[index].AnalysisMethod)
		definition.Provenance.AnalysisMethodVersions[index].Version = strings.TrimSpace(definition.Provenance.AnalysisMethodVersions[index].Version)
	}
	for index := range definition.Provenance.PromptVersions {
		definition.Provenance.PromptVersions[index] = strings.TrimSpace(definition.Provenance.PromptVersions[index])
	}
	for index := range definition.Provenance.ModelPolicies {
		definition.Provenance.ModelPolicies[index] = strings.TrimSpace(definition.Provenance.ModelPolicies[index])
	}
}

func applyDefaults(definition *report.ReportDefinition) error {
	if definition.SchemaVersion == "" {
		definition.SchemaVersion = report.SchemaVersion
	} else {
		parts := strings.Split(definition.SchemaVersion, ".")
		if len(parts) != 2 {
			return fmt.Errorf("schemaVersion %q is invalid", definition.SchemaVersion)
		}
		major, majorErr := strconv.Atoi(parts[0])
		minor, minorErr := strconv.Atoi(parts[1])
		if majorErr != nil || minorErr != nil || major < 0 || minor < 0 ||
			strconv.Itoa(major) != parts[0] || strconv.Itoa(minor) != parts[1] {
			return fmt.Errorf("schemaVersion %q is invalid", definition.SchemaVersion)
		}
		if major != 1 {
			return fmt.Errorf("schemaVersion %q requires an explicit major-version migrator", definition.SchemaVersion)
		}
		// Compatible V1 minor versions are read into the current closed V1
		// shape; canonical mutable output always declares the current version.
		definition.SchemaVersion = report.SchemaVersion
	}
	if definition.Metadata.Locale == "" {
		definition.Metadata.Locale = "zh-CN"
	}
	if definition.Canvas.Desktop.Columns == 0 {
		definition.Canvas.Desktop = report.DesktopCanvas{
			DesignWidth: 1920, Columns: 24, BaseCellWidth: 80, BaseRowHeight: 54,
			GapX: 12, GapY: 12, PaddingX: 24, PaddingY: 24,
		}
	}
	if definition.Canvas.Mobile.Columns == 0 {
		definition.Canvas.Mobile = report.MobileCanvas{Columns: 1, GapY: 12, PaddingX: 12, PaddingY: 12}
	}
	if definition.RuntimePolicy.MaxConcurrentQueries == 0 {
		definition.RuntimePolicy.MaxConcurrentQueries = 8
	}
	if definition.RuntimePolicy.ComponentTimeoutMS == 0 {
		definition.RuntimePolicy.ComponentTimeoutMS = 25_000
	}
	if definition.RuntimePolicy.RefreshMode == "" {
		definition.RuntimePolicy.RefreshMode = report.RefreshManual
	}
	if definition.RuntimePolicy.FailureMode == "" {
		definition.RuntimePolicy.FailureMode = report.FailurePartial
	}
	if definition.Provenance.CreatedFrom == "" {
		definition.Provenance.CreatedFrom = report.CreatedManually
	}
	return nil
}

func applyComponentDefaults(definition *report.ReportDefinition, registry *template.Registry) error {
	for index := range definition.Components {
		component := &definition.Components[index]
		manifest, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
		if !exists {
			continue
		}
		raw, err := json.Marshal(component.Options)
		if err != nil {
			return fmt.Errorf("encode components[%d] options: %w", index, err)
		}
		values := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("decode components[%d] options: %w", index, err)
		}
		for key, value := range manifest.DefaultOptions {
			if _, present := values[key]; !present {
				values[key] = append(json.RawMessage(nil), value...)
			}
		}
		merged, err := json.Marshal(values)
		if err != nil {
			return fmt.Errorf("merge components[%d] defaults: %w", index, err)
		}
		var options report.ComponentOptions
		if err := askdata.DecodeStrictJSON(merged, &options); err != nil {
			return fmt.Errorf("decode components[%d] default options: %w", index, err)
		}
		component.Options = options
	}
	return nil
}

func normalizeCollections(definition *report.ReportDefinition) {
	if definition.DataContexts == nil {
		definition.DataContexts = []report.DataContext{}
	}
	if definition.GlobalFilters == nil {
		definition.GlobalFilters = []report.GlobalFilter{}
	}
	if definition.Pages == nil {
		definition.Pages = []report.Page{}
	}
	if definition.Components == nil {
		definition.Components = []report.Component{}
	}
	if definition.Interactions == nil {
		definition.Interactions = []report.Interaction{}
	}
	if definition.Provenance.SourceQuestionRunIDs == nil {
		definition.Provenance.SourceQuestionRunIDs = []askdata.ID{}
	}
	if definition.Provenance.AIRunIDs == nil {
		definition.Provenance.AIRunIDs = []askdata.ID{}
	}
	for index := range definition.DataContexts {
		sort.Slice(definition.DataContexts[index].DefaultParameters, func(left, right int) bool {
			return definition.DataContexts[index].DefaultParameters[left].Name < definition.DataContexts[index].DefaultParameters[right].Name
		})
		if definition.DataContexts[index].DefaultParameters == nil {
			definition.DataContexts[index].DefaultParameters = []report.DefaultParameter{}
		}
	}
	for index := range definition.GlobalFilters {
		if definition.GlobalFilters[index].Scope.TargetIDs == nil {
			definition.GlobalFilters[index].Scope.TargetIDs = []askdata.ID{}
		}
		if definition.GlobalFilters[index].DefaultValue != nil && definition.GlobalFilters[index].DefaultValue.Values == nil {
			definition.GlobalFilters[index].DefaultValue.Values = []string{}
		}
	}
	for pageIndex := range definition.Pages {
		if definition.Pages[pageIndex].Sections == nil {
			definition.Pages[pageIndex].Sections = []report.Section{}
		}
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			section := &definition.Pages[pageIndex].Sections[sectionIndex]
			if section.Blocks == nil {
				section.Blocks = []report.Block{}
			}
			for blockIndex := range section.Blocks {
				block := &section.Blocks[blockIndex]
				if block.Zones == nil {
					block.Zones = []report.Zone{}
				}
				for zoneIndex := range block.Zones {
					if block.Zones[zoneIndex].Slots == nil {
						block.Zones[zoneIndex].Slots = []report.Slot{}
					}
					for slotIndex := range block.Zones[zoneIndex].Slots {
						sort.Slice(block.Zones[zoneIndex].Slots[slotIndex].MergedFrom, func(left, right int) bool {
							return block.Zones[zoneIndex].Slots[slotIndex].MergedFrom[left] < block.Zones[zoneIndex].Slots[slotIndex].MergedFrom[right]
						})
					}
				}
			}
		}
	}
	for index := range definition.Components {
		binding := definition.Components[index].DataBinding
		if binding != nil && binding.BindingMode == report.BindingDatasetField {
			if binding.Dimensions == nil {
				binding.Dimensions = []report.FieldBinding{}
			}
			if binding.Measures == nil {
				binding.Measures = []report.FieldBinding{}
			}
		}
	}
	for index := range definition.Interactions {
		if definition.Interactions[index].TargetComponentIDs == nil {
			definition.Interactions[index].TargetComponentIDs = []askdata.ID{}
		}
		if definition.Interactions[index].FieldMappings == nil {
			definition.Interactions[index].FieldMappings = []report.FieldMapping{}
		}
	}
}

func sortSemanticCollections(definition *report.ReportDefinition) {
	sort.Slice(definition.DataContexts, func(i, j int) bool { return definition.DataContexts[i].ID < definition.DataContexts[j].ID })
	sort.Slice(definition.GlobalFilters, func(i, j int) bool { return definition.GlobalFilters[i].ID < definition.GlobalFilters[j].ID })
	for index := range definition.GlobalFilters {
		sort.Slice(definition.GlobalFilters[index].Scope.TargetIDs, func(left, right int) bool {
			return definition.GlobalFilters[index].Scope.TargetIDs[left] < definition.GlobalFilters[index].Scope.TargetIDs[right]
		})
	}
	sort.Slice(definition.Components, func(i, j int) bool { return definition.Components[i].ID < definition.Components[j].ID })
	sort.Slice(definition.Pages, func(i, j int) bool {
		if definition.Pages[i].Order != definition.Pages[j].Order {
			return definition.Pages[i].Order < definition.Pages[j].Order
		}
		return definition.Pages[i].ID < definition.Pages[j].ID
	})
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		sort.Slice(page.Sections, func(i, j int) bool {
			if page.Sections[i].Order != page.Sections[j].Order {
				return page.Sections[i].Order < page.Sections[j].Order
			}
			return page.Sections[i].ID < page.Sections[j].ID
		})
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			sort.Slice(section.Blocks, func(i, j int) bool {
				if section.Blocks[i].Layout.Mobile.Order != section.Blocks[j].Layout.Mobile.Order {
					return section.Blocks[i].Layout.Mobile.Order < section.Blocks[j].Layout.Mobile.Order
				}
				return section.Blocks[i].ID < section.Blocks[j].ID
			})
			for blockIndex := range section.Blocks {
				block := &section.Blocks[blockIndex]
				// Zones render in their declared order; EmptyPriority now means
				// only what its name says and no longer decides layout order.
				sort.Slice(block.Zones, func(i, j int) bool {
					if block.Zones[i].Order == 0 && block.Zones[j].Order == 0 {
						if block.Zones[i].Layout.EmptyPriority != block.Zones[j].Layout.EmptyPriority {
							return block.Zones[i].Layout.EmptyPriority < block.Zones[j].Layout.EmptyPriority
						}
						return block.Zones[i].ID < block.Zones[j].ID
					}
					if block.Zones[i].Order != block.Zones[j].Order {
						return block.Zones[i].Order < block.Zones[j].Order
					}
					return block.Zones[i].ID < block.Zones[j].ID
				})
				for zoneIndex := range block.Zones {
					sort.Slice(block.Zones[zoneIndex].Slots, func(i, j int) bool { return block.Zones[zoneIndex].Slots[i].ID < block.Zones[zoneIndex].Slots[j].ID })
				}
			}
		}
	}
	for index := range definition.Interactions {
		sort.Slice(definition.Interactions[index].TargetComponentIDs, func(i, j int) bool {
			return definition.Interactions[index].TargetComponentIDs[i] < definition.Interactions[index].TargetComponentIDs[j]
		})
		sort.Slice(definition.Interactions[index].FieldMappings, func(i, j int) bool {
			left, right := definition.Interactions[index].FieldMappings[i], definition.Interactions[index].FieldMappings[j]
			if left.SourceField != right.SourceField {
				return left.SourceField < right.SourceField
			}
			return left.TargetField < right.TargetField
		})
	}
	sort.Slice(definition.Interactions, func(i, j int) bool {
		left, right := definition.Interactions[i], definition.Interactions[j]
		if left.SourceComponentID != right.SourceComponentID {
			return left.SourceComponentID < right.SourceComponentID
		}
		if left.Event != right.Event {
			return left.Event < right.Event
		}
		leftTargets := strings.Join(idsToStrings(left.TargetComponentIDs), "\x00")
		rightTargets := strings.Join(idsToStrings(right.TargetComponentIDs), "\x00")
		if leftTargets != rightTargets {
			return leftTargets < rightTargets
		}
		if left.Action != right.Action {
			return left.Action < right.Action
		}
		return left.ID < right.ID
	})
	sort.Slice(definition.Provenance.SourceQuestionRunIDs, func(i, j int) bool {
		return definition.Provenance.SourceQuestionRunIDs[i] < definition.Provenance.SourceQuestionRunIDs[j]
	})
	sort.Slice(definition.Provenance.AIRunIDs, func(i, j int) bool { return definition.Provenance.AIRunIDs[i] < definition.Provenance.AIRunIDs[j] })
	sort.Slice(definition.Provenance.AnalysisMethodVersions, func(i, j int) bool {
		left, right := definition.Provenance.AnalysisMethodVersions[i], definition.Provenance.AnalysisMethodVersions[j]
		if left.AnalysisMethod != right.AnalysisMethod {
			return left.AnalysisMethod < right.AnalysisMethod
		}
		return left.Version < right.Version
	})
	sort.Strings(definition.Provenance.PromptVersions)
	sort.Strings(definition.Provenance.ModelPolicies)
}

func idsToStrings(values []askdata.ID) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}

func defaultRegistry() (*template.Registry, error) { return template.NewDefaultRegistry() }
