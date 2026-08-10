package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/auth"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

const maxRuntimeConfigRequestBytes = 1 << 20

type Handler struct{ service *Service }

func NewHandler(authService *auth.Service, idempotency platformidempotency.Repository, service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runtime-config/definitions", h.definitions)
	mux.HandleFunc("GET /api/v1/runtime-config/deployment-parameters", h.deploymentParameters)
	mux.HandleFunc("GET /api/v1/runtime-config/versions", h.list)
	mux.HandleFunc("POST /api/v1/runtime-config/versions", h.create)
	mux.HandleFunc("GET /api/v1/runtime-config/versions/{id}", h.get)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/submit", h.submit)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/approve", h.approve)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/reject", h.reject)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/apply", h.apply)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/rollback", h.rollback)
	mux.HandleFunc("POST /api/v1/runtime-config/versions/{id}/rollout-nodes/{nodeId}/restart-ack", h.restartAck)
	governed := platformidempotency.Middleware(platformidempotency.MiddlewareOptions{
		Repository: idempotency,
		ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
			claims, ok := auth.ClaimsFromContext(ctx)
			if !ok {
				return platformidempotency.Identity{}, ErrForbidden
			}
			return platformidempotency.Identity{TenantID: claims.TenantID, ActorID: claims.Subject}, nil
		},
		Requires: platformidempotency.RequiresGovernedWrite, WriteError: writeRuntimeConfigError,
		MaxRequestBytes: maxRuntimeConfigRequestBytes,
	}, mux)
	return auth.RequireTenantAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		governed.ServeHTTP(w, r)
	}))
}

func (h *Handler) identity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeRuntimeConfigServiceError(w, ErrForbidden)
		return "", "", false
	}
	return claims.TenantID, claims.Subject, true
}

func (h *Handler) definitions(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	if _, err := h.service.List(r.Context(), tenant, actor, 1); err != nil {
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, map[string]any{"items": Definitions()})
}

func (h *Handler) deploymentParameters(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	if _, err := h.service.List(r.Context(), tenant, actor, 1); err != nil {
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, map[string]any{"items": DeploymentParameters()})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeRuntimeConfigServiceError(w, ErrInvalid)
			return
		}
		limit = parsed
	}
	items, err := h.service.List(r.Context(), tenant, actor, limit)
	if err != nil {
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	var input CreateInput
	if decodeRuntimeConfigJSON(r, &input) != nil {
		writeRuntimeConfigServiceError(w, ErrInvalid)
		return
	}
	value, err := h.service.Create(r.Context(), tenant, actor, input)
	if err != nil {
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusCreated, value)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	value, err := h.service.Get(r.Context(), tenant, actor, r.PathValue("id"))
	if err != nil {
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, value)
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, operation func(context.Context, string, string, string, VersionInput) (Version, error)) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	var input VersionInput
	if decodeRuntimeConfigJSON(r, &input) != nil {
		writeRuntimeConfigServiceError(w, ErrInvalid)
		return
	}
	value, err := operation(r.Context(), tenant, actor, r.PathValue("id"), input)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			h.writeConflict(w, r, tenant, actor, err)
			return
		}
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, value)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.service.Submit)
}
func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.service.Approve)
}
func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	var input RejectInput
	if decodeRuntimeConfigJSON(r, &input) != nil {
		writeRuntimeConfigServiceError(w, ErrInvalid)
		return
	}
	value, err := h.service.Reject(r.Context(), tenant, actor, r.PathValue("id"), input)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			h.writeConflict(w, r, tenant, actor, err)
			return
		}
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, value)
}
func (h *Handler) apply(w http.ResponseWriter, r *http.Request) { h.transition(w, r, h.service.Apply) }
func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.service.Rollback)
}

func (h *Handler) restartAck(w http.ResponseWriter, r *http.Request) {
	tenant, actor, ok := h.identity(w, r)
	if !ok {
		return
	}
	if decodeEmptyRuntimeConfigJSON(r) != nil {
		writeRuntimeConfigServiceError(w, ErrInvalid)
		return
	}
	value, err := h.service.AcknowledgeRestart(r.Context(), tenant, actor, r.PathValue("id"), r.PathValue("nodeId"))
	if err != nil {
		if errors.Is(err, ErrConflict) {
			h.writeConflict(w, r, tenant, actor, err)
			return
		}
		writeRuntimeConfigServiceError(w, err)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusOK, value)
}

func (h *Handler) writeConflict(w http.ResponseWriter, r *http.Request, tenant, actor string, conflict error) {
	current, err := h.service.Get(r.Context(), tenant, actor, r.PathValue("id"))
	if err != nil {
		writeRuntimeConfigServiceError(w, conflict)
		return
	}
	writeRuntimeConfigJSON(w, http.StatusConflict, map[string]any{
		"code": "RUNTIME_CONFIG_CONFLICT", "message": conflict.Error(), "current": current,
	})
}

func decodeRuntimeConfigJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRuntimeConfigRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func decodeEmptyRuntimeConfigJSON(r *http.Request) error {
	var value map[string]any
	if err := decodeRuntimeConfigJSON(r, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return ErrInvalid
	}
	return nil
}

func writeRuntimeConfigJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRuntimeConfigError(w http.ResponseWriter, status int, code, message string) {
	writeRuntimeConfigJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeRuntimeConfigServiceError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "RUNTIME_CONFIG_INTERNAL", "runtime configuration operation failed"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code, message = 400, "RUNTIME_CONFIG_INVALID", err.Error()
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "RUNTIME_CONFIG_FORBIDDEN", err.Error()
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "RUNTIME_CONFIG_NOT_FOUND", err.Error()
	case errors.Is(err, ErrConflict):
		status, code, message = 409, "RUNTIME_CONFIG_CONFLICT", err.Error()
	}
	writeRuntimeConfigError(w, status, code, message)
}
