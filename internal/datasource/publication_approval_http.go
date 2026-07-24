package datasource

import (
	"errors"
	"log/slog"
	"net/http"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewPublicationApprovalHandler owns every public publication route. The legacy /publish
// endpoint is a submission alias so an HTTP caller cannot bypass human review.
func NewPublicationApprovalHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *PublicationApprovalService,
	credentials CredentialManager,
) http.Handler {
	mux := http.NewServeMux()
	objectID := func(request *http.Request) string { return request.PathValue("id") }
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_SOURCE", action, objectID, next))
	}
	submit := protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input SubmitPublicationInput
		if !decodeDS(writer, request, &input) {
			return
		}
		record, err := service.Submit(
			request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input,
		)
		if err != nil {
			writeDataSourceReviewError(writer, request, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDSJSON(writer, http.StatusAccepted, record)
	}))
	mux.Handle("POST /api/v1/data-sources/{id}/publish", submit)
	mux.Handle("POST /api/v1/data-sources/{id}/publish-requests", submit)
	mux.Handle("GET /api/v1/data-sources/{id}/publish-requests", protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		items, err := service.List(request.Context(), claims.TenantID, request.PathValue("id"))
		if err != nil {
			writeDataSourceReviewError(writer, request, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDSJSON(writer, http.StatusOK, PublicationRequestPage{Items: items, Total: len(items)})
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/withdraw", protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input ReviewPublicationInput
		if !decodeDS(writer, request, &input) {
			return
		}
		record, err := service.Withdraw(
			request.Context(), claims.TenantID, claims.Subject,
			request.PathValue("id"), request.PathValue("requestId"), input,
		)
		if err != nil {
			writeDataSourceReviewError(writer, request, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDSJSON(writer, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/approve", protect("PUBLISH", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input ReviewPublicationInput
		if !decodeDS(writer, request, &input) {
			return
		}
		record, published, err := service.Approve(
			request.Context(), claims.TenantID, claims.Subject,
			request.PathValue("id"), request.PathValue("requestId"), input,
		)
		if err != nil {
			writeDataSourceReviewError(writer, request, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDSJSON(writer, http.StatusOK, map[string]any{
			"request": record,
			"source":  publicDataSource(request.Context(), published, credentials),
		})
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/publish-requests/{requestId}/reject", protect("PUBLISH", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input ReviewPublicationInput
		if !decodeDS(writer, request, &input) {
			return
		}
		record, err := service.Reject(
			request.Context(), claims.TenantID, claims.Subject,
			request.PathValue("id"), request.PathValue("requestId"), input,
		)
		if err != nil {
			writeDataSourceReviewError(writer, request, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDSJSON(writer, http.StatusOK, record)
	})))
	return mux
}

func writeDataSourceReviewError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrConnectionTestPending):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_TEST_PENDING", "连接测试仍在执行，请等待测试完成后再提交审核")
	case errors.Is(err, ErrConnectionTestFailed), errors.Is(err, ErrTestRequired):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_TEST_REQUIRED", "当前配置尚未通过连接测试；请先点击“测试连接”并处理失败原因")
	case errors.Is(err, ErrTestExpired):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_TEST_EXPIRED", "连接测试结果已过期，请重新测试当前配置")
	case errors.Is(err, ErrSourceVersionChanged):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_VERSION_CHANGED", "配置在测试或审核期间发生变化，请重新测试后提交")
	case errors.Is(err, ErrReviewPending):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_REVIEW_PENDING", "该数据源已有审核中的申请，请先撤销或等待审核")
	case errors.Is(err, ErrReviewRequestNotFound):
		writeDSError(writer, http.StatusNotFound, "DATA_SOURCE_REVIEW_NOT_FOUND", "未找到数据源审核申请")
	case errors.Is(err, ErrReviewRequestConflict):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_REVIEW_VERSION_CONFLICT", "审核状态已变化，请刷新页面后重试")
	case errors.Is(err, ErrReviewRequestNotPending):
		writeDSError(writer, http.StatusConflict, "DATA_SOURCE_REVIEW_NOT_PENDING", "该审核申请已处理，不能重复操作")
	case errors.Is(err, ErrReviewWithdrawForbidden):
		writeDSError(writer, http.StatusForbidden, "DATA_SOURCE_REVIEW_WITHDRAW_FORBIDDEN", "只有提交申请的用户可以撤销")
	case errors.Is(err, ErrReviewSelfApproval):
		writeDSError(writer, http.StatusForbidden, "DATA_SOURCE_REVIEW_SELF_APPROVAL_FORBIDDEN", "提交人不能审核自己的发布申请")
	case errors.Is(err, ErrInvalidConfiguration):
		writeDSError(writer, http.StatusBadRequest, "DATA_SOURCE_REVIEW_INPUT_INVALID", "审核说明不符合要求；驳回原因不能为空且最多 1000 字")
	default:
		slog.ErrorContext(request.Context(), "data source publication review failed", "error", err)
		writeDSError(writer, http.StatusBadRequest, "DATA_SOURCE_REVIEW_FAILED", "数据源审核操作失败，请刷新状态后重试")
	}
}
