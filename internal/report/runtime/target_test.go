package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

func testIdentity() store.Identity {
	return store.Identity{
		TenantID: "00000000-0000-4000-8000-000000000001",
		ActorID:  "00000000-0000-4000-8000-000000000002",
		DomainID: "00000000-0000-4000-8000-000000000003",
	}
}

func testDefinition(components ...report.Component) report.ReportDefinition {
	blocks := make([]report.Block, 0, len(components))
	for index, component := range components {
		id := askdata.ID(strings.Repeat("0", 8) + "-0000-4000-8000-00000000020" + string(rune('0'+index)))
		blocks = append(blocks, report.Block{
			ID:     id,
			Type:   report.BlockChart,
			Layout: report.BlockLayout{Desktop: report.DesktopBlockLayout{X: 0, Y: index * 4, W: 8, H: 4}},
			Zones: []report.Zone{{
				Order:  1,
				ID:     askdata.ID(strings.Repeat("0", 8) + "-0000-4000-8000-00000000030" + string(rune('0'+index))),
				Type:   report.ZoneContent,
				Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 1, Columns: 8, Rows: 4, Overflow: report.OverflowExpand},
				Slots: []report.Slot{{
					ID:          askdata.ID(strings.Repeat("0", 8) + "-0000-4000-8000-00000000040" + string(rune('0'+index))),
					Grid:        report.SlotGrid{X: 0, Y: 0, W: 8, H: 4},
					ComponentID: component.ID,
				}},
			}},
		})
	}
	return report.ReportDefinition{
		Pages: []report.Page{{
			ID: "00000000-0000-4000-8000-000000000110", Name: "page", Order: 1,
			Sections: []report.Section{{
				ID: "00000000-0000-4000-8000-000000000120", Name: "section", Order: 1, Blocks: blocks,
			}},
		}},
		Components: components,
		DataContexts: []report.DataContext{{
			ID:               "00000000-0000-4000-8000-000000000130",
			DatasetID:        "00000000-0000-4000-8000-000000000131",
			DatasetVersionID: "00000000-0000-4000-8000-000000000132",
			QueryPolicy:      report.QueryPolicy{TimeoutMS: 5000, MaxRows: 500},
		}},
		RuntimePolicy: report.RuntimePolicy{MaxConcurrentQueries: 4},
	}
}

func datasetComponent(id askdata.ID) report.Component {
	contextID := askdata.ID("00000000-0000-4000-8000-000000000130")
	return report.Component{
		ID: id, TemplateRef: report.ComponentTemplateReference{Type: "bar-comparison", Version: "1.0.0"},
		DataBinding: &report.DataBinding{
			BindingMode: report.BindingDatasetField, DataContextID: &contextID,
			Dimensions: []report.FieldBinding{{Role: report.RoleCategory, Field: "channel"}},
			Measures:   []report.FieldBinding{{Role: report.RoleValue, Field: "revenue"}},
		},
	}
}

func semanticComponent(id askdata.ID) report.Component {
	return report.Component{
		ID: id, TemplateRef: report.ComponentTemplateReference{Type: "line-trend", Version: "1.0.0"},
		DataBinding: &report.DataBinding{
			BindingMode:      report.BindingSemanticIR,
			SemanticQueryRef: &report.SemanticQueryRef{},
		},
	}
}

func draftTarget(definition report.ReportDefinition) ExecutionTarget {
	return DraftTarget("00000000-0000-4000-8000-000000000101", definition, strings.Repeat("c", 64), 7)
}

func TestExecutionTargetRejectsMixedProvenance(t *testing.T) {
	published := PublishedTarget(testLoadedReport())
	if err := published.Validate(); err != nil {
		t.Fatalf("a loaded version must be a valid target: %v", err)
	}
	if err := draftTarget(testDefinition()).Validate(); err != nil {
		t.Fatalf("a draft must be a valid target: %v", err)
	}

	withVersion := draftTarget(testDefinition())
	withVersion.VersionID = published.VersionID
	if withVersion.Validate() == nil {
		t.Fatal("a draft carrying a published version must be rejected")
	}
	withoutVersion := published
	withoutVersion.VersionID = ""
	if withoutVersion.Validate() == nil {
		t.Fatal("a published target without an immutable version must be rejected")
	}
}

// A preview must never be able to reuse — or be reused as — a published result.
func TestDraftAndPublishedPolicyScopesNeverCollide(t *testing.T) {
	identity := testIdentity()
	published := PublishedTarget(testLoadedReport())
	draft := draftTarget(testDefinition())
	draft.ReportID = published.ReportID

	publishedHash, err := published.PolicyScopeHash(identity)
	if err != nil {
		t.Fatal(err)
	}
	draftHash, err := draft.PolicyScopeHash(identity)
	if err != nil {
		t.Fatal(err)
	}
	if publishedHash == draftHash {
		t.Fatal("draft and published scopes for the same report must differ")
	}

	// Editing the draft must change its scope, so a result computed before a
	// binding change can never be served after it.
	edited := draft
	edited.DefinitionHash = strings.Repeat("d", 64)
	editedHash, err := edited.PolicyScopeHash(identity)
	if err != nil {
		t.Fatal(err)
	}
	if editedHash == draftHash {
		t.Fatal("changing the draft definition must change its policy scope")
	}

	other := identity
	other.ActorID = "00000000-0000-4000-8000-00000000000f"
	otherHash, err := draft.PolicyScopeHash(other)
	if err != nil {
		t.Fatal(err)
	}
	if otherHash == draftHash {
		t.Fatal("a different viewer must get a different policy scope")
	}
}

func TestDraftSessionPlansDatasetBindingsWithoutAVersionPin(t *testing.T) {
	definition := testDefinition(datasetComponent("00000000-0000-4000-8000-000000000201"))
	session, err := NewSession(testIdentity(), draftTarget(definition), time.Now().UTC())
	if err != nil {
		t.Fatalf("draft session: %v", err)
	}
	plan, err := session.Plan(HTTPPlanInput{PageID: definition.Pages[0].ID})
	if err != nil {
		t.Fatalf("draft plan: %v", err)
	}
	if len(plan.Components) != 1 {
		t.Fatalf("expected one planned component, got %d", len(plan.Components))
	}
	query := plan.Components[0].Query
	if query == nil {
		t.Fatal("a dataset binding must be executable against a draft")
	}
	if !query.Draft || query.ReportVersionID != "" {
		t.Fatalf("a draft query must carry no version pin: %#v", query)
	}
	if plan.Components[0].Blocked != "" {
		t.Fatalf("a dataset binding must not be blocked, got %q", plan.Components[0].Blocked)
	}
}

func TestDraftSessionBlocksSemanticBindingsWithAReason(t *testing.T) {
	definition := testDefinition(semanticComponent("00000000-0000-4000-8000-000000000201"))
	session, err := NewSession(testIdentity(), draftTarget(definition), time.Now().UTC())
	if err != nil {
		t.Fatalf("draft session: %v", err)
	}
	plan, err := session.Plan(HTTPPlanInput{PageID: definition.Pages[0].ID})
	if err != nil {
		t.Fatalf("draft plan: %v", err)
	}
	if plan.Components[0].Query != nil {
		t.Fatal("a pinned semantic binding must not be planned against a draft")
	}
	if plan.Components[0].Blocked != CodeDraftPreviewRequiresPublish {
		t.Fatalf("expected %s, got %q", CodeDraftPreviewRequiresPublish, plan.Components[0].Blocked)
	}

	// A blocked component must surface its reason rather than read as ready.
	results := ExecuteBatch(t.Context(), plan, nil, 1)
	if len(results) != 1 || results[0].State != StateError ||
		results[0].ErrorCode != CodeDraftPreviewRequiresPublish {
		t.Fatalf("blocked component must report its reason, got %#v", results)
	}
}

func TestPublishedSessionStillPinsItsVersion(t *testing.T) {
	definition := testDefinition(datasetComponent("00000000-0000-4000-8000-000000000201"))
	loaded := testLoadedReport()
	loaded.Definition = definition
	session, err := NewSession(testIdentity(), PublishedTarget(loaded), time.Now().UTC())
	if err != nil {
		t.Fatalf("published session: %v", err)
	}
	plan, err := session.Plan(HTTPPlanInput{PageID: definition.Pages[0].ID})
	if err != nil {
		t.Fatalf("published plan: %v", err)
	}
	query := plan.Components[0].Query
	if query.Draft || query.ReportVersionID != loaded.VersionID {
		t.Fatalf("a published query must carry its immutable version: %#v", query)
	}
}

// The executor keeps the invariant locally so a mis-planned request still fails.
func TestExecutorRejectsInconsistentVersionPins(t *testing.T) {
	executor := GovernedQueryExecutor{Dataset: datasetRunnerFunc(func(context.Context, DatasetExecutionRequest) (QueryResult, error) {
		return QueryResult{}, nil
	})}
	base := QueryRequest{
		BindingMode: report.BindingDatasetField, PolicyScopeHash: strings.Repeat("a", 64), Limit: 10,
		ReportID:         "00000000-0000-4000-8000-000000000101",
		DatasetID:        "00000000-0000-4000-8000-000000000131",
		DatasetVersionID: "00000000-0000-4000-8000-000000000132",
		DataContextID:    "00000000-0000-4000-8000-000000000130",
		Measures:         []report.FieldBinding{{Role: report.RoleValue, Field: "revenue"}},
	}

	draftWithVersion := base
	draftWithVersion.Draft = true
	draftWithVersion.ReportVersionID = "00000000-0000-4000-8000-000000000102"
	if _, err := executor.ExecuteReportQuery(t.Context(), draftWithVersion); err == nil {
		t.Fatal("a draft query carrying a version pin must be rejected")
	}

	publishedWithoutVersion := base
	if _, err := executor.ExecuteReportQuery(t.Context(), publishedWithoutVersion); err == nil {
		t.Fatal("a published query without a version pin must be rejected")
	}

	semanticDraft := base
	semanticDraft.BindingMode = report.BindingSemanticIR
	semanticDraft.Draft = true
	if _, err := executor.ExecuteReportQuery(t.Context(), semanticDraft); err == nil {
		t.Fatal("a semantic binding must never execute as a draft")
	}
}
