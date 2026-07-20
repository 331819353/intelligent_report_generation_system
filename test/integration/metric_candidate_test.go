//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/metriccandidate"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestMetricCandidateExtractionAtomicAcceptanceAndRetryBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metric-candidate-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "metric-candidate-foreign-"+suffix)
	// Published versions and candidates are immutable audit facts; the disposable integration
	// database owns cleanup for the primary tenant.
	t.Cleanup(func() { cleanupTenant(pool, foreignTenantID) })

	actorID, _, _, datasetRecord, datasetVersion := preparePublishedMetricDataset(t, ctx, pool, tenantID, suffix)
	candidateStore := metriccandidate.NewPostgresStore(pool)
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return candidateStore.EnqueueDatasetMetricExtractionTx(ctx, tx, tenantID, actorID, datasetVersion)
	}); err != nil {
		t.Fatal(err)
	}

	worker := metriccandidate.NewWorker(candidateStore)
	processed, err := worker.ProcessNext(ctx, tenantID, "metric-candidate-integration", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() processed=%v err=%v", processed, err)
	}
	items, total, err := candidateStore.List(ctx, tenantID, metriccandidate.ListFilter{Limit: 20})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("List() items=%#v total=%d err=%v", items, total, err)
	}
	candidate := items[0]
	if candidate.DatasetID != datasetRecord.ID || candidate.DatasetVersionID != datasetVersion.ID ||
		candidate.DSLHash != datasetVersion.DSLHash || candidate.Status != metriccandidate.CandidateStatusNeedsReview {
		t.Fatalf("extracted candidate=%#v", candidate)
	}
	if foreign, foreignTotal, err := candidateStore.List(ctx, foreignTenantID, metriccandidate.ListFilter{Limit: 20}); err != nil || foreignTotal != 0 || len(foreign) != 0 {
		t.Fatalf("cross-tenant candidates leaked items=%#v total=%d err=%v", foreign, foreignTotal, err)
	}

	metricService := metric.NewService(metric.NewPostgresStore(pool))
	reviewService := metriccandidate.NewService(candidateStore, metricService)
	accepted, err := reviewService.Accept(ctx, tenantID, actorID, candidate.ID,
		metriccandidate.AcceptInput{ExpectedVersion: candidate.Version})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reviewService.Accept(ctx, tenantID, actorID, candidate.ID,
		metriccandidate.AcceptInput{ExpectedVersion: candidate.Version})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Metric.ID == "" || accepted.Metric.ID != replayed.Metric.ID ||
		accepted.Candidate.Status != metriccandidate.CandidateStatusAccepted ||
		accepted.Candidate.AcceptedMetricID != accepted.Metric.ID {
		t.Fatalf("accept=%#v replay=%#v", accepted, replayed)
	}
	var linkedMetrics int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metrics
			WHERE origin_candidate_id=$1 AND dataset_id=$2`, candidate.ID, datasetRecord.ID).Scan(&linkedMetrics)
	}); err != nil || linkedMetrics != 1 {
		t.Fatalf("origin candidate metric count=%d err=%v", linkedMetrics, err)
	}

	var expiredJobID string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.metric_extraction_jobs(
			tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version,
			status,lease_owner,lease_expires_at,attempt,started_at
		) VALUES($1,$2,$3,$4,$5,'integration-expired-lease','RUNNING','dead-worker',
			now()-interval '1 second',3,now()-interval '10 minutes') RETURNING id::text`,
			tenantID, datasetRecord.ID, datasetVersion.ID, datasetVersion.DSLHash, actorID).Scan(&expiredJobID)
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := candidateStore.ClaimJob(ctx, tenantID, "recovery-worker", time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("ClaimJob() claim=%#v err=%v, want expired third attempt finalized", claim, err)
	}
	var expiredStatus, expiredCode string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status,error_code FROM platform.metric_extraction_jobs WHERE id=$1`, expiredJobID).
			Scan(&expiredStatus, &expiredCode)
	}); err != nil || expiredStatus != "FAILED" || expiredCode != "LEASE_EXPIRED" {
		t.Fatalf("expired job status=%q code=%q err=%v", expiredStatus, expiredCode, err)
	}

	var lateJobID string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.metric_extraction_jobs(
			tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version,
			status,lease_owner,lease_expires_at,attempt,started_at
		) VALUES($1,$2,$3,$4,$5,'integration-late-failure','RUNNING','late-worker',
			now()-interval '1 second',1,now()-interval '2 minutes') RETURNING id::text`,
			tenantID, datasetRecord.ID, datasetVersion.ID, datasetVersion.DSLHash, actorID).Scan(&lateJobID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := candidateStore.FailJob(ctx, metriccandidate.JobClaim{
		ID: lateJobID, TenantID: tenantID, DatasetID: datasetRecord.ID,
		DatasetVersionID: datasetVersion.ID, DSLHash: datasetVersion.DSLHash, RequestedBy: actorID,
	}, "late-worker", "INTEGRATION_FAILURE", "late failure"); err != nil {
		t.Fatal(err)
	}
	var lateStatus string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM platform.metric_extraction_jobs WHERE id=$1`, lateJobID).Scan(&lateStatus)
	}); err != nil || lateStatus != "PENDING" {
		t.Fatalf("late failure status=%q err=%v", lateStatus, err)
	}

	mismatchedHash := strings.Repeat("f", 64)
	if mismatchedHash == datasetVersion.DSLHash {
		mismatchedHash = strings.Repeat("e", 64)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, insertErr := tx.Exec(ctx, `INSERT INTO platform.metric_extraction_jobs(
			tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
		) VALUES($1,$2,$3,$4,$5,'integration-wrong-hash')`,
			tenantID, datasetRecord.ID, datasetVersion.ID, mismatchedHash, actorID)
		return insertErr
	})
	if err == nil {
		t.Fatal("metric extraction job accepted a DSL hash that does not belong to the exact dataset version")
	}
}
