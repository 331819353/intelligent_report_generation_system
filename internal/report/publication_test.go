package report

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/reportjson"
)

type memoryArtifactStore struct{ objects map[string][]byte }

func (s *memoryArtifactStore) Put(_ context.Context, bucket, key string, body io.Reader, size int64, _ string) error {
	payload, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(payload)) != size {
		return io.ErrUnexpectedEOF
	}
	s.objects[bucket+"/"+key] = bytes.Clone(payload)
	return nil
}

func (s *memoryArtifactStore) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	payload, exists := s.objects[bucket+"/"+key]
	if !exists {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func TestBuildPublishedArtifactFreezesDependenciesWithoutMutatingDraft(t *testing.T) {
	draft := readPublicationFixture(t)
	published, err := buildPublishedArtifact(draft, 7, draft.Hash, []DependencySnapshot{
		{Type: "METRIC_VERSION", ID: "metric_revenue_v2", VersionID: "87d46537-d9f6-47af-bba3-959e91e77737", Path: "dataRequirements[0].resolvedMetricIds[0]"},
		{Type: "DATASET_VERSION", ID: "dsv_enterprise_revenue_v3", VersionID: "5f36af70-516d-401c-a95e-6176ef746734", Path: "dataRequirements[0].resolvedDatasetVersionId"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Document.Report.Status != "DRAFT" {
		t.Fatal("publishing mutated the draft document")
	}
	if published.Document.Report.Status != "PUBLISHED" || published.Hash == draft.Hash {
		t.Fatal("published artifact did not receive an independent immutable identity")
	}
	publication, ok := published.Document.Extensions["publication"].(map[string]any)
	if !ok {
		t.Fatal("publication snapshot is missing")
	}
	metrics, ok := publication["metricVersions"].(map[string]any)
	if !ok || metrics["metric_revenue_v2"] != "87d46537-d9f6-47af-bba3-959e91e77737" {
		t.Fatalf("metric version was not frozen: %#v", publication["metricVersions"])
	}
}

func TestPublicationSecurityRejectsTenantAndExecutablePayloads(t *testing.T) {
	draft := readPublicationFixture(t)
	document := draft.Document
	document.Extensions = map[string]any{
		"tenantId": "other-tenant",
		"link":     "javascript:alert(1)",
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := reportjson.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	issues := validatePublishableDocument(prepared.Document, prepared.JSON)
	securityIssues := 0
	for _, issue := range issues {
		if issue.Code == "REPORT_SECURITY_INVALID" {
			securityIssues++
		}
	}
	if securityIssues != 2 {
		t.Fatalf("expected two security issues, got %#v", issues)
	}
}

func TestPublishedArtifactUsesContentAddressedObject(t *testing.T) {
	artifact := readPublicationFixture(t)
	store := &memoryArtifactStore{objects: map[string][]byte{}}
	service := NewService(nil)
	service.SetArtifactStore(store, "reports")

	uri, err := service.persistPublishedArtifact(context.Background(), "tenant-a", "report-a", artifact)
	if err != nil {
		t.Fatal(err)
	}
	expectedSuffix := "/artifacts/" + artifact.Hash + ".json"
	if !strings.HasPrefix(uri, "s3://reports/tenants/tenant-a/reports/report-a/") || !strings.HasSuffix(uri, expectedSuffix) {
		t.Fatalf("unexpected artifact URI %q", uri)
	}
	payload, err := service.readPublishedArtifact(context.Background(), uri, int64(len(artifact.JSON)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, artifact.JSON) {
		t.Fatal("object storage did not preserve the canonical artifact bytes")
	}
}

func TestPublishedArtifactRejectsUnexpectedBucketOrSize(t *testing.T) {
	artifact := readPublicationFixture(t)
	store := &memoryArtifactStore{objects: map[string][]byte{}}
	service := NewService(nil)
	service.SetArtifactStore(store, "reports")
	uri, err := service.persistPublishedArtifact(context.Background(), "tenant-a", "report-a", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.readPublishedArtifact(context.Background(), strings.Replace(uri, "s3://reports/", "s3://other/", 1), int64(len(artifact.JSON))); err != ErrArtifactCorrupt {
		t.Fatalf("expected corrupt URI error, got %v", err)
	}
	if _, err := service.readPublishedArtifact(context.Background(), uri, int64(len(artifact.JSON)-1)); err != ErrArtifactCorrupt {
		t.Fatalf("expected corrupt size error, got %v", err)
	}
}

func readPublicationFixture(t *testing.T) reportjson.Prepared {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/report-json-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := reportjson.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
