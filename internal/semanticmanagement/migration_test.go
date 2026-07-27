package semanticmanagement

import (
	"os"
	"strings"
	"testing"
)

func TestSensitiveDimensionMigrationClosesStorageAndEmbeddingBypasses(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000065_sensitive_dimension_index_guards.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"semantic_dimensions_sensitive_index_policy_check",
		"CHECK(NOT sensitive OR member_index_policy<>'FULL')",
		"敏感维度禁止 FULL 成员扫描",
		"semantic_dimensions_apply_index_privacy_guard",
		"status='DEPRECATED'",
		"SENSITIVE_DIMENSION_INDEX_DISABLED",
		"DELETE FROM platform.semantic_documents",
		"subject_type='DIMENSION_MEMBER'",
		"semantic_documents_reject_dimension_member",
		"禁止创建语义文档",
		"OLD.status IN ('QUEUED','RUNNING') AND NEW.status='SKIPPED'",
		"member_index_policy='FULL'",
		"(attempt>0 AND started_at IS NOT NULL)",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing sensitive dimension guard %q", fragment)
		}
	}
}

func TestDimensionSurveyMigrationFixesExactEvidenceAndRiskFences(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000068_dws_dimension_survey.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"dimension_survey_runs",
		"dimension_survey_candidates",
		"'WAITING_MATERIALIZATION'",
		"dataset_versions_enqueue_dimension_survey",
		"dataset_materializations_complete_dimension_survey",
		"AFTER INSERT OR UPDATE OF status",
		"field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')",
		"'containsBusinessSamples',false",
		"materialization_snapshot_hash",
		"risk_high_cardinality",
		"risk_sensitive",
		"'cardinalityAssessment'",
		"'NOT_PROFILED'",
		"维度勘测风险策略只能收紧",
		"semantic_dimensions_high_cardinality_index_policy_check",
		"guard_published_dimension_field_sensitivity",
		"tighten_sensitive_field_dimensions",
		"apply_approved_field_sensitivity",
		"apply_activated_sensitivity_tag",
		"'dimension-field-risk:'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing dimension survey guard %q", fragment)
		}
	}
}

func TestDimensionProfileMigrationIsBoundedFencedAndAggregateOnly(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000071_dws_dimension_profiling.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"dimension_profile_jobs",
		"'dws-dimension-profile-v1'",
		"'dimension-member-policy-v1'",
		"materialization_snapshot_hash",
		"expected_row_count",
		"non_null_count",
		"null_count",
		"distinct_count",
		"distinct_overflow",
		"distinct_cap",
		"timeout_seconds",
		"work_mem_kb",
		"temp_file_limit_kb",
		"lease_owner",
		"lease_token",
		"dimension_profile_jobs_identity_key",
		"dataset_materializations_00_enqueue_dimension_profiles",
		"guard_published_dimension_profile_policy",
		"SKIPPED_POLICY",
		"SENSITIVE_FIELD_PROFILE_SKIPPED",
		"IDENTIFIER_FIELD_PROFILE_SKIPPED",
		"dataset.current_published_version_id=version.id",
		"semantic-governance-write:",
		"dimension_members_00_lock_governance_write",
		"semantic_tag_aliases_00_lock_governance_write",
		"dimension_metric_compatibility_00_lock_governance_write",
		"member.refresh_generation=dimension.member_refresh_generation",
		"refresh_job.status='SUCCEEDED'",
		"apply_dimension_profile_resource_limits(\n  selected_job_id uuid",
		"lease_expires_at>now()",
		"REVOKE ALL ON FUNCTION",
		"敏感字段只能使用 NONE 成员索引策略",
		"FULL 策略超过当前 DWS 画像允许的风险下限",
		"不保存业务值",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing bounded dimension profile guard %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"sample_value",
		"top_value",
		"minimum_value",
		"maximum_value",
	} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("profile migration must not persist %q", forbidden)
		}
	}
}

func TestSeparatedWarehouseDimensionProfileLimitsAreScoped(t *testing.T) {
	migrateRaw, err := os.ReadFile("../../scripts/migrate.sh")
	if err != nil {
		t.Fatalf("read migrate script: %v", err)
	}
	migrateSQL := string(migrateRaw)
	for _, fragment := range []string{
		"warehouse_published.apply_dimension_profile_resource_limits",
		"SECURITY DEFINER",
		"selected_timeout_seconds NOT BETWEEN 1 AND 300",
		"selected_work_mem_kb NOT BETWEEN 64 AND 262144",
		"selected_temp_file_limit_kb NOT BETWEEN 1024 AND 1048576",
		"set_config('temp_file_limit',selected_temp_file_limit_kb::text,true)",
		"REVOKE ALL ON FUNCTION",
		"GRANT EXECUTE ON FUNCTION",
	} {
		if !strings.Contains(migrateSQL, fragment) {
			t.Errorf("missing separated warehouse profile limit guard %q", fragment)
		}
	}

	storeRaw, err := os.ReadFile("dimension_profile_postgres.go")
	if err != nil {
		t.Fatalf("read dimension profile store: %v", err)
	}
	store := string(storeRaw)
	if !strings.Contains(
		store,
		"SELECT warehouse_published.apply_dimension_profile_resource_limits(",
	) {
		t.Error("separated warehouse profiling bypasses the bounded resource helper")
	}
	if strings.Contains(
		store,
		"set_config('temp_file_limit',$3,true)",
	) {
		t.Error("separated warehouse worker still sets superuser-only temp_file_limit directly")
	}

	for _, fragment := range []string{
		"REVOKE ALL ON FUNCTION platform.dataset_version_effective_domain(uuid) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION platform.dataset_version_effective_domain(uuid)",
		":'app_user'",
		":'worker_user'",
	} {
		if !strings.Contains(migrateSQL, fragment) {
			t.Errorf("missing effective domain function privilege %q", fragment)
		}
	}
}

func TestMemberRefreshDoesNotRequireProfileWritePrivilege(t *testing.T) {
	raw, err := os.ReadFile("dimension_refresh_postgres.go")
	if err != nil {
		t.Fatalf("read member refresh store: %v", err)
	}
	store := string(raw)
	if strings.Contains(store, "FOR SHARE OF materialization,profile") {
		t.Fatal("API member refresh must not require UPDATE privilege on worker-owned profiles")
	}
	if !strings.Contains(store, "FOR SHARE OF materialization") {
		t.Fatal("member refresh must fence the current physical materialization")
	}
}

func TestManualDimensionIdentificationReplaysExhaustedProfiles(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000125_manual_dimension_profile_replay.up.sql",
	)
	if err != nil {
		t.Fatalf("read dimension profile replay migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"OLD.status='FAILED' AND NEW.status='QUEUED'",
		"NEW.attempt<>0",
		"NEW.started_at IS NOT NULL",
		"UPDATE platform.dimension_profile_jobs AS profile",
		"SET status='QUEUED',attempt=0,next_attempt_at=now()",
		"profile.materialization_id=target.materialization_id",
		"profile.status='FAILED'",
		"'REPLAY_DIMENSION_PROFILE'",
		"'MANUAL_IDENTIFICATION_REPLAY'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing manual profile replay guard %q", fragment)
		}
	}
}

func TestRepeatedManualIdentificationReusesCurrentProfiles(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000126_idempotent_manual_dimension_identification.up.sql",
	)
	if err != nil {
		t.Fatalf("read idempotent manual identification migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"IF EXISTS(",
		"AND NOT EXISTS(",
		"profile.materialization_id=target.materialization_id",
		"profile.profile_version='dws-dimension-profile-v1'",
		"profile.policy_version='dimension-member-policy-v1'",
		"PERFORM platform.enqueue_dws_dimension_profiles(",
		"profile.status='FAILED'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing idempotent manual identification guard %q", fragment)
		}
	}
}

func TestSemanticRelationshipMigrationBoundsPathsAndIndexesExactLookup(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000086_semantic_relationship_contracts.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"semantic_join_path_is_valid",
		"path_length>8",
		"'fromDatasetVersionId','fromFieldId'",
		"'toDatasetVersionId','toFieldId','cardinality'",
		"'ONE_TO_ONE','MANY_TO_ONE','ONE_TO_MANY','MANY_TO_MANY'",
		"selected_fanout_policy='SAFE'",
		"previous_to_dataset_version_id",
		"dimension_metric_compatibility_join_path_contract_check",
		"propose_metric_semantic_dimension_compatibility",
		"metric_versions_propose_semantic_dimension_compatibility",
		"propose_dimension_metric_compatibility",
		"semantic_dimensions_propose_metric_compatibility",
		"'DIRECT','SAFE','[]'::jsonb",
		"'RULE',1.0000,'PROPOSED'",
		"DIMENSION_METRIC_COMPATIBILITY_RULE_BACKFILL",
		"dimension_members_tenant_normalized_dimension_active_idx",
		"tenant_id,normalized_value,dimension_id,id",
		"dimension_member_aliases_tenant_normalized_dimension_idx",
		"tenant_id,normalized_alias,dimension_id,dimension_member_id,id",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing semantic relationship guard %q", fragment)
		}
	}
}
