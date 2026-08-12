package operation

import (
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

// Invert returns the operation that restores beforeDefinition after operation
// has been applied. Delete inverses carry the complete deleted snapshot.
func Invert(operation Operation, beforeDefinition report.ReportDefinition) (Operation, error) {
	if err := operation.Validate(); err != nil {
		return Operation{}, err
	}
	before, err := cloneDefinition(beforeDefinition)
	if err != nil {
		return Operation{}, err
	}
	result := Operation{}
	switch operation.Op {
	case ReportCreate:
		result = Operation{Op: ReportCreate, TargetID: before.Metadata.ID, Payload: &ReportCreatePayload{Definition: before}}
	case ReportSettingsUpdate:
		result = Operation{Op: ReportSettingsUpdate, TargetID: before.Metadata.ID, Payload: &ReportSettingsUpdatePayload{Metadata: before.Metadata, RuntimePolicy: before.RuntimePolicy}}
	case TemplateApply:
		result = Operation{Op: ReportCreate, TargetID: before.Metadata.ID, Payload: &ReportCreatePayload{Definition: before}}
	case ThemeUpdate:
		result = Operation{Op: ReportCreate, TargetID: before.Metadata.ID, Payload: &ReportCreatePayload{Definition: before}}
	case PageCreate:
		result = Operation{Op: PageDelete, TargetID: operation.Payload.(*PageCreatePayload).Page.ID, Payload: &PageDeletePayload{}}
	case PageUpdate:
		page, findErr := pageByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: PageUpdate, TargetID: page.ID, Payload: &PageUpdatePayload{Name: page.Name}}
	case PageDelete:
		page, findErr := pageByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: PageCreate, TargetID: before.Metadata.ID, Payload: &PageCreatePayload{Page: *page}}
	case PageReorder:
		page, findErr := pageByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: PageReorder, TargetID: page.ID, Payload: &PageReorderPayload{Order: page.Order}}
	case SectionCreate:
		result = Operation{Op: SectionDelete, TargetID: operation.Payload.(*SectionCreatePayload).Section.ID, Payload: &SectionDeletePayload{}}
	case SectionUpdate:
		_, section, findErr := sectionByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SectionUpdate, TargetID: section.ID, Payload: &SectionUpdatePayload{Name: section.Name}}
	case SectionDelete:
		page, section, findErr := sectionByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SectionCreate, TargetID: page.ID, Payload: &SectionCreatePayload{Section: *section}}
	case SectionReorder:
		_, section, findErr := sectionByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SectionReorder, TargetID: section.ID, Payload: &SectionReorderPayload{Order: section.Order}}
	case BlockCreate:
		result = Operation{Op: BlockDelete, TargetID: operation.Payload.(*BlockCreatePayload).Block.ID, Payload: &BlockDeletePayload{}}
	case BlockMove:
		_, _, block, findErr := blockByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: BlockMove, TargetID: block.ID, Payload: &BlockMovePayload{X: block.Layout.Desktop.X, Y: block.Layout.Desktop.Y}}
	case BlockResize:
		_, _, block, findErr := blockByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: BlockResize, TargetID: block.ID, Payload: &BlockResizePayload{W: block.Layout.Desktop.W, H: block.Layout.Desktop.H}}
	case BlockUpdate:
		_, _, block, findErr := blockByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: BlockUpdate, TargetID: block.ID, Payload: &BlockUpdatePayload{Type: block.Type}}
	case BlockCopy:
		result = Operation{Op: BlockDelete, TargetID: operation.Payload.(*BlockCopyPayload).NewID, Payload: &BlockDeletePayload{}}
	case BlockDelete:
		_, section, block, findErr := blockByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: BlockCreate, TargetID: section.ID, Payload: &BlockCreatePayload{Block: *block}}
	case ZoneCreate:
		result = Operation{Op: ZoneDelete, TargetID: operation.Payload.(*ZoneCreatePayload).Zone.ID, Payload: &ZoneDeletePayload{}}
	case ZoneUpdate:
		_, _, _, zone, findErr := zoneByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ZoneUpdate, TargetID: zone.ID, Payload: &ZoneUpdatePayload{Type: zone.Type, Layout: zone.Layout}}
	case ZoneDelete:
		_, _, block, zone, findErr := zoneByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ZoneCreate, TargetID: block.ID, Payload: &ZoneCreatePayload{Zone: *zone}}
	case ZoneReorder:
		_, _, _, zone, findErr := zoneByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ZoneReorder, TargetID: zone.ID, Payload: &ZoneReorderPayload{Order: zone.Order}}
	case SlotCreate:
		result = Operation{Op: SlotDelete, TargetID: operation.Payload.(*SlotCreatePayload).Slot.ID, Payload: &SlotDeletePayload{}}
	case SlotMerge:
		payload := operation.Payload.(*SlotMergePayload)
		originals, findErr := slotsByID(&before, payload.SlotIDs)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SlotSplit, TargetID: payload.NewSlot.ID, Payload: &SlotSplitPayload{Slots: originals}}
	case SlotSplit:
		_, _, _, _, _, original, findErr := slotValueByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		payload := operation.Payload.(*SlotSplitPayload)
		ids := make([]askdata.ID, len(payload.Slots))
		for index, slot := range payload.Slots {
			ids[index] = slot.ID
		}
		newSlot := *original
		result = Operation{Op: SlotMerge, TargetID: ids[0], Payload: &SlotMergePayload{
			SlotIDs: ids, NewSlot: newSlot, restoreMergedFrom: true,
		}}
	case SlotUpdate:
		_, _, _, _, _, slot, findErr := slotValueByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SlotUpdate, TargetID: slot.ID, Payload: &SlotUpdatePayload{Grid: slot.Grid, ComponentID: slot.ComponentID}}
	case SlotDelete:
		_, _, _, zone, _, slot, findErr := slotValueByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: SlotCreate, TargetID: zone.ID, Payload: &SlotCreatePayload{Slot: *slot}}
	case ComponentCreate:
		result = Operation{Op: ComponentDelete, TargetID: operation.Payload.(*ComponentCreatePayload).Component.ID, Payload: &ComponentDeletePayload{}}
	case ComponentUpdate:
		component, findErr := componentByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ComponentUpdate, TargetID: component.ID, Payload: &ComponentUpdatePayload{Options: component.Options}}
	case ComponentReplace:
		component, findErr := componentByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		replacement := operation.Payload.(*ComponentReplacePayload).Component
		result = Operation{Op: ComponentReplace, TargetID: replacement.ID, Payload: &ComponentReplacePayload{Component: *component}}
	case ComponentCopy:
		result = Operation{Op: ComponentDelete, TargetID: operation.Payload.(*ComponentCopyPayload).NewID, Payload: &ComponentDeletePayload{}}
	case ComponentDelete:
		component, findErr := componentByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ComponentCreate, TargetID: before.Metadata.ID, Payload: &ComponentCreatePayload{Component: *component}}
	case DataBindingUpdate:
		component, findErr := componentByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		mode := DataBindingClear
		if component.DataBinding != nil {
			mode = DataBindingSet
		}
		result = Operation{Op: DataBindingUpdate, TargetID: component.ID, Payload: &DataBindingUpdatePayload{Mode: mode, DataBinding: component.DataBinding}}
	case FilterCreate:
		result = Operation{Op: FilterDelete, TargetID: operation.Payload.(*FilterCreatePayload).Filter.ID, Payload: &FilterDeletePayload{}}
	case FilterUpdate:
		index := filterIndex(before, operation.TargetID)
		if index < 0 {
			return Operation{}, missing("filter", operation.TargetID)
		}
		result = Operation{Op: FilterUpdate, TargetID: operation.TargetID, Payload: &FilterUpdatePayload{Filter: before.GlobalFilters[index]}}
	case FilterDelete:
		index := filterIndex(before, operation.TargetID)
		if index < 0 {
			return Operation{}, missing("filter", operation.TargetID)
		}
		result = Operation{Op: FilterCreate, TargetID: before.Metadata.ID, Payload: &FilterCreatePayload{Filter: before.GlobalFilters[index]}}
	case InteractionCreate:
		result = Operation{Op: InteractionDelete, TargetID: operation.Payload.(*InteractionCreatePayload).Interaction.ID, Payload: &InteractionDeletePayload{}}
	case InteractionUpdate:
		index := interactionIndex(before, operation.TargetID)
		if index < 0 {
			return Operation{}, missing("interaction", operation.TargetID)
		}
		result = Operation{Op: InteractionUpdate, TargetID: operation.TargetID, Payload: &InteractionUpdatePayload{Interaction: before.Interactions[index]}}
	case InteractionDelete:
		index := interactionIndex(before, operation.TargetID)
		if index < 0 {
			return Operation{}, missing("interaction", operation.TargetID)
		}
		result = Operation{Op: InteractionCreate, TargetID: before.Metadata.ID, Payload: &InteractionCreatePayload{Interaction: before.Interactions[index]}}
	case InsightUpdate:
		component, findErr := componentByID(&before, operation.TargetID)
		if findErr != nil {
			return Operation{}, findErr
		}
		result = Operation{Op: ComponentUpdate, TargetID: component.ID, Payload: &ComponentUpdatePayload{Options: component.Options}}
	case InsightRegenerate:
		payload := *operation.Payload.(*InsightRegeneratePayload)
		result = Operation{Op: InsightRegenerate, TargetID: operation.TargetID, Payload: &payload}
	default:
		return Operation{}, fmt.Errorf("unsupported operation type %q", operation.Op)
	}
	if err := result.Validate(); err != nil {
		return Operation{}, fmt.Errorf("inverse of %s is invalid: %w", operation.Op, err)
	}
	return result, nil
}

// InvertBundle derives inverse operations in reverse application order.
func InvertBundle(operations []Operation, before report.ReportDefinition) ([]Operation, error) {
	states := make([]report.ReportDefinition, len(operations)+1)
	states[0] = before
	for index, operation := range operations {
		next, err := Apply(states[index], []Operation{operation})
		if err != nil {
			return nil, err
		}
		states[index+1] = next
	}
	result := make([]Operation, 0, len(operations))
	for index := len(operations) - 1; index >= 0; index-- {
		inverse, err := Invert(operations[index], states[index])
		if err != nil {
			return nil, fmt.Errorf("invert operation %d: %w", index, err)
		}
		result = append(result, inverse)
	}
	return result, nil
}

func slotsByID(definition *report.ReportDefinition, ids []askdata.ID) ([]report.Slot, error) {
	result := make([]report.Slot, 0, len(ids))
	for _, id := range ids {
		_, _, _, _, _, slot, err := slotValueByID(definition, id)
		if err != nil {
			return nil, err
		}
		result = append(result, *slot)
	}
	return result, nil
}
