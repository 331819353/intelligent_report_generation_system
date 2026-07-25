CREATE OR REPLACE FUNCTION platform.enqueue_semantic_materialization_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.status='ACTIVE'
    OR (TG_OP='UPDATE' AND OLD.status='ACTIVE' AND NEW.status<>'ACTIVE') THEN
    PERFORM platform.enqueue_semantic_change(
      NEW.tenant_id,'DATASET_VERSION',NEW.dataset_version_id::text,'REBUILD'
    );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.enqueue_semantic_materialization_change()
FROM PUBLIC;

CREATE TRIGGER dataset_materializations_enqueue_semantic_graph
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION
  platform.enqueue_semantic_materialization_change();

SELECT platform.enqueue_semantic_change(
  materialization.tenant_id,'DATASET_VERSION',
  materialization.dataset_version_id::text,'REBUILD'
)
FROM platform.dataset_materializations AS materialization
WHERE materialization.status='ACTIVE';
