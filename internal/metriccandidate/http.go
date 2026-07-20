package metriccandidate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/metric"
)

// NewHandler 注册候选目录和人工审核接口。候选沿用 METRIC 的 READ/MANAGE
// 权限，并额外要求全局 DATASET/READ：列表会跨数据集返回字段、口径和来源事实，
// 因此第一阶段不向仅有单个数据集对象授权的调用者开放聚合目录。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, next http.Handler) http.Handler {
		datasetRead := access.Require(permissions, "DATASET", "READ", nil, next)
		return auth.RequireAccessToken(authService, access.Require(permissions, "METRIC", action, nil, datasetRead))
	}
	mux.Handle("GET /api/v1/metric-candidates", protect("READ", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		limit, offset, ok := candidatePage(writer, request)
		if !ok {
			return
		}
		items, total, err := service.List(request.Context(), claims.TenantID, ListFilter{
			Status: request.URL.Query().Get("status"), DatasetID: request.URL.Query().Get("datasetId"),
			Limit: limit, Offset: offset,
		})
		if err != nil {
			writeCandidateError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeCandidateJSON(writer, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	mux.Handle("GET /api/v1/metric-candidates/{id}", protect("READ", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		candidate, err := service.Get(request.Context(), claims.TenantID, request.PathValue("id"))
		if err != nil {
			writeCandidateError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeCandidateJSON(writer, http.StatusOK, candidate)
	})))
	mux.Handle("POST /api/v1/metric-candidates/{id}/reject", protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input RejectInput
		if !decodeCandidateRequest(writer, request, &input) {
			return
		}
		candidate, err := service.Reject(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input)
		if err != nil {
			writeCandidateError(writer, err)
			return
		}
		writeCandidateJSON(writer, http.StatusOK, candidate)
	})))
	mux.Handle("POST /api/v1/metric-candidates/{id}/accept", protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input AcceptInput
		if !decodeCandidateRequest(writer, request, &input) {
			return
		}
		result, err := service.Accept(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input)
		if err != nil {
			writeCandidateError(writer, err)
			return
		}
		writeCandidateJSON(writer, http.StatusCreated, result)
	})))
	return mux
}

func decodeCandidateRequest(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		writeCandidateJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体不是有效的候选指标 JSON"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeCandidateJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func candidatePage(writer http.ResponseWriter, request *http.Request) (int, int, bool) {
	limit, offset := 50, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		raw := request.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeCandidateJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
			return 0, 0, false
		}
		*target = value
	}
	if limit < 1 || limit > 200 || offset < 0 {
		writeCandidateJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
		return 0, 0, false
	}
	return limit, offset, true
}

func writeCandidateError(writer http.ResponseWriter, err error) {
	var validation *metric.ValidationError
	switch {
	case errors.As(err, &validation):
		writeCandidateJSON(writer, http.StatusUnprocessableEntity, map[string]any{"code": "METRIC_VALIDATION_FAILED", "message": "候选定义无法创建为指标草稿", "details": validation.Issues})
	case errors.Is(err, ErrNotFound):
		writeCandidateJSON(writer, http.StatusNotFound, map[string]string{"code": "METRIC_CANDIDATE_NOT_FOUND", "message": "候选指标不存在"})
	case errors.Is(err, ErrConflict):
		writeCandidateJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_CANDIDATE_VERSION_CONFLICT", "message": "候选已被其他请求处理，请重新加载"})
	case errors.Is(err, ErrBlocked):
		writeCandidateJSON(writer, http.StatusUnprocessableEntity, map[string]string{"code": "METRIC_CANDIDATE_BLOCKED", "message": "候选存在执行边界阻塞，不能创建指标草稿"})
	case errors.Is(err, ErrNotReviewable):
		writeCandidateJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_CANDIDATE_NOT_REVIEWABLE", "message": "候选当前状态不能执行该审核操作"})
	case errors.Is(err, metric.ErrAlreadyExists):
		writeCandidateJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_CODE_CONFLICT", "message": "候选指标编码已被其他指标使用"})
	case errors.Is(err, metric.ErrForbidden):
		writeCandidateJSON(writer, http.StatusForbidden, map[string]string{"code": "PERMISSION_DENIED", "message": "没有读取候选精确数据集的权限"})
	case errors.Is(err, metric.ErrVersionUnavailable), errors.Is(err, metric.ErrConflict):
		writeCandidateJSON(writer, http.StatusConflict, map[string]string{"code": "METRIC_CANDIDATE_SOURCE_UNAVAILABLE", "message": "候选固定的数据集版本已不可用，请重新提取"})
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, metric.ErrInvalidDefinition):
		writeCandidateJSON(writer, http.StatusBadRequest, map[string]string{"code": "METRIC_CANDIDATE_INVALID", "message": "候选请求或定义无效"})
	default:
		writeCandidateJSON(writer, http.StatusInternalServerError, map[string]string{"code": "METRIC_CANDIDATE_PERSISTENCE_FAILED", "message": "候选指标服务暂时不可用"})
	}
}

func writeCandidateJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
