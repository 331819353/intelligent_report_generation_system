package graph

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	nebula "github.com/vesoft-inc/nebula-go/v3"
	nebulaTypes "github.com/vesoft-inc/nebula-go/v3/nebula"
	nebulaGraph "github.com/vesoft-inc/nebula-go/v3/nebula/graph"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type fakeNebulaExecutor struct {
	result *nebula.ResultSet
	err    error
}

func (executor fakeNebulaExecutor) ExecuteWithParameter(string, map[string]interface{}) (*nebula.ResultSet, error) {
	return executor.result, executor.err
}

type fakeQueryRunner struct {
	responses map[queryKind][]queryRow
	err       error
	queries   []compiledQuery
}

func (runner *fakeQueryRunner) Run(ctx context.Context, query compiledQuery) ([]queryRow, error) {
	runner.queries = append(runner.queries, query)
	if runner.err != nil {
		return nil, runner.err
	}
	return append([]queryRow(nil), runner.responses[query.kind]...), nil
}

func TestClientResolveBuildsReleaseBoundGraphPlan(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-orders-lines@v1",
		FromModelVersionID: "model-orders@v1", ToModelVersionID: "model-lines@v1",
		Direction: TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner := graphFixtureRunner(request, path)
	client := newClientWithRunner(runner)
	plan, err := client.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.queries) != 4 || runner.queries[0].kind != queryMetricModels ||
		runner.queries[3].kind != queryJoinPaths {
		t.Fatalf("unexpected query sequence: %#v", runner.queries)
	}
	if len(plan.Models) != 2 || len(plan.MetricModels) != 2 ||
		len(plan.CompatibleDimensions) != 2 || len(plan.MemberOwnerships) != 1 || len(plan.JoinPaths) != 1 {
		t.Fatalf("unexpected graph plan: %#v", plan)
	}
	if plan.Scope.PolicyHash != request.Scope.PolicyHash || plan.Scope.Release != request.Scope.Release ||
		plan.DomainID != request.DomainID || plan.PlanHash == "" || plan.RequestHash == "" {
		t.Fatalf("plan lost immutable scope: %#v", plan)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "match ") || strings.Contains(strings.ToLower(string(raw)), "ngql") {
		t.Fatalf("plan leaked generated query: %s", raw)
	}

	reversed := graphFixtureRunner(request, path)
	reversed.responses[queryMetricModels][0], reversed.responses[queryMetricModels][1] =
		reversed.responses[queryMetricModels][1], reversed.responses[queryMetricModels][0]
	reversedPlan, err := newClientWithRunner(reversed).Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, reversedPlan) {
		t.Fatalf("result order changed plan:\n%#v\n%#v", plan, reversedPlan)
	}
}

func TestClientRejectsCrossScopeAndInventedGraphRows(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-orders-lines@v1",
		FromModelVersionID: "model-orders@v1", ToModelVersionID: "model-lines@v1",
		Direction: TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*fakeQueryRunner)
	}{
		{name: "cross tenant", mutate: func(runner *fakeQueryRunner) {
			runner.responses[queryMetricModels][0]["tenant_id"] = "tenant-other"
		}},
		{name: "cross release", mutate: func(runner *fakeQueryRunner) {
			runner.responses[queryMetricModels][0]["release_hash"] = string(askdata.HashBytes([]byte("other")))
		}},
		{name: "invented model", mutate: func(runner *fakeQueryRunner) {
			runner.responses[queryMetricModels][0]["model_object_id"] = "model-secret"
			runner.responses[queryMetricModels][0]["model_version_id"] = "model-secret@v1"
		}},
		{name: "member wrong dimension", mutate: func(runner *fakeQueryRunner) {
			runner.responses[queryMemberOwnerships][0]["dimension_version_id"] = "dimension-secret@v1"
		}},
		{name: "path cross scope", mutate: func(runner *fakeQueryRunner) {
			value := runner.responses[queryJoinPaths][0]["join_path"].(scopedJoinPath)
			value.scope.tenantID = "tenant-other"
			runner.responses[queryJoinPaths][0]["join_path"] = value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := graphFixtureRunner(request, path)
			test.mutate(runner)
			_, err := newClientWithRunner(runner).Resolve(context.Background(), request)
			if !errors.Is(err, ErrInvalidGraphResult) {
				t.Fatalf("Resolve error = %v", err)
			}
		})
	}
}

func TestClientAllowsOnlyRequestedBridgeModels(t *testing.T) {
	request := graphTestRequest(t)
	request.ModelRefs = append(request.ModelRefs, ObjectVersionRef{
		ObjectID: "model-zz-bridge", VersionID: "model-zz-bridge@v1", Version: 1,
	})
	request, err := request.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewJoinPath([]JoinStep{
		{
			Hop: 1, RelationshipVersionID: "relationship-orders-bridge@v1",
			FromModelVersionID: "model-orders@v1", ToModelVersionID: "model-zz-bridge@v1",
			Direction: TraversalForward, JoinType: registry.JoinInner,
			Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
		},
		{
			Hop: 2, RelationshipVersionID: "relationship-bridge-lines@v1",
			FromModelVersionID: "model-zz-bridge@v1", ToModelVersionID: "model-lines@v1",
			Direction: TraversalForward, JoinType: registry.JoinInner,
			Cardinality: registry.CardinalityOneToOne, FanoutPolicy: registry.FanoutSafe,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := graphFixtureRunner(request, path)
	plan, err := newClientWithRunner(runner).Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := refIndex(plan.Models)["model-zz-bridge@v1"]; !exists {
		t.Fatalf("authorized bridge model is missing: %#v", plan.Models)
	}
	pathQuery := runner.queries[len(runner.queries)-1].statement
	if !strings.Contains(pathQuery, `"tenant-graph-a:semantic_model:model-zz-bridge:1"`) {
		t.Fatalf("authorized bridge VID is missing from path bound:\n%s", pathQuery)
	}

	badPath, err := NewJoinPath([]JoinStep{
		{
			Hop: 1, RelationshipVersionID: "relationship-orders-secret@v1",
			FromModelVersionID: "model-orders@v1", ToModelVersionID: "model-secret@v1",
			Direction: TraversalForward, JoinType: registry.JoinInner,
			Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
		},
		{
			Hop: 2, RelationshipVersionID: "relationship-secret-lines@v1",
			FromModelVersionID: "model-secret@v1", ToModelVersionID: "model-lines@v1",
			Direction: TraversalForward, JoinType: registry.JoinInner,
			Cardinality: registry.CardinalityOneToOne, FanoutPolicy: registry.FanoutSafe,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner = graphFixtureRunner(request, badPath)
	if _, err := newClientWithRunner(runner).Resolve(context.Background(), request); !errors.Is(err, ErrInvalidGraphResult) {
		t.Fatalf("unrequested bridge Resolve error = %v", err)
	}
}

func TestClientFailsClosedOnContextAndRunnerErrors(t *testing.T) {
	request := graphTestRequest(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeQueryRunner{responses: map[queryKind][]queryRow{}}
	if _, err := newClientWithRunner(runner).Resolve(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Resolve error = %v", err)
	}
	if len(runner.queries) != 0 {
		t.Fatal("cancelled request executed a graph query")
	}

	runner.err = ErrGraphQueryFailed
	if _, err := newClientWithRunner(runner).Resolve(context.Background(), request); !errors.Is(err, ErrGraphQueryFailed) {
		t.Fatalf("runner Resolve error = %v", err)
	}
}

func TestSessionQueryRunnerDecodesNebulaPath(t *testing.T) {
	request, err := graphTestRequest(t).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	result := nebulaPathResult(t, request, true)
	runner := sessionQueryRunner{executor: fakeNebulaExecutor{result: result}}
	rows, err := runner.Run(context.Background(), compiledQuery{kind: queryJoinPaths, maxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("decoded rows = %d", len(rows))
	}
	decoded, ok := rows[0]["join_path"].(scopedJoinPath)
	if !ok || decoded.scope.tenantID != request.Scope.TenantID ||
		decoded.scope.releaseHash != request.Scope.Release.ContentHash || len(decoded.path.Steps) != 1 {
		t.Fatalf("unexpected decoded path: %#v", rows)
	}
	step := decoded.path.Steps[0]
	if step.RelationshipVersionID != "relationship-orders-lines@v1" ||
		step.FromModelVersionID != "model-orders@v1" || step.ToModelVersionID != "model-lines@v1" ||
		step.Direction != TraversalForward || step.Cardinality != registry.CardinalityOneToMany ||
		step.FanoutPolicy != registry.FanoutCertifiedPre {
		t.Fatalf("unexpected decoded step: %#v", step)
	}

	runner = sessionQueryRunner{executor: fakeNebulaExecutor{result: nebulaPathResult(t, request, false)}}
	if _, err := runner.Run(context.Background(), compiledQuery{kind: queryJoinPaths, maxRows: 1}); !errors.Is(err, ErrInvalidGraphResult) {
		t.Fatalf("uncertified path error = %v", err)
	}
}

func graphFixtureRunner(request PlanRequest, path JoinPath) *fakeQueryRunner {
	scope := graphScope{
		tenantID: request.Scope.TenantID, domainID: request.DomainID,
		releaseHash: request.Scope.Release.ContentHash,
	}
	metricRows := []queryRow{
		scalarFixtureRow(request, request.MetricRefs[0], "metric", request.ModelRefs[1], "model"),
		scalarFixtureRow(request, request.MetricRefs[1], "metric", request.ModelRefs[0], "model"),
	}
	dimensionRows := []queryRow{
		scalarFixtureRow(request, request.ModelRefs[0], "model", request.DimensionRefs[0], "dimension"),
		scalarFixtureRow(request, request.ModelRefs[1], "model", request.DimensionRefs[1], "dimension"),
	}
	memberRow := scalarFixtureRow(request, request.MemberRefs[0], "member", request.DimensionRefs[1], "dimension")
	memberRow["member_status"] = string(MemberStatusActive)
	return &fakeQueryRunner{responses: map[queryKind][]queryRow{
		queryMetricModels:         metricRows,
		queryCompatibleDimensions: dimensionRows,
		queryMemberOwnerships:     {memberRow},
		queryJoinPaths:            {{"join_path": scopedJoinPath{scope: scope, path: path}}},
	}}
}

func scalarFixtureRow(request PlanRequest, left ObjectVersionRef, leftPrefix string, right ObjectVersionRef, rightPrefix string) queryRow {
	row := queryRow{
		"tenant_id": string(request.Scope.TenantID), "domain_id": string(request.DomainID),
		"release_hash": string(request.Scope.Release.ContentHash),
	}
	addRefColumns(row, leftPrefix, left)
	addRefColumns(row, rightPrefix, right)
	return row
}

func addRefColumns(row queryRow, prefix string, ref ObjectVersionRef) {
	row[prefix+"_object_id"] = string(ref.ObjectID)
	row[prefix+"_version_id"] = string(ref.VersionID)
	row[prefix+"_version"] = int64(ref.Version)
}

func nebulaPathResult(t *testing.T, request PlanRequest, certified bool) *nebula.ResultSet {
	t.Helper()
	orders := refIndex(request.ModelRefs)["model-orders@v1"]
	lines := refIndex(request.ModelRefs)["model-lines@v1"]
	ordersVertex := nebulaModelVertex(t, request, orders)
	linesVertex := nebulaModelVertex(t, request, lines)
	path := &nebulaTypes.Path{
		Src: ordersVertex,
		Steps: []*nebulaTypes.Step{{
			Dst: linesVertex, Type: 1, Name: []byte("JOINS_TO"), Ranking: 0,
			Props: map[string]*nebulaTypes.Value{
				"tenant_id":               stringNebulaValue(string(request.Scope.TenantID)),
				"domain_id":               stringNebulaValue(string(request.DomainID)),
				"release_hash":            stringNebulaValue(string(request.Scope.Release.ContentHash)),
				"relationship_version_id": stringNebulaValue("relationship-orders-lines@v1"),
				"join_type":               stringNebulaValue(string(registry.JoinInner)),
				"cardinality":             stringNebulaValue(string(registry.CardinalityOneToMany)),
				"fanout_policy":           stringNebulaValue(string(registry.FanoutCertifiedPre)),
				"certified":               boolNebulaValue(certified),
			},
		}},
	}
	response := &nebulaGraph.ExecutionResponse{
		ErrorCode: nebulaTypes.ErrorCode_SUCCEEDED,
		Data: &nebulaTypes.DataSet{
			ColumnNames: [][]byte{[]byte("join_path")},
			Rows:        []*nebulaTypes.Row{{Values: []*nebulaTypes.Value{{PVal: path}}}},
		},
	}
	result, err := nebula.GenResultSet(response)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func nebulaModelVertex(t *testing.T, request PlanRequest, ref ObjectVersionRef) *nebulaTypes.Vertex {
	t.Helper()
	vid, err := BuildVID(request.Scope.TenantID, ObjectTypeSemanticModel, ref)
	if err != nil {
		t.Fatal(err)
	}
	return &nebulaTypes.Vertex{
		Vid: stringNebulaValue(vid),
		Tags: []*nebulaTypes.Tag{{
			Name: []byte(string(ObjectTypeSemanticModel)),
			Props: map[string]*nebulaTypes.Value{
				"tenant_id":    stringNebulaValue(string(request.Scope.TenantID)),
				"domain_id":    stringNebulaValue(string(request.DomainID)),
				"release_hash": stringNebulaValue(string(request.Scope.Release.ContentHash)),
				"object_id":    stringNebulaValue(string(ref.ObjectID)),
				"version_id":   stringNebulaValue(string(ref.VersionID)),
				"version_no":   intNebulaValue(int64(ref.Version)),
			},
		}},
	}
}

func stringNebulaValue(value string) *nebulaTypes.Value {
	return &nebulaTypes.Value{SVal: []byte(value)}
}

func intNebulaValue(value int64) *nebulaTypes.Value {
	return &nebulaTypes.Value{IVal: &value}
}

func boolNebulaValue(value bool) *nebulaTypes.Value {
	return &nebulaTypes.Value{BVal: &value}
}
