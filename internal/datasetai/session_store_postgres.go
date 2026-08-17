package datasetai

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresSessionStore persists modeling sessions in
// platform.dataset_ai_modeling_sessions under tenant RLS.
type PostgresSessionStore struct{ pool *pgxpool.Pool }

func NewPostgresSessionStore(pool *pgxpool.Pool) *PostgresSessionStore {
	return &PostgresSessionStore{pool: pool}
}

func (s *PostgresSessionStore) Create(ctx context.Context, session *ModelingSession) error {
	return database.WithTenantTx(ctx, s.pool, session.TenantID, func(tx pgx.Tx) error {
		// A saved dataset already belongs to a business domain; record it so
		// knowledge lookups are scoped without the client having to know it.
		if session.DatasetID != "" && session.State.DomainID == "" {
			var domainID string
			if err := tx.QueryRow(ctx,
				`SELECT COALESCE(domain_id::text,'') FROM platform.datasets WHERE id=$1::uuid AND tenant_id=$2::uuid`,
				session.DatasetID, session.TenantID,
			).Scan(&domainID); err == nil {
				session.State.DomainID = domainID
			}
		}
		document, err := json.Marshal(session.State)
		if err != nil {
			return err
		}
		if session.DatasetID != "" {
			if _, err := tx.Exec(ctx,
				`UPDATE platform.dataset_ai_modeling_sessions
				   SET status='CLOSED', revision=revision+1, updated_at=now()
				 WHERE tenant_id=$1 AND actor_id=$2 AND dataset_id=$3 AND status='ACTIVE'`,
				session.TenantID, session.ActorID, session.DatasetID); err != nil {
				return err
			}
		}
		var datasetID any
		if session.DatasetID != "" {
			datasetID = session.DatasetID
		}
		return tx.QueryRow(ctx,
			`INSERT INTO platform.dataset_ai_modeling_sessions(tenant_id,actor_id,dataset_id,document)
			 VALUES($1,$2,$3,$4)
			 RETURNING id,status,revision,created_at,updated_at`,
			session.TenantID, session.ActorID, datasetID, document,
		).Scan(&session.ID, &session.Status, &session.Revision, &session.CreatedAt, &session.UpdatedAt)
	})
}

func (s *PostgresSessionStore) Get(ctx context.Context, tenantID, actorID, sessionID string) (ModelingSession, error) {
	session := ModelingSession{TenantID: tenantID, ActorID: actorID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanSession(tx.QueryRow(ctx,
			`SELECT id,COALESCE(dataset_id::text,''),status,revision,document,created_at,updated_at
			   FROM platform.dataset_ai_modeling_sessions
			  WHERE tenant_id=$1 AND actor_id=$2 AND id=$3`,
			tenantID, actorID, sessionID), &session)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelingSession{}, ErrSessionNotFound
	}
	return session, err
}

func (s *PostgresSessionStore) FindActiveByDataset(ctx context.Context, tenantID, actorID, datasetID string) (ModelingSession, bool, error) {
	session := ModelingSession{TenantID: tenantID, ActorID: actorID}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanSession(tx.QueryRow(ctx,
			`SELECT id,COALESCE(dataset_id::text,''),status,revision,document,created_at,updated_at
			   FROM platform.dataset_ai_modeling_sessions
			  WHERE tenant_id=$1 AND actor_id=$2 AND dataset_id=$3 AND status='ACTIVE'`,
			tenantID, actorID, datasetID), &session)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelingSession{}, false, nil
	}
	if err != nil {
		return ModelingSession{}, false, err
	}
	return session, true, nil
}

func (s *PostgresSessionStore) Update(ctx context.Context, session *ModelingSession) error {
	document, err := json.Marshal(session.State)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, session.TenantID, func(tx pgx.Tx) error {
		result := tx.QueryRow(ctx,
			`UPDATE platform.dataset_ai_modeling_sessions
			    SET document=$4, revision=revision+1, updated_at=now()
			  WHERE tenant_id=$1 AND actor_id=$2 AND id=$3 AND revision=$5 AND status='ACTIVE'
			 RETURNING revision,updated_at`,
			session.TenantID, session.ActorID, session.ID, document, session.Revision)
		if err := result.Scan(&session.Revision, &session.UpdatedAt); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// Distinguish a concurrent write from a missing or closed session so the
			// caller can reload-and-retry only when a retry can actually succeed.
			var exists bool
			if probeErr := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM platform.dataset_ai_modeling_sessions
				  WHERE tenant_id=$1 AND actor_id=$2 AND id=$3 AND status='ACTIVE')`,
				session.TenantID, session.ActorID, session.ID).Scan(&exists); probeErr != nil {
				return probeErr
			}
			if exists {
				return ErrSessionConflict
			}
			return ErrSessionNotFound
		}
		return nil
	})
}

func scanSession(row pgx.Row, session *ModelingSession) error {
	var document []byte
	if err := row.Scan(&session.ID, &session.DatasetID, &session.Status, &session.Revision,
		&document, &session.CreatedAt, &session.UpdatedAt); err != nil {
		return err
	}
	return json.Unmarshal(document, &session.State)
}
