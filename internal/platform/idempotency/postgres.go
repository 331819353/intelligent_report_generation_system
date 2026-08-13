package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (repository *PostgresRepository) Begin(
	ctx context.Context,
	identity Identity,
	endpoint, key, requestHash string,
	now time.Time,
) (Record, error) {
	if repository == nil || repository.pool == nil ||
		!ValidCoordinates(identity, endpoint, key, requestHash) || now.IsZero() {
		return Record{}, errors.New("invalid idempotency acquisition")
	}
	var result Record
	err := database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		// Retention is enforced by a database-clock trigger. Bound the caller's
		// clock with that same source so tiny host/database clock skew cannot
		// attempt to delete a completed record a few milliseconds too early.
		if _, err := tx.Exec(ctx, `DELETE FROM askdata.idempotency_records
			WHERE tenant_id=$1 AND actor_id=$2 AND endpoint=$3 AND idempotency_key=$4
				AND expires_at<=LEAST($5,clock_timestamp())`,
			identity.TenantID, identity.ActorID, endpoint, key, now.UTC()); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `INSERT INTO askdata.idempotency_records(
			id,tenant_id,actor_id,endpoint,idempotency_key,request_hash,state,created_at,expires_at
		) VALUES($1,$2,$3,$4,$5,$6,'IN_FLIGHT',$7,$8)
		ON CONFLICT(tenant_id,actor_id,endpoint,idempotency_key) DO NOTHING`,
			uuid.NewString(), identity.TenantID, identity.ActorID, endpoint, key,
			requestHash, now.UTC(), now.UTC().Add(TTL))
		if err != nil {
			return err
		}
		if command.RowsAffected() == 1 {
			result = Record{State: StateAcquired, RequestHash: requestHash}
			return nil
		}
		var state, storedHash string
		var status *int
		var body []byte
		var responseHash *string
		if err := tx.QueryRow(ctx, `SELECT state,request_hash,response_status,response_body,response_hash
			FROM askdata.idempotency_records
			WHERE tenant_id=$1 AND actor_id=$2 AND endpoint=$3 AND idempotency_key=$4
			FOR SHARE`, identity.TenantID, identity.ActorID, endpoint, key).
			Scan(&state, &storedHash, &status, &body, &responseHash); err != nil {
			return err
		}
		result.RequestHash = storedHash
		if storedHash != requestHash {
			result.State = StateReused
			return nil
		}
		if state == "IN_FLIGHT" {
			result.State = StateInFlight
			return nil
		}
		if state != "COMPLETED" || status == nil || *status < 100 || *status > 599 ||
			responseHash == nil || *responseHash != Hash(body) || len(body) > MaxResponseBodyBytes ||
			!json.Valid(body) {
			return errors.New("corrupt idempotency record")
		}
		result.State, result.ResponseStatus = StateReplay, *status
		result.ResponseBody = append([]byte(nil), body...)
		return nil
	})
	return result, err
}

func (repository *PostgresRepository) Complete(
	ctx context.Context,
	identity Identity,
	endpoint, key, requestHash string,
	status int,
	body []byte,
) error {
	if repository == nil || repository.pool == nil ||
		!ValidCoordinates(identity, endpoint, key, requestHash) || status < 100 || status > 599 ||
		len(body) > MaxResponseBodyBytes || !json.Valid(body) {
		return errors.New("invalid idempotency completion")
	}
	return database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE askdata.idempotency_records
			SET state='COMPLETED',response_status=$6,response_body=$7,response_hash=$8
			WHERE tenant_id=$1 AND actor_id=$2 AND endpoint=$3 AND idempotency_key=$4
				AND request_hash=$5 AND state='IN_FLIGHT'`, identity.TenantID, identity.ActorID,
			endpoint, key, requestHash, status, body, Hash(body))
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("idempotency completion lost ownership")
		}
		return nil
	})
}

func (repository *PostgresRepository) Release(
	ctx context.Context,
	identity Identity,
	endpoint, key, requestHash string,
) error {
	if repository == nil || repository.pool == nil ||
		!ValidCoordinates(identity, endpoint, key, requestHash) {
		return errors.New("invalid idempotency release")
	}
	return database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM askdata.idempotency_records
			WHERE tenant_id=$1 AND actor_id=$2 AND endpoint=$3 AND idempotency_key=$4
				AND request_hash=$5 AND state='IN_FLIGHT'`, identity.TenantID, identity.ActorID,
			endpoint, key, requestHash)
		return err
	})
}

type cleanupStore interface {
	TenantIDs(context.Context) ([]string, error)
	DeleteExpired(context.Context, string, time.Time, int) (int, error)
}

type postgresCleanupStore struct{ pool *pgxpool.Pool }

type CleanupWorker struct{ store cleanupStore }

func NewCleanupWorker(pool *pgxpool.Pool) *CleanupWorker {
	return &CleanupWorker{store: &postgresCleanupStore{pool: pool}}
}

func (worker *CleanupWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, errors.New("idempotency cleanup worker is unavailable")
	}
	return worker.store.TenantIDs(ctx)
}

func (store *postgresCleanupStore) TenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("idempotency cleanup store is unavailable")
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text FROM platform.tenants
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

func (worker *CleanupWorker) ProcessTenant(
	ctx context.Context,
	tenantID string,
	now time.Time,
	limit int,
) (int, error) {
	if worker == nil || worker.store == nil || !canonicalUUID(tenantID) || now.IsZero() ||
		limit < 1 || limit > MaxExpiredCleanupBatch {
		return 0, errors.New("invalid idempotency cleanup request")
	}
	return worker.store.DeleteExpired(ctx, tenantID, now.UTC(), limit)
}

func (store *postgresCleanupStore) DeleteExpired(
	ctx context.Context,
	tenantID string,
	now time.Time,
	limit int,
) (int, error) {
	if store == nil || store.pool == nil {
		return 0, errors.New("idempotency cleanup store is unavailable")
	}
	deleted := 0
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		// The lifecycle trigger compares expiry with clock_timestamp(). Use the
		// same database clock here (while still respecting the caller's cutoff)
		// so boundary-time cleanup never trips the 24-hour retention guard.
		// Deliberately no FOR UPDATE SKIP LOCKED. Row locking would require the
		// UPDATE privilege, and the cleanup worker holds only SELECT and DELETE —
		// granting UPDATE would let it rewrite idempotency records, which it must
		// never be able to do. The lock is not needed for correctness either: the
		// DELETE below is guarded by id and expires_at, so two workers racing the
		// same row simply means one deletes it and the other affects no rows.
		rows, err := tx.Query(ctx, `SELECT id::text FROM askdata.idempotency_records
			WHERE tenant_id=$1 AND expires_at<=LEAST($2,clock_timestamp())
			ORDER BY expires_at,id LIMIT $3`, tenantID, now.UTC(), limit)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		sort.Strings(ids)
		for _, id := range ids {
			command, err := tx.Exec(ctx, `DELETE FROM askdata.idempotency_records
				WHERE tenant_id=$1 AND id=$2
				  AND expires_at<=LEAST($3,clock_timestamp())`, tenantID, id, now.UTC())
			if err != nil {
				return err
			}
			deleted += int(command.RowsAffected())
		}
		return nil
	})
	return deleted, err
}
