package registry

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimeContractDatabaseGuardsCertificationImmutabilityAndReleaseClosure(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, actorID, domainID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(tenantID[:8], "-", "")
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,$3)`, tenantID, "tc_"+suffix, "Time Contract Integration"); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no
	) VALUES($1,$2,$3,$4,$5,$6)`, actorID, tenantID,
		"time-contract-"+suffix+"@example.test", "Time Contract Owner",
		"integration-only", "TC"+strings.ToUpper(suffix)); err != nil {
		t.Fatalf("insert actor: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,created_by
	) VALUES($1,$2,$3,$4,$5)`, domainID, tenantID,
		"tc_"+suffix, "Time Contract Domain", actorID); err != nil {
		t.Fatalf("insert business domain: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
		id,tenant_id,code,name,owner_id
	) VALUES($1,$2,$3,$4,$5)`, domainID, tenantID,
		"tc_"+suffix, "Time Contract Domain", actorID); err != nil {
		t.Fatalf("insert askdata domain: %v", err)
	}

	contractID := uuid.NewString()
	version := TimeContractVersion{
		ID: uuid.NewString(), TenantID: tenantID, DomainID: domainID,
		TimeContractID: contractID, VersionNo: 1, Status: VersionStatusDraft,
		Timezone: "Asia/Shanghai", WeekStart: WeekStartMonday, WeekNumbering: WeekNumberingISO,
		FiscalYearStartMonth: 1, FiscalMonthRule: FiscalMonthCalendar,
		IncompletePeriodPolicy:   IncompletePeriodMTD,
		ComparisonAlignment:      ComparisonSameDayCount,
		MonthEndOverflowRule:     MonthEndClampToLastDay,
		SupportedGrains:          []TimeGrain{TimeGrainDay, TimeGrainMonth},
		DataAvailableThroughExpr: "MATERIALIZATION_MAX_PRIMARY_TIME", ExpectedLagHours: 26,
	}
	version.ContentHash = mustTimeContractHash(t, version)
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.time_contracts(
		id,tenant_id,domain_id,code,name,owner_user_id
	) VALUES($1,$2,$3,$4,$5,$6)`, contractID, tenantID, domainID,
		"tc_"+suffix, "Time Contract", actorID); err != nil {
		t.Fatalf("insert time contract: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.time_contract_versions(
		id,tenant_id,domain_id,time_contract_id,version_no,status,timezone,
		week_start,week_numbering,fiscal_year_start_month,fiscal_month_rule,
		incomplete_period_policy,comparison_alignment,month_end_overflow_rule,
		supported_grains,data_available_through_expr,expected_lag_hours,content_hash
	) VALUES($1,$2,$3,$4,1,'DRAFT',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		version.ID, tenantID, domainID, contractID, version.Timezone, version.WeekStart,
		version.WeekNumbering, version.FiscalYearStartMonth, version.FiscalMonthRule,
		version.IncompletePeriodPolicy, version.ComparisonAlignment,
		version.MonthEndOverflowRule, []string{"DAY", "MONTH"},
		version.DataAvailableThroughExpr, version.ExpectedLagHours, version.ContentHash); err != nil {
		t.Fatalf("insert time contract version: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.time_contract_versions
		SET status='CERTIFIED' WHERE id=$1`, version.ID); err != nil {
		t.Fatalf("certify time contract version: %v", err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT immutable_version`); err != nil {
		t.Fatalf("create immutable savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.time_contract_versions
		SET expected_lag_hours=27 WHERE id=$1`, version.ID); err == nil || !strings.Contains(err.Error(), TimeContractVersionImmutable) {
		t.Fatalf("certified mutation error = %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT immutable_version`); err != nil {
		t.Fatalf("rollback immutable savepoint: %v", err)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT fiscal_calendar`); err != nil {
		t.Fatalf("create fiscal savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.time_contract_versions(
		id,tenant_id,domain_id,time_contract_id,version_no,status,timezone,
		week_start,week_numbering,fiscal_year_start_month,fiscal_month_rule,
		comparison_alignment,month_end_overflow_rule,supported_grains,
		data_available_through_expr,expected_lag_hours,content_hash
	) VALUES($1,$2,$3,$4,2,'DRAFT','Asia/Shanghai','MONDAY','ISO',1,
		'CALENDAR','SAME_DAY_COUNT','CLAMP_TO_LAST_DAY',ARRAY['FISCAL_MONTH'],
		'MATERIALIZATION_MAX_PRIMARY_TIME',26,$5)`, uuid.NewString(), tenantID,
		domainID, contractID, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), TimeCalendarRequired) {
		t.Fatalf("missing fiscal calendar error = %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT fiscal_calendar`); err != nil {
		t.Fatalf("rollback fiscal savepoint: %v", err)
	}

	// Build a rollback-only malformed model row with FK triggers disabled. Once
	// normal trigger execution is restored, certification must fail at the new
	// time-contract gate before any other semantic dependency is considered.
	modelVersionID := uuid.NewString()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable fixture triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_models(
		id,tenant_id,domain_id,model_id,version_no,code,name,dataset_id,
		dataset_version_id,materialization_id,dataset_schema_hash,layer,
		grain_contract,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,'Fixture Model',$6,$7,$8,$9,'DWS',
		'{"keys":["id"]}'::jsonb,'DRAFT',$10,$11)`, modelVersionID, tenantID,
		domainID, uuid.NewString(), "model_"+suffix, uuid.New(), uuid.New(),
		uuid.New(), strings.Repeat("b", 64), strings.Repeat("c", 64), actorID); err != nil {
		t.Fatalf("insert rollback-only model fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatalf("restore fixture triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT missing_contract`); err != nil {
		t.Fatalf("create missing-contract savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.semantic_models
		SET status='CERTIFIED' WHERE id=$1`, modelVersionID); err == nil || !strings.Contains(err.Error(), TimeContractMissing) {
		t.Fatalf("model certification without time contract error = %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT missing_contract`); err != nil {
		t.Fatalf("rollback missing-contract savepoint: %v", err)
	}

	releaseID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,object_count,
		created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,1,$6,$6)`, releaseID, tenantID, domainID,
		"tc-integration-"+suffix, strings.Repeat("d", 64), actorID); err != nil {
		t.Fatalf("insert release fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable release fixture triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
		content_hash,sensitivity,contract_json
	) VALUES($1,$2,$3,'SEMANTIC_MODEL',$4,$5,$6,'INTERNAL',$7)`, tenantID,
		domainID, releaseID, uuid.NewString(), modelVersionID, strings.Repeat("e", 64),
		map[string]any{"type": "SEMANTIC_MODEL", "timeContractVersionId": uuid.NewString()}); err != nil {
		t.Fatalf("insert release object fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatalf("restore release fixture triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT release_closure`); err != nil {
		t.Fatalf("create release-closure savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.releases
		SET status='VALIDATING' WHERE id=$1`, releaseID); err == nil || !strings.Contains(err.Error(), TimeContractMissing) {
		t.Fatalf("release without time contract closure error = %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT release_closure`); err != nil {
		t.Fatalf("rollback release-closure savepoint: %v", err)
	}

	var secureTableCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class AS relation
		JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname='askdata'
		  AND relation.relname IN ('time_contracts','time_contract_versions')
		  AND relation.relrowsecurity AND relation.relforcerowsecurity`).Scan(&secureTableCount); err != nil {
		t.Fatalf("inspect time contract RLS: %v", err)
	}
	if secureTableCount != 2 {
		t.Fatalf("secured time contract table count = %d, want 2", secureTableCount)
	}
}
