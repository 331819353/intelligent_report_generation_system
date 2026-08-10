package decision

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/platform/database"
)

type decisionE2EFixture struct {
	tenantID, ownerID, approverID, domainID, otherDomainID string
	ownerRoleID, approverRoleID, releaseID, releaseHash    string
}

type capturedOutcomeRunner struct {
	actorID askdata.ID
	value   string
}

func (runner *capturedOutcomeRunner) Refresh(_ context.Context, identity Identity, _ OutcomeMetric) (OutcomeRefresh, error) {
	runner.actorID = identity.ActorID
	return OutcomeRefresh{
		Value: runner.value, ResultHash: askdata.HashBytes([]byte("current outcome")),
		PolicyScopeHash: askdata.HashBytes([]byte("current viewer policy")),
		AsOf:            time.Now().UTC().Truncate(time.Microsecond), Status: "SUCCEEDED",
	}, nil
}

func TestPostgresAnswerDecisionActionOutcomeCloseE2E(t *testing.T) {
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

	fixture := createDecisionE2EFixture(t, ctx, adminPool)
	defer cleanupDecisionE2EFixture(t, adminPool, fixture.tenantID)
	ownerScope := decisionE2EScope(t, fixture, fixture.ownerID, fixture.ownerRoleID)
	ownerContext := database.WithAccessContext(ctx, fixture.ownerID, fixture.domainID)
	questionStore := orchestrator.NewPostgresStore(appPool)
	createdRun, err := questionStore.CreateRun(ownerContext, orchestrator.CreateRunRequest{
		Scope: ownerScope, DomainID: askdata.ID(fixture.domainID), ConversationID: askdata.ID(uuid.NewString()),
		IdempotencyKeyHash: askdata.HashBytes([]byte("decision e2e idempotency")),
		QuestionHash:       askdata.HashBytes([]byte("decision e2e governed question")),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := advanceDecisionE2ERun(t, ownerContext, questionStore, ownerScope, createdRun.Run)
	dataAsOf := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Microsecond)
	resultSummary, _ := json.Marshal(map[string]any{
		"resolvedTimeSpec": map[string]string{"dataAvailableThrough": dataAsOf.Format(time.RFC3339Nano)},
	})
	if _, err = questionStore.RecordArtifact(ownerContext, orchestrator.RecordArtifactRequest{
		Scope: ownerScope, DomainID: run.DomainID, RunID: run.ID, ExpectedRunVersion: run.RecordVersion,
		Type: orchestrator.ArtifactResultSummary, SchemaVersion: "decision-e2e-result-v1", Payload: resultSummary,
	}); err != nil {
		t.Fatal(err)
	}
	run = transitionDecisionE2ERun(t, ownerContext, questionStore, ownerScope, run, orchestrator.StateAnswerVerifying, orchestrator.HashUpdates{}, nil)
	answerPayload := decisionE2EAnswerPayload(t, run, askdata.ID(fixture.releaseID))
	run = transitionDecisionE2ERun(t, ownerContext, questionStore, ownerScope, run, orchestrator.StateAnswered, orchestrator.HashUpdates{}, &orchestrator.CompletionArtifactInput{
		Code: "ANSWER_DEGRADED", Type: orchestrator.ArtifactAnswer, SchemaVersion: answer.SchemaVersion,
		EvidenceIDs: []askdata.ID{"decision-e2e-evidence"}, Payload: answerPayload,
	})
	snapshot, err := questionStore.Resume(ownerContext, orchestrator.ResumeRequest{Scope: ownerScope, DomainID: run.DomainID, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	var answerFact orchestrator.Artifact
	for _, artifact := range snapshot.Artifacts {
		if artifact.Type == orchestrator.ArtifactAnswer {
			answerFact = artifact
		}
	}
	if answerFact.ID == "" || run.CompletedAt == nil {
		t.Fatalf("answer completion was not persisted: %#v", snapshot)
	}

	store := NewPostgresStore(appPool)
	outcomes := &capturedOutcomeRunner{value: "125"}
	service, err := NewService(store, NewPostgresEvidenceVerifier(appPool), outcomes)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the review timestamp unambiguously behind both the database and test
	// process clocks; worker discovery deliberately uses database now().
	base := time.Now().UTC().Add(-48 * time.Hour)
	service.now = func() time.Time { return base }
	owner := Identity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.ownerID)}
	prefilledEvidence, err := service.PrefillEvidence(ownerContext, owner, SourceAnswerArtifact, answerFact.ID)
	if err != nil || prefilledEvidence.SourceHash != answerFact.Hash || prefilledEvidence.SemanticReleaseID != askdata.ID(fixture.releaseID) ||
		!prefilledEvidence.AsOf.Equal(dataAsOf) || prefilledEvidence.PolicyScopeHash != ownerScope.PolicyHash {
		t.Fatalf("server evidence prefill = %#v, %v", prefilledEvidence, err)
	}
	aggregate, err := service.Create(ownerContext, owner, CreateInput{
		OwnerUserID: askdata.ID(fixture.ownerID), Title: "验证后的经营决策", Question: "是否调整资源投入？",
		Decision: "执行受控调整", ExpectedEffect: "指标改善", Risks: []string{"短期波动"},
		EvidenceMode: EvidencePlatformVerified, ApprovalPolicyID: "decision-e2e-single",
		ReviewAt: base.Add(time.Hour), Options: []OptionInput{{Title: "执行", Selected: true}, {Title: "保持"}},
		Evidence: []EvidenceInput{prefilledEvidence},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Evidence) != 1 || !aggregate.Evidence[0].Verified || !aggregate.Evidence[0].AsOf.Equal(dataAsOf) ||
		aggregate.Decision.Status != StatusDraft {
		t.Fatalf("created decision = %#v", aggregate)
	}
	if _, err = service.Create(ownerContext, owner, CreateInput{
		OwnerUserID: owner.ActorID, Title: "手工备选决策", Question: "是否保留现状？", Decision: "", ExpectedEffect: "",
		Risks: []string{}, EvidenceMode: EvidenceManual, ApprovalPolicyID: "decision-e2e-single",
		ReviewAt: base.Add(2 * time.Hour), Options: []OptionInput{}, Evidence: []EvidenceInput{},
	}); err != nil {
		t.Fatalf("create second decision: %v", err)
	}

	aggregate, err = service.Submit(ownerContext, owner, aggregate.Decision.ID, aggregate.Decision.RecordVersion)
	if err != nil || aggregate.Decision.Status != StatusInReview || len(aggregate.Approvals) != 1 {
		t.Fatalf("submitted decision = %#v, %v", aggregate, err)
	}
	ownerPage, err := service.ListDetailed(ownerContext, owner, ListQuery{Scope: "MINE", Search: "经营", Status: StatusInReview, EvidenceMode: EvidencePlatformVerified, Sort: "UPDATED_DESC", Limit: 1})
	if err != nil || ownerPage.Total != 1 || len(ownerPage.Items) != 1 || ownerPage.ScopeCounts.Mine != 2 ||
		ownerPage.Items[0].EvidenceCount != 1 || ownerPage.Items[0].VerifiedEvidenceCount != 1 || ownerPage.Items[0].OwnerDisplayName == "" {
		t.Fatalf("owner decision list = %#v, %v", ownerPage, err)
	}
	firstListPage, err := service.ListDetailed(ownerContext, owner, ListQuery{Scope: "MINE", Sort: "UPDATED_DESC", Limit: 1})
	if err != nil || firstListPage.Total != 2 || len(firstListPage.Items) != 1 || firstListPage.NextCursor == "" {
		t.Fatalf("first paginated decision list = %#v, %v", firstListPage, err)
	}
	secondListPage, err := service.ListDetailed(ownerContext, owner, ListQuery{Scope: "MINE", Sort: "UPDATED_DESC", Limit: 1, Cursor: firstListPage.NextCursor})
	if err != nil || secondListPage.Total != 2 || len(secondListPage.Items) != 1 || secondListPage.Items[0].ID == firstListPage.Items[0].ID {
		t.Fatalf("second paginated decision list = %#v, %v", secondListPage, err)
	}
	policies, err := service.ListApprovalPolicies(ownerContext, owner)
	if err != nil || len(policies) != 1 || policies[0].ID != "decision-e2e-single" || policies[0].ApproverSummary == "" {
		t.Fatalf("approval policy directory = %#v, %v", policies, err)
	}
	if _, err = service.ListDetailed(ownerContext, owner, ListQuery{DecisionType: "UNCONFIRMED", Sort: "UPDATED_DESC", Limit: 20}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconfirmed decision type error = %v", err)
	}
	approver := Identity{TenantID: owner.TenantID, DomainID: owner.DomainID, ActorID: askdata.ID(fixture.approverID)}
	approverContext := database.WithAccessContext(ctx, fixture.approverID, fixture.domainID)
	approvalPage, err := service.ListDetailed(approverContext, approver, ListQuery{Scope: "APPROVALS", Sort: "REVIEW_ASC", Limit: 20})
	if err != nil || approvalPage.Total != 1 || approvalPage.ScopeCounts.Approvals != 1 || len(approvalPage.Items) != 1 {
		t.Fatalf("approver decision list = %#v, %v", approvalPage, err)
	}
	aggregate, err = service.DecideApproval(approverContext, approver, aggregate.Decision.ID, aggregate.Decision.RecordVersion, true, "同意")
	if err != nil || aggregate.Decision.Status != StatusApproved || aggregate.Approvals[0].Status != "APPROVED" {
		t.Fatalf("approved decision = %#v, %v", aggregate, err)
	}

	action, err := service.CreateAction(ownerContext, owner, aggregate.Decision.ID, CreateActionInput{
		Title: "执行调整", Description: "按已批准边界执行", AssigneeUserID: owner.ActorID,
		DueAt: time.Now().UTC().Add(2 * time.Hour), DeliverableRefs: []string{"artifact://decision-e2e/plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action, err = service.TransitionAction(ownerContext, owner, aggregate.Decision.ID, action.ID, TransitionActionInput{ExpectedVersion: action.RecordVersion, Target: ActionDoing})
	if err != nil {
		t.Fatal(err)
	}
	action, err = service.TransitionAction(ownerContext, owner, aggregate.Decision.ID, action.ID, TransitionActionInput{
		ExpectedVersion: action.RecordVersion, Target: ActionDone, CompletionEvidence: "artifact://decision-e2e/completed",
	})
	if err != nil || action.Status != ActionDone {
		t.Fatalf("completed action = %#v, %v", action, err)
	}

	metricVersionID, modelVersionID := askdata.ID(uuid.NewString()), askdata.ID(uuid.NewString())
	semanticIR := ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: askdata.ID(fixture.releaseID),
		SemanticContentHash: askdata.ContentHash(fixture.releaseHash), DomainID: owner.DomainID,
		ModelVersionID: modelVersionID, Metrics: []ir.Metric{{MetricVersionID: metricVersionID, Alias: "metric_value"}},
		GroupBy: []ir.GroupBy{}, Filters: []ir.Filter{}, Sort: []ir.Sort{}, Limit: 1,
		OtherPolicy: ir.OtherNone, TieBreaking: ir.TieDeterministicCut,
	}
	_, semanticRaw, semanticHash, err := ir.Canonicalize(semanticIR)
	if err != nil {
		t.Fatal(err)
	}
	metric, err := service.AddOutcomeMetric(ownerContext, owner, aggregate.Decision.ID, AddMetricInput{
		MetricVersionID: string(metricVersionID), SemanticIR: semanticRaw, SemanticIRHash: semanticHash,
		SemanticReleaseID: askdata.ID(fixture.releaseID), SemanticReleaseHash: askdata.ContentHash(fixture.releaseHash),
		BaselineValue: "120", TargetDirection: DirectionIncrease, ReviewAt: base.Add(time.Hour), AttributionNote: "仅作为相关性复盘",
	})
	if err != nil || metric.RefreshStatus != "PENDING" {
		t.Fatalf("outcome metric = %#v, %v", metric, err)
	}
	refreshed, err := service.RefreshOutcome(ownerContext, owner, aggregate.Decision.ID)
	if err != nil || len(refreshed) != 1 || refreshed[0].RefreshStatus != "SUCCEEDED" || outcomes.actorID != owner.ActorID {
		t.Fatalf("outcome refresh = %#v, actor=%s, %v", refreshed, outcomes.actorID, err)
	}

	beforeDue, err := service.Get(ownerContext, owner, aggregate.Decision.ID, false)
	if err != nil || beforeDue.Decision.Status != StatusInExecution {
		t.Fatalf("pre-worker decision = %#v, %v", beforeDue, err)
	}
	var discovered int
	if err = appPool.QueryRow(ctx, `SELECT count(*) FROM decision.list_work_tenants()`).Scan(&discovered); err != nil || discovered < 1 {
		t.Fatalf("decision worker discovery = %d, %v; reviewAt=%s", discovered, err, beforeDue.Decision.ReviewAt)
	}
	service.now = time.Now
	processed, err := service.ProcessDue(database.WithoutAccessContext(ctx), 100)
	if err != nil || processed < 1 {
		t.Fatalf("due processing = %d, %v", processed, err)
	}
	aggregate, err = service.Get(ownerContext, owner, aggregate.Decision.ID, true)
	if err != nil || aggregate.Decision.Status != StatusReviewDue {
		t.Fatalf("review-due decision = %#v, %v", aggregate, err)
	}
	review, err := service.ConfirmOutcome(ownerContext, owner, aggregate.Decision.ID, ConfirmOutcomeInput{
		ExpectedVersion: 1, Conclusion: ConclusionAchieved, Notes: "达到预期，仍不声称因果关系",
	})
	if err != nil || review.Status != ReviewConfirmed {
		t.Fatalf("confirmed review = %#v, %v", review, err)
	}
	aggregate, err = service.Close(ownerContext, owner, aggregate.Decision.ID, aggregate.Decision.RecordVersion, "")
	if err != nil || aggregate.Decision.Status != StatusClosed {
		t.Fatalf("closed decision = %#v, %v", aggregate, err)
	}

	wrongDomain := owner
	wrongDomain.DomainID = askdata.ID(fixture.otherDomainID)
	wrongContext := database.WithAccessContext(ctx, fixture.ownerID, fixture.otherDomainID)
	if _, err = service.Get(wrongContext, wrongDomain, aggregate.Decision.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-domain decision read error = %v", err)
	}
}

func advanceDecisionE2ERun(t *testing.T, ctx context.Context, store *orchestrator.PostgresStore, scope askdata.PolicyScope, run orchestrator.Run) orchestrator.Run {
	t.Helper()
	understanding, binding := askdata.HashBytes([]byte("understanding")), askdata.HashBytes([]byte("binding"))
	graph, semantic := askdata.HashBytes([]byte("graph")), askdata.HashBytes([]byte("semantic ir"))
	query, result := askdata.HashBytes([]byte("query plan")), askdata.HashBytes([]byte("result"))
	steps := []struct {
		state  orchestrator.State
		hashes orchestrator.HashUpdates
	}{
		{orchestrator.StateAuthorized, orchestrator.HashUpdates{}},
		{orchestrator.StateContextReady, orchestrator.HashUpdates{}},
		{orchestrator.StateUnderstanding, orchestrator.HashUpdates{Understanding: &understanding}},
		{orchestrator.StateRetrieving, orchestrator.HashUpdates{}},
		{orchestrator.StateBinding, orchestrator.HashUpdates{BindingBundle: &binding}},
		{orchestrator.StateGraphValidating, orchestrator.HashUpdates{GraphPlan: &graph}},
		{orchestrator.StateIRReady, orchestrator.HashUpdates{SemanticIR: &semantic}},
		{orchestrator.StatePlanValidating, orchestrator.HashUpdates{QueryPlan: &query}},
		{orchestrator.StateExecuting, orchestrator.HashUpdates{}},
		{orchestrator.StateResultVerifying, orchestrator.HashUpdates{Result: &result}},
	}
	for _, step := range steps {
		run = transitionDecisionE2ERun(t, ctx, store, scope, run, step.state, step.hashes, nil)
	}
	return run
}

func transitionDecisionE2ERun(t *testing.T, ctx context.Context, store *orchestrator.PostgresStore, scope askdata.PolicyScope, run orchestrator.Run, target orchestrator.State, hashes orchestrator.HashUpdates, completion *orchestrator.CompletionArtifactInput) orchestrator.Run {
	t.Helper()
	result, err := store.Transition(ctx, orchestrator.TransitionRequest{
		Scope: scope, DomainID: run.DomainID, RunID: run.ID, ExpectedVersion: run.RecordVersion,
		TargetState: target, Usage: run.Usage, Hashes: hashes, Completion: completion,
	})
	if err != nil {
		t.Fatalf("question transition %s -> %s: %v", run.State, target, err)
	}
	return result.Run
}

func decisionE2EAnswerPayload(t *testing.T, run orchestrator.Run, releaseID askdata.ID) json.RawMessage {
	t.Helper()
	artifact := answer.AnswerArtifact{
		SchemaVersion: answer.SchemaVersion, RunID: run.ID,
		Layers: answer.AnswerLayers{Structured: answer.StructuredLayer{
			Headline: &answer.MetricValue{MetricVersionID: askdata.ID(uuid.NewString()), Value: "120", Unit: "COUNT", Label: "核心指标", ColumnKey: "metric_value"},
			Cards:    []answer.MetricValue{}, TableRef: "result:decision-e2e",
		}, Narrative: answer.NarrativeLayer{Findings: []string{}, Citations: nil}},
		Verification: answer.Verification{VerifierVersion: "decision-e2e-v1", PolicyWordlistVersion: "decision-e2e-v1", Attempts: 1, Degraded: true},
		Provenance: answer.Provenance{
			PromptVersion: "decision-e2e-v1", ModelPolicy: "degraded-structured-only",
			EvidenceHash: askdata.HashBytes([]byte("answer evidence")), ResultHash: run.Hashes.Result,
			SemanticReleaseID: releaseID, ChartRuleVersion: "decision-e2e-v1",
		},
	}
	raw, err := artifact.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decisionE2EScope(t *testing.T, fixture decisionE2EFixture, actorID, roleID string) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		askdata.ID(fixture.tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(fixture.domainID)},
		[]askdata.ID{askdata.ID(roleID)}, askdata.ReleaseRef{ReleaseID: askdata.ID(fixture.releaseID), ContentHash: askdata.ContentHash(fixture.releaseHash)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func createDecisionE2EFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) decisionE2EFixture {
	t.Helper()
	fixture := decisionE2EFixture{releaseHash: string(askdata.HashBytes([]byte("decision e2e release")))}
	suffix := uuid.NewString()[:8]
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = tx.QueryRow(ctx, `INSERT INTO platform.tenants(code,name) VALUES($1,$2) RETURNING id::text`, "dece2e_"+suffix, "decision e2e "+suffix).Scan(&fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.access_mode','SYSTEM',true),set_config('app.user_id','',true),set_config('app.domain_id','',true)`, fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	insertUser := func(prefix string) string {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,employee_no,email,display_name,password_hash,status)
			VALUES($1,$2,$3,$4,'integration-only-not-a-login-secret','ACTIVE') RETURNING id::text`, fixture.tenantID, prefix+suffix, prefix+"."+suffix+"@example.invalid", prefix+suffix).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	fixture.ownerID, fixture.approverID = insertUser("DECO"), insertUser("DECA")
	if err = tx.QueryRow(ctx, `INSERT INTO platform.business_domains(tenant_id,code,name,is_default,created_by) VALUES($1,$2,$3,true,$4) RETURNING id::text`, fixture.tenantID, "dece2e_"+suffix, "decision e2e", fixture.ownerID).Scan(&fixture.domainID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO platform.business_domains(tenant_id,code,name,is_default,created_by) VALUES($1,$2,$3,false,$4) RETURNING id::text`, fixture.tenantID, "decother_"+suffix, "decision other", fixture.ownerID).Scan(&fixture.otherDomainID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,status,member_role,assigned_by)
		VALUES($1,$2,$3,'ACTIVE','MEMBER',$3),($1,$2,$4,'ACTIVE','MEMBER',$3),($1,$5,$3,'ACTIVE','MEMBER',$3)`, fixture.tenantID, fixture.domainID, fixture.ownerID, fixture.approverID, fixture.otherDomainID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id) VALUES($1,$2,$3,$4,$5)`, fixture.domainID, fixture.tenantID, "dece2e_"+suffix, "decision e2e", fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	insertRole := func(code string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name,status) VALUES($1,$2::citext,$2::text,'ACTIVE') RETURNING id::text`, fixture.tenantID, code+suffix).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	fixture.ownerRoleID, fixture.approverRoleID = insertRole("DEC_OWNER_"), insertRole("DEC_APPROVER_")
	if _, err = tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) VALUES($1,$2,$3,$2),($1,$4,$5,$2)`, fixture.tenantID, fixture.ownerID, fixture.ownerRoleID, fixture.approverID, fixture.approverRoleID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO askdata.releases(tenant_id,domain_id,semantic_version,content_hash,status,object_count,created_by,updated_by,activated_by,ready_at,activated_at)
		VALUES($1,$2,$3,$4,'ACTIVE',0,$5,$5,$5,now(),now()) RETURNING id::text`, fixture.tenantID, fixture.domainID, "dece2e-"+suffix, fixture.releaseHash, fixture.ownerID).Scan(&fixture.releaseID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"POSTGRES_REGISTRY", "SEARCH_INDEX", "NEBULA_GRAPH", "EXECUTION_SEMANTIC_LAYER"} {
		if _, err = tx.Exec(ctx, `INSERT INTO askdata.release_projections(tenant_id,domain_id,release_id,target,status,expected_content_hash,applied_content_hash,resource_version,completed_at)
			VALUES($1,$2,$3,$4,'READY',$5,$5,'decision-e2e',now())`, fixture.tenantID, fixture.domainID, fixture.releaseID, target, fixture.releaseHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO decision.approval_policies(id,tenant_id,domain_id,name,required_approvals,status,created_by)
		VALUES('decision-e2e-single',$1,$2,'decision e2e single approver',1,'ACTIVE',$3)`, fixture.tenantID, fixture.domainID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO decision.approval_policy_approvers(tenant_id,domain_id,policy_id,approver_user_id,sequence_no)
		VALUES($1,$2,'decision-e2e-single',$3,1)`, fixture.tenantID, fixture.domainID, fixture.approverID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupDecisionE2EFixture(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin decision cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	statements := []string{
		`DELETE FROM decision.decision_notifications WHERE tenant_id=$1`,
		`DELETE FROM decision.decision_events WHERE tenant_id=$1`,
		`DELETE FROM decision.outcome_reviews WHERE tenant_id=$1`,
		`DELETE FROM decision.outcome_metrics WHERE tenant_id=$1`,
		`DELETE FROM decision.action_events WHERE tenant_id=$1`,
		`DELETE FROM decision.action_items WHERE tenant_id=$1`,
		`DELETE FROM decision.decision_approval_events WHERE tenant_id=$1`,
		`DELETE FROM decision.decision_approvals WHERE tenant_id=$1`,
		`DELETE FROM decision.decision_evidence WHERE tenant_id=$1`,
		`DELETE FROM decision.decision_options WHERE tenant_id=$1`,
		`DELETE FROM decision.decisions WHERE tenant_id=$1`,
		`DELETE FROM decision.approval_policy_approvers WHERE tenant_id=$1`,
		`DELETE FROM decision.approval_policies WHERE tenant_id=$1`,
		`DELETE FROM askdata.question_run_events WHERE tenant_id=$1`,
		`DELETE FROM askdata.question_artifacts WHERE tenant_id=$1`,
		`DELETE FROM askdata.question_runs WHERE tenant_id=$1`,
		`DELETE FROM askdata.release_projection_artifacts WHERE tenant_id=$1`,
		`DELETE FROM askdata.release_projections WHERE tenant_id=$1`,
		`DELETE FROM askdata.release_events WHERE tenant_id=$1`,
		`DELETE FROM askdata.releases WHERE tenant_id=$1`,
		`DELETE FROM askdata.release_state WHERE tenant_id=$1`,
		`DELETE FROM askdata.domains WHERE tenant_id=$1`,
		`DELETE FROM platform.domain_memberships WHERE tenant_id=$1`,
		`DELETE FROM platform.user_roles WHERE tenant_id=$1`,
		`DELETE FROM platform.roles WHERE tenant_id=$1`,
		`DELETE FROM platform.business_domains WHERE tenant_id=$1`,
		`DELETE FROM platform.users WHERE tenant_id=$1`,
		`DELETE FROM platform.tenants WHERE id=$1`,
	}
	for _, statement := range statements {
		if _, err = tx.Exec(ctx, statement, tenantID); err != nil {
			t.Errorf("decision cleanup %q: %v", statement, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Errorf("commit decision cleanup: %v", err)
	}
}
