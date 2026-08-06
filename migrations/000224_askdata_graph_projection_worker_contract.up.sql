-- Target-scoped release projection leasing and a label-free graph snapshot.
-- The generic 000216 claim function remains available for compatibility, but
-- graph workers use this target-specific overload so they cannot consume work
-- owned by search, registry, or execution-semantic projectors.
CREATE OR REPLACE FUNCTION askdata.list_release_projection_tenants(
  selected_target text
)
RETURNS TABLE(tenant_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
BEGIN
  IF selected_target NOT IN (
    'POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER'
  ) THEN
    RAISE EXCEPTION 'invalid release projection target' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  SELECT DISTINCT projection.tenant_id
  FROM askdata.release_projections AS projection
  JOIN askdata.releases AS release
    ON release.id=projection.release_id AND release.tenant_id=projection.tenant_id
  JOIN platform.tenants AS tenant ON tenant.id=projection.tenant_id
  WHERE projection.target=selected_target
    AND release.status IN ('PROJECTING','BLOCKED')
    AND tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
    AND projection.attempt<projection.max_attempts
    AND projection.next_attempt_at<=now()
    AND (
      projection.status IN ('PENDING','FAILED')
      OR (projection.status='RUNNING' AND projection.lease_expires_at<=now())
    )
  ORDER BY projection.tenant_id;
END
$$;

CREATE OR REPLACE FUNCTION askdata.claim_release_projection(
  selected_tenant_id uuid,
  selected_target text,
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
    OR selected_target NOT IN (
      'POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER'
    )
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid target-scoped release projection lease parameters'
      USING ERRCODE='22023';
  END IF;

  RETURN QUERY
  WITH candidate AS (
    SELECT projection.id
    FROM askdata.release_projections AS projection
    JOIN askdata.releases AS release
      ON release.id=projection.release_id
     AND release.tenant_id=projection.tenant_id
    WHERE projection.tenant_id=selected_tenant_id
      AND projection.target=selected_target
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
    UPDATE askdata.release_projections AS projection SET
      status='RUNNING',lease_owner=btrim(selected_worker_id),
      lease_token=gen_random_uuid(),
      lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
      attempt=projection.attempt+1,error_code='',detail='{}'::jsonb,
      started_at=COALESCE(projection.started_at,now()),completed_at=NULL,
      version=projection.version+1
    FROM candidate
    WHERE projection.id=candidate.id
    RETURNING projection.*
  )
  SELECT claimed.id,claimed.domain_id,claimed.release_id,claimed.target,
    release.semantic_version,claimed.expected_content_hash,
    claimed.lease_token,claimed.attempt
  FROM claimed
  JOIN askdata.releases AS release ON release.id=claimed.release_id;
END
$$;

CREATE OR REPLACE FUNCTION askdata.heartbeat_release_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
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
  IF selected_tenant_id IS NULL OR selected_projection_id IS NULL
    OR selected_lease_token IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid release projection heartbeat parameters'
      USING ERRCODE='22023';
  END IF;

  UPDATE askdata.release_projections AS projection SET
    lease_expires_at=now()+(selected_lease_seconds*interval '1 second'),
    version=projection.version+1
  FROM askdata.releases AS release
  WHERE projection.tenant_id=selected_tenant_id
    AND projection.id=selected_projection_id
    AND projection.status='RUNNING'
    AND projection.lease_owner=btrim(selected_worker_id)
    AND projection.lease_token=selected_lease_token
    AND projection.lease_expires_at>now()
    AND release.id=projection.release_id
    AND release.tenant_id=projection.tenant_id
    AND release.status IN ('PROJECTING','BLOCKED');
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

-- This function is the only member metadata surface needed by the graph
-- projector. It deliberately returns opaque IDs, version numbers and graph
-- relationship facts, never canonical labels, aliases, lookup hashes, ASTs,
-- physical identifiers, or arbitrary contract JSON.
CREATE OR REPLACE FUNCTION askdata.load_release_graph_projection(
  selected_tenant_id uuid,
  selected_projection_id uuid,
  selected_worker_id text,
  selected_lease_token uuid
)
RETURNS TABLE(
  element_kind text,
  graph_type text,
  object_id text,
  object_version_id text,
  version_no integer,
  member_status text,
  from_object_id text,
  from_version_id text,
  from_version_no integer,
  to_object_id text,
  to_version_id text,
  to_version_no integer,
  relationship_version_id text,
  join_type text,
  cardinality text,
  fanout_policy text,
  certified boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_domain_id uuid;
BEGIN
  IF selected_tenant_id IS NULL OR selected_projection_id IS NULL
    OR selected_lease_token IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'invalid graph projection snapshot parameters'
      USING ERRCODE='22023';
  END IF;

  SELECT projection.release_id,projection.domain_id
  INTO selected_release_id,selected_domain_id
  FROM askdata.release_projections AS projection
  JOIN askdata.releases AS release
    ON release.id=projection.release_id
   AND release.domain_id=projection.domain_id
   AND release.tenant_id=projection.tenant_id
  WHERE projection.tenant_id=selected_tenant_id
    AND projection.id=selected_projection_id
    AND projection.target='NEBULA_GRAPH'
    AND projection.status='RUNNING'
    AND projection.lease_owner=btrim(selected_worker_id)
    AND projection.lease_token=selected_lease_token
    AND projection.lease_expires_at>now()
    AND release.status IN ('PROJECTING','BLOCKED')
    AND release.content_hash=projection.expected_content_hash;
  IF selected_release_id IS NULL THEN
    RAISE EXCEPTION 'graph projection lease is not current'
      USING ERRCODE='55000';
  END IF;

  RETURN QUERY
  WITH release_object AS MATERIALIZED (
    SELECT released.object_type,released.object_id,
      released.object_version_id,released.content_hash
    FROM askdata.release_objects AS released
    WHERE released.tenant_id=selected_tenant_id
      AND released.domain_id=selected_domain_id
      AND released.release_id=selected_release_id
  ), vertices AS (
    SELECT 'VERTEX'::text AS element_kind,'semantic_model'::text AS graph_type,
      model.model_id::text AS object_id,model.id::text AS object_version_id,
      model.version_no,''::text AS member_status
    FROM release_object AS released
    JOIN askdata.semantic_models AS model
      ON released.object_type='SEMANTIC_MODEL'
     AND model.tenant_id=selected_tenant_id
     AND model.domain_id=selected_domain_id
     AND model.model_id=released.object_id
     AND model.id=released.object_version_id
     AND model.content_hash=released.content_hash
     AND model.status='CERTIFIED'
    UNION ALL
    SELECT 'VERTEX','metric',metric.metric_id::text,metric.id::text,
      metric.version_no,''
    FROM release_object AS released
    JOIN askdata.metric_versions AS metric
      ON released.object_type='METRIC'
     AND metric.tenant_id=selected_tenant_id
     AND metric.domain_id=selected_domain_id
     AND metric.metric_id=released.object_id
     AND metric.id=released.object_version_id
     AND metric.content_hash=released.content_hash
     AND metric.status='CERTIFIED'
    UNION ALL
    SELECT 'VERTEX','dimension',dimension.dimension_id::text,dimension.id::text,
      dimension.version_no,''
    FROM release_object AS released
    JOIN askdata.dimensions AS dimension
      ON released.object_type='DIMENSION'
     AND dimension.tenant_id=selected_tenant_id
     AND dimension.domain_id=selected_domain_id
     AND dimension.dimension_id=released.object_id
     AND dimension.id=released.object_version_id
     AND dimension.content_hash=released.content_hash
     AND dimension.status='CERTIFIED'
    UNION ALL
    SELECT 'VERTEX','member',member.member_id::text,member.id::text,
      member.version_no,
      CASE WHEN member.valid_from<=CURRENT_TIMESTAMP
        AND (member.valid_to IS NULL OR member.valid_to>CURRENT_TIMESTAMP)
        THEN 'ACTIVE' ELSE 'EXPIRED' END
    FROM release_object AS released
    JOIN askdata.dimension_members AS member
      ON released.object_type='MEMBER'
     AND member.tenant_id=selected_tenant_id
     AND member.domain_id=selected_domain_id
     AND member.member_id=released.object_id
     AND member.id=released.object_version_id
     AND member.content_hash=released.content_hash
     AND member.status='CERTIFIED'
  ), edges AS (
    SELECT 'EDGE'::text AS element_kind,'MODELED_BY'::text AS graph_type,
      metric_release.object_id::text AS object_id,
      metric_release.object_version_id::text AS object_version_id,
      metric.version_no,
      model_release.object_id::text AS to_object_id,
      model_release.object_version_id::text AS to_version_id,
      model.version_no AS to_version_no,
      ''::text AS relationship_version_id,''::text AS join_type,
      ''::text AS cardinality,''::text AS fanout_policy,false AS certified
    FROM release_object AS metric_release
    JOIN askdata.metric_versions AS metric
      ON metric_release.object_type='METRIC'
     AND metric.id=metric_release.object_version_id
     AND metric.metric_id=metric_release.object_id
     AND metric.tenant_id=selected_tenant_id
     AND metric.domain_id=selected_domain_id
     AND metric.content_hash=metric_release.content_hash
     AND metric.status='CERTIFIED'
    JOIN release_object AS model_release
      ON model_release.object_type='SEMANTIC_MODEL'
     AND model_release.object_version_id=metric.semantic_model_version_id
    JOIN askdata.semantic_models AS model
      ON model.id=model_release.object_version_id
     AND model.model_id=model_release.object_id
     AND model.tenant_id=selected_tenant_id
     AND model.domain_id=selected_domain_id
     AND model.content_hash=model_release.content_hash
     AND model.status='CERTIFIED'
    UNION ALL
    SELECT 'EDGE','HAS_DIMENSION',model_release.object_id::text,
      model_release.object_version_id::text,model.version_no,
      dimension_release.object_id::text,dimension_release.object_version_id::text,
      dimension.version_no,'','','','',false
    FROM release_object AS dimension_release
    JOIN askdata.dimensions AS dimension
      ON dimension_release.object_type='DIMENSION'
     AND dimension.id=dimension_release.object_version_id
     AND dimension.dimension_id=dimension_release.object_id
     AND dimension.tenant_id=selected_tenant_id
     AND dimension.domain_id=selected_domain_id
     AND dimension.content_hash=dimension_release.content_hash
     AND dimension.status='CERTIFIED'
    JOIN release_object AS model_release
      ON model_release.object_type='SEMANTIC_MODEL'
     AND model_release.object_version_id=dimension.semantic_model_version_id
    JOIN askdata.semantic_models AS model
      ON model.id=model_release.object_version_id
     AND model.model_id=model_release.object_id
     AND model.tenant_id=selected_tenant_id
     AND model.domain_id=selected_domain_id
     AND model.content_hash=model_release.content_hash
     AND model.status='CERTIFIED'
    UNION ALL
    SELECT 'EDGE','HAS_MEMBER',dimension_release.object_id::text,
      dimension_release.object_version_id::text,dimension.version_no,
      member_release.object_id::text,member_release.object_version_id::text,
      member.version_no,'','','','',false
    FROM release_object AS member_release
    JOIN askdata.dimension_members AS member
      ON member_release.object_type='MEMBER'
     AND member.id=member_release.object_version_id
     AND member.member_id=member_release.object_id
     AND member.tenant_id=selected_tenant_id
     AND member.domain_id=selected_domain_id
     AND member.content_hash=member_release.content_hash
     AND member.status='CERTIFIED'
    JOIN release_object AS dimension_release
      ON dimension_release.object_type='DIMENSION'
     AND dimension_release.object_version_id=member.dimension_version_id
    JOIN askdata.dimensions AS dimension
      ON dimension.id=dimension_release.object_version_id
     AND dimension.dimension_id=dimension_release.object_id
     AND dimension.tenant_id=selected_tenant_id
     AND dimension.domain_id=selected_domain_id
     AND dimension.content_hash=dimension_release.content_hash
     AND dimension.status='CERTIFIED'
    UNION ALL
    SELECT 'EDGE','JOINS_TO',left_release.object_id::text,
      left_release.object_version_id::text,left_model.version_no,
      right_release.object_id::text,right_release.object_version_id::text,
      right_model.version_no,relationship.id::text,relationship.join_type,
      relationship.cardinality,relationship.fanout_policy,true
    FROM release_object AS relationship_release
    JOIN askdata.relationships AS relationship
      ON relationship_release.object_type='RELATIONSHIP'
     AND relationship.id=relationship_release.object_version_id
     AND relationship.relationship_id=relationship_release.object_id
     AND relationship.tenant_id=selected_tenant_id
     AND relationship.domain_id=selected_domain_id
     AND relationship.content_hash=relationship_release.content_hash
     AND relationship.status='CERTIFIED'
     AND relationship.relationship_type='MODEL_JOIN'
    JOIN release_object AS left_release
      ON left_release.object_type='SEMANTIC_MODEL'
     AND left_release.object_version_id=relationship.left_model_version_id
    JOIN askdata.semantic_models AS left_model
      ON left_model.id=left_release.object_version_id
     AND left_model.model_id=left_release.object_id
     AND left_model.tenant_id=selected_tenant_id
     AND left_model.domain_id=selected_domain_id
     AND left_model.content_hash=left_release.content_hash
     AND left_model.status='CERTIFIED'
    JOIN release_object AS right_release
      ON right_release.object_type='SEMANTIC_MODEL'
     AND right_release.object_version_id=relationship.right_model_version_id
    JOIN askdata.semantic_models AS right_model
      ON right_model.id=right_release.object_version_id
     AND right_model.model_id=right_release.object_id
     AND right_model.tenant_id=selected_tenant_id
     AND right_model.domain_id=selected_domain_id
     AND right_model.content_hash=right_release.content_hash
     AND right_model.status='CERTIFIED'
  )
  SELECT vertices.element_kind,vertices.graph_type,vertices.object_id,
    vertices.object_version_id,vertices.version_no,vertices.member_status,
    ''::text,''::text,0,''::text,''::text,0,
    ''::text,''::text,''::text,''::text,false
  FROM vertices
  UNION ALL
  SELECT edges.element_kind,edges.graph_type,''::text,''::text,0,''::text,
    edges.object_id,edges.object_version_id,edges.version_no,
    edges.to_object_id,edges.to_version_id,edges.to_version_no,
    edges.relationship_version_id,edges.join_type,edges.cardinality,
    edges.fanout_policy,edges.certified
  FROM edges
  ORDER BY 1,2,3,4,7,8,10,11,13;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.list_release_projection_tenants(text),
  askdata.claim_release_projection(uuid,text,text,integer),
  askdata.heartbeat_release_projection(uuid,uuid,text,uuid,integer),
  askdata.load_release_graph_projection(uuid,uuid,text,uuid)
FROM PUBLIC;

COMMENT ON FUNCTION askdata.load_release_graph_projection(uuid,uuid,text,uuid) IS
  'Lease-bound, label-free immutable graph projection snapshot for the NEBULA_GRAPH worker';
