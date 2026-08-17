package materialization

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

var _ dataset.MaterializationDeletionSink = (*PostgresStore)(nil)

// CleanupClaim is a leased ODS/DIM/DWD/DWS/ADS warehouse cleanup outbox item.
type CleanupClaim struct {
	ID            string
	TenantID      string
	DatasetID     string
	Layer         Layer
	RequestedBy   string
	ExpectedCount int
	Attempt       int
	MaxAttempts   int
	WorkerID      string
	LeaseToken    string
}

type cleanupTarget struct {
	BuildRunID   string
	Status       string
	RelationKind string
	Physical     PhysicalIdentifier
}

// EnqueueDatasetMaterializationCleanupTx captures the exact number of active or
// retired ODS/DIM/DWD/DWS/ADS materializations in the same control-plane transaction that
// soft-deletes the dataset. It never connects to the warehouse.
func (store *PostgresStore) EnqueueDatasetMaterializationCleanupTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID, layerValue string,
) (int, error) {
	layer := Layer(layerValue)
	if store == nil || tx == nil || !validUUID(tenantID) || !validUUID(actorID) ||
		!validUUID(datasetID) ||
		(layer != LayerODS && layer != LayerDIM && layer != LayerDWD && layer != LayerDWS && layer != LayerADS) {
		return 0, ErrInvalidRequest
	}
	var expectedCount int
	if err := tx.QueryRow(ctx, `WITH cleanup_targets AS (
			SELECT snapshot.physical_schema,snapshot.physical_name
			FROM platform.materialization_snapshots AS snapshot
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.id=snapshot.materialization_id
			 AND materialization.tenant_id=snapshot.tenant_id
			WHERE materialization.tenant_id=$1
			  AND materialization.dataset_id=$2 AND materialization.layer=$3
			  AND materialization.status IN ('ACTIVE','RETIRED')
			UNION
			SELECT materialization.physical_schema,materialization.physical_name
			FROM platform.dataset_materializations AS materialization
			WHERE materialization.tenant_id=$1
			  AND materialization.dataset_id=$2 AND materialization.layer=$3
			  AND materialization.status IN ('ACTIVE','RETIRED')
			  AND NOT EXISTS(
				SELECT 1 FROM platform.materialization_snapshots AS snapshot
				WHERE snapshot.materialization_id=materialization.id
			  )
		) SELECT count(*)::integer FROM cleanup_targets`,
		tenantID, datasetID, layer).Scan(&expectedCount); err != nil {
		return 0, err
	}
	// A build that is still queued or running for the deleted dataset must not
	// activate a fresh materialization after the cleanup job has already dropped
	// the old ones. Cancel it in the same transaction; the worker's lease check
	// then rejects any late completion.
	if _, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs
		SET status='CANCELLED',error_code='DATASET_DELETED',
			error_message='dataset deleted before the build completed',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
		WHERE tenant_id=$1 AND dataset_id=$2 AND status IN ('QUEUED','RUNNING')`,
		tenantID, datasetID); err != nil {
		return 0, err
	}
	if expectedCount == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_materialization_cleanup_jobs(
		tenant_id,dataset_id,layer,requested_by,expected_count
	) VALUES($1,$2,$3,$4,$5)
	ON CONFLICT(tenant_id,dataset_id) DO UPDATE SET
		expected_count=EXCLUDED.expected_count
	WHERE platform.dataset_materialization_cleanup_jobs.status='QUEUED'`,
		tenantID, datasetID, layer, actorID, expectedCount); err != nil {
		return 0, err
	}
	return expectedCount, nil
}

// ClaimDatasetMaterializationCleanup leases at most one cleanup task for a
// tenant. Expired RUNNING claims are reclaimed within the bounded retry budget.
func (store *PostgresStore) ClaimDatasetMaterializationCleanup(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (*CleanupClaim, error) {
	if store == nil || store.pool == nil || !validUUID(tenantID) ||
		strings.TrimSpace(workerID) == "" || lease <= 0 {
		return nil, ErrInvalidRequest
	}
	var claim CleanupClaim
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id
			FROM platform.dataset_materialization_cleanup_jobs
			WHERE tenant_id=$1
			  AND attempt<max_attempts
			  AND (
			    (status='QUEUED' AND next_attempt_at<=now())
			    OR (status='RUNNING' AND lease_expires_at<=now())
			  )
			ORDER BY next_attempt_at,created_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE platform.dataset_materialization_cleanup_jobs AS job SET
			status='RUNNING',
			attempt=job.attempt+1,
			started_at=COALESCE(job.started_at,now()),
			lease_owner=$2,
			lease_token=gen_random_uuid(),
			lease_expires_at=now()+($3::bigint*interval '1 millisecond'),
			error_code='',
			error_message=''
		FROM candidate
		WHERE job.id=candidate.id
		RETURNING job.id::text,job.tenant_id::text,job.dataset_id::text,
			job.layer,job.requested_by::text,job.expected_count,
			job.attempt,job.max_attempts,job.lease_token::text`,
			tenantID, workerID, lease.Milliseconds())
		if err := row.Scan(
			&claim.ID, &claim.TenantID, &claim.DatasetID, &claim.Layer,
			&claim.RequestedBy, &claim.ExpectedCount, &claim.Attempt,
			&claim.MaxAttempts, &claim.LeaseToken,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		claim.WorkerID = workerID
		return nil
	})
	if err != nil {
		return nil, err
	}
	if claim.ID == "" {
		return nil, nil
	}
	return &claim, nil
}

// ProcessNextDatasetMaterializationCleanup performs one leased cleanup. DROP
// statements only use identifiers regenerated from immutable UUID metadata.
func (store *PostgresStore) ProcessNextDatasetMaterializationCleanup(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	claim, err := store.ClaimDatasetMaterializationCleanup(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	targets, err := store.loadCleanupTargets(ctx, *claim)
	if err == nil && len(targets) != claim.ExpectedCount {
		err = fmt.Errorf(
			"cleanup target count changed: expected %d, found %d",
			claim.ExpectedCount, len(targets),
		)
	}
	if err == nil {
		err = store.dropCleanupTargets(ctx, *claim, targets)
	}
	if err != nil {
		if failErr := store.failCleanup(ctx, *claim, err); failErr != nil {
			return true, failErr
		}
		return true, nil
	}
	if err := store.completeCleanup(ctx, *claim, len(targets)); err != nil {
		return true, err
	}
	return true, nil
}

func (store *PostgresStore) loadCleanupTargets(
	ctx context.Context,
	claim CleanupClaim,
) ([]cleanupTarget, error) {
	targets := make([]cleanupTarget, 0, claim.ExpectedCount)
	err := database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.dataset_materialization_cleanup_jobs
			WHERE id=$1 AND tenant_id=$2 AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token::text=$4
			  AND lease_expires_at>now()
		)`, claim.ID, claim.TenantID, claim.WorkerID, claim.LeaseToken).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrLeaseLost
		}
		rows, err := tx.Query(ctx, `WITH targets AS (
			SELECT snapshot.build_run_id,snapshot.snapshot_started_at,
			  CASE
				WHEN materialization.status='ACTIVE'
				 AND snapshot.build_run_id=materialization.build_run_id THEN 'ACTIVE'
				ELSE 'RETIRED'
			  END AS cleanup_status,
			  materialization.relation_kind,snapshot.physical_schema,
			  snapshot.physical_name,materialization.published_schema,
			  materialization.published_name
			FROM platform.materialization_snapshots AS snapshot
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.id=snapshot.materialization_id
			 AND materialization.tenant_id=snapshot.tenant_id
			WHERE materialization.tenant_id=$1
			  AND materialization.dataset_id=$2 AND materialization.layer=$3
			  AND materialization.status IN ('ACTIVE','RETIRED')
			UNION ALL
			SELECT materialization.build_run_id,materialization.created_at,
			  materialization.status,materialization.relation_kind,
			  materialization.physical_schema,materialization.physical_name,
			  materialization.published_schema,materialization.published_name
			FROM platform.dataset_materializations AS materialization
			WHERE materialization.tenant_id=$1
			  AND materialization.dataset_id=$2 AND materialization.layer=$3
			  AND materialization.status IN ('ACTIVE','RETIRED')
			  AND NOT EXISTS(
				SELECT 1 FROM platform.materialization_snapshots AS snapshot
				WHERE snapshot.materialization_id=materialization.id
			  )
		) SELECT build_run_id::text,cleanup_status,relation_kind,
			physical_schema,physical_name,published_schema,published_name
			FROM targets
			ORDER BY snapshot_started_at,build_run_id`,
			claim.TenantID, claim.DatasetID, claim.Layer)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target cleanupTarget
			if err := rows.Scan(
				&target.BuildRunID, &target.Status, &target.RelationKind,
				&target.Physical.Schema, &target.Physical.Name,
				&target.Physical.PublishedSchema, &target.Physical.PublishedName,
			); err != nil {
				return err
			}
			if err := ValidatePhysicalIdentifier(
				target.Physical, claim.TenantID, claim.DatasetID,
				target.BuildRunID, claim.Layer,
			); err != nil {
				return fmt.Errorf("validate cleanup target: %w", err)
			}
			if target.RelationKind != "TABLE" &&
				target.RelationKind != "PARTITIONED_TABLE" {
				return fmt.Errorf("%w: cleanup relation kind is invalid", ErrConflict)
			}
			if target.Status != "ACTIVE" && target.Status != "RETIRED" {
				return fmt.Errorf("%w: cleanup materialization status is invalid", ErrConflict)
			}
			targets = append(targets, target)
		}
		return rows.Err()
	})
	return targets, err
}

func (store *PostgresStore) dropCleanupTargets(
	ctx context.Context,
	claim CleanupClaim,
	targets []cleanupTarget,
) error {
	if store.warehousePool == nil {
		return ErrInvalidRequest
	}
	tx, err := store.warehousePool.BeginTx(
		ctx, pgx.TxOptions{IsoLevel: pgx.Serializable},
	)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	for _, target := range targets {
		if err := dropCleanupTargetTx(ctx, tx, target); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func dropCleanupTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	target cleanupTarget,
) error {
	var physicalOID uint32
	var physicalKind string
	var ownedByCurrentUser bool
	err := tx.QueryRow(ctx, `SELECT class.oid,class.relkind::text,
		class.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		target.Physical.Schema, target.Physical.Name).
		Scan(&physicalOID, &physicalKind, &ownedByCurrentUser)
	if errors.Is(err, pgx.ErrNoRows) {
		// A retired materialization may already have been reclaimed by a prior
		// idempotent cleanup/retention pass while the stable view legitimately
		// points at the current ACTIVE build.
		if target.Status == "RETIRED" {
			return nil
		}
		return assertPublishedViewAbsentTx(ctx, tx, target.Physical)
	}
	if err != nil {
		return err
	}
	expectedKind := "r"
	if target.RelationKind == "PARTITIONED_TABLE" {
		expectedKind = "p"
	}
	if physicalKind != expectedKind || !ownedByCurrentUser {
		return fmt.Errorf(
			"%w: cleanup physical relation kind or owner is invalid",
			ErrConflict,
		)
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT
		view_namespace.nspname,view_class.relname,
		view_class.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)
		FROM pg_depend AS dependency
		JOIN pg_rewrite AS rewrite ON rewrite.oid=dependency.objid
		JOIN pg_class AS view_class ON view_class.oid=rewrite.ev_class
		JOIN pg_namespace AS view_namespace
		  ON view_namespace.oid=view_class.relnamespace
		WHERE dependency.refobjid=$1 AND view_class.relkind='v'
		ORDER BY view_namespace.nspname,view_class.relname`, physicalOID)
	if err != nil {
		return err
	}
	type dependentView struct {
		schema string
		name   string
	}
	views := make([]dependentView, 0, 2)
	for rows.Next() {
		var view dependentView
		var owned bool
		if err := rows.Scan(&view.schema, &view.name, &owned); err != nil {
			rows.Close()
			return err
		}
		validStable := view.schema == target.Physical.PublishedSchema &&
			view.name == target.Physical.PublishedName
		validRetired := view.schema == target.Physical.Schema &&
			strings.HasPrefix(view.name, target.Physical.PublishedName+"_r") &&
			physicalNamePattern.MatchString(view.name)
		if !owned || (!validStable && !validRetired) {
			rows.Close()
			return fmt.Errorf(
				"%w: physical table has an unexpected dependent view",
				ErrConflict,
			)
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sort.Slice(views, func(i, j int) bool {
		if views[i].schema == views[j].schema {
			return views[i].name < views[j].name
		}
		return views[i].schema < views[j].schema
	})
	for _, view := range views {
		if _, err := tx.Exec(
			ctx,
			"DROP VIEW "+quoteWarehouseIdentifier(view.schema)+"."+
				quoteWarehouseIdentifier(view.name),
		); err != nil {
			return fmt.Errorf("drop dataset published view: %w", err)
		}
	}
	if _, err := tx.Exec(
		ctx,
		"DROP TABLE "+quoteWarehouseIdentifier(target.Physical.Schema)+"."+
			quoteWarehouseIdentifier(target.Physical.Name),
	); err != nil {
		return fmt.Errorf("drop dataset physical table: %w", err)
	}
	return nil
}

func assertPublishedViewAbsentTx(
	ctx context.Context,
	tx pgx.Tx,
	physical PhysicalIdentifier,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2
	)`, physical.PublishedSchema, physical.PublishedName).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return fmt.Errorf(
			"%w: published view exists without its recorded physical table",
			ErrConflict,
		)
	}
	return nil
}

func (store *PostgresStore) completeCleanup(
	ctx context.Context,
	claim CleanupClaim,
	deletedCount int,
) error {
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_materializations SET
			status='RETIRED',retired_at=COALESCE(retired_at,now())
			WHERE tenant_id=$1 AND dataset_id=$2 AND layer=$3
			  AND status='ACTIVE'`,
			claim.TenantID, claim.DatasetID, claim.Layer)
		if err != nil {
			return err
		}
		_ = tag
		tag, err = tx.Exec(ctx, `UPDATE platform.dataset_materialization_cleanup_jobs SET
			status='SUCCEEDED',deleted_count=$1,completed_at=now(),
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			error_code='',error_message=''
			WHERE id=$2 AND tenant_id=$3 AND status='RUNNING'
			  AND lease_owner=$4 AND lease_token::text=$5
			  AND lease_expires_at>now()`,
			deletedCount, claim.ID, claim.TenantID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(
			$1,$2,'DELETE_WAREHOUSE_MATERIALIZATIONS','DATASET',$3,
			jsonb_build_object(
			  'layer',$4::text,
			  'physicalMaterializationCount',$5::integer,
			  'cleanupJobId',$6::text
			)
		)`, claim.TenantID, claim.RequestedBy, claim.DatasetID, claim.Layer,
			deletedCount, claim.ID)
		return err
	})
}

func (store *PostgresStore) failCleanup(
	ctx context.Context,
	claim CleanupClaim,
	cause error,
) error {
	message := cleanupErrorMessage(cause)
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		status := "QUEUED"
		var completed any
		if claim.Attempt >= claim.MaxAttempts {
			status = "FAILED"
			completed = time.Now().UTC()
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_materialization_cleanup_jobs SET
			status=$1,error_code='PHYSICAL_CLEANUP_FAILED',error_message=$2,
			next_attempt_at=now()+($3::bigint*interval '1 second'),
			completed_at=$4,lease_owner='',lease_token=NULL,lease_expires_at=NULL
			WHERE id=$5 AND tenant_id=$6 AND status='RUNNING'
			  AND lease_owner=$7 AND lease_token::text=$8`,
			status, message, int64(claim.Attempt*claim.Attempt), completed,
			claim.ID, claim.TenantID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func cleanupErrorMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	message = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, message)
	characters := []rune(message)
	if len(characters) > 2048 {
		message = string(characters[:2048])
	}
	return message
}
