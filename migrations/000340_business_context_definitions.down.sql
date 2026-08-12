BEGIN;

ALTER TABLE askdata.dimension_members DROP COLUMN IF EXISTS definition;
ALTER TABLE askdata.metric_versions DROP COLUMN IF EXISTS business_definition;

COMMIT;
