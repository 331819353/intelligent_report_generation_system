package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

func TestApplyAndInvertAll41Operations(t *testing.T) {
	cases := operationApplyCases(t)
	if len(cases) != len(Types()) || len(cases) != 41 {
		t.Fatalf("apply cases = %d, operation types = %d", len(cases), len(Types()))
	}
	seen := map[Type]struct{}{}
	for _, testCase := range cases {
		t.Run(string(testCase.operation.Op), func(t *testing.T) {
			if _, duplicate := seen[testCase.operation.Op]; duplicate {
				t.Fatalf("duplicate operation case %s", testCase.operation.Op)
			}
			seen[testCase.operation.Op] = struct{}{}
			beforeFingerprint := semanticFingerprint(t, testCase.before)
			updated, err := Apply(testCase.before, []Operation{testCase.operation})
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			inverse, err := Invert(testCase.operation, testCase.before)
			if err != nil {
				t.Fatalf("Invert() error = %v", err)
			}
			restored, err := Apply(updated, []Operation{inverse})
			if err != nil {
				t.Fatalf("Apply(inverse) error = %v", err)
			}
			if restoredFingerprint := semanticFingerprint(t, restored); restoredFingerprint != beforeFingerprint {
				t.Fatalf("apply + inverse changed definition: %s != %s", restoredFingerprint, beforeFingerprint)
			}
			if currentFingerprint := semanticFingerprint(t, testCase.before); currentFingerprint != beforeFingerprint {
				t.Fatal("Apply or Invert mutated the caller definition")
			}
		})
	}
}

func TestApplyBundleIsAtomicAndReportsFailureIndex(t *testing.T) {
	definition := operationDefinition(t)
	beforeHash := semanticFingerprint(t, definition)
	_, err := Apply(definition, []Operation{
		{Op: PageUpdate, TargetID: definition.Pages[0].ID, Payload: &PageUpdatePayload{Name: "temporary"}},
		{Op: PageUpdate, TargetID: "page_missing", Payload: &PageUpdatePayload{Name: "must fail"}},
	})
	var applyError *ApplyError
	if !errors.As(err, &applyError) || applyError.Index != 1 || applyError.Code != "REPORT_OPERATION_APPLY_FAILED" {
		t.Fatalf("Apply() error = %#v / %v", applyError, err)
	}
	if semanticFingerprint(t, definition) != beforeHash {
		t.Fatal("failed operation bundle mutated the input")
	}
}

func TestRandomOperationSequenceUndoRestoresInitialHash(t *testing.T) {
	definition := operationDefinition(t)
	initialCanonical, initialHash, err := compiler.Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	_ = initialCanonical
	random := rand.New(rand.NewSource(20260810))
	operations := make([]Operation, 0, 120)
	for index := 0; index < 120; index++ {
		switch random.Intn(4) {
		case 0:
			operations = append(operations, Operation{Op: PageUpdate, TargetID: definition.Pages[0].ID,
				Payload: &PageUpdatePayload{Name: fmt.Sprintf("page-%03d", index)}})
		case 1:
			operations = append(operations, Operation{Op: SectionUpdate, TargetID: definition.Pages[0].Sections[0].ID,
				Payload: &SectionUpdatePayload{Name: fmt.Sprintf("section-%03d", index)}})
		case 2:
			component := definition.Components[0]
			component.Options.Title = fmt.Sprintf("component-%03d", index)
			operations = append(operations, Operation{Op: ComponentUpdate, TargetID: component.ID,
				Payload: &ComponentUpdatePayload{Options: component.Options}})
		case 3:
			settings := definition.Metadata
			settings.Name = fmt.Sprintf("report-%03d", index)
			operations = append(operations, Operation{Op: ReportSettingsUpdate, TargetID: definition.Metadata.ID,
				Payload: &ReportSettingsUpdatePayload{Metadata: settings, RuntimePolicy: definition.RuntimePolicy}})
		}
	}
	updated, err := Apply(definition, operations)
	if err != nil {
		t.Fatal(err)
	}
	inverse, err := InvertBundle(operations, definition)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, restoredHash, err := ApplyAndValidate(updated, inverse)
	if err != nil {
		t.Fatal(err)
	}
	if restoredHash != initialHash || restored.Metadata.Name != definition.Metadata.Name {
		t.Fatalf("restored hash/name = %s/%q, want %s/%q", restoredHash, restored.Metadata.Name, initialHash, definition.Metadata.Name)
	}
}

func TestTemplateAndThemeInverseUseWholeBeforeSnapshot(t *testing.T) {
	definition := operationDefinition(t)
	for _, item := range []Operation{
		{Op: TemplateApply, TargetID: definition.Metadata.ID, Payload: &TemplateApplyPayload{TemplateRef: definition.TemplateRef}},
		{Op: ThemeUpdate, TargetID: definition.Metadata.ID, Payload: &ThemeUpdatePayload{ThemeRef: definition.ThemeRef}},
	} {
		inverse, err := Invert(item, definition)
		if err != nil {
			t.Fatal(err)
		}
		payload, ok := inverse.Payload.(*ReportCreatePayload)
		if inverse.Op != ReportCreate || !ok || semanticFingerprint(t, payload.Definition) != semanticFingerprint(t, definition) {
			t.Fatalf("snapshot inverse = %#v", inverse)
		}
	}
}

func TestSlotMergeUndoRestoresOriginalSlotIDs(t *testing.T) {
	definition, operation := slotMergeCase(t)
	zone := definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	want := []askdata.ID{zone.Slots[0].ID, zone.Slots[1].ID}
	provenanceWant := slices.Clone(want)
	slices.Sort(provenanceWant)
	updated, err := Apply(definition, []Operation{operation})
	if err != nil {
		t.Fatal(err)
	}
	mergedZone := updated.Pages[0].Sections[0].Blocks[0].Zones[0]
	if len(mergedZone.Slots) != 1 || fmt.Sprint(mergedZone.Slots[0].MergedFrom) != fmt.Sprint(provenanceWant) ||
		mergedZone.Slots[0].Grid != (report.SlotGrid{X: 0, Y: 0, W: 2, H: 1}) || mergedZone.Slots[0].ComponentID == "" {
		t.Fatalf("merged slot did not derive geometry/component/provenance: %#v", mergedZone.Slots)
	}
	inverse, err := Invert(operation, definition)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Apply(updated, []Operation{inverse})
	if err != nil {
		t.Fatal(err)
	}
	gotZone := restored.Pages[0].Sections[0].Blocks[0].Zones[0]
	got := []askdata.ID{gotZone.Slots[0].ID, gotZone.Slots[1].ID}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("restored slot IDs = %v, want %v", got, want)
	}
}

func TestSlotMergeReturnsStableLayoutErrorCode(t *testing.T) {
	definition, operation := slotMergeCase(t)
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Slots[1].ComponentID = zone.Slots[0].ComponentID
	_, err := Apply(definition, []Operation{operation})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Index != 0 || applyErr.Code != "REPORT_SLOT_MERGE_MULTIPLE_COMPONENTS" {
		t.Fatalf("slot merge error = %#v, %v", applyErr, err)
	}
}

func TestSlotMergeEnforcesTargetManifestMinimum(t *testing.T) {
	definition, operation := slotMergeCase(t)
	definition.Pages[0].Sections[0].Blocks[0].Layout.Desktop.W = 1
	definition.Pages[0].Sections[0].Blocks[0].Layout.Desktop.H = 1
	_, err := Apply(definition, []Operation{operation})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || applyErr.Code != "REPORT_SLOT_MERGE_BELOW_MIN_SIZE" {
		t.Fatalf("manifest minimum merge error = %#v, %v", applyErr, err)
	}
}

type operationApplyCase struct {
	before    report.ReportDefinition
	operation Operation
}

func operationApplyCases(t *testing.T) []operationApplyCase {
	t.Helper()
	base := operationDefinition(t)
	page := base.Pages[0]
	section := page.Sections[0]
	block := section.Blocks[0]
	zone := block.Zones[0]
	slot := zone.Slots[0]
	component := base.Components[0]
	filter := base.GlobalFilters[0]
	interaction := base.Interactions[0]
	changedDefinition := operationDefinition(t)
	changedDefinition.Metadata.Name = "替换后的报告"
	newPage := page
	newPage.ID = "page_created"
	newSection := section
	newSection.ID = "section_created"
	newBlock := block
	newBlock.ID = "block_created"
	newZone := zone
	newZone.ID = "zone_created"
	newSlot := slot
	newSlot.ID = "slot_created"
	newComponent := component
	newComponent.ID = "component_created"
	replacement := component
	replacement.ID = "component_replaced"
	newFilter := filter
	newFilter.ID = "filter_created"
	newInteraction := interaction
	newInteraction.ID = "interaction_created"
	updatedFilter := filter
	updatedFilter.Type = report.FilterMultiSelect
	updatedInteraction := interaction
	updatedInteraction.Action = report.InteractionHighlight
	settings := base.Metadata
	settings.Name = "更新后的报告"
	templateRef := base.TemplateRef
	templateRef.ReportTemplateVersion = "1.1.0"
	themeRef := base.ThemeRef
	themeRef.Version = "2.1.0"
	componentOptions := component.Options
	componentOptions.Title = "更新后的组件"
	dataBinding := component.DataBinding
	mergeDefinition, mergeOperation := slotMergeCase(t)
	splitDefinition, err := Apply(mergeDefinition, []Operation{mergeOperation})
	if err != nil {
		t.Fatal(err)
	}
	splitSlots := slices.Clone(mergeDefinition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots)
	splitOperation := Operation{Op: SlotSplit, TargetID: mergeOperation.Payload.(*SlotMergePayload).NewSlot.ID, Payload: &SlotSplitPayload{Slots: splitSlots}}

	return []operationApplyCase{
		{base, Operation{Op: ReportCreate, TargetID: base.Metadata.ID, Payload: &ReportCreatePayload{Definition: changedDefinition}}},
		{base, Operation{Op: ReportSettingsUpdate, TargetID: base.Metadata.ID, Payload: &ReportSettingsUpdatePayload{Metadata: settings, RuntimePolicy: base.RuntimePolicy}}},
		{base, Operation{Op: TemplateApply, TargetID: base.Metadata.ID, Payload: &TemplateApplyPayload{TemplateRef: templateRef}}},
		{base, Operation{Op: ThemeUpdate, TargetID: base.Metadata.ID, Payload: &ThemeUpdatePayload{ThemeRef: themeRef}}},
		{base, Operation{Op: PageCreate, TargetID: base.Metadata.ID, Payload: &PageCreatePayload{Page: newPage}}},
		{base, Operation{Op: PageUpdate, TargetID: page.ID, Payload: &PageUpdatePayload{Name: "updated page"}}},
		{base, Operation{Op: PageDelete, TargetID: page.ID, Payload: &PageDeletePayload{}}},
		{base, Operation{Op: PageReorder, TargetID: page.ID, Payload: &PageReorderPayload{Order: 9}}},
		{base, Operation{Op: SectionCreate, TargetID: page.ID, Payload: &SectionCreatePayload{Section: newSection}}},
		{base, Operation{Op: SectionUpdate, TargetID: section.ID, Payload: &SectionUpdatePayload{Name: "updated section"}}},
		{base, Operation{Op: SectionDelete, TargetID: section.ID, Payload: &SectionDeletePayload{}}},
		{base, Operation{Op: SectionReorder, TargetID: section.ID, Payload: &SectionReorderPayload{Order: 9}}},
		{base, Operation{Op: BlockCreate, TargetID: section.ID, Payload: &BlockCreatePayload{Block: newBlock}}},
		{base, Operation{Op: BlockMove, TargetID: block.ID, Payload: &BlockMovePayload{X: 2, Y: 20}}},
		{base, Operation{Op: BlockResize, TargetID: block.ID, Payload: &BlockResizePayload{W: 8, H: 8}}},
		{base, Operation{Op: BlockUpdate, TargetID: block.ID, Payload: &BlockUpdatePayload{Type: report.BlockChart}}},
		{base, Operation{Op: BlockCopy, TargetID: block.ID, Payload: &BlockCopyPayload{NewID: "block_copied"}}},
		{base, Operation{Op: BlockDelete, TargetID: block.ID, Payload: &BlockDeletePayload{}}},
		{base, Operation{Op: ZoneCreate, TargetID: block.ID, Payload: &ZoneCreatePayload{Zone: newZone}}},
		{base, Operation{Op: ZoneUpdate, TargetID: zone.ID, Payload: &ZoneUpdatePayload{Type: report.ZoneInsight, Layout: zone.Layout}}},
		{base, Operation{Op: ZoneDelete, TargetID: zone.ID, Payload: &ZoneDeletePayload{}}},
		{base, Operation{Op: ZoneReorder, TargetID: zone.ID, Payload: &ZoneReorderPayload{Order: 9}}},
		{base, Operation{Op: SlotCreate, TargetID: zone.ID, Payload: &SlotCreatePayload{Slot: newSlot}}},
		{mergeDefinition, mergeOperation},
		{splitDefinition, splitOperation},
		{base, Operation{Op: SlotUpdate, TargetID: slot.ID, Payload: &SlotUpdatePayload{Grid: slot.Grid, ComponentID: slot.ComponentID}}},
		{base, Operation{Op: SlotDelete, TargetID: slot.ID, Payload: &SlotDeletePayload{}}},
		{base, Operation{Op: ComponentCreate, TargetID: base.Metadata.ID, Payload: &ComponentCreatePayload{Component: newComponent}}},
		{base, Operation{Op: ComponentUpdate, TargetID: component.ID, Payload: &ComponentUpdatePayload{Options: componentOptions}}},
		{base, Operation{Op: ComponentReplace, TargetID: component.ID, Payload: &ComponentReplacePayload{Component: replacement}}},
		{base, Operation{Op: ComponentCopy, TargetID: component.ID, Payload: &ComponentCopyPayload{NewID: "component_copied"}}},
		{base, Operation{Op: ComponentDelete, TargetID: component.ID, Payload: &ComponentDeletePayload{}}},
		{base, Operation{Op: DataBindingUpdate, TargetID: component.ID, Payload: &DataBindingUpdatePayload{Mode: DataBindingSet, DataBinding: dataBinding}}},
		{base, Operation{Op: FilterCreate, TargetID: base.Metadata.ID, Payload: &FilterCreatePayload{Filter: newFilter}}},
		{base, Operation{Op: FilterUpdate, TargetID: filter.ID, Payload: &FilterUpdatePayload{Filter: updatedFilter}}},
		{base, Operation{Op: FilterDelete, TargetID: filter.ID, Payload: &FilterDeletePayload{}}},
		{base, Operation{Op: InteractionCreate, TargetID: base.Metadata.ID, Payload: &InteractionCreatePayload{Interaction: newInteraction}}},
		{base, Operation{Op: InteractionUpdate, TargetID: interaction.ID, Payload: &InteractionUpdatePayload{Interaction: updatedInteraction}}},
		{base, Operation{Op: InteractionDelete, TargetID: interaction.ID, Payload: &InteractionDeletePayload{}}},
		{base, Operation{Op: InsightUpdate, TargetID: component.ID, Payload: &InsightUpdatePayload{RichText: "<p>updated</p>", EvidenceID: "evidence_updated"}}},
		{base, Operation{Op: InsightRegenerate, TargetID: component.ID, Payload: &InsightRegeneratePayload{EvidenceID: "evidence_updated", AnalysisMethod: "TREND", AnalysisMethodVersion: "1.0.0"}}},
	}
}

func slotMergeCase(t *testing.T) (report.ReportDefinition, Operation) {
	t.Helper()
	definition := operationDefinition(t)
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Layout.Columns, zone.Layout.Rows = 2, 1
	zone.Slots[0].Grid = report.SlotGrid{X: 0, Y: 0, W: 1, H: 1}
	right := zone.Slots[0]
	right.ID = "slot_merge_right"
	right.Grid.X = 1
	right.ComponentID = ""
	zone.Slots = append(zone.Slots, right)
	merged := zone.Slots[0]
	merged.ID = "slot_merged"
	merged.Grid = report.SlotGrid{X: 0, Y: 0, W: 2, H: 1}
	return definition, Operation{Op: SlotMerge, TargetID: zone.Slots[0].ID, Payload: &SlotMergePayload{
		SlotIDs: []askdata.ID{zone.Slots[0].ID, zone.Slots[1].ID}, NewSlot: merged,
	}}
}

func semanticFingerprint(t *testing.T, definition report.ReportDefinition) string {
	t.Helper()
	if _, hash, err := compiler.Normalize(definition); err == nil {
		return hash
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	return string(askdata.HashBytes(raw))
}

func operationDefinition(t *testing.T) report.ReportDefinition {
	t.Helper()
	definition := mustReportExample(t, "multi-page-report.json")
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			for blockIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks {
				block := &definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex]
				for zoneIndex := range block.Zones {
					zone := &block.Zones[zoneIndex]
					zone.Layout.Columns, zone.Layout.Rows = 24, 12
					for slotIndex := range zone.Slots {
						zone.Slots[slotIndex].Grid = report.SlotGrid{X: 0, Y: 0, W: 6, H: 4}
					}
				}
			}
		}
	}
	if _, _, err := compiler.Normalize(definition); err != nil {
		t.Fatalf("operation fixture is not compiler-valid: %v", err)
	}
	return definition
}
