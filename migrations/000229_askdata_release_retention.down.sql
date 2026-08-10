DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM askdata.releases WHERE status IN ('RETAINED','RETIRED'))
    OR EXISTS(SELECT 1 FROM askdata.release_references) THEN
    RAISE EXCEPTION 'cannot roll back release retention while retained releases or references exist';
  END IF;
END
$$;

DROP TRIGGER IF EXISTS askdata_evaluation_sets_release_reference ON askdata.evaluation_sets;
DROP TRIGGER IF EXISTS askdata_releases_active_asset_references_update ON askdata.releases;
DROP TRIGGER IF EXISTS askdata_releases_active_asset_references_insert ON askdata.releases;
DROP TRIGGER IF EXISTS askdata_evaluation_case_versions_release_reference ON askdata.evaluation_case_versions;
DROP TRIGGER IF EXISTS askdata_kpi_bundle_versions_release_reference ON askdata.kpi_bundle_versions;
DROP TRIGGER IF EXISTS askdata_certified_example_versions_release_reference ON askdata.certified_example_versions;
DROP TRIGGER IF EXISTS askdata_question_runs_00_runnable_release ON askdata.question_runs;
DROP TRIGGER IF EXISTS askdata_releases_retention_events ON askdata.releases;
DROP TRIGGER IF EXISTS askdata_releases_retention_lifecycle ON askdata.releases;
DROP TRIGGER IF EXISTS askdata_release_references_retain ON askdata.release_references;
DROP TRIGGER IF EXISTS askdata_release_references_validate ON askdata.release_references;

DROP FUNCTION IF EXISTS askdata.sync_sealed_evaluation_set_release_reference();
DROP FUNCTION IF EXISTS askdata.sync_active_release_asset_references();
DROP FUNCTION IF EXISTS askdata.backfill_active_release_asset_references(uuid);
DROP FUNCTION IF EXISTS askdata.sync_certified_asset_release_reference();
DROP FUNCTION IF EXISTS askdata.upsert_release_reference(uuid,uuid,text,uuid,text,uuid);
DROP FUNCTION IF EXISTS askdata.reject_non_runnable_question_release();
DROP FUNCTION IF EXISTS askdata.cleanup_retained_release_projections(uuid);
DROP FUNCTION IF EXISTS askdata.release_registry_facts_complete(uuid);
DROP FUNCTION IF EXISTS askdata.retire_release(uuid);
DROP FUNCTION IF EXISTS askdata.record_release_retention_event();
DROP FUNCTION IF EXISTS askdata.retain_referenced_release();
DROP FUNCTION IF EXISTS askdata.enforce_release_retention_lifecycle();
DROP FUNCTION IF EXISTS askdata.validate_release_reference();

DROP TABLE askdata.release_references;

ALTER TABLE askdata.release_events
  DROP CONSTRAINT askdata_release_events_event_type_check;
ALTER TABLE askdata.release_events
  ADD CONSTRAINT release_events_event_type_check CHECK(event_type IN (
    'CREATED','VALIDATING','PROJECTING','PROJECTION_READY','PROJECTION_FAILED',
    'READY','ACTIVATED','SUPERSEDED','BLOCKED'
  ));

ALTER TABLE askdata.releases
  DROP CONSTRAINT askdata_releases_retention_shape_check,
  DROP CONSTRAINT askdata_releases_activation_shape_check,
  DROP CONSTRAINT askdata_releases_ready_shape_check,
  DROP CONSTRAINT askdata_releases_status_check,
  DROP COLUMN retired_at,
  DROP COLUMN retention_until,
  DROP COLUMN retained_at;

ALTER TABLE askdata.releases
  ADD CONSTRAINT releases_status_check CHECK(status IN (
    'DRAFT','VALIDATING','PROJECTING','READY','ACTIVE','BLOCKED','SUPERSEDED'
  )),
  ADD CONSTRAINT askdata_releases_ready_shape_check CHECK(
    (status IN ('READY','ACTIVE','SUPERSEDED') AND ready_at IS NOT NULL)
    OR status NOT IN ('READY','ACTIVE','SUPERSEDED')
  ),
  ADD CONSTRAINT askdata_releases_activation_shape_check CHECK(
    (status IN ('ACTIVE','SUPERSEDED') AND activated_by IS NOT NULL AND activated_at IS NOT NULL)
    OR status NOT IN ('ACTIVE','SUPERSEDED')
  );
