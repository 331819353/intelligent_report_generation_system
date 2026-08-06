package graph

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestFallbackTraversalMatchesFrozenVIDDirectionAndRisk(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	lines, orders := request.ModelRefs[0], request.ModelRefs[1]
	relationship := fallbackRelationship{
		VersionID:          "relationship-orders-lines@v1",
		LeftModelVersionID: orders.VersionID, RightModelVersionID: lines.VersionID,
		JoinType: registry.JoinInner, Cardinality: registry.CardinalityOneToMany,
		FanoutPolicy: registry.FanoutCertifiedPre,
	}
	paths, err := enumerateFallbackPaths(request, request.ModelRefs, []fallbackRelationship{relationship})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || len(paths[0].Steps) != 1 {
		t.Fatalf("paths = %#v", paths)
	}
	step := paths[0].Steps[0]
	if step.FromModelVersionID != lines.VersionID || step.ToModelVersionID != orders.VersionID ||
		step.Direction != TraversalReverse || !paths[0].Allowed ||
		!containsRisk(paths[0].RiskCodes, JoinRiskOneToMany) ||
		!containsRisk(paths[0].RiskCodes, JoinRiskPreaggregation) {
		t.Fatalf("unexpected fallback path: %#v", paths[0])
	}
}

func TestFallbackTraversalAllowsOnlyRequestedIntermediateModels(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	left, right := request.ModelRefs[0], request.ModelRefs[1]
	middle := ObjectVersionRef{ObjectID: "model-bridge", VersionID: "model-bridge@v1", Version: 1}
	request.ModelRefs = append(request.ModelRefs, middle)
	request, err = request.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	relationships := []fallbackRelationship{
		{
			VersionID:          "relationship-left-bridge@v1",
			LeftModelVersionID: left.VersionID, RightModelVersionID: middle.VersionID,
			JoinType: registry.JoinInner, Cardinality: registry.CardinalityOneToOne,
			FanoutPolicy: registry.FanoutSafe,
		},
		{
			VersionID:          "relationship-bridge-right@v1",
			LeftModelVersionID: middle.VersionID, RightModelVersionID: right.VersionID,
			JoinType: registry.JoinLeft, Cardinality: registry.CardinalityManyToMany,
			FanoutPolicy: registry.FanoutBlock,
		},
	}
	paths, err := enumerateFallbackPaths(request, []ObjectVersionRef{left, right}, relationships)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || len(paths[0].Steps) != 2 || paths[0].Allowed ||
		paths[0].Steps[0].ToModelVersionID != middle.VersionID ||
		!containsRisk(paths[0].RiskCodes, JoinRiskManyToMany) ||
		!containsRisk(paths[0].RiskCodes, JoinRiskFanoutBlocked) {
		t.Fatalf("unexpected bridge path: %#v", paths)
	}

	request.ModelRefs = []ObjectVersionRef{left, right}
	request, err = request.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enumerateFallbackPaths(request, request.ModelRefs, relationships); !errors.Is(err, ErrPostgresFallbackUnavailable) {
		t.Fatalf("unrequested bridge error = %v", err)
	}
}

func TestFallbackTraversalIsDeterministicAndBoundedByRequest(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	request.MaxPaths = 1
	relationships := []fallbackRelationship{
		{
			VersionID:           "relationship-z@v1",
			LeftModelVersionID:  request.ModelRefs[0].VersionID,
			RightModelVersionID: request.ModelRefs[1].VersionID,
			JoinType:            registry.JoinInner, Cardinality: registry.CardinalityOneToOne,
			FanoutPolicy: registry.FanoutSafe,
		},
		{
			VersionID:           "relationship-a@v1",
			LeftModelVersionID:  request.ModelRefs[0].VersionID,
			RightModelVersionID: request.ModelRefs[1].VersionID,
			JoinType:            registry.JoinInner, Cardinality: registry.CardinalityOneToOne,
			FanoutPolicy: registry.FanoutSafe,
		},
	}
	paths, err := enumerateFallbackPaths(request, request.ModelRefs, relationships)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].Steps[0].RelationshipVersionID != "relationship-a@v1" {
		t.Fatalf("deterministic bounded paths = %#v", paths)
	}
}

func TestFallbackAccessRequiresAuthenticatedMatchingActorAndDomain(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFallbackAccessContext(context.Background(), request); !errors.Is(err, ErrFallbackAccessDenied) {
		t.Fatalf("missing access context error = %v", err)
	}
	wrong := database.WithAccessContext(context.Background(), "another-actor", string(request.DomainID))
	if err := validateFallbackAccessContext(wrong, request); !errors.Is(err, ErrFallbackAccessDenied) {
		t.Fatalf("wrong actor error = %v", err)
	}
	valid := database.WithAccessContext(
		context.Background(), string(request.Scope.ActorID), string(request.DomainID),
	)
	if err := validateFallbackAccessContext(valid, request); err != nil {
		t.Fatalf("matching access context error = %v", err)
	}
}

func TestCachedPlanStrictContractCannotBeReboundByHashOnly(t *testing.T) {
	request, plan := resolverFixture(t)
	requestHash := plan.RequestHash
	tampered := plan
	tampered.Scope.ActorID = askdata.ID("another-actor")
	if err := validatePlanForRequest(tampered, request, requestHash); err == nil {
		t.Fatal("validatePlanForRequest accepted a plan with a mutated actor")
	}
}

func containsRisk(values []JoinRiskCode, wanted JoinRiskCode) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
