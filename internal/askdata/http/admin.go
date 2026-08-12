package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	registryimport "intelligent-report-generation-system/internal/askdata/registry/import"
	"intelligent-report-generation-system/internal/auth"
)

const (
	maxAdminBodyBytes      = 8 << 20
	adminIdempotencyDomain = "askdata-semantic-admin-v1\x00"
)

type adminIdentityResolver func(context.Context) (registry.AdminScope, error)

type importCommitter interface {
	Commit(context.Context, registryimport.CommitInput) (registryimport.CommitResult, error)
}

type importWithdrawer interface {
	Withdraw(context.Context, registryimport.WithdrawInput) (registryimport.WithdrawResult, error)
}

type semanticImportReader interface {
	Get(context.Context, string, string, string) (registryimport.SemanticImport, error)
}

type semanticBulkCertifier interface {
	BulkCertify(
		context.Context, registry.AdminScope, string, []string, string,
	) (registry.BulkCertificationResult, error)
}

type semanticExporter interface {
	Count(context.Context, registryimport.ExportSelection) (int, error)
	Generate(context.Context, registryimport.ExportSelection) (registryimport.ExportArtifact, error)
}

type adminIdempotencyProvider interface {
	IdempotencyRepository() IdempotencyRepository
}

type ImportMutationServices struct {
	Reads           semanticImportReader
	Commit          importCommitter
	Withdraw        importWithdrawer
	Certify         semanticBulkCertifier
	Export          semanticExporter
	ExportJobs      registryimport.ExportJobReader
	ExportArtifacts registryimport.ImportObjectStorage
	ReleaseReview   *registry.ReleaseReviewService
}

type AdminHandler struct {
	backend            registry.AdminBackend
	lifecycle          registry.ReleaseLifecycleBackend
	additivity         registry.AdditivityAdminBackend
	identity           adminIdentityResolver
	template           *registryimport.TemplateService
	upload             *registryimport.UploadService
	report             *registryimport.ReportService
	importReads        semanticImportReader
	commit             importCommitter
	withdraw           importWithdrawer
	certify            semanticBulkCertifier
	export             semanticExporter
	exportJobs         registryimport.ExportJobReader
	exportArtifacts    registryimport.ImportObjectStorage
	releaseReview      *registry.ReleaseReviewService
	releaseCatalog     registry.ReleaseCatalogBackend
	evaluationSets     registry.EvaluationSetCatalogBackend
	evaluationSetAdmin registry.EvaluationSetAdminBackend
	timeContracts      registry.TimeContractCatalogBackend
	timeContractAdmin  registry.TimeContractAdminBackend
	releaseComposer    registry.ReleaseComposer
}

func NewAdminHandler(
	authService *auth.Service,
	backend registry.AdminBackend,
	template ...*registryimport.TemplateService,
) http.Handler {
	protected := newProtectedAdminHandler(backend, authenticatedAdminScope, template...)
	if provider, ok := backend.(adminIdempotencyProvider); ok && provider.IdempotencyRepository() != nil {
		protected = idempotencyMiddleware(provider.IdempotencyRepository(), authenticatedIdentity, protected)
	}
	return auth.RequireAccessToken(authService, protected)
}

func NewAdminHandlerWithImportServices(
	authService *auth.Service,
	backend registry.AdminBackend,
	template *registryimport.TemplateService,
	upload *registryimport.UploadService,
	report *registryimport.ReportService,
	mutations ...ImportMutationServices,
) http.Handler {
	protected := newProtectedAdminHandlerWithImports(
		backend, authenticatedAdminScope, template, upload, report, mutations...,
	)
	if provider, ok := backend.(adminIdempotencyProvider); ok && provider.IdempotencyRepository() != nil {
		protected = idempotencyMiddleware(provider.IdempotencyRepository(), authenticatedIdentity, protected)
	}
	return auth.RequireAccessToken(authService, protected)
}

func newProtectedAdminHandler(
	backend registry.AdminBackend,
	identity adminIdentityResolver,
	template ...*registryimport.TemplateService,
) http.Handler {
	var templateService *registryimport.TemplateService
	if len(template) == 1 {
		templateService = template[0]
	}
	return newProtectedAdminHandlerWithImports(backend, identity, templateService, nil, nil)
}

func newProtectedAdminHandlerWithImports(
	backend registry.AdminBackend,
	identity adminIdentityResolver,
	template *registryimport.TemplateService,
	upload *registryimport.UploadService,
	report *registryimport.ReportService,
	mutations ...ImportMutationServices,
) http.Handler {
	var mutationServices ImportMutationServices
	if len(mutations) == 1 {
		mutationServices = mutations[0]
	}
	handler := &AdminHandler{
		backend: backend, identity: identity, template: template, upload: upload,
		report: report, commit: mutationServices.Commit,
		importReads: mutationServices.Reads,
		withdraw:    mutationServices.Withdraw, certify: mutationServices.Certify,
		export: mutationServices.Export, exportJobs: mutationServices.ExportJobs,
		exportArtifacts: mutationServices.ExportArtifacts, releaseReview: mutationServices.ReleaseReview,
	}
	if additivity, ok := backend.(registry.AdditivityAdminBackend); ok {
		handler.additivity = additivity
	}
	if lifecycle, ok := backend.(registry.ReleaseLifecycleBackend); ok {
		handler.lifecycle = lifecycle
	}
	if catalog, ok := backend.(registry.ReleaseCatalogBackend); ok {
		handler.releaseCatalog = catalog
	}
	if catalog, ok := backend.(registry.EvaluationSetCatalogBackend); ok {
		handler.evaluationSets = catalog
	}
	if admin, ok := backend.(registry.EvaluationSetAdminBackend); ok {
		handler.evaluationSetAdmin = admin
	}
	if catalog, ok := backend.(registry.TimeContractCatalogBackend); ok {
		handler.timeContracts = catalog
	}
	if admin, ok := backend.(registry.TimeContractAdminBackend); ok {
		handler.timeContractAdmin = admin
	}
	if composer, ok := backend.(registry.ReleaseComposer); ok {
		handler.releaseComposer = composer
	}
	mux := http.NewServeMux()
	for _, path := range []string{
		"models", "measures", "metrics", "metric-versions",
		"dimensions", "terms", "kpi-bundles", "relationships", "metric-dimensions",
		// Governed data quality rules bind a semantic object to a check the
		// materialization pipeline already runs; see registry/quality_rule.go.
		"quality-rules",
	} {
		collection := "/api/v1/askdata/semantic/" + path
		item := collection + "/{id}"
		mux.HandleFunc("GET "+collection, handler.listDrafts)
		mux.HandleFunc("POST "+collection, handler.createDraft)
		mux.HandleFunc("GET "+item, handler.getDraft)
		mux.HandleFunc("PUT "+item, handler.updateDraft)
		mux.HandleFunc("DELETE "+item, handler.deleteDraft)
	}
	// 只读对象类型：成员、层级与认证问法目前只能经导入通道写入，
	// 因此只注册 GET，不注册 POST/PUT/DELETE——注册一个没有实现的写入路由，
	// 会把「不支持」变成运行期错误。
	//
	// 评测用例（EVAL_CASE）**刻意不在此列**：evaluation_case_versions 含
	// set_type='SEALED' 的密封集正文，而密封集正文不可显示、被查看样本必须立即退役
	// （02 §10.1、03 J-P08-04、06 §4.10）。开放一个普通读取接口会让持有
	// SEMANTIC_VIEW 的人直接读到密封题面，使 95% 门禁失效。
	// 若确需查看，应走单独的、带退役副作用的受控接口，见 05_TODO SEM-READ-002。
	for _, path := range []string{
		"members", "hierarchies", "certified-examples",
	} {
		collection := "/api/v1/askdata/semantic/" + path
		mux.HandleFunc("GET "+collection, handler.listDrafts)
		mux.HandleFunc("GET "+collection+"/{id}", handler.getDraft)
	}
	if handler.releaseCatalog != nil {
		mux.HandleFunc("GET /api/v1/askdata/semantic/releases", handler.listReleaseCatalog)
	}
	if handler.evaluationSets != nil {
		mux.HandleFunc("GET /api/v1/askdata/semantic/evaluation-sets", handler.listEvaluationSetCatalog)
	}
	if handler.evaluationSetAdmin != nil {
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/evaluation-sets", handler.createReleaseEvaluationSet)
		mux.HandleFunc("GET /api/v1/askdata/semantic/evaluation-sets/{id}/cases", handler.listEvaluationCasesForReview)
		mux.HandleFunc("POST /api/v1/askdata/semantic/evaluation-sets/{id}/reviews", handler.reviewEvaluationSet)
		mux.HandleFunc("POST /api/v1/askdata/semantic/evaluation-sets/{id}/seal", handler.sealEvaluationSet)
		mux.HandleFunc("GET /api/v1/askdata/semantic/evaluation-sets/{id}/shards", handler.getEvaluationShardHealth)
		mux.HandleFunc("POST /api/v1/askdata/semantic/evaluation-sets/{id}/shards/expose", handler.exposeEvaluationShard)
	}
	if handler.timeContracts != nil {
		mux.HandleFunc("GET /api/v1/askdata/semantic/time-contracts", handler.listTimeContractCatalog)
	}
	if handler.timeContractAdmin != nil {
		mux.HandleFunc("POST /api/v1/askdata/semantic/time-contracts", handler.createCertifiedTimeContract)
	}
	mux.HandleFunc("POST /api/v1/askdata/semantic/releases", handler.createReleaseDraft)
	if handler.releaseComposer != nil {
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/compose", handler.composeReleaseDraft)
	}
	if handler.lifecycle != nil {
		mux.HandleFunc("GET /api/v1/askdata/semantic/releases/{id}/lifecycle", handler.getReleaseLifecycle)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/validate-project", handler.validateAndProjectRelease)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/retry-projections", handler.retryReleaseProjections)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/evaluation-batches", handler.planReleaseEvaluationBatch)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/error-budget", handler.recordReleaseErrorBudget)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/gate", handler.recomputeReleaseGate)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/review-report", handler.recordReleaseReviewReport)
		if handler.releaseReview != nil {
			mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/review-report/generate", handler.generateReleaseReviewReport)
		}
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/approvals", handler.submitReleaseApproval)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/approvals/withdraw", handler.withdrawReleaseApproval)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/approvals/reset-rejection", handler.resetRejectedReleaseApprovals)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/approvals/escalate", handler.escalateReleaseApproval)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/activate", handler.activateRelease)
		mux.HandleFunc("GET /api/v1/askdata/semantic/releases/{id}/operations", handler.getReleaseOperations)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollouts", handler.startReleaseRollout)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollouts/advance", handler.advanceReleaseRollout)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollouts/pause", handler.pauseReleaseRollout)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollouts/resume", handler.resumeReleaseRollout)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollouts/stop", handler.stopReleaseRollout)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/rollback", handler.rollbackRelease)
		mux.HandleFunc("POST /api/v1/askdata/semantic/releases/{id}/retire", handler.retireRelease)
	}
	if handler.additivity != nil {
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/metrics/additivity/confirm",
			handler.bulkConfirmAdditivity,
		)
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/metrics/additivity/suggestions",
			handler.refreshAdditivitySuggestions,
		)
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/domains/{id}/readiness",
			handler.getDomainReadiness,
		)
	}
	if handler.template != nil {
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/imports/template",
			handler.downloadImportTemplate,
		)
	}
	if handler.upload != nil {
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/imports",
			handler.uploadImport,
		)
	}
	if handler.importReads != nil {
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/imports/{id}",
			handler.getImport,
		)
	}
	if handler.report != nil {
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/imports/{id}/report",
			handler.downloadImportReport,
		)
	}
	if handler.commit != nil {
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/imports/{id}/commit",
			handler.commitImport,
		)
	}
	if handler.withdraw != nil {
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/imports/{id}/withdraw",
			handler.withdrawImport,
		)
	}
	if handler.certify != nil {
		mux.HandleFunc(
			"POST /api/v1/askdata/semantic/bulk-certify",
			handler.bulkCertify,
		)
	}
	if handler.export != nil {
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/exports",
			handler.requestSemanticExport,
		)
	}
	if handler.exportJobs != nil {
		mux.HandleFunc(
			"GET /api/v1/askdata/semantic/exports/{id}",
			handler.getSemanticExport,
		)
		if handler.exportArtifacts != nil {
			mux.HandleFunc(
				"GET /api/v1/askdata/semantic/exports/{id}/download",
				handler.downloadSemanticExport,
			)
		}
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	})
}

func (handler *AdminHandler) withdrawReleaseApproval(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseApprovalRecovery(writer, request, handler.lifecycle.WithdrawReleaseApproval)
}

func (handler *AdminHandler) resetRejectedReleaseApprovals(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseApprovalRecovery(writer, request, handler.lifecycle.ResetRejectedReleaseApprovals)
}

func (handler *AdminHandler) escalateReleaseApproval(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseApprovalRecovery(writer, request, handler.lifecycle.EscalateReleaseApproval)
}

func (handler *AdminHandler) handleReleaseApprovalRecovery(writer http.ResponseWriter, request *http.Request, execute func(context.Context, registry.AdminScope, string, registry.ReleaseApprovalRecoveryInput) (registry.ReleaseApprovalRecoveryResult, error)) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseApprovalRecoveryInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := execute(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) listReleaseCatalog(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "cursor" && key != "limit" {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	limit := 50
	var err error
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	result, err := handler.releaseCatalog.ListReleaseCatalog(request.Context(), scope, query.Get("cursor"), limit)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) listEvaluationSetCatalog(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "limit" {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	limit := 50
	var err error
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	result, err := handler.evaluationSets.ListEvaluationSetCatalog(request.Context(), scope, limit)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) listTimeContractCatalog(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "limit" {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	limit := 50
	var err error
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	result, err := handler.timeContracts.ListTimeContractCatalog(request.Context(), scope, limit)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": result})
}

func (handler *AdminHandler) createCertifiedTimeContract(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" || handler.timeContractAdmin == nil {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input registry.TimeContractCreateInput
	canonical, err := decodeAdminJSON(writer, request, &input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.timeContractAdmin.CreateCertifiedTimeContract(
		request.Context(), scope, input, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *AdminHandler) composeReleaseDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input registry.ReleaseComposeInput
	canonical, err := decodeAdminJSON(writer, request, &input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.releaseComposer.CreateReleaseFromCertified(request.Context(), scope, input, command)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *AdminHandler) getReleaseLifecycle(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.lifecycle.GetReleaseLifecycle(request.Context(), scope, request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) validateAndProjectRelease(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input struct{}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.ValidateAndStartProjection(request.Context(), scope, request.PathValue("id"))
	if err != nil {
		var preflightError *registry.ReleasePreflightError
		if errors.As(err, &preflightError) {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
				"code": "RELEASE_PREFLIGHT_FAILED", "message": "semantic release preflight did not pass",
				"preflight": preflightError.Result,
			})
			return
		}
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (handler *AdminHandler) retryReleaseProjections(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input struct{}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.RetryFailedProjections(request.Context(), scope, request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (handler *AdminHandler) planReleaseEvaluationBatch(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.EvaluationBatchPlanInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.PlanEvaluationBatch(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (handler *AdminHandler) recordReleaseErrorBudget(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ErrorBudgetAttachmentInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	reportHash, err := handler.lifecycle.RecordErrorBudget(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"reportHash": reportHash})
}

func (handler *AdminHandler) recomputeReleaseGate(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseGateInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.RecomputeReleaseGate(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) recordReleaseReviewReport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseReviewReportInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	reportHash, err := handler.lifecycle.RecordReleaseReviewReport(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"reportHash": reportHash})
}

func (handler *AdminHandler) generateReleaseReviewReport(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input struct {
		EvaluationSetID   string `json:"evaluationSetId"`
		EvaluationBatchID string `json:"evaluationBatchId"`
		PromptVersion     string `json:"promptVersion"`
		PreferredModel    string `json:"preferredModel"`
	}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	releaseID := request.PathValue("id")
	gate, err := handler.lifecycle.RecomputeReleaseGate(request.Context(), scope, releaseID, registry.ReleaseGateInput{
		EvaluationSetID: input.EvaluationSetID, EvaluationBatchID: input.EvaluationBatchID,
	})
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	canonicalFacts, err := registry.CanonicalJSON(gate.Facts)
	if err != nil || len(gate.ReceiptHash) != 64 {
		writeAdminError(writer, registry.ErrReleaseReviewInvalid)
		return
	}
	evidenceHash := askdata.HashBytes(canonicalFacts)
	result, err := handler.releaseReview.GenerateAndRecord(request.Context(), registry.GenerateReleaseReviewRequest{
		Scope: scope, ReleaseID: releaseID, EvaluationSetID: input.EvaluationSetID,
		EvaluationBatchID: input.EvaluationBatchID, Gate: gate,
		PromptVersion: input.PromptVersion, PreferredModel: input.PreferredModel,
		Evidence: []registry.ReleaseReviewEvidence{{
			EvidenceID: askdata.ID("release-gate-" + gate.ReceiptHash[:16]), Kind: "EVALUATION_GATE",
			ContentHash: evidenceHash, Payload: canonicalFacts,
		}},
	})
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler *AdminHandler) submitReleaseApproval(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseApprovalInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.SubmitReleaseApproval(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler *AdminHandler) activateRelease(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseActivationInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.ActivateRelease(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) getReleaseOperations(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	result, err := handler.lifecycle.GetReleaseOperationalImpact(request.Context(), scope, request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) handleReleaseRolloutMutation(writer http.ResponseWriter, request *http.Request, execute func(context.Context, registry.AdminScope, string, registry.ReleaseRolloutMutationInput) (registry.ReleaseRolloutSnapshot, error)) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseRolloutMutationInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := execute(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
func (handler *AdminHandler) startReleaseRollout(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseRolloutMutation(writer, request, handler.lifecycle.StartReleaseRollout)
}
func (handler *AdminHandler) advanceReleaseRollout(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseRolloutMutation(writer, request, handler.lifecycle.AdvanceReleaseRollout)
}
func (handler *AdminHandler) pauseReleaseRollout(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseRolloutMutation(writer, request, handler.lifecycle.PauseReleaseRollout)
}
func (handler *AdminHandler) resumeReleaseRollout(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseRolloutMutation(writer, request, handler.lifecycle.ResumeReleaseRollout)
}
func (handler *AdminHandler) stopReleaseRollout(writer http.ResponseWriter, request *http.Request) {
	handler.handleReleaseRolloutMutation(writer, request, handler.lifecycle.StopReleaseRollout)
}

func (handler *AdminHandler) rollbackRelease(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	var input registry.ReleaseRollbackInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.lifecycle.RollbackRelease(request.Context(), scope, request.PathValue("id"), input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
func (handler *AdminHandler) retireRelease(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	if _, err := decodeAdminJSON(writer, request, &struct{}{}); err != nil {
		writeAdminError(writer, err)
		return
	}
	if err := handler.lifecycle.RetireRelease(request.Context(), scope, request.PathValue("id")); err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"releaseId": request.PathValue("id"), "retired": true})
}

func requireLifecycleIdempotency(writer http.ResponseWriter, request *http.Request) bool {
	if _, err := requireIdempotencyKey(request); err != nil {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return false
	}
	return true
}

func authenticatedAdminScope(ctx context.Context) (registry.AdminScope, error) {
	identity, err := authenticatedIdentity(ctx)
	if err != nil {
		return registry.AdminScope{}, err
	}
	return registry.AdminScope{
		TenantID: string(identity.TenantID), DomainID: string(identity.DomainID),
		ActorID: string(identity.ActorID),
	}, nil
}

func (handler *AdminHandler) listDrafts(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	resource, err := adminResourceFromPath(request.URL.Path)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "cursor" && key != "limit" && key != "additivityStatus" &&
			key != "suggestion" && key != "status" {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	additivityStatus := strings.ToUpper(strings.TrimSpace(query.Get("additivityStatus")))
	if additivityStatus != "" {
		if resource != registry.AdminResourceMetric || additivityStatus != "UNCONFIRMED" || handler.additivity == nil {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
		group := registry.Additivity(strings.ToUpper(strings.TrimSpace(query.Get("suggestion"))))
		limit := 50
		if raw := query.Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil {
				writeAdminError(writer, registry.ErrRegistryInvalidRequest)
				return
			}
		}
		page, err := handler.additivity.ListUnconfirmedAdditivity(
			request.Context(), scope, group, query.Get("cursor"), limit,
		)
		if err != nil {
			writeAdminError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, page)
		return
	}
	if query.Get("suggestion") != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeAdminError(writer, registry.ErrRegistryInvalidRequest)
			return
		}
	}
	// 不带 status 时返回该领域下全部状态的对象。导入核对、Release 候选评审与
	// 语义工作台读取的都是 CERTIFIED 对象，只返回 DRAFT 会让这些场景看不到数据。
	page, err := handler.backend.ListObjects(request.Context(), scope, resource, registry.AdminListFilter{
		Status: strings.ToUpper(strings.TrimSpace(query.Get("status"))),
		Cursor: query.Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *AdminHandler) getDomainReadiness(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" || request.PathValue("id") != scope.DomainID || handler.additivity == nil {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	result, err := handler.additivity.GetAdditivityReadiness(request.Context(), scope)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

// refreshAdditivitySuggestions recomputes the advisory heuristic for the whole
// domain. The request body is empty on purpose: candidates and their suggested
// values are derived server-side, so the browser cannot propose an additivity
// value for a metric. Confirmation remains a separate, explicit human act.
func (handler *AdminHandler) refreshAdditivitySuggestions(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" || handler.additivity == nil {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input struct{}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.additivity.RefreshAdditivitySuggestions(request.Context(), scope)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) bulkConfirmAdditivity(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" || handler.additivity == nil {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input registry.BulkAdditivityConfirmation
	canonical, err := decodeAdminJSON(writer, request, &input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.additivity.BulkConfirmAdditivity(
		request.Context(), scope, input, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) getDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	resource, err := adminResourceFromPath(request.URL.Path)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	resourceID, err := adminResourceID(request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	// 与列表一致：详情读取不限定 DRAFT，否则已认证对象无法查看。
	result, err := handler.backend.GetObject(request.Context(), scope, resource, resourceID)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) createDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	resource, err := adminResourceFromPath(request.URL.Path)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	mutation, canonical, err := decodeAdminMutation(writer, request, resource)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.backend.CreateDraft(
		request.Context(), scope, resource, mutation, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *AdminHandler) updateDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	resource, err := adminResourceFromPath(request.URL.Path)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	resourceID, err := adminResourceID(request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	mutation, canonical, err := decodeAdminMutation(writer, request, resource)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.backend.UpdateDraft(
		request.Context(), scope, resource, resourceID, mutation, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) deleteDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	resource, err := adminResourceFromPath(request.URL.Path)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	resourceID, err := adminResourceID(request.PathValue("id"))
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	var input registry.DeleteDraftInput
	canonical, err := decodeAdminJSON(writer, request, &input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.backend.DeleteDraft(
		request.Context(), scope, resource, resourceID, input, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) createReleaseDraft(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrRegistryInvalidRequest)
		return
	}
	var input registry.ReleaseDraftInput
	canonical, err := decodeAdminJSON(writer, request, &input)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	command, err := newAdminCommand(request, scope, canonical)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	result, err := handler.backend.CreateAdminReleaseDraft(
		request.Context(), scope, input, command,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *AdminHandler) resolveScope(
	writer http.ResponseWriter,
	request *http.Request,
) (registry.AdminScope, bool) {
	if handler == nil || handler.backend == nil || handler.identity == nil {
		writeAdminError(writer, errors.New("semantic admin service is not configured"))
		return registry.AdminScope{}, false
	}
	scope, err := handler.identity(request.Context())
	if err != nil {
		writeAdminError(writer, ErrUnauthenticated)
		return registry.AdminScope{}, false
	}
	return scope, true
}

func adminResourceFromPath(path string) (registry.AdminResource, error) {
	trimmed := strings.TrimPrefix(path, "/api/v1/askdata/semantic/")
	segment := strings.SplitN(trimmed, "/", 2)[0]
	switch segment {
	case "models":
		return registry.AdminResourceSemanticModel, nil
	case "measures":
		return registry.AdminResourceMeasure, nil
	case "metrics":
		return registry.AdminResourceMetric, nil
	case "metric-versions":
		return registry.AdminResourceMetricVersion, nil
	case "dimensions":
		return registry.AdminResourceDimension, nil
	case "terms":
		return registry.AdminResourceBusinessTerm, nil
	case "kpi-bundles":
		return registry.AdminResourceKPIBundle, nil
	case "relationships":
		return registry.AdminResourceRelationship, nil
	case "quality-rules":
		return registry.AdminResourceQualityRule, nil
	case "members":
		return registry.AdminResourceMember, nil
	case "hierarchies":
		return registry.AdminResourceHierarchy, nil
	case "certified-examples":
		return registry.AdminResourceCertifiedExample, nil
	case "metric-dimensions":
		return registry.AdminResourceMetricDimension, nil
	default:
		return "", fmt.Errorf("%w: unsupported semantic resource", registry.ErrRegistryInvalidRequest)
	}
}

func adminResourceID(raw string) (string, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != strings.ToLower(raw) {
		return "", fmt.Errorf("%w: resource ID must be a canonical UUID", registry.ErrRegistryInvalidRequest)
	}
	return raw, nil
}

func decodeAdminMutation(
	writer http.ResponseWriter,
	request *http.Request,
	resource registry.AdminResource,
) (registry.AdminMutation, []byte, error) {
	var mutation registry.AdminMutation
	var target any
	switch resource {
	case registry.AdminResourceSemanticModel:
		mutation.SemanticModel = &registry.SemanticModelDraftInput{}
		target = mutation.SemanticModel
	case registry.AdminResourceMeasure:
		mutation.Measure = &registry.MeasureDraftInput{}
		target = mutation.Measure
	case registry.AdminResourceMetric:
		mutation.Metric = &registry.MetricDraftInput{}
		target = mutation.Metric
	case registry.AdminResourceMetricVersion:
		mutation.MetricVersion = &registry.MetricVersionDraftInput{}
		target = mutation.MetricVersion
	case registry.AdminResourceDimension:
		mutation.Dimension = &registry.DimensionDraftInput{}
		target = mutation.Dimension
	case registry.AdminResourceBusinessTerm:
		mutation.BusinessTerm = &registry.BusinessTermDraftInput{}
		target = mutation.BusinessTerm
	case registry.AdminResourceKPIBundle:
		mutation.KPIBundle = &registry.KPIBundleDraftInput{}
		target = mutation.KPIBundle
	case registry.AdminResourceRelationship:
		mutation.Relationship = &registry.RelationshipDraftInput{}
		target = mutation.Relationship
	case registry.AdminResourceQualityRule:
		mutation.QualityRule = &registry.QualityRuleDraftInput{}
		target = mutation.QualityRule
	case registry.AdminResourceMetricDimension:
		mutation.MetricDimension = &registry.MetricDimensionDraftInput{}
		target = mutation.MetricDimension
	default:
		return registry.AdminMutation{}, nil, registry.ErrRegistryInvalidRequest
	}
	canonical, err := decodeAdminJSON(writer, request, target)
	return mutation, canonical, err
}

func decodeAdminJSON(writer http.ResponseWriter, request *http.Request, target any) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if request.Body == nil || err != nil || strings.ToLower(mediaType) != "application/json" {
		return nil, registry.ErrRegistryInvalidRequest
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxAdminBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, registry.ErrRegistryInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, registry.ErrRegistryInvalidRequest
	}
	canonical, err := registry.CanonicalValue(target)
	if err != nil {
		return nil, registry.ErrRegistryInvalidRequest
	}
	return canonical, nil
}

func newAdminCommand(
	request *http.Request,
	scope registry.AdminScope,
	canonicalBody []byte,
) (registry.AdminCommand, error) {
	key, err := requireIdempotencyKey(request)
	if err != nil {
		return registry.AdminCommand{}, registry.ErrRegistryInvalidRequest
	}
	keyHash := askdata.HashBytes([]byte(adminIdempotencyDomain + key))
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		scope.TenantID+"\x00"+scope.ActorID+"\x00"+string(keyHash),
	)).String()
	actionMaterial := append([]byte(request.Method+"\x00"+request.URL.Path+"\x00"), canonicalBody...)
	return registry.AdminCommand{
		RequestID: requestID, ActionHash: askdata.HashBytes(actionMaterial),
	}, nil
}

func writeAdminError(writer http.ResponseWriter, err error) {
	var validation registry.ValidationErrors
	switch {
	case errors.Is(err, ErrUnauthenticated):
		writeError(writer, http.StatusUnauthorized, "REG_AUTHENTICATION_REQUIRED", "valid semantic administration access is required")
	case errors.Is(err, registry.ErrRegistryPermissionDenied):
		writeError(writer, http.StatusForbidden, "REG_PERMISSION_DENIED", "semantic administration permission is required")
	case errors.As(err, &validation):
		writeJSON(writer, http.StatusBadRequest, map[string]any{
			"code": "REG_VALIDATION_FAILED", "message": "semantic draft validation failed",
			"issues": validation.Issues,
		})
	case errors.Is(err, registry.ErrRegistryInvalidRequest):
		writeError(writer, http.StatusBadRequest, "REG_INVALID_REQUEST", "semantic administration request is invalid")
	case errors.Is(err, registry.ErrRegistryNotFound):
		writeError(writer, http.StatusNotFound, "REG_NOT_FOUND", "semantic draft was not found")
	case errors.Is(err, registry.ErrRegistryVersionConflict):
		writeError(writer, http.StatusConflict, "REG_VERSION_CONFLICT", "semantic draft changed or is no longer editable")
	case errors.Is(err, registry.ErrRegistryIdempotencyConflict):
		writeError(writer, http.StatusConflict, "REG_IDEMPOTENCY_CONFLICT", "idempotency key was already used for a different semantic write")
	case errors.Is(err, registry.ErrRegistryDraftInUse):
		writeError(writer, http.StatusConflict, "REG_DRAFT_IN_USE", "semantic draft is referenced by another object")
	case errors.Is(err, registry.ErrRegistryConflict):
		writeError(writer, http.StatusConflict, "REG_CONFLICT", "semantic draft conflicts with an existing object")
	case errors.Is(err, registry.ErrReleasePreflightFailed):
		writeError(writer, http.StatusUnprocessableEntity, "RELEASE_PREFLIGHT_FAILED", "semantic release preflight did not pass")
	case errors.Is(err, registry.ErrReleaseStateConflict):
		writeError(writer, http.StatusConflict, "RELEASE_STATE_VERSION_CONFLICT", "semantic release state changed concurrently")
	case errors.Is(err, registry.ErrReleaseApprovalFailed):
		writeError(writer, http.StatusUnprocessableEntity, "RELEASE_APPROVALS_REQUIRED", "semantic release requires two independent approvals")
	case errors.Is(err, registry.ErrReleaseGateFailed):
		writeError(writer, http.StatusUnprocessableEntity, "RELEASE_GATE_FAILED", "semantic release evaluation gate did not pass")
	case errors.Is(err, registry.ErrReleaseReviewInvalid):
		writeError(writer, http.StatusUnprocessableEntity, "RELEASE_REVIEW_INVALID", "semantic release review evidence or model output is invalid")
	case errors.Is(err, registry.ErrReleaseRolloutInvalid):
		writeError(writer, http.StatusConflict, "RELEASE_ROLLOUT_INVALID", "semantic release rollout cannot transition from its current state")
	case errors.Is(err, registry.ErrReleaseRollbackInvalid):
		writeError(writer, http.StatusConflict, "RELEASE_ROLLBACK_INVALID", "semantic release cannot be rolled back or retired from its current state")
	case errors.Is(err, registry.ErrReleaseRetireBlocked):
		writeError(writer, http.StatusConflict, "RELEASE_RETIRE_BLOCKED", "semantic release still has active governed references")
	case errors.Is(err, registry.ErrReleaseRetentionOpen):
		writeError(writer, http.StatusConflict, "RELEASE_RETENTION_NOT_EXPIRED", "semantic release is still within its mandatory retention window")
	case errors.Is(err, registry.ErrEvaluationSetCasesMissing):
		writeError(writer, http.StatusUnprocessableEntity, "EVALUATION_CASES_MISSING", "certified SEALED evaluation cases are required")
	case errors.Is(err, registry.ErrEvaluationSetHintInvalid):
		writeError(writer, http.StatusUnprocessableEntity, "EVALUATION_EXPECTED_CONTRACT_INVALID", "evaluation expected-result contract is incomplete or invalid")
	case errors.Is(err, registry.ErrEvaluationSetReviewConflict):
		writeError(writer, http.StatusConflict, "EVALUATION_REVIEW_CONFLICT", "the current actor cannot add another independent review")
	case errors.Is(err, registry.ErrEvaluationSetNotFound):
		writeError(writer, http.StatusNotFound, "EVALUATION_SET_NOT_FOUND", "evaluation set was not found")
	case errors.Is(err, registry.ErrEvaluationSetInvalid):
		writeError(writer, http.StatusBadRequest, "EVALUATION_SET_INVALID", "evaluation set request is invalid")
	default:
		writeError(writer, http.StatusInternalServerError, "REG_SERVICE_FAILED", "semantic administration service failed")
	}
}
