DROP TRIGGER IF EXISTS report_asset_projection_enqueue ON askdata.report_semantic_assets;
DROP TRIGGER IF EXISTS report_asset_certification_actor_guard ON askdata.report_asset_certifications;
DROP TRIGGER IF EXISTS report_version_asset_extraction_enqueue ON platform.report_versions;
DROP TABLE IF EXISTS askdata.report_asset_projection_outbox;
DROP TABLE IF EXISTS askdata.report_asset_extraction_outbox;
DROP FUNCTION IF EXISTS askdata.enqueue_report_asset_projection();
DROP FUNCTION IF EXISTS askdata.guard_report_asset_certification_actor();
DROP FUNCTION IF EXISTS askdata.enqueue_report_version_asset_extraction();
ALTER TABLE askdata.report_semantic_assets
  DROP COLUMN IF EXISTS projection_state,
  DROP COLUMN IF EXISTS contains_uncertified_free_text,
  DROP COLUMN IF EXISTS block_title,
  DROP COLUMN IF EXISTS section_purpose,
  DROP COLUMN IF EXISTS report_description,
  DROP COLUMN IF EXISTS report_title,
  DROP COLUMN IF EXISTS sensitivity,
  DROP COLUMN IF EXISTS query_plan_hash,
  DROP COLUMN IF EXISTS semantic_release_content_hash;
