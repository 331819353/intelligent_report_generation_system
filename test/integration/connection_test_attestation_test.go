//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestConnectionTestAttestationRoleBoundaryAndPublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appPool := connectionTestPool(
		t, ctx, env(
			"DATABASE_URL",
			"postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable",
		),
	)
	workerPool := connectionTestPool(
		t, ctx, env(
			"WORKER_DATABASE_URL",
			"postgres://report_worker:local_worker_password@127.0.0.1:5432/intelligent_report?sslmode=disable",
		),
	)
	testerPool := connectionTestPool(
		t, ctx, env(
			"CONNECTION_TEST_DATABASE_URL",
			"postgres://report_connection_tester:local_connection_test_password@127.0.0.1:5432/intelligent_report?sslmode=disable",
		),
	)

	tenantID := insertTenant(
		t, ctx, appPool, fmt.Sprintf("connection-attestation-%d", time.Now().UnixNano()),
	)
	var actorID string
	if err := database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.users(
				tenant_id,email,display_name,password_hash
			) VALUES($1,$2,'connection attestation publisher','integration-hash')
			RETURNING id::text`,
			tenantID,
			"connection-attestation-"+fmt.Sprint(time.Now().UnixNano())+"@it.test",
		).Scan(&actorID)
	}); err != nil {
		t.Fatal(err)
	}

	dataSourceRepository := datasource.NewPostgresRepository(appPool)
	connectionTests := datasource.NewPostgresConnectionTestRepository(appPool)
	service := datasource.NewService(dataSourceRepository)
	service.SetConnectionTestJobRepository(connectionTests)
	source, err := service.Create(ctx, datasource.Source{
		TenantID: tenantID, Code: "attested_mysql", Name: "Attested MySQL",
		Type: datasource.TypeMySQL,
		Config: map[string]any{
			"host": "mysql", "port": 3306,
			"database": "report_source", "username": "report_reader",
		},
		SecretRef: "env://EXTERNAL_SOURCE_SECRET",
		OwnerID:   actorID, CreatedBy: actorID, UpdatedBy: actorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.QueueConnectionTest(
		ctx, tenantID, actorID, source.ID, "integration-idempotency-key",
	)
	if err != nil || job.Status != datasource.ConnectionTestQueued {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	replayed, err := service.QueueConnectionTest(
		ctx, tenantID, actorID, source.ID, "integration-idempotency-key",
	)
	if err != nil || replayed.ID != job.ID {
		t.Fatalf("idempotent replay=%#v err=%v", replayed, err)
	}

	for _, directWrite := range []struct {
		name string
		sql  string
	}{
		{
			name: "job",
			sql:  `UPDATE platform.data_source_connection_test_jobs SET status='SUCCEEDED' WHERE id=$1`,
		},
		{
			name: "attestation",
			sql: `INSERT INTO platform.data_source_connection_test_attestations(
				tenant_id,connection_test_job_id,data_source_id,data_source_version_id,
				config_hash,executor_id,latency_ms,started_at,completed_at,expires_at
			) SELECT tenant_id,id,data_source_id,data_source_version_id,config_hash,
				'forged',0,now(),now(),now()+interval '30 minutes'
			  FROM platform.data_source_connection_test_jobs WHERE id=$1`,
		},
		{
			name: "legacy test run",
			sql: `INSERT INTO platform.data_source_test_runs(
				tenant_id,data_source_id,data_source_version_id,config_hash,status,
				started_at,completed_at,expires_at
			) SELECT tenant_id,data_source_id,data_source_version_id,config_hash,
				'PASSED',now(),now(),now()+interval '30 minutes'
			  FROM platform.data_source_connection_test_jobs WHERE id=$1`,
		},
	} {
		t.Run("app cannot forge "+directWrite.name, func(t *testing.T) {
			expectTenantPermissionDenied(
				t, ctx, appPool, tenantID, directWrite.sql, job.ID,
			)
		})
		t.Run("generic worker cannot forge "+directWrite.name, func(t *testing.T) {
			expectTenantPermissionDenied(
				t, ctx, workerPool, tenantID, directWrite.sql, job.ID,
			)
		})
		t.Run("tester cannot directly forge "+directWrite.name, func(t *testing.T) {
			expectTenantPermissionDenied(
				t, ctx, testerPool, tenantID, directWrite.sql, job.ID,
			)
		})
	}

	expectTenantPermissionDenied(
		t, ctx, appPool, tenantID,
		`SELECT platform.complete_data_source_connection_test(
			$1::uuid,'00000000-0000-4000-8000-000000000000'::uuid,'forged',0
		)`,
		job.ID,
	)
	expectTenantPermissionDenied(
		t, ctx, testerPool, tenantID,
		`SELECT platform.enqueue_data_source_connection_test(
			$1::uuid,NULL,NULL
		)`,
		source.ID,
	)

	testerRepository := datasource.NewPostgresConnectionTestRepository(testerPool)
	claim, err := testerRepository.ClaimConnectionTest(
		ctx, tenantID, "integration-connection-tester", time.Minute,
	)
	if err != nil || claim == nil {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if claim.Job.ID != job.ID ||
		claim.Source.ConfigVersionID != source.ConfigVersionID ||
		claim.Source.ConfigHash != source.ConfigHash {
		t.Fatalf("claim did not freeze exact source: %#v", claim)
	}
	if err := testerRepository.CompleteConnectionTest(
		ctx, tenantID, claim.Job.ID, claim.LeaseToken, "MySQL 8.4.10", 19,
	); err != nil {
		t.Fatal(err)
	}

	finished, err := service.GetConnectionTest(ctx, tenantID, source.ID, job.ID)
	if err != nil || finished.Status != datasource.ConnectionTestSucceeded {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	if finished.TestedAt == nil || finished.ExpiresAt == nil ||
		finished.ExpiresAt.Sub(*finished.TestedAt) != 30*time.Minute {
		t.Fatalf("database did not generate exact 30-minute proof: %#v", finished)
	}

	published, err := service.Publish(ctx, tenantID, actorID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.PublicationStatus != datasource.PublicationPublished ||
		published.PublishedVersionID != source.ConfigVersionID {
		t.Fatalf("published=%#v", published)
	}

	// Regression for source-edit -> stale-job-trigger versus job completion.
	// The edit transaction deliberately holds the source row while completion
	// starts. Completion must wait on the source before taking the job row; the
	// reverse order forms a source/job deadlock when this update fires.
	concurrentJob, err := service.QueueConnectionTest(
		ctx, tenantID, actorID, source.ID, "integration-concurrent-edit",
	)
	if err != nil {
		t.Fatal(err)
	}
	concurrentClaim, err := testerRepository.ClaimConnectionTest(
		ctx, tenantID, "integration-lock-order-tester", time.Minute,
	)
	if err != nil || concurrentClaim == nil ||
		concurrentClaim.Job.ID != concurrentJob.ID {
		t.Fatalf("concurrent claim=%#v err=%v", concurrentClaim, err)
	}
	editTx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer editTx.Rollback(context.Background())
	if _, err := editTx.Exec(
		ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenantID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := editTx.Exec(
		ctx, `SET LOCAL lock_timeout='3s'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := editTx.Exec(
		ctx, `SELECT id FROM platform.data_sources WHERE id=$1 FOR UPDATE`,
		source.ID,
	); err != nil {
		t.Fatal(err)
	}
	completionResult := make(chan error, 1)
	go func() {
		completeCtx, cancelComplete := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancelComplete()
		completionResult <- testerRepository.CompleteConnectionTest(
			completeCtx, tenantID, concurrentClaim.Job.ID,
			concurrentClaim.LeaseToken, "concurrent-edit", 1,
		)
	}()
	time.Sleep(250 * time.Millisecond)
	if _, err := editTx.Exec(
		ctx,
		`UPDATE platform.data_sources
		 SET deleted_at=clock_timestamp()
		 WHERE id=$1`,
		source.ID,
	); err != nil {
		t.Fatalf("source edit deadlocked with completion: %v", err)
	}
	if err := editTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent source edit: %v", err)
	}
	if err := <-completionResult; !errors.Is(
		err, datasource.ErrConnectionTestLeaseLost,
	) {
		t.Fatalf("stale completion error=%v, want lease lost", err)
	}
}

func connectionTestPool(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func expectTenantPermissionDenied(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, statement string,
	arguments ...any,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, statement, arguments...)
		return err
	})
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "42501" {
		t.Fatalf("expected permission denied, got %v", err)
	}
}
