package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

type Handler struct{ service *Service }

func NewHandler(authService *auth.Service, idempotency platformidempotency.Repository, service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/reports/{id}/schedules", h.list)
	mux.HandleFunc("POST /api/v1/reports/{id}/schedules", h.create)
	mux.HandleFunc("GET /api/v1/report-schedules/{id}", h.get)
	mux.HandleFunc("POST /api/v1/report-schedules/{id}/pause", h.pause)
	mux.HandleFunc("POST /api/v1/report-schedules/{id}/resume", h.resume)
	mux.HandleFunc("POST /api/v1/report-schedules/{id}/subscriptions", h.subscribe)
	mux.HandleFunc("DELETE /api/v1/report-schedules/{id}/subscriptions/{subscriptionId}", h.unsubscribe)
	mux.HandleFunc("POST /api/v1/report-schedules/{id}/backfill", h.backfill)
	mux.HandleFunc("GET /api/v1/report-deliveries", h.deliveries)
	mux.HandleFunc("POST /api/v1/report-deliveries/{id}/read", h.readDelivery)
	governed := platformidempotency.Middleware(platformidempotency.MiddlewareOptions{Repository: idempotency, ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
		i, e := scheduleIdentity(ctx)
		return platformidempotency.Identity{TenantID: string(i.TenantID), ActorID: string(i.ActorID)}, e
	}, Requires: platformidempotency.RequiresGovernedWrite, WriteError: writeError, MaxRequestBytes: 1 << 20}, mux)
	return auth.RequireAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		governed.ServeHTTP(w, r)
	}))
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.reportSubject(w, r)
	if !ok {
		return
	}
	var input CreateInput
	if decode(r, &input) != nil {
		writeError(w, 400, "REPORT_SCHEDULE_INVALID", "request body is invalid")
		return
	}
	v, e := h.service.Create(r.Context(), i, id, input)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.reportSubject(w, r)
	if !ok {
		return
	}
	limit := 50
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil {
			writeServiceError(w, ErrInvalid)
			return
		}
	}
	items, e := h.service.List(r.Context(), i, id, limit)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.scheduleSubject(w, r)
	if !ok {
		return
	}
	v, subscriptions, e := h.service.Get(r.Context(), i, id)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"schedule": v, "subscriptions": subscriptions})
}
func (h *Handler) pause(w http.ResponseWriter, r *http.Request)  { h.changeState(w, r, StatePaused) }
func (h *Handler) resume(w http.ResponseWriter, r *http.Request) { h.changeState(w, r, StateActive) }
func (h *Handler) changeState(w http.ResponseWriter, r *http.Request, state State) {
	i, id, ok := h.scheduleSubject(w, r)
	if !ok {
		return
	}
	var input VersionInput
	if decode(r, &input) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	v, e := h.service.SetState(r.Context(), i, id, input, state)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *Handler) subscribe(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.scheduleSubject(w, r)
	if !ok {
		return
	}
	var input SubscriptionInput
	if decode(r, &input) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	v, e := h.service.Subscribe(r.Context(), i, id, input)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.scheduleSubject(w, r)
	if !ok {
		return
	}
	subscription := askdata.ID(r.PathValue("subscriptionId"))
	if subscription.Validate() != nil || decodeEmpty(r) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	if e := h.service.Unsubscribe(r.Context(), i, id, subscription); e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}
func (h *Handler) backfill(w http.ResponseWriter, r *http.Request) {
	i, id, ok := h.scheduleSubject(w, r)
	if !ok {
		return
	}
	var body struct {
		ScheduledFor time.Time `json:"scheduledFor"`
	}
	if decode(r, &body) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	count, e := h.service.Backfill(r.Context(), i, id, body.ScheduledFor)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 202, map[string]int{"deliveryCount": count})
}
func (h *Handler) deliveries(w http.ResponseWriter, r *http.Request) {
	i, e := scheduleIdentity(r.Context())
	if e != nil {
		writeServiceError(w, ErrForbidden)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil {
			writeServiceError(w, ErrInvalid)
			return
		}
	}
	items, e := h.service.Deliveries(r.Context(), i, limit)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (h *Handler) readDelivery(w http.ResponseWriter, r *http.Request) {
	i, e := scheduleIdentity(r.Context())
	id := askdata.ID(r.PathValue("id"))
	if e != nil {
		writeServiceError(w, ErrForbidden)
		return
	}
	if id.Validate() != nil || decodeEmpty(r) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	value, e := h.service.MarkDeliveryRead(r.Context(), i, id)
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (h *Handler) reportSubject(w http.ResponseWriter, r *http.Request) (Identity, askdata.ID, bool) {
	i, e := scheduleIdentity(r.Context())
	id := askdata.ID(r.PathValue("id"))
	if e != nil {
		writeServiceError(w, ErrForbidden)
		return Identity{}, "", false
	}
	if id.Validate() != nil {
		writeServiceError(w, ErrInvalid)
		return Identity{}, "", false
	}
	return i, id, true
}
func (h *Handler) scheduleSubject(w http.ResponseWriter, r *http.Request) (Identity, askdata.ID, bool) {
	return h.reportSubject(w, r)
}
func scheduleIdentity(ctx context.Context) (Identity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	access, aok := database.AccessContextFromContext(ctx)
	if !ok || !aok || claims.Subject != access.UserID || access.DomainID == "" {
		return Identity{}, ErrForbidden
	}
	i := Identity{askdata.ID(claims.TenantID), askdata.ID(access.DomainID), askdata.ID(claims.Subject)}
	return i, i.Validate()
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
func decodeEmpty(r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	if e := decode(r, &body); e != nil || len(body) != 0 {
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
	status, code, message := 500, "REPORT_SCHEDULE_INTERNAL", "report schedule operation failed"
	switch {
	case errors.Is(e, ErrInvalid):
		status, code, message = 400, "REPORT_SCHEDULE_INVALID", e.Error()
	case errors.Is(e, ErrNotFound):
		status, code, message = 404, "REPORT_SCHEDULE_NOT_FOUND", e.Error()
	case errors.Is(e, ErrForbidden):
		status, code, message = 403, "REPORT_SCHEDULE_FORBIDDEN", e.Error()
	case errors.Is(e, ErrConflict):
		status, code, message = 409, "REPORT_SCHEDULE_CONFLICT", e.Error()
	case errors.Is(e, ErrUnavailable):
		status, code, message = 409, "REPORT_SCHEDULE_UNAVAILABLE", e.Error()
	}
	writeError(w, status, code, message)
}
