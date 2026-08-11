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

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestKPIBundleAdminRoundTripAndReferenceValidation(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, domainID, foreignDomainID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(tenantID, "-", "")[:12]
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'KPI bundle integration')`,
		tenantID, "kpi_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no
	) VALUES($1,$2,$3,'KPI bundle owner','not-a-login-hash',$4)`, actorID, tenantID,
		"kpi_"+suffix+"@example.invalid", "KPI"+strings.ToUpper(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,'KPI bundle',true,$4)`, domainID, tenantID, "kpi_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,$3,'KPI bundle',$4)`, domainID, tenantID, "kpi_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	certifiedMetricID, draftMetricID, foreignMetricID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, fixture := range []struct {
		id, domain, status string
	}{
		{certifiedMetricID, domainID, "CERTIFIED"},
		{draftMetricID, domainID, "DRAFT"},
		{foreignMetricID, foreignDomainID, "CERTIFIED"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
			id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
			formula_ast,default_filters_ast,unit,time_grain,additivity,
			zero_denominator_policy,display_precision,additivity_suggestion,
			additivity_suggestion_rule_id,null_policy,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,1,$5,'{"type":"TRUE"}'::jsonb,'{"type":"TRUE"}'::jsonb,
			'COUNT','NONE','FULLY_ADDITIVE','NULL',2,'FULLY_ADDITIVE','TEST_FIXTURE','PRESERVE',$6,$7,$8)`,
			fixture.id, tenantID, fixture.domain, uuid.NewString(), uuid.NewString(), fixture.status,
			strings.Repeat("a", 64), actorID); err != nil {
			t.Fatal(err)
		}
	}
	compatibleDimensionID, incompatibleDimensionID := uuid.NewString(), uuid.NewString()
	for _, fixture := range []struct {
		dimensionID string
		compatible  bool
	}{
		{compatibleDimensionID, true}, {incompatibleDimensionID, false},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_dimension_versions(
			id,tenant_id,domain_id,metric_dimension_id,version_no,metric_version_id,
			dimension_version_id,compatible,role,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,1,$5,$6,$7,'GROUP_BY','CERTIFIED',$8,$9)`, uuid.NewString(),
			tenantID, domainID, uuid.NewString(), certifiedMetricID, fixture.dimensionID,
			fixture.compatible, strings.Repeat("b", 64), actorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}

	bundle := validKPIBundleFixture("admin_" + suffix)
	bundle.TenantID, bundle.DomainID, bundle.OwnerID = tenantID, domainID, actorID
	bundle.Items[0].MetricVersionID = certifiedMetricID
	bundle.Items[0].GroupByDimensionVersionIDs = []string{compatibleDimensionID}
	bundle.DefaultDimensionVersionIDs = []string{compatibleDimensionID}
	bundle.ContentHash = KPIBundleContentHash(bundle)
	if err := validateKPIBundleReferencesTx(ctx, tx, bundle); err != nil {
		t.Fatalf("valid references = %v", err)
	}
	for _, test := range []struct {
		name, metricID, dimensionID, code string
	}{
		{"draft metric", draftMetricID, compatibleDimensionID, "KPI_BUNDLE_METRIC_INVALID"},
		{"cross domain metric", foreignMetricID, compatibleDimensionID, "KPI_BUNDLE_METRIC_INVALID"},
		{"incompatible dimension", certifiedMetricID, incompatibleDimensionID, "KPI_BUNDLE_DIMENSION_INCOMPATIBLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := bundle
			invalid.Items = append([]KPIBundleItem(nil), bundle.Items...)
			invalid.Items[0].MetricVersionID = test.metricID
			invalid.Items[0].GroupByDimensionVersionIDs = []string{test.dimensionID}
			if err := validateKPIBundleReferencesTx(ctx, tx, invalid); err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("reference error = %v", err)
			}
		})
	}

	input := &KPIBundleDraftInput{
		VersionedDraftInput: VersionedDraftInput{VersionNo: 1},
		Code:                bundle.Code, Name: bundle.Name, Items: bundle.Items,
		DefaultDimensionVersionIDs: bundle.DefaultDimensionVersionIDs,
		DefaultTimeExpression:      bundle.DefaultTimeExpression,
		DefaultChartTypes:          bundle.DefaultChartTypes, RoleMapping: bundle.RoleMapping,
		ApplicableQuestionPatterns: bundle.ApplicableQuestionPatterns,
	}
	created, err := createDraftTx(ctx, tx, AdminScope{TenantID: tenantID, DomainID: domainID, ActorID: actorID},
		AdminResourceKPIBundle, input, uuid.NewString())
	if err != nil {
		t.Fatalf("create KPI bundle = %v", err)
	}
	loadedValue, err := getObjectTx(ctx, tx, domainID, AdminResourceKPIBundle, created.ResourceID, StatusDraft, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded := loadedValue.(KPIBundle)
	if loaded.Code != input.Code || loaded.ContentHash != created.ContentHash || len(loaded.Items) != 1 {
		t.Fatalf("loaded KPI bundle = %#v", loaded)
	}
	if err := validateKPIBundleCertification(ctx, tx, loaded.ID); err != nil {
		t.Fatalf("certification validation = %v", err)
	}
	page, err := listObjectsTx(ctx, tx, domainID, AdminResourceKPIBundle, StatusDraft, metricCursor{}, 200)
	if err != nil || len(page.Items.([]KPIBundle)) != 1 {
		t.Fatalf("list KPI bundles = %#v/%v", page, err)
	}
	input.ExpectedUpdatedAt = &loaded.UpdatedAt
	input.DefaultTimeExpression = "PREVIOUS_MONTH"
	updated, err := updateDraftTx(ctx, tx, AdminScope{TenantID: tenantID, DomainID: domainID, ActorID: actorID},
		AdminResourceKPIBundle, created.ResourceID, input)
	if err != nil || updated.ContentHash == created.ContentHash {
		t.Fatalf("update KPI bundle = %#v/%v", updated, err)
	}
	if _, err := deleteDraftTx(ctx, tx, domainID, AdminResourceKPIBundle, created.ResourceID,
		DeleteDraftInput{ExpectedUpdatedAt: updated.UpdatedAt}); err != nil {
		t.Fatalf("delete KPI bundle = %v", err)
	}
	if _, err := getObjectTx(ctx, tx, domainID, AdminResourceKPIBundle, created.ResourceID, StatusDraft, false); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("get deleted KPI bundle = %v", err)
	}
}

func TestPostgresKPIBundleMatcherPinsAndRollsBackReleaseVersion(t *testing.T) {
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

	tenantID, domainID, actorID, roleID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	metricID, metricVersionID, dimensionVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	bundleID, releaseV1ID, releaseV2ID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(tenantID, "-", "")[:12]
	metricHash := askdata.HashBytes([]byte("kpi-metric:" + suffix))
	dimensionHash := askdata.HashBytes([]byte("kpi-dimension:" + suffix))
	releaseV1Hash := askdata.HashBytes([]byte("kpi-release-v1:" + suffix))
	releaseV2Hash := askdata.HashBytes([]byte("kpi-release-v2:" + suffix))
	bundleV1 := validKPIBundleFixture("release_overview")
	bundleV1.ID, bundleV1.ObjectID, bundleV1.TenantID, bundleV1.DomainID = uuid.NewString(), bundleID, tenantID, domainID
	bundleV1.OwnerID, bundleV1.Status = actorID, VersionStatusCertified
	bundleV1.Items[0].MetricVersionID = metricVersionID
	bundleV1.Items[0].GroupByDimensionVersionIDs = []string{dimensionVersionID}
	bundleV1.DefaultDimensionVersionIDs = []string{dimensionVersionID}
	bundleV1.ApplicableQuestionPatterns = []string{"旧版经营概览"}
	bundleV1.ContentHash = KPIBundleContentHash(bundleV1)
	bundleV2 := bundleV1
	bundleV2.ID, bundleV2.VersionNo = uuid.NewString(), 2
	bundleV2.ApplicableQuestionPatterns = []string{"新版经营概览"}
	bundleV2.ContentHash = KPIBundleContentHash(bundleV2)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'KPI matcher integration')`,
		tenantID, "kpim_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no
	) VALUES($1,$2,$3,'KPI matcher','not-a-login-hash',$4)`, actorID, tenantID,
		"kpim_"+suffix+"@example.invalid", "KPIM"+strings.ToUpper(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,'KPI matcher',true,$4)`, domainID, tenantID, "kpim_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,member_role,assigned_by
	) VALUES($1,$2,$3,'DOMAIN_ADMIN',$3)`, tenantID, domainID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.roles(id,tenant_id,code,name)
		VALUES($1,$2,$3,'KPI matcher role')`, roleID, tenantID, "kpim_role_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,$3,'KPI matcher',$4)`, domainID, tenantID, "kpim_"+suffix, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.metrics(
		id,tenant_id,domain_id,code,name,status,owner_id,version
	) VALUES($1,$2,$3,'sales_total','销售额','ACTIVE',$4,1)`, metricID, tenantID, domainID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
		id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
		formula_ast,default_filters_ast,unit,time_grain,additivity,
		zero_denominator_policy,display_precision,additivity_suggestion,
		additivity_suggestion_rule_id,null_policy,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,'{"type":"TRUE"}'::jsonb,'{"type":"TRUE"}'::jsonb,
		'COUNT','NONE','FULLY_ADDITIVE','NULL',2,'FULLY_ADDITIVE','TEST_FIXTURE','PRESERVE','CERTIFIED',$6,$7)`,
		metricVersionID, tenantID, domainID, metricID, uuid.NewString(), metricHash, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.dimensions(
		id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
		logical_field_id,code,name,description,dimension_kind,sensitivity,
		member_index_policy,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,'region_id','region','区域','','CATEGORICAL',
		'INTERNAL','EXACT_ONLY','CERTIFIED',$6,$7)`, dimensionVersionID, tenantID, domainID,
		uuid.NewString(), uuid.NewString(), dimensionHash, actorID); err != nil {
		t.Fatal(err)
	}
	itemsV1, _ := CanonicalValue(bundleV1.Items)
	itemsV2, _ := CanonicalValue(bundleV2.Items)
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.kpi_bundles(
		id,tenant_id,domain_id,code,name,owner_user_id
	) VALUES($1,$2,$3,$4,$5,$6)`, bundleID, tenantID, domainID, bundleV1.Code, bundleV1.Name, actorID); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		bundle KPIBundle
		items  []byte
	}{{bundleV1, itemsV1}, {bundleV2, itemsV2}} {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.kpi_bundle_versions(
			id,tenant_id,domain_id,kpi_bundle_id,version_no,status,items,
			default_dimension_version_ids,default_time_expression,default_chart_types,
			role_mapping,applicable_question_patterns,content_hash,owner_id
		) VALUES($1,$2,$3,$4,$5,'CERTIFIED',$6,$7,$8,$9,$10,$11,$12,$13)`,
			fixture.bundle.ID, tenantID, domainID, bundleID, fixture.bundle.VersionNo,
			fixture.items, fixture.bundle.DefaultDimensionVersionIDs, fixture.bundle.DefaultTimeExpression,
			fixture.bundle.DefaultChartTypes, fixture.bundle.RoleMapping,
			fixture.bundle.ApplicableQuestionPatterns, fixture.bundle.ContentHash, actorID); err != nil {
			t.Fatal(err)
		}
	}
	for _, release := range []struct {
		id, version string
		hash        askdata.ContentHash
		bundle      KPIBundle
	}{{releaseV1ID, "kpi-v1-" + suffix, releaseV1Hash, bundleV1}, {releaseV2ID, "kpi-v2-" + suffix, releaseV2Hash, bundleV2}} {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
			id,tenant_id,domain_id,semantic_version,content_hash,status,object_count,
			created_by,updated_by,ready_at
		) VALUES($1,$2,$3,$4,$5,'READY',3,$6,$6,now())`, release.id, tenantID, domainID,
			release.version, release.hash, actorID); err != nil {
			t.Fatal(err)
		}
		for _, object := range []struct {
			objectType, objectID, versionID string
			hash                            askdata.ContentHash
		}{
			{"METRIC", metricID, metricVersionID, metricHash},
			{"DIMENSION", uuid.NewString(), dimensionVersionID, dimensionHash},
			{"KPI_BUNDLE", bundleID, release.bundle.ID, release.bundle.ContentHash},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
				tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
				content_hash,sensitivity,contract_json
			) VALUES($1,$2,$3,$4,$5,$6,$7,'INTERNAL','{}'::jsonb)`, tenantID, domainID,
				release.id, object.objectType, object.objectID, object.versionID, object.hash); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	defer cleanupKPIBundleMatcherFixture(t, admin, tenantID)

	loader := NewPostgresKPIBundleLoader(app)
	matcher, err := NewKPIBundleMatcher(loader, DefaultKPIBundleMatchConfig())
	if err != nil {
		t.Fatal(err)
	}
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	for _, test := range []struct {
		release  askdata.ReleaseRef
		question string
		want     string
	}{
		{askdata.ReleaseRef{ReleaseID: askdata.ID(releaseV1ID), ContentHash: releaseV1Hash}, "旧版经营概览", bundleV1.ID},
		{askdata.ReleaseRef{ReleaseID: askdata.ID(releaseV2ID), ContentHash: releaseV2Hash}, "新版经营概览", bundleV2.ID},
	} {
		scope, err := askdata.NewPolicyScope(askdata.ID(tenantID), askdata.ID(actorID),
			[]askdata.ID{askdata.ID(domainID)}, []askdata.ID{askdata.ID(roleID)}, test.release)
		if err != nil {
			t.Fatal(err)
		}
		result, err := matcher.MatchBundle(requestContext, scope, askdata.ID(domainID),
			KPIBundleMatchInput{Question: test.question})
		if err != nil || result.Selected == nil || result.Selected.BundleVersionID != askdata.ID(test.want) {
			t.Fatalf("release %s match = %#v/%v", test.release.ReleaseID, result, err)
		}
	}
}

func cleanupKPIBundleMatcherFixture(t *testing.T, admin *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Errorf("begin KPI cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable KPI cleanup triggers: %v", err)
		return
	}
	for _, statement := range []string{
		`DELETE FROM askdata.release_objects WHERE tenant_id=$1`,
		`DELETE FROM askdata.releases WHERE tenant_id=$1`,
		`DELETE FROM askdata.kpi_bundle_versions WHERE tenant_id=$1`,
		`DELETE FROM askdata.kpi_bundles WHERE tenant_id=$1`,
		`DELETE FROM askdata.metric_dimension_versions WHERE tenant_id=$1`,
		`DELETE FROM askdata.dimensions WHERE tenant_id=$1`,
		`DELETE FROM askdata.metric_versions WHERE tenant_id=$1`,
		`DELETE FROM askdata.metrics WHERE tenant_id=$1`,
		`DELETE FROM askdata.domains WHERE tenant_id=$1`,
		`DELETE FROM platform.domain_memberships WHERE tenant_id=$1`,
		`DELETE FROM platform.roles WHERE tenant_id=$1`,
		`DELETE FROM platform.business_domains WHERE tenant_id=$1`,
		`DELETE FROM platform.users WHERE tenant_id=$1`,
		`DELETE FROM platform.tenants WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, tenantID); err != nil {
			t.Errorf("KPI cleanup %q: %v", statement, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit KPI cleanup: %v", err)
	}
}
