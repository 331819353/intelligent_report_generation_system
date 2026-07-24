package datasource

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresMetadataJobRepository 保存批任务和逐表进度；最多十行样本只在 worker 内存中短暂存在。
type PostgresMetadataJobRepository struct{ pool *pgxpool.Pool }

func NewPostgresMetadataJobRepository(pool *pgxpool.Pool) *PostgresMetadataJobRepository {
	return &PostgresMetadataJobRepository{pool: pool}
}

const metadataJobSelect = `j.id::text,j.data_source_id::text,j.kind,j.refresh_mode,
	j.sample_data_mode,j.sample_policy_version,j.status,j.stage,j.total,
	(SELECT count(*)::integer FROM platform.data_source_metadata_job_items i WHERE i.job_id=j.id AND i.status IN ('SUCCEEDED','SKIPPED','FAILED')),
	(SELECT count(*)::integer FROM platform.data_source_metadata_job_items i WHERE i.job_id=j.id AND i.status='SUCCEEDED'),
	(SELECT count(*)::integer FROM platform.data_source_metadata_job_items i WHERE i.job_id=j.id AND i.status='SKIPPED'),
	(SELECT count(*)::integer FROM platform.data_source_metadata_job_items i WHERE i.job_id=j.id AND i.status='FAILED'),
	COALESCE((SELECT i.table_name FROM platform.data_source_metadata_job_items i WHERE i.job_id=j.id AND i.status='RUNNING' ORDER BY i.started_at NULLS LAST,i.id LIMIT 1),''),
	j.error_code,j.error_message,j.created_at::text,COALESCE(j.started_at::text,''),COALESCE(j.completed_at::text,'')`

func (r *PostgresMetadataJobRepository) EnqueueMetadataJob(ctx context.Context, request metadataJobRequest) (job MetadataJob, err error) {
	err = database.WithTenantTx(ctx, r.pool, request.TenantID, func(tx pgx.Tx) error {
		request.SampleDataMode = normalizeMetadataSampleMode(request.SampleDataMode)
		if !request.SampleDataMode.Valid() {
			return ErrSamplePolicyDenied
		}
		var policyMode MetadataSampleMode
		var policyVersion int64
		if err := tx.QueryRow(ctx, `SELECT metadata_sample_mode,version
			FROM platform.ai_tenant_policies
			WHERE tenant_id=platform.current_tenant_id()
			FOR SHARE`).Scan(&policyMode, &policyVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSamplePolicyDenied
			}
			return err
		}
		if metadataSampleModeRank(request.SampleDataMode) >
			metadataSampleModeRank(policyMode) {
			return ErrSamplePolicyDenied
		}
		status, stage := "QUEUED", "QUEUED"
		if len(request.Tables) == 0 {
			status, stage = "SUCCEEDED", "COMPLETE"
		}
		var consentBy, consentAt any
		if request.SampleDataMode != MetadataSampleDeny {
			consentBy = request.RequestedBy
			consentAt = time.Now().UTC()
		}
		var jobID string
		err := tx.QueryRow(ctx, `INSERT INTO platform.data_source_metadata_jobs(
			tenant_id,data_source_id,requested_by,kind,refresh_mode,source_config_hash,
			sample_data_mode,sample_policy_version,sample_consent_by,sample_consent_at,
			status,stage,total,completed_at)
			VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9::uuid,$10,
				$11,$12,$13,CASE WHEN $13=0 THEN now() END)
			RETURNING id::text`, request.TenantID, request.DataSourceID,
			request.RequestedBy, request.Kind, request.Mode, request.SourceConfigHash,
			request.SampleDataMode, policyVersion, consentBy, consentAt,
			status, stage, len(request.Tables)).Scan(&jobID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrMetadataJobActive
			}
			return err
		}
		for _, table := range request.Tables {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_metadata_job_items(
				tenant_id,job_id,catalog_name,schema_name,table_name,table_id,previous_structure_hash,previous_enrichment_status)
				VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8)`, request.TenantID, jobID, table.CatalogName, table.SchemaName, table.TableName, table.TableID, table.StructureHash, table.LatestEnrichmentStatus); err != nil {
				return err
			}
		}
		return scanMetadataJob(tx.QueryRow(ctx, `SELECT `+metadataJobSelect+` FROM platform.data_source_metadata_jobs j WHERE j.id=$1 AND j.data_source_id=$2`, jobID, request.DataSourceID), &job)
	})
	return job, err
}

func (r *PostgresMetadataJobRepository) GetMetadataJob(ctx context.Context, tenantID, sourceID, jobID string) (job MetadataJob, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanMetadataJob(tx.QueryRow(ctx, `SELECT `+metadataJobSelect+` FROM platform.data_source_metadata_jobs j WHERE j.id=$1 AND j.data_source_id=$2`, jobID, sourceID), &job); err != nil {
			return err
		}
		if job.Failed == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT catalog_name,schema_name,table_name,error_code,error_message
			FROM platform.data_source_metadata_job_items
			WHERE job_id=$1 AND status='FAILED'
			ORDER BY created_at,id`, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			failure, err := scanMetadataJobFailure(rows)
			if err != nil {
				return err
			}
			job.Failures = append(job.Failures, failure)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MetadataJob{}, ErrMetadataJobNotFound
	}
	return job, err
}

func scanMetadataJobFailure(row rowScanner) (failure MetadataJobFailure, err error) {
	err = row.Scan(&failure.CatalogName, &failure.SchemaName, &failure.TableName, &failure.ErrorCode, &failure.ErrorMessage)
	return failure, err
}

func (r *PostgresMetadataJobRepository) LatestActiveMetadataJob(ctx context.Context, tenantID, sourceID string) (job *MetadataJob, err error) {
	var item MetadataJob
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanMetadataJob(tx.QueryRow(ctx, `SELECT `+metadataJobSelect+` FROM platform.data_source_metadata_jobs j
			WHERE j.data_source_id=$1 AND j.status IN ('QUEUED','RUNNING') ORDER BY j.created_at DESC LIMIT 1`, sourceID), &item)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ListMetadataJobTenantIDs 只读取未启用 RLS 的租户目录；实际 claim 仍逐租户进入 RLS 事务。
func (r *PostgresMetadataJobRepository) ListMetadataJobTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM platform.tenants WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *PostgresMetadataJobRepository) ClaimMetadataJob(ctx context.Context, tenantID, workerID string, lease time.Duration) (claim *metadataJobClaim, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var jobID string
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id FROM platform.data_source_metadata_jobs
			WHERE status='QUEUED' OR (status='RUNNING' AND lease_expires_at<now())
			ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1
		) UPDATE platform.data_source_metadata_jobs j SET status='RUNNING',stage='DISCOVERY',lease_owner=$1,
			lease_expires_at=now()+($2 * interval '1 second'),heartbeat_at=now(),attempt=attempt+1,started_at=COALESCE(started_at,now())
			FROM candidate WHERE j.id=candidate.id RETURNING j.id::text`, workerID, int64(lease/time.Second)).Scan(&jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// 上一 worker 租约过期时，只重置尚未形成终态的表；成功和失败结果均保留。
		if _, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_job_items SET status='QUEUED',stage='QUEUED',started_at=NULL
			WHERE job_id=$1 AND status='RUNNING'`, jobID); err != nil {
			return err
		}
		var base MetadataJob
		if err := scanMetadataJob(tx.QueryRow(ctx, `SELECT `+metadataJobSelect+` FROM platform.data_source_metadata_jobs j WHERE j.id=$1`, jobID), &base); err != nil {
			return err
		}
		claim = &metadataJobClaim{MetadataJob: base, TenantID: tenantID}
		return tx.QueryRow(ctx, `SELECT COALESCE(requested_by::text,''),source_config_hash FROM platform.data_source_metadata_jobs WHERE id=$1`, jobID).Scan(&claim.RequestedBy, &claim.SourceConfigHash)
	})
	return claim, err
}

func (r *PostgresMetadataJobRepository) ListMetadataJobItems(ctx context.Context, tenantID, jobID string) (items []metadataJobItem, err error) {
	items = []metadataJobItem{}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,catalog_name,schema_name,table_name,COALESCE(table_id::text,''),
			previous_structure_hash,previous_enrichment_status,status
			FROM platform.data_source_metadata_job_items WHERE job_id=$1 ORDER BY created_at,id`, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item metadataJobItem
			if err := rows.Scan(&item.ID, &item.CatalogName, &item.SchemaName, &item.TableName, &item.TableID, &item.PreviousStructureHash, &item.PreviousEnrichmentStatus, &item.Status); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// IsMetadataTableEnriched 只承认当前活动结构对应的成功完善标记，旧结构任务不能触发增量跳过。
func (r *PostgresMetadataJobRepository) IsMetadataTableEnriched(ctx context.Context, tenantID, tableID, structureHash string) (enriched bool, err error) {
	if tableID == "" || structureHash == "" {
		return false, nil
	}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metadata_tables t
			WHERE t.id=$1 AND t.asset_status='ACTIVE' AND t.structure_hash=$2 AND t.last_enriched_structure_hash=$2
			AND t.table_structure_hash=t.last_enriched_table_structure_hash
			AND NOT EXISTS (SELECT 1 FROM platform.metadata_columns c WHERE c.table_id=t.id AND c.asset_status='ACTIVE'
				AND c.last_enriched_structure_hash<>c.structure_hash)
		)`, tableID, structureHash).Scan(&enriched)
	})
	return enriched, err
}

// IsMetadataJobItemCompleted 把恢复凭据限定到当前表任务与精确结构，不能用历史全量完善结果代替。
func (r *PostgresMetadataJobRepository) IsMetadataJobItemCompleted(ctx context.Context, tenantID, itemID, tableID, structureHash string) (completed bool, err error) {
	if itemID == "" || tableID == "" || structureHash == "" {
		return false, nil
	}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.ai_metadata_jobs j
			JOIN platform.metadata_tables t ON t.id=j.table_id AND t.tenant_id=j.tenant_id
			WHERE j.data_source_metadata_job_item_id=$1 AND j.table_id=$2
			AND j.metadata_structure_hash=$3 AND j.status='SUCCEEDED'
			AND t.asset_status='ACTIVE' AND t.structure_hash=$3 AND t.last_enriched_structure_hash=$3
			AND t.table_structure_hash=t.last_enriched_table_structure_hash
			AND NOT EXISTS (SELECT 1 FROM platform.metadata_columns c WHERE c.table_id=t.id AND c.asset_status='ACTIVE'
				AND c.last_enriched_structure_hash<>c.structure_hash)
		)`, itemID, tableID, structureHash).Scan(&completed)
	})
	return completed, err
}

// ValidateMetadataSamplePolicy 在访问业务样本前重新检查冻结授权。DENY 任务从不读取
// 样本，因此租户后续策略变化不应阻断纯技术元数据处理；MASK/RAW 则失败关闭。
func (r *PostgresMetadataJobRepository) ValidateMetadataSamplePolicy(
	ctx context.Context,
	tenantID, jobID string,
	mode MetadataSampleMode,
	policyVersion int64,
) (err error) {
	mode = normalizeMetadataSampleMode(mode)
	if !mode.Valid() || policyVersion < 1 {
		return ErrSamplePolicyChanged
	}
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var frozenMode, currentMode MetadataSampleMode
		var frozenVersion, currentVersion int64
		var consentValid bool
		queryErr := tx.QueryRow(ctx, `SELECT
				job.sample_data_mode,job.sample_policy_version,
				policy.metadata_sample_mode,policy.version,
				(job.sample_data_mode='DENY' OR (
					job.sample_consent_by=job.requested_by
					AND job.sample_consent_at IS NOT NULL
					AND EXISTS(
						SELECT 1 FROM platform.users AS actor
						WHERE actor.id=job.sample_consent_by
						  AND actor.tenant_id=job.tenant_id
						  AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
					)
				))
			FROM platform.data_source_metadata_jobs AS job
			JOIN platform.ai_tenant_policies AS policy
			  ON policy.tenant_id=job.tenant_id
			WHERE job.id=$1
			FOR SHARE OF job,policy`, jobID).Scan(
			&frozenMode, &frozenVersion, &currentMode, &currentVersion, &consentValid,
		)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ErrSamplePolicyChanged
		}
		if queryErr != nil {
			return queryErr
		}
		if frozenMode != mode || frozenVersion != policyVersion || !consentValid {
			return ErrSamplePolicyChanged
		}
		if mode == MetadataSampleDeny {
			return nil
		}
		if currentVersion != policyVersion ||
			metadataSampleModeRank(currentMode) < metadataSampleModeRank(mode) {
			return ErrSamplePolicyChanged
		}
		return nil
	})
	return err
}

func (r *PostgresMetadataJobRepository) HeartbeatMetadataJob(ctx context.Context, tenantID, jobID, workerID string, lease time.Duration) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_jobs SET heartbeat_at=now(),lease_expires_at=now()+($1 * interval '1 second')
			WHERE id=$2 AND status='RUNNING' AND lease_owner=$3 AND lease_expires_at>now()`, int64(lease/time.Second), jobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metadata job lease was lost")
		}
		return nil
	})
}

func (r *PostgresMetadataJobRepository) UpdateMetadataJobStage(ctx context.Context, tenantID, jobID, workerID, stage string, lease time.Duration) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_jobs SET stage=$1,heartbeat_at=now(),lease_expires_at=now()+($2 * interval '1 second')
			WHERE id=$3 AND status='RUNNING' AND lease_owner=$4 AND lease_expires_at>now()`, stage, int64(lease/time.Second), jobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metadata job lease was lost")
		}
		return nil
	})
}

func (r *PostgresMetadataJobRepository) UpdateMetadataJobItem(ctx context.Context, tenantID, jobID, itemID, workerID string, update metadataJobItemUpdate, lease time.Duration) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		terminal := update.Status == "SUCCEEDED" || update.Status == "SKIPPED" || update.Status == "FAILED"
		tag, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_job_items i SET status=$1,stage=$2,
			table_id=COALESCE(NULLIF($3,'')::uuid,table_id),error_code=$4,error_message=$5,
			started_at=CASE WHEN $1='RUNNING' THEN COALESCE(started_at,now()) ELSE started_at END,
			completed_at=CASE WHEN $6 THEN now() ELSE completed_at END
			WHERE i.id=$7 AND i.job_id=$8 AND EXISTS(SELECT 1 FROM platform.data_source_metadata_jobs j
				WHERE j.id=i.job_id AND j.status='RUNNING' AND j.lease_owner=$9 AND j.lease_expires_at>now())`, update.Status, update.Stage, update.TableID, update.ErrorCode, update.ErrorMessage, terminal, itemID, jobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metadata job item update lost its lease")
		}
		return r.updateJobHeartbeat(ctx, tx, jobID, workerID, update.Stage, lease)
	})
}

func (r *PostgresMetadataJobRepository) FinishMetadataJob(ctx context.Context, tenantID, jobID, workerID string) (job MetadataJob, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `WITH counts AS (
			SELECT count(*) FILTER(WHERE status='FAILED')::integer failed,
				count(*) FILTER(WHERE status IN ('SUCCEEDED','SKIPPED'))::integer completed,
				count(*)::integer total FROM platform.data_source_metadata_job_items WHERE job_id=$1
		) UPDATE platform.data_source_metadata_jobs j SET
			status=CASE WHEN counts.failed=0 THEN 'SUCCEEDED' WHEN counts.completed=0 THEN 'FAILED' ELSE 'PARTIAL' END,
			stage=CASE WHEN counts.completed=0 AND counts.failed>0 THEN 'FAILED' ELSE 'COMPLETE' END,
			completed_at=now(),heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			FROM counts WHERE j.id=$1 AND j.status='RUNNING' AND j.lease_owner=$2 AND j.lease_expires_at>now()`, jobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metadata job finalization lost its lease")
		}
		return scanMetadataJob(tx.QueryRow(ctx, `SELECT `+metadataJobSelect+` FROM platform.data_source_metadata_jobs j WHERE j.id=$1`, jobID), &job)
	})
	return job, err
}

func (r *PostgresMetadataJobRepository) FailMetadataJob(ctx context.Context, tenantID, jobID, workerID, code, message string) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_job_items i SET status='FAILED',stage='FAILED',error_code=$1,error_message=$2,completed_at=now()
			WHERE i.job_id=$3 AND i.status IN ('QUEUED','RUNNING') AND EXISTS(SELECT 1 FROM platform.data_source_metadata_jobs j WHERE j.id=i.job_id AND j.status='RUNNING' AND j.lease_owner=$4 AND j.lease_expires_at>now())`, code, message, jobID, workerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `WITH counts AS (
			SELECT count(*) FILTER(WHERE status='FAILED')::integer failed,
				count(*) FILTER(WHERE status IN ('SUCCEEDED','SKIPPED'))::integer completed
			FROM platform.data_source_metadata_job_items WHERE job_id=$3
		) UPDATE platform.data_source_metadata_jobs j SET
			status=CASE WHEN counts.failed=0 THEN 'SUCCEEDED' WHEN counts.completed=0 THEN 'FAILED' ELSE 'PARTIAL' END,
			stage=CASE WHEN counts.failed>0 AND counts.completed=0 THEN 'FAILED' ELSE 'COMPLETE' END,
			error_code=CASE WHEN counts.failed=0 THEN '' ELSE $1 END,
			error_message=CASE WHEN counts.failed=0 THEN '' ELSE $2 END,
			completed_at=now(),heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			FROM counts WHERE j.id=$3 AND j.status='RUNNING' AND j.lease_owner=$4 AND j.lease_expires_at>now()`, code, message, jobID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metadata job failure update lost its lease")
		}
		return nil
	})
}

func (r *PostgresMetadataJobRepository) updateJobHeartbeat(ctx context.Context, tx pgx.Tx, jobID, workerID, stage string, lease time.Duration) error {
	tag, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_jobs SET stage=$1,heartbeat_at=now(),lease_expires_at=now()+($2 * interval '1 second')
		WHERE id=$3 AND status='RUNNING' AND lease_owner=$4 AND lease_expires_at>now()`, stage, int64(lease/time.Second), jobID, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("metadata job lease was lost")
	}
	return nil
}

func scanMetadataJob(row rowScanner, job *MetadataJob) error {
	return row.Scan(&job.ID, &job.DataSourceID, &job.Kind, &job.Mode,
		&job.SampleDataMode, &job.SamplePolicyVersion, &job.Status, &job.Stage, &job.Total,
		&job.Completed, &job.Succeeded, &job.Skipped, &job.Failed, &job.CurrentTable, &job.ErrorCode, &job.ErrorMessage,
		&job.CreatedAt, &job.StartedAt, &job.CompletedAt)
}
