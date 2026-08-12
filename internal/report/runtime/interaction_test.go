package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

const (
	sourceComponentID = askdata.ID("00000000-0000-4000-8000-000000000201")
	targetComponentID = askdata.ID("00000000-0000-4000-8000-000000000202")
	interactionID     = askdata.ID("00000000-0000-4000-8000-000000000301")
)

func crossFilterDefinition(action report.InteractionAction) report.ReportDefinition {
	definition := testDefinition(datasetComponent(sourceComponentID), datasetComponent(targetComponentID))
	definition.Interactions = []report.Interaction{{
		ID: interactionID, SourceComponentID: sourceComponentID, Event: report.InteractionClick,
		Action: action, TargetComponentIDs: []askdata.ID{targetComponentID},
		FieldMappings: []report.FieldMapping{{SourceField: "channel", TargetField: "channel"}},
	}}
	return definition
}

func selection(componentID askdata.ID, field, rawValue string) ReportSelection {
	return ReportSelection{
		ComponentID: componentID,
		Values:      map[string]json.RawMessage{field: json.RawMessage(rawValue)},
	}
}

func TestSelectionsBecomeFiltersOnlyOnDeclaredTargets(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	resolved, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "channel", `"线上"`),
	})
	if err != nil {
		t.Fatalf("resolve selections: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected exactly the declared target to be filtered, got %d", len(resolved))
	}
	filters := resolved[targetComponentID]
	if len(filters) != 1 {
		t.Fatalf("expected one filter on the target, got %d", len(filters))
	}
	if filters[0].Field != "channel" || filters[0].Value != "线上" || !filters[0].Temporary {
		t.Fatalf("unexpected interaction filter: %#v", filters[0])
	}
	// The source component filters itself only if the report says so.
	if _, filtered := resolved[sourceComponentID]; filtered {
		t.Fatal("a source component must not be filtered unless it is also a declared target")
	}
}

// The central safety property: a caller cannot invent cross-filtering.
func TestSelectionOnAComponentWithNoInteractionFiltersNothing(t *testing.T) {
	definition := testDefinition(datasetComponent(sourceComponentID), datasetComponent(targetComponentID))
	resolved, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "channel", `"线上"`),
	})
	if err != nil {
		t.Fatalf("resolve selections: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("undeclared cross-filtering must produce nothing, got %#v", resolved)
	}
}

func TestSelectionCannotNameAFieldTheInteractionDoesNotMap(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	resolved, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "secret_cost", `"9.99"`),
	})
	if err != nil {
		t.Fatalf("resolve selections: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("an unmapped source field must not become a filter, got %#v", resolved)
	}
}

func TestPresentationOnlyActionsNeverNarrowAQuery(t *testing.T) {
	for _, action := range []report.InteractionAction{report.InteractionHighlight, report.InteractionNavigatePage} {
		definition := crossFilterDefinition(action)
		resolved, err := ResolveSelections(definition, []ReportSelection{
			selection(sourceComponentID, "channel", `"线上"`),
		})
		if err != nil {
			t.Fatalf("resolve selections for %s: %v", action, err)
		}
		if len(resolved) != 0 {
			t.Fatalf("%s must not produce a filter, got %#v", action, resolved)
		}
	}
}

func TestDrillDownNarrowsLikeAFilter(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionDrillDown)
	resolved, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "channel", `"线上"`),
	})
	if err != nil {
		t.Fatalf("resolve selections: %v", err)
	}
	if len(resolved[targetComponentID]) != 1 {
		t.Fatal("DRILL_DOWN must narrow its target")
	}
}

func TestSelectionValuesAcceptOnlyScalarsAndScalarLists(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	multi, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "channel", `["线上","线下"]`),
	})
	if err != nil {
		t.Fatalf("a scalar list must be accepted: %v", err)
	}
	values, ok := multi[targetComponentID][0].Value.([]string)
	if !ok || len(values) != 2 {
		t.Fatalf("expected a two-item string list, got %#v", multi[targetComponentID][0].Value)
	}

	for _, raw := range []string{`{"$gt":1}`, `[{"a":1}]`, `[]`, `null`} {
		if _, err := ResolveSelections(definition, []ReportSelection{
			selection(sourceComponentID, "channel", raw),
		}); err == nil {
			t.Fatalf("value %s must be rejected", raw)
		}
	}
}

func TestSelectionNumbersKeepTheirExactText(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	resolved, err := ResolveSelections(definition, []ReportSelection{
		selection(sourceComponentID, "channel", `900719925474099101`),
	})
	if err != nil {
		t.Fatalf("resolve selections: %v", err)
	}
	number, ok := resolved[targetComponentID][0].Value.(json.Number)
	if !ok || number.String() != "900719925474099101" {
		t.Fatalf("a large key must survive without float rounding, got %#v", resolved[targetComponentID][0].Value)
	}
}

func TestSelectionsAreRejectedWhenMalformed(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	cases := map[string][]ReportSelection{
		"unknown component": {selection("00000000-0000-4000-8000-0000000009ff", "channel", `"x"`)},
		"invalid id":        {selection("not-a-uuid", "channel", `"x"`)},
		"no values":         {{ComponentID: sourceComponentID, Values: map[string]json.RawMessage{}}},
		"duplicate source": {
			selection(sourceComponentID, "channel", `"a"`),
			selection(sourceComponentID, "channel", `"b"`),
		},
	}
	for name, selections := range cases {
		if _, err := ResolveSelections(definition, selections); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// End to end: a selection must reach the target's query and only the target's.
func TestSessionAppliesSelectionFiltersToTheDeclaredTargetOnly(t *testing.T) {
	definition := crossFilterDefinition(report.InteractionFilter)
	session, err := NewSession(testIdentity(), draftTarget(definition), time.Now().UTC())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	plan, err := session.Plan(HTTPPlanInput{
		PageID:     definition.Pages[0].ID,
		Selections: []ReportSelection{selection(sourceComponentID, "channel", `"线上"`)},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, component := range plan.Components {
		filters := component.Query.Filters
		switch component.ComponentID {
		case targetComponentID:
			if len(filters) != 1 || filters[0].Field != "channel" || !filters[0].Temporary {
				t.Fatalf("target must carry the interaction filter, got %#v", filters)
			}
		case sourceComponentID:
			if len(filters) != 0 {
				t.Fatalf("source must stay unfiltered, got %#v", filters)
			}
		}
	}
}
