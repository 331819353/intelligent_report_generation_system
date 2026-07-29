package queryruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestLiveDWDSourcePreviewResolution(t *testing.T) {
	if os.Getenv("QUERYRUNTIME_LIVE_RESOLVE") != "1" {
		t.Skip("set QUERYRUNTIME_LIVE_RESOLVE=1 to check a local control database")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	tenantID := os.Getenv("QUERYRUNTIME_LIVE_TENANT_ID")
	userID := os.Getenv("QUERYRUNTIME_LIVE_USER_ID")
	domainID := os.Getenv("QUERYRUNTIME_LIVE_DOMAIN_ID")
	if databaseURL == "" || tenantID == "" || userID == "" || domainID == "" {
		t.Fatal("live resolve environment is incomplete")
	}
	ctx := database.WithAccessContext(
		context.Background(), userID, domainID,
	)
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var raw json.RawMessage
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version.dsl_json
			FROM platform.datasets AS owner
			JOIN platform.dataset_versions AS version
			  ON version.id=owner.current_draft_version_id
			WHERE owner.layer='DWD'
			  AND owner.deleted_at IS NULL
			ORDER BY owner.updated_at DESC
			LIMIT 1`).Scan(&raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := dataset.DecodeAndNormalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NewPostgresStore(pool).Resolve(ctx, tenantID, document)
	if err != nil {
		t.Fatalf("resolve live DWD preview: %v", err)
	}
	if resolved.ExecutionDocument == nil ||
		len(resolved.Tables) != len(document.Nodes) {
		t.Fatalf("incomplete live DWD preview resolution: %#v", resolved)
	}
	if err := dataset.Validate(*resolved.ExecutionDocument); err != nil {
		if validation, ok := err.(*dataset.ValidationError); ok {
			t.Fatalf("invalid execution document: %#v", validation.Issues)
		}
		t.Fatalf("invalid execution document: %v", err)
	}
}
