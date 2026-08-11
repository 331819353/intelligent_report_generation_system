package registry

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

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
		t.Skipf("no active domain identity integration fixture: %v", err)
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
