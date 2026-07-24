package materialization

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testTenantID          = "11111111-1111-4111-8111-111111111111"
	testDatasetID         = "22222222-2222-4222-8222-222222222222"
	testDatasetVersionID  = "33333333-3333-4333-8333-333333333333"
	testRunID             = "44444444-4444-4444-8444-444444444444"
	testSourceTableID     = "55555555-5555-4555-8555-555555555555"
	testDataSourceID      = "aaaaaaaa-1111-4111-8111-111111111111"
	testSourceVersionID   = "aaaaaaaa-2222-4222-8222-222222222222"
	testInputDatasetID    = "66666666-6666-4666-8666-666666666666"
	testInputVersionID    = "77777777-7777-4777-8777-777777777777"
	testSecondDatasetID   = "88888888-8888-4888-8888-888888888888"
	testSecondVersionID   = "99999999-9999-4999-8999-999999999999"
	testSchemaHash        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSnapshotHash      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testOtherSnapshotHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestPrepareIsCanonicalAndDerivesIdempotencyKey(t *testing.T) {
	first := validODSRequest()
	first.Inputs[0].SnapshotJSON = json.RawMessage(`{"watermark":{"to":20,"from":10},"partition":"2026-07"}`)
	second := validODSRequest()
	second.Inputs[0].SnapshotJSON = json.RawMessage(`{
		"partition": "2026-07",
		"watermark": {"from": 10, "to": 20}
	}`)

	preparedFirst, err := Prepare(first)
	if err != nil {
		t.Fatalf("prepare first request: %v", err)
	}
	preparedSecond, err := Prepare(second)
	if err != nil {
		t.Fatalf("prepare second request: %v", err)
	}
	if preparedFirst.PlanHash != preparedSecond.PlanHash ||
		preparedFirst.InputSnapshotHash != preparedSecond.InputSnapshotHash ||
		preparedFirst.RequestHash != preparedSecond.RequestHash ||
		preparedFirst.IdempotencyKey != preparedSecond.IdempotencyKey {
		t.Fatalf("semantically equal requests produced different identities:\n%+v\n%+v", preparedFirst, preparedSecond)
	}
	if len(preparedFirst.IdempotencyKey) != 64 {
		t.Fatalf("idempotency key is not a sha256 digest: %q", preparedFirst.IdempotencyKey)
	}

	changed := validODSRequest()
	changed.Inputs[0].SnapshotHash = testOtherSnapshotHash
	preparedChanged, err := Prepare(changed)
	if err != nil {
		t.Fatalf("prepare changed request: %v", err)
	}
	if preparedChanged.IdempotencyKey == preparedFirst.IdempotencyKey {
		t.Fatal("a changed frozen input reused the prior idempotency key")
	}
}

func TestPrepareSortsInputSnapshotsByOrdinal(t *testing.T) {
	request := validDWDRequest()
	request.Inputs[0], request.Inputs[1] = request.Inputs[1], request.Inputs[0]
	prepared, err := Prepare(request)
	if err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	if prepared.Inputs[0].Ordinal != 1 || prepared.Inputs[1].Ordinal != 2 {
		t.Fatalf("inputs were not canonicalized: %+v", prepared.Inputs)
	}
}

func TestValidationRejectsExecutableOrSensitiveSnapshotPayload(t *testing.T) {
	cases := []string{
		`{"watermark":{"rawSql":"select * from secret"}}`,
		`{"credentials":{"token":"x"}}`,
		`{"sampleRows":[{"id":1}]}`,
	}
	for _, raw := range cases {
		request := validODSRequest()
		request.Inputs[0].SnapshotJSON = json.RawMessage(raw)
		if _, err := Prepare(request); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("payload %s was not rejected: %v", raw, err)
		}
	}
}

func TestSourceInputRequiresExactDataSourceVersionIdentity(t *testing.T) {
	request := validODSRequest()
	request.Inputs[0].DataSourceVersionID = ""
	if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing data source version error=%v", err)
	}

	request = validODSRequest()
	request.Inputs[0].DataSourceID = ""
	if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing data source identity error=%v", err)
	}

	request = validDWDRequest()
	request.Inputs[0].DataSourceID = testDataSourceID
	request.Inputs[0].DataSourceVersionID = testSourceVersionID
	if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("data source identity on dataset input error=%v", err)
	}
}

func TestLayerPlanRulesAreEnforced(t *testing.T) {
	t.Run("ODS cannot join", func(t *testing.T) {
		request := validODSRequest()
		request.Plan.Nodes[1].Kind = NodeJoin
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected invalid ODS join, got %v", err)
		}
	})

	t.Run("DWD cannot aggregate", func(t *testing.T) {
		request := validDWDRequest()
		request.Plan.Nodes[1].Kind = NodeAggregate
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected invalid DWD aggregate, got %v", err)
		}
	})

	t.Run("DWS requires PostgreSQL aggregation", func(t *testing.T) {
		request := validDWSRequest()
		if err := request.Validate(); err != nil {
			t.Fatalf("valid DWS request failed: %v", err)
		}
		request.Plan.Nodes[0].Engine = EngineSourceDB
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected source execution rejection, got %v", err)
		}
	})

	t.Run("DWS only accepts DWD inputs", func(t *testing.T) {
		request := validDWSRequest()
		request.Inputs[0].Layer = string(LayerODS)
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected wrong input layer rejection, got %v", err)
		}
	})
}

func TestPlanRejectsCyclesAndOrphanNodes(t *testing.T) {
	cyclic := validDWDRequest()
	cyclic.Plan.Nodes[0].Kind = NodeProject
	cyclic.Plan.Nodes[0].InputOrdinals = nil
	cyclic.Plan.Nodes[0].DependsOn = []string{"join"}
	if err := cyclic.Plan.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected cycle rejection, got %v", err)
	}

	orphan := validDWDRequest()
	orphan.Plan.Nodes = append(orphan.Plan.Nodes, PlanNode{
		ID: "orphan", Kind: NodeExtract, Engine: EnginePostgres, InputOrdinals: []int{1},
	})
	if err := orphan.Plan.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected orphan rejection, got %v", err)
	}
}

func TestDecodePlanUsesCanonicalRepresentation(t *testing.T) {
	prepared, err := Prepare(validODSRequest())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(prepared.PlanJSON, &generic); err != nil {
		t.Fatalf("decode generated plan: %v", err)
	}
	jsonbStyle, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatalf("marshal jsonb-style plan: %v", err)
	}
	decoded, err := decodePlan(jsonbStyle, prepared.PlanHash)
	if err != nil {
		t.Fatalf("canonical plan was rejected: %v", err)
	}
	if decoded.DatasetVersionID != testDatasetVersionID {
		t.Fatalf("wrong plan decoded: %+v", decoded)
	}

	generic["rawSql"] = "select 1"
	unknown, _ := json.Marshal(generic)
	if _, err := decodePlan(unknown, prepared.PlanHash); !errors.Is(err, ErrCorruptPlan) {
		t.Fatalf("unknown executable field was not rejected: %v", err)
	}
}

func TestStateMachinesAreClosedAfterTerminalState(t *testing.T) {
	allowed := [][2]RunStatus{
		{RunQueued, RunRunning},
		{RunQueued, RunCancelled},
		{RunRunning, RunRunning},
		{RunRunning, RunSucceeded},
		{RunRunning, RunFailed},
		{RunRunning, RunCancelled},
	}
	for _, transition := range allowed {
		if !CanTransition(transition[0], transition[1]) {
			t.Errorf("expected %s -> %s to be allowed", transition[0], transition[1])
		}
	}
	for _, terminal := range []RunStatus{RunSucceeded, RunFailed, RunCancelled} {
		for _, next := range []RunStatus{RunQueued, RunRunning, RunSucceeded, RunFailed, RunCancelled} {
			if CanTransition(terminal, next) {
				t.Errorf("terminal transition %s -> %s was allowed", terminal, next)
			}
		}
	}
	if !CanTransitionNode(NodePending, NodeRunning) ||
		!CanTransitionNode(NodeRunning, NodeSucceeded) ||
		CanTransitionNode(NodeSucceeded, NodeRunning) {
		t.Fatal("node transition contract is incorrect")
	}
}

func TestPhysicalIdentifiersAreGeneratedAndFenced(t *testing.T) {
	identifier, err := GeneratePhysicalIdentifier(
		testTenantID, testDatasetID, testRunID, LayerDWD,
	)
	if err != nil {
		t.Fatalf("generate physical identifier: %v", err)
	}
	if identifier.Schema != "warehouse_dwd" ||
		identifier.PublishedSchema != "warehouse_published" ||
		len(identifier.Name) > 63 || len(identifier.PublishedName) > 63 ||
		strings.Contains(identifier.Name, testTenantID) {
		t.Fatalf("unsafe identifier: %+v", identifier)
	}
	if err := ValidatePhysicalIdentifier(
		identifier, testTenantID, testDatasetID, testRunID, LayerDWD,
	); err != nil {
		t.Fatalf("generated identifier failed validation: %v", err)
	}
	tampered := identifier
	tampered.Schema = "platform"
	if err := ValidatePhysicalIdentifier(
		tampered, testTenantID, testDatasetID, testRunID, LayerDWD,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("tampered schema was accepted: %v", err)
	}

	schema, staging, err := GenerateStagingIdentifier(testTenantID, testRunID, "extract_orders")
	if err != nil {
		t.Fatalf("generate staging identifier: %v", err)
	}
	if schema != "warehouse_staging" || len(staging) > 63 || !physicalNamePattern.MatchString(staging) {
		t.Fatalf("unsafe staging identifier: %s.%s", schema, staging)
	}
	if _, _, err := GenerateStagingIdentifier(testTenantID, testRunID, `x";drop table`); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe node ID was accepted: %v", err)
	}
}

func TestPublicationSwapNamesAreSafeAndRunScoped(t *testing.T) {
	identifier, err := GeneratePhysicalIdentifier(
		testTenantID,
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		testRunID,
		LayerDWD,
	)
	if err != nil {
		t.Fatal(err)
	}
	next, retired, err := publicationSwapNames(
		identifier,
		testRunID,
		"dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !physicalNamePattern.MatchString(next) || !physicalNamePattern.MatchString(retired) ||
		len(next) > 63 || len(retired) > 63 || next == retired {
		t.Fatalf("next=%q retired=%q", next, retired)
	}
	if _, _, err := publicationSwapNames(identifier, "unsafe", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
}

func validODSRequest() RegisterRequest {
	return RegisterRequest{
		Plan: BuildPlan{
			Version: PlanVersion, DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			Layer: LayerODS, Mode: RunModeFull,
			Nodes: []PlanNode{
				{ID: "extract", Kind: NodeExtract, Engine: EngineSourceDB, InputOrdinals: []int{1}},
				{ID: "stage", Kind: NodeStage, Engine: EnginePostgres, DependsOn: []string{"extract"}},
				{ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres, DependsOn: []string{"stage"}},
			},
			Target: TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(RunModeFull), StableViewName: true,
			},
		},
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputSourceTable, Layer: "SOURCE",
			DataSourceID: testDataSourceID, DataSourceVersionID: testSourceVersionID,
			MetadataTableID: testSourceTableID, SourceVersion: "metadata-version:7",
			SchemaHash: testSchemaHash, SnapshotHash: testSnapshotHash,
			SnapshotJSON: json.RawMessage(`{"watermark":"full"}`),
		}},
		MaxAttempts: 3,
	}
}

func validDWDRequest() RegisterRequest {
	return RegisterRequest{
		Plan: BuildPlan{
			Version: PlanVersion, DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			Layer: LayerDWD, Mode: RunModeFull,
			Nodes: []PlanNode{
				{ID: "extract", Kind: NodeExtract, Engine: EnginePostgres, InputOrdinals: []int{1, 2}},
				{ID: "join", Kind: NodeJoin, Engine: EnginePostgres, DependsOn: []string{"extract"}},
				{ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres, DependsOn: []string{"join"}},
			},
			Target: TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(RunModeFull), StableViewName: true,
			},
		},
		Inputs: []InputSnapshot{
			{
				Ordinal: 1, Type: InputDatasetVersion, Layer: string(LayerODS),
				DatasetID: testInputDatasetID, DatasetVersionID: testInputVersionID,
				SourceVersion: "dataset-version:1", SchemaHash: testSchemaHash,
				SnapshotHash: testSnapshotHash, SnapshotJSON: json.RawMessage(`{}`),
			},
			{
				Ordinal: 2, Type: InputDatasetVersion, Layer: string(LayerODS),
				DatasetID: testSecondDatasetID, DatasetVersionID: testSecondVersionID,
				SourceVersion: "dataset-version:2", SchemaHash: testSchemaHash,
				SnapshotHash: testOtherSnapshotHash, SnapshotJSON: json.RawMessage(`{}`),
			},
		},
		MaxAttempts: 3,
	}
}

func validDWSRequest() RegisterRequest {
	request := validDWDRequest()
	request.Plan.Layer = LayerDWS
	request.Plan.Nodes[1].Kind = NodeAggregate
	for index := range request.Inputs {
		request.Inputs[index].Layer = string(LayerDWD)
	}
	return request
}
