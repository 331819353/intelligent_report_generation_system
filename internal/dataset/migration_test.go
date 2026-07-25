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

func TestSystemMappedDraftUpgradeMigrationUsesCurrentRelationalFacts(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000076_system_mapped_draft_upgrade_publication.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"WHEN 'SYSTEM_MAPPED_DEFAULT'",
		"dataset.version=candidate.source_draft_record_version",
		"draft.record_version=candidate.source_draft_record_version",
		"draft.schema_hash=candidate.schema_hash",
		"draft.plan_hash=candidate.plan_hash",
		")=candidate.source_draft_record_version",
		"revision.operation_type='CREATE'",
		"revision.operation_type='ROLLBACK'",
		"platform.dataset_publication_requests AS request",
		"history.status IN ('PUBLISHED','STALE','DEPRECATED')",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("system mapped draft upgrade migration is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "audit_logs") {
		t.Fatal("system mapped default publication authorization depends on audit logs")
	}
}

func TestDeletedODSMetadataCleanupMigrationOnlyTouchesControlPlaneAssets(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000077_deleted_ods_metadata_asset_cleanup.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.ToUpper(string(raw))
	required := []string{
		"UPDATE PLATFORM.METADATA_COLUMNS",
		"UPDATE PLATFORM.METADATA_TABLES",
		"DATASET.ORIGIN_TABLE_ID",
		"DATASET.LAYER='ODS'",
		"DATASET.DELETED_AT IS NOT NULL",
		"ASSET_STATUS='INACTIVE'",
		"MANAGEMENT_STATUS='DISABLED'",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("deleted ODS metadata cleanup migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"DROP TABLE", "TRUNCATE", "DELETE FROM"} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("deleted ODS metadata cleanup migration contains %q", forbidden)
		}
	}
}

func TestODSDWDModelingMigrationSchedulesLLMWorkAndGuardsDeletion(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000078_ods_dwd_modeling.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	required := []string{
		"CREATE TABLE platform.dwd_modeling_jobs",
		"CREATE TABLE platform.dwd_modeling_outputs",
		"prompt_version text NOT NULL DEFAULT 'dwd-modeling-v1'",
		"ai_request_id uuid",
		"NEW.layer='ODS'",
		"NEW.status='PUBLISHED'",
		"interval '5 minutes'",
		"UNIQUE(tenant_id,trigger_dataset_version_id)",
		"last_generated_schema_hash",
		"dataset_versions_enqueue_dwd_modeling",
		"downstream_version.layer='DWD'",
		"datasets_ods_dwd_reference_guard",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("ODS-to-DWD migration is missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToUpper(migration), "DROP TABLE PLATFORM.METADATA") {
		t.Fatal("ODS-to-DWD migration must not alter physical source tables")
	}
}

func TestDWDModelingOtherRoleMigrationMatchesLLMClassificationContract(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000079_dwd_modeling_other_role.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"DROP CONSTRAINT dwd_modeling_jobs_trigger_role_check",
		"'FACT','DIMENSION','MASTER','OTHER'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("DWD OTHER role migration is missing %q", fragment)
		}
	}
}

func TestODSDWDHistoricalReferenceGuardIncludesDeprecatedVersions(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000080_ods_dwd_historical_reference_guard.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"downstream_version.layer='DWD'",
		"downstream_dataset.deleted_at IS NULL",
		"datasets_ods_dwd_reference_guard",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("historical DWD reference guard is missing %q", fragment)
		}
	}
	if strings.Contains(migration, "downstream_version.status<>'DEPRECATED'") {
		t.Fatal("historical DWD references are incorrectly ignored after version deprecation")
	}
}

func TestPostApprovalProcessingMigrationCancelsOnlyLegacyPreApprovalWork(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000083_post_approval_dataset_processing.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"UPDATE platform.metric_candidate_preparation_jobs",
		"WHERE status IN ('PENDING','RUNNING')",
		"status='CANCELLED'",
		"error_code='MOVED_AFTER_APPROVAL'",
		"request.status='PENDING'",
		"metric_candidate_generation_status='PENDING'",
		"v83 起不再创建",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("post-approval processing migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"UPDATE platform.dataset_build_runs",
		"UPDATE platform.metric_extraction_jobs",
		"DELETE FROM",
		"DROP TABLE",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("post-approval migration unexpectedly contains %q", forbidden)
		}
	}
}

func TestResumableWarehouseModelingMigrationPersistsOnlyValidatedStages(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000085_resumable_warehouse_modeling.up.sql",
	)
	if err != nil {
		t.Fatalf("read resumable warehouse modeling migration: %v", err)
	}
	migration := string(raw)
	for _, fragment := range []string{
		"ADD COLUMN checkpoint_version",
		"ADD COLUMN claimed_checkpoint_version",
		"CREATE TABLE platform.dwd_modeling_checkpoints",
		"'CLASSIFICATION','FACT_DESIGN'",
		"snapshot_hash",
		"prompt_version",
		"payload_hash",
		"materialization_json_is_safe(payload_json)",
		"REFERENCES platform.ai_requests(id,tenant_id)",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("resumable modeling migration is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"prompt_text", "response_text", "sample_rows", "physical_table",
	} {
		if strings.Contains(strings.ToLower(migration), forbidden) {
			t.Fatalf(
				"resumable modeling migration stores forbidden content %q",
				forbidden,
			)
		}
	}
}
