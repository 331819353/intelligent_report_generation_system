//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
)

// insertVersionedDataSourceTx mirrors the production v58 identity/config split.
// The pointer foreign keys are deferred, so a fixture can insert the source
// identity first and its immutable configuration version second in one tx.
// It deliberately never fabricates publication evidence: callers that need an
// ACTIVE source must call attestAndPublishDataSourceFixture after this tx commits.
func insertVersionedDataSourceTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, code, name, sourceType, secretRef, status, configJSON string,
) (sourceID string, sourceVersion int64, err error) {
	sourceID = uuid.NewString()
	configVersionID := uuid.NewString()
	code = fixtureDataSourceCode(code)
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" {
		status = "DRAFT"
	}
	if strings.TrimSpace(configJSON) == "" {
		configJSON = "{}"
	}
	insertStatus := status
	if status == "ACTIVE" || status == "SYNCING" || status == "DISABLED" {
		insertStatus = "DRAFT"
	}
	digest := sha256.Sum256([]byte(
		sourceType + "\x00" + configJSON + "\x00" + secretRef + "\x00" + sourceID,
	))
	configHash := hex.EncodeToString(digest[:])

	err = tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			id,tenant_id,code,name,source_type,status,config,secret_ref,
			current_draft_version_id,current_published_version_id,
			validation_status,publication_status,last_synced_at
		) VALUES(
			$1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,NULL,'UNTESTED','UNPUBLISHED',NULL
		)
		RETURNING version`,
		sourceID, tenantID, code, name, sourceType, insertStatus, configJSON, secretRef,
		configVersionID,
	).Scan(&sourceVersion)
	if err != nil {
		return "", 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
			id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,config_hash
		) VALUES($1,$2,$3,1,$4,$5::jsonb,$6,$7)`,
		configVersionID, tenantID, sourceID, sourceType, configJSON, secretRef, configHash,
	)
	if err != nil {
		return "", 0, err
	}
	return sourceID, sourceVersion, nil
}

// attestAndPublishDataSourceFixture exercises the same role boundary as
// production. The app role can only enqueue and publish; a short-lived pool
// using the dedicated tester role claims and completes the frozen job through
// the guarded SECURITY DEFINER functions.
func attestAndPublishDataSourceFixture(
	t *testing.T,
	ctx context.Context,
	appPool *pgxpool.Pool,
	tenantID, actorID, sourceID string,
) datasource.Source {
	t.Helper()

	appRepository := datasource.NewPostgresRepository(appPool)
	connectionTests := datasource.NewPostgresConnectionTestRepository(appPool)
	service := datasource.NewService(appRepository)
	service.SetConnectionTestJobRepository(connectionTests)
	job, err := service.QueueConnectionTest(
		ctx, tenantID, actorID, sourceID, "integration-fixture-"+sourceID,
	)
	if err != nil {
		t.Fatalf("queue trusted connection-test fixture: %v", err)
	}

	testerPool, err := pgxpool.New(
		ctx,
		env(
			"CONNECTION_TEST_DATABASE_URL",
			"postgres://report_connection_tester:local_connection_test_password@127.0.0.1:5432/intelligent_report?sslmode=disable",
		),
	)
	if err != nil {
		t.Fatalf("open connection-test fixture role: %v", err)
	}
	defer testerPool.Close()
	if err := testerPool.Ping(ctx); err != nil {
		t.Fatalf("ping connection-test fixture role: %v", err)
	}
	testerRepository := datasource.NewPostgresConnectionTestRepository(testerPool)
	claim, err := testerRepository.ClaimConnectionTest(
		ctx, tenantID, "integration-fixture", time.Minute,
	)
	if err != nil {
		t.Fatalf("claim trusted connection-test fixture: %v", err)
	}
	if claim == nil || claim.Job.ID != job.ID {
		t.Fatalf("claimed unexpected connection-test fixture: job=%#v claim=%#v", job, claim)
	}
	if err := testerRepository.CompleteConnectionTest(
		ctx, tenantID, claim.Job.ID, claim.LeaseToken, "integration-fixture", 0,
	); err != nil {
		t.Fatalf("complete trusted connection-test fixture: %v", err)
	}
	published, err := service.Publish(ctx, tenantID, actorID, sourceID)
	if err != nil {
		t.Fatalf("publish attested data-source fixture: %v", err)
	}
	// ACTIVE database-source fixtures already have synchronized metadata rows.
	// Record the matching sync watermark so cross-source execution can freeze a
	// reproducible source snapshot just as it does after a production metadata sync.
	if err := database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(
			ctx,
			`UPDATE platform.data_sources
			 SET last_synced_at=clock_timestamp()
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`,
			tenantID, sourceID,
		)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	}); err != nil {
		t.Fatalf("record data-source fixture sync watermark: %v", err)
	}
	return published
}

func fixtureDataSourceCode(value string) string {
	var normalized strings.Builder
	for _, character := range strings.TrimSpace(value) {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '_':
			normalized.WriteRune(character)
		default:
			normalized.WriteByte('_')
		}
	}
	return normalized.String()
}
