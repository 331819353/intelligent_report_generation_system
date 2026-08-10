package evaluation

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReleaseGatePostgresRecomputesFactsAndEnforcesTwoPersonActivation(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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

	tenantID, domainID := uuid.NewString(), uuid.NewString()
	initiatorID, semanticOwnerID := uuid.NewString(), uuid.NewString()
	dataOwnerID := uuid.NewString()
	releaseID, evaluationSetID, batchID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(tenantID, "-", "")[:12]
	releaseHash, setHash, warehouseHash := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)

	mustExecGateFixture(t, ctx, tx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Release gate integration')`, tenantID, "gate_"+suffix)
	for index, actorID := range []string{initiatorID, semanticOwnerID, dataOwnerID} {
		mustExecGateFixture(t, ctx, tx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash,employee_no,status
		) VALUES($1,$2,$3,$4,'not-a-login-hash',$5,'ACTIVE')`, actorID, tenantID,
			"gate_"+string(rune('a'+index))+suffix+"@example.invalid", "Gate reviewer", "GATE"+strings.ToUpper(suffix)+string(rune('A'+index)))
	}
	mustExecGateFixture(t, ctx, tx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by,status
	) VALUES($1,$2,$3,'Release gate',true,$4,'ACTIVE')`, domainID, tenantID, "gate_"+suffix, initiatorID)
	for _, actorID := range []string{initiatorID, semanticOwnerID, dataOwnerID} {
		mustExecGateFixture(t, ctx, tx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,status,member_role,assigned_by
		) VALUES($1,$2,$3,'ACTIVE','DOMAIN_ADMIN',$4)`, tenantID, domainID, actorID, initiatorID)
	}
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.domains(
		id,tenant_id,code,name,owner_id,status
	) VALUES($1,$2,$3,'Release gate',$4,'ACTIVE')`, domainID, tenantID, "gate_"+suffix, initiatorID)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,object_count,
		created_by,updated_by,ready_at
	) VALUES($1,$2,$3,$4,$5,'READY',1,$6,$6,clock_timestamp())`, releaseID, tenantID,
		domainID, "gate-"+suffix, releaseHash, initiatorID)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.release_projections(
		tenant_id,domain_id,release_id,target,status,expected_content_hash,
		applied_content_hash,resource_version,object_count,completed_at
	) SELECT $1,$2,$3,target,'READY',$4,$4,'gate-integration-v1',1,clock_timestamp()
	FROM unnest(ARRAY[
		'POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER'
	]) AS target`, tenantID, domainID, releaseID, releaseHash)

	// The DB-006 lifecycle is independently covered. Bulk-load an exact sealed
	// fixture with triggers disabled so this test stays focused and fast enough
	// to exercise the hard 2,000-case gate.
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='replica'`)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.evaluation_sets(
		id,tenant_id,domain_id,code,version_no,name,dataset_split,evaluation_mode,
		status,target_release_id,target_semantic_version,target_release_content_hash,
		sealed_content_hash,sealed_case_count,sealed_review_count,created_by,updated_by,
		sealed_by,sealed_at
	) VALUES($1,$2,$3,$4,1,'Release gate sealed set','SEALED',
		'END_TO_END_RESULT_EQUIVALENCE','SEALED',$5,$6,$7,$8,2000,4000,$9,$9,$9,clock_timestamp())`,
		evaluationSetID, tenantID, domainID, "gate_"+suffix, releaseID, "gate-"+suffix,
		releaseHash, setHash, initiatorID)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.evaluation_cases(
		id,tenant_id,domain_id,evaluation_set_id,case_key,schema_version,question_hash,
		priority,answerable,expected_disposition,security_expectation,complexity,ambiguity,
		expected_ir_hash,expected_result_hash,content_hash,independent_review_count,
		created_by,content_updated_by,updated_by,shard_id
	) SELECT gen_random_uuid(),$1,$2,$3,'case-'||lpad(item::text,4,'0'),'gate-v1',
		encode(public.digest('question-'||item::text,'sha256'),'hex'),
		CASE WHEN item<=20 THEN 'P0' ELSE 'P1' END,
		item<=1980,
		CASE WHEN item<=1700 THEN 'DIRECT' WHEN item<=1880 THEN 'CLARIFY' ELSE 'REFUSE' END,
		CASE WHEN item>1980 THEN 'UNAUTHORIZED_BLOCK' ELSE 'NONE' END,
		'SIMPLE','NONE',
		CASE WHEN item<=1700 THEN encode(public.digest('ir-'||item::text,'sha256'),'hex') END,
		CASE WHEN item<=1700 THEN encode(public.digest('result-'||item::text,'sha256'),'hex') END,
		encode(public.digest('case-'||item::text,'sha256'),'hex'),2,$4,$4,$4,
		((item-1)%4+1)::smallint
	FROM generate_series(1,2000) AS item`, tenantID, domainID, evaluationSetID, initiatorID)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.evaluation_case_reviews(
		tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,reviewer_id,
		decision,reviewed_case_content_hash,review_hash
	) SELECT evaluation_case.tenant_id,evaluation_case.domain_id,evaluation_case.evaluation_set_id,
		evaluation_case.id,reviewer.slot,reviewer.actor_id,'APPROVED',evaluation_case.content_hash,
		encode(public.digest(evaluation_case.id::text||':'||reviewer.slot::text,'sha256'),'hex')
	FROM askdata.evaluation_cases AS evaluation_case
	CROSS JOIN (VALUES(1::smallint,$4::uuid),(2::smallint,$5::uuid)) AS reviewer(slot,actor_id)
	WHERE evaluation_case.tenant_id=$1 AND evaluation_case.domain_id=$2
		AND evaluation_case.evaluation_set_id=$3`, tenantID, domainID, evaluationSetID, semanticOwnerID, dataOwnerID)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.evaluation_runs(
		tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,evaluation_case_id,
		evaluation_set_content_hash,case_content_hash,release_id,semantic_version,
		release_content_hash,evaluation_mode,runner_version,run_key_hash,
		warehouse_snapshot_hash,warehouse_freshness_at,status,expected_disposition,
		actual_disposition,expected_ir_hash,actual_ir_hash,expected_result_hash,
		actual_result_hash,ir_equivalent,result_equivalent,strict_correct,
		security_passed,sensitive_leak_detected,duration_ms
	) SELECT evaluation_case.tenant_id,evaluation_case.domain_id,$4::uuid,evaluation_case.evaluation_set_id,
		evaluation_case.id,$5,evaluation_case.content_hash,$6,$7,$8,
		'END_TO_END_RESULT_EQUIVALENCE','gate-integration-v1',
		encode(public.digest(evaluation_case.id::text||':'||$4::uuid::text,'sha256'),'hex'),
		$9,statement_timestamp(),'PASSED',evaluation_case.expected_disposition,
		evaluation_case.expected_disposition,evaluation_case.expected_ir_hash,
		evaluation_case.expected_ir_hash,evaluation_case.expected_result_hash,
		evaluation_case.expected_result_hash,evaluation_case.expected_ir_hash IS NOT NULL,
		evaluation_case.expected_result_hash IS NOT NULL,true,true,false,1
	FROM askdata.evaluation_cases AS evaluation_case
	WHERE evaluation_case.tenant_id=$1 AND evaluation_case.domain_id=$2
		AND evaluation_case.evaluation_set_id=$3`, tenantID, domainID, evaluationSetID,
		batchID, setHash, releaseID, "gate-"+suffix, releaseHash, warehouseHash)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.evaluation_narrative_results(
		tenant_id,domain_id,evaluation_set_id,evaluation_batch_id,evaluation_case_id,
		release_id,release_content_hash,passed,evidence_hash
	) SELECT evaluation_case.tenant_id,evaluation_case.domain_id,evaluation_case.evaluation_set_id,
		$4,evaluation_case.id,$5,$6,true,
		encode(public.digest('narrative-'||evaluation_case.id::text,'sha256'),'hex')
	FROM askdata.evaluation_cases AS evaluation_case
	WHERE evaluation_case.tenant_id=$1 AND evaluation_case.domain_id=$2
		AND evaluation_case.evaluation_set_id=$3`, tenantID, domainID, evaluationSetID, batchID, releaseID, releaseHash)
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='origin'`)
	// Bulk fixture loading bypasses normal autovacuum statistics. Refresh the
	// four gate relations so this test exercises the production query plan
	// instead of a pathological zero-row estimate.
	mustExecGateFixture(t, ctx, tx, `ANALYZE askdata.evaluation_cases,askdata.evaluation_case_reviews,
		askdata.evaluation_runs,askdata.evaluation_narrative_results`)
	setGateActor(t, ctx, tx, tenantID, domainID, initiatorID)

	var plannedShards []int16
	if err := tx.QueryRow(ctx, `SELECT askdata.plan_evaluation_batch($1,$2,'FIRST_95_CLAIM',$3)`,
		evaluationSetID, batchID, initiatorID).Scan(&plannedShards); err != nil {
		t.Fatalf("plan_evaluation_batch: %v", err)
	}
	if len(plannedShards) != 4 {
		t.Fatalf("planned shards = %v", plannedShards)
	}
	var budgetHash string
	if err := tx.QueryRow(ctx, `SELECT askdata.record_release_error_budget(
		$1,$2,$3,'{"residualTarget":0.038,"lines":[{"stage":"INTENT","errorRate":0.01,"budget":0.02,"recoveryRate":0.5,"recoveryMeasured":true}]}'::jsonb,$4
	)`, releaseID, evaluationSetID, batchID, initiatorID).Scan(&budgetHash); err != nil {
		t.Fatalf("record_release_error_budget: %v", err)
	}
	if len(budgetHash) != 64 {
		t.Fatalf("budget hash = %q", budgetHash)
	}

	passed, gateHash, failures := recomputePostgresGate(t, ctx, tx, releaseID, evaluationSetID, batchID, initiatorID)
	if !passed || len(gateHash) != 64 || len(failures) != 0 {
		t.Fatalf("passing gate = %v %q %v", passed, gateHash, failures)
	}

	// A caller summary cannot hide a leaked run fact: recomputation observes the
	// row directly and emits stable security failures.
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='replica'`)
	mustExecGateFixture(t, ctx, tx, `UPDATE askdata.evaluation_runs SET
		status='FAILED',strict_correct=false,security_passed=false,
		sensitive_leak_detected=true,failure_stage='SECURITY',failure_code='SENSITIVE_LEAK'
	WHERE tenant_id=$1 AND evaluation_batch_id=$2 AND evaluation_case_id=(
		SELECT evaluation_case_id FROM askdata.evaluation_runs
		WHERE tenant_id=$1 AND evaluation_batch_id=$2 ORDER BY evaluation_case_id LIMIT 1
	)`, tenantID, batchID)
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='origin'`)
	passed, failedGateHash, failures := recomputePostgresGate(t, ctx, tx, releaseID, evaluationSetID, batchID, initiatorID)
	if passed || !containsGateFailure(failures, "EVAL_SENSITIVE_LEAK") {
		t.Fatalf("leak gate = %v %v", passed, failures)
	}
	var failedReviewHash string
	if err := tx.QueryRow(ctx, `SELECT askdata.record_release_review_report(
		$1,$2,$3,$4,'REJECT','{"failureClusters":["EVAL_SENSITIVE_LEAK"],"evidenceReceipts":[]}'::jsonb,$5
	)`, releaseID, evaluationSetID, batchID, failedGateHash, initiatorID).Scan(&failedReviewHash); err != nil || len(failedReviewHash) != 64 {
		t.Fatalf("failed-gate review report: hash=%q err=%v", failedReviewHash, err)
	}
	mustExecGateFixture(t, ctx, tx, `SAVEPOINT failed_gate_approve`)
	if _, err := tx.Exec(ctx, `SELECT askdata.record_release_review_report(
		$1,$2,$3,$4,'APPROVE','{"evidenceReceipts":[]}'::jsonb,$5
	)`, releaseID, evaluationSetID, batchID, failedGateHash, initiatorID); err == nil || !strings.Contains(err.Error(), "RELEASE_REVIEW_GATE_CONFLICT") {
		t.Fatalf("failed gate was allowed to recommend approval: %v", err)
	}
	mustExecGateFixture(t, ctx, tx, `ROLLBACK TO SAVEPOINT failed_gate_approve`)
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='replica'`)
	mustExecGateFixture(t, ctx, tx, `UPDATE askdata.evaluation_runs SET
		status='PASSED',strict_correct=true,security_passed=true,
		sensitive_leak_detected=false,failure_stage='',failure_code=''
	WHERE tenant_id=$1 AND evaluation_batch_id=$2`, tenantID, batchID)
	mustExecGateFixture(t, ctx, tx, `SET LOCAL session_replication_role='origin'`)
	passed, gateHash, failures = recomputePostgresGate(t, ctx, tx, releaseID, evaluationSetID, batchID, initiatorID)
	if !passed || len(failures) != 0 {
		t.Fatalf("restored gate = %v %v", passed, failures)
	}

	var reportHash string
	if err := tx.QueryRow(ctx, `SELECT askdata.record_release_review_report(
		$1,$2,$3,$4,'APPROVE','{"impact":{"changedObjects":1},"risks":[],"evidenceReceipts":[]}'::jsonb,$5
	)`, releaseID, evaluationSetID, batchID, gateHash, initiatorID).Scan(&reportHash); err != nil {
		t.Fatalf("record_release_review_report: %v", err)
	}
	if len(reportHash) != 64 {
		t.Fatalf("review report hash = %q", reportHash)
	}
	commentHash := strings.Repeat("d", 64)
	setGateActor(t, ctx, tx, tenantID, domainID, semanticOwnerID)
	if _, err := tx.Exec(ctx, `SELECT askdata.submit_release_approval(
		$1,$2,$3,$4,'SEMANTIC_OWNER','APPROVED',$5,$6
	)`, releaseID, evaluationSetID, batchID, gateHash, commentHash, semanticOwnerID); err != nil {
		t.Fatalf("semantic approval: %v", err)
	}
	mustExecGateFixture(t, ctx, tx, `SAVEPOINT duplicate_release_approver`)
	if _, err := tx.Exec(ctx, `SELECT askdata.submit_release_approval(
		$1,$2,$3,$4,'DATA_OWNER','APPROVED',$5,$6
	)`, releaseID, evaluationSetID, batchID, gateHash, commentHash, semanticOwnerID); err == nil || !strings.Contains(err.Error(), "RELEASE_APPROVAL_DUTY_SEPARATION") {
		t.Fatalf("same reviewer occupied both slots: %v", err)
	}
	mustExecGateFixture(t, ctx, tx, `ROLLBACK TO SAVEPOINT duplicate_release_approver`)
	setGateActor(t, ctx, tx, tenantID, domainID, dataOwnerID)
	if _, err := tx.Exec(ctx, `SELECT askdata.submit_release_approval(
		$1,$2,$3,$4,'DATA_OWNER','APPROVED',$5,$6
	)`, releaseID, evaluationSetID, batchID, gateHash, commentHash, dataOwnerID); err != nil {
		t.Fatalf("data approval: %v", err)
	}

	setGateActor(t, ctx, tx, tenantID, domainID, initiatorID)
	var activated bool
	var activeID, gateReceipt string
	var supersededID *string
	var stateVersion int64
	var activationFailures []string
	if err := tx.QueryRow(ctx, `SELECT * FROM askdata.activate_release($1,$2,$3,$4,1)`,
		releaseID, evaluationSetID, batchID, initiatorID).Scan(
		&activated, &activeID, &supersededID, &stateVersion, &gateReceipt, &activationFailures,
	); err != nil {
		t.Fatalf("activate_release: %v", err)
	}
	if !activated || activeID != releaseID || supersededID != nil || stateVersion != 2 || gateReceipt != gateHash || len(activationFailures) != 0 {
		t.Fatalf("activation = %v %s %v %d %s %v", activated, activeID, supersededID, stateVersion, gateReceipt, activationFailures)
	}
	if err := tx.QueryRow(ctx, `SELECT activation_succeeded,failure_codes
		FROM askdata.activate_release($1,$2,$3,$4,1)`, releaseID, evaluationSetID,
		batchID, initiatorID).Scan(&activated, &activationFailures); err != nil {
		t.Fatalf("stale concurrent activation: %v", err)
	}
	if activated || !containsGateFailure(activationFailures, "RELEASE_STATE_VERSION_CONFLICT") {
		t.Fatalf("stale activation = %v %v", activated, activationFailures)
	}

	// OPS-006 uses an aggregate-only SECURITY DEFINER view so a user can enforce
	// tenant/domain limits without being able to read another actor's raw cost
	// rows. Cost facts are idempotent and pinned to the exact active run.
	runID, costRecordID := uuid.NewString(), uuid.NewString()
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.question_runs(
		id,tenant_id,domain_id,actor_id,idempotency_key_hash,question_hash,
		policy_scope_hash,release_id,release_content_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, runID, tenantID, domainID, initiatorID,
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), releaseID, releaseHash)
	mustExecGateFixture(t, ctx, tx, `INSERT INTO askdata.quotas(
		tenant_id,scope_type,scope_id,period,llm_token_limit,run_limit,cost_limit_cents
	) VALUES
		($1,'TENANT',$1,'MONTH',1000,100,100),
		($1,'DOMAIN',$2,'DAY',500,50,50),
		($1,'USER',$3,'DAY',100,10,10),
		($1,'RUN',$1,'RUN',30,1,2),
		($1,'RUN',$4,'RUN',40,2,4)`, tenantID, domainID, initiatorID, runID)
	provisionalRunID := uuid.NewString()
	var provisionalScopes, provisionalRunScopes int
	var provisionalRunScopeID string
	if err := tx.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER(WHERE scope_type='RUN'),
		max(scope_id::text) FILTER(WHERE scope_type='RUN')
		FROM askdata.load_quota_usage_snapshots($1,$2,$3,clock_timestamp())`,
		domainID, initiatorID, provisionalRunID,
	).Scan(&provisionalScopes, &provisionalRunScopes, &provisionalRunScopeID); err != nil {
		t.Fatalf("load provisional quota snapshots: %v", err)
	}
	if provisionalScopes != 4 || provisionalRunScopes != 1 || provisionalRunScopeID != provisionalRunID {
		t.Fatalf("provisional quota scopes=%d runScopes=%d runScopeID=%s", provisionalScopes, provisionalRunScopes, provisionalRunScopeID)
	}
	var costInserted bool
	if err := tx.QueryRow(ctx, `SELECT askdata.record_cost_usage(
		$1,$2,$3,$4,'SINGLE_QUERY_COMPLEX','openai','governed-model',12,8,3,0
	)`, costRecordID, runID, domainID, initiatorID).Scan(&costInserted); err != nil || !costInserted {
		t.Fatalf("record cost usage: inserted=%v err=%v", costInserted, err)
	}
	if err := tx.QueryRow(ctx, `SELECT askdata.record_cost_usage(
		$1,$2,$3,$4,'SINGLE_QUERY_COMPLEX','openai','governed-model',12,8,3,0
	)`, costRecordID, runID, domainID, initiatorID).Scan(&costInserted); err != nil || costInserted {
		t.Fatalf("idempotent cost replay: inserted=%v err=%v", costInserted, err)
	}
	mustExecGateFixture(t, ctx, tx, `SAVEPOINT conflicting_cost_replay`)
	if _, err := tx.Exec(ctx, `SELECT askdata.record_cost_usage(
		$1,$2,$3,$4,'SINGLE_QUERY_COMPLEX','openai','governed-model',13,8,3,0
	)`, costRecordID, runID, domainID, initiatorID); err == nil || !strings.Contains(err.Error(), "ASKDATA_COST_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflicting cost replay was not rejected: %v", err)
	}
	mustExecGateFixture(t, ctx, tx, `ROLLBACK TO SAVEPOINT conflicting_cost_replay`)
	rows, err := tx.Query(ctx, `SELECT scope_type,llm_tokens_used,runs_used,cost_cents_used
		FROM askdata.load_quota_usage_snapshots($1,$2,$3,clock_timestamp())`, domainID, initiatorID, runID)
	if err != nil {
		t.Fatalf("load quota snapshots: %v", err)
	}
	defer rows.Close()
	quotaRows := 0
	for rows.Next() {
		var scope string
		var tokens, runs, cost int64
		if err := rows.Scan(&scope, &tokens, &runs, &cost); err != nil {
			t.Fatal(err)
		}
		if tokens != 20 || runs != 1 || cost != 3 {
			t.Fatalf("quota %s usage = tokens:%d runs:%d cost:%d", scope, tokens, runs, cost)
		}
		quotaRows++
	}
	if err := rows.Err(); err != nil || quotaRows != 4 {
		t.Fatalf("quota snapshot rows=%d err=%v", quotaRows, err)
	}
}

func mustExecGateFixture(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func setGateActor(t *testing.T, ctx context.Context, tx pgx.Tx, tenantID, domainID, actorID string) {
	t.Helper()
	mustExecGateFixture(t, ctx, tx, `SELECT
		set_config('app.tenant_id',$1,true),set_config('app.domain_id',$2,true),
		set_config('app.user_id',$3,true),set_config('app.access_mode','USER',true)`,
		tenantID, domainID, actorID)
}

func recomputePostgresGate(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	releaseID, evaluationSetID, batchID, actorID string,
) (bool, string, []string) {
	t.Helper()
	var passed bool
	var receiptHash string
	var failures []string
	var facts []byte
	if err := tx.QueryRow(ctx, `SELECT * FROM askdata.recompute_release_evaluation_gate($1,$2,$3,$4)`,
		releaseID, evaluationSetID, batchID, actorID).Scan(&passed, &receiptHash, &failures, &facts); err != nil {
		t.Fatalf("recompute_release_evaluation_gate: %v", err)
	}
	if len(facts) == 0 || !strings.Contains(string(facts), `"databaseRecomputed": true`) {
		t.Fatalf("gate facts are not database recomputed: %s", facts)
	}
	return passed, receiptHash, failures
}

func containsGateFailure(failures []string, expected string) bool {
	for _, failure := range failures {
		if failure == expected {
			return true
		}
	}
	return false
}
