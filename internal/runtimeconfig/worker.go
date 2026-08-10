package runtimeconfig

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type Worker struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewWorker(pool *pgxpool.Pool) (*Worker, error) {
	if pool == nil {
		return nil, errors.New("runtime config worker requires database")
	}
	return &Worker{pool: pool, now: time.Now}, nil
}
func (w *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	rows, e := w.pool.Query(ctx, `SELECT tenant_id::text FROM platform.runtime_config_rollout_tenants()`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		values = append(values, id)
	}
	return values, rows.Err()
}
func (w *Worker) ProcessTenant(ctx context.Context, tenant string, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, ErrInvalid
	}
	count := 0
	for count < limit {
		processed, e := w.processNext(ctx, tenant)
		if e != nil {
			return count, e
		}
		if !processed {
			break
		}
		count++
	}
	return count, nil
}
func (w *Worker) processNext(ctx context.Context, tenant string) (bool, error) {
	now := w.clock()
	processed := false
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), w.pool, tenant, func(tx pgx.Tx) error {
		var nodeID, versionID, expected, compatibility string
		var configRaw []byte
		e := tx.QueryRow(ctx, `SELECT node.id::text,node.version_id::text,node.expected_hash,version.config_json,version.compatibility FROM platform.runtime_config_rollout_nodes node JOIN platform.runtime_config_versions version ON version.tenant_id=node.tenant_id AND version.id=node.version_id WHERE node.tenant_id=$1 AND node.state='PENDING' AND version.state='ROLLING_OUT' AND NOT EXISTS(SELECT 1 FROM platform.runtime_config_rollout_nodes previous WHERE previous.tenant_id=node.tenant_id AND previous.version_id=node.version_id AND previous.ordinal<node.ordinal AND previous.state NOT IN('APPLIED','WAITING_RESTART')) ORDER BY version.created_at,node.ordinal,node.id LIMIT 1 FOR UPDATE OF node SKIP LOCKED`, tenant).Scan(&nodeID, &versionID, &expected, &configRaw, &compatibility)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		processed = true
		var scope string
		if e = tx.QueryRow(ctx, `SELECT scope_type FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2`, tenant, versionID).Scan(&scope); e != nil {
			return e
		}
		_, hash, storedCompatibility, validateErr := ValidateConfig(scope, configRaw)
		if validateErr != nil || string(hash) != expected || storedCompatibility != compatibility {
			if _, e = tx.Exec(ctx, `UPDATE platform.runtime_config_rollout_nodes SET state='FAILED',failure_code='VALIDATION_FAILED',attempt=attempt+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenant, nodeID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='FAILED',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenant, versionID); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE platform.runtime_config_rollout_nodes SET state='CANCELED',updated_at=$1 WHERE tenant_id=$2 AND version_id=$3 AND state='PENDING'`, now, tenant, versionID)
			return e
		}
		state := "APPLIED"
		appliedHash := expected
		appliedAt := any(now)
		if compatibility == "NEXT_RESTART" {
			state = "WAITING_RESTART"
			appliedHash = ""
			appliedAt = nil
		}
		if _, e = tx.Exec(ctx, `UPDATE platform.runtime_config_rollout_nodes SET state=$1,applied_hash=$2,attempt=attempt+1,applied_at=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND state='PENDING'`, state, appliedHash, appliedAt, now, tenant, nodeID); e != nil {
			return e
		}
		if state == "APPLIED" {
			return activateIfComplete(ctx, tx, tenant, versionID, now)
		}
		return nil
	})
	return processed, e
}
func activateIfComplete(ctx context.Context, tx pgx.Tx, tenant, versionID string, now time.Time) error {
	var ready bool
	if e := tx.QueryRow(ctx, `SELECT count(*)>0 AND bool_and(state='APPLIED') FROM platform.runtime_config_rollout_nodes WHERE tenant_id=$1 AND version_id=$2`, tenant, versionID).Scan(&ready); e != nil || !ready {
		return e
	}
	var scope, scopeID string
	if e := tx.QueryRow(ctx, `SELECT scope_type,scope_id FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND state='ROLLING_OUT' FOR UPDATE`, tenant, versionID).Scan(&scope, &scopeID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='SUPERSEDED',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=(SELECT version_id FROM platform.runtime_config_effective WHERE tenant_id=$2 AND scope_type=$3 AND scope_id=$4) AND id<>$5`, now, tenant, scope, scopeID, versionID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `INSERT INTO platform.runtime_config_effective(tenant_id,scope_type,scope_id,version_id,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,scope_type,scope_id) DO UPDATE SET version_id=EXCLUDED.version_id,updated_at=EXCLUDED.updated_at`, tenant, scope, scopeID, versionID, now); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='ACTIVE',activated_at=$1,record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND state='ROLLING_OUT'`, now, tenant, versionID); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO platform.runtime_config_events(id,tenant_id,version_id,event_type,details_json,created_at) VALUES($1,$2,$3,'ACTIVATED','{}',$4)`, uuid.NewString(), tenant, versionID, now)
	return e
}
func (w *Worker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}
