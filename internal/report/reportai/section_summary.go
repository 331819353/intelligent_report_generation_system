package reportai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

// SectionSummaryComponent is the bounded, data-free description of one item in
// an analysis subsection. Raw rows and query results never cross this boundary;
// the model can only synthesize titles, bindings, filters and already-authored
// narrative that are part of the draft definition.
type SectionSummaryComponent struct {
	Role       string   `json:"role"`
	Type       string   `json:"type"`
	Title      string   `json:"title,omitempty"`
	Subtitle   string   `json:"subtitle,omitempty"`
	Narrative  string   `json:"narrative,omitempty"`
	Dimensions []string `json:"dimensions"`
	Measures   []string `json:"measures"`
	Filters    []string `json:"filters"`
}

type SectionSummarySubsection struct {
	ID         askdata.ID                `json:"id"`
	Title      string                    `json:"title"`
	Layout     string                    `json:"layout"`
	Weight     int                       `json:"weight"`
	Components []SectionSummaryComponent `json:"components"`
}

type SectionSummaryRequest struct {
	SectionID        askdata.ID                  `json:"sectionId"`
	SectionName      string                      `json:"sectionName"`
	Question         string                      `json:"question,omitempty"`
	AnalysisApproach report.AngleInsightApproach `json:"analysisApproach"`
	Subsections      []SectionSummarySubsection  `json:"subsections"`
}

type SectionSummaryContent struct {
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
	Risks    []string `json:"risks"`
	Actions  []string `json:"actions"`
}

type SectionSummaryGenerator interface {
	GenerateSectionSummary(context.Context, SectionSummaryRequest) (SectionSummaryContent, error)
}

type SubsectionSummaryItem struct {
	ComponentID askdata.ID              `json:"componentId"`
	Weight      int                     `json:"weight"`
	Component   SectionSummaryComponent `json:"component"`
}

type SubsectionSummaryRequest struct {
	SectionID        askdata.ID                  `json:"sectionId"`
	SubsectionID     askdata.ID                  `json:"subsectionId"`
	SectionName      string                      `json:"sectionName"`
	SubsectionTitle  string                      `json:"subsectionTitle"`
	AnalysisApproach report.AngleInsightApproach `json:"analysisApproach"`
	Items            []SubsectionSummaryItem     `json:"items"`
}

type SubsectionSummaryGenerator interface {
	GenerateSubsectionSummary(context.Context, SubsectionSummaryRequest) (SectionSummaryContent, error)
}

func boundedSummaryText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}

func summaryBindingLabel(binding report.FieldBinding) string {
	label := strings.TrimSpace(binding.Label)
	if label == "" {
		label = strings.TrimSpace(binding.Field)
	}
	if binding.Aggregation != "" {
		label = fmt.Sprintf("%s（%s）", label, binding.Aggregation)
	}
	return boundedSummaryText(label, 160)
}

func summarySlotRole(cardKind string) string {
	role := strings.TrimPrefix(strings.TrimSpace(cardKind), "FRAME_")
	if role == "" || role == cardKind {
		return "CONTENT"
	}
	return role
}

func summaryFilterValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return boundedSummaryText(text, 240)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return boundedSummaryText(string(encoded), 240)
}

// BuildSectionSummaryRequest uses the persisted smart-conclusion configuration
// when present and otherwise selects every subsection with equal weight.
func BuildSectionSummaryRequest(definition report.ReportDefinition, sectionID askdata.ID) (SectionSummaryRequest, error) {
	return BuildSectionSummaryRequestWithConfig(definition, sectionID, nil)
}

// BuildSectionSummaryRequestWithConfig projects only the author-selected
// subsections into a bounded model request. The optional override supports a
// single save-and-generate action without persisting half of the edit first.
// Angle-level summary blocks are deliberately excluded from their own source.
func BuildSectionSummaryRequestWithConfig(definition report.ReportDefinition, sectionID askdata.ID, override *report.AngleInsightConfig) (SectionSummaryRequest, error) {
	if sectionID.Validate() != nil {
		return SectionSummaryRequest{}, errors.New("section id is invalid")
	}
	components := make(map[askdata.ID]report.Component, len(definition.Components))
	for _, component := range definition.Components {
		components[component.ID] = component
	}
	globalFilters := make(map[askdata.ID]report.GlobalFilter, len(definition.GlobalFilters))
	for _, filter := range definition.GlobalFilters {
		globalFilters[filter.ID] = filter
	}
	for _, page := range definition.Pages {
		for _, section := range page.Sections {
			if section.ID != sectionID {
				continue
			}
			subsectionBlocks := make([]report.Block, 0, len(section.Blocks))
			for _, block := range section.Blocks {
				if strings.HasPrefix(block.CardKind, "LAYOUT_SUBSECTION_") {
					subsectionBlocks = append(subsectionBlocks, block)
				}
			}
			if len(subsectionBlocks) == 0 {
				return SectionSummaryRequest{}, errors.New("section has no analysis subsections")
			}
			config := override
			if config == nil {
				for _, block := range section.Blocks {
					if block.CardKind != "LAYOUT_ANGLE_INSIGHT" {
						continue
					}
					for _, zone := range block.Zones {
						for _, slot := range zone.Slots {
							if component, exists := components[slot.ComponentID]; exists && component.Options.AngleInsight != nil {
								config = component.Options.AngleInsight
							}
						}
					}
				}
			}
			if config == nil {
				ids := make([]askdata.ID, 0, len(subsectionBlocks))
				for _, block := range subsectionBlocks {
					ids = append(ids, block.ID)
				}
				fallback := report.DefaultAngleInsightConfig(ids)
				config = &fallback
			}
			if err := config.Validate(); err != nil {
				return SectionSummaryRequest{}, fmt.Errorf("angle insight configuration is invalid: %w", err)
			}
			weights := make(map[askdata.ID]int, len(config.AnalysisItems))
			available := make(map[askdata.ID]struct{}, len(subsectionBlocks))
			for _, block := range subsectionBlocks {
				available[block.ID] = struct{}{}
			}
			for _, item := range config.AnalysisItems {
				if _, exists := available[item.SubsectionID]; !exists {
					return SectionSummaryRequest{}, fmt.Errorf("configured subsection %q does not exist in this analysis angle", item.SubsectionID)
				}
				weights[item.SubsectionID] = item.Weight
			}
			result := SectionSummaryRequest{
				SectionID: section.ID, SectionName: boundedSummaryText(section.Name, 160), Question: boundedSummaryText(section.Question, 500),
				AnalysisApproach: config.AnalysisApproach, Subsections: []SectionSummarySubsection{},
			}
			componentCount := 0
			for _, block := range subsectionBlocks {
				weight, selected := weights[block.ID]
				if !selected {
					continue
				}
				subsection := SectionSummarySubsection{
					ID: block.ID, Title: boundedSummaryText(block.Title, 160),
					Layout: strings.TrimPrefix(block.CardKind, "LAYOUT_SUBSECTION_"), Weight: weight, Components: []SectionSummaryComponent{},
				}
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						component, exists := components[slot.ComponentID]
						if !exists {
							continue
						}
						item := SectionSummaryComponent{
							Role: summarySlotRole(slot.CardKind), Type: boundedSummaryText(component.TemplateRef.Type, 120),
							Title: boundedSummaryText(component.Options.Title, 200), Subtitle: boundedSummaryText(component.Options.Subtitle, 300),
							Narrative:  boundedSummaryText(component.Options.RichText, 2_000),
							Dimensions: []string{}, Measures: []string{}, Filters: []string{},
						}
						if component.DataBinding != nil {
							for _, binding := range component.DataBinding.Dimensions {
								item.Dimensions = append(item.Dimensions, summaryBindingLabel(binding))
							}
							for _, binding := range component.DataBinding.Measures {
								item.Measures = append(item.Measures, summaryBindingLabel(binding))
							}
							if component.DataBinding.FilterPolicy != nil {
								for _, mapping := range component.DataBinding.FilterPolicy.GlobalMappings {
									filter := globalFilters[mapping.FilterID]
									label := strings.TrimSpace(filter.Label)
									if label == "" {
										label = strings.TrimSpace(filter.FieldRef.Field)
									}
									item.Filters = append(item.Filters, boundedSummaryText("全局 "+label+" → "+mapping.Field, 240))
								}
								for _, filter := range component.DataBinding.FilterPolicy.LocalFilters {
									predicate := strings.TrimSpace(filter.Field + " " + filter.Operator + " " + summaryFilterValue(filter.Value))
									item.Filters = append(item.Filters, boundedSummaryText(predicate, 320))
								}
							}
						}
						subsection.Components = append(subsection.Components, item)
						componentCount++
						if componentCount > 100 {
							return SectionSummaryRequest{}, errors.New("section contains too many components to summarize")
						}
					}
				}
				result.Subsections = append(result.Subsections, subsection)
			}
			return result, nil
		}
	}
	return SectionSummaryRequest{}, errors.New("section does not exist")
}

// BuildSubsectionSummaryRequestWithConfig projects the author-selected content
// of one subsection. The native conclusion slot is always excluded from its own
// evidence, including legacy conclusion cards that are awaiting migration.
func BuildSubsectionSummaryRequestWithConfig(definition report.ReportDefinition, sectionID, subsectionID askdata.ID, override *report.SubsectionInsightConfig) (SubsectionSummaryRequest, error) {
	if sectionID.Validate() != nil || subsectionID.Validate() != nil {
		return SubsectionSummaryRequest{}, errors.New("section or subsection id is invalid")
	}
	components := make(map[askdata.ID]report.Component, len(definition.Components))
	for _, component := range definition.Components {
		components[component.ID] = component
	}
	globalFilters := make(map[askdata.ID]report.GlobalFilter, len(definition.GlobalFilters))
	for _, filter := range definition.GlobalFilters {
		globalFilters[filter.ID] = filter
	}
	for _, page := range definition.Pages {
		for _, section := range page.Sections {
			if section.ID != sectionID {
				continue
			}
			for _, block := range section.Blocks {
				if block.ID != subsectionID || !strings.HasPrefix(block.CardKind, "LAYOUT_SUBSECTION_") {
					continue
				}
				type candidate struct {
					id        askdata.ID
					component report.Component
					role      string
				}
				candidates := make([]candidate, 0)
				available := make(map[askdata.ID]candidate)
				config := override
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						component, exists := components[slot.ComponentID]
						if !exists {
							continue
						}
						if summarySlotRole(slot.CardKind) == "CONCLUSION" {
							if config == nil && component.Options.SubsectionInsight != nil {
								config = component.Options.SubsectionInsight
							}
							continue
						}
						item := candidate{id: component.ID, component: component, role: summarySlotRole(slot.CardKind)}
						if _, duplicate := available[component.ID]; duplicate {
							continue
						}
						available[component.ID] = item
						candidates = append(candidates, item)
					}
				}
				if len(candidates) == 0 {
					return SubsectionSummaryRequest{}, errors.New("subsection has no evidence, detail or appendix content")
				}
				if config == nil {
					ids := make([]askdata.ID, 0, len(candidates))
					for _, item := range candidates {
						if item.role == "EVIDENCE" {
							ids = append(ids, item.id)
						}
					}
					if len(ids) == 0 {
						return SubsectionSummaryRequest{}, errors.New("subsection has no configured charts")
					}
					fallback := report.DefaultSubsectionInsightConfig(ids)
					config = &fallback
				}
				if err := config.Validate(); err != nil {
					return SubsectionSummaryRequest{}, fmt.Errorf("subsection insight configuration is invalid: %w", err)
				}
				result := SubsectionSummaryRequest{
					SectionID: section.ID, SubsectionID: block.ID,
					SectionName: boundedSummaryText(section.Name, 160), SubsectionTitle: boundedSummaryText(block.Title, 160),
					AnalysisApproach: config.AnalysisApproach, Items: []SubsectionSummaryItem{},
				}
				for _, configured := range config.AnalysisItems {
					candidate, exists := available[configured.ComponentID]
					if !exists {
						return SubsectionSummaryRequest{}, fmt.Errorf("configured component %q does not exist in this subsection", configured.ComponentID)
					}
					component := candidate.component
					projected := SectionSummaryComponent{
						Role: candidate.role, Type: boundedSummaryText(component.TemplateRef.Type, 120),
						Title: boundedSummaryText(component.Options.Title, 200), Subtitle: boundedSummaryText(component.Options.Subtitle, 300),
						Narrative:  boundedSummaryText(component.Options.RichText, 2_000),
						Dimensions: []string{}, Measures: []string{}, Filters: []string{},
					}
					if component.DataBinding != nil {
						for _, binding := range component.DataBinding.Dimensions {
							projected.Dimensions = append(projected.Dimensions, summaryBindingLabel(binding))
						}
						for _, binding := range component.DataBinding.Measures {
							projected.Measures = append(projected.Measures, summaryBindingLabel(binding))
						}
						if component.DataBinding.FilterPolicy != nil {
							for _, mapping := range component.DataBinding.FilterPolicy.GlobalMappings {
								filter := globalFilters[mapping.FilterID]
								label := strings.TrimSpace(filter.Label)
								if label == "" {
									label = strings.TrimSpace(filter.FieldRef.Field)
								}
								projected.Filters = append(projected.Filters, boundedSummaryText("全局 "+label+" → "+mapping.Field, 240))
							}
							for _, filter := range component.DataBinding.FilterPolicy.LocalFilters {
								predicate := strings.TrimSpace(filter.Field + " " + filter.Operator + " " + summaryFilterValue(filter.Value))
								projected.Filters = append(projected.Filters, boundedSummaryText(predicate, 320))
							}
						}
					}
					result.Items = append(result.Items, SubsectionSummaryItem{ComponentID: component.ID, Weight: configured.Weight, Component: projected})
				}
				return result, nil
			}
			return SubsectionSummaryRequest{}, errors.New("subsection does not exist in this analysis angle")
		}
	}
	return SubsectionSummaryRequest{}, errors.New("section does not exist")
}

func ValidateSectionSummaryContent(content SectionSummaryContent) (SectionSummaryContent, error) {
	content.Summary = strings.TrimSpace(content.Summary)
	if content.Summary == "" || utf8.RuneCountInString(content.Summary) > 1_200 {
		return SectionSummaryContent{}, errors.New("section summary must contain 1..1200 characters")
	}
	for name, values := range map[string][]string{"findings": content.Findings, "risks": content.Risks, "actions": content.Actions} {
		if len(values) > 8 {
			return SectionSummaryContent{}, fmt.Errorf("%s exceeds 8 items", name)
		}
		for index := range values {
			values[index] = strings.TrimSpace(values[index])
			if values[index] == "" || utf8.RuneCountInString(values[index]) > 500 {
				return SectionSummaryContent{}, fmt.Errorf("%s[%d] is invalid", name, index)
			}
		}
	}
	return content, nil
}

func (content SectionSummaryContent) RichText() string {
	parts := []string{strings.TrimSpace(content.Summary)}
	appendItems := func(label string, values []string) {
		for index, value := range values {
			parts = append(parts, fmt.Sprintf("%s %d：%s", label, index+1, strings.TrimSpace(value)))
		}
	}
	appendItems("核心发现", content.Findings)
	appendItems("风险提示", content.Risks)
	appendItems("建议动作", content.Actions)
	return strings.Join(parts, "\n")
}
