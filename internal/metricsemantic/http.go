package metricsemantic

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		query := request.URL.Query().Get("q")
		limit := 10
		if raw := request.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "检索数量无效"})
				return
			}
			limit = parsed
		}
		result, err := service.Search(request.Context(), claims.TenantID, query, limit)
		if err != nil {
			if errors.Is(err, ErrInvalidRequest) {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请输入 1 至 1000 个字符的指标检索要求"})
				return
			}
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"code": "METRIC_RETRIEVAL_FAILED", "message": "指标语义检索暂不可用"})
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeJSON(writer, http.StatusOK, result)
	})
	// 聚合检索需要全局指标与数据集读取能力；对象级过滤由未来报告生成器的精确绑定阶段再次复核。
	protected := access.Require(permissions, "DATASET", "READ", nil,
		access.Require(permissions, "METRIC", "READ", nil, handler))
	mux.Handle("GET /api/v1/metrics/semantic-search", auth.RequireAccessToken(authService, protected))
	return mux
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
