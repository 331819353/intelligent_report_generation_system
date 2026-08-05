package registry

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestImportedDraftPersistsOnlyAgainstCurrentPublishedActiveDWS(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var record PublishedAssetRecord
	var actorID string
	if err := tx.QueryRow(ctx, `SELECT
		dataset.tenant_id::text,dataset.domain_id::text,dataset.id::text,
		dataset.code::text,dataset.name,version.id::text,version.version_no,
		version.layer,version.schema_hash,version.dsl_json,
		materialization.id::text,materialization.published_schema,
		materialization.published_name,materialization.schema_hash,
		materialization.snapshot_hash,materialization.row_count,
		materialization.activated_at,
		(SELECT user_account.id::text FROM platform.users AS user_account
		 WHERE user_account.tenant_id=dataset.tenant_id AND user_account.deleted_at IS NULL
		 ORDER BY user_account.created_at LIMIT 1)
	FROM platform.datasets AS dataset
	JOIN platform.dataset_versions AS version
	  ON version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
	JOIN platform.dataset_materializations AS materialization
	  ON materialization.dataset_id=dataset.id
	 AND materialization.dataset_version_id=version.id
	 AND materialization.tenant_id=dataset.tenant_id
	WHERE version.layer IN ('DWS','ADS') AND materialization.status='ACTIVE'
	  AND dataset.domain_id IS NOT NULL
	ORDER BY materialization.activated_at DESC LIMIT 1`).Scan(
		&record.TenantID, &record.DomainID, &record.DatasetID,
		&record.DatasetCode, &record.DatasetName, &record.DatasetVersionID,
		&record.VersionNo, &record.Layer, &record.SchemaHash, &record.DSLJSON,
		&record.MaterializationID, &record.PublishedSchema, &record.PublishedName,
		&record.MaterializationHash, &record.SnapshotHash, &record.RowCount,
		&record.ActivatedAt, &actorID,
	); err != nil {
		t.Skipf("no active DWS/ADS materialization integration fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET
		status='PUBLISHED',current_published_version_id=$1,deleted_at=NULL
		WHERE id=$2`, record.DatasetVersionID, record.DatasetID); err != nil {
		t.Fatalf("publish fixture dataset in transaction: %v", err)
	}
	// The selected row is an intentionally retired historical fixture. Disable
	// its one-way lifecycle triggers only inside this rollback-only transaction;
	// the askdata source trigger remains enabled and validates the resulting
	// current-published/ACTIVE relational facts.
	if _, err := tx.Exec(ctx, `ALTER TABLE platform.dataset_versions DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable fixture lifecycle triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions
		SET status='PUBLISHED' WHERE id=$1`, record.DatasetVersionID); err != nil {
		t.Fatalf("publish fixture version in transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE platform.dataset_versions ENABLE TRIGGER USER`); err != nil {
		t.Fatalf("restore fixture lifecycle triggers: %v", err)
	}
	summary, err := (DatasetDocumentSummarizer{}).Summarize(record.DSLJSON, record.SchemaHash)
	if err != nil {
		t.Fatalf("summarize integration DWS/ADS: %v", err)
	}
	asset := InventoryAsset{
		DomainID: record.DomainID, DatasetID: record.DatasetID,
		DatasetCode: record.DatasetCode, DatasetName: record.DatasetName,
		DatasetVersionID: record.DatasetVersionID, VersionNo: record.VersionNo,
		Layer: record.Layer, SchemaHash: record.SchemaHash,
		MaterializationID:   record.MaterializationID,
		MaterializationHash: record.MaterializationHash,
		SnapshotHash:        record.SnapshotHash, ActivatedAt: record.ActivatedAt,
		Fields: summary.Fields, OutputGrain: summary.OutputGrain, TimeFields: summary.TimeFields,
	}
	draft, err := buildImportedDraft(record.TenantID, actorID, asset)
	if err != nil {
		t.Fatalf("buildImportedDraft() error = %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		SELECT id,tenant_id,code,name,$3 FROM platform.business_domains
		WHERE id=$1 AND tenant_id=$2 ON CONFLICT(id) DO NOTHING`,
		record.DomainID, record.TenantID, actorID); err != nil {
		t.Fatalf("insert integration askdata domain: %v", err)
	}
	if err := insertSemanticModelDraft(ctx, tx, draft.SemanticModel); err != nil {
		t.Fatalf("insert semantic model draft: %v", err)
	}
	for _, measure := range draft.Measures {
		if err := insertMeasureDraft(ctx, tx, measure); err != nil {
			t.Fatalf("insert measure draft: %v", err)
		}
	}
	for _, dimension := range draft.Dimensions {
		if err := insertDimensionDraft(ctx, tx, dimension); err != nil {
			t.Fatalf("insert dimension draft: %v", err)
		}
	}
	var modelStatus string
	var measureCount, dimensionCount int
	if err := tx.QueryRow(ctx, `SELECT model.status,
		(SELECT count(*) FROM askdata.measures WHERE semantic_model_version_id=model.id),
		(SELECT count(*) FROM askdata.dimensions WHERE semantic_model_version_id=model.id)
	FROM askdata.semantic_models AS model WHERE model.id=$1`, draft.SemanticModel.ID).Scan(
		&modelStatus, &measureCount, &dimensionCount); err != nil {
		t.Fatalf("verify imported draft: %v", err)
	}
	if modelStatus != "DRAFT" || measureCount != len(draft.Measures) || dimensionCount != len(draft.Dimensions) {
		t.Fatalf("imported status/counts = %s/%d/%d", modelStatus, measureCount, dimensionCount)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.semantic_models SET status='CERTIFIED' WHERE id=$1`, draft.SemanticModel.ID); err != nil {
		t.Fatalf("certify imported model: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.measures SET status='CERTIFIED' WHERE semantic_model_version_id=$1`, draft.SemanticModel.ID); err != nil {
		t.Fatalf("certify imported measures: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.dimensions SET status='CERTIFIED' WHERE semantic_model_version_id=$1`, draft.SemanticModel.ID); err != nil {
		t.Fatalf("certify imported dimensions: %v", err)
	}
	draft.SemanticModel.Status = VersionStatusCertified
	releaseObject, err := SemanticModelReleaseObject(draft.SemanticModel)
	if err != nil {
		t.Fatalf("SemanticModelReleaseObject() error = %v", err)
	}
	manifest, err := BuildReleaseManifest([]ReleaseObject{releaseObject})
	if err != nil {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
	releaseID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,1,$6,$6)`, releaseID, record.TenantID,
		record.DomainID, "integration-"+releaseID[:8], manifest.ContentHash, actorID); err != nil {
		t.Fatalf("insert integration release: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
		content_hash,sensitivity,contract_json
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, record.TenantID, record.DomainID,
		releaseID, releaseObject.Type, releaseObject.ObjectID, releaseObject.ObjectVersionID,
		releaseObject.ContentHash, releaseObject.Sensitivity, releaseObject.Contract); err != nil {
		t.Fatalf("insert integration release object: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),set_config('app.domain_id',$2,true),
		set_config('app.user_id',$3,true),set_config('app.access_mode','USER',true)`,
		record.TenantID, record.DomainID, actorID); err != nil {
		t.Fatalf("set integration release context: %v", err)
	}
	var projectionStarted bool
	if err := tx.QueryRow(ctx, `SELECT askdata.start_release_projection($1,$2,'{"integration":true}'::jsonb)`,
		releaseID, actorID).Scan(&projectionStarted); err != nil || !projectionStarted {
		t.Fatalf("start_release_projection() = %v, %v", projectionStarted, err)
	}
	for index := 0; index < 4; index++ {
		var projectionID, claimedDomainID, claimedReleaseID, target, semanticVersion, contentHash, leaseToken string
		var attempt int
		if err := tx.QueryRow(ctx, `SELECT * FROM askdata.claim_release_projection($1,$2,60)`,
			record.TenantID, "integration-worker").Scan(
			&projectionID, &claimedDomainID, &claimedReleaseID, &target,
			&semanticVersion, &contentHash, &leaseToken, &attempt); err != nil {
			t.Fatalf("claim_release_projection(%d): %v", index, err)
		}
		var completed bool
		if err := tx.QueryRow(ctx, `SELECT askdata.complete_release_projection(
			$1,$2,$3,$4,$5,$6,1,'{"integration":true}'::jsonb
		)`, record.TenantID, projectionID, "integration-worker", leaseToken,
			contentHash, "integration-resource-v1").Scan(&completed); err != nil || !completed {
			t.Fatalf("complete_release_projection(%d) = %v, %v", index, completed, err)
		}
	}
	var releaseStatus string
	var readyProjectionCount int
	if err := tx.QueryRow(ctx, `SELECT release.status,
		(SELECT count(*) FROM askdata.release_projections
		 WHERE release_id=release.id AND status='READY'
		   AND applied_content_hash=expected_content_hash)
	FROM askdata.releases AS release WHERE release.id=$1`, releaseID).Scan(
		&releaseStatus, &readyProjectionCount); err != nil {
		t.Fatalf("verify release projections: %v", err)
	}
	if releaseStatus != "READY" || readyProjectionCount != 4 {
		t.Fatalf("release status/projection count = %s/%d", releaseStatus, readyProjectionCount)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.graph_plan_cache(
		tenant_id,domain_id,release_id,question_shape_hash,policy_scope_hash,
		graph_content_hash,plan_hash,plan_json,expires_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,'{"paths":[]}'::jsonb,now()+interval '5 minutes')`,
		record.TenantID, record.DomainID, releaseID,
		strings.Repeat("1", 64), strings.Repeat("2", 64), manifest.ContentHash,
		strings.Repeat("3", 64)); err != nil {
		t.Fatalf("insert READY graph plan cache: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.datasets SET status='DEPRECATED' WHERE id=$1`, record.DatasetID); err != nil {
		t.Fatalf("deprecate integration source: %v", err)
	}
	staleReleaseID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,1,$6,$6)`, staleReleaseID, record.TenantID,
		record.DomainID, "integration-stale-"+staleReleaseID[:8], manifest.ContentHash, actorID); err != nil {
		t.Fatalf("insert stale-source release: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
		content_hash,sensitivity,contract_json
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, record.TenantID, record.DomainID,
		staleReleaseID, releaseObject.Type, releaseObject.ObjectID, releaseObject.ObjectVersionID,
		releaseObject.ContentHash, releaseObject.Sensitivity, releaseObject.Contract); err == nil {
		t.Fatal("release object accepted a semantic model whose DWS/ADS source is no longer published")
	}
}

func TestPostgresStoreTenantTransactionOptimisticLockAndPagination(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_DATABASE_URL and ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()

	var tenantID, domainID, actorID, otherDomainID string
	if err := adminPool.QueryRow(ctx, `SELECT membership.tenant_id::text,
		membership.domain_id::text,membership.user_id::text,
		COALESCE((SELECT domain.id::text FROM platform.business_domains AS domain
		 WHERE domain.tenant_id=membership.tenant_id AND domain.id<>membership.domain_id
		   AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
		 ORDER BY domain.id LIMIT 1),membership.domain_id::text)
	FROM platform.domain_memberships AS membership
	JOIN platform.business_domains AS domain
	  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
	JOIN platform.users AS user_account
	  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
	WHERE membership.status='ACTIVE' AND domain.status='ACTIVE'
	  AND domain.deleted_at IS NULL AND user_account.deleted_at IS NULL
	ORDER BY membership.created_at LIMIT 1`).Scan(&tenantID, &domainID, &actorID, &otherDomainID); err != nil {
		t.Fatalf("select integration identity: %v", err)
	}
	tag, err := adminPool.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		SELECT id,tenant_id,code,name,$3 FROM platform.business_domains
		WHERE id=$1 AND tenant_id=$2 ON CONFLICT(id) DO NOTHING`, domainID, tenantID, actorID)
	if err != nil {
		t.Fatalf("create askdata integration domain: %v", err)
	}
	createdDomain := tag.RowsAffected() == 1
	metricIDs := []string{}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range metricIDs {
			_, _ = adminPool.Exec(cleanupContext, `DELETE FROM askdata.metrics WHERE id=$1`, id)
		}
		if createdDomain {
			_, _ = adminPool.Exec(cleanupContext, `DELETE FROM askdata.release_state WHERE tenant_id=$1 AND domain_id=$2`, tenantID, domainID)
			_, _ = adminPool.Exec(cleanupContext, `DELETE FROM askdata.domains WHERE tenant_id=$1 AND id=$2`, tenantID, domainID)
		}
	}()

	store := NewPostgresStore(appPool)
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	for index := 0; index < 3; index++ {
		id := uuid.NewString()
		metric, err := store.CreateMetric(requestContext, Metric{
			ID: id, TenantID: tenantID, DomainID: domainID,
			Code: "integration_" + id[:8], Name: "集成指标", Status: "DRAFT",
			OwnerID: actorID, Version: 1,
		})
		if err != nil {
			t.Fatalf("CreateMetric(%d) error = %v", index, err)
		}
		metricIDs = append(metricIDs, metric.ID)
	}
	metric, err := store.GetMetric(requestContext, tenantID, domainID, metricIDs[0])
	if err != nil {
		t.Fatalf("GetMetric() error = %v", err)
	}
	stale := metric
	metric.Description = "乐观锁更新"
	updated, err := store.UpdateMetric(requestContext, metric)
	if err != nil || updated.Version != metric.Version+1 {
		t.Fatalf("UpdateMetric() = %#v, %v", updated, err)
	}
	stale.Description = "覆盖新值"
	if _, err := store.UpdateMetric(requestContext, stale); !errors.Is(err, ErrRegistryVersionConflict) {
		t.Fatalf("stale UpdateMetric() error = %v", err)
	}

	pageOne, err := store.ListMetrics(requestContext, tenantID, domainID, "", 1)
	if err != nil || len(pageOne.Items) != 1 || pageOne.NextCursor == "" {
		t.Fatalf("ListMetrics(page 1) = %#v, %v", pageOne, err)
	}
	pageTwo, err := store.ListMetrics(requestContext, tenantID, domainID, pageOne.NextCursor, 1)
	if err != nil || len(pageTwo.Items) != 1 || pageTwo.Items[0].ID == pageOne.Items[0].ID {
		t.Fatalf("ListMetrics(page 2) = %#v, %v", pageTwo, err)
	}

	if otherDomainID != domainID {
		otherContext := database.WithAccessContext(ctx, actorID, otherDomainID)
		if _, err := store.GetMetric(otherContext, tenantID, domainID, metricIDs[0]); !errors.Is(err, ErrRegistryNotFound) {
			t.Fatalf("cross-domain GetMetric() error = %v, want not found", err)
		}
	}
}
