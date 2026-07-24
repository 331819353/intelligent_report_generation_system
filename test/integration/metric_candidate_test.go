//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
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
	var candidateID string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.metric_candidates
			WHERE dataset_version_id=$1
			  AND source_field_ids @> ARRAY['field_revenue']::text[]
			ORDER BY id LIMIT 1`, datasetVersion.ID).Scan(&candidateID)
	}); err != nil {
		t.Fatal(err)
	}
	candidate, err := candidateStore.Get(ctx, tenantID, candidateID)
	if err != nil {
		t.Fatal(err)
	}
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

	// 发布 V1 后给同一数据集增加维度并重新提取。新候选必须迭代原指标草稿，
	// 而不是因稳定指标编码的唯一约束尝试创建第二个指标主对象。
	metricStore := metric.NewPostgresStore(pool)
	grantMetricPublish(t, ctx, pool, tenantID, actorID, "", accepted.Metric.ID)
	initialPrepared, err := metric.Prepare(accepted.Metric.Definition)
	if err != nil {
		t.Fatal(err)
	}
	publishedV1, err := metricStore.Publish(ctx, tenantID, actorID, accepted.Metric.ID,
		metricPublishPlan(accepted.Metric, initialPrepared, "metric-candidate-v1-"+suffix, strings.Repeat("7", 64)))
	if err != nil {
		t.Fatal(err)
	}

	datasetService := dataset.NewService(dataset.NewPostgresStore(pool), &publicationValidatorStub{
		result: dataset.PreviewResult{QueryID: uuid.NewString()},
	})
	currentDataset, err := datasetService.Get(ctx, tenantID, datasetRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	document, err := dataset.DecodeAndNormalize(currentDataset.DSL)
	if err != nil {
		t.Fatal(err)
	}
	visible := true
	document.Fields = append(document.Fields, dataset.Field{
		ID: "field_order_month", Code: "order_month", Name: "下单年月", Role: "DIMENSION",
		Expression:    dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"},
		CanonicalType: "DATE", Nullable: false, Visible: &visible,
	})
	changedDSL, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	currentDataset, err = datasetService.Update(ctx, tenantID, actorID, datasetRecord.ID, dataset.UpdateInput{
		Name: currentDataset.Name, Description: currentDataset.Description,
		ExpectedVersion: currentDataset.Version, DSL: changedDSL,
	})
	if err != nil {
		t.Fatal(err)
	}
	datasetVersionV2, err := datasetService.Publish(ctx, tenantID, actorID, datasetRecord.ID,
		"metric-candidate-dataset-v2-"+suffix, dataset.PublishInput{
			DraftVersionID: currentDataset.DraftVersionID, ExpectedVersion: currentDataset.Version,
			ExpectedDraftRecordVersion: currentDataset.DraftRecordVersion, ExpectedDSLHash: currentDataset.DSLHash,
			ValidationParameters: map[string]any{},
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return candidateStore.EnqueueDatasetMetricExtractionTx(ctx, tx, tenantID, actorID, datasetVersionV2)
	}); err != nil {
		t.Fatal(err)
	}
	processed, err = worker.ProcessNext(ctx, tenantID, "metric-candidate-integration-v2", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("ProcessNext(V2) processed=%v err=%v", processed, err)
	}
	var candidateV2ID string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.metric_candidates
			WHERE dataset_version_id=$1 AND code=$2 ORDER BY created_at DESC LIMIT 1`,
			datasetVersionV2.ID, candidate.Code).Scan(&candidateV2ID)
	}); err != nil {
		t.Fatal(err)
	}
	candidateV2, err := candidateStore.Get(ctx, tenantID, candidateV2ID)
	if err != nil {
		t.Fatal(err)
	}
	iterated, err := reviewService.Accept(ctx, tenantID, actorID, candidateV2.ID,
		metriccandidate.AcceptInput{ExpectedVersion: candidateV2.Version})
	if err != nil {
		t.Fatal(err)
	}
	if iterated.Metric.ID != accepted.Metric.ID ||
		iterated.Metric.CurrentPublishedVersionID != publishedV1.ID ||
		iterated.Metric.DatasetVersionID != datasetVersionV2.ID ||
		iterated.Metric.DraftRecordVersion != accepted.Metric.DraftRecordVersion+1 {
		t.Fatalf("iterated metric=%#v initial=%#v publishedV1=%#v", iterated.Metric, accepted.Metric, publishedV1)
	}
	var draftDimensions []string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT field_id FROM platform.metric_dimensions
			WHERE metric_version_id::text=$1 ORDER BY ordinal_position`, iterated.Metric.DraftVersionID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var fieldID string
			if scanErr := rows.Scan(&fieldID); scanErr != nil {
				return scanErr
			}
			draftDimensions = append(draftDimensions, fieldID)
		}
		return rows.Err()
	})
	if err != nil || !containsIntegrationString(draftDimensions, "field_order_month") {
		t.Fatalf("iterated draft dimensions=%v err=%v", draftDimensions, err)
	}
	unchangedV1, err := metricStore.GetVersion(ctx, tenantID, accepted.Metric.ID, publishedV1.ID)
	if err != nil || unchangedV1.DatasetVersionID != datasetVersion.ID ||
		unchangedV1.DefinitionHash != publishedV1.DefinitionHash {
		t.Fatalf("published V1 changed after iteration: %#v err=%v", unchangedV1, err)
	}
	iteratedPrepared, err := metric.Prepare(iterated.Metric.Definition)
	if err != nil {
		t.Fatal(err)
	}
	publishedV2, err := metricStore.Publish(ctx, tenantID, actorID, accepted.Metric.ID,
		metricPublishPlan(iterated.Metric, iteratedPrepared, "metric-candidate-v2-"+suffix, strings.Repeat("8", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if publishedV2.VersionNo != 2 || publishedV2.DatasetVersionID != datasetVersionV2.ID {
		t.Fatalf("published V2=%#v", publishedV2)
	}
	replayedV2, err := reviewService.Accept(ctx, tenantID, actorID, candidateV2.ID,
		metriccandidate.AcceptInput{ExpectedVersion: candidateV2.Version})
	if err != nil || replayedV2.Metric.ID != accepted.Metric.ID {
		t.Fatalf("replay V2=%#v err=%v", replayedV2, err)
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

func containsIntegrationString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
