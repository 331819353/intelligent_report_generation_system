-- NebulaGraph 是 semantic_releases 的可重建投影。租约函数是 worker 唯一的
-- 写入口；worker 不获得发布清单、投影水位或活动版本指针的直接 DML 权限。
ALTER TABLE platform.semantic_release_projections
  ADD COLUMN lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  ADD COLUMN lease_token uuid,
  ADD COLUMN lease_expires_at timestamptz,
  ADD COLUMN attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
  ADD COLUMN max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT semantic_release_projections_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL)
    OR (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  );

CREATE INDEX semantic_release_nebula_claim_idx
  ON platform.semantic_release_projections(
    target,status,next_attempt_at,lease_expires_at,tenant_id,updated_at,id
  ) WHERE target='NEBULA_GRAPH';

CREATE OR REPLACE FUNCTION platform.list_semantic_nebula_projection_tenants()
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
  WHERE projection.target='NEBULA_GRAPH'
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

CREATE OR REPLACE FUNCTION platform.claim_semantic_nebula_projection(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
RETURNS TABLE(
  projection_id uuid,
  release_id uuid,
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
    OR selected_worker_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION '无效的 NebulaGraph 投影租约参数' USING ERRCODE='22023';
  END IF;

  RETURN QUERY
  WITH candidate AS (
    SELECT projection.id
    FROM platform.semantic_release_projections AS projection
    JOIN platform.semantic_releases AS release
      ON release.id=projection.release_id
     AND release.tenant_id=projection.tenant_id
    WHERE projection.tenant_id=selected_tenant_id
      AND projection.target='NEBULA_GRAPH'
      AND release.status IN ('PROJECTING','BLOCKED')
      AND projection.attempt<projection.max_attempts
      AND projection.next_attempt_at<=now()
      AND (
        projection.status IN ('PENDING','FAILED')
        OR (projection.status='RUNNING' AND projection.lease_expires_at<=now())
      )
    ORDER BY release.created_at,projection.id
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
    RETURNING projection.id,projection.release_id,
      projection.expected_content_hash,projection.lease_token,projection.attempt
  )
  SELECT claimed.id,claimed.release_id,release.semantic_version,
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

CREATE OR REPLACE FUNCTION platform.heartbeat_semantic_nebula_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
  selected_worker_id text,
  selected_lease_token uuid,
  selected_lease_seconds integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE changed integer;
BEGIN
  IF selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION '无效的 NebulaGraph 投影续租时间' USING ERRCODE='22023';
  END IF;
  UPDATE platform.semantic_release_projections
  SET lease_expires_at=now()+(selected_lease_seconds*interval '1 second')
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND target='NEBULA_GRAPH' AND status='RUNNING'
    AND lease_owner=selected_worker_id AND lease_token=selected_lease_token
    AND lease_expires_at>now();
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

CREATE OR REPLACE FUNCTION platform.complete_semantic_nebula_projection(
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
DECLARE ready_release boolean;
BEGIN
  IF selected_content_hash !~ '^[0-9a-f]{64}$'
    OR length(btrim(selected_resource_version)) NOT BETWEEN 1 AND 256
    OR selected_object_count<1
    OR jsonb_typeof(selected_detail)<>'object'
    OR pg_column_size(selected_detail)>65536
    OR NOT platform.materialization_json_is_safe(selected_detail) THEN
    RAISE EXCEPTION '无效的 NebulaGraph 投影完成证明' USING ERRCODE='22023';
  END IF;

  UPDATE platform.semantic_release_projections
  SET status='READY',applied_content_hash=selected_content_hash,
      resource_version=btrim(selected_resource_version),
      object_count=selected_object_count,detail=selected_detail,error_code='',
      completed_at=now(),lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      attempt=0,next_attempt_at=now(),version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND target='NEBULA_GRAPH' AND status='RUNNING'
    AND lease_owner=selected_worker_id AND lease_token=selected_lease_token
    AND lease_expires_at>now() AND expected_content_hash=selected_content_hash
  RETURNING release_id INTO selected_release_id;
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
      'target','NEBULA_GRAPH','status','READY',
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

CREATE OR REPLACE FUNCTION platform.fail_semantic_nebula_projection(
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
DECLARE exhausted boolean;
BEGIN
  IF length(btrim(selected_error_code)) NOT BETWEEN 1 AND 128
    OR selected_error_code ~ '[[:cntrl:]]'
    OR jsonb_typeof(selected_detail)<>'object'
    OR pg_column_size(selected_detail)>65536
    OR NOT platform.materialization_json_is_safe(selected_detail) THEN
    RAISE EXCEPTION '无效的 NebulaGraph 投影失败证明' USING ERRCODE='22023';
  END IF;

  UPDATE platform.semantic_release_projections
  SET status='FAILED',error_code=btrim(selected_error_code),detail=selected_detail,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      next_attempt_at=now()+(least(300,power(2,attempt)::integer)*interval '1 second'),
      version=version+1
  WHERE tenant_id=selected_tenant_id AND id=selected_projection_id
    AND target='NEBULA_GRAPH' AND status='RUNNING'
    AND lease_owner=selected_worker_id AND lease_token=selected_lease_token
  RETURNING release_id,(attempt>=max_attempts)
  INTO selected_release_id,exhausted;
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
      'target','NEBULA_GRAPH','status','FAILED',
      'errorCode',btrim(selected_error_code),'exhausted',exhausted
    )
  );
  RETURN true;
END
$$;

REVOKE ALL ON FUNCTION
  platform.list_semantic_nebula_projection_tenants(),
  platform.claim_semantic_nebula_projection(uuid,text,integer),
  platform.heartbeat_semantic_nebula_projection(uuid,uuid,text,uuid,integer),
  platform.complete_semantic_nebula_projection(uuid,uuid,text,uuid,text,text,integer,jsonb),
  platform.fail_semantic_nebula_projection(uuid,uuid,text,uuid,text,jsonb)
FROM PUBLIC;

-- 只有完整匹配活动语义版本和内容哈希的认证 GraphPlan 才能在图服务故障时
-- 被 API 重放；缓存不是图定义事实源，也不能跨租户或跨版本复用。
CREATE TABLE platform.semantic_graph_plan_cache(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  semantic_version text NOT NULL CHECK(length(semantic_version) BETWEEN 3 AND 128),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  plan_json jsonb NOT NULL CHECK(
    jsonb_typeof(plan_json)='object' AND pg_column_size(plan_json)<=262144
    AND platform.materialization_json_is_safe(plan_json)
  ),
  certified boolean NOT NULL CHECK(certified),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_graph_plan_cache_key UNIQUE(
    tenant_id,semantic_version,content_hash,request_hash
  )
);
CREATE INDEX semantic_graph_plan_cache_expiry_idx
  ON platform.semantic_graph_plan_cache(tenant_id,expires_at);
CREATE TRIGGER semantic_graph_plan_cache_set_updated_at
BEFORE UPDATE ON platform.semantic_graph_plan_cache
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
ALTER TABLE platform.semantic_graph_plan_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_plan_cache FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_graph_plan_cache_tenant_isolation
  ON platform.semantic_graph_plan_cache
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.semantic_graph_plan_cache IS
  'NebulaGraph 故障时唯一允许的降级路径；仅保存同版本同哈希认证 GraphPlan';
