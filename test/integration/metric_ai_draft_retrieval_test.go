//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metricai"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestMetricAIDraftDatasetRetrievalRequiresReadAndManage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metric-ai-draft-it-"+suffix)
	// Dataset draft revisions are immutable audit records. The disposable integration database
	// owns cleanup for this isolated tenant.
	actorID, readerID, sourceID, tableID := prepareMetricAIDraftActorsAndTable(t, ctx, pool, tenantID, suffix)
	dsl := metricDatasetDefinition(t, sourceID, tableID)
	record, err := dataset.NewService(dataset.NewPostgresStore(pool)).Create(ctx, tenantID, actorID, dataset.CreateInput{
		Code: "metric_orders", Name: "指标明细数据集", Description: "指标持久化集成测试",
		Type: "SINGLE_SOURCE", DSL: dsl,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantMetricAIDraftDatasetAccess(t, ctx, pool, tenantID, actorID, readerID, record.ID)

	retriever := metricai.NewPostgresRetriever(pool)
	requirement := metricai.AuthoringRequest{Requirement: "基于订单表和客户表，创建一个月度各区域销售额的指标"}
	modifiable, err := retriever.Retrieve(ctx, tenantID, actorID, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if len(modifiable.Datasets) != 0 {
		t.Fatalf("published datasets=%#v, want none for a draft-only tenant", modifiable.Datasets)
	}
	if len(modifiable.ModifiableDraftDatasets) != 1 {
		t.Fatalf("modifiable draft datasets=%#v, want one", modifiable.ModifiableDraftDatasets)
	}
	draft := modifiable.ModifiableDraftDatasets[0]
	if draft.ID != record.ID || draft.VersionID != record.DraftVersionID || draft.Status != "DRAFT" || !draft.Manageable {
		t.Fatalf("modifiable draft dataset=%#v, want exact manageable DRAFT snapshot", draft)
	}
	if len(modifiable.ModifiableDraftFields) != 2 {
		t.Fatalf("modifiable draft fields=%#v, want two visible fields", modifiable.ModifiableDraftFields)
	}
	for _, field := range modifiable.ModifiableDraftFields {
		if field.DatasetID != record.ID || field.DatasetVersionID != record.DraftVersionID {
			t.Fatalf("modifiable draft field=%#v, want exact draft dataset/version", field)
		}
	}

	readOnly, err := retriever.Retrieve(ctx, tenantID, readerID, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if len(readOnly.Datasets) != 0 || len(readOnly.ModifiableDraftDatasets) != 0 || len(readOnly.ModifiableDraftFields) != 0 {
		t.Fatalf("read-only actor received draft authoring context: %#v", readOnly)
	}
}

func prepareMetricAIDraftActorsAndTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, suffix string) (actorID, readerID, sourceID, tableID string) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'metric AI draft editor','integration-hash') RETURNING id::text`,
			tenantID, "metric-ai-draft-editor-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'metric AI draft reader','integration-hash') RETURNING id::text`,
			tenantID, "metric-ai-draft-reader-"+suffix+"@it.test").Scan(&readerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			tenant_id,code,name,source_type,secret_ref,status,last_synced_at
		) VALUES($1,'metric_ai_draft_orders','Metric AI Draft Orders','MYSQL','env://METRIC_AI_DRAFT_IT','ACTIVE',now()) RETURNING id::text`, tenantID).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at
		) VALUES($1,$2,'sales','orders','TABLE',repeat('7',64),now()) RETURNING id::text`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		columns := []struct{ name, nativeType, canonicalType string }{
			{name: "order_date", nativeType: "date", canonicalType: "DATE"},
			{name: "order_amount", nativeType: "decimal(18,2)", canonicalType: "DECIMAL"},
		}
		for index, column := range columns {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
				tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at
			) VALUES($1,$2,$3,$4,$5,$6,false,repeat($7,64),now())`, tenantID, tableID,
				column.name, index+1, column.nativeType, column.canonicalType, fmt.Sprintf("%d", index+8)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return actorID, readerID, sourceID, tableID
}

func grantMetricAIDraftDatasetAccess(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, readerID, datasetID string) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES
			($1,'USER',$2,'DATASET',$4,'READ',$2),
			($1,'USER',$2,'DATASET',$4,'MANAGE',$2),
			($1,'USER',$3,'DATASET',$4,'READ',$2)`, tenantID, actorID, readerID, datasetID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
