BEGIN;

ALTER TABLE platform.semantic_query_plan_evidence
  DROP CONSTRAINT semantic_query_plan_evidence_subject_type_check;
ALTER TABLE platform.semantic_query_plan_evidence
  ADD CONSTRAINT semantic_query_plan_evidence_subject_type_check CHECK(subject_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET','SOURCE','TAG'
  ));

ALTER TABLE platform.semantic_graph_edges
  DROP CONSTRAINT semantic_graph_edges_relation_type_check;
ALTER TABLE platform.semantic_graph_edges
  ADD CONSTRAINT semantic_graph_edges_relation_type_check CHECK(relation_type IN (
    'MEMBER_OF','DIMENSION_FIELD','METRIC_DIMENSION','METRIC_DATASET',
    'FIELD_DATASET','DATASET_VERSION_OF','DATASET_DEPENDS_ON',
    'DATASET_SOURCE','TAGGED_AS','ALIAS_OF'
  ));

ALTER TABLE platform.semantic_graph_nodes
  DROP CONSTRAINT semantic_graph_nodes_node_type_check;
ALTER TABLE platform.semantic_graph_nodes
  ADD CONSTRAINT semantic_graph_nodes_node_type_check CHECK(node_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET','SOURCE','TAG'
  ));

ALTER TABLE platform.semantic_query_plans
  DROP COLUMN IF EXISTS execution_error_code,
  DROP COLUMN IF EXISTS executed_query_id,
  DROP COLUMN IF EXISTS selected_materialization_id,
  DROP COLUMN IF EXISTS selected_metric_id;

COMMIT;
