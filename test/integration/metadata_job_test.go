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
	missing     bool
	sample      datasource.SampleResult
	sampleCalls int
}

func (c *metadataJobConnector) Type() datasource.Type { return datasource.TypeMySQL }
func (c *metadataJobConnector) Test(context.Context, datasource.Source) (datasource.TestResult, error) {
	return datasource.TestResult{}, nil
}
func (c *metadataJobConnector) Sync(context.Context, datasource.Source) (datasource.SyncResult, error) {
	tables := []datasource.MetadataTable{c.table}
	if c.missing {
		tables = []datasource.MetadataTable{}
	}
	return datasource.SyncResult{
		Assets: len(tables), Watermark: time.Now().UTC().Format(time.RFC3339Nano),
		SnapshotHash: strings.Repeat("a", 64), Tables: tables,
	}, nil
}
func (c *metadataJobConnector) Sample(context.Context, datasource.Source, datasource.MetadataTable, int) (datasource.SampleResult, error) {
	c.sampleCalls++
	if c.sample.Columns != nil {
		return c.sample, nil
	}
	return datasource.SampleResult{Columns: []string{"order_id"}, Rows: [][]any{{1}, {2}, {3}}}, nil
}
func (c *metadataJobConnector) Close(context.Context, datasource.Source) error { return nil }

type metadataJobAIProvider struct {
	calls        int
	inputs       []metadataai.CompletionInput
	beforeReturn func() error
}

func (*metadataJobAIProvider) Name() string     { return "integration" }
func (*metadataJobAIProvider) Model() string    { return "metadata-job-test-v1" }
func (*metadataJobAIProvider) Configured() bool { return true }
func (p *metadataJobAIProvider) Complete(_ context.Context, _, _ string, input metadataai.CompletionInput) (metadataai.ProviderResult, error) {
	p.calls++
	p.inputs = append(p.inputs, input)
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
	var tableSuggestion *metadataai.SuggestionValue
	if input.TargetTable {
		tableSuggestion = &metadataai.SuggestionValue{TargetID: input.Table.ID, BusinessName: input.Table.Name, BusinessDescription: "集成测试表",
			Tags: []string{"作用:事实表"}, SensitivityLevel: "INTERNAL", Confidence: 0.95}
	}
	return metadataai.ProviderResult{Output: metadataai.CompletionOutput{
		SchemaVersion: metadataai.SchemaVersion,
		Table:         tableSuggestion,
		Columns:       columns,
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
		var err error
		sourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "metadata-job-"+suffix, "Metadata Job", "MYSQL",
			"encrypted://metadata-job", "ACTIVE",
			`{"host":"db.internal","port":3306,"database":"sales","username":"reader"}`,
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	attestAndPublishDataSourceFixture(t, ctx, pool, tenantID, actorID, sourceID)

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
	if completed.SampleDataMode != datasource.MetadataSampleDeny || completed.SamplePolicyVersion < 1 {
		t.Fatalf("default sample authorization was not frozen closed: %#v", completed)
	}
	if connector.sampleCalls != 0 || provider.calls != 1 {
		t.Fatalf("sample=%d complete=%d", connector.sampleCalls, provider.calls)
	}
	if _, err := service.QueueImportTablesWithSampleMode(
		ctx, tenantID, actorID, sourceID, datasource.MetadataSampleRaw,
		[]datasource.TableSelection{{SchemaName: "sales", TableName: "orders"}},
	); !errors.Is(err, datasource.ErrSamplePolicyDenied) {
		t.Fatalf("RAW task exceeded default tenant policy: %v", err)
	}
	immutabilityErr := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, updateErr := tx.Exec(ctx, `UPDATE platform.data_source_metadata_jobs
			SET sample_data_mode='RAW',sample_consent_by=$2,sample_consent_at=now()
			WHERE id=$1`, job.ID, actorID)
		return updateErr
	})
	if immutabilityErr == nil {
		t.Fatal("frozen metadata sample authorization was mutable")
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
	if err != nil || recovery.Status != "SUCCEEDED" || recovery.Succeeded != 1 || connector.sampleCalls != 0 || provider.calls != 1 {
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
	if connector.sampleCalls != 0 || provider.calls != 1 {
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

func TestIncrementalMetadataRefreshOnlyCompletesChangedFieldsAndHandlesRemoval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metadata-field-delta-it-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantID) })

	var actorID, sourceID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'metadata field delta','integration-hash') RETURNING id`, tenantID, "metadata-field-delta-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		var err error
		sourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "metadata-field-delta-"+suffix, "Metadata Field Delta", "MYSQL",
			"encrypted://metadata-field-delta", "ACTIVE",
			`{"host":"db.internal","port":3306,"database":"sales","username":"reader"}`,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE platform.ai_tenant_policies
			SET metadata_sample_mode='RAW' WHERE tenant_id=$1`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	attestAndPublishDataSourceFixture(t, ctx, pool, tenantID, actorID, sourceID)

	connector := &metadataJobConnector{table: datasource.MetadataTable{
		SchemaName: "sales", Name: "orders", Type: "TABLE",
		Columns: []datasource.MetadataColumn{{Name: "order_id", OrdinalPosition: 1, NativeType: "bigint", CanonicalType: "INTEGER", Nullable: false}},
	}}
	provider := &metadataJobAIProvider{}
	service := datasource.NewService(datasource.NewPostgresRepository(pool), connector)
	service.SetTableCompleter(metadataai.NewService(metadataai.NewPostgresStore(pool), provider, 5*time.Second, 0.8))
	service.SetMetadataJobRepository(datasource.NewPostgresMetadataJobRepository(pool))

	job, err := service.QueueImportTablesWithSampleMode(
		ctx, tenantID, actorID, sourceID, datasource.MetadataSampleRaw,
		[]datasource.TableSelection{{SchemaName: "sales", TableName: "orders"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := service.ProcessNextMetadataJob(ctx, tenantID, "field-delta-worker", 2*time.Minute); err != nil || !processed {
		t.Fatalf("initial import processed=%v err=%v", processed, err)
	}
	job, err = service.GetMetadataJob(ctx, tenantID, sourceID, job.ID)
	if err != nil || job.Status != "SUCCEEDED" {
		t.Fatalf("initial import=%#v err=%v", job, err)
	}

	var tableID, orderColumnID string
	var orderBusinessName, orderDescription string
	var orderBusinessVersion int64
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.metadata_tables WHERE data_source_id=$1 AND table_name='orders'`, sourceID).Scan(&tableID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,business_name,business_description,business_version
			FROM platform.metadata_columns WHERE table_id=$1 AND column_name='order_id'`, tableID).
			Scan(&orderColumnID, &orderBusinessName, &orderDescription, &orderBusinessVersion)
	})
	if err != nil {
		t.Fatal(err)
	}

	length := int64(255)
	connector.table.Columns = append(connector.table.Columns, datasource.MetadataColumn{
		Name: "email", OrdinalPosition: 2, NativeType: "varchar(255)", CanonicalType: "STRING", Length: &length, Nullable: true,
	})
	connector.sample = datasource.SampleResult{
		Columns: []string{"order_id", "email"},
		Rows:    [][]any{{1, "first@example.com"}, {2, "second@example.com"}},
	}
	refresh, err := service.QueueRefreshTablesWithSampleMode(
		ctx, tenantID, actorID, sourceID,
		datasource.MetadataRefreshIncremental, datasource.MetadataSampleRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := service.ProcessNextMetadataJob(ctx, tenantID, "field-delta-worker", 2*time.Minute); err != nil || !processed {
		t.Fatalf("field delta processed=%v err=%v", processed, err)
	}
	refresh, err = service.GetMetadataJob(ctx, tenantID, sourceID, refresh.ID)
	if err != nil || refresh.Status != "SUCCEEDED" || refresh.Succeeded != 1 {
		t.Fatalf("field delta refresh=%#v err=%v", refresh, err)
	}
	if provider.calls != 2 || connector.sampleCalls != 2 || len(provider.inputs) != 2 {
		t.Fatalf("calls provider=%d sample=%d inputs=%d", provider.calls, connector.sampleCalls, len(provider.inputs))
	}
	deltaInput := provider.inputs[1]
	if deltaInput.TargetTable || len(deltaInput.Columns) != 1 || deltaInput.Columns[0].Name != "email" {
		t.Fatalf("incremental target scope=%#v", deltaInput)
	}
	if deltaInput.Columns[0].Length == nil || *deltaInput.Columns[0].Length != 255 || deltaInput.Columns[0].OrdinalPosition != 2 || !deltaInput.Columns[0].Nullable {
		t.Fatalf("changed field technical context=%#v", deltaInput.Columns[0])
	}
	for _, row := range deltaInput.SampleRows {
		if len(row) != 1 || row["email"] == nil {
			t.Fatalf("incremental sample leaked unchanged fields: %#v", row)
		}
		if _, exists := row["order_id"]; exists {
			t.Fatalf("unchanged order_id leaked into sample: %#v", row)
		}
	}

	var emailColumnID, emailDescription, emailStructureHash, emailEnrichedHash string
	var currentOrderName, currentOrderDescription string
	var currentOrderVersion int64
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT business_name,business_description,business_version FROM platform.metadata_columns WHERE id=$1`, orderColumnID).
			Scan(&currentOrderName, &currentOrderDescription, &currentOrderVersion); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,business_description,structure_hash,last_enriched_structure_hash
			FROM platform.metadata_columns WHERE table_id=$1 AND column_name='email'`, tableID).
			Scan(&emailColumnID, &emailDescription, &emailStructureHash, &emailEnrichedHash)
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentOrderName != orderBusinessName || currentOrderDescription != orderDescription || currentOrderVersion != orderBusinessVersion {
		t.Fatalf("unchanged field was overwritten: before=%q/%q/%d after=%q/%q/%d", orderBusinessName, orderDescription, orderBusinessVersion, currentOrderName, currentOrderDescription, currentOrderVersion)
	}
	if emailColumnID == "" || emailDescription != "集成测试字段" || emailEnrichedHash == "" || emailEnrichedHash != emailStructureHash {
		t.Fatalf("changed field was not completed: id=%q description=%q current=%q enriched=%q", emailColumnID, emailDescription, emailStructureHash, emailEnrichedHash)
	}

	// 字段从源表消失时只软停用该字段，不再采样，也不调用 LLM。
	connector.table.Columns = connector.table.Columns[:1]
	sampleCallsBeforeRemoval, providerCallsBeforeRemoval := connector.sampleCalls, provider.calls
	removal, err := service.QueueRefreshTablesWithSampleMode(
		ctx, tenantID, actorID, sourceID,
		datasource.MetadataRefreshIncremental, datasource.MetadataSampleRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := service.ProcessNextMetadataJob(ctx, tenantID, "field-delta-worker", 2*time.Minute); err != nil || !processed {
		t.Fatalf("field removal processed=%v err=%v", processed, err)
	}
	removal, err = service.GetMetadataJob(ctx, tenantID, sourceID, removal.ID)
	if err != nil || removal.Status != "SUCCEEDED" || removal.Succeeded != 1 || connector.sampleCalls != sampleCallsBeforeRemoval || provider.calls != providerCallsBeforeRemoval {
		t.Fatalf("field removal=%#v sample=%d provider=%d err=%v", removal, connector.sampleCalls, provider.calls, err)
	}
	var emailStatus string
	var tableFenceCurrent bool
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT asset_status::text FROM platform.metadata_columns WHERE id=$1`, emailColumnID).Scan(&emailStatus); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT last_enriched_structure_hash=structure_hash FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&tableFenceCurrent)
	})
	if err != nil || emailStatus != "INACTIVE" || !tableFenceCurrent {
		t.Fatalf("removed field status=%s tableFenceCurrent=%v err=%v", emailStatus, tableFenceCurrent, err)
	}

	// 整个源表从权威快照中消失时停用 PostgreSQL 资产，且不触发 LLM。
	connector.missing = true
	tableRemoval, err := service.QueueRefreshTablesWithSampleMode(
		ctx, tenantID, actorID, sourceID,
		datasource.MetadataRefreshIncremental, datasource.MetadataSampleRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := service.ProcessNextMetadataJob(ctx, tenantID, "field-delta-worker", 2*time.Minute); err != nil || !processed {
		t.Fatalf("table removal processed=%v err=%v", processed, err)
	}
	tableRemoval, err = service.GetMetadataJob(ctx, tenantID, sourceID, tableRemoval.ID)
	if err != nil || tableRemoval.Status != "SUCCEEDED" || tableRemoval.Succeeded != 1 || connector.sampleCalls != sampleCallsBeforeRemoval || provider.calls != providerCallsBeforeRemoval {
		t.Fatalf("table removal=%#v sample=%d provider=%d err=%v", tableRemoval, connector.sampleCalls, provider.calls, err)
	}
	var tableStatus, orderStatus string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT asset_status::text FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&tableStatus); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT asset_status::text FROM platform.metadata_columns WHERE id=$1`, orderColumnID).Scan(&orderStatus)
	})
	if err != nil || tableStatus != "INACTIVE" || orderStatus != "INACTIVE" {
		t.Fatalf("removed table status=%s orderStatus=%s err=%v", tableStatus, orderStatus, err)
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
		var err error
		sourceID, sourceVersion, err = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "managed-refresh-"+suffix, "Managed Refresh", "MYSQL",
			"encrypted://managed-refresh", "ACTIVE",
			`{"host":"db.internal","port":3306,"database":"sales","username":"reader"}`,
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedSource := attestAndPublishDataSourceFixture(
		t, ctx, pool, tenantID, "", sourceID,
	)
	sourceVersion = publishedSource.Version
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
	// 更晚的同结构同步已经确认资产存在时，旧发现快照不得再将其停用。
	if _, err := repository.DeactivateManagedMetadata(ctx, source, datasource.TableSelection{
		SchemaName: "sales", TableName: "orders", TableID: tableID, StructureHash: initialStructureHash,
	}, time.Now().UTC().Add(-time.Minute)); !errors.Is(err, datasource.ErrMetadataRefreshSuperseded) {
		t.Fatalf("stale source removal err=%v", err)
	}
	var currentSourceVersion int64
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE platform.data_sources SET version=version+1 WHERE id=$1 RETURNING version`, sourceID).Scan(&currentSourceVersion)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult); !errors.Is(err, datasource.ErrMetadataSourceChanged) {
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
	if _, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult); !errors.Is(err, datasource.ErrMetadataRefreshSuperseded) {
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

	applied, err := repository.ApplyManagedMetadata(ctx, source, tableID, initialStructureHash, refreshResult)
	if err != nil || applied.Managed || applied.TableID != "" {
		t.Fatalf("guarded refresh result=%#v err=%v", applied, err)
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
