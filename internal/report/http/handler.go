package reporthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
	reportmodel "intelligent-report-generation-system/internal/report"
	reportasset "intelligent-report-generation-system/internal/report/asset"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/publication"
	"intelligent-report-generation-system/internal/report/reportai"
	"intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/sharing"
	"intelligent-report-generation-system/internal/report/store"
	"intelligent-report-generation-system/internal/report/template"
)

type AIOptions struct {
	PlanGenerator reportai.PlanGenerator
	EditGenerator reportai.ScopedEditGenerator
	Fields        reportai.FieldCatalog
	Components    *template.Registry
	Methods       *insight.Registry
	Runtime       runtime.QueryExecutor
	Upgrade       *publication.UpgradeService
}

type Handler struct {
	repository *store.PostgresStore
	publisher  *publication.Publisher
	loader     runtime.Loader
	shares     sharing.Service
	exports    *publication.ExportJobStore
	insights   *insight.PostgresStore
	aiAudit    *reportai.PostgresStore
	assets     reportasset.Service
	assetRepo  *reportasset.PostgresRepository
	ai         AIOptions
}

func NewHandler(
	authService *auth.Service,
	idempotencyRepository platformidempotency.Repository,
	repository *store.PostgresStore,
	publisher *publication.Publisher,
	loader runtime.Loader,
	shares sharing.Service,
	exports *publication.ExportJobStore,
	insights *insight.PostgresStore,
	aiAudit *reportai.PostgresStore,
	assets reportasset.Service,
	aiOptions ...AIOptions,
) http.Handler {
	configuredAI := AIOptions{}
	if len(aiOptions) == 1 {
		configuredAI = aiOptions[0]
	}
	handler := &Handler{repository: repository, publisher: publisher, loader: loader, shares: shares, exports: exports, insights: insights, aiAudit: aiAudit, assets: assets, assetRepo: assets.Repository, ai: configuredAI}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/reports", handler.list)
	mux.HandleFunc("POST /api/v1/reports", handler.create)
	mux.HandleFunc("GET /api/v1/reports/{id}", handler.get)
	mux.HandleFunc("GET /api/v1/reports/{id}/draft", handler.getDraft)
	mux.HandleFunc("GET /api/v1/reports/{id}/revisions", handler.listRevisions)
	mux.HandleFunc("POST /api/v1/reports/{id}/operations", handler.operations)
	mux.HandleFunc("POST /api/v1/reports/{id}/ai/plan", handler.aiPlan)
	mux.HandleFunc("POST /api/v1/reports/{id}/ai/preview", handler.aiPreview)
	mux.HandleFunc("POST /api/v1/reports/{id}/undo", handler.undo)
	mux.HandleFunc("POST /api/v1/reports/{id}/redo", handler.redo)
	mux.HandleFunc("POST /api/v1/reports/{id}/publish", handler.publish)
	mux.HandleFunc("POST /api/v1/reports/{id}/rollback", handler.rollback)
	mux.HandleFunc("GET /api/v1/reports/{id}/versions", handler.listVersions)
	mux.HandleFunc("GET /api/v1/reports/{id}/versions/{versionNo}", handler.getVersion)
	mux.HandleFunc("GET /api/v1/reports/{id}/runtime", handler.loadRuntime)
	mux.HandleFunc("POST /api/v1/reports/{id}/runtime/plan", handler.runtimePlan)
	mux.HandleFunc("POST /api/v1/reports/{id}/runtime/execute", handler.runtimeExecute)
	mux.HandleFunc("POST /api/v1/reports/{id}/upgrade/preview", handler.upgradePreview)
	mux.HandleFunc("POST /api/v1/reports/{id}/upgrade/confirm", handler.upgradeConfirm)
	mux.HandleFunc("GET /api/v1/reports/{id}/permissions", handler.listPermissions)
	mux.HandleFunc("POST /api/v1/reports/{id}/permissions", handler.grantPermission)
	mux.HandleFunc("DELETE /api/v1/reports/{id}/permissions/{grantId}", handler.revokePermission)
	mux.HandleFunc("POST /api/v1/reports/{id}/archive", handler.archiveReport)
	mux.HandleFunc("POST /api/v1/reports/{id}/restore", handler.restoreReport)
	mux.HandleFunc("GET /api/v1/reports/{id}/asset-events", handler.listAssetEvents)
	mux.HandleFunc("POST /api/v1/reports/{id}/shares", handler.createShare)
	mux.HandleFunc("POST /api/v1/reports/{id}/shares/{shareId}/revoke", handler.revokeShare)
	mux.HandleFunc("GET /api/v1/report-shares/{token}", handler.accessShare)
	mux.HandleFunc("POST /api/v1/reports/{id}/exports", handler.createExport)
	mux.HandleFunc("GET /api/v1/reports/{id}/exports/{exportId}", handler.getExport)
	mux.HandleFunc("POST /api/v1/reports/{id}/exports/{exportId}/retry", handler.retryExport)
	mux.HandleFunc("POST /api/v1/reports/{id}/insights/evidence", handler.saveEvidence)
	mux.HandleFunc("GET /api/v1/reports/{id}/insights/{componentId}", handler.getInsight)
	mux.HandleFunc("POST /api/v1/reports/{id}/insights/{componentId}/edit", handler.editInsight)
	governed := WithIdempotency(idempotencyRepository, func(ctx context.Context) (platformidempotency.Identity, error) {
		identity, err := identityFromContext(ctx)
		return platformidempotency.Identity{TenantID: string(identity.TenantID), ActorID: string(identity.ActorID)}, err
	}, mux)
	return auth.RequireAccessToken(authService, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		governed.ServeHTTP(writer, request)
	}))
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "limit is invalid")
			return
		}
		limit = parsed
	}
	if handler.assetRepo == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_ASSET_UNAVAILABLE", "report asset service is unavailable")
		return
	}
	items, err := handler.assetRepo.List(request.Context(), identity, reportasset.ListQuery{
		Scope:      request.URL.Query().Get("scope"),
		Lifecycle:  reportasset.Lifecycle(request.URL.Query().Get("lifecycle")),
		OwnerID:    askdata.ID(request.URL.Query().Get("ownerId")),
		ReportType: reportmodel.ReportType(request.URL.Query().Get("reportType")),
		Search:     request.URL.Query().Get("search"),
		Cursor:     request.URL.Query().Get("cursor"),
		Limit:      limit,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, items)
}

func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	var body struct {
		Definition reportmodel.ReportDefinition `json:"definition"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	report, draft, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: body.Definition.Metadata.ID, Code: body.Definition.Metadata.Code,
		Name: body.Definition.Metadata.Name, ReportType: body.Definition.Metadata.ReportType,
		Definition: body.Definition,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"report": report, "draft": draft})
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	item, err := handler.repository.GetReport(request.Context(), identity, id)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) getDraft(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	item, err := handler.repository.GetDraft(request.Context(), identity, id)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) listRevisions(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.ListRevisions(request.Context(), identity, id, 500)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) operations(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var bundle operation.Bundle
	if decodeJSON(request, &bundle) != nil || bundle.ReportID != id || bundle.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_OPERATION_INVALID", "operation bundle is invalid")
		return
	}
	if bundle.Source == operation.SourceAI {
		if handler.aiAudit == nil || bundle.AIRunID == nil {
			writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_AUDIT_UNAVAILABLE", "report AI audit is unavailable")
			return
		}
	}
	aiRunID := askdata.ID("")
	if bundle.AIRunID != nil {
		aiRunID = *bundle.AIRunID
	}
	draft, revision, err := handler.repository.SaveDraftWithRevision(request.Context(), identity, id, store.SaveInput{
		ExpectedRevision: bundle.BaseRevision, Operations: bundle.Operations,
		Source: string(bundle.Source), AIRunID: aiRunID, Scope: bundle.Scope,
	})
	if err != nil {
		if bundle.Source == operation.SourceAI && shouldAuditAIRejection(err) {
			if auditErr := handler.recordRejectedAI(request.Context(), identity, *bundle.AIRunID, bundle.Operations, "REPORT_AI_OPERATION_REJECTED"); auditErr != nil {
				writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI rejection audit failed")
				return
			}
		}
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"draft": draft, "revision": revision})
}

func shouldAuditAIRejection(err error) bool {
	if operation.ErrorCode(err) != "" {
		return true
	}
	var applyError *operation.ApplyError
	if errors.As(err, &applyError) {
		return true
	}
	var validation compiler.ValidationIssues
	return errors.As(err, &validation)
}

func (handler *Handler) recordRejectedAI(ctx context.Context, identity store.Identity, runID askdata.ID, operations []operation.Operation, code string) error {
	for _, item := range operations {
		if _, err := handler.aiAudit.RecordOperation(ctx, identity, runID, item, code, nil); err != nil {
			return err
		}
	}
	return handler.aiAudit.FinishRun(ctx, identity, runID, reportai.RunRejected,
		map[string]any{"operationCount": len(operations)}, code)
}

func (handler *Handler) aiPlan(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if !handler.reportAIReady(false) {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI is unavailable")
		return
	}
	var body struct {
		Intent        string     `json:"intent"`
		DataContextID askdata.ID `json:"dataContextId"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "report AI plan request is invalid")
		return
	}
	draft, err := handler.repository.GetDraft(request.Context(), identity, reportID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	dataContext, fields, err := handler.resolveAIFields(request.Context(), identity, draft.Definition, body.DataContextID)
	if err != nil || len(fields) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_FIELDS_UNAVAILABLE", "no governed fields are available for report AI")
		return
	}
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunPlan, PromptVersion: "report-plan-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{
			Intent: body.Intent, SelectionIDs: []string{string(dataContext.ID)}, AvailableFields: fields,
		}, BaseRevision: &draft.RevisionNo,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	planRequest := reportai.PlanRequest{
		Intent: body.Intent, AllowedFieldNames: fields,
		AllowedComponents: reportAIComponentTypes(handler.ai.Components),
		AllowedMethods:    reportAIAnalysisMethods(handler.ai.Methods),
		TemplateVersions:  []string{"1.0.0"},
	}
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	plan, generationErr := reportai.GeneratePlan(ctx, handler.ai.PlanGenerator, planRequest, handler.ai.Components, handler.ai.Methods)
	if generationErr != nil {
		if handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunFailed,
			map[string]any{"stage": "plan"}, "REPORT_AI_PLAN_REJECTED") != nil {
			writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
			return
		}
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_PLAN_REJECTED", "report AI plan was rejected")
		return
	}
	if err := handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunSucceeded,
		map[string]any{"sectionCount": len(plan.Sections)}, ""); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"aiRunId": run.ID, "plan": plan})
}

func (handler *Handler) aiPreview(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if !handler.reportAIReady(true) {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI is unavailable")
		return
	}
	var body struct {
		Intent        string          `json:"intent"`
		DataContextID askdata.ID      `json:"dataContextId"`
		Scope         operation.Scope `json:"scope"`
	}
	if decodeJSON(request, &body) != nil || body.Scope.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "report AI preview request is invalid")
		return
	}
	draft, err := handler.repository.GetDraft(request.Context(), identity, reportID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	_, fields, err := handler.resolveAIFields(request.Context(), identity, draft.Definition, body.DataContextID)
	if err != nil || len(fields) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_FIELDS_UNAVAILABLE", "no governed fields are available for report AI")
		return
	}
	// Validate that the declared scope exists before creating a model run.
	if _, err := reportai.BuildScopedContext(draft.Definition, body.Scope, draft.RevisionNo, body.Intent, fields, handler.ai.Components); err != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_SCOPE_INVALID", "report AI scope is invalid")
		return
	}
	scopeJSON, _ := json.Marshal(body.Scope)
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunScopedEdit, PromptVersion: "report-scoped-edit-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{
			Intent: body.Intent, SelectionIDs: scopeSelectionIDs(body.Scope), AvailableFields: fields,
		}, BaseRevision: &draft.RevisionNo, Scope: scopeJSON,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	bounded, err := reportai.BuildScopedContext(
		draft.Definition, body.Scope, draft.RevisionNo, body.Intent, fields, handler.ai.Components, run.ID,
	)
	if err != nil {
		handler.finishAIError(writer, request.Context(), identity, run.ID, reportai.RunFailed, "REPORT_AI_SCOPE_INVALID")
		return
	}
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	bundle, generationErr := handler.ai.EditGenerator.GenerateScopedOperations(ctx, bounded)
	if generationErr != nil {
		handler.finishAIError(writer, request.Context(), identity, run.ID, reportai.RunFailed, "REPORT_AI_GENERATION_FAILED")
		return
	}
	preview, validationErr := reportai.PreviewBundle(draft.Definition, reportID, draft.RevisionNo, bundle, run.ID)
	if validationErr != nil {
		if err := handler.aiAudit.RejectPreview(request.Context(), identity, run.ID, bundle.Operations, "REPORT_AI_PREVIEW_REJECTED"); err != nil {
			handler.finishAIError(writer, request.Context(), identity, run.ID, reportai.RunRejected, "REPORT_AI_PREVIEW_REJECTED")
			return
		}
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_PREVIEW_REJECTED", "report AI preview was rejected")
		return
	}
	if err := handler.aiAudit.CompletePreview(request.Context(), identity, run.ID, bundle.Operations,
		map[string]any{"beforeHash": preview.BeforeHash, "afterHash": preview.AfterHash,
			"affectedComponentCount": len(preview.AffectedComponents)}); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"aiRunId": run.ID, "preview": preview})
}

func (handler *Handler) reportAIReady(scoped bool) bool {
	if handler == nil || handler.repository == nil || handler.aiAudit == nil || handler.ai.Fields == nil ||
		handler.ai.Components == nil || handler.ai.Methods == nil || handler.ai.PlanGenerator == nil {
		return false
	}
	return !scoped || handler.ai.EditGenerator != nil
}

func (handler *Handler) resolveAIFields(ctx context.Context, identity store.Identity, definition reportmodel.ReportDefinition, dataContextID askdata.ID) (reportmodel.DataContext, []string, error) {
	if dataContextID.Validate() != nil {
		return reportmodel.DataContext{}, nil, errors.New("invalid report AI data context")
	}
	for _, dataContext := range definition.DataContexts {
		if dataContext.ID == dataContextID {
			fields, err := handler.ai.Fields.AllowedFields(ctx, identity, dataContext)
			return dataContext, fields, err
		}
	}
	return reportmodel.DataContext{}, nil, errors.New("report AI data context is unavailable")
}

func (handler *Handler) finishAIError(writer http.ResponseWriter, ctx context.Context, identity store.Identity, runID askdata.ID, state reportai.RunState, code string) {
	if err := handler.aiAudit.FinishRun(ctx, identity, runID, state, map[string]any{"stage": "preview"}, code); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	writeError(writer, http.StatusUnprocessableEntity, code, "report AI preview failed")
}

func reportAIComponentTypes(registry *template.Registry) []string {
	values := []string{}
	for _, manifest := range registry.List() {
		found := false
		for _, value := range values {
			found = found || value == manifest.Type
		}
		if !found {
			values = append(values, manifest.Type)
		}
	}
	return values
}

func reportAIAnalysisMethods(registry *insight.Registry) []insight.AnalysisMethod {
	values := make([]insight.AnalysisMethod, 0, len(registry.List()))
	for _, method := range registry.List() {
		values = append(values, method.ID())
	}
	return values
}

func scopeSelectionIDs(scope operation.Scope) []string {
	values := []string{}
	for _, id := range []*askdata.ID{scope.PageID, scope.SectionID, scope.BlockID} {
		if id != nil {
			values = append(values, string(*id))
		}
	}
	return values
}

func (handler *Handler) undo(writer http.ResponseWriter, request *http.Request) {
	handler.undoRedo(writer, request, false)
}
func (handler *Handler) redo(writer http.ResponseWriter, request *http.Request) {
	handler.undoRedo(writer, request, true)
}
func (handler *Handler) undoRedo(writer http.ResponseWriter, request *http.Request, redo bool) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var draft store.Draft
	var revision store.Revision
	var err error
	if redo {
		draft, revision, err = handler.repository.Redo(request.Context(), identity, id)
	} else {
		draft, revision, err = handler.repository.Undo(request.Context(), identity, id)
	}
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"draft": draft, "revision": revision})
}

func (handler *Handler) publish(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		SourceRevisionNo         *int64              `json:"sourceRevisionNo,omitempty"`
		AcknowledgeStaleInsights bool                `json:"acknowledgeStaleInsights"`
		DesktopPreviewHash       askdata.ContentHash `json:"desktopPreviewHash"`
		MobilePreviewHash        askdata.ContentHash `json:"mobilePreviewHash"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	version, err := handler.publisher.Publish(request.Context(), identity, publication.PublishRequest{
		ReportID: id, SourceRevisionNo: body.SourceRevisionNo,
		AcknowledgeStaleInsights: body.AcknowledgeStaleInsights,
		DesktopPreviewHash:       body.DesktopPreviewHash, MobilePreviewHash: body.MobilePreviewHash,
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, version)
}

func (handler *Handler) rollback(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		TargetVersionNo          int    `json:"targetVersionNo"`
		Reason                   string `json:"reason"`
		AcknowledgeStaleInsights bool   `json:"acknowledgeStaleInsights"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	ctx := publication.WithIdempotencyKey(request.Context(), request.Header.Get("Idempotency-Key"))
	version, err := handler.publisher.Rollback(ctx, identity, id, body.TargetVersionNo, body.Reason, body.AcknowledgeStaleInsights)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, version)
}

func (handler *Handler) listVersions(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.ListVersions(request.Context(), identity, id, 500)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) getVersion(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	versionNo, err := strconv.Atoi(request.PathValue("versionNo"))
	if err != nil || versionNo < 1 {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "version number is invalid")
		return
	}
	item, err := handler.repository.GetVersion(request.Context(), identity, id, &versionNo)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) loadRuntime(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	versionNo, valid := optionalVersionNo(writer, request)
	if !valid {
		return
	}
	loaded, err := handler.loader.Load(request.Context(), identity, id, versionNo)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, loaded)
}

func (handler *Handler) runtimePlan(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	versionNo, valid := optionalVersionNo(writer, request)
	if !valid {
		return
	}
	loaded, err := handler.loader.Load(request.Context(), identity, id, versionNo)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	var body runtime.HTTPPlanInput
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	location, err := runtime.RuntimeTimezone(loaded.Definition)
	if err != nil {
		writeReportError(writer, runtime.NewError("REPORT_RUNTIME_TIMEZONE_INVALID", "report runtime timezone is invalid", err))
		return
	}
	asOf := time.Now().UTC()
	policyHash, err := runtime.ViewerPolicyHash(identity, loaded)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	resolved, err := body.Resolve(loaded.Definition, asOf, location, policyHash)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "report runtime request is invalid")
		return
	}
	plan, err := runtime.BuildExecutionPlan(loaded.Definition, resolved)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if err := runtime.PinExecutionVersion(&plan, loaded); err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"reportId": loaded.ReportID, "versionId": loaded.VersionID, "versionNo": loaded.VersionNo,
		"asOf": asOf, "timezone": location.String(), "plan": plan,
	})
}

func (handler *Handler) runtimeExecute(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.ai.Runtime == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_RUNTIME_UNAVAILABLE", "report runtime is unavailable")
		return
	}
	versionNo, valid := optionalVersionNo(writer, request)
	if !valid {
		return
	}
	loaded, err := handler.loader.Load(request.Context(), identity, id, versionNo)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	var body runtime.HTTPPlanInput
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	location, err := runtime.RuntimeTimezone(loaded.Definition)
	if err != nil {
		writeReportError(writer, runtime.NewError("REPORT_RUNTIME_TIMEZONE_INVALID", "report runtime timezone is invalid", err))
		return
	}
	asOf := time.Now().UTC()
	policyHash, err := runtime.ViewerPolicyHash(identity, loaded)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	resolved, err := body.Resolve(loaded.Definition, asOf, location, policyHash)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "report runtime request is invalid")
		return
	}
	plan, err := runtime.BuildExecutionPlan(loaded.Definition, resolved)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if err := runtime.PinExecutionVersion(&plan, loaded); err != nil {
		writeReportError(writer, err)
		return
	}
	executionContext := database.WithAccessContext(request.Context(), string(identity.ActorID), string(identity.DomainID))
	executionContext = runtime.WithViewerIdentity(executionContext, identity)
	results := runtime.ExecuteBatch(
		executionContext, plan, handler.ai.Runtime,
		loaded.Definition.RuntimePolicy.MaxConcurrentQueries,
	)
	writeJSON(writer, http.StatusOK, map[string]any{
		"reportId": loaded.ReportID, "versionId": loaded.VersionID, "versionNo": loaded.VersionNo,
		"asOf": asOf, "timezone": location.String(), "components": results,
	})
}

func (handler *Handler) upgradePreview(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.ai.Upgrade == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_UPGRADE_UNAVAILABLE", "report upgrade is unavailable")
		return
	}
	var spec publication.UpgradeSpec
	if decodeJSON(request, &spec) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_UPGRADE_INVALID", "report upgrade request is invalid")
		return
	}
	preview, err := handler.ai.Upgrade.Preview(request.Context(), identity, reportID, spec)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, preview)
}

func (handler *Handler) upgradeConfirm(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.ai.Upgrade == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_UPGRADE_UNAVAILABLE", "report upgrade is unavailable")
		return
	}
	var body struct {
		Spec              publication.UpgradeSpec `json:"spec"`
		ConfirmationToken askdata.ContentHash     `json:"confirmationToken"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_UPGRADE_INVALID", "report upgrade confirmation is invalid")
		return
	}
	draft, revision, err := handler.ai.Upgrade.Confirm(
		request.Context(), identity, reportID, body.Spec, body.ConfirmationToken,
	)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"draft": draft, "revision": revision})
}

func (handler *Handler) listPermissions(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.assetRepo == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_ASSET_UNAVAILABLE", "report asset service is unavailable")
		return
	}
	items, err := handler.assetRepo.ListPermissions(request.Context(), identity, reportID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) grantPermission(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.assetRepo == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_ASSET_UNAVAILABLE", "report asset service is unavailable")
		return
	}
	var body reportasset.GrantInput
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_PERMISSION_INVALID", "report permission request is invalid")
		return
	}
	item, created, err := handler.assetRepo.Grant(request.Context(), identity, reportID, body)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, item)
}

func (handler *Handler) revokePermission(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	grantID := askdata.ID(request.PathValue("grantId"))
	if grantID.Validate() != nil || handler.assetRepo == nil {
		writeError(writer, http.StatusBadRequest, "REPORT_PERMISSION_INVALID", "report permission grant is invalid")
		return
	}
	item, err := handler.assetRepo.Revoke(request.Context(), identity, reportID, grantID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"revoked": true, "grant": item})
}

func (handler *Handler) archiveReport(writer http.ResponseWriter, request *http.Request) {
	handler.transitionReport(writer, request, false)
}

func (handler *Handler) restoreReport(writer http.ResponseWriter, request *http.Request) {
	handler.transitionReport(writer, request, true)
}

func (handler *Handler) transitionReport(writer http.ResponseWriter, request *http.Request, restore bool) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_ASSET_REASON_INVALID", "report lifecycle reason is invalid")
		return
	}
	var err error
	if restore {
		err = handler.assets.Restore(request.Context(), identity, reportID, body.Reason)
	} else {
		err = handler.assets.Archive(request.Context(), identity, reportID, body.Reason)
	}
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"reportId": reportID, "status": map[bool]string{true: "ACTIVE", false: "ARCHIVED"}[restore]})
}

func (handler *Handler) listAssetEvents(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.assetRepo == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_ASSET_UNAVAILABLE", "report asset service is unavailable")
		return
	}
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "REPORT_ASSET_QUERY_INVALID", "event limit is invalid")
			return
		}
		limit = parsed
	}
	items, err := handler.assetRepo.ListEvents(request.Context(), identity, reportID, limit)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) createShare(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		ReportVersionID askdata.ID        `json:"reportVersionId,omitempty"`
		Type            sharing.ShareType `json:"shareType"`
		PrincipalID     askdata.ID        `json:"principalId"`
		FilterSnapshot  map[string]any    `json:"filterSnapshot,omitempty"`
		ExpiresAt       *jsonTime         `json:"expiresAt,omitempty"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil {
		value := time.Time(*body.ExpiresAt)
		expiresAt = &value
	}
	created, err := handler.shares.Create(request.Context(), identity, sharing.CreateRequest{ID: askdata.ID(uuid.NewString()), ReportID: reportID, ReportVersionID: body.ReportVersionID, Type: body.Type, PrincipalID: body.PrincipalID, FilterSnapshot: body.FilterSnapshot, ExpiresAt: expiresAt})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (handler *Handler) revokeShare(writer http.ResponseWriter, request *http.Request) {
	identity, _, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	shareID := askdata.ID(request.PathValue("shareId"))
	if shareID.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "share ID is invalid")
		return
	}
	if err := handler.shares.Revoke(request.Context(), identity, shareID); err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"revoked": true})
}

func (handler *Handler) accessShare(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	version, filters, err := handler.shares.AccessShare(request.Context(), request.PathValue("token"), identity)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"version": version, "filterSnapshot": filters})
}

func (handler *Handler) createExport(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.exports == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_EXPORT_UNAVAILABLE", "report export is unavailable")
		return
	}
	var body struct {
		VersionNo *int                           `json:"versionNo,omitempty"`
		Format    publication.ExportFormat       `json:"format"`
		PageIDs   []askdata.ID                   `json:"pageIds,omitempty"`
		Filters   map[askdata.ID]json.RawMessage `json:"filterValues,omitempty"`
		AsOf      jsonTime                       `json:"asOf"`
		Timezone  string                         `json:"timezone"`
		ExpiresAt *jsonTime                      `json:"expiresAt,omitempty"`
	}
	if decodeJSON(request, &body) != nil || body.VersionNo != nil && *body.VersionNo < 1 {
		writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_INVALID", "report export request is invalid")
		return
	}
	version, err := handler.repository.GetVersion(request.Context(), identity, reportID, body.VersionNo)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if !version.Definition.RuntimePolicy.ExportEnabled {
		writeError(writer, http.StatusForbidden, "REPORT_EXPORT_DISABLED", "report export is disabled by the published version")
		return
	}
	availablePages := map[askdata.ID]struct{}{}
	for _, page := range version.Definition.Pages {
		availablePages[page.ID] = struct{}{}
	}
	for _, pageID := range body.PageIDs {
		if _, exists := availablePages[pageID]; !exists {
			writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_PAGE_INVALID", "export page does not exist in the pinned version")
			return
		}
	}
	location, err := runtime.RuntimeTimezone(version.Definition)
	if err != nil || body.Timezone != "" && body.Timezone != location.String() {
		writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_TIMEZONE_INVALID", "report export timezone does not match the published version")
		return
	}
	asOf := time.Time(body.AsOf).UTC()
	now := time.Now().UTC()
	if asOf.IsZero() {
		asOf = now
	}
	if asOf.After(now.Add(time.Minute)) {
		writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_AS_OF_INVALID", "report export asOf cannot be in the future")
		return
	}
	loaded := runtime.LoadedReport{
		ReportID: reportID, VersionID: version.ID, VersionNo: version.VersionNo,
		DefinitionHash: version.DefinitionHash, Definition: version.Definition,
	}
	policyHash, err := runtime.ViewerPolicyHash(identity, loaded)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if _, err := (runtime.HTTPPlanInput{
		PageID: version.Definition.Pages[0].ID, FilterValues: body.Filters,
	}).Resolve(version.Definition, asOf, location, policyHash); err != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_FILTER_INVALID", "report export filters are invalid")
		return
	}
	filterSummary := make(map[string]any, len(body.Filters))
	for id, raw := range body.Filters {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_FILTER_INVALID", "report export filters are invalid")
			return
		}
		filterSummary[string(id)] = value
	}
	expiresAt := time.Time{}
	if body.ExpiresAt != nil {
		expiresAt = time.Time(*body.ExpiresAt)
	}
	created, err := handler.exports.Create(request.Context(), identity, publication.CreateExportInput{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, ReportVersionID: version.ID,
		Format: body.Format, PageIDs: body.PageIDs, FilterSummary: filterSummary,
		AsOf: asOf, Timezone: location.String(), ExpiresAt: expiresAt,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, created)
}

func (handler *Handler) getExport(writer http.ResponseWriter, request *http.Request) {
	handler.exportAction(writer, request, false)
}

func (handler *Handler) retryExport(writer http.ResponseWriter, request *http.Request) {
	handler.exportAction(writer, request, true)
}

func (handler *Handler) exportAction(writer http.ResponseWriter, request *http.Request, retry bool) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.exports == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_EXPORT_UNAVAILABLE", "report export is unavailable")
		return
	}
	exportID := askdata.ID(request.PathValue("exportId"))
	if exportID.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_EXPORT_INVALID", "report export ID is invalid")
		return
	}
	item, err := handler.exports.Get(request.Context(), identity, exportID)
	if err == nil && item.ReportID != reportID {
		err = store.ErrNotFound
	}
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if retry {
		item, err = handler.exports.Retry(request.Context(), identity, exportID)
		if err != nil {
			writeReportError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) saveEvidence(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.insights == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_INSIGHT_UNAVAILABLE", "report insight storage is unavailable")
		return
	}
	var body struct {
		ComponentID askdata.ID             `json:"componentId"`
		Evidence    insight.EvidenceBundle `json:"evidence"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_EVIDENCE_INVALID", "report evidence is invalid")
		return
	}
	item, err := handler.insights.SaveEvidence(request.Context(), identity, reportID, body.ComponentID, body.Evidence)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, item)
}

func (handler *Handler) getInsight(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	componentID := askdata.ID(request.PathValue("componentId"))
	if handler.insights == nil || componentID.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_INSIGHT_INVALID", "report insight request is invalid")
		return
	}
	item, err := handler.insights.GetCurrent(request.Context(), identity, reportID, componentID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) editInsight(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	componentID := askdata.ID(request.PathValue("componentId"))
	if handler.insights == nil || componentID.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_INSIGHT_INVALID", "report insight request is invalid")
		return
	}
	var body struct {
		Content insight.InsightContent `json:"content"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_INSIGHT_INVALID", "report insight edit is invalid")
		return
	}
	item, err := handler.insights.EditCurrent(request.Context(), identity, reportID, componentID, body.Content, time.Now().UTC())
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, item)
}

func (handler *Handler) identity(writer http.ResponseWriter, request *http.Request) (store.Identity, bool) {
	identity, err := identityFromContext(request.Context())
	if err != nil {
		writeError(writer, http.StatusForbidden, "REPORT_FORBIDDEN", "report access is forbidden")
		return store.Identity{}, false
	}
	return identity, true
}
func (handler *Handler) subject(writer http.ResponseWriter, request *http.Request) (store.Identity, askdata.ID, bool) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return store.Identity{}, "", false
	}
	id := askdata.ID(request.PathValue("id"))
	if id.Validate() != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "report ID is invalid")
		return store.Identity{}, "", false
	}
	return identity, id, true
}

func identityFromContext(ctx context.Context) (store.Identity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	access, accessOK := database.AccessContextFromContext(ctx)
	if !ok || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		return store.Identity{}, errors.New("report identity unavailable")
	}
	identity := store.Identity{TenantID: askdata.ID(claims.TenantID), ActorID: askdata.ID(claims.Subject), DomainID: askdata.ID(access.DomainID)}
	return identity, identity.Validate()
}

func decodeJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxReportMutationBodyBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing JSON")
	}
	return nil
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}
func writeReportError(writer http.ResponseWriter, err error) {
	status, code := http.StatusUnprocessableEntity, "REPORT_OPERATION_FAILED"
	var conflict *store.RevisionConflict
	var applyError *operation.ApplyError
	var shareErr *sharing.Error
	var step *publication.StepError
	var runtimeErr *runtime.Error
	var assetErr *reportasset.Error
	switch {
	case errors.Is(err, publication.ErrUpgradeInvalid):
		status, code = http.StatusBadRequest, "REPORT_UPGRADE_INVALID"
	case errors.Is(err, publication.ErrUpgradeDraftDiverged), errors.Is(err, publication.ErrUpgradePreviewStale):
		status, code = http.StatusConflict, "REPORT_UPGRADE_STALE"
	case errors.Is(err, publication.ErrUpgradeUnavailable), errors.Is(err, publication.ErrSemanticCompilationInvalid):
		status, code = http.StatusUnprocessableEntity, "REPORT_UPGRADE_UNAVAILABLE"
	case errors.Is(err, reportasset.ErrNotFound):
		status, code = http.StatusNotFound, "REPORT_ASSET_NOT_FOUND"
	case errors.Is(err, store.ErrReportOffline):
		status, code = http.StatusGone, "REPORT_OFFLINE"
	case errors.As(err, &assetErr):
		code = assetErr.Code()
		switch code {
		case "REPORT_ASSET_FORBIDDEN", "REPORT_PERMISSION_FORBIDDEN":
			status = http.StatusForbidden
		case "REPORT_ASSET_STATE_CONFLICT":
			status = http.StatusConflict
		case "REPORT_ASSET_QUERY_INVALID", "REPORT_ASSET_CURSOR_INVALID", "REPORT_ASSET_REASON_INVALID", "REPORT_PERMISSION_INVALID":
			status = http.StatusBadRequest
		case "REPORT_RESTORE_VALIDATION_FAILED":
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"code": code, "message": assetErr.Error(), "issues": assetErr.Issues})
			return
		default:
			status = http.StatusUnprocessableEntity
		}
	case errors.Is(err, store.ErrNotFound):
		status, code = http.StatusNotFound, "REPORT_NOT_FOUND"
	case errors.As(err, &conflict):
		writeJSON(writer, http.StatusConflict, map[string]any{
			"code": "REPORT_REVISION_CONFLICT", "message": err.Error(),
			"expectedRevision": conflict.Expected, "currentRevision": conflict.Current,
			"operationSummaries": conflict.Summaries,
		})
		return
	case errors.Is(err, store.ErrAIEditForbidden):
		status, code = http.StatusForbidden, "REPORT_AI_EDIT_FORBIDDEN"
	case operation.ErrorCode(err) != "":
		status, code = http.StatusForbidden, operation.ErrorCode(err)
	case errors.As(err, &applyError):
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"code": applyError.Code, "message": applyError.Message, "operationIndex": applyError.Index,
		})
		return
	case errors.Is(err, store.ErrPublicationConflict):
		status, code = http.StatusConflict, "IDEMPOTENCY_KEY_REUSED"
	case errors.Is(err, store.ErrNothingToUndo):
		status, code = http.StatusConflict, "REPORT_NOTHING_TO_UNDO"
	case errors.Is(err, store.ErrNothingToRedo):
		status, code = http.StatusConflict, "REPORT_NOTHING_TO_REDO"
	case errors.As(err, &shareErr):
		if shareErr.Code == "SHARE_LOGIN_REQUIRED" {
			status = http.StatusUnauthorized
		} else if strings.Contains(shareErr.Code, "NOT_FOUND") {
			status = http.StatusNotFound
		} else {
			status = http.StatusForbidden
		}
		code = shareErr.Code
	case errors.As(err, &step):
		code = step.Code
		if step.Code == "REPORT_PUBLISH_FORBIDDEN" {
			status = http.StatusForbidden
		} else if step.Code == "IDEMPOTENCY_KEY_REQUIRED" {
			status = http.StatusBadRequest
		} else if step.Code == "REPORT_ROLLBACK_VERSION_NOT_FOUND" {
			status = http.StatusNotFound
		} else if step.Step == 2 {
			status = http.StatusConflict
		} else if step.Step >= 12 {
			status = http.StatusServiceUnavailable
		}
	case errors.As(err, &runtimeErr):
		code = runtimeErr.Code()
		switch code {
		case "REPORT_ARTIFACT_NOT_READY", "REPORT_COMPONENT_VERSION_UNAVAILABLE":
			status = http.StatusServiceUnavailable
		default:
			status = http.StatusUnprocessableEntity
		}
	}
	if step != nil {
		var validation compiler.ValidationIssues
		if errors.As(step.Err, &validation) {
			writeJSON(writer, status, map[string]any{
				"code": code, "message": err.Error(), "issues": validation,
			})
			return
		}
	}
	writeError(writer, status, code, err.Error())
}
func optionalVersionNo(writer http.ResponseWriter, request *http.Request) (*int, bool) {
	raw := request.URL.Query().Get("versionNo")
	if raw == "" {
		return nil, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "versionNo is invalid")
		return nil, false
	}
	return &value, true
}

// jsonTime retains RFC3339 decoding while keeping the public DTO strict.
type jsonTime time.Time

func (value *jsonTime) UnmarshalJSON(raw []byte) error {
	var parsed time.Time
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*value = jsonTime(parsed)
	return nil
}
