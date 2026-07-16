package report

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/reportjson"
)

// NewHandler 注册报告创建、草稿加载、批量语义变更和修订查询接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, withObject bool, next http.Handler) http.Handler {
		var objectID func(*http.Request) string
		if withObject {
			objectID = func(r *http.Request) string { return r.PathValue("id") }
		}
		guarded := nextWithAccess(permissions, action, objectID, next)
		if withObject {
			guarded = requireReportUUID(guarded)
		}
		return auth.RequireAccessToken(authService, guarded)
	}

	mux.Handle("POST /api/v1/reports", protect("CREATE", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		var input CreateInput
		if !decodeReportRequest(w, r, &input) {
			return
		}
		record, err := service.Create(r.Context(), claims.TenantID, claims.Subject, key, input)
		if err != nil {
			writeReportError(w, err)
			return
		}
		record.Capabilities.Edit = true
		writeReportJSON(w, http.StatusCreated, record)
	})))

	mux.Handle("GET /api/v1/reports/{id}/draft", protect("READ", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		record, err := service.Get(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"))
		if err != nil {
			writeReportError(w, err)
			return
		}
		canEdit, err := permissions.Allowed(r.Context(), access.Check{TenantID: claims.TenantID, UserID: claims.Subject, ResourceType: "REPORT", Action: "UPDATE", ObjectID: r.PathValue("id")})
		if err != nil {
			writeReportError(w, err)
			return
		}
		record.Capabilities.Edit = canEdit
		writeReportJSON(w, http.StatusOK, record)
	})))

	mux.Handle("PUT /api/v1/reports/{id}/draft", protect("UPDATE", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		var input UpdateInput
		if !decodeReportRequest(w, r, &input) {
			return
		}
		record, err := service.Update(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), key, input)
		if err != nil {
			writeReportError(w, err)
			return
		}
		record.Capabilities.Edit = true
		writeReportJSON(w, http.StatusOK, record)
	})))

	mux.Handle("GET /api/v1/reports/{id}/revisions", protect("READ", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		limit, offset := 50, 0
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err == nil {
			if raw := r.URL.Query().Get("offset"); raw != "" {
				offset, err = strconv.Atoi(raw)
			}
		}
		if err != nil {
			writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
			return
		}
		items, total, err := service.ListRevisions(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), limit, offset)
		if err != nil {
			writeReportError(w, err)
			return
		}
		writeReportJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	return mux
}

func nextWithAccess(permissions *access.Service, action string, objectID func(*http.Request) string, next http.Handler) http.Handler {
	return access.Require(permissions, "REPORT", action, objectID, next)
}

func requireReportUUID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uuid.Validate(r.PathValue("id")) != nil {
			writeReportJSON(w, http.StatusNotFound, map[string]string{"code": "REPORT_NOT_FOUND", "message": "报告不存在"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(key) {
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_IDEMPOTENCY_KEY", "message": "Idempotency-Key 必须为 1 到 128 个非首尾空白字符"})
		return "", false
	}
	return key, true
}

// decodeReportRequest 同时限制完整定义和 Patch 的总传输体积、未知字段及尾随文档。
func decodeReportRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体不是有效的报告草稿 JSON"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func writeReportError(w http.ResponseWriter, err error) {
	var validation *reportjson.ValidationError
	var conflict *ConflictError
	var locked *LockedError
	var occupied *OccupiedError
	switch {
	case errors.As(err, &validation):
		writeReportJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "REPORT_JSON_VALIDATION_FAILED", "message": "报告 JSON 校验失败", "details": validation.Issues})
	case errors.As(err, &conflict):
		writeReportJSON(w, http.StatusConflict, map[string]any{"code": "REPORT_DRAFT_CONFLICT", "message": "报告草稿已被其他请求修改，请重新加载", "currentRevision": conflict.Revision, "currentHash": conflict.Hash})
	case errors.As(err, &locked):
		details := make([]map[string]string, 0, len(locked.Paths))
		for _, path := range locked.Paths {
			details = append(details, map[string]string{"path": path, "reason": "当前服务端草稿锁定了该内容"})
		}
		writeReportJSON(w, http.StatusConflict, map[string]any{"code": "REPORT_EDIT_LOCKED", "message": "报告锁定内容不能修改", "details": details})
	case errors.As(err, &occupied):
		writeReportJSON(w, http.StatusConflict, map[string]any{"code": "REPORT_RESOURCE_OCCUPIED", "message": "报告或分块正被其他任务占用", "reportOccupied": occupied.ReportOccupied, "blockIds": occupied.BlockIDs})
	case errors.Is(err, ErrNotFound):
		writeReportJSON(w, http.StatusNotFound, map[string]string{"code": "REPORT_NOT_FOUND", "message": "报告不存在"})
	case errors.Is(err, ErrForbidden):
		writeReportJSON(w, http.StatusForbidden, map[string]string{"code": "PERMISSION_DENIED", "message": "没有报告编辑权限"})
	case errors.Is(err, ErrAlreadyExists):
		writeReportJSON(w, http.StatusConflict, map[string]string{"code": "REPORT_CODE_CONFLICT", "message": "报告编码已存在"})
	case errors.Is(err, ErrIdempotencyConflict):
		writeReportJSON(w, http.StatusConflict, map[string]string{"code": "REPORT_IDEMPOTENCY_CONFLICT", "message": "Idempotency-Key 已绑定其他请求"})
	case errors.Is(err, ErrPatchMismatch):
		writeReportJSON(w, http.StatusConflict, map[string]string{"code": "REPORT_PATCH_MISMATCH", "message": "Patch 结果与完整报告定义不一致"})
	case errors.Is(err, ErrIdentityInvalid):
		writeReportJSON(w, http.StatusConflict, map[string]string{"code": "REPORT_IDENTITY_INVALID", "message": "报告身份、类型或草稿状态不能通过此接口修改"})
	case errors.Is(err, ErrInvalidPatch):
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "REPORT_PATCH_INVALID", "message": "报告 Patch 或语义操作无效"})
	case errors.Is(err, ErrInvalidRequest):
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "报告请求无效"})
	case errors.Is(err, reportjson.ErrInvalidDocument):
		writeReportJSON(w, http.StatusBadRequest, map[string]string{"code": "REPORT_JSON_INVALID", "message": "报告 JSON 无法解析或包含不支持的字段"})
	default:
		writeReportJSON(w, http.StatusInternalServerError, map[string]string{"code": "REPORT_PERSISTENCE_FAILED", "message": "报告服务暂时不可用"})
	}
}

func writeReportJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
