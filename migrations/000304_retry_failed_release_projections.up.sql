BEGIN;

CREATE OR REPLACE FUNCTION askdata.retry_failed_release_projections(
  selected_release_id uuid,
  selected_actor_id uuid
)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE retried_count integer;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id() THEN
    RAISE EXCEPTION 'RELEASE_PROJECTION_RETRY_SCOPE' USING ERRCODE='42501';
  END IF;
  SELECT * INTO selected_release
  FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id()
    AND domain_id=askdata.current_domain_id()
    AND id=selected_release_id
  FOR UPDATE;
  IF selected_release.id IS NULL
    OR selected_release.status<>'BLOCKED'
    OR askdata.evaluation_control_can_access(
      selected_release.tenant_id,selected_release.domain_id
    ) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'RELEASE_PROJECTION_RETRY_INVALID' USING ERRCODE='55000';
  END IF;

  UPDATE askdata.release_projections SET
    status='PENDING',attempt=0,next_attempt_at=now(),error_code='',detail='{}'::jsonb,
    applied_content_hash='',resource_version='',object_count=0,
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    started_at=NULL,completed_at=NULL,version=version+1
  WHERE tenant_id=selected_release.tenant_id
    AND domain_id=selected_release.domain_id
    AND release_id=selected_release.id
    AND status='FAILED';
  GET DIAGNOSTICS retried_count=ROW_COUNT;
  IF retried_count<1 THEN
    RAISE EXCEPTION 'RELEASE_PROJECTION_RETRY_EMPTY' USING ERRCODE='55000';
  END IF;

  UPDATE askdata.releases SET
    status='PROJECTING',updated_by=selected_actor_id,version=version+1
  WHERE id=selected_release.id;
  INSERT INTO askdata.release_events(
    tenant_id,domain_id,release_id,event_type,detail
  ) VALUES(
    selected_release.tenant_id,selected_release.domain_id,selected_release.id,
    'PROJECTION_RETRIED',jsonb_build_object('projectionCount',retried_count)
  );
  RETURN retried_count;
END
$$;

REVOKE ALL ON FUNCTION askdata.retry_failed_release_projections(uuid,uuid) FROM PUBLIC;

COMMIT;
