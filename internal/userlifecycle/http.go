package userlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"intelligent-report-generation-system/internal/auth"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

type Handler struct{ service *Service }

func NewHandler(authService *auth.Service, idempotency platformidempotency.Repository, service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/users/{id}/deactivation-preview", h.preview)
	mux.HandleFunc("POST /api/v1/users/{id}/deactivation-batches", h.execute)
	mux.HandleFunc("GET /api/v1/user-lifecycle-batches/{id}", h.get)
	mux.HandleFunc("POST /api/v1/user-lifecycle-batches/{id}/retry", h.retry)
	governed := platformidempotency.Middleware(platformidempotency.MiddlewareOptions{Repository: idempotency, ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
		claims, ok := auth.ClaimsFromContext(ctx)
		if !ok {
			return platformidempotency.Identity{}, ErrForbidden
		}
		return platformidempotency.Identity{TenantID: claims.TenantID, ActorID: claims.Subject}, nil
	}, Requires: platformidempotency.RequiresGovernedWrite, WriteError: writeError, MaxRequestBytes: 1 << 20}, mux)
	return auth.RequireTenantAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		governed.ServeHTTP(w, r)
	}))
}
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeServiceError(w, ErrForbidden)
		return
	}
	value, e := h.service.Preview(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"))
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, value)
}
func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeServiceError(w, ErrForbidden)
		return
	}
	var body struct {
		Mappings []Mapping `json:"mappings"`
	}
	if decode(r, &body) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	value, e := h.service.PlanAndExecute(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), body.Mappings)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	status := 201
	if value.Status == "TRANSFER_FAILED" {
		status = 409
	}
	writeJSON(w, status, value)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeServiceError(w, ErrForbidden)
		return
	}
	value, e := h.service.Get(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"))
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, value)
}
func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeServiceError(w, ErrForbidden)
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expectedVersion"`
	}
	if decode(r, &body) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	value, e := h.service.Retry(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), body.ExpectedVersion)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	status := http.StatusOK
	if value.Status == "TRANSFER_FAILED" {
		status = http.StatusConflict
	}
	writeJSON(w, status, value)
}
func decode(r *http.Request, value any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	d.DisallowUnknownFields()
	if e := d.Decode(value); e != nil {
		return e
	}
	var trailing any
	if e := d.Decode(&trailing); !errors.Is(e, io.EOF) {
		return ErrInvalid
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
func writeServiceError(w http.ResponseWriter, e error) {
	status, code, message := 500, "USER_LIFECYCLE_INTERNAL", "user lifecycle operation failed"
	switch {
	case errors.Is(e, ErrInvalid):
		status, code, message = 400, "USER_LIFECYCLE_INVALID", e.Error()
	case errors.Is(e, ErrForbidden):
		status, code, message = 403, "USER_LIFECYCLE_FORBIDDEN", e.Error()
	case errors.Is(e, ErrBlocked):
		status, code, message = 409, "USER_LIFECYCLE_BLOCKED", e.Error()
	case errors.Is(e, ErrConflict):
		status, code, message = 409, "USER_LIFECYCLE_CONFLICT", e.Error()
	case errors.Is(e, ErrNotFound):
		status, code, message = 404, "USER_LIFECYCLE_NOT_FOUND", e.Error()
	}
	writeError(w, status, code, message)
}
