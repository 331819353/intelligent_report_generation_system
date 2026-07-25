package backgroundtask

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

func NewHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *Service,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/background-tasks", auth.RequireAccessToken(
		authService,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			view := strings.ToUpper(strings.TrimSpace(request.URL.Query().Get("view")))
			if view == "" {
				view = ViewActive
			}
			limit := 100
			if raw := request.URL.Query().Get("limit"); raw != "" {
				value, err := strconv.Atoi(raw)
				if err != nil {
					writeError(writer, http.StatusBadRequest, "BACKGROUND_TASK_INVALID_REQUEST", "limit 必须是整数")
					return
				}
				limit = value
			}
			for key, values := range request.URL.Query() {
				if (key != "view" && key != "limit") || len(values) != 1 {
					writeError(writer, http.StatusBadRequest, "BACKGROUND_TASK_INVALID_REQUEST", "查询参数无效")
					return
				}
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			page, err := service.List(request.Context(), claims.TenantID, view, limit)
			if err != nil {
				writeServiceError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, page)
		}),
	))
	mux.Handle("POST /api/v1/background-tasks/{kind}/{id}/cancel", auth.RequireAccessToken(
		authService,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			body, bodyErr := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1024))
			if request.URL.RawQuery != "" || bodyErr != nil || strings.TrimSpace(string(body)) != "" {
				writeError(writer, http.StatusBadRequest, "BACKGROUND_TASK_INVALID_REQUEST", "中止接口不接受请求体或查询参数")
				return
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			kind := strings.ToUpper(strings.TrimSpace(request.PathValue("kind")))
			task, err := service.Find(request.Context(), claims.TenantID, kind, request.PathValue("id"))
			if err != nil {
				writeServiceError(writer, err)
				return
			}
			allowed, err := permissions.Allowed(request.Context(), access.Check{
				TenantID: claims.TenantID, UserID: claims.Subject,
				ResourceType: task.ResourceType, Action: "MANAGE", ObjectID: task.ResourceID,
			})
			if err != nil {
				writeError(writer, http.StatusInternalServerError, "PERMISSION_EVALUATION_FAILED", "权限检查失败")
				return
			}
			if !allowed {
				writeError(writer, http.StatusForbidden, "PERMISSION_DENIED", "需要对应数据源或数据集的管理权限")
				return
			}
			task, err = service.Cancel(
				request.Context(), claims.TenantID, claims.Subject, kind, request.PathValue("id"),
			)
			if err != nil {
				writeServiceError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, task)
		}),
	))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(writer, request)
	})
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeError(writer, http.StatusBadRequest, "BACKGROUND_TASK_INVALID_REQUEST", "后台任务请求无效")
	case errors.Is(err, ErrNotFound):
		writeError(writer, http.StatusNotFound, "BACKGROUND_TASK_NOT_FOUND", "后台任务不存在")
	case errors.Is(err, ErrNotActive):
		writeError(writer, http.StatusConflict, "BACKGROUND_TASK_NOT_ACTIVE", "任务已经结束，请刷新列表")
	case errors.Is(err, ErrNotCancellable):
		writeError(writer, http.StatusConflict, "BACKGROUND_TASK_NOT_CANCELLABLE", "该任务当前不支持安全中止")
	default:
		writeError(writer, http.StatusInternalServerError, "BACKGROUND_TASK_OPERATION_FAILED", "后台任务操作失败")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
