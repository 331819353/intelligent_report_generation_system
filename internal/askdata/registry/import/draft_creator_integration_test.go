package registryimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresDraftCreatorPersistsAllTwelveAssetTypes(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	fixture := seedDraftCreatorReferences(t, ctx, tx)
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','SYSTEM',true),
		set_config('app.user_id','',true),
		set_config('app.domain_id','',true)`, fixture.tenantID); err != nil {
		t.Fatalf("set import context: %v", err)
	}
	creator := NewPostgresDraftCreator()
	batch := SemanticImport{
		ID: uuid.NewString(), TenantID: fixture.tenantID, DomainID: fixture.domainID,
		CreatedBy: fixture.actorID,
	}
	assertDraftCreatorModelReferences(t, ctx, tx, batch, fixture)
	assets := []struct {
		asset  AssetType
		values map[string]string
	}{
		{AssetModel, draftValues(t, AssetModel,
			"code", "new_model", "name", "New Model", "datasetVersionId", fixture.datasetVersionID,
			"entityCode", "order", "grainDescription", "one row per order", "grainKeyFields", "order_id",
			"timeContractCode", "calendar", "ownerEmail", fixture.ownerEmail)},
		{AssetMeasure, draftValues(t, AssetMeasure,
			"modelCode", "sales", "code", "new_measure", "name", "New Measure",
			"logicalFieldId", "amount", "defaultAggregation", "SUM", "unit", "COUNT",
			"additivity", "FULLY_ADDITIVE", "nullPolicy", "PRESERVE")},
		{AssetMetric, draftValues(t, AssetMetric,
			"code", "new_metric", "name", "New Metric", "modelCode", "sales",
			"formula", `{"type":"MEASURE_REF","measureCode":"revenue"}`,
			"unit", "COUNT", "additivity", "FULLY_ADDITIVE", "timeGrain", "NONE",
			"displayPrecision", "2", "zeroDenominatorPolicy", "NULL", "ownerEmail", fixture.ownerEmail)},
		{AssetMetricDimension, draftValues(t, AssetMetricDimension,
			"metricCode", "revenue", "dimensionCode", "region", "compatible", "TRUE", "role", "FILTER")},
		{AssetDimension, draftValues(t, AssetDimension,
			"modelCode", "sales", "code", "new_dimension", "name", "New Dimension",
			"kind", "CATEGORICAL", "logicalFieldId", "category_id", "sensitivity", "INTERNAL",
			"memberIndexPolicy", "EXACT_ONLY", "groupable", "TRUE", "filterable", "TRUE",
			"sortable", "TRUE", "ownerEmail", fixture.ownerEmail)},
		{AssetMember, draftValues(t, AssetMember,
			"dimensionCode", "region", "canonicalValue", "west", "displayLabel", "West",
			"aliases", "Western", "validFrom", "2026-01-01", "sensitivity", "CONFIDENTIAL")},
		{AssetHierarchy, draftValues(t, AssetHierarchy,
			"code", "geo", "name", "Geography", "levelOrder", "1", "dimensionCode", "country")},
		{AssetRelationship, draftValues(t, AssetRelationship,
			"leftModelCode", "sales", "rightModelCode", "other",
			"joinAst", `{"type":"EQ","leftField":"id","rightField":"id"}`,
			"joinType", "INNER", "cardinality", "ONE_TO_ONE", "fanoutPolicy", "SAFE",
			"validFrom", "2026-01-01")},
		{AssetTerm, draftValues(t, AssetTerm,
			"term", "华东", "termType", "MEMBER", "targetCode", "region::east",
			"matchMode", "EXACT", "priority", "100", "validFrom", "2026-01-01", "source", "IMPORT")},
		{AssetCertifiedExample, draftValues(t, AssetCertifiedExample,
			"question", "华东收入是多少", "expectedMetricCodes", "revenue",
			"expectedDimensionCodes", "region", "expectedMemberValues", "east",
			"expectedTimeExpression", "CURRENT_MONTH", "applicableRoles", "analyst")},
		{AssetKPIBundle, draftValues(t, AssetKPIBundle,
			"code", "sales_overview", "name", "Sales Overview", "metricCodes", "revenue",
			"defaultDimensionCodes", "region", "defaultTimeExpression", "CURRENT_MONTH",
			"defaultChartTypes", "line-trend", "applicableQuestionTypes", "TREND",
			"roleMapping", `{"analyst":"VIEW"}`)},
		{AssetEvalCase, draftValues(t, AssetEvalCase,
			"question", "华东收入是多少", "actorRole", "analyst", "expectedOutcome", "DIRECT",
			"expectedMetricCodes", "revenue", "expectedDimensionCodes", "region",
			"expectedMemberValues", "east", "expectedTimeExpression", "CURRENT_MONTH",
			"expectedResultHint", `{"nonEmpty":true}`, "setType", "VALIDATION", "shardId", "1")},
	}

	references := make(map[AssetType]DraftReference, len(assets))
	valuesByType := make(map[AssetType]map[string]string, len(assets))
	for rowNo, candidate := range assets {
		batch.AssetType = candidate.asset
		normalized, err := json.Marshal(candidate.values)
		if err != nil {
			t.Fatal(err)
		}
		reference, err := creator.CreateDraft(ctx, tx, batch, ImportRow{
			RowNo: rowNo + 1, NormalizedJSON: normalized, ValidationState: RowValid,
		})
		if err != nil {
			t.Fatalf("create %s DRAFT: %v", candidate.asset, err)
		}
		table, err := versionTableForAsset(candidate.asset)
		if err != nil {
			t.Fatal(err)
		}
		var status string
		query := fmt.Sprintf(`SELECT status FROM askdata.%s WHERE id=$1`, table)
		if err := tx.QueryRow(ctx, query, reference.VersionID).Scan(&status); err != nil || status != "DRAFT" {
			t.Fatalf("persisted %s status = %q/%v", candidate.asset, status, err)
		}
		references[candidate.asset] = reference
		valuesByType[candidate.asset] = candidate.values
	}
	batch.AssetType = AssetTerm
	sensitiveTermJSON, _ := json.Marshal(draftValues(t, AssetTerm,
		"term", "敏感区域", "termType", "MEMBER", "targetCode", "region::secret",
		"matchMode", "EXACT", "priority", "100", "validFrom", "2026-01-01", "source", "IMPORT"))
	sensitiveTerm, err := creator.CreateDraft(ctx, tx, batch, ImportRow{
		RowNo: 13, NormalizedJSON: sensitiveTermJSON, ValidationState: RowValid,
	})
	if err != nil {
		t.Fatalf("create sensitive MEMBER term DRAFT: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	for assetType, reference := range references {
		table, _ := versionTableForAsset(assetType)
		query := fmt.Sprintf(`UPDATE askdata.%s SET status='CERTIFIED' WHERE id=$1`, table)
		if _, err := tx.Exec(ctx, query, reference.VersionID); err != nil {
			t.Fatalf("make %s export fixture certified: %v", assetType, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_member_aliases SET status='CERTIFIED'
		WHERE member_version_id=$1`, references[AssetMember].VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.business_term_versions SET status='CERTIFIED'
		WHERE id=$1`, sensitiveTerm.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range assets {
		rows, omitted, err := loadExportRows(
			ctx, tx, candidate.asset, []string{references[candidate.asset].VersionID},
		)
		expectedRows, expectedOmitted := 1, 0
		if candidate.asset == AssetMember {
			expectedRows, expectedOmitted = 0, 1
		}
		if err != nil || len(rows) != expectedRows || omitted != expectedOmitted {
			t.Fatalf("export %s rows = %#v omitted=%d error=%v", candidate.asset, rows, omitted, err)
		}
		if expectedRows == 0 {
			continue
		}
		definition, _ := TemplateDefinitionFor(candidate.asset)
		if len(rows[0]) != len(definition.Columns) {
			t.Fatalf("export %s column count = %d, want %d", candidate.asset, len(rows[0]), len(definition.Columns))
		}
		for column, expected := range candidate.values {
			if !sameExportValue(rows[0][column], expected) {
				t.Fatalf("export %s column %s = %q, want %q",
					candidate.asset, column, rows[0][column], expected)
			}
		}
	}
	sensitiveTermRows, sensitiveTermOmitted, err := loadExportRows(
		ctx, tx, AssetTerm, []string{sensitiveTerm.VersionID},
	)
	if err != nil || len(sensitiveTermRows) != 0 || sensitiveTermOmitted != 1 {
		t.Fatalf("sensitive MEMBER term export = %#v omitted=%d error=%v",
			sensitiveTermRows, sensitiveTermOmitted, err)
	}

	newMetricValues := make(map[string]string, len(valuesByType[AssetMetric]))
	for key, value := range valuesByType[AssetMetric] {
		newMetricValues[key] = value
	}
	newMetricValues["description"] = "second certified version"
	newMetricJSON, _ := json.Marshal(newMetricValues)
	batch.AssetType = AssetMetric
	newMetric, err := creator.CreateDraft(ctx, tx, batch, ImportRow{
		RowNo: 14, NormalizedJSON: newMetricJSON, ValidationState: RowValid,
	})
	if err != nil || newMetric.ObjectID != references[AssetMetric].ObjectID {
		t.Fatalf("create second metric version = %#v/%v", newMetric, err)
	}
	releaseID := uuid.NewString()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.metric_versions SET status='CERTIFIED' WHERE id=$1`, newMetric.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,'DRAFT',1,$6,$6)`, releaseID, fixture.tenantID,
		fixture.domainID, "export-history-"+releaseID[:8], strings.Repeat("f", 64), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	var oldMetricHash string
	if err := tx.QueryRow(ctx, `SELECT content_hash FROM askdata.metric_versions WHERE id=$1`,
		references[AssetMetric].VersionID).Scan(&oldMetricHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,content_hash,contract_json
	) VALUES($1,$2,$3,'METRIC',$4,$5,$6,'{}'::jsonb)`, fixture.tenantID, fixture.domainID,
		releaseID, references[AssetMetric].ObjectID, references[AssetMetric].VersionID, oldMetricHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	currentIDs, err := selectedExportVersionIDs(ctx, tx, ExportSelection{
		TenantID: fixture.tenantID, DomainID: fixture.domainID, ActorID: fixture.actorID,
		AssetTypes: []AssetType{AssetMetric},
	}, AssetMetric)
	if err != nil || len(currentIDs) != 2 || !containsString(currentIDs, newMetric.VersionID) ||
		containsString(currentIDs, references[AssetMetric].VersionID) {
		t.Fatalf("current metric export ids = %#v/%v", currentIDs, err)
	}
	releaseIDs, err := selectedExportVersionIDs(ctx, tx, ExportSelection{
		TenantID: fixture.tenantID, DomainID: fixture.domainID, ActorID: fixture.actorID,
		AssetTypes: []AssetType{AssetMetric}, ReleaseID: releaseID,
	}, AssetMetric)
	if err != nil || len(releaseIDs) != 1 || releaseIDs[0] != references[AssetMetric].VersionID {
		t.Fatalf("release metric export ids = %#v/%v", releaseIDs, err)
	}
	currentRows, _, err := loadExportRows(ctx, tx, AssetMetric, currentIDs)
	if err != nil {
		t.Fatal(err)
	}
	var currentDescription string
	for _, row := range currentRows {
		if row["code"] == "new_metric" {
			currentDescription = row["description"]
		}
	}
	releaseRows, _, err := loadExportRows(ctx, tx, AssetMetric, releaseIDs)
	if err != nil || len(releaseRows) != 1 || releaseRows[0]["code"] != "new_metric" ||
		currentDescription != "second certified version" || releaseRows[0]["description"] != "" {
		t.Fatalf("current/release metric export = %#v/%#v/%v", currentRows, releaseRows, err)
	}
}

func sameExportValue(actual, expected string) bool {
	if actual == expected {
		return true
	}
	var actualJSON, expectedJSON any
	return json.Unmarshal([]byte(actual), &actualJSON) == nil &&
		json.Unmarshal([]byte(expected), &expectedJSON) == nil &&
		reflect.DeepEqual(actualJSON, expectedJSON)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestPostgresDraftCreatorMergesHierarchyRowsIntoOneVersion(t *testing.T) {
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
	fixture := seedDraftCreatorReferences(t, ctx, tx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','SYSTEM',true)`, fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	batch := SemanticImport{
		ID: uuid.NewString(), TenantID: fixture.tenantID, DomainID: fixture.domainID,
		AssetType: AssetHierarchy, CreatedBy: fixture.actorID,
	}
	creator := NewPostgresDraftCreator()
	firstValues := draftValues(t, AssetHierarchy,
		"code", "geo", "name", "Geography", "levelOrder", "1", "dimensionCode", "country")
	firstJSON, _ := json.Marshal(firstValues)
	first, err := creator.CreateDraft(ctx, tx, batch, ImportRow{
		RowNo: 1, NormalizedJSON: firstJSON, ValidationState: RowValid,
	})
	if err != nil {
		t.Fatalf("create first hierarchy level: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_imports(
		id,tenant_id,domain_id,asset_type,file_object_uri,file_hash,file_name,state,
		total_rows,valid_rows,invalid_rows,created_by,attempt,validation_completed_at,committed_at
	) VALUES($1,$2,$3,'HIERARCHY','minio://semantic-imports/hierarchy.csv',$4,
		'hierarchy.csv','COMMITTED',1,1,0,$5,1,now(),now())`, batch.ID, batch.TenantID,
		batch.DomainID, strings.Repeat("d", 64), batch.CreatedBy); err != nil {
		t.Fatalf("insert hierarchy import: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_import_rows(
		tenant_id,import_id,row_no,raw_json,normalized_json,validation_state,errors_json,
		created_object_id,created_version_id
	) VALUES($1,$2,1,$3,$3,'COMMITTED','[]'::jsonb,$4,$5)`, batch.TenantID,
		batch.ID, firstJSON, first.ObjectID, first.VersionID); err != nil {
		t.Fatalf("insert committed hierarchy row: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	secondValues := draftValues(t, AssetHierarchy,
		"code", "geo", "name", "Geography", "levelOrder", "2", "dimensionCode", "region",
		"parentDimensionCode", "country")
	secondJSON, _ := json.Marshal(secondValues)
	second, err := creator.CreateDraft(ctx, tx, batch, ImportRow{
		RowNo: 2, NormalizedJSON: secondJSON, ValidationState: RowValid,
	})
	if err != nil {
		t.Fatalf("create second hierarchy level: %v", err)
	}
	if second != first {
		t.Fatalf("hierarchy rows produced different references: %#v / %#v", first, second)
	}
	var levels int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.hierarchy_levels
		WHERE hierarchy_version_id=$1`, first.VersionID).Scan(&levels); err != nil || levels != 2 {
		t.Fatalf("hierarchy level count = %d/%v", levels, err)
	}
}

func assertDraftCreatorModelReferences(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	fixture draftCreatorFixture,
) {
	t.Helper()
	if _, err := resolveOwner(ctx, tx, batch, fixture.ownerEmail); err != nil {
		t.Fatalf("resolve model owner: %v", err)
	}
	for _, reference := range [][2]string{{"ENTITY", "order"}, {"TIME_CONTRACT", "calendar"}} {
		if _, _, err := resolveCertifiedCode(ctx, tx, batch, reference[0], reference[1]); err != nil {
			t.Fatalf("resolve model %s reference: %v", reference[0], err)
		}
	}
	if _, versionNo, err := nextCodeVersion(ctx, tx, batch, AssetModel, "new_model"); err != nil || versionNo != 1 {
		t.Fatalf("resolve model identity/version = %d/%v", versionNo, err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*)
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.dataset_id=dataset.id AND materialization.dataset_version_id=version.id
		 AND materialization.tenant_id=dataset.tenant_id
		WHERE version.id=$1 AND version.tenant_id=$2 AND dataset.domain_id=$3
		  AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
		  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
		  AND dataset.current_published_version_id=version.id
		  AND materialization.status='ACTIVE' AND materialization.layer=version.layer
		  AND materialization.schema_hash=version.schema_hash`, fixture.datasetVersionID,
		batch.TenantID, batch.DomainID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resolve model dataset/materialization = %d/%v", count, err)
	}
}

type draftCreatorFixture struct {
	tenantID, domainID, actorID, ownerEmail, datasetVersionID string
}

func seedDraftCreatorReferences(t *testing.T, ctx context.Context, tx pgx.Tx) draftCreatorFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := draftCreatorFixture{
		tenantID: uuid.NewString(), domainID: uuid.NewString(), actorID: uuid.NewString(),
		ownerEmail: "draft-creator-" + suffix + "@example.invalid", datasetVersionID: uuid.NewString(),
	}
	datasetID, materializationID := uuid.NewString(), uuid.NewString()
	entityVersionID, entityID := uuid.NewString(), uuid.NewString()
	timeContractID, timeContractVersionID := uuid.NewString(), uuid.NewString()
	salesModelVersionID, salesModelID := uuid.NewString(), uuid.NewString()
	otherModelVersionID, otherModelID := uuid.NewString(), uuid.NewString()
	measureVersionID, measureID := uuid.NewString(), uuid.NewString()
	metricVersionID, metricID := uuid.NewString(), uuid.NewString()
	regionVersionID, regionID := uuid.NewString(), uuid.NewString()
	countryVersionID, countryID := uuid.NewString(), uuid.NewString()
	memberVersionID, memberID := uuid.NewString(), uuid.NewString()
	sensitiveMemberVersionID, sensitiveMemberID := uuid.NewString(), uuid.NewString()
	roleID := uuid.NewString()
	hashA, hashB, hashC := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Draft Creator Tenant')`, []any{fixture.tenantID, "draft_" + suffix}},
		{`INSERT INTO platform.users(id,tenant_id,email,display_name,password_hash,employee_no)
		 VALUES($1,$2,$3,'Draft Creator Owner','integration-only',$4)`, []any{fixture.actorID, fixture.tenantID, fixture.ownerEmail, "DRAFT" + strings.ToUpper(suffix)}},
		{`INSERT INTO platform.business_domains(id,tenant_id,code,name,created_by)
		 VALUES($1,$2,$3,'Draft Creator Domain',$4)`, []any{fixture.domainID, fixture.tenantID, "draft_" + suffix, fixture.actorID}},
		{`INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by)
		 VALUES($1,$2,$3,'DOMAIN_ADMIN',$3)`, []any{fixture.tenantID, fixture.domainID, fixture.actorID}},
		{`INSERT INTO platform.roles(id,tenant_id,code,name) VALUES($1,$2,'analyst','Analyst')`, []any{roleID, fixture.tenantID}},
		{`INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		 VALUES($1,$2,$3,'Draft Creator Domain',$4)`, []any{fixture.domainID, fixture.tenantID, "draft_" + suffix, fixture.actorID}},
		{`INSERT INTO platform.datasets(id,tenant_id,code,name,dataset_type,status,created_by,updated_by,layer,domain_id)
		 VALUES($1,$2,$3,'Sales DWS','SINGLE_SOURCE','PUBLISHED',$4,$4,'DWS',$5)`, []any{datasetID, fixture.tenantID, "sales_" + suffix, fixture.actorID, fixture.domainID}},
		{`INSERT INTO platform.dataset_versions(
		 id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,schema_hash,
		 logical_plan_json,plan_hash,created_by,updated_by,created_at,published_at,published_by,
		 source_draft_version_id,source_draft_record_version,layer,publication_origin
		 ) VALUES($1,$2,$3,1,'PUBLISHED','1.0','{}'::jsonb,$4,'{}'::jsonb,$5,$6,$6,
		 now()-interval '2 seconds',now()-interval '1 second',$6,$7,1,'DWS','LEGACY')`, []any{
			fixture.datasetVersionID, fixture.tenantID, datasetID, hashA, hashB, fixture.actorID, uuid.NewString()}},
		{`UPDATE platform.datasets SET current_published_version_id=$1 WHERE id=$2`, []any{fixture.datasetVersionID, datasetID}},
		{`INSERT INTO platform.dataset_materializations(
		 id,tenant_id,dataset_id,dataset_version_id,build_run_id,layer,status,refresh_mode,
		 physical_schema,physical_name,published_name,schema_hash,snapshot_hash,row_count,size_bytes,activated_at
		 ) VALUES($1,$2,$3,$4,$5,'DWS','ACTIVE','FULL','warehouse_dws',$6,$7,$8,$9,1,1,now())`, []any{
			materializationID, fixture.tenantID, datasetID, fixture.datasetVersionID, uuid.NewString(),
			"sales_" + suffix, "sales_" + suffix, hashA, hashC}},
		{`INSERT INTO askdata.entities(
		 id,tenant_id,domain_id,entity_id,version_no,code,name,key_contract,status,content_hash,owner_id
		 ) VALUES($1,$2,$3,$4,1,'order','Order','{}'::jsonb,'CERTIFIED',$5,$6)`, []any{
			entityVersionID, fixture.tenantID, fixture.domainID, entityID, hashA, fixture.actorID}},
		{`INSERT INTO askdata.time_contracts(id,tenant_id,domain_id,code,name,owner_user_id)
		 VALUES($1,$2,$3,'calendar','Calendar',$4)`, []any{timeContractID, fixture.tenantID, fixture.domainID, fixture.actorID}},
		{`INSERT INTO askdata.time_contract_versions(
		 id,tenant_id,domain_id,time_contract_id,version_no,status,timezone,week_start,
		 week_numbering,fiscal_year_start_month,fiscal_month_rule,incomplete_period_policy,
		 comparison_alignment,month_end_overflow_rule,supported_grains,data_available_through_expr,
		 expected_lag_hours,content_hash
		 ) VALUES($1,$2,$3,$4,1,'CERTIFIED','Asia/Shanghai','MONDAY','ISO',1,'CALENDAR',
		 'MTD','SAME_DAY_COUNT','CLAMP_TO_LAST_DAY',ARRAY['DAY','MONTH'],
		 'MATERIALIZATION_MAX_PRIMARY_TIME',26,$5)`, []any{
			timeContractVersionID, fixture.tenantID, fixture.domainID, timeContractID, hashA}},
		{`INSERT INTO askdata.semantic_models(
		 id,tenant_id,domain_id,model_id,version_no,code,name,entity_version_id,dataset_id,
		 dataset_version_id,materialization_id,dataset_schema_hash,layer,grain_contract,
		 primary_time_field_id,time_contract_version_id,status,content_hash,owner_id
		 ) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,'DWS','{}'::jsonb,'',$12,
		 'CERTIFIED',$13,$14)`, []any{salesModelVersionID, fixture.tenantID, fixture.domainID,
			salesModelID, "sales", "Sales", entityVersionID, datasetID, fixture.datasetVersionID,
			materializationID, hashA, timeContractVersionID, hashA, fixture.actorID}},
		{`INSERT INTO askdata.semantic_models(
		 id,tenant_id,domain_id,model_id,version_no,code,name,entity_version_id,dataset_id,
		 dataset_version_id,materialization_id,dataset_schema_hash,layer,grain_contract,
		 primary_time_field_id,time_contract_version_id,status,content_hash,owner_id
		 ) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,'DWS','{}'::jsonb,'',$12,
		 'CERTIFIED',$13,$14)`, []any{otherModelVersionID, fixture.tenantID, fixture.domainID,
			otherModelID, "other", "Other", entityVersionID, datasetID, fixture.datasetVersionID,
			materializationID, hashA, timeContractVersionID, hashB, fixture.actorID}},
		{`INSERT INTO askdata.measures(
		 id,tenant_id,domain_id,measure_id,version_no,semantic_model_version_id,code,name,
		 formula_ast,aggregation,data_type,unit,status,content_hash,owner_id,additivity,
		 additivity_confirmed_by,additivity_confirmed_at
		 ) VALUES($1,$2,$3,$4,1,$5,'revenue','Revenue','{"type":"FIELD_REF","logicalFieldId":"amount"}'::jsonb,
		 'SUM','DECIMAL','COUNT','CERTIFIED',$6,$7,'FULLY_ADDITIVE',$7,now())`, []any{
			measureVersionID, fixture.tenantID, fixture.domainID, measureID, salesModelVersionID, hashA, fixture.actorID}},
		{`INSERT INTO askdata.metrics(id,tenant_id,domain_id,code,name,status,owner_id)
		 VALUES($1,$2,$3,'revenue','Revenue','ACTIVE',$4)`, []any{metricID, fixture.tenantID, fixture.domainID, fixture.actorID}},
		{`INSERT INTO askdata.metric_versions(
		 id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,formula_ast,
		 unit,time_grain,status,content_hash,owner_id,additivity,additivity_confirmed_by,
		 additivity_confirmed_at
		 ) VALUES($1,$2,$3,$4,1,$5,'{"type":"MEASURE_REF","measureCode":"revenue"}'::jsonb,
		 'COUNT','NONE','CERTIFIED',$6,$7,'FULLY_ADDITIVE',$7,now())`, []any{
			metricVersionID, fixture.tenantID, fixture.domainID, metricID, salesModelVersionID, hashA, fixture.actorID}},
		{`INSERT INTO askdata.dimensions(
		 id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,logical_field_id,
		 code,name,dimension_kind,sensitivity,member_index_policy,status,content_hash,owner_id
		 ) VALUES($1,$2,$3,$4,1,$5,'region_id','region','Region','CATEGORICAL','INTERNAL',
		 'EXACT_ONLY','CERTIFIED',$6,$7)`, []any{regionVersionID, fixture.tenantID, fixture.domainID,
			regionID, salesModelVersionID, hashA, fixture.actorID}},
		{`INSERT INTO askdata.dimensions(
		 id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,logical_field_id,
		 code,name,dimension_kind,sensitivity,member_index_policy,status,content_hash,owner_id
		 ) VALUES($1,$2,$3,$4,1,$5,'country_id','country','Country','CATEGORICAL','INTERNAL',
		 'EXACT_ONLY','CERTIFIED',$6,$7)`, []any{countryVersionID, fixture.tenantID, fixture.domainID,
			countryID, salesModelVersionID, hashB, fixture.actorID}},
		{`INSERT INTO askdata.dimension_members(
		 id,tenant_id,domain_id,member_id,version_no,dimension_version_id,member_key,
		 member_key_hash,canonical_label,sensitivity,valid_from,status,content_hash,created_by
		 ) VALUES($1,$2,$3,$4,1,$5,'east',$6,'East','INTERNAL','2026-01-01',
		 'CERTIFIED',$7,$8)`, []any{memberVersionID, fixture.tenantID, fixture.domainID, memberID,
			regionVersionID, boundValueHash(regionVersionID, "east"), hashA, fixture.actorID}},
		{`INSERT INTO askdata.dimension_members(
		 id,tenant_id,domain_id,member_id,version_no,dimension_version_id,member_key,
		 member_key_hash,canonical_label,sensitivity,valid_from,status,content_hash,created_by
		 ) VALUES($1,$2,$3,$4,1,$5,'secret',$6,'Secret','CONFIDENTIAL','2026-01-01',
		 'CERTIFIED',$7,$8)`, []any{sensitiveMemberVersionID, fixture.tenantID, fixture.domainID,
			sensitiveMemberID, regionVersionID, boundValueHash(regionVersionID, "secret"), hashB, fixture.actorID}},
	}
	for index, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed draft creator statement %d: %v", index+1, err)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func draftValues(t *testing.T, assetType AssetType, pairs ...string) map[string]string {
	t.Helper()
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(definition.Columns))
	for _, column := range definition.Columns {
		values[column.Name] = ""
	}
	for index := 0; index < len(pairs); index += 2 {
		values[pairs[index]] = pairs[index+1]
	}
	return values
}
