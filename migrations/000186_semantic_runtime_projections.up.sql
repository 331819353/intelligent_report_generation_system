-- Materialized outputs for the three PostgreSQL-backed release projections.
-- NebulaGraph remains owned by its dedicated fenced projection worker.
CREATE TABLE platform.semantic_execution_registry(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  release_id uuid NOT NULL,
  object_type text NOT NULL,
  object_id text NOT NULL,
  object_version text NOT NULL,
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  native_object_id text NOT NULL DEFAULT '',
  native_version_id text NOT NULL DEFAULT '',
  contract_json jsonb NOT NULL CHECK(
    jsonb_typeof(contract_json)='object'
    AND pg_column_size(contract_json)<=65536
    AND platform.materialization_json_is_safe(contract_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,release_id,object_type,object_id,object_version),
  CONSTRAINT semantic_execution_registry_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX semantic_execution_registry_native_idx
  ON platform.semantic_execution_registry(
    tenant_id,release_id,object_type,native_object_id,native_version_id
  );
ALTER TABLE platform.semantic_execution_registry ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_execution_registry FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_execution_registry_tenant_isolation
  ON platform.semantic_execution_registry
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE TABLE platform.semantic_release_search_documents(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  release_id uuid NOT NULL,
  object_type text NOT NULL,
  object_id text NOT NULL,
  object_version text NOT NULL,
  view_type text NOT NULL CHECK(view_type IN (
    'NAME_ALIAS','DEFINITION_QUESTION','EXAMPLE_INTENT'
  )),
  document text NOT NULL CHECK(
    length(document) BETWEEN 1 AND 32768 AND document !~ '[[:cntrl:]]'
  ),
  document_tsv tsvector GENERATED ALWAYS AS (
    to_tsvector('simple',document)
  ) STORED,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(metadata)='object'
    AND pg_column_size(metadata)<=65536
    AND platform.materialization_json_is_safe(metadata)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(
    tenant_id,release_id,object_type,object_id,object_version,view_type
  ),
  CONSTRAINT semantic_release_search_documents_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE CASCADE
);
CREATE INDEX semantic_release_search_documents_fts_idx
  ON platform.semantic_release_search_documents USING gin(document_tsv);
CREATE INDEX semantic_release_search_documents_lookup_idx
  ON platform.semantic_release_search_documents(
    tenant_id,release_id,object_type,view_type,object_id
  );
ALTER TABLE platform.semantic_release_search_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_release_search_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_release_search_documents_tenant_isolation
  ON platform.semantic_release_search_documents
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.list_semantic_runtime_projection_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT DISTINCT projection.tenant_id
  FROM platform.semantic_release_projections AS projection
  JOIN platform.semantic_releases AS release
    ON release.id=projection.release_id AND release.tenant_id=projection.tenant_id
  JOIN platform.tenants AS tenant ON tenant.id=projection.tenant_id
  WHERE projection.target IN (
      'EXECUTION_SEMANTIC_LAYER','POSTGRES_REGISTRY','SEARCH_INDEX'
    )
    AND release.status IN ('PROJECTING','BLOCKED')
    AND tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    AND projection.attempt<projection.max_attempts
    AND projection.next_attempt_at<=now()
    AND (
      projection.status IN ('PENDING','FAILED')
      OR (projection.status='RUNNING' AND projection.lease_expires_at<=now())
    )
  ORDER BY projection.tenant_id
$$;

CREATE OR REPLACE FUNCTION platform.claim_semantic_runtime_projection(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  projection_id uuid,
  release_id uuid,
  target text,
  semantic_version text,
  content_hash text,
  lease_token uuid,
  attempt integer
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION '无效的语义运行时投影租约参数' USING ERRCODE='22023';
  END IF;

  RETURN QUERY
  WITH candidate AS (
    SELECT projection.id
    FROM platform.semantic_release_projections AS projection
    JOIN platform.semantic_releases AS release
      ON release.id=projection.release_id
     AND release.tenant_id=projection.tenant_id
    WHERE projection.tenant_id=selected_tenant_id
      AND projection.target IN (
        'EXECUTION_SEMANTIC_LAYER','POSTGRES_REGISTRY','SEARCH_INDEX'
      )
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
    UPDATE platform.semantic_release_projections AS projection
    SET status='RUNNING',lease_owner=btrim(selected_worker_id),
        lease_token=gen_random_uuid(),
        lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
        attempt=projection.attempt+1,error_code='',detail='{}'::jsonb,
        started_at=COALESCE(projection.started_at,now()),completed_at=NULL,
        version=projection.version+1
    FROM candidate
    WHERE projection.id=candidate.id
    RETURNING projection.id,projection.release_id,projection.target,
      projection.expected_content_hash,projection.lease_token,projection.attempt
  )
  SELECT claimed.id,claimed.release_id,claimed.target,release.semantic_version,
    claimed.expected_content_hash,claimed.lease_token,claimed.attempt
  FROM claimed
  JOIN platform.semantic_releases AS release ON release.id=claimed.release_id;

  UPDATE platform.semantic_releases AS release
  SET status='PROJECTING',version=release.version+1
  WHERE release.tenant_id=selected_tenant_id
    AND release.status='BLOCKED'
    AND EXISTS(
      SELECT 1 FROM platform.semantic_release_projections AS projection
      WHERE projection.release_id=release.id AND projection.status='RUNNING'
        AND projection.lease_owner=btrim(selected_worker_id)
    );
END
$$;

CREATE OR REPLACE FUNCTION platform.complete_semantic_runtime_projection(
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
SET search_path=pg_catalog,platform
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_target text;
DECLARE ready_release boolean;
BEGIN
  IF selected_content_hash !~ '^[0-9a-f]{64}$'
    OR length(btrim(selected_resource_version)) NOT BETWEEN 1 AND 256
    OR selected_object_count<1
    OR jsonb_typeof(selected_detail)<>'object'
    OR pg_column_size(selected_detail)>65536
    OR NOT platform.materialization_json_is_safe(selected_detail) THEN
    RAISE EXCEPTION '无效的语义运行时投影完成证明' USING ERRCODE='22023';
  END IF;

  UPDATE platform.semantic_release_projections
  SET status='READY',applied_content_hash=selected_content_hash,
      resource_version=btrim(selected_resource_version),
      object_count=selected_object_count,detail=selected_detail,error_code='',
      completed_at=now(),lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      attempt=0,next_attempt_at=now(),version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND target IN ('EXECUTION_SEMANTIC_LAYER','POSTGRES_REGISTRY','SEARCH_INDEX')
    AND status='RUNNING' AND lease_owner=selected_worker_id
    AND lease_token=selected_lease_token AND lease_expires_at>now()
    AND expected_content_hash=selected_content_hash
  RETURNING release_id,target INTO selected_release_id,selected_target;
  IF selected_release_id IS NULL THEN
    RETURN false;
  END IF;

  SELECT count(*)=4 AND bool_and(
    projection.status='READY'
    AND projection.applied_content_hash=projection.expected_content_hash
    AND projection.resource_version<>''
  ) INTO ready_release
  FROM platform.semantic_release_projections AS projection
  WHERE projection.tenant_id=selected_tenant_id
    AND projection.release_id=selected_release_id;

  IF ready_release THEN
    UPDATE platform.semantic_releases
    SET status='READY',version=version+1
    WHERE tenant_id=selected_tenant_id AND id=selected_release_id
      AND status='PROJECTING';
  END IF;
  INSERT INTO platform.semantic_release_events(
    tenant_id,release_id,event_type,detail
  ) VALUES(
    selected_tenant_id,selected_release_id,'PROJECTION_UPDATED',
    jsonb_build_object(
      'target',selected_target,'status','READY',
      'contentHash',selected_content_hash,
      'resourceVersion',selected_resource_version,
      'objectCount',selected_object_count
    )
  );
  IF ready_release THEN
    INSERT INTO platform.semantic_release_events(
      tenant_id,release_id,event_type,detail
    ) VALUES(selected_tenant_id,selected_release_id,'READY','{}'::jsonb);
  END IF;
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION platform.fail_semantic_runtime_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_error_code text,
  selected_detail jsonb
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_target text;
DECLARE exhausted boolean;
BEGIN
  IF length(btrim(selected_error_code)) NOT BETWEEN 1 AND 128
    OR selected_error_code ~ '[[:cntrl:]]'
    OR jsonb_typeof(selected_detail)<>'object'
    OR pg_column_size(selected_detail)>65536
    OR NOT platform.materialization_json_is_safe(selected_detail) THEN
    RAISE EXCEPTION '无效的语义运行时投影失败证明' USING ERRCODE='22023';
  END IF;
  UPDATE platform.semantic_release_projections
  SET status='FAILED',error_code=btrim(selected_error_code),detail=selected_detail,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      next_attempt_at=now()+(least(300,power(2,attempt)::integer)*interval '1 second'),
      version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND target IN ('EXECUTION_SEMANTIC_LAYER','POSTGRES_REGISTRY','SEARCH_INDEX')
    AND status='RUNNING' AND lease_owner=selected_worker_id
    AND lease_token=selected_lease_token
  RETURNING release_id,target,(attempt>=max_attempts)
  INTO selected_release_id,selected_target,exhausted;
  IF selected_release_id IS NULL THEN
    RETURN false;
  END IF;
  IF exhausted THEN
    UPDATE platform.semantic_releases
    SET status='BLOCKED',version=version+1
    WHERE tenant_id=selected_tenant_id AND id=selected_release_id
      AND status='PROJECTING';
  END IF;
  INSERT INTO platform.semantic_release_events(
    tenant_id,release_id,event_type,detail
  ) VALUES(
    selected_tenant_id,selected_release_id,'PROJECTION_UPDATED',
    jsonb_build_object(
      'target',selected_target,'status','FAILED',
      'errorCode',btrim(selected_error_code),'exhausted',exhausted
    )
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION
  platform.list_semantic_runtime_projection_tenants(),
  platform.claim_semantic_runtime_projection(uuid,text,integer),
  platform.complete_semantic_runtime_projection(uuid,uuid,text,uuid,text,text,integer,jsonb),
  platform.fail_semantic_runtime_projection(uuid,uuid,text,uuid,text,jsonb)
FROM PUBLIC;

COMMENT ON TABLE platform.semantic_execution_registry IS
  '活动发布前构建的确定性执行语义适配注册表；固定发布对象、原生对象和版本引用';
COMMENT ON TABLE platform.semantic_release_search_documents IS
  '按 semantic release 构建的名称别名、定义问法和认证示例多视图全文检索投影';
