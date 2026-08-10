package datarequest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresStoreActiveRequestLifecycleAndRLS(t *testing.T) {
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
	fixture := createDataRequestFixture(t, ctx, root)
	if _, err := root.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()); err != nil {
		t.Fatal(err)
	}

	runner := func(ctx context.Context, tenantID string, operation func(pgx.Tx) error) error {
		nested, err := root.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = nested.Rollback(ctx) }()
		access, ok := database.AccessContextFromContext(ctx)
		if !ok {
			return ErrPermissionDenied
		}
		if _, err := nested.Exec(ctx, `SELECT
			set_config('app.tenant_id',$1,true),
			set_config('app.access_mode','USER',true),
			set_config('app.user_id',$2,true),
			set_config('app.domain_id',$3,true)`,
			tenantID, access.UserID, access.DomainID); err != nil {
			return err
		}
		if err := operation(nested); err != nil {
			return err
		}
		return nested.Commit(ctx)
	}
	store := NewPostgresStore(appPool)
	store.tenantTx = runner
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	service := NewService(store)
	service.now = func() time.Time { return now }
	requester := Identity{
		TenantID: fixture.tenantID, DomainID: fixture.domainID, ActorID: fixture.requesterID,
	}
	approver := Identity{
		TenantID: fixture.tenantID, DomainID: fixture.domainID, ActorID: fixture.approverID,
	}
	requesterContext := database.WithAccessContext(ctx, fixture.requesterID, fixture.domainID)
	created, err := service.Create(requesterContext, requester, CreateInput{
		RequestText:     "导出本月订单明细",
		ParsedContext:   ParsedContext{},
		BusinessPurpose: "月度经营复盘",
		RequiredFields: []FieldRef{{
			DatasetVersionID: fixture.datasetVersionID, FieldID: fixture.fieldID,
		}},
		SLADueAt: now.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create active request: %v", err)
	}
	if created.SourceQuestionRunID != "" || !created.ParsedContext.Empty() ||
		created.State != StateDraft || created.RecordVersion != 1 || len(created.Events) != 1 ||
		created.SensitivityLevel != SensitivityRestricted ||
		len(created.ApproverUserIDs) != 1 || created.ApproverUserIDs[0] != fixture.approverID {
		t.Fatalf("created request = %#v", created)
	}

	observerContext := database.WithAccessContext(ctx, fixture.observerID, fixture.domainID)
	observer := Identity{
		TenantID: fixture.tenantID, DomainID: fixture.domainID, ActorID: fixture.observerID,
	}
	if _, err := service.Get(observerContext, observer, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("observer get error = %v", err)
	}
	if _, err := service.Get(observerContext, requester, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("identity/context mismatch error = %v", err)
	}

	submitted, err := service.Submit(requesterContext, requester, created.ID, created.RecordVersion)
	if err != nil || submitted.State != StateSubmitted || submitted.RecordVersion != 2 ||
		len(submitted.Events) != 2 {
		t.Fatalf("submitted request = %#v, %v", submitted, err)
	}
	if _, err := service.Submit(requesterContext, requester, created.ID, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale submit error = %v", err)
	}

	approverContext := database.WithAccessContext(ctx, fixture.approverID, fixture.domainID)
	if _, err := service.Transition(approverContext, approver, created.ID, TransitionInput{
		ToState: StateApproved, RecordVersion: submitted.RecordVersion, Note: "用途清晰",
	}); !errors.Is(err, ErrSecurityCosignRequired) {
		t.Fatalf("approval without independent cosign error = %v", err)
	}
	approved, err := service.Transition(approverContext, approver, created.ID, TransitionInput{
		ToState: StateApproved, RecordVersion: submitted.RecordVersion, Note: "用途清晰",
		SecurityCosignUserID: fixture.observerID,
	})
	if err != nil || approved.State != StateApproved || approved.RecordVersion != 3 ||
		approved.SecurityCosignUserID != fixture.observerID {
		t.Fatalf("approved request = %#v, %v", approved, err)
	}
	exportJob, err := service.EnqueueExport(
		approverContext, approver, created.ID, approved.RecordVersion,
	)
	if err != nil || exportJob.State != ControlledExportPending ||
		exportJob.MaxDownloads != DefaultExportDownloads {
		t.Fatalf("export job = %#v, %v", exportJob, err)
	}
	if err := store.MarkControlledExportReady(
		approverContext, fixture.tenantID, exportJob.JobID,
		"data-requests/"+exportJob.JobID+".csv", askdata.HashBytes([]byte("fixture-export")),
		128, now.Add(time.Minute),
	); err != nil {
		t.Fatalf("mark export ready: %v", err)
	}
	inProgress, err := service.Transition(approverContext, approver, created.ID, TransitionInput{
		ToState: StateInProgress, RecordVersion: approved.RecordVersion,
	})
	if err != nil || inProgress.State != StateInProgress ||
		inProgress.AssigneeUserID != fixture.approverID || inProgress.RecordVersion != 4 {
		t.Fatalf("in-progress request = %#v, %v", inProgress, err)
	}
	delivered, err := service.Transition(approverContext, approver, created.ID, TransitionInput{
		ToState: StateDelivered, RecordVersion: inProgress.RecordVersion,
		DeliveryType: DeliveryOneTimeExport, DeliveryRef: exportJob.JobID,
	})
	if err != nil || delivered.State != StateDelivered || delivered.RecordVersion != 5 {
		t.Fatalf("delivered request = %#v, %v", delivered, err)
	}
	for downloadNo := 1; downloadNo <= DefaultExportDownloads; downloadNo++ {
		grant, err := store.AcquireControlledExportDownload(
			approverContext, approver, exportJob.JobID, now.Add(time.Duration(downloadNo)*time.Minute),
		)
		if err != nil || grant.RemainingDownloads != DefaultExportDownloads-downloadNo ||
			grant.StorageKey == "" {
			t.Fatalf("download %d grant=%#v err=%v", downloadNo, grant, err)
		}
	}
	if _, err := store.AcquireControlledExportDownload(
		approverContext, approver, exportJob.JobID, now.Add(5*time.Minute),
	); !errors.Is(err, ErrControlledExportLimit) {
		t.Fatalf("fourth download error = %v", err)
	}
	closed, err := service.Transition(requesterContext, requester, created.ID, TransitionInput{
		ToState: StateClosed, RecordVersion: delivered.RecordVersion, Note: "已验收",
	})
	if err != nil || closed.State != StateClosed || closed.RecordVersion != 6 ||
		len(closed.Events) != 10 {
		t.Fatalf("closed request = %#v, %v", closed, err)
	}
	eventCounts := map[string]int{}
	for index, event := range closed.Events {
		eventCounts[event.EventType]++
		if event.AuditNo != int64(index+1) || event.Details.SensitivityLevel != SensitivityRestricted {
			t.Fatalf("event[%d]=%#v", index, event)
		}
		if event.EventType != "STATE_TRANSITION" && event.Details.ExportJobID != exportJob.JobID {
			t.Fatalf("export audit event[%d]=%#v", index, event)
		}
	}
	if eventCounts["STATE_TRANSITION"] != 6 || eventCounts["EXPORT_ENQUEUED"] != 1 ||
		eventCounts["EXPORT_DOWNLOADED"] != DefaultExportDownloads {
		t.Fatalf("event counts=%v", eventCounts)
	}
}

type dataRequestFixture struct {
	tenantID, domainID                  string
	requesterID, approverID, observerID string
	datasetVersionID, fieldID           string
}

func createDataRequestFixture(t *testing.T, ctx context.Context, tx pgx.Tx) dataRequestFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := dataRequestFixture{
		tenantID: uuid.NewString(), domainID: uuid.NewString(),
		requesterID: uuid.NewString(), approverID: uuid.NewString(), observerID: uuid.NewString(),
		datasetVersionID: uuid.NewString(), fieldID: "order_id",
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Data request integration')`, fixture.tenantID, "datareq_"+suffix); err != nil {
		t.Fatalf("insert tenant fixture: %v", err)
	}
	insertUser := func(userID, prefix string) {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,$5,'integration-only-not-a-login-secret','ACTIVE')`,
			userID, fixture.tenantID, strings.ToUpper(prefix)+suffix,
			prefix+"."+suffix+"@example.invalid", prefix+" data request integration"); err != nil {
			t.Fatalf("insert %s fixture: %v", prefix, err)
		}
	}
	insertUser(fixture.requesterID, "requester")
	insertUser(fixture.approverID, "approver")
	insertUser(fixture.observerID, "observer")
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by
	) VALUES($1,$2,$3,'Data request integration',true,$4)`, fixture.domainID,
		fixture.tenantID, "datareq_"+suffix, fixture.approverID); err != nil {
		t.Fatalf("insert domain fixture: %v", err)
	}
	for _, membership := range []struct{ userID, role string }{
		{fixture.requesterID, "MEMBER"},
		{fixture.approverID, "DOMAIN_ADMIN"},
		{fixture.observerID, "MEMBER"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by,status
		) VALUES($1,$2,$3,$4,$5,'ACTIVE')`, fixture.tenantID, fixture.domainID,
			membership.userID, membership.role, fixture.approverID); err != nil {
			t.Fatalf("insert membership fixture: %v", err)
		}
	}

	datasetID := uuid.NewString()
	sourceDraftID := uuid.NewString()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable fixture triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.datasets(
		id,tenant_id,code,name,dataset_type,status,layer,domain_id,sharing_scope,
		created_by,updated_by,owner_user_id
	) VALUES($1,$2,$3,'Data request fields','SINGLE_SOURCE','PUBLISHED','DWS',$4,'DOMAIN',$5,$5,$5)`,
		datasetID, fixture.tenantID, "datareq_fields_"+suffix, fixture.domainID,
		fixture.approverID); err != nil {
		t.Fatalf("insert dataset fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,schema_hash,
		logical_plan_json,plan_hash,created_by,updated_by,created_at,updated_at,
		published_at,published_by,source_draft_version_id,source_draft_record_version,
		layer,publication_origin
	) VALUES($1,$2,$3,1,'PUBLISHED','1.0','{}',$4,'{}',$5,$6,$6,
		$7,$7,$8,$6,$9,1,'DWS','LEGACY')`, fixture.datasetVersionID, fixture.tenantID,
		datasetID, strings.Repeat("a", 64), strings.Repeat("b", 64), fixture.approverID,
		time.Now().UTC().Add(-time.Minute), time.Now().UTC(), sourceDraftID); err != nil {
		t.Fatalf("insert dataset version fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(
		id,tenant_id,dataset_version_id,field_id,field_code,field_name,expression_json,
		canonical_type,semantic_type,sensitivity_level,field_role,aggregation,nullable,visible,ordinal_position
	) VALUES($1,$2,$3,$4::text,$4::citext,'订单号','{"kind":"COLUMN","column":"order_id"}',
		'STRING','','RESTRICTED','DIMENSION','',false,true,1)`, uuid.NewString(), fixture.tenantID,
		fixture.datasetVersionID, fixture.fieldID); err != nil {
		t.Fatalf("insert dataset field fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatalf("restore fixture triggers: %v", err)
	}
	return fixture
}
