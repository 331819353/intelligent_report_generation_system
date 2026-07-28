DROP TRIGGER IF EXISTS semantic_term_embedding_outbox_set_updated_at
  ON platform.semantic_term_embedding_outbox;
DROP TRIGGER IF EXISTS semantic_term_assets_set_updated_at
  ON platform.semantic_term_assets;
DROP TRIGGER IF EXISTS semantic_term_assets_enqueue_embedding
  ON platform.semantic_term_assets;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_term_embedding();
DROP TABLE IF EXISTS platform.semantic_term_embedding_outbox;
DROP TABLE IF EXISTS platform.semantic_term_assets;
