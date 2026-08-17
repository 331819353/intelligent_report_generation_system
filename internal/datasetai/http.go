package datasetai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler exposes proposal-only endpoints for blank and existing datasets. Object-level
// DATASET permission checks remain distinct, and both routes also require asset read access.
func NewHandler(authService *auth.Service, permissions *access.Service, planner Planner) http.Handler {
	mux := http.NewServeMux()
	protect := func(objectID func(*http.Request) string, next http.Handler) http.Handler {
		assetRead := access.Require(permissions, "DATA_ASSET", "READ", nil, next)
		datasetManage := access.Require(permissions, "DATASET", "MANAGE", objectID, assetRead)
		return auth.RequireAccessToken(authService, datasetManage)
	}
	plan := func(editing bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input PlanRequest
			if !decodePlanRequest(w, r, &input) {
				return
			}
			resourceID := ""
			if editing {
				resourceID = r.PathValue("id")
			}
			streaming := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/x-ndjson")
			if streaming {
				flusher, ok := w.(http.Flusher)
				if !ok {
					writePlanJSON(w, http.StatusInternalServerError, map[string]string{"code": "AI_STREAM_UNAVAILABLE", "message": "当前服务不支持生成进度流"})
					return
				}
				w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusOK)
				encoder := json.NewEncoder(w)
				writeFrame := func(value any) {
					if err := encoder.Encode(value); err == nil {
						flusher.Flush()
					}
				}
				ctx := withPlanProgressReporter(r.Context(), func(event PlanProgressEvent) {
					writeFrame(planStreamProgressFrame{Type: "progress", Progress: event})
				})
				result, err := planner.Plan(ctx, claims.TenantID, claims.Subject, resourceID, input)
				if err != nil {
					if errors.Is(err, ErrInvalidOutput) {
						slog.WarnContext(r.Context(), "dataset AI proposal failed validation", "resource_id", resourceID, "error", err)
					}
					status, body := capturePlanError(err)
					writeFrame(planStreamErrorFrame{Type: "error", Status: status, Error: body})
					return
				}
				writeFrame(planStreamResultFrame{Type: "result", Result: result})
				return
			}
			result, err := planner.Plan(r.Context(), claims.TenantID, claims.Subject, resourceID, input)
			if err != nil {
				if errors.Is(err, ErrInvalidOutput) {
					slog.WarnContext(r.Context(), "dataset AI proposal failed validation", "resource_id", resourceID, "error", err)
				}
				writePlanError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writePlanJSON(w, http.StatusOK, result)
		}
	}
	sessionManager, hasSessions := planner.(SessionManager)
	requireSessions := func(next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !hasSessions {
				writePlanError(w, ErrSessionStoreUnavailable)
				return
			}
			next(w, r)
		}
	}
	openSession := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input SessionOpenRequest
		if !decodeOptionalRequest(w, r, &input) {
			return
		}
		session, err := sessionManager.OpenSession(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	getSession := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		session, err := sessionManager.GetSession(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"))
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	findDatasetSession := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		session, found, err := sessionManager.FindDatasetSession(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"))
		if err != nil {
			writePlanError(w, err)
			return
		}
		if !found {
			writePlanJSON(w, http.StatusNotFound, map[string]string{"code": "DATASET_AI_SESSION_NOT_FOUND", "message": "该数据集当前没有进行中的 AI 建模会话"})
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	confirmScope := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input SessionScopeRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		session, err := sessionManager.ConfirmSessionScope(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	prepareIntent := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input SessionIntentRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		session, err := sessionManager.PrepareSessionIntent(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	blueprintTurn := func(run func(ctx context.Context, tenantID, actorID, sessionID string, input BlueprintRevisionRequest) (ModelingSession, error), decodeBody bool) http.HandlerFunc {
		return requireSessions(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input BlueprintRevisionRequest
			if decodeBody && !decodePlanRequest(w, r, &input) {
				return
			}
			streaming := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/x-ndjson")
			if streaming {
				flusher, ok := w.(http.Flusher)
				if !ok {
					writePlanJSON(w, http.StatusInternalServerError, map[string]string{"code": "AI_STREAM_UNAVAILABLE", "message": "当前服务不支持生成进度流"})
					return
				}
				w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusOK)
				encoder := json.NewEncoder(w)
				writeFrame := func(value any) {
					if err := encoder.Encode(value); err == nil {
						flusher.Flush()
					}
				}
				ctx := withPlanProgressReporter(r.Context(), func(event PlanProgressEvent) {
					writeFrame(planStreamProgressFrame{Type: "progress", Progress: event})
				})
				session, err := run(ctx, claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
				if err != nil {
					status, body := capturePlanError(err)
					writeFrame(planStreamErrorFrame{Type: "error", Status: status, Error: body})
					return
				}
				writeFrame(sessionStreamResultFrame{Type: "result", Session: session})
				return
			}
			session, err := run(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
			if err != nil {
				writePlanError(w, err)
				return
			}
			writePlanJSON(w, http.StatusOK, session)
		})
	}
	screenSources := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input SourceScreeningRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		input.SessionID = r.PathValue("sessionId")
		streaming := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/x-ndjson")
		if streaming {
			flusher, ok := w.(http.Flusher)
			if !ok {
				writePlanJSON(w, http.StatusInternalServerError, map[string]string{"code": "AI_STREAM_UNAVAILABLE", "message": "当前服务不支持生成进度流"})
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			encoder := json.NewEncoder(w)
			writeFrame := func(value any) {
				if err := encoder.Encode(value); err == nil {
					flusher.Flush()
				}
			}
			ctx := withPlanProgressReporter(r.Context(), func(event PlanProgressEvent) {
				writeFrame(planStreamProgressFrame{Type: "progress", Progress: event})
			})
			session, err := sessionManager.ScreenSources(ctx, claims.TenantID, claims.Subject, input)
			if err != nil {
				status, body := capturePlanError(err)
				writeFrame(planStreamErrorFrame{Type: "error", Status: status, Error: body})
				return
			}
			writeFrame(sessionStreamResultFrame{Type: "result", Session: session})
			return
		}
		session, err := sessionManager.ScreenSources(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	generateBlueprint := blueprintTurn(func(ctx context.Context, tenantID, actorID, sessionID string, _ BlueprintRevisionRequest) (ModelingSession, error) {
		return sessionManager.GenerateBlueprint(ctx, tenantID, actorID, sessionID)
	}, false)
	reviseBlueprint := blueprintTurn(func(ctx context.Context, tenantID, actorID, sessionID string, input BlueprintRevisionRequest) (ModelingSession, error) {
		return sessionManager.ReviseBlueprint(ctx, tenantID, actorID, sessionID, input.Instruction)
	}, true)
	resolveStage := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input StageResolution
		if !decodePlanRequest(w, r, &input) {
			return
		}
		session, err := sessionManager.ResolveBlueprintStage(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	recordSessionEvent := requireSessions(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input SessionEventRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		session, err := sessionManager.ResolveSessionProposal(r.Context(), claims.TenantID, claims.Subject, r.PathValue("sessionId"), input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		writePlanJSON(w, http.StatusOK, session)
	})
	classifyIntake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		classifier, ok := planner.(IntakeClassifier)
		if !ok {
			writePlanJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "AI_PROVIDER_UNAVAILABLE", "message": "AI 配置服务暂时不可用，请联系管理员检查模型配置"})
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input IntakeRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		classification, err := classifier.ClassifyIntake(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writePlanJSON(w, http.StatusOK, classification)
	})
	suggestTables := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suggester, ok := planner.(TableSuggester)
		if !ok {
			writePlanJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "AI_RETRIEVAL_UNAVAILABLE", "message": "候选表检索服务暂时不可用"})
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input TableSuggestionRequest
		if !decodePlanRequest(w, r, &input) {
			return
		}
		result, err := suggester.SuggestTables(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writePlanError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writePlanJSON(w, http.StatusOK, result)
	})
	datasetObjectID := func(r *http.Request) string { return r.PathValue("id") }
	mux.Handle("POST /api/v1/datasets/ai/table-suggestions", protect(nil, suggestTables))
	mux.Handle("POST /api/v1/datasets/ai/intake", protect(nil, classifyIntake))
	mux.Handle("POST /api/v1/datasets/ai/proposals", protect(nil, plan(false)))
	mux.Handle("POST /api/v1/datasets/{id}/ai/proposals", protect(datasetObjectID, plan(true)))
	mux.Handle("POST /api/v1/datasets/ai/sessions", protect(nil, openSession))
	mux.Handle("GET /api/v1/datasets/ai/sessions/{sessionId}", protect(nil, getSession))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/intent", protect(nil, prepareIntent))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/scope", protect(nil, confirmScope))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/sources/screen", protect(nil, screenSources))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/blueprint", protect(nil, generateBlueprint))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/blueprint/revisions", protect(nil, reviseBlueprint))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/stages", protect(nil, resolveStage))
	mux.Handle("POST /api/v1/datasets/ai/sessions/{sessionId}/events", protect(nil, recordSessionEvent))
	mux.Handle("POST /api/v1/datasets/{id}/ai/session", protect(datasetObjectID, openSession))
	mux.Handle("GET /api/v1/datasets/{id}/ai/session", protect(datasetObjectID, findDatasetSession))
	return mux
}

// SessionManager is the session surface the HTTP layer needs. The concrete
// *Service implements it; deployments without a session store simply do not.
type SessionManager interface {
	OpenSession(ctx context.Context, tenantID, actorID, datasetID string, input SessionOpenRequest) (ModelingSession, error)
	GetSession(ctx context.Context, tenantID, actorID, sessionID string) (ModelingSession, error)
	FindDatasetSession(ctx context.Context, tenantID, actorID, datasetID string) (ModelingSession, bool, error)
	ConfirmSessionScope(ctx context.Context, tenantID, actorID, sessionID string, input SessionScopeRequest) (ModelingSession, error)
	PrepareSessionIntent(ctx context.Context, tenantID, actorID, sessionID string, input SessionIntentRequest) (ModelingSession, error)
	ResolveSessionProposal(ctx context.Context, tenantID, actorID, sessionID string, input SessionEventRequest) (ModelingSession, error)
	ScreenSources(ctx context.Context, tenantID, actorID string, input SourceScreeningRequest) (ModelingSession, error)
	GenerateBlueprint(ctx context.Context, tenantID, actorID, sessionID string) (ModelingSession, error)
	ReviseBlueprint(ctx context.Context, tenantID, actorID, sessionID, instruction string) (ModelingSession, error)
	ResolveBlueprintStage(ctx context.Context, tenantID, actorID, sessionID string, input StageResolution) (ModelingSession, error)
}

// IntakeClassifier is the model-kind classification surface for the CREATE intake.
type IntakeClassifier interface {
	ClassifyIntake(ctx context.Context, tenantID, actorID string, input IntakeRequest) (IntakeClassification, error)
}

type planStreamProgressFrame struct {
	Type     string            `json:"type"`
	Progress PlanProgressEvent `json:"progress"`
}

type planStreamResultFrame struct {
	Type   string     `json:"type"`
	Result PlanResult `json:"result"`
}

// BlueprintRevisionRequest is the natural-language change request on a blueprint.
type BlueprintRevisionRequest struct {
	Instruction string `json:"instruction"`
}

type sessionStreamResultFrame struct {
	Type    string          `json:"type"`
	Session ModelingSession `json:"session"`
}

type planStreamErrorFrame struct {
	Type   string          `json:"type"`
	Status int             `json:"status"`
	Error  json.RawMessage `json:"error"`
}

type planErrorCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (capture *planErrorCapture) Header() http.Header {
	if capture.header == nil {
		capture.header = make(http.Header)
	}
	return capture.header
}

func (capture *planErrorCapture) WriteHeader(status int) {
	if capture.status == 0 {
		capture.status = status
	}
}

func (capture *planErrorCapture) Write(value []byte) (int, error) {
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	return capture.body.Write(value)
}

func capturePlanError(err error) (int, json.RawMessage) {
	capture := &planErrorCapture{}
	writePlanError(capture, err)
	if capture.status == 0 {
		capture.status = http.StatusBadGateway
	}
	return capture.status, append(json.RawMessage(nil), capture.body.Bytes()...)
}

func decodePlanRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请输入有效的数据集配置目标"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

// decodeOptionalRequest accepts an empty body (older clients send none or "{}")
// and otherwise applies the strict single-document rule.
func decodeOptionalRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请求体无法读取"})
		return false
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请求体格式无效"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func writePlanError(w http.ResponseWriter, err error) {
	var providerErr *aiplatform.ProviderError
	var clarificationErr *ClarificationRequiredError
	switch {
	case errors.As(err, &clarificationErr):
		candidates := clarificationErr.Candidates
		if candidates == nil {
			candidates = []ClarificationCandidate{}
		}
		writePlanJSON(w, http.StatusConflict, planClarificationResponse{
			Code:       "DATASET_AI_CLARIFICATION_REQUIRED",
			Message:    clarificationErr.Question,
			Candidates: candidates,
		})
	case errors.Is(err, ErrCurrentRequired):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_CURRENT_REQUIRED", "message": "当前画布基线缺失，请重新打开数据集后再让 AI 修改"})
	case errors.Is(err, ErrSessionNotFound):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_SESSION_STALE", "message": "AI 建模会话已失效，系统将开始新的会话"})
	case errors.Is(err, ErrSessionConflict):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_SESSION_CONFLICT", "message": "AI 建模会话正被其他窗口更新，请稍后重试"})
	case errors.Is(err, ErrScopeRequired):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_SCOPE_REQUIRED", "message": "请先确认业务目标、模型类型与数据表范围，再生成建模蓝图"})
	case errors.Is(err, ErrBlueprintRequired):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_BLUEPRINT_PENDING", "message": "建模蓝图还有待确认的阶段，请先在蓝图卡片中确认或调整后再生成 DAG"})
	case errors.Is(err, ErrSessionStoreUnavailable):
		writePlanJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "DATASET_AI_SESSION_UNAVAILABLE", "message": "AI 建模会话存储暂不可用，本次将以无会话模式继续"})
	case errors.Is(err, ErrInvalidRequest):
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请用 1 至 4000 个字符说明希望生成或修改的数据流程"})
	case errors.Is(err, ErrNoAssets):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_NO_ASSETS", "message": "暂无可用于建模的已映射启用表，请先完成数据资产映射"})
	case errors.Is(err, ErrContextStale):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_CONTEXT_STALE", "message": "生成期间表结构发生变化，请重新生成方案"})
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		writePlanJSON(w, http.StatusForbidden, map[string]string{"code": "AI_TENANT_FORBIDDEN", "message": "当前平台未启用数据集 AI 配置能力"})
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		writePlanJSON(w, http.StatusTooManyRequests, map[string]string{"code": "AI_QUOTA_EXCEEDED", "message": "当前平台 AI 配额已用尽，请稍后重试或联系管理员"})
	case errors.Is(err, ErrProviderUnavailable):
		writePlanJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "AI_PROVIDER_UNAVAILABLE", "message": "AI 配置服务暂时不可用，请联系管理员检查模型配置"})
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeTimeout:
		writePlanJSON(w, http.StatusGatewayTimeout, map[string]string{"code": "AI_TIMEOUT", "message": "AI 生成超时，原画布未发生变化，请重试"})
	case errors.Is(err, ErrInvalidOutput):
		metadata := invalidOutputMetadata(err)
		diagnostic := publicInvalidOutputDiagnostic(metadata)
		writePlanJSON(w, http.StatusBadGateway, planInvalidOutputResponse{
			Code:            "DATASET_AI_INVALID_OUTPUT",
			Message:         diagnostic.Message,
			ReasonCode:      safeInvalidOutputReason(metadata.ReasonCode),
			Stage:           safeInvalidOutputStage(metadata.Stage),
			RepairAttempted: metadata.RepairAttempted,
			RequestID:       metadata.RequestID,
			DiagnosticCode:  diagnostic.Code,
			Suggestion:      diagnostic.Suggestion,
		})
	default:
		writePlanJSON(w, http.StatusBadGateway, map[string]string{"code": "AI_COMPLETION_FAILED", "message": "AI 方案生成失败，原画布未发生变化，请稍后重试"})
	}
}

// planClarificationResponse keeps message as the question for existing clients while
// exposing the structured candidate components for the clarification card.
type planClarificationResponse struct {
	Code       string                   `json:"code"`
	Message    string                   `json:"message"`
	Candidates []ClarificationCandidate `json:"candidates"`
}

type planInvalidOutputResponse struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	ReasonCode      string `json:"reasonCode"`
	Stage           string `json:"stage"`
	RepairAttempted bool   `json:"repairAttempted"`
	RequestID       string `json:"requestId,omitempty"`
	DiagnosticCode  string `json:"diagnosticCode"`
	Suggestion      string `json:"suggestion"`
}

type invalidOutputDiagnostic struct {
	Code       string
	Message    string
	Suggestion string
}

// publicInvalidOutputDiagnostic maps trusted local validator categories to safe, actionable
// copy. It never returns the raw detail, which can contain catalog identifiers or model text.
func publicInvalidOutputDiagnostic(metadata InvalidOutputError) invalidOutputDiagnostic {
	detail := strings.ToLower(metadata.Detail)
	base := invalidOutputDiagnostic{
		Code:       "PLAN_VALIDATION_FAILED",
		Message:    "AI 方案未通过数据集安全校验，原画布未发生变化。",
		Suggestion: "请按原要求重试；系统会重新分析完整画布，无需提供组件 ID。若存在多个同等合理目标，界面会继续向你确认。",
	}
	switch {
	case strings.Contains(detail, "clarify requires"):
		return invalidOutputDiagnostic{"CLARIFICATION_QUESTION_MISSING", "AI 判断需要补充信息，但没有生成可回答的问题，原画布已保留。", "请按原要求重试；若仍出现此提示，请补充目标组件和要处理的字段。"}
	case strings.Contains(detail, "plan contains undeclared"):
		return invalidOutputDiagnostic{"UNDECLARED_COMPONENT_CHANGE", "AI 方案额外改动了本次要求之外的组件或字段，已为你拦截。", "请按原要求重试；系统会从完整画布重新推断必要修改，不需要指定技术组件名。"}
	case strings.Contains(detail, "outside locked scope"):
		return invalidOutputDiagnostic{"COMPONENT_FIELDS_MISMATCH", "AI 方案修改了目标范围之外的配置，原画布已保留。", "请按原要求重试；系统只会应用与你的业务目标直接相关的变化。"}
	case strings.Contains(detail, "did not realize locked"):
		return invalidOutputDiagnostic{"REQUESTED_CHANGE_MISSING", "AI 方案没有完整落实本次要求，原画布已保留。", "请按原要求重试；系统会重新检查所有上下游并补全必要连线。"}
	case strings.Contains(detail, "input rewiring differs"):
		return invalidOutputDiagnostic{"INPUT_CONNECTION_MISMATCH", "AI 方案的组件连线与你的业务要求不一致，已阻止应用。", "请按原要求重试；系统会基于完整链路重新确定上下游。"}
	case strings.Contains(detail, "downstream consumer") || strings.Contains(detail, "downstream input change"):
		return invalidOutputDiagnostic{"COMPONENT_NOT_CONNECTED", "AI 生成的处理步骤没有完整接入数据链路，原画布已保留。", "请按原要求重试；系统会自动定位最近的有效下游，无需提供组件名称或 ID。"}
	case strings.Contains(detail, "field propagation") || strings.Contains(detail, "does not reach end"):
		return invalidOutputDiagnostic{"FIELD_LINEAGE_INCOMPLETE", "AI 方案中有字段没有从上游完整传递到分组或最终输出。", "请写明该字段是分组维度、聚合指标还是仅用于最终展示。"}
	case metadata.ReasonCode == InvalidOutputReasonTransform:
		return invalidOutputDiagnostic{"TRANSFORM_COMPONENT_REQUIRED", "当前要求需要字段处理组件，但 AI 方案没有正确生成或使用该产物。", "请写明输入字段、处理方式和下游用途，例如“将支付时间转为年月，再进入分组组件”。"}
	case metadata.ReasonCode == InvalidOutputReasonJoin:
		return invalidOutputDiagnostic{"JOIN_CONFIGURATION_INVALID", "AI 方案的关联输入或关联字段不可用。", "请指明左右两张表、关联字段和 INNER/LEFT 关联方式。"}
	case metadata.ReasonCode == InvalidOutputReasonGroup || metadata.ReasonCode == InvalidOutputReasonAggregationField:
		return invalidOutputDiagnostic{"GROUP_CONFIGURATION_INVALID", "AI 方案的分组维度或聚合指标不完整。", "请分别写明分组维度、指标字段和 SUM/COUNT 等聚合方式；日期粒度请使用独立日期转换组件。"}
	case metadata.ReasonCode == InvalidOutputReasonFieldReference || metadata.ReasonCode == InvalidOutputReasonFieldCaseMismatch:
		return invalidOutputDiagnostic{"FIELD_REFERENCE_INVALID", "AI 方案引用了当前映射表中不可用的字段。", "请在要求中选用画布上已有的字段名，或先在数据节点中勾选该字段。"}
	case metadata.ReasonCode == InvalidOutputReasonBlueprint && metadata.Stage == InvalidOutputStagePlanValidation:
		return invalidOutputDiagnostic{"BLUEPRINT_NOT_REALIZED", "AI 方案没有完整落实已确认的建模蓝图（关联、指标口径、过滤或输出），原画布已保留。", "请按原要求重试；若持续出现，可在蓝图卡片中重新打开对应阶段调整后再生成。"}
	case metadata.ReasonCode == InvalidOutputReasonBlueprint:
		return invalidOutputDiagnostic{"BLUEPRINT_INVALID", "AI 生成的建模蓝图引用了范围外的表或字段，或缺少该类型必需的阶段。", "请重试生成蓝图；若持续出现，可调整候选表范围或补充业务目标描述。"}
	case metadata.ReasonCode == InvalidOutputReasonOutput:
		return invalidOutputDiagnostic{"FINAL_OUTPUT_INVALID", "AI 方案的最终输出中包含上游未产生的字段。", "请明确最终保留哪些字段，并确保它们已由上游数据、分组或字段处理组件产生。"}
	}
	return base
}

func safeInvalidOutputReason(value string) string {
	switch value {
	case InvalidOutputReasonResponseFormat,
		InvalidOutputReasonProviderResponse,
		InvalidOutputReasonSchema,
		InvalidOutputReasonGraph,
		InvalidOutputReasonTableReference,
		InvalidOutputReasonFieldReference,
		InvalidOutputReasonFieldCaseMismatch,
		InvalidOutputReasonAggregationField,
		InvalidOutputReasonJoin,
		InvalidOutputReasonGroup,
		InvalidOutputReasonTransform,
		InvalidOutputReasonOutput,
		InvalidOutputReasonChangeScope,
		InvalidOutputReasonBlueprint:
		return value
	default:
		return InvalidOutputReasonUnknown
	}
}

func safeInvalidOutputStage(value string) string {
	switch value {
	case InvalidOutputStageIntentResponse,
		InvalidOutputStageIntentValidation,
		InvalidOutputStagePlannerResponse,
		InvalidOutputStagePlanValidation,
		InvalidOutputStageChangeSetValidation,
		InvalidOutputStageBlueprintValidation:
		return value
	default:
		return InvalidOutputStagePlanValidation
	}
}

func writePlanJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
