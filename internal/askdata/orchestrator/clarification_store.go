package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const MaxClarificationExpiryBatch = 100

// ExpireClarification is the runtime read guard. It makes timeout correctness
// independent of worker timing and is idempotent under concurrent readers.
func (store *PostgresStore) ExpireClarification(
	ctx context.Context,
	request ResumeRequest,
	now time.Time,
) (bool, error) {
	tenantID, err := validateActorScope(ctx, request.Scope, request.DomainID)
	if err != nil {
		return false, err
	}
	if !canonicalUUID(request.RunID) || now.IsZero() {
		return false, ErrInvalidRun
	}
	expired := false
	err = store.withActorTx(ctx, pgx.TxOptions{}, tenantID, func(tx pgx.Tx) error {
		run, err := loadRunByIDTx(ctx, tx, request.Scope, request.DomainID, request.RunID, true)
		if err != nil {
			return err
		}
		if !runMatchesScope(run, request.Scope, request.DomainID) {
			return ErrPinnedScopeMismatch
		}
		expired, err = expireClarificationTx(ctx, tx, run, now.UTC())
		return err
	})
	return expired, mapPersistenceError(err)
}

func expireClarificationTx(ctx context.Context, tx pgx.Tx, current Run, now time.Time) (bool, error) {
	if current.State != StateClarificationRequired || !ClarificationExpired(current, now) {
		return false, nil
	}
	events, err := loadEventsTx(ctx, tx, current)
	if err != nil {
		return false, err
	}
	artifacts, err := loadArtifactsTx(ctx, tx, current)
	if err != nil {
		return false, err
	}
	tools, err := loadToolCallsTx(ctx, tx, current)
	if err != nil {
		return false, err
	}
	if err := (ReplaySnapshot{Run: current, Events: events, Artifacts: artifacts, ToolCalls: tools}).Validate(); err != nil {
		return false, err
	}
	next := current
	next.State = StateClarificationExpired
	next.CompletionCode = "CLARIFICATION_EXPIRED"
	next.RecordVersion++
	next.UpdatedAt = now
	persisted, err := updateRunTx(ctx, tx, current, next)
	if err != nil {
		if errors.Is(err, ErrVersionConflict) {
			return false, nil
		}
		return false, err
	}
	last := events[len(events)-1]
	event, err := buildTransitionEvent(persisted, current.State, last.Index+1, last.Hash, TransitionEventInput{
		Details: json.RawMessage(`{"reason":"DEADLINE_EXCEEDED"}`),
	})
	if err != nil {
		return false, err
	}
	event.ArtifactHash = persisted.CompletionArtifact
	event.Hash, err = computeEventHash(event)
	if err != nil {
		return false, err
	}
	if err := insertEventTx(ctx, tx, event); err != nil {
		return false, err
	}
	return true, nil
}

type ClarificationExpiryWorker struct{ pool *pgxpool.Pool }

func NewClarificationExpiryWorker(pool *pgxpool.Pool) *ClarificationExpiryWorker {
	return &ClarificationExpiryWorker{pool: pool}
}

func (worker *ClarificationExpiryWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.pool == nil {
		return nil, ErrPersistence
	}
	rows, err := worker.pool.Query(ctx, `SELECT id::text FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (worker *ClarificationExpiryWorker) ProcessTenant(
	ctx context.Context,
	tenantID string,
	now time.Time,
	limit int,
) (int, error) {
	if worker == nil || worker.pool == nil || uuid.Validate(tenantID) != nil || now.IsZero() ||
		limit < 1 || limit > MaxClarificationExpiryBatch {
		return 0, ErrInvalidRun
	}
	processed := 0
	err := database.WithTenantTx(ctx, worker.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, runSelect+`
			WHERE tenant_id=$1 AND current_state='CLARIFICATION_REQUIRED'
			  AND clarification_deadline<=$2
			ORDER BY clarification_deadline,id
			FOR UPDATE SKIP LOCKED LIMIT $3`, tenantID, now.UTC(), limit)
		if err != nil {
			return err
		}
		runs := []Run{}
		for rows.Next() {
			run, scanErr := scanRun(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			runs = append(runs, run)
		}
		rows.Close()
		for _, run := range runs {
			expired, err := expireClarificationTx(ctx, tx, run, now.UTC())
			if err != nil {
				return fmt.Errorf("expire clarification %s: %w", run.ID, err)
			}
			if expired {
				processed++
			}
		}
		return nil
	})
	return processed, err
}
