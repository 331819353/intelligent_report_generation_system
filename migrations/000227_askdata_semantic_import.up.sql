-- Durable, leased semantic-asset import batches. Parsing and validation run
-- outside API transactions; imported objects are created only as DRAFT by a
-- later explicit commit command.

CREATE OR REPLACE FUNCTION askdata.semantic_import_errors_valid(errors jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT jsonb_typeof(errors)='array'
    AND jsonb_array_length(errors)<=128
    AND NOT EXISTS(
      SELECT 1
      FROM jsonb_array_elements(errors) AS item(value)
      WHERE jsonb_typeof(item.value) IS DISTINCT FROM 'object'
        OR jsonb_typeof(item.value->'column') IS DISTINCT FROM 'string'
        OR jsonb_typeof(item.value->'code') IS DISTINCT FROM 'string'
        OR jsonb_typeof(item.value->'message') IS DISTINCT FROM 'string'
        OR jsonb_typeof(item.value->'expected') IS DISTINCT FROM 'string'
        OR item.value->>'code' !~ '^[A-Z][A-Z0-9_]{0,127}$'
        OR length(item.value->>'column') NOT BETWEEN 1 AND 128
        OR length(item.value->>'message') NOT BETWEEN 1 AND 2048
        OR length(item.value->>'expected') NOT BETWEEN 1 AND 1024
        OR item.value->>'column' ~ '[[:cntrl:]]'
        OR item.value->>'message' ~ '[[:cntrl:]]'
        OR item.value->>'expected' ~ '[[:cntrl:]]'
        OR (
          item.value ? 'actual'
          AND (
            jsonb_typeof(item.value->'actual') IS DISTINCT FROM 'string'
            OR length(item.value->>'actual')>1024
            OR item.value->>'actual' ~ '[[:cntrl:]]'
          )
        )
        OR EXISTS(
          SELECT 1 FROM jsonb_object_keys(item.value) AS key(name)
          WHERE key.name NOT IN ('column','code','message','expected','actual')
        )
    )
$$;

CREATE TABLE askdata.semantic_imports(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  asset_type text NOT NULL CHECK(asset_type IN (
    'MODEL','MEASURE','METRIC','METRIC_DIMENSION','DIMENSION','MEMBER',
    'HIERARCHY','RELATIONSHIP','TERM','CERTIFIED_EXAMPLE','KPI_BUNDLE','EVAL_CASE'
  )),
  file_object_uri text NOT NULL CHECK(
    length(file_object_uri) BETWEEN 1 AND 2048
    AND file_object_uri=btrim(file_object_uri)
    AND file_object_uri !~ '[[:cntrl:]]'
  ),
  file_hash text NOT NULL CHECK(file_hash ~ '^[0-9a-f]{64}$'),
  file_name text NOT NULL CHECK(
    length(file_name) BETWEEN 1 AND 255
    AND file_name=btrim(file_name)
    AND file_name !~ '[[:cntrl:]]'
  ),
  state text NOT NULL DEFAULT 'UPLOADED' CHECK(state IN (
    'UPLOADED','VALIDATING','VALIDATED','PARTIALLY_COMMITTED',
    'COMMITTED','WITHDRAWN','FAILED'
  )),
  total_rows integer NOT NULL DEFAULT 0 CHECK(total_rows>=0),
  valid_rows integer NOT NULL DEFAULT 0 CHECK(valid_rows>=0),
  invalid_rows integer NOT NULL DEFAULT 0 CHECK(invalid_rows>=0),
  failure_reason text CHECK(
    failure_reason IS NULL OR (
      length(failure_reason) BETWEEN 1 AND 1024
      AND failure_reason=btrim(failure_reason)
      AND failure_reason !~ '[[:cntrl:]]'
    )
  ),
  created_by uuid NOT NULL,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  started_at timestamptz,
  validation_completed_at timestamptz,
  committed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_semantic_imports_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_imports_actor_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_semantic_imports_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_semantic_imports_file_idempotency_key
    UNIQUE(tenant_id,file_hash,asset_type),
  CONSTRAINT askdata_semantic_imports_count_check CHECK(
    valid_rows+invalid_rows<=total_rows
  ),
  CONSTRAINT askdata_semantic_imports_state_shape_check CHECK(
    (
      state='UPLOADED' AND attempt=0 AND total_rows=0
      AND valid_rows=0 AND invalid_rows=0 AND failure_reason IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND started_at IS NULL AND validation_completed_at IS NULL
      AND committed_at IS NULL
    ) OR (
      state='VALIDATING' AND attempt>0 AND started_at IS NOT NULL
      AND validation_completed_at IS NULL AND failure_reason IS NULL
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
      AND committed_at IS NULL
    ) OR (
      state='VALIDATED' AND attempt>0 AND started_at IS NOT NULL
      AND validation_completed_at IS NOT NULL AND failure_reason IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND committed_at IS NULL
    ) OR (
      state IN ('PARTIALLY_COMMITTED','COMMITTED')
      AND attempt>0 AND validation_completed_at IS NOT NULL
      AND failure_reason IS NULL AND committed_at IS NOT NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
    ) OR (
      state='WITHDRAWN' AND attempt>0 AND validation_completed_at IS NOT NULL
      AND failure_reason IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
    ) OR (
      state='FAILED' AND attempt>0 AND started_at IS NOT NULL
      AND validation_completed_at IS NOT NULL AND failure_reason IS NOT NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND committed_at IS NULL
    )
  )
);

CREATE TABLE askdata.semantic_import_rows(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  import_id uuid NOT NULL,
  row_no integer NOT NULL CHECK(row_no>0),
  raw_json jsonb NOT NULL CHECK(
    jsonb_typeof(raw_json)='object'
    AND pg_column_size(raw_json)<=1048576
    AND askdata.json_is_safe(raw_json)
  ),
  normalized_json jsonb CHECK(
    normalized_json IS NULL OR (
      jsonb_typeof(normalized_json)='object'
      AND pg_column_size(normalized_json)<=1048576
      AND askdata.json_is_safe(normalized_json)
    )
  ),
  validation_state text NOT NULL CHECK(
    validation_state IN ('VALID','INVALID','SKIPPED','COMMITTED')
  ),
  errors_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(
    pg_column_size(errors_json)<=65536
    AND askdata.json_is_safe(errors_json)
    AND askdata.semantic_import_errors_valid(errors_json)
  ),
  created_object_id uuid,
  created_version_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_semantic_import_rows_import_fk
    FOREIGN KEY(import_id,tenant_id)
    REFERENCES askdata.semantic_imports(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_semantic_import_rows_number_key UNIQUE(import_id,row_no),
  CONSTRAINT askdata_semantic_import_rows_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_semantic_import_rows_state_shape_check CHECK(
    (
      validation_state='VALID' AND normalized_json IS NOT NULL
      AND created_object_id IS NULL AND created_version_id IS NULL
    ) OR (
      validation_state IN ('INVALID','SKIPPED')
      AND jsonb_array_length(errors_json)>0
      AND created_object_id IS NULL AND created_version_id IS NULL
    ) OR (
      validation_state='COMMITTED' AND normalized_json IS NOT NULL
      AND created_object_id IS NOT NULL AND created_version_id IS NOT NULL
    )
  )
);

CREATE INDEX askdata_semantic_imports_claim_idx
  ON askdata.semantic_imports(state,lease_expires_at,tenant_id,created_at,id)
  WHERE state IN ('UPLOADED','VALIDATING');
CREATE INDEX askdata_semantic_import_rows_state_idx
  ON askdata.semantic_import_rows(import_id,validation_state,row_no);

CREATE TRIGGER askdata_semantic_imports_set_updated_at
BEFORE UPDATE ON askdata.semantic_imports
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_semantic_import_rows_set_updated_at
BEFORE UPDATE ON askdata.semantic_import_rows
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

CREATE OR REPLACE FUNCTION askdata.enforce_semantic_import_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE row_total integer;
DECLARE row_valid integer;
DECLARE row_invalid integer;
DECLARE row_skipped integer;
DECLARE row_committed integer;
BEGIN
  IF (NEW.id,NEW.tenant_id,NEW.domain_id,NEW.asset_type,NEW.file_object_uri,
      NEW.file_hash,NEW.file_name,NEW.created_by,NEW.created_at)
    IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.domain_id,OLD.asset_type,OLD.file_object_uri,
      OLD.file_hash,OLD.file_name,OLD.created_by,OLD.created_at) THEN
    RAISE EXCEPTION 'semantic import identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NOT (
    (OLD.state='UPLOADED' AND NEW.state='VALIDATING')
    OR (OLD.state='VALIDATING' AND NEW.state IN ('VALIDATING','VALIDATED','FAILED'))
    OR (OLD.state='VALIDATED' AND NEW.state IN ('PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN'))
    OR (OLD.state='PARTIALLY_COMMITTED' AND NEW.state IN ('PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN'))
  ) THEN
    RAISE EXCEPTION 'illegal semantic import transition % -> %',OLD.state,NEW.state
      USING ERRCODE='55000';
  END IF;
  IF NEW.state IN ('VALIDATED','PARTIALLY_COMMITTED','COMMITTED','WITHDRAWN') THEN
    SELECT count(*)::integer,
      count(*) FILTER(WHERE validation_state IN ('VALID','COMMITTED'))::integer,
      count(*) FILTER(WHERE validation_state='INVALID')::integer,
      count(*) FILTER(WHERE validation_state='SKIPPED')::integer,
      count(*) FILTER(WHERE validation_state='COMMITTED')::integer
    INTO row_total,row_valid,row_invalid,row_skipped,row_committed
    FROM askdata.semantic_import_rows
    WHERE import_id=NEW.id AND tenant_id=NEW.tenant_id;
    IF NEW.total_rows<>row_total OR NEW.valid_rows<>row_valid
      OR NEW.invalid_rows<>row_invalid THEN
      RAISE EXCEPTION 'semantic import counters do not match row facts'
        USING ERRCODE='23514';
    END IF;
    IF NEW.state='PARTIALLY_COMMITTED'
      AND (row_committed=0 OR row_committed=row_valid AND row_invalid=0) THEN
      RAISE EXCEPTION 'partial semantic import commit shape is invalid'
        USING ERRCODE='23514';
    END IF;
    IF NEW.state='COMMITTED'
      AND (row_committed<>row_valid OR row_invalid<>0) THEN
      RAISE EXCEPTION 'complete semantic import contains uncommitted valid or invalid rows'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_semantic_import_row_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE import_state text;
BEGIN
  IF TG_OP='INSERT' THEN
    SELECT state INTO import_state
    FROM askdata.semantic_imports
    WHERE id=NEW.import_id AND tenant_id=NEW.tenant_id;
    IF import_state<>'VALIDATING' THEN
      RAISE EXCEPTION 'semantic import rows require an active validation batch'
        USING ERRCODE='55000';
    END IF;
    RETURN NEW;
  END IF;
  IF (NEW.id,NEW.tenant_id,NEW.import_id,NEW.row_no,NEW.raw_json,
      NEW.normalized_json,NEW.errors_json,NEW.created_at)
    IS DISTINCT FROM
     (OLD.id,OLD.tenant_id,OLD.import_id,OLD.row_no,OLD.raw_json,
      OLD.normalized_json,OLD.errors_json,OLD.created_at)
    OR OLD.validation_state<>'VALID' OR NEW.validation_state<>'COMMITTED'
    OR OLD.created_object_id IS NOT NULL OR OLD.created_version_id IS NOT NULL THEN
    RAISE EXCEPTION 'illegal semantic import row mutation' USING ERRCODE='55000';
  END IF;
  SELECT state INTO import_state
  FROM askdata.semantic_imports
  WHERE id=NEW.import_id AND tenant_id=NEW.tenant_id;
  IF import_state NOT IN ('VALIDATED','PARTIALLY_COMMITTED') THEN
    RAISE EXCEPTION 'semantic import row commit requires a validated batch'
      USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER askdata_semantic_imports_transition_guard
BEFORE UPDATE ON askdata.semantic_imports
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_semantic_import_transition();
CREATE TRIGGER askdata_semantic_import_rows_transition_guard
BEFORE INSERT OR UPDATE ON askdata.semantic_import_rows
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_semantic_import_row_transition();

ALTER TABLE askdata.semantic_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.semantic_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_semantic_imports_domain_isolation
  ON askdata.semantic_imports
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

ALTER TABLE askdata.semantic_import_rows ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.semantic_import_rows FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_semantic_import_rows_domain_isolation
  ON askdata.semantic_import_rows
  USING(
    askdata.tenant_matches(tenant_id)
    AND EXISTS(
      SELECT 1 FROM askdata.semantic_imports AS batch
      WHERE batch.id=import_id AND batch.tenant_id=semantic_import_rows.tenant_id
        AND askdata.domain_can_access(batch.domain_id)
    )
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND EXISTS(
      SELECT 1 FROM askdata.semantic_imports AS batch
      WHERE batch.id=import_id AND batch.tenant_id=semantic_import_rows.tenant_id
        AND askdata.domain_can_access(batch.domain_id)
    )
  );

CREATE OR REPLACE FUNCTION askdata.list_semantic_import_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
STABLE
SET search_path=pg_catalog,askdata,platform
AS $$
  SELECT DISTINCT batch.tenant_id
  FROM askdata.semantic_imports AS batch
  JOIN platform.tenants AS tenant ON tenant.id=batch.tenant_id
  WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    AND (
      batch.state='UPLOADED'
      OR (batch.state='VALIDATING' AND batch.lease_expires_at<=now())
    )
  ORDER BY batch.tenant_id
$$;

CREATE OR REPLACE FUNCTION askdata.claim_semantic_import(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  import_id uuid,
  domain_id uuid,
  asset_type text,
  file_object_uri text,
  file_hash text,
  file_name text,
  lease_token uuid,
  attempt integer,
  resume_after_row integer
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid semantic import lease parameters' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH candidate AS (
    SELECT batch.id
    FROM askdata.semantic_imports AS batch
    WHERE batch.tenant_id=selected_tenant_id
      AND (
        batch.state='UPLOADED'
        OR (batch.state='VALIDATING' AND batch.lease_expires_at<=now())
      )
    ORDER BY batch.created_at,batch.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
  ), claimed AS (
    UPDATE askdata.semantic_imports AS batch SET
      state='VALIDATING',attempt=batch.attempt+1,
      lease_owner=btrim(selected_worker_id),lease_token=gen_random_uuid(),
      lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
      started_at=COALESCE(batch.started_at,now()),failure_reason=NULL
    FROM candidate
    WHERE batch.id=candidate.id
    RETURNING batch.*
  )
  SELECT claimed.id,claimed.domain_id,claimed.asset_type,
    claimed.file_object_uri,claimed.file_hash,claimed.file_name,
    claimed.lease_token,claimed.attempt,
    COALESCE((
      SELECT max(row_no) FROM askdata.semantic_import_rows AS row
      WHERE row.import_id=claimed.id AND row.tenant_id=claimed.tenant_id
    ),0)::integer
  FROM claimed;
END
$$;

CREATE OR REPLACE FUNCTION askdata.heartbeat_semantic_import(
  selected_tenant_id uuid,
  selected_import_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_lease_seconds integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE changed integer;
BEGIN
  IF selected_tenant_id IS NULL OR selected_import_id IS NULL
    OR selected_lease_token IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid semantic import heartbeat parameters' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.semantic_imports SET
    lease_expires_at=now()+(selected_lease_seconds*interval '1 second')
  WHERE tenant_id=selected_tenant_id AND id=selected_import_id
    AND state='VALIDATING' AND lease_owner=btrim(selected_worker_id)
    AND lease_token=selected_lease_token AND lease_expires_at>now();
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.semantic_import_errors_valid(jsonb),
  askdata.enforce_semantic_import_transition(),
  askdata.enforce_semantic_import_row_transition(),
  askdata.list_semantic_import_tenants(),
  askdata.claim_semantic_import(uuid,text,integer),
  askdata.heartbeat_semantic_import(uuid,uuid,text,uuid,integer)
FROM PUBLIC;

COMMENT ON TABLE askdata.semantic_imports IS
  'Durable idempotent semantic-asset upload and validation batches; never an implicit certification path';
COMMENT ON TABLE askdata.semantic_import_rows IS
  'Row-level import validation facts and DRAFT creation provenance';
