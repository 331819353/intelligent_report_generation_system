package semanticqa

import (
	"os"
	"strings"
	"testing"
)

func TestSemanticQAMigrationsContainAuthorityRecoveryAndPrivacyFences(t *testing.T) {
	files := []string{
		"000087_semantic_qa_control_plane.up.sql",
		"000089_semantic_qa_candidate_patch.up.sql",
		"000090_semantic_qa_quality_catalog.up.sql",
		"000091_semantic_query_execution_evidence.up.sql",
		"000093_dws_analysis_modeling.up.sql",
		"000094_semantic_materialization_graph_event.up.sql",
		"000095_semantic_query_execution_quality.up.sql",
	}
	var combined strings.Builder
	for _, file := range files {
		raw, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		combined.Write(raw)
	}
	sql := combined.String()
	for _, fragment := range []string{
		"CREATE TABLE platform.warehouse_dag_change_sets(",
		"expected_operation_count",
		"CREATE TABLE platform.semantic_graph_generations(",
		"CREATE TABLE platform.semantic_query_plan_evidence(",
		"question_hash",
		"CREATE OR REPLACE FUNCTION platform.enforce_ads_consumer_contract()",
		"version.layer='DWS' AND version.status='PUBLISHED'",
		"CREATE TABLE platform.semantic_golden_question_sets(",
		"CREATE TABLE platform.semantic_golden_question_runs(",
		"selected_materialization_id",
		"'MATERIALIZATION'",
		"CREATE TABLE platform.dws_modeling_jobs(",
		"'WAITING_DEPENDENCY'",
		"CREATE TABLE platform.dws_modeling_outputs(",
		"dataset_materializations_enqueue_semantic_graph",
		"execution_duration_ms",
		"execution_row_count",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("semantic QA migrations missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"raw_question",
		"result_rows",
		"generated_sql",
	} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("semantic QA control plane must not persist %q", forbidden)
		}
	}
}
