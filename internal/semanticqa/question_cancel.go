package semanticqa

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type QuestionCancelResponse struct {
	QuestionID string        `json:"questionId"`
	State      QuestionState `json:"state"`
	Status     string        `json:"status"`
}

func (service *Service) registerActiveQuestion(
	questionID string,
	cancel context.CancelFunc,
) {
	if service == nil || uuid.Validate(questionID) != nil || cancel == nil {
		return
	}
	service.questionMu.Lock()
	defer service.questionMu.Unlock()
	if service.activeQuestions == nil {
		service.activeQuestions = map[string]context.CancelFunc{}
	}
	service.activeQuestions[questionID] = cancel
}

func (service *Service) unregisterActiveQuestion(questionID string) {
	if service == nil || questionID == "" {
		return
	}
	service.questionMu.Lock()
	defer service.questionMu.Unlock()
	delete(service.activeQuestions, questionID)
}

func (service *Service) CancelQuestion(
	ctx context.Context,
	tenantID, questionID string,
) (QuestionCancelResponse, error) {
	result := QuestionCancelResponse{QuestionID: questionID}
	if service == nil || service.store == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(questionID) != nil {
		return result, ErrInvalidRequest
	}
	service.questionMu.Lock()
	cancel := service.activeQuestions[questionID]
	service.questionMu.Unlock()
	if cancel == nil {
		return result, ErrInvalidState
	}
	cancel()

	recorder, ok := service.store.(questionRunRecorder)
	if !ok {
		return result, ErrInvalidState
	}
	for attempt := 0; attempt < 3; attempt++ {
		run, err := service.GetQuestion(ctx, tenantID, questionID)
		if err != nil {
			return result, err
		}
		if terminalQuestionState(run.State) {
			return result, ErrInvalidState
		}
		event := QuestionStateEvent{
			State:     QuestionStateBlocked,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Stage:     string(QuestionStateBlocked), Status: "BLOCKED",
			Code:    "CLIENT_CANCELLED",
			Summary: map[string]any{"reason": "client_cancelled"},
		}
		if err := recorder.AppendQuestionState(
			ctx, tenantID, questionID, run.State, event,
		); err == nil {
			result.State = QuestionStateBlocked
			result.Status = "CANCELLED"
			return result, nil
		} else if !errors.Is(err, ErrConflict) {
			return result, err
		}
	}
	return result, ErrConflict
}
