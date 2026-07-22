//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metriccandidate"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestDatasetPublicationApprovalCommitsDecisionAndVersionAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "dataset-approval-it-"+suffix)
	// 已发布版本和审批事实均受不可变约束保护；一次性集成数据库统一回收。
	var requesterID, reviewerID, sourceID, tableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'approval requester','integration-hash') RETURNING id`, tenantID, "requester-"+suffix+"@it.test").Scan(&requesterID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'approval reviewer','integration-hash') RETURNING id`, tenantID, "reviewer-"+suffix+"@it.test").Scan(&reviewerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			tenant_id,code,name,source_type,secret_ref,status,last_synced_at
		) VALUES($1,'orders','Orders','MYSQL','env://DATASET_APPROVAL_IT','ACTIVE',now()) RETURNING id`, tenantID).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at
		) VALUES($1,$2,'sales','orders','TABLE',repeat('7',64),now()) RETURNING id`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		for position, name := range []string{"order_date", "order_amount", "order_status"} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
				tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at
			) VALUES($1,$2,$3,$4,'varchar','STRING',false,$3||'-approval-hash',now())`, tenantID, tableID, name, position+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	validator := &publicationValidatorStub{result: dataset.PreviewResult{
		QueryID: "3cbbad54-8ee8-49b8-881c-13e1c7da2a13", Columns: []string{"stat_month", "valid_order_amount"},
		Rows: [][]any{{"2026-01", 100}}, RowCount: 1, DurationMS: 3,
	}}
	store := dataset.NewPostgresStore(pool)
	candidateStore := metriccandidate.NewPostgresStore(pool)
	store.SetPublicationCommitSink(candidateStore)
	datasetService := dataset.NewService(store, validator)
	approvalService := dataset.NewPublicationApprovalService(store, datasetService)
	created, err := datasetService.Create(ctx, tenantID, requesterID, dataset.CreateInput{
		Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额",
		Type: "SINGLE_SOURCE", DSL: datasetExampleForAssets(t, sourceID, tableID),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, grant := range []struct{ subjectID, action string }{{requesterID, "MANAGE"}, {reviewerID, "PUBLISH"}} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
				tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
			) VALUES($1,'USER',$2,'DATASET',$3,$4,$5)`, tenantID, grant.subjectID, created.ID, grant.action, requesterID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	request, err := approvalService.Submit(ctx, tenantID, requesterID, created.ID, dataset.SubmitPublicationInput{
		DraftVersionID: created.DraftVersionID, ExpectedVersion: created.Version,
		ExpectedDraftRecordVersion: created.DraftRecordVersion, ExpectedDSLHash: created.DSLHash,
		ValidationParameters: map[string]any{}, Note: "用于指标设计",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != dataset.PublicationRequestPending || request.ExpectedPlanHash != created.PlanHash {
		t.Fatalf("submitted request=%#v", request)
	}
	beforeApproval, err := datasetService.Get(ctx, tenantID, created.ID)
	if err != nil || beforeApproval.CurrentPublishedVersionID != "" || beforeApproval.Status != "DRAFT" {
		t.Fatalf("submission published early: record=%#v err=%v", beforeApproval, err)
	}
	if _, err := approvalService.Approve(ctx, tenantID, requesterID, created.ID, request.ID, dataset.ApprovePublicationInput{
		ExpectedVersion: request.Version, Note: "越权审批",
	}); !errors.Is(err, dataset.ErrForbidden) {
		t.Fatalf("requester Approve() error=%v, want ErrForbidden", err)
	}

	result, err := approvalService.Approve(ctx, tenantID, reviewerID, created.ID, request.ID, dataset.ApprovePublicationInput{
		ExpectedVersion: request.Version, Note: "试跑与口径校验通过",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.Status != dataset.PublicationRequestApproved ||
		result.Request.PublishedVersionID != result.PublishedVersion.ID || result.PublishedVersion.Status != "PUBLISHED" {
		t.Fatalf("approval result=%#v", result)
	}
	afterApproval, err := datasetService.Get(ctx, tenantID, created.ID)
	if err != nil || afterApproval.CurrentPublishedVersionID != result.PublishedVersion.ID || afterApproval.Status != "PUBLISHED" {
		t.Fatalf("published pointer=%#v err=%v", afterApproval, err)
	}

	var approvedCount, versionCount, approvalAuditCount, publishAuditCount, extractionJobCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_publication_requests
			WHERE id=$1 AND status='APPROVED' AND published_version_id=$2`, request.ID, result.PublishedVersion.ID).Scan(&approvedCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_versions
			WHERE id=$1 AND status='PUBLISHED'`, result.PublishedVersion.ID).Scan(&versionCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET_PUBLICATION_REQUEST' AND resource_id=$1 AND action='APPROVE'`, request.ID).Scan(&approvalAuditCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET' AND resource_id=$1 AND action='PUBLISH'`, created.ID).Scan(&publishAuditCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_extraction_jobs
			WHERE dataset_id=$1 AND dataset_version_id=$2 AND dsl_hash=$3
			  AND extractor_version=$4 AND status='PENDING'`, created.ID, result.PublishedVersion.ID,
			result.PublishedVersion.DSLHash, metriccandidate.JobVersion).Scan(&extractionJobCount)
	})
	if err != nil || approvedCount != 1 || versionCount != 1 || approvalAuditCount != 1 ||
		publishAuditCount != 1 || extractionJobCount != 1 {
		t.Fatalf("atomic facts request=%d version=%d approval_audit=%d publish_audit=%d extraction_job=%d err=%v",
			approvedCount, versionCount, approvalAuditCount, publishAuditCount, extractionJobCount, err)
	}

	// Approval commits only a durable extraction outbox. Candidate generation is asynchronous and
	// does not make the dataset publication depend on a worker or an LLM being available.
	if items, total, listErr := candidateStore.List(ctx, tenantID, metriccandidate.ListFilter{Limit: 20}); listErr != nil || total != 0 || len(items) != 0 {
		t.Fatalf("candidates existed before worker: items=%#v total=%d err=%v", items, total, listErr)
	}
	replayed, err := approvalService.Approve(ctx, tenantID, reviewerID, created.ID, request.ID, dataset.ApprovePublicationInput{
		ExpectedVersion: request.Version, Note: "重复请求不应再次入队",
	})
	if err != nil || replayed.PublishedVersion.ID != result.PublishedVersion.ID {
		t.Fatalf("approval replay=%#v err=%v", replayed, err)
	}
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_extraction_jobs
			WHERE dataset_version_id=$1 AND extractor_version=$2`, result.PublishedVersion.ID,
			metriccandidate.JobVersion).Scan(&extractionJobCount)
	}); err != nil || extractionJobCount != 1 {
		t.Fatalf("approval replay extraction jobs=%d err=%v, want one", extractionJobCount, err)
	}

	worker := metriccandidate.NewWorker(candidateStore)
	processed, err := worker.ProcessNext(ctx, tenantID, "dataset-approval-metric-extraction", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("metric extraction processed=%v err=%v", processed, err)
	}
	candidates, total, err := candidateStore.List(ctx, tenantID, metriccandidate.ListFilter{Limit: 20})
	if err != nil || total != 1 || len(candidates) != 1 {
		t.Fatalf("generated candidates=%#v total=%d err=%v", candidates, total, err)
	}
	candidate := candidates[0]
	if candidate.DatasetID != created.ID || candidate.DatasetVersionID != result.PublishedVersion.ID ||
		candidate.DSLHash != result.PublishedVersion.DSLHash || candidate.Status != metriccandidate.CandidateStatusBlocked ||
		candidate.Method != "RULE" || len(candidate.SourceFieldIDs) != 1 || candidate.SourceFieldIDs[0] != "field_revenue" {
		t.Fatalf("generated candidate=%#v", candidate)
	}
	if len(candidate.BlockReasons) != 2 || candidate.BlockReasons[0] != metriccandidate.BlockReasonAggregatedDataset ||
		candidate.BlockReasons[1] != metriccandidate.BlockReasonAggregateExpression {
		t.Fatalf("blocked candidate reasons=%#v", candidate.BlockReasons)
	}
	var metricCount, completedJobCount int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metrics WHERE dataset_id=$1`, created.ID).Scan(&metricCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_extraction_jobs
			WHERE dataset_version_id=$1 AND status='PARTIAL' AND total=1 AND blocked_count=1`,
			result.PublishedVersion.ID).Scan(&completedJobCount)
	}); err != nil || metricCount != 0 || completedJobCount != 1 {
		t.Fatalf("post-extraction metrics=%d completed_jobs=%d err=%v", metricCount, completedJobCount, err)
	}
}
