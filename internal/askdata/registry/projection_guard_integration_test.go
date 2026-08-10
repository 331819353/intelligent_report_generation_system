package registry

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestProjectionGuardPostgresFourHashRLSAndInvalidation(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("set AskData app and admin integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	primary := createReleaseRetentionFixture(t, ctx, admin, true)
	observer := createReleaseRetentionFixture(t, ctx, admin, false)
	defer cleanupReleaseRetentionFixtures(t, admin, primary.tenantID, observer.tenantID)

	guard := NewProjectionGuard(app)
	primaryContext := database.WithAccessContext(ctx, primary.actorID, primary.domainID)
	primaryContext = WithProjectionGuardScope(primaryContext, primary.tenantID, primary.domainID)
	if err := guard.AssertRunnable(primaryContext, primary.releaseID); err != nil {
		t.Fatalf("initial AssertRunnable() error = %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE askdata.release_projections
		SET status='FAILED',error_code='INTEGRATION_DRIFT'
		WHERE tenant_id=$1 AND release_id=$2 AND target='NEBULA_GRAPH'`,
		primary.tenantID, primary.releaseID); err != nil {
		t.Fatalf("drift graph projection: %v", err)
	}
	// The 30-second decision cache checks a lightweight revision first, so a
	// cross-process projection mutation invalidates immediately on the next run.
	err = guard.AssertRunnable(primaryContext, primary.releaseID)
	var failure *ReleaseProjectionMismatchError
	if !errors.As(err, &failure) || len(failure.Mismatches) != 1 ||
		failure.Mismatches[0].Projection != "GRAPH" ||
		failure.Mismatches[0].Status != "FAILED" ||
		failure.Mismatches[0].Expected != primary.releaseHash ||
		failure.Mismatches[0].Applied != primary.releaseHash {
		t.Fatalf("drifted AssertRunnable() = %#v, %v", failure, err)
	}
	guard.Invalidate(primary.releaseID)
	if err := guard.AssertRunnable(primaryContext, primary.releaseID); !errors.Is(err, ErrReleaseProjectionMismatch) {
		t.Fatalf("explicitly invalidated AssertRunnable() error = %v", err)
	}

	observerContext := database.WithAccessContext(ctx, observer.actorID, observer.domainID)
	observerContext = WithProjectionGuardScope(observerContext, observer.tenantID, observer.domainID)
	err = guard.AssertRunnable(observerContext, primary.releaseID)
	if !errors.As(err, &failure) || failure.ReleaseStatus != "MISSING" ||
		len(failure.Mismatches) != 1 || failure.Mismatches[0].Projection != "RELEASE" ||
		failure.ContentHash != "" {
		t.Fatalf("cross-tenant AssertRunnable() = %#v, %v", failure, err)
	}
}
