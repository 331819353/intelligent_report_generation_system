package materialization

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

const (
	controlTestTenantID  = "11111111-1111-4111-8111-111111111111"
	controlTestActorID   = "22222222-2222-4222-8222-222222222222"
	controlTestDatasetID = "33333333-3333-4333-8333-333333333333"
	controlTestVersionID = "44444444-4444-4444-8444-444444444444"
	controlTestBuildID   = "55555555-5555-4555-8555-555555555555"
)

type fakeControlStore struct {
	request       RegisterCurrentRequest
	tenantID      string
	actorID       string
	datasetID     string
	buildID       string
	registerCalls int
	listCalls     int
	getCalls      int
	cancelCalls   int
	created       bool
	err           error
	run           Run
	detail        BuildDetail
}

func (store *fakeControlStore) RegisterCurrent(
	_ context.Context,
	tenantID, actorID, datasetID string,
	request RegisterCurrentRequest,
) (Run, bool, error) {
	store.registerCalls++
	store.tenantID, store.actorID, store.datasetID = tenantID, actorID, datasetID
	store.request = request
	if store.err != nil {
		return Run{}, false, store.err
	}
	return store.run, store.created, nil
}

func (store *fakeControlStore) ListBuilds(
	_ context.Context,
	tenantID, datasetID string,
	_, _ int,
) ([]Run, int, error) {
	store.listCalls++
	store.tenantID, store.datasetID = tenantID, datasetID
	if store.err != nil {
		return nil, 0, store.err
	}
	return []Run{store.run}, 1, nil
}

func (store *fakeControlStore) GetBuild(
	_ context.Context,
	tenantID, datasetID, buildID string,
) (BuildDetail, error) {
	store.getCalls++
	store.tenantID, store.datasetID, store.buildID = tenantID, datasetID, buildID
	if store.err != nil {
		return BuildDetail{}, store.err
	}
	return store.detail, nil
}

func (store *fakeControlStore) CancelQueued(
	_ context.Context,
	tenantID, actorID, datasetID, buildID string,
) (Run, error) {
	store.cancelCalls++
	store.tenantID, store.actorID = tenantID, actorID
	store.datasetID, store.buildID = datasetID, buildID
	if store.err != nil {
		return Run{}, store.err
	}
	run := store.run
	run.Status = RunCancelled
	return run, nil
}

func testControlStore() *fakeControlStore {
	run := Run{
		ID: controlTestBuildID, TenantID: controlTestTenantID,
		DatasetID: controlTestDatasetID, DatasetVersionID: controlTestVersionID,
		Layer: LayerDWD, Mode: RunModeFull, Status: RunQueued,
	}
	return &fakeControlStore{
		run: run, created: true,
		detail: BuildDetail{Build: buildFromRun(run), Inputs: []BuildInput{}, Nodes: []BuildNode{}},
	}
}

func TestControlServiceDefaultsAndForwardsOnlyControlFields(t *testing.T) {
	store := testControlStore()
	service := NewControlService(store)
	result, created, err := service.Register(
		context.Background(), controlTestTenantID, controlTestActorID,
		controlTestDatasetID, CreateBuildInput{},
	)
	if err != nil || !created {
		t.Fatalf("Register() created=%v err=%v", created, err)
	}
	if result.ID != controlTestBuildID ||
		store.request.Mode != RunModeFull ||
		store.request.PartitionKey != "" ||
		store.request.MaxAttempts != 3 {
		t.Fatalf("result=%+v request=%+v", result.Build, store.request)
	}
	if store.registerCalls != 1 || store.getCalls != 1 ||
		store.tenantID != controlTestTenantID ||
		store.actorID != controlTestActorID ||
		store.datasetID != controlTestDatasetID {
		t.Fatalf("store=%+v", store)
	}
}

func TestControlServiceRejectsUnsupportedControlValues(t *testing.T) {
	tests := []CreateBuildInput{
		{Mode: RunModeIncremental},
		{Mode: RunModeBackfill},
		{PartitionKey: "2026-07"},
		{PartitionKey: " "},
		{MaxAttempts: intPointer(0)},
		{MaxAttempts: intPointer(11)},
	}
	for _, input := range tests {
		store := testControlStore()
		_, _, err := NewControlService(store).Register(
			context.Background(), controlTestTenantID, controlTestActorID,
			controlTestDatasetID, input,
		)
		if !errors.Is(err, ErrInvalidRequest) || store.registerCalls != 0 {
			t.Fatalf("input=%+v calls=%d err=%v", input, store.registerCalls, err)
		}
	}
}

func TestControlServiceCancelLoadsUpdatedDetail(t *testing.T) {
	store := testControlStore()
	store.detail.Status = RunCancelled
	result, err := NewControlService(store).Cancel(
		context.Background(), controlTestTenantID, controlTestActorID,
		controlTestDatasetID, controlTestBuildID,
	)
	if err != nil || result.Status != RunCancelled {
		t.Fatalf("Cancel() result=%+v err=%v", result.Build, err)
	}
	if store.cancelCalls != 1 || store.getCalls != 1 ||
		store.buildID != controlTestBuildID {
		t.Fatalf("store=%+v", store)
	}
}

func intPointer(value int) *int {
	return &value
}

func datasetDocument(
	layer dataset.Layer,
	nodes []dataset.Node,
	joined bool,
) dataset.Document {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Layer: layer},
		Nodes:   nodes,
	}
	if joined {
		document.Joins = []dataset.Join{{ID: "joined"}}
	}
	return document
}

func TestDeriveBuildPlanUsesServerOwnedTopology(t *testing.T) {
	dwsTarget := publishedBuildTarget{
		DatasetID: controlTestDatasetID, VersionID: controlTestVersionID,
		Layer: LayerDWS,
		Document: datasetDocument(
			dataset.LayerDWS,
			[]dataset.Node{
				{ID: "left", Type: "DATASET"},
				{ID: "right", Type: "DATASET"},
			},
			true,
		),
	}
	plan, err := deriveBuildPlan(dwsTarget, RunModeFull, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("derived plan invalid: %+v err=%v", plan, err)
	}
	if plan.DatasetID != controlTestDatasetID ||
		plan.DatasetVersionID != controlTestVersionID ||
		plan.Target.Storage != "POSTGRES" {
		t.Fatalf("plan identity=%+v", plan)
	}
	hasAggregate := false
	for _, node := range plan.Nodes {
		if node.Engine != EnginePostgres {
			t.Fatalf("DWS node escaped PostgreSQL: %+v", node)
		}
		hasAggregate = hasAggregate || node.Kind == NodeAggregate
	}
	if !hasAggregate {
		t.Fatalf("DWS plan has no aggregate: %+v", plan.Nodes)
	}

	odsTarget := publishedBuildTarget{
		DatasetID: controlTestDatasetID, VersionID: controlTestVersionID,
		Layer: LayerODS,
		Document: datasetDocument(
			dataset.LayerODS,
			[]dataset.Node{{ID: "source", Type: "TABLE"}},
			false,
		),
	}
	plan, err = deriveBuildPlan(odsTarget, RunModeFull, []int{1})
	if err != nil || len(plan.Nodes) != 3 ||
		plan.Nodes[0].Engine != EngineSourceDB ||
		plan.Nodes[1].Kind != NodeStage ||
		plan.Nodes[2].Kind != NodeMaterialize {
		t.Fatalf("ODS plan=%+v err=%v", plan, err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("derived ODS plan invalid: %v", err)
	}
}
