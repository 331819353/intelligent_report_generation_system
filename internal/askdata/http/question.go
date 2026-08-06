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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	maxQuestionBodyBytes      = 16 << 10
	maxQuestionRunes          = 4096
	maxIdempotencyKeyBytes    = 256
	questionHashDomain        = "askdata-question-v1\x00"
	questionIdempotencyDomain = "askdata-question-create-v1\x00"
	clarificationHashDomain   = "askdata-clarification-v1\x00"
)

var (
	ErrInvalidRequest         = errors.New("invalid question request")
	ErrUnauthenticated        = errors.New("question request is unauthenticated")
	ErrNoActiveRelease        = errors.New("question domain has no active semantic release")
	ErrClarificationRequired  = errors.New("question run does not accept a clarification")
	ErrClarificationOption    = errors.New("clarification option is not available")
	ErrQuestionServiceFailure = errors.New("question service failed")
	publicCodePattern         = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
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
	QuestionHash       askdata.ContentHash
	IdempotencyKeyHash askdata.ContentHash
	ConversationID     askdata.ID
}

type SubmitClarificationInput struct {
	RunID              askdata.ID
	OptionID           askdata.ID
	IdempotencyKeyHash askdata.ContentHash
}

type OperationResult struct {
	Snapshot orchestrator.ReplaySnapshot
	Replayed bool
}

// Backend keeps HTTP concerns separate from the actor-scoped durable store.
// Inputs contain hashes and stable IDs only; the raw question never crosses
// this boundary.
type Backend interface {
	CreateQuestion(context.Context, RequestIdentity, CreateQuestionInput) (OperationResult, error)
	GetQuestion(context.Context, RequestIdentity, askdata.ID) (orchestrator.ReplaySnapshot, error)
	SubmitClarification(context.Context, RequestIdentity, SubmitClarificationInput) (OperationResult, error)
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
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	})
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
		Question       string `json:"question"`
		ConversationID string `json:"conversationId"`
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
		QuestionHash:       askdata.HashBytes([]byte(questionHashDomain + question)),
		IdempotencyKeyHash: idempotencyHash,
		ConversationID:     conversationID,
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
	key, err := requireIdempotencyKey(request)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		OptionID string `json:"optionId"`
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
	result, err := handler.backend.SubmitClarification(request.Context(), identity, SubmitClarificationInput{
		RunID: runID, OptionID: optionID,
		IdempotencyKeyHash: askdata.HashBytes([]byte(
			clarificationHashDomain + string(runID) + "\x00" + key,
		)),
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
}

type RunBudgetView struct {
	Limits orchestrator.BudgetLimits `json:"limits"`
	Usage  orchestrator.BudgetUsage  `json:"usage"`
}

type CompletionView struct {
	Code          string                    `json:"code"`
	ArtifactType  orchestrator.ArtifactType `json:"artifactType"`
	ArtifactHash  askdata.ContentHash       `json:"artifactHash"`
	EvidenceIDs   []askdata.ID              `json:"evidenceIds"`
	Clarification *ClarificationView        `json:"clarification,omitempty"`
}

type ClarificationView struct {
	ConflictCode string                    `json:"conflictCode,omitempty"`
	Message      string                    `json:"message,omitempty"`
	Options      []ClarificationOptionView `json:"options"`
}

type ClarificationOptionView struct {
	OptionID askdata.ID `json:"optionId"`
	Label    string     `json:"label"`
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
	return view
}

func publicCompletion(snapshot orchestrator.ReplaySnapshot) *CompletionView {
	run := snapshot.Run
	for _, artifact := range snapshot.Artifacts {
		if artifact.Hash != run.CompletionArtifact {
			continue
		}
		view := &CompletionView{
			Code: run.CompletionCode, ArtifactType: artifact.Type,
			ArtifactHash: artifact.Hash,
			EvidenceIDs:  append([]askdata.ID(nil), artifact.EvidenceIDs...),
		}
		if artifact.Type == orchestrator.ArtifactClarification {
			view.Clarification = parsePublicClarification(artifact.Payload)
		}
		return view
	}
	return nil
}

type clarificationPayload struct {
	ConflictCode          string                       `json:"conflictCode"`
	ClarificationQuestion string                       `json:"clarificationQuestion"`
	Options               []clarificationPayloadOption `json:"options"`
	ClarificationOptions  []clarificationPayloadOption `json:"clarificationOptions"`
	Retryable             bool                         `json:"retryable"`
}

type clarificationPayloadOption struct {
	OptionID askdata.ID `json:"optionId"`
	Label    string     `json:"label"`
}

func parsePublicClarification(payload json.RawMessage) *ClarificationView {
	var value clarificationPayload
	if json.Unmarshal(payload, &value) != nil {
		return nil
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
		Message: boundedPublicText(value.ClarificationQuestion, 512),
		Options: make([]ClarificationOptionView, 0, len(options)),
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
		result.Options = append(result.Options, ClarificationOptionView{OptionID: option.OptionID, Label: label})
	}
	return result
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
	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, orchestrator.ErrInvalidRun):
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "question request is invalid")
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, orchestrator.ErrInvalidAccessContext):
		writeError(writer, http.StatusUnauthorized, "QUESTION_AUTHENTICATION_REQUIRED", "valid question access is required")
	case errors.Is(err, orchestrator.ErrRunNotFound):
		writeError(writer, http.StatusNotFound, "QUESTION_RUN_NOT_FOUND", "question run was not found")
	case errors.Is(err, ErrNoActiveRelease):
		writeError(writer, http.StatusConflict, "QUESTION_RELEASE_UNAVAILABLE", "no active semantic release is available")
	case errors.Is(err, ErrClarificationRequired):
		writeError(writer, http.StatusConflict, "QUESTION_CLARIFICATION_NOT_ACCEPTED", "question run does not accept clarification")
	case errors.Is(err, ErrClarificationOption):
		writeError(writer, http.StatusConflict, "QUESTION_CLARIFICATION_OPTION_INVALID", "clarification option is no longer available")
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
	scopeRunner scopeTransactionRunner
}

type scopeTransactionRunner func(context.Context, string, func(pgx.Tx) error) error

type questionRunStore interface {
	CreateRun(context.Context, orchestrator.CreateRunRequest) (orchestrator.CreateResult, error)
	Resume(context.Context, orchestrator.ResumeRequest) (orchestrator.ReplaySnapshot, error)
}

func NewPostgresService(pool *pgxpool.Pool) *PostgresService {
	return &PostgresService{
		pool: pool, runs: orchestrator.NewPostgresStore(pool),
		scopeRunner: func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, pool, tenantID, fn)
		},
	}
}

func (service *PostgresService) CreateQuestion(
	ctx context.Context,
	identity RequestIdentity,
	input CreateQuestionInput,
) (OperationResult, error) {
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil ||
		input.QuestionHash.Validate() != nil || input.IdempotencyKeyHash.Validate() != nil ||
		!canonicalUUID(input.ConversationID) {
		return OperationResult{}, ErrInvalidRequest
	}
	scope, err := service.resolveActiveScope(ctx, identity)
	if err != nil {
		return OperationResult{}, err
	}
	created, err := service.runs.CreateRun(ctx, orchestrator.CreateRunRequest{
		Scope: scope, DomainID: identity.DomainID, ConversationID: input.ConversationID,
		IdempotencyKeyHash: input.IdempotencyKeyHash, QuestionHash: input.QuestionHash,
	})
	if err != nil {
		return OperationResult{}, err
	}
	snapshot, err := service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: created.Run.ID,
	})
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Snapshot: snapshot, Replayed: created.Replayed}, nil
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
	return service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: runID,
	})
}

func (service *PostgresService) SubmitClarification(
	ctx context.Context,
	identity RequestIdentity,
	input SubmitClarificationInput,
) (OperationResult, error) {
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil ||
		!canonicalUUID(input.RunID) || input.OptionID.Validate() != nil || input.IdempotencyKeyHash.Validate() != nil {
		return OperationResult{}, ErrInvalidRequest
	}
	scope, err := service.resolveRunScope(ctx, identity, input.RunID)
	if err != nil {
		return OperationResult{}, err
	}
	parent, err := service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: input.RunID,
	})
	if err != nil {
		return OperationResult{}, err
	}
	if parent.Run.State != orchestrator.StateClarificationRequired || parent.Run.ConversationID == "" {
		return OperationResult{}, ErrClarificationRequired
	}
	if !clarificationOptionAllowed(parent, input.OptionID) {
		return OperationResult{}, ErrClarificationOption
	}
	questionHash := askdata.HashBytes([]byte(
		clarificationHashDomain + string(parent.Run.QuestionHash) + "\x00" + string(input.OptionID),
	))
	created, err := service.runs.CreateRun(ctx, orchestrator.CreateRunRequest{
		Scope: scope, DomainID: identity.DomainID,
		ConversationID: parent.Run.ConversationID, ParentRunID: parent.Run.ID,
		IdempotencyKeyHash: input.IdempotencyKeyHash, QuestionHash: questionHash,
	})
	if err != nil {
		return OperationResult{}, err
	}
	snapshot, err := service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: created.Run.ID,
	})
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Snapshot: snapshot, Replayed: created.Replayed}, nil
}

func clarificationOptionAllowed(snapshot orchestrator.ReplaySnapshot, optionID askdata.ID) bool {
	completion := publicCompletion(snapshot)
	if completion == nil || completion.Clarification == nil {
		return false
	}
	for _, option := range completion.Clarification.Options {
		if option.OptionID == optionID {
			return true
		}
	}
	return false
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
