package compiler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresContractStoreLoadsExistingPinnedRelease(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_DATABASE_URL and ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	var tenantID, domainID, actorID, releaseID, releaseHash, modelVersionID, metricVersionID string
	if err := adminPool.QueryRow(ctx, `SELECT release.tenant_id::text,release.domain_id::text,
		membership.user_id::text,release.id::text,release.content_hash,
		model_object.object_version_id::text,metric_object.object_version_id::text
	FROM askdata.releases AS release
	JOIN platform.domain_memberships AS membership
	  ON membership.tenant_id=release.tenant_id AND membership.domain_id=release.domain_id
	 AND membership.status='ACTIVE'
	JOIN platform.users AS user_account
	  ON user_account.tenant_id=membership.tenant_id AND user_account.id=membership.user_id
	 AND user_account.deleted_at IS NULL
	JOIN askdata.release_objects AS model_object
	  ON model_object.tenant_id=release.tenant_id AND model_object.domain_id=release.domain_id
	 AND model_object.release_id=release.id AND model_object.object_type='SEMANTIC_MODEL'
	JOIN askdata.semantic_models AS model
	  ON model.tenant_id=model_object.tenant_id AND model.id=model_object.object_version_id
	JOIN platform.datasets AS dataset
	  ON dataset.tenant_id=model.tenant_id AND dataset.id=model.dataset_id
	 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
	 AND dataset.current_published_version_id=model.dataset_version_id
	JOIN platform.dataset_versions AS version
	  ON version.tenant_id=model.tenant_id AND version.id=model.dataset_version_id
	 AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
	JOIN platform.dataset_materializations AS materialization
	  ON materialization.tenant_id=model.tenant_id AND materialization.id=model.materialization_id
	 AND materialization.dataset_version_id=model.dataset_version_id
	 AND materialization.status='ACTIVE'
	JOIN askdata.release_objects AS metric_object
	  ON metric_object.tenant_id=release.tenant_id AND metric_object.domain_id=release.domain_id
	 AND metric_object.release_id=release.id AND metric_object.object_type='METRIC'
	JOIN askdata.metric_versions AS metric
	  ON metric.tenant_id=metric_object.tenant_id AND metric.id=metric_object.object_version_id
	 AND metric.semantic_model_version_id=model.id
	WHERE release.status IN ('READY','ACTIVE','SUPERSEDED')
	  AND (SELECT count(*) FROM askdata.release_projections AS projection
	       WHERE projection.release_id=release.id
	         AND projection.target IN ('POSTGRES_REGISTRY','EXECUTION_SEMANTIC_LAYER')
	         AND projection.status='READY'
	         AND projection.expected_content_hash=release.content_hash
	         AND projection.applied_content_hash=release.content_hash
	         AND projection.object_count=release.object_count)=2
	ORDER BY release.updated_at DESC,model_object.object_version_id,metric_object.object_version_id
	LIMIT 1`).Scan(
		&tenantID, &domainID, &actorID, &releaseID, &releaseHash,
		&modelVersionID, &metricVersionID,
	); err != nil {
		t.Skipf("no READY pinned semantic contract fixture: %v", err)
	}
	release := askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{"integration-role"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	lookup := ContractLookup{
		Scope: scope, DomainID: askdata.ID(domainID), IRHash: askdata.HashBytes([]byte("integration-ir")),
		ModelVersionID: askdata.ID(modelVersionID), MetricVersionIDs: []askdata.ID{askdata.ID(metricVersionID)},
	}
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	snapshot, err := NewPostgresContractStore(appPool).LoadContractSnapshot(requestContext, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Release != release || snapshot.Model.ModelVersionID != askdata.ID(modelVersionID) ||
		len(snapshot.Metrics) != 1 || snapshot.Metrics[0].MetricVersionID != askdata.ID(metricVersionID) {
		t.Fatalf("unexpected pinned snapshot: %#v", snapshot)
	}
	if err := validateSnapshot(lookup, nil, snapshot); err != nil {
		t.Fatalf("loaded snapshot validation failed: %v", err)
	}
}
