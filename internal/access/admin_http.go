package access

import (
	"encoding/json"
	"net/http"

	"intelligent-report-generation-system/internal/auth"
)

// NewAdminHandler 注册角色、用户角色与对象授权的管理接口。
func NewAdminHandler(authService *auth.Service, permissions *Service, store *AdminStore) http.Handler {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return auth.RequireTenantAccessToken(authService, next)
	}
	managed := func(next http.Handler) http.Handler {
		return auth.RequireTenantAccessToken(authService, Require(permissions, "USER", "MANAGE", nil, next))
	}
	mux.Handle("GET /api/v1/domains", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		domains, err := store.ListDomains(r.Context(), c.TenantID, c.Subject)
		if err != nil {
			writeError(w, 500, "DOMAIN_LIST_FAILED", "failed to list business domains")
			return
		}
		writeJSON(w, 200, map[string]any{"items": domains})
	})))
	mux.Handle("POST /api/v1/domains", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Code        string `json:"code"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		domain, err := store.CreateDomain(
			r.Context(), c.TenantID, c.Subject, in.Code, in.Name, in.Description,
		)
		if err != nil {
			writeError(w, 400, "DOMAIN_CREATE_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, domain)
	})))
	mux.Handle("PATCH /api/v1/domains/{id}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Status string `json:"status"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		domain, err := store.UpdateDomainStatus(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.Status,
		)
		if err != nil {
			writeError(w, 400, "DOMAIN_UPDATE_FAILED", err.Error())
			return
		}
		writeJSON(w, 200, domain)
	})))
	mux.Handle("GET /api/v1/roles", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		roles, err := store.ListRoles(r.Context(), c.TenantID)
		if err != nil {
			writeError(w, 500, "ROLE_LIST_FAILED", "failed to list roles")
			return
		}
		writeJSON(w, 200, map[string]any{"items": roles})
	})))
	mux.Handle("POST /api/v1/roles", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Code        string `json:"code"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		role, err := store.CreateRole(r.Context(), c.TenantID, c.Subject, in.Code, in.Name, in.Description)
		if err != nil {
			writeError(w, 400, "ROLE_CREATE_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, role)
	})))
	mux.Handle("GET /api/v1/users", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		users, err := store.ListUsers(r.Context(), c.TenantID)
		if err != nil {
			writeError(w, 500, "USER_LIST_FAILED", "failed to list users")
			return
		}
		writeJSON(w, 200, map[string]any{"items": users})
	})))
	mux.Handle("GET /api/v1/permissions", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := store.ListPermissions(r.Context(), c.TenantID)
		if err != nil {
			writeError(w, 500, "PERMISSION_LIST_FAILED", "failed to list permissions")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})))
	mux.Handle("PUT /api/v1/roles/{id}/permissions", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			PermissionCodes []string `json:"permissionCodes"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.ReplaceRolePermissions(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.PermissionCodes); err != nil {
			writeError(w, 400, "ROLE_PERMISSIONS_UPDATE_FAILED", err.Error())
			return
		}
		w.WriteHeader(204)
	})))
	mux.Handle("POST /api/v1/users/{id}/roles", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			RoleID string `json:"roleId"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.AssignUserRole(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.RoleID); err != nil {
			writeError(w, 400, "USER_ROLE_ASSIGN_FAILED", err.Error())
			return
		}
		w.WriteHeader(204)
	})))
	mux.Handle("DELETE /api/v1/users/{id}/roles/{roleId}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		if err := store.RevokeUserRole(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), r.PathValue("roleId")); err != nil {
			writeError(w, 404, "USER_ROLE_NOT_FOUND", "user role assignment not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /api/v1/users/{id}/domains", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			DomainID string `json:"domainId"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.AssignUserDomain(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.DomainID,
		); err != nil {
			writeError(w, 400, "USER_DOMAIN_ASSIGN_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("DELETE /api/v1/users/{id}/domains/{domainId}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		if err := store.RevokeUserDomain(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
			r.PathValue("domainId"),
		); err != nil {
			writeError(w, 400, "USER_DOMAIN_REVOKE_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /api/v1/object-permissions", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in ObjectGrant
		if !decodeAdmin(w, r, &in) {
			return
		}
		id, err := store.GrantObject(r.Context(), c.TenantID, c.Subject, in)
		if err != nil {
			writeError(w, 400, "OBJECT_PERMISSION_GRANT_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"id": id})
	})))
	mux.Handle("DELETE /api/v1/object-permissions/{id}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		if err := store.RevokeObject(r.Context(), c.TenantID, c.Subject, r.PathValue("id")); err != nil {
			writeError(w, 404, "OBJECT_PERMISSION_NOT_FOUND", "object permission not found")
			return
		}
		w.WriteHeader(204)
	})))
	return mux
}

// decodeAdmin 严格解析管理请求，避免静默接受未知字段。
func decodeAdmin(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid request body")
		return false
	}
	return true
}
