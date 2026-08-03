package semanticgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestBuildProjectionCreatesVersionedVerticesAndCertifiedEdges(t *testing.T) {
	manifest := projectionManifestForTest()
	projection, err := BuildProjection(manifest)
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if len(projection.Vertices) != 4 || len(projection.Edges) != 3 {
		t.Fatalf("unexpected projection: %#v", projection)
	}
	for _, vertex := range projection.Vertices {
		if len(vertex.VID) > 128 || vertex.Props["semantic_version"] != manifest.SemanticVersion ||
			vertex.Props["tenant_scope"] != manifest.TenantID {
			t.Fatalf("unsafe vertex: %#v", vertex)
		}
	}
	for _, edge := range projection.Edges {
		if edge.Type == "" || edge.FromVID == edge.ToVID || edge.Props["certified"] != true {
			t.Fatalf("unsafe edge: %#v", edge)
		}
	}
}

func TestBuildProjectionRejectsAmbiguousRelationEndpoint(t *testing.T) {
	manifest := projectionManifestForTest()
	manifest.Objects = append(manifest.Objects, releaseObjectForTest(
		"ENTITY", "region", `{"title":"区域","grain":"region","primaryKey":"id"}`,
	))
	manifest.Objects = append(manifest.Objects, releaseObjectForTest(
		"RELATION", "ambiguous", `{
			"relationType":"belongs_to","fromId":"region","toId":"orders",
			"certified":true,"allowedForQuery":true,"cardinality":"many_to_one"
		}`,
	))
	if _, err := BuildProjection(manifest); err == nil {
		t.Fatal("ambiguous endpoint was accepted")
	}
}

func TestProjectorRequiresCountAndOrphanVerification(t *testing.T) {
	manifest := projectionManifestForTest()
	sink := &projectionSinkForTest{}
	projector := NewProjector(sink)
	verification, err := projector.Project(context.Background(), manifest)
	if err != nil {
		t.Fatalf("project manifest: %v", err)
	}
	if verification.VertexCount != len(sink.vertices) || verification.EdgeCount != len(sink.edges) {
		t.Fatalf("verification mismatch: %#v", verification)
	}
	sink.orphans = 1
	if _, err := projector.Project(context.Background(), manifest); err == nil {
		t.Fatal("orphaned projection was accepted")
	}
}

type projectionSinkForTest struct {
	vertices []Vertex
	edges    []Edge
	orphans  int
}

func (sink *projectionSinkForTest) UpsertVertex(_ context.Context, vertex Vertex) error {
	sink.vertices = append(sink.vertices, vertex)
	return nil
}

func (sink *projectionSinkForTest) UpsertEdge(_ context.Context, edge Edge) error {
	sink.edges = append(sink.edges, edge)
	return nil
}

func (sink *projectionSinkForTest) Verify(_ context.Context, projection Projection) (ProjectionVerification, error) {
	return ProjectionVerification{VertexCount: len(projection.Vertices), EdgeCount: len(projection.Edges), OrphanCount: sink.orphans}, nil
}

func projectionManifestForTest() ReleaseManifest {
	objects := []ReleaseObject{
		releaseObjectForTest("METRIC", "paid_gmv", `{
			"title":"支付GMV","sourceDatasetIds":["orders"],
			"groupableDimensionIds":["region"]
		}`),
		releaseObjectForTest("DATASET", "orders", `{"title":"订单"}`),
		releaseObjectForTest("DIMENSION", "region", `{"title":"区域"}`),
		releaseObjectForTest("DIMENSION_VALUE", "east", `{
			"title":"华东","dimensionId":"region","canonicalCode":"EAST"
		}`),
	}
	return ReleaseManifest{
		TenantID: "11111111-1111-4111-8111-111111111111", ReleaseID: "release-1",
		SemanticVersion: "commerce-1", ContentHash: strings64("manifest"), Objects: objects,
	}
}

func releaseObjectForTest(objectType, objectID, contract string) ReleaseObject {
	return ReleaseObject{ObjectType: objectType, ObjectID: objectID, ObjectVersion: "1",
		DomainID: "commerce", ContentHash: strings64(objectID), Certification: "CERTIFIED",
		Sensitivity: "INTERNAL", ValidFrom: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		Contract: json.RawMessage(contract)}
}

func strings64(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
