package graph

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestPlanRequestNormalizesStableReferencesAndBuildsVID(t *testing.T) {
	request := graphTestRequest(t)
	request.MetricRefs = []ObjectVersionRef{request.MetricRefs[1], request.MetricRefs[0], request.MetricRefs[0]}
	request.ModelRefs = []ObjectVersionRef{request.ModelRefs[1], request.ModelRefs[0]}
	normalized, err := request.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.MetricRefs) != 2 || normalized.MetricRefs[0].VersionID != "metric-orders@v1" ||
		normalized.ModelRefs[0].VersionID != "model-lines@v1" {
		t.Fatalf("unexpected normalized request: %#v", normalized)
	}
	if normalized.MaxJoinHops != DefaultJoinHops || normalized.MaxPaths != DefaultJoinPaths {
		t.Fatalf("defaults were not applied: %#v", normalized)
	}
	vid, err := BuildVID(normalized.Scope.TenantID, ObjectTypeMetric, normalized.MetricRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	if vid != "tenant-graph-a:metric:metric-orders:1" {
		t.Fatalf("VID = %q", vid)
	}
}

func TestPlanRequestRejectsInjectionScopeAndVIDOverflow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanRequest)
	}{
		{name: "object ID injection", mutate: func(request *PlanRequest) {
			request.MetricRefs[0].ObjectID = `metric\"; DROP SPACE askdata; --`
		}},
		{name: "version ID injection", mutate: func(request *PlanRequest) {
			request.MetricRefs[0].VersionID = "metric bad\nMATCH"
		}},
		{name: "domain outside policy", mutate: func(request *PlanRequest) {
			request.DomainID = "finance"
		}},
		{name: "tampered policy hash", mutate: func(request *PlanRequest) {
			request.Scope.PolicyHash = askdata.HashBytes([]byte("tampered"))
		}},
		{name: "unbounded hops", mutate: func(request *PlanRequest) {
			request.MaxJoinHops = MaxJoinHops + 1
		}},
		{name: "member without dimension", mutate: func(request *PlanRequest) {
			request.DimensionRefs = nil
		}},
		{name: "conflicting version ID", mutate: func(request *PlanRequest) {
			request.ModelRefs[1].VersionID = request.ModelRefs[0].VersionID
		}},
		{name: "duplicate VID", mutate: func(request *PlanRequest) {
			request.ModelRefs[1].ObjectID = request.ModelRefs[0].ObjectID
			request.ModelRefs[1].Version = request.ModelRefs[0].Version
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := graphTestRequest(t)
			test.mutate(&request)
			if _, err := request.Normalize(); !errors.Is(err, ErrInvalidPlanRequest) {
				t.Fatalf("Normalize error = %v", err)
			}
		})
	}

	longTenant := askdata.ID("t" + strings.Repeat("x", askdata.MaxIDLength-1))
	longObject := askdata.ID("o" + strings.Repeat("x", askdata.MaxIDLength-1))
	if _, err := BuildVID(longTenant, ObjectTypeSemanticModel, ObjectVersionRef{
		ObjectID: longObject, VersionID: "model@v1", Version: 1,
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long VID error = %v", err)
	}
}

func TestJoinPathRiskAndGraphPlanHashesAreDeterministic(t *testing.T) {
	request := graphTestRequest(t)
	normalized, err := request.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-orders-lines@v1",
		FromModelVersionID: "model-orders@v1", ToModelVersionID: "model-lines@v1",
		Direction: TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityOneToMany, FanoutPolicy: registry.FanoutBlock,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if path.Allowed || !reflect.DeepEqual(path.RiskCodes, []JoinRiskCode{JoinRiskFanoutBlocked, JoinRiskOneToMany}) {
		t.Fatalf("unexpected path risk: %#v", path)
	}

	bindings := []MetricModelBinding{
		{MetricVersionID: "metric-revenue@v1", ModelVersionID: "model-lines@v1"},
		{MetricVersionID: "metric-orders@v1", ModelVersionID: "model-orders@v1"},
	}
	models := []ObjectVersionRef{normalized.ModelRefs[1], normalized.ModelRefs[0]}
	plan, err := NewGraphPlan(normalized, models, bindings, nil, nil, []JoinPath{path})
	if err != nil {
		t.Fatal(err)
	}
	reversedPlan, err := NewGraphPlan(normalized, []ObjectVersionRef{models[1], models[0]},
		[]MetricModelBinding{bindings[1], bindings[0]}, nil, nil, []JoinPath{path})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlanHash != reversedPlan.PlanHash || !reflect.DeepEqual(plan, reversedPlan) {
		t.Fatalf("plan is not canonical:\n%#v\n%#v", plan, reversedPlan)
	}
	evidence, err := plan.EvidenceRef()
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != askdata.EvidenceKindGraphPath || evidence.ContentHash != plan.PlanHash ||
		evidence.SourceID != normalized.Scope.Release.ReleaseID {
		t.Fatalf("unexpected graph evidence: %#v", evidence)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	if strings.Contains(lower, "ngql") || strings.Contains(lower, "statement") || strings.Contains(lower, "query") {
		t.Fatalf("GraphPlan exposed a query surface: %s", raw)
	}

	tampered := plan
	tampered.JoinPaths[0].Allowed = true
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered path risk was accepted")
	}
}

func TestJoinPathRiskDoesNotDependOnRelationshipOrientation(t *testing.T) {
	path, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-lines-orders@v1",
		FromModelVersionID: "model-lines@v1", ToModelVersionID: "model-orders@v1",
		Direction: TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(path.RiskCodes, []JoinRiskCode{JoinRiskOneToMany}) {
		t.Fatalf("orientation-dependent join risk: %#v", path.RiskCodes)
	}
}

func graphTestRequest(t *testing.T) PlanRequest {
	t.Helper()
	release := askdata.ReleaseRef{ReleaseID: "release-sales@v1", ContentHash: askdata.HashBytes([]byte("release-sales-v1"))}
	scope, err := askdata.NewPolicyScope(
		"tenant-graph-a", "actor-analyst", []askdata.ID{"sales"}, []askdata.ID{"analyst"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	return PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []ObjectVersionRef{
			{ObjectID: "metric-orders", VersionID: "metric-orders@v1", Version: 1},
			{ObjectID: "metric-revenue", VersionID: "metric-revenue@v1", Version: 1},
		},
		ModelRefs: []ObjectVersionRef{
			{ObjectID: "model-orders", VersionID: "model-orders@v1", Version: 1},
			{ObjectID: "model-lines", VersionID: "model-lines@v1", Version: 1},
		},
		DimensionRefs: []ObjectVersionRef{
			{ObjectID: "dimension-product", VersionID: "dimension-product@v1", Version: 1},
			{ObjectID: "dimension-region", VersionID: "dimension-region@v1", Version: 1},
		},
		MemberRefs: []ObjectVersionRef{
			{ObjectID: "member-east", VersionID: "member-east@v1", Version: 1},
		},
	}
}
