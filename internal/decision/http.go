package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

const maxDecisionRequestBytes = 2 << 20

type Handler struct{ service *Service }

// NewHandler exposes the decision bounded context behind the standard domain
// authentication and actor-scoped idempotency boundaries.
func NewHandler(authService *auth.Service, idempotencyRepository platformidempotency.Repository, service *Service) http.Handler {
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/decisions", handler.list)
	mux.HandleFunc("POST /api/v1/decisions", handler.create)
	mux.HandleFunc("GET /api/v1/decisions/approval-policies", handler.approvalPolicies)
	mux.HandleFunc("GET /api/v1/decisions/evidence-prefill", handler.evidencePrefill)
	mux.HandleFunc("GET /api/v1/decisions/{id}", handler.get)
	mux.HandleFunc("PUT /api/v1/decisions/{id}", handler.update)
	mux.HandleFunc("POST /api/v1/decisions/{id}/submit", handler.submit)
	mux.HandleFunc("POST /api/v1/decisions/{id}/approvals", handler.approve)
	mux.HandleFunc("POST /api/v1/decisions/{id}/actions", handler.createAction)
	mux.HandleFunc("POST /api/v1/decisions/{id}/actions/{actionId}/transition", handler.transitionAction)
	mux.HandleFunc("POST /api/v1/decisions/{id}/outcome/metrics", handler.addMetric)
	mux.HandleFunc("POST /api/v1/decisions/{id}/outcome/start", handler.startReview)
	mux.HandleFunc("POST /api/v1/decisions/{id}/outcome/refresh", handler.refreshOutcome)
	mux.HandleFunc("POST /api/v1/decisions/{id}/outcome/confirm", handler.confirmOutcome)
	mux.HandleFunc("POST /api/v1/decisions/{id}/close", handler.closeDecision)
	mux.HandleFunc("POST /api/v1/decisions/{id}/reopen", handler.reopenDecision)
	mux.HandleFunc("POST /api/v1/decisions/{id}/cancel", handler.cancelDecision)
	mux.HandleFunc("GET /api/v1/decisions/{id}/events", handler.events)
	governed := platformidempotency.Middleware(platformidempotency.MiddlewareOptions{
		Repository: idempotencyRepository,
		ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
			identity, err := decisionIdentity(ctx)
			return platformidempotency.Identity{TenantID: string(identity.TenantID), ActorID: string(identity.ActorID)}, err
		},
		Requires:        platformidempotency.RequiresGovernedWrite,
		WriteError:      writeDecisionError,
		MaxRequestBytes: maxDecisionRequestBytes,
	}, mux)
	return auth.RequireAccessToken(authService, noStore(governed))
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "limit is invalid")
			return
		}
		limit = value
	}
	parseTime := func(name string) (*time.Time, error) {
		raw := request.URL.Query().Get(name)
		if raw == "" {
			return nil, nil
		}
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	reviewFrom, err := parseTime("reviewFrom")
	if err != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "reviewFrom is invalid")
		return
	}
	reviewTo, err := parseTime("reviewTo")
	if err != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "reviewTo is invalid")
		return
	}
	owner := askdata.ID(request.URL.Query().Get("owner"))
	if strings.EqualFold(string(owner), "ME") {
		owner = identity.ActorID
	}
	page, err := handler.service.ListDetailed(request.Context(), identity, ListQuery{
		Scope: request.URL.Query().Get("scope"), Search: request.URL.Query().Get("q"),
		Status:       Status(strings.ToUpper(request.URL.Query().Get("status"))),
		EvidenceMode: EvidenceMode(strings.ToUpper(request.URL.Query().Get("evidenceMode"))),
		DecisionType: request.URL.Query().Get("decisionType"), ReviewFrom: reviewFrom, ReviewTo: reviewTo,
		Owner: owner, Sort: request.URL.Query().Get("sort"), Limit: limit, Cursor: request.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, page)
}

func (handler *Handler) approvalPolicies(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	items, err := handler.service.ListApprovalPolicies(request.Context(), identity)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) evidencePrefill(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.PrefillEvidence(request.Context(), identity,
		SourceType(strings.ToUpper(request.URL.Query().Get("sourceType"))), askdata.ID(request.URL.Query().Get("sourceId")))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, map[string]any{"evidence": value})
}

func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return
	}
	var input CreateInput
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.Create(request.Context(), identity, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(request.Context(), identity, id, false)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) update(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var input UpdateInput
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.Update(request.Context(), identity, id, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) submit(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expectedVersion"`
	}
	if decodeDecisionJSON(request, &body) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.Submit(request.Context(), identity, id, body.ExpectedVersion)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) approve(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expectedVersion"`
		Decision        string `json:"decision"`
		Comment         string `json:"comment"`
	}
	if decodeDecisionJSON(request, &body) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	approved := strings.EqualFold(body.Decision, "APPROVE")
	if !approved && !strings.EqualFold(body.Decision, "REJECT") {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "approval decision is invalid")
		return
	}
	value, err := handler.service.DecideApproval(request.Context(), identity, id, body.ExpectedVersion, approved, body.Comment)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) createAction(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var input CreateActionInput
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.CreateAction(request.Context(), identity, id, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) transitionAction(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	actionID := askdata.ID(request.PathValue("actionId"))
	var input TransitionActionInput
	if actionID.Validate() != nil || decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request is invalid")
		return
	}
	value, err := handler.service.TransitionAction(request.Context(), identity, id, actionID, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) addMetric(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var input AddMetricInput
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.AddOutcomeMetric(request.Context(), identity, id, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusCreated, value)
}

func (handler *Handler) startReview(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expectedVersion"`
	}
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.StartReview(request.Context(), identity, id, input.ExpectedVersion)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) refreshOutcome(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	if decodeEmptyDecisionJSON(request) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.RefreshOutcome(request.Context(), identity, id)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, map[string]any{"items": value})
}

func (handler *Handler) confirmOutcome(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var input ConfirmOutcomeInput
	if decodeDecisionJSON(request, &input) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	value, err := handler.service.ConfirmOutcome(request.Context(), identity, id, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) closeDecision(writer http.ResponseWriter, request *http.Request) {
	handler.terminal(writer, request, "CLOSE")
}
func (handler *Handler) reopenDecision(writer http.ResponseWriter, request *http.Request) {
	handler.terminal(writer, request, "REOPEN")
}
func (handler *Handler) cancelDecision(writer http.ResponseWriter, request *http.Request) {
	handler.terminal(writer, request, "CANCEL")
}

func (handler *Handler) terminal(writer http.ResponseWriter, request *http.Request, operation string) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expectedVersion"`
		Reason          string `json:"reason"`
	}
	if decodeDecisionJSON(request, &body) != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "request body is invalid")
		return
	}
	var value Aggregate
	var err error
	switch operation {
	case "CLOSE":
		value, err = handler.service.Close(request.Context(), identity, id, body.ExpectedVersion, body.Reason)
	case "REOPEN":
		value, err = handler.service.Reopen(request.Context(), identity, id, body.ExpectedVersion, body.Reason)
	default:
		value, err = handler.service.Cancel(request.Context(), identity, id, body.ExpectedVersion, body.Reason)
	}
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, value)
}

func (handler *Handler) events(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := handler.subject(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Get(request.Context(), identity, id, true)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeDecisionJSON(writer, http.StatusOK, map[string]any{"items": value.Events})
}

func (handler *Handler) identity(writer http.ResponseWriter, request *http.Request) (Identity, bool) {
	if handler == nil || handler.service == nil {
		writeDecisionError(writer, http.StatusServiceUnavailable, "DECISION_UNAVAILABLE", "decision service is unavailable")
		return Identity{}, false
	}
	identity, err := decisionIdentity(request.Context())
	if err != nil {
		writeDecisionError(writer, http.StatusForbidden, "DECISION_FORBIDDEN", "decision access is forbidden")
		return Identity{}, false
	}
	return identity, true
}

func (handler *Handler) subject(writer http.ResponseWriter, request *http.Request) (Identity, askdata.ID, bool) {
	identity, ok := handler.identity(writer, request)
	if !ok {
		return Identity{}, "", false
	}
	id := askdata.ID(request.PathValue("id"))
	if id.Validate() != nil {
		writeDecisionError(writer, http.StatusBadRequest, "DECISION_REQUEST_INVALID", "decision ID is invalid")
		return Identity{}, "", false
	}
	return identity, id, true
}

func decisionIdentity(ctx context.Context) (Identity, error) {
	claims, claimsOK := auth.ClaimsFromContext(ctx)
	access, accessOK := database.AccessContextFromContext(ctx)
	if !claimsOK || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		return Identity{}, ErrForbidden
	}
	identity := Identity{TenantID: askdata.ID(claims.TenantID), DomainID: askdata.ID(access.DomainID), ActorID: askdata.ID(claims.Subject)}
	return identity, identity.Validate()
}

func decodeDecisionJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxDecisionRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func decodeEmptyDecisionJSON(request *http.Request) error {
	if request.Body == nil || request.ContentLength == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	if err := decodeDecisionJSON(request, &body); err != nil || len(body) != 0 {
		return ErrInvalid
	}
	return nil
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func writeDecisionJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
func writeDecisionError(writer http.ResponseWriter, status int, code, message string) {
	writeDecisionJSON(writer, status, map[string]string{"code": code, "message": message})
}
func writeServiceError(writer http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "DECISION_INTERNAL", "decision operation failed"
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrEvidenceInvalid):
		status, code, message = http.StatusBadRequest, "DECISION_REQUEST_INVALID", err.Error()
	case errors.Is(err, ErrNotFound):
		status, code, message = http.StatusNotFound, "DECISION_NOT_FOUND", err.Error()
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrPolicyUnavailable):
		status, code, message = http.StatusForbidden, "DECISION_FORBIDDEN", err.Error()
	case errors.Is(err, ErrSelfApproval):
		status, code, message = http.StatusConflict, "DECISION_SELF_APPROVAL_FORBIDDEN", err.Error()
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIllegalTransition), errors.Is(err, ErrOutcomeBlocked):
		status, code, message = http.StatusConflict, "DECISION_CONFLICT", err.Error()
	}
	writeDecisionError(writer, status, code, message)
}
