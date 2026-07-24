package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrepareInfersLegacyLayersDeterministicallyWithoutRewritingLegacyHashShape(t *testing.T) {
	var legacy map[string]any
	if err := json.Unmarshal(readExample(t), &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy["dataset"].(map[string]any), "layer")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Document.Dataset.Layer != LayerDWS {
		t.Fatalf("legacy aggregate layer=%s, want DWS", prepared.Document.Dataset.Layer)
	}
	if strings.Contains(string(prepared.DSLJSON), `"layer"`) {
		t.Fatalf("legacy DSL hash shape was rewritten: %s", prepared.DSLJSON)
	}
	explicitOverride := prepared.Document
	explicitOverride.Dataset.Layer = LayerDWD
	overrideJSON, err := json.Marshal(explicitOverride)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(overrideJSON), `"layer":"DWD"`) {
		t.Fatalf("programmatic explicit layer override was omitted: %s", overrideJSON)
	}
	second, err := Prepare(prepared.DSLJSON)
	if err != nil {
		t.Fatal(err)
	}
	if second.Document.Dataset.Layer != LayerDWS || second.DSLHash != prepared.DSLHash ||
		string(second.DSLJSON) != string(prepared.DSLJSON) {
		t.Fatalf("legacy inference is not stable: first=%#v second=%#v", prepared, second)
	}

	ods := layerTestODS(t)
	ods.Dataset.Layer = ""
	odsRaw, err := json.Marshal(ods)
	if err != nil {
		t.Fatal(err)
	}
	odsPrepared, err := Prepare(odsRaw)
	if err != nil {
		t.Fatal(err)
	}
	if odsPrepared.Document.Dataset.Layer != LayerODS {
		t.Fatalf("single physical table layer=%s, want ODS", odsPrepared.Document.Dataset.Layer)
	}

	dwd := odsPrepared.Document
	dwd.Dataset.Layer = ""
	secondNode := dwd.Nodes[0]
	secondNode.ID, secondNode.Alias = "customers", "c"
	secondNode.TableID = "33333333-3333-4333-8333-333333333333"
	dwd.Nodes = append(dwd.Nodes, secondNode)
	dwd.Joins = []Join{{
		ID: "orders_customers", LeftNodeID: "node_1", RightNodeID: "customers",
		JoinType: "LEFT", Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
		Conditions: []JoinCondition{{
			LeftExpression: Expression{Type: "FIELD_REF", NodeID: "node_1", Field: "order_id"},
			Operator:       "EQUALS",
			RightExpression: Expression{
				Type: "FIELD_REF", NodeID: "customers", Field: "order_id",
			},
		}},
	}}
	dwdRaw, err := json.Marshal(dwd)
	if err != nil {
		t.Fatal(err)
	}
	dwdPrepared, err := Prepare(dwdRaw)
	if err != nil {
		t.Fatal(err)
	}
	if dwdPrepared.Document.Dataset.Layer != LayerDWD {
		t.Fatalf("joined detail layer=%s, want DWD", dwdPrepared.Document.Dataset.Layer)
	}
}

func TestValidateEnforcesExplicitLayerContracts(t *testing.T) {
	ods := layerTestODS(t)

	t.Run("ODS rejects joins", func(t *testing.T) {
		document := ods
		second := document.Nodes[0]
		second.ID, second.Alias, second.TableID = "customers", "c", "33333333-3333-4333-8333-333333333333"
		document.Nodes = append(document.Nodes, second)
		document.Joins = []Join{{
			ID: "orders_customers", LeftNodeID: "node_1", RightNodeID: "customers",
			JoinType: "INNER", Cardinality: "ONE_TO_ONE", ManualConfirmed: true,
			Conditions: []JoinCondition{{
				LeftExpression:  Expression{Type: "FIELD_REF", NodeID: "node_1", Field: "order_id"},
				Operator:        "EQUALS",
				RightExpression: Expression{Type: "FIELD_REF", NodeID: "customers", Field: "order_id"},
			}},
		}}
		if err := Validate(document); !validationHasReason(err, "ODS 不允许 Join") {
			t.Fatalf("ODS Join error=%v", err)
		}
	})

	t.Run("DWD rejects business aggregation", func(t *testing.T) {
		prepared, err := Prepare(readExample(t))
		if err != nil {
			t.Fatal(err)
		}
		document := prepared.Document
		document.Dataset.Layer = LayerDWD
		if err := Validate(document); !validationHasReason(err, "DWD 必须保持明细粒度") {
			t.Fatalf("DWD aggregate error=%v", err)
		}
	})

	t.Run("DWS requires aggregate and explicit grain", func(t *testing.T) {
		document := ods
		document.Dataset.Layer = LayerDWS
		document.OutputGrain = OutputGrain{}
		err := Validate(document)
		if !validationHasReason(err, "DWS 至少需要一个聚合指标") ||
			!validationHasReason(err, "DWS 必须显式声明输出业务粒度") {
			t.Fatalf("DWS contract error=%v", err)
		}
	})

	t.Run("explicit empty layer is invalid", func(t *testing.T) {
		raw, err := json.Marshal(ods)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		input["dataset"].(map[string]any)["layer"] = ""
		raw, _ = json.Marshal(input)
		if _, err := Prepare(raw); !validationHasReason(err, "必须为 ODS、DWD 或 DWS") {
			t.Fatalf("explicit empty layer error=%v", err)
		}
	})

	t.Run("explicit DWD and DWS reject physical TABLE nodes", func(t *testing.T) {
		odsRaw, err := json.Marshal(ods)
		if err != nil {
			t.Fatal(err)
		}
		var dwd map[string]any
		if err := json.Unmarshal(odsRaw, &dwd); err != nil {
			t.Fatal(err)
		}
		dwd["dataset"].(map[string]any)["layer"] = "DWD"
		dwdRaw, _ := json.Marshal(dwd)
		if _, err := Prepare(dwdRaw); !validationHasReason(err, "显式 DWD 只能引用已发布 ODS") {
			t.Fatalf("explicit DWD TABLE error=%v", err)
		}

		var dws map[string]any
		if err := json.Unmarshal(readExample(t), &dws); err != nil {
			t.Fatal(err)
		}
		dws["dataset"].(map[string]any)["layer"] = "DWS"
		dwsRaw, _ := json.Marshal(dws)
		if _, err := Prepare(dwsRaw); !validationHasReason(err, "显式 DWS 只能引用已发布 DWD") {
			t.Fatalf("explicit DWS TABLE error=%v", err)
		}
	})
}

type layerResolverStub struct {
	layers map[string]Layer
	err    error
}

func (resolver layerResolverStub) ResolveDatasetVersionLayer(_ context.Context, versionID string) (Layer, error) {
	if resolver.err != nil {
		return "", resolver.err
	}
	layer, exists := resolver.layers[versionID]
	if !exists {
		return "", ErrLayerDependencyUnavailable
	}
	return layer, nil
}

func TestValidateLayerDependenciesUsesExactUpstreamVersionLayers(t *testing.T) {
	document := layerTestODS(t)
	document.Dataset.Layer = LayerDWD
	document.Nodes = []Node{{
		ID: "upstream", Type: "DATASET", DatasetVersionID: "version-ods",
		Alias: "o", Projection: []string{"order_id"}, SourceFilters: []SourceFilter{},
	}}
	resolver := layerResolverStub{layers: map[string]Layer{"version-ods": LayerODS}}
	if err := ValidateLayerDependencies(context.Background(), document, resolver); err != nil {
		t.Fatalf("DWD <- ODS rejected: %v", err)
	}

	document.Dataset.Layer = LayerDWS
	if err := ValidateLayerDependencies(context.Background(), document, resolver); !validationHasReason(err, "DWS 只能引用 DWD") {
		t.Fatalf("DWS <- ODS error=%v", err)
	}
	resolver.layers["version-ods"] = LayerDWD
	if err := ValidateLayerDependencies(context.Background(), document, resolver); err != nil {
		t.Fatalf("DWS <- DWD rejected: %v", err)
	}

	if err := ValidateLayerDependencies(context.Background(), document, nil); !errors.Is(err, ErrLayerDependencyUnavailable) {
		t.Fatalf("nil resolver error=%v", err)
	}
}

func TestServiceCreatePersistsInferredLayerAndRejectsOuterMismatch(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store)
	var legacy map[string]any
	if err := json.Unmarshal(readExample(t), &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy["dataset"].(map[string]any), "layer")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{
		Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额",
		Type: "SINGLE_SOURCE", DSL: raw,
	}
	created, err := service.Create(context.Background(), "tenant-1", "actor-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Layer != LayerDWS {
		t.Fatalf("created layer=%s, want DWS", created.Layer)
	}

	input.Layer = LayerDWD
	if _, err := service.Create(context.Background(), "tenant-1", "actor-1", input); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("outer/DSL layer mismatch error=%v", err)
	}
}

func layerTestODS(t *testing.T) Document {
	t.Helper()
	document, err := BuildMappedDatasetDocument(MappedDatasetTable{
		ID:                  "22222222-2222-4222-8222-222222222222",
		DataSourceID:        "11111111-1111-4111-8111-111111111111",
		TableName:           "orders",
		BusinessName:        "订单明细",
		BusinessDescription: "订单源表",
	}, []MappedDatasetColumn{{
		ColumnName: "order_id", BusinessName: "订单编号", CanonicalType: "STRING", PrimaryKey: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return document
}
