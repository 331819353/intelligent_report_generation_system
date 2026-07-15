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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

func TestDatasetDraftPersistenceAndTenantIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "dataset-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "dataset-foreign-"+suffix)
	t.Cleanup(func() {
		cleanupDatasetTenant(pool, tenantID)
		cleanupTenant(pool, tenantID)
		cleanupTenant(pool, foreignTenantID)
	})

	var actorID, sourceID, tableID, customerSourceID, customerTableID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'dataset tester','integration-hash') RETURNING id`, tenantID, "dataset-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,secret_ref,status,last_synced_at) VALUES($1,'orders','Orders','MYSQL','env://DATASET_IT','ACTIVE',now()) RETURNING id`, tenantID).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'sales','orders','TABLE','dataset-table-hash',now()) RETURNING id`, tenantID, sourceID).Scan(&tableID); err != nil {
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
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'crm','customers','TABLE','customer-table-hash',now()) RETURNING id`, tenantID, customerSourceID).Scan(&customerTableID); err != nil {
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
	service := dataset.NewService(dataset.NewPostgresStore(pool))
	created, err := service.Create(ctx, tenantID, actorID, dataset.CreateInput{Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额", Type: "SINGLE_SOURCE", DSL: raw})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.DSLHash == "" || created.PlanHash == "" {
		t.Fatalf("created=%#v", created)
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
	queryID := "b7e77e2c-8db1-4396-89c8-9c682f1aa031"
	orderNode, customerNode := crossResolved.Nodes["orders"], crossResolved.Nodes["customers"]
	if err := runtimeStore.Start(ctx, queryruntime.RunRecord{
		ID: queryID, TenantID: tenantID, DatasetID: created.ID, DatasetVersionID: updated.DraftVersionID,
		ActorID: actorID, SourceID: sourceID, PlanHash: strings.Repeat("a", 64), ParameterHash: strings.Repeat("b", 64),
		Sources: []queryruntime.RunSourceRecord{
			{NodeID: orderNode.NodeID, SourceID: orderNode.SourceID, SourceType: orderNode.SourceType, SubqueryID: "856024ca-6f40-44e1-b939-070844530d9f", SourceVersion: orderNode.SourceVersion, Watermark: orderNode.Watermark},
			{NodeID: customerNode.NodeID, SourceID: customerNode.SourceID, SourceType: customerNode.SourceType, SubqueryID: "2902414a-bd3e-467c-8288-98ec62fbe9c6", SourceVersion: customerNode.SourceVersion, Watermark: customerNode.Watermark},
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
		{NodeID: "orders", SubqueryID: "856024ca-6f40-44e1-b939-070844530d9f", RowCount: 3, DurationMS: 7, Status: "SUCCEEDED"},
		{NodeID: "customers", SubqueryID: "2902414a-bd3e-467c-8288-98ec62fbe9c6", RowCount: 2, DurationMS: 4, Status: "SUCCEEDED"},
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
	failedQueryID := "cad45904-5618-4f1b-b73d-61947446dbef"
	failedOrderSubqueryID := "2a6a6516-cc39-43cd-9ad7-b72a987565c3"
	if err := runtimeStore.Start(ctx, queryruntime.RunRecord{
		ID: failedQueryID, TenantID: tenantID, DatasetID: created.ID, DatasetVersionID: updated.DraftVersionID,
		ActorID: actorID, SourceID: sourceID, PlanHash: strings.Repeat("c", 64), ParameterHash: strings.Repeat("d", 64),
		Sources: []queryruntime.RunSourceRecord{
			{NodeID: orderNode.NodeID, SourceID: orderNode.SourceID, SourceType: orderNode.SourceType, SubqueryID: failedOrderSubqueryID, SourceVersion: orderNode.SourceVersion, Watermark: orderNode.Watermark},
			{NodeID: customerNode.NodeID, SourceID: customerNode.SourceID, SourceType: customerNode.SourceType, SubqueryID: "105801dc-7b88-435b-8bbc-9154ef3fe4d4", SourceVersion: customerNode.SourceVersion, Watermark: customerNode.Watermark},
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
