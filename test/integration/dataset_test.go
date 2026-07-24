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
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasettagsuggestion"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/semanticmanagement"
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
		sourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "orders", "Orders", "MYSQL", "env://DATASET_IT", "ACTIVE", "{}",
		)
		if err != nil {
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
		customerSourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "customers", "Customers", "ORACLE",
			"env://DATASET_ORACLE_IT", "ACTIVE", "{}",
		)
		if err != nil {
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
	attestAndPublishDataSourceFixture(t, ctx, pool, tenantID, actorID, sourceID)
	attestAndPublishDataSourceFixture(
		t, ctx, pool, tenantID, actorID, customerSourceID,
	)
	raw := datasetODSExampleForAssets(t, sourceID, tableID)
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
	revisions, revisionTotal, err := service.ListRevisions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || revisionTotal != 2 || len(revisions) != 2 ||
		revisions[0].VersionNo != updated.Version || revisions[0].OperationType != "SAVE" ||
		revisions[1].VersionNo != created.Version || revisions[1].OperationType != "CREATE" {
		t.Fatalf("ListRevisions() revisions=%#v total=%d err=%v", revisions, revisionTotal, err)
	}
	createRevision, err := service.GetRevision(ctx, tenantID, created.ID, revisions[1].ID)
	if err != nil || createRevision.DSLHash != created.DSLHash || createRevision.Name != created.Name {
		t.Fatalf("GetRevision(CREATE) record=%#v err=%v", createRevision, err)
	}
	rolledBack, err := service.RollbackRevision(ctx, tenantID, actorID, created.ID, createRevision.ID, dataset.RollbackRevisionInput{ExpectedVersion: updated.Version})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Version != updated.Version+1 || rolledBack.DraftVersionID != updated.DraftVersionID ||
		rolledBack.DraftRecordVersion != updated.DraftRecordVersion+1 || rolledBack.DSLHash != created.DSLHash ||
		rolledBack.Description != created.Description || rolledBack.CurrentPublishedVersionID != "" {
		t.Fatalf("RollbackRevision() record=%#v", rolledBack)
	}
	revisions, revisionTotal, err = service.ListRevisions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || revisionTotal != 3 || len(revisions) != 3 || revisions[0].VersionNo != rolledBack.Version ||
		revisions[0].OperationType != "ROLLBACK" || revisions[0].SourceRevisionID != createRevision.ID {
		t.Fatalf("ListRevisions() after rollback revisions=%#v total=%d err=%v", revisions, revisionTotal, err)
	}
	unchangedCreateRevision, err := service.GetRevision(ctx, tenantID, created.ID, createRevision.ID)
	if err != nil || unchangedCreateRevision.DSLHash != createRevision.DSLHash || string(unchangedCreateRevision.DSL) != string(createRevision.DSL) {
		t.Fatalf("source revision changed after rollback: record=%#v err=%v", unchangedCreateRevision, err)
	}
	var rollbackAudits int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET' AND resource_id=$1 AND action='ROLLBACK_DRAFT'
			  AND detail->>'sourceRevisionId'=$2`, created.ID, createRevision.ID).Scan(&rollbackAudits)
	})
	if err != nil || rollbackAudits != 1 {
		t.Fatalf("rollback audit count=%d err=%v", rollbackAudits, err)
	}
	if _, err := service.RollbackRevision(ctx, tenantID, actorID, created.ID, createRevision.ID, dataset.RollbackRevisionInput{ExpectedVersion: updated.Version}); !errors.Is(err, dataset.ErrConflict) {
		t.Fatalf("stale RollbackRevision() error=%v, want ErrConflict", err)
	}
	if _, err := service.GetRevision(ctx, foreignTenantID, created.ID, createRevision.ID); !errors.Is(err, dataset.ErrRevisionNotFound) {
		t.Fatalf("cross-tenant GetRevision() error=%v, want ErrRevisionNotFound", err)
	}
	if _, _, err := service.ListRevisions(ctx, foreignTenantID, created.ID, 20, 0); !errors.Is(err, dataset.ErrNotFound) {
		t.Fatalf("cross-tenant ListRevisions() error=%v, want ErrNotFound", err)
	}
	// 后续保存从回滚产生的新基线继续推进，恢复操作本身不会产生发布版本。
	updated, err = service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{
		Name: created.Name, Description: "新说明", ExpectedVersion: rolledBack.Version, DSL: updatedDSL,
	})
	if err != nil || updated.Version != rolledBack.Version+1 || updated.DSLHash == rolledBack.DSLHash {
		t.Fatalf("Update() after rollback record=%#v err=%v", updated, err)
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
	// 本段只验证迁移前“物理表直连”草稿的兼容读取与审计，因此刻意保留为
	// 未声明 layer 的历史正文。新建的显式 DWD 必须走 ODS DATASET 上游，
	// 严格分层合同由下方独立的 ODS -> DWD 发布用例覆盖。
	crossDocument.Dataset.Layer = ""
	crossDocument.Nodes = append(append([]dataset.Node(nil), document.Nodes...), dataset.Node{
		ID: "customers", Type: "TABLE", DataSourceID: customerSourceID, TableID: customerTableID, Alias: "c",
		Projection: []string{"customer_id", "customer_name"}, SourceFilters: []dataset.SourceFilter{},
	})
	crossDocument.Joins = []dataset.Join{{
		ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "LEFT", Cardinality: "UNKNOWN", ManualConfirmed: true,
		Conditions: []dataset.JoinCondition{{
			LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"},
			Operator:        "EQUALS",
			RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"},
		}},
	}}
	crossResolved, err := runtimeStore.Resolve(ctx, tenantID, crossDocument)
	if err != nil || len(crossResolved.Nodes) != 2 || crossResolved.Nodes["customers"].SourceID != customerSourceID || crossResolved.Nodes["orders"].Watermark == "" || crossResolved.Nodes["customers"].Watermark == "" {
		t.Fatalf("Resolve() cross plan=%#v err=%v", crossResolved, err)
	}
	// 草稿的类型是节点来源的派生摘要：加入跨源节点后保存为 CROSS_SOURCE，
	// 再移除节点时能够回到 SINGLE_SOURCE，编码和已发布版本均不受影响。
	crossDSL, err := json.Marshal(crossDocument)
	if err != nil {
		t.Fatal(err)
	}
	crossUpdated, err := service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{
		Name: updated.Name, Description: updated.Description, ExpectedVersion: updated.Version, DSL: crossDSL,
	})
	if err != nil || crossUpdated.Type != "CROSS_SOURCE" {
		t.Fatalf("cross-source Update() record=%#v err=%v", crossUpdated, err)
	}
	updated, err = service.Update(ctx, tenantID, actorID, created.ID, dataset.UpdateInput{
		Name: updated.Name, Description: updated.Description, ExpectedVersion: crossUpdated.Version, DSL: updated.DSL,
	})
	if err != nil || updated.Type != "SINGLE_SOURCE" {
		t.Fatalf("single-source Update() record=%#v err=%v", updated, err)
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
	var tagJobStatus, tagJobSchemaHash, tagJobLayer, tagJobPromptVersion, tagJobSourceSnapshot string
	var persistedDatasetVersion int64
	var persistedDraftVersionNo, publishedFields, publishedParameters, publishedDependencies, idempotencyRows, publishAudits, tagJobCount int
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
		if err := tx.QueryRow(ctx, `SELECT count(*)::int,min(status),min(schema_hash),min(layer),
				min(prompt_version),min(source_version_snapshot::text)
				FROM platform.dataset_tag_suggestion_jobs
				WHERE dataset_id::text=$1 AND dataset_version_id::text=$2`,
			created.ID, published.ID,
		).Scan(
			&tagJobCount, &tagJobStatus, &tagJobSchemaHash, &tagJobLayer,
			&tagJobPromptVersion, &tagJobSourceSnapshot,
		); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs WHERE resource_type='DATASET' AND resource_id=$1 AND action='PUBLISH'`, created.ID).Scan(&publishAudits)
	})
	if err != nil || persistedDatasetStatus != "PUBLISHED" || persistedVersionStatus != "PUBLISHED" ||
		persistedDatasetVersion != afterPublish.Version || persistedPointer != published.ID || persistedDraftVersionNo != 2 ||
		publishedFields != 2 || publishedParameters != 1 || publishedDependencies != 1 ||
		persistedSourceHash != strings.Repeat("1", 64) || persistedSourcePlanHash != "" || idempotencyRows != 1 || publishAudits != 1 ||
		tagJobCount != 1 || tagJobStatus != "PENDING" || tagJobSchemaHash != published.DSLHash ||
		tagJobLayer != "ODS" || tagJobPromptVersion != datasettagsuggestion.PromptVersion ||
		!strings.Contains(tagJobSourceSnapshot, sourceID) {
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
	tagSuggestionStore := datasettagsuggestion.NewPostgresStore(pool)
	tagSuggestionClaim, err := tagSuggestionStore.ClaimNext(
		ctx, tenantID, "dataset-integration-tag-worker", 10*time.Minute,
	)
	if err != nil || tagSuggestionClaim == nil ||
		tagSuggestionClaim.DatasetVersionID != published.ID {
		t.Fatalf("claim published dataset tag job=%+v err=%v", tagSuggestionClaim, err)
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
	if _, err := tagSuggestionStore.LoadInput(
		ctx, *tagSuggestionClaim, "dataset-integration-tag-worker",
	); !errors.Is(err, datasettagsuggestion.ErrSubjectChanged) {
		t.Fatalf("superseded published version tag input error=%v", err)
	}
	if err := tagSuggestionStore.Skip(
		ctx, *tagSuggestionClaim, "dataset-integration-tag-worker", "SUBJECT_CHANGED",
	); err != nil {
		t.Fatalf("skip superseded tag job: %v", err)
	}
	var oldTagJobStatus string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status
			FROM platform.dataset_tag_suggestion_jobs WHERE id=$1`,
			tagSuggestionClaim.ID,
		).Scan(&oldTagJobStatus)
	})
	if err != nil || oldTagJobStatus != "SKIPPED" {
		t.Fatalf("old tag job status=%q err=%v", oldTagJobStatus, err)
	}
	tagID := uuid.NewString()
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.ai_tenant_policies
			SET enabled=true WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_tags(
			id,tenant_id,code,name,description,category,governance,status,
			created_by,updated_by
		) VALUES(
			$1,$2,'order_detail_fact','订单明细事实表',
			'保存订单粒度业务事实','TABLE_FUNCTION','CONTROLLED','ACTIVE',$3,$3
		)`, tagID, tenantID, actorID)
		return err
	})
	if err != nil {
		t.Fatalf("prepare controlled taxonomy: %v", err)
	}
	aiService, err := aiplatform.NewService(
		aiplatform.NewPostgresStore(pool),
		&datasetTagSuggestionProvider{tagID: tagID},
		aiplatform.ServiceOptions{
			Timeout: time.Second, AttemptTimeout: time.Second,
			MaxAttempts: 1, BaseRetryDelay: time.Millisecond,
			MaxRetryDelay: time.Millisecond, MaxInputBytes: 256 << 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tagWorker := datasettagsuggestion.NewWorker(
		tagSuggestionStore,
		datasettagsuggestion.NewGenerator(aiService, time.Second),
	)
	tagJobStatus = ""
	for attempt := 0; attempt < 4 && tagJobStatus != "SUCCEEDED"; attempt++ {
		processed, processErr := tagWorker.ProcessNext(
			ctx, tenantID, "dataset-integration-tag-worker-v2", 2*time.Minute,
		)
		if processErr != nil {
			t.Fatalf("process dataset tag suggestion: %v", processErr)
		}
		if !processed {
			t.Fatal("expected a pending dataset tag suggestion job")
		}
		err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status
				FROM platform.dataset_tag_suggestion_jobs
				WHERE dataset_version_id=$1`,
				publishedV2.ID,
			).Scan(&tagJobStatus)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if tagJobStatus != "SUCCEEDED" {
		t.Fatalf("current tag job status=%q", tagJobStatus)
	}
	var bindingID, bindingStatus, bindingOrigin, bindingRecordVersion string
	var bindingEvidence json.RawMessage
	var bindingConfidence *float64
	var suggestionItems, suggestionRowsInEvidence, outboxVersionBefore int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text,status,origin,xmin::text,
			evidence_json,confidence
			FROM platform.asset_tag_bindings
			WHERE tag_id=$1 AND dataset_id=$2 AND dataset_version_id=$3`,
			tagID, created.ID, publishedV2.ID,
		).Scan(
			&bindingID, &bindingStatus, &bindingOrigin, &bindingRecordVersion,
			&bindingEvidence, &bindingConfidence,
		); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.dataset_tag_suggestion_items AS item
			JOIN platform.dataset_tag_suggestion_jobs AS job ON job.id=item.job_id
			WHERE job.dataset_version_id=$1 AND item.binding_id=$2`,
			publishedV2.ID, bindingID,
		).Scan(&suggestionItems); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.asset_tag_bindings
			WHERE id=$1 AND (
			  evidence_json ? 'rows'
			  OR evidence_json ? 'sampleRows'
			  OR evidence_json ? 'rawData'
			)`, bindingID).Scan(&suggestionRowsInEvidence); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT event_version
			FROM platform.semantic_change_outbox
			WHERE subject_type='DATASET_VERSION' AND subject_ref=$1`,
			publishedV2.ID,
		).Scan(&outboxVersionBefore)
	})
	if err != nil || bindingStatus != "SUGGESTED" || bindingOrigin != "LLM" ||
		bindingConfidence == nil || suggestionItems != 1 ||
		suggestionRowsInEvidence != 0 {
		t.Fatalf(
			"binding=%s status=%s origin=%s confidence=%v items=%d rowEvidence=%d evidence=%s err=%v",
			bindingID, bindingStatus, bindingOrigin, bindingConfidence,
			suggestionItems, suggestionRowsInEvidence, bindingEvidence, err,
		)
	}
	semanticService := semanticmanagement.NewService(
		semanticmanagement.NewPostgresStore(pool),
	)
	approvedBinding, err := semanticService.UpdateAssetTagBinding(
		ctx, tenantID, actorID, bindingID,
		semanticmanagement.UpdateAssetTagBindingInput{
			ExpectedRecordVersion: bindingRecordVersion,
			Origin:                "LLM",
			Status:                "APPROVED",
			Confidence:            bindingConfidence,
			Evidence:              bindingEvidence,
		},
	)
	if err != nil || approvedBinding.Status != "APPROVED" ||
		approvedBinding.ApprovedBy != actorID {
		t.Fatalf("approve suggested binding=%+v err=%v", approvedBinding, err)
	}
	var outboxVersionAfter int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT event_version
			FROM platform.semantic_change_outbox
			WHERE subject_type='DATASET_VERSION' AND subject_ref=$1`,
			publishedV2.ID,
		).Scan(&outboxVersionAfter)
	})
	if err != nil || outboxVersionAfter <= outboxVersionBefore {
		t.Fatalf(
			"approval semantic outbox before=%d after=%d err=%v",
			outboxVersionBefore, outboxVersionAfter, err,
		)
	}

	// 并发停用可能发生在保存的首次可用性检查之后。保存事务在固定依赖快照时必须
	// 对版本和所属数据集持共享锁；停用提交后重新判断谓词并失败关闭，不能留下新依赖。
	concurrentDependencyDSL := datasetExampleWithIdentity(t,
		datasetExampleForPublishedVersion(t, publishedV2.ID),
		"concurrent_disabled_consumer", "并发停用消费数据集", "并发停用时不得绑定历史发布版本",
	)
	ownerLocked := make(chan error, 1)
	releaseOwner := make(chan struct{})
	disableDone := make(chan error, 1)
	go func() {
		disableDone <- database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var status, publishedVersionID string
			err := tx.QueryRow(ctx, `SELECT status,COALESCE(current_published_version_id::text,'')
				FROM platform.datasets WHERE id::text=$1 AND deleted_at IS NULL FOR UPDATE`, created.ID).
				Scan(&status, &publishedVersionID)
			if err == nil && (status != "PUBLISHED" || publishedVersionID != publishedV2.ID) {
				err = fmt.Errorf("unexpected upstream before concurrent disable: status=%s version=%s", status, publishedVersionID)
			}
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE platform.datasets SET
					status='DISABLED',current_published_version_id=NULL,
					disabled_from_status='PUBLISHED',disabled_published_version_id=$1,
					version=version+1,updated_by=$2 WHERE id::text=$3`, publishedV2.ID, actorID, created.ID)
			}
			ownerLocked <- err
			if err != nil {
				return err
			}
			select {
			case <-releaseOwner:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	if err := <-ownerLocked; err != nil {
		if txErr := <-disableDone; txErr != nil && !errors.Is(txErr, err) {
			t.Logf("concurrent disable transaction error=%v", txErr)
		}
		t.Fatalf("lock upstream for concurrent disable: %v", err)
	}
	createDone := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{
			Code: "concurrent_disabled_consumer", Name: "并发停用消费数据集",
			Description: "并发停用时不得绑定历史发布版本", Type: "SINGLE_SOURCE", DSL: concurrentDependencyDSL,
		})
		createDone <- err
	}()
	lockWaitCtx, cancelLockWait := context.WithTimeout(ctx, 5*time.Second)
	lockWaitErr := waitForDatasetDependencyShareLock(lockWaitCtx, pool)
	cancelLockWait()
	close(releaseOwner)
	disableErr := <-disableDone
	createErr := <-createDone
	if lockWaitErr != nil {
		t.Fatalf("dependency snapshot did not wait for the owner lock: %v", lockWaitErr)
	}
	if disableErr != nil {
		t.Fatalf("concurrent disable transaction: %v", disableErr)
	}
	if !errors.Is(createErr, dataset.ErrInvalidDocument) {
		t.Fatalf("Create(concurrent disabled upstream) error=%v, want ErrInvalidDocument", createErr)
	}
	concurrentlyDisabled, err := service.Get(ctx, tenantID, created.ID)
	if err != nil || concurrentlyDisabled.Status != "DISABLED" {
		t.Fatalf("Get() after concurrent disable record=%#v err=%v", concurrentlyDisabled, err)
	}
	restoredAfterConcurrentDisable, err := service.Restore(ctx, tenantID, actorID, created.ID,
		dataset.LifecycleInput{ExpectedVersion: concurrentlyDisabled.Version})
	if err != nil || restoredAfterConcurrentDisable.Status != "PUBLISHED" ||
		restoredAfterConcurrentDisable.CurrentPublishedVersionID != publishedV2.ID {
		t.Fatalf("Restore() after concurrent disable record=%#v err=%v", restoredAfterConcurrentDisable, err)
	}
	afterSecondPublish = restoredAfterConcurrentDisable

	// 目录级停用保留不可变发布快照但阻止精确版本继续绑定；恢复后重新挂接同一版本。
	disabledPublished, err := service.Disable(ctx, tenantID, actorID, created.ID,
		dataset.LifecycleInput{ExpectedVersion: afterSecondPublish.Version})
	if err != nil || disabledPublished.Status != "DISABLED" || disabledPublished.CurrentPublishedVersionID != "" ||
		disabledPublished.Version != afterSecondPublish.Version+1 {
		t.Fatalf("Disable(PUBLISHED) record=%#v err=%v", disabledPublished, err)
	}
	if err := datasetStore.ValidateVersionDependencies(ctx, tenantID, created.ID, publishedV2.ID); !errors.Is(err, dataset.ErrVersionUnavailable) {
		t.Fatalf("disabled ValidateVersionDependencies() error=%v, want ErrVersionUnavailable", err)
	}
	disabledDependencyDSL := datasetExampleWithIdentity(t,
		datasetExampleForPublishedVersion(t, publishedV2.ID),
		"disabled_upstream_consumer", "停用上游消费数据集", "不得绑定已停用数据集的历史发布版本",
	)
	if _, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{
		Code: "disabled_upstream_consumer", Name: "停用上游消费数据集",
		Description: "不得绑定已停用数据集的历史发布版本", Type: "SINGLE_SOURCE", DSL: disabledDependencyDSL,
	}); !errors.Is(err, dataset.ErrInvalidDocument) {
		t.Fatalf("Create(disabled upstream) error=%v, want ErrInvalidDocument", err)
	}
	restoredPublished, err := service.Restore(ctx, tenantID, actorID, created.ID,
		dataset.LifecycleInput{ExpectedVersion: disabledPublished.Version})
	if err != nil || restoredPublished.Status != "PUBLISHED" || restoredPublished.CurrentPublishedVersionID != publishedV2.ID ||
		restoredPublished.Version != disabledPublished.Version+1 {
		t.Fatalf("Restore(PUBLISHED) record=%#v err=%v", restoredPublished, err)
	}
	if err := datasetStore.ValidateVersionDependencies(ctx, tenantID, created.ID, publishedV2.ID); err != nil {
		t.Fatalf("restored ValidateVersionDependencies() error=%v", err)
	}
	afterSecondPublish = restoredPublished

	// 已有当前发布版本时，回滚只产生新的草稿历史，不移动或改写发布指针。
	rollbackWithPublished, err := service.RollbackRevision(ctx, tenantID, actorID, created.ID, createRevision.ID,
		dataset.RollbackRevisionInput{ExpectedVersion: afterSecondPublish.Version})
	if err != nil || rollbackWithPublished.Version != afterSecondPublish.Version+1 ||
		rollbackWithPublished.CurrentPublishedVersionID != publishedV2.ID || rollbackWithPublished.Status != "PUBLISHED" ||
		rollbackWithPublished.DSLHash != createRevision.DSLHash {
		t.Fatalf("RollbackRevision() with current publication record=%#v err=%v", rollbackWithPublished, err)
	}
	versions, versionTotal, err = service.ListVersions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || versionTotal != 2 || len(versions) != 2 || versions[0].ID != publishedV2.ID || versions[1].ID != published.ID {
		t.Fatalf("published versions changed by draft rollback: versions=%#v total=%d err=%v", versions, versionTotal, err)
	}
	afterSecondPublish = rollbackWithPublished

	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.dataset_versions SET status='STALE',dsl_json=jsonb_set(dsl_json,'{dataset,description}','\"被篡改\"'::jsonb) WHERE id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`DELETE FROM platform.dataset_versions WHERE id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`INSERT INTO platform.dataset_fields(tenant_id,dataset_version_id,field_id,field_code,field_name,expression_json,canonical_type,field_role,ordinal_position) VALUES($1,$2,'forged_field','forged_field','伪造字段','{}','STRING','DIMENSION',999)`, tenantID, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.dataset_publish_idempotency SET response_json=jsonb_set(response_json,'{status}','\"STALE\"'::jsonb) WHERE dataset_id::text=$1 AND idempotency_key=$2`, created.ID, publishKey)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.dataset_draft_revisions SET description='被篡改' WHERE id::text=$1`, createRevision.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`DELETE FROM platform.dataset_draft_revisions WHERE id::text=$1`, createRevision.ID)

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

	// 失效态同样可以停用并恢复到停用前状态；仍有精确版本下游占用时软删除必须失败关闭。
	disabled, err := service.Disable(ctx, tenantID, actorID, created.ID, dataset.LifecycleInput{ExpectedVersion: afterStale.Version})
	if err != nil || disabled.Status != "DISABLED" || disabled.CurrentPublishedVersionID != "" || disabled.Version != afterStale.Version+1 {
		t.Fatalf("Disable() record=%#v err=%v", disabled, err)
	}
	restoredStale, err := service.Restore(ctx, tenantID, actorID, created.ID, dataset.LifecycleInput{ExpectedVersion: disabled.Version})
	if err != nil || restoredStale.Status != "STALE" || restoredStale.CurrentPublishedVersionID != "" || restoredStale.Version != disabled.Version+1 {
		t.Fatalf("Restore(STALE) record=%#v err=%v", restoredStale, err)
	}
	if err := service.Delete(ctx, tenantID, actorID, created.ID, dataset.LifecycleInput{ExpectedVersion: restoredStale.Version}); !errors.Is(err, dataset.ErrInUse) {
		t.Fatalf("Delete(in-use) error=%v, want ErrInUse", err)
	}

	// 未发布且无下游占用的数据集可以软删除，并立即从加载和目录接口隐藏。
	deletableDSL := datasetExampleWithIdentity(t, raw, "temporary_dataset", "临时数据集", "用于验证软删除")
	deletable, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{
		Code: "temporary_dataset", Name: "临时数据集", Description: "用于验证软删除", Type: "SINGLE_SOURCE", DSL: deletableDSL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, tenantID, actorID, deletable.ID, dataset.LifecycleInput{ExpectedVersion: deletable.Version}); err != nil {
		t.Fatalf("Delete() error=%v", err)
	}
	if _, err := service.Get(ctx, tenantID, deletable.ID); !errors.Is(err, dataset.ErrNotFound) {
		t.Fatalf("Get(deleted) error=%v, want ErrNotFound", err)
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

func datasetExampleWithIdentity(t *testing.T, raw []byte, code, name, description string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	descriptor := document["dataset"].(map[string]any)
	descriptor["code"], descriptor["name"], descriptor["description"] = code, name, description
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

func datasetODSExampleForAssets(t *testing.T, sourceID, tableID string) []byte {
	t.Helper()
	raw := datasetExampleForAssets(t, sourceID, tableID)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	// 本用例后续会构造 DATASET 下游并验证依赖锁；按 ODS -> DWD 的层级合同，
	// 先把通用 DWS 示例收敛为单物理表、无聚合的 ODS fixture。
	document["dataset"].(map[string]any)["layer"] = "ODS"
	document["groupBy"] = []any{}
	document["having"] = []any{}
	document["preAggregations"] = []any{}
	fields := document["fields"].([]any)
	fields[1].(map[string]any)["expression"] = map[string]any{
		"type": "FIELD_REF", "nodeId": "orders", "field": "order_amount",
	}
	raw, err := json.Marshal(document)
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
	descriptor["layer"] = "DWD"
	document["nodes"] = []any{map[string]any{
		"id": "upstream", "type": "DATASET", "datasetVersionId": versionID, "alias": "u",
		"projection": []any{"stat_month", "revenue"}, "sourceFilters": []any{},
	}}
	document["groupBy"] = []any{}
	document["having"] = []any{}
	document["preAggregations"] = []any{}
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

type datasetTagSuggestionProvider struct {
	tagID string
}

func (*datasetTagSuggestionProvider) Name() string     { return "integration-provider" }
func (*datasetTagSuggestionProvider) Model() string    { return "integration-model" }
func (*datasetTagSuggestionProvider) Configured() bool { return true }
func (provider *datasetTagSuggestionProvider) Complete(
	_ context.Context,
	_ aiplatform.ProviderRequest,
) (aiplatform.ProviderResult, error) {
	content, err := json.Marshal(map[string]any{"items": []map[string]any{{
		"tagId": provider.tagID, "confidence": 0.97,
		"rationale": "数据集说明、输出粒度和字段元数据共同支持该表功能",
	}}})
	if err != nil {
		return aiplatform.ProviderResult{}, err
	}
	return aiplatform.ProviderResult{
		Content: content, Model: "ignored-provider-model",
		FinishReason: "stop", RequestID: "integration-dataset-tag-request",
		Usage: aiplatform.Usage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		},
	}, nil
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

func waitForDatasetDependencyShareLock(ctx context.Context, pool *pgxpool.Pool) error {
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
			  AND state='active' AND wait_event_type='Lock'
			  AND position('FOR SHARE OF version,owner' in query)>0
		)`).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
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
