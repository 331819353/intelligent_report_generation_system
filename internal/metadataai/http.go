package metadataai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler 注册智能补全生成、建议查询和人工决策接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_ASSET", action, nil, next))
	}
	mux.Handle("POST /api/v1/metadata-ai/tables/{id}/completions", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		result, err := service.Generate(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err, result)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /api/v1/metadata-ai/suggestions", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if err := validateSuggestionFilter(status); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_STATUS", "invalid suggestion status")
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				writeError(w, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit must be between 1 and 500")
				return
			}
			limit = parsed
		}
		items, err := service.ListSuggestions(r.Context(), claims.TenantID, r.URL.Query().Get("jobId"), status, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SUGGESTION_QUERY_FAILED", "failed to query metadata AI suggestions")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	})))
	mux.Handle("POST /api/v1/metadata-ai/suggestions/{id}/decision", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			Decision string `json:"decision"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := service.DecideSuggestion(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), strings.ToUpper(strings.TrimSpace(input.Decision)))
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				writeError(w, http.StatusNotFound, "SUGGESTION_NOT_FOUND", "metadata AI suggestion not found")
			case errors.Is(err, ErrConflict):
				writeError(w, http.StatusConflict, "SUGGESTION_CONFLICT", "suggestion is no longer pending or the asset changed or is locked")
			case errors.Is(err, ErrInvalidDecision):
				writeError(w, http.StatusBadRequest, "INVALID_DECISION", "decision must be ACCEPT or REJECT")
			default:
				writeError(w, http.StatusInternalServerError, "SUGGESTION_DECISION_FAILED", "failed to decide metadata AI suggestion")
			}
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	return mux
}

// writeServiceError 将领域错误映射为稳定的 HTTP 状态和错误码。
func writeServiceError(w http.ResponseWriter, err error, result GenerateResult) {
	switch {
	case errors.Is(err, ErrProviderUnavailable):
		writeError(w, http.StatusServiceUnavailable, "AI_PROVIDER_UNAVAILABLE", "metadata AI is not configured")
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "AI_TENANT_FORBIDDEN", "message": "当前租户未启用该 AI 能力", "job": result.Job})
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"code": "AI_QUOTA_EXCEEDED", "message": "当前租户 AI 配额已用尽", "job": result.Job})
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "table asset not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"code": "AI_TIMEOUT", "message": "metadata AI request timed out", "job": result.Job})
	case errors.Is(err, ErrInvalidOutput):
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "AI_INVALID_OUTPUT", "message": "metadata AI returned invalid structured output", "job": result.Job})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "AI_COMPLETION_FAILED", "message": "metadata AI completion failed", "job": result.Job})
	}
}

// decodeJSON 严格解析智能补全请求体。
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return false
	}
	return true
}

// writeError 输出智能补全模块的标准错误结构。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// writeJSON 输出智能补全模块的 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
