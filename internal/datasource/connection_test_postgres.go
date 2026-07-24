package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresConnectionTestRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresConnectionTestRepository(pool *pgxpool.Pool) *PostgresConnectionTestRepository {
	return &PostgresConnectionTestRepository{pool: pool}
}

const connectionTestJobProjection = `job.id::text,job.data_source_id::text,
	job.data_source_version_id::text,job.status,job.attempt,job.max_attempts,
	job.error_code,job.error_message,COALESCE(attestation.server_version,''),
	COALESCE(attestation.latency_ms,0),job.created_at,job.started_at,job.completed_at,
	attestation.completed_at,attestation.expires_at`

const connectionTestJobFrom = ` FROM platform.data_source_connection_test_jobs AS job
	LEFT JOIN platform.data_source_connection_test_attestations AS attestation
	  ON attestation.connection_test_job_id=job.id
	 AND attestation.tenant_id=job.tenant_id`

func (r *PostgresConnectionTestRepository) EnqueueConnectionTest(
	ctx context.Context,
	tenantID, sourceID, requestedBy, idempotencyKeyHash string,
) (job ConnectionTestJob, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var jobID string
		if err := tx.QueryRow(ctx,
			`SELECT platform.enqueue_data_source_connection_test(
				$1::uuid,NULLIF($2,'')::uuid,NULLIF($3,'')
			)::text`,
			sourceID, requestedBy, idempotencyKeyHash,
		).Scan(&jobID); err != nil {
			return err
		}
		return scanConnectionTestJob(tx.QueryRow(ctx,
			`SELECT `+connectionTestJobProjection+connectionTestJobFrom+`
			WHERE job.id=$1 AND job.data_source_id=$2`,
			jobID, sourceID,
		), &job)
	})
	return job, err
}

func (r *PostgresConnectionTestRepository) GetConnectionTest(
	ctx context.Context,
	tenantID, sourceID, jobID string,
) (job ConnectionTestJob, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanConnectionTestJob(tx.QueryRow(ctx,
			`SELECT `+connectionTestJobProjection+connectionTestJobFrom+`
			WHERE job.id=$1 AND job.data_source_id=$2`,
			jobID, sourceID,
		), &job)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectionTestJob{}, ErrConnectionTestNotFound
	}
	return job, err
}

func (r *PostgresConnectionTestRepository) LatestConnectionTest(
	ctx context.Context,
	tenantID, sourceID, configVersionID, configHash string,
) (job *ConnectionTestJob, err error) {
	var item ConnectionTestJob
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return scanConnectionTestJob(tx.QueryRow(ctx,
			`SELECT `+connectionTestJobProjection+connectionTestJobFrom+`
			WHERE job.data_source_id=$1
			  AND job.data_source_version_id=$2
			  AND job.config_hash=$3
			ORDER BY job.created_at DESC,job.id DESC
			LIMIT 1`,
			sourceID, configVersionID, configHash,
		), &item)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func scanConnectionTestJob(row rowScanner, job *ConnectionTestJob) error {
	return row.Scan(
		&job.ID, &job.DataSourceID, &job.ConfigVersionID, &job.Status,
		&job.Attempt, &job.MaxAttempts, &job.ErrorCode, &job.ErrorMessage,
		&job.ServerVersion, &job.LatencyMS, &job.RequestedAt, &job.StartedAt,
		&job.CompletedAt, &job.TestedAt, &job.ExpiresAt,
	)
}

func (r *PostgresConnectionTestRepository) ListConnectionTestTenantIDs(
	ctx context.Context,
) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT tenant_id::text
		FROM platform.list_connection_test_job_tenant_ids() AS tenant_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantIDs := make([]string, 0)
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	return tenantIDs, rows.Err()
}

func (r *PostgresConnectionTestRepository) ClaimConnectionTest(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *ConnectionTestClaim, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var (
			jobID, sourceID, versionID, configHash string
			sourceType                             string
			configJSON                             []byte
			secretRef, fileAssetID, fileVersionID  string
			maxExcelFileBytes                      int64
			leaseToken                             string
			attempt, maxAttempts                   int
		)
		scanErr := tx.QueryRow(ctx,
			`SELECT job_id::text,tenant_id::text,data_source_id::text,
				data_source_version_id::text,config_hash,source_type,config,
				secret_ref,file_asset_id,file_version_id,max_excel_file_bytes,
				lease_token::text,attempt,max_attempts
			FROM platform.claim_data_source_connection_test($1,$2)`,
			workerID, durationSeconds(lease),
		).Scan(
			&jobID, &tenantID, &sourceID, &versionID, &configHash,
			&sourceType, &configJSON, &secretRef, &fileAssetID, &fileVersionID,
			&maxExcelFileBytes, &leaseToken, &attempt, &maxAttempts,
		)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		source := Source{
			ID:              sourceID,
			TenantID:        tenantID,
			Type:            Type(sourceType),
			SecretRef:       secretRef,
			FileAssetID:     fileAssetID,
			FileVersionID:   fileVersionID,
			ConfigVersionID: versionID,
			ConfigHash:      configHash,
			RuntimeQuota: Quota{
				MaxExcelFileBytes: maxExcelFileBytes,
			},
		}
		if err := json.Unmarshal(configJSON, &source.Config); err != nil {
			return err
		}
		claim = &ConnectionTestClaim{
			Job: ConnectionTestJob{
				ID: jobID, DataSourceID: sourceID, ConfigVersionID: versionID,
				Status: ConnectionTestRunning, Attempt: attempt, MaxAttempts: maxAttempts,
			},
			TenantID: tenantID, LeaseToken: leaseToken, Source: source,
		}
		return nil
	})
	return claim, err
}

func (r *PostgresConnectionTestRepository) HeartbeatConnectionTest(
	ctx context.Context,
	tenantID, jobID, leaseToken string,
	lease time.Duration,
) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var renewed bool
		if err := tx.QueryRow(ctx,
			`SELECT platform.heartbeat_data_source_connection_test(
				$1::uuid,$2::uuid,$3
			)`,
			jobID, leaseToken, durationSeconds(lease),
		).Scan(&renewed); err != nil {
			return err
		}
		if !renewed {
			return ErrConnectionTestLeaseLost
		}
		return nil
	})
}

func (r *PostgresConnectionTestRepository) CompleteConnectionTest(
	ctx context.Context,
	tenantID, jobID, leaseToken, serverVersion string,
	latencyMS int64,
) error {
	return database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var completed bool
		if err := tx.QueryRow(ctx,
			`SELECT platform.complete_data_source_connection_test(
				$1::uuid,$2::uuid,$3,$4
			)`,
			jobID, leaseToken, serverVersion, latencyMS,
		).Scan(&completed); err != nil {
			return err
		}
		if !completed {
			return ErrConnectionTestLeaseLost
		}
		return nil
	})
}

func (r *PostgresConnectionTestRepository) FailConnectionTest(
	ctx context.Context,
	tenantID, jobID, leaseToken, errorCode string,
	retryable bool,
) (status ConnectionTestJobStatus, err error) {
	err = database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var value string
		if err := tx.QueryRow(ctx,
			`SELECT platform.fail_data_source_connection_test(
				$1::uuid,$2::uuid,$3,$4
			)`,
			jobID, leaseToken, errorCode, retryable,
		).Scan(&value); err != nil {
			return err
		}
		if value == "" {
			return ErrConnectionTestLeaseLost
		}
		status = ConnectionTestJobStatus(value)
		return nil
	})
	return status, err
}

func durationSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
