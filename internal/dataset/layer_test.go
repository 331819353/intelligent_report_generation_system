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
		if _, err := Prepare(raw); !validationHasReason(err, "必须为 ODS、DIM、DWD、DWS 或 ADS") {
			t.Fatalf("explicit empty layer error=%v", err)
		}
	})

	t.Run("DIM requires entity grain and ADS accepts non-aggregate DWS composition", func(t *testing.T) {
		dimension := ods
		dimension.Nodes = append([]Node(nil), ods.Nodes...)
		dimension.Dataset.Layer = LayerDIM
		dimension.Nodes[0] = Node{
			ID: "node_1", Type: "DATASET", DatasetVersionID: "version-ods",
			Alias: "o", Projection: []string{"order_id"}, SourceFilters: []SourceFilter{},
		}
		dimension.OutputGrain = OutputGrain{}
		if err := Validate(dimension); !validationHasReason(err, "DIM 必须显式声明实体粒度") {
			t.Fatalf("DIM grain error=%v", err)
		}

		ads := dimension
		ads.Dataset.Layer = LayerADS
		ads.Nodes[0].DatasetVersionID = "version-dws"
		ads.OutputGrain = OutputGrain{
			Description: "每行一个应用交付对象", KeyFields: []string{"order_id"},
		}
		if err := Validate(ads); err != nil {
			t.Fatalf("non-aggregate ADS rejected: %v", err)
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
		if _, err := Prepare(dwdRaw); !validationHasReason(err, "显式 DWD 只能引用已发布 ODS 或 DIM") {
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

func TestPrepareEnforcesDWDRelationshipContracts(t *testing.T) {
	build := func(cardinality, relationshipType, relationshipRole, fanoutPolicy string) []byte {
		t.Helper()
		document := layerTestODS(t)
		document.Dataset.Layer = LayerDWD
		document.Nodes[0] = Node{
			ID: "fact_orders", Type: "DATASET", DatasetVersionID: "version-ods-orders",
			Alias: "orders", Projection: []string{"order_id"}, SourceFilters: []SourceFilter{},
		}
		document.Nodes = append(document.Nodes, Node{
			ID: "dim_customer", Type: "DATASET", DatasetVersionID: "version-dim-customer",
			Alias: "customer", Projection: []string{"order_id", "relationship_type"}, SourceFilters: []SourceFilter{},
		})
		document.Fields[0].Expression.NodeID = "fact_orders"
		join := Join{
			ID: "orders_customer", LeftNodeID: "fact_orders", RightNodeID: "dim_customer",
			JoinType: "LEFT", Cardinality: cardinality, RelationshipType: relationshipType,
			RelationshipRole: relationshipRole, FanoutPolicy: fanoutPolicy, ManualConfirmed: true,
			Conditions: []JoinCondition{{
				LeftExpression:  Expression{Type: "FIELD_REF", NodeID: "fact_orders", Field: "order_id"},
				Operator:        "EQUALS",
				RightExpression: Expression{Type: "FIELD_REF", NodeID: "dim_customer", Field: "order_id"},
			}},
		}
		if relationshipType == "BRIDGE" {
			join.Bridge = &BridgeContract{
				BridgeNodeID:          "dim_customer",
				RelationshipTypeField: "relationship_type",
			}
		}
		document.Joins = []Join{join}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	t.Run("multiple ordinary dimensions use independent safe joins", func(t *testing.T) {
		prepared, err := Prepare(build("MANY_TO_ONE", "ROLE_PLAYING", "ordering_user", "SAFE"))
		if err != nil {
			t.Fatalf("safe role-playing dimension rejected: %v", err)
		}
		if prepared.Document.Joins[0].RelationshipRole != "ORDERING_USER" {
			t.Fatalf("relationship role was not normalized: %#v", prepared.Document.Joins[0])
		}
	})

	t.Run("unknown cardinality is not proof for a new DWD", func(t *testing.T) {
		if _, err := Prepare(build("UNKNOWN", "DIRECT", "", "SAFE")); !validationHasReason(err, "不能使用 UNKNOWN") {
			t.Fatalf("unknown cardinality error=%v", err)
		}
	})

	t.Run("multi-valued relation requires bridge and fanout policy", func(t *testing.T) {
		if _, err := Prepare(build("ONE_TO_MANY", "DIRECT", "", "SAFE")); !validationHasReason(err, "必须显式建模为 BRIDGE") {
			t.Fatalf("direct one-to-many error=%v", err)
		}
		if _, err := Prepare(build("MANY_TO_MANY", "BRIDGE", "product_category", "")); !validationHasReason(err, "必须声明指标扇出处理策略") {
			t.Fatalf("bridge without fanout policy error=%v", err)
		}
		prepared, err := Prepare(build("MANY_TO_MANY", "BRIDGE", "product_category", "DEDUPLICATE"))
		if err != nil {
			t.Fatalf("governed bridge rejected: %v", err)
		}
		if prepared.Document.Joins[0].FanoutPolicy != "DEDUPLICATE" {
			t.Fatalf("fanout policy=%s", prepared.Document.Joins[0].FanoutPolicy)
		}
	})
}

func TestSemanticContractV1RequiresFactAndAnalysisProofs(t *testing.T) {
	t.Run("DWD fact contract covers grain time and measures", func(t *testing.T) {
		document := layerTestODS(t)
		document.Dataset.Layer = LayerDWD
		document.Dataset.SemanticContractVersion = "1.0"
		document.Nodes[0] = Node{
			ID: "fact_orders", Type: "DATASET", DatasetVersionID: "version-ods",
			Alias: "orders", Projection: []string{"order_id", "event_time", "amount"},
			SourceFilters: []SourceFilter{},
		}
		document.Fields = append(document.Fields,
			Field{
				ID: "field_event_time", Code: "event_time", Name: "事件时间", Role: "TIME",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "fact_orders", Field: "event_time",
				},
				CanonicalType: "DATETIME", Nullable: false,
			},
			Field{
				ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE",
				Expression: Expression{
					Type: "FIELD_REF", NodeID: "fact_orders", Field: "amount",
				},
				CanonicalType: "DECIMAL", Nullable: false,
			},
		)
		document.Fields[0].Expression.NodeID = "fact_orders"
		document.OutputGrain.TimeField = "event_time"
		document.OutputGrain.DefaultTimeGrain = "DAY"
		document.FactContract = &FactContract{
			BusinessAction: "用户购买商品",
			GrainKeyFields: append([]string(nil), document.OutputGrain.KeyFields...),
			EventTimeField: "event_time",
			AtomicMeasures: []AtomicMeasureContract{{
				Field: "amount", Additivity: "ADDITIVE", Unit: "CNY",
				NullPolicy: "PRESERVE",
			}},
		}
		if err := Validate(document); err != nil {
			t.Fatalf("valid fact contract rejected: %v", err)
		}
		document.FactContract.AtomicMeasures = nil
		if err := Validate(document); !validationHasReason(err, "必须覆盖每一个 MEASURE") {
			t.Fatalf("missing measure proof error=%v", err)
		}
	})

	t.Run("DWS analysis contract fixes intent and common grain", func(t *testing.T) {
		prepared, err := Prepare(readExample(t))
		if err != nil {
			t.Fatal(err)
		}
		document := prepared.Document
		document.Dataset.Layer = LayerDWS
		document.Dataset.SemanticContractVersion = "1.0"
		document.Nodes[0] = Node{
			ID: "orders", Type: "DATASET", DatasetVersionID: "version-dwd-orders",
			Alias: "o", Projection: []string{"order_date", "order_amount", "order_status"},
			SourceFilters: []SourceFilter{},
		}
		document.AnalysisContract = &AnalysisContract{
			Intent: "TREND", InputMode: "SINGLE_FACT",
			CommonGrainFields:   append([]string(nil), document.OutputGrain.KeyFields...),
			ConformedDimensions: []string{"stat_month"},
			TimeField:           "stat_month", TimeGrain: "MONTH",
			Measures: []AnalysisMeasureContract{{
				Field: "revenue", SourceNodeIDs: []string{"orders"},
				Aggregation: "SUM", Additivity: "ADDITIVE", Unit: "CNY",
			}},
		}
		if err := Validate(document); err != nil {
			t.Fatalf("valid analysis contract rejected: %v", err)
		}
		document.AnalysisContract.InputMode = "MULTI_FACT"
		if err := Validate(document); !validationHasReason(err, "必须与 DWD 输入数量一致") {
			t.Fatalf("input mode error=%v", err)
		}
	})
}

func TestTemporalJoinContractRequiresCompleteSCD2Predicate(t *testing.T) {
	join := Join{
		Temporal: &TemporalJoinContract{
			EventNodeID: "fact", EventTimeField: "occurred_at",
			ValidityNodeID: "dim", ValidFromField: "valid_from",
			ValidToField: "valid_to",
		},
		Conditions: []JoinCondition{
			{
				LeftExpression: Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "occurred_at",
				},
				Operator: "GTE",
				RightExpression: Expression{
					Type: "FIELD_REF", NodeID: "dim", Field: "valid_from",
				},
			},
			{
				LeftExpression: Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "occurred_at",
				},
				Operator: "LT",
				RightExpression: Expression{
					Type: "FIELD_REF", NodeID: "dim", Field: "valid_to",
				},
			},
		},
	}
	if !temporalJoinConditionsPresent(join) {
		t.Fatal("complete half-open SCD2 interval was not recognized")
	}
	join.Conditions = join.Conditions[:1]
	if temporalJoinConditionsPresent(join) {
		t.Fatal("incomplete SCD2 interval was accepted")
	}
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
	factNode := document.Nodes[0]
	document.Nodes = append(document.Nodes, Node{
		ID: "dimension", Type: "DATASET", DatasetVersionID: "version-dim",
		Alias: "d", Projection: []string{"order_id"}, SourceFilters: []SourceFilter{},
	})
	resolver.layers["version-dim"] = LayerDIM
	if err := ValidateLayerDependencies(context.Background(), document, resolver); err != nil {
		t.Fatalf("DWD <- ODS + DIM rejected: %v", err)
	}
	document.Nodes = document.Nodes[1:]
	if err := ValidateLayerDependencies(context.Background(), document, resolver); !validationHasReason(err, "DWD 至少需要一个") {
		t.Fatalf("DWD with DIM only error=%v", err)
	}

	document.Dataset.Layer = LayerDWS
	document.Nodes = []Node{factNode}
	if err := ValidateLayerDependencies(context.Background(), document, resolver); !validationHasReason(err, "DWS 只能引用 DWD") {
		t.Fatalf("DWS <- ODS error=%v", err)
	}
	resolver.layers["version-ods"] = LayerDWD
	if err := ValidateLayerDependencies(context.Background(), document, resolver); err != nil {
		t.Fatalf("DWS <- DWD rejected: %v", err)
	}

	document.Nodes = append(document.Nodes, Node{
		ID: "dimension", Type: "DATASET", DatasetVersionID: "version-dim",
		Alias: "d", Projection: []string{"order_id"}, SourceFilters: []SourceFilter{},
	})
	if err := ValidateLayerDependencies(context.Background(), document, resolver); !validationHasReason(err, "DWS 只能引用 DWD") {
		t.Fatalf("DWS with DIM input error=%v", err)
	}

	document.Dataset.Layer = LayerADS
	document.Nodes = document.Nodes[:1]
	document.Nodes[0].DatasetVersionID = "version-dws"
	resolver.layers["version-dws"] = LayerDWS
	if err := ValidateLayerDependencies(context.Background(), document, resolver); err != nil {
		t.Fatalf("ADS <- DWS rejected: %v", err)
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
