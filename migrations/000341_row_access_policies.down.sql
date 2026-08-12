BEGIN;

DROP FUNCTION IF EXISTS askdata.row_access_policy_coverage(uuid,uuid);
DROP FUNCTION IF EXISTS askdata.resolve_subject_attributes(uuid,uuid);

ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','METRIC_DIMENSION',
    'DIMENSION','MEMBER','HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM',
    'CERTIFIED_EXAMPLE','TIME_CONTRACT','KPI_BUNDLE','EVAL_CASE'
  ));

DROP TABLE IF EXISTS askdata.row_access_policies;
DROP TABLE IF EXISTS platform.subject_attributes;

COMMIT;
