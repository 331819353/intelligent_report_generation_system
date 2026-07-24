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
