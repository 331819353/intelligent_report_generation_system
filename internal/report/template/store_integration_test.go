package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresTemplateCompositionSeedStateAndReferenceProtection(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()

	if err := SeedBundledComponents(ctx, appPool); err != nil {
		t.Fatalf("SeedBundledComponents() error = %v", err)
	}
	if err := SeedBundledComponents(ctx, appPool); err != nil {
		t.Fatalf("idempotent SeedBundledComponents() error = %v", err)
	}
	assertBundledManifestRows(t, ctx, adminPool)

	fixture := createTemplateFixture(t, ctx, adminPool)
	defer cleanupTemplateFixture(t, adminPool, fixture)
	actorContext := database.WithAccessContext(ctx, fixture.actorID, "")
	if err := database.WithTenantTx(actorContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.component_template_versions AS version
			SET status='DEPRECATED'
			FROM platform.component_templates AS template
			WHERE template.id=version.component_template_id
			  AND template.tenant_id IS NULL AND template.type='metric-card'`)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 0 {
			return errors.New("tenant actor mutated a platform component manifest")
		}
		return nil
	}); err != nil {
		t.Fatalf("platform component manifests must be tenant-read-only: %v", err)
	}
	_, invalidSemverErr := adminPool.Exec(ctx,
		`UPDATE platform.report_template_versions SET version='01.0.0' WHERE id=$1`,
		fixture.reportTemplateVersionID,
	)
	assertPostgresCode(t, invalidSemverErr, "23514")
	identity := TemplateIdentity{TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.actorID)}
	store := NewPostgresTemplateStore(appPool)
	resolved, err := store.ResolveTemplate(ctx, identity, askdata.ID(fixture.reportTemplateID), "1.10.0")
	if err != nil {
		t.Fatalf("ResolveTemplate() error = %v", err)
	}
	assertResolvedFixture(t, resolved, fixture)
	byID, err := store.ResolveTemplateVersion(ctx, identity, askdata.ID(fixture.reportTemplateVersionID))
	if err != nil {
		t.Fatalf("ResolveTemplateVersion() error = %v", err)
	}
	assertResolvedFixture(t, byID, fixture)
	crossIdentity := TemplateIdentity{
		TenantID: askdata.ID(fixture.otherTenantID), ActorID: askdata.ID(fixture.otherActorID),
	}
	if _, err := store.ResolveTemplateVersion(ctx, crossIdentity, askdata.ID(fixture.reportTemplateVersionID)); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("cross-tenant ResolveTemplateVersion() error = %v", err)
	}

	if err := database.WithTenantTx(actorContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.component_template_versions SET status='DEPRECATED' WHERE id=$1`, fixture.componentVersionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE platform.component_template_versions SET status='RETAINED' WHERE id=$1`, fixture.componentVersionID)
		return err
	}); err != nil {
		t.Fatalf("ACTIVE -> DEPRECATED -> RETAINED: %v", err)
	}
	assertPostgresCode(t, database.WithTenantTx(actorContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.component_template_versions SET status='ACTIVE' WHERE id=$1`, fixture.componentVersionID)
		return err
	}), "55000")
	assertPostgresCode(t, database.WithTenantTx(actorContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.component_template_versions SET status='RETAINED' WHERE id=$1`, fixture.activeComponentVersionID)
		return err
	}), "23514")
	assertPostgresCode(t, database.WithTenantTx(actorContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM platform.component_template_versions WHERE id=$1`, fixture.componentVersionID)
		return err
	}), "55000")
}

type templateFixture struct {
	tenantID, actorID, otherTenantID, otherActorID string
	reportTemplateID, reportTemplateVersionID      string
	structureVersionID, layoutVersionID            string
	themeVersionID, narrativeVersionID             string
	componentVersionID, activeComponentVersionID   string
	reportID, reportVersionID                      string
	definitions                                    map[string][]byte
	hashes                                         map[string]string
}

func createTemplateFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) templateFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := templateFixture{
		tenantID: uuid.NewString(), actorID: uuid.NewString(), otherTenantID: uuid.NewString(), otherActorID: uuid.NewString(),
		reportTemplateID: uuid.NewString(), reportTemplateVersionID: uuid.NewString(),
		structureVersionID: uuid.NewString(), layoutVersionID: uuid.NewString(),
		themeVersionID: uuid.NewString(), narrativeVersionID: uuid.NewString(),
		componentVersionID: uuid.NewString(), activeComponentVersionID: uuid.NewString(),
		reportID: uuid.NewString(), reportVersionID: uuid.NewString(),
		definitions: map[string][]byte{}, hashes: map[string]string{},
	}
	for name, raw := range map[string][]byte{
		"report": []byte(`{"kind":"report"}`), "structure": []byte(`{"kind":"structure"}`),
		"layout": []byte(`{"kind":"layout"}`), "theme": []byte(`{"kind":"theme"}`),
		"narrative": []byte(`{"kind":"narrative"}`),
	} {
		canonical, err := canonicalTemplateJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		fixture.definitions[name] = canonical
		fixture.hashes[name] = string(askdata.HashBytes(canonical))
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tenant := range []struct{ id, code string }{
		{fixture.tenantID, "rpttpl_" + suffix}, {fixture.otherTenantID, "rpttpl_other_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Report template integration')`, tenant.id, tenant.code); err != nil {
			t.Fatal(err)
		}
	}
	for _, actor := range []struct{ id, tenantID, prefix string }{
		{fixture.actorID, fixture.tenantID, "tplactor"}, {fixture.otherActorID, fixture.otherTenantID, "tplother"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,'Report template integration','integration-only-not-a-login-secret','ACTIVE')`,
			actor.id, actor.tenantID, strings.ToUpper(actor.prefix)+suffix,
			actor.prefix+"."+suffix+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	type childTemplate struct {
		table, versionTable, ownerColumn, versionID, kind string
	}
	children := []childTemplate{
		{"report_structure_templates", "report_structure_template_versions", "template_id", fixture.structureVersionID, "structure"},
		{"report_layout_templates", "report_layout_template_versions", "template_id", fixture.layoutVersionID, "layout"},
		{"report_themes", "report_theme_versions", "theme_id", fixture.themeVersionID, "theme"},
		{"report_narrative_templates", "report_narrative_template_versions", "template_id", fixture.narrativeVersionID, "narrative"},
	}
	for index, child := range children {
		templateID := uuid.NewString()
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO platform.%s(
			id,tenant_id,code,name,owner_user_id
		) VALUES($1,$2,$3,'Template integration',$4)`, child.table), templateID, fixture.tenantID,
			fmt.Sprintf("tpl_%d_%s", index, suffix), fixture.actorID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO platform.%s(
			id,tenant_id,%s,version,status,definition_json,content_hash
		) VALUES($1,$2,$3,'1.10.0','PUBLISHED',$4,$5)`, child.versionTable, child.ownerColumn),
			child.versionID, fixture.tenantID, templateID, fixture.definitions[child.kind], fixture.hashes[child.kind]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.report_templates(
		id,tenant_id,code,name,category,owner_user_id
	) VALUES($1,$2,$3,'Template integration','ANALYSIS',$4)`, fixture.reportTemplateID,
		fixture.tenantID, "report_tpl_"+suffix, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.report_template_versions(
		id,tenant_id,report_template_id,version,status,structure_template_version_id,
		layout_template_version_id,theme_version_id,narrative_template_version_id,definition_json,content_hash
	) VALUES($1,$2,$3,'1.10.0','PUBLISHED',$4,$5,$6,$7,$8,$9)`, fixture.reportTemplateVersionID,
		fixture.tenantID, fixture.reportTemplateID, fixture.structureVersionID, fixture.layoutVersionID,
		fixture.themeVersionID, fixture.narrativeVersionID, fixture.definitions["report"], fixture.hashes["report"]); err != nil {
		t.Fatal(err)
	}
	componentTemplateID := uuid.NewString()
	componentType := "test-widget-" + suffix
	manifest := []byte(fmt.Sprintf(`{"type":%q,"version":"1.0.0"}`, componentType))
	manifestHash := string(askdata.HashBytes(manifest))
	if _, err := tx.Exec(ctx, `INSERT INTO platform.component_templates(id,tenant_id,type) VALUES($1,$2,$3)`,
		componentTemplateID, fixture.tenantID, componentType); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.component_template_versions(
		id,component_template_id,version,status,manifest_json,content_hash
	) VALUES($1,$2,'1.0.0','ACTIVE',$3,$4),($5,$2,'1.1.0','ACTIVE',$3,$4)`,
		fixture.componentVersionID, componentTemplateID, manifest, manifestHash, fixture.activeComponentVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.reports(
		id,tenant_id,code,name,report_type,owner_user_id,status,created_by
	) VALUES($1,$2,$3,'Template reference','REPORT',$4,'ACTIVE',$4)`, fixture.reportID,
		fixture.tenantID, "template_ref_"+suffix, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.report_versions(
		id,tenant_id,report_id,version_no,source_revision_no,definition_json,definition_bytes,
		definition_hash,schema_version,object_uri,published_by,artifact_state
	) VALUES($1,$2,$3,1,0,'{}',2,$4,'1.0','s3://template-reference/version.json',$5,'READY')`,
		fixture.reportVersionID, fixture.tenantID, fixture.reportID, strings.Repeat("a", 64), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.report_version_dependencies(
		report_version_id,report_id,tenant_id,dependency_type,dependency_id,component_ids
	) VALUES($1,$2,$3,'COMPONENT_TEMPLATE',$4,'{}')`, fixture.reportVersionID, fixture.reportID,
		fixture.tenantID, componentType+"@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertBundledManifestRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT version.manifest_json,version.content_hash
		FROM platform.component_templates AS template
		JOIN platform.component_template_versions AS version ON version.component_template_id=template.id
		WHERE template.tenant_id IS NULL AND version.version='1.0.0' ORDER BY template.type`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var raw []byte
		var hash string
		if err := rows.Scan(&raw, &hash); err != nil {
			t.Fatal(err)
		}
		manifest, err := DecodeManifest(raw)
		if err != nil {
			t.Fatalf("seeded manifest is unusable: %v", err)
		}
		canonical, _ := json.Marshal(manifest)
		if string(askdata.HashBytes(canonical)) != hash {
			t.Fatalf("seeded manifest %s hash mismatch", manifest.Type)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != BundledManifestCount {
		t.Fatalf("seeded component manifests = %d, want %d", count, BundledManifestCount)
	}
}

func assertResolvedFixture(t *testing.T, resolved ResolvedTemplate, fixture templateFixture) {
	t.Helper()
	if resolved.ReportTemplateVersionID != askdata.ID(fixture.reportTemplateVersionID) || resolved.Version != "1.10.0" ||
		resolved.StructureTemplateVersionID != askdata.ID(fixture.structureVersionID) ||
		resolved.LayoutTemplateVersionID != askdata.ID(fixture.layoutVersionID) ||
		resolved.ThemeVersionID != askdata.ID(fixture.themeVersionID) ||
		resolved.NarrativeTemplateVersionID != askdata.ID(fixture.narrativeVersionID) {
		t.Fatalf("resolved template composition = %#v", resolved)
	}
	for name, raw := range map[string]json.RawMessage{
		"report": resolved.ReportDefinition, "structure": resolved.Structure, "layout": resolved.Layout,
		"theme": resolved.Theme, "narrative": resolved.Narrative,
	} {
		if string(raw) != string(fixture.definitions[name]) {
			t.Fatalf("resolved %s = %s", name, raw)
		}
	}
}

func cleanupTemplateFixture(t *testing.T, pool *pgxpool.Pool, fixture templateFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin template fixture cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable template cleanup triggers: %v", err)
		return
	}
	tenantIDs := []string{fixture.tenantID, fixture.otherTenantID}
	for _, table := range []string{
		"platform.report_version_dependencies", "platform.report_versions", "platform.reports",
		"platform.report_template_versions", "platform.report_templates",
		"platform.report_narrative_template_versions", "platform.report_theme_versions",
		"platform.report_layout_template_versions", "platform.report_structure_template_versions",
		"platform.report_narrative_templates", "platform.report_themes", "platform.report_layout_templates",
		"platform.report_structure_templates", "platform.component_template_versions",
		"platform.component_templates", "platform.users", "platform.tenants",
	} {
		var relation *string
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&relation); err != nil || relation == nil {
			continue
		}
		key := "tenant_id"
		if table == "platform.tenants" {
			key = "id"
		}
		if table == "platform.component_template_versions" {
			if _, err := tx.Exec(ctx, `DELETE FROM platform.component_template_versions AS version
				USING platform.component_templates AS template
				WHERE version.component_template_id=template.id AND template.tenant_id=ANY($1::uuid[])`, tenantIDs); err != nil {
				t.Errorf("cleanup component versions: %v", err)
				return
			}
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=ANY($1::uuid[])`, table, key), tenantIDs); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit template fixture cleanup: %v", err)
	}
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != code {
		t.Fatalf("database error = %v, want SQLSTATE %s", err, code)
	}
}
