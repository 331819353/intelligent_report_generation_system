package sharing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const MaxShareExpiryBatch = 500

type expiryStore interface {
	TenantIDs(context.Context) ([]string, error)
	MarkExpired(context.Context, string, time.Time, int) (int, error)
}

type ExpiryWorker struct{ store expiryStore }

func NewExpiryWorker(pool *pgxpool.Pool) *ExpiryWorker {
	return &ExpiryWorker{store: &postgresExpiryStore{pool: pool}}
}

func (worker *ExpiryWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, errors.New("report share expiry worker is unavailable")
	}
	return worker.store.TenantIDs(ctx)
}

func (worker *ExpiryWorker) ProcessTenant(
	ctx context.Context, tenantID string, now time.Time, limit int,
) (int, error) {
	if worker == nil || worker.store == nil || !validTenantUUID(tenantID) || now.IsZero() ||
		limit < 1 || limit > MaxShareExpiryBatch {
		return 0, errors.New("invalid report share expiry request")
	}
	return worker.store.MarkExpired(ctx, tenantID, now.UTC(), limit)
}

type postgresExpiryStore struct{ pool *pgxpool.Pool }

func (store *postgresExpiryStore) TenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("report share expiry store is unavailable")
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

func (store *postgresExpiryStore) MarkExpired(
	ctx context.Context, tenantID string, now time.Time, limit int,
) (int, error) {
	if store == nil || store.pool == nil {
		return 0, errors.New("report share expiry store is unavailable")
	}
	marked := 0
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `WITH picked AS (
			SELECT id FROM platform.report_shares
			WHERE tenant_id=$1 AND revoked_at IS NULL AND expired_at IS NULL AND expires_at<=$2
			ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $3
		) UPDATE platform.report_shares AS share SET expired_at=$2
		  FROM picked WHERE share.id=picked.id AND share.tenant_id=$1`, tenantID, now, limit)
		if err != nil {
			return err
		}
		marked = int(command.RowsAffected())
		return nil
	})
	return marked, err
}

func validTenantUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
