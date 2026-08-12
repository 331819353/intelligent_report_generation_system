package askdatahttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
)

type httpImportReader struct {
	value registryimport.SemanticImport
	err   error
}

func (reader *httpImportReader) Get(
	_ context.Context, _, _, _ string,
) (registryimport.SemanticImport, error) {
	return reader.value, reader.err
}

func TestImportStatusUsesAuthenticatedScope(t *testing.T) {
	scope := testAdminScope()
	importID := uuid.NewString()
	reader := &httpImportReader{value: registryimport.SemanticImport{
		ID: importID, TenantID: scope.TenantID, DomainID: scope.DomainID,
		AssetType: registryimport.AssetEvalCase, State: registryimport.StateValidated,
		TotalRows: 12, ValidRows: 12,
	}}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil, ImportMutationServices{Reads: reader},
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/askdata/semantic/imports/"+importID, nil,
	))
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"state":"VALIDATED"`) ||
		!strings.Contains(response.Body.String(), `"validRows":12`) {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
}

func TestImportTemplateDownloadUsesAuthenticatedDomainAndSafeHeaders(t *testing.T) {
	scope := testAdminScope()
	service := registryimport.NewTemplateService(httpTemplateCatalog{references: []registryimport.TemplateReference{
		{AssetType: "MODEL", Code: "sales", Name: "销售模型", ID: uuid.NewString()},
	}})
	handler := newProtectedAdminHandler(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		service,
	)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/askdata/semantic/imports/template?assetType=METRIC&domainId="+
			scope.DomainID+"&format=xlsx",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("content type = %q", contentType)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "askdata-metric-template.xlsx") {
		t.Fatalf("content disposition = %q", disposition)
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
	if body := response.Body.Bytes(); len(body) < 4 || string(body[:2]) != "PK" {
		t.Fatalf("XLSX body is invalid: %x", body)
	}
}

func TestImportTemplateDownloadRejectsScopeAndQueryAmbiguity(t *testing.T) {
	scope := testAdminScope()
	service := registryimport.NewTemplateService(httpTemplateCatalog{})
	handler := newProtectedAdminHandler(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		service,
	)
	tests := []struct {
		query  string
		status int
		code   string
	}{
		{
			"assetType=METRIC&domainId=" + uuid.NewString() + "&format=xlsx",
			http.StatusForbidden, "IMPORT_PERMISSION_DENIED",
		},
		{
			"assetType=metric&domainId=" + scope.DomainID + "&format=xlsx",
			http.StatusBadRequest, "IMPORT_TEMPLATE_INVALID_REQUEST",
		},
		{
			"assetType=METRIC&assetType=MODEL&domainId=" + scope.DomainID + "&format=xlsx",
			http.StatusBadRequest, "IMPORT_TEMPLATE_INVALID_REQUEST",
		},
		{
			"assetType=METRIC&domainId=" + scope.DomainID + "&format=pdf",
			http.StatusBadRequest, "IMPORT_TEMPLATE_INVALID_REQUEST",
		},
		{
			"assetType=METRIC&domainId=" + scope.DomainID + "&format=csv&extra=true",
			http.StatusBadRequest, "IMPORT_TEMPLATE_INVALID_REQUEST",
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/askdata/semantic/imports/template?"+test.query,
			nil,
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Errorf("query %q response = %d/%s", test.query, response.Code, response.Body.String())
		}
	}
}

func TestImportValidationReportDownloadUsesAuthenticatedScopeAndSafeHeaders(t *testing.T) {
	scope := testAdminScope()
	importID := uuid.NewString()
	definition, err := registryimport.TemplateDefinitionFor(registryimport.AssetMetric)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, column := range definition.Columns {
		values[column.Name] = ""
	}
	raw, _ := json.Marshal(values)
	reportStore := httpReportStore{
		batch: registryimport.SemanticImport{
			ID: importID, TenantID: scope.TenantID, DomainID: scope.DomainID,
			AssetType: registryimport.AssetMetric, State: registryimport.StateValidated, TotalRows: 1,
		},
		rows: []registryimport.ImportRow{{RowNo: 1, RawJSON: raw, ValidationState: registryimport.RowInvalid,
			Errors: []registryimport.ValidationIssue{{
				Column: "formula", Code: registryimport.ImportFormulaInvalid,
				Message: "invalid formula", Expected: "safe AST", Actual: "sql",
			}}}},
	}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil,
		nil,
		registryimport.NewReportService(reportStore),
	)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/askdata/semantic/imports/"+importID+"/report?format=xlsx",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("content type = %q", contentType)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, importID) {
		t.Fatalf("content disposition = %q", disposition)
	}
	if body := response.Body.Bytes(); len(body) < 4 || string(body[:2]) != "PK" {
		t.Fatalf("XLSX body is invalid: %x", body)
	}
}

func TestImportValidationReportRejectsAmbiguousRequest(t *testing.T) {
	scope := testAdminScope()
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil,
		nil,
		registryimport.NewReportService(httpReportStore{}),
	)
	for _, target := range []string{
		"/api/v1/askdata/semantic/imports/not-a-uuid/report?format=xlsx",
		"/api/v1/askdata/semantic/imports/" + uuid.NewString() + "/report?format=csv",
		"/api/v1/askdata/semantic/imports/" + uuid.NewString() + "/report?format=xlsx&format=xlsx",
		"/api/v1/askdata/semantic/imports/" + uuid.NewString() + "/report?format=xlsx&extra=true",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s response = %d/%s", target, response.Code, response.Body.String())
		}
	}
}

func TestImportUploadStoresFileAndReturnsUploadedBatch(t *testing.T) {
	scope := testAdminScope()
	storage := &httpUploadStorage{}
	creator := &httpUploadCreator{}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil,
		registryimport.NewUploadService(storage, creator, "uploads"),
		nil,
	)
	request := newImportUploadRequest(t, scope.DomainID, "METRIC", "metrics.csv", []byte("code,name\na,A\n"), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"state":"UPLOADED"`) {
		t.Fatalf("response = %d/%s", response.Code, response.Body.String())
	}
	if storage.key == "" || creator.input.TenantID != scope.TenantID ||
		creator.input.DomainID != scope.DomainID || creator.input.CreatedBy != scope.ActorID {
		t.Fatalf("storage/create = %q/%#v", storage.key, creator.input)
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
}

func TestImportUploadRejectsDomainMismatchAndAmbiguousMultipart(t *testing.T) {
	scope := testAdminScope()
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil,
		registryimport.NewUploadService(&httpUploadStorage{}, &httpUploadCreator{}, "uploads"),
		nil,
	)
	tests := []struct {
		domain string
		asset  string
		extra  map[string]string
		status int
	}{
		{uuid.NewString(), "METRIC", nil, http.StatusForbidden},
		{scope.DomainID, "metric", nil, http.StatusBadRequest},
		{scope.DomainID, "METRIC", map[string]string{"extra": "true"}, http.StatusBadRequest},
	}
	for _, test := range tests {
		request := newImportUploadRequest(t, test.domain, test.asset, "metrics.csv", []byte("x"), test.extra)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("domain/asset/extra = %s/%s/%v: %d/%s", test.domain, test.asset, test.extra, response.Code, response.Body.String())
		}
	}
}

func TestImportMutationRoutesUseAuthenticatedScopeAndReturnStructuredResults(t *testing.T) {
	scope := testAdminScope()
	importID, objectID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	commit := &httpCommitter{result: registryimport.CommitResult{
		ImportID: importID, State: registryimport.StateCommitted,
		Committed: []registryimport.DraftReference{{
			ObjectID: objectID, VersionID: versionID, Status: "DRAFT",
		}},
	}}
	withdraw := &httpWithdrawer{result: registryimport.WithdrawResult{
		ImportID: importID, State: registryimport.StateWithdrawn,
		Rejected: []registryimport.WithdrawalRejection{{
			RowNo: 3, VersionID: versionID, Reason: "VERSION_NOT_DRAFT",
		}},
	}}
	certify := &httpBulkCertifier{result: registry.BulkCertificationResult{
		Certified: []string{versionID},
	}}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{},
		func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil,
		ImportMutationServices{Commit: commit, Withdraw: withdraw, Certify: certify},
	)

	commitRequest := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/imports/"+importID+"/commit",
		strings.NewReader(`{"all":true,"acknowledgeImpact":true}`))
	commitRequest.Header.Set("Content-Type", "application/json")
	commitResponse := httptest.NewRecorder()
	handler.ServeHTTP(commitResponse, commitRequest)
	if commitResponse.Code != http.StatusOK || commit.input.TenantID != scope.TenantID ||
		commit.input.DomainID != scope.DomainID || commit.input.ActorID != scope.ActorID ||
		!commit.input.All || !commit.input.AcknowledgeImpact ||
		!strings.Contains(commitResponse.Body.String(), versionID) {
		t.Fatalf("commit response/input = %d/%s %#v", commitResponse.Code, commitResponse.Body.String(), commit.input)
	}

	withdrawRequest := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/imports/"+importID+"/withdraw",
		strings.NewReader(`{"reason":"incorrect source mapping"}`))
	withdrawRequest.Header.Set("Content-Type", "application/json")
	withdrawResponse := httptest.NewRecorder()
	handler.ServeHTTP(withdrawResponse, withdrawRequest)
	if withdrawResponse.Code != http.StatusOK || withdraw.input.Reason != "incorrect source mapping" ||
		withdraw.input.ActorID != scope.ActorID ||
		!strings.Contains(withdrawResponse.Body.String(), "VERSION_NOT_DRAFT") {
		t.Fatalf("withdraw response/input = %d/%s %#v", withdrawResponse.Code, withdrawResponse.Body.String(), withdraw.input)
	}

	certifyRequest := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/bulk-certify",
		strings.NewReader(`{"domainId":"`+scope.DomainID+`","objectVersionIds":["`+versionID+`"],"note":"reviewed"}`))
	certifyRequest.Header.Set("Content-Type", "application/json")
	certifyResponse := httptest.NewRecorder()
	handler.ServeHTTP(certifyResponse, certifyRequest)
	if certifyResponse.Code != http.StatusOK || certify.scope != scope || certify.domainID != scope.DomainID ||
		len(certify.versionIDs) != 1 || certify.versionIDs[0] != versionID || certify.note != "reviewed" {
		t.Fatalf("certify response/input = %d/%s %#v", certifyResponse.Code, certifyResponse.Body.String(), certify)
	}
}

func TestBulkCertificationConflictResponseIncludesEveryCandidate(t *testing.T) {
	versionID, firstConflict, secondConflict := uuid.NewString(), uuid.NewString(), uuid.NewString()
	response := httptest.NewRecorder()
	writeImportError(response, &registry.BulkCertificationError{Failures: []registry.CertificationFailure{{
		ObjectVersionID: versionID,
		Code:            "TERM_PRIORITY_CONFLICT",
		Message:         "TERM_PRIORITY_CONFLICT: overlapping approved term points at another target",
		Conflicts: []registry.TermConflictCandidate{
			{ObjectVersionID: firstConflict, TargetVersionID: uuid.NewString(), Priority: 100, SamePriority: true},
			{ObjectVersionID: secondConflict, TargetVersionID: uuid.NewString(), Priority: 100, SamePriority: true},
		},
	}}})
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":"TERM_PRIORITY_CONFLICT"`) ||
		!strings.Contains(response.Body.String(), firstConflict) ||
		!strings.Contains(response.Body.String(), secondConflict) {
		t.Fatalf("bulk conflict response = %d/%s", response.Code, response.Body.String())
	}
}

func TestImportCommitReturnsFailingRowAndRequiresExactJSONContract(t *testing.T) {
	scope := testAdminScope()
	importID := uuid.NewString()
	commit := &httpCommitter{err: &registryimport.RowOperationError{
		RowNo: 7, Err: registryimport.ErrImportImpactAck,
	}}
	handler := newProtectedAdminHandlerWithImports(
		&fakeAdminBackend{}, func(context.Context) (registry.AdminScope, error) { return scope, nil },
		nil, nil, nil, ImportMutationServices{Commit: commit},
	)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/imports/"+importID+"/commit",
		strings.NewReader(`{"rowNos":[7],"acknowledgeImpact":false}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"IMPORT_IMPACT_ACK_REQUIRED"`) ||
		!strings.Contains(response.Body.String(), `"rowNo":7`) {
		t.Fatalf("impact response = %d/%s", response.Code, response.Body.String())
	}

	unknown := httptest.NewRequest(http.MethodPost,
		"/api/v1/askdata/semantic/imports/"+importID+"/commit",
		strings.NewReader(`{"all":true,"skipStaticValidation":true}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field response = %d/%s", unknownResponse.Code, unknownResponse.Body.String())
	}
}

func newImportUploadRequest(
	t *testing.T,
	domainID, assetType, filename string,
	payload []byte,
	extra map[string]string,
) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("domainId", domainID); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("assetType", assetType); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/askdata/semantic/imports", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

type httpTemplateCatalog struct {
	references []registryimport.TemplateReference
	err        error
}

type httpReportStore struct {
	batch registryimport.SemanticImport
	rows  []registryimport.ImportRow
	err   error
}

type httpUploadStorage struct{ key string }

func (storage *httpUploadStorage) Put(
	_ context.Context,
	_, key string,
	body io.Reader,
	_ int64,
	_ string,
) error {
	storage.key = key
	_, _ = io.Copy(io.Discard, body)
	return nil
}

type httpUploadCreator struct {
	input registryimport.CreateImportInput
}

type httpCommitter struct {
	input  registryimport.CommitInput
	result registryimport.CommitResult
	err    error
}

func (service *httpCommitter) Commit(
	_ context.Context,
	input registryimport.CommitInput,
) (registryimport.CommitResult, error) {
	service.input = input
	return service.result, service.err
}

type httpWithdrawer struct {
	input  registryimport.WithdrawInput
	result registryimport.WithdrawResult
	err    error
}

func (service *httpWithdrawer) Withdraw(
	_ context.Context,
	input registryimport.WithdrawInput,
) (registryimport.WithdrawResult, error) {
	service.input = input
	return service.result, service.err
}

type httpBulkCertifier struct {
	scope      registry.AdminScope
	domainID   string
	versionIDs []string
	note       string
	result     registry.BulkCertificationResult
	err        error
}

func (service *httpBulkCertifier) BulkCertify(
	_ context.Context,
	scope registry.AdminScope,
	domainID string,
	versionIDs []string,
	note string,
) (registry.BulkCertificationResult, error) {
	service.scope, service.domainID, service.note = scope, domainID, note
	service.versionIDs = append([]string(nil), versionIDs...)
	return service.result, service.err
}

func (creator *httpUploadCreator) CreateImport(
	_ context.Context,
	input registryimport.CreateImportInput,
) (registryimport.SemanticImport, bool, error) {
	creator.input = input
	return registryimport.SemanticImport{
		ID: uuid.NewString(), TenantID: input.TenantID, DomainID: input.DomainID,
		AssetType: input.AssetType, FileObjectURI: input.FileObjectURI,
		FileHash: input.FileHash, FileName: input.FileName, State: registryimport.StateUploaded,
		CreatedBy: input.CreatedBy,
	}, true, nil
}

func (store httpReportStore) Get(context.Context, string, string, string) (registryimport.SemanticImport, error) {
	return store.batch, store.err
}

func (store httpReportStore) ListRows(context.Context, string, string, string) ([]registryimport.ImportRow, error) {
	return append([]registryimport.ImportRow(nil), store.rows...), store.err
}

func (catalog httpTemplateCatalog) ListTemplateReferences(
	context.Context,
	string,
	string,
) ([]registryimport.TemplateReference, error) {
	return append([]registryimport.TemplateReference(nil), catalog.references...), catalog.err
}
