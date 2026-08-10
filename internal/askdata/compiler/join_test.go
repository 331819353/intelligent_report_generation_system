package compiler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestCompileJoinCoversCompleteMatrix(t *testing.T) {
	tests := []struct {
		name        string
		cardinality registry.Cardinality
		policy      registry.FanoutPolicy
		bridge      bool
		behavior    JoinBehavior
		contains    []string
	}{
		{name: "one to one safe", cardinality: registry.CardinalityOneToOne, policy: registry.FanoutSafe,
			behavior: JoinDirect, contains: []string{`SELECT * FROM "warehouse_published"."left_view"`, `LEFT JOIN`}},
		{name: "many to one safe", cardinality: registry.CardinalityManyToOne, policy: registry.FanoutSafe,
			behavior: JoinDirect, contains: []string{`LEFT JOIN`, `"left"."entity_id" = "right"."entity_id"`}},
		{name: "one to many preaggregate", cardinality: registry.CardinalityOneToMany, policy: registry.FanoutPreAggregateRequired,
			behavior: JoinPreAggregate, contains: []string{`WITH "right_pre" AS`, `SUM("right_source"."amount")`, `GROUP BY`}},
		{name: "many to many bridge", cardinality: registry.CardinalityManyToMany, policy: registry.FanoutBridgeRequired,
			bridge: true, behavior: JoinThroughBridge,
			contains: []string{`WITH "left_pre" AS`, `"right_pre" AS`, `"bridge_dedup" AS`, `SELECT DISTINCT`, `INNER JOIN`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := joinCompileFixture(test.cardinality, test.policy, test.bridge)
			compiled, err := CompileJoin(request)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Behavior != test.behavior || compiled.RiskCode != graph.JoinRiskCode(
				string(test.cardinality)+"_"+string(test.policy),
			) {
				t.Fatalf("compiled metadata = %#v", compiled)
			}
			for _, fragment := range test.contains {
				if !strings.Contains(compiled.SQL, fragment) {
					t.Fatalf("SQL missing %q: %s", fragment, compiled.SQL)
				}
			}
		})
	}

	for _, cardinality := range []registry.Cardinality{
		registry.CardinalityOneToOne, registry.CardinalityManyToOne,
		registry.CardinalityOneToMany, registry.CardinalityManyToMany,
	} {
		request := joinCompileFixture(cardinality, registry.FanoutBlock, false)
		if compiled, err := CompileJoin(request); !errors.Is(err, ErrJoinBlocked) ||
			compiled.SQL != "" || !strings.Contains(err.Error(), PlanJoinBlockedCode) {
			t.Fatalf("blocked %s compiled as %#v, %v", cardinality, compiled, err)
		}
	}
}

func TestCompileJoinFailsClosedForNullInvalidAndMissingBridgeContracts(t *testing.T) {
	nullCardinality := joinCompileFixture("", registry.FanoutSafe, false)
	if _, err := CompileJoin(nullCardinality); !errors.Is(err, ErrJoinBlocked) {
		t.Fatalf("NULL legacy cardinality error = %v", err)
	}
	invalid := joinCompileFixture(registry.CardinalityManyToMany, registry.FanoutSafe, false)
	if _, err := CompileJoin(invalid); !errors.Is(err, ErrInvalidJoinContract) {
		t.Fatalf("MANY_TO_MANY SAFE error = %v", err)
	}
	missingBridge := joinCompileFixture(
		registry.CardinalityManyToMany, registry.FanoutBridgeRequired, false,
	)
	missingBridge.Relationship.BridgeModelVersionID = ""
	if _, err := CompileJoin(missingBridge); !errors.Is(err, ErrInvalidJoinContract) {
		t.Fatalf("missing bridge error = %v", err)
	}
	injection := joinCompileFixture(registry.CardinalityOneToOne, registry.FanoutSafe, false)
	injection.Right.Relation = `right_view";DROP_TABLE`
	if _, err := CompileJoin(injection); !errors.Is(err, ErrInvalidJoinContract) {
		t.Fatalf("identifier injection error = %v", err)
	}
	derivedIdentifierOverflow := joinCompileFixture(
		registry.CardinalityOneToMany, registry.FanoutPreAggregateRequired, false,
	)
	derivedIdentifierOverflow.Right.Alias = strings.Repeat("a", 63)
	if compiled, err := CompileJoin(derivedIdentifierOverflow); !errors.Is(err, ErrInvalidJoinContract) ||
		compiled.SQL != "" || compiled.Behavior != "" || compiled.RiskCode != "" {
		t.Fatalf("derived identifier overflow compiled as %#v, %v", compiled, err)
	}
}

func TestCompileJoinPropertyAlwaysEmitsItsFanoutProtection(t *testing.T) {
	property := func(selector uint8) bool {
		matrix := []struct {
			cardinality registry.Cardinality
			policy      registry.FanoutPolicy
			bridge      bool
		}{
			{registry.CardinalityOneToOne, registry.FanoutSafe, false},
			{registry.CardinalityManyToOne, registry.FanoutSafe, false},
			{registry.CardinalityOneToMany, registry.FanoutPreAggregateRequired, false},
			{registry.CardinalityManyToMany, registry.FanoutBridgeRequired, true},
			{registry.CardinalityOneToOne, registry.FanoutBlock, false},
			{registry.CardinalityManyToOne, registry.FanoutBlock, false},
			{registry.CardinalityOneToMany, registry.FanoutBlock, false},
			{registry.CardinalityManyToMany, registry.FanoutBlock, false},
		}
		entry := matrix[int(selector)%len(matrix)]
		compiled, err := CompileJoin(joinCompileFixture(entry.cardinality, entry.policy, entry.bridge))
		switch entry.policy {
		case registry.FanoutBlock:
			return errors.Is(err, ErrJoinBlocked) && compiled.SQL == ""
		case registry.FanoutSafe:
			return err == nil && !strings.Contains(compiled.SQL, "_pre") &&
				strings.Count(compiled.SQL, " JOIN ") == 1
		case registry.FanoutPreAggregateRequired:
			return err == nil && strings.Contains(compiled.SQL, "GROUP BY") &&
				strings.Contains(compiled.SQL, `"right_pre"`)
		case registry.FanoutBridgeRequired:
			return err == nil && strings.Count(compiled.SQL, "GROUP BY") == 2 &&
				strings.Contains(compiled.SQL, "SELECT DISTINCT") &&
				strings.Count(compiled.SQL, " INNER JOIN ") == 2
		default:
			return false
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 512}); err != nil {
		t.Fatal(err)
	}
}

func joinCompileFixture(
	cardinality registry.Cardinality,
	policy registry.FanoutPolicy,
	withBridge bool,
) JoinCompileRequest {
	relationship := RelationshipContract{
		RelationshipVersionID: "relationship-v1",
		LeftModelVersionID:    "model-left-v1", RightModelVersionID: "model-right-v1",
		JoinAST:  json.RawMessage(`{"type":"EQUALS","leftFieldId":"entity_id","rightFieldId":"entity_id"}`),
		JoinType: registry.JoinLeft, Cardinality: cardinality, FanoutPolicy: policy,
	}
	request := JoinCompileRequest{
		Relationship: relationship,
		Left: JoinSource{
			ModelVersionID: "model-left-v1", Schema: "warehouse_published",
			Relation: "left_view", Alias: "left", GroupBy: []string{"entity_id"},
			Measures: []JoinMeasure{{Column: "quantity", Function: registry.AggregationSum, Alias: "quantity"}},
		},
		Right: JoinSource{
			ModelVersionID: "model-right-v1", Schema: "warehouse_published",
			Relation: "right_view", Alias: "right", GroupBy: []string{"entity_id"},
			Measures: []JoinMeasure{{Column: "amount", Function: registry.AggregationSum, Alias: "amount"}},
		},
	}
	if withBridge {
		request.Relationship.JoinType = registry.JoinInner
		request.Relationship.BridgeModelVersionID = "model-bridge-v1"
		request.Bridge = &BridgeJoinSpec{
			Source: JoinSource{
				ModelVersionID: "model-bridge-v1", Schema: "warehouse_published",
				Relation: "bridge_view", Alias: "bridge",
			},
			LeftSourceColumn: "entity_id", LeftBridgeColumn: "left_id",
			RightBridgeColumn: "right_id", RightSourceColumn: "entity_id",
		}
	}
	return request
}
