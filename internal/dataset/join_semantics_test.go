package dataset

import (
	"encoding/json"
	"testing"
)

func TestDecodeAndNormalizeDerivesJoinCardinalityAndRemovesLegacySemantics(t *testing.T) {
	document := Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code: "join_migration", Name: "关联迁移",
			Type: "SINGLE_SOURCE", Layer: LayerDWD,
		},
		Joins: []Join{{
			ID: "join_legacy", LeftNodeID: "fact", RightNodeID: "dimension",
			JoinType: "left", Cardinality: "MANY_TO_MANY",
			RelationshipType: "BRIDGE", RelationshipRole: "MEMBER",
			FanoutPolicy: "ALLOCATE",
			Bridge: &BridgeContract{
				BridgeNodeID: "dimension", RelationshipTypeField: "member_type",
				AllocationWeightField: "weight",
			},
			Temporal: &TemporalJoinContract{
				EventNodeID: "fact", EventTimeField: "event_time",
				ValidityNodeID: "dimension", ValidFromField: "valid_from",
				ValidToField: "valid_to", ValidToInclusive: true,
			},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	normalized, err := DecodeAndNormalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	join := normalized.Joins[0]
	if join.JoinType != "LEFT" {
		t.Fatalf("join type = %q, want LEFT", join.JoinType)
	}
	if join.Cardinality != "MANY_TO_ONE" {
		t.Fatalf("cardinality = %q, want MANY_TO_ONE", join.Cardinality)
	}
	if join.RelationshipType != "" {
		t.Fatalf("relationship type = %q, want empty", join.RelationshipType)
	}
	if join.FanoutPolicy != "" {
		t.Fatalf("fanout policy = %q, want empty", join.FanoutPolicy)
	}
	if join.RelationshipRole != "" || join.Bridge != nil || join.Temporal != nil {
		t.Fatalf("legacy relationship semantics were not removed: %#v", join)
	}
}

func TestJoinCardinalityForType(t *testing.T) {
	tests := []struct {
		joinType string
		want     string
	}{
		{joinType: "INNER", want: "ONE_TO_ONE"},
		{joinType: "LEFT", want: "MANY_TO_ONE"},
		{joinType: "RIGHT", want: "ONE_TO_MANY"},
		{joinType: "FULL", want: "MANY_TO_MANY"},
	}
	for _, test := range tests {
		t.Run(test.joinType, func(t *testing.T) {
			if got := joinCardinalityForType(test.joinType); got != test.want {
				t.Fatalf("joinCardinalityForType(%q) = %q, want %q", test.joinType, got, test.want)
			}
		})
	}
}
