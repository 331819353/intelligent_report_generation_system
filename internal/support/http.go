package support

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

type Store interface {
	Create(context.Context, Identity, CreateInput) (Ticket, error)
	List(context.Context, Identity, bool, int) ([]Ticket, error)
	Transition(context.Context, Identity, string, TransitionInput) (Ticket, error)
}

type Handler struct{ store Store }

func NewHandler(authService *auth.Service, store Store) http.Handler {
	handler := &Handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/support-tickets", handler.create)
	mux.HandleFunc("GET /api/v1/support-tickets", handler.list)
	mux.HandleFunc("POST /api/v1/support-tickets/{id}/transition", handler.transition)
	return auth.RequireAccessToken(authService, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	}))
}

func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	identity, ok := requestIdentity(writer, request)
	if !ok {
		return
	}
	var input CreateInput
	if decodeJSON(writer, request, &input) != nil {
		writeError(writer, ErrInvalid)
		return
	}
	value, err := handler.store.Create(request.Context(), identity, input)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := requestIdentity(writer, request)
	if !ok {
		return
	}
	queue := request.URL.Query().Get("scope") == "queue"
	for key := range request.URL.Query() {
		if key != "scope" && key != "limit" {
			writeError(writer, ErrInvalid)
			return
		}
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(writer, ErrInvalid)
			return
		}
	}
	items, err := handler.store.List(request.Context(), identity, queue, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) transition(writer http.ResponseWriter, request *http.Request) {
	identity, ok := requestIdentity(writer, request)
	if !ok {
		return
	}
	var input TransitionInput
	if decodeJSON(writer, request, &input) != nil {
		writeError(writer, ErrInvalid)
		return
	}
	value, err := handler.store.Transition(request.Context(), identity, request.PathValue("id"), input)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func requestIdentity(writer http.ResponseWriter, request *http.Request) (Identity, bool) {
	claims, claimsOK := auth.ClaimsFromContext(request.Context())
	access, accessOK := database.AccessContextFromContext(request.Context())
	identity := Identity{TenantID: claims.TenantID, DomainID: access.DomainID, ActorID: claims.Subject}
	if !claimsOK || !accessOK || !identity.Valid() || access.UserID != claims.Subject {
		writeError(writer, ErrForbidden)
		return Identity{}, false
	}
	return identity, true
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, value any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func writeError(writer http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "SUPPORT_FAILED", "支持服务暂时不可用，请稍后重试"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code, message = http.StatusBadRequest, "SUPPORT_INVALID_REQUEST", "请检查工单内容后重试"
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "SUPPORT_TICKET_NOT_FOUND", "未找到该支持工单"
	case errors.Is(err, ErrConflict):
		status, code, message = http.StatusConflict, "SUPPORT_TICKET_CONFLICT", "工单状态已更新，请刷新后重试"
	case errors.Is(err, ErrForbidden):
		status, code, message = http.StatusForbidden, "SUPPORT_FORBIDDEN", "无权访问该支持工单"
	}
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
