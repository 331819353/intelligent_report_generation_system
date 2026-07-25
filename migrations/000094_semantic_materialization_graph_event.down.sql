DROP TRIGGER IF EXISTS dataset_materializations_enqueue_semantic_graph
  ON platform.dataset_materializations;
DROP FUNCTION IF EXISTS platform.enqueue_semantic_materialization_change();
