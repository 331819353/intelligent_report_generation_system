package registry

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

func TestAdditivityDatabaseChecksRemainIndependentOfApplicationGate(t *testing.T) {
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

	// Superuser fixture mode bypasses FK and certification triggers, but not
	// CHECK constraints. This proves the storage gate is independently active.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable fixture triggers: %v", err)
	}
	metricID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
		id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
		formula_ast,default_filters_ast,unit,time_grain,additivity,
		zero_denominator_policy,display_precision,additivity_suggestion,
		additivity_suggestion_rule_id,null_policy,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,$5,'{"type":"TRUE"}'::jsonb,'{"type":"TRUE"}'::jsonb,
		'COUNT','NONE',NULL,'NULL',2,'FULLY_ADDITIVE','TEST_FIXTURE','PRESERVE','DRAFT',$6,$7)`,
		metricID, uuid.New(), uuid.New(), uuid.New(), uuid.New(), strings.Repeat("a", 64), uuid.New()); err != nil {
		t.Fatalf("insert unconfirmed draft: %v", err)
	}

	assertAdditivityCheckRejected(t, ctx, tx, "missing", `UPDATE askdata.metric_versions
		SET status='CERTIFIED' WHERE id=$1`, metricID, "certified_requires_additivity")
	assertAdditivityCheckRejected(t, ctx, tx, "enum", `UPDATE askdata.metric_versions
		SET additivity='ADDITIVE' WHERE id=$1`, metricID, "additivity_enum")
	assertAdditivityCheckRejected(t, ctx, tx, "semi", `UPDATE askdata.metric_versions
		SET additivity='SEMI_ADDITIVE',semi_additive_time_aggregation=NULL WHERE id=$1`, metricID, "semi_additive_agg")
	assertAdditivityCheckRejected(t, ctx, tx, "non", `UPDATE askdata.metric_versions
		SET additivity='NON_ADDITIVE',aggregation_restriction='PRE_AGGREGATE' WHERE id=$1`, metricID, "non_additive_restriction")

	if _, err := tx.Exec(ctx, `UPDATE askdata.metric_versions SET
		additivity='FULLY_ADDITIVE',semi_additive_time_aggregation=NULL,
		aggregation_restriction=NULL,status='CERTIFIED' WHERE id=$1`, metricID); err != nil {
		t.Fatalf("complete certified metric was rejected: %v", err)
	}
	var fact, suggestion, status string
	if err := tx.QueryRow(ctx, `SELECT additivity,additivity_suggestion,status
		FROM askdata.metric_versions WHERE id=$1`, metricID).Scan(&fact, &suggestion, &status); err != nil {
		t.Fatal(err)
	}
	if fact != "FULLY_ADDITIVE" || suggestion != "FULLY_ADDITIVE" || status != "CERTIFIED" {
		t.Fatalf("stored additivity = %s/%s/%s", fact, suggestion, status)
	}
}

func assertAdditivityCheckRejected(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	name, statement, id, constraint string,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, "SAVEPOINT additivity_"+name); err != nil {
		t.Fatalf("savepoint %s: %v", name, err)
	}
	if _, err := tx.Exec(ctx, statement, id); err == nil || !strings.Contains(err.Error(), constraint) {
		t.Fatalf("%s check error = %v", name, err)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT additivity_"+name); err != nil {
		t.Fatalf("rollback %s: %v", name, err)
	}
}
