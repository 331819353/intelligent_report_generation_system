package materialization

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const maxBuildControlBodyBytes = 4096

// NewControlHandler registers the server-derived materialization control
// plane. The only writable body is CreateBuildInput; there is no route that
// accepts a plan, SQL, input snapshot or physical relation identifier.
func NewControlHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *ControlService,
) http.Handler {
	mux := http.NewServeMux()
	objectID := func(request *http.Request) string {
		return request.PathValue("id")
	}
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(
			authService,
			access.Require(permissions, "DATASET", action, objectID, next),
		)
	}
	noQuery := func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.RawQuery == "" {
			return true
		}
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"该接口不接受查询参数",
		)
		return false
	}

	mux.Handle(
		"POST /api/v1/datasets/{id}/materializations/builds",
		protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !noQuery(writer, request) {
				return
			}
			var input CreateBuildInput
			if !decodeBuildControlRequest(writer, request, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			result, created, err := service.Register(
				request.Context(), claims.TenantID, claims.Subject,
				request.PathValue("id"), input,
			)
			if err != nil {
				writeMaterializationError(writer, err)
				return
			}
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeControlJSON(writer, status, result)
		})),
	)
	mux.Handle(
		"GET /api/v1/datasets/{id}/materializations/builds",
		protect("READ", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			limit, offset, ok := buildPage(request)
			if !ok {
				writeControlError(
					writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_PAGE",
					"分页参数无效",
				)
				return
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			result, err := service.List(
				request.Context(), claims.TenantID, request.PathValue("id"),
				limit, offset,
			)
			if err != nil {
				writeMaterializationError(writer, err)
				return
			}
			writeControlJSON(writer, http.StatusOK, result)
		})),
	)
	mux.Handle(
		"GET /api/v1/datasets/{id}/materializations/builds/{buildId}",
		protect("READ", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !noQuery(writer, request) {
				return
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			result, err := service.Get(
				request.Context(), claims.TenantID, request.PathValue("id"),
				request.PathValue("buildId"),
			)
			if err != nil {
				writeMaterializationError(writer, err)
				return
			}
			writeControlJSON(writer, http.StatusOK, result)
		})),
	)
	mux.Handle(
		"POST /api/v1/datasets/{id}/materializations/builds/{buildId}/cancel",
		protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !noQuery(writer, request) ||
				!requireEmptyBuildControlBody(writer, request) {
				return
			}
			claims, _ := auth.ClaimsFromContext(request.Context())
			result, err := service.Cancel(
				request.Context(), claims.TenantID, claims.Subject,
				request.PathValue("id"), request.PathValue("buildId"),
			)
			if err != nil {
				writeMaterializationError(writer, err)
				return
			}
			writeControlJSON(writer, http.StatusOK, result)
		})),
	)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(writer, request)
	})
}

func decodeBuildControlRequest(
	writer http.ResponseWriter,
	request *http.Request,
	target any,
) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeControlError(
			writer, http.StatusUnsupportedMediaType, "MATERIALIZATION_JSON_REQUIRED",
			"请求体必须使用 application/json",
		)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBuildControlBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeControlError(
				writer, http.StatusRequestEntityTooLarge,
				"MATERIALIZATION_REQUEST_TOO_LARGE", "请求体过大",
			)
			return false
		}
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"请求体不是有效的物化构建 JSON",
		)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"请求体只能包含一个 JSON 文档",
		)
		return false
	}
	return true
}

func requireEmptyBuildControlBody(
	writer http.ResponseWriter,
	request *http.Request,
) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBuildControlBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"无法读取请求体",
		)
		return false
	}
	if strings.TrimSpace(string(body)) != "" {
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"取消接口不接受请求体",
		)
		return false
	}
	return true
}

func buildPage(request *http.Request) (int, int, bool) {
	query := request.URL.Query()
	for key, values := range query {
		if (key != "limit" && key != "offset") || len(values) != 1 {
			return 0, 0, false
		}
	}
	limit, offset := DefaultBuildPageLimit, 0
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > MaxBuildPageLimit {
			return 0, 0, false
		}
		limit = value
	}
	if raw := query.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, false
		}
		offset = value
	}
	return limit, offset, true
}

func writeMaterializationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeControlError(
			writer, http.StatusBadRequest, "MATERIALIZATION_INVALID_REQUEST",
			"物化构建请求无效",
		)
	case errors.Is(err, ErrNotFound):
		writeControlError(
			writer, http.StatusNotFound, "MATERIALIZATION_NOT_FOUND",
			"数据集或物化构建不存在",
		)
	case errors.Is(err, ErrInvalidTransition):
		writeControlError(
			writer, http.StatusConflict, "MATERIALIZATION_INVALID_TRANSITION",
			"只有排队中的物化构建可以取消",
		)
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIdempotencyConflict):
		writeControlError(
			writer, http.StatusConflict, "MATERIALIZATION_CONFLICT",
			"当前发布版本、上游物化或构建状态已变化，请重新加载",
		)
	default:
		writeControlError(
			writer, http.StatusInternalServerError, "MATERIALIZATION_INTERNAL_ERROR",
			"物化控制面暂时不可用",
		)
	}
}

func writeControlError(
	writer http.ResponseWriter,
	status int,
	code, message string,
) {
	writeControlJSON(writer, status, map[string]string{
		"code": code, "message": message,
	})
}

func writeControlJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
