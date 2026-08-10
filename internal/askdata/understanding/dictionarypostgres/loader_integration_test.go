package dictionarypostgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresLoaderReadsOnlyApprovedTermPinnedByReadyRelease(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set AskData admin and app integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	app, err := database.Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	tenantID, domainID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	roleID, termID, termVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	targetID, releaseID := uuid.NewString(), uuid.NewString()
	unreleasedTermID, unreleasedVersionID := uuid.NewString(), uuid.NewString()
	pendingTermID, pendingVersionID := uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	termHash := askdata.HashBytes([]byte("dictionary-postgres-term:" + suffix))
	releaseHash := askdata.HashBytes([]byte("dictionary-postgres-release:" + suffix))
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Dictionary loader integration')`, tenantID, "dict_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no
	) VALUES($1,$2,$3,'Dictionary loader','not-a-login-hash',$4)`, actorID, tenantID,
		"dict_"+suffix+"@example.invalid", "DICT"+strings.ToUpper(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,'Dictionary loader',true,$4)`, domainID, tenantID,
		"dict_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,member_role,assigned_by
	) VALUES($1,$2,$3,'DOMAIN_ADMIN',$3)`, tenantID, domainID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.roles(
		id,tenant_id,code,name
	) VALUES($1,$2,$3,'Dictionary role')`, roleID, tenantID, "dict_role_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,$3,'Dictionary loader',$4)`, domainID, tenantID, "dict_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.business_terms(
		id,tenant_id,domain_id,term,term_type,created_by
	) VALUES($1,$2,$3,'销售额','METRIC',$4)`, termID, tenantID, domainID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.business_term_versions(
		id,tenant_id,domain_id,business_term_id,version_no,status,
		target_object_type,target_version_id,target_code,match_mode,priority,
		negative_contexts,applicable_role_ids,source,review_status,reviewed_by,reviewed_at,
		code,name,definition,aliases,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,'CERTIFIED','OPERATOR',$5,'SUM','EXACT',100,
		ARRAY['物流']::text[],ARRAY[$6]::uuid[],'MANUAL','APPROVED',$7,now(),
		'sales_total','销售额','认证词条','{}'::text[],$8,$7)`, termVersionID, tenantID,
		domainID, termID, targetID, roleID, actorID, termHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.business_terms(
		id,tenant_id,domain_id,term,term_type,created_by
	) VALUES($1,$2,$3,'毛利率','METRIC',$4),($5,$2,$3,'一次澄清','METRIC',$4)`,
		unreleasedTermID, tenantID, domainID, actorID, pendingTermID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.business_term_versions(
		id,tenant_id,domain_id,business_term_id,version_no,status,
		target_object_type,target_version_id,target_code,match_mode,priority,
		source,review_status,reviewed_by,reviewed_at,code,name,definition,aliases,content_hash,owner_id
	) VALUES(
		$1,$2,$3,$4,1,'CERTIFIED','OPERATOR',$5,'AVG','EXACT',100,
		'MANUAL','APPROVED',$6,now(),'gross_margin','毛利率','未进入 Release','{}'::text[],$7,$6
	),(
		$8,$2,$3,$9,1,'DRAFT','OPERATOR',$10,'SUM','EXACT',100,
		'FEEDBACK','PENDING',NULL,NULL,'clarification_once','一次澄清','待审批候选','{}'::text[],$11,$6
	)`, unreleasedVersionID, tenantID, domainID, unreleasedTermID, uuid.NewString(), actorID,
		askdata.HashBytes([]byte("unreleased:"+suffix)), pendingVersionID, pendingTermID,
		uuid.NewString(), askdata.HashBytes([]byte("pending:"+suffix))); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,'DRAFT',1,$6,$6)`, releaseID, tenantID, domainID,
		"dict-"+suffix, releaseHash, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,content_hash,contract_json
	) VALUES($1,$2,$3,'BUSINESS_TERM',$4,$5,$6,'{}'::jsonb)`, tenantID, domainID,
		releaseID, termID, termVersionID, termHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.releases SET status='READY',ready_at=now()
		WHERE id=$1`, releaseID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	defer cleanupDictionaryFixture(t, admin, tenantID)

	release := askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: releaseHash}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{askdata.ID(roleID)}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	loader := NewLoader(app)
	snapshot, err := loader.LoadDictionary(requestContext, scope, askdata.ID(domainID))
	if err != nil || len(snapshot.Terms) != 1 || snapshot.Terms[0].TermVersionID != askdata.ID(termVersionID) ||
		snapshot.Terms[0].TargetCode != "SUM" || snapshot.Terms[0].ContentHash != termHash ||
		len(snapshot.Terms[0].ApplicableRoleIDs) != 1 {
		t.Fatalf("LoadDictionary() = %#v/%v", snapshot, err)
	}
	wrongRelease := release
	wrongRelease.ContentHash = askdata.HashBytes([]byte("wrong-release-hash"))
	wrongScope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{askdata.ID(roleID)}, wrongRelease,
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongSnapshot, err := loader.LoadDictionary(requestContext, wrongScope, askdata.ID(domainID))
	if err != nil || len(wrongSnapshot.Terms) != 0 {
		t.Fatalf("wrong release hash snapshot = %#v/%v", wrongSnapshot, err)
	}
	matcher, err := understanding.NewDictionaryMatcher(loader, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := matcher.Match(requestContext, understanding.DictionaryMatchRequest{
		Scope: scope, Question: "销售额情况",
	})
	if err != nil || len(matched.Hits) != 1 || matched.Hits[0].TermVersionID != askdata.ID(termVersionID) {
		t.Fatalf("Postgres-backed Match() = %#v/%v", matched, err)
	}
	blocked, err := matcher.Match(requestContext, understanding.DictionaryMatchRequest{
		Scope: scope, Question: "销售额物流情况",
	})
	if err != nil || len(blocked.Hits) != 0 || len(blocked.Dropped) == 0 ||
		blocked.Dropped[0].Reason != understanding.DictionaryDropNegativeContext {
		t.Fatalf("Postgres-backed negative context = %#v/%v", blocked, err)
	}
}

func cleanupDictionaryFixture(t *testing.T, admin *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Errorf("begin dictionary cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable dictionary cleanup triggers: %v", err)
		return
	}
	for _, statement := range []string{
		`DELETE FROM askdata.release_objects WHERE tenant_id=$1`,
		`DELETE FROM askdata.releases WHERE tenant_id=$1`,
		`DELETE FROM askdata.business_term_versions WHERE tenant_id=$1`,
		`DELETE FROM askdata.business_terms WHERE tenant_id=$1`,
		`DELETE FROM askdata.domains WHERE tenant_id=$1`,
		`DELETE FROM platform.domain_memberships WHERE tenant_id=$1`,
		`DELETE FROM platform.roles WHERE tenant_id=$1`,
		`DELETE FROM platform.business_domains WHERE tenant_id=$1`,
		`DELETE FROM platform.users WHERE tenant_id=$1`,
		`DELETE FROM platform.tenants WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, tenantID); err != nil {
			t.Errorf("dictionary cleanup %q: %v", statement, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit dictionary cleanup: %v", err)
	}
}
