package materialization

import (
	"encoding/json"
	"testing"
)

func TestRegisterRequestAcceptsFactlessEntityCountDWS(t *testing.T) {
	request := validWarehouseRegisterRequest(LayerDWS, LayerDIM)
	if err := request.Validate(); err != nil {
		t.Fatalf("DIM-only aggregate DWS should be valid: %v", err)
	}
}

func TestRegisterRequestKeepsDWDODSInputRequirement(t *testing.T) {
	request := validWarehouseRegisterRequest(LayerDWD, LayerDIM)
	request.Plan.Nodes = []PlanNode{
		{
			ID: "extract_001", Kind: NodeExtract, Engine: EnginePostgres,
			InputOrdinals: []int{1},
		},
		{
			ID: "project", Kind: NodeProject, Engine: EnginePostgres,
			DependsOn: []string{"extract_001"},
		},
		{
			ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres,
			DependsOn: []string{"project"},
		},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("DWD without an ODS input should remain invalid")
	}
}

func TestRegisterRequestAcceptsSourceBackedDWS(t *testing.T) {
	request := validSourceBackedRegisterRequest(LayerDWS)
	if err := request.Validate(); err != nil {
		t.Fatalf("source-backed DWS should be valid: %v", err)
	}
}

func TestRegisterRequestRejectsSourceDWSWithWarehouseAggregatePlan(t *testing.T) {
	request := validWarehouseRegisterRequest(LayerDWS, LayerDWD)
	request.Inputs[0] = validSourceBackedRegisterRequest(LayerDWS).Inputs[0]
	if err := request.Validate(); err == nil {
		t.Fatal("source input must require the exact source-backed topology")
	}
}

func validSourceBackedRegisterRequest(targetLayer Layer) RegisterRequest {
	const (
		datasetID        = "11111111-1111-4111-8111-111111111111"
		datasetVersionID = "22222222-2222-4222-8222-222222222222"
		dataSourceID     = "33333333-3333-4333-8333-333333333333"
		sourceVersionID  = "44444444-4444-4444-8444-444444444444"
		metadataTableID  = "55555555-5555-4555-8555-555555555555"
		hash             = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	return RegisterRequest{
		Plan: BuildPlan{
			Version: PlanVersion, DatasetID: datasetID,
			DatasetVersionID: datasetVersionID, Layer: targetLayer, Mode: RunModeFull,
			Target: TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true,
				RelationKind: "TABLE", RefreshMode: string(RunModeFull), StableViewName: true,
			},
			Nodes: []PlanNode{
				{ID: "extract", Kind: NodeExtract, Engine: EngineSourceDB, InputOrdinals: []int{1}},
				{ID: "stage", Kind: NodeStage, Engine: EnginePostgres, DependsOn: []string{"extract"}},
				{ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres, DependsOn: []string{"stage"}},
			},
		},
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputSourceTable, Layer: "SOURCE",
			DataSourceID: dataSourceID, DataSourceVersionID: sourceVersionID,
			MetadataTableID: metadataTableID, SourceVersion: sourceVersionID,
			SchemaHash: hash, SnapshotHash: hash, SnapshotJSON: json.RawMessage(`{}`),
		}},
		MaxAttempts: 3,
	}
}

func validWarehouseRegisterRequest(
	targetLayer, inputLayer Layer,
) RegisterRequest {
	const (
		datasetID        = "11111111-1111-4111-8111-111111111111"
		datasetVersionID = "22222222-2222-4222-8222-222222222222"
		inputDatasetID   = "33333333-3333-4333-8333-333333333333"
		inputVersionID   = "44444444-4444-4444-8444-444444444444"
		hash             = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	return RegisterRequest{
		Plan: BuildPlan{
			Version:   PlanVersion,
			DatasetID: datasetID, DatasetVersionID: datasetVersionID,
			Layer: targetLayer, Mode: RunModeFull,
			Target: TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true,
				RelationKind: "TABLE", RefreshMode: string(RunModeFull),
				StableViewName: true,
			},
			Nodes: []PlanNode{
				{
					ID: "extract_001", Kind: NodeExtract,
					Engine: EnginePostgres, InputOrdinals: []int{1},
				},
				{
					ID: "aggregate", Kind: NodeAggregate,
					Engine: EnginePostgres, DependsOn: []string{"extract_001"},
				},
				{
					ID: "project", Kind: NodeProject,
					Engine: EnginePostgres, DependsOn: []string{"aggregate"},
				},
				{
					ID: "materialize", Kind: NodeMaterialize,
					Engine: EnginePostgres, DependsOn: []string{"project"},
				},
			},
		},
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputDatasetVersion, Layer: string(inputLayer),
			DatasetID: inputDatasetID, DatasetVersionID: inputVersionID,
			SourceVersion: inputVersionID, SchemaHash: hash,
			SnapshotHash: hash, SnapshotJSON: json.RawMessage(`{}`),
		}},
		MaxAttempts: 3,
	}
}
