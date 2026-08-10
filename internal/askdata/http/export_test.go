package askdatahttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
)

func TestSemanticExportReturnsSynchronousWorkbookWithAuthenticatedSelection(t *testing.T) {
	scope := testAdminScope()
	releaseID := uuid.NewString()
	exporter := &httpSemanticExporter{
		count: 1,
		artifact: registryimport.ExportArtifact{
			Filename:    "askdata-semantic-release.xlsx",
			ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			Bytes:       []byte("PK-export"), ContentHash: strings.Repeat("a", 64),
			OmittedSensitiveMembers: 2,
		},
	}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{Export: exporter},
	)
	target := "/api/v1/askdata/semantic/exports?domainId=" + scope.DomainID +
		"&assetTypes=METRIC,DIMENSION&releaseId=" + releaseID + "&format=xlsx"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusOK || response.Body.String() != "PK-export" {
		t.Fatalf("response = %d/%q", response.Code, response.Body.String())
	}
	if exporter.selection.TenantID != scope.TenantID ||
		exporter.selection.DomainID != scope.DomainID ||
		exporter.selection.ActorID != scope.ActorID ||
		exporter.selection.ReleaseID != releaseID ||
		len(exporter.selection.AssetTypes) != 2 ||
		exporter.selection.AssetTypes[0] != registryimport.AssetMetric ||
		exporter.selection.AssetTypes[1] != registryimport.AssetDimension {
		t.Fatalf("selection = %#v", exporter.selection)
	}
	if response.Header().Get("X-Content-SHA256") != strings.Repeat("a", 64) ||
		response.Header().Get("X-Omitted-Sensitive-Members") != "2" {
		t.Fatalf("export headers = %#v", response.Header())
	}
}

func TestSemanticExportLargeRequestCreatesPinnedAsyncJob(t *testing.T) {
	scope := testAdminScope()
	jobID := uuid.NewString()
	exporter := &httpSemanticExporter{count: registryimport.MaxSynchronousExportRows + 1}
	jobs := &httpExportJobs{job: registryimport.SemanticExportJob{
		ID: jobID, TenantID: scope.TenantID, DomainID: scope.DomainID,
		State: registryimport.ExportPending, SourceRowCount: 5001,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{Export: exporter, ExportJobs: jobs},
	)
	target := "/api/v1/askdata/semantic/exports?domainId=" + scope.DomainID +
		"&assetTypes=MEMBER&format=xlsx"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusAccepted ||
		!strings.Contains(response.Body.String(), `"status":"PENDING"`) ||
		!strings.Contains(response.Body.String(), "/exports/"+jobID+"/download") {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
	if jobs.input.Selection.ActorID != scope.ActorID ||
		jobs.input.Selection.AssetTypes[0] != registryimport.AssetMember {
		t.Fatalf("job input = %#v", jobs.input)
	}
}

func TestSemanticExportStatusAndDownloadAreDomainScoped(t *testing.T) {
	scope := testAdminScope()
	jobID := uuid.NewString()
	jobs := &httpExportJobs{job: registryimport.SemanticExportJob{
		ID: jobID, TenantID: scope.TenantID, DomainID: scope.DomainID,
		State: registryimport.ExportReady, SourceRowCount: 3, RowCount: 2,
		OmittedSensitiveMembers: 1, ContentHash: strings.Repeat("b", 64),
		ObjectURI: "s3://uploads/semantic-exports/result.xlsx",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}}
	storage := &httpExportStorage{body: []byte("PK-ready")}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{ExportJobs: jobs, ExportArtifacts: storage},
	)

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(
		http.MethodGet, "/api/v1/askdata/semantic/exports/"+jobID, nil,
	))
	if statusResponse.Code != http.StatusOK ||
		!strings.Contains(statusResponse.Body.String(), `"omittedSensitiveMembers":1`) {
		t.Fatalf("status = %d/%s", statusResponse.Code, statusResponse.Body.String())
	}

	downloadResponse := httptest.NewRecorder()
	handler.ServeHTTP(downloadResponse, httptest.NewRequest(
		http.MethodGet, "/api/v1/askdata/semantic/exports/"+jobID+"/download", nil,
	))
	if downloadResponse.Code != http.StatusOK || downloadResponse.Body.String() != "PK-ready" ||
		storage.bucket != "uploads" || storage.key != "semantic-exports/result.xlsx" ||
		jobs.tenantID != scope.TenantID || jobs.domainID != scope.DomainID || jobs.actorID != scope.ActorID {
		t.Fatalf("download = %d/%q storage=%s/%s scope=%s/%s",
			downloadResponse.Code, downloadResponse.Body.String(), storage.bucket,
			storage.key, jobs.tenantID, jobs.domainID)
	}
}

func TestSemanticExportRejectsAmbiguousOrCrossDomainQueries(t *testing.T) {
	scope := testAdminScope()
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{Export: &httpSemanticExporter{}},
	)
	tests := []struct {
		query  string
		status int
	}{
		{"domainId=" + uuid.NewString() + "&assetTypes=METRIC&format=xlsx", http.StatusForbidden},
		{"domainId=" + scope.DomainID + "&assetTypes=metric&format=xlsx", http.StatusBadRequest},
		{"domainId=" + scope.DomainID + "&assetTypes=METRIC,METRIC&format=xlsx", http.StatusBadRequest},
		{"domainId=" + scope.DomainID + "&assetTypes=METRIC&format=csv", http.StatusBadRequest},
		{"domainId=" + scope.DomainID + "&assetTypes=METRIC&assetTypes=MODEL&format=xlsx", http.StatusBadRequest},
		{"domainId=" + scope.DomainID + "&assetTypes=METRIC&format=xlsx&extra=true", http.StatusBadRequest},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/askdata/semantic/exports?"+test.query, nil,
		))
		if response.Code != test.status {
			t.Errorf("query %q = %d/%s", test.query, response.Code, response.Body.String())
		}
	}
}

func TestSemanticExportExpiredArtifactReturnsGone(t *testing.T) {
	scope := testAdminScope()
	jobID := uuid.NewString()
	jobs := &httpExportJobs{job: registryimport.SemanticExportJob{
		ID: jobID, State: registryimport.ExportReady,
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{ExportJobs: jobs, ExportArtifacts: &httpExportStorage{}},
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/askdata/semantic/exports/"+jobID+"/download", nil,
	))
	if response.Code != http.StatusGone ||
		!strings.Contains(response.Body.String(), "SEMANTIC_EXPORT_EXPIRED") {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
}

type httpSemanticExporter struct {
	count     int
	artifact  registryimport.ExportArtifact
	err       error
	selection registryimport.ExportSelection
}

func (exporter *httpSemanticExporter) Count(
	_ context.Context,
	selection registryimport.ExportSelection,
) (int, error) {
	exporter.selection = selection
	return exporter.count, exporter.err
}

func (exporter *httpSemanticExporter) Generate(
	_ context.Context,
	selection registryimport.ExportSelection,
) (registryimport.ExportArtifact, error) {
	exporter.selection = selection
	return exporter.artifact, exporter.err
}

type httpExportJobs struct {
	job                                registryimport.SemanticExportJob
	input                              registryimport.CreateExportJobInput
	tenantID, domainID, actorID, jobID string
	err                                error
}

func (jobs *httpExportJobs) Create(
	_ context.Context,
	input registryimport.CreateExportJobInput,
) (registryimport.SemanticExportJob, error) {
	jobs.input = input
	return jobs.job, jobs.err
}

func (jobs *httpExportJobs) Get(
	_ context.Context,
	tenantID, domainID, actorID, jobID string,
) (registryimport.SemanticExportJob, error) {
	jobs.tenantID, jobs.domainID, jobs.actorID, jobs.jobID = tenantID, domainID, actorID, jobID
	return jobs.job, jobs.err
}

type httpExportStorage struct {
	bucket, key string
	body        []byte
	err         error
}

func (storage *httpExportStorage) Get(
	_ context.Context,
	bucket, key string,
) (io.ReadCloser, error) {
	storage.bucket, storage.key = bucket, key
	return io.NopCloser(bytes.NewReader(storage.body)), storage.err
}
