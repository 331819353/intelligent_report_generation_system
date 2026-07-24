package materialization

import (
	"os"
	"strings"
	"testing"
)

func TestFoundationMigrationKeepsBusinessRowsOutOfPlatform(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000060_materialization_semantic_foundation.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	requiredSchemas := []string{
		"warehouse_staging", "warehouse_ods", "warehouse_dwd",
		"warehouse_dws", "warehouse_published",
	}
	for _, schema := range requiredSchemas {
		if !strings.Contains(sql, "CREATE SCHEMA IF NOT EXISTS "+schema) ||
			!strings.Contains(sql, "REVOKE ALL ON SCHEMA "+schema+" FROM PUBLIC") {
			t.Errorf("schema %s does not have the expected least-privilege contract", schema)
		}
		if strings.Contains(sql, "CREATE TABLE "+schema+".") {
			t.Errorf("migration creates dynamic business tables in %s", schema)
		}
	}

	requiredTables := []string{
		"dataset_build_runs", "build_run_inputs", "build_node_runs",
		"dataset_materializations", "data_quality_results", "semantic_tags",
		"semantic_tag_aliases", "asset_tag_bindings", "semantic_documents",
		"semantic_change_outbox", "semantic_dimensions", "dimension_members",
		"dimension_member_aliases", "dimension_metric_compatibility",
	}
	for _, table := range requiredTables {
		if !strings.Contains(sql, "CREATE TABLE platform."+table+"(") {
			t.Errorf("missing table platform.%s", table)
		}
		if !strings.Contains(sql, "ALTER TABLE platform."+table+" ENABLE ROW LEVEL SECURITY") ||
			!strings.Contains(sql, "ALTER TABLE platform."+table+" FORCE ROW LEVEL SECURITY") {
			t.Errorf("platform.%s is not protected by forced RLS", table)
		}
	}
}

func TestFoundationMigrationHasRecoveryAndImmutabilityFences(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000060_materialization_semantic_foundation.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	required := []string{
		"lease_token uuid",
		"dataset_build_runs_enforce_transition",
		"data_quality_results_immutable",
		"dataset_materializations_one_active_dataset_idx",
		"semantic_change_outbox_claim_idx",
		"semantic_documents_embedding_hnsw_idx",
		"asset_tag_bindings_enqueue_change",
		"ADD COLUMN description text NOT NULL DEFAULT ''",
		"datasets_draft_layer_consistency",
		"dataset_fields_enqueue_semantic_change",
		"metric_versions_enqueue_semantic_change",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing migration safety contract %q", fragment)
		}
	}
}

func TestSourceAndLayerGuardMigrationClosesDirectWriteBypasses(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000062_materialization_source_and_layer_guards.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	required := []string{
		"owner.current_published_version_id=version.id",
		"source.current_published_version_id=source_version.id",
		"input_data_source_id",
		"input_data_source_version_id",
		"build_run_inputs_data_source_version_fk",
		"source_version.source_type IN ('MYSQL','ORACLE')",
		"source_version.source_type='EXCEL'",
		"build_run_inputs_immutable",
		"datasets_enforce_published_layer_identity",
		"dataset_versions_enforce_published_layer_identity",
		"OLD.current_published_version_id",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing source/layer guard %q", fragment)
		}
	}
}
