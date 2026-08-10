package savedquestion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

type Repository interface {
	Create(context.Context, Identity, CreateInput) (SavedQuestion, error)
	List(context.Context, Identity) ([]SavedQuestion, error)
	Get(context.Context, Identity, askdata.ID) (SavedQuestion, error)
	Share(context.Context, Identity, askdata.ID, ShareInput) error
	Promote(context.Context, Identity, askdata.ID) error
	Archive(context.Context, Identity, askdata.ID) error
}

type LaunchInput struct {
	Identity       Identity
	Question       SavedQuestion
	IdempotencyKey string
}

type LaunchResult struct {
	RunID          askdata.ID `json:"runId"`
	ConversationID askdata.ID `json:"conversationId"`
	Replayed       bool       `json:"replayed"`
}

type Launcher interface {
	LaunchSavedQuestion(context.Context, LaunchInput) (LaunchResult, error)
}

type HTTPHandler struct {
	repository Repository
	launcher   Launcher
}

func NewHandler(authService *auth.Service, repository Repository, launcher Launcher) http.Handler {
	handler := &HTTPHandler{repository: repository, launcher: launcher}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/askdata/saved-questions", handler.list)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions", handler.create)
	mux.HandleFunc("GET /api/v1/askdata/saved-questions/{id}", handler.get)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions/{id}/open", handler.open)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions/{id}/share", handler.share)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions/{id}/promote", handler.promote)
	mux.HandleFunc("DELETE /api/v1/askdata/saved-questions/{id}", handler.archive)
	protected := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	})
	return auth.RequireAccessToken(authService, protected)
}

func (handler *HTTPHandler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := httpIdentity(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.List(request.Context(), identity)
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	writeSavedJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *HTTPHandler) create(writer http.ResponseWriter, request *http.Request) {
	identity, ok := httpIdentity(writer, request)
	if !ok {
		return
	}
	var body struct {
		Name                string                `json:"name"`
		QuestionText        string                `json:"questionText"`
		Visibility          Visibility            `json:"visibility"`
		SemanticIR          ircontract.SemanticIR `json:"semanticIr"`
		SourceQuestionRunID askdata.ID            `json:"sourceQuestionRunId,omitempty"`
	}
	if err := decodeSavedJSON(request, &body); err != nil {
		writeSavedError(writer, err)
		return
	}
	item, err := handler.repository.Create(request.Context(), identity, CreateInput{
		Name: body.Name, QuestionText: body.QuestionText, Visibility: body.Visibility,
		SemanticIR: body.SemanticIR, SourceQuestionRunID: body.SourceQuestionRunID,
	})
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	writeSavedJSON(writer, http.StatusCreated, item)
}

func (handler *HTTPHandler) get(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := savedSubject(writer, request)
	if !ok {
		return
	}
	item, err := handler.repository.Get(request.Context(), identity, id)
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	writeSavedJSON(writer, http.StatusOK, item)
}

func (handler *HTTPHandler) open(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := savedSubject(writer, request)
	if !ok {
		return
	}
	if handler.launcher == nil {
		writeSavedError(writer, errors.New("saved question launcher unavailable"))
		return
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 256 {
		writeSavedError(writer, ErrInvalid)
		return
	}
	item, err := handler.repository.Get(request.Context(), identity, id)
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	if item.Status != Active {
		writeSavedError(writer, ErrInvalid)
		return
	}
	result, err := handler.launcher.LaunchSavedQuestion(request.Context(), LaunchInput{
		Identity: identity, Question: item, IdempotencyKey: key,
	})
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeSavedJSON(writer, status, result)
}

func (handler *HTTPHandler) share(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := savedSubject(writer, request)
	if !ok {
		return
	}
	var body struct {
		PrincipalType PrincipalType `json:"principalType"`
		PrincipalID   askdata.ID    `json:"principalId"`
	}
	if err := decodeSavedJSON(request, &body); err != nil {
		writeSavedError(writer, err)
		return
	}
	if err := handler.repository.Share(request.Context(), identity, id, ShareInput(body)); err != nil {
		writeSavedError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) promote(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := savedSubject(writer, request)
	if !ok {
		return
	}
	if err := requireEmptyBody(request); err != nil {
		writeSavedError(writer, err)
		return
	}
	if err := handler.repository.Promote(request.Context(), identity, id); err != nil {
		writeSavedError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) archive(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := savedSubject(writer, request)
	if !ok {
		return
	}
	if err := requireEmptyBody(request); err != nil {
		writeSavedError(writer, err)
		return
	}
	if err := handler.repository.Archive(request.Context(), identity, id); err != nil {
		writeSavedError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func savedSubject(writer http.ResponseWriter, request *http.Request) (Identity, askdata.ID, bool) {
	identity, ok := httpIdentity(writer, request)
	if !ok {
		return Identity{}, "", false
	}
	id := askdata.ID(request.PathValue("id"))
	if id.Validate() != nil {
		writeSavedError(writer, ErrInvalid)
		return Identity{}, "", false
	}
	return identity, id, true
}

func httpIdentity(writer http.ResponseWriter, request *http.Request) (Identity, bool) {
	claims, ok := auth.ClaimsFromContext(request.Context())
	access, accessOK := database.AccessContextFromContext(request.Context())
	if !ok || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		writeSavedError(writer, ErrPermissionDenied)
		return Identity{}, false
	}
	identity := Identity{TenantID: askdata.ID(claims.TenantID), DomainID: askdata.ID(access.DomainID), ActorID: askdata.ID(claims.Subject)}
	if identity.Validate() != nil {
		writeSavedError(writer, ErrPermissionDenied)
		return Identity{}, false
	}
	return identity, true
}

func decodeSavedJSON(request *http.Request, destination any) error {
	if request.Body == nil {
		return ErrInvalid
	}
	limited := io.LimitReader(request.Body, 300<<10)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func requireEmptyBody(request *http.Request) error {
	if request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 2))
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		return ErrInvalid
	}
	return nil
}

func writeSavedError(writer http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "SAVED_QUESTION_FAILED"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code = http.StatusBadRequest, "SAVED_QUESTION_INVALID"
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "SAVED_QUESTION_NOT_FOUND"
	case errors.Is(err, ErrPermissionDenied):
		status, code = http.StatusForbidden, "SAVED_QUESTION_FORBIDDEN"
	}
	writeSavedJSON(writer, status, map[string]string{"code": code})
}

func writeSavedJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
