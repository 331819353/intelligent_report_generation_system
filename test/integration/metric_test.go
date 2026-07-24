//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/queryruntime"
)

func TestMetricPersistencePublicationAndUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metric-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "metric-foreign-"+suffix)
	// 已发布版本受数据库不可变约束保护，一次性集成数据库统一回收主测试租户。
	t.Cleanup(func() { cleanupTenant(pool, foreignTenantID) })

	actorID, secondActorID, sourceID, datasetRecord, datasetVersion := preparePublishedMetricDataset(t, ctx, pool, tenantID, suffix)
	store := metric.NewPostgresStore(pool)
	atomicPrepared := prepareMetricDefinition(t, datasetRecord.ID, datasetVersion.ID,
		"order_revenue", "订单收入", "ATOMIC", metric.Expression{Type: "FIELD_REF", FieldID: "field_revenue"}, "SUM", "ADDITIVE")
	created, err := store.Create(ctx, tenantID, actorID, atomicPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.DraftRecordVersion != 1 || created.DraftVersionNo != 1 || created.Status != "DRAFT" {
		t.Fatalf("Create() record=%#v", created)
	}
	if _, err := time.Parse(time.RFC3339Nano, created.CreatedAt); err != nil {
		t.Fatalf("Create() createdAt 不是 RFC3339：%q error=%v", created.CreatedAt, err)
	}
	if _, err := store.GetVersionByID(ctx, tenantID, created.DraftVersionID); !errors.Is(err, metric.ErrVersionNotFound) {
		t.Fatalf("GetVersionByID(draft) error=%v, want ErrVersionNotFound", err)
	}
	if _, err := store.Get(ctx, foreignTenantID, created.ID); !errors.Is(err, metric.ErrNotFound) {
		t.Fatalf("cross-tenant Get() error=%v, want ErrNotFound", err)
	}
	if items, total, err := store.List(ctx, foreignTenantID, 20, 0); err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("cross-tenant List() items=%#v total=%d err=%v", items, total, err)
	}
	if _, err := store.Create(ctx, tenantID, actorID, atomicPrepared); !errors.Is(err, metric.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error=%v, want ErrAlreadyExists", err)
	}

	updatedPrepared := prepareMetricDefinition(t, datasetRecord.ID, datasetVersion.ID,
		"order_revenue", "订单收入（已复核）", "ATOMIC", metric.Expression{Type: "FIELD_REF", FieldID: "field_revenue"}, "SUM", "ADDITIVE")
	if _, err := store.Update(ctx, tenantID, actorID, created.ID, metric.UpdateInput{
		ExpectedVersion: created.Version + 1, ExpectedDraftRecordVersion: created.DraftRecordVersion,
		ExpectedDefinitionHash: created.DefinitionHash,
	}, updatedPrepared); !errors.Is(err, metric.ErrConflict) {
		t.Fatalf("stale Update() error=%v, want ErrConflict", err)
	}
	identityChanged := prepareMetricDefinition(t, datasetRecord.ID, datasetVersion.ID,
		"renamed_order_revenue", "订单收入（已复核）", "ATOMIC", metric.Expression{Type: "FIELD_REF", FieldID: "field_revenue"}, "SUM", "ADDITIVE")
	if _, err := store.Update(ctx, tenantID, actorID, created.ID, metric.UpdateInput{
		ExpectedVersion: created.Version, ExpectedDraftRecordVersion: created.DraftRecordVersion,
		ExpectedDefinitionHash: created.DefinitionHash,
	}, identityChanged); !errors.Is(err, metric.ErrInvalidDefinition) {
		t.Fatalf("identity-changing Update() error=%v, want ErrInvalidDefinition", err)
	}
	updated, err := store.Update(ctx, tenantID, actorID, created.ID, metric.UpdateInput{
		ExpectedVersion: created.Version, ExpectedDraftRecordVersion: created.DraftRecordVersion,
		ExpectedDefinitionHash: created.DefinitionHash,
	}, updatedPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.DraftRecordVersion != 2 || updated.DefinitionHash != updatedPrepared.DefinitionHash {
		t.Fatalf("Update() record=%#v", updated)
	}

	forbiddenPlan := metricPublishPlan(updated, updatedPrepared, "metric-forbidden-"+suffix, strings.Repeat("f", 64))
	if _, err := store.Publish(ctx, tenantID, actorID, created.ID, forbiddenPlan); !errors.Is(err, metric.ErrForbidden) {
		t.Fatalf("Publish() without permission error=%v, want ErrForbidden", err)
	}
	grantMetricPublish(t, ctx, pool, tenantID, actorID, secondActorID, created.ID)

	// 两个不同幂等键同时发布同一草稿时，只允许一个事务分配该版本号并移动指针。
	plans := []metric.PublishPlan{
		metricPublishPlan(updated, updatedPrepared, "metric-publish-a-"+suffix, strings.Repeat("a", 64)),
		metricPublishPlan(updated, updatedPrepared, "metric-publish-b-"+suffix, strings.Repeat("b", 64)),
	}
	type publishResult struct {
		plan   metric.PublishPlan
		record metric.VersionRecord
		err    error
	}
	results := make(chan publishResult, len(plans))
	for _, plan := range plans {
		go func(candidate metric.PublishPlan) {
			record, publishErr := store.Publish(ctx, tenantID, actorID, created.ID, candidate)
			results <- publishResult{plan: candidate, record: record, err: publishErr}
		}(plan)
	}
	var published metric.VersionRecord
	var winningPlan metric.PublishPlan
	conflicts := 0
	for range plans {
		result := <-results
		if result.err == nil {
			published, winningPlan = result.record, result.plan
			continue
		}
		if errors.Is(result.err, metric.ErrConflict) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent Publish() unexpected error=%v", result.err)
	}
	if published.ID == "" || published.Status != "PUBLISHED" || published.VersionNo != 1 || conflicts != 1 {
		t.Fatalf("concurrent Publish() record=%#v conflicts=%d", published, conflicts)
	}
	if _, err := time.Parse(time.RFC3339Nano, published.PublishedAt); err != nil {
		t.Fatalf("Publish() publishedAt 不是 RFC3339：%q error=%v", published.PublishedAt, err)
	}

	replayed, found, err := store.ReplayPublication(ctx, tenantID, actorID, created.ID,
		winningPlan.IdempotencyKey, winningPlan.RequestHash)
	if err != nil || !found || replayed.ID != published.ID || string(replayed.Definition) != string(published.Definition) {
		t.Fatalf("ReplayPublication() record=%#v found=%v err=%v", replayed, found, err)
	}
	if _, _, err := store.ReplayPublication(ctx, tenantID, secondActorID, created.ID,
		winningPlan.IdempotencyKey, winningPlan.RequestHash); !errors.Is(err, metric.ErrIdempotencyConflict) {
		t.Fatalf("cross-actor ReplayPublication() error=%v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := store.ReplayPublication(ctx, tenantID, actorID, created.ID,
		winningPlan.IdempotencyKey, strings.Repeat("c", 64)); !errors.Is(err, metric.ErrIdempotencyConflict) {
		t.Fatalf("changed-request ReplayPublication() error=%v, want ErrIdempotencyConflict", err)
	}
	if _, err := store.Publish(ctx, tenantID, actorID, created.ID,
		metricPublishPlan(updated, updatedPrepared, "metric-stale-"+suffix, strings.Repeat("d", 64))); !errors.Is(err, metric.ErrConflict) {
		t.Fatalf("stale Publish() error=%v, want ErrConflict", err)
	}

	afterPublish, err := store.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPublish.Status != "PUBLISHED" || afterPublish.CurrentPublishedVersionID != published.ID ||
		afterPublish.Version != updated.Version+1 || afterPublish.DraftVersionNo != 2 {
		t.Fatalf("metric after Publish()=%#v", afterPublish)
	}
	exact, err := store.GetVersion(ctx, tenantID, created.ID, published.ID)
	if err != nil || exact.ID != published.ID || exact.DefinitionHash != updatedPrepared.DefinitionHash {
		t.Fatalf("GetVersion() record=%#v err=%v", exact, err)
	}
	byID, err := store.GetVersionByID(ctx, tenantID, published.ID)
	if err != nil || byID.ID != published.ID || byID.MetricID != created.ID {
		t.Fatalf("GetVersionByID() record=%#v err=%v", byID, err)
	}
	versions, versionTotal, err := store.ListVersions(ctx, tenantID, created.ID, 20, 0)
	if err != nil || versionTotal != 1 || len(versions) != 1 || versions[0].ID != published.ID {
		t.Fatalf("ListVersions() versions=%#v total=%d err=%v", versions, versionTotal, err)
	}
	assertMetricPublicationRows(t, ctx, pool, tenantID, created.ID, published.ID, winningPlan.IdempotencyKey)

	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.metric_versions SET definition_hash=$1 WHERE id::text=$2`, strings.Repeat("9", 64), published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`DELETE FROM platform.metric_versions WHERE id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.metric_dimensions SET dimension_name='篡改维度' WHERE metric_version_id::text=$1`, published.ID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.metric_publish_idempotency SET response_json=jsonb_set(response_json,'{status}','"STALE"'::jsonb) WHERE metric_id::text=$1 AND idempotency_key=$2`, created.ID, winningPlan.IdempotencyKey)

	derivedPrepared := prepareMetricDefinition(t, datasetRecord.ID, datasetVersion.ID,
		"double_order_revenue", "双倍订单收入", "DERIVED", metric.Expression{
			Type: "MULTIPLY", Arguments: []metric.Expression{
				{Type: "METRIC_REF", MetricVersionID: published.ID},
				{Type: "LITERAL", Value: "2"},
			},
		}, "NONE", "NON_ADDITIVE")
	derived, err := store.Create(ctx, tenantID, actorID, derivedPrepared)
	if err != nil {
		t.Fatal(err)
	}
	grantMetricPublish(t, ctx, pool, tenantID, actorID, "", derived.ID)

	queryStore := queryruntime.NewPostgresStore(pool)
	activeQueryID := uuid.NewString()
	if err := queryStore.Start(ctx, queryruntime.RunRecord{
		ID: activeQueryID, TenantID: tenantID, DatasetID: datasetRecord.ID, DatasetVersionID: datasetVersion.ID,
		MetricID: created.ID, MetricVersionID: published.ID, ActorID: actorID, SourceID: sourceID,
		RunType: "PREVIEW", PlanHash: strings.Repeat("e", 64), ParameterHash: strings.Repeat("f", 64),
	}); err != nil {
		t.Fatal(err)
	}
	insertMetricReportDependencies(t, ctx, pool, tenantID, actorID, created.ID, published.ID, suffix)

	usage, err := store.GetVersionUsage(ctx, tenantID, created.ID, published.ID)
	if err != nil || usage.ReportDraftReferences != 1 || usage.DownstreamDraftReferences != 1 ||
		usage.DownstreamPublishedReferences != 0 || usage.ActiveQueryRuns != 1 {
		t.Fatalf("GetVersionUsage() usage=%#v err=%v", usage, err)
	}

	// 查询运行时允许草稿试算精确关联 DRAFT 版本，但版本接口仍不会暴露该草稿。
	draftQueryID := uuid.NewString()
	if err := queryStore.Start(ctx, queryruntime.RunRecord{
		ID: draftQueryID, TenantID: tenantID, DatasetID: datasetRecord.ID, DatasetVersionID: datasetVersion.ID,
		MetricID: created.ID, MetricVersionID: afterPublish.DraftVersionID, ActorID: actorID, SourceID: sourceID,
		RunType: "PREVIEW", PlanHash: strings.Repeat("1", 64), ParameterHash: strings.Repeat("2", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queryStore.Finish(ctx, tenantID, draftQueryID, "SUCCEEDED", 0, 1, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := insertMismatchedMetricQueryRun(ctx, pool, tenantID, actorID, sourceID,
		datasetRecord.ID, datasetVersion.ID, created.ID, derived.DraftVersionID); err == nil {
		t.Fatal("query_runs 接受了不属于 metric_id 的 metric_version_id")
	}
	if err := insertMismatchedMetricQueryRun(ctx, pool, tenantID, actorID, sourceID,
		datasetRecord.ID, datasetRecord.DraftVersionID, created.ID, published.ID); err == nil {
		t.Fatal("query_runs 接受了与指标版本不一致的 dataset_version_id")
	}

	// 草稿已经生成查询审计后仍应允许升级到同一数据集的新发布版本；
	// 历史审计必须保留执行时的数据集版本，不能被级联改写。
	datasetService := dataset.NewService(dataset.NewPostgresStore(pool), &publicationValidatorStub{
		result: dataset.PreviewResult{QueryID: uuid.NewString()},
	})
	currentDataset, err := datasetService.Get(ctx, tenantID, datasetRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentDataset, err = datasetService.Update(ctx, tenantID, actorID, datasetRecord.ID, dataset.UpdateInput{
		Name: currentDataset.Name, Description: currentDataset.Description,
		ExpectedVersion: currentDataset.Version, DSL: currentDataset.DSL,
	})
	if err != nil {
		t.Fatal(err)
	}
	nextDatasetVersion, err := datasetService.Publish(ctx, tenantID, actorID, datasetRecord.ID,
		"metric-dataset-upgrade-"+suffix, dataset.PublishInput{
			DraftVersionID: currentDataset.DraftVersionID, ExpectedVersion: currentDataset.Version,
			ExpectedDraftRecordVersion: currentDataset.DraftRecordVersion, ExpectedDSLHash: currentDataset.DSLHash,
			ValidationParameters: map[string]any{},
		})
	if err != nil {
		t.Fatal(err)
	}
	switchedPrepared := prepareMetricDefinition(t, datasetRecord.ID, nextDatasetVersion.ID,
		"order_revenue", "订单收入（已复核）", "ATOMIC",
		metric.Expression{Type: "FIELD_REF", FieldID: "field_revenue"}, "SUM", "ADDITIVE")
	afterPublish, err = store.Update(ctx, tenantID, actorID, created.ID, metric.UpdateInput{
		ExpectedVersion: afterPublish.Version, ExpectedDraftRecordVersion: afterPublish.DraftRecordVersion,
		ExpectedDefinitionHash: afterPublish.DefinitionHash,
	}, switchedPrepared)
	if err != nil || afterPublish.DatasetVersionID != nextDatasetVersion.ID {
		t.Fatalf("Update() dataset version switch record=%#v err=%v", afterPublish, err)
	}
	var auditedDatasetVersion string
	var rebuiltDimensions int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT dataset_version_id::text FROM platform.query_runs WHERE id::text=$1`,
			draftQueryID).Scan(&auditedDatasetVersion); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_dimensions
			WHERE metric_version_id::text=$1 AND dataset_version_id::text=$2`,
			afterPublish.DraftVersionID, nextDatasetVersion.ID).Scan(&rebuiltDimensions)
	})
	if err != nil || auditedDatasetVersion != datasetVersion.ID || rebuiltDimensions != 1 {
		t.Fatalf("draft dataset switch audit=%s dimensions=%d err=%v", auditedDatasetVersion, rebuiltDimensions, err)
	}
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.query_runs SET dataset_version_id=$1 WHERE id::text=$2`, nextDatasetVersion.ID, draftQueryID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.query_runs SET id=$1 WHERE id::text=$2`, uuid.NewString(), draftQueryID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.query_runs SET actor_user_id=$1 WHERE id::text=$2`, secondActorID, draftQueryID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.query_runs SET status='RUNNING',completed_at=NULL WHERE id::text=$1`, draftQueryID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`UPDATE platform.query_runs SET row_count=row_count+1 WHERE id::text=$1`, draftQueryID)
	assertTenantSQLRejected(t, ctx, pool, tenantID,
		`DELETE FROM platform.query_runs WHERE id::text=$1`, draftQueryID)

	derivedPublished, err := store.Publish(ctx, tenantID, actorID, derived.ID,
		metricPublishPlan(derived, derivedPrepared, "derived-publish-"+suffix, strings.Repeat("3", 64)))
	if err != nil {
		t.Fatal(err)
	}
	usage, err = store.GetVersionUsage(ctx, tenantID, created.ID, published.ID)
	if err != nil || usage.DownstreamDraftReferences != 1 || usage.DownstreamPublishedReferences != 1 ||
		usage.ActiveQueryRuns != 1 {
		t.Fatalf("GetVersionUsage() after downstream publication usage=%#v err=%v", usage, err)
	}
	if derivedPublished.Status != "PUBLISHED" {
		t.Fatalf("derived Publish() record=%#v", derivedPublished)
	}
	if err := queryStore.Finish(ctx, tenantID, activeQueryID, "SUCCEEDED", 0, 1, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	usage, err = store.GetVersionUsage(ctx, tenantID, created.ID, published.ID)
	if err != nil || usage.ActiveQueryRuns != 0 {
		t.Fatalf("GetVersionUsage() after query finish usage=%#v err=%v", usage, err)
	}
	if _, err := store.GetVersion(ctx, foreignTenantID, created.ID, published.ID); !errors.Is(err, metric.ErrVersionNotFound) {
		t.Fatalf("cross-tenant GetVersion() error=%v, want ErrVersionNotFound", err)
	}
	if _, err := store.GetVersionUsage(ctx, tenantID, derived.ID, published.ID); !errors.Is(err, metric.ErrVersionNotFound) {
		t.Fatalf("wrong-parent GetVersionUsage() error=%v, want ErrVersionNotFound", err)
	}

	if _, err := store.TransitionVersion(ctx, tenantID, actorID, created.ID, published.ID, metric.VersionTransitionInput{
		ExpectedVersion: afterPublish.Version, ExpectedStatus: "PUBLISHED", TargetStatus: "DEPRECATED",
	}); !errors.Is(err, metric.ErrVersionInUse) {
		t.Fatalf("TransitionVersion() with active published downstream error=%v, want ErrVersionInUse", err)
	}
	derivedDeprecated, err := store.TransitionVersion(ctx, tenantID, actorID, derived.ID, derivedPublished.ID, metric.VersionTransitionInput{
		ExpectedVersion: derivedPublished.MetricRecordVersion, ExpectedStatus: "PUBLISHED", TargetStatus: "DEPRECATED",
	})
	if err != nil || derivedDeprecated.Status != "DEPRECATED" {
		t.Fatalf("deprecate downstream version record=%#v err=%v", derivedDeprecated, err)
	}
	deprecated, err := store.TransitionVersion(ctx, tenantID, actorID, created.ID, published.ID, metric.VersionTransitionInput{
		ExpectedVersion: afterPublish.Version, ExpectedStatus: "PUBLISHED", TargetStatus: "DEPRECATED",
	})
	if err != nil || deprecated.Status != "DEPRECATED" {
		t.Fatalf("TransitionVersion() record=%#v err=%v", deprecated, err)
	}
	afterDeprecated, err := store.Get(ctx, tenantID, created.ID)
	if err != nil || afterDeprecated.Status != "DEPRECATED" || afterDeprecated.CurrentPublishedVersionID != "" ||
		afterDeprecated.Version != afterPublish.Version+1 {
		t.Fatalf("metric after deprecation=%#v err=%v", afterDeprecated, err)
	}
	if err := queryStore.Start(ctx, queryruntime.RunRecord{
		ID: uuid.NewString(), TenantID: tenantID, DatasetID: datasetRecord.ID, DatasetVersionID: datasetVersion.ID,
		MetricID: created.ID, MetricVersionID: published.ID, ActorID: actorID, SourceID: sourceID,
		RunType: "PREVIEW", PlanHash: strings.Repeat("4", 64), ParameterHash: strings.Repeat("5", 64),
	}); !errors.Is(err, metric.ErrVersionUnavailable) {
		t.Fatalf("query_runs 接受了已废弃指标版本或错误映射异常：%v", err)
	}
}

func preparePublishedMetricDataset(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, suffix string) (actorID, secondActorID, sourceID string, record dataset.Record, version dataset.VersionRecord) {
	t.Helper()
	var tableID string
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'metric tester','integration-hash') RETURNING id::text`, tenantID, "metric-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'metric second tester','integration-hash') RETURNING id::text`, tenantID, "metric-second-"+suffix+"@it.test").Scan(&secondActorID); err != nil {
			return err
		}
		var sourceErr error
		sourceID, _, sourceErr = insertVersionedDataSourceTx(
			ctx, tx, tenantID, "metric_orders", "Metric Orders", "MYSQL",
			"env://METRIC_IT", "ACTIVE", "{}",
		)
		if sourceErr != nil {
			return sourceErr
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at
		) VALUES($1,$2,'sales','orders','TABLE',repeat('4',64),now()) RETURNING id::text`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		columns := []struct{ name, nativeType, canonicalType string }{
			{"order_date", "date", "DATE"},
			{"order_amount", "decimal(18,2)", "DECIMAL"},
		}
		for index, column := range columns {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
				tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at
			) VALUES($1,$2,$3,$4,$5,$6,false,repeat($7,64),now())`, tenantID, tableID,
				column.name, index+1, column.nativeType, column.canonicalType, fmt.Sprintf("%d", index+5)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	attestAndPublishDataSourceFixture(t, ctx, pool, tenantID, actorID, sourceID)

	raw := metricDatasetDefinition(t, sourceID, tableID)
	validator := &publicationValidatorStub{result: dataset.PreviewResult{QueryID: uuid.NewString()}}
	service := dataset.NewService(dataset.NewPostgresStore(pool), validator)
	record, err = service.Create(ctx, tenantID, actorID, dataset.CreateInput{
		Code: "metric_orders", Name: "指标明细数据集", Description: "指标持久化集成测试", Type: "SINGLE_SOURCE", DSL: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$2)`, tenantID, actorID, record.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err = service.Publish(ctx, tenantID, actorID, record.ID, "metric-dataset-publish-"+suffix, dataset.PublishInput{
		DraftVersionID: record.DraftVersionID, ExpectedVersion: record.Version,
		ExpectedDraftRecordVersion: record.DraftRecordVersion, ExpectedDSLHash: record.DSLHash,
		ValidationParameters: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return actorID, secondActorID, sourceID, record, version
}

func metricDatasetDefinition(t *testing.T, sourceID, tableID string) []byte {
	t.Helper()
	visible := true
	document := dataset.Document{
		DSLVersion: "1.0",
		Dataset:    dataset.Descriptor{Code: "metric_orders", Name: "指标明细数据集", Description: "指标持久化集成测试", Type: "SINGLE_SOURCE"},
		Nodes: []dataset.Node{{
			ID: "orders", Type: "TABLE", DataSourceID: sourceID, TableID: tableID, Alias: "o",
			Projection: []string{"order_date", "order_amount"}, SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		Fields: []dataset.Field{
			{ID: "field_stat_month", Code: "order_date", Name: "订单日期", Role: "TIME", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_date"}, CanonicalType: "DATE", Nullable: false, Visible: &visible},
			{ID: "field_revenue", Code: "order_amount", Name: "订单金额", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_amount"}, CanonicalType: "DECIMAL", Unit: "元", Nullable: false, Visible: &visible},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{},
		Sorts:      []dataset.Sort{{FieldID: "field_stat_month", Direction: "ASC"}},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每一行代表一笔订单明细", KeyFields: []string{"order_date"},
			TimeField: "order_date", DefaultTimeGrain: "DAY",
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 100, ResultLimit: 1000,
			CacheTTLSeconds: 60, Materialization: dataset.MaterializationPolicy{Enabled: false},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func prepareMetricDefinition(t *testing.T, datasetID, datasetVersionID, code, name, metricType string, expression metric.Expression, aggregation, additivity string) metric.Prepared {
	t.Helper()
	definition := metric.Definition{
		SchemaVersion: "1.0",
		Metric:        metric.Descriptor{Code: code, Name: name, Description: "指标持久化集成测试", Type: metricType},
		DatasetID:     datasetID, DatasetVersionID: datasetVersionID,
		Expression: expression, Aggregation: aggregation, Unit: "元", NumberFormat: "#,##0.00",
		TimeFieldID: "field_stat_month", TimeGrain: "MONTH", Additivity: additivity,
		NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions: []metric.Dimension{{
			FieldID: "field_stat_month", Name: "统计月份", HierarchyFieldIDs: []string{},
			SortDirection: "ASC", NullLabel: "未知",
		}},
		DecimalScale: 2, RoundingMode: "HALF_UP", NullHandling: "IGNORE", DivisionByZero: "NULL",
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := metric.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func metricPublishPlan(record metric.Record, prepared metric.Prepared, key, requestHash string) metric.PublishPlan {
	return metric.PublishPlan{
		IdempotencyKey: key, RequestHash: requestHash, ExpectedVersion: record.Version,
		DraftVersionID: record.DraftVersionID, ExpectedDraftRecordVersion: record.DraftRecordVersion,
		ExpectedDefinitionHash: record.DefinitionHash, Prepared: prepared,
	}
}

func grantMetricPublish(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, secondActorID, metricID string) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		actorIDs := []string{actorID}
		if secondActorID != "" {
			actorIDs = append(actorIDs, secondActorID)
		}
		for _, subjectID := range actorIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
				tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
			) VALUES($1,'USER',$2,'METRIC',$3,'PUBLISH',$4)`, tenantID, subjectID, metricID, actorID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertMetricPublicationRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, metricID, versionID, key string) {
	t.Helper()
	var dimensions, idempotency, audits int
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_dimensions WHERE metric_version_id::text=$1`, versionID).Scan(&dimensions); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_publish_idempotency
			WHERE metric_id::text=$1 AND idempotency_key=$2 AND published_version_id::text=$3`, metricID, key, versionID).Scan(&idempotency); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs
			WHERE resource_type='METRIC' AND resource_id=$1 AND action='PUBLISH'`, metricID).Scan(&audits)
	})
	if err != nil || dimensions != 1 || idempotency != 1 || audits != 1 {
		t.Fatalf("published rows dimensions=%d idempotency=%d audits=%d err=%v", dimensions, idempotency, audits, err)
	}
}

func insertMetricReportDependencies(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, metricID, versionID, suffix string) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var reportID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.reports(
			tenant_id,code,name,report_type,created_by,updated_by
		) VALUES($1,$2,'指标占用测试报告','REPORT',$3,$3) RETURNING id::text`, tenantID, "metric-usage-"+suffix, actorID).Scan(&reportID); err != nil {
			return err
		}
		dependencies := []struct{ dependencyType, dependencyID string }{
			{"METRIC_VERSION", versionID},
			// 旧报告只保存指标主对象 ID；精确版本索引启用后仍需兼容其占用语义。
			{"METRIC", metricID},
		}
		for index, dependency := range dependencies {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_dependencies(
				tenant_id,report_id,revision_no,dependency_type,dependency_id,json_path
			) VALUES($1,$2,1,$3,$4,$5)`, tenantID, reportID, dependency.dependencyType, dependency.dependencyID,
				fmt.Sprintf("pages[0].blocks[%d].component.binding.metricVersionId", index)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertMismatchedMetricQueryRun(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, sourceID, datasetID, datasetVersionID, metricID, metricVersionID string) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.query_runs(
			id,tenant_id,dataset_id,dataset_version_id,metric_id,metric_version_id,
			actor_user_id,data_source_id,run_type,plan_hash,parameter_hash,status
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PREVIEW',$9,$10,'RUNNING')`, uuid.NewString(), tenantID,
			datasetID, datasetVersionID, metricID, metricVersionID, actorID, sourceID,
			strings.Repeat("7", 64), strings.Repeat("8", 64))
		return err
	})
}
