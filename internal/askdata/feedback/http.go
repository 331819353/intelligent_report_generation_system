package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

type HTTPRepository interface {
	List(context.Context, Identity) ([]Ticket, error)
	Get(context.Context, Identity, askdata.ID) (Ticket, []Event, error)
	Transition(context.Context, Identity, askdata.ID, TransitionInput) (Ticket, error)
	Metrics(context.Context, Identity, time.Time) (Metrics, error)
	ListCandidates(context.Context, Identity) ([]Candidate, error)
	ReviewCandidate(context.Context, Identity, askdata.ID, string, time.Time) (Candidate, error)
}

type HTTPHandler struct{ repository HTTPRepository }

func NewHandler(authService *auth.Service, repository HTTPRepository) http.Handler {
	handler := &HTTPHandler{repository: repository}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/askdata/feedback-tickets", handler.list)
	mux.HandleFunc("GET /api/v1/askdata/feedback-tickets/metrics", handler.metrics)
	mux.HandleFunc("GET /api/v1/askdata/feedback-tickets/{id}", handler.get)
	mux.HandleFunc("POST /api/v1/askdata/feedback-tickets/{id}/transition", handler.transition)
	mux.HandleFunc("GET /api/v1/askdata/active-learning-candidates", handler.listCandidates)
	mux.HandleFunc("POST /api/v1/askdata/active-learning-candidates/{id}/review", handler.reviewCandidate)
	return auth.RequireAccessToken(authService, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	}))
}

func (handler *HTTPHandler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := feedbackIdentity(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.List(request.Context(), identity)
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, map[string]any{"items": items})
}
func (handler *HTTPHandler) metrics(writer http.ResponseWriter, request *http.Request) {
	identity, ok := feedbackIdentity(writer, request)
	if !ok {
		return
	}
	result, err := handler.repository.Metrics(request.Context(), identity, time.Now().UTC())
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, result)
}
func (handler *HTTPHandler) get(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := feedbackSubject(writer, request)
	if !ok {
		return
	}
	ticket, events, err := handler.repository.Get(request.Context(), identity, id)
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, map[string]any{"ticket": ticket, "events": events})
}
func (handler *HTTPHandler) transition(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := feedbackSubject(writer, request)
	if !ok {
		return
	}
	var body TransitionInput
	if decodeFeedbackJSON(request, &body) != nil {
		writeFeedbackError(writer, ErrInvalid)
		return
	}
	result, err := handler.repository.Transition(request.Context(), identity, id, body)
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, result)
}
func (handler *HTTPHandler) listCandidates(writer http.ResponseWriter, request *http.Request) {
	identity, ok := feedbackIdentity(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.ListCandidates(request.Context(), identity)
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, map[string]any{"items": items})
}
func (handler *HTTPHandler) reviewCandidate(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := feedbackSubject(writer, request)
	if !ok {
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if decodeFeedbackJSON(request, &body) != nil {
		writeFeedbackError(writer, ErrInvalid)
		return
	}
	result, err := handler.repository.ReviewCandidate(request.Context(), identity, id, strings.ToUpper(strings.TrimSpace(body.Decision)), time.Now().UTC())
	if err != nil {
		writeFeedbackError(writer, err)
		return
	}
	writeFeedbackJSON(writer, http.StatusOK, result)
}

func feedbackIdentity(writer http.ResponseWriter, request *http.Request) (Identity, bool) {
	claims, ok := auth.ClaimsFromContext(request.Context())
	access, accessOK := database.AccessContextFromContext(request.Context())
	if !ok || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		writeFeedbackError(writer, ErrInvalid)
		return Identity{}, false
	}
	identity := Identity{TenantID: askdata.ID(claims.TenantID), DomainID: askdata.ID(access.DomainID), ActorID: askdata.ID(claims.Subject)}
	if identity.Validate() != nil {
		writeFeedbackError(writer, ErrInvalid)
		return Identity{}, false
	}
	return identity, true
}
func feedbackSubject(writer http.ResponseWriter, request *http.Request) (Identity, askdata.ID, bool) {
	identity, ok := feedbackIdentity(writer, request)
	if !ok {
		return Identity{}, "", false
	}
	id := askdata.ID(request.PathValue("id"))
	if id.Validate() != nil {
		writeFeedbackError(writer, ErrInvalid)
		return Identity{}, "", false
	}
	return identity, id, true
}
func decodeFeedbackJSON(request *http.Request, destination any) error {
	if request.Body == nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return ErrInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}
func writeFeedbackJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
func writeFeedbackError(writer http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "FEEDBACK_INTERNAL", "反馈治理服务暂时不可用，请稍后重试"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code, message = http.StatusBadRequest, "FEEDBACK_INVALID", "请检查反馈治理内容后重试"
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "FEEDBACK_NOT_FOUND", "未找到该反馈工单或改进候选"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIllegalTransition):
		status, code, message = http.StatusConflict, "FEEDBACK_CONFLICT", "状态已被更新，请刷新后重试"
	}
	if status >= http.StatusInternalServerError {
		slog.Error("feedback governance request failed", "error", err)
	}
	writeFeedbackJSON(writer, status, map[string]string{"code": code, "message": message})
}
