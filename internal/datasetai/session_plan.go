package datasetai

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// This file binds the persisted modeling session into the planning pipeline. The
// session supplies trusted prompt context and receives this turn's outcome; the
// stateless pipeline (no store, or no session id) keeps working unchanged.

const maxPromptClarificationTurns = 8

func (s *Service) loadPlanSession(ctx context.Context, tenantID, actorID string, input PlanRequest) (*ModelingSession, error) {
	if s.sessions == nil || input.SessionID == "" {
		return nil, nil
	}
	session, err := s.sessions.Get(ctx, tenantID, actorID, input.SessionID)
	if err != nil {
		return nil, err
	}
	if session.Status != SessionStatusActive {
		return nil, ErrSessionNotFound
	}
	return &session, nil
}

// mutateSession applies one state transition and persists it. A concurrent write is
// resolved by reloading and reapplying the transition once: sessions are per-actor,
// so a conflict means another tab of the same user, and the transition either still
// applies or fails its own validation on the fresh state.
func (s *Service) mutateSession(ctx context.Context, session *ModelingSession, mutate func(*ModelingSessionState) error) error {
	if s.sessions == nil || session == nil {
		return nil
	}
	if err := mutate(&session.State); err != nil {
		return err
	}
	err := s.sessions.Update(ctx, session)
	if !errors.Is(err, ErrSessionConflict) {
		return err
	}
	reloaded, getErr := s.sessions.Get(ctx, session.TenantID, session.ActorID, session.ID)
	if getErr != nil {
		return getErr
	}
	if err := mutate(&reloaded.State); err != nil {
		return err
	}
	if err := s.sessions.Update(ctx, &reloaded); err != nil {
		return err
	}
	*session = reloaded
	return nil
}

// recordClarificationAnswer validates the structured answer against the session's
// open question and the current graph, then persists it. Without a session the
// answer still becomes prompt context, but only its shape can be checked.
func (s *Service) recordClarificationAnswer(ctx context.Context, session *ModelingSession, input PlanRequest) error {
	answer := input.ClarificationAnswer
	if answer == nil {
		return nil
	}
	if answer.SelectedComponent != nil {
		if input.Current == nil {
			return fmt.Errorf("%w: a component answer requires the current graph", ErrInvalidRequest)
		}
		components, err := indexPlanComponents(normalizeGraphPlan(cloneGraphPlan(*input.Current)))
		if err != nil {
			return err
		}
		key, err := componentKey(answer.SelectedComponent.ComponentKind, answer.SelectedComponent.ComponentID)
		if err != nil {
			return err
		}
		if _, exists := components[key]; !exists {
			return fmt.Errorf("%w: the answered component no longer exists on the canvas", ErrInvalidRequest)
		}
	}
	if session == nil {
		return nil
	}
	return s.mutateSession(ctx, session, func(state *ModelingSessionState) error {
		return state.AnswerOpenClarification(*answer, time.Now())
	})
}

func (s *Service) recordClarificationAsked(ctx context.Context, session *ModelingSession, clarification *ClarificationRequiredError) error {
	if session == nil || clarification == nil {
		return nil
	}
	return s.mutateSession(ctx, session, func(state *ModelingSessionState) error {
		state.RecordClarificationAsked(clarification.Question, clarification.Candidates, time.Now())
		return nil
	})
}

func (s *Service) recordProposalStaged(ctx context.Context, session *ModelingSession, requestID, mode, summary, instruction string, current *GraphPlan, proposal GraphPlan) error {
	if session == nil {
		return nil
	}
	var stageUpdates []StageDecision
	var reopenStages []string
	if mode == "MODIFY" && current != nil {
		stageUpdates, reopenStages = deriveBlueprintResync(*current, proposal, session.State.Blueprint)
	}
	return s.mutateSession(ctx, session, func(state *ModelingSessionState) error {
		state.StageProposal(requestID, mode, summary, instruction, stageUpdates, reopenStages, time.Now())
		return nil
	})
}

// buildModelingPromptContext turns the session's confirmed decisions into the trusted
// context block. The immediate answer is included even without a session so a
// stateless deployment still grounds the current turn.
func buildModelingPromptContext(session *ModelingSession, answer *ClarificationAnswer) *promptModelingContext {
	result := promptModelingContext{}
	if session != nil {
		result.Goal = session.State.Goal
		result.ModelKind = session.State.ModelKind
		result.ConfirmedTableIDs = session.State.ScopeTableIDs()
		result.Blueprint = confirmedBlueprintContext(session.State)
		for _, finding := range session.State.ExecutionFindings {
			result.ExecutionFindings = append(result.ExecutionFindings, promptExecutionFinding{Code: finding.Code, Message: finding.Message, JoinID: finding.JoinID})
		}
		answered := make([]promptClarificationTurn, 0, len(session.State.Clarifications))
		for _, record := range session.State.Clarifications {
			if record.AnsweredAt == nil {
				continue
			}
			answered = append(answered, promptClarificationTurn{
				Question: record.Question, Answer: record.Answer, SelectedComponent: record.SelectedComponent,
			})
		}
		if len(answered) > maxPromptClarificationTurns {
			answered = answered[len(answered)-maxPromptClarificationTurns:]
		}
		result.Clarifications = answered
	} else if answer != nil {
		result.Clarifications = []promptClarificationTurn{{
			Question: answer.Question, Answer: answer.Text, SelectedComponent: answer.SelectedComponent,
		}}
	}
	if result.Goal == "" && result.ModelKind == "" && len(result.ConfirmedTableIDs) == 0 && len(result.Clarifications) == 0 && result.Blueprint == nil && len(result.ExecutionFindings) == 0 {
		return nil
	}
	return &result
}

// plannerModelingContext narrows the context for the graph planner. In MODIFY mode
// the planner deliberately receives no natural language at all — the locked changeSet
// is its only authority — so the context is withheld entirely. In CREATE mode the
// clarification turns are dropped: clarifications only arise from the intent phase.
func plannerModelingContext(mode string, promptCtx *promptModelingContext) *promptModelingContext {
	if mode != "CREATE" || promptCtx == nil {
		return nil
	}
	narrowed := promptModelingContext{
		Goal: promptCtx.Goal, ModelKind: promptCtx.ModelKind,
		ConfirmedTableIDs: promptCtx.ConfirmedTableIDs, Blueprint: promptCtx.Blueprint,
		ExecutionFindings: promptCtx.ExecutionFindings,
	}
	if narrowed.Goal == "" && narrowed.ModelKind == "" && len(narrowed.ConfirmedTableIDs) == 0 && narrowed.Blueprint == nil && len(narrowed.ExecutionFindings) == 0 {
		return nil
	}
	return &narrowed
}
