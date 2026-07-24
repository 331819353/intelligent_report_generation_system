package semanticmanagement

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
)

const refreshJobColumns = `job.id::text,job.dimension_id::text,job.dimension_version,
	job.dataset_id::text,job.dataset_version_id::text,job.field_id,job.field_code,
	job.member_index_policy,COALESCE(job.materialization_id::text,''),
	job.refresh_generation::text,job.status,job.max_members,job.timeout_seconds,
	job.request_hash,job.requested_by::text,job.attempt,job.max_attempts,
	job.member_count,job.result_code,job.error_message,job.created_at,job.updated_at,
	job.started_at,job.completed_at`

func (s *PostgresStore) CreateRefreshJob(
	ctx context.Context,
	tenantID, actorID string,
	prepared PreparedRefreshJob,
) (item RefreshJob, created bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		replayed, found, replayErr := getRefreshJobByKey(
			ctx, tx, prepared.DimensionID, prepared.IdempotencyKey,
		)
		if replayErr != nil {
			return replayErr
		}
		if found {
			if replayed.RequestHash != prepared.RequestHash {
				return ErrIdempotencyConflict
			}
			item = replayed
			return nil
		}

		var dimensionVersion int64
		var datasetID, datasetVersionID, fieldID, fieldCode, policy, status string
		var sensitive bool
		err = tx.QueryRow(ctx, `SELECT dataset_id::text,
				dataset_version_id::text,field_id
			FROM platform.semantic_dimensions
			WHERE id=$1::uuid`, prepared.DimensionID).Scan(
			&datasetID, &datasetVersionID, &fieldID,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := lockDimensionGovernanceScope(
			ctx, tx, datasetID, datasetVersionID, fieldID,
		); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT dimension.version,dimension.dataset_id::text,
				dimension.dataset_version_id::text,dimension.field_id,field.field_code::text,
				dimension.member_index_policy,dimension.status,dimension.sensitive
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dimension.tenant_id
			  AND version.id=dimension.dataset_version_id
			  AND version.dataset_id=dimension.dataset_id
			  AND version.layer='DWS' AND version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			  AND dataset.id=version.dataset_id
			  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
			  AND dataset.current_published_version_id=version.id
			  AND dataset.deleted_at IS NULL
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			  AND field.dataset_version_id=dimension.dataset_version_id
			  AND field.field_id=dimension.field_id
			  AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
			WHERE dimension.id=$1::uuid FOR SHARE OF dimension`,
			prepared.DimensionID).Scan(
			&dimensionVersion, &datasetID, &datasetVersionID, &fieldID,
			&fieldCode, &policy, &status, &sensitive,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if dimensionVersion != prepared.ExpectedDimensionVersion || status != "PUBLISHED" ||
			(sensitive && policy == "FULL") {
			return ErrConflict
		}

		jobStatus, resultCode := "QUEUED", ""
		var materializationID any
		completedAt := any(nil)
		switch policy {
		case "FULL":
			var id string
			err := tx.QueryRow(ctx, `SELECT materialization.id::text
				FROM platform.dataset_materializations AS materialization
				JOIN platform.dimension_profile_jobs AS profile
				  ON profile.tenant_id=materialization.tenant_id
				  AND profile.dataset_id=materialization.dataset_id
				  AND profile.dataset_version_id=materialization.dataset_version_id
				  AND profile.materialization_id=materialization.id
				  AND profile.schema_hash=materialization.schema_hash
				  AND profile.materialization_snapshot_hash=
				    materialization.snapshot_hash
				  AND profile.field_id=$3
				  AND profile.profile_version='dws-dimension-profile-v1'
				  AND profile.policy_version='dimension-member-policy-v1'
				  AND profile.status='SUCCEEDED'
				  AND profile.recommended_member_index_policy='FULL'
				WHERE materialization.dataset_id=$1::uuid
				  AND materialization.dataset_version_id=$2::uuid
				  AND materialization.layer='DWS'
				  AND materialization.status='ACTIVE'
				FOR SHARE OF materialization,profile`,
				datasetID, datasetVersionID, fieldID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			materializationID = id
		case "EXACT_ONLY":
			jobStatus, resultCode, completedAt = "SKIPPED", "EXACT_ONLY_AUTOMATIC_DISCOVERY_SKIPPED", time.Now().UTC()
		case "NONE":
			jobStatus, resultCode, completedAt = "SKIPPED", "MEMBER_INDEX_DISABLED", time.Now().UTC()
		default:
			return ErrConflict
		}

		var id string
		err = tx.QueryRow(ctx, `INSERT INTO platform.dimension_member_refresh_jobs(
				tenant_id,dimension_id,dimension_version,dataset_id,dataset_version_id,
				field_id,field_code,member_index_policy,materialization_id,status,
				max_members,timeout_seconds,request_hash,idempotency_key,requested_by,
				result_code,completed_at
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,
				$10,$11,$12,$13,$14,$15,$16
			)
			ON CONFLICT(tenant_id,dimension_id,idempotency_key) DO NOTHING
			RETURNING id::text`,
			prepared.DimensionID, dimensionVersion, datasetID, datasetVersionID,
			fieldID, fieldCode, policy, materializationID, jobStatus,
			prepared.MaxMembers, prepared.TimeoutSeconds, prepared.RequestHash,
			prepared.IdempotencyKey, actorID, resultCode, completedAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			replayed, found, replayErr = getRefreshJobByKey(
				ctx, tx, prepared.DimensionID, prepared.IdempotencyKey,
			)
			if replayErr != nil {
				return replayErr
			}
			if found && replayed.RequestHash == prepared.RequestHash {
				item = replayed
				return nil
			}
			return ErrIdempotencyConflict
		}
		if err != nil {
			return mapWriteError(err)
		}
		created = true
		if err := auditMutation(ctx, tx, actorID, "DIMENSION_MEMBER_REFRESH_REQUEST", "DIMENSION_MEMBER_REFRESH_JOB", id,
			map[string]any{"dimensionId": prepared.DimensionID, "status": jobStatus}); err != nil {
			return err
		}
		item, err = getRefreshJob(ctx, tx, id)
		return err
	})
	return item, created, err
}

func (s *PostgresStore) ListRefreshJobs(ctx context.Context, tenantID string, filter RefreshJobFilter) (items []RefreshJob, total int, err error) {
	items = []RefreshJob{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+refreshJobColumns+`,count(*) OVER()::int
			FROM platform.dimension_member_refresh_jobs AS job
			WHERE job.tenant_id=platform.current_tenant_id()
			  AND ($1='' OR job.dimension_id::text=$1)
			  AND ($2='' OR job.status=$2)
			ORDER BY job.created_at DESC,job.id
			LIMIT $3 OFFSET $4`,
			filter.DimensionID, filter.Status, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item RefreshJob
			if err := scanRefreshJob(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) ListRefreshTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM platform.tenants
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

func (s *PostgresStore) ClaimDimensionRefresh(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *DimensionRefreshClaim, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.dimension_member_refresh_jobs
			SET status='FAILED',result_code='LEASE_EXPIRED',
				error_message='dimension member refresh lease expired',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT id FROM platform.dimension_member_refresh_jobs
				WHERE member_index_policy='FULL' AND attempt<max_attempts
				  AND (
				    (status='QUEUED' AND next_attempt_at<=now())
				    OR (status='RUNNING' AND lease_expires_at<=now())
				  )
				ORDER BY created_at,id
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE platform.dimension_member_refresh_jobs AS job SET
				status='RUNNING',attempt=attempt+1,
				started_at=COALESCE(started_at,now()),completed_at=NULL,
				lease_owner=$1,lease_token=gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				member_count=NULL,result_code='',error_message=''
			FROM candidate WHERE job.id=candidate.id
			RETURNING `+refreshJobColumns+`,
				job.tenant_id::text,job.lease_owner,job.lease_token::text,job.lease_expires_at`,
			workerID, int64(lease/time.Second))
		candidate := DimensionRefreshClaim{}
		if err := scanRefreshClaim(row, &candidate); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		claim = &candidate
		return nil
	})
	return claim, err
}

func (s *PostgresStore) RefreshDimensionMembers(
	ctx context.Context,
	claim DimensionRefreshClaim,
	workerID string,
) (returnErr error) {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, cleanupErr := connection.Exec(cleanupCtx, `DROP TABLE IF EXISTS
			pg_temp.dimension_member_refresh_stage_raw,
			pg_temp.dimension_member_refresh_stage`)
		if cleanupErr == nil {
			return
		}
		// A pooled session must never be returned with another tenant's staged
		// member values. Closing the underlying connection makes Release discard it.
		_ = connection.Conn().Close(cleanupCtx)
		if returnErr == nil {
			returnErr = cleanupErr
		}
	}()

	tx, err := connection.BeginTx(
		ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted},
	)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), 5*time.Second,
		)
		defer cancel()
		rollbackErr := tx.Rollback(rollbackCtx)
		if rollbackErr == nil || errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return
		}
		// A transaction that cannot be rolled back must not leave its session in
		// the pool. The outer cleanup will make one final bounded attempt and then
		// discard the same physical connection.
		_ = connection.Conn().Close(rollbackCtx)
		if returnErr == nil {
			returnErr = rollbackErr
		}
	}()
	if err := configureDimensionRefreshTransaction(ctx, tx, claim); err != nil {
		return classifyRefreshDatabaseError(err)
	}
	physicalTx := tx
	separatedWarehouse := s.warehousePool != nil && s.warehousePool != s.pool
	if separatedWarehouse {
		warehouseTx, beginErr := s.warehousePool.BeginTx(
			ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly},
		)
		if beginErr != nil {
			return beginErr
		}
		defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
		if err := configureDimensionRefreshWarehouseTransaction(
			ctx, warehouseTx, claim,
		); err != nil {
			return classifyRefreshDatabaseError(err)
		}
		physicalTx = warehouseTx
	}
	source, count, err := scanDimensionMembersToStage(
		ctx, tx, physicalTx, claim, workerID, s.dimensionRefreshScanHook,
	)
	if err != nil {
		return classifyRefreshDatabaseError(err)
	}
	if separatedWarehouse {
		physical := source.physicalIdentifier()
		if err := lockAndVerifyPublishedView(ctx, physicalTx, physical); err != nil {
			return classifyRefreshDatabaseError(err)
		}
		if err := requirePublishedColumn(
			ctx, physicalTx, physical, source.FieldCode,
		); err != nil {
			return classifyRefreshDatabaseError(err)
		}
	}
	if err := mergeDimensionMemberStage(
		ctx, tx, claim, workerID, source, count, !separatedWarehouse,
	); err != nil {
		return classifyRefreshDatabaseError(err)
	}
	// The package-private hook makes it possible for integration tests to prove
	// that the physical SHARE lock still covers the late-gate merge and the
	// generation commit. Production stores leave it nil.
	if s.dimensionRefreshCommitHook != nil {
		if err := s.dimensionRefreshCommitHook(ctx); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return classifyRefreshDatabaseError(err)
	}
	return nil
}

type dimensionRefreshSource struct {
	DimensionVersion  int64
	DatasetID         string
	DatasetVersionID  string
	FieldID           string
	FieldCode         string
	MemberIndexPolicy string
	DimensionStatus   string
	Sensitive         bool
	MaterializationID string
	BuildRunID        string
	Layer             string
	Materialization   string
	SchemaHash        string
	SnapshotHash      string
	ProfileID         string
	PhysicalSchema    string
	PhysicalName      string
	PublishedSchema   string
	PublishedName     string
}

func scanDimensionMembersToStage(
	ctx context.Context,
	controlTx, physicalTx pgx.Tx,
	claim DimensionRefreshClaim,
	workerID string,
	scanHook func(context.Context) error,
) (source dimensionRefreshSource, count int, returnErr error) {
	if err := verifyRefreshLease(ctx, controlTx, claim, workerID, false); err != nil {
		return source, 0, err
	}
	source, err := loadDimensionRefreshSource(ctx, controlTx, claim, false)
	if err != nil {
		return source, 0, err
	}
	physical := source.physicalIdentifier()
	if err := lockAndVerifyPhysicalTable(ctx, physicalTx, physical); err != nil {
		return source, 0, err
	}
	if err := requirePhysicalColumn(ctx, physicalTx, physical, source.FieldCode); err != nil {
		return source, 0, err
	}
	// The hook is intentionally package-private and used only by deterministic
	// PostgreSQL concurrency tests. Production stores leave it nil.
	if scanHook != nil {
		if err := scanHook(ctx); err != nil {
			return source, 0, err
		}
	}

	if _, err := controlTx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS
			dimension_member_refresh_stage_raw(
				member_key text NOT NULL,
				canonical_label text NOT NULL,
				normalized_value text NOT NULL,
				source_value_hash text NOT NULL
				  CHECK(source_value_hash ~ '^[0-9a-f]{64}$')
			) ON COMMIT PRESERVE ROWS`); err != nil {
		return source, 0, err
	}
	if _, err := controlTx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS
			dimension_member_refresh_stage(
				member_key text NOT NULL,
				canonical_label text NOT NULL,
				normalized_value text PRIMARY KEY,
				source_value_hash text NOT NULL
				  CHECK(source_value_hash ~ '^[0-9a-f]{64}$')
			) ON COMMIT PRESERVE ROWS`); err != nil {
		return source, 0, err
	}
	if _, err := controlTx.Exec(ctx, `TRUNCATE
			dimension_member_refresh_stage_raw,
			dimension_member_refresh_stage`); err != nil {
		return source, 0, err
	}

	qualified := quoteTrustedIdentifier(source.PhysicalSchema) + "." +
		quoteTrustedIdentifier(source.PhysicalName)
	quotedField := quoteTrustedIdentifier(source.FieldCode)
	cursorSQL := `DECLARE dimension_member_values NO SCROLL CURSOR FOR
		SELECT DISTINCT ` + quotedField + `::text COLLATE "C" AS member_value
		FROM ` + qualified + `
		WHERE ` + quotedField + ` IS NOT NULL
		ORDER BY member_value`
	if _, err := physicalTx.Exec(ctx, cursorSQL); err != nil {
		return source, 0, err
	}

	for {
		rows, err := physicalTx.Query(ctx, `FETCH FORWARD 1000 FROM dimension_member_values`)
		if err != nil {
			return source, 0, err
		}
		batch := make([][]any, 0, 1000)
		rawFetched := 0
		for rows.Next() {
			rawFetched++
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return source, 0, err
			}
			memberKey, label, normalized, valueHash, err :=
				prepareDimensionMemberValue(raw)
			if err != nil {
				rows.Close()
				return source, 0, err
			}
			batch = append(batch, []any{
				memberKey, label, normalized, valueHash,
			})
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return source, 0, rowsErr
		}
		if rawFetched == 0 {
			break
		}
		if _, err := controlTx.CopyFrom(
			ctx,
			pgx.Identifier{"dimension_member_refresh_stage_raw"},
			[]string{
				"member_key", "canonical_label", "normalized_value",
				"source_value_hash",
			},
			pgx.CopyFromRows(batch),
		); err != nil {
			return source, 0, err
		}
		remainingWithOverflowSentinel := claim.MaxMembers - count + 1
		tag, err := controlTx.Exec(ctx, `INSERT INTO dimension_member_refresh_stage(
				member_key,canonical_label,normalized_value,source_value_hash
			)
			SELECT canonical.member_key,canonical.canonical_label,
				canonical.normalized_value,canonical.source_value_hash
			FROM (
				SELECT DISTINCT ON (raw.normalized_value)
					raw.member_key,raw.canonical_label,raw.normalized_value,
					raw.source_value_hash
				FROM dimension_member_refresh_stage_raw AS raw
				ORDER BY raw.normalized_value,raw.member_key COLLATE "C"
			) AS canonical
			WHERE NOT EXISTS(
				SELECT 1 FROM dimension_member_refresh_stage AS staged
				WHERE staged.normalized_value=canonical.normalized_value
			)
			ORDER BY canonical.normalized_value
			LIMIT $1
			ON CONFLICT(normalized_value) DO NOTHING`,
			remainingWithOverflowSentinel)
		if err != nil {
			return source, 0, err
		}
		count += int(tag.RowsAffected())
		if _, err := controlTx.Exec(
			ctx, `TRUNCATE dimension_member_refresh_stage_raw`,
		); err != nil {
			return source, 0, err
		}
		if count > claim.MaxMembers {
			return source, 0, ErrRefreshCardinality
		}
	}
	if _, err := physicalTx.Exec(ctx, `CLOSE dimension_member_values`); err != nil {
		return source, 0, err
	}

	// NFKC/case variants are normalized in a bounded 1000-row Go batch and
	// deduplicated into a max+1 PostgreSQL stage after each fetch. Because the
	// source cursor is globally C-sorted, ON CONFLICT keeps the deterministic
	// first raw spelling across batches. The scratch table never exceeds 1000
	// rows and the worker never retains 100k member keys in its heap.
	if _, err := controlTx.Exec(ctx, `DROP TABLE
		dimension_member_refresh_stage_raw`); err != nil {
		return source, 0, err
	}
	return source, count, nil
}

func mergeDimensionMemberStage(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionRefreshClaim,
	workerID string,
	scanned dimensionRefreshSource,
	count int,
	validatePhysical bool,
) error {
	if err := lockDimensionGovernanceScope(
		ctx, tx, claim.DatasetID, claim.DatasetVersionID, claim.FieldID,
	); err != nil {
		return err
	}
	if err := verifyRefreshLease(ctx, tx, claim, workerID, true); err != nil {
		return err
	}
	current, err := loadDimensionRefreshSource(ctx, tx, claim, true)
	if err != nil {
		return err
	}
	if current != scanned {
		return ErrRefreshSourceChanged
	}
	if validatePhysical {
		physical := current.physicalIdentifier()
		if err := lockAndVerifyPublishedView(ctx, tx, physical); err != nil {
			return err
		}
		if err := requirePublishedColumn(ctx, tx, physical, current.FieldCode); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `INSERT INTO platform.dimension_members(
			tenant_id,dimension_id,member_key,canonical_label,normalized_value,
			source_value_hash,status,refresh_generation,last_refresh_job_id
		)
		SELECT platform.current_tenant_id(),$1::uuid,stage.member_key,
			stage.canonical_label,stage.normalized_value,stage.source_value_hash,
			'ACTIVE',$2::uuid,$3::uuid
		FROM dimension_member_refresh_stage AS stage
		WHERE NOT EXISTS(
			SELECT 1 FROM platform.dimension_members AS member
			WHERE member.dimension_id=$1::uuid
			  AND member.normalized_value=stage.normalized_value
		)`, claim.DimensionID, claim.RefreshGeneration, claim.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dimension_members AS member SET
			member_key=stage.member_key,canonical_label=stage.canonical_label,
			source_value_hash=stage.source_value_hash,status='ACTIVE',
			last_seen_at=now(),refresh_generation=$2::uuid,
			last_refresh_job_id=$3::uuid
		FROM dimension_member_refresh_stage AS stage
		WHERE member.dimension_id=$1::uuid
		  AND member.normalized_value=stage.normalized_value
		  AND ROW(
		    member.member_key,member.canonical_label,
		    member.source_value_hash,member.status
		  ) IS DISTINCT FROM ROW(
		    stage.member_key,stage.canonical_label,
		    stage.source_value_hash,'ACTIVE'
		  )`,
		claim.DimensionID, claim.RefreshGeneration, claim.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dimension_members AS member SET
			last_seen_at=now(),refresh_generation=$2::uuid,
			last_refresh_job_id=$3::uuid,updated_at=now()
		FROM dimension_member_refresh_stage AS stage
		WHERE member.dimension_id=$1::uuid
		  AND member.normalized_value=stage.normalized_value
		  AND member.refresh_generation IS DISTINCT FROM $2::uuid`,
		claim.DimensionID, claim.RefreshGeneration, claim.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dimension_members AS member SET
			status='DEPRECATED',refresh_generation=$2::uuid,
			last_refresh_job_id=$3::uuid,updated_at=now()
		WHERE member.dimension_id=$1::uuid AND member.status='ACTIVE'
		  AND NOT EXISTS(
			SELECT 1 FROM dimension_member_refresh_stage AS stage
			WHERE stage.normalized_value=member.normalized_value
		  )`, claim.DimensionID, claim.RefreshGeneration, claim.ID); err != nil {
		return err
	}

	dimensionTag, err := tx.Exec(ctx, `UPDATE platform.semantic_dimensions SET
			member_refresh_generation=$1::uuid,member_count=$2,
			member_refreshed_at=now(),last_member_refresh_job_id=$3::uuid
		WHERE id=$4::uuid AND version=$5 AND status='PUBLISHED'
		  AND dataset_version_id=$6::uuid AND field_id=$7`,
		claim.RefreshGeneration, count, claim.ID, claim.DimensionID,
		claim.DimensionVersion, claim.DatasetVersionID, claim.FieldID)
	if err != nil {
		return err
	}
	if dimensionTag.RowsAffected() != 1 {
		return ErrRefreshSourceChanged
	}
	jobTag, err := tx.Exec(ctx, `UPDATE platform.dimension_member_refresh_jobs SET
			status='SUCCEEDED',member_count=$1,result_code='',error_message='',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,completed_at=now()
		WHERE id=$2::uuid AND status='RUNNING' AND lease_owner=$3
		  AND lease_token=$4::uuid AND lease_expires_at>now()`,
		count, claim.ID, workerID, claim.LeaseToken)
	if err != nil {
		return err
	}
	if jobTag.RowsAffected() != 1 {
		return ErrRefreshLeaseLost
	}
	if err := auditMutation(
		ctx, tx, claim.RequestedBy,
		"DIMENSION_MEMBER_REFRESH_COMPLETE", "DIMENSION_MEMBER_REFRESH_JOB",
		claim.ID,
		map[string]any{
			"dimensionId":       claim.DimensionID,
			"memberCount":       count,
			"materializationId": current.MaterializationID,
			"schemaHash":        current.SchemaHash,
			"snapshotHash":      current.SnapshotHash,
			"profileId":         current.ProfileID,
		},
	); err != nil {
		return err
	}
	return nil
}

func configureDimensionRefreshTransaction(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionRefreshClaim,
) error {
	if _, err := tx.Exec(
		ctx, `SELECT set_config('app.tenant_id',$1,true)`, claim.TenantID,
	); err != nil {
		return err
	}
	timeoutMS := strconv.Itoa(claim.TimeoutSeconds * 1000)
	lockTimeoutSeconds := min(claim.TimeoutSeconds, 5)
	if lockTimeoutSeconds < 1 {
		lockTimeoutSeconds = 1
	}
	_, err := tx.Exec(ctx, `SELECT
		set_config('statement_timeout',$1,true),
		set_config('lock_timeout',$2,true)`,
		timeoutMS, strconv.Itoa(lockTimeoutSeconds*1000))
	return err
}

func configureDimensionRefreshWarehouseTransaction(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionRefreshClaim,
) error {
	timeoutMS := strconv.Itoa(claim.TimeoutSeconds * 1000)
	lockTimeoutSeconds := min(claim.TimeoutSeconds, 5)
	if lockTimeoutSeconds < 1 {
		lockTimeoutSeconds = 1
	}
	_, err := tx.Exec(ctx, `SELECT
		set_config('statement_timeout',$1,true),
		set_config('lock_timeout',$2,true)`,
		timeoutMS, strconv.Itoa(lockTimeoutSeconds*1000))
	return err
}

func loadDimensionRefreshSource(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionRefreshClaim,
	lock bool,
) (source dimensionRefreshSource, err error) {
	lockClause := ""
	if lock {
		lockClause = " FOR SHARE OF dimension,materialization,profile"
	}
	err = tx.QueryRow(ctx, `SELECT
			dimension.version,dimension.dataset_id::text,
			dimension.dataset_version_id::text,dimension.field_id,
			field.field_code::text,dimension.member_index_policy,
			dimension.status,dimension.sensitive,
			materialization.id::text,materialization.build_run_id::text,
			materialization.layer,materialization.status,
			materialization.schema_hash,materialization.snapshot_hash,
			profile.id::text,materialization.physical_schema,
			materialization.physical_name,materialization.published_schema,
			materialization.published_name
		FROM platform.semantic_dimensions AS dimension
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=dimension.tenant_id
		  AND version.id=dimension.dataset_version_id
		  AND version.dataset_id=dimension.dataset_id
		  AND version.layer='DWS' AND version.status='PUBLISHED'
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id
		  AND dataset.id=version.dataset_id
		  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
		  AND dataset.current_published_version_id=version.id
		  AND dataset.deleted_at IS NULL
		JOIN platform.dataset_fields AS field
		  ON field.tenant_id=dimension.tenant_id
		  AND field.dataset_version_id=dimension.dataset_version_id
		  AND field.field_id=dimension.field_id
		  AND field.field_role IN (
		    'DIMENSION','ATTRIBUTE','TIME','IDENTIFIER'
		  )
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.tenant_id=dimension.tenant_id
		  AND materialization.id=$2::uuid
		  AND materialization.dataset_id=dimension.dataset_id
		  AND materialization.dataset_version_id=dimension.dataset_version_id
		JOIN platform.dimension_profile_jobs AS profile
		  ON profile.tenant_id=materialization.tenant_id
		  AND profile.dataset_id=materialization.dataset_id
		  AND profile.dataset_version_id=materialization.dataset_version_id
		  AND profile.materialization_id=materialization.id
		  AND profile.schema_hash=materialization.schema_hash
		  AND profile.materialization_snapshot_hash=materialization.snapshot_hash
		  AND profile.field_id=dimension.field_id
		  AND profile.profile_version='dws-dimension-profile-v1'
		  AND profile.policy_version='dimension-member-policy-v1'
		  AND profile.status='SUCCEEDED'
		  AND profile.recommended_member_index_policy='FULL'
		WHERE dimension.id=$1::uuid`+lockClause,
		claim.DimensionID, claim.MaterializationID).Scan(
		&source.DimensionVersion, &source.DatasetID, &source.DatasetVersionID,
		&source.FieldID, &source.FieldCode, &source.MemberIndexPolicy,
		&source.DimensionStatus, &source.Sensitive,
		&source.MaterializationID, &source.BuildRunID, &source.Layer,
		&source.Materialization, &source.SchemaHash, &source.SnapshotHash,
		&source.ProfileID, &source.PhysicalSchema, &source.PhysicalName,
		&source.PublishedSchema, &source.PublishedName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, ErrRefreshSourceChanged
	}
	if err != nil {
		return source, err
	}
	if source.DimensionVersion != claim.DimensionVersion ||
		source.DatasetID != claim.DatasetID ||
		source.DatasetVersionID != claim.DatasetVersionID ||
		source.FieldID != claim.FieldID || source.FieldCode != claim.FieldCode ||
		source.MemberIndexPolicy != "FULL" ||
		source.DimensionStatus != "PUBLISHED" || source.Sensitive ||
		source.MaterializationID != claim.MaterializationID ||
		source.Layer != "DWS" || source.Materialization != "ACTIVE" ||
		materialization.ValidatePhysicalIdentifier(
			source.physicalIdentifier(), claim.TenantID, claim.DatasetID,
			source.BuildRunID, materialization.LayerDWS,
		) != nil {
		return source, ErrRefreshSourceChanged
	}
	return source, nil
}

func (source dimensionRefreshSource) physicalIdentifier() materialization.PhysicalIdentifier {
	return materialization.PhysicalIdentifier{
		Schema: source.PhysicalSchema, Name: source.PhysicalName,
		PublishedSchema: source.PublishedSchema,
		PublishedName:   source.PublishedName,
	}
}

func (s *PostgresStore) FailDimensionRefresh(
	ctx context.Context,
	claim DimensionRefreshClaim,
	code, message string,
) error {
	retryable := code == "REFRESH_TIMEOUT" || code == "REFRESH_FAILED"
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		targetStatus := "FAILED"
		if retryable && claim.Attempt < claim.MaxAttempts {
			targetStatus = "QUEUED"
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dimension_member_refresh_jobs SET
				status=$1,
				next_attempt_at=CASE WHEN $1='QUEUED'
				  THEN now()+(LEAST(attempt,5)*interval '30 seconds')
				  ELSE next_attempt_at END,
				result_code=CASE WHEN $1='FAILED' THEN $2 ELSE '' END,
				error_message=CASE WHEN $1='FAILED' THEN $3 ELSE '' END,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=CASE WHEN $1='FAILED' THEN now() ELSE NULL END
			WHERE id=$4::uuid AND status='RUNNING' AND lease_owner=$5
			  AND lease_token=$6::uuid AND lease_expires_at>now()`,
			targetStatus, code, message, claim.ID, claim.LeaseOwner, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRefreshLeaseLost
		}
		return nil
	})
}

func getRefreshJobByKey(
	ctx context.Context,
	tx pgx.Tx,
	dimensionID, idempotencyKey string,
) (RefreshJob, bool, error) {
	item, err := scanRefreshJobValue(tx.QueryRow(ctx, `SELECT `+refreshJobColumns+`
		FROM platform.dimension_member_refresh_jobs AS job
		WHERE job.dimension_id=$1::uuid AND job.idempotency_key=$2`,
		dimensionID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshJob{}, false, nil
	}
	return item, err == nil, err
}

func getRefreshJob(ctx context.Context, tx pgx.Tx, id string) (RefreshJob, error) {
	return scanRefreshJobValue(tx.QueryRow(ctx, `SELECT `+refreshJobColumns+`
		FROM platform.dimension_member_refresh_jobs AS job WHERE job.id=$1::uuid`, id))
}

func verifyRefreshLease(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionRefreshClaim,
	workerID string,
	lock bool,
) error {
	sql := `SELECT 1 FROM platform.dimension_member_refresh_jobs
		WHERE id=$1::uuid AND status='RUNNING' AND lease_owner=$2
		  AND lease_token=$3::uuid AND lease_expires_at>now()`
	if lock {
		sql += ` FOR UPDATE`
	}
	var one int
	err := tx.QueryRow(ctx, sql, claim.ID, workerID, claim.LeaseToken).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRefreshLeaseLost
	}
	return err
}

func lockAndVerifyPhysicalTable(
	ctx context.Context,
	tx pgx.Tx,
	identifier materialization.PhysicalIdentifier,
) error {
	qualified := quoteTrustedIdentifier(identifier.Schema) + "." +
		quoteTrustedIdentifier(identifier.Name)
	// Run-scoped materialization tables are immutable by lifecycle contract and
	// owned by the trusted worker role. SHARE also fences accidental DML for the
	// duration of this scan instead of relying on that contract alone.
	if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN SHARE MODE"); err != nil {
		return ErrRefreshUnsafeView
	}
	var relationKind string
	var ownedByWorker bool
	err := tx.QueryRow(ctx, `SELECT relation.relkind::text,
			relation.relowner=(
			  SELECT usesysid FROM pg_user WHERE usename=current_user
			)
		FROM pg_class AS relation
		JOIN pg_namespace AS namespace
		  ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname=$1 AND relation.relname=$2`,
		identifier.Schema, identifier.Name).
		Scan(&relationKind, &ownedByWorker)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRefreshUnsafeView
	}
	if err != nil {
		return err
	}
	if relationKind != "r" || !ownedByWorker {
		return ErrRefreshUnsafeView
	}
	return nil
}

func requirePhysicalColumn(
	ctx context.Context,
	tx pgx.Tx,
	identifier materialization.PhysicalIdentifier,
	fieldCode string,
) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM pg_attribute AS attribute
		JOIN pg_class AS relation ON relation.oid=attribute.attrelid
		JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname=$1 AND relation.relname=$2
		  AND relation.relkind='r' AND attribute.attname=$3
		  AND attribute.attnum>0 AND NOT attribute.attisdropped
	)`, identifier.Schema, identifier.Name, fieldCode).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrRefreshSourceChanged
	}
	return nil
}

func lockAndVerifyPublishedView(
	ctx context.Context,
	tx pgx.Tx,
	identifier materialization.PhysicalIdentifier,
) error {
	qualified := quoteTrustedIdentifier(identifier.PublishedSchema) + "." +
		quoteTrustedIdentifier(identifier.PublishedName)
	if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN ACCESS SHARE MODE"); err != nil {
		return ErrRefreshUnsafeView
	}
	var viewKind string
	var sameOwner, ownedByWorker, exactDependency bool
	err := tx.QueryRow(ctx, `SELECT view.relkind::text,
			view.relowner=physical.relowner,
			view.relowner=(SELECT usesysid FROM pg_user WHERE usename=current_user),
			COALESCE((
				SELECT count(DISTINCT dependency.refobjid)=1
				  AND bool_and(dependency.refobjid=physical.oid)
				FROM pg_rewrite AS rewrite
				JOIN pg_depend AS dependency ON dependency.objid=rewrite.oid
				  AND dependency.classid='pg_rewrite'::regclass
				  AND dependency.refclassid='pg_class'::regclass
				WHERE rewrite.ev_class=view.oid
				  AND dependency.refobjid<>view.oid
			),false)
		FROM pg_class AS view
		JOIN pg_namespace AS view_namespace ON view_namespace.oid=view.relnamespace
		JOIN pg_class AS physical ON physical.relname=$3
		JOIN pg_namespace AS physical_namespace
		  ON physical_namespace.oid=physical.relnamespace
		  AND physical_namespace.nspname=$4
		WHERE view_namespace.nspname=$1 AND view.relname=$2`,
		identifier.PublishedSchema, identifier.PublishedName,
		identifier.Name, identifier.Schema).
		Scan(&viewKind, &sameOwner, &ownedByWorker, &exactDependency)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRefreshUnsafeView
	}
	if err != nil {
		return err
	}
	if viewKind != "v" || !sameOwner || !ownedByWorker || !exactDependency {
		return ErrRefreshUnsafeView
	}
	return nil
}

func requirePublishedColumn(
	ctx context.Context,
	tx pgx.Tx,
	identifier materialization.PhysicalIdentifier,
	fieldCode string,
) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM pg_attribute AS attribute
		JOIN pg_class AS relation ON relation.oid=attribute.attrelid
		JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname=$1 AND relation.relname=$2
		  AND relation.relkind='v' AND attribute.attname=$3
		  AND attribute.attnum>0 AND NOT attribute.attisdropped
	)`, identifier.PublishedSchema, identifier.PublishedName, fieldCode).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrRefreshSourceChanged
	}
	return nil
}

func prepareDimensionMemberValue(raw string) (string, string, string, string, error) {
	if raw != strings.TrimSpace(raw) || !utf8.ValidString(raw) {
		return "", "", "", "", ErrRefreshInvalidValue
	}
	length := utf8.RuneCountInString(raw)
	if length < 1 || length > 1024 {
		return "", "", "", "", ErrRefreshInvalidValue
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return "", "", "", "", ErrRefreshInvalidValue
		}
	}
	normalized := normalizeSearchValue(raw)
	if !validText(normalized, 1, 1024) {
		return "", "", "", "", ErrRefreshInvalidValue
	}
	digest := sha256.Sum256([]byte(raw))
	return raw, raw, normalized, fmt.Sprintf("%x", digest), nil
}

func classifyRefreshDatabaseError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrRefreshTimeout
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "57014", "55P03":
			return ErrRefreshTimeout
		case "23505", "22001", "22P02":
			return ErrRefreshInvalidValue
		}
	}
	return err
}

func quoteTrustedIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func scanRefreshJob(row scanRow, item *RefreshJob, extra ...any) error {
	targets := []any{
		&item.ID, &item.DimensionID, &item.DimensionVersion, &item.DatasetID,
		&item.DatasetVersionID, &item.FieldID, &item.FieldCode,
		&item.MemberIndexPolicy, &item.MaterializationID, &item.RefreshGeneration,
		&item.Status, &item.MaxMembers, &item.TimeoutSeconds, &item.RequestHash,
		&item.RequestedBy, &item.Attempt, &item.MaxAttempts, &item.MemberCount,
		&item.ResultCode, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt,
		&item.StartedAt, &item.CompletedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanRefreshJobValue(row scanRow) (item RefreshJob, err error) {
	err = scanRefreshJob(row, &item)
	return item, err
}

func scanRefreshClaim(row scanRow, claim *DimensionRefreshClaim) error {
	return scanRefreshJob(row, &claim.RefreshJob,
		&claim.TenantID, &claim.LeaseOwner, &claim.LeaseToken, &claim.LeaseExpiresAt)
}
