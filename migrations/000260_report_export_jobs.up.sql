CREATE TABLE platform.report_export_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  domain_id uuid NOT NULL,
  report_id uuid NOT NULL,
  report_version_id uuid NOT NULL,
  requested_by uuid NOT NULL,
  format text NOT NULL CHECK(format IN ('PDF','PNG','CSV','XLSX')),
  page_ids text[] NOT NULL DEFAULT '{}' CHECK(
    cardinality(page_ids)<=100 AND array_position(page_ids,NULL) IS NULL
  ),
  filter_summary_json jsonb NOT NULL DEFAULT '{}' CHECK(
    jsonb_typeof(filter_summary_json)='object' AND pg_column_size(filter_summary_json)<=65536
  ),
  as_of timestamptz NOT NULL,
  timezone text NOT NULL CHECK(length(btrim(timezone)) BETWEEN 1 AND 128),
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','READY','FAILED','EXPIRED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 5),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '',
  lease_token uuid,
  lease_expires_at timestamptz,
  object_uri text NOT NULL DEFAULT '',
  content_hash text NOT NULL DEFAULT '' CHECK(content_hash='' OR content_hash ~ '^[0-9a-f]{64}$'),
  artifact_bytes bigint NOT NULL DEFAULT 0 CHECK(artifact_bytes>=0 AND artifact_bytes<=1073741824),
  failure_code text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(id,tenant_id),
  FOREIGN KEY(report_id,tenant_id) REFERENCES platform.reports(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(domain_id,tenant_id) REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(report_version_id,report_id,tenant_id)
    REFERENCES platform.report_versions(id,report_id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY(requested_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CHECK(expires_at>created_at AND expires_at<=created_at+interval '7 days'),
  CHECK((state='RUNNING')=(lease_token IS NOT NULL AND lease_owner<>'' AND lease_expires_at IS NOT NULL)),
  CHECK((state='READY')=(object_uri<>'' AND content_hash<>'' AND artifact_bytes>0 AND completed_at IS NOT NULL)),
  CHECK(state='READY' OR (object_uri='' AND content_hash='' AND artifact_bytes=0)),
  CHECK(state NOT IN ('FAILED','EXPIRED') OR failure_code<>''),
  CHECK(state NOT IN ('READY','FAILED','EXPIRED') OR completed_at IS NOT NULL)
);

CREATE INDEX report_export_jobs_claim_idx ON platform.report_export_jobs(
  tenant_id,next_attempt_at,created_at,id
) WHERE state IN ('PENDING','RUNNING');
CREATE INDEX report_export_jobs_report_idx ON platform.report_export_jobs(
  tenant_id,report_id,created_at DESC
);

ALTER TABLE platform.report_export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.report_export_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY report_export_jobs_access ON platform.report_export_jobs
  USING(
    tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR requested_by=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW'])
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND (platform.is_system_access() OR requested_by=platform.current_user_id())
    AND platform.report_v2_can_access(report_id,ARRAY['VIEW'])
  );

CREATE OR REPLACE FUNCTION platform.list_report_export_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT DISTINCT job.tenant_id FROM platform.report_export_jobs job
  WHERE (job.state='PENDING' AND job.next_attempt_at<=now())
     OR (job.state='RUNNING' AND job.lease_expires_at<=now())
  ORDER BY job.tenant_id
$$;

REVOKE ALL ON FUNCTION platform.list_report_export_tenants() FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.guard_report_export_job()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF OLD.id<>NEW.id OR OLD.tenant_id<>NEW.tenant_id OR OLD.domain_id<>NEW.domain_id OR OLD.report_id<>NEW.report_id
    OR OLD.report_version_id<>NEW.report_version_id OR OLD.requested_by<>NEW.requested_by
    OR OLD.format<>NEW.format OR OLD.page_ids<>NEW.page_ids
    OR OLD.filter_summary_json<>NEW.filter_summary_json OR OLD.as_of<>NEW.as_of
    OR OLD.timezone<>NEW.timezone OR OLD.expires_at<>NEW.expires_at
    OR OLD.created_at<>NEW.created_at THEN
    RAISE EXCEPTION 'report export request is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state IN ('READY','EXPIRED') OR (OLD.state='FAILED' AND NEW.state<>'PENDING') THEN
    RAISE EXCEPTION 'report export terminal state is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state='PENDING' AND NEW.state NOT IN ('PENDING','RUNNING','FAILED','EXPIRED')
    OR OLD.state='RUNNING' AND NEW.state NOT IN ('RUNNING','PENDING','READY','FAILED','EXPIRED')
    OR OLD.state='FAILED' AND NEW.state<>'PENDING' THEN
    RAISE EXCEPTION 'invalid report export state transition' USING ERRCODE='23514';
  END IF;
  NEW.updated_at=now();
  RETURN NEW;
END
$$;
CREATE TRIGGER report_export_job_guard BEFORE UPDATE ON platform.report_export_jobs
FOR EACH ROW EXECUTE FUNCTION platform.guard_report_export_job();
CREATE TRIGGER report_export_job_no_delete BEFORE DELETE ON platform.report_export_jobs
FOR EACH ROW EXECUTE FUNCTION platform.reject_report_v2_immutable_mutation();
REVOKE ALL ON FUNCTION platform.guard_report_export_job() FROM PUBLIC;
