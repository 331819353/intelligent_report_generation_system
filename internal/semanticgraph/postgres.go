package semanticgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type ProjectionClaim struct {
	TenantID        string
	ProjectionID    string
	ReleaseID       string
	SemanticVersion string
	ContentHash     string
	LeaseToken      string
	Attempt         int
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) TenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text
		FROM platform.list_semantic_nebula_projection_tenants()`)
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

func (store *PostgresStore) Claim(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (*ProjectionClaim, error) {
	if store == nil || store.pool == nil || tenantID == "" || workerID == "" ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrInvalidRequest
	}
	claim := ProjectionClaim{TenantID: tenantID}
	err := store.pool.QueryRow(ctx, `SELECT
		projection_id::text,release_id::text,semantic_version,content_hash,
		lease_token::text,attempt
		FROM platform.claim_semantic_nebula_projection($1::uuid,$2,$3)`,
		tenantID, workerID, int(lease.Seconds()),
	).Scan(&claim.ProjectionID, &claim.ReleaseID, &claim.SemanticVersion,
		&claim.ContentHash, &claim.LeaseToken, &claim.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func (store *PostgresStore) LoadManifest(
	ctx context.Context, claim ProjectionClaim,
) (manifest ReleaseManifest, err error) {
	manifest = ReleaseManifest{TenantID: claim.TenantID, ReleaseID: claim.ReleaseID,
		SemanticVersion: claim.SemanticVersion, ContentHash: claim.ContentHash, Objects: []ReleaseObject{}}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status
			FROM platform.semantic_releases
			WHERE id=$1::uuid AND semantic_version=$2 AND content_hash=$3`,
			claim.ReleaseID, claim.SemanticVersion, claim.ContentHash,
		).Scan(&status); err != nil {
			return err
		}
		if status != "PROJECTING" {
			return ErrProjectionLease
		}
		rows, err := tx.Query(ctx, `SELECT object_type,object_id,object_version,
			domain_id,content_hash,certification,sensitivity,valid_from,valid_to,
			contract_json
			FROM platform.semantic_release_objects
			WHERE release_id=$1::uuid
			ORDER BY object_type,object_id,object_version`, claim.ReleaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var object ReleaseObject
			var contract []byte
			if err := rows.Scan(&object.ObjectType, &object.ObjectID, &object.ObjectVersion,
				&object.DomainID, &object.ContentHash, &object.Certification,
				&object.Sensitivity, &object.ValidFrom, &object.ValidTo, &contract); err != nil {
				return err
			}
			object.Contract = json.RawMessage(contract)
			manifest.Objects = append(manifest.Objects, object)
		}
		return rows.Err()
	})
	return manifest, err
}

func (store *PostgresStore) Heartbeat(
	ctx context.Context, claim ProjectionClaim, workerID string, lease time.Duration,
) (bool, error) {
	var renewed bool
	err := store.pool.QueryRow(ctx, `SELECT platform.heartbeat_semantic_nebula_projection(
		$1::uuid,$2::uuid,$3,$4::uuid,$5
	)`, claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		int(lease.Seconds())).Scan(&renewed)
	return renewed, err
}

func (store *PostgresStore) Complete(
	ctx context.Context, claim ProjectionClaim, workerID, resourceVersion string,
	verification ProjectionVerification,
) error {
	detail, err := json.Marshal(map[string]any{
		"vertexCount": verification.VertexCount, "edgeCount": verification.EdgeCount,
		"orphanCount": verification.OrphanCount, "projectionMode": "IDEMPOTENT_UPSERT_AND_FETCH_VERIFY",
	})
	if err != nil {
		return err
	}
	var completed bool
	err = store.pool.QueryRow(ctx, `SELECT platform.complete_semantic_nebula_projection(
		$1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8::jsonb
	)`, claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		claim.ContentHash, resourceVersion, verification.VertexCount+verification.EdgeCount,
		detail).Scan(&completed)
	if err != nil {
		return err
	}
	if !completed {
		return ErrProjectionLease
	}
	return nil
}

func (store *PostgresStore) Fail(
	ctx context.Context, claim ProjectionClaim, workerID, code string, cause error,
) error {
	detail, err := json.Marshal(map[string]any{
		"errorCode": code, "attempt": claim.Attempt,
		"retryable": !errors.Is(cause, ErrInvalidRequest),
	})
	if err != nil {
		return err
	}
	var failed bool
	err = store.pool.QueryRow(ctx, `SELECT platform.fail_semantic_nebula_projection(
		$1::uuid,$2::uuid,$3,$4::uuid,$5,$6::jsonb
	)`, claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		code, detail).Scan(&failed)
	if err != nil {
		return err
	}
	if !failed {
		return ErrProjectionLease
	}
	return nil
}

func (store *PostgresStore) Get(
	ctx context.Context, scope Scope, requestHash string,
) (item CachedGraphPlan, found bool, err error) {
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		var encoded []byte
		queryErr := tx.QueryRow(ctx, `SELECT plan_json
			FROM platform.semantic_graph_plan_cache
			WHERE semantic_version=$1 AND content_hash=$2 AND request_hash=$3
			  AND certified AND expires_at>now()`,
			scope.SemanticVersion, scope.ContentHash, requestHash,
		).Scan(&encoded)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		if unmarshalErr := json.Unmarshal(encoded, &item); unmarshalErr != nil {
			return unmarshalErr
		}
		found = true
		return nil
	})
	return item, found, err
}

func (store *PostgresStore) Put(ctx context.Context, item CachedGraphPlan) error {
	if !item.Certified || item.RequestHash == "" || !item.ExpiresAt.After(time.Now().UTC()) {
		return ErrInvalidRequest
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, store.pool, item.Scope.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_graph_plan_cache(
				tenant_id,semantic_version,content_hash,request_hash,plan_json,
				certified,expires_at
			) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,true,$5)
			ON CONFLICT(tenant_id,semantic_version,content_hash,request_hash)
			DO UPDATE SET plan_json=excluded.plan_json,certified=true,
				expires_at=excluded.expires_at`,
			item.Scope.SemanticVersion, item.Scope.ContentHash, item.RequestHash,
			encoded, item.ExpiresAt)
		return err
	})
}

func (claim ProjectionClaim) ResourceVersion(space string) string {
	hash := claim.ContentHash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return fmt.Sprintf("nebula:%s:%s:%s", space, claim.SemanticVersion, hash)
}
