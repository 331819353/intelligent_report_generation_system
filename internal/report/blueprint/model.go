// Package blueprint defines the compact intent-layer contract shared by human
// authors, templates and LLMs. A blueprint contains no database UUIDs, SQL or
// layout coordinates; Expand is the only route into Report Definition.
package blueprint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/cardkind"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/template"
)

const SchemaVersion = "report-blueprint/1.0"

type Blueprint struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Title         string                 `json:"title"`
	ReportType    reportmodel.ReportType `json:"reportType"`
	Audience      string                 `json:"audience"`
	Locale        string                 `json:"locale"`
	Theme         string                 `json:"theme"`
	Datasets      []DatasetReference     `json:"datasets"`
	Sections      []Section              `json:"sections"`
	Summary       NarrativeSwitch        `json:"summary"`
	Recommend     NarrativeSwitch        `json:"recommend"`
	Style         Style                  `json:"style"`
}

type DatasetReference struct {
	Ref   string `json:"ref"`
	Alias string `json:"alias"`
}

type Section struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Question string `json:"question"`
	LeadIn   bool   `json:"leadIn"`
	Layout   string `json:"layout"`
	Rows     []Row  `json:"rows"`
}

type Row struct {
	Cards []Card `json:"cards"`
}

type Card struct {
	Ref        string                   `json:"ref"`
	Kind       cardkind.Kind            `json:"kind"`
	Dataset    string                   `json:"dataset"`
	Metrics    []string                 `json:"metrics"`
	Dimensions []string                 `json:"dimensions"`
	Methods    []insight.AnalysisMethod `json:"methods"`
	Narrative  bool                     `json:"narrative"`
	Weight     int                      `json:"weight"`
	Title      *string                  `json:"title"`
	TopN       *int                     `json:"topN"`
}

type NarrativeSwitch struct {
	Enabled bool `json:"enabled"`
}

type Style struct {
	Tone   string `json:"tone"`
	Length string `json:"length"`
}

// Catalog is the actor-visible, policy-trimmed reference table used while
// validating and expanding a blueprint. Field contains a governed schema field
// name, never an expression or SQL fragment.
type Catalog struct {
	Datasets []DatasetCatalog `json:"datasets"`
}

type DatasetCatalog struct {
	Ref         string                  `json:"ref"`
	Alias       string                  `json:"alias"`
	DataContext reportmodel.DataContext `json:"-"`
	Metrics     []Field                 `json:"metrics"`
	Dimensions  []Field                 `json:"dimensions"`
}

type Field struct {
	Ref   string `json:"ref"`
	Field string `json:"field"`
	Label string `json:"label"`
}

type ExpandInput struct {
	Base        reportmodel.ReportDefinition
	Catalog     Catalog
	CreatedFrom reportmodel.CreatedFrom
	Kinds       *cardkind.Registry
	Components  *template.Registry
	Methods     *insight.Registry
}

// ContextPack is the complete model-visible input. It contains governed schema
// metadata and short references only; raw rows, SQL and database object IDs are
// structurally absent.
type ContextPack struct {
	SchemaVersion string              `json:"schemaVersion"`
	Intent        string              `json:"intent"`
	Datasets      []DatasetCatalog    `json:"datasets"`
	Kinds         []cardkind.Manifest `json:"kinds"`
	Methods       []string            `json:"methods"`
	Rules         Rules               `json:"rules"`
}

type Rules struct {
	MaxSections int `json:"maxSections"`
	MaxCards    int `json:"maxCards"`
}

type Request struct {
	Intent     string                 `json:"intent"`
	ReportType reportmodel.ReportType `json:"reportType"`
	Context    ContextPack            `json:"context"`
}

type Generator interface {
	GenerateReportBlueprint(context.Context, Request) (Blueprint, error)
}

func (blueprint Blueprint) Validate(catalog Catalog, kinds *cardkind.Registry, components *template.Registry, methods *insight.Registry) error {
	if blueprint.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if title := strings.TrimSpace(blueprint.Title); title == "" || len([]rune(title)) > 200 {
		return errors.New("title must contain 1..200 characters")
	}
	if blueprint.ReportType != reportmodel.ReportTypeReport && blueprint.ReportType != reportmodel.ReportTypeDashboard {
		return errors.New("reportType is invalid")
	}
	if !slices.Contains([]string{"EXECUTIVE", "BUSINESS", "ANALYST"}, blueprint.Audience) || blueprint.Locale != "zh-CN" || blueprint.Theme != "corporate-light" {
		return errors.New("audience, locale or theme is invalid")
	}
	if kinds == nil || components == nil || methods == nil || len(catalog.Datasets) == 0 {
		return errors.New("blueprint registries and catalog are required")
	}
	if blueprint.Datasets == nil || blueprint.Sections == nil || len(blueprint.Datasets) == 0 || len(blueprint.Datasets) > 30 || len(blueprint.Sections) == 0 || len(blueprint.Sections) > 30 {
		return errors.New("blueprint requires 1..30 datasets and 1..30 sections")
	}
	catalogByRef := map[string]DatasetCatalog{}
	for index, dataset := range catalog.Datasets {
		if strings.TrimSpace(dataset.Ref) == "" || dataset.DataContext.ID.Validate() != nil {
			return fmt.Errorf("catalog.datasets[%d] is invalid", index)
		}
		catalogByRef[dataset.Ref] = dataset
	}
	declared := map[string]bool{}
	for index, dataset := range blueprint.Datasets {
		available, ok := catalogByRef[dataset.Ref]
		if !ok || declared[dataset.Ref] || strings.TrimSpace(dataset.Alias) == "" || dataset.Alias != available.Alias {
			return fmt.Errorf("datasets[%d] is unavailable, duplicated or has a mismatched alias", index)
		}
		declared[dataset.Ref] = true
	}
	seenSection, seenCard, totalCards := map[string]bool{}, map[string]bool{}, 0
	for sectionIndex, section := range blueprint.Sections {
		if strings.TrimSpace(section.Ref) == "" || seenSection[section.Ref] || strings.TrimSpace(section.Title) == "" || strings.TrimSpace(section.Question) == "" || section.Layout != "FLOW" || len(section.Rows) == 0 {
			return fmt.Errorf("sections[%d] is invalid", sectionIndex)
		}
		seenSection[section.Ref] = true
		for rowIndex, row := range section.Rows {
			if row.Cards == nil || len(row.Cards) == 0 || len(row.Cards) > 4 {
				return fmt.Errorf("sections[%d].rows[%d] requires 1..4 cards", sectionIndex, rowIndex)
			}
			weight := 0
			for cardIndex, card := range row.Cards {
				path := fmt.Sprintf("sections[%d].rows[%d].cards[%d]", sectionIndex, rowIndex, cardIndex)
				if strings.TrimSpace(card.Ref) == "" || seenCard[card.Ref] || card.Weight < 1 || card.Weight > 4 {
					return fmt.Errorf("%s identity or weight is invalid", path)
				}
				seenCard[card.Ref] = true
				weight += card.Weight
				totalCards++
				kind, _, err := kinds.Resolve(card.Kind, len(card.Metrics), len(card.Dimensions), components)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if kind.Contract.TopN {
					if card.TopN == nil || *card.TopN < 1 || *card.TopN > 50 {
						return fmt.Errorf("%s topN must be 1..50", path)
					}
				} else if card.TopN != nil {
					return fmt.Errorf("%s topN is not allowed for %s", path, card.Kind)
				}
				if len(card.Metrics)+len(card.Dimensions) != 0 {
					dataset, ok := catalogByRef[card.Dataset]
					if !ok || !declared[card.Dataset] {
						return fmt.Errorf("%s dataset is unavailable", path)
					}
					if err := validateFieldRefs(card.Metrics, dataset.Metrics, "metric"); err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
					if err := validateFieldRefs(card.Dimensions, dataset.Dimensions, "dimension"); err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
				} else if card.Dataset != "" {
					return fmt.Errorf("%s dataset must be empty for an unbound card", path)
				}
				if len(card.Methods) == 0 {
					card.Methods = kind.DefaultMethods
				}
				for _, method := range card.Methods {
					if _, ok := methods.Get(method); !ok {
						return fmt.Errorf("%s method %q is unavailable", path, method)
					}
				}
			}
			if weight > 4 {
				return fmt.Errorf("sections[%d].rows[%d] total weight exceeds 4", sectionIndex, rowIndex)
			}
		}
	}
	if totalCards > 300 {
		return errors.New("blueprint exceeds 300 cards")
	}
	if !slices.Contains([]string{"REPORTING", "CONCISE", "FORMAL"}, blueprint.Style.Tone) || !slices.Contains([]string{"SHORT", "MEDIUM", "LONG"}, blueprint.Style.Length) {
		return errors.New("style is invalid")
	}
	return nil
}

func validateFieldRefs(refs []string, catalog []Field, kind string) error {
	known := map[string]bool{}
	for _, field := range catalog {
		known[field.Ref] = true
	}
	seen := map[string]bool{}
	for index, ref := range refs {
		if !known[ref] || seen[ref] {
			return fmt.Errorf("%s[%d] %q is unavailable or duplicated", kind, index, ref)
		}
		seen[ref] = true
	}
	return nil
}

// Expand deterministically translates one validated blueprint into the frozen
// facts consumed by the existing compiler, runtime, editor and publisher.
func Expand(blueprint Blueprint, input ExpandInput) (reportmodel.ReportDefinition, error) {
	if err := blueprint.Validate(input.Catalog, input.Kinds, input.Components, input.Methods); err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	if len(input.Base.Pages) == 0 {
		return reportmodel.ReportDefinition{}, errors.New("base definition requires a page")
	}
	raw, err := json.Marshal(input.Base)
	if err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	var definition reportmodel.ReportDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	definition.Metadata.Name = strings.TrimSpace(blueprint.Title)
	definition.Metadata.ReportType = blueprint.ReportType
	definition.Metadata.Locale = blueprint.Locale
	definition.Provenance.CreatedFrom = input.CreatedFrom
	definition.Pages[0].Sections = []reportmodel.Section{}
	definition.Components = []reportmodel.Component{}
	definition.Interactions = []reportmodel.Interaction{}
	definition.DataContexts = make([]reportmodel.DataContext, 0, len(blueprint.Datasets))
	catalogByRef := map[string]DatasetCatalog{}
	for _, dataset := range input.Catalog.Datasets {
		catalogByRef[dataset.Ref] = dataset
	}
	for _, ref := range blueprint.Datasets {
		definition.DataContexts = append(definition.DataContexts, catalogByRef[ref.Ref].DataContext)
	}
	factory, err := newIDFactory(blueprint, input.Catalog, definition.Metadata.ID)
	if err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	y, mobileOrder := 0, 1
	methodVersions := map[insight.AnalysisMethod]bool{}
	for sectionIndex, plannedSection := range blueprint.Sections {
		section := reportmodel.Section{
			ID: factory.ID("section", plannedSection.Ref), Name: plannedSection.Title, Order: sectionIndex + 1,
			Question: plannedSection.Question, LayoutIntent: &reportmodel.SectionLayoutIntent{Mode: plannedSection.Layout}, Blocks: []reportmodel.Block{},
		}
		filterSlots, insightSlots, contentSlots := []reportmodel.Slot{}, []reportmodel.Slot{}, []reportmodel.Slot{}
		filterY, insightY, contentY := 0, 0, 0
		for _, row := range plannedSection.Rows {
			filterX, insightX, contentX := 0, 0, 0
			filterRowHeight, insightRowHeight, contentRowHeight := 0, 0, 0
			for _, plannedCard := range row.Cards {
				kind, componentManifest, err := input.Kinds.Resolve(plannedCard.Kind, len(plannedCard.Metrics), len(plannedCard.Dimensions), input.Components)
				if err != nil {
					return reportmodel.ReportDefinition{}, err
				}
				width := plannedCard.Weight * 6
				if width < componentManifest.MinSize.W {
					width = componentManifest.MinSize.W
				}
				height := max(kind.LayoutIntent.MinRows, componentManifest.MinSize.H)
				component, err := buildComponent(factory, plannedCard, componentManifest, catalogByRef)
				if err != nil {
					return reportmodel.ReportDefinition{}, err
				}
				definition.Components = append(definition.Components, component)
				var slots *[]reportmodel.Slot
				var x, zoneY int
				switch plannedCard.Kind {
				case cardkind.Filter:
					slots, x, zoneY = &filterSlots, filterX, filterY
					filterX += width
					filterRowHeight = max(filterRowHeight, height)
				case cardkind.Insight, cardkind.Summary, cardkind.Recommend:
					slots, x, zoneY = &insightSlots, insightX, insightY
					insightX += width
					insightRowHeight = max(insightRowHeight, height)
				default:
					slots, x, zoneY = &contentSlots, contentX, contentY
					contentX += width
					contentRowHeight = max(contentRowHeight, height)
				}
				if x+width > 24 {
					return reportmodel.ReportDefinition{}, fmt.Errorf("row containing %s cannot fit its block zone", plannedCard.Ref)
				}
				*slots = append(*slots, reportmodel.Slot{ID: factory.ID("slot", plannedCard.Ref), Grid: reportmodel.SlotGrid{X: x, Y: zoneY, W: width, H: height}, ComponentID: component.ID, CardKind: string(plannedCard.Kind)})
				methodsForCard := plannedCard.Methods
				if len(methodsForCard) == 0 {
					methodsForCard = kind.DefaultMethods
				}
				for _, method := range methodsForCard {
					methodVersions[method] = true
				}
				if plannedCard.Narrative && component.DataBinding != nil && len(component.DataBinding.Measures) != 0 {
					if narrative, ok := buildNarrativeComponent(factory, plannedCard, component, input.Components); ok {
						narrativeHeight := 2
						insightSlots = append(insightSlots, reportmodel.Slot{ID: factory.ID("narrative-slot", plannedCard.Ref), Grid: reportmodel.SlotGrid{X: 0, Y: insightY + insightRowHeight, W: 24, H: narrativeHeight}, ComponentID: narrative.ID, CardKind: string(cardkind.Insight)})
						definition.Components = append(definition.Components, narrative)
						insightRowHeight += narrativeHeight
					}
				}
			}
			filterY += filterRowHeight
			insightY += insightRowHeight
			contentY += contentRowHeight
		}
		filterRows, insightRows, contentRows := max(filterY, 2), max(insightY, 3), max(contentY, 6)
		if len(filterSlots) == 0 {
			filterSlots = []reportmodel.Slot{{ID: factory.ID("empty-filter-slot", plannedSection.Ref), Grid: reportmodel.SlotGrid{X: 0, Y: 0, W: 24, H: filterRows}}}
		}
		if len(insightSlots) == 0 {
			insightSlots = []reportmodel.Slot{{ID: factory.ID("empty-insight-slot", plannedSection.Ref), Grid: reportmodel.SlotGrid{X: 0, Y: 0, W: 24, H: insightRows}}}
		}
		if len(contentSlots) == 0 {
			contentSlots = []reportmodel.Slot{{ID: factory.ID("empty-content-slot", plannedSection.Ref), Grid: reportmodel.SlotGrid{X: 0, Y: 0, W: 24, H: contentRows}}}
		}
		fr := 1.0
		zones := []reportmodel.Zone{
			{ID: factory.ID("filter-zone", plannedSection.Ref), Order: 1, Type: reportmodel.ZoneFilter,
				Layout: reportmodel.ZoneLayout{HeightMode: reportmodel.ZoneHeightAuto, MinHeight: 1, Columns: 24, Rows: filterRows, Overflow: reportmodel.OverflowExpand, EmptyPriority: 0}, Slots: filterSlots},
			{ID: factory.ID("insight-zone", plannedSection.Ref), Order: 2, Type: reportmodel.ZoneInsight,
				Layout: reportmodel.ZoneLayout{HeightMode: reportmodel.ZoneHeightAuto, MinHeight: 1, Columns: 24, Rows: insightRows, Overflow: reportmodel.OverflowExpand, EmptyPriority: 0}, Slots: insightSlots},
			{ID: factory.ID("content-zone", plannedSection.Ref), Order: 3, Type: reportmodel.ZoneContent,
				Layout: reportmodel.ZoneLayout{HeightMode: reportmodel.ZoneHeightFR, MinHeight: 1, FR: &fr, Columns: 24, Rows: contentRows, Overflow: reportmodel.OverflowExpand, EmptyPriority: 1}, Slots: contentSlots},
		}
		blockHeight := filterRows + insightRows + contentRows + 1
		section.Blocks = append(section.Blocks, reportmodel.Block{
			ID: factory.ID("block", plannedSection.Ref), Type: reportmodel.BlockAnalysisCard, Title: plannedSection.Title,
			LayoutIntent: &reportmodel.BlockLayoutIntent{Span: 24, MinRows: blockHeight, NarrativeAttach: "INSIDE", ManualOverride: false},
			Layout: reportmodel.BlockLayout{Desktop: reportmodel.DesktopBlockLayout{X: 0, Y: y, W: 24, H: blockHeight},
				Mobile: reportmodel.MobileBlockLayout{Order: mobileOrder, Visible: true, HeightMode: reportmodel.MobileHeightAuto, SlotMode: reportmodel.MobileSlotStack}},
			Zones: zones,
		})
		y += blockHeight
		mobileOrder++
		definition.Pages[0].Sections = append(definition.Pages[0].Sections, section)
	}
	definition.Provenance.AnalysisMethodVersions = []reportmodel.AnalysisMethodVersionReference{}
	methodNames := make([]string, 0, len(methodVersions))
	for method := range methodVersions {
		methodNames = append(methodNames, string(method))
	}
	sort.Strings(methodNames)
	for _, method := range methodNames {
		definition.Provenance.AnalysisMethodVersions = append(definition.Provenance.AnalysisMethodVersions,
			reportmodel.AnalysisMethodVersionReference{AnalysisMethod: method, Version: "1.0.0"})
	}
	canonical, _, err := compiler.Normalize(definition)
	if err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	var normalized reportmodel.ReportDefinition
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return reportmodel.ReportDefinition{}, err
	}
	return normalized, nil
}

func buildComponent(factory idFactory, card Card, manifest template.Manifest, catalogs map[string]DatasetCatalog) (reportmodel.Component, error) {
	options := reportmodel.ComponentOptions{}
	defaults, _ := json.Marshal(manifest.DefaultOptions)
	_ = json.Unmarshal(defaults, &options)
	title := manifest.DisplayName
	if card.Title != nil && strings.TrimSpace(*card.Title) != "" {
		title = strings.TrimSpace(*card.Title)
	}
	if _, ok := manifest.OptionSchema.Properties["title"]; ok {
		options.Title = title
	}
	if card.TopN != nil {
		if _, ok := manifest.OptionSchema.Properties["topN"]; ok {
			options.TopN = card.TopN
		}
	}
	component := reportmodel.Component{ID: factory.ID("component", card.Ref), TemplateRef: reportmodel.ComponentTemplateReference{Type: manifest.Type, Version: manifest.Version}, Options: options}
	if len(card.Metrics)+len(card.Dimensions) == 0 {
		return component, nil
	}
	dataset := catalogs[card.Dataset]
	dimensions := make([]reportmodel.FieldBinding, 0, len(card.Dimensions))
	for _, ref := range card.Dimensions {
		field, ok := resolveField(dataset.Dimensions, ref)
		if !ok {
			return reportmodel.Component{}, fmt.Errorf("dimension %q is unavailable", ref)
		}
		dimensions = append(dimensions, reportmodel.FieldBinding{Role: preferredRole(manifest, true), Field: field.Field})
	}
	measures := make([]reportmodel.FieldBinding, 0, len(card.Metrics))
	for _, ref := range card.Metrics {
		field, ok := resolveField(dataset.Metrics, ref)
		if !ok {
			return reportmodel.Component{}, fmt.Errorf("metric %q is unavailable", ref)
		}
		measures = append(measures, reportmodel.FieldBinding{Role: preferredRole(manifest, false), Field: field.Field})
	}
	contextID := dataset.DataContext.ID
	component.DataBinding = &reportmodel.DataBinding{BindingMode: reportmodel.BindingDatasetField, DataContextID: &contextID, Dimensions: dimensions, Measures: measures}
	return component, nil
}

func buildNarrativeComponent(factory idFactory, card Card, source reportmodel.Component, components *template.Registry) (reportmodel.Component, bool) {
	var manifest template.Manifest
	found := false
	for _, candidate := range components.List() {
		if candidate.Type == "insight-text" {
			manifest, found = candidate, true
		}
	}
	if !found || source.DataBinding == nil || source.DataBinding.DataContextID == nil {
		return reportmodel.Component{}, false
	}
	dimensions := make([]reportmodel.FieldBinding, 0, len(source.DataBinding.Dimensions))
	for _, binding := range source.DataBinding.Dimensions {
		if len(dimensions) == manifest.DataContract.Dimensions.Max {
			break
		}
		dimensions = append(dimensions, reportmodel.FieldBinding{Role: reportmodel.RoleDimension, Field: binding.Field})
	}
	measures := make([]reportmodel.FieldBinding, 0, len(source.DataBinding.Measures))
	for _, binding := range source.DataBinding.Measures {
		if len(measures) == manifest.DataContract.Measures.Max {
			break
		}
		measures = append(measures, reportmodel.FieldBinding{Role: reportmodel.RoleValue, Field: binding.Field})
	}
	options := reportmodel.ComponentOptions{}
	defaults, _ := json.Marshal(manifest.DefaultOptions)
	_ = json.Unmarshal(defaults, &options)
	if _, supportsTitle := manifest.OptionSchema.Properties["title"]; supportsTitle {
		options.Title = "智能结论"
	}
	contextID := *source.DataBinding.DataContextID
	return reportmodel.Component{ID: factory.ID("narrative", card.Ref), TemplateRef: reportmodel.ComponentTemplateReference{Type: manifest.Type, Version: manifest.Version},
		DataBinding: &reportmodel.DataBinding{BindingMode: reportmodel.BindingDatasetField, DataContextID: &contextID, Dimensions: dimensions, Measures: measures}, Options: options}, true
}

func resolveField(fields []Field, ref string) (Field, bool) {
	for _, field := range fields {
		if field.Ref == ref {
			return field, true
		}
	}
	return Field{}, false
}

func preferredRole(manifest template.Manifest, dimension bool) reportmodel.BindingRole {
	candidates := []reportmodel.BindingRole{reportmodel.RoleCategory, reportmodel.RoleXAxis, reportmodel.RoleDimension, reportmodel.RoleSeries}
	if !dimension {
		candidates = []reportmodel.BindingRole{reportmodel.RoleValue, reportmodel.RoleYAxis, reportmodel.RoleSize}
	}
	for _, candidate := range candidates {
		for _, allowed := range manifest.DataContract.Roles {
			if reportmodel.BindingRole(allowed) == candidate {
				return candidate
			}
		}
	}
	if dimension {
		return reportmodel.RoleDimension
	}
	return reportmodel.RoleValue
}

func blockType(manifest template.Manifest) reportmodel.BlockType {
	switch manifest.Category {
	case template.CategoryTable:
		return reportmodel.BlockTable
	case template.CategoryContent:
		return reportmodel.BlockContent
	case template.CategoryControl:
		return reportmodel.BlockFilter
	default:
		return reportmodel.BlockChart
	}
}

type idFactory struct{ seed string }

func newIDFactory(blueprint Blueprint, catalog Catalog, reportID askdata.ID) (idFactory, error) {
	bp, err := json.Marshal(blueprint)
	if err != nil {
		return idFactory{}, err
	}
	refs := make([]string, 0, len(catalog.Datasets))
	for _, dataset := range catalog.Datasets {
		refs = append(refs, dataset.Ref+"="+string(dataset.DataContext.ID))
	}
	sort.Strings(refs)
	seed := bytes.Join([][]byte{[]byte(SchemaVersion), []byte(reportID), bp, []byte(strings.Join(refs, "\x00"))}, []byte{0})
	return idFactory{seed: string(askdata.HashBytes(seed))}, nil
}

func (factory idFactory) ID(kind, ref string) askdata.ID {
	return askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(factory.seed+"\x00"+kind+"\x00"+ref)).String())
}
