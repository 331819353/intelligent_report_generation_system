package publication

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/operation"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/store"
)

type upgradeComponentMigratorStub struct {
	called int
	err    error
}

func (stub *upgradeComponentMigratorStub) MigrateComponent(component reportmodel.Component, target string) (reportmodel.Component, error) {
	stub.called++
	if stub.err != nil {
		return reportmodel.Component{}, stub.err
	}
	component.TemplateRef.Version = target
	component.Options.Title = "migrated"
	return component, nil
}

func TestApplyUpgradeSpecKeepsPublishedInputImmutable(t *testing.T) {
	oldMetric := askdata.ID("11111111-1111-4111-8111-111111111111")
	newMetric := askdata.ID("22222222-2222-4222-8222-222222222222")
	oldRelease := askdata.ID("33333333-3333-4333-8333-333333333333")
	newRelease := askdata.ID("44444444-4444-4444-8444-444444444444")
	componentID := askdata.ID("component-a")
	definition := reportmodel.ReportDefinition{Components: []reportmodel.Component{{
		ID: componentID,
		DataBinding: &reportmodel.DataBinding{BindingMode: reportmodel.BindingSemanticIR, SemanticQueryRef: &reportmodel.SemanticQueryRef{
			SemanticReleaseID: oldRelease, SemanticIR: ircontract.SemanticIR{
				SemanticReleaseID: oldRelease, Metrics: []ircontract.Metric{{MetricVersionID: oldMetric}},
			},
		}},
	}}}
	updated, affected, before, err := applyUpgradeSpec(definition, UpgradeSpec{
		Kind: compiler.ChangeMetricVersion, OldObjectID: string(oldMetric), NewObjectID: string(newMetric),
		NewSemanticReleaseID: newRelease, NewSemanticContentHash: askdata.HashBytes([]byte("new")),
	})
	if err != nil || len(affected) != 1 {
		t.Fatalf("applyUpgradeSpec() = %#v, %v", affected, err)
	}
	if definition.Components[0].DataBinding.SemanticQueryRef.SemanticIR.Metrics[0].MetricVersionID != oldMetric {
		t.Fatal("published input was mutated")
	}
	if before[componentID].DataBinding.SemanticQueryRef.SemanticIR.Metrics[0].MetricVersionID != oldMetric {
		t.Fatal("before snapshot was mutated")
	}
	if updated.Components[0].DataBinding.SemanticQueryRef.SemanticIR.Metrics[0].MetricVersionID != newMetric {
		t.Fatal("replacement was not applied")
	}
}

func TestComponentTemplateUpgradeRequiresAndInvokesMigrator(t *testing.T) {
	oldID := "bar-comparison@1.0.0"
	newID := "bar-comparison@2.0.0"
	if err := validateUpgradeSpec(UpgradeSpec{Kind: compiler.ChangeComponentTemplate, OldObjectID: oldID, NewObjectID: newID}); err != nil {
		t.Fatal(err)
	}
	if err := validateUpgradeSpec(UpgradeSpec{Kind: compiler.ChangeComponentTemplate, OldObjectID: oldID, NewObjectID: "line-trend@2.0.0"}); !errors.Is(err, ErrUpgradeInvalid) {
		t.Fatalf("cross-type upgrade error=%v", err)
	}

	definition := reportmodel.ReportDefinition{Components: []reportmodel.Component{{
		ID: askdata.ID("component-a"), TemplateRef: reportmodel.ComponentTemplateReference{Type: "bar-comparison", Version: "1.0.0"},
	}}}
	updated, affected, before, err := applyUpgradeSpec(definition, UpgradeSpec{
		Kind: compiler.ChangeComponentTemplate, OldObjectID: oldID, NewObjectID: newID,
	})
	if err != nil || len(affected) != 1 {
		t.Fatalf("applyUpgradeSpec() affected=%v err=%v", affected, err)
	}
	migrator := &upgradeComponentMigratorStub{}
	original := before[affected[0]]
	migrated, err := migrator.MigrateComponent(original, "2.0.0")
	if err != nil || migrator.called != 1 || migrated.TemplateRef.Version != "2.0.0" || migrated.Options.Title != "migrated" {
		t.Fatalf("migrated=%+v called=%d err=%v", migrated, migrator.called, err)
	}
	if definition.Components[0].TemplateRef.Version != "1.0.0" || updated.Components[0].TemplateRef.Version != "2.0.0" {
		t.Fatal("template upgrade did not preserve the published input")
	}
}

type upgradeRepositoryStub struct {
	draft     store.Draft
	version   store.Version
	saveCalls int
}

func (stub *upgradeRepositoryStub) GetDraft(context.Context, store.Identity, askdata.ID) (store.Draft, error) {
	return stub.draft, nil
}

func (stub *upgradeRepositoryStub) GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error) {
	return stub.version, nil
}

func (stub *upgradeRepositoryStub) SaveDraftWithRevision(
	_ context.Context, _ store.Identity, reportID askdata.ID, input store.SaveInput,
) (store.Draft, store.Revision, error) {
	stub.saveCalls++
	if input.ExpectedRevision != stub.draft.RevisionNo || len(input.Operations) != 1 ||
		input.Operations[0].Op != operation.ReportCreate {
		return store.Draft{}, store.Revision{}, errors.New("unexpected upgrade save")
	}
	stub.draft.RevisionNo++
	return stub.draft, store.Revision{ReportID: reportID, RevisionNo: stub.draft.RevisionNo}, nil
}

type upgradeRecompilerStub struct{ calls int }

func (stub *upgradeRecompilerStub) RecompileComponent(
	_ context.Context, _ store.Identity, _ askdata.ID, component reportmodel.Component,
) (RecompiledSemantic, error) {
	stub.calls++
	reference := *component.DataBinding.SemanticQueryRef
	id := askdata.ID("99999999-9999-4999-8999-999999999999")
	reference.SourceQuestionRunID = nil
	reference.CompilationArtifactID = &id
	reference.QueryPlanHash = askdata.HashBytes([]byte("upgraded-plan"))
	return RecompiledSemantic{
		Reference:   reference,
		Compilation: SemanticCompilation{ID: id, ComponentID: component.ID},
	}, nil
}

type upgradeComparatorStub struct{ calls int }

func (stub *upgradeComparatorStub) CompareUpgrade(
	_ context.Context, _ store.Identity, comparison UpgradeComparison,
) (SampleImpact, error) {
	stub.calls++
	if comparison.Compilation == nil || comparison.Compilation.ComponentID != comparison.After.ID {
		return SampleImpact{}, errors.New("compiled sample is missing")
	}
	return SampleImpact{Direction: "INCREASE", RelativeChange: "0.125000"}, nil
}

type upgradeCompilationStoreStub struct{ calls int }

func (stub *upgradeCompilationStoreStub) SaveCompilations(
	_ context.Context, _ store.Identity, _ askdata.ID, compilations []SemanticCompilation,
) error {
	stub.calls++
	if len(compilations) != 1 {
		return errors.New("unexpected compilation count")
	}
	return nil
}

func TestUpgradePreviewDoesNotPersistAndConfirmCreatesRevision(t *testing.T) {
	reportID := askdata.ID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	versionID := askdata.ID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	oldMetric := askdata.ID("11111111-1111-4111-8111-111111111111")
	newMetric := askdata.ID("22222222-2222-4222-8222-222222222222")
	oldRelease := askdata.ID("33333333-3333-4333-8333-333333333333")
	newRelease := askdata.ID("44444444-4444-4444-8444-444444444444")
	domainID := askdata.ID("55555555-5555-4555-8555-555555555555")
	definition := publicationDefinition(t, "ask-data-report.json")
	definition.Metadata.ID = reportID
	reference := definition.Components[0].DataBinding.SemanticQueryRef
	reference.SemanticReleaseID = oldRelease
	reference.SemanticContentHash = askdata.HashBytes([]byte("old release"))
	reference.SemanticIR.SemanticReleaseID = oldRelease
	reference.SemanticIR.SemanticContentHash = reference.SemanticContentHash
	reference.SemanticIR.DomainID = domainID
	reference.SemanticIR.Metrics[0].MetricVersionID = oldMetric
	_, definitionHash, err := compiler.Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	repository := &upgradeRepositoryStub{
		draft: store.Draft{ReportID: reportID, Definition: definition, DefinitionHash: definitionHash, RevisionNo: 7},
		version: store.Version{
			ID: versionID, ReportID: reportID, Definition: definition, DefinitionHash: definitionHash,
			PublishedAt: time.Now().UTC(),
		},
	}
	recompiler := &upgradeRecompilerStub{}
	comparator := &upgradeComparatorStub{}
	compilations := &upgradeCompilationStoreStub{}
	service := &UpgradeService{
		Repository: repository, Recompiler: recompiler, Comparator: comparator, Compilations: compilations,
	}
	identity := store.Identity{
		TenantID: "66666666-6666-4666-8666-666666666666",
		ActorID:  "77777777-7777-4777-8777-777777777777",
		DomainID: domainID,
	}
	spec := UpgradeSpec{
		Kind: compiler.ChangeMetricVersion, OldObjectID: string(oldMetric), NewObjectID: string(newMetric),
		NewSemanticReleaseID: newRelease, NewSemanticContentHash: askdata.HashBytes([]byte("new release")),
	}
	preview, err := service.Preview(context.Background(), identity, reportID, spec)
	if err != nil {
		t.Fatal(err)
	}
	if repository.saveCalls != 0 || compilations.calls != 0 || recompiler.calls != 1 || comparator.calls != 1 ||
		len(preview.AffectedComponents) != 1 || len(preview.SampleImpacts) != 1 {
		t.Fatalf("preview caused writes or skipped validation: %#v", preview)
	}
	draft, revision, err := service.Confirm(context.Background(), identity, reportID, spec, preview.ConfirmationToken)
	if err != nil {
		t.Fatal(err)
	}
	if repository.saveCalls != 1 || compilations.calls != 1 || recompiler.calls != 2 || comparator.calls != 2 ||
		draft.RevisionNo != 8 || revision.RevisionNo != 8 {
		t.Fatalf("confirm did not persist exactly one artifact/revision: draft=%+v revision=%+v", draft, revision)
	}
}

func TestUpgradeConfirmRejectsStaleTokenBeforeWriting(t *testing.T) {
	service := &UpgradeService{}
	_, _, err := service.Confirm(
		context.Background(), store.Identity{}, "bad", UpgradeSpec{}, askdata.HashBytes([]byte("stale")),
	)
	if !errors.Is(err, ErrUpgradeInvalid) {
		t.Fatalf("Confirm() error=%v", err)
	}
}

func TestCompareSampleResultsUsesOnlyMetricAliases(t *testing.T) {
	beforeRef := &reportmodel.SemanticQueryRef{SemanticIR: ircontract.SemanticIR{
		Metrics: []ircontract.Metric{{MetricVersionID: "metric-a", Alias: "revenue"}},
	}}
	afterRef := &reportmodel.SemanticQueryRef{SemanticIR: beforeRef.SemanticIR}
	before := reportruntime.QueryResult{
		Columns: []string{"region_code", "revenue"}, Rows: [][]any{{999, "10.5"}, {888, int64(9)}},
		Hash: askdata.HashBytes([]byte("before")),
	}
	after := reportruntime.QueryResult{
		Columns: []string{"region_code", "revenue"}, Rows: [][]any{{1, 12.5}, {2, 9}},
		Hash: askdata.HashBytes([]byte("after")),
	}
	impact, err := compareSampleResults(beforeRef, afterRef, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if impact.Direction != "INCREASE" || impact.RelativeChange != "0.102564" ||
		askdata.ContentHash(impact.EvidenceHash).Validate() != nil {
		t.Fatalf("unexpected sample impact: %+v", impact)
	}
}
