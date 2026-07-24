package materializationworker

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/warehouse"
)

const (
	workerTenantID  = "11111111-1111-4111-8111-111111111111"
	workerDatasetID = "22222222-2222-4222-8222-222222222222"
	workerVersionID = "33333333-3333-4333-8333-333333333333"
	workerRunID     = "44444444-4444-4444-8444-444444444444"
	workerLease     = "55555555-5555-4555-8555-555555555555"
	workerHash      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	workerInputHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeStore struct {
	mu              sync.Mutex
	claim           *materialization.Claim
	claimed         bool
	heartbeatErr    error
	heartbeatCount  int
	calls           []string
	failedCode      string
	failedMessage   string
	failedQuality   []materialization.QualityResult
	activation      *materialization.Activation
	activationError error
}

func (store *fakeStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{workerTenantID}, nil
}

func (store *fakeStore) Claim(
	context.Context,
	string,
	string,
	time.Duration,
) (*materialization.Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claimed || store.claim == nil {
		return nil, nil
	}
	store.claimed = true
	copy := *store.claim
	return &copy, nil
}

func (store *fakeStore) Heartbeat(
	_ context.Context,
	claim materialization.Claim,
	lease time.Duration,
) (materialization.Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.heartbeatCount++
	if store.heartbeatErr != nil {
		return materialization.Claim{}, store.heartbeatErr
	}
	claim.LeaseExpiresAt = time.Now().Add(lease)
	return claim, nil
}

func (store *fakeStore) StartNode(
	_ context.Context,
	_ materialization.Claim,
	nodeID string,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls = append(store.calls, "start:"+nodeID)
	return nil
}

func (store *fakeStore) FinishNode(
	_ context.Context,
	_ materialization.Claim,
	nodeID string,
	_ materialization.NodeResult,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls = append(store.calls, "finish:"+nodeID)
	return nil
}

func (store *fakeStore) Fail(
	_ context.Context,
	_ materialization.Claim,
	code, message string,
	quality []materialization.QualityResult,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failedCode = code
	store.failedMessage = message
	store.failedQuality = append([]materialization.QualityResult(nil), quality...)
	return nil
}

func (store *fakeStore) Activate(
	_ context.Context,
	_ materialization.Claim,
	activation materialization.Activation,
) (materialization.Materialization, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	copy := activation
	store.activation = &copy
	return materialization.Materialization{}, store.activationError
}

type resolverFunc func(context.Context, materialization.Claim) (ResolvedBuild, error)

func (function resolverFunc) Resolve(
	ctx context.Context,
	claim materialization.Claim,
) (ResolvedBuild, error) {
	return function(ctx, claim)
}

type builderFunc func(context.Context, warehouse.BuildInput) (warehouse.BuildResult, error)

func (function builderFunc) Build(
	ctx context.Context,
	input warehouse.BuildInput,
) (warehouse.BuildResult, error) {
	return function(ctx, input)
}

func TestWorkerExecutesPlanInTopologyOrderAndActivates(t *testing.T) {
	claim := testDWDClaim()
	store := &fakeStore{claim: &claim}
	resolver := resolverFunc(func(context.Context, materialization.Claim) (ResolvedBuild, error) {
		return testResolvedBuild(), nil
	})
	builder := builderFunc(func(_ context.Context, input warehouse.BuildInput) (warehouse.BuildResult, error) {
		physical, err := materialization.GeneratePhysicalIdentifier(
			input.TenantID, input.DatasetID, input.RunID, materialization.Layer(input.Layer),
		)
		if err != nil {
			return warehouse.BuildResult{}, err
		}
		if input.DatasetVersionID != workerVersionID ||
			!reflect.DeepEqual(input.BusinessKeyCode, []string{"order_id"}) {
			t.Fatalf("unexpected build input: %#v", input)
		}
		return warehouse.BuildResult{
			Schema: physical.Schema, Table: physical.Name,
			QualifiedName: physical.Schema + "." + physical.Name,
			RowCount:      12, SizeBytes: 8192,
		}, nil
	})
	worker := NewWorker(store, resolver, builder)

	processed, err := worker.ProcessNext(
		context.Background(), workerTenantID, "worker-a", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	wantCalls := []string{
		"start:extract", "finish:extract",
		"start:project", "finish:project",
		"start:materialize", "finish:materialize",
	}
	if !reflect.DeepEqual(store.calls, wantCalls) {
		t.Fatalf("calls=%#v want=%#v", store.calls, wantCalls)
	}
	if store.activation == nil ||
		store.activation.RowCount != 12 ||
		store.activation.SizeBytes != 8192 ||
		store.activation.SchemaHash != workerHash ||
		len(store.activation.Quality) != 2 {
		t.Fatalf("activation=%#v", store.activation)
	}
	if store.failedCode != "" {
		t.Fatalf("unexpected failure code %q", store.failedCode)
	}
}

func TestWorkerRecordsQualityFailureWithoutActivation(t *testing.T) {
	claim := testDWDClaim()
	store := &fakeStore{claim: &claim}
	worker := NewWorker(
		store,
		resolverFunc(func(context.Context, materialization.Claim) (ResolvedBuild, error) {
			return testResolvedBuild(), nil
		}),
		builderFunc(func(context.Context, warehouse.BuildInput) (warehouse.BuildResult, error) {
			return warehouse.BuildResult{}, warehouse.ErrQualityFailed
		}),
	)
	processed, err := worker.ProcessNext(
		context.Background(), workerTenantID, "worker-a", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if store.failedCode != CodeQualityGateFailed ||
		len(store.failedQuality) != 1 ||
		store.failedQuality[0].Status != materialization.QualityFailed ||
		store.activation != nil {
		t.Fatalf(
			"code=%q quality=%#v activation=%#v",
			store.failedCode, store.failedQuality, store.activation,
		)
	}
}

func TestWorkerStopsWithoutTerminalMutationWhenHeartbeatLosesLease(t *testing.T) {
	claim := testDWDClaim()
	store := &fakeStore{claim: &claim, heartbeatErr: materialization.ErrLeaseLost}
	worker := NewWorker(
		store,
		resolverFunc(func(ctx context.Context, _ materialization.Claim) (ResolvedBuild, error) {
			<-ctx.Done()
			return ResolvedBuild{}, ctx.Err()
		}),
		builderFunc(func(context.Context, warehouse.BuildInput) (warehouse.BuildResult, error) {
			t.Fatal("builder must not run")
			return warehouse.BuildResult{}, nil
		}),
	)
	worker.heartbeatInterval = func(time.Duration) time.Duration { return time.Millisecond }

	processed, err := worker.ProcessNext(
		context.Background(), workerTenantID, "worker-a", time.Second,
	)
	if !processed || !errors.Is(err, materialization.ErrLeaseLost) {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if store.heartbeatCount == 0 || store.failedCode != "" || store.activation != nil {
		t.Fatalf(
			"heartbeats=%d failed=%q activation=%#v",
			store.heartbeatCount, store.failedCode, store.activation,
		)
	}
}

func TestPostgresResolverFailsClosedForExcelODSWithoutDatabase(t *testing.T) {
	claim := testDWDClaim()
	claim.Layer = materialization.LayerODS
	claim.Plan.Layer = materialization.LayerODS
	claim.Plan.Nodes = []materialization.PlanNode{
		{
			ID: "extract", Kind: materialization.NodeExtract,
			Engine: materialization.EngineSourceDB, InputOrdinals: []int{1},
		},
		{
			ID: "stage", Kind: materialization.NodeStage,
			Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
		},
		{
			ID: "materialize", Kind: materialization.NodeMaterialize,
			Engine: materialization.EnginePostgres, DependsOn: []string{"stage"},
		},
	}
	claim.Inputs = []materialization.InputSnapshot{{
		Ordinal: 1, Type: materialization.InputFileVersion, Layer: "SOURCE",
		FileVersionID: "66666666-6666-4666-8666-666666666666",
		SourceVersion: "file-version:1",
		SchemaHash:    workerHash, SnapshotHash: workerInputHash,
	}}
	_, err := NewPostgresResolver(nil).Resolve(context.Background(), claim)
	var execution *ExecutionError
	if !errors.As(err, &execution) || execution.Code != CodeODSExcelUnsupported {
		t.Fatalf("error=%v", err)
	}
}

func TestPostgresResolverFailsClosedForDatabaseODSWithoutDatabase(t *testing.T) {
	claim := testDWDClaim()
	claim.Layer = materialization.LayerODS
	claim.Plan.Layer = materialization.LayerODS
	claim.Plan.Nodes = []materialization.PlanNode{
		{
			ID: "extract", Kind: materialization.NodeExtract,
			Engine: materialization.EngineSourceDB, InputOrdinals: []int{1},
		},
		{
			ID: "materialize", Kind: materialization.NodeMaterialize,
			Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
		},
	}
	claim.Inputs = []materialization.InputSnapshot{{
		Ordinal: 1, Type: materialization.InputSourceTable, Layer: "SOURCE",
		MetadataTableID: "66666666-6666-4666-8666-666666666666",
		SourceVersion:   "published:1",
		SchemaHash:      workerHash, SnapshotHash: workerInputHash,
	}}
	_, err := NewPostgresResolver(nil).Resolve(context.Background(), claim)
	var execution *ExecutionError
	if !errors.As(err, &execution) || execution.Code != CodeODSSourceStagingNotConfigured {
		t.Fatalf("error=%v", err)
	}
}

func testDWDClaim() materialization.Claim {
	// Intentionally reverse declaration order to exercise stable topological
	// execution rather than relying on caller-provided array order.
	plan := materialization.BuildPlan{
		Version:   materialization.PlanVersion,
		DatasetID: workerDatasetID, DatasetVersionID: workerVersionID,
		Layer: materialization.LayerDWD, Mode: materialization.RunModeFull,
		Nodes: []materialization.PlanNode{
			{
				ID: "materialize", Kind: materialization.NodeMaterialize,
				Engine: materialization.EnginePostgres, DependsOn: []string{"project"},
			},
			{
				ID: "project", Kind: materialization.NodeProject,
				Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
			},
			{
				ID: "extract", Kind: materialization.NodeExtract,
				Engine: materialization.EnginePostgres, InputOrdinals: []int{1},
			},
		},
		Target: materialization.TargetPlan{
			Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
			RefreshMode: string(materialization.RunModeFull), StableViewName: true,
		},
	}
	return materialization.Claim{
		Run: materialization.Run{
			ID: workerRunID, TenantID: workerTenantID,
			DatasetID: workerDatasetID, DatasetVersionID: workerVersionID,
			Layer: materialization.LayerDWD, Mode: materialization.RunModeFull,
			Status:   materialization.RunRunning,
			PlanHash: workerHash, InputSnapshotHash: workerInputHash,
		},
		Plan: plan,
		Inputs: []materialization.InputSnapshot{{
			Ordinal: 1, Type: materialization.InputDatasetVersion,
			Layer:            string(materialization.LayerODS),
			DatasetID:        "77777777-7777-4777-8777-777777777777",
			DatasetVersionID: "88888888-8888-4888-8888-888888888888",
			SourceVersion:    "published:1",
			SchemaHash:       workerHash, SnapshotHash: workerInputHash,
		}},
		WorkerID: "worker-a", LeaseToken: workerLease,
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}
}

func testResolvedBuild() ResolvedBuild {
	return ResolvedBuild{
		Document: dataset.Document{
			Dataset: dataset.Descriptor{Layer: dataset.LayerDWD},
			OutputGrain: dataset.OutputGrain{
				Description: "one row per order", KeyFields: []string{"order_id"},
			},
			ExecutionPolicy: dataset.ExecutionPolicy{TimeoutMS: 30_000},
		},
		Tables: map[string]querycompiler.TableRef{
			"orders": {
				NodeID: "orders", Schema: "warehouse_published",
				Name:    "ods_t000000000000_d000000000000",
				Columns: map[string]bool{"order_id": true},
			},
		},
		SchemaHash: workerHash, VersionNo: 1,
		InputRowCount: map[int]int64{1: 12},
	}
}
