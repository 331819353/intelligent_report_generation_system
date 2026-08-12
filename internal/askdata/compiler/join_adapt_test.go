package compiler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// joinedFixture 建一个最常见的跨模型问题：指标在事实模型上，分组维度在另一张
// 维表上，两者由一条已认证的多对一关系连接。
func joinedFixture(t *testing.T, cardinality registry.Cardinality, fanout registry.FanoutPolicy) (ir.SemanticIR, Resolution) {
	t.Helper()
	semanticIR, resolution := baseAggregationFixture(t)
	resolution.Metrics = []MetricContract{{
		MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1",
		FormulaAST:       json.RawMessage(`{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"}`),
		DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), Unit: "CNY",
		Additivity:            registry.Additive,
		ZeroDenominatorPolicy: registry.ZeroDenominatorNull, NullPolicy: "PRESERVE",
		Measures: []MeasureContract{{
			MeasureID: "measure-sales", MeasureVersionID: "measure-sales-v1",
			FormulaAST:  json.RawMessage(`{"fieldId":"net_sales","type":"FIELD_REF"}`),
			Aggregation: registry.AggregationSum, Additivity: registry.Additive,
			DataType: registry.NumericDecimal, Unit: "CNY",
			ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
		}},
	}}
	semanticIR.Metrics = []ir.Metric{{MetricVersionID: "metric-sales-v1", Alias: "net_sales_total"}}

	customerKey := resolvedField(t, "customer_id", "customer_id", "DIMENSION", "STRING")
	customerTier := resolvedField(t, "customer_tier", "customer_tier", "DIMENSION", "STRING")
	resolution.JoinedModels = []ModelContract{{
		ModelVersionID: "model-customer-v1", ContentHash: hash("model-customer-v1"),
		DatasetSchemaHash: hash("customer-schema-v1"),
		GrainContract:     json.RawMessage(`{"keys":["customer_id"],"type":"ENTITY"}`),
		Fields:            []FieldContract{customerKey, customerTier},
		Materialization: MaterializationContract{
			MaterializationID: "materialization-customer-v1", DatasetID: "dataset-customer",
			DatasetVersionID: "dataset-customer-v1", Layer: "DWS", Status: "ACTIVE",
			PublishedSchema: "warehouse_published", PublishedName: "dws_customers",
			SchemaHash: hash("customer-schema-v1"), SnapshotHash: hash("customer-snapshot-v1"),
			RowCount: 10,
		},
	}}
	resolution.Dimensions = append(resolution.Dimensions, DimensionContract{
		DimensionVersionID: "dimension-tier-v1", ModelVersionID: "model-customer-v1",
		LogicalFieldID: "customer_tier", ContentHash: hash("dimension-tier-v1"),
		Kind: registry.DimensionCategorical, Sensitivity: registry.SensitivityInternal,
		MemberIndexPolicy: registry.MemberIndexNone,
	})
	resolution.Relationships = []RelationshipContract{{
		RelationshipVersionID: "relationship-customer-v1", ContentHash: hash("relationship-customer-v1"),
		LeftModelVersionID: "model-sales-v1", RightModelVersionID: "model-customer-v1",
		JoinAST: json.RawMessage(
			`{"type":"EQ","leftFieldId":"customer_id","rightFieldId":"customer_id"}`,
		),
		JoinType: registry.JoinLeft, Cardinality: cardinality, FanoutPolicy: fanout,
	}}
	resolution.GraphPath = &graph.JoinPath{
		Steps: []graph.JoinStep{{
			Hop: 1, RelationshipVersionID: "relationship-customer-v1",
			FromModelVersionID: "model-sales-v1", ToModelVersionID: "model-customer-v1",
		}},
	}
	semanticIR.GroupBy = []ir.GroupBy{{DimensionVersionID: "dimension-tier-v1"}}
	return semanticIR, resolution
}

// 跨模型问题必须真正编译成带 JOIN 的 SQL，并且维度列要来自被连接的那张表。
// 这是「图谱解析出的连接路径」第一次真正落到执行计划里。
func TestCrossModelQuestionCompilesToAJoinedQuery(t *testing.T) {
	semanticIR, resolution := joinedFixture(t, registry.CardinalityManyToOne, registry.FanoutSafe)
	sql := compileAggregationDocument(t, semanticIR, resolution)
	if !strings.Contains(sql, "LEFT JOIN") {
		t.Fatalf("cross-model query did not compile a join: %s", sql)
	}
	if !strings.Contains(sql, `"warehouse_published"."dws_customers"`) {
		t.Fatalf("joined table is missing from the query: %s", sql)
	}
	if !strings.Contains(sql, "customer_tier") {
		t.Fatalf("joined dimension column is missing: %s", sql)
	}
}

// 连接边的两侧都必须按节点限定，否则同名列会在多表下变成歧义引用。
func TestJoinedDimensionIsQualifiedByItsOwnNode(t *testing.T) {
	semanticIR, resolution := joinedFixture(t, registry.CardinalityManyToOne, registry.FanoutSafe)
	document, _, _, _, err := buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Nodes) != 2 || len(document.Joins) != 1 {
		t.Fatalf("document did not describe a two-node join: %#v", document.Nodes)
	}
	joinedNode := joinedNodeID("model-customer-v1")
	for _, field := range document.Fields {
		if field.Code == "customer_tier" && field.Expression.NodeID != joinedNode {
			t.Fatalf("joined dimension resolved to node %q, want %q", field.Expression.NodeID, joinedNode)
		}
	}
	join := document.Joins[0]
	if join.LeftNodeID != anchorNodeID || join.RightNodeID != joinedNode {
		t.Fatalf("join connects the wrong nodes: %+v", join)
	}
	if join.Conditions[0].LeftExpression.NodeID != anchorNodeID ||
		join.Conditions[0].RightExpression.NodeID != joinedNode {
		t.Fatalf("join condition is not node-qualified: %+v", join.Conditions[0])
	}
}

// 会放大行数的关系必须被拒绝，而不是编译成普通 LEFT JOIN。
// 扇出会在没有任何报错的情况下把每一个度量值放大，这是这个编译器能产出的
// 最坏结果：一个被自信地呈现出来的错误数字。
func TestFanoutBearingJoinIsRefusedRatherThanInflatingMeasures(t *testing.T) {
	for _, test := range []struct {
		name        string
		cardinality registry.Cardinality
		fanout      registry.FanoutPolicy
	}{
		{"one to many", registry.CardinalityOneToMany, registry.FanoutPreAggregateRequired},
		{"many to many", registry.CardinalityManyToMany, registry.FanoutBridgeRequired},
		{"safe cardinality but blocked policy", registry.CardinalityManyToOne, registry.FanoutBlock},
	} {
		t.Run(test.name, func(t *testing.T) {
			semanticIR, resolution := joinedFixture(t, test.cardinality, test.fanout)
			_, _, _, _, err := buildQueryDocument(semanticIR, resolution)
			if !errors.Is(err, ErrUnsupportedQuery) {
				t.Fatalf("fanout-bearing join compiled with %v", err)
			}
		})
	}
}

// 连接键在两侧同名是常态，不能因此拒绝：这是最普通的一种连接。
func TestSharedJoinKeyCodeDoesNotBlockAnOrdinaryJoin(t *testing.T) {
	semanticIR, resolution := joinedFixture(t, registry.CardinalityManyToOne, registry.FanoutSafe)
	// customer_id 同时存在于事实模型与维表，正是连接键本身。
	if _, _, _, _, err := buildQueryDocument(semanticIR, resolution); err != nil {
		t.Fatalf("ordinary join on a shared key code was refused: %v", err)
	}
}

// 但两列真的同时进入扁平输出投影时必须失败关闭：静默选一边等于用用户
// 没有指名的那一列回答问题。
func TestAmbiguousOutputColumnAcrossJoinedModelsFailsClosed(t *testing.T) {
	semanticIR, resolution := joinedFixture(t, registry.CardinalityManyToOne, registry.FanoutSafe)
	collision := resolvedField(t, "tier_dup", "customer_tier", "DIMENSION", "STRING")
	resolution.Model.Fields = append(resolution.Model.Fields, collision)
	resolution.Dimensions = append(resolution.Dimensions, DimensionContract{
		DimensionVersionID: "dimension-tier-dup-v1", ModelVersionID: "model-sales-v1",
		LogicalFieldID: "tier_dup", ContentHash: hash("dimension-tier-dup-v1"),
		Kind: registry.DimensionCategorical, Sensitivity: registry.SensitivityInternal,
		MemberIndexPolicy: registry.MemberIndexNone,
	})
	semanticIR.GroupBy = append(semanticIR.GroupBy, ir.GroupBy{DimensionVersionID: "dimension-tier-dup-v1"})
	_, _, _, _, err := buildQueryDocument(semanticIR, resolution)
	if !errors.Is(err, ErrInvalidQueryPlan) || !strings.Contains(err.Error(), "result column") {
		t.Fatalf("ambiguous output column compiled with %v", err)
	}
}

// 单模型查询的计划序列化不能因为支持了 join 而漂移：joinedSources 是
// omitempty，规范形式必须与以前逐字节一致，否则所有历史 planHash 都会变。
func TestSingleModelPlanIsUnchangedByJoinSupport(t *testing.T) {
	semanticIR, resolution := joinedFixture(t, registry.CardinalityManyToOne, registry.FanoutSafe)
	// 退回单模型：去掉连接的一切痕迹，分组改用事实模型自己的维度。
	resolution.JoinedModels = nil
	resolution.Relationships = nil
	resolution.GraphPath = nil
	semanticIR.GroupBy = []ir.GroupBy{{DimensionVersionID: "dimension-region-v1"}}

	document, source, values, shapes, err := buildQueryDocument(semanticIR, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Nodes) != 1 || len(document.Joins) != 0 {
		t.Fatalf("single-model document grew a join: %#v", document.Joins)
	}
	plan, err := compileQueryPlan(
		QueryRoleCurrent, document, source, nil, shapes, values, semanticIR.Limit,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "joinedSources") {
		t.Fatalf("single-model plan serialized a joined-sources field: %s", raw)
	}
	var _ askdata.ContentHash = plan.PlanHash
}
