package askdatahttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

const (
	maxPublicEventBytes = 16 << 10
	maxEventCursor      = 1_000_000
)

type streamOptions struct {
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	retryMilliseconds int
}

func defaultStreamOptions() streamOptions {
	return streamOptions{
		pollInterval: 250 * time.Millisecond, heartbeatInterval: 15 * time.Second,
		retryMilliseconds: 1000,
	}
}

func (options streamOptions) validate() bool {
	return options.pollInterval > 0 && options.heartbeatInterval > 0 &&
		options.retryMilliseconds >= 100 && options.retryMilliseconds <= 60_000
}

type PublicEvent struct {
	EventID      askdata.ID               `json:"eventId"`
	EventIndex   int                      `json:"eventIndex"`
	RunVersion   int64                    `json:"runVersion"`
	State        orchestrator.State       `json:"state"`
	Type         orchestrator.EventType   `json:"type"`
	Stage        string                   `json:"stage,omitempty"`
	Status       orchestrator.EventStatus `json:"status"`
	Code         string                   `json:"code,omitempty"`
	ActionHash   askdata.ContentHash      `json:"actionHash,omitempty"`
	ArtifactHash askdata.ContentHash      `json:"artifactHash,omitempty"`
	EvidenceIDs  []askdata.ID             `json:"evidenceIds"`
	DurationMS   *int64                   `json:"durationMs,omitempty"`
	CreatedAt    time.Time                `json:"createdAt"`
}

func newPublicEvent(event orchestrator.Event) PublicEvent {
	return PublicEvent{
		EventID: event.ID, EventIndex: event.Index, RunVersion: event.RunVersion,
		State: event.State, Type: event.Type, Stage: event.Stage, Status: event.Status,
		Code: event.Code, ActionHash: event.ActionHash, ArtifactHash: event.ArtifactHash,
		EvidenceIDs: append([]askdata.ID(nil), event.EvidenceIDs...),
		DurationMS:  event.DurationMS, CreatedAt: event.CreatedAt,
	}
}

func (handler *Handler) streamEvents(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "event stream does not accept query parameters")
		return
	}
	if !handler.stream.validate() {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}
	runID, err := parseRunID(request.PathValue("runId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	cursor, err := parseLastEventID(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "QUESTION_EVENT_CURSOR_INVALID", "Last-Event-ID must be a valid event index")
		return
	}
	snapshot, err := handler.backend.GetQuestion(request.Context(), identity, runID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	if cursor > lastEventID(snapshot.Events) {
		writeError(writer, http.StatusConflict, "QUESTION_EVENT_CURSOR_AHEAD", "Last-Event-ID is ahead of the durable event stream")
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeServiceError(writer, ErrQuestionServiceFailure)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(writer, "retry: %d\n\n", handler.stream.retryMilliseconds)
	flusher.Flush()

	cursor, ok = writeSnapshotEvents(writer, flusher, snapshot, cursor)
	if !ok || snapshot.Run.Terminal() {
		return
	}
	poll := time.NewTicker(handler.stream.pollInterval)
	heartbeat := time.NewTicker(handler.stream.heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			next, err := handler.backend.GetQuestion(request.Context(), identity, runID)
			if err != nil {
				writeStreamError(writer, flusher, "QUESTION_STREAM_REFRESH_FAILED")
				return
			}
			if lastEventID(next.Events) < cursor {
				writeStreamError(writer, flusher, "QUESTION_STREAM_REPLAY_INVALID")
				return
			}
			cursor, ok = writeSnapshotEvents(writer, flusher, next, cursor)
			if !ok || next.Run.Terminal() {
				return
			}
		}
	}
}

func parseLastEventID(request *http.Request) (int, error) {
	values := request.Header.Values("Last-Event-ID")
	if len(values) == 0 {
		return 0, nil
	}
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return 0, ErrInvalidRequest
	}
	for _, character := range values[0] {
		if character < '0' || character > '9' {
			return 0, ErrInvalidRequest
		}
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < 0 || value > maxEventCursor {
		return 0, ErrInvalidRequest
	}
	return value, nil
}

func writeSnapshotEvents(
	writer http.ResponseWriter,
	flusher http.Flusher,
	snapshot orchestrator.ReplaySnapshot,
	cursor int,
) (int, bool) {
	for _, event := range snapshot.Events {
		if event.Index <= cursor {
			continue
		}
		if event.Index != cursor+1 {
			writeStreamError(writer, flusher, "QUESTION_STREAM_EVENT_GAP")
			return cursor, false
		}
		payload, err := json.Marshal(newPublicEvent(event))
		if err != nil || len(payload) > maxPublicEventBytes {
			writeStreamError(writer, flusher, "QUESTION_EVENT_PAYLOAD_REJECTED")
			return cursor, false
		}
		if _, err := fmt.Fprintf(
			writer, "id: %d\nevent: question.run\ndata: %s\n\n", event.Index, payload,
		); err != nil {
			return cursor, false
		}
		cursor = event.Index
		flusher.Flush()
	}
	return cursor, true
}

func writeStreamError(writer http.ResponseWriter, flusher http.Flusher, code string) {
	payload, _ := json.Marshal(map[string]string{"code": code})
	if len(payload) > maxPublicEventBytes {
		return
	}
	_, _ = fmt.Fprintf(writer, "event: question.error\ndata: %s\n\n", payload)
	flusher.Flush()
}
