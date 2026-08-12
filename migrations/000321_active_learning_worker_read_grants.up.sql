-- Active learning may mine only stable IDs, hashes and governed aggregates.
-- Keep raw dimension-member labels and request purposes behind narrow
-- SECURITY DEFINER projections rather than granting the worker table access.
BEGIN;
CREATE OR REPLACE FUNCTION askdata.active_learning_member_signals(selected_domain_id uuid)
RETURNS TABLE(
  id uuid,dimension_version_id uuid,member_key_hash text,
  created_at timestamptz,updated_at timestamptz
) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform AS $$
  SELECT member.id,member.dimension_version_id,member.member_key_hash,
    member.created_at,member.updated_at
  FROM askdata.dimension_members AS member
  WHERE member.tenant_id=askdata.current_tenant_id()
    AND member.domain_id=selected_domain_id
    AND member.status='CERTIFIED'
    AND askdata.system_access()
$$;

CREATE OR REPLACE FUNCTION askdata.active_learning_data_request_signals(selected_domain_id uuid)
RETURNS TABLE(
  id uuid,requester_user_id uuid,business_purpose text,
  parsed_context_json jsonb,created_at timestamptz,updated_at timestamptz
) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform AS $$
  SELECT request.id,request.requester_user_id,request.business_purpose,
    request.parsed_context_json,request.created_at,request.updated_at
  FROM platform.data_requests AS request
  WHERE request.tenant_id=askdata.current_tenant_id()
    AND request.domain_id=selected_domain_id
    AND askdata.system_access()
$$;

REVOKE ALL ON FUNCTION
  askdata.active_learning_member_signals(uuid),
  askdata.active_learning_data_request_signals(uuid)
FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION
      askdata.active_learning_member_signals(uuid),
      askdata.active_learning_data_request_signals(uuid)
    TO report_worker;
  END IF;
END
$$;
COMMIT;
