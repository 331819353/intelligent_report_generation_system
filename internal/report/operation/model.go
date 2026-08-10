// Package operation defines the closed Report Operation v1 protocol shared by
// human edits, AI plans, template application and imports.
package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

const (
	SchemaVersion         = "1.0"
	MaxOperations         = 100
	MaxAIOperations       = 30
	MaxAIDeleteOperations = 5
)

type Source string

const (
	SourceUser   Source = "USER"
	SourceAI     Source = "AI"
	SourceImport Source = "IMPORT"
	SourceSystem Source = "SYSTEM"
)

type Type string

const (
	ReportCreate         Type = "REPORT_CREATE"
	ReportSettingsUpdate Type = "REPORT_SETTINGS_UPDATE"
	TemplateApply        Type = "TEMPLATE_APPLY"
	ThemeUpdate          Type = "THEME_UPDATE"
	PageCreate           Type = "PAGE_CREATE"
	PageUpdate           Type = "PAGE_UPDATE"
	PageDelete           Type = "PAGE_DELETE"
	PageReorder          Type = "PAGE_REORDER"
	SectionCreate        Type = "SECTION_CREATE"
	SectionUpdate        Type = "SECTION_UPDATE"
	SectionDelete        Type = "SECTION_DELETE"
	SectionReorder       Type = "SECTION_REORDER"
	BlockCreate          Type = "BLOCK_CREATE"
	BlockMove            Type = "BLOCK_MOVE"
	BlockResize          Type = "BLOCK_RESIZE"
	BlockUpdate          Type = "BLOCK_UPDATE"
	BlockCopy            Type = "BLOCK_COPY"
	BlockDelete          Type = "BLOCK_DELETE"
	ZoneCreate           Type = "ZONE_CREATE"
	ZoneUpdate           Type = "ZONE_UPDATE"
	ZoneDelete           Type = "ZONE_DELETE"
	ZoneReorder          Type = "ZONE_REORDER"
	SlotCreate           Type = "SLOT_CREATE"
	SlotMerge            Type = "SLOT_MERGE"
	SlotSplit            Type = "SLOT_SPLIT"
	SlotUpdate           Type = "SLOT_UPDATE"
	SlotDelete           Type = "SLOT_DELETE"
	ComponentCreate      Type = "COMPONENT_CREATE"
	ComponentUpdate      Type = "COMPONENT_UPDATE"
	ComponentReplace     Type = "COMPONENT_REPLACE"
	ComponentCopy        Type = "COMPONENT_COPY"
	ComponentDelete      Type = "COMPONENT_DELETE"
	DataBindingUpdate    Type = "DATA_BINDING_UPDATE"
	FilterCreate         Type = "FILTER_CREATE"
	FilterUpdate         Type = "FILTER_UPDATE"
	FilterDelete         Type = "FILTER_DELETE"
	InteractionCreate    Type = "INTERACTION_CREATE"
	InteractionUpdate    Type = "INTERACTION_UPDATE"
	InteractionDelete    Type = "INTERACTION_DELETE"
	InsightUpdate        Type = "INSIGHT_UPDATE"
	InsightRegenerate    Type = "INSIGHT_REGENERATE"
)

var allTypes = []Type{
	ReportCreate, ReportSettingsUpdate, TemplateApply, ThemeUpdate,
	PageCreate, PageUpdate, PageDelete, PageReorder,
	SectionCreate, SectionUpdate, SectionDelete, SectionReorder,
	BlockCreate, BlockMove, BlockResize, BlockUpdate, BlockCopy, BlockDelete,
	ZoneCreate, ZoneUpdate, ZoneDelete, ZoneReorder,
	SlotCreate, SlotMerge, SlotSplit, SlotUpdate, SlotDelete,
	ComponentCreate, ComponentUpdate, ComponentReplace, ComponentCopy, ComponentDelete,
	DataBindingUpdate,
	FilterCreate, FilterUpdate, FilterDelete,
	InteractionCreate, InteractionUpdate, InteractionDelete,
	InsightUpdate, InsightRegenerate,
}

func Types() []Type {
	return append([]Type(nil), allTypes...)
}

type Bundle struct {
	SchemaVersion string      `json:"schemaVersion"`
	ReportID      askdata.ID  `json:"reportId"`
	BaseRevision  int64       `json:"baseRevision"`
	Source        Source      `json:"source"`
	AIRunID       *askdata.ID `json:"aiRunId"`
	Scope         *Scope      `json:"scope"`
	Operations    []Operation `json:"operations"`
}

type Scope struct {
	PageID    *askdata.ID `json:"pageId,omitempty"`
	SectionID *askdata.ID `json:"sectionId,omitempty"`
	BlockID   *askdata.ID `json:"blockId,omitempty"`
}

type Operation struct {
	Op       Type       `json:"op"`
	TargetID askdata.ID `json:"targetId"`
	Payload  Payload    `json:"-"`
}

type Payload interface {
	operationPayload()
	Validate() error
}

type ReportCreatePayload struct {
	Definition report.ReportDefinition `json:"definition"`
}

type ReportSettingsUpdatePayload struct {
	Metadata      report.Metadata      `json:"metadata"`
	RuntimePolicy report.RuntimePolicy `json:"runtimePolicy"`
}

type TemplateApplyPayload struct {
	TemplateRef report.TemplateReference `json:"templateRef"`
}

type ThemeUpdatePayload struct {
	ThemeRef report.ThemeReference `json:"themeRef"`
}

type PageCreatePayload struct {
	Page report.Page `json:"page"`
}

type PageUpdatePayload struct {
	Name string `json:"name"`
}

type PageDeletePayload struct{}

type PageReorderPayload struct {
	Order int `json:"order"`
}

type SectionCreatePayload struct {
	Section report.Section `json:"section"`
}

type SectionUpdatePayload struct {
	Name string `json:"name"`
}

type SectionDeletePayload struct{}

type SectionReorderPayload struct {
	Order int `json:"order"`
}

type BlockCreatePayload struct {
	Block report.Block `json:"block"`
}

type BlockMovePayload struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type BlockResizePayload struct {
	W int `json:"w"`
	H int `json:"h"`
}

type BlockUpdatePayload struct {
	Type report.BlockType `json:"type"`
}

type BlockCopyPayload struct {
	NewID askdata.ID `json:"newId"`
}

type BlockDeletePayload struct{}

type ZoneCreatePayload struct {
	Zone report.Zone `json:"zone"`
}

type ZoneUpdatePayload struct {
	Type   report.ZoneType   `json:"type"`
	Layout report.ZoneLayout `json:"layout"`
}

type ZoneDeletePayload struct{}

type ZoneReorderPayload struct {
	Order int `json:"order"`
}

type SlotCreatePayload struct {
	Slot report.Slot `json:"slot"`
}

type SlotMergePayload struct {
	SlotIDs []askdata.ID `json:"slotIds"`
	NewSlot report.Slot  `json:"newSlot"`
	// restoreMergedFrom is set only by an in-memory inverse operation. External
	// SLOT_MERGE payloads always derive complete provenance from slotIds.
	restoreMergedFrom bool
}

type SlotSplitPayload struct {
	Slots []report.Slot `json:"slots"`
}

type SlotUpdatePayload struct {
	Grid        report.SlotGrid `json:"grid"`
	ComponentID askdata.ID      `json:"componentId"`
}

type SlotDeletePayload struct{}

type ComponentCreatePayload struct {
	Component report.Component `json:"component"`
}

type ComponentUpdatePayload struct {
	Options report.ComponentOptions `json:"options"`
}

type ComponentReplacePayload struct {
	Component report.Component `json:"component"`
}

type ComponentCopyPayload struct {
	NewID askdata.ID `json:"newId"`
}

type ComponentDeletePayload struct{}

type DataBindingUpdateMode string

const (
	DataBindingSet   DataBindingUpdateMode = "SET"
	DataBindingClear DataBindingUpdateMode = "CLEAR"
)

type DataBindingUpdatePayload struct {
	Mode        DataBindingUpdateMode `json:"mode"`
	DataBinding *report.DataBinding   `json:"dataBinding"`
}

type FilterCreatePayload struct {
	Filter report.GlobalFilter `json:"filter"`
}

type FilterUpdatePayload struct {
	Filter report.GlobalFilter `json:"filter"`
}

type FilterDeletePayload struct{}

type InteractionCreatePayload struct {
	Interaction report.Interaction `json:"interaction"`
}

type InteractionUpdatePayload struct {
	Interaction report.Interaction `json:"interaction"`
}

type InteractionDeletePayload struct{}

type InsightUpdatePayload struct {
	RichText   string     `json:"richText"`
	EvidenceID askdata.ID `json:"evidenceId"`
}

type InsightRegeneratePayload struct {
	EvidenceID            askdata.ID `json:"evidenceId"`
	AnalysisMethod        string     `json:"analysisMethod"`
	AnalysisMethodVersion string     `json:"analysisMethodVersion"`
}

func Decode(raw []byte, current *report.ReportDefinition) (Bundle, error) {
	if err := requireObjectFields(raw, "schemaVersion", "reportId", "baseRevision", "source", "aiRunId", "scope", "operations"); err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := askdata.DecodeStrictJSON(raw, &bundle); err != nil {
		return Bundle{}, err
	}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	if bundle.Source == SourceAI {
		if err := GuardAI(bundle, current); err != nil {
			return Bundle{}, err
		}
	}
	return bundle, nil
}

func (bundle Bundle) Validate() error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if err := bundle.ReportID.Validate(); err != nil {
		return fmt.Errorf("reportId: %w", err)
	}
	if bundle.BaseRevision < 0 {
		return errors.New("baseRevision must be non-negative")
	}
	if bundle.Source != SourceUser && bundle.Source != SourceAI && bundle.Source != SourceImport && bundle.Source != SourceSystem {
		return errors.New("source is invalid")
	}
	if bundle.Operations == nil || len(bundle.Operations) == 0 || len(bundle.Operations) > MaxOperations {
		return fmt.Errorf("operations count must be between 1 and %d", MaxOperations)
	}
	if bundle.Source == SourceAI {
		if bundle.AIRunID == nil {
			return errors.New("AI source requires aiRunId")
		}
		if bundle.Scope == nil {
			return errors.New("AI source requires scope")
		}
		if len(bundle.Operations) > MaxAIOperations {
			return fmt.Errorf("AI operations exceeds %d items", MaxAIOperations)
		}
	} else if bundle.AIRunID != nil {
		return errors.New("aiRunId is only valid for AI source")
	}
	if bundle.AIRunID != nil {
		if err := bundle.AIRunID.Validate(); err != nil {
			return fmt.Errorf("aiRunId: %w", err)
		}
	}
	if bundle.Scope != nil {
		if err := bundle.Scope.Validate(); err != nil {
			return fmt.Errorf("scope: %w", err)
		}
	}
	for index, operation := range bundle.Operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("operations[%d]: %w", index, err)
		}
	}
	return nil
}

func (scope Scope) Validate() error {
	if scope.PageID == nil {
		return errors.New("pageId is required")
	}
	if scope.BlockID != nil && scope.SectionID == nil {
		return errors.New("blockId requires sectionId")
	}
	for name, id := range map[string]*askdata.ID{
		"pageId": scope.PageID, "sectionId": scope.SectionID, "blockId": scope.BlockID,
	} {
		if id != nil {
			if err := id.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

func (operation Operation) Validate() error {
	if err := operation.TargetID.Validate(); err != nil {
		return fmt.Errorf("targetId: %w", err)
	}
	if operation.Payload == nil {
		return errors.New("payload is required")
	}
	expected, err := newPayload(operation.Op)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%T", expected) != fmt.Sprintf("%T", operation.Payload) {
		return fmt.Errorf("payload type does not match op %s", operation.Op)
	}
	return operation.Payload.Validate()
}

func (operation *Operation) UnmarshalJSON(raw []byte) error {
	if err := requireObjectFields(raw, "op", "targetId", "payload"); err != nil {
		return err
	}
	var envelope struct {
		Op       Type            `json:"op"`
		TargetID askdata.ID      `json:"targetId"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := askdata.DecodeStrictJSON(raw, &envelope); err != nil {
		return err
	}
	payload, err := newPayload(envelope.Op)
	if err != nil {
		return err
	}
	if len(envelope.Payload) == 0 || string(envelope.Payload) == "null" {
		return errors.New("payload must be an object")
	}
	if err := requireObjectFields(envelope.Payload, requiredPayloadFields(envelope.Op)...); err != nil {
		return fmt.Errorf("decode %s payload: %w", envelope.Op, err)
	}
	if err := askdata.DecodeStrictJSON(envelope.Payload, payload); err != nil {
		return fmt.Errorf("decode %s payload: %w", envelope.Op, err)
	}
	operation.Op = envelope.Op
	operation.TargetID = envelope.TargetID
	operation.Payload = payload
	return nil
}

func requireObjectFields(raw []byte, required ...string) error {
	var object map[string]json.RawMessage
	if err := askdata.DecodeStrictJSON(raw, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("JSON value must be an object")
	}
	for _, field := range required {
		if _, exists := object[field]; !exists {
			return fmt.Errorf("required field %q is missing", field)
		}
	}
	return nil
}

func requiredPayloadFields(operationType Type) []string {
	switch operationType {
	case ReportCreate:
		return []string{"definition"}
	case ReportSettingsUpdate:
		return []string{"metadata", "runtimePolicy"}
	case TemplateApply:
		return []string{"templateRef"}
	case ThemeUpdate:
		return []string{"themeRef"}
	case PageCreate:
		return []string{"page"}
	case PageUpdate:
		return []string{"name"}
	case PageDelete, SectionDelete, BlockDelete, ZoneDelete, SlotDelete, ComponentDelete, FilterDelete, InteractionDelete:
		return nil
	case PageReorder, SectionReorder, ZoneReorder:
		return []string{"order"}
	case SectionCreate:
		return []string{"section"}
	case SectionUpdate:
		return []string{"name"}
	case BlockCreate:
		return []string{"block"}
	case BlockMove:
		return []string{"x", "y"}
	case BlockResize:
		return []string{"w", "h"}
	case BlockUpdate:
		return []string{"type"}
	case BlockCopy, ComponentCopy:
		return []string{"newId"}
	case ZoneCreate:
		return []string{"zone"}
	case ZoneUpdate:
		return []string{"type", "layout"}
	case SlotCreate:
		return []string{"slot"}
	case SlotMerge:
		return []string{"slotIds", "newSlot"}
	case SlotSplit:
		return []string{"slots"}
	case SlotUpdate:
		return []string{"grid", "componentId"}
	case ComponentCreate, ComponentReplace:
		return []string{"component"}
	case ComponentUpdate:
		return []string{"options"}
	case DataBindingUpdate:
		return []string{"mode", "dataBinding"}
	case FilterCreate, FilterUpdate:
		return []string{"filter"}
	case InteractionCreate, InteractionUpdate:
		return []string{"interaction"}
	case InsightUpdate:
		return []string{"richText", "evidenceId"}
	case InsightRegenerate:
		return []string{"evidenceId", "analysisMethod", "analysisMethodVersion"}
	default:
		return nil
	}
}

func (operation Operation) MarshalJSON() ([]byte, error) {
	if err := operation.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Op       Type       `json:"op"`
		TargetID askdata.ID `json:"targetId"`
		Payload  Payload    `json:"payload"`
	}{operation.Op, operation.TargetID, operation.Payload})
}

func newPayload(operationType Type) (Payload, error) {
	switch operationType {
	case ReportCreate:
		return &ReportCreatePayload{}, nil
	case ReportSettingsUpdate:
		return &ReportSettingsUpdatePayload{}, nil
	case TemplateApply:
		return &TemplateApplyPayload{}, nil
	case ThemeUpdate:
		return &ThemeUpdatePayload{}, nil
	case PageCreate:
		return &PageCreatePayload{}, nil
	case PageUpdate:
		return &PageUpdatePayload{}, nil
	case PageDelete:
		return &PageDeletePayload{}, nil
	case PageReorder:
		return &PageReorderPayload{}, nil
	case SectionCreate:
		return &SectionCreatePayload{}, nil
	case SectionUpdate:
		return &SectionUpdatePayload{}, nil
	case SectionDelete:
		return &SectionDeletePayload{}, nil
	case SectionReorder:
		return &SectionReorderPayload{}, nil
	case BlockCreate:
		return &BlockCreatePayload{}, nil
	case BlockMove:
		return &BlockMovePayload{}, nil
	case BlockResize:
		return &BlockResizePayload{}, nil
	case BlockUpdate:
		return &BlockUpdatePayload{}, nil
	case BlockCopy:
		return &BlockCopyPayload{}, nil
	case BlockDelete:
		return &BlockDeletePayload{}, nil
	case ZoneCreate:
		return &ZoneCreatePayload{}, nil
	case ZoneUpdate:
		return &ZoneUpdatePayload{}, nil
	case ZoneDelete:
		return &ZoneDeletePayload{}, nil
	case ZoneReorder:
		return &ZoneReorderPayload{}, nil
	case SlotCreate:
		return &SlotCreatePayload{}, nil
	case SlotMerge:
		return &SlotMergePayload{}, nil
	case SlotSplit:
		return &SlotSplitPayload{}, nil
	case SlotUpdate:
		return &SlotUpdatePayload{}, nil
	case SlotDelete:
		return &SlotDeletePayload{}, nil
	case ComponentCreate:
		return &ComponentCreatePayload{}, nil
	case ComponentUpdate:
		return &ComponentUpdatePayload{}, nil
	case ComponentReplace:
		return &ComponentReplacePayload{}, nil
	case ComponentCopy:
		return &ComponentCopyPayload{}, nil
	case ComponentDelete:
		return &ComponentDeletePayload{}, nil
	case DataBindingUpdate:
		return &DataBindingUpdatePayload{}, nil
	case FilterCreate:
		return &FilterCreatePayload{}, nil
	case FilterUpdate:
		return &FilterUpdatePayload{}, nil
	case FilterDelete:
		return &FilterDeletePayload{}, nil
	case InteractionCreate:
		return &InteractionCreatePayload{}, nil
	case InteractionUpdate:
		return &InteractionUpdatePayload{}, nil
	case InteractionDelete:
		return &InteractionDeletePayload{}, nil
	case InsightUpdate:
		return &InsightUpdatePayload{}, nil
	case InsightRegenerate:
		return &InsightRegeneratePayload{}, nil
	default:
		return nil, fmt.Errorf("unsupported operation type %q", operationType)
	}
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" || len([]rune(name)) > 4096 {
		return errors.New("name must contain 1..4096 characters")
	}
	return nil
}

func validateOrder(order int) error {
	if order < 1 {
		return errors.New("order must be positive")
	}
	return nil
}

func validateNewID(id askdata.ID) error {
	if err := id.Validate(); err != nil {
		return fmt.Errorf("newId: %w", err)
	}
	return nil
}

func (*ReportCreatePayload) operationPayload()         {}
func (*ReportSettingsUpdatePayload) operationPayload() {}
func (*TemplateApplyPayload) operationPayload()        {}
func (*ThemeUpdatePayload) operationPayload()          {}
func (*PageCreatePayload) operationPayload()           {}
func (*PageUpdatePayload) operationPayload()           {}
func (*PageDeletePayload) operationPayload()           {}
func (*PageReorderPayload) operationPayload()          {}
func (*SectionCreatePayload) operationPayload()        {}
func (*SectionUpdatePayload) operationPayload()        {}
func (*SectionDeletePayload) operationPayload()        {}
func (*SectionReorderPayload) operationPayload()       {}
func (*BlockCreatePayload) operationPayload()          {}
func (*BlockMovePayload) operationPayload()            {}
func (*BlockResizePayload) operationPayload()          {}
func (*BlockUpdatePayload) operationPayload()          {}
func (*BlockCopyPayload) operationPayload()            {}
func (*BlockDeletePayload) operationPayload()          {}
func (*ZoneCreatePayload) operationPayload()           {}
func (*ZoneUpdatePayload) operationPayload()           {}
func (*ZoneDeletePayload) operationPayload()           {}
func (*ZoneReorderPayload) operationPayload()          {}
func (*SlotCreatePayload) operationPayload()           {}
func (*SlotMergePayload) operationPayload()            {}
func (*SlotSplitPayload) operationPayload()            {}
func (*SlotUpdatePayload) operationPayload()           {}
func (*SlotDeletePayload) operationPayload()           {}
func (*ComponentCreatePayload) operationPayload()      {}
func (*ComponentUpdatePayload) operationPayload()      {}
func (*ComponentReplacePayload) operationPayload()     {}
func (*ComponentCopyPayload) operationPayload()        {}
func (*ComponentDeletePayload) operationPayload()      {}
func (*DataBindingUpdatePayload) operationPayload()    {}
func (*FilterCreatePayload) operationPayload()         {}
func (*FilterUpdatePayload) operationPayload()         {}
func (*FilterDeletePayload) operationPayload()         {}
func (*InteractionCreatePayload) operationPayload()    {}
func (*InteractionUpdatePayload) operationPayload()    {}
func (*InteractionDeletePayload) operationPayload()    {}
func (*InsightUpdatePayload) operationPayload()        {}
func (*InsightRegeneratePayload) operationPayload()    {}

func (payload *ReportCreatePayload) Validate() error   { return payload.Definition.Validate() }
func (*ReportSettingsUpdatePayload) Validate() error   { return nil }
func (*TemplateApplyPayload) Validate() error          { return nil }
func (*ThemeUpdatePayload) Validate() error            { return nil }
func (payload *PageCreatePayload) Validate() error     { return payload.Page.ID.Validate() }
func (payload *PageUpdatePayload) Validate() error     { return validateName(payload.Name) }
func (*PageDeletePayload) Validate() error             { return nil }
func (payload *PageReorderPayload) Validate() error    { return validateOrder(payload.Order) }
func (payload *SectionCreatePayload) Validate() error  { return payload.Section.ID.Validate() }
func (payload *SectionUpdatePayload) Validate() error  { return validateName(payload.Name) }
func (*SectionDeletePayload) Validate() error          { return nil }
func (payload *SectionReorderPayload) Validate() error { return validateOrder(payload.Order) }
func (payload *BlockCreatePayload) Validate() error    { return payload.Block.ID.Validate() }
func (payload *BlockMovePayload) Validate() error {
	if payload.X < 0 || payload.Y < 0 {
		return errors.New("x/y must be non-negative")
	}
	return nil
}
func (payload *BlockResizePayload) Validate() error {
	if payload.W < 1 || payload.H < 1 || payload.W > 24 {
		return errors.New("w/h must be positive and w must not exceed 24")
	}
	return nil
}
func (payload *BlockUpdatePayload) Validate() error {
	switch payload.Type {
	case report.BlockAnalysisCard, report.BlockKPIGroup, report.BlockChart, report.BlockTable, report.BlockContent, report.BlockFilter:
		return nil
	default:
		return errors.New("type is invalid")
	}
}
func (payload *BlockCopyPayload) Validate() error  { return validateNewID(payload.NewID) }
func (*BlockDeletePayload) Validate() error        { return nil }
func (payload *ZoneCreatePayload) Validate() error { return payload.Zone.ID.Validate() }
func (payload *ZoneUpdatePayload) Validate() error {
	switch payload.Type {
	case report.ZoneHeader, report.ZoneFilter, report.ZoneInsight, report.ZoneContent, report.ZoneFooter:
		return nil
	default:
		return errors.New("type is invalid")
	}
}
func (*ZoneDeletePayload) Validate() error          { return nil }
func (payload *ZoneReorderPayload) Validate() error { return validateOrder(payload.Order) }
func (payload *SlotCreatePayload) Validate() error  { return payload.Slot.ID.Validate() }
func (payload *SlotMergePayload) Validate() error {
	if len(payload.SlotIDs) < 2 || len(payload.SlotIDs) > report.MaxSlotsPerBlock {
		return errors.New("slotIds count must be between 2 and 16")
	}
	seen := map[askdata.ID]struct{}{}
	for index, id := range payload.SlotIDs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("slotIds[%d]: %w", index, err)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("slotIds[%d] is duplicated", index)
		}
		seen[id] = struct{}{}
	}
	return payload.NewSlot.ID.Validate()
}
func (payload *SlotSplitPayload) Validate() error {
	if len(payload.Slots) < 2 || len(payload.Slots) > report.MaxSlotsPerBlock {
		return errors.New("slots count must be between 2 and 16")
	}
	seen := map[askdata.ID]struct{}{}
	for index, slot := range payload.Slots {
		if err := slot.ID.Validate(); err != nil {
			return fmt.Errorf("slots[%d].id: %w", index, err)
		}
		if _, exists := seen[slot.ID]; exists {
			return fmt.Errorf("slots[%d].id is duplicated", index)
		}
		seen[slot.ID] = struct{}{}
	}
	return nil
}
func (payload *SlotUpdatePayload) Validate() error       { return payload.ComponentID.Validate() }
func (*SlotDeletePayload) Validate() error               { return nil }
func (payload *ComponentCreatePayload) Validate() error  { return payload.Component.ID.Validate() }
func (*ComponentUpdatePayload) Validate() error          { return nil }
func (payload *ComponentReplacePayload) Validate() error { return payload.Component.ID.Validate() }
func (payload *ComponentCopyPayload) Validate() error    { return validateNewID(payload.NewID) }
func (*ComponentDeletePayload) Validate() error          { return nil }
func (payload *DataBindingUpdatePayload) Validate() error {
	switch payload.Mode {
	case DataBindingSet:
		if payload.DataBinding == nil {
			return errors.New("SET requires dataBinding")
		}
	case DataBindingClear:
		if payload.DataBinding != nil {
			return errors.New("CLEAR requires dataBinding=null")
		}
	default:
		return errors.New("mode is invalid")
	}
	return nil
}
func (payload *FilterCreatePayload) Validate() error      { return payload.Filter.ID.Validate() }
func (payload *FilterUpdatePayload) Validate() error      { return payload.Filter.ID.Validate() }
func (*FilterDeletePayload) Validate() error              { return nil }
func (payload *InteractionCreatePayload) Validate() error { return payload.Interaction.ID.Validate() }
func (payload *InteractionUpdatePayload) Validate() error { return payload.Interaction.ID.Validate() }
func (*InteractionDeletePayload) Validate() error         { return nil }
func (payload *InsightUpdatePayload) Validate() error {
	if strings.TrimSpace(payload.RichText) == "" || len(payload.RichText) > report.MaxRichTextBytes {
		return errors.New("richText must contain 1..65536 bytes")
	}
	return payload.EvidenceID.Validate()
}
func (payload *InsightRegeneratePayload) Validate() error {
	if err := payload.EvidenceID.Validate(); err != nil {
		return fmt.Errorf("evidenceId: %w", err)
	}
	if strings.TrimSpace(payload.AnalysisMethod) == "" || strings.TrimSpace(payload.AnalysisMethodVersion) == "" {
		return errors.New("analysisMethod and analysisMethodVersion are required")
	}
	return nil
}
