package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type projectionStoreFixture struct {
	claim           *ProjectionClaim
	snapshot        ProjectionSnapshot
	claimErr        error
	loadErr         error
	heartbeatErr    error
	completeErr     error
	heartbeats      int
	completed       *ProjectionProof
	failedCode      string
	failedRetryable bool
}

func (store *projectionStoreFixture) ListTenantIDs(context.Context) ([]string, error) {
	return []string{store.claim.TenantID}, nil
}

func (store *projectionStoreFixture) Claim(context.Context, string, string, time.Duration) (*ProjectionClaim, error) {
	return store.claim, store.claimErr
}

func (store *projectionStoreFixture) LoadGraphSnapshot(context.Context, ProjectionClaim, string) (ProjectionSnapshot, error) {
	return store.snapshot, store.loadErr
}

func (store *projectionStoreFixture) Heartbeat(context.Context, ProjectionClaim, string, time.Duration) error {
	store.heartbeats++
	return store.heartbeatErr
}

func (store *projectionStoreFixture) Complete(_ context.Context, _ ProjectionClaim, _ string, proof ProjectionProof) error {
	store.completed = &proof
	return store.completeErr
}

func (store *projectionStoreFixture) Fail(_ context.Context, _ ProjectionClaim, _ string, code string, retryable bool) error {
	store.failedCode, store.failedRetryable = code, retryable
	return nil
}

type projectionWriterFixture struct {
	proof      ProjectionProof
	err        error
	heartbeats int
}

func (writer *projectionWriterFixture) Apply(
	ctx context.Context, _ ProjectionSnapshot, heartbeat func(context.Context) error,
) (ProjectionProof, error) {
	writer.heartbeats++
	if err := heartbeat(ctx); err != nil {
		return ProjectionProof{}, err
	}
	return writer.proof, writer.err
}

func TestProjectionSnapshotProofIsDeterministic(t *testing.T) {
	snapshot := graphProjectionFixture(t)
	left, err := snapshot.Proof()
	if err != nil {
		t.Fatal(err)
	}
	reversed := snapshot
	reversed.Vertices = reverseVertices(snapshot.Vertices)
	reversed.Edges = reverseEdges(snapshot.Edges)
	right, err := reversed.Proof()
	if err != nil {
		t.Fatal(err)
	}
	if left != right || left.ObjectCount != len(snapshot.Vertices)+len(snapshot.Edges) {
		t.Fatalf("projection proofs differ: left=%#v right=%#v", left, right)
	}
	changed := snapshot
	changed.Edges = append([]ProjectionEdge(nil), snapshot.Edges...)
	changed.Edges[len(changed.Edges)-1].FanoutPolicy = registry.FanoutBlock
	next, err := changed.Proof()
	if err != nil {
		t.Fatal(err)
	}
	if next.GraphHash == left.GraphHash {
		t.Fatal("join policy change did not change graph proof")
	}
}

func TestProjectorCompletesOnlyMatchingCanonicalProof(t *testing.T) {
	snapshot := graphProjectionFixture(t)
	proof, err := snapshot.Proof()
	if err != nil {
		t.Fatal(err)
	}
	store := &projectionStoreFixture{claim: projectionClaimFixture(snapshot), snapshot: snapshot}
	writer := &projectionWriterFixture{proof: proof}
	projector, err := NewProjector(store, writer)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := projector.ProcessNext(
		context.Background(), string(snapshot.TenantID), "projector-1", 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !processed || store.heartbeats != 1 || writer.heartbeats != 1 ||
		store.completed == nil || *store.completed != proof || store.failedCode != "" {
		t.Fatalf("unexpected projector result: store=%#v writer=%#v", store, writer)
	}
}

func TestProjectorClassifiesRetryAndProofFailures(t *testing.T) {
	snapshot := graphProjectionFixture(t)
	proof, err := snapshot.Proof()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		writer        *projectionWriterFixture
		wantCode      string
		wantRetryable bool
	}{
		{
			name: "transport is retryable", writer: &projectionWriterFixture{err: ErrProjectionMutation},
			wantCode: "NEBULA_PROJECTION_UNAVAILABLE", wantRetryable: true,
		},
		{
			name: "proof mismatch is terminal", writer: &projectionWriterFixture{proof: ProjectionProof{
				SchemaVersion: GraphProjectionSchemaVersion, GraphHash: askdata.HashBytes([]byte("wrong")),
				ObjectCount: proof.ObjectCount, VertexCount: proof.VertexCount, EdgeCount: proof.EdgeCount,
			}},
			wantCode: "GRAPH_PROJECTION_PROOF_MISMATCH", wantRetryable: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &projectionStoreFixture{claim: projectionClaimFixture(snapshot), snapshot: snapshot}
			projector, err := NewProjector(store, test.writer)
			if err != nil {
				t.Fatal(err)
			}
			processed, processErr := projector.ProcessNext(
				context.Background(), string(snapshot.TenantID), "projector-1", 30*time.Second,
			)
			if !processed || processErr == nil || store.failedCode != test.wantCode ||
				store.failedRetryable != test.wantRetryable || store.completed != nil {
				t.Fatalf("processed=%v err=%v store=%#v", processed, processErr, store)
			}
		})
	}
}

func TestProjectionContractRejectsMissingEndpointsAndInjectedIDs(t *testing.T) {
	snapshot := graphProjectionFixture(t)
	snapshot.Vertices = snapshot.Vertices[1:]
	if err := snapshot.Validate(); !errors.Is(err, ErrProjectionContract) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	snapshot = graphProjectionFixture(t)
	snapshot.Vertices[0].Ref.ObjectID = `bad\"; DROP SPACE askdata; --`
	if err := snapshot.Validate(); !errors.Is(err, ErrProjectionContract) {
		t.Fatalf("injected object ID error = %v", err)
	}
}

func TestProjectionMutationBuildersUseFrozenIdentifiersAndParameters(t *testing.T) {
	snapshot := graphProjectionFixture(t).normalized()
	metricVertices := []ProjectionVertex{}
	for _, vertex := range snapshot.Vertices {
		if vertex.Type == ObjectTypeMetric {
			metricVertices = append(metricVertices, vertex)
		}
	}
	statement, parameters, err := buildVertexMutation(snapshot, metricVertices)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statement, "INSERT VERTEX metric(") || strings.Contains(statement, string(snapshot.ContentHash)) ||
		parameters["release_hash"] != string(snapshot.ContentHash) {
		t.Fatalf("unexpected vertex mutation: %s %#v", statement, parameters)
	}
	var join ProjectionEdge
	for _, edge := range snapshot.Edges {
		if edge.Type == ProjectionEdgeJoinsTo {
			join = edge
			break
		}
	}
	statement, parameters, err = buildEdgeMutation(snapshot, []ProjectionEdge{join})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statement, "INSERT EDGE JOINS_TO(") || !strings.Contains(statement, "@") ||
		parameters["relationship_0"] != string(join.RelationshipVersionID) {
		t.Fatalf("unexpected edge mutation: %s %#v", statement, parameters)
	}
}

func TestParseNebulaAddressesFailsClosed(t *testing.T) {
	addresses, err := parseNebulaAddresses("127.0.0.1:9669,graphd.internal:9669")
	if err != nil || len(addresses) != 2 {
		t.Fatalf("addresses=%#v err=%v", addresses, err)
	}
	for _, value := range []string{"", "graphd", "graphd:0", "graphd:70000", "graphd:9669,graphd:bad"} {
		if _, err := parseNebulaAddresses(value); err == nil {
			t.Fatalf("accepted invalid addresses %q", value)
		}
	}
}

func projectionClaimFixture(snapshot ProjectionSnapshot) *ProjectionClaim {
	return &ProjectionClaim{
		ProjectionID: "77777777-7777-4777-8777-777777777777",
		TenantID:     string(snapshot.TenantID), DomainID: string(snapshot.DomainID),
		ReleaseID: string(snapshot.ReleaseID), Target: ProjectionTargetNebula,
		SemanticVersion: snapshot.SemanticVersion, ContentHash: string(snapshot.ContentHash),
		LeaseToken: "88888888-8888-4888-8888-888888888888", Attempt: 1,
	}
}

func graphProjectionFixture(t *testing.T) ProjectionSnapshot {
	t.Helper()
	ref := func(objectID, versionID string, version int) ObjectVersionRef {
		return ObjectVersionRef{ObjectID: askdata.ID(objectID), VersionID: askdata.ID(versionID), Version: version}
	}
	modelOrders := ref("10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001", 1)
	modelLines := ref("10000000-0000-4000-8000-000000000002", "20000000-0000-4000-8000-000000000002", 1)
	metricOrders := ref("30000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001", 1)
	metricRevenue := ref("30000000-0000-4000-8000-000000000002", "40000000-0000-4000-8000-000000000002", 1)
	dimension := ref("50000000-0000-4000-8000-000000000001", "60000000-0000-4000-8000-000000000001", 1)
	member := ref("50000000-0000-4000-8000-000000000002", "60000000-0000-4000-8000-000000000002", 1)
	return ProjectionSnapshot{
		TenantID:        "00000000-0000-4000-8000-000000000001",
		DomainID:        "00000000-0000-4000-8000-000000000002",
		ReleaseID:       "00000000-0000-4000-8000-000000000003",
		SemanticVersion: "fixture-v1", ContentHash: askdata.HashBytes([]byte("graph-projection-fixture")),
		ManifestCount: 7,
		Vertices: []ProjectionVertex{
			{Type: ObjectTypeMetric, Ref: metricOrders}, {Type: ObjectTypeMetric, Ref: metricRevenue},
			{Type: ObjectTypeSemanticModel, Ref: modelOrders}, {Type: ObjectTypeSemanticModel, Ref: modelLines},
			{Type: ObjectTypeDimension, Ref: dimension},
			{Type: ObjectTypeMember, Ref: member, MemberStatus: MemberStatusActive},
		},
		Edges: []ProjectionEdge{
			{Type: ProjectionEdgeModeledBy, FromType: ObjectTypeMetric, From: metricOrders, ToType: ObjectTypeSemanticModel, To: modelOrders},
			{Type: ProjectionEdgeModeledBy, FromType: ObjectTypeMetric, From: metricRevenue, ToType: ObjectTypeSemanticModel, To: modelLines},
			{Type: ProjectionEdgeHasDimension, FromType: ObjectTypeSemanticModel, From: modelOrders, ToType: ObjectTypeDimension, To: dimension},
			{Type: ProjectionEdgeHasMember, FromType: ObjectTypeDimension, From: dimension, ToType: ObjectTypeMember, To: member},
			{
				Type: ProjectionEdgeJoinsTo, FromType: ObjectTypeSemanticModel, From: modelOrders,
				ToType: ObjectTypeSemanticModel, To: modelLines,
				RelationshipVersionID: "70000000-0000-4000-8000-000000000001",
				JoinType:              registry.JoinInner, Cardinality: registry.CardinalityOneToMany,
				FanoutPolicy: registry.FanoutCertifiedPre, Certified: true,
			},
		},
	}
}

func reverseVertices(values []ProjectionVertex) []ProjectionVertex {
	result := append([]ProjectionVertex(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseEdges(values []ProjectionEdge) []ProjectionEdge {
	result := append([]ProjectionEdge(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
