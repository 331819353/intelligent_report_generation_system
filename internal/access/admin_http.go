package access

import (
	"encoding/json"
	"net/http"

	"intelligent-report-generation-system/internal/auth"
)

// NewAdminHandler 注册平台、领域与用户归属的固定层级管理接口。
func NewAdminHandler(authService *auth.Service, store *AdminStore) http.Handler {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return auth.RequireTenantAccessToken(authService, next)
	}
	platformManaged := func(next http.Handler) http.Handler {
		return authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			allowed, err := store.IsPlatformAdministrator(
				r.Context(), claims.TenantID, claims.Subject,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "PLATFORM_ADMIN_CHECK_FAILED", "failed to verify platform administrator")
				return
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "PLATFORM_ADMIN_REQUIRED", "platform administrator permission is required")
				return
			}
			next.ServeHTTP(w, r)
		}))
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
	mux.Handle("GET /api/v1/domain-catalog", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := store.ListDomainCatalog(r.Context(), c.TenantID, c.Subject)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DOMAIN_CATALOG_FAILED", "failed to list available business domains")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})))
	mux.Handle("GET /api/v1/managed-domains", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := store.ListManagedDomains(r.Context(), c.TenantID, c.Subject)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "MANAGED_DOMAIN_LIST_FAILED", "failed to list managed business domains")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})))
	mux.Handle("POST /api/v1/domains", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Code                 string   `json:"code"`
			Name                 string   `json:"name"`
			Description          string   `json:"description"`
			AdministratorUserIDs []string `json:"administratorUserIds"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		domain, err := store.CreateDomain(
			r.Context(), c.TenantID, c.Subject, in.Code, in.Name, in.Description,
			in.AdministratorUserIDs,
		)
		if err != nil {
			writeError(w, 400, "DOMAIN_CREATE_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, domain)
	})))
	mux.Handle("PATCH /api/v1/domains/{id}", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("PUT /api/v1/domains/{id}/administrators", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			UserIDs []string `json:"userIds"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.ReplaceDomainAdministrators(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.UserIDs,
		); err != nil {
			writeError(w, http.StatusBadRequest, "DOMAIN_ADMINISTRATORS_UPDATE_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /api/v1/domains/{id}/applications", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Reason string `json:"reason"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		application, err := store.ApplyDomainAccess(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.Reason,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, "DOMAIN_APPLICATION_CREATE_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, application)
	})))
	mux.Handle("GET /api/v1/domain-applications", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := store.ListMyDomainApplications(r.Context(), c.TenantID, c.Subject)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DOMAIN_APPLICATION_LIST_FAILED", "failed to list domain applications")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})))
	mux.Handle("GET /api/v1/domains/{id}/applications", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := store.ListPendingDomainApplications(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
		)
		if err != nil {
			writeError(w, http.StatusForbidden, "DOMAIN_ADMIN_REQUIRED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})))
	mux.Handle("POST /api/v1/domain-applications/{id}/decision", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Decision string `json:"decision"`
			Comment  string `json:"comment"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.ReviewDomainApplication(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
			in.Decision, in.Comment,
		); err != nil {
			writeError(w, http.StatusBadRequest, "DOMAIN_APPLICATION_REVIEW_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /api/v1/users", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		users, err := store.ListUsers(r.Context(), c.TenantID)
		if err != nil {
			writeError(w, 500, "USER_LIST_FAILED", "failed to list users")
			return
		}
		writeJSON(w, 200, map[string]any{"items": users})
	})))
	mux.Handle("PUT /api/v1/users/{id}/platform-administrator", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if !decodeAdmin(w, r, &in) {
			return
		}
		if err := store.SetPlatformAdministrator(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"), in.Enabled,
		); err != nil {
			writeError(w, http.StatusBadRequest, "PLATFORM_ADMINISTRATOR_UPDATE_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /api/v1/users/{id}/domains", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("DELETE /api/v1/users/{id}/domains/{domainId}", platformManaged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
