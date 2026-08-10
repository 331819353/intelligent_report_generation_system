package registry

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateRelationshipCombinationCoversExactMatrix(t *testing.T) {
	bridgeID := "1ecf1685-edab-46f4-a7a7-0eec17f2ab31"
	valid := map[string]bool{
		"ONE_TO_ONE/SAFE":                    true,
		"ONE_TO_ONE/BLOCK":                   true,
		"MANY_TO_ONE/SAFE":                   true,
		"MANY_TO_ONE/BLOCK":                  true,
		"ONE_TO_MANY/PRE_AGGREGATE_REQUIRED": true,
		"ONE_TO_MANY/BLOCK":                  true,
		"MANY_TO_MANY/BRIDGE_REQUIRED":       true,
		"MANY_TO_MANY/BLOCK":                 true,
	}
	cardinalities := []Cardinality{
		CardinalityOneToOne, CardinalityManyToOne,
		CardinalityOneToMany, CardinalityManyToMany,
	}
	policies := []FanoutPolicy{
		FanoutSafe, FanoutPreAggregateRequired, FanoutBridgeRequired, FanoutBlock,
	}
	for _, cardinality := range cardinalities {
		for _, policy := range policies {
			key := string(cardinality) + "/" + string(policy)
			err := ValidateRelationshipCombination(cardinality, policy, bridgeID)
			if valid[key] && err != nil {
				t.Errorf("valid combination %s rejected: %v", key, err)
			}
			if !valid[key] && !errors.Is(err, ErrInvalidRelationshipCombination) {
				t.Errorf("invalid combination %s error = %v", key, err)
			}
		}
	}
	if err := ValidateRelationshipCombination(
		CardinalityManyToMany, FanoutBridgeRequired, "",
	); !errors.Is(err, ErrInvalidRelationshipCombination) {
		t.Fatalf("missing bridge error = %v", err)
	}
	for _, pair := range []struct {
		cardinality Cardinality
		policy      FanoutPolicy
	}{
		{"", FanoutSafe},
		{CardinalityOneToOne, ""},
		{"UNKNOWN", FanoutSafe},
		{CardinalityOneToOne, "UNKNOWN"},
	} {
		if err := ValidateRelationshipCombination(
			pair.cardinality, pair.policy, bridgeID,
		); !errors.Is(err, ErrInvalidRelationshipCombination) {
			t.Errorf("incomplete/unknown pair %#v error = %v", pair, err)
		}
	}
}

func TestRelationshipCertificationValidationRejectsInvalidCombinationAndMissingBridge(t *testing.T) {
	fixture := func(cardinality Cardinality, policy FanoutPolicy, bridgeID string) Relationship {
		return Relationship{
			VersionIdentity:      validVersionIdentity(),
			LeftModelVersionID:   validationRow,
			RightModelVersionID:  "1ecf1685-edab-46f4-a7a7-0eec17f2ab31",
			Type:                 RelationshipModelJoin,
			JoinType:             JoinInner,
			Cardinality:          cardinality,
			FanoutPolicy:         policy,
			BridgeModelVersionID: bridgeID,
			JoinAST: json.RawMessage(
				`{"type":"EQUALS","leftFieldId":"entity_id","rightFieldId":"entity_id"}`,
			),
		}
	}
	for name, relationship := range map[string]Relationship{
		"many-to-many safe": fixture(CardinalityManyToMany, FanoutSafe, ""),
		"bridge missing":    fixture(CardinalityManyToMany, FanoutBridgeRequired, ""),
	} {
		t.Run(name, func(t *testing.T) {
			err := relationship.Validate()
			var validation ValidationErrors
			if !errors.As(err, &validation) {
				t.Fatalf("Validate() error = %v, want ValidationErrors", err)
			}
			assertIssue(t, validation, validationCodeUnsafeFanout, "fanoutPolicy")
		})
	}
	valid := fixture(
		CardinalityManyToMany,
		FanoutBridgeRequired,
		"3da2f127-83d0-4bd5-a8cc-4a01e81aca1e",
	)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid bridge relationship rejected: %v", err)
	}
}
