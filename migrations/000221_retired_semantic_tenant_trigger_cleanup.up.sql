-- 000195 removed the retired semantic release tables, but the tenant trigger
-- function did not match that migration's semantic_* function-name filter.
-- A fresh tenant insert therefore called a function that referenced the
-- already-dropped platform.semantic_release_state relation.
DROP TRIGGER IF EXISTS tenants_initialize_semantic_release_state
  ON platform.tenants;
DROP FUNCTION IF EXISTS platform.initialize_semantic_release_state();
