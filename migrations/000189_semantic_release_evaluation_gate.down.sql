BEGIN;

DROP FUNCTION IF EXISTS platform.semantic_evaluation_set_passes(uuid,text,text);
ALTER TABLE platform.semantic_releases
  DROP CONSTRAINT IF EXISTS semantic_releases_evaluation_shape_check,
  DROP CONSTRAINT IF EXISTS semantic_releases_evaluation_set_fk,
  DROP COLUMN IF EXISTS evaluation_set_content_hash,
  DROP COLUMN IF EXISTS evaluation_set_id;

COMMIT;
