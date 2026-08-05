-- Atomic semantic release manifest and four derived projection watermarks.
-- PostgreSQL remains authoritative; NebulaGraph and search/runtime artifacts
-- are rebuildable and cannot independently become active.
CREATE TABLE askdata.releases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  semantic_version text NOT NULL CHECK(
    length(semantic_version) BETWEEN 3 AND 128
    AND semantic_version=btrim(semantic_version)
    AND semantic_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{2,127}$'
  ),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','VALIDATING','PROJECTING','READY','ACTIVE','BLOCKED','SUPERSEDED')),
  base_release_id uuid,
  object_count integer NOT NULL DEFAULT 0 CHECK(object_count BETWEEN 0 AND 10000),
  notes text NOT NULL DEFAULT '' CHECK(length(notes)<=4096 AND notes !~ '[[:cntrl:]]'),
  validation_summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(validation_summary)='object'
    AND pg_column_size(validation_summary)<=65536
    AND askdata.json_is_safe(validation_summary)
  ),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  activated_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  validated_at timestamptz,
  ready_at timestamptz,
  activated_at timestamptz,
  CONSTRAINT askdata_releases_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_releases_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_releases_version_key UNIQUE(tenant_id,domain_id,semantic_version),
  CONSTRAINT askdata_releases_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_releases_base_fk
    FOREIGN KEY(base_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_releases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_releases_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_releases_activated_by_fk
    FOREIGN KEY(activated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_releases_ready_shape_check CHECK(
    (status IN ('READY','ACTIVE','SUPERSEDED') AND ready_at IS NOT NULL)
    OR status NOT IN ('READY','ACTIVE','SUPERSEDED')
  ),
  CONSTRAINT askdata_releases_activation_shape_check CHECK(
    (status IN ('ACTIVE','SUPERSEDED') AND activated_by IS NOT NULL AND activated_at IS NOT NULL)
    OR status NOT IN ('ACTIVE','SUPERSEDED')
  )
);

CREATE UNIQUE INDEX askdata_releases_one_active_idx
  ON askdata.releases(tenant_id,domain_id) WHERE status='ACTIVE';
CREATE INDEX askdata_releases_lifecycle_idx
  ON askdata.releases(tenant_id,domain_id,status,created_at DESC,id);

CREATE TABLE askdata.release_objects(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  object_type text NOT NULL CHECK(object_type IN ('DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','DIMENSION','MEMBER','HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM','CERTIFIED_EXAMPLE')),
  object_id uuid NOT NULL,
  object_version_id uuid NOT NULL,
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  sensitivity text NOT NULL DEFAULT 'INTERNAL' CHECK(sensitivity IN ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED')),
  contract_json jsonb NOT NULL CHECK(
    jsonb_typeof(contract_json)='object'
    AND pg_column_size(contract_json)<=131072
    AND askdata.json_is_safe(contract_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_objects_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_objects_manifest_key UNIQUE(tenant_id,release_id,object_type,object_id,object_version_id),
  CONSTRAINT askdata_release_objects_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_objects_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE askdata.release_projections(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  target text NOT NULL CHECK(target IN ('POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER')),
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','RUNNING','READY','FAILED','STALE')),
  expected_content_hash text NOT NULL CHECK(expected_content_hash ~ '^[0-9a-f]{64}$'),
  applied_content_hash text NOT NULL DEFAULT '' CHECK(applied_content_hash='' OR applied_content_hash ~ '^[0-9a-f]{64}$'),
  resource_version text NOT NULL DEFAULT '' CHECK(length(resource_version)<=256),
  object_count integer NOT NULL DEFAULT 0 CHECK(object_count>=0),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object'
    AND pg_column_size(detail)<=65536
    AND askdata.json_is_safe(detail)
  ),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
  max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  started_at timestamptz,
  completed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_projections_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_projections_target_key UNIQUE(tenant_id,release_id,target),
  CONSTRAINT askdata_release_projections_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_projections_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT askdata_release_projections_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR
    (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT askdata_release_projections_ready_shape_check CHECK(
    (status='READY' AND applied_content_hash=expected_content_hash
      AND resource_version<>'' AND completed_at IS NOT NULL AND error_code='')
    OR status<>'READY'
  )
);

CREATE INDEX askdata_release_projections_claim_idx
  ON askdata.release_projections(status,next_attempt_at,lease_expires_at,tenant_id,domain_id,updated_at,id);

CREATE TABLE askdata.release_projection_artifacts(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  target text NOT NULL CHECK(target IN ('POSTGRES_REGISTRY','SEARCH_INDEX','EXECUTION_SEMANTIC_LAYER')),
  artifact_type text NOT NULL CHECK(length(artifact_type) BETWEEN 1 AND 64),
  artifact_id text NOT NULL CHECK(length(btrim(artifact_id)) BETWEEN 1 AND 256 AND artifact_id !~ '[[:cntrl:]]'),
  object_version_id uuid,
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  contract_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(contract_json)='object'
    AND pg_column_size(contract_json)<=131072
    AND askdata.json_is_safe(contract_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,release_id,target,artifact_type,artifact_id),
  CONSTRAINT askdata_release_projection_artifacts_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_projection_artifacts_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE askdata.release_state(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  active_release_id uuid,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,domain_id),
  CONSTRAINT askdata_release_state_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_state_active_release_fk
    FOREIGN KEY(active_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_state_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.release_events(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  event_type text NOT NULL CHECK(event_type IN ('CREATED','VALIDATING','PROJECTING','PROJECTION_READY','PROJECTION_FAILED','READY','ACTIVATED','SUPERSEDED','BLOCKED')),
  actor_id uuid,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(detail)='object'
    AND pg_column_size(detail)<=65536
    AND askdata.json_is_safe(detail)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_events_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_events_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_events_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_events_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_release_events_lookup_idx
  ON askdata.release_events(tenant_id,domain_id,release_id,created_at,id);

-- Bounded resolver output cache. It is keyed by the immutable release and
-- policy scope, so old runs can continue to resolve against their pinned
-- release after a newer release becomes active.
CREATE TABLE askdata.graph_plan_cache(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  question_shape_hash text NOT NULL CHECK(question_shape_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  graph_content_hash text NOT NULL CHECK(graph_content_hash ~ '^[0-9a-f]{64}$'),
  plan_hash text NOT NULL CHECK(plan_hash ~ '^[0-9a-f]{64}$'),
  plan_json jsonb NOT NULL CHECK(
    jsonb_typeof(plan_json)='object'
    AND pg_column_size(plan_json)<=131072
    AND askdata.json_is_safe(plan_json)
  ),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_graph_plan_cache_expiry_check CHECK(expires_at>created_at),
  CONSTRAINT askdata_graph_plan_cache_lookup_key UNIQUE(
    tenant_id,release_id,question_shape_hash,policy_scope_hash
  ),
  CONSTRAINT askdata_graph_plan_cache_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_graph_plan_cache_release_fk
    FOREIGN KEY(release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE CASCADE
);

CREATE INDEX askdata_graph_plan_cache_expiry_idx
  ON askdata.graph_plan_cache(tenant_id,domain_id,release_id,expires_at,id);

CREATE OR REPLACE FUNCTION askdata.validate_graph_plan_cache()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE source_valid boolean := false;
BEGIN
  SELECT EXISTS(
    SELECT 1
    FROM askdata.releases AS release
    JOIN askdata.release_projections AS projection
      ON projection.release_id=release.id
     AND projection.domain_id=release.domain_id
     AND projection.tenant_id=release.tenant_id
     AND projection.target='NEBULA_GRAPH'
    WHERE release.id=NEW.release_id AND release.domain_id=NEW.domain_id
      AND release.tenant_id=NEW.tenant_id
      AND release.status IN ('READY','ACTIVE','SUPERSEDED')
      AND release.content_hash=NEW.graph_content_hash
      AND projection.status='READY'
      AND projection.applied_content_hash=NEW.graph_content_hash
  ) INTO source_valid;
  IF NOT source_valid THEN
    RAISE EXCEPTION 'graph plan cache requires a matching READY NebulaGraph projection'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_release_object()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE release_domain_id uuid;
DECLARE source_valid boolean := false;
DECLARE source_object_id uuid;
DECLARE source_hash text;
BEGIN
  SELECT domain_id INTO release_domain_id
  FROM askdata.releases
  WHERE tenant_id=NEW.tenant_id AND id=NEW.release_id AND status='DRAFT';
  IF release_domain_id IS NULL OR release_domain_id<>NEW.domain_id THEN
    RAISE EXCEPTION 'release object requires a DRAFT release in the same domain'
      USING ERRCODE='23514';
  END IF;
  CASE NEW.object_type
    WHEN 'DOMAIN' THEN
      SELECT true,id,NULL INTO source_valid,source_object_id,source_hash
      FROM askdata.domains WHERE tenant_id=NEW.tenant_id AND id=NEW.object_version_id AND status='ACTIVE';
    WHEN 'ENTITY' THEN
      SELECT true,entity_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.entities WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'SEMANTIC_MODEL' THEN
      SELECT true,model.model_id,model.content_hash
      INTO source_valid,source_object_id,source_hash
      FROM askdata.semantic_models AS model
      JOIN platform.datasets AS dataset
        ON dataset.id=model.dataset_id AND dataset.tenant_id=model.tenant_id
      JOIN platform.dataset_versions AS version
        ON version.id=model.dataset_version_id
       AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
      JOIN platform.dataset_materializations AS materialization
        ON materialization.id=model.materialization_id
       AND materialization.dataset_id=dataset.id
       AND materialization.dataset_version_id=version.id
       AND materialization.tenant_id=dataset.tenant_id
      WHERE model.tenant_id=NEW.tenant_id AND model.domain_id=NEW.domain_id
        AND model.id=NEW.object_version_id AND model.status='CERTIFIED'
        AND dataset.domain_id=NEW.domain_id AND dataset.deleted_at IS NULL
        AND dataset.status='PUBLISHED'
        AND dataset.current_published_version_id=version.id
        AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
        AND version.layer=model.layer AND version.schema_hash=model.dataset_schema_hash
        AND materialization.status='ACTIVE' AND materialization.layer=version.layer
        AND materialization.schema_hash=version.schema_hash;
    WHEN 'MEASURE' THEN
      SELECT true,measure_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.measures WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'METRIC' THEN
      SELECT true,metric_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.metric_versions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'DIMENSION' THEN
      SELECT true,dimension_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimensions WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'MEMBER' THEN
      SELECT true,member_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.dimension_members WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'HIERARCHY' THEN
      SELECT true,hierarchy_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.hierarchies WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'RELATIONSHIP' THEN
      SELECT true,relationship_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.relationships WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'QUALITY_RULE' THEN
      SELECT true,quality_rule_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.quality_rules WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'BUSINESS_TERM' THEN
      SELECT true,term_id,content_hash INTO source_valid,source_object_id,source_hash
      FROM askdata.business_terms WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.object_version_id AND status='CERTIFIED';
    WHEN 'CERTIFIED_EXAMPLE' THEN
      source_valid := false;
  END CASE;
  IF NOT COALESCE(source_valid,false)
    OR source_object_id<>NEW.object_id
    OR (source_hash IS NOT NULL AND source_hash<>NEW.content_hash) THEN
    RAISE EXCEPTION 'release object does not match a certified immutable source'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.protect_release_manifest()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE release_status text;
BEGIN
  IF TG_OP='DELETE' THEN
    SELECT status INTO release_status FROM askdata.releases
    WHERE tenant_id=OLD.tenant_id AND id=OLD.release_id;
  ELSE
    SELECT status INTO release_status FROM askdata.releases
    WHERE tenant_id=NEW.tenant_id AND id=NEW.release_id;
  END IF;
  IF release_status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'sealed release manifest is immutable' USING ERRCODE='55000';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END
$$;

CREATE OR REPLACE FUNCTION askdata.release_manifest_hash(selected_release_id uuid)
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT encode(public.digest(COALESCE(string_agg(
    object_type||':'||object_id::text||':'||object_version_id::text||':'||content_hash,
    E'\n' ORDER BY object_type,object_id,object_version_id
  ),''),'sha256'),'hex')
  FROM askdata.release_objects
  WHERE tenant_id=askdata.current_tenant_id() AND release_id=selected_release_id
$$;

CREATE OR REPLACE FUNCTION askdata.initialize_release_state()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  INSERT INTO askdata.release_state(tenant_id,domain_id)
  VALUES(NEW.tenant_id,NEW.id)
  ON CONFLICT(tenant_id,domain_id) DO NOTHING;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.start_release_projection(
  selected_release_id uuid,
  selected_actor_id uuid,
  selected_validation_summary jsonb
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE manifest_hash text;
DECLARE manifest_count integer;
BEGIN
  IF selected_validation_summary IS NULL
    OR jsonb_typeof(selected_validation_summary)<>'object'
    OR pg_column_size(selected_validation_summary)>65536
    OR NOT askdata.json_is_safe(selected_validation_summary) THEN
    RAISE EXCEPTION 'invalid release validation summary' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_release FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id() AND id=selected_release_id
  FOR UPDATE;
  IF selected_release.id IS NULL OR selected_release.status<>'DRAFT'
    OR selected_release.domain_id<>askdata.current_domain_id()
    OR selected_actor_id<>askdata.current_actor_id() THEN
    RETURN false;
  END IF;
  UPDATE askdata.releases SET status='VALIDATING',updated_by=selected_actor_id,
    validation_summary=selected_validation_summary,validated_at=now(),version=version+1
  WHERE id=selected_release.id;
  SELECT askdata.release_manifest_hash(selected_release.id),count(*)
    INTO manifest_hash,manifest_count
  FROM askdata.release_objects
  WHERE tenant_id=selected_release.tenant_id AND release_id=selected_release.id;
  IF manifest_count<1 OR manifest_hash<>selected_release.content_hash THEN
    UPDATE askdata.releases SET status='BLOCKED',object_count=manifest_count,
      validation_summary=selected_validation_summary||jsonb_build_object('gate','MANIFEST_HASH_MISMATCH'),
      updated_by=selected_actor_id,version=version+1
    WHERE id=selected_release.id;
    INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,actor_id,detail)
    VALUES(selected_release.tenant_id,selected_release.domain_id,selected_release.id,'BLOCKED',selected_actor_id,jsonb_build_object('gate','MANIFEST_HASH_MISMATCH'));
    RETURN false;
  END IF;
  INSERT INTO askdata.release_projections(
    tenant_id,domain_id,release_id,target,expected_content_hash
  )
  SELECT selected_release.tenant_id,selected_release.domain_id,selected_release.id,target,selected_release.content_hash
  FROM unnest(ARRAY['POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER']) AS target;
  UPDATE askdata.releases SET status='PROJECTING',object_count=manifest_count,
    updated_by=selected_actor_id,version=version+1
  WHERE id=selected_release.id;
  INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,actor_id,detail)
  VALUES(selected_release.tenant_id,selected_release.domain_id,selected_release.id,'PROJECTING',selected_actor_id,jsonb_build_object('objectCount',manifest_count));
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION askdata.list_release_projection_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
  SELECT DISTINCT projection.tenant_id
  FROM askdata.release_projections AS projection
  JOIN askdata.releases AS release
    ON release.id=projection.release_id AND release.tenant_id=projection.tenant_id
  JOIN platform.tenants AS tenant ON tenant.id=projection.tenant_id
  WHERE release.status IN ('PROJECTING','BLOCKED')
    AND tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    AND projection.attempt<projection.max_attempts
    AND projection.next_attempt_at<=now()
    AND (
      projection.status IN ('PENDING','FAILED')
      OR (projection.status='RUNNING' AND projection.lease_expires_at<=now())
    )
  ORDER BY projection.tenant_id
$$;

CREATE OR REPLACE FUNCTION askdata.claim_release_projection(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  projection_id uuid,
  domain_id uuid,
  release_id uuid,
  target text,
  semantic_version text,
  content_hash text,
  lease_token uuid,
  attempt integer
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
    RAISE EXCEPTION 'invalid release projection lease parameters' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH candidate AS (
    SELECT projection.id
    FROM askdata.release_projections AS projection
    JOIN askdata.releases AS release
      ON release.id=projection.release_id AND release.tenant_id=projection.tenant_id
    WHERE projection.tenant_id=selected_tenant_id
      AND release.status IN ('PROJECTING','BLOCKED')
      AND projection.attempt<projection.max_attempts
      AND projection.next_attempt_at<=now()
      AND (
        projection.status IN ('PENDING','FAILED')
        OR (projection.status='RUNNING' AND projection.lease_expires_at<=now())
      )
    ORDER BY release.created_at,projection.target,projection.id
    FOR UPDATE OF projection SKIP LOCKED
    LIMIT 1
  ), claimed AS (
    UPDATE askdata.release_projections AS projection SET
      status='RUNNING',lease_owner=btrim(selected_worker_id),
      lease_token=gen_random_uuid(),
      lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
      attempt=projection.attempt+1,error_code='',detail='{}'::jsonb,
      started_at=COALESCE(projection.started_at,now()),completed_at=NULL,
      version=projection.version+1
    FROM candidate WHERE projection.id=candidate.id
    RETURNING projection.*
  )
  SELECT claimed.id,claimed.domain_id,claimed.release_id,claimed.target,
    release.semantic_version,claimed.expected_content_hash,
    claimed.lease_token,claimed.attempt
  FROM claimed JOIN askdata.releases AS release ON release.id=claimed.release_id;
END
$$;

CREATE OR REPLACE FUNCTION askdata.complete_release_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_content_hash text,
  selected_resource_version text,
  selected_object_count integer,
  selected_detail jsonb
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_domain_id uuid;
DECLARE all_ready boolean;
BEGIN
  IF selected_content_hash !~ '^[0-9a-f]{64}$'
    OR length(btrim(selected_resource_version)) NOT BETWEEN 1 AND 256
    OR selected_object_count<0
    OR selected_detail IS NULL OR jsonb_typeof(selected_detail)<>'object'
    OR pg_column_size(selected_detail)>65536
    OR NOT askdata.json_is_safe(selected_detail) THEN
    RAISE EXCEPTION 'invalid release projection completion proof' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.release_projections SET
    status='READY',applied_content_hash=selected_content_hash,
    resource_version=btrim(selected_resource_version),object_count=selected_object_count,
    detail=selected_detail,error_code='',completed_at=now(),
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    attempt=0,next_attempt_at=now(),version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND status='RUNNING' AND lease_owner=btrim(selected_worker_id)
    AND lease_token=selected_lease_token AND lease_expires_at>now()
    AND expected_content_hash=selected_content_hash
  RETURNING release_id,domain_id INTO selected_release_id,selected_domain_id;
  IF selected_release_id IS NULL THEN
    RETURN false;
  END IF;
  SELECT count(*)=4 AND bool_and(
    status='READY' AND applied_content_hash=expected_content_hash
    AND resource_version<>''
  ) INTO all_ready
  FROM askdata.release_projections
  WHERE tenant_id=selected_tenant_id AND release_id=selected_release_id;
  IF all_ready THEN
    UPDATE askdata.releases SET status='READY',ready_at=now(),version=version+1
    WHERE tenant_id=selected_tenant_id AND id=selected_release_id
      AND status IN ('PROJECTING','BLOCKED');
    INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,detail)
    VALUES(selected_tenant_id,selected_domain_id,selected_release_id,'READY',jsonb_build_object('contentHash',selected_content_hash));
  ELSE
    INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,detail)
    VALUES(selected_tenant_id,selected_domain_id,selected_release_id,'PROJECTION_READY',jsonb_build_object('projectionId',selected_projection_id));
  END IF;
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION askdata.fail_release_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_error_code text,
  retryable boolean
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_domain_id uuid;
DECLARE changed integer;
BEGIN
  IF selected_error_code !~ '^[A-Z][A-Z0-9_]{0,127}$' THEN
    RAISE EXCEPTION 'invalid release projection error code' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.release_projections SET
    status='FAILED',error_code=selected_error_code,
    next_attempt_at=CASE WHEN retryable THEN now()+LEAST(attempt,10)*interval '30 seconds' ELSE 'infinity'::timestamptz END,
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND status='RUNNING' AND lease_owner=btrim(selected_worker_id)
    AND lease_token=selected_lease_token
  RETURNING release_id,domain_id INTO selected_release_id,selected_domain_id;
  GET DIAGNOSTICS changed=ROW_COUNT;
  IF changed=0 THEN RETURN false; END IF;
  UPDATE askdata.releases SET status='BLOCKED',version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_release_id AND status='PROJECTING';
  INSERT INTO askdata.release_events(tenant_id,domain_id,release_id,event_type,detail)
  VALUES(selected_tenant_id,selected_domain_id,selected_release_id,'PROJECTION_FAILED',jsonb_build_object('projectionId',selected_projection_id,'errorCode',selected_error_code,'retryable',retryable));
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION askdata.reject_release_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'release events are immutable' USING ERRCODE='55000';
END
$$;

REVOKE ALL ON FUNCTION
  askdata.validate_release_object(),
  askdata.protect_release_manifest(),
  askdata.release_manifest_hash(uuid),
  askdata.initialize_release_state(),
  askdata.start_release_projection(uuid,uuid,jsonb),
  askdata.list_release_projection_tenants(),
  askdata.claim_release_projection(uuid,text,integer),
  askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb),
  askdata.fail_release_projection(uuid,uuid,text,uuid,text,boolean),
  askdata.validate_graph_plan_cache(),
  askdata.reject_release_event_mutation()
FROM PUBLIC;

CREATE TRIGGER askdata_release_objects_validate
BEFORE INSERT OR UPDATE ON askdata.release_objects
FOR EACH ROW EXECUTE FUNCTION askdata.validate_release_object();
CREATE TRIGGER askdata_release_objects_protect_manifest
BEFORE UPDATE OR DELETE ON askdata.release_objects
FOR EACH ROW EXECUTE FUNCTION askdata.protect_release_manifest();
CREATE TRIGGER askdata_domains_initialize_release_state
AFTER INSERT ON askdata.domains
FOR EACH ROW EXECUTE FUNCTION askdata.initialize_release_state();
CREATE TRIGGER askdata_release_events_immutable
BEFORE UPDATE OR DELETE ON askdata.release_events
FOR EACH ROW EXECUTE FUNCTION askdata.reject_release_event_mutation();
CREATE TRIGGER askdata_graph_plan_cache_validate
BEFORE INSERT OR UPDATE ON askdata.graph_plan_cache
FOR EACH ROW EXECUTE FUNCTION askdata.validate_graph_plan_cache();

INSERT INTO askdata.release_state(tenant_id,domain_id)
SELECT tenant_id,id FROM askdata.domains
ON CONFLICT(tenant_id,domain_id) DO NOTHING;

CREATE TRIGGER askdata_releases_set_updated_at BEFORE UPDATE ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_release_projections_set_updated_at BEFORE UPDATE ON askdata.release_projections
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();
CREATE TRIGGER askdata_release_state_set_updated_at BEFORE UPDATE ON askdata.release_state
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

DO $rls$
DECLARE relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'releases','release_objects','release_projections',
    'release_projection_artifacts','release_state','release_events',
    'graph_plan_cache'
  ] LOOP
    EXECUTE format('ALTER TABLE askdata.%I ENABLE ROW LEVEL SECURITY',relation_name);
    EXECUTE format('ALTER TABLE askdata.%I FORCE ROW LEVEL SECURITY',relation_name);
    EXECUTE format(
      'CREATE POLICY %I ON askdata.%I USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id)) WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))',
      'askdata_'||relation_name||'_domain_isolation',relation_name
    );
  END LOOP;
END
$rls$;

COMMENT ON TABLE askdata.releases IS
  'Immutable semantic manifests; READY requires four projection hashes to match exactly';
COMMENT ON TABLE askdata.release_state IS
  'Per-domain ACTIVE release pointer. No activation function exists before evaluation and approval gates are installed';
COMMENT ON TABLE askdata.release_projection_artifacts IS
  'Rebuildable PostgreSQL/search/execution projection artifacts; never the authoritative semantic source';
COMMENT ON TABLE askdata.graph_plan_cache IS
  'Bounded rebuildable GraphPlan cache pinned to release, policy scope and a READY NebulaGraph projection';
