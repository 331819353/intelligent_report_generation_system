package reportai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/template"
)

type InstantiateInput struct {
	Base               report.ReportDefinition
	DataContextID      askdata.ID
	AllowedFields      []string
	AllMetricsAdditive bool
	EstimatedRows      int
}

func Instantiate(plan Plan, input InstantiateInput, components *template.Registry, methods *insight.Registry) (report.ReportDefinition, error) {
	request := PlanRequest{AllowedFieldNames: input.AllowedFields, AllowedComponents: componentTypes(components), AllowedMethods: methodIDs(methods)}
	if err := ValidatePlan(plan, request, components, methods); err != nil {
		return report.ReportDefinition{}, err
	}
	if len(input.Base.Pages) == 0 {
		return report.ReportDefinition{}, errors.New("base template requires at least one page")
	}
	if !slices.ContainsFunc(input.Base.DataContexts, func(value report.DataContext) bool { return value.ID == input.DataContextID }) {
		return report.ReportDefinition{}, errors.New("data context is unavailable")
	}
	idFactory, err := newDeterministicIDFactory(plan, input)
	if err != nil {
		return report.ReportDefinition{}, err
	}
	baseRaw, err := json.Marshal(input.Base)
	if err != nil {
		return report.ReportDefinition{}, fmt.Errorf("clone base report template: %w", err)
	}
	var definition report.ReportDefinition
	if err := json.Unmarshal(baseRaw, &definition); err != nil {
		return report.ReportDefinition{}, fmt.Errorf("clone base report template: %w", err)
	}
	page := &definition.Pages[0]
	y := nextBlockY(*page)
	sectionOrder := len(page.Sections) + 1
	for sectionIndex, plannedSection := range plan.Sections {
		section := report.Section{ID: idFactory.ID("section", sectionIndex, -1), Name: plannedSection.Title, Order: sectionOrder, Blocks: []report.Block{}}
		sectionOrder++
		for blockIndex, plannedBlock := range plannedSection.Blocks {
			componentType := plannedBlock.RecommendedComponent
			componentVersion := ""
			if len(plannedBlock.DataRoles.Measures) != 0 {
				recommendation := RecommendPlanBlock(plannedBlock, input, components)
				componentType, componentVersion = recommendation.ComponentType, recommendation.ComponentVersion
			}
			manifest, ok := components.Get(componentType, componentVersion)
			if componentVersion == "" {
				manifest, ok = latestManifest(components, componentType)
			}
			if !ok {
				return report.ReportDefinition{}, fmt.Errorf("component %q is unavailable", componentType)
			}
			componentID := idFactory.ID("component", sectionIndex, blockIndex)
			blockID := idFactory.ID("block", sectionIndex, blockIndex)
			zoneID := idFactory.ID("zone", sectionIndex, blockIndex)
			slotID := idFactory.ID("slot", sectionIndex, blockIndex)
			options := report.ComponentOptions{}
			defaults, _ := json.Marshal(manifest.DefaultOptions)
			_ = json.Unmarshal(defaults, &options)
			if _, supportsTitle := manifest.OptionSchema.Properties["title"]; supportsTitle {
				options.Title = plannedBlock.Purpose
			}
			component := report.Component{ID: componentID, TemplateRef: report.ComponentTemplateReference{Type: manifest.Type, Version: manifest.Version}, Options: options}
			if len(plannedBlock.DataRoles.Measures) != 0 {
				dimensions := make([]report.FieldBinding, len(plannedBlock.DataRoles.Dimensions))
				for index, field := range plannedBlock.DataRoles.Dimensions {
					dimensions[index] = report.FieldBinding{Role: preferredRole(manifest, true), Field: field}
				}
				measures := make([]report.FieldBinding, len(plannedBlock.DataRoles.Measures))
				for index, field := range plannedBlock.DataRoles.Measures {
					measures[index] = report.FieldBinding{Role: preferredRole(manifest, false), Field: field}
				}
				contextID := input.DataContextID
				component.DataBinding = &report.DataBinding{BindingMode: report.BindingDatasetField, DataContextID: &contextID, Dimensions: dimensions, Measures: measures}
			}
			width := max(plannedBlock.DesktopHint.W, manifest.MinSize.W)
			width = min(width, 24)
			height := max(plannedBlock.DesktopHint.H, manifest.MinSize.H)
			zone := report.Zone{ID: zoneID, Type: report.ZoneContent,
				Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 1, Columns: width, Rows: height, Overflow: report.OverflowExpand, EmptyPriority: 1},
				Slots:  []report.Slot{{ID: slotID, Grid: report.SlotGrid{X: 0, Y: 0, W: width, H: height}, ComponentID: componentID}}}
			block := report.Block{ID: blockID, Type: blockType(manifest),
				Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: 0, Y: y, W: width, H: height}, Mobile: report.MobileBlockLayout{Order: plannedBlock.MobileHint.Order, Visible: true, HeightMode: report.MobileHeightAuto, SlotMode: report.MobileSlotStack}},
				Zones:  []report.Zone{zone}}
			y += height
			section.Blocks = append(section.Blocks, block)
			definition.Components = append(definition.Components, component)
		}
		page.Sections = append(page.Sections, section)
	}
	canonical, _, err := compiler.Normalize(definition)
	if err != nil {
		return report.ReportDefinition{}, err
	}
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return report.ReportDefinition{}, err
	}
	return definition, nil
}

// RecommendPlanBlock is the Report AI entry into the same frozen registry used
// by Ask Data. The LLM's recommendedComponent is a hint only and cannot select
// a chart that violates result shape or additivity.
func RecommendPlanBlock(block PlanBlock, input InstantiateInput, registry *template.Registry) answer.ChartRecommendation {
	chartInput := answer.ChartRuleInput{
		MetricCount: len(block.DataRoles.Measures), NonTimeGroupByCount: len(block.DataRoles.Dimensions),
		RowCount: input.EstimatedRows, Additive: input.AllMetricsAdditive,
	}
	if chartInput.RowCount <= 0 {
		if chartInput.NonTimeGroupByCount == 0 {
			chartInput.RowCount = 1
		} else {
			chartInput.RowCount = 20
		}
	}
	for _, method := range block.AnalysisMethods {
		switch method {
		case insight.AnalysisTrend, insight.AnalysisAnomalyPoint:
			chartInput.TimeGrain = "AUTO"
			if chartInput.NonTimeGroupByCount > 0 {
				chartInput.NonTimeGroupByCount--
			}
			if chartInput.RowCount < 2 {
				chartInput.RowCount = 2
			}
		case insight.AnalysisPeriodComparison, insight.AnalysisMaxChange:
			chartInput.HasComparison = true
		case insight.AnalysisShareOfTotal, insight.AnalysisContribution:
			chartInput.ShareIntent = true
		}
	}
	return answer.RecommendChart(chartInput, registry)
}

type deterministicIDFactory struct{ seed string }

func newDeterministicIDFactory(plan Plan, input InstantiateInput) (deterministicIDFactory, error) {
	baseCanonical, baseHash, err := compiler.Normalize(input.Base)
	if err != nil {
		return deterministicIDFactory{}, fmt.Errorf("normalize base report template: %w", err)
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		return deterministicIDFactory{}, fmt.Errorf("encode report plan: %w", err)
	}
	allowedFields := append([]string(nil), input.AllowedFields...)
	slices.Sort(allowedFields)
	seed := bytes.Join([][]byte{
		[]byte("report-ai-instantiate-v1"), []byte(baseHash), baseCanonical, planRaw,
		[]byte(input.DataContextID), []byte(fmt.Sprintf("%q", allowedFields)),
	}, []byte{0})
	return deterministicIDFactory{seed: string(askdata.HashBytes(seed))}, nil
}

func (factory deterministicIDFactory) ID(kind string, sectionIndex, blockIndex int) askdata.ID {
	name := fmt.Sprintf("%s\x00%s\x00%d\x00%d", factory.seed, kind, sectionIndex, blockIndex)
	return askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String())
}

func componentTypes(registry *template.Registry) []string {
	values := []string{}
	for _, manifest := range registry.List() {
		if !slices.Contains(values, manifest.Type) {
			values = append(values, manifest.Type)
		}
	}
	return values
}

func methodIDs(registry *insight.Registry) []insight.AnalysisMethod {
	values := []insight.AnalysisMethod{}
	for _, method := range registry.List() {
		values = append(values, method.ID())
	}
	return values
}

func latestManifest(registry *template.Registry, componentType string) (template.Manifest, bool) {
	var result template.Manifest
	found := false
	for _, manifest := range registry.List() {
		if manifest.Type == componentType {
			result, found = manifest, true
		}
	}
	return result, found
}

func nextBlockY(page report.Page) int {
	y := 0
	for _, section := range page.Sections {
		for _, block := range section.Blocks {
			y = max(y, block.Layout.Desktop.Y+block.Layout.Desktop.H)
		}
	}
	return y
}

func blockType(manifest template.Manifest) report.BlockType {
	switch manifest.Category {
	case template.CategoryTable:
		return report.BlockTable
	case template.CategoryContent:
		return report.BlockContent
	case template.CategoryControl:
		return report.BlockFilter
	default:
		return report.BlockChart
	}
}

func preferredRole(manifest template.Manifest, dimension bool) report.BindingRole {
	candidates := []report.BindingRole{report.RoleCategory, report.RoleXAxis, report.RoleDimension, report.RoleSeries}
	if !dimension {
		candidates = []report.BindingRole{report.RoleValue, report.RoleYAxis, report.RoleSize}
	}
	for _, candidate := range candidates {
		for _, allowed := range manifest.DataContract.Roles {
			if report.BindingRole(allowed) == candidate {
				return candidate
			}
		}
	}
	if dimension {
		return report.RoleDimension
	}
	return report.RoleValue
}
