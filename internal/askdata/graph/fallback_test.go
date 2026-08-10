package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestClassifyQueryShapeCoversGraphUnavailableMatrix(t *testing.T) {
	request := graphTestRequest(t)
	oneHop := matrixJoinPath(t, request.ModelRefs[0].VersionID, request.ModelRefs[1].VersionID, 1,
		registry.CardinalityManyToOne, registry.FanoutSafe)
	multiHop := matrixJoinPath(t, request.ModelRefs[0].VersionID, request.ModelRefs[1].VersionID, 2,
		registry.CardinalityManyToOne, registry.FanoutSafe)
	preaggregate := matrixJoinPath(t, request.ModelRefs[0].VersionID, request.ModelRefs[1].VersionID, 1,
		registry.CardinalityOneToMany, registry.FanoutPreAggregateRequired)

	singleMetric := request
	singleMetric.MetricRefs = singleMetric.MetricRefs[:1]
	ambiguous := request
	ambiguous.MemberDimensionAmbiguous = true
	tests := []struct {
		name        string
		candidates  FallbackCandidates
		want        ShapeClass
		code        string
		disposition string
	}{
		{
			name: "single model single metric",
			candidates: FallbackCandidates{Request: singleMetric, MetricModels: []MetricModelBinding{{
				MetricVersionID: singleMetric.MetricRefs[0].VersionID,
				ModelVersionID:  singleMetric.ModelRefs[0].VersionID,
			}}},
			want: ShapeSingleModelSingleMetric,
		},
		{
			name: "single model multiple metrics",
			candidates: FallbackCandidates{Request: request, MetricModels: []MetricModelBinding{
				{MetricVersionID: request.MetricRefs[0].VersionID, ModelVersionID: request.ModelRefs[0].VersionID},
				{MetricVersionID: request.MetricRefs[1].VersionID, ModelVersionID: request.ModelRefs[0].VersionID},
			}},
			want: ShapeSingleModelMultiMetric,
		},
		{
			name:       "cross model unique safe one hop",
			candidates: matrixCandidates(request, oneHop),
			want:       ShapeCrossModelSafeOneHop,
		},
		{
			name:       "cross model multi hop",
			candidates: matrixCandidates(request, multiHop),
			want:       ShapeCrossModelMultiHop, code: GraphUnavailableCode, disposition: FallbackDispositionBlocked,
		},
		{
			name:       "many to many or preaggregation",
			candidates: matrixCandidates(request, preaggregate),
			want:       ShapeUnsafeOrPreaggregate, code: GraphUnsafeJoinCode, disposition: FallbackDispositionBlocked,
		},
		{
			name:       "member same name across dimensions",
			candidates: matrixCandidates(ambiguous, oneHop),
			want:       ShapeMemberDimensionAmbiguous, code: GraphMemberAmbiguousCode,
			disposition: FallbackDispositionClarify,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyQueryShape(test.candidates)
			if got != test.want {
				t.Fatalf("ClassifyQueryShape() = %s, want %s", got, test.want)
			}
			failure := fallbackMatrixFailure(got)
			if test.code == "" {
				if failure != nil {
					t.Fatalf("fallbackMatrixFailure() = %v", failure)
				}
				return
			}
			var matrixError *GraphFallbackError
			if !errors.As(failure, &matrixError) || matrixError.Code != test.code ||
				matrixError.Disposition != test.disposition || matrixError.Shape != test.want {
				t.Fatalf("fallbackMatrixFailure() = %#v", failure)
			}
			if test.disposition == FallbackDispositionClarify {
				if !errors.Is(failure, ErrGraphFallbackClarification) {
					t.Fatalf("clarification error = %v", failure)
				}
			} else if !errors.Is(failure, ErrGraphFallbackBlocked) {
				t.Fatalf("blocked error = %v", failure)
			}
		})
	}
}

func TestCrossModelMultiHopFallbackNeverProducesAnExecutableJoin(t *testing.T) {
	request := graphTestRequest(t)
	for seed := 0; seed < 128; seed++ {
		hops := 2 + seed%3
		path := matrixJoinPath(t, request.ModelRefs[0].VersionID, request.ModelRefs[1].VersionID,
			hops, registry.CardinalityManyToOne, registry.FanoutSafe)
		shape := ClassifyQueryShape(matrixCandidates(request, path))
		if shape != ShapeCrossModelMultiHop || !errors.Is(fallbackMatrixFailure(shape), ErrGraphFallbackBlocked) {
			t.Fatalf("seed %d produced executable shape %s for %d hops", seed, shape, hops)
		}
	}
}

func TestDegradedGraphPlanKeepsTheNormalGraphPlanContract(t *testing.T) {
	_, normal := resolverFixture(t)
	degraded, err := normal.WithDegradation(DegradationNebulaUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.TypeOf(normal) != reflect.TypeOf(degraded) || !degraded.Degraded ||
		degraded.DegradationReason != DegradationNebulaUnavailable || degraded.PlanHash == normal.PlanHash {
		t.Fatalf("degraded GraphPlan contract = %#v", degraded)
	}
	normalKeys, degradedKeys := jsonObjectKeys(t, normal), jsonObjectKeys(t, degraded)
	if !reflect.DeepEqual(normalKeys, degradedKeys) {
		t.Fatalf("GraphPlan structures differ: normal=%v degraded=%v", normalKeys, degradedKeys)
	}
	evidence, err := degraded.EvidenceRef()
	if err != nil || evidence.ContentHash != degraded.PlanHash {
		t.Fatalf("degraded evidence = %#v, %v", evidence, err)
	}
}

func matrixCandidates(request PlanRequest, path JoinPath) FallbackCandidates {
	return FallbackCandidates{
		Request: request,
		MetricModels: []MetricModelBinding{
			{MetricVersionID: request.MetricRefs[0].VersionID, ModelVersionID: request.ModelRefs[0].VersionID},
			{MetricVersionID: request.MetricRefs[1].VersionID, ModelVersionID: request.ModelRefs[1].VersionID},
		},
		JoinPaths: []JoinPath{path},
	}
}

func matrixJoinPath(
	t *testing.T,
	from askdata.ID,
	to askdata.ID,
	hops int,
	cardinality registry.Cardinality,
	fanout registry.FanoutPolicy,
) JoinPath {
	t.Helper()
	steps := make([]JoinStep, 0, hops)
	current := from
	for index := 0; index < hops; index++ {
		next := to
		if index < hops-1 {
			next = askdata.ID(fmt.Sprintf("model-matrix-bridge-%d", index))
		}
		steps = append(steps, JoinStep{
			Hop: index + 1, RelationshipVersionID: askdata.ID(fmt.Sprintf("relationship-matrix-%d@v1", index)),
			FromModelVersionID: current, ToModelVersionID: next, Direction: TraversalForward,
			JoinType: registry.JoinInner, Cardinality: cardinality, FanoutPolicy: fanout,
		})
		current = next
	}
	path, err := NewJoinPath(steps)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonObjectKeys(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

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
		FanoutPolicy: registry.FanoutPreAggregateRequired,
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
		!containsRisk(paths[0].RiskCodes, JoinRiskOneToManyPreAggregateRequired) {
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
		!containsRisk(paths[0].RiskCodes, JoinRiskManyToManyBlock) {
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
