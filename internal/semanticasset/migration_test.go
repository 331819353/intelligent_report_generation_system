package semanticasset

import (
	"os"
	"strings"
	"testing"
)

func TestSemanticTermAssetMigrationKeepsEmbeddingInputSeparate(t *testing.T) {
	raw, err := os.ReadFile(
		"../../migrations/000129_semantic_term_assets.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"CREATE TABLE platform.semantic_term_assets",
		"common_term citext NOT NULL",
		"mapping_value text NOT NULL",
		"knowledge_type text NOT NULL",
		"embedding halfvec(2560)",
		"UNIQUE(tenant_id,knowledge_type,common_term)",
		"CREATE TABLE platform.semantic_term_embedding_outbox",
		"AFTER INSERT OR UPDATE OF common_term,status",
		"FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
}
