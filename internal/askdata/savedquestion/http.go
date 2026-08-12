package savedquestion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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

type AtomicCreator interface {
	CreateWithShare(context.Context, Identity, CreateInput, ShareInput) (SavedQuestion, error)
}

type Page struct {
	Items      []SavedQuestion `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}
type PagedRepository interface {
	ListPage(context.Context, Identity, int, string, string) (Page, error)
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

// RunResolver resolves the governed Semantic IR on the server. Browser clients
// only submit the source run and display metadata, so they cannot forge a saved
// question by supplying a different IR.
type RunResolver interface {
	ResolveSavedQuestionIR(context.Context, Identity, askdata.ID) (ircontract.SemanticIR, error)
}

type HTTPHandler struct {
	repository Repository
	launcher   Launcher
	runs       RunResolver
}

func NewHandler(authService *auth.Service, repository Repository, launcher Launcher, runResolver ...RunResolver) http.Handler {
	handler := &HTTPHandler{repository: repository, launcher: launcher}
	if len(runResolver) > 0 {
		handler.runs = runResolver[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/askdata/saved-questions", handler.list)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions", handler.create)
	mux.HandleFunc("POST /api/v1/askdata/saved-questions/from-run", handler.createFromRun)
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

func (handler *HTTPHandler) createFromRun(writer http.ResponseWriter, request *http.Request) {
	identity, ok := httpIdentity(writer, request)
	if !ok {
		return
	}
	if handler.runs == nil {
		writeSavedError(writer, errors.New("saved question run resolver unavailable"))
		return
	}
	var body struct {
		Name                string        `json:"name"`
		QuestionText        string        `json:"questionText"`
		Visibility          Visibility    `json:"visibility"`
		SourceQuestionRunID askdata.ID    `json:"sourceQuestionRunId"`
		PrincipalType       PrincipalType `json:"principalType,omitempty"`
		PrincipalID         askdata.ID    `json:"principalId,omitempty"`
	}
	if decodeSavedJSON(request, &body) != nil || body.SourceQuestionRunID.Validate() != nil ||
		(body.PrincipalID == "") != (body.PrincipalType == "") {
		writeSavedError(writer, ErrInvalid)
		return
	}
	semanticIR, err := handler.runs.ResolveSavedQuestionIR(request.Context(), identity, body.SourceQuestionRunID)
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	createInput := CreateInput{
		Name: body.Name, QuestionText: body.QuestionText, Visibility: body.Visibility,
		SemanticIR: semanticIR, SourceQuestionRunID: body.SourceQuestionRunID,
	}
	var item SavedQuestion
	if body.PrincipalID != "" {
		creator, supported := handler.repository.(AtomicCreator)
		share := ShareInput{PrincipalType: body.PrincipalType, PrincipalID: body.PrincipalID}
		if !supported || share.Validate() != nil || body.Visibility != Team {
			writeSavedError(writer, ErrInvalid)
			return
		}
		item, err = creator.CreateWithShare(request.Context(), identity, createInput, share)
	} else {
		item, err = handler.repository.Create(request.Context(), identity, createInput)
	}
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	writeSavedJSON(writer, http.StatusCreated, item)
}

func (handler *HTTPHandler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := httpIdentity(writer, request)
	if !ok {
		return
	}
	limit := 50
	var err error
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeSavedError(writer, ErrInvalid)
			return
		}
	}
	if repository, ok := handler.repository.(PagedRepository); ok {
		page, pageErr := repository.ListPage(request.Context(), identity, limit, request.URL.Query().Get("cursor"), request.URL.Query().Get("order"))
		if pageErr != nil {
			writeSavedError(writer, pageErr)
			return
		}
		writeSavedJSON(writer, http.StatusOK, page)
		return
	}
	items, err := handler.repository.List(request.Context(), identity)
	if err != nil {
		writeSavedError(writer, err)
		return
	}
	if limit < 1 || limit > 200 {
		writeSavedError(writer, ErrInvalid)
		return
	}
	if len(items) > limit {
		items = items[:limit]
	}
	writeSavedJSON(writer, http.StatusOK, Page{Items: items})
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
		slog.ErrorContext(request.Context(), "launch saved question", "saved_question_id", id, "error", err)
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
	status, code, message := http.StatusInternalServerError, "SAVED_QUESTION_FAILED", "收藏服务暂时不可用，请稍后重试。"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code, message = http.StatusBadRequest, "SAVED_QUESTION_INVALID", "收藏内容或来源运行无效，请刷新结果后重试。"
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "SAVED_QUESTION_NOT_FOUND", "该收藏不存在或已归档。"
	case errors.Is(err, ErrPermissionDenied):
		status, code, message = http.StatusForbidden, "SAVED_QUESTION_FORBIDDEN", "当前账号无权访问或共享该收藏。"
	}
	writeSavedJSON(writer, status, map[string]string{"code": code, "message": message})
}

func writeSavedJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
