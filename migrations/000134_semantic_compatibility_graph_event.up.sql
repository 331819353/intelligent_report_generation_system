BEGIN;

-- A verified dimension/metric compatibility is part of the executable
-- semantic graph. The original semantic outbox triggers covered dimensions,
-- members, metrics and datasets, but not this relationship table. As a
-- result, verifying a compatibility left the current graph stale until some
-- unrelated asset changed.
CREATE OR REPLACE FUNCTION
  platform.enqueue_dimension_metric_compatibility_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  changed_row jsonb;
  previous_row jsonb;
BEGIN
  changed_row := CASE
    WHEN TG_OP='DELETE' THEN to_jsonb(OLD)
    ELSE to_jsonb(NEW)
  END;
  previous_row := CASE
    WHEN TG_OP='INSERT' THEN '{}'::jsonb
    ELSE to_jsonb(OLD)
  END;

  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'DIMENSION',
    changed_row->>'dimension_id',
    'REBUILD'
  );
  PERFORM platform.enqueue_semantic_change(
    (changed_row->>'tenant_id')::uuid,
    'METRIC_VERSION',
    changed_row->>'metric_version_id',
    'REBUILD'
  );

  IF TG_OP='UPDATE' AND (
    previous_row->>'dimension_id' IS DISTINCT FROM
      changed_row->>'dimension_id'
    OR previous_row->>'metric_version_id' IS DISTINCT FROM
      changed_row->>'metric_version_id'
  ) THEN
    PERFORM platform.enqueue_semantic_change(
      (previous_row->>'tenant_id')::uuid,
      'DIMENSION',
      previous_row->>'dimension_id',
      'REBUILD'
    );
    PERFORM platform.enqueue_semantic_change(
      (previous_row->>'tenant_id')::uuid,
      'METRIC_VERSION',
      previous_row->>'metric_version_id',
      'REBUILD'
    );
  END IF;

  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.enqueue_dimension_metric_compatibility_change()
FROM PUBLIC;

DROP TRIGGER IF EXISTS
  dimension_metric_compatibility_enqueue_semantic_change
ON platform.dimension_metric_compatibility;

CREATE TRIGGER dimension_metric_compatibility_enqueue_semantic_change
AFTER INSERT OR UPDATE OR DELETE
ON platform.dimension_metric_compatibility
FOR EACH ROW EXECUTE FUNCTION
  platform.enqueue_dimension_metric_compatibility_change();

-- Rebuild the graph once for already verified relationships, including
-- relationships verified before this trigger existed.
SELECT platform.enqueue_semantic_change(
  compatibility.tenant_id,
  'DIMENSION',
  compatibility.dimension_id::text,
  'REBUILD'
)
FROM platform.dimension_metric_compatibility AS compatibility
WHERE compatibility.status='VERIFIED';

COMMIT;
