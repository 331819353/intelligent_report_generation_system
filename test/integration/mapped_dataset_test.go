//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestMappedDatasetIsCreatedWithIndependentPublishedV1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// 发布版本和幂等事实受数据库不可变约束保护；集成环境按一次性数据库处理，
	// 不为测试清理绕过生产约束。
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "mapped-default-publish-"+suffix)
	var actorID, reconcilerID, sourceID, sourceVersionID, tableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'mapped dataset owner','integration-hash') RETURNING id::text`,
			tenantID, "mapped-default-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'mapped dataset reconciler','integration-hash') RETURNING id::text`,
			tenantID, "mapped-reconcile-"+suffix+"@it.test").Scan(&reconcilerID); err != nil {
			return err
		}
		sourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "mapped_source_"+suffix, "Mapped Source", "MYSQL",
			"encrypted://mapped-default", "ACTIVE",
			`{"host":"db.internal","port":3306,"database":"sales","username":"reader"}`,
		)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,table_structure_hash,
			last_enriched_structure_hash,last_enriched_table_structure_hash,business_name,business_description,last_sync_at
		) VALUES($1,$2,'sales','orders','TABLE',repeat('a',64),repeat('b',64),repeat('a',64),repeat('b',64),
			'订单事实表','完整映射后的订单明细',now()) RETURNING id::text`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,
			last_enriched_structure_hash,business_name,business_description,semantic_type,is_primary_key,last_sync_at
		) VALUES($1,$2,'order_id',1,'bigint','INTEGER',false,repeat('c',64),repeat('c',64),
			'订单编号','订单业务主键','IDENTIFIER',true,now())`, tenantID, tableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedSource := attestAndPublishDataSourceFixture(
		t, ctx, pool, tenantID, actorID, sourceID,
	)
	sourceVersionID = publishedSource.PublishedVersionID

	store := dataset.NewPostgresStore(pool)
	materializationStore := materialization.NewPostgresStore(pool)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	})
	if err == nil || !strings.Contains(err.Error(), "materialization commit sink") {
		t.Fatalf("unconfigured mapped materialization sink error=%v", err)
	}
	var rolledBackDatasets int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.datasets
			WHERE origin_table_id=$1`, tableID).Scan(&rolledBackDatasets)
	})
	if err != nil || rolledBackDatasets != 0 {
		t.Fatalf("unconfigured sink did not roll back mapped publication: datasets=%d err=%v", rolledBackDatasets, err)
	}
	store.SetMappedPublicationCommitSink(materializationStore)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}

	var datasetID, status, draftID, draftStatus, publishedID, publishedStatus, sourceDraftID string
	var publishedOrigin dataset.PublicationOrigin
	var ownerVersion, draftRecordVersion, sourceDraftRecordVersion int64
	var draftVersionNo, publishedVersionNo int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT dataset.id::text,dataset.status,dataset.version,
			draft.id::text,draft.status,draft.version_no,draft.record_version,
			published.id::text,published.status,published.publication_origin,published.version_no,
			published.source_draft_version_id::text,published.source_draft_record_version
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft ON draft.id=dataset.current_draft_version_id
			JOIN platform.dataset_versions AS published ON published.id=dataset.current_published_version_id
			WHERE dataset.origin_table_id=$1`, tableID).Scan(
			&datasetID, &status, &ownerVersion,
			&draftID, &draftStatus, &draftVersionNo, &draftRecordVersion,
			&publishedID, &publishedStatus, &publishedOrigin, &publishedVersionNo,
			&sourceDraftID, &sourceDraftRecordVersion,
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "PUBLISHED" || ownerVersion != 2 || draftStatus != "DRAFT" || draftVersionNo != 2 || draftRecordVersion != 1 ||
		publishedStatus != "PUBLISHED" || publishedVersionNo != 1 || publishedID == draftID ||
		publishedOrigin != dataset.PublicationOriginSystemMappedDefault ||
		sourceDraftID != draftID || sourceDraftRecordVersion != draftRecordVersion {
		t.Fatalf("invalid mapped publication: dataset=%s status=%s ownerV=%d draft=%s/%s/V%d/R%d published=%s/%s/%s/V%d source=%s/R%d",
			datasetID, status, ownerVersion, draftID, draftStatus, draftVersionNo, draftRecordVersion,
			publishedID, publishedStatus, publishedOrigin, publishedVersionNo, sourceDraftID, sourceDraftRecordVersion)
	}
	builds, totalBuilds, err := materializationStore.ListBuilds(
		ctx, tenantID, datasetID, 10, 0,
	)
	if err != nil || totalBuilds != 1 || len(builds) != 1 ||
		builds[0].DatasetVersionID != publishedID ||
		builds[0].Layer != materialization.LayerODS ||
		builds[0].Mode != materialization.RunModeFull ||
		builds[0].Status != materialization.RunQueued {
		t.Fatalf("auto materialization builds=%#v total=%d err=%v", builds, totalBuilds, err)
	}
	buildDetail, err := materializationStore.GetBuild(
		ctx, tenantID, datasetID, builds[0].ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(buildDetail.Inputs) != 1 ||
		buildDetail.Inputs[0].Type != materialization.InputSourceTable ||
		buildDetail.Inputs[0].DataSourceID != sourceID ||
		buildDetail.Inputs[0].DataSourceVersionID != sourceVersionID ||
		buildDetail.Inputs[0].MetadataTableID != tableID ||
		buildDetail.Inputs[0].SchemaHash != strings.Repeat("a", 64) ||
		len(buildDetail.Nodes) != 3 {
		t.Fatalf("auto materialization detail=%#v", buildDetail)
	}
	versions, total, err := store.ListVersions(ctx, tenantID, datasetID, 10, 0)
	if err != nil || total != 1 || len(versions) != 1 || versions[0].ID != publishedID || versions[0].Status != "PUBLISHED" {
		t.Fatalf("published history=%#v total=%d err=%v", versions, total, err)
	}

	// API 与 worker 可并发执行启动对账；再次 Ensure 必须保持同一个发布 V1。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var publishedCount, autoPublishAuditCount, buildCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_versions
			WHERE dataset_id=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')`, datasetID).Scan(&publishedCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET' AND resource_id=$1 AND action='AUTO_PUBLISH_MAPPED_DEFAULT'`, datasetID).
			Scan(&autoPublishAuditCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_build_runs
			WHERE dataset_id=$1 AND dataset_version_id=$2`,
			datasetID, publishedID).Scan(&buildCount)
	})
	if err != nil || publishedCount != 1 || autoPublishAuditCount != 1 ||
		buildCount != 1 || builds[0].RequestedBy != actorID {
		t.Fatalf("idempotency published=%d audit=%d builds=%d err=%v", publishedCount, autoPublishAuditCount, buildCount, err)
	}

	// 删除后重新完成映射必须恢复同一个来源数据集主对象，并创建新的不可变 V2；
	// 否则来源表唯一约束会让文件重新映射成功但目录中没有数据集。
	if err := store.Delete(ctx, tenantID, actorID, datasetID, dataset.LifecycleInput{ExpectedVersion: ownerVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, tenantID, datasetID); !errors.Is(err, dataset.ErrNotFound) {
		t.Fatalf("deleted mapped dataset Get error=%v, want ErrNotFound", err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var regeneratedID, regeneratedStatus, regeneratedPublishedID, oldPublishedStatus string
	var regeneratedOrigin dataset.PublicationOrigin
	var regeneratedVersion int64
	var regeneratedPublishedNo, regenerateAuditCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT dataset.id::text,dataset.status,dataset.version,
			published.id::text,published.publication_origin,published.version_no,old_published.status
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS published ON published.id=dataset.current_published_version_id
			JOIN platform.dataset_versions AS old_published ON old_published.id=$2
			WHERE dataset.origin_table_id=$1 AND dataset.deleted_at IS NULL`, tableID, publishedID).Scan(
			&regeneratedID, &regeneratedStatus, &regeneratedVersion,
			&regeneratedPublishedID, &regeneratedOrigin, &regeneratedPublishedNo, &oldPublishedStatus,
		); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET' AND resource_id=$1 AND action='AUTO_REGENERATE_MAPPED_DATASET'`, datasetID).
			Scan(&regenerateAuditCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if regeneratedID != datasetID || regeneratedStatus != "PUBLISHED" || regeneratedVersion != 5 ||
		regeneratedPublishedID == publishedID || regeneratedPublishedNo != 2 || oldPublishedStatus != "DEPRECATED" || regenerateAuditCount != 1 {
		t.Fatalf("regenerated dataset=%s status=%s ownerV=%d published=%s/%s/V%d oldStatus=%s audit=%d",
			regeneratedID, regeneratedStatus, regeneratedVersion, regeneratedPublishedID,
			regeneratedOrigin, regeneratedPublishedNo, oldPublishedStatus, regenerateAuditCount)
	}
	if regeneratedOrigin != dataset.PublicationOriginSystemMappedRegenerate {
		t.Fatalf("regenerated publication origin=%s", regeneratedOrigin)
	}

	var secondSourceID, secondTableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var err error
		secondSourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "mapped_source_copy_"+suffix, "Mapped Source Copy", "MYSQL",
			"encrypted://mapped-default-copy", "ACTIVE",
			`{"host":"db-copy.internal","port":3306,"database":"sales","username":"reader"}`,
		)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,table_structure_hash,
			last_enriched_structure_hash,last_enriched_table_structure_hash,business_name,business_description,last_sync_at
		) VALUES($1,$2,'sales','orders_copy','TABLE',repeat('d',64),repeat('e',64),repeat('d',64),repeat('e',64),
			'订单事实表','另一个数据源中的同名订单表',now()) RETURNING id::text`, tenantID, secondSourceID).Scan(&secondTableID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,
			last_enriched_structure_hash,business_name,business_description,semantic_type,is_primary_key,last_sync_at
		) VALUES($1,$2,'order_id',1,'bigint','INTEGER',false,repeat('f',64),repeat('f',64),
			'订单编号','订单业务主键','IDENTIFIER',true,now())`, tenantID, secondTableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	attestAndPublishDataSourceFixture(
		t, ctx, pool, tenantID, actorID, secondSourceID,
	)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, secondTableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := store.List(ctx, tenantID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	sameNameSources := map[string]bool{}
	for _, item := range items {
		if item.Name == "订单事实表" {
			sameNameSources[item.OriginDataSourceName] = true
		}
	}
	if total != 2 || len(sameNameSources) != 2 || !sameNameSources["Mapped Source"] || !sameNameSources["Mapped Source Copy"] {
		t.Fatalf("same-name datasets total=%d sources=%#v items=%#v", total, sameNameSources, items)
	}

	// 人工保存会使草稿偏离当前系统发布的精确来源修订。后续元数据变化不得
	// 借“映射表刷新”覆盖人工草稿，也不得移动发布指针、登记 build 或审计。
	current, err := store.Get(ctx, tenantID, datasetID)
	if err != nil {
		t.Fatal(err)
	}
	var manualDocument dataset.Document
	if err := json.Unmarshal(current.DSL, &manualDocument); err != nil {
		t.Fatal(err)
	}
	manualDocument.Dataset.Description = "人工维护的订单口径，必须保留"
	manualRaw, err := json.Marshal(manualDocument)
	if err != nil {
		t.Fatal(err)
	}
	manualPrepared, err := dataset.Prepare(manualRaw)
	if err != nil {
		t.Fatal(err)
	}
	manuallySaved, err := store.Update(ctx, tenantID, actorID, datasetID, dataset.UpdateInput{
		Name: current.Name, Description: manualDocument.Dataset.Description,
		ExpectedVersion: current.Version, DSL: manualRaw,
	}, manualPrepared)
	if err != nil {
		t.Fatal(err)
	}
	advanceMappedTableMetadata(t, ctx, pool, tenantID, tableID, "1", "2", "元数据刷新后的系统说明")
	beforeManualRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterManualRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	if !reflect.DeepEqual(afterManualRefresh, beforeManualRefresh) {
		t.Fatalf("metadata refresh overwrote manual draft:\nbefore=%#v\nafter=%#v",
			beforeManualRefresh, afterManualRefresh)
	}

	// 即使人工草稿经过审批成为当前发布，draft 与 published source 再次相等，
	// 它也没有不可变的系统发布来源，后续映射变化仍不得重置人工定制。
	manuallySavedForApproval, err := store.Update(ctx, tenantID, actorID, datasetID, dataset.UpdateInput{
		Name: manuallySaved.Name, Description: manualDocument.Dataset.Description,
		ExpectedVersion: manuallySaved.Version, DSL: manualRaw,
	}, manualPrepared)
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$2)
		ON CONFLICT(tenant_id,subject_type,subject_id,object_type,object_id,action) DO NOTHING`,
			tenantID, actorID, datasetID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, reservedVersionID := insertMappedPublicationRequest(
		t, ctx, pool, tenantID, actorID, datasetID,
	)
	approvedRequest, manuallyPublished, err := store.ApproveAndPublish(
		ctx, tenantID, actorID, datasetID, requestID, 1, "批准人工维护口径",
		dataset.PublishPlan{
			IdempotencyKey: requestID, RequestHash: strings.Repeat("9", 64),
			ExpectedVersion:            manuallySavedForApproval.Version,
			DraftVersionID:             manuallySavedForApproval.DraftVersionID,
			ExpectedDraftRecordVersion: manuallySavedForApproval.DraftRecordVersion,
			ExpectedDSLHash:            manuallySavedForApproval.DSLHash,
			ReservedPublishedVersionID: reservedVersionID,
			Prepared:                   manualPrepared,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if approvedRequest.Status != dataset.PublicationRequestApproved ||
		manuallyPublished.ID == "" || manuallyPublished.DSLHash != manuallySavedForApproval.DSLHash ||
		manuallyPublished.PublicationOrigin != dataset.PublicationOriginHumanApproval {
		t.Fatalf("manual approval request=%#v published=%#v", approvedRequest, manuallyPublished)
	}
	insertForgedMappedPublicationAudit(
		t, ctx, pool, tenantID, actorID, tableID, manuallyPublished,
		"AUTO_REFRESH_MAPPED_DATASET", "SYSTEM_MAPPED_REFRESH",
	)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.dataset_versions
			SET publication_origin='SYSTEM_MAPPED_REFRESH' WHERE id=$1`, manuallyPublished.ID)
		return err
	})
	if err == nil {
		t.Fatal("published publication_origin was mutable")
	}
	advanceMappedTableMetadata(t, ctx, pool, tenantID, tableID, "3", "4", "再次变化的系统说明")
	beforeApprovedRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterApprovedRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	if !reflect.DeepEqual(afterApprovedRefresh, beforeApprovedRefresh) {
		t.Fatalf("metadata refresh replaced human-approved publication:\nbefore=%#v\nafter=%#v",
			beforeApprovedRefresh, afterApprovedRefresh)
	}

	// 软删除同样不是丢弃人工修订的授权。再次映射时应保持删除状态和全部事实。
	if err := store.Delete(ctx, tenantID, actorID, datasetID, dataset.LifecycleInput{
		ExpectedVersion: beforeApprovedRefresh.DatasetVersion,
	}); err != nil {
		t.Fatal(err)
	}
	beforeUnsafeRegenerate := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterUnsafeRegenerate := readMappedDatasetSnapshot(t, ctx, pool, tenantID, datasetID)
	if !reflect.DeepEqual(afterUnsafeRegenerate, beforeUnsafeRegenerate) {
		t.Fatalf("remapping regenerated a deleted human draft:\nbefore=%#v\nafter=%#v",
			beforeUnsafeRegenerate, afterUnsafeRegenerate)
	}

	// 未修改的系统草稿即使与当前发布完全一致，只要存在 PENDING 审批，也必须
	// 让人工流程拥有优先权；元数据变化不能改草稿、发布、build 或审计。
	var secondDatasetID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.datasets
			WHERE origin_table_id=$1`, secondTableID).Scan(&secondDatasetID)
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeAllowedRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	advanceMappedTableMetadata(t, ctx, pool, tenantID, secondTableID, "7", "8", "系统维护的映射说明")
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, secondTableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterAllowedRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	if afterAllowedRefresh.PublishedVersionID == beforeAllowedRefresh.PublishedVersionID ||
		afterAllowedRefresh.PublishedOrigin != dataset.PublicationOriginSystemMappedRefresh ||
		afterAllowedRefresh.PublishedCount != beforeAllowedRefresh.PublishedCount+1 ||
		afterAllowedRefresh.BuildCount != beforeAllowedRefresh.BuildCount+1 ||
		afterAllowedRefresh.AuditCount != beforeAllowedRefresh.AuditCount+2 {
		t.Fatalf("eligible system-owned mapping was not refreshed:\nbefore=%#v\nafter=%#v",
			beforeAllowedRefresh, afterAllowedRefresh)
	}
	pendingRequestID, _ := insertMappedPublicationRequest(t, ctx, pool, tenantID, actorID, secondDatasetID)
	advanceMappedTableMetadata(t, ctx, pool, tenantID, secondTableID, "5", "6", "待审批期间的元数据变化")
	beforePendingRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, secondTableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterPendingRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	if !reflect.DeepEqual(afterPendingRefresh, beforePendingRefresh) {
		t.Fatalf("metadata refresh bypassed pending approval:\nbefore=%#v\nafter=%#v",
			beforePendingRefresh, afterPendingRefresh)
	}

	// DIRECT 版本即使被追加一条内容完整的伪造 AUTO 审计，也不能取得系统刷新资格。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$2)
		ON CONFLICT(tenant_id,subject_type,subject_id,object_type,object_id,action) DO NOTHING`,
			tenantID, actorID, secondDatasetID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RejectPublicationRequest(
		ctx, tenantID, actorID, secondDatasetID, pendingRequestID,
		dataset.RejectPublicationInput{ExpectedVersion: 1, Reason: "转为兼容直发测试"},
	); err != nil {
		t.Fatal(err)
	}
	directCurrent, err := store.Get(ctx, tenantID, secondDatasetID)
	if err != nil {
		t.Fatal(err)
	}
	var directDocument dataset.Document
	if err := json.Unmarshal(directCurrent.DSL, &directDocument); err != nil {
		t.Fatal(err)
	}
	directDocument.Dataset.Description = "兼容直发的人工作业口径"
	directRaw, err := json.Marshal(directDocument)
	if err != nil {
		t.Fatal(err)
	}
	directPrepared, err := dataset.Prepare(directRaw)
	if err != nil {
		t.Fatal(err)
	}
	directSaved, err := store.Update(ctx, tenantID, actorID, secondDatasetID, dataset.UpdateInput{
		Name: directCurrent.Name, Description: directDocument.Dataset.Description,
		ExpectedVersion: directCurrent.Version, DSL: directRaw,
	}, directPrepared)
	if err != nil {
		t.Fatal(err)
	}
	directPublished, err := store.Publish(ctx, tenantID, actorID, secondDatasetID, dataset.PublishPlan{
		IdempotencyKey: "direct-mapped-" + suffix, RequestHash: strings.Repeat("8", 64),
		ExpectedVersion: directSaved.Version, DraftVersionID: directSaved.DraftVersionID,
		ExpectedDraftRecordVersion: directSaved.DraftRecordVersion,
		ExpectedDSLHash:            directSaved.DSLHash, Prepared: directPrepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if directPublished.PublicationOrigin != dataset.PublicationOriginDirect {
		t.Fatalf("direct publication origin=%s", directPublished.PublicationOrigin)
	}
	insertForgedMappedPublicationAudit(
		t, ctx, pool, tenantID, actorID, secondTableID, directPublished,
		"AUTO_REFRESH_MAPPED_DATASET", "SYSTEM_MAPPED_REFRESH",
	)
	advanceMappedTableMetadata(t, ctx, pool, tenantID, secondTableID, "9", "a", "直发后的元数据变化")
	beforeDirectRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, reconcilerID, secondTableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	afterDirectRefresh := readMappedDatasetSnapshot(t, ctx, pool, tenantID, secondDatasetID)
	if !reflect.DeepEqual(afterDirectRefresh, beforeDirectRefresh) {
		t.Fatalf("forged AUTO audit authorized DIRECT refresh:\nbefore=%#v\nafter=%#v",
			beforeDirectRefresh, afterDirectRefresh)
	}
}

type mappedDatasetPersistenceSnapshot struct {
	Deleted            bool
	Status             string
	DatasetVersion     int64
	Name               string
	Description        string
	DraftVersionID     string
	DraftRecordVersion int64
	DraftSchemaHash    string
	DraftPlanHash      string
	DraftDSL           string
	DraftLogicalPlan   string
	PublishedVersionID string
	PublishedOrigin    dataset.PublicationOrigin
	PublishedCount     int
	BuildCount         int
	AuditCount         int
}

func readMappedDatasetSnapshot(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, datasetID string,
) mappedDatasetPersistenceSnapshot {
	t.Helper()
	var snapshot mappedDatasetPersistenceSnapshot
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT dataset.deleted_at IS NOT NULL,dataset.status,dataset.version,
			dataset.name,dataset.description,draft.id::text,draft.record_version,
			draft.schema_hash,draft.plan_hash,draft.dsl_json::text,draft.logical_plan_json::text,
			COALESCE(dataset.current_published_version_id::text,''),
			COALESCE((
				SELECT published.publication_origin
				FROM platform.dataset_versions AS published
				WHERE published.id=dataset.current_published_version_id
			),''),
			(SELECT count(*) FROM platform.dataset_versions AS published
			 WHERE published.dataset_id=dataset.id
			   AND published.status IN ('PUBLISHED','STALE','DEPRECATED')),
			(SELECT count(*) FROM platform.dataset_build_runs AS build
			 WHERE build.dataset_id=dataset.id),
			(SELECT count(*) FROM platform.audit_logs AS audit
			 WHERE audit.resource_type='DATASET' AND audit.resource_id=dataset.id::text)
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft
			  ON draft.id=dataset.current_draft_version_id
			 AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
			WHERE dataset.id=$1`, datasetID).Scan(
			&snapshot.Deleted, &snapshot.Status, &snapshot.DatasetVersion,
			&snapshot.Name, &snapshot.Description, &snapshot.DraftVersionID,
			&snapshot.DraftRecordVersion, &snapshot.DraftSchemaHash, &snapshot.DraftPlanHash,
			&snapshot.DraftDSL, &snapshot.DraftLogicalPlan, &snapshot.PublishedVersionID,
			&snapshot.PublishedOrigin,
			&snapshot.PublishedCount, &snapshot.BuildCount, &snapshot.AuditCount,
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func advanceMappedTableMetadata(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, tableID, structureDigit, tableStructureDigit, description string,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET
			metadata_version=metadata_version+1,
			structure_hash=repeat($2,64),table_structure_hash=repeat($3,64),
			last_enriched_structure_hash=repeat($2,64),
			last_enriched_table_structure_hash=repeat($3,64),
			business_description=$4
			WHERE id=$1`, tableID, structureDigit, tableStructureDigit, description)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("metadata table %s was not updated", tableID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertMappedPublicationRequest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, actorID, datasetID string,
) (requestID, reservedVersionID string) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.dataset_publication_requests(
			tenant_id,dataset_id,draft_version_id,expected_dataset_version,
			expected_draft_record_version,expected_dsl_hash,expected_plan_hash,
			requester_user_id,request_note
		)
		SELECT dataset.tenant_id,dataset.id,draft.id,dataset.version,
			draft.record_version,draft.schema_hash,draft.plan_hash,$2,'integration approval fence'
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id
		 AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
		WHERE dataset.id=$1
		RETURNING id::text,reserved_published_version_id::text`,
			datasetID, actorID).Scan(&requestID, &reservedVersionID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return requestID, reservedVersionID
}

func insertForgedMappedPublicationAudit(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, actorID, tableID string,
	version dataset.VersionRecord,
	action, publicationSource string,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,$3,'DATASET',$4,jsonb_build_object(
			'publicationSource',$5::text,'originTableId',$6::text,
			'publishedVersionId',$7::text,'versionNo',$8::int,
			'dslHash',$9::text,'planHash',$10::text
		))`, tenantID, actorID, action, version.DatasetID, publicationSource,
			tableID, version.ID, version.VersionNo, version.DSLHash, version.PlanHash)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
