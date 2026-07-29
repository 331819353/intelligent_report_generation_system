package dataset

import (
	"reflect"
	"testing"
)

func TestApplyDatasetMetadataUpdatePreservesDAGAndTechnicalStructure(t *testing.T) {
	visible := true
	document := Document{
		Dataset: Descriptor{
			Code: "dwd_orders", Name: "订单事实表", Description: "旧说明",
			Subject: "经营分析", Type: "SINGLE_SOURCE", Layer: LayerDWD,
		},
		Nodes: []Node{{ID: "t1", Type: "DATASET", Alias: "orders"}},
		Fields: []Field{{
			ID: "field_1", Code: "order_id", Name: "订单编号",
			Description: "旧字段说明", Role: "IDENTIFIER",
			CanonicalType: "STRING", SemanticType: "IDENTIFIER",
			Expression: Expression{
				Type: "TRIM",
				Argument: &Expression{
					Type: "FIELD_REF", NodeID: "t1", Field: "order_id",
				},
			},
			Nullable: false, Visible: &visible,
		}},
		Designer: map[string]any{
			"version": "1.0",
			"end":     map[string]any{"id": "end_1"},
		},
	}
	nodesBefore := append([]Node(nil), document.Nodes...)
	expressionBefore := document.Fields[0].Expression
	codeBefore := document.Fields[0].Code
	typeBefore := document.Fields[0].CanonicalType
	designerBefore := document.Designer

	err := applyDatasetMetadataUpdate(&document, MetadataUpdateInput{
		Name: "订单业务事实表", Description: "新的数据集说明", Subject: "订单履约",
		Fields: []FieldMetadataUpdate{{
			ID: "field_1", Name: "业务订单编号", Description: "稳定的订单业务键",
			Role: "IDENTIFIER", SemanticType: "BUSINESS_KEY",
			Nullable: false, Visible: true,
		}},
	})
	if err != nil {
		t.Fatalf("apply metadata update: %v", err)
	}
	if document.Dataset.Name != "订单业务事实表" ||
		document.Fields[0].Name != "业务订单编号" {
		t.Fatalf("business metadata was not updated: %#v", document)
	}
	if !reflect.DeepEqual(document.Nodes, nodesBefore) ||
		!reflect.DeepEqual(document.Fields[0].Expression, expressionBefore) ||
		document.Fields[0].Code != codeBefore ||
		document.Fields[0].CanonicalType != typeBefore ||
		!reflect.DeepEqual(document.Designer, designerBefore) {
		t.Fatal("metadata update changed DAG or technical field structure")
	}
}
