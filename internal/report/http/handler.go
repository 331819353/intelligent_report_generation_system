package reporthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"intelligent-report-generation-system/internal/report/blueprint"
	"intelligent-report-generation-system/internal/report/cardkind"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/publication"
	"intelligent-report-generation-system/internal/report/reportai"
	"intelligent-report-generation-system/internal/report/reportinsight"
	"intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/sharing"
	"intelligent-report-generation-system/internal/report/store"
	"intelligent-report-generation-system/internal/report/template"
)

type AIOptions struct {
	PlanGenerator      reportai.PlanGenerator
	BlueprintGenerator blueprint.Generator
	EditGenerator      reportai.ScopedEditGenerator
	// BindingSuggester identifies a card's measures/dimensions from the governed
	// field catalog of one dataset; optional — without it the editor keeps the
	// deterministic role-based fill only.
	BindingSuggester reportai.CardBindingSuggester
	Reviewer         reportai.PublishReviewGenerator
	Selector         reportai.DataContextSelector
	Contexts         reportai.DataContextCatalog
	Fields           reportai.FieldCatalog
	Components       *template.Registry
	Kinds            *cardkind.Registry
	Methods          *insight.Registry
	Runtime          runtime.QueryExecutor
	// Measures supplies governed dataset metadata (declared units) that a
	// derived Evidence Bundle must carry.
	Measures reportinsight.MeasureContract
	// Narrative writes and verifies report conclusions.
	Narrative *reportinsight.NarrativeService
	Upgrade   *publication.UpgradeService
}

type Handler struct {
	repository  *store.PostgresStore
	publisher   *publication.Publisher
	loader      runtime.Loader
	shares      sharing.Service
	exports     *publication.ExportJobStore
	exportFiles interface {
		Get(context.Context, string, string) (io.ReadCloser, error)
	}
	exportBucket string
	insights     *insight.PostgresStore
	aiAudit      *reportai.PostgresStore
	assets       reportasset.Service
	assetRepo    *reportasset.PostgresRepository
	ai           AIOptions
}

func NewHandler(
	authService *auth.Service,
	idempotencyRepository platformidempotency.Repository,
	repository *store.PostgresStore,
	publisher *publication.Publisher,
	loader runtime.Loader,
	shares sharing.Service,
	exports *publication.ExportJobStore,
	exportFiles interface {
		Get(context.Context, string, string) (io.ReadCloser, error)
	},
	exportBucket string,
	insights *insight.PostgresStore,
	aiAudit *reportai.PostgresStore,
	assets reportasset.Service,
	aiOptions ...AIOptions,
) http.Handler {
	configuredAI := AIOptions{}
	if len(aiOptions) == 1 {
		configuredAI = aiOptions[0]
	}
	handler := &Handler{repository: repository, publisher: publisher, loader: loader, shares: shares, exports: exports,
		exportFiles: exportFiles, exportBucket: strings.TrimSpace(exportBucket), insights: insights,
		aiAudit: aiAudit, assets: assets, assetRepo: assets.Repository, ai: configuredAI}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/reports", handler.list)
	mux.HandleFunc("POST /api/v1/reports", handler.create)
	mux.HandleFunc("POST /api/v1/reports/blank", handler.createBlank)
	mux.HandleFunc("GET /api/v1/report-templates", handler.reportTemplates)
	mux.HandleFunc("POST /api/v1/report-templates/{templateId}/instantiate", handler.instantiateTemplate)
	mux.HandleFunc("GET /api/v1/report-data-contexts", handler.dataContexts)
	mux.HandleFunc("GET /api/v1/report-component-manifests", handler.componentManifests)
	mux.HandleFunc("GET /api/v1/report-card-kinds", handler.cardKinds)
	mux.HandleFunc("POST /api/v1/report-blueprints/expand", handler.expandBlueprint)
	mux.HandleFunc("POST /api/v1/reports/ai/create", handler.createAI)
	mux.HandleFunc("GET /api/v1/reports/{id}", handler.get)
	mux.HandleFunc("GET /api/v1/reports/{id}/draft", handler.getDraft)
	mux.HandleFunc("GET /api/v1/reports/{id}/revisions", handler.listRevisions)
	mux.HandleFunc("POST /api/v1/reports/{id}/operations", handler.operations)
	mux.HandleFunc("POST /api/v1/reports/{id}/ai/plan", handler.aiPlan)
	mux.HandleFunc("POST /api/v1/reports/{id}/ai/preview", handler.aiPreview)
	mux.HandleFunc("POST /api/v1/reports/{id}/ai/card-binding", handler.aiCardBinding)
	mux.HandleFunc("POST /api/v1/reports/{id}/undo", handler.undo)
	mux.HandleFunc("POST /api/v1/reports/{id}/redo", handler.redo)
	mux.HandleFunc("POST /api/v1/reports/{id}/publish", handler.publish)
	mux.HandleFunc("POST /api/v1/reports/{id}/publish-review", handler.publishReview)
	mux.HandleFunc("POST /api/v1/reports/{id}/rollback", handler.rollback)
	mux.HandleFunc("GET /api/v1/reports/{id}/versions", handler.listVersions)
	mux.HandleFunc("GET /api/v1/reports/{id}/versions/{versionNo}", handler.getVersion)
	mux.HandleFunc("GET /api/v1/reports/{id}/runtime", handler.loadRuntime)
	mux.HandleFunc("POST /api/v1/reports/{id}/runtime/plan", handler.runtimePlan)
	mux.HandleFunc("POST /api/v1/reports/{id}/runtime/execute", handler.runtimeExecute)
	mux.HandleFunc("POST /api/v1/reports/{id}/draft/execute", handler.draftExecute)
	mux.HandleFunc("POST /api/v1/reports/{id}/upgrade/preview", handler.upgradePreview)
	mux.HandleFunc("POST /api/v1/reports/{id}/upgrade/confirm", handler.upgradeConfirm)
	mux.HandleFunc("GET /api/v1/reports/{id}/permissions", handler.listPermissions)
	mux.HandleFunc("POST /api/v1/reports/{id}/permissions", handler.grantPermission)
	mux.HandleFunc("DELETE /api/v1/reports/{id}/permissions/{grantId}", handler.revokePermission)
	mux.HandleFunc("POST /api/v1/reports/{id}/archive", handler.archiveReport)
	mux.HandleFunc("POST /api/v1/reports/{id}/restore", handler.restoreReport)
	mux.HandleFunc("GET /api/v1/reports/{id}/asset-events", handler.listAssetEvents)
	mux.HandleFunc("POST /api/v1/reports/{id}/shares", handler.createShare)
	mux.HandleFunc("GET /api/v1/reports/{id}/shares", handler.listShares)
	mux.HandleFunc("POST /api/v1/reports/{id}/shares/{shareId}/revoke", handler.revokeShare)
	mux.HandleFunc("GET /api/v1/report-shares/{token}", handler.accessShare)
	mux.HandleFunc("POST /api/v1/reports/{id}/exports", handler.createExport)
	mux.HandleFunc("GET /api/v1/reports/{id}/exports/{exportId}", handler.getExport)
	mux.HandleFunc("GET /api/v1/reports/{id}/exports/{exportId}/download", handler.downloadExport)
	mux.HandleFunc("POST /api/v1/reports/{id}/exports/{exportId}/retry", handler.retryExport)
	mux.HandleFunc("POST /api/v1/reports/{id}/insights/{componentId}/derive", handler.deriveEvidence)
	mux.HandleFunc("POST /api/v1/reports/{id}/insights/{componentId}/generate", handler.generateInsight)
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

func (handler *Handler) createAI(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	// The production path uses the intent-layer Blueprint contract. Keeping the
	// legacy plan path below for one compatibility window lets older injected
	// generators and stored integration fixtures continue to operate.
	if handler.ai.BlueprintGenerator != nil && handler.ai.Kinds != nil {
		handler.createAIFromBlueprint(writer, request, identity)
		return
	}
	if handler.repository == nil || handler.aiAudit == nil || handler.ai.Selector == nil || handler.ai.Contexts == nil ||
		handler.ai.PlanGenerator == nil || handler.ai.Components == nil || handler.ai.Methods == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI creation is unavailable")
		return
	}
	var body struct {
		Intent        string     `json:"intent"`
		ReportType    string     `json:"reportType"`
		DataContextID askdata.ID `json:"dataContextId"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.Intent) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "report AI creation request is invalid")
		return
	}
	reportType, typeOK := resolveReportType(body.ReportType)
	if !typeOK {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "reportType must be REPORT or DASHBOARD")
		return
	}
	body.Intent = strings.TrimSpace(body.Intent)
	reportID := askdata.ID(uuid.NewString())
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	candidates, err := handler.ai.Contexts.Candidates(ctx, identity, 24)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if len(candidates) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_UNAVAILABLE", "no governed semantic asset is available for report AI")
		return
	}
	selection, err := reportai.SelectDataContext(ctx, handler.ai.Selector, reportai.DataContextSelectionRequest{
		Intent: body.Intent, Candidates: candidates,
	})
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_REJECTED", "report AI could not select a governed semantic asset")
		return
	}
	var selected reportai.DataContextCandidate
	for _, candidate := range candidates {
		if candidate.DataContext.ID == selection.DataContextID {
			selected = candidate
			break
		}
	}
	base := applyReportType(newAIReportDefinition(reportID, selection.ReportName, body.Intent, selected.DataContext), reportType)
	reportRecord, initial, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: reportID, Code: base.Metadata.Code, Name: base.Metadata.Name,
		ReportType: base.Metadata.ReportType, Definition: base,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	pageID := initial.Definition.Pages[0].ID
	scope := operation.Scope{PageID: &pageID}
	scopeJSON, _ := json.Marshal(scope)
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunGenerateDraft, PromptVersion: "report-plan-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{
			Intent: body.Intent, SelectionIDs: []string{string(selected.DataContext.ID)}, AvailableFields: selected.Fields,
		}, BaseRevision: &initial.RevisionNo, Scope: scopeJSON,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	plan, generationErr := reportai.GeneratePlan(ctx, handler.ai.PlanGenerator, reportai.PlanRequest{
		Intent: body.Intent, AllowedFieldNames: selected.Fields,
		AllowedComponents: reportAIComponentTypes(handler.ai.Components),
		AllowedMethods:    reportAIAnalysisMethods(handler.ai.Methods), TemplateVersions: []string{"1.0.0"},
	}, handler.ai.Components, handler.ai.Methods)
	if generationErr != nil {
		_ = handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunFailed, map[string]any{"stage": "plan"}, "REPORT_AI_PLAN_REJECTED")
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_PLAN_REJECTED", "report AI plan was rejected")
		return
	}
	generated, instantiateErr := reportai.Instantiate(plan, reportai.InstantiateInput{
		Base: initial.Definition, DataContextID: selected.DataContext.ID, AllowedFields: selected.Fields,
		AllMetricsAdditive: false, EstimatedRows: 20,
	}, handler.ai.Components, handler.ai.Methods)
	if instantiateErr != nil {
		_ = handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunRejected, map[string]any{"stage": "instantiate"}, "REPORT_AI_DRAFT_REJECTED")
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_DRAFT_REJECTED", "report AI draft was rejected")
		return
	}
	generated.Provenance.AIRunIDs = []askdata.ID{run.ID}
	generated.Provenance.PromptVersions = []string{"report-context-v1", "report-plan-v1"}
	generated.Provenance.ModelPolicies = []string{"governed-default"}
	createOperation := operation.Operation{Op: operation.ReportCreate, TargetID: reportID,
		Payload: &operation.ReportCreatePayload{Definition: generated}}
	if err := handler.aiAudit.CompletePreview(request.Context(), identity, run.ID, []operation.Operation{createOperation}, map[string]any{
		"sectionCount": len(plan.Sections), "dataContextId": selected.DataContext.ID,
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	draft, revision, err := handler.repository.SaveDraftWithRevision(request.Context(), identity, reportID, store.SaveInput{
		ExpectedRevision: initial.RevisionNo, Operations: []operation.Operation{createOperation},
		Source: string(operation.SourceAI), AIRunID: run.ID, Scope: &scope,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"report": reportRecord, "draft": draft, "revision": revision, "aiRunId": run.ID,
		"selection": selection, "plan": plan,
	})
}

func (handler *Handler) createAIFromBlueprint(writer http.ResponseWriter, request *http.Request, identity store.Identity) {
	if handler.repository == nil || handler.aiAudit == nil || handler.ai.Selector == nil || handler.ai.Contexts == nil ||
		handler.ai.Components == nil || handler.ai.Methods == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI blueprint creation is unavailable")
		return
	}
	var body struct {
		Intent        string     `json:"intent"`
		ReportType    string     `json:"reportType"`
		DataContextID askdata.ID `json:"dataContextId"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.Intent) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "report AI creation request is invalid")
		return
	}
	reportType, typeOK := resolveReportType(body.ReportType)
	if !typeOK {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "reportType must be REPORT or DASHBOARD")
		return
	}
	body.Intent = strings.TrimSpace(body.Intent)
	reportID := askdata.ID(uuid.NewString())
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID})
	candidates, err := handler.ai.Contexts.Candidates(ctx, identity, 24)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if len(candidates) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_UNAVAILABLE", "no governed semantic asset is available for report AI")
		return
	}
	selection := reportai.DataContextSelection{}
	if body.DataContextID != "" {
		for _, candidate := range candidates {
			if candidate.DataContext.ID == body.DataContextID {
				selection = reportai.DataContextSelection{DataContextID: candidate.DataContext.ID, ReportName: candidate.Name, Rationale: "用户在生成向导中指定数据来源", Confidence: "HIGH"}
				break
			}
		}
		if selection.DataContextID == "" {
			writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_REJECTED", "the requested data context is not available to this actor")
			return
		}
	} else {
		selection, err = reportai.SelectDataContext(ctx, handler.ai.Selector, reportai.DataContextSelectionRequest{Intent: body.Intent, Candidates: candidates})
		if err != nil {
			writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_REJECTED", "report AI could not select a governed semantic asset")
			return
		}
	}
	var selected reportai.DataContextCandidate
	for _, candidate := range candidates {
		if candidate.DataContext.ID == selection.DataContextID {
			selected = candidate
			break
		}
	}
	catalog, err := reportai.BuildBlueprintCatalog(candidates, []askdata.ID{selection.DataContextID})
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_REJECTED", err.Error())
		return
	}
	contextPack, err := reportai.BuildBlueprintContext(body.Intent, catalog, handler.ai.Kinds, handler.ai.Methods)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_CONTEXT_REJECTED", err.Error())
		return
	}
	base := applyReportType(newAIReportDefinition(reportID, selection.ReportName, body.Intent, selected.DataContext), reportType)
	reportRecord, initial, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: reportID, Code: base.Metadata.Code, Name: base.Metadata.Name, ReportType: base.Metadata.ReportType, Definition: base,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	pageID := initial.Definition.Pages[0].ID
	scope := operation.Scope{PageID: &pageID}
	scopeJSON, _ := json.Marshal(scope)
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunGenerateDraft, PromptVersion: "report-blueprint-v1", ModelPolicy: "governed-default",
		Summary:      reportai.RequestSummary{Intent: body.Intent, SelectionIDs: []string{string(selected.DataContext.ID)}, AvailableFields: selected.Fields},
		BaseRevision: &initial.RevisionNo, Scope: scopeJSON,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	generatedBlueprint, generationErr := reportai.GenerateBlueprint(ctx, handler.ai.BlueprintGenerator,
		blueprint.Request{Intent: body.Intent, ReportType: reportType, Context: contextPack}, catalog, handler.ai.Kinds, handler.ai.Components, handler.ai.Methods)
	if generationErr != nil {
		_ = handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunRejected, map[string]any{"stage": "blueprint"}, "REPORT_AI_BLUEPRINT_REJECTED")
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_BLUEPRINT_REJECTED", generationErr.Error())
		return
	}
	generated, expandErr := blueprint.Expand(generatedBlueprint, blueprint.ExpandInput{
		Base: initial.Definition, Catalog: catalog, CreatedFrom: reportmodel.CreatedAI,
		Kinds: handler.ai.Kinds, Components: handler.ai.Components, Methods: handler.ai.Methods,
	})
	if expandErr != nil {
		_ = handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunRejected, map[string]any{"stage": "expand"}, "REPORT_AI_BLUEPRINT_EXPAND_REJECTED")
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_BLUEPRINT_EXPAND_REJECTED", expandErr.Error())
		return
	}
	generated.Provenance.AIRunIDs = []askdata.ID{run.ID}
	generated.Provenance.PromptVersions = []string{"report-context-pack/1.0", "report-blueprint-v1"}
	generated.Provenance.ModelPolicies = []string{"governed-default"}
	createOperation := operation.Operation{Op: operation.ReportCreate, TargetID: reportID, Payload: &operation.ReportCreatePayload{Definition: generated}}
	if err := handler.aiAudit.CompletePreview(request.Context(), identity, run.ID, []operation.Operation{createOperation}, map[string]any{
		"sectionCount": len(generatedBlueprint.Sections), "cardCount": blueprintCardCount(generatedBlueprint), "datasetRefs": reportai.RequestedDatasetRefs(generatedBlueprint),
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	draft, revision, err := handler.repository.SaveDraftWithRevision(request.Context(), identity, reportID, store.SaveInput{
		ExpectedRevision: initial.RevisionNo, Operations: []operation.Operation{createOperation}, Source: string(operation.SourceAI), AIRunID: run.ID, Scope: &scope,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"report": reportRecord, "draft": draft, "revision": revision, "aiRunId": run.ID, "selection": selection,
		"blueprint": generatedBlueprint, "context": contextPack,
	})
}

func blueprintCardCount(value blueprint.Blueprint) int {
	total := 0
	for _, section := range value.Sections {
		for _, row := range section.Rows {
			total += len(row.Cards)
		}
	}
	return total
}

func newAIReportDefinition(reportID askdata.ID, name, intent string, dataContext reportmodel.DataContext) reportmodel.ReportDefinition {
	return newReportDefinition(reportID, name, intent, dataContext, reportmodel.CreatedAI)
}

// resolveReportType maps the optional creation-time choice onto the closed
// ReportType enum. 报告（REPORT）是分章节的分析文档，报表（DASHBOARD）是以卡片
// 与筛选器为主的看板；两者共用同一份 Report Definition、同一个编辑器与发布链，
// 类型只影响默认页面命名、运行页的展示策略与资产库分类。
func resolveReportType(raw string) (reportmodel.ReportType, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", string(reportmodel.ReportTypeReport):
		return reportmodel.ReportTypeReport, true
	case string(reportmodel.ReportTypeDashboard):
		return reportmodel.ReportTypeDashboard, true
	default:
		return "", false
	}
}

// applyReportType stamps the chosen type on a freshly built definition and
// renames the default page for dashboards.
func applyReportType(definition reportmodel.ReportDefinition, reportType reportmodel.ReportType) reportmodel.ReportDefinition {
	definition.Metadata.ReportType = reportType
	if reportType == reportmodel.ReportTypeDashboard {
		for index := range definition.Pages {
			if definition.Pages[index].Name == "报告正文" {
				definition.Pages[index].Name = "看板"
			}
		}
	}
	return definition
}

func newReportDefinition(
	reportID askdata.ID, name, description string,
	dataContext reportmodel.DataContext, createdFrom reportmodel.CreatedFrom,
) reportmodel.ReportDefinition {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "AI 智能报告"
	}
	prefix := "ai_report_"
	if createdFrom != reportmodel.CreatedAI {
		prefix = "report_"
	}
	code := prefix + strings.ReplaceAll(string(reportID), "-", "")[:16]
	return reportmodel.ReportDefinition{
		SchemaVersion: reportmodel.SchemaVersion,
		Metadata:      reportmodel.Metadata{ID: reportID, Code: code, Name: name, Description: description, ReportType: reportmodel.ReportTypeReport, Locale: "zh-CN"},
		TemplateRef:   reportmodel.TemplateReference{ReportTemplateID: "report_ai_default", ReportTemplateVersion: "1.0.0", StructureTemplateVersion: "1.0.0", LayoutTemplateVersion: "1.0.0", NarrativeTemplateVersion: "1.0.0"},
		ThemeRef:      reportmodel.ThemeReference{ThemeID: "theme_corporate_light", Version: "1.0.0"},
		Canvas:        reportmodel.Canvas{Desktop: reportmodel.DesktopCanvas{DesignWidth: 1920, Columns: 24, BaseCellWidth: 80, BaseRowHeight: 54, GapX: 12, GapY: 12, PaddingX: 24, PaddingY: 24}, Mobile: reportmodel.MobileCanvas{Columns: 1, GapY: 12, PaddingX: 12, PaddingY: 12}},
		DataContexts:  []reportmodel.DataContext{dataContext}, GlobalFilters: []reportmodel.GlobalFilter{},
		Pages:      []reportmodel.Page{{ID: askdata.ID(uuid.NewString()), Name: "报告正文", Order: 1, Sections: []reportmodel.Section{}}},
		Components: []reportmodel.Component{}, Interactions: []reportmodel.Interaction{},
		RuntimePolicy: reportmodel.RuntimePolicy{RefreshMode: reportmodel.RefreshOnOpen, MaxConcurrentQueries: 4, ComponentTimeoutMS: 10_000, ExportEnabled: true, FailureMode: reportmodel.FailurePartial},
		Provenance:    reportmodel.Provenance{CreatedFrom: createdFrom, SourceQuestionRunIDs: []askdata.ID{}, AIRunIDs: []askdata.ID{}},
	}
}

// componentManifests 暴露已注册的组件模板合同（角色白名单、维度/度量上下限、
// 默认选项）。设计器的数据绑定面板需要它才能只提交合法的绑定。
func (handler *Handler) componentManifests(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.identity(writer, request); !ok {
		return
	}
	if handler.ai.Components == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_COMPONENT_REGISTRY_UNAVAILABLE", "component manifest registry is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": handler.ai.Components.List()})
}

// cardKinds exposes the semantic vocabulary used by both the component panel
// and the model-facing Blueprint contract. Renderer candidates remain hints;
// the server resolves the final component after validating the data shape.
func (handler *Handler) cardKinds(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.identity(writer, request); !ok {
		return
	}
	if handler.ai.Kinds == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_CARD_KIND_REGISTRY_UNAVAILABLE", "card kind registry is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": handler.ai.Kinds.List()})
}

// expandBlueprint is the model-free configuration path: a human-authored or
// template-authored Blueprint passes through the same validation, card resolver
// and deterministic expansion used by AI creation.
func (handler *Handler) expandBlueprint(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	if handler.repository == nil || handler.ai.Contexts == nil || handler.ai.Kinds == nil || handler.ai.Components == nil || handler.ai.Methods == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_BLUEPRINT_UNAVAILABLE", "manual blueprint expansion is unavailable")
		return
	}
	var body struct {
		Blueprint      blueprint.Blueprint `json:"blueprint"`
		DataContextIDs []askdata.ID        `json:"dataContextIds"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_BLUEPRINT_REQUEST_INVALID", "blueprint request body is invalid")
		return
	}
	candidates, err := handler.ai.Contexts.Candidates(request.Context(), identity, 30)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if len(body.DataContextIDs) == 0 && len(candidates) != 0 {
		body.DataContextIDs = []askdata.ID{candidates[0].DataContext.ID}
	}
	catalog, err := reportai.BuildBlueprintCatalog(candidates, body.DataContextIDs)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_BLUEPRINT_CONTEXT_REJECTED", err.Error())
		return
	}
	reportID := askdata.ID(uuid.NewString())
	base := applyReportType(newReportDefinition(reportID, body.Blueprint.Title, "", catalog.Datasets[0].DataContext, reportmodel.CreatedManually), body.Blueprint.ReportType)
	definition, err := blueprint.Expand(body.Blueprint, blueprint.ExpandInput{
		Base: base, Catalog: catalog, CreatedFrom: reportmodel.CreatedManually,
		Kinds: handler.ai.Kinds, Components: handler.ai.Components, Methods: handler.ai.Methods,
	})
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_BLUEPRINT_REJECTED", err.Error())
		return
	}
	reportRecord, draft, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: reportID, Code: definition.Metadata.Code, Name: definition.Metadata.Name, ReportType: definition.Metadata.ReportType, Definition: definition,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"report": reportRecord, "draft": draft, "blueprint": body.Blueprint, "context": catalog})
}

// dataContexts 暴露当前用户可见的受治理数据上下文（已发布数据集版本及其允许字段）。
// 报告的空白新建与 DATASET_FIELD 数据绑定都依赖它，因此它不能依赖模型提供方。
func (handler *Handler) dataContexts(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	if handler.ai.Contexts == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_DATA_CONTEXT_UNAVAILABLE", "governed data context catalog is unavailable")
		return
	}
	candidates, err := handler.ai.Contexts.Candidates(request.Context(), identity, 30)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": candidates})
}

// createBlank 在不调用任何模型的情况下创建一份空白报告草稿。它是报告主链的
// 保底入口：未配置 LLM 提供方时，用户依然可以新建、绑定数据并发布报告。
// 画布、运行策略与查询策略在服务端固定，客户端不能自带这些受治理字段。
func (handler *Handler) createBlank(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	if handler.repository == nil || handler.ai.Contexts == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_DATA_CONTEXT_UNAVAILABLE", "governed data context catalog is unavailable")
		return
	}
	var body struct {
		Name          string     `json:"name"`
		Description   string     `json:"description"`
		DataContextID askdata.ID `json:"dataContextId"`
		// DataContextIDs lets the creation wizard declare every dataset the
		// report will use up front; the first one becomes the primary context.
		DataContextIDs []askdata.ID `json:"dataContextIds"`
		ReportType     string       `json:"reportType"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.Name) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "report name is required")
		return
	}
	reportType, typeOK := resolveReportType(body.ReportType)
	if !typeOK {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "reportType must be REPORT or DASHBOARD")
		return
	}
	candidates, err := handler.ai.Contexts.Candidates(request.Context(), identity, 30)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if len(candidates) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_DATA_CONTEXT_EMPTY", "no published dataset version is available in this business domain")
		return
	}
	// 数据上下文只能取自服务端候选，客户端 ID 不作为可信引用。
	requested := append([]askdata.ID(nil), body.DataContextIDs...)
	if body.DataContextID != "" {
		requested = append([]askdata.ID{body.DataContextID}, requested...)
	}
	selectedContexts := make([]reportmodel.DataContext, 0, len(requested)+1)
	seenContexts := map[askdata.ID]bool{}
	for _, wanted := range requested {
		if seenContexts[wanted] {
			continue
		}
		found := false
		for _, candidate := range candidates {
			if candidate.DataContext.ID == wanted {
				selectedContexts = append(selectedContexts, candidate.DataContext)
				seenContexts[wanted] = true
				found = true
				break
			}
		}
		if !found {
			writeError(writer, http.StatusUnprocessableEntity, "REPORT_DATA_CONTEXT_REJECTED", "the requested data context is not available to this actor")
			return
		}
	}
	if len(selectedContexts) == 0 {
		selectedContexts = append(selectedContexts, candidates[0].DataContext)
	}
	reportID := askdata.ID(uuid.NewString())
	definition := applyReportType(newReportDefinition(
		reportID, strings.TrimSpace(body.Name), strings.TrimSpace(body.Description),
		selectedContexts[0], reportmodel.CreatedManually,
	), reportType)
	definition.DataContexts = selectedContexts
	reportRecord, draft, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: reportID, Code: definition.Metadata.Code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"report": reportRecord, "draft": draft})
}

type reportStarterTemplate struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Category          string `json:"category"`
	ComponentCount    int    `json:"componentCount"`
	RequiresDimension bool   `json:"requiresDimension"`
}

var reportStarterTemplates = []reportStarterTemplate{
	{ID: "executive-overview", Name: "经营概览", Description: "核心指标、分类对比与明细表，适合经营例会和管理层快速阅览。", Category: "经营分析", ComponentCount: 3, RequiresDimension: true},
	{ID: "trend-analysis", Name: "趋势分析", Description: "按时间或业务维度观察指标变化，并保留明细数据用于追溯。", Category: "趋势洞察", ComponentCount: 2, RequiresDimension: true},
	{ID: "data-detail", Name: "数据明细", Description: "以可审计明细表为主体，适合核对、下钻和导出。", Category: "数据核对", ComponentCount: 1, RequiresDimension: false},
}

func (handler *Handler) reportTemplates(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.identity(writer, request); !ok {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": reportStarterTemplates})
}

func (handler *Handler) instantiateTemplate(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	if handler.repository == nil || handler.ai.Contexts == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_DATA_CONTEXT_UNAVAILABLE", "governed data context catalog is unavailable")
		return
	}
	templateID := strings.TrimSpace(request.PathValue("templateId"))
	known := false
	for _, item := range reportStarterTemplates {
		if item.ID == templateID {
			known = true
			break
		}
	}
	if !known {
		writeError(writer, http.StatusNotFound, "REPORT_TEMPLATE_NOT_FOUND", "report starter template was not found")
		return
	}
	var body struct {
		Name          string     `json:"name"`
		Description   string     `json:"description"`
		DataContextID askdata.ID `json:"dataContextId"`
		ReportType    string     `json:"reportType"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.Name) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "report name is required")
		return
	}
	reportType, typeOK := resolveReportType(body.ReportType)
	if !typeOK {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "reportType must be REPORT or DASHBOARD")
		return
	}
	candidates, err := handler.ai.Contexts.Candidates(request.Context(), identity, 30)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	selected, found := selectReportDataContext(candidates, body.DataContextID)
	if !found {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_DATA_CONTEXT_REJECTED", "the requested data context is not available to this actor")
		return
	}
	reportID := askdata.ID(uuid.NewString())
	definition, err := instantiateStarterDefinition(reportID, strings.TrimSpace(body.Name), strings.TrimSpace(body.Description), templateID, selected)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_TEMPLATE_NOT_APPLICABLE", err.Error())
		return
	}
	definition = applyReportType(definition, reportType)
	reportRecord, draft, err := handler.repository.CreateReport(request.Context(), identity, store.CreateInput{
		ID: reportID, Code: definition.Metadata.Code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"report": reportRecord, "draft": draft, "templateId": templateID})
}

func selectReportDataContext(candidates []reportai.DataContextCandidate, requested askdata.ID) (reportai.DataContextCandidate, bool) {
	if len(candidates) == 0 {
		return reportai.DataContextCandidate{}, false
	}
	if requested == "" {
		return candidates[0], true
	}
	for _, candidate := range candidates {
		if candidate.DataContext.ID == requested {
			return candidate, true
		}
	}
	return reportai.DataContextCandidate{}, false
}

func instantiateStarterDefinition(reportID askdata.ID, name, description, templateID string, candidate reportai.DataContextCandidate) (reportmodel.ReportDefinition, error) {
	definition := newReportDefinition(reportID, name, description, candidate.DataContext, reportmodel.CreatedTemplate)
	dimensions := []reportai.FieldDefinition{}
	measures := []reportai.FieldDefinition{}
	for _, field := range candidate.FieldDefinitions {
		if strings.EqualFold(field.Role, "MEASURE") {
			measures = append(measures, field)
		} else {
			dimensions = append(dimensions, field)
		}
	}
	if len(measures) == 0 {
		return reportmodel.ReportDefinition{}, errors.New("所选数据集没有可用度量字段，无法套用报告模板")
	}
	if (templateID == "executive-overview" || templateID == "trend-analysis") && len(dimensions) == 0 {
		return reportmodel.ReportDefinition{}, errors.New("所选数据集没有可用维度字段，无法套用该报告模板")
	}
	page := &definition.Pages[0]
	add := func(sectionName, componentType string, width, height int, dimensionBindings, measureBindings []reportmodel.FieldBinding, options reportmodel.ComponentOptions) {
		order := len(page.Sections) + 1
		componentID := askdata.ID(uuid.NewString())
		sectionID := askdata.ID(uuid.NewString())
		blockID := askdata.ID(uuid.NewString())
		zoneID := askdata.ID(uuid.NewString())
		slotID := askdata.ID(uuid.NewString())
		contextID := candidate.DataContext.ID
		definition.Components = append(definition.Components, reportmodel.Component{
			ID:          componentID,
			TemplateRef: reportmodel.ComponentTemplateReference{Type: componentType, Version: "1.0.0"},
			DataBinding: &reportmodel.DataBinding{BindingMode: reportmodel.BindingDatasetField, DataContextID: &contextID, Dimensions: dimensionBindings, Measures: measureBindings},
			Options:     options,
		})
		page.Sections = append(page.Sections, reportmodel.Section{ID: sectionID, Name: sectionName, Order: order, Blocks: []reportmodel.Block{{
			ID: blockID, Type: starterBlockType(componentType),
			Layout: reportmodel.BlockLayout{Desktop: reportmodel.DesktopBlockLayout{X: 0, Y: (order - 1) * height, W: width, H: height}, Mobile: reportmodel.MobileBlockLayout{Order: order, Visible: true, HeightMode: reportmodel.MobileHeightAuto, SlotMode: reportmodel.MobileSlotStack}},
			Zones:  []reportmodel.Zone{{ID: zoneID, Order: 1, Type: reportmodel.ZoneContent, Layout: reportmodel.ZoneLayout{HeightMode: reportmodel.ZoneHeightAuto, MinHeight: 1, Columns: width, Rows: height, Overflow: reportmodel.OverflowExpand, EmptyPriority: 1}, Slots: []reportmodel.Slot{{ID: slotID, Grid: reportmodel.SlotGrid{X: 0, Y: 0, W: width, H: height}, ComponentID: componentID}}}},
		}}})
	}
	value := func(field reportai.FieldDefinition, role reportmodel.BindingRole) reportmodel.FieldBinding {
		return reportmodel.FieldBinding{Role: role, Field: field.Code}
	}
	showLegend, showLabel := true, true
	switch templateID {
	case "executive-overview":
		add("核心指标", "metric-card", 8, 3, []reportmodel.FieldBinding{}, []reportmodel.FieldBinding{value(measures[0], reportmodel.RoleValue)}, reportmodel.ComponentOptions{Title: measures[0].Name, ShowLabel: &showLabel})
		add("分类对比", "bar-comparison", 12, 5, []reportmodel.FieldBinding{value(dimensions[0], reportmodel.RoleXAxis)}, []reportmodel.FieldBinding{value(measures[0], reportmodel.RoleYAxis)}, reportmodel.ComponentOptions{Title: dimensions[0].Name + "对比", ShowLegend: &showLegend, Orientation: reportmodel.OrientationVertical})
		add("经营明细", "data-table", 24, 7, []reportmodel.FieldBinding{value(dimensions[0], reportmodel.RoleDimension)}, []reportmodel.FieldBinding{value(measures[0], reportmodel.RoleValue)}, reportmodel.ComponentOptions{Title: "经营明细"})
	case "trend-analysis":
		add("指标趋势", "line-trend", 16, 5, []reportmodel.FieldBinding{value(dimensions[0], reportmodel.RoleXAxis)}, []reportmodel.FieldBinding{value(measures[0], reportmodel.RoleYAxis)}, reportmodel.ComponentOptions{Title: measures[0].Name + "趋势", ShowLegend: &showLegend})
		add("趋势明细", "data-table", 24, 7, []reportmodel.FieldBinding{value(dimensions[0], reportmodel.RoleDimension)}, []reportmodel.FieldBinding{value(measures[0], reportmodel.RoleValue)}, reportmodel.ComponentOptions{Title: "趋势明细"})
	case "data-detail":
		detailDimensions := make([]reportmodel.FieldBinding, 0, min(4, len(dimensions)))
		for _, field := range dimensions[:min(4, len(dimensions))] {
			detailDimensions = append(detailDimensions, value(field, reportmodel.RoleDimension))
		}
		detailMeasures := make([]reportmodel.FieldBinding, 0, min(3, len(measures)))
		for _, field := range measures[:min(3, len(measures))] {
			detailMeasures = append(detailMeasures, value(field, reportmodel.RoleValue))
		}
		add("数据明细", "data-table", 24, 9, detailDimensions, detailMeasures, reportmodel.ComponentOptions{Title: "数据明细"})
	default:
		return reportmodel.ReportDefinition{}, errors.New("报告模板不存在")
	}
	if err := definition.Validate(); err != nil {
		return reportmodel.ReportDefinition{}, fmt.Errorf("报告模板生成失败：%w", err)
	}
	return definition, nil
}

func starterBlockType(componentType string) reportmodel.BlockType {
	if componentType == "data-table" {
		return reportmodel.BlockTable
	}
	return reportmodel.BlockChart
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
	if !handler.trustDataContextOperations(writer, request, identity, bundle.Operations) {
		return
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

// trustDataContextOperations re-derives every DATA_CONTEXT_CREATE payload from
// the actor-visible governed catalog. The client only names the dataset
// version it wants; identifiers, dataset linkage and query policy come from the
// server so a report can never bind a dataset the actor cannot read. Aliases
// remain editable by people. AI bundles are not allowed to add data contexts.
func (handler *Handler) trustDataContextOperations(
	writer http.ResponseWriter, request *http.Request, identity store.Identity, operations []operation.Operation,
) bool {
	var candidates []reportai.DataContextCandidate
	for index := range operations {
		if operations[index].Op != operation.DataContextCreate {
			continue
		}
		if handler.ai.Contexts == nil {
			writeError(writer, http.StatusServiceUnavailable, "REPORT_DATA_CONTEXT_UNAVAILABLE", "governed data context catalog is unavailable")
			return false
		}
		if candidates == nil {
			loaded, err := handler.ai.Contexts.Candidates(request.Context(), identity, 30)
			if err != nil {
				writeReportError(writer, err)
				return false
			}
			candidates = loaded
		}
		payload := operations[index].Payload.(*operation.DataContextCreatePayload)
		trusted := false
		for _, candidate := range candidates {
			if candidate.DataContext.DatasetID != payload.DataContext.DatasetID ||
				candidate.DataContext.DatasetVersionID != payload.DataContext.DatasetVersionID {
				continue
			}
			resolved := candidate.DataContext
			if alias := strings.TrimSpace(payload.DataContext.Alias); alias != "" {
				resolved.Alias = alias
			}
			operations[index].Payload = &operation.DataContextCreatePayload{DataContext: resolved}
			trusted = true
			break
		}
		if !trusted {
			writeError(writer, http.StatusUnprocessableEntity, "REPORT_DATA_CONTEXT_REJECTED", "the requested data context is not available to this actor")
			return false
		}
	}
	return true
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

// aiCardBinding lets the model identify a card's dimensions and measures from
// one governed dataset. The suggestion is advisory: it is returned to the
// editor, and only a subsequent USER operation bundle writes a binding.
func (handler *Handler) aiCardBinding(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler == nil || handler.repository == nil || handler.aiAudit == nil || handler.ai.Fields == nil ||
		handler.ai.Components == nil || handler.ai.BindingSuggester == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI card binding is unavailable")
		return
	}
	definitions, ok := handler.ai.Fields.(reportai.FieldDefinitionCatalog)
	if !ok {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_AI_UNAVAILABLE", "report AI field catalog is unavailable")
		return
	}
	var body struct {
		ComponentID     askdata.ID `json:"componentId"`
		DataContextID   askdata.ID `json:"dataContextId"`
		ManifestType    string     `json:"manifestType"`
		ManifestVersion string     `json:"manifestVersion"`
		Title           string     `json:"title"`
		Intent          string     `json:"intent"`
	}
	if decodeJSON(request, &body) != nil || body.DataContextID.Validate() != nil || strings.TrimSpace(body.ManifestType) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_AI_REQUEST_INVALID", "report AI card binding request is invalid")
		return
	}
	draft, err := handler.repository.GetDraft(request.Context(), identity, reportID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	var dataContext *reportmodel.DataContext
	for index := range draft.Definition.DataContexts {
		if draft.Definition.DataContexts[index].ID == body.DataContextID {
			dataContext = &draft.Definition.DataContexts[index]
		}
	}
	if dataContext == nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_FIELDS_UNAVAILABLE", "the data context is not part of this report")
		return
	}
	var manifest *template.Manifest
	for _, candidate := range handler.ai.Components.List() {
		if candidate.Type == body.ManifestType && (body.ManifestVersion == "" || candidate.Version == body.ManifestVersion) {
			item := candidate
			manifest = &item
			break
		}
	}
	if manifest == nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_REQUEST_INVALID", "unknown component manifest")
		return
	}
	fieldDefinitions, err := definitions.AllowedFieldDefinitions(request.Context(), identity, *dataContext)
	if err != nil || len(fieldDefinitions) == 0 {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_FIELDS_UNAVAILABLE", "no governed fields are available for report AI")
		return
	}
	fields := make([]reportai.CardBindingField, 0, len(fieldDefinitions))
	fieldNames := make([]string, 0, len(fieldDefinitions))
	for _, field := range fieldDefinitions {
		fields = append(fields, reportai.CardBindingField{Code: field.Code, Name: field.Name, Role: field.Role, SemanticType: field.SemanticType, CanonicalType: field.CanonicalType})
		fieldNames = append(fieldNames, field.Code)
	}
	roles := make([]string, 0, len(manifest.DataContract.Roles))
	for _, role := range manifest.DataContract.Roles {
		roles = append(roles, string(role))
	}
	aiRequest := reportai.CardBindingRequest{
		CardTitle: strings.TrimSpace(body.Title), ComponentType: manifest.Type, ComponentName: manifest.DisplayName,
		Contract: reportai.CardBindingContract{
			DimensionsMin: manifest.DataContract.Dimensions.Min, DimensionsMax: manifest.DataContract.Dimensions.Max,
			MeasuresMin: manifest.DataContract.Measures.Min, MeasuresMax: manifest.DataContract.Measures.Max, Roles: roles,
		},
		DataContextName: dataContext.Alias, Fields: fields, Intent: strings.TrimSpace(body.Intent),
	}
	if err := reportai.ValidateCardBindingRequest(aiRequest); err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "REPORT_AI_REQUEST_INVALID", err.Error())
		return
	}
	selection := []string{string(body.DataContextID)}
	if body.ComponentID != "" {
		selection = append(selection, string(body.ComponentID))
	}
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunScopedEdit, PromptVersion: "report-card-binding-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{
			Intent: aiRequest.CardTitle + " " + aiRequest.Intent, SelectionIDs: selection, AvailableFields: fieldNames,
		}, BaseRevision: &draft.RevisionNo,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	suggestion, err := handler.ai.BindingSuggester.SuggestCardBinding(ctx, aiRequest)
	if err != nil {
		handler.finishAIError(writer, request.Context(), identity, run.ID, reportai.RunFailed, "REPORT_AI_GENERATION_FAILED")
		return
	}
	if err := handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunSucceeded, map[string]any{
		"dimensionCount": len(suggestion.Dimensions), "measureCount": len(suggestion.Measures), "componentType": manifest.Type,
	}, ""); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"aiRunId": run.ID, "suggestion": suggestion})
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
		SourceRevisionNo         *int64     `json:"sourceRevisionNo,omitempty"`
		AcknowledgeStaleInsights bool       `json:"acknowledgeStaleInsights"`
		PreviewedDesktop         bool       `json:"previewedDesktop"`
		PreviewedMobile          bool       `json:"previewedMobile"`
		ReviewRunID              askdata.ID `json:"reviewRunId"`
		HumanComment             string     `json:"humanComment"`
		AcknowledgedIssueCodes   []string   `json:"acknowledgedIssueCodes"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	if body.SourceRevisionNo == nil || handler.aiAudit == nil {
		writeError(writer, http.StatusBadRequest, "REPORT_PUBLISH_REVIEW_REQUIRED", "a completed AI publication review is required")
		return
	}
	warningCodes, err := handler.aiAudit.ValidatePublicationReview(request.Context(), identity, body.ReviewRunID, id, *body.SourceRevisionNo)
	if err != nil {
		writeError(writer, http.StatusConflict, "REPORT_PUBLISH_REVIEW_STALE", "publication review is missing or no longer matches the selected revision")
		return
	}
	acknowledged := map[string]struct{}{}
	for _, code := range body.AcknowledgedIssueCodes {
		acknowledged[strings.TrimSpace(code)] = struct{}{}
	}
	for _, code := range warningCodes {
		if _, exists := acknowledged[code]; !exists {
			writeError(writer, http.StatusUnprocessableEntity, "REPORT_PUBLISH_WARNING_UNACKNOWLEDGED", "all AI-reviewed publication warnings must be acknowledged")
			return
		}
	}
	version, err := handler.publisher.Publish(request.Context(), identity, publication.PublishRequest{
		ReportID: id, SourceRevisionNo: body.SourceRevisionNo,
		// A specific governance decision comes from its own field. Inferring it
		// from a blanket list of acknowledged codes let a client satisfy the
		// stale-insight gate without ever asking the publisher about it.
		AcknowledgeStaleInsights: body.AcknowledgeStaleInsights,
		PreviewedDesktop:         body.PreviewedDesktop, PreviewedMobile: body.PreviewedMobile,
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if handler.assetRepo != nil {
		if auditErr := handler.assetRepo.RecordPublishReview(request.Context(), identity, id, body.ReviewRunID,
			version.ID, version.VersionNo, body.HumanComment, body.AcknowledgedIssueCodes); auditErr != nil {
			writeError(writer, http.StatusInternalServerError, "REPORT_PUBLISH_REVIEW_AUDIT_FAILED", "report was published but the human review receipt could not be recorded")
			return
		}
	}
	writeJSON(writer, http.StatusCreated, version)
}

func (handler *Handler) publishReview(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	// 发布评审的裁决部分完全由确定性门禁产生，模型只负责叙述；因此缺少模型
	// 提供方不应阻断发布，只有缺少发布器、资产库或审计存储才是真正不可用。
	if handler.publisher == nil || handler.assetRepo == nil || handler.aiAudit == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_PUBLISH_REVIEW_UNAVAILABLE", "publication review is unavailable")
		return
	}
	var body struct {
		SourceRevisionNo *int64 `json:"sourceRevisionNo,omitempty"`
	}
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return
	}
	preflight, err := handler.publisher.Preflight(request.Context(), identity, publication.PreflightRequest{
		ReportID: reportID, SourceRevisionNo: body.SourceRevisionNo,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	impact, err := handler.assetRepo.PublicationImpact(request.Context(), identity, reportID)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	gates := make([]reportai.PublishGateSummary, 0, len(preflight.Checks))
	for _, check := range preflight.Checks {
		codes := make([]string, 0, len(check.Issues))
		for _, issue := range check.Issues {
			if !containsString(codes, issue.Code) {
				codes = append(codes, issue.Code)
			}
		}
		gates = append(gates, reportai.PublishGateSummary{ID: check.ID, Label: check.Label,
			Status: string(check.Status), IssueCodes: codes, Summary: check.Summary})
	}
	refs := publicationDependencyRefs(preflight.Draft.Definition)
	requestSummary := reportai.PublishReviewRequest{
		ReportTitle: preflight.Draft.Definition.Metadata.Name, SourceRevisionNo: preflight.Draft.RevisionNo,
		TargetVersionNo: impact.TargetVersionNo, DefinitionHash: preflight.Draft.DefinitionHash,
		Gates: gates, BlockerCodes: preflight.BlockerCodes, WarningCodes: preflight.WarningCodes,
		DependencyRefs: refs, Impact: reportai.PublishImpactSummary{
			VisibleCount: impact.VisibleCount, EditableCount: impact.EditableCount,
			SubscriptionCount: impact.SubscriptionCount, ActiveShareCount: impact.ActiveShareCount,
		},
	}
	run, err := handler.aiAudit.StartRun(request.Context(), identity, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunPublishReview, PromptVersion: "report-publish-review-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{
			Intent: "发布评审", SelectionIDs: append(append([]string{}, preflight.BlockerCodes...), preflight.WarningCodes...),
		}, BaseRevision: &preflight.Draft.RevisionNo,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	// 未配置模型提供方或模型评审失败时，回退到由确定性门禁直接生成的评审结论。
	// 两条路径的发布裁决完全一致，模型永远不能放宽门禁；来源记入审计与响应。
	review := reportai.DeterministicPublishReview(requestSummary)
	if handler.ai.Reviewer != nil {
		if generated, reviewErr := reportai.ReviewPublication(ctx, handler.ai.Reviewer, requestSummary); reviewErr == nil {
			review = generated
		}
	}
	if err := handler.aiAudit.FinishRun(request.Context(), identity, run.ID, reportai.RunSucceeded, map[string]any{
		"recommendation": review.Recommendation, "warningCodes": preflight.WarningCodes,
		"blockerCodes": preflight.BlockerCodes, "definitionHash": preflight.Draft.DefinitionHash,
		"checkCount": len(preflight.Checks), "riskCount": len(review.Risks),
		"reviewSource": review.Source,
	}, ""); err != nil {
		writeError(writer, http.StatusInternalServerError, "REPORT_AI_AUDIT_FAILED", "report AI audit failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"reviewRunId": run.ID, "checkedAt": time.Now().UTC(), "preflight": preflight,
		"impact": impact, "dependencyRefs": refs, "review": review,
	})
}

func publicationDependencyRefs(definition reportmodel.ReportDefinition) []string {
	result := []string{}
	for _, dataContext := range definition.DataContexts {
		result = append(result, "dataset:"+string(dataContext.DatasetVersionID))
	}
	for _, component := range definition.Components {
		if component.DataBinding == nil || component.DataBinding.SemanticQueryRef == nil {
			continue
		}
		ref := component.DataBinding.SemanticQueryRef
		value := "semantic:" + string(ref.SemanticReleaseID) + "@" + string(ref.SemanticContentHash)
		if !containsString(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

// runtimeSession resolves the requested execution target and opens the shared
// pipeline for it. Published and draft targets differ only here; everything
// after this point is identical, which is what makes a preview trustworthy.
func (handler *Handler) runtimeSession(
	writer http.ResponseWriter, request *http.Request, identity store.Identity, reportID askdata.ID, draft bool,
) (runtime.Session, runtime.HTTPPlanInput, bool) {
	var target runtime.ExecutionTarget
	if draft {
		if handler.repository == nil {
			writeError(writer, http.StatusServiceUnavailable, "REPORT_RUNTIME_UNAVAILABLE", "report draft store is unavailable")
			return runtime.Session{}, runtime.HTTPPlanInput{}, false
		}
		current, err := handler.repository.GetDraft(request.Context(), identity, reportID)
		if err != nil {
			writeReportError(writer, err)
			return runtime.Session{}, runtime.HTTPPlanInput{}, false
		}
		target = runtime.DraftTarget(reportID, current.Definition, current.DefinitionHash, current.RevisionNo)
	} else {
		versionNo, valid := optionalVersionNo(writer, request)
		if !valid {
			return runtime.Session{}, runtime.HTTPPlanInput{}, false
		}
		loaded, err := handler.loader.Load(request.Context(), identity, reportID, versionNo)
		if err != nil {
			writeReportError(writer, err)
			return runtime.Session{}, runtime.HTTPPlanInput{}, false
		}
		target = runtime.PublishedTarget(loaded)
	}
	var body runtime.HTTPPlanInput
	if decodeJSON(request, &body) != nil {
		writeError(writer, http.StatusBadRequest, "REPORT_REQUEST_INVALID", "request body is invalid")
		return runtime.Session{}, runtime.HTTPPlanInput{}, false
	}
	session, err := runtime.NewSession(identity, target, time.Now().UTC())
	if err != nil {
		writeReportError(writer, err)
		return runtime.Session{}, runtime.HTTPPlanInput{}, false
	}
	return session, body, true
}

// runtimeEnvelope reports which definition produced a result so a client can
// never mistake a draft preview for a published run.
func runtimeEnvelope(session runtime.Session) map[string]any {
	envelope := map[string]any{
		"reportId": session.Target.ReportID,
		"asOf":     session.AsOf,
		"timezone": session.Location.String(),
		"draft":    session.Target.Draft,
	}
	if session.Target.Draft {
		envelope["revisionNo"] = session.Target.RevisionNo
		envelope["definitionHash"] = session.Target.DefinitionHash
		return envelope
	}
	envelope["versionId"] = session.Target.VersionID
	envelope["versionNo"] = session.Target.VersionNo
	return envelope
}

func (handler *Handler) runtimePlan(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	session, body, ok := handler.runtimeSession(writer, request, identity, id, false)
	if !ok {
		return
	}
	plan, err := session.Plan(body)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	response := runtimeEnvelope(session)
	response["plan"] = plan
	writeJSON(writer, http.StatusOK, response)
}

// runtimeExecute runs an immutable published version for a viewer.
func (handler *Handler) runtimeExecute(writer http.ResponseWriter, request *http.Request) {
	handler.executeReport(writer, request, false)
}

// draftExecute runs the current editable draft for someone who can read it.
//
// Without it an author binds fields blind and only discovers whether the
// binding works after publishing a version. Execution still applies the calling
// viewer's row and column policy, so previewing grants no data the actor could
// not already query; what it deliberately does not do is create a version,
// write an artifact, or let its result be replayed as a published run.
func (handler *Handler) draftExecute(writer http.ResponseWriter, request *http.Request) {
	handler.executeReport(writer, request, true)
}

func (handler *Handler) executeReport(writer http.ResponseWriter, request *http.Request, draft bool) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.ai.Runtime == nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_RUNTIME_UNAVAILABLE", "report runtime is unavailable")
		return
	}
	session, body, ok := handler.runtimeSession(writer, request, identity, id, draft)
	if !ok {
		return
	}
	executionContext := database.WithAccessContext(request.Context(), string(identity.ActorID), string(identity.DomainID))
	results, err := session.Run(executionContext, body, handler.ai.Runtime)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	response := runtimeEnvelope(session)
	response["components"] = results
	writeJSON(writer, http.StatusOK, response)
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

func (handler *Handler) listShares(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	items, err := handler.shares.List(request.Context(), identity, reportID, 200)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
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
	// Validate the requested filters against the same session the export worker
	// will later run, so an export job is never queued with filters that only
	// fail once the worker picks it up.
	exportSession, err := runtime.NewSession(identity, runtime.PublishedTarget(runtime.LoadedReport{
		ReportID: reportID, VersionID: version.ID, VersionNo: version.VersionNo,
		DefinitionHash: version.DefinitionHash, Definition: version.Definition,
	}), asOf)
	if err != nil {
		writeReportError(writer, err)
		return
	}
	if _, err := (runtime.HTTPPlanInput{
		PageID: version.Definition.Pages[0].ID, FilterValues: body.Filters,
	}).Resolve(version.Definition, exportSession.AsOf, exportSession.Location, exportSession.PolicyScopeHash); err != nil {
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

func (handler *Handler) downloadExport(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if handler.exports == nil || handler.exportFiles == nil || handler.exportBucket == "" {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_EXPORT_UNAVAILABLE", "report export download is unavailable")
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
	if item.State != publication.ExportReady || item.ObjectURI == "" || item.ExpiresAt.Before(time.Now().UTC()) {
		writeError(writer, http.StatusConflict, "REPORT_EXPORT_NOT_READY", "report export is not ready for download")
		return
	}
	prefix := "s3://" + handler.exportBucket + "/"
	if !strings.HasPrefix(item.ObjectURI, prefix) {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_EXPORT_UNAVAILABLE", "report export artifact is unavailable")
		return
	}
	object, err := handler.exportFiles.Get(request.Context(), handler.exportBucket, strings.TrimPrefix(item.ObjectURI, prefix))
	if err != nil {
		writeError(writer, http.StatusNotFound, "REPORT_EXPORT_NOT_FOUND", "report export artifact was not found")
		return
	}
	defer object.Close()
	contentType := map[publication.ExportFormat]string{
		publication.ExportPDF: "application/pdf", publication.ExportPNG: "image/png",
		publication.ExportCSV:  "text/csv; charset=utf-8",
		publication.ExportXLSX: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}[item.Format]
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="report-%s.%s"`, item.ID, strings.ToLower(string(item.Format))))
	writer.Header().Set("Content-Length", strconv.FormatInt(item.ArtifactBytes, 10))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, object)
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

// deriveEvidence computes a component's Evidence Bundle from a live execution
// of that component.
//
// The previous endpoint accepted an Evidence Bundle from the caller. Because
// the bundle is exactly what the publication fact gate and the narrative
// verifier check a conclusion against, accepting one from a client meant anyone
// with edit rights could assert arbitrary "verified" numbers. The caller now
// chooses only which component to analyse and by which registered method;
// every fact comes from the result the server executed under that caller's own
// data permissions.
func (handler *Handler) deriveEvidence(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	componentID := askdata.ID(request.PathValue("componentId"))
	if handler.insights == nil || handler.repository == nil || handler.ai.Runtime == nil ||
		handler.ai.Methods == nil || handler.ai.Measures == nil || componentID.Validate() != nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_INSIGHT_UNAVAILABLE", "report evidence derivation is unavailable")
		return
	}
	var body struct {
		AnalysisMethod string `json:"analysisMethod"`
		TopN           int    `json:"topN"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.AnalysisMethod) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_EVIDENCE_INVALID", "analysisMethod is required")
		return
	}
	record, _, err := reportinsight.Deriver{
		Drafts: handler.repository, Executor: handler.ai.Runtime,
		Methods: handler.ai.Methods, Evidence: handler.insights, Measures: handler.ai.Measures,
	}.Derive(request.Context(), identity, reportinsight.DeriveRequest{
		ReportID: reportID, ComponentID: componentID,
		Method: insight.AnalysisMethod(strings.ToUpper(strings.TrimSpace(body.AnalysisMethod))), TopN: body.TopN,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}

// generateInsight derives fresh evidence for a component and writes a
// conclusion from it. Nothing is stored unless the prose passes the shared fact
// verifier, and the model never writes a figure: it emits markers that the
// server substitutes with values taken from the evidence itself.
func (handler *Handler) generateInsight(writer http.ResponseWriter, request *http.Request) {
	identity, reportID, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	componentID := askdata.ID(request.PathValue("componentId"))
	if handler.insights == nil || handler.repository == nil || handler.ai.Runtime == nil ||
		handler.ai.Methods == nil || handler.ai.Measures == nil || handler.ai.Narrative == nil ||
		componentID.Validate() != nil {
		writeError(writer, http.StatusServiceUnavailable, "REPORT_INSIGHT_UNAVAILABLE", "report conclusion generation is unavailable")
		return
	}
	var body struct {
		AnalysisMethod string `json:"analysisMethod"`
		TopN           int    `json:"topN"`
	}
	if decodeJSON(request, &body) != nil || strings.TrimSpace(body.AnalysisMethod) == "" {
		writeError(writer, http.StatusBadRequest, "REPORT_EVIDENCE_INVALID", "analysisMethod is required")
		return
	}
	evidence, objects, err := reportinsight.Deriver{
		Drafts: handler.repository, Executor: handler.ai.Runtime,
		Methods: handler.ai.Methods, Evidence: handler.insights, Measures: handler.ai.Measures,
	}.Derive(request.Context(), identity, reportinsight.DeriveRequest{
		ReportID: reportID, ComponentID: componentID,
		Method: insight.AnalysisMethod(strings.ToUpper(strings.TrimSpace(body.AnalysisMethod))), TopN: body.TopN,
	})
	if err != nil {
		writeReportError(writer, err)
		return
	}
	ctx := reportai.WithInvocationIdentity(request.Context(), reportai.InvocationIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, ReportID: reportID,
	})
	record, report, err := handler.ai.Narrative.Generate(ctx, identity, reportID, componentID, evidence, objects)
	if err != nil {
		// A rejected conclusion is a normal outcome, not a server fault: report
		// the verifier's own findings so the author can see what was unsupported.
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"code": "REPORT_INSIGHT_UNVERIFIED", "message": "生成的结论未通过事实校验，未予保存",
			"evidence": evidence, "verification": report,
		})
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"artifact": record, "evidence": evidence, "verification": report,
	})
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
