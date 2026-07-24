package datasource

import (
	"os"
	"strings"
	"testing"
)

func TestPublicationGuardMigrationClosesDirectWriteBypasses(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000063_data_source_publication_guards.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	required := []string{
		"data_source_test_runs_validate_evidence",
		"source.current_draft_version_id=version.id",
		"expected_hash IS DISTINCT FROM NEW.config_hash",
		"data_source_test_runs_immutable",
		"data_sources_reject_direct_published_insert",
		"data_sources_require_publication_evidence",
		"NEW.current_draft_version_id IS DISTINCT FROM NEW.current_published_version_id",
		"NEW.last_tested_version_id IS DISTINCT FROM NEW.current_published_version_id",
		"NEW.last_tested_config_hash IS DISTINCT FROM published_hash",
		"test_run.status='PASSED'",
		"test_run.expires_at>clock_timestamp()",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing publication guard %q", fragment)
		}
	}
}

func TestMetadataSampleConsentMigrationDefaultsClosedAndFencesRevocation(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000067_metadata_sample_consent.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"metadata_sample_mode text NOT NULL DEFAULT 'DENY'",
		"CHECK(metadata_sample_mode IN ('DENY','MASK','RAW'))",
		"sample_data_mode text NOT NULL DEFAULT 'DENY'",
		"sample_policy_version",
		"sample_consent_by",
		"data_source_metadata_jobs_sample_consent_shape_check",
		"enforce_metadata_sample_consent",
		"元数据样本授权快照不可修改",
		"请求的元数据样本模式超出租户策略",
		"元数据样本任务必须由请求人逐任务明确同意",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing metadata sample consent guard %q", fragment)
		}
	}
}

func TestMetadataSampleDefaultMigrationUsesTenMaskedRowsWithoutOpeningRawValues(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000075_metadata_sample_default_mask.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"ALTER COLUMN metadata_sample_mode SET DEFAULT 'MASK'",
		"SET metadata_sample_mode='MASK'",
		"WHERE metadata_sample_mode='DENY'",
		"原始值仍必须逐任务明确选择 RAW",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing ten-row masked sample default %q", fragment)
		}
	}
}

func TestConnectionTestEvidenceMigrationFixesThirtyMinuteTTL(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000069_data_source_test_evidence_ttl.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"NEW.started_at<NEW.completed_at-interval '15 minutes'",
		"NEW.completed_at>clock_timestamp()+interval '5 seconds'",
		"NEW.expires_at IS DISTINCT FROM",
		"NEW.completed_at+interval '30 minutes'",
		"test_run.expires_at IS NOT DISTINCT FROM NEW.test_expires_at",
		"test_run.completed_at<=clock_timestamp()+interval '5 seconds'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing connection-test TTL guard %q", fragment)
		}
	}
}

func TestAsyncConnectionTestMigrationRequiresWorkerAttestation(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000070_data_source_connection_test_attestation.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"CREATE TABLE platform.data_source_connection_test_jobs",
		"CREATE TABLE platform.data_source_connection_test_attestations",
		"platform.enqueue_data_source_connection_test(",
		"platform.claim_data_source_connection_test(",
		"platform.heartbeat_data_source_connection_test(",
		"platform.complete_data_source_connection_test(",
		"platform.fail_data_source_connection_test(",
		"v_completed_at timestamptz := clock_timestamp()",
		"v_completed_at+interval '30 minutes'",
		"attestation.attestation_version='connection-test-worker-v1'",
		"job.status='SUCCEEDED'",
		"历史连接测试记录只读",
		"current_draft_version_id IS DISTINCT FROM current_published_version_id",
		"validation_status='UNTESTED'",
		"All source/job mutations use source -> job row order",
		"Expired terminal attempts also follow source -> job",
		"session_user||':'||v_job.lease_owner",
		"job.next_attempt_at<=clock_timestamp()",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing asynchronous connection-test guard %q", fragment)
		}
	}
	exhaustedSweep := strings.Index(
		sql, "Expired terminal attempts also follow source -> job",
	)
	staleSweep := strings.Index(
		sql, "The stale repair sweep is also source -> job",
	)
	candidateClaim := strings.Index(sql, "SELECT candidate.id")
	if exhaustedSweep < 0 || staleSweep <= exhaustedSweep ||
		candidateClaim <= staleSweep {
		t.Error("claim function does not preserve source -> job lock ordering")
	}
}

func TestMigrationScriptRevokesProtectedConnectionTestWrites(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/migrate.sh")
	if err != nil {
		t.Fatalf("read migration script: %v", err)
	}
	script := string(raw)
	for _, fragment := range []string{
		"POSTGRES_CONNECTION_TEST_USER",
		"REVOKE INSERT, UPDATE, DELETE ON TABLE",
		"platform.data_source_test_runs",
		"platform.data_source_connection_test_jobs",
		"platform.data_source_connection_test_attestations",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA platform",
		"REVOKE INSERT, UPDATE, DELETE ON TABLES FROM",
		"GRANT EXECUTE ON FUNCTION",
		"platform.enqueue_data_source_connection_test(uuid,uuid,text)",
		"platform.complete_data_source_connection_test(uuid,uuid,text,bigint)",
		"admin and all runtime database roles must be distinct",
		`[ "$APP_ROLE" = "$ADMIN_ROLE" ]`,
		`[ "$WORKER_ROLE" = "$ADMIN_ROLE" ]`,
		`[ "$CONNECTION_TEST_ROLE" = "$ADMIN_ROLE" ]`,
		"NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit",
		"FROM pg_auth_members AS membership",
		"BEGIN;",
		"COMMIT;",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("missing connection-test role boundary %q", fragment)
		}
	}
	initRaw, err := os.ReadFile(
		"../../deployments/postgres/init/001-create-app-role.sh",
	)
	if err != nil {
		t.Fatalf("read role init script: %v", err)
	}
	initScript := string(initRaw)
	for _, fragment := range []string{
		"admin and all runtime database roles must be distinct",
		`[ "$POSTGRES_APP_USER" = "$POSTGRES_USER" ]`,
		`[ "$POSTGRES_WORKER_USER" = "$POSTGRES_USER" ]`,
		`[ "$POSTGRES_CONNECTION_TEST_USER" = "$POSTGRES_USER" ]`,
		"NOREPLICATION NOBYPASSRLS",
		"NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit",
		"FROM pg_auth_members AS membership",
		"BEGIN;",
		"COMMIT;",
	} {
		if !strings.Contains(initScript, fragment) {
			t.Errorf("missing transactional role hardening %q", fragment)
		}
	}
}
