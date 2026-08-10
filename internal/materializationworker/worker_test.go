package materializationworker

import (
	"context"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/warehouse"
)

const workerTestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type workerStoreStub struct {
	claim      *materialization.Claim
	events     []string
	started    materialization.SnapshotStart
	activation materialization.Activation
}

func (*workerStoreStub) ListTenantIDs(context.Context) ([]string, error) { return nil, nil }

func (store *workerStoreStub) Claim(
	context.Context, string, string, time.Duration,
) (*materialization.Claim, error) {
	claim := store.claim
	store.claim = nil
	return claim, nil
}

func (*workerStoreStub) Heartbeat(
	_ context.Context, claim materialization.Claim, _ time.Duration,
) (materialization.Claim, error) {
	return claim, nil
}

func (store *workerStoreStub) StartNode(
	context.Context, materialization.Claim, string,
) error {
	store.events = append(store.events, "start-node")
	return nil
}

func (store *workerStoreStub) FinishNode(
	context.Context, materialization.Claim, string, materialization.NodeResult,
) error {
	store.events = append(store.events, "finish-node")
	return nil
}

func (store *workerStoreStub) BeginSnapshot(
	_ context.Context,
	claim materialization.Claim,
	start materialization.SnapshotStart,
) (materialization.MaterializationSnapshot, error) {
	store.events = append(store.events, "begin-snapshot")
	store.started = start
	return materialization.MaterializationSnapshot{
		MaterializationID: "99999999-9999-4999-8999-999999999999",
		BuildRunID:        claim.ID,
		SchemaHash:        start.SchemaHash,
		SnapshotVersion:   start.SnapshotVersion,
	}, nil
}

func (*workerStoreStub) Fail(
	context.Context,
	materialization.Claim,
	string,
	string,
	[]materialization.QualityResult,
) error {
	return nil
}

func (store *workerStoreStub) Activate(
	_ context.Context,
	_ materialization.Claim,
	activation materialization.Activation,
) (materialization.Materialization, error) {
	store.events = append(store.events, "activate")
	store.activation = activation
	return materialization.Materialization{ID: "99999999-9999-4999-8999-999999999999"}, nil
}

type workerResolverStub struct{ resolved ResolvedBuild }

func (stub workerResolverStub) Resolve(
	context.Context,
	materialization.Claim,
) (ResolvedBuild, error) {
	return stub.resolved, nil
}

type workerBuilderStub struct {
	events    *[]string
	available time.Time
}

func (stub workerBuilderStub) Build(
	_ context.Context,
	input warehouse.BuildInput,
) (warehouse.BuildResult, error) {
	*stub.events = append(*stub.events, "build")
	schema, table, err := warehouse.PhysicalTarget(
		input.TenantID, input.Layer, input.DatasetID, input.RunID,
	)
	if err != nil {
		return warehouse.BuildResult{}, err
	}
	return warehouse.BuildResult{
		Schema: schema, Table: table, QualifiedName: schema + "." + table,
		RowCount: 10, SizeBytes: 100, DataAvailableThrough: &stub.available,
	}, nil
}

func TestWorkerStartsSnapshotBeforeBuildAndCompletesActivationMetadata(t *testing.T) {
	claim := materialization.Claim{
		Run: materialization.Run{
			ID:               "11111111-1111-4111-8111-111111111111",
			TenantID:         "22222222-2222-4222-8222-222222222222",
			DatasetID:        "33333333-3333-4333-8333-333333333333",
			DatasetVersionID: "44444444-4444-4444-8444-444444444444",
			Layer:            materialization.LayerDWS, Mode: materialization.RunModeFull,
			Status: materialization.RunRunning, InputSnapshotHash: workerTestHash,
			PlanHash: workerTestHash,
		},
		Plan: materialization.BuildPlan{
			Version:          materialization.PlanVersion,
			DatasetID:        "33333333-3333-4333-8333-333333333333",
			DatasetVersionID: "44444444-4444-4444-8444-444444444444",
			Layer:            materialization.LayerDWS, Mode: materialization.RunModeFull,
			Nodes: []materialization.PlanNode{
				{
					ID: "extract", Kind: materialization.NodeExtract,
					Engine: materialization.EnginePostgres, InputOrdinals: []int{1},
				},
				{
					ID: "aggregate", Kind: materialization.NodeAggregate,
					Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
				},
				{
					ID: "materialize", Kind: materialization.NodeMaterialize,
					Engine: materialization.EnginePostgres, DependsOn: []string{"aggregate"},
				},
			},
			Target: materialization.TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(materialization.RunModeFull), StableViewName: true,
			},
		},
		WorkerID: "snapshot-worker", LeaseToken: "55555555-5555-4555-8555-555555555555",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}
	store := &workerStoreStub{claim: &claim}
	available := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	worker := NewWorker(
		store,
		workerResolverStub{resolved: ResolvedBuild{
			Document: dataset.Document{
				OutputGrain:     dataset.OutputGrain{Description: "daily facts"},
				ExecutionPolicy: dataset.ExecutionPolicy{TimeoutMS: 1000},
			},
			SchemaHash: workerTestHash, InputRowCount: map[int]int64{1: 10},
		}},
		workerBuilderStub{events: &store.events, available: available},
	)
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, claim.WorkerID, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected one processed refresh")
	}
	if len(store.events) < 2 || store.events[0] != "begin-snapshot" || store.events[1] != "start-node" {
		t.Fatalf("events = %v", store.events)
	}
	if store.started.SnapshotVersion != claim.ID || store.started.SchemaHash != workerTestHash {
		t.Fatalf("snapshot start = %+v", store.started)
	}
	if store.activation.SnapshotVersion != claim.ID ||
		store.activation.DataAvailableThrough == nil ||
		!store.activation.DataAvailableThrough.Equal(available) {
		t.Fatalf("activation = %+v", store.activation)
	}
}
