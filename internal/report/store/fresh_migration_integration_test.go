package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFreshReportMigrationsAndStoreLifecycle is opt-in because it creates and
// force-drops one nonce-named database. It bypasses Docker CLI so migration
// and Report V2 integration remain testable when the container engine control
// socket is unavailable but PostgreSQL is still reachable.
func TestFreshReportMigrationsAndStoreLifecycle(t *testing.T) {
	adminURL := os.Getenv("REPORT_FRESH_MIGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("REPORT_FRESH_MIGRATION_APP_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set REPORT_FRESH_MIGRATION_ADMIN_DATABASE_URL and REPORT_FRESH_MIGRATION_APP_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	databaseName := "codex_rptdb003_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	postgresURL, err := databaseURLWithName(adminURL, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	control, err := pgxpool.New(ctx, postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err := control.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, dropErr := control.Exec(dropCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)")
		if dropErr != nil {
			t.Errorf("drop temporary report migration database: %v", dropErr)
		}
	}()
	tempAdminURL, _ := databaseURLWithName(adminURL, databaseName)
	tempAppURL, _ := databaseURLWithName(appURL, databaseName)
	adminPool, err := pgxpool.New(ctx, tempAdminURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE TABLE platform_schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}

	migrationPaths, err := filepath.Glob(filepath.Join(repositoryRootForMigrations(t), "migrations", "*.up.sql"))
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	sort.Strings(migrationPaths)
	for _, migrationPath := range migrationPaths {
		version := strings.TrimSuffix(filepath.Base(migrationPath), ".up.sql")
		// 000150 already contains the plain-domain definition. The production
		// migration runner records 000161 as satisfied on fresh databases.
		if strings.HasPrefix(version, "000161_") {
			if _, err := adminPool.Exec(ctx,
				"INSERT INTO platform_schema_migrations(version) VALUES ($1)", version,
			); err != nil {
				adminPool.Close()
				t.Fatal(err)
			}
			continue
		}
		raw, err := os.ReadFile(migrationPath)
		if err != nil {
			adminPool.Close()
			t.Fatal(err)
		}
		tx, err := adminPool.Begin(ctx)
		if err != nil {
			adminPool.Close()
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			_ = tx.Rollback(ctx)
			adminPool.Close()
			t.Fatalf("apply %s: %v", filepath.Base(migrationPath), err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO platform_schema_migrations(version) VALUES ($1)", version,
		); err != nil {
			_ = tx.Rollback(ctx)
			adminPool.Close()
			t.Fatalf("record %s: %v", filepath.Base(migrationPath), err)
		}
		if err := tx.Commit(ctx); err != nil {
			adminPool.Close()
			t.Fatalf("commit %s: %v", filepath.Base(migrationPath), err)
		}
	}
	if _, err := adminPool.Exec(ctx, `GRANT USAGE ON SCHEMA platform TO report_app;
		GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE
			platform.reports,platform.report_drafts,platform.report_revisions,
			platform.report_versions,platform.report_publication_idempotency,
			platform.report_draft_component_indexes,platform.report_draft_dependencies,
			platform.report_version_component_indexes,platform.report_version_dependencies
		TO report_app;
		GRANT EXECUTE ON FUNCTION platform.report_v2_can_access(uuid,text[]),
			platform.report_v2_row_can_access(uuid,uuid,uuid,text[]) TO report_app;`); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	adminPool.Close()

	previousAdmin, hadAdmin := os.LookupEnv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	previousApp, hadApp := os.LookupEnv("ASKDATA_INTEGRATION_DATABASE_URL")
	if err := os.Setenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL", tempAdminURL); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ASKDATA_INTEGRATION_DATABASE_URL", tempAppURL); err != nil {
		t.Fatal(err)
	}
	defer restoreEnvironment(t, "ASKDATA_INTEGRATION_ADMIN_DATABASE_URL", previousAdmin, hadAdmin)
	defer restoreEnvironment(t, "ASKDATA_INTEGRATION_DATABASE_URL", previousApp, hadApp)
	t.Run("store-lifecycle", TestPostgresStoreReportLifecycleConcurrencyImmutabilityAndRLS)
}

func databaseURLWithName(raw, databaseName string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("database URL scheme %q is unsupported", parsed.Scheme)
	}
	parsed.Path = "/" + databaseName
	return parsed.String(), nil
}

func repositoryRootForMigrations(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration integration path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

func restoreEnvironment(t *testing.T, key, value string, existed bool) {
	t.Helper()
	var err error
	if existed {
		err = os.Setenv(key, value)
	} else {
		err = os.Unsetenv(key)
	}
	if err != nil {
		t.Errorf("restore %s: %v", key, err)
	}
}
