-- FORCE RLS intentionally hides report-asset outboxes unless a tenant context
-- has already been established. The worker needs this narrowly-scoped,
-- read-only bootstrap to discover which tenant context to establish next.
CREATE OR REPLACE FUNCTION askdata.list_report_asset_projection_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
SET row_security=off
AS $$
  SELECT work.tenant_id
  FROM (
    SELECT extraction.tenant_id
    FROM askdata.report_asset_extraction_outbox AS extraction
    WHERE extraction.state IN ('PENDING','FAILED','RUNNING')
      AND extraction.attempt<10
    UNION
    SELECT projection.tenant_id
    FROM askdata.report_asset_projection_outbox AS projection
    WHERE projection.state IN ('PENDING','FAILED','RUNNING')
      AND projection.attempt<10
  ) AS work
  ORDER BY work.tenant_id
$$;

REVOKE ALL ON FUNCTION askdata.list_report_asset_projection_tenants() FROM PUBLIC;
