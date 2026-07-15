package access

import (
	"encoding/json"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/auth"
)

// Require 构造对象权限中间件，拒绝未认证或无权访问的请求。
func Require(service *Service, resourceType, action string, objectID func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
			return
		}
		id := ""
		if objectID != nil {
			id = objectID(r)
		}
		allowed, err := service.Allowed(r.Context(), Check{TenantID: claims.TenantID, UserID: claims.Subject, ResourceType: resourceType, Action: action, ObjectID: id})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PERMISSION_EVALUATION_FAILED", "permission evaluation failed")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "permission denied")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// EvaluateHandler 提供前端按需检查资源权限的接口。
func EvaluateHandler(service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "ACCESS_TOKEN_REQUIRED", "valid bearer token is required")
			return
		}
		var input struct {
			ResourceType string `json:"resourceType"`
			Action       string `json:"action"`
			ObjectID     string `json:"objectId"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || strings.TrimSpace(input.ResourceType) == "" || strings.TrimSpace(input.Action) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "resourceType and action are required")
			return
		}
		allowed, err := service.Allowed(r.Context(), Check{TenantID: claims.TenantID, UserID: claims.Subject, ResourceType: input.ResourceType, Action: input.Action, ObjectID: input.ObjectID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PERMISSION_EVALUATION_FAILED", "permission evaluation failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"allowed": allowed})
	}
}

// writeError 输出权限模块的标准错误结构。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// writeJSON 输出权限模块的 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
