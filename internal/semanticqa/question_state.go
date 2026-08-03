package semanticqa

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type QuestionState string

const (
	QuestionStateReceived              QuestionState = "RECEIVED"
	QuestionStateAuthorized            QuestionState = "AUTHORIZED"
	QuestionStateContextReady          QuestionState = "CONTEXT_READY"
	QuestionStateValidating            QuestionState = "VALIDATING"
	QuestionStatePlanReady             QuestionState = "PLAN_READY"
	QuestionStateClarificationRequired QuestionState = "CLARIFICATION_REQUIRED"
	QuestionStateCostApproved          QuestionState = "COST_APPROVED"
	QuestionStateExecuting             QuestionState = "EXECUTING"
	QuestionStateResultVerified        QuestionState = "RESULT_VERIFIED"
	QuestionStateAnswered              QuestionState = "ANSWERED"
	QuestionStateBlocked               QuestionState = "BLOCKED"
)

type QuestionStateEvent struct {
	State      QuestionState  `json:"state"`
	Timestamp  string         `json:"timestamp"`
	Stage      string         `json:"stage,omitempty"`
	Status     string         `json:"status,omitempty"`
	Code       string         `json:"code,omitempty"`
	DurationMS *int64         `json:"durationMs,omitempty"`
	Summary    map[string]any `json:"summary,omitempty"`
}

type questionStateMachine struct {
	runID     string
	state     QuestionState
	events    []QuestionStateEvent
	now       func() time.Time
	onAdvance func(QuestionState, QuestionStateEvent) error
}

func newQuestionStateMachine(runID string) *questionStateMachine {
	if uuid.Validate(runID) != nil {
		runID = uuid.NewString()
	}
	return &questionStateMachine{
		runID: runID, events: []QuestionStateEvent{}, now: time.Now,
	}
}

func (machine *questionStateMachine) advance(next QuestionState) error {
	if machine == nil || !questionStateTransitionAllowed(machine.state, next) {
		return ErrInvalidState
	}
	event := QuestionStateEvent{
		State: next, Timestamp: machine.now().UTC().Format(time.RFC3339Nano),
		Stage: string(next), Status: questionStateEventStatus(next),
	}
	if machine.onAdvance != nil {
		if err := machine.onAdvance(machine.state, event); err != nil {
			return err
		}
	}
	machine.state = next
	machine.events = append(machine.events, event)
	return nil
}

func questionStateEventStatus(state QuestionState) string {
	switch state {
	case QuestionStateBlocked:
		return "BLOCKED"
	case QuestionStateClarificationRequired:
		return "WAITING_USER"
	default:
		return QueryProgressStatusSucceeded
	}
}

func questionEventSummaryJSON(event QuestionStateEvent) []byte {
	if len(event.Summary) == 0 {
		return []byte(`{}`)
	}
	value, err := json.Marshal(event.Summary)
	if err != nil {
		return []byte(`{}`)
	}
	return value
}

func (machine *questionStateMachine) lifecycle() []QuestionStateEvent {
	if machine == nil {
		return []QuestionStateEvent{}
	}
	return append([]QuestionStateEvent(nil), machine.events...)
}

func questionStateTransitionAllowed(current, next QuestionState) bool {
	allowed := map[QuestionState]map[QuestionState]bool{
		"": {QuestionStateReceived: true},
		QuestionStateReceived: {
			QuestionStateAuthorized: true, QuestionStateBlocked: true,
		},
		QuestionStateAuthorized: {
			QuestionStateContextReady: true, QuestionStateBlocked: true,
		},
		QuestionStateContextReady: {
			QuestionStateValidating:            true,
			QuestionStatePlanReady:             true,
			QuestionStateClarificationRequired: true,
			QuestionStateBlocked:               true,
		},
		QuestionStateValidating: {
			QuestionStatePlanReady:             true,
			QuestionStateCostApproved:          true,
			QuestionStateClarificationRequired: true,
			QuestionStateBlocked:               true,
		},
		QuestionStatePlanReady: {
			QuestionStateValidating:            true,
			QuestionStateClarificationRequired: true,
			QuestionStateCostApproved:          true,
			QuestionStateBlocked:               true,
		},
		QuestionStateCostApproved: {
			QuestionStateExecuting: true, QuestionStateBlocked: true,
		},
		QuestionStateExecuting: {
			QuestionStateResultVerified: true, QuestionStateBlocked: true,
		},
		QuestionStateResultVerified: {
			QuestionStateAnswered: true, QuestionStateBlocked: true,
		},
	}
	return allowed[current][next]
}

func terminalQuestionState(state QuestionState) bool {
	return state == QuestionStateClarificationRequired ||
		state == QuestionStateAnswered || state == QuestionStateBlocked
}

type questionRunRecorder interface {
	StartQuestionRun(
		context.Context, string, string, string, string, QuestionStateEvent,
	) error
	AppendQuestionState(
		context.Context, string, string, QuestionState, QuestionStateEvent,
	) error
}

func persistQuestionStateMachine(
	ctx context.Context,
	service *Service,
	tenantID, actorID, questionHash string,
	machine *questionStateMachine,
) error {
	recorder, ok := service.store.(questionRunRecorder)
	if !ok || len(machine.events) != 1 ||
		machine.events[0].State != QuestionStateReceived {
		return nil
	}
	if err := recorder.StartQuestionRun(
		ctx, tenantID, actorID, machine.runID, questionHash,
		machine.events[0],
	); err != nil {
		return err
	}
	machine.onAdvance = func(
		current QuestionState,
		event QuestionStateEvent,
	) error {
		return recorder.AppendQuestionState(
			ctx, tenantID, machine.runID, current, event,
		)
	}
	return nil
}

func syncTurnLifecycle(
	result *QueryTurnPlan,
	machine *questionStateMachine,
) {
	if result == nil || machine == nil {
		return
	}
	result.QuestionRunID = machine.runID
	result.State = machine.state
	result.Lifecycle = machine.lifecycle()
}

func advanceTurnLifecycle(
	result *QueryTurnPlan,
	machine *questionStateMachine,
	next QuestionState,
) error {
	if err := machine.advance(next); err != nil {
		return err
	}
	syncTurnLifecycle(result, machine)
	return nil
}

func blockQuestionState(
	result *QueryTurnPlan,
	machine *questionStateMachine,
) {
	if machine == nil || machine.onAdvance == nil ||
		machine.state == QuestionStateBlocked ||
		machine.state == QuestionStateAnswered ||
		machine.state == QuestionStateClarificationRequired {
		return
	}
	if machine.advance(QuestionStateBlocked) == nil {
		syncTurnLifecycle(result, machine)
	}
}
