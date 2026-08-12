package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tiendc/go-deepcopy"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/template"
)

type ApplyError struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (err *ApplyError) Error() string {
	return fmt.Sprintf("operation %d: %s: %s", err.Index, err.Code, err.Message)
}

// Apply is atomic and side-effect free: every operation is applied to a deep
// copy, and a failure returns the original value unchanged.
func Apply(definition report.ReportDefinition, operations []Operation) (report.ReportDefinition, error) {
	working, err := cloneDefinition(definition)
	if err != nil {
		return definition, err
	}
	for index, operation := range operations {
		if err := operation.Validate(); err != nil {
			return definition, &ApplyError{Index: index, Code: "REPORT_OPERATION_INVALID", Message: err.Error()}
		}
		if err := applyOne(&working, operation); err != nil {
			code := "REPORT_OPERATION_APPLY_FAILED"
			var mergeError *compiler.SlotMergeError
			if errors.As(err, &mergeError) {
				code = mergeError.Code
			}
			return definition, &ApplyError{Index: index, Code: code, Message: err.Error()}
		}
	}
	return working, nil
}

// ApplyAndValidate is the standard service boundary. It additionally performs
// all compiler validation and canonicalization stages.
func ApplyAndValidate(definition report.ReportDefinition, operations []Operation) (report.ReportDefinition, []byte, string, error) {
	updated, err := Apply(definition, operations)
	if err != nil {
		return definition, nil, "", err
	}
	canonical, hash, err := compiler.Normalize(updated)
	if err != nil {
		return definition, nil, "", err
	}
	var normalized report.ReportDefinition
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return definition, nil, "", fmt.Errorf("decode normalized definition: %w", err)
	}
	return normalized, canonical, hash, nil
}

func cloneDefinition(definition report.ReportDefinition) (report.ReportDefinition, error) {
	var clone report.ReportDefinition
	if err := deepcopy.Copy(&clone, &definition); err != nil {
		return report.ReportDefinition{}, fmt.Errorf("clone report definition: %w", err)
	}
	return clone, nil
}

func applyOne(definition *report.ReportDefinition, operation Operation) error {
	switch operation.Op {
	case ReportCreate:
		*definition = operation.Payload.(*ReportCreatePayload).Definition
	case ReportSettingsUpdate:
		payload := operation.Payload.(*ReportSettingsUpdatePayload)
		definition.Metadata = payload.Metadata
		definition.RuntimePolicy = payload.RuntimePolicy
	case TemplateApply:
		definition.TemplateRef = operation.Payload.(*TemplateApplyPayload).TemplateRef
	case ThemeUpdate:
		definition.ThemeRef = operation.Payload.(*ThemeUpdatePayload).ThemeRef
	case PageCreate:
		definition.Pages = append(definition.Pages, operation.Payload.(*PageCreatePayload).Page)
	case PageUpdate:
		page, err := pageByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		page.Name = operation.Payload.(*PageUpdatePayload).Name
	case PageDelete:
		index := pageIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("page", operation.TargetID)
		}
		definition.Pages = append(definition.Pages[:index], definition.Pages[index+1:]...)
	case PageReorder:
		page, err := pageByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		page.Order = operation.Payload.(*PageReorderPayload).Order
	case SectionCreate:
		page, err := pageByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		page.Sections = append(page.Sections, operation.Payload.(*SectionCreatePayload).Section)
	case SectionUpdate:
		_, section, err := sectionByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		section.Name = operation.Payload.(*SectionUpdatePayload).Name
	case SectionDelete:
		page, index, err := sectionLocation(definition, operation.TargetID)
		if err != nil {
			return err
		}
		page.Sections = append(page.Sections[:index], page.Sections[index+1:]...)
	case SectionReorder:
		_, section, err := sectionByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		section.Order = operation.Payload.(*SectionReorderPayload).Order
	case BlockCreate:
		_, section, err := sectionByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		section.Blocks = append(section.Blocks, operation.Payload.(*BlockCreatePayload).Block)
	case BlockMove:
		_, _, block, err := blockByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*BlockMovePayload)
		block.Layout.Desktop.X, block.Layout.Desktop.Y = payload.X, payload.Y
	case BlockResize:
		_, _, block, err := blockByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*BlockResizePayload)
		block.Layout.Desktop.W, block.Layout.Desktop.H = payload.W, payload.H
	case BlockUpdate:
		_, _, block, err := blockByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		block.Type = operation.Payload.(*BlockUpdatePayload).Type
	case BlockCopy:
		_, section, block, err := blockByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		var copy report.Block
		if err := deepcopy.Copy(&copy, block); err != nil {
			return fmt.Errorf("clone block %q: %w", block.ID, err)
		}
		copy.ID = operation.Payload.(*BlockCopyPayload).NewID
		copyNestedBlockIDs(&copy)
		section.Blocks = append(section.Blocks, copy)
	case BlockDelete:
		section, index, err := blockLocation(definition, operation.TargetID)
		if err != nil {
			return err
		}
		section.Blocks = append(section.Blocks[:index], section.Blocks[index+1:]...)
	case ZoneCreate:
		_, _, block, err := blockByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		block.Zones = append(block.Zones, operation.Payload.(*ZoneCreatePayload).Zone)
	case ZoneUpdate:
		_, _, _, zone, err := zoneByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*ZoneUpdatePayload)
		zone.Type, zone.Layout = payload.Type, payload.Layout
	case ZoneDelete:
		block, index, err := zoneLocation(definition, operation.TargetID)
		if err != nil {
			return err
		}
		block.Zones = append(block.Zones[:index], block.Zones[index+1:]...)
	case ZoneReorder:
		_, _, _, zone, err := zoneByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		zone.Order = operation.Payload.(*ZoneReorderPayload).Order
	case SlotCreate:
		_, _, _, zone, err := zoneByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		zone.Slots = append(zone.Slots, operation.Payload.(*SlotCreatePayload).Slot)
	case SlotMerge:
		_, _, block, zone, _, err := slotByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*SlotMergePayload)
		if !slices.Contains(payload.SlotIDs, operation.TargetID) {
			return &compiler.SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "operation target must be one of slotIds"}
		}
		merged, err := compiler.MergeSlots(*zone, payload.SlotIDs, payload.NewSlot.ID, template.GridSize{W: 1, H: 1})
		if err != nil {
			return err
		}
		if merged.ComponentID != "" {
			component, err := componentByID(definition, merged.ComponentID)
			if err != nil {
				return err
			}
			registry, err := template.NewDefaultRegistry()
			if err != nil {
				return err
			}
			manifest, exists := registry.Get(component.TemplateRef.Type, component.TemplateRef.Version)
			if !exists {
				return fmt.Errorf("component manifest %s@%s is not registered", component.TemplateRef.Type, component.TemplateRef.Version)
			}
			size := compiler.SlotRenderSize(*block, *zone, merged.Grid)
			if size.W < manifest.MinSize.W || size.H < manifest.MinSize.H {
				return &compiler.SlotMergeError{
					Code:    "REPORT_SLOT_MERGE_BELOW_MIN_SIZE",
					Message: fmt.Sprintf("merged slot render size %dx%d is below component manifest minimum %dx%d", size.W, size.H, manifest.MinSize.W, manifest.MinSize.H),
				}
			}
		}
		zone.Slots = removeSlots(zone.Slots, payload.SlotIDs)
		if payload.restoreMergedFrom {
			merged.MergedFrom = append([]askdata.ID(nil), payload.NewSlot.MergedFrom...)
		}
		sort.Slice(merged.MergedFrom, func(left, right int) bool { return merged.MergedFrom[left] < merged.MergedFrom[right] })
		zone.Slots = append(zone.Slots, merged)
	case SlotSplit:
		_, _, _, zone, index, err := slotByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		current := zone.Slots[index]
		if len(current.MergedFrom) < 2 {
			return &compiler.SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "only a slot produced by SLOT_MERGE can be split"}
		}
		splitSlots := operation.Payload.(*SlotSplitPayload).Slots
		provided := make([]askdata.ID, len(splitSlots))
		for slotIndex, slot := range splitSlots {
			provided[slotIndex] = slot.ID
		}
		sort.Slice(provided, func(left, right int) bool { return provided[left] < provided[right] })
		expected := slices.Clone(current.MergedFrom)
		sort.Slice(expected, func(left, right int) bool { return expected[left] < expected[right] })
		if !slices.Equal(provided, expected) {
			return &compiler.SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "split slots do not match mergedFrom"}
		}
		rebuilt, err := compiler.MergeSlots(report.Zone{Slots: splitSlots}, provided, current.ID, template.GridSize{W: 1, H: 1})
		if err != nil {
			return err
		}
		if rebuilt.Grid != current.Grid || rebuilt.ComponentID != current.ComponentID {
			return &compiler.SlotMergeError{Code: "REPORT_SLOT_MERGE_NOT_RECTANGULAR", Message: "split slots do not restore the merged geometry and component"}
		}
		zone.Slots = append(zone.Slots[:index], zone.Slots[index+1:]...)
		zone.Slots = append(zone.Slots, splitSlots...)
	case SlotUpdate:
		_, _, _, _, _, slot, err := slotValueByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*SlotUpdatePayload)
		slot.Grid, slot.ComponentID = payload.Grid, payload.ComponentID
	case SlotDelete:
		_, _, _, zone, index, err := slotByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		zone.Slots = append(zone.Slots[:index], zone.Slots[index+1:]...)
	case ComponentCreate:
		definition.Components = append(definition.Components, operation.Payload.(*ComponentCreatePayload).Component)
	case ComponentUpdate:
		component, err := componentByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		component.Options = operation.Payload.(*ComponentUpdatePayload).Options
	case ComponentReplace:
		index := componentIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("component", operation.TargetID)
		}
		replacement := operation.Payload.(*ComponentReplacePayload).Component
		definition.Components[index] = replacement
		if replacement.ID != operation.TargetID {
			replaceSlotComponentID(definition, operation.TargetID, replacement.ID)
		}
	case ComponentCopy:
		component, err := componentByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		copy := *component
		copy.ID = operation.Payload.(*ComponentCopyPayload).NewID
		definition.Components = append(definition.Components, copy)
	case ComponentDelete:
		index := componentIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("component", operation.TargetID)
		}
		definition.Components = append(definition.Components[:index], definition.Components[index+1:]...)
	case DataBindingUpdate:
		component, err := componentByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		payload := operation.Payload.(*DataBindingUpdatePayload)
		component.DataBinding = payload.DataBinding
	case FilterCreate:
		definition.GlobalFilters = append(definition.GlobalFilters, operation.Payload.(*FilterCreatePayload).Filter)
	case FilterUpdate:
		index := filterIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("filter", operation.TargetID)
		}
		definition.GlobalFilters[index] = operation.Payload.(*FilterUpdatePayload).Filter
	case FilterDelete:
		index := filterIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("filter", operation.TargetID)
		}
		definition.GlobalFilters = append(definition.GlobalFilters[:index], definition.GlobalFilters[index+1:]...)
	case InteractionCreate:
		definition.Interactions = append(definition.Interactions, operation.Payload.(*InteractionCreatePayload).Interaction)
	case InteractionUpdate:
		index := interactionIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("interaction", operation.TargetID)
		}
		definition.Interactions[index] = operation.Payload.(*InteractionUpdatePayload).Interaction
	case InteractionDelete:
		index := interactionIndex(*definition, operation.TargetID)
		if index < 0 {
			return missing("interaction", operation.TargetID)
		}
		definition.Interactions = append(definition.Interactions[:index], definition.Interactions[index+1:]...)
	case InsightUpdate:
		component, err := componentByID(definition, operation.TargetID)
		if err != nil {
			return err
		}
		component.Options.RichText = operation.Payload.(*InsightUpdatePayload).RichText
	case InsightRegenerate:
		if _, err := componentByID(definition, operation.TargetID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported operation type %q", operation.Op)
	}
	sortDefinition(definition)
	return nil
}

func copyNestedBlockIDs(block *report.Block) {
	for zoneIndex := range block.Zones {
		zone := &block.Zones[zoneIndex]
		zone.ID = derivedID(block.ID, zone.ID)
		for slotIndex := range zone.Slots {
			zone.Slots[slotIndex].ID = derivedID(block.ID, zone.Slots[slotIndex].ID)
		}
	}
}

func derivedID(prefix, original askdata.ID) askdata.ID {
	value := string(prefix) + "/" + string(original)
	if len(value) <= askdata.MaxIDLength {
		return askdata.ID(value)
	}
	return askdata.ID(string(prefix) + "/" + string(askdata.HashBytes([]byte(original)))[:16])
}

func removeSlots(slots []report.Slot, ids []askdata.ID) []report.Slot {
	wanted := map[askdata.ID]struct{}{}
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := slots[:0]
	for _, slot := range slots {
		if _, remove := wanted[slot.ID]; !remove {
			result = append(result, slot)
		}
	}
	return result
}

func replaceSlotComponentID(definition *report.ReportDefinition, oldID, newID askdata.ID) {
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			for blockIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks {
				for zoneIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex].Zones {
					for slotIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex].Zones[zoneIndex].Slots {
						slot := &definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex].Zones[zoneIndex].Slots[slotIndex]
						if slot.ComponentID == oldID {
							slot.ComponentID = newID
						}
					}
				}
			}
		}
	}
}

func sortDefinition(definition *report.ReportDefinition) {
	sort.SliceStable(definition.Pages, func(i, j int) bool { return definition.Pages[i].Order < definition.Pages[j].Order })
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		sort.SliceStable(page.Sections, func(i, j int) bool { return page.Sections[i].Order < page.Sections[j].Order })
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			sort.SliceStable(section.Blocks, func(i, j int) bool {
				return section.Blocks[i].Layout.Mobile.Order < section.Blocks[j].Layout.Mobile.Order
			})
			for blockIndex := range section.Blocks {
				sort.SliceStable(section.Blocks[blockIndex].Zones, func(i, j int) bool {
					return section.Blocks[blockIndex].Zones[i].Layout.EmptyPriority < section.Blocks[blockIndex].Zones[j].Layout.EmptyPriority
				})
			}
		}
	}
}

func missing(kind string, id askdata.ID) error { return fmt.Errorf("%s %q does not exist", kind, id) }

func pageIndex(definition report.ReportDefinition, id askdata.ID) int {
	for index := range definition.Pages {
		if definition.Pages[index].ID == id {
			return index
		}
	}
	return -1
}

func pageByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, error) {
	index := pageIndex(*definition, id)
	if index < 0 {
		return nil, missing("page", id)
	}
	return &definition.Pages[index], nil
}

func sectionLocation(definition *report.ReportDefinition, id askdata.ID) (*report.Page, int, error) {
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			if definition.Pages[pageIndex].Sections[sectionIndex].ID == id {
				return &definition.Pages[pageIndex], sectionIndex, nil
			}
		}
	}
	return nil, -1, missing("section", id)
}

func sectionByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, *report.Section, error) {
	page, index, err := sectionLocation(definition, id)
	if err != nil {
		return nil, nil, err
	}
	return page, &page.Sections[index], nil
}

func blockLocation(definition *report.ReportDefinition, id askdata.ID) (*report.Section, int, error) {
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			section := &definition.Pages[pageIndex].Sections[sectionIndex]
			for blockIndex := range section.Blocks {
				if section.Blocks[blockIndex].ID == id {
					return section, blockIndex, nil
				}
			}
		}
	}
	return nil, -1, missing("block", id)
}

func blockByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, *report.Section, *report.Block, error) {
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			for blockIndex := range section.Blocks {
				if section.Blocks[blockIndex].ID == id {
					return page, section, &section.Blocks[blockIndex], nil
				}
			}
		}
	}
	return nil, nil, nil, missing("block", id)
}

func zoneLocation(definition *report.ReportDefinition, id askdata.ID) (*report.Block, int, error) {
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			for blockIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks {
				block := &definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex]
				for zoneIndex := range block.Zones {
					if block.Zones[zoneIndex].ID == id {
						return block, zoneIndex, nil
					}
				}
			}
		}
	}
	return nil, -1, missing("zone", id)
}

func zoneByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, *report.Section, *report.Block, *report.Zone, error) {
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			for blockIndex := range section.Blocks {
				block := &section.Blocks[blockIndex]
				for zoneIndex := range block.Zones {
					if block.Zones[zoneIndex].ID == id {
						return page, section, block, &block.Zones[zoneIndex], nil
					}
				}
			}
		}
	}
	return nil, nil, nil, nil, missing("zone", id)
}

func slotByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, *report.Section, *report.Block, *report.Zone, int, error) {
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		for sectionIndex := range page.Sections {
			section := &page.Sections[sectionIndex]
			for blockIndex := range section.Blocks {
				block := &section.Blocks[blockIndex]
				for zoneIndex := range block.Zones {
					zone := &block.Zones[zoneIndex]
					for slotIndex := range zone.Slots {
						if zone.Slots[slotIndex].ID == id {
							return page, section, block, zone, slotIndex, nil
						}
					}
				}
			}
		}
	}
	return nil, nil, nil, nil, -1, missing("slot", id)
}

func slotValueByID(definition *report.ReportDefinition, id askdata.ID) (*report.Page, *report.Section, *report.Block, *report.Zone, int, *report.Slot, error) {
	page, section, block, zone, index, err := slotByID(definition, id)
	if err != nil {
		return nil, nil, nil, nil, -1, nil, err
	}
	return page, section, block, zone, index, &zone.Slots[index], nil
}

func componentIndex(definition report.ReportDefinition, id askdata.ID) int {
	for index := range definition.Components {
		if definition.Components[index].ID == id {
			return index
		}
	}
	return -1
}

func componentByID(definition *report.ReportDefinition, id askdata.ID) (*report.Component, error) {
	index := componentIndex(*definition, id)
	if index < 0 {
		return nil, missing("component", id)
	}
	return &definition.Components[index], nil
}

func filterIndex(definition report.ReportDefinition, id askdata.ID) int {
	for index := range definition.GlobalFilters {
		if definition.GlobalFilters[index].ID == id {
			return index
		}
	}
	return -1
}

func interactionIndex(definition report.ReportDefinition, id askdata.ID) int {
	for index := range definition.Interactions {
		if definition.Interactions[index].ID == id {
			return index
		}
	}
	return -1
}

func parentID(definition *report.ReportDefinition, kind string, id askdata.ID) (askdata.ID, error) {
	switch strings.ToLower(kind) {
	case "section":
		page, _, err := sectionByID(definition, id)
		if err != nil {
			return "", err
		}
		return page.ID, nil
	case "block":
		_, section, _, err := blockByID(definition, id)
		if err != nil {
			return "", err
		}
		return section.ID, nil
	case "zone":
		_, _, block, _, err := zoneByID(definition, id)
		if err != nil {
			return "", err
		}
		return block.ID, nil
	case "slot":
		_, _, _, zone, _, err := slotByID(definition, id)
		if err != nil {
			return "", err
		}
		return zone.ID, nil
	}
	return "", fmt.Errorf("unsupported parent kind %q", kind)
}
