package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/validator"
)

const addToReportIdempotencyDomain = "askdata-add-to-report-v1\x00"

var (
	ErrPartialResultNotExportable = errors.New("RESULT_PARTIAL_NOT_EXPORTABLE")
	ErrAddToReportNotAccepted     = errors.New("question result cannot be added to a report")
	ErrAddToReportUnavailable     = errors.New("add-to-report backend is unavailable")
)

type AddToReportInput struct {
	RunID              askdata.ID
	ReportID           askdata.ID
	TargetPageID       askdata.ID
	TargetSectionID    askdata.ID
	RunVersion         int64
	OutcomeHash        askdata.ContentHash
	IdempotencyKeyHash askdata.ContentHash
}

type AddToReportResult struct {
	IntentID    askdata.ID          `json:"intentId"`
	ReportID    askdata.ID          `json:"reportId"`
	RunID       askdata.ID          `json:"runId"`
	Status      string              `json:"status"`
	PreviewHash askdata.ContentHash `json:"previewHash,omitempty"`
	Replayed    bool                `json:"replayed"`
}

// AddToReportBackend is intentionally separate from the Question Backend.
// QUERY-011 owns the fail-closed outcome gate; the report bounded context can
// implement durable intent/outbox persistence without weakening that gate.
type AddToReportBackend interface {
	AddToReport(context.Context, RequestIdentity, AddToReportInput) (AddToReportResult, error)
}

type AddToReportConfirmationBackend interface {
	ConfirmAddToReport(context.Context, RequestIdentity, askdata.ID, askdata.ContentHash) (AddToReportResult, error)
	GetAddToReportIntent(context.Context, RequestIdentity, askdata.ID) (AddToReportResult, error)
}

func (handler *Handler) addToReport(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	key, err := requireIdempotencyKey(request)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	runID, err := parseRunID(request.PathValue("runId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		ReportID        string `json:"reportId"`
		TargetPageID    string `json:"targetPageId,omitempty"`
		TargetSectionID string `json:"targetSectionId,omitempty"`
		RunVersion      int64  `json:"runVersion"`
	}
	if err := decodeStrictJSON(writer, request, &body); err != nil {
		writeServiceError(writer, err)
		return
	}
	reportID := askdata.ID(strings.TrimSpace(body.ReportID))
	if body.ReportID != string(reportID) || !canonicalUUID(reportID) || body.RunVersion < 1 {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}

	// The outcome comes only from the actor-scoped persisted completion
	// artifact. A request body cannot downgrade or replace PARTIAL.
	snapshot, err := handler.backend.GetQuestion(request.Context(), identity, runID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	if snapshot.Run.ID != runID || snapshot.Run.State != orchestrator.StateAnswered {
		writeServiceError(writer, ErrAddToReportNotAccepted)
		return
	}
	if snapshot.Run.RecordVersion != body.RunVersion {
		writeServiceError(writer, orchestrator.ErrVersionConflict)
		return
	}
	outcome, err := completionOutcome(snapshot)
	if err != nil {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	if err := ValidateAddToReportOutcome(outcome); err != nil {
		writeServiceError(writer, err)
		return
	}

	reportBackend, ok := handler.backend.(AddToReportBackend)
	if !ok {
		writeServiceError(writer, ErrAddToReportUnavailable)
		return
	}
	result, err := reportBackend.AddToReport(request.Context(), identity, AddToReportInput{
		RunID: runID, ReportID: reportID, RunVersion: body.RunVersion,
		TargetPageID:    askdata.ID(strings.TrimSpace(body.TargetPageID)),
		TargetSectionID: askdata.ID(strings.TrimSpace(body.TargetSectionID)),
		OutcomeHash:     outcome.OutcomeHash,
		IdempotencyKeyHash: askdata.HashBytes([]byte(
			addToReportIdempotencyDomain + key,
		)),
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	if !validAddToReportResult(result, runID, reportID) {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}

func (handler *Handler) confirmAddToReport(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	intentID := askdata.ID(strings.TrimSpace(request.PathValue("intentId")))
	var body struct {
		PreviewHash askdata.ContentHash `json:"previewHash"`
	}
	if intentID.Validate() != nil || decodeStrictJSON(writer, request, &body) != nil || body.PreviewHash.Validate() != nil {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	backend, ok := handler.backend.(AddToReportConfirmationBackend)
	if !ok {
		writeServiceError(writer, ErrAddToReportUnavailable)
		return
	}
	result, err := backend.ConfirmAddToReport(request.Context(), identity, intentID, body.PreviewHash)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (handler *Handler) getAddToReportIntent(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	intentID := askdata.ID(strings.TrimSpace(request.PathValue("intentId")))
	if intentID.Validate() != nil {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	backend, ok := handler.backend.(AddToReportConfirmationBackend)
	if !ok {
		writeServiceError(writer, ErrAddToReportUnavailable)
		return
	}
	result, err := backend.GetAddToReportIntent(request.Context(), identity, intentID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

// ValidateAddToReportOutcome is the shared report-ingress guard. Non-partial
// quality warnings are exportable; every P1-P6 marker is not.
func ValidateAddToReportOutcome(outcome validator.Outcome) error {
	if outcome.Validate() != nil {
		return ErrAddToReportNotAccepted
	}
	if outcome.Status == validator.OutcomePartial {
		return ErrPartialResultNotExportable
	}
	return nil
}

func completionOutcome(snapshot orchestrator.ReplaySnapshot) (validator.Outcome, error) {
	if snapshot.Run.State != orchestrator.StateAnswered ||
		snapshot.Run.CompletionArtifact.Validate() != nil {
		return validator.Outcome{}, ErrAddToReportNotAccepted
	}
	var matched *validator.Outcome
	for _, artifact := range snapshot.Artifacts {
		if artifact.Hash != snapshot.Run.CompletionArtifact || artifact.Type != orchestrator.ArtifactAnswer {
			continue
		}
		outcome := parsePublicOutcome(artifact.Payload)
		if outcome == nil || matched != nil {
			return validator.Outcome{}, ErrAddToReportNotAccepted
		}
		matched = outcome
	}
	if matched == nil {
		return validator.Outcome{}, ErrAddToReportNotAccepted
	}
	return *matched, nil
}

func parsePublicOutcome(payload json.RawMessage) *validator.Outcome {
	var envelope struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if json.Unmarshal(payload, &envelope) != nil || len(envelope.Outcome) == 0 ||
		string(envelope.Outcome) == "null" {
		return nil
	}
	var outcome validator.Outcome
	if askdata.DecodeStrictJSON(envelope.Outcome, &outcome) != nil || outcome.Validate() != nil {
		return nil
	}
	return &outcome
}

func validAddToReportResult(result AddToReportResult, runID, reportID askdata.ID) bool {
	return canonicalUUID(result.IntentID) && result.RunID == runID && result.ReportID == reportID &&
		(result.Status == "PENDING_CONFIRMATION" || result.Status == "PENDING" || result.Status == "APPLIED")
}
