package datasetai

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
			result, err := planner.Plan(r.Context(), claims.TenantID, claims.Subject, resourceID, input)
			if err != nil {
				writePlanError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writePlanJSON(w, http.StatusOK, result)
		}
	}
	mux.Handle("POST /api/v1/datasets/ai/proposals", protect(nil, plan(false)))
	mux.Handle("POST /api/v1/datasets/{id}/ai/proposals", protect(func(r *http.Request) string { return r.PathValue("id") }, plan(true)))
	return mux
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

func writePlanError(w http.ResponseWriter, err error) {
	var providerErr *aiplatform.ProviderError
	var clarificationErr *ClarificationRequiredError
	switch {
	case errors.As(err, &clarificationErr):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_CLARIFICATION_REQUIRED", "message": clarificationErr.Question})
	case errors.Is(err, ErrCurrentRequired):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_CURRENT_REQUIRED", "message": "当前画布基线缺失，请重新打开数据集后再让 AI 修改"})
	case errors.Is(err, ErrInvalidRequest):
		writePlanJSON(w, http.StatusBadRequest, map[string]string{"code": "DATASET_AI_REQUEST_INVALID", "message": "请用 1 至 4000 个字符说明希望生成或修改的数据流程"})
	case errors.Is(err, ErrNoAssets):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_NO_ASSETS", "message": "暂无可用于建模的已映射启用表，请先完成数据资产映射"})
	case errors.Is(err, ErrContextStale):
		writePlanJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_AI_CONTEXT_STALE", "message": "生成期间表结构发生变化，请重新生成方案"})
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		writePlanJSON(w, http.StatusForbidden, map[string]string{"code": "AI_TENANT_FORBIDDEN", "message": "当前租户未启用数据集 AI 配置能力"})
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		writePlanJSON(w, http.StatusTooManyRequests, map[string]string{"code": "AI_QUOTA_EXCEEDED", "message": "当前租户 AI 配额已用尽，请稍后重试或联系管理员"})
	case errors.Is(err, ErrProviderUnavailable):
		writePlanJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "AI_PROVIDER_UNAVAILABLE", "message": "AI 配置服务暂时不可用，请联系管理员检查模型配置"})
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeTimeout:
		writePlanJSON(w, http.StatusGatewayTimeout, map[string]string{"code": "AI_TIMEOUT", "message": "AI 生成超时，原画布未发生变化，请重试"})
	case errors.Is(err, ErrInvalidOutput):
		metadata := invalidOutputMetadata(err)
		writePlanJSON(w, http.StatusBadGateway, planInvalidOutputResponse{
			Code:            "DATASET_AI_INVALID_OUTPUT",
			Message:         "AI 方案未通过数据集安全校验，原画布未发生变化，请修改要求后重试",
			ReasonCode:      safeInvalidOutputReason(metadata.ReasonCode),
			Stage:           safeInvalidOutputStage(metadata.Stage),
			RepairAttempted: metadata.RepairAttempted,
			RequestID:       metadata.RequestID,
		})
	default:
		writePlanJSON(w, http.StatusBadGateway, map[string]string{"code": "AI_COMPLETION_FAILED", "message": "AI 方案生成失败，原画布未发生变化，请稍后重试"})
	}
}

type planInvalidOutputResponse struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	ReasonCode      string `json:"reasonCode"`
	Stage           string `json:"stage"`
	RepairAttempted bool   `json:"repairAttempted"`
	RequestID       string `json:"requestId,omitempty"`
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
		InvalidOutputReasonOutput,
		InvalidOutputReasonChangeScope:
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
		InvalidOutputStageChangeSetValidation:
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
