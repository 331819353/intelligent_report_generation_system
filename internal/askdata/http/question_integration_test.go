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

type historyRunStore struct {
	conversationID askdata.ID
	now            time.Time
}

func (store historyRunStore) CreateRun(context.Context, orchestrator.CreateRunRequest) (orchestrator.CreateResult, error) {
	return orchestrator.CreateResult{}, errors.New("history test does not create runs through the stub")
}

func (store historyRunStore) Resume(_ context.Context, request orchestrator.ResumeRequest) (orchestrator.ReplaySnapshot, error) {
	return orchestrator.ReplaySnapshot{Run: orchestrator.Run{
		ID: request.RunID, TenantID: request.Scope.TenantID, DomainID: request.DomainID,
		ActorID: request.Scope.ActorID, ConversationID: store.conversationID,
		Release: request.Scope.Release, PolicyScopeHash: request.Scope.PolicyHash,
		State: orchestrator.StateReceived, RecordVersion: 1, CreatedAt: store.now, UpdatedAt: store.now,
	}}, nil
}

func TestPostgresConversationHistoryPaginationMutationsAndIsolation(t *testing.T) {
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
	secondRunID, secondConversationID, thirdRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = root.Exec(ctx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,conversation_id,trace_id,idempotency_key_hash,question_hash,
		policy_scope_hash,release_id,release_content_hash)
		SELECT $1,tenant_id,domain_id,actor_id,conversation_id,$2,$3,question_hash,policy_scope_hash,release_id,release_content_hash
		FROM askdata.question_runs WHERE tenant_id=$4 AND id=$5`, secondRunID, uuid.NewString(),
		askdata.HashBytes([]byte("history-second-run")), fixture.tenantID, fixture.runID); err != nil {
		t.Fatalf("insert second run: %v", err)
	}
	if _, err = root.Exec(ctx, `INSERT INTO askdata.conversations(id,tenant_id,domain_id,actor_id)
		VALUES($1,$2,$3,$4)`, secondConversationID, fixture.tenantID, fixture.domainID, fixture.actorID); err != nil {
		t.Fatalf("insert second conversation: %v", err)
	}
	if _, err = root.Exec(ctx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,conversation_id,trace_id,idempotency_key_hash,question_hash,
		policy_scope_hash,release_id,release_content_hash)
		SELECT $1,tenant_id,domain_id,actor_id,$2,$3,$4,question_hash,policy_scope_hash,release_id,release_content_hash
		FROM askdata.question_runs WHERE tenant_id=$5 AND id=$6`, thirdRunID, secondConversationID,
		uuid.NewString(), askdata.HashBytes([]byte("history-third-run")), fixture.tenantID, fixture.runID); err != nil {
		t.Fatalf("insert third run: %v", err)
	}
	if _, err = root.Exec(ctx, `UPDATE askdata.conversations SET record_version=record_version+1,updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND id IN($2,$3)`, fixture.tenantID, fixture.conversationID, secondConversationID); err != nil {
		t.Fatalf("refresh conversations: %v", err)
	}
	if _, err = root.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()); err != nil {
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
		if _, err = nested.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),
			set_config('app.access_mode','USER',true),set_config('app.user_id',$2,true),
			set_config('app.domain_id',$3,true)`, tenantID, access.UserID, access.DomainID); err != nil {
			return err
		}
		if err = fn(nested); err != nil {
			return err
		}
		return nested.Commit(ctx)
	}
	now := time.Now().UTC()
	service := &PostgresService{pool: appPool, scopeRunner: runner, runs: historyRunStore{conversationID: askdata.ID(fixture.conversationID), now: now}}
	identity := RequestIdentity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.actorID)}
	actorContext := database.WithAccessContext(ctx, fixture.actorID, fixture.domainID)
	summary, err := service.conversationByID(actorContext, identity, askdata.ID(fixture.conversationID))
	if err != nil || summary.RunCount != 2 {
		t.Fatalf("conversation summary = %#v, %v", summary, err)
	}
	summary, err = service.SetConversationPinned(actorContext, identity, summary.ConversationID, ConversationMutationInput{ExpectedVersion: summary.RecordVersion}, true)
	if err != nil || !summary.Pinned {
		t.Fatalf("pin conversation = %#v, %v", summary, err)
	}
	first, err := service.ListConversations(actorContext, identity, "", false, 1, "")
	if err != nil || len(first.Items) != 1 || first.Items[0].ConversationID != summary.ConversationID || first.NextCursor == "" {
		t.Fatalf("first history page = %#v, %v", first, err)
	}
	second, err := service.ListConversations(actorContext, identity, "", false, 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].ConversationID == first.Items[0].ConversationID {
		t.Fatalf("second history page = %#v, %v", second, err)
	}
	if _, err = service.ListConversations(actorContext, identity, "", false, 1, "not-a-cursor"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid history cursor error = %v", err)
	}
	detail, err := service.GetConversation(actorContext, identity, summary.ConversationID, 1, "")
	if err != nil || len(detail.Runs) != 1 || detail.NextRunCursor == "" {
		t.Fatalf("first run page = %#v, %v", detail, err)
	}
	nextDetail, err := service.GetConversation(actorContext, identity, summary.ConversationID, 1, detail.NextRunCursor)
	if err != nil || len(nextDetail.Runs) != 1 || nextDetail.Runs[0].RunID == detail.Runs[0].RunID {
		t.Fatalf("second run page = %#v, %v", nextDetail, err)
	}
	summary, err = service.SetConversationPinned(actorContext, identity, summary.ConversationID, ConversationMutationInput{ExpectedVersion: summary.RecordVersion}, false)
	if err != nil || summary.Pinned {
		t.Fatalf("unpin conversation = %#v, %v", summary, err)
	}
	summary, err = service.SetConversationArchived(actorContext, identity, summary.ConversationID, ConversationMutationInput{ExpectedVersion: summary.RecordVersion}, true)
	if err != nil || !summary.Archived {
		t.Fatalf("archive conversation = %#v, %v", summary, err)
	}
	archived, err := service.ListConversations(actorContext, identity, "", true, 10, "")
	if err != nil || len(archived.Items) != 1 || archived.Items[0].ConversationID != summary.ConversationID {
		t.Fatalf("archived history = %#v, %v", archived, err)
	}
	if _, err = service.SetConversationArchived(actorContext, identity, summary.ConversationID, ConversationMutationInput{ExpectedVersion: summary.RecordVersion - 1}, false); !errors.Is(err, orchestrator.ErrVersionConflict) {
		t.Fatalf("stale restore error = %v", err)
	}
	observerIdentity := RequestIdentity{TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: askdata.ID(fixture.observerID)}
	observerContext := database.WithAccessContext(ctx, fixture.observerID, fixture.domainID)
	observerPage, err := service.ListConversations(observerContext, observerIdentity, "", false, 10, "")
	if err != nil || len(observerPage.Items) != 0 {
		t.Fatalf("cross-actor history = %#v, %v", observerPage, err)
	}
}

type questionHTTPFixture struct {
	tenantID, actorID, observerID, domainID string
	actorRoleID, observerRoleID             string
	releaseID, releaseHash, conversationID  string
	runID                                   string
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
	fixture.conversationID = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.conversations(id,tenant_id,domain_id,actor_id)
		VALUES($1,$2,$3,$4)`, fixture.conversationID, fixture.tenantID, fixture.domainID, fixture.actorID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	fixture.runID = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,conversation_id,trace_id,
		idempotency_key_hash,question_hash,policy_scope_hash,release_id,release_content_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		fixture.runID, fixture.tenantID, fixture.domainID, fixture.actorID,
		fixture.conversationID, uuid.NewString(), askdata.HashBytes([]byte("idempotency")),
		askdata.HashBytes([]byte("question")), scope.PolicyHash, fixture.releaseID, fixture.releaseHash,
	); err != nil {
		t.Fatalf("insert question run: %v", err)
	}
	return fixture
}
