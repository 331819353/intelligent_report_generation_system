// Package report defines the versioned, dependency-safe Report Definition
// contract shared by editors, AI planners and the report runtime.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	askdatair "intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

const (
	SchemaVersion       = "1.0"
	MaxDefinitionBytes  = 5 * 1024 * 1024
	MaxJSONDepth        = 24
	MaxStringLength     = 4_096
	MaxRichTextBytes    = 64 * 1024
	MaxPages            = 20
	MaxSectionsPerPage  = 30
	MaxBlocks           = 300
	MaxSlotsPerBlock    = 16
	MaxComponents       = 500
	MaxDataContexts     = 128
	MaxGlobalFilters    = 128
	MaxInteractions     = 500
	MaxZonesPerBlock    = 16
	MaxBindingsPerRole  = 32
	MaxParameters       = 64
	MaxInteractionLinks = 64
)

var (
	codePattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,127}$`)
	localePattern    = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Z]{2})?$`)
	semverPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	componentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	fieldPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,127}$`)
	parameterPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

type ReportDefinition struct {
	SchemaVersion string            `json:"schemaVersion"`
	Metadata      Metadata          `json:"metadata"`
	TemplateRef   TemplateReference `json:"templateRef"`
	ThemeRef      ThemeReference    `json:"themeRef"`
	Canvas        Canvas            `json:"canvas"`
	DataContexts  []DataContext     `json:"dataContexts"`
	GlobalFilters []GlobalFilter    `json:"globalFilters"`
	Pages         []Page            `json:"pages"`
	Components    []Component       `json:"components"`
	Interactions  []Interaction     `json:"interactions"`
	RuntimePolicy RuntimePolicy     `json:"runtimePolicy"`
	Provenance    Provenance        `json:"provenance"`
}

type Metadata struct {
	ID          askdata.ID `json:"id"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	ReportType  ReportType `json:"reportType"`
	Locale      string     `json:"locale"`
}

type ReportType string

const (
	ReportTypeReport    ReportType = "REPORT"
	ReportTypeDashboard ReportType = "DASHBOARD"
)

type TemplateReference struct {
	ReportTemplateID         askdata.ID `json:"reportTemplateId"`
	ReportTemplateVersion    string     `json:"reportTemplateVersion"`
	StructureTemplateVersion string     `json:"structureTemplateVersion"`
	LayoutTemplateVersion    string     `json:"layoutTemplateVersion"`
	NarrativeTemplateVersion string     `json:"narrativeTemplateVersion"`
}

type ThemeReference struct {
	ThemeID askdata.ID `json:"themeId"`
	Version string     `json:"version"`
}

type Canvas struct {
	Desktop DesktopCanvas `json:"desktop"`
	Mobile  MobileCanvas  `json:"mobile"`
}

type DesktopCanvas struct {
	DesignWidth   int `json:"designWidth"`
	Columns       int `json:"columns"`
	BaseCellWidth int `json:"baseCellWidth"`
	BaseRowHeight int `json:"baseRowHeight"`
	GapX          int `json:"gapX"`
	GapY          int `json:"gapY"`
	PaddingX      int `json:"paddingX"`
	PaddingY      int `json:"paddingY"`
}

type MobileCanvas struct {
	Columns  int `json:"columns"`
	GapY     int `json:"gapY"`
	PaddingX int `json:"paddingX"`
	PaddingY int `json:"paddingY"`
}

type DataContext struct {
	ID                askdata.ID         `json:"id"`
	DatasetID         askdata.ID         `json:"datasetId"`
	DatasetVersionID  askdata.ID         `json:"datasetVersionId"`
	Alias             string             `json:"alias"`
	DefaultParameters []DefaultParameter `json:"defaultParameters"`
	QueryPolicy       QueryPolicy        `json:"queryPolicy"`
}

type ParameterType string

const (
	ParameterString  ParameterType = "STRING"
	ParameterNumber  ParameterType = "NUMBER"
	ParameterBoolean ParameterType = "BOOLEAN"
)

// DefaultParameter is an array item instead of an open JSON object so the
// Report Definition remains closed under additionalProperties=false.
type DefaultParameter struct {
	Name         string        `json:"name"`
	Type         ParameterType `json:"type"`
	StringValue  *string       `json:"stringValue,omitempty"`
	NumberValue  *float64      `json:"numberValue,omitempty"`
	BooleanValue *bool         `json:"booleanValue,omitempty"`
}

type QueryPolicy struct {
	TimeoutMS       int `json:"timeoutMs"`
	MaxRows         int `json:"maxRows"`
	CacheTTLSeconds int `json:"cacheTtlSeconds"`
}

type GlobalFilter struct {
	ID           askdata.ID          `json:"id"`
	Type         FilterType          `json:"type"`
	FieldRef     FieldReference      `json:"fieldRef"`
	Scope        FilterScope         `json:"scope"`
	DefaultValue *FilterDefaultValue `json:"defaultValue,omitempty"`
}

type FilterType string

const (
	FilterSingleSelect   FilterType = "SINGLE_SELECT"
	FilterMultiSelect    FilterType = "MULTI_SELECT"
	FilterDate           FilterType = "DATE"
	FilterDateRange      FilterType = "DATE_RANGE"
	FilterRelativeTime   FilterType = "RELATIVE_TIME"
	FilterNumberRange    FilterType = "NUMBER_RANGE"
	FilterSearchSelect   FilterType = "SEARCH_SELECT"
	FilterParameterInput FilterType = "PARAMETER_INPUT"

	// FilterSelect and FilterBoolean remain readable for Report Definition
	// 1.x compatibility. New definitions should use SINGLE_SELECT and
	// PARAMETER_INPUT; normalization rewrites SELECT to SINGLE_SELECT.
	FilterSelect  FilterType = "SELECT"
	FilterBoolean FilterType = "BOOLEAN"
)

type FieldReference struct {
	DataContextID askdata.ID `json:"dataContextId"`
	Field         string     `json:"field"`
}

type FilterScope struct {
	Type      FilterScopeType `json:"type"`
	TargetIDs []askdata.ID    `json:"targetIds"`
}

type FilterScopeType string

const (
	FilterScopeReport    FilterScopeType = "REPORT"
	FilterScopePage      FilterScopeType = "PAGE"
	FilterScopeSection   FilterScopeType = "SECTION"
	FilterScopeBlock     FilterScopeType = "BLOCK"
	FilterScopeComponent FilterScopeType = "COMPONENT"
)

type FilterDefaultValue struct {
	Mode    string   `json:"mode,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Offset  *int     `json:"offset,omitempty"`
	Values  []string `json:"values,omitempty"`
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	Boolean *bool    `json:"boolean,omitempty"`
}

type Page struct {
	ID       askdata.ID `json:"id"`
	Name     string     `json:"name"`
	Order    int        `json:"order"`
	Sections []Section  `json:"sections"`
}

type Section struct {
	ID           askdata.ID           `json:"id"`
	Name         string               `json:"name"`
	Order        int                  `json:"order"`
	Question     string               `json:"question,omitempty"`
	LayoutIntent *SectionLayoutIntent `json:"layoutIntent,omitempty"`
	Blocks       []Block              `json:"blocks"`
}

// SectionLayoutIntent is semantic input to the deterministic solver. Existing
// 1.0 artifacts omit it and keep their frozen coordinates unchanged.
type SectionLayoutIntent struct {
	Mode string `json:"mode"`
}

type Block struct {
	ID   askdata.ID `json:"id"`
	Type BlockType  `json:"type"`
	// Title belongs to the layout container rather than any one component.
	// Legacy 1.0 reports omit it and continue to render their component titles.
	Title        string             `json:"title,omitempty"`
	CardKind     string             `json:"cardKind,omitempty"`
	LayoutIntent *BlockLayoutIntent `json:"layoutIntent,omitempty"`
	Layout       BlockLayout        `json:"layout"`
	Zones        []Zone             `json:"zones"`
}

// BlockLayoutIntent records what the author or blueprint requested; Layout is
// still the compiler-owned fact consumed by every renderer.
type BlockLayoutIntent struct {
	Span            int    `json:"span"`
	MinRows         int    `json:"minRows"`
	NarrativeAttach string `json:"narrativeAttach"`
	ManualOverride  bool   `json:"manualOverride"`
}

type BlockType string

const (
	BlockAnalysisCard BlockType = "ANALYSIS_CARD"
	BlockKPIGroup     BlockType = "KPI_GROUP"
	BlockChart        BlockType = "CHART"
	BlockTable        BlockType = "TABLE"
	BlockContent      BlockType = "CONTENT"
	BlockFilter       BlockType = "FILTER"
)

type BlockLayout struct {
	Desktop DesktopBlockLayout `json:"desktop"`
	Mobile  MobileBlockLayout  `json:"mobile"`
}

type DesktopBlockLayout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type MobileBlockLayout struct {
	Order         int              `json:"order"`
	Visible       bool             `json:"visible"`
	HeightMode    MobileHeightMode `json:"heightMode"`
	SlotMode      MobileSlotMode   `json:"slotMode"`
	FixedHeight   *int             `json:"fixedHeight,omitempty"`
	AspectRatio   *float64         `json:"aspectRatio,omitempty"`
	PrimarySlotID *askdata.ID      `json:"primarySlotId,omitempty"`
}

type MobileHeightMode string

const (
	MobileHeightAuto        MobileHeightMode = "AUTO"
	MobileHeightFixed       MobileHeightMode = "FIXED"
	MobileHeightAspectRatio MobileHeightMode = "ASPECT_RATIO"
)

type MobileSlotMode string

const (
	MobileSlotStack       MobileSlotMode = "STACK"
	MobileSlotCarousel    MobileSlotMode = "CAROUSEL"
	MobileSlotPrimaryOnly MobileSlotMode = "PRIMARY_ONLY"
	MobileSlotCollapse    MobileSlotMode = "COLLAPSE"
)

type Zone struct {
	ID askdata.ID `json:"id"`
	// Order is the zone's position within its card, top to bottom. It is
	// structural, like Page.Order and Section.Order.
	//
	// Zone order used to be inferred from Layout.EmptyPriority, which also
	// decides which zone absorbs space freed by empty ones. The two consumers
	// sorted that field in opposite directions, so a zone could not both render
	// first and absorb freed space first — and ZONE_REORDER, whose payload has
	// always been an order, wrote into the redistribution weight instead.
	// omitempty is intentional compatibility: reports published before zone
	// ordering was separated from EmptyPriority do not contain this field. A
	// missing order remains a valid immutable V1 artifact and keeps its original
	// content hash; every newly created or edited zone uses a positive order.
	Order  int        `json:"order,omitempty"`
	Type   ZoneType   `json:"type"`
	Layout ZoneLayout `json:"layout"`
	Slots  []Slot     `json:"slots"`
}

type ZoneType string

const (
	ZoneHeader  ZoneType = "HEADER"
	ZoneFilter  ZoneType = "FILTER"
	ZoneInsight ZoneType = "INSIGHT"
	ZoneContent ZoneType = "CONTENT"
	ZoneFooter  ZoneType = "FOOTER"
)

type ZoneLayout struct {
	HeightMode    ZoneHeightMode `json:"heightMode"`
	MinHeight     int            `json:"minHeight"`
	MaxHeight     *int           `json:"maxHeight,omitempty"`
	FixedHeight   *int           `json:"fixedHeight,omitempty"`
	FR            *float64       `json:"fr,omitempty"`
	Columns       int            `json:"columns"`
	Rows          int            `json:"rows"`
	Overflow      OverflowPolicy `json:"overflow"`
	EmptyPriority int            `json:"emptyPriority"`
}

type ZoneHeightMode string

const (
	ZoneHeightAuto   ZoneHeightMode = "AUTO"
	ZoneHeightFixed  ZoneHeightMode = "FIXED"
	ZoneHeightFR     ZoneHeightMode = "FR"
	ZoneHeightHidden ZoneHeightMode = "HIDDEN"
)

type OverflowPolicy string

const (
	OverflowClip   OverflowPolicy = "CLIP"
	OverflowScroll OverflowPolicy = "SCROLL"
	OverflowExpand OverflowPolicy = "EXPAND"
)

type Slot struct {
	ID          askdata.ID   `json:"id"`
	Grid        SlotGrid     `json:"grid"`
	ComponentID askdata.ID   `json:"componentId,omitempty"`
	CardKind    string       `json:"cardKind,omitempty"`
	MergedFrom  []askdata.ID `json:"mergedFrom,omitempty"`
}

type SlotGrid struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type Component struct {
	ID          askdata.ID                 `json:"id"`
	TemplateRef ComponentTemplateReference `json:"templateRef"`
	DataBinding *DataBinding               `json:"dataBinding,omitempty"`
	Options     ComponentOptions           `json:"options"`
}

type ComponentTemplateReference struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

type BindingMode string

const (
	BindingSemanticIR   BindingMode = "SEMANTIC_IR"
	BindingDatasetField BindingMode = "DATASET_FIELD"
)

type DataBinding struct {
	BindingMode      BindingMode       `json:"bindingMode"`
	SemanticQueryRef *SemanticQueryRef `json:"semanticQueryRef,omitempty"`
	DataContextID    *askdata.ID       `json:"dataContextId,omitempty"`
	Dimensions       []FieldBinding    `json:"dimensions,omitempty"`
	Measures         []FieldBinding    `json:"measures,omitempty"`
}

// MarshalJSON preserves the closed union shape: semantic bindings never emit
// dataset fields, while dataset bindings always emit both arrays, including an
// intentionally empty dimensions array.
func (binding DataBinding) MarshalJSON() ([]byte, error) {
	if binding.BindingMode == BindingSemanticIR {
		return json.Marshal(struct {
			BindingMode      BindingMode       `json:"bindingMode"`
			SemanticQueryRef *SemanticQueryRef `json:"semanticQueryRef,omitempty"`
		}{
			BindingMode:      binding.BindingMode,
			SemanticQueryRef: binding.SemanticQueryRef,
		})
	}
	return json.Marshal(struct {
		BindingMode   BindingMode    `json:"bindingMode"`
		DataContextID *askdata.ID    `json:"dataContextId,omitempty"`
		Dimensions    []FieldBinding `json:"dimensions"`
		Measures      []FieldBinding `json:"measures"`
	}{
		BindingMode:   binding.BindingMode,
		DataContextID: binding.DataContextID,
		Dimensions:    binding.Dimensions,
		Measures:      binding.Measures,
	})
}

type SemanticQueryRef struct {
	SemanticReleaseID     askdata.ID                  `json:"semanticReleaseId"`
	SemanticContentHash   askdata.ContentHash         `json:"semanticContentHash"`
	SemanticIR            ircontract.SemanticIR       `json:"semanticIr"`
	QueryPlanHash         askdata.ContentHash         `json:"queryPlanHash"`
	SourceQuestionRunID   *askdata.ID                 `json:"sourceQuestionRunId,omitempty"`
	CompilationArtifactID *askdata.ID                 `json:"compilationArtifactId,omitempty"`
	DatasetVersionID      *askdata.ID                 `json:"datasetVersionId,omitempty"`
	ResolvedTimeSpec      *askdatair.ResolvedTimeSpec `json:"resolvedTimeSpec,omitempty"`
	EvidenceRefs          []askdata.EvidenceRef       `json:"evidenceRefs,omitempty"`
	ChartRuleVersion      string                      `json:"chartRuleVersion,omitempty"`
}

type BindingRole string

const (
	RoleDimension BindingRole = "DIMENSION"
	RoleSeries    BindingRole = "SERIES"
	RoleXAxis     BindingRole = "X_AXIS"
	RoleYAxis     BindingRole = "Y_AXIS"
	RoleValue     BindingRole = "VALUE"
	RoleCategory  BindingRole = "CATEGORY"
	RoleTime      BindingRole = "TIME"
	RoleColor     BindingRole = "COLOR"
	RoleSize      BindingRole = "SIZE"
	RoleLabel     BindingRole = "LABEL"
	RoleDetail    BindingRole = "DETAIL"
	RoleTooltip   BindingRole = "TOOLTIP"
)

type FieldBinding struct {
	Role  BindingRole `json:"role"`
	Field string      `json:"field"`
}

// ComponentOptions contains only renderer-independent V1 options. The
// component manifest further narrows this set for each type and version.
type ComponentOptions struct {
	Title            string           `json:"title,omitempty"`
	Subtitle         string           `json:"subtitle,omitempty"`
	ShowLegend       *bool            `json:"showLegend,omitempty"`
	ShowLabel        *bool            `json:"showLabel,omitempty"`
	Smooth           *bool            `json:"smooth,omitempty"`
	Orientation      Orientation      `json:"orientation,omitempty"`
	TopN             *int             `json:"topN,omitempty"`
	ColorPaletteRef  string           `json:"colorPaletteRef,omitempty"`
	NumberFormat     string           `json:"numberFormat,omitempty"`
	NullPolicy       NullPolicy       `json:"nullPolicy,omitempty"`
	Animation        *bool            `json:"animation,omitempty"`
	MobileLegendMode MobileLegendMode `json:"mobileLegendMode,omitempty"`
	RichText         string           `json:"richText,omitempty"`
	ImageAssetID     *askdata.ID      `json:"imageAssetId,omitempty"`
	InsightRole      string           `json:"insightRole,omitempty"`
	TablePageSize    *int             `json:"tablePageSize,omitempty"`
	CardVariant      string           `json:"cardVariant,omitempty"`
}

type Orientation string

const (
	OrientationHorizontal Orientation = "HORIZONTAL"
	OrientationVertical   Orientation = "VERTICAL"
)

type NullPolicy string

const (
	NullPolicyGap  NullPolicy = "GAP"
	NullPolicyZero NullPolicy = "ZERO"
	NullPolicyHide NullPolicy = "HIDE"
)

type MobileLegendMode string

const (
	MobileLegendVisible MobileLegendMode = "VISIBLE"
	MobileLegendHidden  MobileLegendMode = "HIDDEN"
	MobileLegendScroll  MobileLegendMode = "SCROLL"
)

type Interaction struct {
	ID                 askdata.ID        `json:"id"`
	SourceComponentID  askdata.ID        `json:"sourceComponentId"`
	Event              InteractionEvent  `json:"event"`
	Action             InteractionAction `json:"action"`
	TargetComponentIDs []askdata.ID      `json:"targetComponentIds"`
	TargetPageID       *askdata.ID       `json:"targetPageId,omitempty"`
	FieldMappings      []FieldMapping    `json:"fieldMappings"`
}

type InteractionEvent string

const (
	InteractionClick  InteractionEvent = "CLICK"
	InteractionSelect InteractionEvent = "SELECT"
	InteractionBrush  InteractionEvent = "BRUSH"
	InteractionZoom   InteractionEvent = "ZOOM"
)

type InteractionAction string

const (
	InteractionFilter       InteractionAction = "FILTER"
	InteractionDrillDown    InteractionAction = "DRILL_DOWN"
	InteractionHighlight    InteractionAction = "HIGHLIGHT"
	InteractionNavigatePage InteractionAction = "NAVIGATE_PAGE"
)

type FieldMapping struct {
	SourceField string `json:"sourceField"`
	TargetField string `json:"targetField"`
}

type RuntimePolicy struct {
	RefreshMode            RefreshMode `json:"refreshMode"`
	RefreshIntervalSeconds int         `json:"refreshIntervalSeconds"`
	MaxConcurrentQueries   int         `json:"maxConcurrentQueries"`
	ComponentTimeoutMS     int         `json:"componentTimeoutMs"`
	ExportEnabled          bool        `json:"exportEnabled"`
	FailureMode            FailureMode `json:"failureMode"`
}

type RefreshMode string

const (
	RefreshManual   RefreshMode = "MANUAL"
	RefreshOnOpen   RefreshMode = "ON_OPEN"
	RefreshInterval RefreshMode = "INTERVAL"
)

type FailureMode string

const (
	FailurePartial FailureMode = "PARTIAL"
	FailureStrict  FailureMode = "STRICT"
)

type Provenance struct {
	CreatedFrom            CreatedFrom                      `json:"createdFrom"`
	SourceQuestionRunIDs   []askdata.ID                     `json:"sourceQuestionRunIds"`
	AIRunIDs               []askdata.ID                     `json:"aiRunIds"`
	AnalysisMethodVersions []AnalysisMethodVersionReference `json:"analysisMethodVersions,omitempty"`
	PromptVersions         []string                         `json:"promptVersions,omitempty"`
	ModelPolicies          []string                         `json:"modelPolicies,omitempty"`
}

type AnalysisMethodVersionReference struct {
	AnalysisMethod string `json:"analysisMethod"`
	Version        string `json:"version"`
}

type CreatedFrom string

const (
	CreatedManually CreatedFrom = "MANUAL"
	CreatedTemplate CreatedFrom = "TEMPLATE"
	CreatedAI       CreatedFrom = "AI"
	CreatedAskData  CreatedFrom = "ASK_DATA"
	CreatedImport   CreatedFrom = "IMPORT"
)

func (definition ReportDefinition) Validate() error {
	if err := validateEncodedDefinition(definition); err != nil {
		return err
	}
	if definition.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if err := definition.Metadata.validate(); err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	if err := definition.TemplateRef.validate(); err != nil {
		return fmt.Errorf("templateRef: %w", err)
	}
	if err := definition.ThemeRef.validate(); err != nil {
		return fmt.Errorf("themeRef: %w", err)
	}
	if err := definition.Canvas.validate(); err != nil {
		return fmt.Errorf("canvas: %w", err)
	}
	if definition.DataContexts == nil || definition.GlobalFilters == nil || definition.Pages == nil ||
		definition.Components == nil || definition.Interactions == nil {
		return errors.New("top-level collection fields must be JSON arrays, not null")
	}
	if len(definition.DataContexts) > MaxDataContexts {
		return fmt.Errorf("dataContexts exceeds %d items", MaxDataContexts)
	}
	if len(definition.GlobalFilters) > MaxGlobalFilters {
		return fmt.Errorf("globalFilters exceeds %d items", MaxGlobalFilters)
	}
	if len(definition.Pages) == 0 || len(definition.Pages) > MaxPages {
		return fmt.Errorf("pages count must be between 1 and %d", MaxPages)
	}
	if len(definition.Components) > MaxComponents {
		return fmt.Errorf("components exceeds %d items", MaxComponents)
	}
	if len(definition.Interactions) > MaxInteractions {
		return fmt.Errorf("interactions exceeds %d items", MaxInteractions)
	}

	contextIDs := map[askdata.ID]struct{}{}
	for index, context := range definition.DataContexts {
		if err := context.validate(); err != nil {
			return fmt.Errorf("dataContexts[%d]: %w", index, err)
		}
		if err := addUniqueID(contextIDs, context.ID, fmt.Sprintf("dataContexts[%d].id", index)); err != nil {
			return err
		}
	}

	componentIDs := map[askdata.ID]struct{}{}
	for index, component := range definition.Components {
		if err := component.validate(contextIDs); err != nil {
			return fmt.Errorf("components[%d]: %w", index, err)
		}
		if err := addUniqueID(componentIDs, component.ID, fmt.Sprintf("components[%d].id", index)); err != nil {
			return err
		}
	}

	pageIDs := map[askdata.ID]struct{}{}
	sectionIDs := map[askdata.ID]struct{}{}
	blockIDs := map[askdata.ID]struct{}{}
	zoneIDs := map[askdata.ID]struct{}{}
	slotIDs := map[askdata.ID]struct{}{}
	componentPlacements := map[askdata.ID]int{}
	totalBlocks := 0
	for pageIndex, page := range definition.Pages {
		if err := page.ID.Validate(); err != nil {
			return fmt.Errorf("pages[%d].id: %w", pageIndex, err)
		}
		if err := addUniqueID(pageIDs, page.ID, fmt.Sprintf("pages[%d].id", pageIndex)); err != nil {
			return err
		}
		if strings.TrimSpace(page.Name) == "" || page.Order < 1 {
			return fmt.Errorf("pages[%d] requires name and positive order", pageIndex)
		}
		if len(page.Sections) > MaxSectionsPerPage {
			return fmt.Errorf("pages[%d].sections exceeds %d items", pageIndex, MaxSectionsPerPage)
		}
		if page.Sections == nil {
			return fmt.Errorf("pages[%d].sections must be a JSON array, not null", pageIndex)
		}
		for sectionIndex, section := range page.Sections {
			path := fmt.Sprintf("pages[%d].sections[%d]", pageIndex, sectionIndex)
			if err := section.ID.Validate(); err != nil {
				return fmt.Errorf("%s.id: %w", path, err)
			}
			if err := addUniqueID(sectionIDs, section.ID, path+".id"); err != nil {
				return err
			}
			if strings.TrimSpace(section.Name) == "" || section.Order < 1 {
				return fmt.Errorf("%s requires name and positive order", path)
			}
			if section.Blocks == nil {
				return fmt.Errorf("%s.blocks must be a JSON array, not null", path)
			}
			totalBlocks += len(section.Blocks)
			if totalBlocks > MaxBlocks {
				return fmt.Errorf("report blocks exceeds %d items", MaxBlocks)
			}
			for blockIndex, block := range section.Blocks {
				blockPath := fmt.Sprintf("%s.blocks[%d]", path, blockIndex)
				if err := addUniqueID(blockIDs, block.ID, blockPath+".id"); err != nil {
					return err
				}
				if err := block.validate(definition.Canvas.Desktop.Columns, componentIDs, zoneIDs, slotIDs); err != nil {
					return fmt.Errorf("%s: %w", blockPath, err)
				}
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						if slot.ComponentID != "" {
							componentPlacements[slot.ComponentID]++
						}
					}
				}
			}
		}
	}
	for componentID := range componentIDs {
		if componentPlacements[componentID] != 1 {
			return fmt.Errorf("component %q must be placed in exactly one slot", componentID)
		}
	}

	filterIDs := map[askdata.ID]struct{}{}
	for index, filter := range definition.GlobalFilters {
		if err := addUniqueID(filterIDs, filter.ID, fmt.Sprintf("globalFilters[%d].id", index)); err != nil {
			return err
		}
		if err := filter.validate(contextIDs, pageIDs, sectionIDs, blockIDs, componentIDs); err != nil {
			return fmt.Errorf("globalFilters[%d]: %w", index, err)
		}
	}

	interactionIDs := map[askdata.ID]struct{}{}
	for index, interaction := range definition.Interactions {
		if err := addUniqueID(interactionIDs, interaction.ID, fmt.Sprintf("interactions[%d].id", index)); err != nil {
			return err
		}
		if err := interaction.validate(componentIDs, pageIDs); err != nil {
			return fmt.Errorf("interactions[%d]: %w", index, err)
		}
	}
	if err := definition.RuntimePolicy.validate(); err != nil {
		return fmt.Errorf("runtimePolicy: %w", err)
	}
	if err := definition.Provenance.validate(); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	return nil
}

func (metadata Metadata) validate() error {
	if err := metadata.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if !codePattern.MatchString(metadata.Code) {
		return errors.New("code is invalid")
	}
	if strings.TrimSpace(metadata.Name) == "" {
		return errors.New("name is required")
	}
	if metadata.ReportType != ReportTypeReport && metadata.ReportType != ReportTypeDashboard {
		return errors.New("reportType is invalid")
	}
	if !localePattern.MatchString(metadata.Locale) {
		return errors.New("locale is invalid")
	}
	return nil
}

func (reference TemplateReference) validate() error {
	if err := reference.ReportTemplateID.Validate(); err != nil {
		return fmt.Errorf("reportTemplateId: %w", err)
	}
	versions := map[string]string{
		"reportTemplateVersion":    reference.ReportTemplateVersion,
		"structureTemplateVersion": reference.StructureTemplateVersion,
		"layoutTemplateVersion":    reference.LayoutTemplateVersion,
		"narrativeTemplateVersion": reference.NarrativeTemplateVersion,
	}
	for name, version := range versions {
		if !semverPattern.MatchString(version) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func (reference ThemeReference) validate() error {
	if err := reference.ThemeID.Validate(); err != nil {
		return fmt.Errorf("themeId: %w", err)
	}
	if !semverPattern.MatchString(reference.Version) {
		return errors.New("version is invalid")
	}
	return nil
}

func (canvas Canvas) validate() error {
	desktop := canvas.Desktop
	if desktop.DesignWidth != 1920 || desktop.Columns != 24 || desktop.BaseCellWidth != 80 ||
		desktop.BaseRowHeight != 54 || desktop.GapX != 12 || desktop.GapY != 12 ||
		desktop.PaddingX != 24 || desktop.PaddingY != 24 {
		return errors.New("desktop must use the frozen 1920/24-column V1 canvas")
	}
	mobile := canvas.Mobile
	if mobile.Columns != 1 || mobile.GapY != 12 || mobile.PaddingX != 12 || mobile.PaddingY != 12 {
		return errors.New("mobile must use the frozen single-column V1 canvas")
	}
	return nil
}

func (context DataContext) validate() error {
	if err := context.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if err := context.DatasetID.Validate(); err != nil {
		return fmt.Errorf("datasetId: %w", err)
	}
	if err := context.DatasetVersionID.Validate(); err != nil {
		return fmt.Errorf("datasetVersionId: %w", err)
	}
	if strings.TrimSpace(context.Alias) == "" {
		return errors.New("alias is required")
	}
	if len(context.DefaultParameters) > MaxParameters {
		return fmt.Errorf("defaultParameters exceeds %d items", MaxParameters)
	}
	if context.DefaultParameters == nil {
		return errors.New("defaultParameters must be a JSON array, not null")
	}
	parameters := map[string]struct{}{}
	for index, parameter := range context.DefaultParameters {
		if !parameterPattern.MatchString(parameter.Name) {
			return fmt.Errorf("defaultParameters[%d].name is invalid", index)
		}
		if _, exists := parameters[parameter.Name]; exists {
			return fmt.Errorf("defaultParameters[%d] duplicates name %q", index, parameter.Name)
		}
		parameters[parameter.Name] = struct{}{}
		if err := parameter.validate(); err != nil {
			return fmt.Errorf("defaultParameters[%d]: %w", index, err)
		}
	}
	if context.QueryPolicy.TimeoutMS < 100 || context.QueryPolicy.TimeoutMS > 120_000 {
		return errors.New("queryPolicy.timeoutMs must be between 100 and 120000")
	}
	if context.QueryPolicy.MaxRows < 1 || context.QueryPolicy.MaxRows > 10_000 {
		return errors.New("queryPolicy.maxRows must be between 1 and 10000")
	}
	if context.QueryPolicy.CacheTTLSeconds < 0 || context.QueryPolicy.CacheTTLSeconds > 86_400 {
		return errors.New("queryPolicy.cacheTtlSeconds must be between 0 and 86400")
	}
	return nil
}

func (parameter DefaultParameter) validate() error {
	set := 0
	if parameter.StringValue != nil {
		set++
	}
	if parameter.NumberValue != nil {
		set++
	}
	if parameter.BooleanValue != nil {
		set++
	}
	if set != 1 {
		return errors.New("exactly one typed value is required")
	}
	switch parameter.Type {
	case ParameterString:
		if parameter.StringValue == nil {
			return errors.New("STRING requires stringValue")
		}
	case ParameterNumber:
		if parameter.NumberValue == nil {
			return errors.New("NUMBER requires numberValue")
		}
	case ParameterBoolean:
		if parameter.BooleanValue == nil {
			return errors.New("BOOLEAN requires booleanValue")
		}
	default:
		return errors.New("type is invalid")
	}
	return nil
}

func (block Block) validate(columns int, componentIDs, zoneIDs, slotIDs map[askdata.ID]struct{}) error {
	if err := block.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if !validBlockType(block.Type) {
		return errors.New("type is invalid")
	}
	if len([]rune(block.Title)) > 200 {
		return errors.New("title exceeds 200 characters")
	}
	desktop := block.Layout.Desktop
	if desktop.X < 0 || desktop.Y < 0 || desktop.W < 1 || desktop.H < 1 || desktop.X+desktop.W > columns {
		return fmt.Errorf("layout.desktop must fit the %d-column canvas", columns)
	}
	if err := block.Layout.Mobile.validate(); err != nil {
		return fmt.Errorf("layout.mobile: %w", err)
	}
	if len(block.Zones) > MaxZonesPerBlock {
		return fmt.Errorf("zones exceeds %d items", MaxZonesPerBlock)
	}
	if block.Zones == nil {
		return errors.New("zones must be a JSON array, not null")
	}
	totalSlots := 0
	orderedZones := 0
	for zoneIndex, zone := range block.Zones {
		path := fmt.Sprintf("zones[%d]", zoneIndex)
		if err := addUniqueID(zoneIDs, zone.ID, path+".id"); err != nil {
			return err
		}
		if err := zone.validate(componentIDs, slotIDs); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if zone.Order > 0 {
			orderedZones++
		}
		totalSlots += len(zone.Slots)
		if totalSlots > MaxSlotsPerBlock {
			return fmt.Errorf("slots across block exceeds %d items", MaxSlotsPerBlock)
		}
	}
	// A whole block without explicit zone orders is a legacy immutable V1
	// artifact. Mixing legacy and explicit ordering is ambiguous and therefore
	// rejected; normal edit operations upgrade all zones together.
	if orderedZones != 0 && orderedZones != len(block.Zones) {
		return errors.New("zones must either all declare order or all use legacy ordering")
	}
	return nil
}

func (layout MobileBlockLayout) validate() error {
	if layout.Order < 1 {
		return errors.New("order must be positive")
	}
	if layout.SlotMode != MobileSlotStack && layout.SlotMode != MobileSlotCarousel &&
		layout.SlotMode != MobileSlotPrimaryOnly && layout.SlotMode != MobileSlotCollapse {
		return errors.New("slotMode is invalid")
	}
	if layout.SlotMode == MobileSlotPrimaryOnly {
		if layout.PrimarySlotID == nil || layout.PrimarySlotID.Validate() != nil {
			return errors.New("PRIMARY_ONLY requires a valid primarySlotId")
		}
	} else if layout.PrimarySlotID != nil {
		return errors.New("primarySlotId is only valid for PRIMARY_ONLY")
	}
	switch layout.HeightMode {
	case MobileHeightAuto:
		if layout.FixedHeight != nil || layout.AspectRatio != nil {
			return errors.New("AUTO cannot set fixedHeight or aspectRatio")
		}
	case MobileHeightFixed:
		if layout.FixedHeight == nil || *layout.FixedHeight < 40 || layout.AspectRatio != nil {
			return errors.New("FIXED requires fixedHeight >= 40 only")
		}
	case MobileHeightAspectRatio:
		if layout.AspectRatio == nil || *layout.AspectRatio <= 0 || layout.FixedHeight != nil {
			return errors.New("ASPECT_RATIO requires a positive aspectRatio only")
		}
	default:
		return errors.New("heightMode is invalid")
	}
	return nil
}

func (zone Zone) validate(componentIDs, slotIDs map[askdata.ID]struct{}) error {
	if err := zone.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if zone.Order < 0 {
		return errors.New("order cannot be negative")
	}
	if zone.Type != ZoneHeader && zone.Type != ZoneFilter && zone.Type != ZoneInsight &&
		zone.Type != ZoneContent && zone.Type != ZoneFooter {
		return errors.New("type is invalid")
	}
	if err := zone.Layout.validate(); err != nil {
		return fmt.Errorf("layout: %w", err)
	}
	if zone.Slots == nil {
		return errors.New("slots must be a JSON array, not null")
	}
	for index, slot := range zone.Slots {
		if err := addUniqueID(slotIDs, slot.ID, fmt.Sprintf("slots[%d].id", index)); err != nil {
			return err
		}
		if err := slot.validate(zone.Layout.Columns, zone.Layout.Rows, componentIDs); err != nil {
			return fmt.Errorf("slots[%d]: %w", index, err)
		}
	}
	return nil
}

func (layout ZoneLayout) validate() error {
	if layout.Columns < 1 || layout.Columns > 24 || layout.Rows < 1 || layout.Rows > 100 {
		return errors.New("columns/rows are outside supported grid bounds")
	}
	if layout.MinHeight < 0 || (layout.MaxHeight != nil && *layout.MaxHeight < layout.MinHeight) {
		return errors.New("height bounds are invalid")
	}
	if layout.Overflow != OverflowClip && layout.Overflow != OverflowScroll && layout.Overflow != OverflowExpand {
		return errors.New("overflow is invalid")
	}
	switch layout.HeightMode {
	case ZoneHeightAuto, ZoneHeightHidden:
		if layout.FixedHeight != nil || layout.FR != nil {
			return errors.New("AUTO/HIDDEN cannot set fixedHeight or fr")
		}
	case ZoneHeightFixed:
		if layout.FixedHeight == nil || *layout.FixedHeight < layout.MinHeight || layout.FR != nil {
			return errors.New("FIXED requires fixedHeight within the declared bounds")
		}
	case ZoneHeightFR:
		if layout.FR == nil || *layout.FR <= 0 || layout.FixedHeight != nil {
			return errors.New("FR requires a positive fr only")
		}
	default:
		return errors.New("heightMode is invalid")
	}
	return nil
}

func (slot Slot) validate(columns, rows int, componentIDs map[askdata.ID]struct{}) error {
	if err := slot.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	grid := slot.Grid
	if grid.X < 0 || grid.Y < 0 || grid.W < 1 || grid.H < 1 || grid.X+grid.W > columns || grid.Y+grid.H > rows {
		return errors.New("grid exceeds zone bounds")
	}
	if slot.ComponentID != "" {
		if _, exists := componentIDs[slot.ComponentID]; !exists {
			return fmt.Errorf("componentId %q does not reference a component", slot.ComponentID)
		}
	}
	if len([]rune(slot.CardKind)) > 64 {
		return errors.New("cardKind exceeds 64 characters")
	}
	seenMerged := map[askdata.ID]struct{}{}
	if len(slot.MergedFrom) == 1 {
		return errors.New("mergedFrom must contain at least two source slot IDs")
	}
	for index, id := range slot.MergedFrom {
		if id.Validate() != nil || id == slot.ID {
			return fmt.Errorf("mergedFrom[%d] is invalid", index)
		}
		if _, duplicate := seenMerged[id]; duplicate {
			return fmt.Errorf("mergedFrom[%d] is duplicated", index)
		}
		seenMerged[id] = struct{}{}
	}
	return nil
}

func (component Component) validate(contextIDs map[askdata.ID]struct{}) error {
	if err := component.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if !componentPattern.MatchString(component.TemplateRef.Type) {
		return errors.New("templateRef.type is invalid")
	}
	if !semverPattern.MatchString(component.TemplateRef.Version) {
		return errors.New("templateRef.version is invalid")
	}
	if component.DataBinding != nil {
		if err := component.DataBinding.validate(contextIDs); err != nil {
			return fmt.Errorf("dataBinding: %w", err)
		}
	}
	return component.Options.validate()
}

func (binding DataBinding) validate(contextIDs map[askdata.ID]struct{}) error {
	switch binding.BindingMode {
	case BindingSemanticIR:
		if binding.SemanticQueryRef == nil || binding.DataContextID != nil || len(binding.Dimensions) != 0 || len(binding.Measures) != 0 {
			return errors.New("SEMANTIC_IR requires semanticQueryRef and forbids dataset fields")
		}
		return binding.SemanticQueryRef.validate()
	case BindingDatasetField:
		if binding.SemanticQueryRef != nil || binding.DataContextID == nil {
			return errors.New("DATASET_FIELD requires dataContextId and forbids semanticQueryRef")
		}
		if binding.Dimensions == nil || binding.Measures == nil {
			return errors.New("DATASET_FIELD dimensions and measures must be JSON arrays, not null")
		}
		if _, exists := contextIDs[*binding.DataContextID]; !exists {
			return fmt.Errorf("dataContextId %q does not reference a data context", *binding.DataContextID)
		}
		if len(binding.Dimensions) > MaxBindingsPerRole || len(binding.Measures) == 0 || len(binding.Measures) > MaxBindingsPerRole {
			return fmt.Errorf("DATASET_FIELD requires 1..%d measures and at most %d dimensions", MaxBindingsPerRole, MaxBindingsPerRole)
		}
		seen := map[string]struct{}{}
		for index, item := range append(append([]FieldBinding(nil), binding.Dimensions...), binding.Measures...) {
			if err := item.validate(); err != nil {
				return fmt.Errorf("field binding %d: %w", index, err)
			}
			key := string(item.Role) + "\x00" + item.Field
			if _, exists := seen[key]; exists {
				return fmt.Errorf("field binding %d is duplicated", index)
			}
			seen[key] = struct{}{}
		}
	default:
		return errors.New("bindingMode is invalid")
	}
	return nil
}

func (reference SemanticQueryRef) validate() error {
	if err := reference.SemanticReleaseID.Validate(); err != nil {
		return fmt.Errorf("semanticReleaseId: %w", err)
	}
	if err := reference.SemanticContentHash.Validate(); err != nil {
		return fmt.Errorf("semanticContentHash: %w", err)
	}
	if reference.SemanticIR.SemanticReleaseID != reference.SemanticReleaseID ||
		reference.SemanticIR.SemanticContentHash != reference.SemanticContentHash {
		return errors.New("semantic release ID/hash must match semanticIr")
	}
	if err := reference.SemanticIR.Validate(); err != nil {
		return fmt.Errorf("semanticIr: %w", err)
	}
	if err := reference.QueryPlanHash.Validate(); err != nil {
		return fmt.Errorf("queryPlanHash: %w", err)
	}
	if reference.SourceQuestionRunID != nil {
		if err := reference.SourceQuestionRunID.Validate(); err != nil {
			return fmt.Errorf("sourceQuestionRunId: %w", err)
		}
	}
	if reference.CompilationArtifactID != nil {
		if err := reference.CompilationArtifactID.Validate(); err != nil {
			return fmt.Errorf("compilationArtifactId: %w", err)
		}
	}
	if reference.SourceQuestionRunID != nil && reference.CompilationArtifactID != nil {
		return errors.New("semantic query ref cannot mix question and upgrade compilation sources")
	}
	if reference.DatasetVersionID != nil {
		if err := reference.DatasetVersionID.Validate(); err != nil {
			return fmt.Errorf("datasetVersionId: %w", err)
		}
	}
	if reference.ResolvedTimeSpec != nil {
		value := reference.ResolvedTimeSpec
		if value.RequestedPeriod == "" || value.Grain == "" || value.PolicyApplied == "" ||
			value.PolicySource == "" || value.ResolvedStart.IsZero() || value.ResolvedEndExclusive.IsZero() ||
			!value.ResolvedEndExclusive.After(value.ResolvedStart) || value.DataAvailableThrough.IsZero() ||
			value.Timezone == "" {
			return errors.New("resolvedTimeSpec is incomplete")
		}
		if _, err := time.LoadLocation(value.Timezone); err != nil {
			return errors.New("resolvedTimeSpec.timezone is invalid")
		}
	}
	if len(reference.EvidenceRefs) > 64 {
		return errors.New("evidenceRefs exceeds 64 items")
	}
	seenEvidence := map[askdata.ID]struct{}{}
	for index, evidence := range reference.EvidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
		if _, duplicate := seenEvidence[evidence.EvidenceID]; duplicate {
			return fmt.Errorf("evidenceRefs[%d] is duplicated", index)
		}
		seenEvidence[evidence.EvidenceID] = struct{}{}
	}
	if reference.ChartRuleVersion != "" && !semverPattern.MatchString(reference.ChartRuleVersion) {
		return errors.New("chartRuleVersion must be SemVer")
	}
	return nil
}

func (binding FieldBinding) validate() error {
	if !validBindingRole(binding.Role) {
		return errors.New("role is invalid")
	}
	if !fieldPattern.MatchString(binding.Field) {
		return errors.New("field must be a logical field identifier")
	}
	return nil
}

func (options ComponentOptions) validate() error {
	if options.RichText != SanitizeRichText(options.RichText) {
		return errors.New("options.richText contains forbidden or non-canonical HTML")
	}
	if options.Orientation != "" && options.Orientation != OrientationHorizontal && options.Orientation != OrientationVertical {
		return errors.New("options.orientation is invalid")
	}
	if options.TopN != nil && (*options.TopN < 1 || *options.TopN > 1_000) {
		return errors.New("options.topN must be between 1 and 1000")
	}
	if options.NullPolicy != "" && options.NullPolicy != NullPolicyGap && options.NullPolicy != NullPolicyZero && options.NullPolicy != NullPolicyHide {
		return errors.New("options.nullPolicy is invalid")
	}
	if options.MobileLegendMode != "" && options.MobileLegendMode != MobileLegendVisible && options.MobileLegendMode != MobileLegendHidden && options.MobileLegendMode != MobileLegendScroll {
		return errors.New("options.mobileLegendMode is invalid")
	}
	if options.CardVariant != "" && options.CardVariant != "01" && options.CardVariant != "02" && options.CardVariant != "03" {
		return errors.New("options.cardVariant is invalid")
	}
	if options.ImageAssetID != nil {
		if err := options.ImageAssetID.Validate(); err != nil {
			return fmt.Errorf("options.imageAssetId: %w", err)
		}
	}
	if options.TablePageSize != nil && (*options.TablePageSize < 1 || *options.TablePageSize > 1_000) {
		return errors.New("options.tablePageSize must be between 1 and 1000")
	}
	return nil
}

func (filter GlobalFilter) validate(contextIDs, pageIDs, sectionIDs, blockIDs, componentIDs map[askdata.ID]struct{}) error {
	if err := filter.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	switch filter.Type {
	case FilterSingleSelect, FilterMultiSelect, FilterDate, FilterDateRange,
		FilterRelativeTime, FilterNumberRange, FilterSearchSelect, FilterParameterInput,
		FilterSelect, FilterBoolean:
	default:
		return errors.New("type is invalid")
	}
	if _, exists := contextIDs[filter.FieldRef.DataContextID]; !exists {
		return errors.New("fieldRef.dataContextId does not reference a data context")
	}
	if !fieldPattern.MatchString(filter.FieldRef.Field) {
		return errors.New("fieldRef.field must be a logical field identifier")
	}
	var namespace map[askdata.ID]struct{}
	if filter.Scope.TargetIDs == nil {
		return errors.New("scope.targetIds must be a JSON array, not null")
	}
	switch filter.Scope.Type {
	case FilterScopeReport:
		if len(filter.Scope.TargetIDs) != 0 {
			return errors.New("REPORT scope must not have targetIds")
		}
		return nil
	case FilterScopePage:
		namespace = pageIDs
	case FilterScopeSection:
		namespace = sectionIDs
	case FilterScopeBlock:
		namespace = blockIDs
	case FilterScopeComponent:
		namespace = componentIDs
	default:
		return errors.New("scope.type is invalid")
	}
	if len(filter.Scope.TargetIDs) == 0 {
		return errors.New("scoped filter requires targetIds")
	}
	seenTargets := map[askdata.ID]struct{}{}
	for _, targetID := range filter.Scope.TargetIDs {
		if _, exists := namespace[targetID]; !exists {
			return fmt.Errorf("scope target %q does not exist", targetID)
		}
		if _, exists := seenTargets[targetID]; exists {
			return fmt.Errorf("scope target %q is duplicated", targetID)
		}
		seenTargets[targetID] = struct{}{}
	}
	return nil
}

func (interaction Interaction) validate(componentIDs, pageIDs map[askdata.ID]struct{}) error {
	if err := interaction.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if _, exists := componentIDs[interaction.SourceComponentID]; !exists {
		return errors.New("sourceComponentId does not reference a component")
	}
	if interaction.TargetComponentIDs == nil || interaction.FieldMappings == nil {
		return errors.New("targetComponentIds and fieldMappings must be JSON arrays, not null")
	}
	switch interaction.Event {
	case InteractionClick, InteractionSelect, InteractionBrush, InteractionZoom:
	default:
		return errors.New("event is invalid")
	}
	switch interaction.Action {
	case InteractionFilter, InteractionDrillDown, InteractionHighlight:
		if len(interaction.TargetComponentIDs) == 0 || interaction.TargetPageID != nil {
			return errors.New("component action requires targetComponentIds only")
		}
	case InteractionNavigatePage:
		if interaction.TargetPageID == nil || len(interaction.TargetComponentIDs) != 0 {
			return errors.New("NAVIGATE_PAGE requires targetPageId only")
		}
		if _, exists := pageIDs[*interaction.TargetPageID]; !exists {
			return errors.New("targetPageId does not reference a page")
		}
	default:
		return errors.New("action is invalid")
	}
	if len(interaction.TargetComponentIDs) > MaxInteractionLinks || len(interaction.FieldMappings) > MaxInteractionLinks {
		return fmt.Errorf("interaction links exceed %d items", MaxInteractionLinks)
	}
	seenTargets := map[askdata.ID]struct{}{}
	for _, targetID := range interaction.TargetComponentIDs {
		if _, exists := componentIDs[targetID]; !exists {
			return fmt.Errorf("target component %q does not exist", targetID)
		}
		if _, exists := seenTargets[targetID]; exists {
			return fmt.Errorf("target component %q is duplicated", targetID)
		}
		seenTargets[targetID] = struct{}{}
	}
	for index, mapping := range interaction.FieldMappings {
		if !fieldPattern.MatchString(mapping.SourceField) || !fieldPattern.MatchString(mapping.TargetField) {
			return fmt.Errorf("fieldMappings[%d] must use logical field identifiers", index)
		}
	}
	return nil
}

func (policy RuntimePolicy) validate() error {
	if policy.RefreshMode != RefreshManual && policy.RefreshMode != RefreshOnOpen && policy.RefreshMode != RefreshInterval {
		return errors.New("refreshMode is invalid")
	}
	if policy.RefreshMode == RefreshInterval {
		if policy.RefreshIntervalSeconds < 60 || policy.RefreshIntervalSeconds > 86_400 {
			return errors.New("INTERVAL requires refreshIntervalSeconds between 60 and 86400")
		}
	} else if policy.RefreshIntervalSeconds != 0 {
		return errors.New("refreshIntervalSeconds is only valid for INTERVAL")
	}
	if policy.MaxConcurrentQueries < 1 || policy.MaxConcurrentQueries > 32 {
		return errors.New("maxConcurrentQueries must be between 1 and 32")
	}
	if policy.ComponentTimeoutMS < 100 || policy.ComponentTimeoutMS > 120_000 {
		return errors.New("componentTimeoutMs must be between 100 and 120000")
	}
	if policy.FailureMode != FailurePartial && policy.FailureMode != FailureStrict {
		return errors.New("failureMode is invalid")
	}
	return nil
}

func (provenance Provenance) validate() error {
	switch provenance.CreatedFrom {
	case CreatedManually, CreatedTemplate, CreatedAI, CreatedAskData, CreatedImport:
	default:
		return errors.New("createdFrom is invalid")
	}
	if provenance.SourceQuestionRunIDs == nil || provenance.AIRunIDs == nil {
		return errors.New("sourceQuestionRunIds and aiRunIds must be JSON arrays, not null")
	}
	for name, ids := range map[string][]askdata.ID{
		"sourceQuestionRunIds": provenance.SourceQuestionRunIDs,
		"aiRunIds":             provenance.AIRunIDs,
	} {
		if len(ids) > 128 {
			return fmt.Errorf("%s exceeds 128 items", name)
		}
		seen := map[askdata.ID]struct{}{}
		for index, id := range ids {
			if err := id.Validate(); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, index, err)
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("%s[%d] is duplicated", name, index)
			}
			seen[id] = struct{}{}
		}
	}
	if len(provenance.AnalysisMethodVersions) > 128 || len(provenance.PromptVersions) > 128 || len(provenance.ModelPolicies) > 128 {
		return errors.New("version provenance collections cannot exceed 128 items")
	}
	seenMethods := map[string]struct{}{}
	for index, reference := range provenance.AnalysisMethodVersions {
		if askdata.ID(reference.AnalysisMethod).Validate() != nil || !semverPattern.MatchString(reference.Version) {
			return fmt.Errorf("analysisMethodVersions[%d] is invalid", index)
		}
		key := reference.AnalysisMethod + "\x00" + reference.Version
		if _, duplicate := seenMethods[key]; duplicate {
			return fmt.Errorf("analysisMethodVersions[%d] is duplicated", index)
		}
		seenMethods[key] = struct{}{}
	}
	for name, values := range map[string][]string{"promptVersions": provenance.PromptVersions, "modelPolicies": provenance.ModelPolicies} {
		seen := map[string]struct{}{}
		for index, value := range values {
			if strings.TrimSpace(value) != value || askdata.ID(value).Validate() != nil {
				return fmt.Errorf("%s[%d] is invalid", name, index)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("%s[%d] is duplicated", name, index)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func addUniqueID(namespace map[askdata.ID]struct{}, id askdata.ID, path string) error {
	if err := id.Validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, exists := namespace[id]; exists {
		return fmt.Errorf("%s duplicates ID %q in its namespace", path, id)
	}
	namespace[id] = struct{}{}
	return nil
}

func validBlockType(value BlockType) bool {
	switch value {
	case BlockAnalysisCard, BlockKPIGroup, BlockChart, BlockTable, BlockContent, BlockFilter:
		return true
	default:
		return false
	}
}

func validBindingRole(value BindingRole) bool {
	values := []BindingRole{RoleDimension, RoleSeries, RoleXAxis, RoleYAxis, RoleValue, RoleCategory, RoleTime, RoleColor, RoleSize, RoleLabel, RoleDetail, RoleTooltip}
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
