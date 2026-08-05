package search

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresRetrievalQueriesApplyEmptyPinnedRelease(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_DATABASE_URL and ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	var tenantID, domainID, actorID string
	if err := adminPool.QueryRow(ctx, `SELECT membership.tenant_id::text,membership.domain_id::text,membership.user_id::text
		FROM platform.domain_memberships AS membership
		JOIN platform.business_domains AS domain
		  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
		JOIN platform.users AS user_account
		  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
		WHERE membership.status='ACTIVE' AND domain.status='ACTIVE'
		  AND domain.deleted_at IS NULL AND user_account.deleted_at IS NULL
		ORDER BY membership.created_at LIMIT 1`).Scan(&tenantID, &domainID, &actorID); err != nil {
		t.Skipf("no active membership fixture: %v", err)
	}
	release := askdata.ReleaseRef{ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte("missing release"))}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{"integration-role"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx = database.WithAccessContext(ctx, actorID, domainID)
	store := NewPostgresRetrievalStore(pool)
	for name, run := range map[string]func() ([]RawHit, error){
		"exact": func() ([]RawHit, error) { return store.Exact(ctx, scope, "销售额", []ObjectType{ObjectMetric}, 10) },
		"lexical": func() ([]RawHit, error) {
			return store.Lexical(ctx, scope, "销售额", []ObjectType{ObjectMetric}, 10)
		},
		"vector": func() ([]RawHit, error) {
			return store.Vector(ctx, scope, make([]float32, 2_560), "Qwen3-Embedding-4B", []ObjectType{ObjectMetric}, 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			hits, err := run()
			if err != nil || len(hits) != 0 {
				t.Fatalf("query = %#v, %v", hits, err)
			}
		})
	}
}
