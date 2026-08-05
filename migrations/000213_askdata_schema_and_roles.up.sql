-- Intelligent data-questioning uses an isolated control-plane schema. The
-- historical platform.semantic_* runtime was deliberately removed in 000195;
-- this migration does not recreate those retired tables.

-- AskData cognition participates in the common tenant AI policy, quota and
-- immutable request audit. Merely applying this migration does not authorize
-- any tenant: SEMANTIC_QUESTION must be explicitly added by a trusted tenant
-- administration workflow.
ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT ai_tenant_policies_purposes_check;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 6
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION','DATASET_DAG_GENERATION',
      'DATASET_TAG_SUGGESTION','DATASET_SEMANTIC_NAMING',
      'DATA_SOURCE_CONFIGURATION','SEMANTIC_QUESTION'
    ]::text[]
  );

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

-- Preserve every historical purpose while allowing the new immutable audit
-- value. No prompt, reasoning content or response body is stored here.
ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION','REPORT_GENERATION','BLOCK_EDIT',
    'CONCLUSION_GENERATION','DATASET_DAG_GENERATION','METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION','SEMANTIC_QUERY_PLANNING',
    'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION',
    'SEMANTIC_QUESTION'
  ));

COMMENT ON COLUMN platform.ai_tenant_policies.allowed_purposes IS
  '租户显式授权的 AI 用途；SEMANTIC_QUESTION 仅允许受限认知动作和工具请求，不能生成或执行任意 SQL';

CREATE SCHEMA askdata;
REVOKE ALL ON SCHEMA askdata FROM PUBLIC;
COMMENT ON SCHEMA askdata IS
  'Versioned intelligent data-questioning registry, evidence and release control plane';

CREATE OR REPLACE FUNCTION askdata.current_tenant_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT platform.current_tenant_id()
$$;

CREATE OR REPLACE FUNCTION askdata.current_actor_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT platform.current_user_id()
$$;

CREATE OR REPLACE FUNCTION askdata.current_domain_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT platform.current_domain_id()
$$;

CREATE OR REPLACE FUNCTION askdata.system_access()
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT platform.is_system_access()
$$;

CREATE OR REPLACE FUNCTION askdata.tenant_matches(selected_tenant_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT selected_tenant_id IS NOT NULL
    AND askdata.current_tenant_id() IS NOT NULL
    AND selected_tenant_id=askdata.current_tenant_id()
$$;

CREATE OR REPLACE FUNCTION askdata.domain_can_access(selected_domain_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
  SELECT selected_domain_id IS NOT NULL
    AND askdata.current_tenant_id() IS NOT NULL
    AND (
      askdata.system_access()
      OR (
        selected_domain_id=askdata.current_domain_id()
        AND askdata.current_actor_id() IS NOT NULL
        AND platform.user_has_active_domain_membership(selected_domain_id)
      )
    )
$$;

CREATE OR REPLACE FUNCTION askdata.json_is_safe(document jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT platform.materialization_json_is_safe(document)
$$;

CREATE OR REPLACE FUNCTION askdata.set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.updated_at=now();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'askdata append-only fact cannot be modified or deleted'
    USING ERRCODE='55000';
END
$$;

REVOKE ALL ON FUNCTION
  askdata.current_tenant_id(),
  askdata.current_actor_id(),
  askdata.current_domain_id(),
  askdata.system_access(),
  askdata.tenant_matches(uuid),
  askdata.domain_can_access(uuid),
  askdata.json_is_safe(jsonb),
  askdata.set_updated_at(),
  askdata.reject_immutable_mutation()
FROM PUBLIC;

CREATE TABLE askdata.audit_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid,
  actor_id uuid,
  event_type text NOT NULL CHECK(
    length(event_type) BETWEEN 1 AND 64
    AND event_type ~ '^[A-Z][A-Z0-9_]{0,63}$'
  ),
  resource_type text NOT NULL CHECK(
    length(resource_type) BETWEEN 1 AND 64
    AND resource_type ~ '^[A-Z][A-Z0-9_]{0,63}$'
  ),
  resource_id text NOT NULL CHECK(
    length(resource_id) BETWEEN 1 AND 128
    AND resource_id=btrim(resource_id)
    AND resource_id !~ '[[:cntrl:]]'
  ),
  request_id uuid,
  action_hash text NOT NULL CHECK(action_hash ~ '^[0-9a-f]{64}$'),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object'
    AND pg_column_size(detail)<=65536
    AND askdata.json_is_safe(detail)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_audit_events_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_audit_events_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_audit_events_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_audit_events_resource_idx
  ON askdata.audit_events(tenant_id,resource_type,resource_id,created_at DESC,id);
CREATE INDEX askdata_audit_events_request_idx
  ON askdata.audit_events(tenant_id,request_id) WHERE request_id IS NOT NULL;

CREATE TRIGGER askdata_audit_events_immutable
BEFORE UPDATE OR DELETE ON askdata.audit_events
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.audit_events FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_audit_events_domain_isolation
  ON askdata.audit_events
  USING(
    askdata.tenant_matches(tenant_id)
    AND (
      (domain_id IS NOT NULL AND askdata.domain_can_access(domain_id))
      OR (domain_id IS NULL AND askdata.system_access())
    )
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND (
      (domain_id IS NOT NULL AND askdata.domain_can_access(domain_id))
      OR (domain_id IS NULL AND askdata.system_access())
    )
  );

COMMENT ON TABLE askdata.audit_events IS
  'Append-only sanitized decisions and lifecycle facts; no prompts, chain-of-thought, SQL parameters or result rows';

ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA askdata REVOKE ALL ON FUNCTIONS FROM PUBLIC;
