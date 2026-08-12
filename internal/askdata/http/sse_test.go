package askdatahttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

func TestEventStreamResumesAfterLastEventIDWithoutLeakingAuditDetails(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateAnswered, 2)
	snapshot.Events[0].Details = json.RawMessage(`{"prompt":"FIRST SECRET"}`)
	snapshot.Events[1].Details = json.RawMessage(`{"sqlText":"SELECT SECOND SECRET","resultRows":[1]}`)
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodGet, "/api/v1/questions/"+string(snapshot.Run.ID)+"/events", nil,
	)
	request.Header.Set("Last-Event-ID", "1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream response = %d, headers=%v", response.Code, response.Header())
	}
	body := response.Body.String()
	if !strings.Contains(body, "id: 2\n") || strings.Contains(body, "id: 1\n") {
		t.Fatalf("resume cursor was not enforced: %s", body)
	}
	for _, secret := range []string{"FIRST SECRET", "SELECT SECOND SECRET", "resultRows", "sqlText", "prompt"} {
		if strings.Contains(body, secret) {
			t.Fatalf("stream leaked %q: %s", secret, body)
		}
	}
	data := eventDataLine(t, body, "question.run")
	if len(data) > maxPublicEventBytes {
		t.Fatalf("public event size = %d", len(data))
	}
	var event PublicEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil || event.EventIndex != 2 {
		t.Fatalf("public event = %#v, %v", event, err)
	}
}

func TestPublicGraphToolEventExposesOnlyDegradationFlag(t *testing.T) {
	event := orchestrator.Event{
		Type:    orchestrator.EventToolResult,
		Details: json.RawMessage(`{"tool":"resolve_graph_plan","graphDegraded":true,"sqlText":"SECRET"}`),
	}
	public := newPublicEvent(event)
	if !public.GraphDegraded {
		t.Fatal("graphDegraded was not exposed on the public event")
	}
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "sqlText") {
		t.Fatalf("public event leaked private audit details: %s", raw)
	}

	event.Details = json.RawMessage(`{"tool":"execute_query_plan","graphDegraded":true}`)
	if newPublicEvent(event).GraphDegraded {
		t.Fatal("non-graph tool forged graphDegraded")
	}
}

func TestPublicEventAlwaysEmitsEvidenceIDsAsAnArray(t *testing.T) {
	raw, err := json.Marshal(newPublicEvent(orchestrator.Event{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"evidenceIds":[]`) {
		t.Fatalf("empty evidence IDs must remain a JSON array: %s", raw)
	}
}

func TestEventStreamPollsUntilTerminalAndDeduplicatesSnapshots(t *testing.T) {
	identity := testIdentity()
	initial := testSnapshot(orchestrator.StateUnderstanding, 1)
	terminal := initial
	terminal.Run.State = orchestrator.StateBlocked
	terminal.Events = append([]orchestrator.Event(nil), initial.Events...)
	terminal.Events = append(terminal.Events, orchestrator.Event{
		ID: askdata.ID(uuid.NewString()), Index: 2, RunVersion: 2,
		State: orchestrator.StateBlocked, Type: orchestrator.EventStateTransition,
		Status: orchestrator.EventBlocked, Code: "POLICY_BLOCK", CreatedAt: time.Now().UTC(),
	})
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{initial, initial, terminal}}
	handler := newProtectedHandler(
		backend,
		func(context.Context) (RequestIdentity, error) { return identity, nil },
		streamOptions{pollInterval: time.Millisecond, heartbeatInterval: time.Second, retryMilliseconds: 100},
	)
	request := httptest.NewRequest(
		http.MethodGet, "/api/v1/questions/"+string(initial.Run.ID)+"/events", nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if strings.Count(body, "id: 1\n") != 1 || strings.Count(body, "id: 2\n") != 1 ||
		strings.Count(body, "event: question.run\n") != 2 || backend.getCalls < 3 {
		t.Fatalf("poll stream = %s, calls=%d", body, backend.getCalls)
	}
}

func TestEventStreamRejectsInvalidOrAheadCursorBeforeSSEHeaders(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateReceived, 1)
	for _, test := range []struct {
		cursor string
		code   int
	}{
		{cursor: "2", code: http.StatusConflict},
		{cursor: "-1", code: http.StatusBadRequest},
		{cursor: " 1", code: http.StatusBadRequest},
		{cursor: "1000001", code: http.StatusBadRequest},
	} {
		backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
		handler := testHandler(backend, identity)
		request := httptest.NewRequest(
			http.MethodGet, "/api/v1/questions/"+string(snapshot.Run.ID)+"/events", nil,
		)
		request.Header.Set("Last-Event-ID", test.cursor)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.code || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("cursor %q response = %d/%v/%s", test.cursor, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestEventStreamFailsClosedWhenPublicProjectionExceedsBound(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateAnswered, 1)
	snapshot.Events[0].EvidenceIDs = make([]askdata.ID, 300)
	for index := range snapshot.Events[0].EvidenceIDs {
		snapshot.Events[0].EvidenceIDs[index] = askdata.ID(strings.Repeat("e", 120) + string(rune('A'+index%26)))
	}
	backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
	handler := testHandler(backend, identity)
	request := httptest.NewRequest(
		http.MethodGet, "/api/v1/questions/"+string(snapshot.Run.ID)+"/events", nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if !strings.Contains(body, "QUESTION_EVENT_PAYLOAD_REJECTED") || strings.Contains(body, strings.Repeat("e", 120)) {
		t.Fatalf("oversized projection did not fail closed: %s", body)
	}
}

func TestAnswerVerificationEventSequenceResumesFromCursor(t *testing.T) {
	identity := testIdentity()
	snapshot := testSnapshot(orchestrator.StateAnswered, 4)
	snapshot.Events[0].State = orchestrator.StateResultVerifying
	snapshot.Events[0].Code = "RESULT_VERIFIED"
	snapshot.Events[1].State = orchestrator.StateAnswerVerifying
	snapshot.Events[1].Status = orchestrator.EventStarted
	snapshot.Events[1].Code = "ANSWER_VERIFYING"
	snapshot.Events[2].State = orchestrator.StateAnswerVerifying
	snapshot.Events[2].Status = orchestrator.EventFailed
	snapshot.Events[2].Code = "ANSWER_VERIFICATION_FAILED"
	snapshot.Events[3].State = orchestrator.StateAnswered
	snapshot.Events[3].Status = orchestrator.EventSucceeded
	snapshot.Events[3].Code = "ANSWER_DEGRADED"

	for _, test := range []struct {
		cursor string
		want   []string
		absent []string
	}{
		{
			cursor: "1",
			want: []string{
				"id: 2\nevent: answer.verifying\n",
				"id: 3\nevent: answer.verifying\n",
				"id: 4\nevent: answer.degraded\n",
			},
			absent: []string{"id: 1\n"},
		},
		{
			cursor: "2",
			want: []string{
				"id: 3\nevent: answer.verifying\n",
				"id: 4\nevent: answer.degraded\n",
			},
			absent: []string{"id: 1\n", "id: 2\n"},
		},
	} {
		t.Run("after event "+test.cursor, func(t *testing.T) {
			backend := &fakeBackend{getSnapshots: []orchestrator.ReplaySnapshot{snapshot}}
			handler := testHandler(backend, identity)
			request := httptest.NewRequest(
				http.MethodGet, "/api/v1/questions/"+string(snapshot.Run.ID)+"/events", nil,
			)
			request.Header.Set("Last-Event-ID", test.cursor)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("stream response = %d: %s", response.Code, response.Body.String())
			}
			body, previous := response.Body.String(), -1
			for _, marker := range test.want {
				position := strings.Index(body, marker)
				if position <= previous {
					t.Fatalf("event sequence is not ordered at %q: %s", marker, body)
				}
				previous = position
			}
			for _, marker := range test.absent {
				if strings.Contains(body, marker) {
					t.Fatalf("resumed stream replayed %q: %s", marker, body)
				}
			}
		})
	}
}

func eventDataLine(t *testing.T, body, eventName string) string {
	t.Helper()
	blocks := strings.Split(body, "\n\n")
	for _, block := range blocks {
		if !strings.Contains(block, "event: "+eventName+"\n") {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				return strings.TrimPrefix(line, "data: ")
			}
		}
	}
	t.Fatalf("event %q not found in %s", eventName, body)
	return ""
}
