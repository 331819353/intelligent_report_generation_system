BEGIN;

ALTER TABLE platform.ai_metadata_suggestions DROP COLUMN IF EXISTS baseline_value;

COMMENT ON COLUMN platform.ai_metadata_suggestions.expected_business_version IS NULL;

COMMIT;
