package datasetai

import (
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
