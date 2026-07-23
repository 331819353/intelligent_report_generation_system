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
	"intelligent-report-generation-system/internal/platform/database"
)

func TestMappedDatasetIsCreatedWithIndependentPublishedV1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	var actorID, sourceID, tableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'mapped dataset owner','integration-hash') RETURNING id::text`,
			tenantID, "mapped-default-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			tenant_id,code,name,source_type,status,config,secret_ref
		) VALUES($1,$2,'Mapped Source','MYSQL','ACTIVE',
			'{"host":"db.internal","port":3306,"database":"sales","username":"reader"}',
			'encrypted://mapped-default') RETURNING id::text`, tenantID, "mapped_source_"+suffix).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,table_structure_hash,
			last_enriched_structure_hash,last_enriched_table_structure_hash,business_name,business_description,last_sync_at
		) VALUES($1,$2,'sales','orders','TABLE',repeat('a',64),repeat('b',64),repeat('a',64),repeat('b',64),
			'订单事实表','完整映射后的订单明细',now()) RETURNING id::text`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,
			last_enriched_structure_hash,business_name,business_description,semantic_type,is_primary_key,last_sync_at
		) VALUES($1,$2,'order_id',1,'bigint','INTEGER',false,repeat('c',64),repeat('c',64),
			'订单编号','订单业务主键','IDENTIFIER',true,now())`, tenantID, tableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	store := dataset.NewPostgresStore(pool)
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}

	var datasetID, status, draftID, draftStatus, publishedID, publishedStatus, sourceDraftID string
	var ownerVersion, draftRecordVersion, sourceDraftRecordVersion int64
	var draftVersionNo, publishedVersionNo int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT dataset.id::text,dataset.status,dataset.version,
			draft.id::text,draft.status,draft.version_no,draft.record_version,
			published.id::text,published.status,published.version_no,
			published.source_draft_version_id::text,published.source_draft_record_version
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft ON draft.id=dataset.current_draft_version_id
			JOIN platform.dataset_versions AS published ON published.id=dataset.current_published_version_id
			WHERE dataset.origin_table_id=$1`, tableID).Scan(
			&datasetID, &status, &ownerVersion,
			&draftID, &draftStatus, &draftVersionNo, &draftRecordVersion,
			&publishedID, &publishedStatus, &publishedVersionNo,
			&sourceDraftID, &sourceDraftRecordVersion,
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "PUBLISHED" || ownerVersion != 2 || draftStatus != "DRAFT" || draftVersionNo != 2 || draftRecordVersion != 1 ||
		publishedStatus != "PUBLISHED" || publishedVersionNo != 1 || publishedID == draftID ||
		sourceDraftID != draftID || sourceDraftRecordVersion != draftRecordVersion {
		t.Fatalf("invalid mapped publication: dataset=%s status=%s ownerV=%d draft=%s/%s/V%d/R%d published=%s/%s/V%d source=%s/R%d",
			datasetID, status, ownerVersion, draftID, draftStatus, draftVersionNo, draftRecordVersion,
			publishedID, publishedStatus, publishedVersionNo, sourceDraftID, sourceDraftRecordVersion)
	}
	versions, total, err := store.ListVersions(ctx, tenantID, datasetID, 10, 0)
	if err != nil || total != 1 || len(versions) != 1 || versions[0].ID != publishedID || versions[0].Status != "PUBLISHED" {
		t.Fatalf("published history=%#v total=%d err=%v", versions, total, err)
	}

	// API 与 worker 可并发执行启动对账；再次 Ensure 必须保持同一个发布 V1。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return store.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var publishedCount, autoPublishAuditCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_versions
			WHERE dataset_id=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')`, datasetID).Scan(&publishedCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='DATASET' AND resource_id=$1 AND action='AUTO_PUBLISH_MAPPED_DEFAULT'`, datasetID).Scan(&autoPublishAuditCount)
	})
	if err != nil || publishedCount != 1 || autoPublishAuditCount != 1 {
		t.Fatalf("idempotency published=%d audit=%d err=%v", publishedCount, autoPublishAuditCount, err)
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
	var regeneratedVersion int64
	var regeneratedPublishedNo, regenerateAuditCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT dataset.id::text,dataset.status,dataset.version,
			published.id::text,published.version_no,old_published.status
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS published ON published.id=dataset.current_published_version_id
			JOIN platform.dataset_versions AS old_published ON old_published.id=$2
			WHERE dataset.origin_table_id=$1 AND dataset.deleted_at IS NULL`, tableID, publishedID).Scan(
			&regeneratedID, &regeneratedStatus, &regeneratedVersion,
			&regeneratedPublishedID, &regeneratedPublishedNo, &oldPublishedStatus,
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
		t.Fatalf("regenerated dataset=%s status=%s ownerV=%d published=%s/V%d oldStatus=%s audit=%d",
			regeneratedID, regeneratedStatus, regeneratedVersion, regeneratedPublishedID,
			regeneratedPublishedNo, oldPublishedStatus, regenerateAuditCount)
	}

	var secondSourceID, secondTableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			tenant_id,code,name,source_type,status,config,secret_ref
		) VALUES($1,$2,'Mapped Source Copy','MYSQL','ACTIVE',
			'{"host":"db-copy.internal","port":3306,"database":"sales","username":"reader"}',
			'encrypted://mapped-default-copy') RETURNING id::text`, tenantID, "mapped_source_copy_"+suffix).Scan(&secondSourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,table_structure_hash,
			last_enriched_structure_hash,last_enriched_table_structure_hash,business_name,business_description,last_sync_at
		) VALUES($1,$2,'sales','orders_copy','TABLE',repeat('d',64),repeat('e',64),repeat('d',64),repeat('e',64),
			'订单事实表','另一个数据源中的同名订单表',now()) RETURNING id::text`, tenantID, secondSourceID).Scan(&secondTableID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,
			last_enriched_structure_hash,business_name,business_description,semantic_type,is_primary_key,last_sync_at
		) VALUES($1,$2,'order_id',1,'bigint','INTEGER',false,repeat('f',64),repeat('f',64),
			'订单编号','订单业务主键','IDENTIFIER',true,now())`, tenantID, secondTableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
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
}
