package dataset

import (
	"os"
	"strings"
	"testing"
)

func TestDatasetPublicationOriginMigrationKeepsAuthorizationOutOfAuditLogs(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000072_dataset_publication_origin.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"ADD COLUMN publication_origin text",
		"SET publication_origin='HUMAN_APPROVAL'",
		"audit.detail->>'publicationSource'",
		"audit.detail->>'originTableId'",
		"audit.detail->>'publishedVersionId'",
		"audit.detail->>'versionNo'",
		"audit.detail->>'dslHash'",
		"audit.detail->>'planHash'",
		"dataset_versions_status_publication_origin_check",
		"dataset_publication_origin_facts_match",
		"request.status='PENDING'",
		"request.reserved_published_version_id=candidate.id",
		"dataset.origin_table_id IS NOT NULL",
		"pending.status='PENDING'",
		"NEW.source_draft_version_id,NEW.source_draft_record_version,NEW.publication_origin",
		"OLD.source_draft_version_id,OLD.source_draft_record_version,OLD.publication_origin",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("publication-origin migration is missing %q", fragment)
		}
	}

	const helperStart = "CREATE OR REPLACE FUNCTION platform.dataset_publication_origin_facts_match("
	start := strings.Index(migration, helperStart)
	if start < 0 {
		t.Fatal("publication-origin fact helper is missing")
	}
	helperTail := migration[start:]
	end := strings.Index(helperTail, "\n$$;")
	if end < 0 {
		t.Fatal("publication-origin fact helper has no terminator")
	}
	helper := helperTail[:end]
	if strings.Contains(helper, "audit_logs") {
		t.Fatal("runtime publication-origin authorization still depends on audit_logs")
	}
}
