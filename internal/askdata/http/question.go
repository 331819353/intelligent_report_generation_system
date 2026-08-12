// Package askdatahttp exposes the authenticated Question API without
// publishing raw questions or internal replay payloads.
package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	askdataobservability "intelligent-report-generation-system/internal/askdata/observability"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/askdata/validator"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	maxQuestionBodyBytes       = 16 << 10
	maxQuestionRunes           = 4096
	maxIdempotencyKeyBytes     = 256
	questionHashDomain         = "askdata-question-v1\x00"
	questionIdempotencyDomain  = "askdata-question-create-v1\x00"
	clarificationHashDomain    = "askdata-clarification-v1\x00"
	clarificationConsumeDomain = "askdata-clarification-consume-v1\x00"
	maxPublicResultDatasets    = 4
	maxPublicResultColumns     = 16
	maxPublicResultRows        = 100
	maxPublicResultCells       = 800
	maxPublicResultViews       = 8
)

var (
	ErrInvalidRequest         = errors.New("invalid question request")
	ErrUnauthenticated        = errors.New("question request is unauthenticated")
	ErrNoActiveRelease        = errors.New("question domain has no active semantic release")
	ErrClarificationRequired  = errors.New("question run does not accept a clarification")
	ErrClarificationOption    = errors.New("clarification option is not available")
	ErrClarificationAnswered  = errors.New("clarification was already answered")
	ErrFeedbackNotAccepted    = errors.New("question run does not accept feedback")
	ErrFeedbackConflict       = errors.New("question feedback changed concurrently")
	ErrQuestionServiceFailure = errors.New("question service failed")
	ErrQuestionQuotaExceeded  = errors.New("question quota exceeded")
	publicCodePattern         = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	publicNumberPattern       = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	publicIntegerPattern      = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
)

// RequestIdentity is derived only from the verified access token and the
// business-domain access context installed by auth.RequireAccessToken.
type RequestIdentity struct {
	TenantID askdata.ID
	ActorID  askdata.ID
	DomainID askdata.ID
}

func (identity RequestIdentity) validate() error {
	for _, value := range []askdata.ID{identity.TenantID, identity.ActorID, identity.DomainID} {
		parsed, err := uuid.Parse(string(value))
		if err != nil || parsed.String() != string(value) {
			return ErrUnauthenticated
		}
	}
	return nil
}

type CreateQuestionInput struct {
	Question           string
	QuestionHash       askdata.ContentHash
	IdempotencyKeyHash askdata.ContentHash
	ConversationID     askdata.ID
	SeedContext        *ReportSeedContextInput
	SavedQuestionID    askdata.ID
}

type SubmitClarificationInput struct {
	RunID           askdata.ID
	ClarificationID askdata.ID
	OptionID        askdata.ID
	RunVersion      int64
}

type OperationResult struct {
	Snapshot orchestrator.ReplaySnapshot
	Replayed bool
}

type QuestionQuotaExceededError struct {
	Decision askdataobservability.QuotaDecision
}

func (failure *QuestionQuotaExceededError) Error() string { return ErrQuestionQuotaExceeded.Error() }
func (failure *QuestionQuotaExceededError) Unwrap() error { return ErrQuestionQuotaExceeded }

// Backend keeps HTTP concerns separate from the actor-scoped durable store.
// Inputs contain hashes and stable IDs only; the raw question never crosses
// this boundary.
type Backend interface {
	CreateQuestion(context.Context, RequestIdentity, CreateQuestionInput) (OperationResult, error)
	GetQuestion(context.Context, RequestIdentity, askdata.ID) (orchestrator.ReplaySnapshot, error)
	SubmitClarification(context.Context, RequestIdentity, SubmitClarificationInput) (OperationResult, error)
	ConfirmReleaseDrift(context.Context, RequestIdentity, ConfirmReleaseDriftInput) (ReleasePinResult, error)
	SubmitFeedback(context.Context, RequestIdentity, SubmitFeedbackInput) (FeedbackResult, error)
}

type identityResolver func(context.Context) (RequestIdentity, error)

type Handler struct {
	backend  Backend
	identity identityResolver
	stream   streamOptions
}

// NewHandler protects all Question endpoints with the repository's standard
// token/session/domain middleware.
func NewHandler(authService *auth.Service, backend Backend) http.Handler {
	protected := newProtectedHandler(backend, authenticatedIdentity, defaultStreamOptions())
	if service, ok := backend.(*PostgresService); ok && service != nil && service.pool != nil {
		protected = idempotencyMiddleware(
			NewPostgresIdempotencyRepository(service.pool), authenticatedIdentity, protected,
		)
	}
	return auth.RequireAccessToken(authService, protected)
}

func newProtectedHandler(
	backend Backend,
	identity identityResolver,
	stream streamOptions,
) http.Handler {
	handler := &Handler{backend: backend, identity: identity, stream: stream}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/questions", handler.createQuestion)
	mux.HandleFunc("GET /api/v1/questions/{runId}", handler.getQuestion)
	mux.HandleFunc("GET /api/v1/questions/{runId}/events", handler.streamEvents)
	mux.HandleFunc("POST /api/v1/questions/{runId}/clarifications", handler.submitClarification)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/release-drift", handler.confirmReleaseDrift)
	mux.HandleFunc("GET /api/v1/conversations", handler.listConversations)
	mux.HandleFunc("GET /api/v1/conversations/{conversationId}", handler.getConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/pin", handler.pinConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/unpin", handler.unpinConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/archive", handler.archiveConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/restore", handler.restoreConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/rename", handler.renameConversation)
	mux.HandleFunc("POST /api/v1/questions/{runId}/feedback", handler.submitFeedback)
	mux.HandleFunc("POST /api/v1/questions/{runId}/add-to-report", handler.addToReport)
	mux.HandleFunc("GET /api/v1/add-to-report-intents/{intentId}", handler.getAddToReportIntent)
	mux.HandleFunc("POST /api/v1/add-to-report-intents/{intentId}/confirm", handler.confirmAddToReport)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	})
}

func (handler *Handler) listConversations(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	backend, ok := handler.backend.(ConversationHistoryBackend)
	if !ok {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	limit := 50
	var err error
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeServiceError(writer, ErrInvalidRequest)
			return
		}
	}
	archived := request.URL.Query().Get("archived") == "true"
	page, err := backend.ListConversations(request.Context(), identity, strings.TrimSpace(request.URL.Query().Get("search")), archived, limit, request.URL.Query().Get("cursor"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}
func (handler *Handler) getConversation(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	backend, ok := handler.backend.(ConversationHistoryBackend)
	if !ok {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	id, err := parseRunID(request.PathValue("conversationId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("runLimit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeServiceError(writer, ErrInvalidRequest)
			return
		}
	}
	detail, err := backend.GetConversation(request.Context(), identity, id, limit, request.URL.Query().Get("runCursor"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}
func (handler *Handler) pinConversation(writer http.ResponseWriter, request *http.Request) {
	handler.mutateConversation(writer, request, "PIN")
}
func (handler *Handler) unpinConversation(writer http.ResponseWriter, request *http.Request) {
	handler.mutateConversation(writer, request, "UNPIN")
}
func (handler *Handler) archiveConversation(writer http.ResponseWriter, request *http.Request) {
	handler.mutateConversation(writer, request, "ARCHIVE")
}
func (handler *Handler) restoreConversation(writer http.ResponseWriter, request *http.Request) {
	handler.mutateConversation(writer, request, "RESTORE")
}
func (handler *Handler) renameConversation(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	backend, ok := handler.backend.(ConversationHistoryBackend)
	if !ok {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	if _, err := requireIdempotencyKey(request); err != nil {
		writeServiceError(writer, err)
		return
	}
	id, err := parseRunID(request.PathValue("conversationId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var input ConversationRenameInput
	if err = decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	result, err := backend.RenameConversation(request.Context(), identity, id, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
func (handler *Handler) mutateConversation(writer http.ResponseWriter, request *http.Request, operation string) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	backend, ok := handler.backend.(ConversationHistoryBackend)
	if !ok {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	if _, err := requireIdempotencyKey(request); err != nil {
		writeServiceError(writer, err)
		return
	}
	id, err := parseRunID(request.PathValue("conversationId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var input ConversationMutationInput
	if err = decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	var result ConversationSummary
	if operation == "PIN" || operation == "UNPIN" {
		result, err = backend.SetConversationPinned(request.Context(), identity, id, input, operation == "PIN")
	} else {
		result, err = backend.SetConversationArchived(request.Context(), identity, id, input, operation == "ARCHIVE")
	}
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func authenticatedIdentity(ctx context.Context) (RequestIdentity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	access, accessOK := database.AccessContextFromContext(ctx)
	if !ok || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		return RequestIdentity{}, ErrUnauthenticated
	}
	identity := RequestIdentity{
		TenantID: askdata.ID(claims.TenantID), ActorID: askdata.ID(claims.Subject),
		DomainID: askdata.ID(access.DomainID),
	}
	if err := identity.validate(); err != nil {
		return RequestIdentity{}, err
	}
	return identity, nil
}

func (handler *Handler) createQuestion(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "question endpoint does not accept query parameters")
		return
	}
	key, err := requireIdempotencyKey(request)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		Question       string                  `json:"question"`
		ConversationID string                  `json:"conversationId"`
		SeedContext    *ReportSeedContextInput `json:"seedContext,omitempty"`
	}
	if err := decodeStrictJSON(writer, request, &body); err != nil {
		writeServiceError(writer, err)
		return
	}
	question, err := normalizeQuestion(body.Question)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	idempotencyHash := askdata.HashBytes([]byte(questionIdempotencyDomain + key))
	conversationID := askdata.ID(strings.TrimSpace(body.ConversationID))
	if conversationID == "" {
		conversationID = deterministicConversationID(identity, idempotencyHash)
	} else if !canonicalUUID(conversationID) || body.ConversationID != string(conversationID) {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	result, err := handler.backend.CreateQuestion(request.Context(), identity, CreateQuestionInput{
		Question:           question,
		QuestionHash:       askdata.HashBytes([]byte(questionHashDomain + question)),
		IdempotencyKeyHash: idempotencyHash,
		ConversationID:     conversationID,
		SeedContext:        body.SeedContext,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, newOperationView(result))
}

func (handler *Handler) getQuestion(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "question endpoint does not accept query parameters")
		return
	}
	runID, err := parseRunID(request.PathValue("runId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	snapshot, err := handler.backend.GetQuestion(request.Context(), identity, runID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newRunView(snapshot))
}

func (handler *Handler) submitClarification(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "clarification endpoint does not accept query parameters")
		return
	}
	runID, err := parseRunID(request.PathValue("runId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	if _, err := requireIdempotencyKey(request); err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		ClarificationID string `json:"clarificationId"`
		OptionID        string `json:"optionId"`
		RunVersion      int64  `json:"runVersion"`
	}
	if err := decodeStrictJSON(writer, request, &body); err != nil {
		writeServiceError(writer, err)
		return
	}
	optionID := askdata.ID(strings.TrimSpace(body.OptionID))
	if optionID.Validate() != nil || body.OptionID != string(optionID) {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	clarificationID, err := parseRunID(body.ClarificationID)
	if err != nil || body.RunVersion < 1 {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	result, err := handler.backend.SubmitClarification(request.Context(), identity, SubmitClarificationInput{
		RunID: runID, ClarificationID: clarificationID, OptionID: optionID,
		RunVersion: body.RunVersion,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, newOperationView(result))
}

func (handler *Handler) confirmReleaseDrift(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "release drift endpoint does not accept query parameters")
		return
	}
	if _, err := requireIdempotencyKey(request); err != nil {
		writeServiceError(writer, err)
		return
	}
	conversationID, err := parseRunID(request.PathValue("conversationId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		PreviousReleaseID string `json:"previousReleaseId"`
		ActiveReleaseID   string `json:"activeReleaseId"`
	}
	if err := decodeStrictJSON(writer, request, &body); err != nil {
		writeServiceError(writer, err)
		return
	}
	previousReleaseID, previousErr := parseRunID(body.PreviousReleaseID)
	activeReleaseID, activeErr := parseRunID(body.ActiveReleaseID)
	if previousErr != nil || activeErr != nil {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	result, err := handler.backend.ConfirmReleaseDrift(request.Context(), identity, ConfirmReleaseDriftInput{
		ConversationID: conversationID, PreviousReleaseID: previousReleaseID,
		ActiveReleaseID: activeReleaseID,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) resolveIdentity(
	writer http.ResponseWriter,
	request *http.Request,
) (RequestIdentity, bool) {
	if handler == nil || handler.backend == nil || handler.identity == nil {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return RequestIdentity{}, false
	}
	identity, err := handler.identity(request.Context())
	if err != nil || identity.validate() != nil {
		writeServiceError(writer, ErrUnauthenticated)
		return RequestIdentity{}, false
	}
	return identity, true
}

func normalizeQuestion(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidRequest
	}
	value := strings.TrimSpace(raw)
	if value == "" || utf8.RuneCountInString(value) > maxQuestionRunes {
		return "", ErrInvalidRequest
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return "", ErrInvalidRequest
		}
	}
	return value, nil
}

func requireIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", ErrInvalidRequest
	}
	value := strings.TrimSpace(values[0])
	if value == "" || value != values[0] || len(value) > maxIdempotencyKeyBytes || !utf8.ValidString(value) {
		return "", ErrInvalidRequest
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalidRequest
		}
	}
	return value, nil
}

func deterministicConversationID(identity RequestIdentity, idempotencyHash askdata.ContentHash) askdata.ID {
	seed := string(identity.TenantID) + "\x00" + string(identity.ActorID) + "\x00" +
		string(identity.DomainID) + "\x00" + string(idempotencyHash)
	return askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String())
}

func parseRunID(raw string) (askdata.ID, error) {
	id := askdata.ID(raw)
	if !canonicalUUID(id) {
		return "", ErrInvalidRequest
	}
	return id, nil
}

func canonicalUUID(id askdata.ID) bool {
	parsed, err := uuid.Parse(string(id))
	return err == nil && parsed.String() == string(id)
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	mediaType, _, mediaTypeErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if request.Body == nil || mediaTypeErr != nil || strings.ToLower(mediaType) != "application/json" {
		return ErrInvalidRequest
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxQuestionBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest
	}
	return nil
}

type OperationView struct {
	RunID          askdata.ID         `json:"runId"`
	ConversationID askdata.ID         `json:"conversationId"`
	State          orchestrator.State `json:"state"`
	Replayed       bool               `json:"replayed"`
	EventsURL      string             `json:"eventsUrl"`
}

func newOperationView(result OperationResult) OperationView {
	run := result.Snapshot.Run
	return OperationView{
		RunID: run.ID, ConversationID: run.ConversationID, State: run.State,
		Replayed: result.Replayed, EventsURL: "/api/v1/questions/" + string(run.ID) + "/events",
	}
}

type RunView struct {
	RunID          askdata.ID               `json:"runId"`
	ConversationID askdata.ID               `json:"conversationId"`
	ParentRunID    askdata.ID               `json:"parentRunId,omitempty"`
	State          orchestrator.State       `json:"state"`
	Disposition    orchestrator.Disposition `json:"disposition"`
	Completion     *CompletionView          `json:"completion,omitempty"`
	Release        askdata.ReleaseRef       `json:"release"`
	Hashes         orchestrator.RunHashes   `json:"hashes"`
	Budget         RunBudgetView            `json:"budget"`
	RecordVersion  int64                    `json:"recordVersion"`
	LastEventID    int                      `json:"lastEventId"`
	CreatedAt      time.Time                `json:"createdAt"`
	UpdatedAt      time.Time                `json:"updatedAt"`
	CompletedAt    *time.Time               `json:"completedAt,omitempty"`
	AllowedActions []string                 `json:"allowedActions"`
}

type RunBudgetView struct {
	Limits orchestrator.BudgetLimits `json:"limits"`
	Usage  orchestrator.BudgetUsage  `json:"usage"`
}

type CompletionView struct {
	Code          string                      `json:"code"`
	ArtifactID    askdata.ID                  `json:"artifactId"`
	ArtifactType  orchestrator.ArtifactType   `json:"artifactType"`
	ArtifactHash  askdata.ContentHash         `json:"artifactHash"`
	EvidenceIDs   []askdata.ID                `json:"evidenceIds"`
	Answer        *AnswerPresentationView     `json:"answer,omitempty"`
	Clarification *ClarificationView          `json:"clarification,omitempty"`
	Result        *QuestionResultView         `json:"result,omitempty"`
	Outcome       *validator.Outcome          `json:"outcome,omitempty"`
	ScopeVerdict  *understanding.ScopeVerdict `json:"scopeVerdict,omitempty"`
}

// AnswerPresentationView exposes only verified narrative or the governed L1
// fallback state. Rejected model prose and verifier failure internals never
// cross the browser boundary.
type AnswerPresentationView struct {
	SchemaVersion     string                       `json:"schemaVersion"`
	NarrativeDegraded bool                         `json:"narrativeDegraded"`
	Hint              string                       `json:"hint,omitempty"`
	Verification      AnswerVerificationView       `json:"verification"`
	Narrative         *AnswerNarrativePresentation `json:"narrative,omitempty"`
}

type AnswerVerificationView struct {
	Attempts int  `json:"attempts"`
	Passed   bool `json:"passed"`
}

type AnswerNarrativePresentation struct {
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
}

type ClarificationView struct {
	ClarificationID askdata.ID                `json:"clarificationId"`
	ConflictCode    string                    `json:"conflictCode,omitempty"`
	Message         string                    `json:"message,omitempty"`
	Options         []ClarificationOptionView `json:"options"`
}

type ClarificationOptionView struct {
	OptionID    askdata.ID                 `json:"optionId"`
	Label       string                     `json:"label"`
	Difference  string                     `json:"difference,omitempty"`
	EvidenceIDs []askdata.ID               `json:"evidenceIds"`
	Evidence    *ClarificationEvidenceView `json:"evidence,omitempty"`
}

type ClarificationEvidenceView struct {
	Definition      string                   `json:"definition"`
	Owner           ClarificationOwnerView   `json:"owner"`
	SemanticVersion string                   `json:"semanticVersion"`
	SemanticStatus  string                   `json:"semanticStatus"`
	Time            ClarificationTimeView    `json:"time"`
	Quality         ClarificationQualityView `json:"quality"`
}

type ClarificationOwnerView struct {
	ID          askdata.ID `json:"id"`
	DisplayName string     `json:"displayName"`
}

type ClarificationTimeView struct {
	Label    string `json:"label"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

type ClarificationQualityView struct {
	Status          string `json:"status"`
	ScorePermillion *int   `json:"scorePermillion,omitempty"`
	DataAsOf        string `json:"dataAsOf"`
	RulesPassed     int    `json:"rulesPassed"`
	RulesTotal      int    `json:"rulesTotal"`
}

// QuestionResultView is the bounded browser-facing result contract. Every
// cell remains an exact string (or null); raw warehouse rows, SQL and prompts
// are never projected from the artifact payload.
type QuestionResultView struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	Title             string                     `json:"title"`
	ResolvedTimeSpec  *compiler.ResolvedTimeSpec `json:"resolvedTimeSpec,omitempty"`
	TimeSpec          *answer.TimeSpecView       `json:"timeSpec,omitempty"`
	Summary           ResultSummaryView          `json:"summary"`
	EvidenceIDs       []askdata.ID               `json:"evidenceIds"`
	Evidence          *ClarificationEvidenceView `json:"evidence,omitempty"`
	Datasets          []ResultDatasetView        `json:"datasets"`
	Views             []ResultPresentationView   `json:"views"`
	DefaultViewID     askdata.ID                 `json:"defaultViewId"`
	RecommendedViewID askdata.ID                 `json:"recommendedViewId,omitempty"`
}

type ResultSummaryView struct {
	MetricLabel    string                `json:"metricLabel"`
	Value          string                `json:"value"`
	FormattedValue string                `json:"formattedValue"`
	Unit           string                `json:"unit"`
	Comparison     *ResultComparisonView `json:"comparison,omitempty"`
	Time           ClarificationTimeView `json:"time"`
}

type ResultComparisonView struct {
	Label            string `json:"label"`
	Direction        string `json:"direction"`
	ChangePermillion int    `json:"changePermillion"`
	FormattedChange  string `json:"formattedChange"`
	BaselineStart    string `json:"baselineStart"`
	BaselineEnd      string `json:"baselineEnd"`
}

type ResultDatasetView struct {
	ID        askdata.ID           `json:"id"`
	Label     string               `json:"label"`
	Columns   []ResultColumnView   `json:"columns"`
	Rows      []map[string]*string `json:"rows"`
	Page      int                  `json:"page"`
	PageSize  int                  `json:"pageSize"`
	TotalRows int                  `json:"totalRows"`
}

type ResultColumnView struct {
	Key   askdata.ID `json:"key"`
	Label string     `json:"label"`
	Type  string     `json:"type"`
	Role  string     `json:"role"`
}

type ResultPresentationView struct {
	ID            askdata.ID   `json:"id"`
	Type          string       `json:"type"`
	Label         string       `json:"label"`
	DatasetID     askdata.ID   `json:"datasetId"`
	DimensionKeys []askdata.ID `json:"dimensionKeys"`
	MeasureKeys   []askdata.ID `json:"measureKeys"`
}

func newRunView(snapshot orchestrator.ReplaySnapshot) RunView {
	run := snapshot.Run
	view := RunView{
		RunID: run.ID, ConversationID: run.ConversationID, ParentRunID: run.ParentRunID,
		State: run.State, Disposition: run.Disposition, Release: run.Release,
		Hashes: run.Hashes, Budget: RunBudgetView{Limits: run.Limits, Usage: run.Usage},
		RecordVersion: run.RecordVersion, LastEventID: lastEventID(snapshot.Events),
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, CompletedAt: run.CompletedAt,
	}
	if run.Terminal() {
		view.Completion = publicCompletion(snapshot)
	}
	view.AllowedActions = allowedRunActions(view, snapshot)
	return view
}

// allowedRunActions is derived only from the server-validated completion
// projection. Clients must not infer export, report or decision eligibility
// from a visual state or from payload fields that failed sanitization.
func allowedRunActions(view RunView, snapshot orchestrator.ReplaySnapshot) []string {
	result := []string{}
	completion := view.Completion
	if completion == nil || completion.ArtifactType != orchestrator.ArtifactAnswer ||
		completion.ArtifactHash.Validate() != nil || completion.Answer == nil {
		return result
	}
	result = append(result, "SAVE", "SHARE", "EXPORT", "CREATE_DECISION")
	_, _, _, exportErr := reportExportArtifacts(snapshot)
	if completion.Result != nil && completion.Outcome != nil &&
		completion.Outcome.Status == validator.OutcomeAnswered && exportErr == nil {
		result = append(result, "ADD_TO_REPORT")
	}
	return result
}

func publicCompletion(snapshot orchestrator.ReplaySnapshot) *CompletionView {
	run := snapshot.Run
	for _, artifact := range snapshot.Artifacts {
		if artifact.Hash != run.CompletionArtifact {
			continue
		}
		view := &CompletionView{
			Code: run.CompletionCode, ArtifactID: artifact.ID, ArtifactType: artifact.Type,
			ArtifactHash: artifact.Hash,
			EvidenceIDs:  copyEvidenceIDs(artifact.EvidenceIDs),
		}
		if artifact.Type == orchestrator.ArtifactClarification {
			view.Clarification = parsePublicClarification(artifact.ID, artifact.Payload)
		}
		if artifact.Type == orchestrator.ArtifactAnswer {
			view.Answer = parsePublicAnswer(artifact.Payload)
			view.Result = parsePublicResult(artifact.Payload)
			view.Outcome = parsePublicOutcome(artifact.Payload)
		}
		if artifact.Type == orchestrator.ArtifactBlock && artifact.SchemaVersion == understanding.ScopeVerdictSchemaVersion {
			view.ScopeVerdict = parsePublicScopeVerdict(artifact.Payload)
		}
		return view
	}
	return nil
}

func copyEvidenceIDs(values []askdata.ID) []askdata.ID {
	result := make([]askdata.ID, len(values))
	copy(result, values)
	return result
}

func parsePublicAnswer(payload json.RawMessage) *AnswerPresentationView {
	raw := payload
	artifact, err := answer.Decode(raw)
	if err != nil {
		var envelope struct {
			Artifact json.RawMessage `json:"artifact"`
			Answer   json.RawMessage `json:"answer"`
		}
		if json.Unmarshal(payload, &envelope) != nil {
			return nil
		}
		rawArtifact := envelope.Artifact
		if len(rawArtifact) == 0 {
			rawArtifact = envelope.Answer
		}
		if len(rawArtifact) == 0 || string(rawArtifact) == "null" {
			return nil
		}
		artifact, err = answer.Decode(rawArtifact)
		if err != nil {
			return nil
		}
	}
	view := &AnswerPresentationView{
		SchemaVersion: artifact.SchemaVersion, NarrativeDegraded: artifact.Verification.Degraded,
		Verification: AnswerVerificationView{
			Attempts: artifact.Verification.Attempts, Passed: artifact.Verification.Passed,
		},
	}
	if artifact.Verification.Degraded {
		view.Hint = answer.DegradedNarrativeHint
		return view
	}
	view.Narrative = &AnswerNarrativePresentation{
		Summary:  artifact.Layers.Narrative.Summary,
		Findings: append([]string{}, artifact.Layers.Narrative.Findings...),
	}
	return view
}

func parsePublicScopeVerdict(payload json.RawMessage) *understanding.ScopeVerdict {
	var verdict understanding.ScopeVerdict
	if askdata.DecodeStrictJSON(payload, &verdict) != nil || verdict.Validate() != nil {
		return nil
	}
	if verdict.ParsedContext != nil {
		normalized, err := verdict.ParsedContext.Normalize()
		if err != nil || normalized.Empty() {
			return nil
		}
		verdict.ParsedContext = &normalized
	}
	return &verdict
}

type clarificationPayload struct {
	ConflictCode          string                       `json:"conflictCode"`
	ClarificationQuestion string                       `json:"clarificationQuestion"`
	Options               []clarificationPayloadOption `json:"options"`
	ClarificationOptions  []clarificationPayloadOption `json:"clarificationOptions"`
	Retryable             bool                         `json:"retryable"`
	Clarification         *struct {
		ConflictCode string                       `json:"conflictCode"`
		Question     string                       `json:"question"`
		Options      []clarificationPayloadOption `json:"options"`
	} `json:"clarification"`
}

type clarificationPayloadOption struct {
	OptionID     askdata.ID                 `json:"optionId"`
	Label        string                     `json:"label"`
	Difference   string                     `json:"difference"`
	EvidenceIDs  []askdata.ID               `json:"evidenceIds"`
	EvidenceRefs []askdata.EvidenceRef      `json:"evidenceRefs"`
	Evidence     *ClarificationEvidenceView `json:"evidence"`
}

func parsePublicClarification(clarificationID askdata.ID, payload json.RawMessage) *ClarificationView {
	if !canonicalUUID(clarificationID) {
		return nil
	}
	var value clarificationPayload
	if json.Unmarshal(payload, &value) != nil {
		return nil
	}
	if value.Clarification != nil {
		value.ConflictCode = value.Clarification.ConflictCode
		value.ClarificationQuestion = value.Clarification.Question
		value.Options = value.Clarification.Options
	}
	options := value.Options
	if len(options) == 0 {
		options = value.ClarificationOptions
	}
	if len(options) == 0 && value.Retryable {
		options = []clarificationPayloadOption{{OptionID: "retry", Label: "重试"}}
	}
	if len(options) == 0 || len(options) > toolhost.MaxClarificationOptions {
		return nil
	}
	result := &ClarificationView{
		ClarificationID: clarificationID,
		Message:         boundedPublicText(value.ClarificationQuestion, 512),
		Options:         make([]ClarificationOptionView, 0, len(options)),
	}
	if publicCodePattern.MatchString(value.ConflictCode) {
		result.ConflictCode = value.ConflictCode
	}
	seen := map[askdata.ID]bool{}
	for _, option := range options {
		label := boundedPublicText(option.Label, 256)
		if option.OptionID.Validate() != nil || seen[option.OptionID] || label == "" {
			return nil
		}
		seen[option.OptionID] = true
		evidenceIDs, ok := publicClarificationEvidenceIDs(option.EvidenceIDs, option.EvidenceRefs)
		if !ok {
			return nil
		}
		result.Options = append(result.Options, ClarificationOptionView{
			OptionID: option.OptionID, Label: label,
			Difference:  boundedPublicText(option.Difference, 512),
			EvidenceIDs: evidenceIDs,
			Evidence:    sanitizeClarificationEvidence(option.Evidence),
		})
	}
	return result
}

func publicClarificationEvidenceIDs(ids []askdata.ID, refs []askdata.EvidenceRef) ([]askdata.ID, bool) {
	seen := map[askdata.ID]bool{}
	result := make([]askdata.ID, 0, len(ids)+len(refs))
	appendID := func(id askdata.ID) bool {
		if id.Validate() != nil {
			return false
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
		return len(result) <= 64
	}
	for _, id := range ids {
		if !appendID(id) {
			return nil, false
		}
	}
	for _, ref := range refs {
		if ref.Validate() != nil || !appendID(ref.EvidenceID) {
			return nil, false
		}
	}
	return result, true
}

func sanitizeClarificationEvidence(value *ClarificationEvidenceView) *ClarificationEvidenceView {
	if value == nil {
		return nil
	}
	definition := boundedPublicText(value.Definition, 4096)
	ownerName := boundedPublicText(value.Owner.DisplayName, 256)
	semanticVersion := boundedPublicText(value.SemanticVersion, 128)
	timeLabel := boundedPublicText(value.Time.Label, 256)
	timezone := boundedPublicText(value.Time.Timezone, 128)
	if definition == "" || value.Owner.ID.Validate() != nil || ownerName == "" ||
		semanticVersion == "" || !publicCodePattern.MatchString(value.SemanticStatus) ||
		timeLabel == "" || !validPublicTimeRange(value.Time.Start, value.Time.End) ||
		timezone == "" || !validPublicQuality(value.Quality) {
		return nil
	}
	copy := *value
	copy.Definition = definition
	copy.Owner.DisplayName = ownerName
	copy.SemanticVersion = semanticVersion
	copy.Time.Label = timeLabel
	copy.Time.Timezone = timezone
	return &copy
}

type questionResultPayload struct {
	Result json.RawMessage `json:"result"`
}

func parsePublicResult(payload json.RawMessage) *QuestionResultView {
	var envelope questionResultPayload
	if json.Unmarshal(payload, &envelope) != nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	var value QuestionResultView
	if json.Unmarshal(envelope.Result, &value) != nil {
		return nil
	}
	// Durable answer artifacts use "records" rather than the forbidden audit
	// key "rows". The HTTP projection restores the browser contract only after
	// every cell has crossed the bounded result validator below.
	var persisted struct {
		Datasets []struct {
			Records []map[string]*string `json:"records"`
		} `json:"datasets"`
	}
	if json.Unmarshal(envelope.Result, &persisted) == nil && len(persisted.Datasets) == len(value.Datasets) {
		for index := range value.Datasets {
			if value.Datasets[index].Rows == nil {
				value.Datasets[index].Rows = persisted.Datasets[index].Records
			}
		}
	}
	if value.SchemaVersion != "question-result-v1" ||
		len(value.Datasets) == 0 || len(value.Datasets) > maxPublicResultDatasets ||
		len(value.Views) == 0 || len(value.Views) > maxPublicResultViews {
		return nil
	}
	title := boundedPublicText(value.Title, 256)
	metricLabel := boundedPublicText(value.Summary.MetricLabel, 256)
	formattedValue := boundedPublicText(value.Summary.FormattedValue, 128)
	unit := boundedPublicText(value.Summary.Unit, 32)
	timeLabel := boundedPublicText(value.Summary.Time.Label, 256)
	timezone := boundedPublicText(value.Summary.Time.Timezone, 128)
	if title == "" || metricLabel == "" || formattedValue == "" || unit == "" ||
		!publicNumberPattern.MatchString(value.Summary.Value) || timeLabel == "" || timezone == "" ||
		!validPublicTimeRange(value.Summary.Time.Start, value.Summary.Time.End) {
		return nil
	}
	evidenceIDs, ok := publicClarificationEvidenceIDs(value.EvidenceIDs, nil)
	if !ok || len(evidenceIDs) == 0 {
		return nil
	}
	evidence := sanitizeClarificationEvidence(value.Evidence)
	if evidence == nil {
		return nil
	}
	var timeSpec *answer.TimeSpecView
	if value.ResolvedTimeSpec != nil {
		if compiler.ValidateResolvedTimeSpec(*value.ResolvedTimeSpec) != nil {
			return nil
		}
		rendered := answer.RenderTimeSpec(*value.ResolvedTimeSpec, answer.RenderOptions{})
		if rendered.RangeLabel == "" || rendered.AsOfLabel == "" || rendered.PolicyLabel == "" {
			return nil
		}
		timeSpec = &rendered
	}

	result := &QuestionResultView{
		SchemaVersion: value.SchemaVersion, Title: title,
		ResolvedTimeSpec: value.ResolvedTimeSpec, TimeSpec: timeSpec,
		Summary: value.Summary, EvidenceIDs: evidenceIDs, Evidence: evidence,
		Datasets:      make([]ResultDatasetView, 0, len(value.Datasets)),
		Views:         make([]ResultPresentationView, 0, len(value.Views)),
		DefaultViewID: value.DefaultViewID, RecommendedViewID: value.RecommendedViewID,
	}
	result.Summary.MetricLabel = metricLabel
	result.Summary.FormattedValue = formattedValue
	result.Summary.Unit = unit
	result.Summary.Time.Label = timeLabel
	result.Summary.Time.Timezone = timezone
	if value.Summary.Comparison != nil {
		comparison := *value.Summary.Comparison
		comparison.Label = boundedPublicText(comparison.Label, 128)
		comparison.FormattedChange = boundedPublicText(comparison.FormattedChange, 64)
		if comparison.Label == "" || comparison.FormattedChange == "" ||
			(comparison.Direction != "UP" && comparison.Direction != "DOWN" && comparison.Direction != "FLAT") ||
			comparison.ChangePermillion < -10_000_000 || comparison.ChangePermillion > 10_000_000 ||
			!validPublicTimeRange(comparison.BaselineStart, comparison.BaselineEnd) {
			return nil
		}
		result.Summary.Comparison = &comparison
	}

	datasets := make(map[askdata.ID]ResultDatasetView, len(value.Datasets))
	totalCells := 0
	for _, dataset := range value.Datasets {
		sanitized, valid := sanitizePublicResultDataset(dataset)
		if !valid || datasets[sanitized.ID].ID != "" {
			return nil
		}
		totalCells += len(sanitized.Columns) * len(sanitized.Rows)
		if totalCells > maxPublicResultCells {
			return nil
		}
		datasets[sanitized.ID] = sanitized
		result.Datasets = append(result.Datasets, sanitized)
	}

	views := make(map[askdata.ID]bool, len(value.Views))
	for _, view := range value.Views {
		sanitized, valid := sanitizePublicResultView(view, datasets)
		if !valid || views[sanitized.ID] {
			return nil
		}
		views[sanitized.ID] = true
		result.Views = append(result.Views, sanitized)
	}
	if !views[result.DefaultViewID] || result.RecommendedViewID != "" && !views[result.RecommendedViewID] {
		return nil
	}
	return result
}

func sanitizePublicResultDataset(value ResultDatasetView) (ResultDatasetView, bool) {
	label := boundedPublicText(value.Label, 256)
	if value.ID.Validate() != nil || label == "" || len(value.Columns) == 0 ||
		len(value.Columns) > maxPublicResultColumns || len(value.Rows) > maxPublicResultRows ||
		value.Page < 1 || value.PageSize < 1 || value.PageSize > maxPublicResultRows ||
		value.TotalRows < len(value.Rows) ||
		(value.TotalRows == 0 && (value.Page != 1 || len(value.Rows) != 0)) ||
		(value.TotalRows > 0 && (value.Page-1)*value.PageSize >= value.TotalRows) {
		return ResultDatasetView{}, false
	}
	columns := make(map[askdata.ID]ResultColumnView, len(value.Columns))
	result := ResultDatasetView{
		ID: value.ID, Label: label, Columns: make([]ResultColumnView, 0, len(value.Columns)),
		Rows: make([]map[string]*string, 0, len(value.Rows)), Page: value.Page,
		PageSize: value.PageSize, TotalRows: value.TotalRows,
	}
	for _, column := range value.Columns {
		column.Label = boundedPublicText(column.Label, 128)
		if column.Key.Validate() != nil || column.Label == "" || columns[column.Key].Key != "" ||
			!validPublicResultColumn(column) {
			return ResultDatasetView{}, false
		}
		columns[column.Key] = column
		result.Columns = append(result.Columns, column)
	}
	for _, row := range value.Rows {
		if len(row) != len(columns) {
			return ResultDatasetView{}, false
		}
		copy := make(map[string]*string, len(row))
		for key, cell := range row {
			column, exists := columns[askdata.ID(key)]
			if !exists || !validPublicResultCell(cell, column.Type) {
				return ResultDatasetView{}, false
			}
			if cell == nil {
				copy[key] = nil
				continue
			}
			cellCopy := *cell
			copy[key] = &cellCopy
		}
		result.Rows = append(result.Rows, copy)
	}
	return result, true
}

func validPublicResultColumn(column ResultColumnView) bool {
	if column.Role != "DIMENSION" && column.Role != "MEASURE" {
		return false
	}
	switch column.Type {
	case "STRING", "INTEGER", "DECIMAL", "DATE", "DATETIME":
		return true
	default:
		return false
	}
}

func validPublicResultCell(cell *string, columnType string) bool {
	if cell == nil {
		return true
	}
	switch columnType {
	case "STRING":
		return boundedPublicText(*cell, 1024) != ""
	case "INTEGER":
		return publicIntegerPattern.MatchString(*cell)
	case "DECIMAL":
		return publicNumberPattern.MatchString(*cell)
	case "DATE":
		_, err := time.Parse(time.DateOnly, *cell)
		return err == nil
	case "DATETIME":
		_, err := time.Parse(time.RFC3339, *cell)
		return err == nil
	default:
		return false
	}
}

func sanitizePublicResultView(
	value ResultPresentationView,
	datasets map[askdata.ID]ResultDatasetView,
) (ResultPresentationView, bool) {
	// Normalize empty slices for the JSON boundary. Historical artifacts may
	// contain null for a view with no dimensions; browser code must always
	// receive arrays so one malformed old result cannot crash the workspace.
	value.DimensionKeys = append([]askdata.ID{}, value.DimensionKeys...)
	value.MeasureKeys = append([]askdata.ID{}, value.MeasureKeys...)
	value.Label = boundedPublicText(value.Label, 128)
	dataset, exists := datasets[value.DatasetID]
	if value.ID.Validate() != nil || value.Label == "" || !exists ||
		len(value.DimensionKeys) > 4 || len(value.MeasureKeys) > 4 {
		return ResultPresentationView{}, false
	}
	columns := make(map[askdata.ID]ResultColumnView, len(dataset.Columns))
	for _, column := range dataset.Columns {
		columns[column.Key] = column
	}
	seen := map[askdata.ID]bool{}
	for _, key := range append(append([]askdata.ID(nil), value.DimensionKeys...), value.MeasureKeys...) {
		if key.Validate() != nil || seen[key] || columns[key].Key == "" {
			return ResultPresentationView{}, false
		}
		seen[key] = true
	}
	for _, key := range value.DimensionKeys {
		if columns[key].Role != "DIMENSION" {
			return ResultPresentationView{}, false
		}
	}
	for _, key := range value.MeasureKeys {
		column := columns[key]
		if column.Role != "MEASURE" || column.Type != "INTEGER" && column.Type != "DECIMAL" {
			return ResultPresentationView{}, false
		}
	}
	switch value.Type {
	case "LINE":
		return value, len(dataset.Rows) >= 2 && len(value.DimensionKeys) == 1 &&
			len(value.MeasureKeys) == 1 &&
			(columns[value.DimensionKeys[0]].Type == "DATE" || columns[value.DimensionKeys[0]].Type == "DATETIME")
	case "BAR":
		return value, len(dataset.Rows) >= 2 && len(dataset.Rows) <= 20 &&
			len(value.DimensionKeys) == 1 && len(value.MeasureKeys) == 1
	case "TABLE":
		return value, true
	case "KPI":
		return value, len(dataset.Rows) == 1 && len(value.DimensionKeys) == 0 && len(value.MeasureKeys) == 1
	default:
		return ResultPresentationView{}, false
	}
}

func validPublicTimeRange(start, end string) bool {
	left, leftOK := parsePublicTime(start)
	right, rightOK := parsePublicTime(end)
	return leftOK && rightOK && !right.Before(left)
}

func parsePublicTime(value string) (time.Time, bool) {
	if parsed, err := time.Parse(time.DateOnly, value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func validPublicQuality(value ClarificationQualityView) bool {
	if value.Status != "PASS" && value.Status != "WARNING" && value.Status != "FAIL" && value.Status != "UNKNOWN" {
		return false
	}
	if _, ok := parsePublicTime(value.DataAsOf); !ok {
		return false
	}
	if value.ScorePermillion != nil && (*value.ScorePermillion < 0 || *value.ScorePermillion > 1_000_000) {
		return false
	}
	return value.RulesPassed >= 0 && value.RulesTotal >= value.RulesPassed && value.RulesTotal <= 10_000
}

func boundedPublicText(value string, maxRunes int) string {
	if strings.TrimSpace(value) != value || value == "" || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maxRunes {
		return ""
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return ""
		}
	}
	return value
}

func lastEventID(events []orchestrator.Event) int {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Index
}

func writeServiceError(writer http.ResponseWriter, err error) {
	var drift *ReleaseDriftRequiredError
	if errors.As(err, &drift) {
		writeJSON(writer, http.StatusConflict, struct {
			Code         string           `json:"code"`
			Message      string           `json:"message"`
			ReleaseDrift ReleaseDriftView `json:"releaseDrift"`
		}{
			Code:         "RELEASE_DRIFT_CONFIRM_REQUIRED",
			Message:      "会话口径已更新，请确认后再发起新查询。",
			ReleaseDrift: drift.Drift,
		})
		return
	}
	var quotaFailure *QuestionQuotaExceededError
	if errors.As(err, &quotaFailure) {
		writer.Header().Set("Retry-After", quotaRetryAfter(quotaFailure.Decision, time.Now().UTC()))
		writeJSON(writer, http.StatusTooManyRequests, struct {
			Code        string                              `json:"code"`
			Message     string                              `json:"message"`
			Limiters    []askdataobservability.QuotaLimiter `json:"limiters"`
			RestoreAt   *time.Time                          `json:"restoreAt,omitempty"`
			RequestPath string                              `json:"requestPath"`
		}{
			Code: "QUOTA_EXCEEDED", Message: "当前问数配额已用尽，请等待恢复或提交额度申请。",
			Limiters:  quotaFailure.Decision.Limiters,
			RestoreAt: earliestQuotaReset(quotaFailure.Decision), RequestPath: "/api/v1/data-requests",
		})
		return
	}
	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, orchestrator.ErrInvalidRun):
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "question request is invalid")
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, orchestrator.ErrInvalidAccessContext):
		writeError(writer, http.StatusUnauthorized, "QUESTION_AUTHENTICATION_REQUIRED", "valid question access is required")
	case errors.Is(err, orchestrator.ErrRunNotFound):
		writeError(writer, http.StatusNotFound, "QUESTION_RUN_NOT_FOUND", "question run was not found")
	case errors.Is(err, ErrNoActiveRelease):
		writeError(writer, http.StatusConflict, "QUESTION_RELEASE_UNAVAILABLE", "no active semantic release is available")
	case errors.Is(err, orchestrator.ErrReleaseNotRunnable):
		writeError(writer, http.StatusConflict, "RELEASE_NOT_RUNNABLE", "semantic release cannot create a new question run")
	case errors.Is(err, orchestrator.ErrReleaseProjectionMismatch):
		writeError(writer, http.StatusConflict, "RELEASE_PROJECTION_MISMATCH", "semantic release projections are not ready")
	case errors.Is(err, ErrClarificationRequired):
		writeError(writer, http.StatusConflict, "QUESTION_CLARIFICATION_NOT_ACCEPTED", "question run does not accept clarification")
	case errors.Is(err, ErrClarificationOption):
		writeError(writer, http.StatusConflict, "QUESTION_CLARIFICATION_OPTION_INVALID", "clarification option is no longer available")
	case errors.Is(err, ErrClarificationAnswered):
		writeError(writer, http.StatusConflict, "QUESTION_CLARIFICATION_ALREADY_ANSWERED", "clarification was already answered")
	case errors.Is(err, orchestrator.ErrClarificationExpired):
		writeError(writer, http.StatusConflict, "CLARIFICATION_EXPIRED", "clarification deadline has expired")
	case errors.Is(err, ErrReleaseDriftRequired):
		writeError(writer, http.StatusConflict, "RELEASE_DRIFT_CONFIRM_REQUIRED", "conversation release drift changed; refresh and retry")
	case errors.Is(err, ErrFeedbackNotAccepted):
		writeError(writer, http.StatusConflict, "QUESTION_FEEDBACK_NOT_ACCEPTED", "question run does not accept feedback")
	case errors.Is(err, ErrFeedbackConflict):
		writeError(writer, http.StatusConflict, "QUESTION_FEEDBACK_CONFLICT", "question feedback changed; refresh and retry")
	case errors.Is(err, ErrPartialResultNotExportable):
		writeError(writer, http.StatusConflict, "RESULT_PARTIAL_NOT_EXPORTABLE", "结果不完整，不能直接加入报告；请缩小查询范围，或确认缺失范围后重新运行。")
	case errors.Is(err, ErrAddToReportNotAccepted):
		writeError(writer, http.StatusConflict, "RESULT_NOT_EXPORTABLE", "question result cannot be added to a report")
	case errors.Is(err, ErrAddToReportUnavailable):
		writeError(writer, http.StatusServiceUnavailable, "REPORT_ADD_UNAVAILABLE", "report integration is temporarily unavailable")
	case errors.Is(err, orchestrator.ErrIdempotencyConflict):
		writeError(writer, http.StatusConflict, "QUESTION_IDEMPOTENCY_CONFLICT", "idempotency key was already used for a different request")
	case errors.Is(err, orchestrator.ErrPinnedScopeMismatch):
		writeError(writer, http.StatusForbidden, "QUESTION_SCOPE_CHANGED", "question access scope has changed")
	case errors.Is(err, orchestrator.ErrVersionConflict), errors.Is(err, orchestrator.ErrTerminalRun):
		writeError(writer, http.StatusConflict, "QUESTION_RUN_CONFLICT", "question run changed; refresh and retry")
	default:
		writeError(writer, http.StatusInternalServerError, "QUESTION_SERVICE_FAILED", "question service failed")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}

func earliestQuotaReset(decision askdataobservability.QuotaDecision) *time.Time {
	var earliest time.Time
	for _, limiter := range decision.Limiters {
		if !limiter.Exceeded || limiter.ResetAt.IsZero() {
			continue
		}
		if earliest.IsZero() || limiter.ResetAt.Before(earliest) {
			earliest = limiter.ResetAt.UTC()
		}
	}
	if earliest.IsZero() {
		return nil
	}
	return &earliest
}

func quotaRetryAfter(decision askdataobservability.QuotaDecision, now time.Time) string {
	reset := earliestQuotaReset(decision)
	if reset == nil || !reset.After(now) {
		return "60"
	}
	seconds := int64(reset.Sub(now)/time.Second) + 1
	return strconv.FormatInt(seconds, 10)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// PostgresService resolves the current actor roles and a pinned release under
// RLS before delegating to the durable orchestrator store.
type PostgresService struct {
	pool        *pgxpool.Pool
	runs        questionRunStore
	quotas      quotaChecker
	questions   *orchestrator.PostgresQuestionEnvelopeStore
	scopeRunner scopeTransactionRunner
}

type scopeTransactionRunner func(context.Context, string, func(pgx.Tx) error) error

type questionRunStore interface {
	CreateRun(context.Context, orchestrator.CreateRunRequest) (orchestrator.CreateResult, error)
	Resume(context.Context, orchestrator.ResumeRequest) (orchestrator.ReplaySnapshot, error)
}

type clarificationExpirer interface {
	ExpireClarification(context.Context, orchestrator.ResumeRequest, time.Time) (bool, error)
}

type quotaChecker interface {
	Check(context.Context, askdataobservability.QuotaCheckRequest) (askdataobservability.QuotaDecision, error)
}

func NewPostgresService(pool *pgxpool.Pool) *PostgresService {
	return NewPostgresServiceWithClarificationTimeout(pool, orchestrator.DefaultClarificationTimeout)
}

func NewPostgresServiceWithClarificationTimeout(pool *pgxpool.Pool, timeout time.Duration) *PostgresService {
	quotaStore, _ := askdataobservability.NewQuotaPostgresStore(pool)
	return &PostgresService{
		pool: pool, runs: orchestrator.NewPostgresStoreWithClarificationTimeout(pool, timeout),
		quotas: quotaStore,
		scopeRunner: func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, pool, tenantID, fn)
		},
	}
}

func (service *PostgresService) SetQuestionEnvelopeStore(
	store *orchestrator.PostgresQuestionEnvelopeStore,
) {
	if service != nil {
		service.questions = store
	}
}

func (service *PostgresService) CreateQuestion(
	ctx context.Context,
	identity RequestIdentity,
	input CreateQuestionInput,
) (OperationResult, error) {
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil ||
		input.QuestionHash.Validate() != nil || input.IdempotencyKeyHash.Validate() != nil ||
		!canonicalUUID(input.ConversationID) || (input.SeedContext != nil && input.SavedQuestionID != "") {
		return OperationResult{}, ErrInvalidRequest
	}
	question := strings.TrimSpace(input.Question)
	if service.questions != nil && (question == "" ||
		(input.SavedQuestionID == "" && askdata.HashBytes([]byte(questionHashDomain+question)) != input.QuestionHash)) {
		return OperationResult{}, ErrInvalidRequest
	}
	var scope askdata.PolicyScope
	var seed *orchestrator.SeedContext
	var err error
	if input.SeedContext != nil {
		validated, release, validateErr := service.validateReportSeedContext(ctx, identity, *input.SeedContext)
		if validateErr != nil {
			return OperationResult{}, validateErr
		}
		scope, err = service.resolveExplicitReleaseScope(ctx, identity, release)
		seed = &validated
	} else if input.SavedQuestionID != "" {
		validated, release, validateErr := service.validateSavedQuestionSeedContext(ctx, identity, input.SavedQuestionID)
		if validateErr != nil {
			return OperationResult{}, validateErr
		}
		scope, err = service.resolveExplicitReleaseScope(ctx, identity, release)
		seed = &validated
	} else {
		scope, err = service.resolveActiveScope(ctx, identity)
		if err == nil {
			var drift *ReleaseDriftView
			_, drift, err = service.resolveConversationRelease(ctx, identity, input.ConversationID, scope.Release)
			if drift != nil {
				return OperationResult{}, &ReleaseDriftRequiredError{Drift: *drift}
			}
		}
	}
	if err != nil {
		return OperationResult{}, err
	}
	if service.quotas != nil {
		certifiedFastPath, checkErr := service.isCertifiedFastPath(ctx, identity, scope, input.QuestionHash)
		if checkErr != nil {
			return OperationResult{}, checkErr
		}
		decision, checkErr := service.quotas.Check(ctx, askdataobservability.QuotaCheckRequest{
			TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
			RunID: askdata.ID(uuid.NewString()), Reserve: askdataobservability.QuotaUsage{Runs: 1},
			CertifiedFastPath: certifiedFastPath, At: time.Now().UTC(),
		})
		if checkErr != nil {
			return OperationResult{}, checkErr
		}
		if !decision.Allowed {
			return OperationResult{}, &QuestionQuotaExceededError{Decision: decision}
		}
	}
	created, err := service.runs.CreateRun(ctx, orchestrator.CreateRunRequest{
		Scope: scope, DomainID: identity.DomainID, ConversationID: input.ConversationID,
		IdempotencyKeyHash: input.IdempotencyKeyHash, QuestionHash: input.QuestionHash,
		SeedContext: seed,
	})
	if err != nil {
		return OperationResult{}, err
	}
	if service.questions != nil {
		if err := service.questions.SaveQuestion(ctx, orchestrator.QuestionRetentionBinding{
			Scope: scope, DomainID: identity.DomainID, ConversationID: input.ConversationID,
			RunID: created.Run.ID, QuestionHash: input.QuestionHash,
		}, question, time.Now().UTC()); err != nil {
			return OperationResult{}, err
		}
	}
	snapshot, err := service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: created.Run.ID,
	})
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Snapshot: snapshot, Replayed: created.Replayed}, nil
}

func (service *PostgresService) isCertifiedFastPath(
	ctx context.Context,
	identity RequestIdentity,
	scope askdata.PolicyScope,
	questionHash askdata.ContentHash,
) (bool, error) {
	runner := service.scopeRunner
	if runner == nil {
		runner = func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, service.pool, tenantID, fn)
		}
	}
	var certified bool
	err := runner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.certified_examples AS example
			JOIN askdata.certified_example_versions AS version
			  ON version.certified_example_id=example.id
			 AND version.tenant_id=example.tenant_id AND version.domain_id=example.domain_id
			JOIN askdata.release_objects AS release_object
			  ON release_object.tenant_id=version.tenant_id
			 AND release_object.domain_id=version.domain_id
			 AND release_object.object_type='CERTIFIED_EXAMPLE'
			 AND release_object.object_version_id=version.id
			WHERE example.tenant_id=$1 AND example.domain_id=$2
			  AND example.question_hash=$3 AND version.status='CERTIFIED'
			  AND release_object.release_id=$4
		)`, identity.TenantID, identity.DomainID, questionHash, scope.Release.ReleaseID).Scan(&certified)
	})
	if err != nil {
		return false, fmt.Errorf("%w: resolve certified fast path", ErrQuestionServiceFailure)
	}
	return certified, nil
}

func (service *PostgresService) GetQuestion(
	ctx context.Context,
	identity RequestIdentity,
	runID askdata.ID,
) (orchestrator.ReplaySnapshot, error) {
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil || !canonicalUUID(runID) {
		return orchestrator.ReplaySnapshot{}, ErrInvalidRequest
	}
	scope, err := service.resolveRunScope(ctx, identity, runID)
	if err != nil {
		return orchestrator.ReplaySnapshot{}, err
	}
	request := orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: runID,
	}
	if expirer, ok := service.runs.(clarificationExpirer); ok {
		if _, err := expirer.ExpireClarification(ctx, request, time.Now().UTC()); err != nil {
			return orchestrator.ReplaySnapshot{}, err
		}
	}
	return service.runs.Resume(ctx, request)
}

func (service *PostgresService) SubmitClarification(
	ctx context.Context,
	identity RequestIdentity,
	input SubmitClarificationInput,
) (OperationResult, error) {
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil ||
		!canonicalUUID(input.RunID) || !canonicalUUID(input.ClarificationID) ||
		input.OptionID.Validate() != nil || input.RunVersion < 1 {
		return OperationResult{}, ErrInvalidRequest
	}
	scope, err := service.resolveRunScope(ctx, identity, input.RunID)
	if err != nil {
		return OperationResult{}, err
	}
	resumeRequest := orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: input.RunID,
	}
	if expirer, ok := service.runs.(clarificationExpirer); ok {
		if _, err := expirer.ExpireClarification(ctx, resumeRequest, time.Now().UTC()); err != nil {
			return OperationResult{}, err
		}
	}
	parent, err := service.runs.Resume(ctx, resumeRequest)
	if err != nil {
		return OperationResult{}, err
	}
	if parent.Run.State == orchestrator.StateClarificationExpired {
		return OperationResult{}, orchestrator.ErrClarificationExpired
	}
	activeScope, err := service.resolveActiveScope(ctx, identity)
	if err != nil {
		return OperationResult{}, err
	}
	_, drift, err := service.resolveConversationRelease(ctx, identity, parent.Run.ConversationID, activeScope.Release)
	if err != nil {
		return OperationResult{}, err
	}
	if drift != nil {
		if _, err := service.confirmReleaseDrift(ctx, identity, ConfirmReleaseDriftInput{
			ConversationID:    parent.Run.ConversationID,
			PreviousReleaseID: drift.Previous.ReleaseID,
			ActiveReleaseID:   drift.Active.ReleaseID,
		}); err != nil {
			return OperationResult{}, err
		}
	}
	now := time.Now().UTC()
	result, err := createClarificationChild(ctx, service.runs, activeScope, identity.DomainID, parent, input, now)
	if err != nil {
		return OperationResult{}, err
	}
	if service.questions != nil {
		label, ok := clarificationOptionLabel(parent, input.OptionID)
		if !ok {
			return OperationResult{}, ErrClarificationOption
		}
		if err := service.questions.SaveClarificationQuestion(ctx,
			orchestrator.QuestionRetentionBinding{
				Scope: scope, DomainID: identity.DomainID,
				ConversationID: parent.Run.ConversationID, RunID: parent.Run.ID,
				QuestionHash: parent.Run.QuestionHash,
			},
			orchestrator.QuestionRetentionBinding{
				Scope: activeScope, DomainID: identity.DomainID,
				ConversationID: result.Snapshot.Run.ConversationID, RunID: result.Snapshot.Run.ID,
				QuestionHash: result.Snapshot.Run.QuestionHash,
			}, label, now); err != nil {
			return OperationResult{}, err
		}
	}
	return result, nil
}

func createClarificationChild(
	ctx context.Context,
	runs questionRunStore,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	parent orchestrator.ReplaySnapshot,
	input SubmitClarificationInput,
	now time.Time,
) (OperationResult, error) {
	if parent.Run.State != orchestrator.StateClarificationRequired || parent.Run.ConversationID == "" {
		return OperationResult{}, ErrClarificationRequired
	}
	if parent.Run.RecordVersion != input.RunVersion {
		return OperationResult{}, orchestrator.ErrVersionConflict
	}
	completion := publicCompletion(parent)
	if completion == nil || completion.Clarification == nil ||
		completion.Clarification.ClarificationID != input.ClarificationID {
		return OperationResult{}, ErrClarificationRequired
	}
	if !clarificationOptionAllowed(parent, input.OptionID) {
		return OperationResult{}, ErrClarificationOption
	}
	resumedUsage, err := orchestrator.ResumeBudget(parent.Run, now)
	if err != nil {
		return OperationResult{}, err
	}
	questionHash := askdata.HashBytes([]byte(
		clarificationHashDomain + string(parent.Run.QuestionHash) + "\x00" + string(input.OptionID),
	))
	created, err := runs.CreateRun(ctx, orchestrator.CreateRunRequest{
		Scope: scope, DomainID: domainID,
		ConversationID: parent.Run.ConversationID, ParentRunID: parent.Run.ID,
		IdempotencyKeyHash: askdata.HashBytes([]byte(
			clarificationConsumeDomain + string(parent.Run.ID) + "\x00" + string(input.ClarificationID),
		)),
		QuestionHash: questionHash,
		Limits:       parent.Run.Limits, InitialUsage: resumedUsage,
	})
	if err != nil {
		if errors.Is(err, orchestrator.ErrIdempotencyConflict) {
			return OperationResult{}, ErrClarificationAnswered
		}
		return OperationResult{}, err
	}
	snapshot, err := runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: domainID, RunID: created.Run.ID,
	})
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Snapshot: snapshot, Replayed: created.Replayed}, nil
}

func clarificationOptionAllowed(snapshot orchestrator.ReplaySnapshot, optionID askdata.ID) bool {
	_, allowed := clarificationOptionLabel(snapshot, optionID)
	return allowed
}

func clarificationOptionLabel(snapshot orchestrator.ReplaySnapshot, optionID askdata.ID) (string, bool) {
	completion := publicCompletion(snapshot)
	if completion == nil || completion.Clarification == nil {
		return "", false
	}
	for _, option := range completion.Clarification.Options {
		if option.OptionID == optionID {
			return option.Label, strings.TrimSpace(option.Label) != ""
		}
	}
	return "", false
}

func (service *PostgresService) resolveActiveScope(
	ctx context.Context,
	identity RequestIdentity,
) (askdata.PolicyScope, error) {
	return service.resolveScope(ctx, identity, "", true)
}

func (service *PostgresService) resolveRunScope(
	ctx context.Context,
	identity RequestIdentity,
	runID askdata.ID,
) (askdata.PolicyScope, error) {
	return service.resolveScope(ctx, identity, runID, false)
}

func (service *PostgresService) resolveScope(
	ctx context.Context,
	identity RequestIdentity,
	runID askdata.ID,
	active bool,
) (askdata.PolicyScope, error) {
	if service == nil || service.pool == nil || identity.validate() != nil || (active == (runID != "")) {
		return askdata.PolicyScope{}, ErrInvalidRequest
	}
	var release askdata.ReleaseRef
	roleIDs := []askdata.ID{}
	runner := service.scopeRunner
	if runner == nil {
		runner = func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, service.pool, tenantID, fn)
		}
	}
	err := runner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var releaseID, releaseHash string
		var err error
		if active {
			err = tx.QueryRow(ctx, `SELECT id::text,content_hash
				FROM askdata.releases
				WHERE tenant_id=$1 AND domain_id=$2 AND status='ACTIVE'`,
				identity.TenantID, identity.DomainID,
			).Scan(&releaseID, &releaseHash)
		} else {
			err = tx.QueryRow(ctx, `SELECT release_id::text,release_content_hash
				FROM askdata.question_runs
				WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND id=$4`,
				identity.TenantID, identity.DomainID, identity.ActorID, runID,
			).Scan(&releaseID, &releaseHash)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if active {
				return ErrNoActiveRelease
			}
			return orchestrator.ErrRunNotFound
		}
		if err != nil {
			return err
		}
		release = askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
		rows, err := tx.Query(ctx, `SELECT role.id::text
			FROM platform.user_roles AS assignment
			JOIN platform.roles AS role
			  ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
			WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL
			ORDER BY role.id
			LIMIT $3`, identity.TenantID, identity.ActorID, askdata.MaxPolicyRoles+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var roleID string
			if err := rows.Scan(&roleID); err != nil {
				return err
			}
			roleIDs = append(roleIDs, askdata.ID(roleID))
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, ErrNoActiveRelease) || errors.Is(err, orchestrator.ErrRunNotFound) {
			return askdata.PolicyScope{}, err
		}
		return askdata.PolicyScope{}, fmt.Errorf("%w: resolve question scope", ErrQuestionServiceFailure)
	}
	if len(roleIDs) == 0 || len(roleIDs) > askdata.MaxPolicyRoles {
		return askdata.PolicyScope{}, orchestrator.ErrPinnedScopeMismatch
	}
	scope, err := askdata.NewPolicyScope(
		identity.TenantID, identity.ActorID, []askdata.ID{identity.DomainID}, roleIDs, release,
	)
	if err != nil {
		return askdata.PolicyScope{}, orchestrator.ErrPinnedScopeMismatch
	}
	return scope, nil
}
