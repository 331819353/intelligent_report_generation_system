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
	Components []SectionSummaryComponent `json:"components"`
}

type SectionSummaryRequest struct {
	SectionID   askdata.ID                 `json:"sectionId"`
	SectionName string                     `json:"sectionName"`
	Question    string                     `json:"question,omitempty"`
	Subsections []SectionSummarySubsection `json:"subsections"`
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

// BuildSectionSummaryRequest projects exactly one analysis angle and all of its
// subsection composition into a bounded model request. Angle-level summary
// blocks are deliberately excluded so regeneration cannot summarize itself.
func BuildSectionSummaryRequest(definition report.ReportDefinition, sectionID askdata.ID) (SectionSummaryRequest, error) {
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
			result := SectionSummaryRequest{
				SectionID: section.ID, SectionName: boundedSummaryText(section.Name, 160),
				Question: boundedSummaryText(section.Question, 500), Subsections: []SectionSummarySubsection{},
			}
			componentCount := 0
			for _, block := range section.Blocks {
				if !strings.HasPrefix(block.CardKind, "LAYOUT_SUBSECTION_") {
					continue
				}
				subsection := SectionSummarySubsection{
					ID: block.ID, Title: boundedSummaryText(block.Title, 160),
					Layout: strings.TrimPrefix(block.CardKind, "LAYOUT_SUBSECTION_"), Components: []SectionSummaryComponent{},
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
			if len(result.Subsections) == 0 {
				return SectionSummaryRequest{}, errors.New("section has no analysis subsections")
			}
			return result, nil
		}
	}
	return SectionSummaryRequest{}, errors.New("section does not exist")
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
