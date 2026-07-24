package materialization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct {
	pool          *pgxpool.Pool
	warehousePool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, warehousePool: pool}
}

func NewPostgresStoreWithWarehouse(
	controlPool, warehousePool *pgxpool.Pool,
) *PostgresStore {
	return &PostgresStore{pool: controlPool, warehousePool: warehousePool}
}

// ListTenantIDs enumerates scheduler partitions only. Every build read and
// mutation still runs inside a tenant-scoped transaction with FORCE RLS.
func (store *PostgresStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantIDs := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tenantIDs, nil
}

func (store *PostgresStore) Register(
	ctx context.Context,
	tenantID, actorID string,
	request RegisterRequest,
) (run Run, created bool, err error) {
	if !validUUID(tenantID) || !validUUID(actorID) {
		return Run{}, false, ErrInvalidRequest
	}
	prepared, err := Prepare(request)
	if err != nil {
		return Run{}, false, err
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		existing, lookupErr := getRunByIdempotencyTx(
			ctx, tx, prepared.Plan.DatasetVersionID, prepared.IdempotencyKey,
		)
		if lookupErr == nil {
			if existing.RequestHash != prepared.RequestHash || existing.RequestedBy != actorID {
				return ErrIdempotencyConflict
			}
			run = existing
			return nil
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return lookupErr
		}

		var storedLayer, versionStatus string
		if err := tx.QueryRow(ctx, `SELECT layer,status
			FROM platform.dataset_versions
			WHERE id::text=$1 AND dataset_id::text=$2
			FOR SHARE`, prepared.Plan.DatasetVersionID, prepared.Plan.DatasetID).
			Scan(&storedLayer, &versionStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if storedLayer != string(prepared.Plan.Layer) || versionStatus != "PUBLISHED" {
			return ErrInvalidRequest
		}

		row := tx.QueryRow(ctx, `INSERT INTO platform.dataset_build_runs(
			tenant_id,dataset_id,dataset_version_id,layer,run_mode,
			plan_version,plan_json,plan_hash,input_snapshot_hash,request_hash,
			idempotency_key,partition_key,requested_by,max_attempts
		) VALUES(
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
		)
		ON CONFLICT(tenant_id,dataset_version_id,idempotency_key) DO NOTHING
		RETURNING `+runSelectColumns,
			tenantID, prepared.Plan.DatasetID, prepared.Plan.DatasetVersionID,
			prepared.Plan.Layer, prepared.Plan.Mode, PlanVersion, prepared.PlanJSON,
			prepared.PlanHash, prepared.InputSnapshotHash, prepared.RequestHash,
			prepared.IdempotencyKey, prepared.PartitionKey, actorID, prepared.MaxAttempts)
		inserted, scanErr := scanRun(row)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			existing, loadErr := getRunByIdempotencyTx(
				ctx, tx, prepared.Plan.DatasetVersionID, prepared.IdempotencyKey,
			)
			if loadErr != nil {
				return loadErr
			}
			if existing.RequestHash != prepared.RequestHash || existing.RequestedBy != actorID {
				return ErrIdempotencyConflict
			}
			run = existing
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		run = inserted
		created = true

		for _, input := range prepared.Inputs {
			snapshot := input.SnapshotJSON
			if len(snapshot) == 0 {
				snapshot = json.RawMessage(`{}`)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.build_run_inputs(
				tenant_id,build_run_id,ordinal_position,source_type,input_layer,
				input_data_source_id,input_data_source_version_id,
				metadata_table_id,file_version_id,input_dataset_id,input_dataset_version_id,
				input_materialization_id,source_version,schema_hash,snapshot_hash,snapshot_json,row_count
			) VALUES(
				$1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,
				NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,
				NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,
				$13,$14,$15,$16,$17
			)`, tenantID, run.ID, input.Ordinal, input.Type, input.Layer,
				input.DataSourceID, input.DataSourceVersionID,
				input.MetadataTableID, input.FileVersionID, input.DatasetID,
				input.DatasetVersionID, input.MaterializationID,
				input.SourceVersion, input.SchemaHash, input.SnapshotHash,
				snapshot, input.RowCount); err != nil {
				return err
			}
		}
		for _, node := range prepared.Plan.Nodes {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.build_node_runs(
				tenant_id,build_run_id,node_id,node_kind,execution_engine
			) VALUES($1,$2,$3,$4,$5)`,
				tenantID, run.ID, node.ID, node.Kind, node.Engine); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Run{}, false, mapStoreError(err)
	}
	return run, created, nil
}

func (store *PostgresStore) Claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *Claim, err error) {
	if !validUUID(tenantID) || !validWorkerID(workerID) ||
		lease < time.Second || lease > 30*time.Minute {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		// Expired final attempt is closed before looking for more work. The run
		// remains an immutable failure record instead of being reclaimed forever.
		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs SET
			status='FAILED',error_code='LEASE_EXPIRED',error_message='worker lease expired',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now() AND attempt>=max_attempts`); err != nil {
			return err
		}

		row := tx.QueryRow(ctx, `WITH picked AS (
			SELECT id,status AS prior_status
			FROM platform.dataset_build_runs
			WHERE attempt<max_attempts AND (
				(status='QUEUED' AND next_attempt_at<=now())
				OR (status='RUNNING' AND lease_expires_at<=now())
			)
			ORDER BY next_attempt_at,created_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		), claimed AS (
			UPDATE platform.dataset_build_runs AS run SET
				status='RUNNING',attempt=run.attempt+1,
				started_at=COALESCE(run.started_at,now()),
				lease_owner=$1,lease_token=gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 millisecond'),
				error_code='',error_message='',updated_at=now()
			FROM picked
			WHERE run.id=picked.id
			RETURNING `+runReturningColumns+`,
				run.plan_json,run.lease_token::text,run.lease_expires_at,picked.prior_status
		)
		SELECT * FROM claimed`, workerID, lease.Milliseconds())

		var item Claim
		var planRaw []byte
		var priorStatus string
		if err := scanClaimRow(row, &item, &planRaw, &priorStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		item.WorkerID = workerID
		plan, err := decodePlan(planRaw, item.PlanHash)
		if err != nil || plan.DatasetID != item.DatasetID ||
			plan.DatasetVersionID != item.DatasetVersionID ||
			plan.Layer != item.Layer || plan.Mode != item.Mode {
			return ErrCorruptPlan
		}
		item.Plan = plan

		if priorStatus == string(RunRunning) {
			// A reclaimed run replays its immutable plan from the beginning.
			// Reset every prior node outcome, not only the node that happened to
			// be RUNNING when the lease expired; otherwise the new worker would
			// hit an invalid transition on already-SUCCEEDED ancestors.
			if _, err := tx.Exec(ctx, `UPDATE platform.build_node_runs SET
				status='PENDING',attempt=0,input_row_count=NULL,output_row_count=NULL,
				output_size_bytes=NULL,error_code='',error_message='',
				started_at=NULL,completed_at=NULL,updated_at=now()
				WHERE build_run_id=$1 AND status<>'PENDING'`, item.ID); err != nil {
				return err
			}
		}
		inputs, err := loadInputsTx(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		item.Inputs = inputs
		if err := (RegisterRequest{
			Plan: item.Plan, Inputs: item.Inputs,
			PartitionKey: item.PartitionKey, MaxAttempts: item.MaxAttempts,
		}).Validate(); err != nil {
			return ErrCorruptPlan
		}
		prepared, err := Prepare(RegisterRequest{
			Plan: item.Plan, Inputs: item.Inputs,
			PartitionKey: item.PartitionKey, MaxAttempts: item.MaxAttempts,
		})
		if err != nil || prepared.InputSnapshotHash != item.InputSnapshotHash ||
			prepared.RequestHash != item.RequestHash || prepared.IdempotencyKey != item.IdempotencyKey {
			return ErrCorruptPlan
		}
		claim = &item
		return nil
	})
	if err != nil {
		return nil, mapStoreError(err)
	}
	return claim, nil
}

// Heartbeat extends a live lease while preserving the random fencing token.
// Concurrent heartbeats from the same worker are harmless; an expired or
// replaced token cannot resurrect the claim.
func (store *PostgresStore) Heartbeat(
	ctx context.Context,
	claim Claim,
	lease time.Duration,
) (Claim, error) {
	if err := validateClaim(claim); err != nil ||
		lease < time.Second || lease > 30*time.Minute {
		return Claim{}, ErrInvalidRequest
	}
	var expiresAt time.Time
	err := database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE platform.dataset_build_runs SET
			lease_expires_at=GREATEST(
				lease_expires_at,
				clock_timestamp()+($1*interval '1 millisecond')
			),
			updated_at=now()
			WHERE id=$2 AND status='RUNNING' AND lease_owner=$3
			  AND lease_token::text=$4 AND lease_expires_at>clock_timestamp()
			RETURNING lease_expires_at`,
			lease.Milliseconds(), claim.ID, claim.WorkerID, claim.LeaseToken).
			Scan(&expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	})
	if err != nil {
		return Claim{}, mapStoreError(err)
	}
	claim.LeaseExpiresAt = expiresAt
	return claim, nil
}

func (store *PostgresStore) StartNode(ctx context.Context, claim Claim, nodeID string) error {
	if err := validateClaim(claim); err != nil || !nodeIDPattern.MatchString(nodeID) {
		return ErrInvalidRequest
	}
	return mapStoreError(database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.build_node_runs AS node SET
			status='RUNNING',attempt=node.attempt+1,started_at=now(),completed_at=NULL,
			error_code='',error_message='',updated_at=now()
			WHERE node.build_run_id=$1 AND node.node_id=$2 AND node.status='PENDING'
			  AND EXISTS(
				SELECT 1 FROM platform.dataset_build_runs AS run
				WHERE run.id=node.build_run_id AND run.status='RUNNING'
				  AND run.lease_owner=$3 AND run.lease_token::text=$4
				  AND run.lease_expires_at>now()
			  )`, claim.ID, nodeID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		return classifyLeaseOrTransitionTx(ctx, tx, claim, nodeID)
	}))
}

func (store *PostgresStore) FinishNode(
	ctx context.Context,
	claim Claim,
	nodeID string,
	result NodeResult,
) error {
	if err := validateClaim(claim); err != nil || !nodeIDPattern.MatchString(nodeID) {
		return ErrInvalidRequest
	}
	if err := validateNodeResult(result); err != nil {
		return err
	}
	return mapStoreError(database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.build_node_runs AS node SET
			status=$1,input_row_count=$2,output_row_count=$3,output_size_bytes=$4,
			error_code=$5,error_message=$6,completed_at=now(),updated_at=now()
			WHERE node.build_run_id=$7 AND node.node_id=$8 AND node.status='RUNNING'
			  AND EXISTS(
				SELECT 1 FROM platform.dataset_build_runs AS run
				WHERE run.id=node.build_run_id AND run.status='RUNNING'
				  AND run.lease_owner=$9 AND run.lease_token::text=$10
				  AND run.lease_expires_at>now()
			  )`, result.Status, result.InputRowCount, result.OutputRowCount,
			result.OutputSizeBytes, result.ErrorCode, result.ErrorMessage,
			claim.ID, nodeID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		return classifyLeaseOrTransitionTx(ctx, tx, claim, nodeID)
	}))
}

func (store *PostgresStore) Fail(
	ctx context.Context,
	claim Claim,
	code, message string,
	quality []QualityResult,
) error {
	if err := validateClaim(claim); err != nil ||
		strings.TrimSpace(code) == "" || len(code) > 128 || len(message) > 4096 {
		return ErrInvalidRequest
	}
	if err := validateQuality(quality); err != nil {
		return err
	}
	return mapStoreError(database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := assertLeaseTx(ctx, tx, claim); err != nil {
			return err
		}
		if err := insertQualityTx(ctx, tx, claim, "", quality); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.build_node_runs SET
			status='FAILED',error_code='BUILD_ABORTED',error_message=$1,
			completed_at=now(),updated_at=now()
			WHERE build_run_id=$2 AND status='RUNNING'`, message, claim.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs SET
			status='FAILED',error_code=$1,error_message=$2,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE id=$3 AND status='RUNNING' AND lease_owner=$4
			  AND lease_token::text=$5 AND lease_expires_at>now()`,
			code, message, claim.ID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	}))
}

func (store *PostgresStore) Activate(
	ctx context.Context,
	claim Claim,
	activation Activation,
) (materialization Materialization, err error) {
	if err := validateClaim(claim); err != nil {
		return Materialization{}, err
	}
	if err := ValidatePhysicalIdentifier(
		activation.Physical, claim.TenantID, claim.DatasetID, claim.ID, claim.Layer,
	); err != nil {
		return Materialization{}, err
	}
	if activation.RelationKind != claim.Plan.Target.RelationKind ||
		!hashPattern.MatchString(activation.SchemaHash) ||
		!hashPattern.MatchString(activation.SnapshotHash) ||
		activation.RowCount < 0 || activation.SizeBytes < 0 {
		return Materialization{}, ErrInvalidRequest
	}
	if err := validateBoundedObject(activation.Watermark, 64<<10); err != nil {
		return Materialization{}, err
	}
	if err := validateQuality(activation.Quality); err != nil {
		return Materialization{}, err
	}

	gateFailed := qualityGateFailed(activation.Quality)
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		// Migration 000071 installs the same tenant-scoped gate as a
		// BEFORE STATEMENT trigger on dataset_materializations. Take it before
		// the lease, version, publication advisory, or ACTIVE-row locks so an
		// API/worker activation cannot invert the trigger's gate -> row order.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtextextended(
			  'semantic-governance-write:'||platform.current_tenant_id()::text,
			  0
			)
		)`); err != nil {
			return err
		}
		if err := assertLeaseTx(ctx, tx, claim); err != nil {
			return err
		}
		var versionLayer, versionStatus, versionSchemaHash string
		if err := tx.QueryRow(ctx, `SELECT layer,status,schema_hash
			FROM platform.dataset_versions
			WHERE id=$1 AND dataset_id=$2 FOR SHARE`,
			claim.DatasetVersionID, claim.DatasetID).
			Scan(&versionLayer, &versionStatus, &versionSchemaHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if versionLayer != string(claim.Layer) ||
			versionStatus != "PUBLISHED" ||
			versionSchemaHash != activation.SchemaHash {
			return ErrConflict
		}

		var totalNodes, incompleteNodes int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int,
			count(*) FILTER(WHERE status NOT IN ('SUCCEEDED','SKIPPED'))::int
			FROM platform.build_node_runs WHERE build_run_id=$1`,
			claim.ID).Scan(&totalNodes, &incompleteNodes); err != nil {
			return err
		}
		if totalNodes != len(claim.Plan.Nodes) || incompleteNodes != 0 {
			return ErrInvalidTransition
		}
		if gateFailed {
			if err := insertQualityTx(ctx, tx, claim, "", activation.Quality); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs SET
				status='FAILED',error_code='QUALITY_GATE_FAILED',
				error_message='one or more ERROR quality rules failed',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
				WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
				  AND lease_token::text=$3 AND lease_expires_at>now()`,
				claim.ID, claim.WorkerID, claim.LeaseToken)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrLeaseLost
			}
			return nil
		}

		// One dataset has one stable published view. Serialize activation even
		// when two first-time builds see no ACTIVE metadata row to lock.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtextextended($1,0)
		)`, "dataset-publication:"+claim.TenantID+":"+claim.DatasetID); err != nil {
			return err
		}
		var previousBuildRunID, previousPhysicalSchema string
		previousErr := tx.QueryRow(ctx, `SELECT build_run_id::text,physical_schema
			FROM platform.dataset_materializations
			WHERE dataset_id=$1 AND status='ACTIVE'
			FOR UPDATE`, claim.DatasetID).Scan(&previousBuildRunID, &previousPhysicalSchema)
		if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
			return previousErr
		}

		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_materializations SET
			status='RETIRED',retired_at=now()
			WHERE dataset_id=$1 AND status='ACTIVE'`, claim.DatasetID); err != nil {
			return err
		}
		watermark := activation.Watermark
		if len(watermark) == 0 {
			watermark = json.RawMessage(`{}`)
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.dataset_materializations(
			tenant_id,dataset_id,dataset_version_id,build_run_id,layer,status,
			relation_kind,refresh_mode,physical_schema,physical_name,
			published_schema,published_name,schema_hash,snapshot_hash,
			row_count,size_bytes,watermark_json,activated_at
		) VALUES(
			$1,$2,$3,$4,$5,'ACTIVE',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now()
		)
		RETURNING id::text,tenant_id::text,dataset_id::text,dataset_version_id::text,
			build_run_id::text,layer,status,physical_schema,physical_name,
			published_schema,published_name,schema_hash,snapshot_hash,row_count,size_bytes,activated_at`,
			claim.TenantID, claim.DatasetID, claim.DatasetVersionID, claim.ID, claim.Layer,
			activation.RelationKind, claim.Mode, activation.Physical.Schema, activation.Physical.Name,
			activation.Physical.PublishedSchema, activation.Physical.PublishedName,
			activation.SchemaHash, activation.SnapshotHash, activation.RowCount,
			activation.SizeBytes, watermark)
		if err := row.Scan(
			&materialization.ID, &materialization.TenantID, &materialization.DatasetID,
			&materialization.DatasetVersionID, &materialization.BuildRunID,
			&materialization.Layer, &materialization.Status,
			&materialization.Physical.Schema, &materialization.Physical.Name,
			&materialization.Physical.PublishedSchema, &materialization.Physical.PublishedName,
			&materialization.SchemaHash, &materialization.SnapshotHash,
			&materialization.RowCount, &materialization.SizeBytes, &materialization.ActivatedAt,
		); err != nil {
			return err
		}
		if err := insertQualityTx(ctx, tx, claim, materialization.ID, activation.Quality); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs SET
			status='SUCCEEDED',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
			  AND lease_token::text=$3 AND lease_expires_at>now()`,
			claim.ID, claim.WorkerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		if err := store.switchPublishedView(
			ctx, tx, activation.Physical, activation.RelationKind,
			claim.ID, previousBuildRunID, previousPhysicalSchema,
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Materialization{}, mapStoreError(err)
	}
	if gateFailed {
		return Materialization{}, ErrQualityGateFailed
	}
	return materialization, nil
}

func (store *PostgresStore) switchPublishedView(
	ctx context.Context,
	controlTx pgx.Tx,
	physical PhysicalIdentifier,
	relationKind string,
	currentRunID string,
	previousBuildRunID string,
	previousPhysicalSchema string,
) error {
	if store.warehousePool == nil {
		return ErrInvalidRequest
	}
	if store.warehousePool == store.pool {
		return switchPublishedViewTx(
			ctx, controlTx, physical, relationKind, currentRunID,
			previousBuildRunID, previousPhysicalSchema,
		)
	}
	warehouseTx, err := store.warehousePool.BeginTx(
		ctx, pgx.TxOptions{IsoLevel: pgx.Serializable},
	)
	if err != nil {
		return err
	}
	defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := switchPublishedViewTx(
		ctx, warehouseTx, physical, relationKind, currentRunID,
		previousBuildRunID, previousPhysicalSchema,
	); err != nil {
		return err
	}
	return warehouseTx.Commit(ctx)
}

func switchPublishedViewTx(
	ctx context.Context,
	tx pgx.Tx,
	physical PhysicalIdentifier,
	relationKind string,
	currentRunID string,
	previousBuildRunID string,
	previousPhysicalSchema string,
) error {
	expectedKind := "r"
	if relationKind == "PARTITIONED_TABLE" {
		expectedKind = "p"
	}
	var physicalKind string
	var ownedByCurrentUser bool
	if err := tx.QueryRow(ctx, `SELECT class.relkind::text,
		class.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		physical.Schema, physical.Name).Scan(&physicalKind, &ownedByCurrentUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: materialized physical relation does not exist", ErrConflict)
		}
		return err
	}
	if physicalKind != expectedKind || !ownedByCurrentUser {
		return fmt.Errorf("%w: materialized physical relation kind or owner is invalid", ErrConflict)
	}

	var publishedKind string
	publishedErr := tx.QueryRow(ctx, `SELECT class.relkind::text
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		physical.PublishedSchema, physical.PublishedName).Scan(&publishedKind)
	hasPublishedView := publishedErr == nil
	if publishedErr != nil && !errors.Is(publishedErr, pgx.ErrNoRows) {
		return publishedErr
	}
	if hasPublishedView && publishedKind != "v" {
		return fmt.Errorf("%w: stable published relation is not a view", ErrConflict)
	}
	if (previousBuildRunID == "") != !hasPublishedView {
		return fmt.Errorf("%w: published view and active metadata are inconsistent", ErrConflict)
	}
	if previousBuildRunID != "" &&
		(previousPhysicalSchema != "warehouse_ods" &&
			previousPhysicalSchema != "warehouse_dwd" &&
			previousPhysicalSchema != "warehouse_dws") {
		return fmt.Errorf("%w: previous materialization schema is invalid", ErrConflict)
	}

	nextName, retiredName, err := publicationSwapNames(physical, currentRunID, previousBuildRunID)
	if err != nil {
		return err
	}
	qualifiedPhysical := quoteWarehouseIdentifier(physical.Schema) + "." + quoteWarehouseIdentifier(physical.Name)
	qualifiedNext := quoteWarehouseIdentifier(physical.PublishedSchema) + "." + quoteWarehouseIdentifier(nextName)
	if _, err := tx.Exec(ctx, "CREATE VIEW "+qualifiedNext+" AS SELECT * FROM "+qualifiedPhysical); err != nil {
		return fmt.Errorf("create next published view: %w", err)
	}
	if hasPublishedView {
		qualifiedPublished := quoteWarehouseIdentifier(physical.PublishedSchema) + "." + quoteWarehouseIdentifier(physical.PublishedName)
		if _, err := tx.Exec(ctx, "ALTER VIEW "+qualifiedPublished+" RENAME TO "+quoteWarehouseIdentifier(retiredName)); err != nil {
			return fmt.Errorf("retire prior published view name: %w", err)
		}
		qualifiedRetired := quoteWarehouseIdentifier(physical.PublishedSchema) + "." + quoteWarehouseIdentifier(retiredName)
		if _, err := tx.Exec(ctx, "ALTER VIEW "+qualifiedRetired+" SET SCHEMA "+quoteWarehouseIdentifier(previousPhysicalSchema)); err != nil {
			return fmt.Errorf("move prior published view out of the API schema: %w", err)
		}
	}
	if _, err := tx.Exec(
		ctx,
		"ALTER VIEW "+qualifiedNext+" RENAME TO "+quoteWarehouseIdentifier(physical.PublishedName),
	); err != nil {
		return fmt.Errorf("activate stable published view: %w", err)
	}
	return nil
}

func quoteWarehouseIdentifier(identifier string) string {
	return `"` + identifier + `"`
}

const runSelectColumns = `id::text,tenant_id::text,dataset_id::text,dataset_version_id::text,
	layer,run_mode,status,plan_hash,input_snapshot_hash,request_hash,idempotency_key,
	partition_key,requested_by::text,attempt,max_attempts,created_at,updated_at,
	started_at,completed_at,error_code,error_message`

const runReturningColumns = `run.id::text,run.tenant_id::text,run.dataset_id::text,
	run.dataset_version_id::text,run.layer,run.run_mode,run.status,run.plan_hash,
	run.input_snapshot_hash,run.request_hash,run.idempotency_key,run.partition_key,
	run.requested_by::text,run.attempt,run.max_attempts,run.created_at,run.updated_at,
	run.started_at,run.completed_at,run.error_code,run.error_message`

type rowScanner interface {
	Scan(...any) error
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var startedAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&run.ID, &run.TenantID, &run.DatasetID, &run.DatasetVersionID,
		&run.Layer, &run.Mode, &run.Status, &run.PlanHash,
		&run.InputSnapshotHash, &run.RequestHash, &run.IdempotencyKey,
		&run.PartitionKey, &run.RequestedBy, &run.Attempt, &run.MaxAttempts,
		&run.CreatedAt, &run.UpdatedAt, &startedAt, &completedAt,
		&run.ErrorCode, &run.ErrorMessage,
	)
	if err != nil {
		return Run{}, err
	}
	run.StartedAt = timePointer(startedAt)
	run.CompletedAt = timePointer(completedAt)
	return run, nil
}

func scanClaimRow(
	row rowScanner,
	claim *Claim,
	planRaw *[]byte,
	priorStatus *string,
) error {
	var startedAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&claim.ID, &claim.TenantID, &claim.DatasetID, &claim.DatasetVersionID,
		&claim.Layer, &claim.Mode, &claim.Status, &claim.PlanHash,
		&claim.InputSnapshotHash, &claim.RequestHash, &claim.IdempotencyKey,
		&claim.PartitionKey, &claim.RequestedBy, &claim.Attempt, &claim.MaxAttempts,
		&claim.CreatedAt, &claim.UpdatedAt, &startedAt, &completedAt,
		&claim.ErrorCode, &claim.ErrorMessage, planRaw, &claim.LeaseToken,
		&claim.LeaseExpiresAt, priorStatus,
	)
	if err != nil {
		return err
	}
	claim.StartedAt = timePointer(startedAt)
	claim.CompletedAt = timePointer(completedAt)
	return nil
}

func getRunByIdempotencyTx(
	ctx context.Context,
	tx pgx.Tx,
	datasetVersionID, key string,
) (Run, error) {
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runSelectColumns+`
		FROM platform.dataset_build_runs
		WHERE dataset_version_id::text=$1 AND idempotency_key=$2`,
		datasetVersionID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

func loadInputsTx(ctx context.Context, tx pgx.Tx, runID string) ([]InputSnapshot, error) {
	rows, err := tx.Query(ctx, `SELECT ordinal_position,source_type,input_layer,
		input_data_source_id,input_data_source_version_id,
		metadata_table_id,file_version_id,input_dataset_id,input_dataset_version_id,
		input_materialization_id,source_version,schema_hash,snapshot_hash,snapshot_json,row_count
		FROM platform.build_run_inputs
		WHERE build_run_id=$1 ORDER BY ordinal_position`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []InputSnapshot{}
	for rows.Next() {
		var input InputSnapshot
		var dataSourceID, dataSourceVersionID, metadataTableID, fileVersionID pgtype.UUID
		var datasetID, datasetVersionID, materializationID pgtype.UUID
		var rowCount pgtype.Int8
		if err := rows.Scan(
			&input.Ordinal, &input.Type, &input.Layer,
			&dataSourceID, &dataSourceVersionID,
			&metadataTableID, &fileVersionID, &datasetID, &datasetVersionID,
			&materializationID, &input.SourceVersion, &input.SchemaHash,
			&input.SnapshotHash, &input.SnapshotJSON, &rowCount,
		); err != nil {
			return nil, err
		}
		input.DataSourceID = uuidText(dataSourceID)
		input.DataSourceVersionID = uuidText(dataSourceVersionID)
		input.MetadataTableID = uuidText(metadataTableID)
		input.FileVersionID = uuidText(fileVersionID)
		input.DatasetID = uuidText(datasetID)
		input.DatasetVersionID = uuidText(datasetVersionID)
		input.MaterializationID = uuidText(materializationID)
		if rowCount.Valid {
			value := rowCount.Int64
			input.RowCount = &value
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func assertLeaseTx(ctx context.Context, tx pgx.Tx, claim Claim) error {
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.dataset_build_runs
		WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
		  AND lease_token::text=$3 AND lease_expires_at>now()
		FOR UPDATE
	)`, claim.ID, claim.WorkerID, claim.LeaseToken).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrLeaseLost
	}
	return nil
}

func classifyLeaseOrTransitionTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	nodeID string,
) error {
	if err := assertLeaseTx(ctx, tx, claim); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.build_node_runs WHERE build_run_id=$1 AND node_id=$2
	)`, claim.ID, nodeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrInvalidTransition
}

func insertQualityTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	materializationID string,
	results []QualityResult,
) error {
	for _, result := range results {
		var nodeRunID string
		if result.NodeID != "" {
			if err := tx.QueryRow(ctx, `SELECT id::text
				FROM platform.build_node_runs
				WHERE build_run_id=$1 AND node_id=$2`,
				claim.ID, result.NodeID).Scan(&nodeRunID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
		}
		expectation := result.Expectation
		if len(expectation) == 0 {
			expectation = json.RawMessage(`{}`)
		}
		observed := result.Observed
		if len(observed) == 0 {
			observed = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_quality_results(
			tenant_id,build_run_id,build_node_run_id,materialization_id,
			rule_code,rule_version,rule_definition_hash,scope,field_id,
			severity,status,expectation_json,observed_json,message
		) VALUES(
			$1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,
			$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
		)`, claim.TenantID, claim.ID, nodeRunID, materializationID,
			result.RuleCode, result.RuleVersion, result.RuleDefinitionHash,
			result.Scope, result.FieldID, result.Severity, result.Status,
			expectation, observed, result.Message); err != nil {
			return err
		}
	}
	return nil
}

func validateClaim(claim Claim) error {
	if !validUUID(claim.ID) || !validUUID(claim.TenantID) ||
		!validUUID(claim.DatasetID) || !validUUID(claim.DatasetVersionID) ||
		!validUUID(claim.LeaseToken) || !validWorkerID(claim.WorkerID) ||
		claim.Status != RunRunning || claim.PlanHash == "" {
		return ErrInvalidRequest
	}
	return nil
}

func validWorkerID(workerID string) bool {
	return len(workerID) >= 1 && len(workerID) <= 128 &&
		workerID == strings.TrimSpace(workerID) &&
		!strings.ContainsAny(workerID, "\x00\r\n\t")
}

func uuidText(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func mapStoreError(err error) error {
	if err == nil ||
		errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrIdempotencyConflict) ||
		errors.Is(err, ErrLeaseLost) ||
		errors.Is(err, ErrInvalidTransition) ||
		errors.Is(err, ErrQualityGateFailed) ||
		errors.Is(err, ErrCorruptPlan) {
		return err
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pgError.ConstraintName)
		case "23503":
			return ErrNotFound
		case "23514", "22P02":
			return ErrInvalidRequest
		}
	}
	return err
}
