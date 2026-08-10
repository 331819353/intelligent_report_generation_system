package search

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresRecallAuditRecordsLabelFreeSampleAndComparesExact(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if appURL == "" || workerURL == "" || adminURL == "" {
		t.Skip("set AskData app, worker and admin integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	appPool := openRecallAuditPool(t, ctx, appURL)
	defer appPool.Close()
	workerPool := openRecallAuditPool(t, ctx, workerURL)
	defer workerPool.Close()
	adminPool := openRecallAuditPool(t, ctx, adminURL)
	defer adminPool.Close()

	var tenantID, domainID, actorID string
	syntheticPlatform := false
	if err := adminPool.QueryRow(ctx, `SELECT membership.tenant_id::text,
		membership.domain_id::text,membership.user_id::text
		FROM platform.domain_memberships AS membership
		JOIN platform.business_domains AS domain
		  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
		JOIN platform.users AS user_account
		  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
		WHERE membership.status='ACTIVE' AND domain.status='ACTIVE'
		  AND domain.deleted_at IS NULL AND user_account.deleted_at IS NULL
		ORDER BY membership.created_at LIMIT 1`).Scan(&tenantID, &domainID, &actorID); err != nil {
		if err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		tenantID, domainID, actorID = uuid.NewString(), uuid.NewString(), uuid.NewString()
		syntheticPlatform = true
	}

	releaseID := uuid.NewString()
	releaseHash := string(askdata.HashBytes([]byte("SEARCH-006 integration " + releaseID)))
	createdDomain := syntheticPlatform
	if !syntheticPlatform {
		if err := adminPool.QueryRow(ctx, `SELECT NOT EXISTS(
		SELECT 1 FROM askdata.domains WHERE id=$1 AND tenant_id=$2
	)`, domainID, tenantID).Scan(&createdDomain); err != nil {
			t.Fatal(err)
		}
	}
	setupRecallAuditFixture(
		t, ctx, adminPool, tenantID, domainID, actorID, releaseID, releaseHash,
		createdDomain, syntheticPlatform,
	)
	defer cleanupRecallAuditFixture(
		t, adminPool, tenantID, domainID, actorID, releaseID, createdDomain, syntheticPlatform,
	)

	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{askdata.ID(uuid.NewString())}, askdata.ReleaseRef{
			ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	queryVector := make([]float32, SearchEmbeddingDimension)
	queryVector[0] = 1
	appCtx := database.WithAccessContext(ctx, actorID, domainID)
	hits, err := NewPostgresRetrievalStore(appPool).Vector(
		appCtx, scope, queryVector, "Qwen3-Embedding-4B", []ObjectType{ObjectMetric}, 30,
	)
	if err != nil || len(hits) != 30 {
		t.Fatalf("online exact-routed vector hits = %d, %v", len(hits), err)
	}
	var sampleCount int
	if err := adminPool.QueryRow(ctx, `SELECT count(*) FROM askdata.search_query_samples
		WHERE tenant_id=$1 AND release_id=$2`, tenantID, releaseID).Scan(&sampleCount); err != nil {
		t.Fatal(err)
	}
	if sampleCount != 1 {
		t.Fatalf("label-free sample count = %d", sampleCount)
	}
	if err := appPool.QueryRow(ctx, `SELECT count(*) FROM askdata.search_query_samples`).Scan(&sampleCount); err == nil {
		t.Fatal("app role unexpectedly read raw query embeddings")
	}

	options := DefaultRecallAuditOptions("Qwen3-Embedding-4B", SearchEmbeddingDimension)
	options.SampleSizePerGroup = 10
	auditor, err := NewRecallAuditor(NewPostgresRecallAuditStore(workerPool), options, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := auditor.RunTenant(ctx, tenantID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("audit results = %#v", results)
	}
	for _, result := range results {
		if result.DocumentType != ObjectMetric || result.SampleSize != 1 ||
			result.Recall != 1 || result.BelowThreshold {
			t.Fatalf("audit result = %#v", result)
		}
	}
	var auditCount int
	if err := adminPool.QueryRow(ctx, `SELECT count(*) FROM askdata.search_recall_audits
		WHERE tenant_id=$1 AND domain_id=$2`, tenantID, domainID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("persisted audit count = %d", auditCount)
	}
}

func openRecallAuditPool(t *testing.T, ctx context.Context, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func setupRecallAuditFixture(
	t *testing.T,
	ctx context.Context,
	adminPool *pgxpool.Pool,
	tenantID, domainID, actorID, releaseID, releaseHash string,
	createdDomain, syntheticPlatform bool,
) {
	t.Helper()
	tx, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if syntheticPlatform {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
			VALUES($1,$2,'SEARCH-006 integration tenant')`,
			tenantID, "search_recall_"+tenantID[:8]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,'SEARCH-006 actor','integration-only','ACTIVE')`,
			actorID, tenantID, "SEARCH"+actorID[:8], actorID+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,'search_recall','SEARCH-006 integration',true,$3)`,
			domainID, tenantID, actorID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,status,member_role,assigned_by
		) VALUES($1,$2,$3,'ACTIVE','MEMBER',$3)`, tenantID, domainID, actorID); err != nil {
			t.Fatal(err)
		}
	}
	if createdDomain {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
			id,tenant_id,code,name,owner_id
		) VALUES($1,$2,$3,'SEARCH-006 integration',$4)`,
			domainID, tenantID, "search-recall-"+releaseID[:8], actorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		tenant_id,domain_id,id,semantic_version,content_hash,status,object_count,
		created_by,updated_by,ready_at
	) VALUES($1,$2,$3,$4,$5,'READY',30,$6,$6,now())`,
		tenantID, domainID, releaseID, "search-recall/"+releaseID, releaseHash, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_projections(
		tenant_id,domain_id,release_id,target,status,expected_content_hash,
		applied_content_hash,resource_version,object_count,completed_at
	) VALUES($1,$2,$3,'SEARCH_INDEX','READY',$4,$4,'integration',30,now())`,
		tenantID, domainID, releaseID, releaseHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatalf("set fixture replication role: %v", err)
	}
	for index := 0; index < 30; index++ {
		objectID := uuid.NewString()
		objectVersionID := uuid.NewString()
		objectHash := string(askdata.HashBytes([]byte(fmt.Sprintf("object-%d", index))))
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
			tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
			content_hash,sensitivity,contract_json
		) VALUES($1,$2,$3,'METRIC',$4,$5,$6,'INTERNAL','{}'::jsonb)`,
			tenantID, domainID, releaseID, objectID, objectVersionID, objectHash); err != nil {
			t.Fatal(err)
		}
		vector := make([]float32, SearchEmbeddingDimension)
		vector[0] = 1
		vector[1] = float32(index) / 1_000
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.search_documents(
			tenant_id,domain_id,object_type,object_version_id,view_type,sensitivity,
			index_policy,document,metadata,input_hash,embedding,embedding_model,
			embedding_version,embedding_dim,embedding_status,embedded_at
		) VALUES($1,$2,'METRIC',$3,'DEFINITION_QUESTION','INTERNAL','HYBRID',
			$4,$5::jsonb,$6,$7::halfvec,'Qwen3-Embedding-4B','Qwen3-Embedding-4B',
			2560,'SUCCEEDED',now())`, tenantID, domainID, objectVersionID,
			fmt.Sprintf("type=metric | name=integration-%02d", index),
			fmt.Sprintf(`{"name":"integration-%02d"}`, index), objectHash,
			formatEmbeddingVector(vector)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func cleanupRecallAuditFixture(
	t *testing.T,
	adminPool *pgxpool.Pool,
	tenantID, domainID, actorID, releaseID string,
	createdDomain, syntheticPlatform bool,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := adminPool.Begin(ctx)
	if err != nil {
		t.Errorf("begin cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Errorf("set cleanup replication role: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.search_recall_audits
		WHERE tenant_id=$1 AND domain_id=$2`, tenantID, domainID); err != nil {
		t.Errorf("cleanup recall audits: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.search_query_samples
		WHERE tenant_id=$1 AND release_id=$2`, tenantID, releaseID); err != nil {
		t.Errorf("cleanup query samples: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.search_documents
		WHERE tenant_id=$1 AND domain_id=$2 AND object_version_id IN (
		  SELECT object_version_id FROM askdata.release_objects
		  WHERE tenant_id=$1 AND release_id=$3
		)`, tenantID, domainID, releaseID); err != nil {
		t.Errorf("cleanup search documents: %v", err)
		return
	}
	for _, cleanup := range []struct{ name, statement string }{
		{"projections", `DELETE FROM askdata.release_projections WHERE tenant_id=$1 AND release_id=$2`},
		{"objects", `DELETE FROM askdata.release_objects WHERE tenant_id=$1 AND release_id=$2`},
		{"release", `DELETE FROM askdata.releases WHERE tenant_id=$1 AND id=$2`},
	} {
		if _, err := tx.Exec(ctx, cleanup.statement, tenantID, releaseID); err != nil {
			t.Errorf("cleanup %s: %v", cleanup.name, err)
			return
		}
	}
	if createdDomain {
		if _, err := tx.Exec(ctx, `DELETE FROM askdata.domains WHERE id=$1 AND tenant_id=$2`, domainID, tenantID); err != nil {
			t.Errorf("cleanup domain: %v", err)
			return
		}
	}
	if syntheticPlatform {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
			WHERE tenant_id=$1 AND domain_id=$2 AND user_id=$3`, tenantID, domainID, actorID); err != nil {
			t.Errorf("cleanup platform membership: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.business_domains
			WHERE tenant_id=$1 AND id=$2`, tenantID, domainID); err != nil {
			t.Errorf("cleanup platform domain: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.users
			WHERE tenant_id=$1 AND id=$2`, tenantID, actorID); err != nil {
			t.Errorf("cleanup platform user: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.tenants WHERE id=$1`, tenantID); err != nil {
			t.Errorf("cleanup platform tenant: %v", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil && err != pgx.ErrTxClosed {
		t.Errorf("commit cleanup: %v", err)
	}
}
