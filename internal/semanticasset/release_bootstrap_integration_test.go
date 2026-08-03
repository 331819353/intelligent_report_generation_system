package semanticasset

import (
	"context"
	"os"
	"testing"

	"intelligent-report-generation-system/internal/platform/database"
)

func TestLegacySemanticReleaseBootstrapIntegration(t *testing.T) {
	databaseURL := os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEMANTIC_BOOTSTRAP_INTEGRATION_DATABASE_URL is not set")
	}
	tenantID := os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_TENANT_ID")
	actorID := os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_ACTOR_ID")
	if tenantID == "" || actorID == "" {
		t.Skip("semantic bootstrap integration tenant and actor are not set")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service := NewService(NewPostgresStore(pool))
	preview, err := service.PreviewBootstrapSemanticRelease(ctx, tenantID, actorID,
		BootstrapSemanticReleaseInput{
			SemanticVersion: "integration-preview-v1", DefaultTimezone: "Asia/Shanghai",
			DefaultCalendar: "GREGORIAN", CompletePeriodPolicy: "EXCLUDE_INCOMPLETE",
		})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || preview.Candidate == nil {
		t.Fatalf("legacy published assets are not migratable: %+v", preview.Issues)
	}
	if preview.SourceCounts["metrics"] < 1 || preview.CandidateCount < 1 {
		t.Fatalf("unexpected empty candidate: %+v", preview.SourceCounts)
	}
	if releaseID := os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_ACTIVATE_RELEASE_ID"); releaseID != "" {
		release, err := service.GetSemanticRelease(ctx, tenantID, releaseID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := service.GetActiveSemanticRelease(ctx, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		state, err = service.ActivateSemanticRelease(ctx, tenantID, actorID, release.ID,
			ActivateSemanticReleaseInput{
				ExpectedVersion: release.Version, ExpectedStateVersion: state.Version,
			})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("activated semantic release id=%s version=%s hash=%s", state.ActiveReleaseID, state.SemanticVersion, state.ContentHash)
		return
	}
	if os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_CREATE") != "true" {
		return
	}
	release, err := service.BootstrapSemanticRelease(ctx, tenantID, actorID,
		BootstrapSemanticReleaseInput{
			SemanticVersion: os.Getenv("SEMANTIC_BOOTSTRAP_INTEGRATION_VERSION"),
			DefaultTimezone: "Asia/Shanghai", DefaultCalendar: "GREGORIAN",
			CompletePeriodPolicy: "EXCLUDE_INCOMPLETE", Notes: "integration migration",
		})
	if err != nil {
		t.Fatal(err)
	}
	release, err = service.ValidateSemanticRelease(ctx, tenantID, actorID, release.ID,
		ValidateSemanticReleaseInput{ExpectedVersion: release.Version})
	if err != nil {
		t.Fatal(err)
	}
	if release.Status != "PROJECTING" {
		t.Fatalf("expected PROJECTING release, got %s", release.Status)
	}
	t.Logf("created semantic release id=%s version=%d hash=%s", release.ID, release.Version, release.ContentHash)
}
