BEGIN;

ALTER TABLE platform.semantic_query_plans
  ADD COLUMN selected_metric_id uuid,
  ADD COLUMN selected_materialization_id uuid,
  ADD COLUMN executed_query_id text NOT NULL DEFAULT ''
    CHECK(length(executed_query_id)<=128 AND executed_query_id !~ '[[:cntrl:]]'),
  ADD COLUMN execution_error_code text NOT NULL DEFAULT ''
    CHECK(length(execution_error_code)<=128);

ALTER TABLE platform.semantic_graph_nodes
  DROP CONSTRAINT semantic_graph_nodes_node_type_check;
ALTER TABLE platform.semantic_graph_nodes
  ADD CONSTRAINT semantic_graph_nodes_node_type_check CHECK(node_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET',
    'MATERIALIZATION','SOURCE','TAG'
  ));

ALTER TABLE platform.semantic_graph_edges
  DROP CONSTRAINT semantic_graph_edges_relation_type_check;
ALTER TABLE platform.semantic_graph_edges
  ADD CONSTRAINT semantic_graph_edges_relation_type_check CHECK(relation_type IN (
    'MEMBER_OF','DIMENSION_FIELD','METRIC_DIMENSION','METRIC_DATASET',
    'FIELD_DATASET','DATASET_VERSION_OF','DATASET_DEPENDS_ON',
    'DATASET_MATERIALIZED_AS','DATASET_SOURCE','TAGGED_AS','ALIAS_OF'
  ));

ALTER TABLE platform.semantic_query_plan_evidence
  DROP CONSTRAINT semantic_query_plan_evidence_subject_type_check;
ALTER TABLE platform.semantic_query_plan_evidence
  ADD CONSTRAINT semantic_query_plan_evidence_subject_type_check CHECK(subject_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET',
    'MATERIALIZATION','SOURCE','TAG'
  ));

COMMENT ON COLUMN platform.semantic_query_plans.selected_materialization_id IS
  '规划时已由同一 graph generation 证明为 ACTIVE 的精确物化；执行前仍需重新校验';

COMMIT;
