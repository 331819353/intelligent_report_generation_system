DROP TRIGGER IF EXISTS semantic_query_execution_runs_guard
  ON platform.semantic_query_execution_runs;
DROP FUNCTION IF EXISTS platform.guard_semantic_query_execution_run();
DROP TABLE IF EXISTS platform.semantic_query_execution_runs;
DROP FUNCTION IF EXISTS platform.load_report_runtime_query_artifact(uuid,uuid,text);
