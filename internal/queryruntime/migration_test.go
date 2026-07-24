package queryruntime

import (
	"os"
	"strings"
	"testing"
)

func TestWarehouseQueryMigrationKeepsSourceAndMaterializationAuditExclusive(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000066_queryruntime_warehouse_execution.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"query_runs_execution_identity_check",
		"execution_engine='POSTGRES' AND data_source_id IS NULL",
		"CREATE TABLE platform.query_run_materializations",
		"CREATE TABLE platform.query_candidate_run_materializations",
		"REFERENCES platform.dataset_materializations",
		"published_schema='warehouse_published'",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"reject_query_materialization_mutation",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}
