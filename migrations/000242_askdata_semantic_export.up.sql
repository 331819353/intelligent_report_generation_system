-- Symmetric governed semantic export. Small exports are generated inline;
-- large exports pin an immutable certified-version manifest and use this
-- durable leased job table to publish a content-addressed XLSX artifact.

-- Metric display metadata participates in import/export content and therefore
-- must be versioned. Legacy writers keep their stable-identity fallback via
-- the empty defaults; existing versions are backfilled before export.
ALTER TABLE askdata.metric_versions
  ADD COLUMN name text NOT NULL DEFAULT '' CHECK(
    length(name)<=256 AND name=btrim(name) AND name !~ '[[:cntrl:]]'
  ),
  ADD COLUMN description text NOT NULL DEFAULT '' CHECK(
    length(description)<=4096 AND description !~ '[[:cntrl:]]'
  );

UPDATE askdata.metric_versions AS version SET
  name=metric.name,
  description=metric.description
FROM askdata.metrics AS metric
WHERE metric.id=version.metric_id;

CREATE OR REPLACE FUNCTION askdata.semantic_export_asset_types_valid(selected_values text[])
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT cardinality(selected_values) BETWEEN 1 AND 12
    AND cardinality(selected_values)=cardinality(ARRAY(
      SELECT DISTINCT value FROM unnest(selected_values) AS item(value)
    ))
    AND NOT EXISTS(
      SELECT 1 FROM unnest(selected_values) AS item(value)
      WHERE item.value NOT IN (
        'MODEL','MEASURE','METRIC','METRIC_DIMENSION','DIMENSION','MEMBER',
        'HIERARCHY','RELATIONSHIP','TERM','CERTIFIED_EXAMPLE','KPI_BUNDLE','EVAL_CASE'
      )
    )
$$;

CREATE TABLE askdata.semantic_export_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid,
  asset_types text[] NOT NULL CHECK(askdata.semantic_export_asset_types_valid(asset_types)),
  format text NOT NULL CHECK(format='xlsx'),
  manifest_json jsonb NOT NULL CHECK(
    jsonb_typeof(manifest_json)='object'
    AND pg_column_size(manifest_json)<=16777216
    AND askdata.json_is_safe(manifest_json)
  ),
  state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','READY','FAILED')),
  source_row_count integer NOT NULL CHECK(source_row_count BETWEEN 0 AND 100000),
  row_count integer NOT NULL DEFAULT 0 CHECK(row_count BETWEEN 0 AND 100000),
  omitted_sensitive_members integer NOT NULL DEFAULT 0 CHECK(omitted_sensitive_members>=0),
  object_uri text CHECK(
    object_uri IS NULL OR (
      length(object_uri) BETWEEN 1 AND 2048
      AND object_uri=btrim(object_uri)
      AND object_uri !~ '[[:cntrl:]]'
    )
  ),
  content_hash text CHECK(content_hash IS NULL OR content_hash ~ '^[0-9a-f]{64}$'),
  failure_code text CHECK(failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
  created_by uuid NOT NULL,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 5),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  started_at timestamptz,
  completed_at timestamptz,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_semantic_export_jobs_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_export_jobs_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES askdata.releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_export_jobs_actor_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_export_jobs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_semantic_export_jobs_expiry_check CHECK(expires_at>created_at),
  CONSTRAINT askdata_semantic_export_jobs_result_count_check CHECK(
    row_count+omitted_sensitive_members<=source_row_count
  ),
  CONSTRAINT askdata_semantic_export_jobs_state_shape_check CHECK(
    (
      state='PENDING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND completed_at IS NULL
      AND object_uri IS NULL AND content_hash IS NULL
      AND row_count=0 AND omitted_sensitive_members=0
    ) OR (
      state='RUNNING' AND attempt>0 AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND started_at IS NOT NULL
      AND completed_at IS NULL AND object_uri IS NULL AND content_hash IS NULL
      AND failure_code IS NULL AND row_count=0 AND omitted_sensitive_members=0
    ) OR (
      state='READY' AND attempt>0 AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND object_uri IS NOT NULL AND content_hash IS NOT NULL AND failure_code IS NULL
      AND row_count+omitted_sensitive_members=source_row_count
    ) OR (
      state='FAILED' AND attempt>0 AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL
      AND object_uri IS NULL AND content_hash IS NULL AND failure_code IS NOT NULL
      AND row_count=0 AND omitted_sensitive_members=0
    )
  )
);

CREATE INDEX askdata_semantic_export_jobs_claim_idx
  ON askdata.semantic_export_jobs(state,lease_expires_at,tenant_id,created_at,id)
  WHERE state IN ('PENDING','RUNNING');
CREATE INDEX askdata_semantic_export_jobs_domain_idx
  ON askdata.semantic_export_jobs(tenant_id,domain_id,created_at DESC,id DESC);

CREATE TRIGGER askdata_semantic_export_jobs_set_updated_at
BEFORE UPDATE ON askdata.semantic_export_jobs
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

CREATE OR REPLACE FUNCTION askdata.enforce_semantic_export_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.domain_id,NEW.release_id,NEW.asset_types,
    NEW.format,NEW.manifest_json,NEW.source_row_count,NEW.created_by,
    NEW.expires_at,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.domain_id,OLD.release_id,OLD.asset_types,
    OLD.format,OLD.manifest_json,OLD.source_row_count,OLD.created_by,
    OLD.expires_at,OLD.created_at
  ) THEN
    RAISE EXCEPTION 'semantic export identity and pinned manifest are immutable'
      USING ERRCODE='55000';
  END IF;
  IF NOT (
    (OLD.state='PENDING' AND NEW.state='RUNNING')
    OR (OLD.state='RUNNING' AND NEW.state IN ('RUNNING','PENDING','READY','FAILED'))
  ) THEN
    RAISE EXCEPTION 'illegal semantic export transition % -> %',OLD.state,NEW.state
      USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_semantic_export_jobs_transition_guard
BEFORE UPDATE ON askdata.semantic_export_jobs
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_semantic_export_transition();

ALTER TABLE askdata.semantic_export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.semantic_export_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_semantic_export_jobs_domain_isolation
  ON askdata.semantic_export_jobs
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

CREATE OR REPLACE FUNCTION askdata.list_semantic_export_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
STABLE
SET search_path=pg_catalog,askdata,platform
AS $$
  SELECT DISTINCT job.tenant_id
  FROM askdata.semantic_export_jobs AS job
  JOIN platform.tenants AS tenant ON tenant.id=job.tenant_id
  WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    AND job.expires_at>now() AND job.attempt<5
    AND (
      job.state='PENDING'
      OR (job.state='RUNNING' AND job.lease_expires_at<=now())
    )
  ORDER BY job.tenant_id
$$;

CREATE OR REPLACE FUNCTION askdata.claim_semantic_export(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  job_id uuid,
  tenant_id uuid,
  domain_id uuid,
  release_id uuid,
  asset_types text[],
  format text,
  manifest_json jsonb,
  state text,
  source_row_count integer,
  row_count integer,
  omitted_sensitive_members integer,
  object_uri text,
  content_hash text,
  failure_code text,
  created_by uuid,
  attempt integer,
  lease_owner text,
  lease_token uuid,
  lease_expires_at timestamptz,
  started_at timestamptz,
  completed_at timestamptz,
  expires_at timestamptz,
  created_at timestamptz,
  updated_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 1800 THEN
    RAISE EXCEPTION 'invalid semantic export lease parameters' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH candidate AS (
    SELECT job.id
    FROM askdata.semantic_export_jobs AS job
    WHERE job.tenant_id=selected_tenant_id
      AND job.expires_at>now() AND job.attempt<5
      AND (
        job.state='PENDING'
        OR (job.state='RUNNING' AND job.lease_expires_at<=now())
      )
    ORDER BY job.created_at,job.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
  ), claimed AS (
    UPDATE askdata.semantic_export_jobs AS job SET
      state='RUNNING',attempt=job.attempt+1,
      lease_owner=btrim(selected_worker_id),lease_token=gen_random_uuid(),
      lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
      started_at=COALESCE(job.started_at,now()),failure_code=NULL
    FROM candidate
    WHERE job.id=candidate.id
    RETURNING job.*
  )
  SELECT
    claimed.id,claimed.tenant_id,claimed.domain_id,claimed.release_id,
    claimed.asset_types,claimed.format,claimed.manifest_json,claimed.state,
    claimed.source_row_count,claimed.row_count,claimed.omitted_sensitive_members,
    claimed.object_uri,claimed.content_hash,claimed.failure_code,claimed.created_by,
    claimed.attempt,claimed.lease_owner,claimed.lease_token,claimed.lease_expires_at,
    claimed.started_at,claimed.completed_at,claimed.expires_at,
    claimed.created_at,claimed.updated_at
  FROM claimed;
END
$$;

CREATE OR REPLACE FUNCTION askdata.complete_semantic_export(
  selected_tenant_id uuid,
  selected_job_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_object_uri text,
  selected_content_hash text,
  selected_row_count integer,
  selected_omitted_sensitive_members integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE changed integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_job_id IS NULL
    OR selected_lease_token IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR length(btrim(selected_object_uri)) NOT BETWEEN 1 AND 2048
    OR selected_object_uri ~ '[[:cntrl:]]'
    OR selected_content_hash !~ '^[0-9a-f]{64}$'
    OR selected_row_count NOT BETWEEN 0 AND 100000
    OR selected_omitted_sensitive_members<0 THEN
    RAISE EXCEPTION 'invalid semantic export completion parameters' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.semantic_export_jobs SET
    state='READY',row_count=selected_row_count,
    omitted_sensitive_members=selected_omitted_sensitive_members,
    object_uri=btrim(selected_object_uri),content_hash=selected_content_hash,
    failure_code=NULL,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now()
  WHERE tenant_id=selected_tenant_id AND id=selected_job_id
    AND state='RUNNING' AND lease_owner=btrim(selected_worker_id)
    AND lease_token=selected_lease_token AND lease_expires_at>now()
    AND expires_at>now()
    AND source_row_count=selected_row_count+selected_omitted_sensitive_members;
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

CREATE OR REPLACE FUNCTION askdata.fail_semantic_export(
  selected_tenant_id uuid,
  selected_job_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_failure_code text,
  selected_retryable boolean
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE changed integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_job_id IS NULL
    OR selected_lease_token IS NULL OR selected_retryable IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_failure_code !~ '^[A-Z][A-Z0-9_]{0,127}$' THEN
    RAISE EXCEPTION 'invalid semantic export failure parameters' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.semantic_export_jobs SET
    state=CASE WHEN selected_retryable AND attempt<5 AND expires_at>now()
      THEN 'PENDING' ELSE 'FAILED' END,
    failure_code=selected_failure_code,
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=CASE WHEN selected_retryable AND attempt<5 AND expires_at>now()
      THEN NULL ELSE now() END
  WHERE tenant_id=selected_tenant_id AND id=selected_job_id
    AND state='RUNNING' AND lease_owner=btrim(selected_worker_id)
    AND lease_token=selected_lease_token;
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.semantic_export_asset_types_valid(text[]),
  askdata.enforce_semantic_export_transition(),
  askdata.list_semantic_export_tenants(),
  askdata.claim_semantic_export(uuid,text,integer),
  askdata.complete_semantic_export(uuid,uuid,text,uuid,text,text,integer,integer),
  askdata.fail_semantic_export(uuid,uuid,text,uuid,text,boolean)
FROM PUBLIC;

COMMENT ON TABLE askdata.semantic_export_jobs IS
  'Durable content-addressed governed semantic exports with request-time pinned certified versions';
COMMENT ON COLUMN askdata.semantic_export_jobs.manifest_json IS
  'Exact asset-type to certified-version UUID manifest frozen when the asynchronous export is requested';
COMMENT ON COLUMN askdata.metric_versions.name IS
  'Version-pinned metric display name used by symmetric semantic import/export';
COMMENT ON COLUMN askdata.metric_versions.description IS
  'Version-pinned metric description used by historical Release export';
