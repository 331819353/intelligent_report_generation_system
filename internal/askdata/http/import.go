package askdatahttp

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
)

type semanticExportResponse struct {
	ID                      string                        `json:"id"`
	Status                  registryimport.ExportJobState `json:"status"`
	SourceRowCount          int                           `json:"sourceRowCount"`
	RowCount                int                           `json:"rowCount,omitempty"`
	OmittedSensitiveMembers int                           `json:"omittedSensitiveMembers,omitempty"`
	ContentHash             string                        `json:"contentHash,omitempty"`
	FailureCode             string                        `json:"failureCode,omitempty"`
	StatusURL               string                        `json:"statusUrl"`
	DownloadURL             string                        `json:"downloadUrl"`
	ExpiresAt               string                        `json:"expiresAt"`
}

func (handler *AdminHandler) requestSemanticExport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	selection, err := parseSemanticExportSelection(request, scope)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	rowCount, err := handler.export.Count(request.Context(), selection)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	if rowCount <= registryimport.MaxSynchronousExportRows {
		artifact, err := handler.export.Generate(request.Context(), selection)
		if err != nil {
			writeImportError(writer, err)
			return
		}
		if artifact.RowCount <= registryimport.MaxSynchronousExportRows {
			writeSemanticExportArtifact(writer, artifact)
			return
		}
	}
	handler.enqueueSemanticExport(writer, request, selection)
}

func (handler *AdminHandler) enqueueSemanticExport(
	writer http.ResponseWriter,
	request *http.Request,
	selection registryimport.ExportSelection,
) {
	if handler.exportJobs == nil {
		writeImportError(writer, registryimport.ErrExportAsyncRequired)
		return
	}
	job, err := handler.exportJobs.Create(request.Context(), registryimport.CreateExportJobInput{
		Selection: selection,
	})
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, exportJobResponse(job))
}

func (handler *AdminHandler) getSemanticExport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	jobID, ok := parseSemanticExportJobRequest(writer, request)
	if !ok {
		return
	}
	job, err := handler.exportJobs.Get(
		request.Context(), scope.TenantID, scope.DomainID, scope.ActorID, jobID,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, exportJobResponse(job))
}

func (handler *AdminHandler) downloadSemanticExport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	jobID, ok := parseSemanticExportJobRequest(writer, request)
	if !ok {
		return
	}
	job, err := handler.exportJobs.Get(
		request.Context(), scope.TenantID, scope.DomainID, scope.ActorID, jobID,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	switch job.EffectiveState(time.Now().UTC()) {
	case registryimport.ExportExpired:
		writeImportError(writer, registryimport.ErrExportExpired)
		return
	case registryimport.ExportReady:
	default:
		writeImportError(writer, registryimport.ErrExportNotReady)
		return
	}
	bucket, key, err := semanticExportObjectLocation(job.ObjectURI)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	body, err := handler.exportArtifacts.Get(request.Context(), bucket, key)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	defer body.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": "askdata-semantic-export-" + job.ID + ".xlsx",
	})
	if disposition == "" {
		writeImportError(writer, registryimport.ErrExportContract)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("X-Content-SHA256", job.ContentHash)
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, body)
}

func parseSemanticExportSelection(
	request *http.Request,
	scope registry.AdminScope,
) (registryimport.ExportSelection, error) {
	query := request.URL.Query()
	want := 3
	if _, exists := query["releaseId"]; exists {
		want = 4
	}
	if len(query) != want || len(query["domainId"]) != 1 ||
		len(query["assetTypes"]) != 1 || len(query["format"]) != 1 ||
		(want == 4 && len(query["releaseId"]) != 1) || query.Get("format") != "xlsx" {
		return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
	}
	domainID := query.Get("domainId")
	parsedDomain, err := uuid.Parse(domainID)
	if err != nil || parsedDomain.String() != domainID || domainID != scope.DomainID {
		return registryimport.ExportSelection{}, registry.ErrRegistryPermissionDenied
	}
	rawTypes := query.Get("assetTypes")
	parts := strings.Split(rawTypes, ",")
	if rawTypes == "" || len(parts) > registryimport.MaxExportAssetTypes {
		return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
	}
	assetTypes := make([]registryimport.AssetType, 0, len(parts))
	seen := map[registryimport.AssetType]struct{}{}
	for _, rawType := range parts {
		assetType := registryimport.AssetType(rawType)
		if rawType == "" || rawType != strings.ToUpper(rawType) ||
			strings.TrimSpace(rawType) != rawType || !assetType.Valid() {
			return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
		}
		if _, duplicate := seen[assetType]; duplicate {
			return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
		}
		seen[assetType] = struct{}{}
		assetTypes = append(assetTypes, assetType)
	}
	releaseID := query.Get("releaseId")
	if releaseID != "" {
		parsedRelease, err := uuid.Parse(releaseID)
		if err != nil || parsedRelease.String() != releaseID {
			return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
		}
	} else if want == 4 {
		return registryimport.ExportSelection{}, registryimport.ErrExportInvalid
	}
	return registryimport.ExportSelection{
		TenantID: scope.TenantID, DomainID: domainID, ActorID: scope.ActorID,
		ReleaseID: releaseID, AssetTypes: registryimport.CanonicalAssetTypes(assetTypes),
	}, nil
}

func parseSemanticExportJobRequest(
	writer http.ResponseWriter,
	request *http.Request,
) (string, bool) {
	if request.URL.RawQuery != "" {
		writeImportError(writer, registryimport.ErrExportInvalid)
		return "", false
	}
	jobID := request.PathValue("id")
	parsed, err := uuid.Parse(jobID)
	if err != nil || parsed.String() != jobID {
		writeImportError(writer, registryimport.ErrExportInvalid)
		return "", false
	}
	return jobID, true
}

func exportJobResponse(job registryimport.SemanticExportJob) semanticExportResponse {
	base := "/api/v1/askdata/semantic/exports/" + job.ID
	return semanticExportResponse{
		ID: job.ID, Status: job.EffectiveState(time.Now().UTC()),
		SourceRowCount: job.SourceRowCount, RowCount: job.RowCount,
		OmittedSensitiveMembers: job.OmittedSensitiveMembers,
		ContentHash:             job.ContentHash, FailureCode: job.FailureCode,
		StatusURL: base, DownloadURL: base + "/download",
		ExpiresAt: job.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func writeSemanticExportArtifact(writer http.ResponseWriter, artifact registryimport.ExportArtifact) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Filename})
	if disposition == "" {
		writeImportError(writer, registryimport.ErrExportContract)
		return
	}
	writer.Header().Set("Content-Type", artifact.ContentType)
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("Content-Length", strconv.Itoa(len(artifact.Bytes)))
	writer.Header().Set("X-Content-SHA256", artifact.ContentHash)
	writer.Header().Set("X-Omitted-Sensitive-Members", strconv.Itoa(artifact.OmittedSensitiveMembers))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(artifact.Bytes)
}

func semanticExportObjectLocation(objectURI string) (string, string, error) {
	if !strings.HasPrefix(objectURI, "s3://") {
		return "", "", registryimport.ErrExportContract
	}
	parts := strings.SplitN(strings.TrimPrefix(objectURI, "s3://"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.ContainsAny(parts[0]+parts[1], "\r\n\x00") {
		return "", "", registryimport.ErrExportContract
	}
	return parts[0], parts[1], nil
}

type commitImportRequest struct {
	RowNumbers        []int `json:"rowNos,omitempty"`
	All               bool  `json:"all,omitempty"`
	AcknowledgeImpact bool  `json:"acknowledgeImpact"`
}

type withdrawImportRequest struct {
	Reason string `json:"reason"`
}

type bulkCertifyRequest struct {
	DomainID         string   `json:"domainId"`
	ObjectVersionIDs []string `json:"objectVersionIds"`
	Note             string   `json:"note"`
}

func (handler *AdminHandler) getImport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" || handler.importReads == nil {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	importID := request.PathValue("id")
	parsedID, err := uuid.Parse(importID)
	if err != nil || parsedID.String() != importID {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	result, err := handler.importReads.Get(
		request.Context(), scope.TenantID, scope.DomainID, importID,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) commitImport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	importID := request.PathValue("id")
	parsedID, err := uuid.Parse(importID)
	if err != nil || parsedID.String() != importID {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	var input commitImportRequest
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	result, err := handler.commit.Commit(request.Context(), registryimport.CommitInput{
		TenantID: scope.TenantID, DomainID: scope.DomainID, ActorID: scope.ActorID,
		ImportID: importID, RowNumbers: input.RowNumbers, All: input.All,
		AcknowledgeImpact: input.AcknowledgeImpact,
	})
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) withdrawImport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	importID := request.PathValue("id")
	parsedID, err := uuid.Parse(importID)
	if err != nil || parsedID.String() != importID {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	var input withdrawImportRequest
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeImportError(writer, registryimport.ErrInvalidImport)
		return
	}
	result, err := handler.withdraw.Withdraw(request.Context(), registryimport.WithdrawInput{
		TenantID: scope.TenantID, DomainID: scope.DomainID, ActorID: scope.ActorID,
		ImportID: importID, Reason: input.Reason,
	})
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) bulkCertify(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeImportError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input bulkCertifyRequest
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeImportError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	if input.DomainID != scope.DomainID {
		writeImportError(writer, registry.ErrRegistryPermissionDenied)
		return
	}
	result, err := handler.certify.BulkCertify(
		request.Context(), scope, input.DomainID, input.ObjectVersionIDs, input.Note,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

const importMultipartOverheadBytes int64 = 1 << 20

func (handler *AdminHandler) uploadImport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeImportError(writer, registryimport.ErrImportUploadInvalid)
		return
	}
	request.Body = http.MaxBytesReader(
		writer, request.Body, registryimport.MaxImportUploadBytes+importMultipartOverheadBytes,
	)
	if err := request.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeImportError(writer, registryimport.ErrImportUploadTooLarge)
			return
		}
		writeImportError(writer, registryimport.ErrImportUploadInvalid)
		return
	}
	defer request.MultipartForm.RemoveAll()
	if len(request.MultipartForm.Value) != 2 ||
		len(request.MultipartForm.Value["assetType"]) != 1 ||
		len(request.MultipartForm.Value["domainId"]) != 1 ||
		len(request.MultipartForm.File) != 1 ||
		len(request.MultipartForm.File["file"]) != 1 {
		writeImportError(writer, registryimport.ErrImportUploadInvalid)
		return
	}
	domainID := request.FormValue("domainId")
	parsedDomain, err := uuid.Parse(domainID)
	if err != nil || parsedDomain.String() != domainID || domainID != scope.DomainID {
		writeImportError(writer, registry.ErrRegistryPermissionDenied)
		return
	}
	rawAssetType := request.FormValue("assetType")
	assetType := registryimport.AssetType(rawAssetType)
	if rawAssetType != strings.ToUpper(rawAssetType) || !assetType.Valid() {
		writeImportError(writer, registryimport.ErrImportUploadInvalid)
		return
	}
	header := request.MultipartForm.File["file"][0]
	if header.Size < 1 || header.Size > registryimport.MaxImportUploadBytes {
		writeImportError(writer, registryimport.ErrImportUploadTooLarge)
		return
	}
	file, err := header.Open()
	if err != nil {
		writeImportError(writer, registryimport.ErrImportUploadInvalid)
		return
	}
	defer file.Close()
	result, err := handler.upload.Upload(request.Context(), registryimport.UploadInput{
		TenantID: scope.TenantID, DomainID: scope.DomainID, ActorID: scope.ActorID,
		AssetType: assetType, Filename: header.Filename,
		ContentType: header.Header.Get("Content-Type"), Size: header.Size, Body: file,
	})
	if err != nil {
		writeImportError(writer, err)
		return
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *AdminHandler) downloadImportTemplate(
	writer http.ResponseWriter,
	request *http.Request,
) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	if len(query) != 3 || len(query["assetType"]) != 1 ||
		len(query["domainId"]) != 1 || len(query["format"]) != 1 {
		writeImportError(writer, registryimport.ErrTemplateInvalidRequest)
		return
	}
	domainID := query.Get("domainId")
	parsedDomain, err := uuid.Parse(domainID)
	if err != nil || parsedDomain.String() != domainID || domainID != scope.DomainID {
		writeImportError(writer, registry.ErrRegistryPermissionDenied)
		return
	}
	rawAssetType := query.Get("assetType")
	assetType := registryimport.AssetType(rawAssetType)
	if rawAssetType != strings.ToUpper(rawAssetType) || !assetType.Valid() {
		writeImportError(writer, registryimport.ErrTemplateInvalidRequest)
		return
	}
	format := registryimport.TemplateFormat(query.Get("format"))
	if !format.Valid() {
		writeImportError(writer, registryimport.ErrTemplateInvalidRequest)
		return
	}
	artifact, err := handler.template.Generate(
		request.Context(), scope.TenantID, scope.DomainID, assetType, format,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": artifact.Filename,
	})
	if disposition == "" {
		writeImportError(writer, registryimport.ErrTemplateInvalidRequest)
		return
	}
	writer.Header().Set("Content-Type", artifact.ContentType)
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("Content-Length", strconv.Itoa(len(artifact.Bytes)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(artifact.Bytes)
}

func (handler *AdminHandler) downloadImportReport(
	writer http.ResponseWriter,
	request *http.Request,
) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	if len(query) != 1 || len(query["format"]) != 1 || query.Get("format") != "xlsx" {
		writeImportError(writer, registryimport.ErrImportReportInvalid)
		return
	}
	importID := request.PathValue("id")
	parsedID, err := uuid.Parse(importID)
	if err != nil || parsedID.String() != importID {
		writeImportError(writer, registryimport.ErrImportReportInvalid)
		return
	}
	artifact, err := handler.report.Generate(
		request.Context(), scope.TenantID, scope.DomainID, importID,
	)
	if err != nil {
		writeImportError(writer, err)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": artifact.Filename,
	})
	if disposition == "" {
		writeImportError(writer, registryimport.ErrImportReportInvalid)
		return
	}
	writer.Header().Set("Content-Type", artifact.ContentType)
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("Content-Length", strconv.Itoa(len(artifact.Bytes)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(artifact.Bytes)
}

func writeImportError(writer http.ResponseWriter, err error) {
	var rowError *registryimport.RowOperationError
	var certificationError *registry.BulkCertificationError
	switch {
	case errors.As(err, &certificationError):
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"code":     "BULK_CERTIFICATION_REJECTED",
			"message":  "one or more semantic versions failed static validation",
			"failures": certificationError.Failures,
		})
	case errors.As(err, &rowError):
		code := "IMPORT_COMMIT_ROW_FAILED"
		message := "semantic import row could not be committed"
		if errors.Is(rowError, registryimport.ErrImportImpactAck) {
			code = "IMPORT_IMPACT_ACK_REQUIRED"
			message = "semantic import impact review must be acknowledged"
		}
		writeJSON(writer, http.StatusConflict, map[string]any{
			"code": code, "message": message, "rowNo": rowError.RowNo,
		})
	case errors.Is(err, registryimport.ErrExportExpired):
		writeError(writer, http.StatusGone, "SEMANTIC_EXPORT_EXPIRED", "semantic export artifact has expired")
	case errors.Is(err, registryimport.ErrExportNotReady):
		writeError(writer, http.StatusConflict, "SEMANTIC_EXPORT_NOT_READY", "semantic export artifact is not ready")
	case errors.Is(err, registryimport.ErrExportNotFound):
		writeError(writer, http.StatusNotFound, "SEMANTIC_EXPORT_NOT_FOUND", "semantic export or release was not found in the authenticated domain")
	case errors.Is(err, registryimport.ErrExportTooLarge):
		writeError(writer, http.StatusUnprocessableEntity, "SEMANTIC_EXPORT_TOO_LARGE", "semantic export exceeds the 100000 row limit")
	case errors.Is(err, registryimport.ErrExportInvalid):
		writeError(writer, http.StatusBadRequest, "SEMANTIC_EXPORT_INVALID_REQUEST", "semantic export request is invalid")
	case errors.Is(err, registryimport.ErrExportAsyncRequired):
		writeError(writer, http.StatusServiceUnavailable, "SEMANTIC_EXPORT_ASYNC_UNAVAILABLE", "asynchronous semantic export is unavailable")
	case errors.Is(err, registryimport.ErrExportContract):
		writeError(writer, http.StatusUnprocessableEntity, "SEMANTIC_EXPORT_CONTRACT_FAILED", "semantic assets cannot be represented by the import workbook contract")
	case errors.Is(err, registryimport.ErrImportPermission):
		writeError(writer, http.StatusForbidden, "IMPORT_OWNER_REQUIRED", "domain owner permission is required")
	case errors.Is(err, registryimport.ErrIllegalTransition):
		writeError(writer, http.StatusConflict, "IMPORT_STATE_CONFLICT", "semantic import is not ready for this operation")
	case errors.Is(err, registry.ErrBulkCertificationInvalid):
		writeError(writer, http.StatusBadRequest, "BULK_CERTIFICATION_INVALID", "bulk certification request is invalid")
	case errors.Is(err, registryimport.ErrImportNotFound):
		writeError(
			writer, http.StatusNotFound, "IMPORT_NOT_FOUND",
			"semantic import was not found in the authenticated domain",
		)
	case errors.Is(err, registryimport.ErrImportReportNotReady):
		writeError(
			writer, http.StatusConflict, "IMPORT_REPORT_NOT_READY",
			"semantic import validation report is not ready",
		)
	case errors.Is(err, registryimport.ErrImportConflict):
		writeError(
			writer, http.StatusConflict, "IMPORT_CONFLICT",
			"semantic import conflicts with existing immutable facts",
		)
	case errors.Is(err, registry.ErrRegistryPermissionDenied):
		writeError(
			writer, http.StatusForbidden, "IMPORT_PERMISSION_DENIED",
			"the selected business domain is outside the authenticated scope",
		)
	case errors.Is(err, registryimport.ErrImportReportInvalid):
		writeError(
			writer, http.StatusBadRequest, "IMPORT_REPORT_INVALID_REQUEST",
			"semantic import validation report request is invalid",
		)
	case errors.Is(err, registryimport.ErrImportUploadTooLarge):
		writeError(
			writer, http.StatusRequestEntityTooLarge, "IMPORT_FILE_TOO_LARGE",
			"semantic import file exceeds the 50 MiB limit",
		)
	case errors.Is(err, registryimport.ErrImportUploadInvalid):
		writeError(
			writer, http.StatusBadRequest, "IMPORT_UPLOAD_INVALID",
			"semantic import upload request is invalid",
		)
	case errors.Is(err, registryimport.ErrTemplateInvalidRequest),
		errors.Is(err, registryimport.ErrInvalidImport):
		writeError(
			writer, http.StatusBadRequest, "IMPORT_TEMPLATE_INVALID_REQUEST",
			"semantic import template request is invalid",
		)
	case errors.Is(err, registryimport.ErrTemplateTooLarge):
		writeError(
			writer, http.StatusUnprocessableEntity, "IMPORT_TEMPLATE_REFERENCE_LIMIT",
			"the business domain reference catalog exceeds the template limit",
		)
	default:
		writeError(
			writer, http.StatusInternalServerError, "IMPORT_TEMPLATE_FAILED",
			"semantic import template generation failed",
		)
	}
}
