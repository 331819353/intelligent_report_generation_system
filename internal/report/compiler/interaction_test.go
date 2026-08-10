package compiler

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestValidateInteractionsRejectsUnsupportedEventAndTargetAction(t *testing.T) {
	registry, err := defaultRegistry()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("event", func(t *testing.T) {
		definition := compilerDefinition(t)
		addRichTextComponent(&definition, 2, "safe")
		definition.Interactions = []report.Interaction{{
			ID: "interaction_unsupported_event", SourceComponentID: definition.Components[1].ID,
			Event: report.InteractionClick, Action: report.InteractionFilter,
			TargetComponentIDs: []askdata.ID{definition.Components[0].ID}, FieldMappings: []report.FieldMapping{},
		}}
		assertHasInteractionIssue(t, ValidateInteractions(definition, registry), "REPORT_INTERACTION_EVENT_UNSUPPORTED")
	})

	t.Run("target action", func(t *testing.T) {
		definition := compilerDefinition(t)
		addRichTextComponent(&definition, 2, "safe")
		definition.Interactions = []report.Interaction{{
			ID: "interaction_unsupported_action", SourceComponentID: definition.Components[0].ID,
			Event: report.InteractionClick, Action: report.InteractionFilter,
			TargetComponentIDs: []askdata.ID{definition.Components[1].ID}, FieldMappings: []report.FieldMapping{},
		}}
		assertHasInteractionIssue(t, ValidateInteractions(definition, registry), "REPORT_INTERACTION_ACTION_UNSUPPORTED")
	})
}

func TestValidateInteractionsRejectsSelfAndThreeNodeCycles(t *testing.T) {
	registry, err := defaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := compilerDefinition(t)
	addLineComponent(&definition, 2)
	addLineComponent(&definition, 3)
	ids := []askdata.ID{definition.Components[0].ID, definition.Components[1].ID, definition.Components[2].ID}

	definition.Interactions = []report.Interaction{interactionForTest("interaction_self", ids[0], ids[0])}
	assertHasInteractionIssue(t, ValidateInteractions(definition, registry), "REPORT_INTERACTION_CYCLE")

	definition.Interactions = []report.Interaction{
		interactionForTest("interaction_a_b", ids[0], ids[1]),
		interactionForTest("interaction_b_c", ids[1], ids[2]),
		interactionForTest("interaction_c_a", ids[2], ids[0]),
	}
	assertHasInteractionIssue(t, ValidateInteractions(definition, registry), "REPORT_INTERACTION_CYCLE")
}

func TestValidateDefinitionUsesStableMissingInteractionTargetCode(t *testing.T) {
	definition := compilerDefinition(t)
	definition.Interactions = []report.Interaction{interactionForTest("interaction_missing", definition.Components[0].ID, "component_missing")}
	assertHasInteractionIssue(t, ValidateDefinition(definition, nil), "REPORT_INTERACTION_TARGET_MISSING")
}

func TestValidateInteractionsRejectsCrossContextFieldMapping(t *testing.T) {
	registry, err := defaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := compilerDefinition(t)
	addLineComponent(&definition, 2)
	otherContext := askdata.ID("inventory_context")
	definition.DataContexts = append(definition.DataContexts, report.DataContext{
		ID: otherContext, DatasetID: "inventory_dataset", DatasetVersionID: "inventory_dataset_version",
		DefaultParameters: []report.DefaultParameter{}, QueryPolicy: report.QueryPolicy{TimeoutMS: 5000, MaxRows: 1000},
	})
	binding := *definition.Components[1].DataBinding
	binding.DataContextID = &otherContext
	binding.Dimensions = append([]report.FieldBinding(nil), binding.Dimensions...)
	binding.Measures = append([]report.FieldBinding(nil), binding.Measures...)
	definition.Components[1].DataBinding = &binding
	definition.Interactions = []report.Interaction{{
		ID: "interaction_cross_context", SourceComponentID: definition.Components[0].ID,
		Event: report.InteractionClick, Action: report.InteractionFilter,
		TargetComponentIDs: []askdata.ID{definition.Components[1].ID},
		FieldMappings:      []report.FieldMapping{{SourceField: "region", TargetField: "warehouse"}},
	}}
	assertHasInteractionIssue(t, ValidateInteractions(definition, registry), "REPORT_INTERACTION_FIELD_INCOMPATIBLE")
}

func TestInteractionWithoutExplicitTargetsDoesNotPropagate(t *testing.T) {
	definition := compilerDefinition(t)
	definition.Interactions = []report.Interaction{{
		ID: "interaction_without_scope", SourceComponentID: definition.Components[0].ID,
		Event: report.InteractionClick, Action: report.InteractionFilter,
		TargetComponentIDs: []askdata.ID{}, FieldMappings: []report.FieldMapping{},
	}}
	if err := definition.Validate(); err == nil {
		t.Fatal("component interaction without explicit targets was accepted")
	}
}

func interactionForTest(id, source, target askdata.ID) report.Interaction {
	return report.Interaction{
		ID: id, SourceComponentID: source, Event: report.InteractionClick, Action: report.InteractionFilter,
		TargetComponentIDs: []askdata.ID{target}, FieldMappings: []report.FieldMapping{},
	}
}

func assertHasInteractionIssue(t *testing.T, issues ValidationIssues, code string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("issues = %#v; want code %s", issues, code)
}
