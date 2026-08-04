package datasourceai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/auth"
)

type Planner interface {
	Turn(context.Context, string, string, string, TurnRequest) (TurnResult, error)
}

func NewHandler(authService *auth.Service, permissions *access.Service, planner Planner) http.Handler {
	mux := http.NewServeMux()
	protect := func(next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_SOURCE", "MANAGE", nil, next))
	}
	turn := func(editing bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input TurnRequest
			if !decodeTurn(w, r, &input) {
				return
			}
			sourceID := ""
			if editing {
				sourceID = r.PathValue("id")
			}
			result, err := planner.Turn(r.Context(), claims.TenantID, claims.Subject, sourceID, input)
			if err != nil {
				writeTurnError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, result)
		}
	}
	mux.Handle("POST /api/v1/data-sources/ai/turns", protect(turn(false)))
	mux.Handle("POST /api/v1/data-sources/{id}/ai/turns", protect(turn(true)))
	return mux
}

func decodeTurn(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "DATA_SOURCE_AI_REQUEST_INVALID", "message": "请输入有效的数据源配置要求"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "DATA_SOURCE_AI_REQUEST_INVALID", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func writeTurnError(w http.ResponseWriter, err error) {
	var providerErr *aiplatform.ProviderError
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "DATA_SOURCE_AI_REQUEST_INVALID", "message": "请说明希望新建或修改的数据源"})
	case errors.Is(err, ErrProviderUnavailable), errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeProviderUnavailable:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "AI_PROVIDER_UNAVAILABLE", "message": "数据源 AI 助手暂时不可用，请联系管理员检查模型配置"})
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "AI_TENANT_FORBIDDEN", "message": "当前租户未启用数据源 AI 配置能力"})
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"code": "AI_QUOTA_EXCEEDED", "message": "当前租户 AI 配额已用尽，请稍后重试"})
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeTimeout:
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"code": "AI_TIMEOUT", "message": "数据源 AI 助手响应超时，请重试"})
	case errors.Is(err, ErrInvalidOutput):
		writeJSON(w, http.StatusBadGateway, map[string]string{"code": "DATA_SOURCE_AI_OUTPUT_INVALID", "message": "AI 返回的连接配置未通过安全校验，请换一种方式描述"})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"code": "DATA_SOURCE_AI_FAILED", "message": "数据源 AI 助手处理失败，请稍后重试"})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
