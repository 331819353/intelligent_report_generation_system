package askdatahttp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresQuestionScopeResolutionUsesActiveReleaseRolesAndActorRLS(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil || appConfig.ConnConfig.User == "" {
		t.Fatalf("parse app database role: %v", err)
	}
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	root, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Rollback(ctx) }()
	fixture := createQuestionHTTPFixture(t, ctx, root)
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
		nested, err := root.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = nested.Rollback(ctx) }()
		access, ok := database.AccessContextFromContext(ctx)
		if !ok {
			return orchestrator.ErrInvalidAccessContext
		}
		if _, err := nested.Exec(ctx, `SELECT
			set_config('app.tenant_id',$1,true),
			set_config('app.access_mode','USER',true),
			set_config('app.user_id',$2,true),
			set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
			return err
		}
		if err := fn(nested); err != nil {
			return err
		}
		return nested.Commit(ctx)
	}
	service := &PostgresService{pool: appPool, scopeRunner: runner}
	identity := RequestIdentity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.actorID),
		DomainID: askdata.ID(fixture.domainID),
	}
	actorContext := database.WithAccessContext(ctx, fixture.actorID, fixture.domainID)
	activeScope, err := service.resolveActiveScope(actorContext, identity)
	if err != nil || activeScope.Release.ReleaseID != askdata.ID(fixture.releaseID) ||
		activeScope.Release.ContentHash != askdata.ContentHash(fixture.releaseHash) ||
		len(activeScope.RoleIDs) != 1 || activeScope.RoleIDs[0] != askdata.ID(fixture.actorRoleID) {
		t.Fatalf("active scope = %#v, %v", activeScope, err)
	}
	runScope, err := service.resolveRunScope(actorContext, identity, askdata.ID(fixture.runID))
	if err != nil || runScope.PolicyHash != activeScope.PolicyHash || runScope.Release != activeScope.Release {
		t.Fatalf("run scope = %#v, %v", runScope, err)
	}

	observerIdentity := RequestIdentity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.observerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	observerContext := database.WithAccessContext(ctx, fixture.observerID, fixture.domainID)
	if _, err := service.resolveRunScope(
		observerContext, observerIdentity, askdata.ID(fixture.runID),
	); !errors.Is(err, orchestrator.ErrRunNotFound) {
		t.Fatalf("cross-actor scope error = %v", err)
	}
}

type questionHTTPFixture struct {
	tenantID, actorID, observerID, domainID string
	actorRoleID, observerRoleID             string
	releaseID, releaseHash, runID           string
}

func createQuestionHTTPFixture(t *testing.T, ctx context.Context, tx pgx.Tx) questionHTTPFixture {
	t.Helper()
	fixture := questionHTTPFixture{releaseHash: string(askdata.HashBytes([]byte("http-release")))}
	suffix := uuid.NewString()[:8]
	if err := tx.QueryRow(ctx, `INSERT INTO platform.tenants(code,name)
		VALUES($1,$2) RETURNING id::text`, "qapi_"+suffix, "question api integration "+suffix,
	).Scan(&fixture.tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	insertUser := func(employee, email string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(
			tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,$5,'ACTIVE') RETURNING id::text`,
			fixture.tenantID, employee, email, employee, "integration-only-not-a-login-secret",
		).Scan(&id); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		return id
	}
	fixture.actorID = insertUser("QAPIA"+suffix, "qapi.a."+suffix+"@example.invalid")
	fixture.observerID = insertUser("QAPIB"+suffix, "qapi.b."+suffix+"@example.invalid")
	if err := tx.QueryRow(ctx, `INSERT INTO platform.business_domains(
		tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,true,$4) RETURNING id::text`, fixture.tenantID,
		"qapi_"+suffix, "question api "+suffix, fixture.actorID,
	).Scan(&fixture.domainID); err != nil {
		t.Fatalf("insert business domain: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,status,member_role,assigned_by
	) VALUES($1,$2,$3,'ACTIVE','MEMBER',$3),($1,$2,$4,'ACTIVE','MEMBER',$3)`,
		fixture.tenantID, fixture.domainID, fixture.actorID, fixture.observerID,
	); err != nil {
		t.Fatalf("insert memberships: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,$3,$4,$5)`, fixture.domainID, fixture.tenantID,
		"qapi_"+suffix, "question api "+suffix, fixture.actorID,
	); err != nil {
		t.Fatalf("insert askdata domain: %v", err)
	}
	insertRole := func(code string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name,status)
			VALUES($1,$2,$3,'ACTIVE') RETURNING id::text`, fixture.tenantID, code, code,
		).Scan(&id); err != nil {
			t.Fatalf("insert role: %v", err)
		}
		return id
	}
	fixture.actorRoleID = insertRole("QAPI_ACTOR_" + suffix)
	fixture.observerRoleID = insertRole("QAPI_OBSERVER_" + suffix)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by)
		VALUES($1,$2,$3,$2),($1,$4,$5,$2)`, fixture.tenantID, fixture.actorID,
		fixture.actorRoleID, fixture.observerID, fixture.observerRoleID,
	); err != nil {
		t.Fatalf("insert role assignments: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO askdata.releases(
		tenant_id,domain_id,semantic_version,content_hash,status,object_count,
		created_by,updated_by,activated_by,ready_at,activated_at
	) VALUES($1,$2,$3,$4,'ACTIVE',0,$5,$5,$5,now(),now()) RETURNING id::text`,
		fixture.tenantID, fixture.domainID, "qapi-"+suffix, fixture.releaseHash, fixture.actorID,
	).Scan(&fixture.releaseID); err != nil {
		t.Fatalf("insert active release: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','SYSTEM',true),
		set_config('app.user_id','',true),
		set_config('app.domain_id','',true)`, fixture.tenantID); err != nil {
		t.Fatalf("set fixture system scope: %v", err)
	}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(fixture.tenantID), askdata.ID(fixture.actorID),
		[]askdata.ID{askdata.ID(fixture.domainID)}, []askdata.ID{askdata.ID(fixture.actorRoleID)},
		askdata.ReleaseRef{ReleaseID: askdata.ID(fixture.releaseID), ContentHash: askdata.ContentHash(fixture.releaseHash)},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runID = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,conversation_id,trace_id,
		idempotency_key_hash,question_hash,policy_scope_hash,release_id,release_content_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		fixture.runID, fixture.tenantID, fixture.domainID, fixture.actorID,
		uuid.NewString(), uuid.NewString(), askdata.HashBytes([]byte("idempotency")),
		askdata.HashBytes([]byte("question")), scope.PolicyHash, fixture.releaseID, fixture.releaseHash,
	); err != nil {
		t.Fatalf("insert question run: %v", err)
	}
	return fixture
}
