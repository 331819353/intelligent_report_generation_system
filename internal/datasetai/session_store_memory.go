package datasetai

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemorySessionStore is a process-local SessionStore with the same takeover and
// optimistic-concurrency semantics as the Postgres store. It backs unit tests and
// any deployment that runs without a database.
type MemorySessionStore struct {
	mutex    sync.Mutex
	sequence int
	sessions map[string]ModelingSession
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: map[string]ModelingSession{}}
}

func (s *MemorySessionStore) Create(_ context.Context, session *ModelingSession) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for id, existing := range s.sessions {
		if existing.TenantID == session.TenantID && existing.ActorID == session.ActorID &&
			existing.DatasetID != "" && existing.DatasetID == session.DatasetID && existing.Status == SessionStatusActive {
			existing.Status = SessionStatusClosed
			existing.Revision++
			s.sessions[id] = existing
		}
	}
	s.sequence++
	session.ID = fmt.Sprintf("session-%d", s.sequence)
	session.Status = SessionStatusActive
	session.Revision = 1
	now := time.Now().UTC()
	session.CreatedAt = now
	session.UpdatedAt = now
	s.sessions[session.ID] = *session
	return nil
}

func (s *MemorySessionStore) Get(_ context.Context, tenantID, actorID, sessionID string) (ModelingSession, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	session, exists := s.sessions[sessionID]
	if !exists || session.TenantID != tenantID || session.ActorID != actorID {
		return ModelingSession{}, ErrSessionNotFound
	}
	return session, nil
}

func (s *MemorySessionStore) FindActiveByDataset(_ context.Context, tenantID, actorID, datasetID string) (ModelingSession, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for _, session := range s.sessions {
		if session.TenantID == tenantID && session.ActorID == actorID &&
			session.DatasetID == datasetID && session.Status == SessionStatusActive {
			return session, true, nil
		}
	}
	return ModelingSession{}, false, nil
}

func (s *MemorySessionStore) Update(_ context.Context, session *ModelingSession) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	existing, exists := s.sessions[session.ID]
	if !exists || existing.TenantID != session.TenantID || existing.ActorID != session.ActorID {
		return ErrSessionNotFound
	}
	if existing.Revision != session.Revision {
		return ErrSessionConflict
	}
	session.Revision++
	session.UpdatedAt = time.Now().UTC()
	s.sessions[session.ID] = *session
	return nil
}
