package operation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestAll41OperationPayloadsDecodeAndRoundTrip(t *testing.T) {
	definition := mustReportExample(t, "multi-page-report.json")
	payloads := validPayloads(t, definition)
	types := Types()
	if len(types) != 41 || len(payloads) != 41 {
		t.Fatalf("operation types = %d, payloads = %d, want 41", len(types), len(payloads))
	}
	seen := map[Type]struct{}{}
	for _, operationType := range types {
		t.Run(string(operationType), func(t *testing.T) {
			if _, duplicate := seen[operationType]; duplicate {
				t.Fatalf("operation type %s is duplicated", operationType)
			}
			seen[operationType] = struct{}{}
			payload, exists := payloads[operationType]
			if !exists {
				t.Fatalf("valid payload for %s is missing", operationType)
			}
			bundle := Bundle{
				SchemaVersion: SchemaVersion,
				ReportID:      definition.Metadata.ID,
				BaseRevision:  18,
				Source:        SourceUser,
				Operations: []Operation{{
					Op:       operationType,
					TargetID: definition.Metadata.ID,
					Payload:  payload,
				}},
			}
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			decoded, err := Decode(raw, nil)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if decoded.Operations[0].Op != operationType {
				t.Fatalf("decoded op = %s, want %s", decoded.Operations[0].Op, operationType)
			}
		})
	}
}

func TestEveryOperationRejectsWrongPayloadShape(t *testing.T) {
	for _, operationType := range Types() {
		t.Run(string(operationType), func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"schemaVersion":"1.0",
				"reportId":"report_test",
				"baseRevision":1,
				"source":"USER",
				"aiRunId":null,
				"scope":null,
				"operations":[{"op":%q,"targetId":"target_test","payload":{"unexpected":true}}]
			}`, operationType))
			if _, err := Decode(raw, nil); err == nil {
				t.Fatalf("Decode(%s invalid payload) succeeded", operationType)
			}
		})
	}
}

func TestBundleLimitsAndAIGuards(t *testing.T) {
	definition := mustReportExample(t, "multi-page-report.json")
	pageID := definition.Pages[0].ID
	sectionID := definition.Pages[0].Sections[0].ID
	blockID := definition.Pages[0].Sections[0].Blocks[1].ID
	aiRunID := askdata.ID("ai_run_report_edit_001")

	t.Run("more than 100 operations", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceUser, MaxOperations+1, &PageDeletePayload{})
		assertBundleErrorContains(t, bundle, "operations count")
	})

	t.Run("AI more than 30 operations", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceAI, MaxAIOperations+1, &ComponentUpdatePayload{Options: report.ComponentOptions{}})
		bundle.AIRunID = &aiRunID
		bundle.Scope = &Scope{PageID: &pageID}
		assertBundleErrorContains(t, bundle, "AI operations exceeds")
	})

	t.Run("AI template apply", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceAI, 1, &TemplateApplyPayload{TemplateRef: definition.TemplateRef})
		bundle.AIRunID = &aiRunID
		bundle.Scope = &Scope{PageID: &pageID}
		bundle.Operations[0].Op = TemplateApply
		bundle.Operations[0].TargetID = pageID
		raw := mustMarshalBundle(t, bundle)
		_, err := Decode(raw, &definition)
		if ErrorCode(err) != CodeNotAllowedForAI {
			t.Fatalf("Decode() error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("AI batch delete", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceAI, MaxAIDeleteOperations+1, &ComponentDeletePayload{})
		bundle.AIRunID = &aiRunID
		bundle.Scope = &Scope{PageID: &pageID, SectionID: &sectionID, BlockID: &blockID}
		for index := range bundle.Operations {
			bundle.Operations[index].Op = ComponentDelete
			bundle.Operations[index].TargetID = "component_region_chart"
		}
		raw := mustMarshalBundle(t, bundle)
		_, err := Decode(raw, &definition)
		if ErrorCode(err) != CodeNotAllowedForAI {
			t.Fatalf("Decode() error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("AI target outside scope", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceAI, 1, &BlockUpdatePayload{Type: report.BlockTable})
		bundle.AIRunID = &aiRunID
		bundle.Scope = &Scope{PageID: &pageID}
		bundle.Operations[0].Op = BlockUpdate
		bundle.Operations[0].TargetID = "block_region_table"
		raw := mustMarshalBundle(t, bundle)
		_, err := Decode(raw, &definition)
		if ErrorCode(err) != CodeOutOfScope {
			t.Fatalf("Decode() error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("AI target inside exact block scope", func(t *testing.T) {
		bundle := baseBundle(definition.Metadata.ID, SourceAI, 1, &ComponentUpdatePayload{Options: report.ComponentOptions{}})
		bundle.AIRunID = &aiRunID
		bundle.Scope = &Scope{PageID: &pageID, SectionID: &sectionID, BlockID: &blockID}
		bundle.Operations[0].Op = ComponentUpdate
		bundle.Operations[0].TargetID = "component_region_chart"
		raw := mustMarshalBundle(t, bundle)
		if _, err := Decode(raw, &definition); err != nil {
			t.Fatalf("Decode(in-scope AI) error = %v", err)
		}
	})
}

func TestOperationSchemaEnumeratesExactlyTheFrozen41Types(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "schemas", "report-operation-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is invalid JSON: %v", err)
	}
	definitions := schema["$defs"].(map[string]any)
	operation := definitions["operation"].(map[string]any)
	branches := operation["oneOf"].([]any)
	if len(branches) != 41 {
		t.Fatalf("schema operation oneOf has %d branches, want 41", len(branches))
	}
	got := make([]string, 0, len(branches))
	for _, branchValue := range branches {
		branch := branchValue.(map[string]any)
		ref := branch["$ref"].(string)
		name := strings.TrimPrefix(ref, "#/$defs/")
		definition := definitions[name].(map[string]any)
		properties := definition["properties"].(map[string]any)
		op := properties["op"].(map[string]any)["const"].(string)
		payload := properties["payload"].(map[string]any)
		if _, typed := payload["$ref"]; !typed {
			t.Fatalf("schema branch %s does not reference a typed payload", op)
		}
		got = append(got, op)
	}
	want := make([]string, 0, len(Types()))
	for _, operationType := range Types() {
		want = append(want, string(operationType))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schema ops = %v, want %v", got, want)
	}
	if strings.Contains(string(raw), `"UNDO"`) || strings.Contains(string(raw), `"REDO"`) {
		t.Fatal("UNDO/REDO must not appear in Report Operation v1")
	}
}

func validPayloads(t *testing.T, definition report.ReportDefinition) map[Type]Payload {
	t.Helper()
	page := definition.Pages[0]
	section := page.Sections[0]
	block := section.Blocks[0]
	zone := block.Zones[0]
	slot := zone.Slots[0]
	component := definition.Components[0]
	filter := definition.GlobalFilters[0]
	interaction := definition.Interactions[0]
	newSlot := slot
	newSlot.ID = "slot_merged"
	splitSlotOne := slot
	splitSlotOne.ID = "slot_split_one"
	splitSlotTwo := slot
	splitSlotTwo.ID = "slot_split_two"
	return map[Type]Payload{
		ReportCreate:         &ReportCreatePayload{Definition: definition},
		ReportSettingsUpdate: &ReportSettingsUpdatePayload{Metadata: definition.Metadata, RuntimePolicy: definition.RuntimePolicy},
		TemplateApply:        &TemplateApplyPayload{TemplateRef: definition.TemplateRef},
		ThemeUpdate:          &ThemeUpdatePayload{ThemeRef: definition.ThemeRef},
		PageCreate:           &PageCreatePayload{Page: page},
		PageUpdate:           &PageUpdatePayload{Name: "更新后的页面"},
		PageDelete:           &PageDeletePayload{},
		PageReorder:          &PageReorderPayload{Order: 2},
		SectionCreate:        &SectionCreatePayload{Section: section},
		SectionUpdate:        &SectionUpdatePayload{Name: "更新后的章节"},
		SectionDelete:        &SectionDeletePayload{},
		SectionReorder:       &SectionReorderPayload{Order: 2},
		BlockCreate:          &BlockCreatePayload{Block: block},
		BlockMove:            &BlockMovePayload{X: 2, Y: 3},
		BlockResize:          &BlockResizePayload{W: 8, H: 6},
		BlockUpdate:          &BlockUpdatePayload{Type: report.BlockChart},
		BlockCopy:            &BlockCopyPayload{NewID: "block_copy"},
		BlockDelete:          &BlockDeletePayload{},
		ZoneCreate:           &ZoneCreatePayload{Zone: zone},
		ZoneUpdate:           &ZoneUpdatePayload{Type: zone.Type, Layout: zone.Layout},
		ZoneDelete:           &ZoneDeletePayload{},
		ZoneReorder:          &ZoneReorderPayload{Order: 2},
		SlotCreate:           &SlotCreatePayload{Slot: slot},
		SlotMerge:            &SlotMergePayload{SlotIDs: []askdata.ID{"slot_one", "slot_two"}, NewSlot: newSlot},
		SlotSplit:            &SlotSplitPayload{Slots: []report.Slot{splitSlotOne, splitSlotTwo}},
		SlotUpdate:           &SlotUpdatePayload{Grid: slot.Grid, ComponentID: slot.ComponentID},
		SlotDelete:           &SlotDeletePayload{},
		ComponentCreate:      &ComponentCreatePayload{Component: component},
		ComponentUpdate:      &ComponentUpdatePayload{Options: component.Options},
		ComponentReplace:     &ComponentReplacePayload{Component: component},
		ComponentCopy:        &ComponentCopyPayload{NewID: "component_copy"},
		ComponentDelete:      &ComponentDeletePayload{},
		DataBindingUpdate:    &DataBindingUpdatePayload{Mode: DataBindingSet, DataBinding: component.DataBinding},
		FilterCreate:         &FilterCreatePayload{Filter: filter},
		FilterUpdate:         &FilterUpdatePayload{Filter: filter},
		FilterDelete:         &FilterDeletePayload{},
		InteractionCreate:    &InteractionCreatePayload{Interaction: interaction},
		InteractionUpdate:    &InteractionUpdatePayload{Interaction: interaction},
		InteractionDelete:    &InteractionDeletePayload{},
		InsightUpdate:        &InsightUpdatePayload{RichText: "收入保持稳定增长。", EvidenceID: "evidence_revenue_001"},
		InsightRegenerate:    &InsightRegeneratePayload{EvidenceID: "evidence_revenue_001", AnalysisMethod: "PERIOD_COMPARISON", AnalysisMethodVersion: "1.0.0"},
	}
}

func baseBundle(reportID askdata.ID, source Source, count int, payload Payload) Bundle {
	operations := make([]Operation, count)
	for index := range operations {
		operations[index] = Operation{Op: operationTypeForPayload(payload), TargetID: reportID, Payload: payload}
	}
	return Bundle{
		SchemaVersion: SchemaVersion,
		ReportID:      reportID,
		BaseRevision:  1,
		Source:        source,
		Operations:    operations,
	}
}

func operationTypeForPayload(payload Payload) Type {
	switch payload.(type) {
	case *PageDeletePayload:
		return PageDelete
	case *ComponentUpdatePayload:
		return ComponentUpdate
	case *TemplateApplyPayload:
		return TemplateApply
	case *ComponentDeletePayload:
		return ComponentDelete
	case *BlockUpdatePayload:
		return BlockUpdate
	default:
		panic(fmt.Sprintf("unsupported test payload %T", payload))
	}
}

func assertBundleErrorContains(t *testing.T, bundle Bundle, want string) {
	t.Helper()
	if err := bundle.Validate(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Bundle.Validate() error = %v, want %q", err, want)
	}
}

func mustMarshalBundle(t *testing.T, bundle Bundle) []byte {
	t.Helper()
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal(Bundle) error = %v", err)
	}
	return raw
}

func mustReportExample(t *testing.T, name string) report.ReportDefinition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "examples", "report-definition", name))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := report.Decode(raw)
	if err != nil {
		t.Fatalf("report.Decode(%s) error = %v", name, err)
	}
	return definition
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
