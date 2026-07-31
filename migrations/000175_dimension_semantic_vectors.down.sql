BEGIN;

DROP TRIGGER IF EXISTS semantic_dimensions_sync_definition_vector
  ON platform.semantic_dimensions;
DROP FUNCTION IF EXISTS platform.sync_dimension_semantic_document();
DROP TABLE IF EXISTS platform.dimension_semantic_documents;

COMMIT;
