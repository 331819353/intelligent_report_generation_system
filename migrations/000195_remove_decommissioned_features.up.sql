-- Report, metric and semantic-Q&A features have been retired. Historical
-- migrations remain immutable; this migration removes their runtime schema.
DELETE FROM platform.object_permissions
WHERE object_type IN (
  'METRIC','METRIC_VERSION','METRIC_CANDIDATE','REPORT','REPORT_VERSION',
  'DIMENSION','SEMANTIC_ASSET','SEMANTIC_RELEASE'
);

DELETE FROM platform.permissions
WHERE resource_type IN ('METRIC','REPORT','AI');

DELETE FROM platform.roles
WHERE is_system AND code IN ('analyst','report_designer','viewer');

DELETE FROM platform.asset_dependencies
WHERE downstream_type <> 'DATASET';

DROP TABLE IF EXISTS
  platform.report_draft_component_indexes,
  platform.report_draft_dependencies,
  platform.report_drafts,
  platform.report_edit_guards,
  platform.report_idempotency_records,
  platform.report_publication_idempotency,
  platform.report_revisions,
  platform.report_version_component_indexes,
  platform.report_version_dependencies,
  platform.report_versions,
  platform.reports,
  platform.metric_candidate_preparation_jobs,
  platform.metric_candidates,
  platform.metric_dependencies,
  platform.metric_dimensions,
  platform.metric_extraction_jobs,
  platform.metric_publish_idempotency,
  platform.metric_semantic_documents,
  platform.metric_versions,
  platform.metrics,
  platform.dimension_member_aliases,
  platform.dimension_member_refresh_jobs,
  platform.dimension_member_semantic_documents,
  platform.dimension_members,
  platform.dimension_metric_compatibility,
  platform.dimension_profile_jobs,
  platform.dimension_semantic_documents,
  platform.dimension_survey_candidates,
  platform.dimension_survey_runs,
  platform.dimension_where_decisions,
  platform.dimension_where_design_policies,
  platform.semantic_change_outbox,
  platform.semantic_consumer_contract_inputs,
  platform.semantic_consumer_contracts,
  platform.semantic_dimensions,
  platform.semantic_documents,
  platform.semantic_execution_registry,
  platform.semantic_golden_question_runs,
  platform.semantic_golden_question_sets,
  platform.semantic_golden_questions,
  platform.semantic_graph_edges,
  platform.semantic_graph_generations,
  platform.semantic_graph_nodes,
  platform.semantic_graph_plan_cache,
  platform.semantic_graph_projection_state,
  platform.semantic_parsing_rules,
  platform.semantic_qa_settings,
  platform.semantic_query_feedback,
  platform.semantic_query_plan_evidence,
  platform.semantic_query_plans,
  platform.semantic_question_artifacts,
  platform.semantic_question_run_events,
  platform.semantic_question_runs,
  platform.semantic_question_templates,
  platform.semantic_release_events,
  platform.semantic_release_objects,
  platform.semantic_release_projections,
  platform.semantic_release_search_documents,
  platform.semantic_release_state,
  platform.semantic_releases,
  platform.semantic_term_assets,
  platform.semantic_term_embedding_outbox,
  platform.semantic_tool_calls,
  platform.warehouse_dag_change_operations,
  platform.warehouse_dag_change_sets,
  platform.warehouse_dag_change_validations,
  platform.warehouse_dag_runs,
  platform.warehouse_dag_stage_runs,
  platform.dws_modeling_jobs,
  platform.dws_modeling_outputs,
  platform.ads_modeling_jobs,
  platform.ads_modeling_outputs
CASCADE;

ALTER TABLE IF EXISTS platform.query_runs
  DROP COLUMN IF EXISTS metric_id,
  DROP COLUMN IF EXISTS metric_version_id;

ALTER TABLE IF EXISTS platform.dataset_publication_requests
  DROP COLUMN IF EXISTS metric_candidate_result,
  DROP COLUMN IF EXISTS metric_candidate_generation_status,
  DROP COLUMN IF EXISTS metric_candidate_total,
  DROP COLUMN IF EXISTS metric_candidate_ready_count,
  DROP COLUMN IF EXISTS metric_candidate_review_count,
  DROP COLUMN IF EXISTS metric_candidate_blocked_count,
  DROP COLUMN IF EXISTS metric_candidate_warning,
  DROP COLUMN IF EXISTS metric_candidate_error_code,
  DROP COLUMN IF EXISTS metric_candidate_generated_at;

DO $$
DECLARE
  function_identity text;
BEGIN
  FOR function_identity IN
    SELECT procedure.oid::regprocedure::text
    FROM pg_proc AS procedure
    JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
    WHERE namespace.nspname='platform'
      AND (
        procedure.proname LIKE 'semantic\_%' ESCAPE '\'
        OR procedure.proname LIKE '%metric%'
        OR procedure.proname LIKE '%report%'
        OR procedure.proname LIKE '%dws\_modeling%' ESCAPE '\'
        OR procedure.proname LIKE '%ads\_modeling%' ESCAPE '\'
        OR procedure.proname LIKE '%dimension\_profile%' ESCAPE '\'
        OR procedure.proname LIKE '%dimension\_member%' ESCAPE '\'
        OR procedure.proname LIKE '%dimension\_survey%' ESCAPE '\'
        OR procedure.proname LIKE '%warehouse\_dag%' ESCAPE '\'
      )
  LOOP
    EXECUTE 'DROP FUNCTION IF EXISTS '||function_identity||' CASCADE';
  END LOOP;
END
$$;
