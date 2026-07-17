//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/platform/database"
)

type metadataJobConnector struct {
	table       datasource.MetadataTable
	sampleCalls int
}

func (c *metadataJobConnector) Type() datasource.Type { return datasource.TypeMySQL }
func (c *metadataJobConnector) Test(context.Context, datasource.Source) (datasource.TestResult, error) {
	return datasource.TestResult{}, nil
}
func (c *metadataJobConnector) Sync(context.Context, datasource.Source) (datasource.SyncResult, error) {
	return datasource.SyncResult{Tables: []datasource.MetadataTable{c.table}}, nil
}
func (c *metadataJobConnector) Sample(context.Context, datasource.Source, datasource.MetadataTable, int) (datasource.SampleResult, error) {
	c.sampleCalls++
	return datasource.SampleResult{Columns: []string{"order_id"}, Rows: [][]any{{1}, {2}, {3}}}, nil
}
func (c *metadataJobConnector) Close(context.Context, datasource.Source) error { return nil }

type metadataJobAIProvider struct {
	calls        int
	beforeReturn func() error
}

func (*metadataJobAIProvider) Name() string     { return "integration" }
func (*metadataJobAIProvider) Model() string    { return "metadata-job-test-v1" }
func (*metadataJobAIProvider) Configured() bool { return true }
func (p *metadataJobAIProvider) Complete(_ context.Context, _, _ string, input metadataai.CompletionInput) (metadataai.ProviderResult, error) {
	p.calls++
	if p.beforeReturn != nil {
		if err := p.beforeReturn(); err != nil {
			return metadataai.ProviderResult{}, err
		}
	}
	columns := make([]metadataai.SuggestionValue, 0, len(input.Columns))
	for _, column := range input.Columns {
		columns = append(columns, metadataai.SuggestionValue{
			TargetID: column.ID, BusinessName: column.Name, BusinessDescription: "集成测试字段",
			Tags: []string{"作用:辅助信息"}, SensitivityLevel: "INTERNAL", SemanticType: "IDENTIFIER", Confidence: 0.95,
		})
	}
	return metadataai.ProviderResult{Output: metadataai.CompletionOutput{
		SchemaVersion: metadataai.SchemaVersion,
		Table: metadataai.SuggestionValue{TargetID: input.Table.ID, BusinessName: input.Table.Name, BusinessDescription: "集成测试表",
			Tags: []string{"作用:事实表"}, SensitivityLevel: "INTERNAL", Confidence: 0.95},
		Columns: columns,
	}, Model: p.Model()}, nil
}

func TestMetadataJobPersistsProgressAndSkipsUnchangedTables(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metadata-job-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "metadata-job-foreign-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantID); cleanupTenant(pool, foreignTenantID) })

	var actorID, sourceID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'metadata job','integration-hash') RETURNING id`, tenantID, "metadata-job-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,status,config,secret_ref)
			VALUES($1,$2,'Metadata Job','MYSQL','ACTIVE','{"host":"db.internal","port":3306,"database":"sales","username":"reader"}','encrypted://metadata-job') RETURNING id`, tenantID, "metadata-job-"+suffix).Scan(&sourceID)
	})
	if err != nil {
		t.Fatal(err)
	}

	connector := &metadataJobConnector{table: datasource.MetadataTable{
		SchemaName: "sales", Name: "orders", Type: "TABLE",
		Columns: []datasource.MetadataColumn{{Name: "order_id", OrdinalPosition: 1, NativeType: "bigint", CanonicalType: "INTEGER", Nullable: false}},
	}}
	provider := &metadataJobAIProvider{}
	completer := metadataai.NewService(metadataai.NewPostgresStore(pool), provider, 5*time.Second, 0.8)
	service := datasource.NewService(datasource.NewPostgresRepository(pool), connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(datasource.NewPostgresMetadataJobRepository(pool))

	job, err := service.QueueImportTables(ctx, tenantID, actorID, sourceID, []datasource.TableSelection{{SchemaName: "sales", TableName: "orders"}})
	if err != nil || job.Status != "QUEUED" || job.Total != 1 {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	if _, err := service.QueueImportTables(ctx, tenantID, actorID, sourceID, []datasource.TableSelection{{SchemaName: "sales", TableName: "orders"}}); !errors.Is(err, datasource.ErrMetadataJobActive) {
		t.Fatalf("second active job err=%v", err)
	}
	processed, err := service.ProcessNextMetadataJob(ctx, tenantID, "integration-worker", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	completed, err := service.GetMetadataJob(ctx, tenantID, sourceID, job.ID)
	if err != nil || completed.Status != "SUCCEEDED" || completed.Completed != 1 || completed.Succeeded != 1 {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	if connector.sampleCalls != 1 || provider.calls != 1 {
		t.Fatalf("sample=%d complete=%d", connector.sampleCalls, provider.calls)
	}
	if _, err := service.GetMetadataJob(ctx, foreignTenantID, sourceID, job.ID); !errors.Is(err, datasource.ErrMetadataJobNotFound) {
		t.Fatalf("cross-tenant job query err=%v", err)
	}

	var tableID, structureHash string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text,structure_hash FROM platform.metadata_tables WHERE data_source_id=$1 AND table_name='orders'`, sourceID).Scan(&tableID, &structureHash); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var linkedCompletionCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.ai_metadata_jobs
			WHERE table_id=$1 AND status='SUCCEEDED' AND data_source_metadata_job_item_id IS NOT NULL
			AND metadata_structure_hash=$2`, tableID, structureHash).Scan(&linkedCompletionCount)
	})
	if err != nil || linkedCompletionCount != 1 {
		t.Fatalf("linked AI completion count=%d err=%v", linkedCompletionCount, err)
	}
	recovery, err := service.QueueRefreshTables(ctx, tenantID, actorID, sourceID, datasource.MetadataRefreshFull)
	if err != nil || recovery.Total != 1 {
		t.Fatalf("recovery job=%#v err=%v", recovery, err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var itemID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.data_source_metadata_job_items WHERE job_id=$1`, recovery.ID).Scan(&itemID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.ai_metadata_jobs(
			tenant_id,table_id,metadata_structure_hash,data_source_metadata_job_item_id,provider,model_name,prompt_version,input_hash,status,created_by,completed_at)
			VALUES($1,$2,$3,$4,'integration','metadata-test','integration-recovery',$5,'SUCCEEDED',$6,now())`,
			tenantID, tableID, structureHash, itemID, fmt.Sprintf("%064x", 2), actorID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, err = service.ProcessNextMetadataJob(ctx, tenantID, "integration-worker", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("recovery processed=%v err=%v", processed, err)
	}
	recovery, err = service.GetMetadataJob(ctx, tenantID, sourceID, recovery.ID)
	if err != nil || recovery.Status != "SUCCEEDED" || recovery.Succeeded != 1 || connector.sampleCalls != 1 || provider.calls != 1 {
		t.Fatalf("recovery=%#v sample=%d complete=%d err=%v", recovery, connector.sampleCalls, provider.calls, err)
	}

	refresh, err := service.QueueRefreshTables(ctx, tenantID, actorID, sourceID, datasource.MetadataRefreshIncremental)
	if err != nil || refresh.Total != 1 {
		t.Fatalf("refresh=%#v err=%v", refresh, err)
	}
	processed, err = service.ProcessNextMetadataJob(ctx, tenantID, "integration-worker", 2*time.Minute)
	if err != nil || !processed {
		t.Fatalf("refresh processed=%v err=%v", processed, err)
	}
	refresh, err = service.GetMetadataJob(ctx, tenantID, sourceID, refresh.ID)
	if err != nil || refresh.Status != "SUCCEEDED" || refresh.Skipped != 1 || refresh.Succeeded != 0 {
		t.Fatalf("refresh=%#v err=%v", refresh, err)
	}
	if connector.sampleCalls != 1 || provider.calls != 1 {
		t.Fatalf("unchanged table was reprocessed: sample=%d complete=%d", connector.sampleCalls, provider.calls)
	}

	lateJob, err := service.QueueRefreshTables(ctx, tenantID, actorID, sourceID, datasource.MetadataRefreshFull)
	if err != nil {
		t.Fatal(err)
	}
	var lateItemID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.data_source_metadata_job_items WHERE job_id=$1`, lateJob.ID).Scan(&lateItemID)
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.beforeReturn = func() error {
		return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE platform.data_source_metadata_jobs
				SET lease_owner='takeover-worker',lease_expires_at=now()+interval '2 minutes' WHERE id=$1`, lateJob.ID)
			return err
		})
	}
	processed, err = service.ProcessNextMetadataJob(ctx, tenantID, "expired-worker", 2*time.Minute)
	if !processed || err == nil {
		t.Fatalf("late worker processed=%v err=%v", processed, err)
	}
	var lateAIStatus, lateAIError string
	var lateSuggestionCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status,error_code FROM platform.ai_metadata_jobs
			WHERE data_source_metadata_job_item_id=$1 ORDER BY created_at DESC LIMIT 1`, lateItemID).Scan(&lateAIStatus, &lateAIError); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.ai_metadata_suggestions s
			JOIN platform.ai_metadata_jobs j ON j.id=s.job_id WHERE j.data_source_metadata_job_item_id=$1`, lateItemID).Scan(&lateSuggestionCount)
	})
	if err != nil || lateAIStatus != "FAILED" || lateAIError != "PROCESSING_LEASE_LOST" || lateSuggestionCount != 0 {
		t.Fatalf("late AI status=%s code=%s suggestions=%d err=%v", lateAIStatus, lateAIError, lateSuggestionCount, err)
	}
}

func TestManagedMetadataRefreshDoesNotReactivateDeletedAsset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "managed-refresh-it-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantID) })

	var sourceID string
	var sourceVersion int64
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,status,config,secret_ref)
			VALUES($1,$2,'Managed Refresh','MYSQL','ACTIVE','{"host":"db.internal","port":3306,"database":"sales","username":"reader"}','encrypted://managed-refresh') RETURNING id,version`, tenantID, "managed-refresh-"+suffix).Scan(&sourceID, &sourceVersion)
	})
	if err != nil {
		t.Fatal(err)
	}
	source := datasource.Source{ID: sourceID, TenantID: tenantID, Type: datasource.TypeMySQL, Status: datasource.StatusActive, Version: sourceVersion}
	repository := datasource.NewPostgresRepository(pool)
	initialTable := datasource.MetadataTable{
		SchemaName: "sales", Name: "orders", Type: "TABLE", SourceComment: "initial",
		Columns: []datasource.MetadataColumn{{Name: "order_id", OrdinalPosition: 1, NativeType: "bigint", CanonicalType: "INTEGER"}},
	}
	initialResult := datasource.SyncResult{
		Watermark: time.Now().UTC().Format(time.RFC3339Nano), SnapshotHash: fmt.Sprintf("%064x", 1), Tables: []datasource.MetadataTable{initialTable},
	}
	ids, err := repository.ApplySelectedMetadata(ctx, source, initialResult)
	if err != nil || len(ids) != 1 {
		t.Fatalf("initial import ids=%#v err=%v", ids, err)
	}
	var tableID string
	for _, id := range ids {
		tableID = id
	}
	refreshedTable := initialTable
	refreshedTable.SourceComment = "refreshed"
	refreshResult := datasource.SyncResult{
		Watermark: time.Now().UTC().Format(time.RFC3339Nano), SnapshotHash: fmt.Sprintf("%064x", 2), Tables: []datasource.MetadataTable{refreshedTable},
	}

	var initialStructureHash string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT structure_hash FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&initialStructureHash)
	})
	if err != nil {
		t.Fatal(err)
	}
	var currentSourceVersion int64
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE platform.data_sources SET version=version+1 WHERE id=$1 RETURNING version`, sourceID).Scan(&currentSourceVersion)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult); !errors.Is(err, datasource.ErrMetadataSourceChanged) {
		t.Fatalf("stale source version refresh err=%v", err)
	}
	source.Version = currentSourceVersion

	// 入队基线 A 之后若其他同步已推进到 C，旧任务发现的 B 不得覆盖 C。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET structure_hash=repeat('c',64),source_comment='superseding' WHERE id=$1`, tableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult); !errors.Is(err, datasource.ErrMetadataRefreshSuperseded) {
		t.Fatalf("superseded refresh err=%v", err)
	}
	var supersedingHash, supersedingComment string
	var supersedingSnapshotCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT structure_hash,source_comment FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&supersedingHash, &supersedingComment); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metadata_snapshots WHERE data_source_id=$1`, sourceID).Scan(&supersedingSnapshotCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if supersedingHash != strings.Repeat("c", 64) || supersedingComment != "superseding" || supersedingSnapshotCount != 1 {
		t.Fatalf("superseding structure changed: hash=%q comment=%q snapshots=%d", supersedingHash, supersedingComment, supersedingSnapshotCount)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET structure_hash=$2,source_comment='initial',asset_status='INACTIVE',management_status='DISABLED' WHERE id=$1`, tableID, initialStructureHash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE platform.metadata_columns SET asset_status='INACTIVE' WHERE table_id=$1`, tableID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	refreshedID, managed, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult)
	if err != nil || managed || refreshedID != "" {
		t.Fatalf("guarded refresh id=%q managed=%v err=%v", refreshedID, managed, err)
	}

	var status, managementStatus, structureHash, sourceComment string
	var snapshotCount int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT asset_status::text,management_status,structure_hash,source_comment FROM platform.metadata_tables WHERE id=$1`, tableID).
			Scan(&status, &managementStatus, &structureHash, &sourceComment); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metadata_snapshots WHERE data_source_id=$1`, sourceID).Scan(&snapshotCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "INACTIVE" || managementStatus != "DISABLED" || structureHash != initialStructureHash || sourceComment != "initial" || snapshotCount != 1 {
		t.Fatalf("deleted asset changed: status=%s management=%s hash=%s comment=%s snapshots=%d", status, managementStatus, structureHash, sourceComment, snapshotCount)
	}

	// 显式 IMPORT 保留重新导入语义，只有后台 REFRESH 受原子纳管校验限制。
	ids, err = repository.ApplySelectedMetadata(ctx, source, refreshResult)
	if err != nil || len(ids) != 1 {
		t.Fatalf("reimport ids=%#v err=%v", ids, err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT asset_status::text,management_status,structure_hash,source_comment FROM platform.metadata_tables WHERE id=$1`, tableID).
			Scan(&status, &managementStatus, &structureHash, &sourceComment)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" || managementStatus != "ENABLED" || structureHash == initialStructureHash || sourceComment != "refreshed" {
		t.Fatalf("reimport did not reactivate asset: status=%s management=%s hash=%s comment=%s", status, managementStatus, structureHash, sourceComment)
	}
}
