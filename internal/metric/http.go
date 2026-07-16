package metric

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/dataset"
)

// NewHandler 注册指标草稿、试算、发布和精确版本管理接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, objectID func(*http.Request) string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "METRIC", action, objectID, next))
	}
	objectID := func(request *http.Request) string { return request.PathValue("id") }

	mux.Handle("POST /api/v1/metrics", protect("MANAGE", nil, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input CreateInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		record, err := service.Create(request.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusCreated, record)
	})))
	mux.Handle("GET /api/v1/metrics", protect("READ", nil, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		limit, offset, ok := metricPage(writer, request)
		if !ok {
			return
		}
		items, total, err := service.List(request.Context(), claims.TenantID, limit, offset)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	mux.Handle("GET /api/v1/metrics/{id}", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		record, err := service.Get(request.Context(), claims.TenantID, request.PathValue("id"))
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, record)
	})))
	mux.Handle("PUT /api/v1/metrics/{id}/draft", protect("MANAGE", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input UpdateInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		record, err := service.Update(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/metrics/{id}/validate", protect("MANAGE", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !decodeMetricRequest(writer, request, &struct{}{}) {
			return
		}
		claims, _ := auth.ClaimsFromContext(request.Context())
		prepared, err := service.ValidateCurrent(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"))
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusOK, map[string]any{"valid": true, "definitionHash": prepared.DefinitionHash})
	})))
	mux.Handle("POST /api/v1/metrics/{id}/preview", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input PreviewInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		result, err := service.Preview(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, result)
	})))
	mux.Handle("POST /api/v1/metrics/{id}/publish", protect("PUBLISH", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		key := request.Header.Get("Idempotency-Key")
		if !validIdempotencyKey(key) {
			writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_IDEMPOTENCY_KEY", "message": "Idempotency-Key 必须为 1 到 128 字节，且不能包含首尾空白或控制字符"})
			return
		}
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input PublishInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		record, err := service.Publish(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), key, input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusCreated, record)
	})))
	mux.Handle("GET /api/v1/metrics/{id}/versions", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		limit, offset, ok := metricPage(writer, request)
		if !ok {
			return
		}
		items, total, err := service.ListVersions(request.Context(), claims.TenantID, request.PathValue("id"), limit, offset)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	mux.Handle("GET /api/v1/metrics/{id}/versions/{versionId}", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		record, err := service.GetVersion(request.Context(), claims.TenantID, request.PathValue("id"), request.PathValue("versionId"))
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/metrics/{id}/versions/{versionId}/preview", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input PreviewInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		result, err := service.PreviewVersion(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), request.PathValue("versionId"), input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, result)
	})))
	mux.Handle("GET /api/v1/metrics/{id}/versions/{versionId}/usage", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		usage, err := service.GetVersionUsage(request.Context(), claims.TenantID, request.PathValue("id"), request.PathValue("versionId"))
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeMetricJSON(writer, http.StatusOK, usage)
	})))
	mux.Handle("POST /api/v1/metrics/{id}/versions/{versionId}/status", protect("PUBLISH", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input VersionTransitionInput
		if !decodeMetricRequest(writer, request, &input) {
			return
		}
		record, err := service.TransitionVersion(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), request.PathValue("versionId"), input)
		if err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/metrics/{id}/query-runs/{queryId}/cancel", protect("READ", objectID, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		if err := service.Cancel(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), request.PathValue("queryId")); err != nil {
			writeMetricError(writer, err)
			return
		}
		writeMetricJSON(writer, http.StatusOK, map[string]bool{"cancelled": true})
	})))
	return mux
}

func decodeMetricRequest(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 2<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体不是有效的指标 JSON"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func writeMetricError(writer http.ResponseWriter, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		writeMetricJSON(writer, http.StatusUnprocessableEntity, map[string]any{"code": "METRIC_VALIDATION_FAILED", "message": "指标定义或发布校验失败", "details": validation.Issues})
	case errors.Is(err, ErrNotFound):
		writeMetricJSON(writer, http.StatusNotFound, map[string]string{"code": "METRIC_NOT_FOUND", "message": "指标不存在"})
	case errors.Is(err, ErrVersionNotFound):
		writeMetricJSON(writer, http.StatusNotFound, map[string]string{"code": "METRIC_VERSION_NOT_FOUND", "message": "指标版本不存在"})
	case errors.Is(err, ErrVersionUnavailable):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_VERSION_UNAVAILABLE", "message": "指标版本已失效、废弃或依赖不可用"})
	case errors.Is(err, ErrConflict):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_VERSION_CONFLICT", "message": "指标已被其他请求修改，请重新加载"})
	case errors.Is(err, ErrIdempotencyConflict):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_IDEMPOTENCY_CONFLICT", "message": "Idempotency-Key 已绑定其他发布请求"})
	case errors.Is(err, ErrAlreadyExists):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_CODE_CONFLICT", "message": "指标编码已存在"})
	case errors.Is(err, ErrForbidden):
		writeMetricJSON(writer, http.StatusForbidden, map[string]string{"code": "PERMISSION_DENIED", "message": "没有执行指标操作的权限"})
	case errors.Is(err, ErrInvalidTransition):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_VERSION_TRANSITION_INVALID", "message": "指标版本状态迁移无效"})
	case errors.Is(err, ErrVersionInUse):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_VERSION_IN_USE", "message": "仍有可运行的已发布下游指标依赖该版本，请先处理下游版本"})
	case errors.Is(err, ErrPreviewUnavailable):
		writeMetricJSON(writer, http.StatusServiceUnavailable, map[string]string{"code": "METRIC_PREVIEW_UNAVAILABLE", "message": "指标试算服务暂时不可用"})
	case errors.Is(err, dataset.ErrPreviewInvalid):
		writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "METRIC_PREVIEW_INVALID", "message": "指标试算参数或派生计划无效"})
	case errors.Is(err, dataset.ErrPreviewUnsupported):
		writeMetricJSON(writer, http.StatusUnprocessableEntity, map[string]string{"code": "METRIC_PREVIEW_SOURCE_UNSUPPORTED", "message": "首阶段指标试算仅支持单源数据库数据集"})
	case errors.Is(err, dataset.ErrPreviewTimeout):
		writeMetricJSON(writer, http.StatusGatewayTimeout, map[string]string{"code": "METRIC_PREVIEW_TIMEOUT", "message": "指标试算超时并已发起取消"})
	case errors.Is(err, dataset.ErrQueryNotFound):
		writeMetricJSON(writer, http.StatusNotFound, map[string]string{"code": "QUERY_RUN_NOT_FOUND", "message": "查询不存在、已结束或无权取消"})
	case errors.Is(err, dataset.ErrQueryConflict):
		writeMetricJSON(writer, http.StatusConflict, map[string]string{"code": "QUERY_ID_CONFLICT", "message": "查询标识已被使用"})
	case errors.Is(err, dataset.ErrPreviewFailed), errors.Is(err, ErrPreviewFailed):
		writeMetricJSON(writer, http.StatusBadGateway, map[string]string{"code": "METRIC_PREVIEW_FAILED", "message": "指标试算执行失败"})
	case errors.Is(err, ErrInvalidDefinition):
		writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "METRIC_DEFINITION_INVALID", "message": "指标定义无效或引用的资源不可用"})
	default:
		writeMetricJSON(writer, http.StatusInternalServerError, map[string]string{"code": "METRIC_PERSISTENCE_FAILED", "message": "指标服务暂时不可用"})
	}
}

func metricPage(writer http.ResponseWriter, request *http.Request) (int, int, bool) {
	limit, offset := 50, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		raw := request.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
			return 0, 0, false
		}
		*target = value
	}
	if limit < 1 || limit > 200 || offset < 0 {
		writeMetricJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
		return 0, 0, false
	}
	return limit, offset, true
}

func writeMetricJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
