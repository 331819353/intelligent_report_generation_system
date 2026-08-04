DROP POLICY IF EXISTS semantic_tag_aliases_read_scope
  ON platform.semantic_tag_aliases;
DROP POLICY IF EXISTS semantic_tag_aliases_write_scope
  ON platform.semantic_tag_aliases;
DROP FUNCTION IF EXISTS platform.semantic_tag_can_read(uuid);
DROP FUNCTION IF EXISTS platform.semantic_tag_can_write(uuid);
