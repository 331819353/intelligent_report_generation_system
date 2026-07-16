//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

func TestDatasetDraftPersistenceAndTenantIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "dataset-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "dataset-foreign-"+suffix)
	retainPublishedTenant := false
	t.Cleanup(func() {
		// 发布版本受数据库不可变约束保护，不为测试清理引入绕过机制；一次性集成数据库会统一回收。
		if !retainPublishedTenant {
			cleanupDatasetTenant(pool, tenantID)
			cleanupTenant(pool, tenantID)
		}
		cleanupTenant(pool, foreignTenantID)
	})

	var actorID, secondActorID, sourceID, tableID, customerSourceID, customerTableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'dataset tester','integration-hash') RETURNING id`, tenantID, "dataset-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'dataset second tester','integration-hash') RETURNING id`, tenantID, "dataset-second-"+suffix+"@it.test").Scan(&secondActorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,secret_ref,status,last_synced_at) VALUES($1,'orders','Orders','MYSQL','env://DATASET_IT','ACTIVE',now()) RETURNING id`, tenantID).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'sales','orders','TABLE',repeat('1',64),now()) RETURNING id`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		for position, name := range []string{"order_date", "order_amount", "order_status"} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at) VALUES($1,$2,$3,$4,'varchar','STRING',false,$3||'-hash',now())`, tenantID, tableID, name, position+1); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,secret_ref,status,last_synced_at) VALUES($1,'customers','Customers','ORACLE','env://DATASET_ORACLE_IT','ACTIVE',now()) RETURNING id`, tenantID).Scan(&customerSourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'crm','customers','TABLE',repeat('2',64),now()) RETURNING id`, tenantID, customerSourceID).Scan(&customerTableID); err != nil {
			return err
		}
		for position, name := range []string{"customer_id", "customer_name"} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at) VALUES($1,$2,$3,$4,'varchar','STRING',false,$3||'-customer-hash',now())`, tenantID, customerTableID, name, position+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := datasetExampleForAssets(t, sourceID, tableID)
	validator := &publicationValidatorStub{result: dataset.PreviewResult{
		QueryID: "3cbbad54-8ee8-49b8-881c-13e1c7da2a13", Columns: []string{"stat_month", "valid_order_amount"},
		Rows: [][]any{{"2026-01", 100}}, RowCount: 1, DurationMS: 3,
	}}
	datasetStore := dataset.NewPostgresStore(pool)
	service := dataset.NewService(datasetStore, validator)
	created, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额", Type: "SINGLE_SOURCE", DSL: raw})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.DSLHash == "" || created.PlanHash == "" {
		t.Fatalf("created=%#v", created)
	}
	// 发布权限使用对象级授权，覆盖同一幂等键由不同操作者重试时的身份冲突。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, subjectID := range []string{actorID, secondActorID} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by) VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$4)`, tenantID, subjectID, created.ID, actorID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := service.List(ctx, tenantID, 20, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("List() items=%#v total=%d err=%v", items, total, err)
	}
	foreignItems, foreignTotal, err := service.List(ctx, foreignTenantID, 20, 0)
	if err != nil || foreignTotal != 0 || len(foreignItems) != 0 {
		t.Fatalf("cross-tenant List() leaked items=%#v total=%d err=%v", foreignItems, foreignTotal, err)
	}
	if _, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额", Type: "SINGLE_SOURCE", DSL: raw}); !errors.Is(err, dataset.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v, want ErrAlreadyExists", err)
	}
	if _, err := service.Get(ctx, foreignTenantID, created.ID); !errors.Is(err, dataset.ErrNotFound) {
		t.Fatalf("cross-tenant Get() error = %v, want ErrNotFound", err)
	}
	if _, err := service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{Name: created.Name, Description: created.Description, ExpectedVersion: 99, DSL: raw}); !errors.Is(err, dataset.ErrConflict) {
		t.Fatalf("Update() error = %v, want ErrConflict", err)
	}
	updatedDSL := datasetExampleWithDescription(t, raw, "新说明")
	updated, err := service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{Name: created.Name, Description: "新说明", ExpectedVersion: 1, DSL: updatedDSL})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated version = %d, want 2", updated.Version)
	}
	document, err := dataset.DecodeAndNormalize(updated.DSL)
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore := queryruntime.NewPostgresStore(pool)
	resolved, err := runtimeStore.Resolve(ctx, tenantID, document)
	if err != nil || resolved.SourceID != sourceID || len(resolved.Tables["orders"].Columns) != 3 {
		t.Fatalf("Resolve() plan=%#v err=%v", resolved, err)
	}
	// 跨源解析必须为每个节点固定可信的物理表、源版本和同步水位。
	crossDocument := document
	crossDocument.Dataset.Type = "CROSS_SOURCE"
	crossDocument.Nodes = append(append([]dataset.Node(nil), document.Nodes...), dataset.Node{
		ID: "customers", Type: "TABLE", DataSourceID: customerSourceID, TableID: customerTableID, Alias: "c",
		Projection: []string{"customer_id", "customer_name"}, SourceFilters: []dataset.SourceFilter{},
	})
	crossResolved, err := runtimeStore.Resolve(ctx, tenantID, crossDocument)
	if err != nil || len(crossResolved.Nodes) != 2 || crossResolved.Nodes["customers"].SourceID != customerSourceID || crossResolved.Nodes["orders"].Watermark == "" || crossResolved.Nodes["customers"].Watermark == "" {
		t.Fatalf("Resolve() cross plan=%#v err=%v", crossResolved, err)
	}
	queryID := uuid.NewString()
	orderSubqueryID := uuid.NewString()
	customerSubqueryID := uuid.NewString()
	orderNode, customerNode := crossResolved.Nodes["orders"], crossResolved.Nodes["customers"]
	if err := runtimeStore.Start(ctx, queryruntime.RunRecord{
		ID: queryID, TenantID: tenantID, DatasetID: created.ID, DatasetVersionID: updated.DraftVersionID,
		ActorID: actorID, SourceID: sourceID, PlanHash: strings.Repeat("a", 64), ParameterHash: strings.Repeat("b", 64),
		Sources: []queryruntime.RunSourceRecord{
			{NodeID: orderNode.NodeID, SourceID: orderNode.SourceID, SourceType: orderNode.SourceType, SubqueryID: orderSubqueryID, SourceVersion: orderNode.SourceVersion, Watermark: orderNode.Watermark},
			{NodeID: customerNode.NodeID, SourceID: customerNode.SourceID, SourceType: customerNode.SourceType, SubqueryID: customerSubqueryID, SourceVersion: customerNode.SourceVersion, Watermark: customerNode.Watermark},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if sources, err := runtimeStore.CancellableSources(ctx, tenantID, actorID, created.ID, queryID); err != nil || len(sources) != 2 || sources[0].NodeID != "customers" || sources[1].NodeID != "orders" {
		t.Fatalf("CancellableSources() sources=%#v err=%v", sources, err)
	}
	if _, err := runtimeStore.CancellableSources(ctx, foreignTenantID, actorID, created.ID, queryID); !errors.Is(err, dataset.ErrQueryNotFound) {
		t.Fatalf("cross-tenant CancellableSources() error=%v", err)
	}
	if err := runtimeStore.Finish(ctx, tenantID, queryID, "SUCCEEDED", 2, 11, "", []datasource.QueryWarning{{Code: "JOIN_FANOUT_RISK", Message: "关联结果可能发生扇出。", JoinID: "orders_customers", EstimatedRows: 4}}, []datasource.QuerySourceStat{
		{NodeID: "orders", SubqueryID: orderSubqueryID, RowCount: 3, DurationMS: 7, Status: "SUCCEEDED"},
		{NodeID: "customers", SubqueryID: customerSubqueryID, RowCount: 2, DurationMS: 4, Status: "SUCCEEDED"},
	}); err != nil {
		t.Fatal(err)
	}
	var queryStatus string
	var queryRows, sourceAuditCount, watermarkedSourceCount, successfulSourceCount, sourceRows, warningCount int
	var sourceDuration int64
	var warningCode string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status,row_count FROM platform.query_runs WHERE id::text=$1`, queryID).Scan(&queryStatus, &queryRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT jsonb_array_length(warnings_json),warnings_json->0->>'code' FROM platform.query_runs WHERE id::text=$1`, queryID).Scan(&warningCount, &warningCode); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE source_watermark<>''),count(*) FILTER(WHERE status='SUCCEEDED'),sum(row_count),sum(duration_ms) FROM platform.query_run_sources WHERE query_run_id::text=$1`, queryID).
			Scan(&sourceAuditCount, &watermarkedSourceCount, &successfulSourceCount, &sourceRows, &sourceDuration)
	})
	if err != nil || queryStatus != "SUCCEEDED" || queryRows != 2 || sourceAuditCount != 2 || watermarkedSourceCount != 2 || successfulSourceCount != 2 || sourceRows != 5 || sourceDuration != 11 || warningCount != 1 || warningCode != "JOIN_FANOUT_RISK" {
		t.Fatalf("query audit status=%s rows=%d sources=%d/%d source_rows=%d source_duration=%d watermarks=%d warnings=%d/%s err=%v", queryStatus, queryRows, sourceAuditCount, successfulSourceCount, sourceRows, sourceDuration, watermarkedSourceCount, warningCount, warningCode, err)
	}

	// 节点提前失败时，已返回指标的节点保留实际值，其他 RUNNING 节点跟随主查询终态收口。
	failedQueryID := uuid.NewString()
	failedOrderSubqueryID := uuid.NewString()
	failedCustomerSubqueryID := uuid.NewString()
	if err := runtimeStore.Start(ctx, queryruntime.RunRecord{
		ID: failedQueryID, TenantID: tenantID, DatasetID: created.ID, DatasetVersionID: updated.DraftVersionID,
		ActorID: actorID, SourceID: sourceID, PlanHash: strings.Repeat("c", 64), ParameterHash: strings.Repeat("d", 64),
		Sources: []queryruntime.RunSourceRecord{
			{NodeID: orderNode.NodeID, SourceID: orderNode.SourceID, SourceType: orderNode.SourceType, SubqueryID: failedOrderSubqueryID, SourceVersion: orderNode.SourceVersion, Watermark: orderNode.Watermark},
			{NodeID: customerNode.NodeID, SourceID: customerNode.SourceID, SourceType: customerNode.SourceType, SubqueryID: failedCustomerSubqueryID, SourceVersion: customerNode.SourceVersion, Watermark: customerNode.Watermark},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.Finish(ctx, tenantID, failedQueryID, "FAILED", 0, 6, "QUERY_EXECUTION_FAILED", nil, []datasource.QuerySourceStat{
		{NodeID: "orders", SubqueryID: failedOrderSubqueryID, DurationMS: 5, Status: "FAILED"},
	}); err != nil {
		t.Fatal(err)
	}
	var failedSources, measuredFailedSources int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='FAILED'),count(*) FILTER(WHERE status='FAILED' AND duration_ms=5) FROM platform.query_run_sources WHERE query_run_id::text=$1`, failedQueryID).
			Scan(&failedSources, &measuredFailedSources)
	})
	if err != nil || failedSources != 2 || measuredFailedSources != 1 {
		t.Fatalf("failed source audit sources=%d measured=%d err=%v", failedSources, measuredFailedSources, err)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var fields, parameters, dependencies, impacts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_fields WHERE dataset_version_id=$1`, created.DraftVersionID).Scan(&fields); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_parameters WHERE dataset_version_id=$1`, created.DraftVersionID).Scan(&parameters); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_dependencies WHERE dataset_version_id=$1`, created.DraftVersionID).Scan(&dependencies); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.asset_dependencies WHERE downstream_id=$1`, created.ID).Scan(&impacts); err != nil {
			return err
		}
		if fields != 2 || parameters != 1 || dependencies != 1 || impacts != 1 {
			return fmt.Errorf("derived indexes: fields=%d parameters=%d dependencies=%d impacts=%d", fields, parameters, dependencies, impacts)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// 发布必须把版本快照、派生索引、当前指针、审计和幂等响应原子地写入同一事务。
	publishInput := dataset.PublishInput{
		DraftVersionID:             updated.DraftVersionID,
		ExpectedVersion:            updated.Version,
		ExpectedDraftRecordVersion: updated.DraftRecordVersion,
		ExpectedDSLHash:            updated.DSLHash,
		ValidationParameters:       map[string]any{"start_date": "2026-01-01"},
	}
	publishKey := "dataset-publish-" + suffix
	published, err := service.Publish(ctx, tenantID, actorID, created.ID, publishKey, publishInput)
	if err != nil {
		t.Fatal(err)
	}
	retainPublishedTenant = true
	if published.DatasetID != created.ID || published.DraftVersionID != updated.DraftVersionID ||
		published.DraftRecordVersion != updated.DraftRecordVersion || published.VersionNo != 1 ||
		published.Status != "PUBLISHED" || published.DSLHash != updated.DSLHash || published.PlanHash != updated.PlanHash ||
		published.PublishedBy != actorID || published.PublishedAt == "" {
		t.Fatalf("Publish() record=%#v", published)
	}
	if validator.calls != 1 || validator.candidate.DatasetID != created.ID ||
		validator.candidate.DraftVersionID != updated.DraftVersionID || validator.candidate.DSLHash != updated.DSLHash {
		t.Fatalf("publication validator calls=%d candidate=%#v", validator.calls, validator.candidate)
	}

	afterPublish, err := service.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPublish.Version != updated.Version+1 || afterPublish.Status != "PUBLISHED" ||
		afterPublish.CurrentPublishedVersionID != published.ID || afterPublish.DraftVersionNo != 2 {
		t.Fatalf("dataset after publish=%#v", afterPublish)
	}
	var persistedDatasetStatus, persistedVersionStatus, persistedPointer, persistedSourceHash, persistedSourcePlanHash string
	var persistedDatasetVersion int64
	var persistedDraftVersionNo, publishedFields, publishedParameters, publishedDependencies, idempotencyRows, publishAudits int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT d.status,d.version,d.current_published_version_id::text,draft.version_no,published.status
			FROM platform.datasets d
			JOIN platform.dataset_versions draft ON draft.id=d.current_draft_version_id
			JOIN platform.dataset_versions published ON published.id=d.current_published_version_id
			WHERE d.id::text=$1`, created.ID).Scan(&persistedDatasetStatus, &persistedDatasetVersion, &persistedPointer, &persistedDraftVersionNo, &persistedVersionStatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_fields WHERE dataset_version_id::text=$1`, published.ID).Scan(&publishedFields); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_parameters WHERE dataset_version_id::text=$1`, published.ID).Scan(&publishedParameters); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*),min(source_hash),min(source_plan_hash) FROM platform.dataset_dependencies WHERE dataset_version_id::text=$1`, published.ID).
			Scan(&publishedDependencies, &persistedSourceHash, &persistedSourcePlanHash); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_publish_idempotency WHERE dataset_id::text=$1 AND idempotency_key=$2 AND published_version_id::text=$3`, created.ID, publishKey, published.ID).Scan(&idempotencyRows); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs WHERE resource_type='DATASET' AND resource_id=$1 AND action='PUBLISH'`, created.ID).Scan(&publishAudits)
	})
	if err != nil || persistedDatasetStatus != "PUBLISHED" || persistedVersionStatus != "PUBLISHED" ||
		persistedDatasetVersion != afterPublish.Version || persistedPointer != published.ID || persistedDraftVersionNo != 2 ||
		publishedFields != 2 || publishedParameters != 1 || publishedDependencies != 1 ||
		persistedSourceHash != strings.Repeat("1", 64) || persistedSourcePlanHash != "" || idempotencyRows != 1 || publishAudits != 1 {
		t.Fatalf("published transaction dataset=%s/%d pointer=%s draft_no=%d version=%s indexes=%d/%d/%d dependency=%s/%s idempotency=%d audits=%d err=%v",
			persistedDatasetStatus, persistedDatasetVersion, persistedPointer, persistedDraftVersionNo, persistedVersionStatus,
			publishedFields, publishedParameters, publishedDependencies, persistedSourceHash, persistedSourcePlanHash, idempotencyRows, publishAudits, err)
	}

	// 同一操作者与请求可以精确重放，换操作者或复用键提交不同请求必须冲突且不能再次试跑。
	replayed, err := service.Publish(ctx, tenantID, actorID, created.ID, publishKey, publishInput)
	if err != nil || replayed.ID != published.ID || replayed.DSLHash != published.DSLHash || string(replayed.DSL) != string(published.DSL) {
		t.Fatalf("idempotent Publish() record=%#v err=%v", replayed, err)
	}
	if _, err := service.Publish(ctx, tenantID, secondActorID, created.ID, publishKey, publishInput); !errors.Is(err, dataset.ErrIdempotencyConflict) {
		t.Fatalf("cross-actor Publish() error=%v, want ErrIdempotencyConflict", err)
	}
	conflictingPublishInput := publishInput
	conflictingPublishInput.ExpectedVersion++
	if _, err := service.Publish(ctx, tenantID, actorID, created.ID, publishKey, conflictingPublishInput); !errors.Is(err, dataset.ErrIdempotencyConflict) {
		t.Fatalf("changed-request Publish() error=%v, want ErrIdempotencyConflict", err)
	}
	if _, err := service.Publish(ctx, tenantID, actorID, created.ID, publishKey+"-stale", publishInput); !errors.Is(err, dataset.ErrConflict) {
		t.Fatalf("stale Publish() error=%v, want ErrConflict", err)
	}
	if validator.calls != 1 {
		t.Fatalf("publication validator calls=%d, want 1", validator.calls)
	}

	exactVersion, err := service.GetVersion(ctx, tenantID, created.ID, published.ID)
	if err != nil || exactVersion.ID != published.ID || exactVersion.Status != "PUBLISHED" || string(exactVersion.DSL) != string(published.DSL) {
		t.Fatalf("GetVersion() record=%#v err=%v", exactVersion, err)
	}
	versions, versionTotal, err := service.ListVersions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || versionTotal != 1 || len(versions) != 1 || versions[0].ID != published.ID || versions[0].VersionNo != 1 {
		t.Fatalf("ListVersions() versions=%#v total=%d err=%v", versions, versionTotal, err)
	}

	// 占用统计只返回聚合数量：同一报告的多条 JSON 路径按报告去重，下游草稿和发布版本分别计数。
	downstreamValidator := &publicationValidatorStub{result: dataset.PreviewResult{QueryID: uuid.NewString()}}
	downstreamService := dataset.NewService(datasetStore, downstreamValidator)
	downstreamDSL := datasetExampleForPublishedVersion(t, published.ID)
	downstream, err := downstreamService.Create(ctx, tenantID, actorID, dataset.CreateInput{
		Code: "monthly_orders_consumer", Name: "月度订单消费数据集", Description: "引用精确发布版本", Type: "SINGLE_SOURCE", DSL: downstreamDSL,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by)
			VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$2)`, tenantID, actorID, downstream.ID); err != nil {
			return err
		}
		var reportID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.reports(tenant_id,code,name,report_type,created_by,updated_by)
			VALUES($1,$2,'数据集占用测试报告','REPORT',$3,$3) RETURNING id::text`, tenantID, "dataset-usage-"+suffix, actorID).Scan(&reportID); err != nil {
			return err
		}
		for _, path := range []string{"pages[0].blocks[0].component.binding.datasetVersionId", "parameters[0].semanticBinding.datasetFields[0].datasetVersionId"} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_dependencies(tenant_id,report_id,revision_no,dependency_type,dependency_id,json_path)
				VALUES($1,$2,1,'DATASET_VERSION',$3,$4)`, tenantID, reportID, published.ID, path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	downstreamPublished, err := downstreamService.Publish(ctx, tenantID, actorID, downstream.ID, "downstream-publish-"+suffix, dataset.PublishInput{
		DraftVersionID: downstream.DraftVersionID, ExpectedVersion: downstream.Version,
		ExpectedDraftRecordVersion: downstream.DraftRecordVersion, ExpectedDSLHash: downstream.DSLHash,
		ValidationParameters: map[string]any{"start_date": "2026-01-01"},
	})
	if err != nil || downstreamPublished.Status != "PUBLISHED" {
		t.Fatalf("downstream Publish() record=%#v err=%v", downstreamPublished, err)
	}
	usageQueryID := uuid.NewString()
	if err := runtimeStore.Start(ctx, queryruntime.RunRecord{
		ID: usageQueryID, TenantID: tenantID, DatasetID: created.ID, DatasetVersionID: published.ID,
		ActorID: actorID, SourceID: sourceID, RunType: "PREVIEW",
		PlanHash: strings.Repeat("e", 64), ParameterHash: strings.Repeat("f", 64),
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := service.GetVersionUsage(ctx, tenantID, created.ID, published.ID)
	if err != nil || usage.ReportDraftReferences != 1 || usage.DownstreamDraftReferences != 1 ||
		usage.DownstreamPublishedReferences != 1 || usage.ActiveQueryRuns != 1 {
		t.Fatalf("GetVersionUsage() usage=%#v err=%v", usage, err)
	}
	if err := runtimeStore.Finish(ctx, tenantID, usageQueryID, "SUCCEEDED", 0, 1, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	usage, err = service.GetVersionUsage(ctx, tenantID, created.ID, published.ID)
	if err != nil || usage.ActiveQueryRuns != 0 {
		t.Fatalf("GetVersionUsage() after finish usage=%#v err=%v", usage, err)
	}
	if _, err := service.GetVersion(ctx, foreignTenantID, created.ID, published.ID); !errors.Is(err, dataset.ErrVersionNotFound) {
		t.Fatalf("cross-tenant GetVersion() error=%v, want ErrVersionNotFound", err)
	}
	if _, err := service.GetVersionUsage(ctx, foreignTenantID, created.ID, published.ID); !errors.Is(err, dataset.ErrVersionNotFound) {
		t.Fatalf("cross-tenant GetVersionUsage() error=%v, want ErrVersionNotFound", err)
	}
	if _, err := service.GetVersionUsage(ctx, tenantID, downstream.ID, published.ID); !errors.Is(err, dataset.ErrVersionNotFound) {
		t.Fatalf("wrong-parent GetVersionUsage() error=%v, want ErrVersionNotFound", err)
	}
	if _, _, err := service.ListVersions(ctx, foreignTenantID, created.ID, 20, 0); !errors.Is(err, dataset.ErrNotFound) {
		t.Fatalf("cross-tenant ListVersions() error=%v, want ErrNotFound", err)
	}

	// 上游结构摘要漂移后，旧版本必须失败关闭；重新保存草稿才能捕获新的依赖快照。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET metadata_version=metadata_version+1,structure_hash=repeat('3',64) WHERE id::text=$1`, tableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := datasetStore.ValidateVersionDependencies(ctx, tenantID, created.ID, published.ID); !errors.Is(err, dataset.ErrVersionUnavailable) {
		t.Fatalf("ValidateVersionDependencies() error=%v, want ErrVersionUnavailable", err)
	}
	publishedDocument, err := dataset.DecodeAndNormalize(published.DSL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeStore.ResolveVersion(ctx, tenantID, created.ID, published.ID, publishedDocument); !errors.Is(err, dataset.ErrVersionUnavailable) {
		t.Fatalf("ResolveVersion(V1) error=%v, want ErrVersionUnavailable", err)
	}

	// 发布后继续编辑草稿不能回写已发布版本的 DSL、逻辑计划和派生索引快照。
	postPublishDSL := datasetExampleWithDescription(t, afterPublish.DSL, "发布后的草稿说明")
	postPublishDraft, err := service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{
		Name: afterPublish.Name, Description: "发布后的草稿说明", ExpectedVersion: afterPublish.Version, DSL: postPublishDSL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if postPublishDraft.DSLHash == published.DSLHash || postPublishDraft.CurrentPublishedVersionID != published.ID || postPublishDraft.Status != "PUBLISHED" {
		t.Fatalf("draft after published snapshot=%#v", postPublishDraft)
	}
	immutableVersion, err := service.GetVersion(ctx, tenantID, created.ID, published.ID)
	if err != nil || immutableVersion.DSLHash != published.DSLHash || immutableVersion.PlanHash != published.PlanHash ||
		string(immutableVersion.DSL) != string(published.DSL) || string(immutableVersion.LogicalPlan) != string(published.LogicalPlan) {
		t.Fatalf("immutable GetVersion() record=%#v err=%v", immutableVersion, err)
	}

	secondPublishInput := dataset.PublishInput{
		DraftVersionID:             postPublishDraft.DraftVersionID,
		ExpectedVersion:            postPublishDraft.Version,
		ExpectedDraftRecordVersion: postPublishDraft.DraftRecordVersion,
		ExpectedDSLHash:            postPublishDraft.DSLHash,
		ValidationParameters:       map[string]any{"start_date": "2026-01-01"},
	}
	publishedV2, err := service.Publish(ctx, tenantID, actorID, created.ID, publishKey+"-v2", secondPublishInput)
	if err != nil || publishedV2.VersionNo != 2 || publishedV2.ID == published.ID || publishedV2.DSLHash != postPublishDraft.DSLHash {
		t.Fatalf("second Publish() record=%#v err=%v", publishedV2, err)
	}
	if err := datasetStore.ValidateVersionDependencies(ctx, tenantID, created.ID, publishedV2.ID); err != nil {
		t.Fatalf("ValidateVersionDependencies(V2) error=%v", err)
	}
	publishedV2Document, err := dataset.DecodeAndNormalize(publishedV2.DSL)
	if err != nil {
		t.Fatal(err)
	}
	resolvedV2, err := runtimeStore.ResolveVersion(ctx, tenantID, created.ID, publishedV2.ID, publishedV2Document)
	if err != nil || resolvedV2.SourceID != sourceID || len(resolvedV2.Tables["orders"].Columns) != 3 {
		t.Fatalf("ResolveVersion(V2) plan=%#v err=%v", resolvedV2, err)
	}
	afterSecondPublish, err := service.Get(ctx, tenantID, created.ID)
	if err != nil || afterSecondPublish.CurrentPublishedVersionID != publishedV2.ID || afterSecondPublish.DraftVersionNo != 3 ||
		afterSecondPublish.Version != publishedV2.DatasetRecordVersion {
		t.Fatalf("dataset after second publish=%#v err=%v", afterSecondPublish, err)
	}
	versions, versionTotal, err = service.ListVersions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || versionTotal != 2 || len(versions) != 2 || versions[0].ID != publishedV2.ID || versions[1].ID != published.ID {
		t.Fatalf("ListVersions() after V2 versions=%#v total=%d err=%v", versions, versionTotal, err)
	}
	if validator.calls != 2 {
		t.Fatalf("publication validator calls=%d, want 2", validator.calls)
	}

	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.dataset_versions SET status='STALE',dsl_json=jsonb_set(dsl_json,'{dataset,description}','\"被篡改\"'::jsonb) WHERE id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`DELETE FROM platform.dataset_versions WHERE id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`INSERT INTO platform.dataset_fields(tenant_id,dataset_version_id,field_id,field_code,field_name,expression_json,canonical_type,field_role,ordinal_position) VALUES($1,$2,'forged_field','forged_field','伪造字段','{}','STRING','DIMENSION',999)`, tenantID, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.dataset_publish_idempotency SET response_json=jsonb_set(response_json,'{status}','\"STALE\"'::jsonb) WHERE dataset_id::text=$1 AND idempotency_key=$2`, created.ID, publishKey)

	// 当前发布版本失效时，版本状态和数据集指针必须在一个事务中同步收口。
	staleVersion, err := service.TransitionVersion(ctx, tenantID, actorID, created.ID, publishedV2.ID, dataset.VersionTransitionInput{
		ExpectedVersion: afterSecondPublish.Version, ExpectedStatus: "PUBLISHED", TargetStatus: "STALE",
	})
	if err != nil || staleVersion.Status != "STALE" || staleVersion.ID != publishedV2.ID || string(staleVersion.DSL) != string(publishedV2.DSL) {
		t.Fatalf("TransitionVersion() record=%#v err=%v", staleVersion, err)
	}
	afterStale, err := service.Get(ctx, tenantID, created.ID)
	if err != nil || afterStale.Status != "STALE" || afterStale.CurrentPublishedVersionID != "" || afterStale.Version != afterSecondPublish.Version+1 {
		t.Fatalf("dataset after stale=%#v err=%v", afterStale, err)
	}
}

func datasetExampleWithDescription(t *testing.T, raw []byte, description string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["dataset"].(map[string]any)["description"] = description
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func datasetExampleForAssets(t *testing.T, sourceID, tableID string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	node := document["nodes"].([]any)[0].(map[string]any)
	node["datasourceId"], node["tableId"] = sourceID, tableID
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func datasetExampleForPublishedVersion(t *testing.T, versionID string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/dataset-dsl-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	descriptor := document["dataset"].(map[string]any)
	descriptor["code"] = "monthly_orders_consumer"
	descriptor["name"] = "月度订单消费数据集"
	descriptor["description"] = "引用精确发布版本"
	document["nodes"] = []any{map[string]any{
		"id": "upstream", "type": "DATASET", "datasetVersionId": versionID, "alias": "u",
		"projection": []any{"stat_month", "revenue"}, "sourceFilters": []any{},
	}}
	fields := document["fields"].([]any)
	fields[0].(map[string]any)["expression"] = map[string]any{"type": "FIELD_REF", "nodeId": "upstream", "field": "stat_month"}
	fields[1].(map[string]any)["expression"] = map[string]any{"type": "FIELD_REF", "nodeId": "upstream", "field": "revenue"}
	filterExpression := document["filters"].([]any)[0].(map[string]any)["expression"].(map[string]any)
	filterExpression["left"] = map[string]any{"type": "FIELD_REF", "nodeId": "upstream", "field": "stat_month"}
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type publicationValidatorStub struct {
	result    dataset.PreviewResult
	candidate dataset.PublicationCandidate
	calls     int
}

func (stub *publicationValidatorStub) ValidatePublication(_ context.Context, _, _ string, candidate dataset.PublicationCandidate) (dataset.PreviewResult, error) {
	stub.calls++
	stub.candidate = candidate
	return stub.result, nil
}

func assertTenantSQLRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, statement string, arguments ...any) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, statement, arguments...)
		return err
	})
	if err == nil {
		t.Fatalf("数据库接受了应被不可变约束拒绝的写入：%s", statement)
	}
}

func cleanupDatasetTenant(pool *pgxpool.Pool, tenantID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"query_runs", "asset_dependencies", "dataset_dependencies", "dataset_parameters", "dataset_fields"} {
			if _, err := tx.Exec(ctx, "DELETE FROM platform."+table+" WHERE tenant_id=$1", tenantID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET current_draft_version_id=NULL,current_published_version_id=NULL WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		for _, table := range []string{"dataset_versions", "datasets"} {
			if _, err := tx.Exec(ctx, "DELETE FROM platform."+table+" WHERE tenant_id=$1", tenantID); err != nil {
				return err
			}
		}
		return nil
	})
}
