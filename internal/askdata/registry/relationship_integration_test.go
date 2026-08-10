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

func TestRelationshipDatabaseChecksAndCertificationFailClosed(t *testing.T) {
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
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable fixture triggers: %v", err)
	}

	insert := func(cardinality any, policy any, bridgeID any) (string, error) {
		id := uuid.NewString()
		_, err := tx.Exec(ctx, `INSERT INTO askdata.relationships(
			id,tenant_id,domain_id,relationship_id,version_no,left_model_version_id,
			right_model_version_id,relationship_type,join_type,cardinality,join_ast,
			fanout_policy,bridge_model_version_id,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,1,$5,$6,'MODEL_JOIN','INNER',$7,
			'{"type":"EQUALS","leftFieldId":"entity_id","rightFieldId":"entity_id"}'::jsonb,
			$8,$9,'DRAFT',$10,$11)`, id, uuid.New(), uuid.New(), uuid.New(),
			uuid.New(), uuid.New(), cardinality, policy, bridgeID,
			strings.Repeat("a", 64), uuid.New())
		return id, err
	}

	assertRelationshipInsertRejected(t, ctx, tx, "invalid_combo", "rel_combination_valid", func() error {
		_, err := insert("MANY_TO_MANY", "SAFE", nil)
		return err
	})
	assertRelationshipInsertRejected(t, ctx, tx, "missing_bridge", "rel_bridge_required", func() error {
		_, err := insert("MANY_TO_MANY", "BRIDGE_REQUIRED", nil)
		return err
	})
	if _, err := insert("MANY_TO_MANY", "BRIDGE_REQUIRED", uuid.New()); err != nil {
		t.Fatalf("valid bridge relationship was rejected: %v", err)
	}

	legacyID, err := insert(nil, nil, nil)
	if err != nil {
		t.Fatalf("manual-review holding row was rejected: %v", err)
	}
	certificationErr := validateCertificationCandidate(ctx, tx, certificationCandidate{
		Table: "relationships", VersionID: legacyID,
	})
	if certificationErr == nil || !strings.Contains(certificationErr.Error(), "RELATIONSHIP_FANOUT_INVALID") {
		t.Fatalf("NULL holding row certification error = %v", certificationErr)
	}
}

func assertRelationshipInsertRejected(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	name, constraint string,
	insert func() error,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, "SAVEPOINT relationship_"+name); err != nil {
		t.Fatalf("savepoint %s: %v", name, err)
	}
	if err := insert(); err == nil || !strings.Contains(err.Error(), constraint) {
		t.Fatalf("%s check error = %v", name, err)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT relationship_"+name); err != nil {
		t.Fatalf("rollback %s: %v", name, err)
	}
}
