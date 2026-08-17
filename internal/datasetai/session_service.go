package datasetai

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Session operations exposed over HTTP. The service owns all validation; handlers
// stay thin. Every operation is scoped to the calling actor: a modeling session is
// personal working state, never shared between users.

// ErrSessionStoreUnavailable reports a deployment without session persistence.
// Planning still works statelessly; only the session endpoints refuse.
var ErrSessionStoreUnavailable = errors.New("dataset AI modeling session store is not configured")

type SessionScopeRequest struct {
	Goal            string        `json:"goal"`
	ModelKind       string        `json:"modelKind"`
	ModelKindSource string        `json:"modelKindSource"`
	AutoConfirmed   bool          `json:"autoConfirmed"`
	Reason          string        `json:"reason"`
	Tables          []ScopedTable `json:"tables"`
}

// SessionOpenRequest carries the optional business domain of a new modeling
// conversation. For a saved dataset the store resolves the dataset's own domain
// when this is empty.
type SessionOpenRequest struct {
	DomainID string `json:"domainId,omitempty"`
}

type SessionEventRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	// Execution accompanies PROPOSAL_EXECUTED: the preview outcome of the applied proposal.
	Execution *ProposalExecution `json:"execution,omitempty"`
}

const (
	SessionEventProposalApplied  = "PROPOSAL_APPLIED"
	SessionEventProposalReverted = "PROPOSAL_REVERTED"
	SessionEventProposalExecuted = "PROPOSAL_EXECUTED"
)

// OpenSession starts a fresh ACTIVE session, closing the actor's previous ACTIVE
// session for the same saved dataset. Opening is deliberately not idempotent: a new
// conversation is a new decision record.
func (s *Service) OpenSession(ctx context.Context, tenantID, actorID, datasetID string, input SessionOpenRequest) (ModelingSession, error) {
	if s == nil || s.sessions == nil {
		return ModelingSession{}, ErrSessionStoreUnavailable
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" {
		return ModelingSession{}, ErrInvalidRequest
	}
	domainID := strings.TrimSpace(input.DomainID)
	if !boundedText(domainID, 0, 128) {
		return ModelingSession{}, ErrInvalidRequest
	}
	session := ModelingSession{TenantID: tenantID, ActorID: actorID, DatasetID: strings.TrimSpace(datasetID), State: ModelingSessionState{DomainID: domainID}}
	if err := s.sessions.Create(ctx, &session); err != nil {
		return ModelingSession{}, err
	}
	return session, nil
}

func (s *Service) GetSession(ctx context.Context, tenantID, actorID, sessionID string) (ModelingSession, error) {
	if s == nil || s.sessions == nil {
		return ModelingSession{}, ErrSessionStoreUnavailable
	}
	return s.sessions.Get(ctx, tenantID, actorID, strings.TrimSpace(sessionID))
}

func (s *Service) FindDatasetSession(ctx context.Context, tenantID, actorID, datasetID string) (ModelingSession, bool, error) {
	if s == nil || s.sessions == nil {
		return ModelingSession{}, false, ErrSessionStoreUnavailable
	}
	if strings.TrimSpace(datasetID) == "" {
		return ModelingSession{}, false, ErrInvalidRequest
	}
	return s.sessions.FindActiveByDataset(ctx, tenantID, actorID, strings.TrimSpace(datasetID))
}

// ConfirmSessionScope records the user's modeling scope. Every confirmed table must
// currently be an available mapped asset: the scope becomes a hard catalog boundary
// for CREATE planning, so an unusable table must be rejected here, while the user is
// still looking at the card.
func (s *Service) ConfirmSessionScope(ctx context.Context, tenantID, actorID, sessionID string, input SessionScopeRequest) (ModelingSession, error) {
	if s == nil || s.sessions == nil {
		return ModelingSession{}, ErrSessionStoreUnavailable
	}
	session, err := s.sessions.Get(ctx, tenantID, actorID, strings.TrimSpace(sessionID))
	if err != nil {
		return ModelingSession{}, err
	}
	if session.Status != SessionStatusActive {
		return ModelingSession{}, ErrSessionNotFound
	}
	if session.State.Blueprint != nil && session.State.Blueprint.Phase == BlueprintPhaseBusiness && len(session.State.PendingBlueprintStages()) > 0 {
		return ModelingSession{}, ErrBlueprintRequired
	}
	if s.catalog != nil {
		for _, table := range input.Tables {
			loaded, tableErr := s.catalog.GetTable(ctx, tenantID, strings.TrimSpace(table.TableID))
			if tableErr != nil || !availableCatalogTable(loaded) {
				return ModelingSession{}, ErrContextStale
			}
		}
	}
	if err := s.mutateSession(ctx, &session, func(state *ModelingSessionState) error {
		return state.ConfirmScope(input.Goal, input.ModelKind, input.ModelKindSource, input.Tables, input.AutoConfirmed, input.Reason, time.Now())
	}); err != nil {
		return ModelingSession{}, err
	}
	return session, nil
}

// ResolveSessionProposal applies a client-reported proposal outcome (applied or
// reverted). The transition table in ResolveProposal rejects everything else.
func (s *Service) ResolveSessionProposal(ctx context.Context, tenantID, actorID, sessionID string, input SessionEventRequest) (ModelingSession, error) {
	if s == nil || s.sessions == nil {
		return ModelingSession{}, ErrSessionStoreUnavailable
	}
	session, err := s.sessions.Get(ctx, tenantID, actorID, strings.TrimSpace(sessionID))
	if err != nil {
		return ModelingSession{}, err
	}
	if session.Status != SessionStatusActive {
		return ModelingSession{}, ErrSessionNotFound
	}
	var status string
	switch strings.ToUpper(strings.TrimSpace(input.Type)) {
	case SessionEventProposalApplied:
		status = ProposalStatusApplied
	case SessionEventProposalReverted:
		status = ProposalStatusReverted
	case SessionEventProposalExecuted:
		if input.Execution == nil {
			return ModelingSession{}, errors.Join(ErrInvalidRequest, errors.New("execution report is required"))
		}
		if err := s.mutateSession(ctx, &session, func(state *ModelingSessionState) error {
			return state.RecordProposalExecution(input.RequestID, *input.Execution, time.Now())
		}); err != nil {
			return ModelingSession{}, err
		}
		return session, nil
	default:
		return ModelingSession{}, errors.Join(ErrInvalidRequest, errors.New("unsupported session event type"))
	}
	if err := s.mutateSession(ctx, &session, func(state *ModelingSessionState) error {
		return state.ResolveProposal(input.RequestID, status, time.Now())
	}); err != nil {
		return ModelingSession{}, err
	}
	return session, nil
}
