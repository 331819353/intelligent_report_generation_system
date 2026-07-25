DROP TRIGGER IF EXISTS dataset_versions_enforce_ads_consumer_contract
  ON platform.dataset_versions;
DROP FUNCTION IF EXISTS platform.enforce_ads_consumer_contract();
DROP TRIGGER IF EXISTS semantic_change_outbox_mark_graph_dirty
  ON platform.semantic_change_outbox;
DROP FUNCTION IF EXISTS platform.mark_semantic_graph_dirty();
DROP TRIGGER IF EXISTS semantic_qa_settings_wake_graph_projection
  ON platform.semantic_qa_settings;
DROP FUNCTION IF EXISTS platform.wake_semantic_graph_projection();

DROP TABLE IF EXISTS platform.semantic_golden_questions;
DROP TABLE IF EXISTS platform.semantic_question_templates;
DROP TABLE IF EXISTS platform.semantic_query_plan_evidence;
DROP TABLE IF EXISTS platform.semantic_query_plans;
DROP TABLE IF EXISTS platform.semantic_graph_projection_state;
DROP TABLE IF EXISTS platform.semantic_graph_edges;
DROP TABLE IF EXISTS platform.semantic_graph_nodes;
DROP TABLE IF EXISTS platform.semantic_graph_generations;
DROP TABLE IF EXISTS platform.warehouse_dag_stage_runs;
DROP TABLE IF EXISTS platform.warehouse_dag_runs;
DROP TABLE IF EXISTS platform.warehouse_dag_change_validations;
DROP TABLE IF EXISTS platform.warehouse_dag_change_operations;
DROP TABLE IF EXISTS platform.warehouse_dag_change_sets;
DROP TABLE IF EXISTS platform.semantic_consumer_contract_inputs;
DROP TABLE IF EXISTS platform.semantic_consumer_contracts;
DROP TABLE IF EXISTS platform.semantic_qa_settings;
