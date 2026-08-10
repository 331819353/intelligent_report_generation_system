package graph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresFallbackAndCertifiedCacheAgainstRuntimeRole(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	appRole := os.Getenv("ASKDATA_INTEGRATION_APP_ROLE")
	if appRole == "" {
		appRole = "report_app"
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

	tenantID, actorID, domainID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'graph fallback integration tenant')`, tenantID, "graph_fallback_"+tenantID[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,employee_no,email,display_name,password_hash,status
	) VALUES($1,$2,$3,$4,'graph fallback actor','integration-only','ACTIVE')`,
		actorID, tenantID, "GRAPH"+actorID[:8], actorID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,'graph_fallback','graph fallback',true,$3)`, domainID, tenantID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,status,member_role,assigned_by
	) VALUES($1,$2,$3,'ACTIVE','MEMBER',$3)`, tenantID, domainID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		VALUES($1,$2,'graph_fallback','graph fallback',$3)`, domainID, tenantID, actorID); err != nil {
		t.Fatal(err)
	}

	modelIDs := []string{uuid.NewString(), uuid.NewString()}
	modelVersionIDs := []string{uuid.NewString(), uuid.NewString()}
	metricIDs := []string{uuid.NewString(), uuid.NewString()}
	metricVersionIDs := []string{uuid.NewString(), uuid.NewString()}
	dimensionID, dimensionVersionID := uuid.NewString(), uuid.NewString()
	memberID, memberVersionID := uuid.NewString(), uuid.NewString()
	relationshipID, relationshipVersionID := uuid.NewString(), uuid.NewString()
	releaseID := uuid.NewString()

	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	for index := range modelIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_models(
			id,tenant_id,domain_id,model_id,version_no,code,name,dataset_id,
			dataset_version_id,materialization_id,dataset_schema_hash,layer,
			grain_contract,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,'DWS','{}','CERTIFIED',$11,$12)`,
			modelVersionIDs[index], tenantID, domainID, modelIDs[index],
			"fallback_model_"+string(rune('a'+index)), "fallback model", uuid.NewString(),
			uuid.NewString(), uuid.NewString(), strings.Repeat(string(rune('a'+index)), 64),
			strings.Repeat(string(rune('1'+index)), 64), actorID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metrics(
			id,tenant_id,domain_id,code,name,status,owner_id
		) VALUES($1,$2,$3,$4,'fallback metric','ACTIVE',$5)`,
			metricIDs[index], tenantID, domainID, "fallback_metric_"+string(rune('a'+index)), actorID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
			id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
			formula_ast,unit,additivity,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,1,$5,'{}','COUNT','FULLY_ADDITIVE','CERTIFIED',$6,$7)`,
			metricVersionIDs[index], tenantID, domainID, metricIDs[index],
			modelVersionIDs[index], strings.Repeat(string(rune('3'+index)), 64), actorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.dimensions(
		id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
		logical_field_id,code,name,dimension_kind,sensitivity,member_index_policy,
		status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,'region_key','fallback_region','fallback region',
		'CATEGORICAL','INTERNAL','EXACT_ONLY','CERTIFIED',$6,$7)`,
		dimensionVersionID, tenantID, domainID, dimensionID, modelVersionIDs[0],
		strings.Repeat("5", 64), actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.dimension_members(
		id,tenant_id,domain_id,member_id,version_no,dimension_version_id,
		member_key,member_key_hash,canonical_label,sensitivity,valid_from,
		status,content_hash,created_by
	) VALUES($1,$2,$3,$4,1,$5,'secret-member',$6,'secret label','INTERNAL',
		now()-interval '1 day','CERTIFIED',$7,$8)`, memberVersionID, tenantID,
		domainID, memberID, dimensionVersionID, strings.Repeat("6", 64),
		strings.Repeat("7", 64), actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.relationships(
		id,tenant_id,domain_id,relationship_id,version_no,left_model_version_id,
		right_model_version_id,relationship_type,join_type,cardinality,join_ast,
		fanout_policy,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,$6,'MODEL_JOIN','INNER','ONE_TO_MANY','{}',
		'SAFE','CERTIFIED',$7,$8)`, relationshipVersionID, tenantID,
		domainID, relationshipID, modelVersionIDs[0], modelVersionIDs[1],
		strings.Repeat("8", 64), actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,created_by,updated_by
	) VALUES($1,$2,$3,'fallback-v1',$4,'DRAFT',$5,$5)`, releaseID, tenantID,
		domainID, strings.Repeat("0", 64), actorID); err != nil {
		t.Fatal(err)
	}
	type releaseObject struct{ objectType, objectID, versionID, contentHash string }
	objects := []releaseObject{
		{"SEMANTIC_MODEL", modelIDs[0], modelVersionIDs[0], strings.Repeat("1", 64)},
		{"SEMANTIC_MODEL", modelIDs[1], modelVersionIDs[1], strings.Repeat("2", 64)},
		{"METRIC", metricIDs[0], metricVersionIDs[0], strings.Repeat("3", 64)},
		{"METRIC", metricIDs[1], metricVersionIDs[1], strings.Repeat("4", 64)},
		{"DIMENSION", dimensionID, dimensionVersionID, strings.Repeat("5", 64)},
		{"MEMBER", memberID, memberVersionID, strings.Repeat("7", 64)},
		{"RELATIONSHIP", relationshipID, relationshipVersionID, strings.Repeat("8", 64)},
	}
	for _, object := range objects {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
			tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
			content_hash,sensitivity,contract_json
		) VALUES($1,$2,$3,$4,$5,$6,$7,'INTERNAL','{}')`, tenantID, domainID,
			releaseID, object.objectType, object.objectID, object.versionID, object.contentHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	var releaseHash string
	if err := tx.QueryRow(ctx, `SELECT askdata.release_manifest_hash($1)`, releaseID).Scan(&releaseHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.releases SET
		content_hash=$2,object_count=$3,status='READY',ready_at=now() WHERE id=$1`,
		releaseID, releaseHash, len(objects)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_projections(
		tenant_id,domain_id,release_id,target,status,expected_content_hash,
		applied_content_hash,resource_version,object_count,completed_at
	) SELECT $1,$2,$3,target,'READY',$4,$4,'integration-v1',$5,now()
	FROM unnest(ARRAY[
		'POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER'
	]) AS target`, tenantID, domainID, releaseID, releaseHash, len(objects)); err != nil {
		t.Fatal(err)
	}

	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{askdata.ID(uuid.NewString())}, askdata.ReleaseRef{
			ReleaseID:   askdata.ID(releaseID),
			ContentHash: askdata.ContentHash(releaseHash),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := (PlanRequest{
		Scope: scope, DomainID: askdata.ID(domainID),
		MetricRefs: []ObjectVersionRef{
			{ObjectID: askdata.ID(metricIDs[0]), VersionID: askdata.ID(metricVersionIDs[0]), Version: 1},
			{ObjectID: askdata.ID(metricIDs[1]), VersionID: askdata.ID(metricVersionIDs[1]), Version: 1},
		},
		ModelRefs: []ObjectVersionRef{
			{ObjectID: askdata.ID(modelIDs[0]), VersionID: askdata.ID(modelVersionIDs[0]), Version: 1},
			{ObjectID: askdata.ID(modelIDs[1]), VersionID: askdata.ID(modelVersionIDs[1]), Version: 1},
		},
		DimensionRefs: []ObjectVersionRef{{
			ObjectID: askdata.ID(dimensionID), VersionID: askdata.ID(dimensionVersionID), Version: 1,
		}},
		MemberRefs: []ObjectVersionRef{{
			ObjectID: askdata.ID(memberID), VersionID: askdata.ID(memberVersionID), Version: 1,
		}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.access_mode','USER',true),
		set_config('app.tenant_id',$1,true),set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true)`, tenantID, actorID, domainID); err != nil {
		t.Fatal(err)
	}
	plan, err := resolvePostgresFallbackTx(ctx, tx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.MetricModels) != 2 || len(plan.Models) != 2 ||
		len(plan.CompatibleDimensions) != 1 || len(plan.MemberOwnerships) != 1 ||
		len(plan.JoinPaths) != 1 || plan.MemberOwnerships[0].Status != MemberStatusActive {
		t.Fatalf("unexpected relational GraphPlan: %#v", plan)
	}
	modelRefByVersion := refIndex(request.ModelRefs)
	leftRef, rightRef := modelRefByVersion[askdata.ID(modelVersionIDs[0])], modelRefByVersion[askdata.ID(modelVersionIDs[1])]
	direction := TraversalForward
	leftVID, err := BuildVID(scope.TenantID, ObjectTypeSemanticModel, leftRef)
	if err != nil {
		t.Fatal(err)
	}
	rightVID, err := BuildVID(scope.TenantID, ObjectTypeSemanticModel, rightRef)
	if err != nil {
		t.Fatal(err)
	}
	if rightVID < leftVID {
		leftRef, rightRef, direction = rightRef, leftRef, TraversalReverse
	}
	expectedPath, err := NewJoinPath([]JoinStep{{
		Hop: 1, RelationshipVersionID: askdata.ID(relationshipVersionID),
		FromModelVersionID: leftRef.VersionID,
		ToModelVersionID:   rightRef.VersionID,
		Direction:          direction, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityOneToMany, FanoutPolicy: registry.FanoutSafe,
	}})
	if err != nil {
		t.Fatal(err)
	}
	expectedPlan, err := NewGraphPlan(
		request,
		request.ModelRefs,
		[]MetricModelBinding{
			{MetricVersionID: askdata.ID(metricVersionIDs[0]), ModelVersionID: askdata.ID(modelVersionIDs[0])},
			{MetricVersionID: askdata.ID(metricVersionIDs[1]), ModelVersionID: askdata.ID(modelVersionIDs[1])},
		},
		[]DimensionCompatibility{{
			ModelVersionID:     askdata.ID(modelVersionIDs[0]),
			DimensionVersionID: request.DimensionRefs[0].VersionID,
		}},
		[]MemberOwnership{{
			MemberVersionID:    request.MemberRefs[0].VersionID,
			DimensionVersionID: request.DimensionRefs[0].VersionID,
			Status:             MemberStatusActive,
		}},
		[]JoinPath{expectedPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedPlan, err = expectedPlan.WithDegradation(DegradationNebulaUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, expectedPlan) {
		t.Fatalf("relational fallback did not reproduce GraphPlan contract:\ngot  %#v\nwant %#v", plan, expectedPlan)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret label") || strings.Contains(string(raw), "secret-member") {
		t.Fatalf("fallback leaked member material: %s", raw)
	}

	if _, err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	requestHash := plan.RequestHash
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.graph_plan_cache(
		tenant_id,domain_id,release_id,question_shape_hash,policy_scope_hash,
		graph_content_hash,plan_hash,plan_json,expires_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()+interval '5 minutes')`, tenantID,
		domainID, releaseID, requestHash, scope.PolicyHash, releaseHash, plan.PlanHash, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cachedRaw, cachedHash, hit, err := loadCertifiedPlanCacheTx(ctx, tx, request, requestHash)
	if err != nil || !hit {
		t.Fatalf("load certified cache hit=%t error=%v", hit, err)
	}
	cached, err := decodeCertifiedPlan(cachedRaw, cachedHash, request, requestHash)
	if err != nil || cached.PlanHash != plan.PlanHash {
		t.Fatalf("decode certified cache = %#v, %v", cached, err)
	}

	if _, err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.release_projections SET
		status='FAILED',applied_content_hash='',resource_version='',error_code='VERIFY_FAILED'
		WHERE release_id=$1 AND target='NEBULA_GRAPH'`, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePostgresFallbackTx(ctx, tx, request); err != nil {
		t.Fatalf("registry fallback depended on stale graph projection: %v", err)
	}
	_, _, hit, err = loadCertifiedPlanCacheTx(ctx, tx, request, requestHash)
	if err != nil || hit {
		t.Fatalf("cache replayed stale graph projection hit=%t error=%v", hit, err)
	}

	outsideRelease := request
	outsideObjectID, outsideVersionID := askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	outsideRelease.MetricRefs = append([]ObjectVersionRef(nil), request.MetricRefs...)
	outsideRelease.MetricRefs[0] = ObjectVersionRef{
		ObjectID: outsideObjectID, VersionID: outsideVersionID, Version: 1,
	}
	outsideRelease, err = outsideRelease.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePostgresFallbackTx(ctx, tx, outsideRelease); !errors.Is(err, ErrGraphFallbackBlocked) ||
		strings.Contains(err.Error(), string(outsideObjectID)) || strings.Contains(err.Error(), string(outsideVersionID)) {
		t.Fatalf("release trimming did not fail closed without leaking object identity: %v", err)
	}

	accessCtx := database.WithAccessContext(context.Background(), actorID, domainID)
	if err := validateFallbackAccessContext(accessCtx, request); err != nil {
		t.Fatalf("fixture access context failed: %v", err)
	}
}
