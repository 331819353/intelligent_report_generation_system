DROP TABLE IF EXISTS askdata.query_feedback;
DROP TABLE IF EXISTS askdata.evaluation_runs;
DROP TABLE IF EXISTS askdata.evaluation_case_reviews;
DROP TABLE IF EXISTS askdata.evaluation_cases;
DROP TABLE IF EXISTS askdata.evaluation_sets;

DROP FUNCTION IF EXISTS askdata.seal_evaluation_set(uuid,uuid);
DROP FUNCTION IF EXISTS askdata.enforce_query_feedback();
DROP FUNCTION IF EXISTS askdata.enforce_evaluation_run_append();
DROP FUNCTION IF EXISTS askdata.refresh_evaluation_case_review_count();
DROP FUNCTION IF EXISTS askdata.enforce_evaluation_case_review();
DROP FUNCTION IF EXISTS askdata.enforce_evaluation_case_lifecycle();
DROP FUNCTION IF EXISTS askdata.enforce_evaluation_set_lifecycle();
DROP FUNCTION IF EXISTS askdata.evaluation_set_manifest_hash(uuid);
DROP FUNCTION IF EXISTS askdata.evaluation_case_can_access(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS askdata.evaluation_control_can_access(uuid,uuid);

ALTER TABLE IF EXISTS askdata.releases
  DROP CONSTRAINT IF EXISTS askdata_releases_evaluation_pin_key;
