package reportai_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/reportai"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

func TestPostgresReportAIAuditInsightAppendOnlyAndRLS(t *testing.T) {
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

	fixture := createFixture(t, ctx, adminPool)
	defer cleanupFixture(t, adminPool, fixture)
	owner := reportstore.Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.ownerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	viewer := reportstore.Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.viewerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	crossTenant := reportstore.Identity{
		TenantID: askdata.ID(fixture.otherTenantID), ActorID: askdata.ID(fixture.otherOwnerID),
		DomainID: askdata.ID(fixture.otherDomainID),
	}
	reportID := askdata.ID(fixture.reportID)
	componentID := askdata.ID("component_sales_chart")
	definition := loadDefinition(t, fixture.reportID, fixture.code)
	if _, _, err := reportstore.NewPostgresStore(appPool).CreateReport(ctx, owner, reportstore.CreateInput{
		ID: reportID, Code: fixture.code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	}); err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.object_permissions(
		tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
	) VALUES($1,'USER',$2,'REPORT',$3,'VIEW',$4)`, fixture.tenantID, fixture.viewerID,
		fixture.reportID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}

	aiStore := reportai.NewPostgresStore(appPool)
	runs := make(map[reportai.RunKind]reportai.Run, 4)
	for _, kind := range []reportai.RunKind{
		reportai.RunPlan, reportai.RunGenerateDraft, reportai.RunScopedEdit, reportai.RunInsight,
	} {
		run, err := aiStore.StartRun(ctx, owner, reportai.StartRunInput{
			ReportID: reportID, Kind: kind, PromptVersion: "report-ai-v1",
			ModelPolicy: "governed-default",
			Summary: reportai.RequestSummary{
				Intent: "sales trend", SelectionIDs: []string{"component_sales_chart"},
				AvailableFields: []string{"month", "sales_amount"},
			},
		})
		if err != nil {
			t.Fatalf("StartRun(%s) error = %v", kind, err)
		}
		runs[kind] = run
	}
	validOperation := operation.Operation{
		Op: operation.PageUpdate, TargetID: definition.Pages[0].ID,
		Payload: &operation.PageUpdatePayload{Name: "AI preview"},
	}
	if err := aiStore.FinishRun(ctx, owner, runs[reportai.RunPlan].ID, reportai.RunSucceeded,
		map[string]any{"decision": "ready"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := aiStore.CompletePreview(ctx, owner, runs[reportai.RunGenerateDraft].ID,
		[]operation.Operation{validOperation}, map[string]any{"operationCount": 1}); err != nil {
		t.Fatal(err)
	}
	if err := aiStore.RejectPreview(ctx, owner, runs[reportai.RunScopedEdit].ID,
		[]operation.Operation{validOperation}, "REPORT_AI_SCOPE_REJECTED"); err != nil {
		t.Fatal(err)
	}
	if err := aiStore.FinishRun(ctx, owner, runs[reportai.RunInsight].ID, reportai.RunFailed,
		map[string]any{"stage": "verification"}, "REPORT_AI_VERIFICATION_FAILED"); err != nil {
		t.Fatal(err)
	}

	assertAIAuditRows(t, ctx, adminPool, fixture, runs)
	if count := visibleRows(t, ctx, appPool, viewer, "report_ai_runs", reportID); count != 0 {
		t.Fatalf("VIEW-only non-actor saw %d report AI runs", count)
	}
	if count := visibleRows(t, ctx, appPool, crossTenant, "report_ai_runs", reportID); count != 0 {
		t.Fatalf("cross-tenant actor saw %d report AI runs", count)
	}
	if _, err := aiStore.StartRun(ctx, viewer, reportai.StartRunInput{
		ReportID: reportID, Kind: reportai.RunPlan, PromptVersion: "report-ai-v1",
		ModelPolicy: "governed-default", Summary: reportai.RequestSummary{Intent: "forbidden"},
	}); !errors.Is(err, reportstore.ErrNotFound) {
		t.Fatalf("VIEW-only StartRun() error = %v", err)
	}
	assertPostgresCode(t, insertForbiddenSummary(ctx, adminPool, fixture), "23514")
	assertPostgresCode(t, updateTerminalRun(ctx, adminPool, runs[reportai.RunPlan].ID), "55000")
	assertPostgresCode(t, deleteRejectedOperation(ctx, adminPool, runs[reportai.RunScopedEdit].ID), "55000")

	insightStore := insight.NewPostgresStore(appPool)
	bundle := evidenceBundle(t)
	evidence, err := insightStore.SaveEvidence(ctx, owner, reportID, componentID, bundle)
	if err != nil {
		t.Fatalf("SaveEvidence() error = %v", err)
	}
	first := insightArtifact(t, "insight_sales_v1", bundle, "销售额较上期增长16.36%。")
	if _, err := insightStore.AppendArtifact(ctx, owner, reportID, componentID, evidence.ID, first); err != nil {
		t.Fatalf("AppendArtifact(first) error = %v", err)
	}
	second := insightArtifact(t, "insight_sales_v2", bundle, "销售额较上期增长16.36%，趋势稳定。")
	if _, err := insightStore.AppendArtifact(ctx, owner, reportID, componentID, evidence.ID, second); err != nil {
		t.Fatalf("AppendArtifact(second) error = %v", err)
	}
	assertInsightStates(t, ctx, adminPool, fixture.reportID, 1, 1, false)
	editedAt := time.Date(2026, 8, 10, 7, 0, 0, 0, time.UTC)
	edited, err := insightStore.EditCurrent(ctx, owner, reportID, componentID,
		insight.InsightContent{Summary: "人工确认销售额增长。", Findings: []string{}, Risks: []string{}, Actions: []string{}}, editedAt)
	if err != nil {
		t.Fatalf("EditCurrent() error = %v", err)
	}
	if !edited.Artifact.HumanEdited || edited.Artifact.HumanEditedBy == nil ||
		*edited.Artifact.HumanEditedBy != owner.ActorID || edited.Artifact.HumanEditedAt == nil {
		t.Fatalf("human edit audit = %#v", edited.Artifact)
	}
	assertInsightStates(t, ctx, adminPool, fixture.reportID, 1, 2, true)
	if current, err := insightStore.GetCurrent(ctx, viewer, reportID, componentID); err != nil ||
		!current.Artifact.HumanEdited {
		t.Fatalf("VIEW-granted GetCurrent() = %#v, %v", current, err)
	}
	if _, err := insightStore.GetCurrent(ctx, crossTenant, reportID, componentID); !errors.Is(err, reportstore.ErrNotFound) {
		t.Fatalf("cross-tenant GetCurrent() error = %v", err)
	}
	assertPostgresCode(t, mutateEvidence(ctx, adminPool, evidence.ID), "55000")
	assertPostgresCode(t, deleteCurrentInsight(ctx, adminPool, fixture.reportID), "55000")
}

type fixture struct {
	tenantID, domainID, ownerID, viewerID      string
	otherTenantID, otherDomainID, otherOwnerID string
	reportID, code                             string
}

func createFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) fixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	result := fixture{
		tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(), viewerID: uuid.NewString(),
		otherTenantID: uuid.NewString(), otherDomainID: uuid.NewString(), otherOwnerID: uuid.NewString(),
		reportID: uuid.NewString(), code: "report_ai_" + suffix,
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tenant := range []struct{ id, code string }{
		{result.tenantID, "rptai_" + suffix}, {result.otherTenantID, "rptai_other_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
			VALUES($1,$2,'Report AI integration')`, tenant.id, tenant.code); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []struct{ tenantID, id, prefix string }{
		{result.tenantID, result.ownerID, "owner"}, {result.tenantID, result.viewerID, "viewer"},
		{result.otherTenantID, result.otherOwnerID, "other"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,'Report AI integration','integration-only-not-a-login-secret','ACTIVE')`,
			user.id, user.tenantID, strings.ToUpper(user.prefix)+suffix,
			user.prefix+"."+suffix+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	for _, domain := range []struct{ id, tenantID, actorID, code string }{
		{result.domainID, result.tenantID, result.ownerID, "rptai_" + suffix},
		{result.otherDomainID, result.otherTenantID, result.otherOwnerID, "rptai_other_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,$3,'Report AI integration',true,$4)`, domain.id, domain.tenantID,
			domain.code, domain.actorID); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []struct{ tenantID, domainID, userID, assignedBy string }{
		{result.tenantID, result.domainID, result.ownerID, result.ownerID},
		{result.tenantID, result.domainID, result.viewerID, result.ownerID},
		{result.otherTenantID, result.otherDomainID, result.otherOwnerID, result.otherOwnerID},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by,status
		) VALUES($1,$2,$3,'MEMBER',$4,'ACTIVE')`, member.tenantID, member.domainID,
			member.userID, member.assignedBy); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return result
}

func cleanupFixture(t *testing.T, pool *pgxpool.Pool, value fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	for _, table := range []string{
		"platform.report_insight_artifacts", "platform.report_evidence_artifacts",
		"platform.report_ai_operations", "platform.report_ai_runs", "platform.report_shares",
		"platform.report_export_jobs", "platform.report_publication_idempotency",
		"platform.report_inbound_idempotency", "platform.report_version_dependencies",
		"platform.report_version_component_indexes", "platform.report_draft_dependencies",
		"platform.report_draft_component_indexes", "platform.report_versions",
		"platform.report_revisions", "platform.report_drafts", "platform.reports",
		"platform.object_permissions", "platform.domain_memberships", "platform.business_domains",
		"platform.users", "platform.tenants",
	} {
		var relation *string
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&relation); err != nil {
			t.Errorf("resolve cleanup relation %s: %v", table, err)
			return
		}
		if relation == nil {
			continue
		}
		column := "tenant_id"
		if table == "platform.tenants" {
			column = "id"
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=ANY($1::uuid[])`, table, column),
			[]string{value.tenantID, value.otherTenantID}); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit cleanup: %v", err)
	}
}

func loadDefinition(t *testing.T, reportID, code string) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..",
		"api", "examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	definition.Metadata.ID = askdata.ID(reportID)
	definition.Metadata.Code = code
	definition.Metadata.Name = "Report AI integration"
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Layout.Columns = 4
	zone.Layout.Rows = 3
	zone.Slots[0].Grid.W = 4
	zone.Slots[0].Grid.H = 3
	return definition
}

func assertAIAuditRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, value fixture, runs map[reportai.RunKind]reportai.Run) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT kind,state,request_summary_json::text
		FROM platform.report_ai_runs WHERE report_id=$1 ORDER BY kind`, value.reportID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[reportai.RunKind]reportai.RunState{}
	for rows.Next() {
		var kind reportai.RunKind
		var state reportai.RunState
		var summary string
		if err := rows.Scan(&kind, &state, &summary); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(summary, "prompt") || strings.Contains(summary, "sampleRows") ||
			strings.Contains(summary, "raw-order-value") {
			t.Fatalf("request summary leaked forbidden content: %s", summary)
		}
		states[kind] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[reportai.RunKind]reportai.RunState{
		reportai.RunPlan: reportai.RunSucceeded, reportai.RunGenerateDraft: reportai.RunSucceeded,
		reportai.RunScopedEdit: reportai.RunRejected, reportai.RunInsight: reportai.RunFailed,
	}
	if len(states) != len(want) {
		t.Fatalf("AI run kinds = %#v", states)
	}
	for kind, state := range want {
		if states[kind] != state || runs[kind].Kind != kind {
			t.Fatalf("AI run %s state = %s", kind, states[kind])
		}
	}
	var rejectionCode string
	if err := pool.QueryRow(ctx, `SELECT rejection_code FROM platform.report_ai_operations
		WHERE ai_run_id=$1 AND validation_state='REJECTED'`, runs[reportai.RunScopedEdit].ID,
	).Scan(&rejectionCode); err != nil || rejectionCode != "REPORT_AI_SCOPE_REJECTED" {
		t.Fatalf("rejected operation = %q, %v", rejectionCode, err)
	}
}

func visibleRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, identity reportstore.Identity, table string, reportID askdata.ID) int {
	t.Helper()
	if table != "report_ai_runs" {
		t.Fatal("unsupported visible row table")
	}
	count := 0
	accessContext := database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	if err := database.WithTenantTx(accessContext, pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_ai_runs WHERE report_id=$1`, reportID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func insertForbiddenSummary(ctx context.Context, pool *pgxpool.Pool, value fixture) error {
	_, err := pool.Exec(ctx, `INSERT INTO platform.report_ai_runs(
		id,tenant_id,report_id,kind,actor_user_id,prompt_version,model_policy,
		request_summary_json,state
	) VALUES($1,$2,$3,'PLAN',$4,'report-ai-v1','governed-default',
		'{"prompt":"raw-order-value"}'::jsonb,'RUNNING')`, uuid.NewString(), value.tenantID,
		value.reportID, value.ownerID)
	return err
}

func updateTerminalRun(ctx context.Context, pool *pgxpool.Pool, runID askdata.ID) error {
	_, err := pool.Exec(ctx, `UPDATE platform.report_ai_runs SET state='FAILED',
		error_code='LATE_MUTATION',finished_at=now() WHERE id=$1`, runID)
	return err
}

func deleteRejectedOperation(ctx context.Context, pool *pgxpool.Pool, runID askdata.ID) error {
	_, err := pool.Exec(ctx, `DELETE FROM platform.report_ai_operations WHERE ai_run_id=$1`, runID)
	return err
}

func evidenceBundle(t *testing.T) insight.EvidenceBundle {
	t.Helper()
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "month", Value: "2026-08"}})
	if err != nil {
		t.Fatal(err)
	}
	previous, rate := "1100000.00", "0.1636"
	return insight.EvidenceBundle{
		SchemaVersion: insight.EvidenceSchemaVersion, SourceType: insight.SourceDatasetQuery,
		DatasetVersionID: "dataset:v1", DataSnapshotVersion: "snapshot:v1",
		QueryPlanHash: askdata.HashBytes([]byte("query-plan")), FilterHash: askdata.HashBytes([]byte("filter")),
		AsOf: "2026-08-10T06:00:00Z",
		ResolvedTimeRange: insight.ResolvedTimeRange{
			Start: "2026-08-01T00:00:00+08:00", EndExclusive: "2026-09-01T00:00:00+08:00",
			Timezone: "Asia/Shanghai",
		},
		AnalysisMethod: insight.AnalysisPeriodComparison, AnalysisMethodVersion: "1.0.0",
		EvidenceAlgorithmVersion: "1.0.0",
		Facts: []insight.Fact{{
			ID: "fact_sales_growth", MetricVersionID: "metric:sales@v1", CurrentValue: "1280000.00",
			PreviousValue: &previous, ChangeRate: &rate, Unit: "CNY",
			CellRefs: []shared.CellRef{{RowKey: rowKey, ColumnKey: "sales_amount"}},
		}},
		QualityWarnings: []insight.QualityWarning{}, GeneratedAt: "2026-08-10T06:01:00Z",
	}
}

func insightArtifact(t *testing.T, id askdata.ID, bundle insight.EvidenceBundle, summary string) insight.InsightArtifact {
	t.Helper()
	hash, err := bundle.Hash()
	if err != nil {
		t.Fatal(err)
	}
	fragment := "16.36%"
	start := strings.Index(summary, fragment)
	if start < 0 {
		t.Fatalf("summary lacks %s", fragment)
	}
	start = len([]rune(summary[:start]))
	return insight.InsightArtifact{
		SchemaVersion: insight.InsightSchemaVersion, ID: id, EvidenceHash: hash,
		PromptVersion: "insight-v1", ModelPolicy: "governed-default",
		VerifierVersion: "1.0.0", PolicyWordlistVersion: "1.0.0",
		Content: insight.InsightContent{Summary: summary, Findings: []string{}, Risks: []string{}, Actions: []string{}},
		Citations: []shared.Citation{shared.NewResultCellCitation(
			shared.TextSpan{Start: start, End: start + len([]rune(fragment))}, bundle.Facts[0].CellRefs[0],
		)},
		Status: insight.InsightCurrent,
	}
}

func assertInsightStates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reportID string, current, stale int, humanCurrent bool) {
	t.Helper()
	var currentCount, staleCount, humanCount int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER(WHERE status='CURRENT'),count(*) FILTER(WHERE status='STALE'),
		count(*) FILTER(WHERE status='CURRENT' AND human_edited)
		FROM platform.report_insight_artifacts WHERE report_id=$1`, reportID,
	).Scan(&currentCount, &staleCount, &humanCount); err != nil {
		t.Fatal(err)
	}
	wantHuman := 0
	if humanCurrent {
		wantHuman = 1
	}
	if currentCount != current || staleCount != stale || humanCount != wantHuman {
		t.Fatalf("insight states current=%d stale=%d human=%d", currentCount, staleCount, humanCount)
	}
}

func mutateEvidence(ctx context.Context, pool *pgxpool.Pool, evidenceID askdata.ID) error {
	_, err := pool.Exec(ctx, `UPDATE platform.report_evidence_artifacts
		SET evidence_hash=repeat('f',64) WHERE id=$1`, evidenceID)
	return err
}

func deleteCurrentInsight(ctx context.Context, pool *pgxpool.Pool, reportID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM platform.report_insight_artifacts
		WHERE report_id=$1 AND status='CURRENT'`, reportID)
	return err
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("PostgreSQL error = %v, want SQLSTATE %s", err, code)
	}
}
