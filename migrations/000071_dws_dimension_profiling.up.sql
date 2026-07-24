-- DWS 维度字段实测画像。
--
-- 每个任务固定到一个精确的 ACTIVE DWS 物化、schema、snapshot 与字段。
-- 画像只保存聚合计数和策略结论；不保存最小值、最大值、TopN、样本或原始值。

CREATE TABLE platform.dimension_profile_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  materialization_id uuid NOT NULL,
  materialization_snapshot_hash text NOT NULL CHECK(
    materialization_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  expected_row_count bigint NOT NULL CHECK(expected_row_count>=0),
  field_id text NOT NULL CHECK(length(field_id) BETWEEN 1 AND 256),
  field_code text NOT NULL CHECK(
    length(field_code) BETWEEN 1 AND 128
    AND field_code=btrim(field_code)
    AND field_code !~ '[[:cntrl:]]'
  ),
  field_role text NOT NULL CHECK(
    field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  ),
  canonical_type text NOT NULL CHECK(
    length(canonical_type) BETWEEN 1 AND 64
    AND canonical_type=btrim(canonical_type)
    AND canonical_type !~ '[[:cntrl:]]'
  ),
  semantic_type text NOT NULL DEFAULT '' CHECK(
    length(semantic_type)<=100
    AND semantic_type=btrim(semantic_type)
    AND semantic_type !~ '[[:cntrl:]]'
  ),
  profile_version text NOT NULL CHECK(profile_version='dws-dimension-profile-v1'),
  policy_version text NOT NULL CHECK(policy_version='dimension-member-policy-v1'),
  distinct_cap integer NOT NULL CHECK(distinct_cap BETWEEN 1 AND 1000000),
  high_ratio_threshold numeric(9,8) NOT NULL CHECK(
    high_ratio_threshold>0 AND high_ratio_threshold<=1
  ),
  high_ratio_min_non_null bigint NOT NULL CHECK(high_ratio_min_non_null>=1),
  timeout_seconds integer NOT NULL CHECK(timeout_seconds BETWEEN 1 AND 300),
  work_mem_kb integer NOT NULL CHECK(work_mem_kb BETWEEN 64 AND 262144),
  temp_file_limit_kb integer NOT NULL CHECK(
    temp_file_limit_kb BETWEEN 1024 AND 1048576
  ),
  status text NOT NULL DEFAULT 'QUEUED' CHECK(
    status IN (
      'QUEUED','RUNNING','SUCCEEDED','SKIPPED_POLICY','FAILED','STALE'
    )
  ),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  row_count bigint CHECK(row_count IS NULL OR row_count>=0),
  non_null_count bigint CHECK(non_null_count IS NULL OR non_null_count>=0),
  null_count bigint CHECK(null_count IS NULL OR null_count>=0),
  distinct_count bigint CHECK(distinct_count IS NULL OR distinct_count>=0),
  distinct_overflow boolean NOT NULL DEFAULT false,
  distinct_ratio numeric(12,10) CHECK(
    distinct_ratio IS NULL OR (distinct_ratio>=0 AND distinct_ratio<=1)
  ),
  risk_high_cardinality boolean NOT NULL DEFAULT false,
  recommended_member_index_policy text NOT NULL DEFAULT '' CHECK(
    recommended_member_index_policy=''
    OR recommended_member_index_policy IN ('FULL','EXACT_ONLY','NONE')
  ),
  evidence_hash text NOT NULL DEFAULT '' CHECK(
    evidence_hash='' OR evidence_hash ~ '^[0-9a-f]{64}$'
  ),
  result_code text NOT NULL DEFAULT '' CHECK(length(result_code)<=128),
  requested_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dimension_profile_jobs_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,schema_hash)
    REFERENCES platform.dataset_versions(
      id,dataset_id,tenant_id,schema_hash
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_profile_jobs_materialization_fk
    FOREIGN KEY(materialization_id,dataset_id,dataset_version_id,tenant_id)
    REFERENCES platform.dataset_materializations(
      id,dataset_id,dataset_version_id,tenant_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_profile_jobs_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,field_id)
    REFERENCES platform.dataset_fields(
      tenant_id,dataset_version_id,field_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_profile_jobs_requested_by_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_profile_jobs_attempt_bounds_check
    CHECK(attempt<=max_attempts),
  CONSTRAINT dimension_profile_jobs_result_counts_check CHECK(
    (row_count IS NULL AND non_null_count IS NULL AND null_count IS NULL)
    OR
    (row_count IS NOT NULL AND non_null_count IS NOT NULL AND null_count IS NOT NULL
      AND row_count=non_null_count+null_count)
  ),
  CONSTRAINT dimension_profile_jobs_distinct_shape_check CHECK(
    distinct_count IS NULL
    OR (
      non_null_count IS NOT NULL
      AND distinct_count<=non_null_count
      AND distinct_count<=distinct_cap
    )
  ),
  CONSTRAINT dimension_profile_jobs_status_shape_check CHECK(
    (
      status='QUEUED'
      AND ((attempt=0 AND started_at IS NULL) OR (attempt>0 AND started_at IS NOT NULL))
      AND completed_at IS NULL
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND row_count IS NULL AND non_null_count IS NULL AND null_count IS NULL
      AND distinct_count IS NULL AND NOT distinct_overflow
      AND distinct_ratio IS NULL AND NOT risk_high_cardinality
      AND recommended_member_index_policy='' AND evidence_hash=''
      AND result_code=''
    )
    OR
    (
      status='RUNNING' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NULL
      AND lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL
      AND row_count IS NULL AND non_null_count IS NULL AND null_count IS NULL
      AND distinct_count IS NULL AND NOT distinct_overflow
      AND distinct_ratio IS NULL AND NOT risk_high_cardinality
      AND recommended_member_index_policy='' AND evidence_hash=''
      AND result_code=''
    )
    OR
    (
      status='SUCCEEDED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND row_count IS NOT NULL AND non_null_count IS NOT NULL
      AND null_count IS NOT NULL AND distinct_count IS NOT NULL
      AND distinct_ratio IS NOT NULL
      AND recommended_member_index_policy IN ('FULL','EXACT_ONLY','NONE')
      AND evidence_hash ~ '^[0-9a-f]{64}$' AND result_code=''
    )
    OR
    (
      status='SKIPPED_POLICY' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND row_count IS NULL AND non_null_count IS NULL AND null_count IS NULL
      AND distinct_count IS NULL AND NOT distinct_overflow
      AND distinct_ratio IS NULL
      AND evidence_hash ~ '^[0-9a-f]{64}$'
      AND (
        (
          result_code='SENSITIVE_FIELD_PROFILE_SKIPPED'
          AND NOT risk_high_cardinality
          AND recommended_member_index_policy='NONE'
        )
        OR
        (
          result_code='IDENTIFIER_FIELD_PROFILE_SKIPPED'
          AND risk_high_cardinality
          AND recommended_member_index_policy='EXACT_ONLY'
        )
      )
    )
    OR
    (
      status='FAILED' AND attempt>0 AND started_at IS NOT NULL
      AND completed_at IS NOT NULL AND completed_at>=started_at
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND row_count IS NULL AND non_null_count IS NULL AND null_count IS NULL
      AND distinct_count IS NULL AND NOT distinct_overflow
      AND distinct_ratio IS NULL AND NOT risk_high_cardinality
      AND recommended_member_index_policy='NONE' AND evidence_hash=''
      AND btrim(result_code)<>''
    )
    OR
    (
      status='STALE' AND completed_at IS NOT NULL
      AND (started_at IS NULL OR completed_at>=started_at)
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND btrim(result_code)<>''
    )
  ),
  CONSTRAINT dimension_profile_jobs_identity_key UNIQUE(
    tenant_id,materialization_id,field_id,profile_version,policy_version
  ),
  CONSTRAINT dimension_profile_jobs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dimension_profile_jobs_claim_idx
  ON platform.dimension_profile_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  ) WHERE status IN ('QUEUED','RUNNING');

CREATE INDEX dimension_profile_jobs_field_time_idx
  ON platform.dimension_profile_jobs(
    tenant_id,dataset_version_id,field_id,created_at DESC,id
  );

CREATE INDEX dimension_profile_jobs_materialization_idx
  ON platform.dimension_profile_jobs(
    tenant_id,materialization_id,status,field_id
  );

CREATE OR REPLACE FUNCTION platform.enforce_dimension_profile_job_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '维度画像任务不可删除' USING ERRCODE='23514';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,
    NEW.schema_hash,NEW.materialization_id,NEW.materialization_snapshot_hash,
    NEW.expected_row_count,NEW.field_id,NEW.field_code,NEW.field_role,
    NEW.canonical_type,NEW.semantic_type,NEW.profile_version,NEW.policy_version,
    NEW.distinct_cap,NEW.high_ratio_threshold,NEW.high_ratio_min_non_null,
    NEW.timeout_seconds,NEW.work_mem_kb,NEW.temp_file_limit_kb,
    NEW.max_attempts,NEW.requested_by,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.dataset_version_id,
    OLD.schema_hash,OLD.materialization_id,OLD.materialization_snapshot_hash,
    OLD.expected_row_count,OLD.field_id,OLD.field_code,OLD.field_role,
    OLD.canonical_type,OLD.semantic_type,OLD.profile_version,OLD.policy_version,
    OLD.distinct_cap,OLD.high_ratio_threshold,OLD.high_ratio_min_non_null,
    OLD.timeout_seconds,OLD.work_mem_kb,OLD.temp_file_limit_kb,
    OLD.max_attempts,OLD.requested_by,OLD.created_at
  ) THEN
    RAISE EXCEPTION '维度画像任务身份与资源边界不可修改' USING ERRCODE='23514';
  END IF;

  IF OLD.status='QUEUED' AND NEW.status='RUNNING' THEN
    IF NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '维度画像 claim 必须推进 attempt' USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING' AND NEW.status='RUNNING' THEN
    IF NEW.attempt=OLD.attempt THEN
      IF NEW.lease_owner<>OLD.lease_owner
        OR NEW.lease_token IS DISTINCT FROM OLD.lease_token
        OR NEW.lease_expires_at<=OLD.lease_expires_at THEN
        RAISE EXCEPTION '维度画像 heartbeat 必须保持栅栏并延长租约'
          USING ERRCODE='23514';
      END IF;
    ELSIF OLD.lease_expires_at>now() OR NEW.attempt<>OLD.attempt+1 THEN
      RAISE EXCEPTION '只能重新认领已过期的维度画像租约'
        USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status='RUNNING'
    AND NEW.status IN (
      'QUEUED','SUCCEEDED','SKIPPED_POLICY','FAILED','STALE'
    ) THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '维度画像完成或重试不能改写 attempt'
        USING ERRCODE='23514';
    END IF;
  ELSIF OLD.status IN ('QUEUED','SUCCEEDED','SKIPPED_POLICY')
    AND NEW.status='STALE' THEN
    IF NEW.attempt<>OLD.attempt THEN
      RAISE EXCEPTION '维度画像失效不能改写 attempt' USING ERRCODE='23514';
    END IF;
  ELSE
    RAISE EXCEPTION '非法的维度画像状态转换：% -> %',OLD.status,NEW.status
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dimension_profile_jobs_enforce_transition
BEFORE UPDATE OR DELETE ON platform.dimension_profile_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_profile_job_transition();

CREATE TRIGGER dimension_profile_jobs_set_updated_at
BEFORE UPDATE ON platform.dimension_profile_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

-- temp_file_limit 是超级用户参数。画像 worker 不能获得该权限，因此由这个
-- 严格限幅、事务局部的 helper 设置资源预算。调用者只能收紧到画像允许的上限，
-- 不能借 SECURITY DEFINER 提升任意会话参数或延长查询时间。
CREATE OR REPLACE FUNCTION platform.apply_dimension_profile_resource_limits(
  selected_job_id uuid,
  selected_attempt integer,
  selected_lease_owner text,
  selected_lease_token uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  selected_timeout_seconds integer;
  selected_work_mem_kb integer;
  selected_temp_file_limit_kb integer;
BEGIN
  SELECT profile.timeout_seconds,profile.work_mem_kb,profile.temp_file_limit_kb
  INTO selected_timeout_seconds,selected_work_mem_kb,
    selected_temp_file_limit_kb
  FROM platform.dimension_profile_jobs AS profile
  WHERE profile.id=selected_job_id
    AND profile.tenant_id=platform.current_tenant_id()
    AND profile.status='RUNNING'
    AND profile.attempt=selected_attempt
    AND profile.lease_owner=selected_lease_owner
    AND profile.lease_token=selected_lease_token
    AND profile.lease_expires_at>now()
  FOR SHARE OF profile;
  IF NOT FOUND THEN
    RAISE EXCEPTION '维度画像资源预算必须绑定有效租约'
      USING ERRCODE='42501';
  END IF;
  IF selected_timeout_seconds NOT BETWEEN 1 AND 300
    OR selected_work_mem_kb NOT BETWEEN 64 AND 262144
    OR selected_temp_file_limit_kb NOT BETWEEN 1024 AND 1048576 THEN
    RAISE EXCEPTION '维度画像资源预算越界' USING ERRCODE='23514';
  END IF;
  PERFORM set_config(
    'statement_timeout',(selected_timeout_seconds*1000)::text,true
  );
  PERFORM set_config(
    'lock_timeout',least(selected_timeout_seconds*1000,5000)::text,true
  );
  PERFORM set_config('work_mem',selected_work_mem_kb::text||'kB',true);
  PERFORM set_config('temp_file_limit',selected_temp_file_limit_kb::text,true);
  PERFORM set_config('max_parallel_workers_per_gather','0',true);
  PERFORM set_config('enable_hashagg','off',true);
END
$$;

REVOKE ALL ON FUNCTION platform.apply_dimension_profile_resource_limits(
  uuid,integer,text,uuid
) FROM PUBLIC;

-- 参数全部由函数内的硬边界校验，函数只设置当前事务 LOCAL GUC 且没有数据访问。
-- 默认部署角色在迁移内获得最小权限；可配置 worker 角色由 migrate.sh 幂等授权。
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION
      platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)
      TO report_worker;
  END IF;
END
$$;

CREATE OR REPLACE FUNCTION platform.enqueue_dws_dimension_profiles(
  selected_tenant_id uuid,
  selected_dataset_id uuid,
  selected_dataset_version_id uuid,
  selected_materialization_id uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  selected_schema_hash text;
  selected_snapshot_hash text;
  selected_row_count bigint;
  selected_actor uuid;
BEGIN
  SELECT materialization.schema_hash,materialization.snapshot_hash,
    materialization.row_count,version.published_by
  INTO selected_schema_hash,selected_snapshot_hash,
    selected_row_count,selected_actor
  FROM platform.dataset_materializations AS materialization
  JOIN platform.dataset_versions AS version
    ON version.id=materialization.dataset_version_id
   AND version.dataset_id=materialization.dataset_id
   AND version.tenant_id=materialization.tenant_id
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id
   AND dataset.tenant_id=version.tenant_id
  WHERE materialization.id=selected_materialization_id
    AND materialization.tenant_id=selected_tenant_id
    AND materialization.dataset_id=selected_dataset_id
    AND materialization.dataset_version_id=selected_dataset_version_id
    AND materialization.layer='DWS'
    AND materialization.status='ACTIVE'
    AND materialization.schema_hash=version.schema_hash
    AND version.layer='DWS' AND version.status='PUBLISHED'
    AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
    AND dataset.current_published_version_id=version.id
    AND dataset.deleted_at IS NULL
  FOR SHARE OF materialization,version,dataset;

  IF NOT FOUND THEN
    RETURN;
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-dataset-profile:'||selected_tenant_id::text||
    ':'||selected_dataset_id::text,
    0
  ));

  -- 锁序统一为：精确物化行 -> 数据集锁 -> 字段锁 -> 画像/候选/维度行。
  -- 覆盖旧画像字段、既有维度字段和新版本待画像字段，随后才能 stale/update。
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-field-risk:'||selected_tenant_id::text||
    ':'||field_scope.dataset_version_id::text||':'||field_scope.field_id,
    0
  ))
  FROM (
    SELECT profile.dataset_version_id,profile.field_id
    FROM platform.dimension_profile_jobs AS profile
    WHERE profile.tenant_id=selected_tenant_id
      AND profile.dataset_id=selected_dataset_id
    UNION
    SELECT dimension.dataset_version_id,dimension.field_id
    FROM platform.semantic_dimensions AS dimension
    WHERE dimension.tenant_id=selected_tenant_id
      AND dimension.dataset_id=selected_dataset_id
      AND dimension.status<>'DEPRECATED'
    UNION
    SELECT selected_dataset_version_id,field.field_id
    FROM platform.dataset_fields AS field
    WHERE field.tenant_id=selected_tenant_id
      AND field.dataset_version_id=selected_dataset_version_id
      AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  ) AS field_scope
  ORDER BY field_scope.dataset_version_id,field_scope.field_id;

  UPDATE platform.dimension_profile_jobs
  SET status='STALE',result_code='MATERIALIZATION_SUPERSEDED',
      recommended_member_index_policy=CASE
        WHEN recommended_member_index_policy='' THEN 'NONE'
        ELSE recommended_member_index_policy
      END,
      lease_owner='',lease_token=NULL,lease_expires_at=NULL,
      completed_at=clock_timestamp()
  WHERE tenant_id=selected_tenant_id
    AND dataset_id=selected_dataset_id
    AND materialization_id<>selected_materialization_id
    AND status IN ('QUEUED','RUNNING','SUCCEEDED','SKIPPED_POLICY');

  -- 新 ACTIVE 物化尚无画像证据，旧 FULL 成员快照必须在同一事务立即失效。
  WITH changed AS (
    UPDATE platform.semantic_dimensions AS dimension
    SET member_index_policy='NONE',
        member_refresh_generation=NULL,
        member_count=NULL,
        member_refreshed_at=NULL,
        last_member_refresh_job_id=NULL,
        definition_hash=encode(public.digest(
          convert_to(
            concat_ws(E'\x1f',
              dimension.dataset_id::text,
              dimension.dataset_version_id::text,
              dimension.field_id,
              dimension.code::text,
              dimension.name,
              dimension.description,
              dimension.dimension_type,
              'NONE',
              dimension.high_cardinality::text,
              dimension.sensitive::text,
              dimension.status
            ),
            'UTF8'
          ),
          'sha256'
        ),'hex'),
        version=dimension.version+1,
        updated_by=selected_actor,
        updated_at=clock_timestamp()
    WHERE dimension.tenant_id=selected_tenant_id
      AND dimension.dataset_id=selected_dataset_id
      AND dimension.status='PUBLISHED'
      AND dimension.member_index_policy='FULL'
    RETURNING dimension.id,dimension.dataset_version_id,
      dimension.field_id,dimension.version-1 AS previous_version
  )
  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  )
  SELECT selected_tenant_id,selected_actor,
    'DIMENSION_ACTIVE_MATERIALIZATION_POLICY_TIGHTEN',
    'SEMANTIC_DIMENSION',changed.id::text,
    jsonb_build_object(
      'datasetVersionId',changed.dataset_version_id::text,
      'materializationId',selected_materialization_id::text,
      'fieldId',changed.field_id,
      'previousVersion',changed.previous_version,
      'policy','NONE'
    )
  FROM changed;

  INSERT INTO platform.dimension_profile_jobs(
    tenant_id,dataset_id,dataset_version_id,schema_hash,
    materialization_id,materialization_snapshot_hash,expected_row_count,
    field_id,field_code,field_role,canonical_type,semantic_type,
    profile_version,policy_version,distinct_cap,
    high_ratio_threshold,high_ratio_min_non_null,
    timeout_seconds,work_mem_kb,temp_file_limit_kb,requested_by
  )
  SELECT
    selected_tenant_id,selected_dataset_id,selected_dataset_version_id,
    selected_schema_hash,selected_materialization_id,selected_snapshot_hash,
    selected_row_count,field.field_id,field.field_code::text,field.field_role,
    field.canonical_type,field.semantic_type,
    'dws-dimension-profile-v1','dimension-member-policy-v1',
    100000,0.20000000,10000,60,16384,262144,selected_actor
  FROM platform.dataset_fields AS field
  WHERE field.tenant_id=selected_tenant_id
    AND field.dataset_version_id=selected_dataset_version_id
    AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  ON CONFLICT(
    tenant_id,materialization_id,field_id,profile_version,policy_version
  ) DO NOTHING;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dws_dimension_profiles(
  uuid,uuid,uuid,uuid
) FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.enqueue_active_dws_dimension_profiles()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWS' AND NEW.status='ACTIVE'
    AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'ACTIVE') THEN
    PERFORM platform.enqueue_dws_dimension_profiles(
      NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,NEW.id
    );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_active_dws_dimension_profiles() FROM PUBLIC;

-- PostgreSQL 按名称顺序执行同事件 trigger；00 前缀确保先取得治理锁，
-- 再由 v68 的 complete_dimension_survey 写候选，避免 candidate -> field 反序。
CREATE TRIGGER dataset_materializations_00_enqueue_dimension_profiles
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_active_dws_dimension_profiles();

-- v68 已让“批准敏感绑定”和“标签后来转为 ACTIVE SENSITIVITY”调用这个
-- 精确字段 helper。这里把同一 advisory lock 扩展到画像与候选：任一并发顺序
-- 最终都只能得到 NONE，解绑或降级标签不会自动放宽历史风险。
CREATE OR REPLACE FUNCTION platform.tighten_sensitive_field_dimensions(
  selected_tenant_id uuid,
  selected_dataset_id uuid,
  selected_dataset_version_id uuid,
  selected_field_id text,
  selected_actor uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-field-risk:'||selected_tenant_id::text||
    ':'||selected_dataset_version_id::text||':'||selected_field_id,
    0
  ));

  WITH changed AS (
    UPDATE platform.dimension_profile_jobs AS profile
    SET status='STALE',result_code='SENSITIVITY_POLICY_CHANGED'
    WHERE profile.tenant_id=selected_tenant_id
      AND profile.dataset_id=selected_dataset_id
      AND profile.dataset_version_id=selected_dataset_version_id
      AND profile.field_id=selected_field_id
      AND profile.status IN ('SUCCEEDED','SKIPPED_POLICY')
    RETURNING profile.id
  )
  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  )
  SELECT selected_tenant_id,selected_actor,
    'DIMENSION_PROFILE_SENSITIVITY_STALE','DIMENSION_PROFILE_JOB',
    changed.id::text,
    jsonb_build_object(
      'datasetVersionId',selected_dataset_version_id::text,
      'fieldId',selected_field_id,
      'policy','NONE'
    )
  FROM changed;

  WITH changed AS (
    UPDATE platform.dimension_survey_candidates AS candidate
    SET proposed_sensitive=true,proposed_member_index_policy='NONE',
        version=candidate.version+1,updated_by=selected_actor,
        updated_at=clock_timestamp()
    WHERE candidate.tenant_id=selected_tenant_id
      AND candidate.dataset_id=selected_dataset_id
      AND candidate.dataset_version_id=selected_dataset_version_id
      AND candidate.field_id=selected_field_id
      AND candidate.status='SUGGESTED'
      AND (
        NOT candidate.proposed_sensitive
        OR candidate.proposed_member_index_policy<>'NONE'
      )
    RETURNING candidate.id,candidate.version-1 AS previous_version
  )
  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  )
  SELECT selected_tenant_id,selected_actor,
    'DIMENSION_PROFILE_CANDIDATE_SENSITIVITY_TIGHTEN',
    'DIMENSION_SURVEY_CANDIDATE',changed.id::text,
    jsonb_build_object(
      'datasetVersionId',selected_dataset_version_id::text,
      'fieldId',selected_field_id,
      'previousVersion',changed.previous_version,
      'policy','NONE'
    )
  FROM changed;

  WITH changed AS (
    UPDATE platform.semantic_dimensions AS dimension
    SET sensitive=true,
        member_index_policy='NONE',
        member_refresh_generation=NULL,
        member_count=NULL,
        member_refreshed_at=NULL,
        last_member_refresh_job_id=NULL,
        definition_hash=encode(public.digest(
          convert_to(
            concat_ws(E'\x1f',
              dimension.dataset_id::text,
              dimension.dataset_version_id::text,
              dimension.field_id,
              dimension.code::text,
              dimension.name,
              dimension.description,
              dimension.dimension_type,
              'NONE',
              dimension.high_cardinality::text,
              'true',
              dimension.status
            ),
            'UTF8'
          ),
          'sha256'
        ),'hex'),
        version=dimension.version+1,
        updated_by=selected_actor,
        updated_at=clock_timestamp()
    WHERE dimension.tenant_id=selected_tenant_id
      AND dimension.dataset_id=selected_dataset_id
      AND dimension.dataset_version_id=selected_dataset_version_id
      AND dimension.field_id=selected_field_id
      AND dimension.status<>'DEPRECATED'
      AND (
        NOT dimension.sensitive
        OR dimension.member_index_policy<>'NONE'
      )
    RETURNING dimension.id,dimension.version-1 AS previous_version
  )
  INSERT INTO platform.audit_logs(
    tenant_id,actor_user_id,action,resource_type,resource_id,detail
  )
  SELECT selected_tenant_id,selected_actor,
    'SEMANTIC_DIMENSION_SENSITIVITY_TIGHTEN',
    'SEMANTIC_DIMENSION',changed.id::text,
    jsonb_build_object(
      'datasetVersionId',selected_dataset_version_id::text,
      'fieldId',selected_field_id,
      'previousVersion',changed.previous_version,
      'policy','NONE'
    )
  FROM changed;
END
$$;

REVOKE ALL ON FUNCTION platform.tighten_sensitive_field_dimensions(
  uuid,uuid,uuid,text,uuid
) FROM PUBLIC;

-- v68 的同名 BEFORE trigger 早于画像 trigger 执行。这里统一它的锁序：
-- 非 NONE 发布先锁精确 ACTIVE 物化，再取数据集锁和字段锁；NONE 只取字段锁。
-- 同时把历史人工/标签风险作为不可自动放宽的敏感分类下限。
CREATE OR REPLACE FUNCTION platform.guard_published_dimension_field_sensitivity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  field_is_sensitive boolean;
BEGIN
  IF NEW.status='PUBLISHED' THEN
    IF NEW.member_index_policy<>'NONE' THEN
      PERFORM 1
      FROM platform.dataset_materializations AS materialization
      WHERE materialization.tenant_id=NEW.tenant_id
        AND materialization.dataset_id=NEW.dataset_id
        AND materialization.dataset_version_id=NEW.dataset_version_id
        AND materialization.layer='DWS'
        AND materialization.status='ACTIVE'
      FOR SHARE OF materialization;
      IF NOT FOUND THEN
        RAISE EXCEPTION '正式维度的非 NONE 策略必须绑定 ACTIVE DWS 物化'
          USING ERRCODE='23514';
      END IF;
      PERFORM pg_advisory_xact_lock(hashtextextended(
        'dimension-dataset-profile:'||NEW.tenant_id::text||
        ':'||NEW.dataset_id::text,
        0
      ));
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended(
      'dimension-field-risk:'||NEW.tenant_id::text||
      ':'||NEW.dataset_version_id::text||':'||NEW.field_id,
      0
    ));

    SELECT
      EXISTS(
        SELECT 1
        FROM platform.asset_tag_bindings AS binding
        JOIN platform.semantic_tags AS tag
          ON tag.id=binding.tag_id
         AND tag.tenant_id=binding.tenant_id
         AND tag.category='SENSITIVITY'
        WHERE binding.tenant_id=NEW.tenant_id
          AND binding.asset_type='DATASET_FIELD'
          AND binding.dataset_id=NEW.dataset_id
          AND binding.dataset_version_id=NEW.dataset_version_id
          AND binding.dataset_field_id=NEW.field_id
          AND binding.status='APPROVED'
      )
      OR EXISTS(
        SELECT 1
        FROM platform.semantic_dimensions AS prior_dimension
        WHERE prior_dimension.tenant_id=NEW.tenant_id
          AND prior_dimension.dataset_id=NEW.dataset_id
          AND prior_dimension.dataset_version_id=NEW.dataset_version_id
          AND prior_dimension.field_id=NEW.field_id
          AND prior_dimension.status<>'DEPRECATED'
          AND prior_dimension.sensitive
      )
      OR EXISTS(
        SELECT 1
        FROM platform.dimension_survey_candidates AS prior_candidate
        WHERE prior_candidate.tenant_id=NEW.tenant_id
          AND prior_candidate.dataset_id=NEW.dataset_id
          AND prior_candidate.dataset_version_id=NEW.dataset_version_id
          AND prior_candidate.field_id=NEW.field_id
          AND (
            prior_candidate.risk_sensitive
            OR prior_candidate.proposed_sensitive
          )
      )
      OR EXISTS(
        SELECT 1
        FROM platform.dimension_profile_jobs AS prior_profile
        WHERE prior_profile.tenant_id=NEW.tenant_id
          AND prior_profile.dataset_id=NEW.dataset_id
          AND prior_profile.dataset_version_id=NEW.dataset_version_id
          AND prior_profile.field_id=NEW.field_id
          AND prior_profile.result_code='SENSITIVE_FIELD_PROFILE_SKIPPED'
      )
    INTO field_is_sensitive;

    IF field_is_sensitive
      AND (NOT NEW.sensitive OR NEW.member_index_policy<>'NONE') THEN
      RAISE EXCEPTION '历史敏感分类字段必须按敏感 NONE 策略发布'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.guard_published_dimension_field_sensitivity() FROM PUBLIC;

-- 直接创建/编辑正式维度不能绕过画像。NONE 是唯一不要求成功画像的安全策略；
-- FULL 与 EXACT_ONLY 都必须绑定当前 ACTIVE 物化上的成功画像。
CREATE OR REPLACE FUNCTION platform.guard_published_dimension_profile_policy()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  field_is_sensitive boolean;
  selected_profile record;
BEGIN
  IF NEW.status<>'PUBLISHED' OR NEW.member_index_policy='NONE' THEN
    RETURN NEW;
  END IF;

  PERFORM 1
  FROM platform.dataset_materializations AS materialization
  WHERE materialization.tenant_id=NEW.tenant_id
    AND materialization.dataset_id=NEW.dataset_id
    AND materialization.dataset_version_id=NEW.dataset_version_id
    AND materialization.layer='DWS'
    AND materialization.status='ACTIVE'
  FOR SHARE OF materialization;
  IF NOT FOUND THEN
    RAISE EXCEPTION '正式维度的非 NONE 策略必须绑定当前 ACTIVE DWS 物化'
      USING ERRCODE='23514';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-dataset-profile:'||NEW.tenant_id::text||
    ':'||NEW.dataset_id::text,
    0
  ));
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-field-risk:'||NEW.tenant_id::text||
    ':'||NEW.dataset_version_id::text||':'||NEW.field_id,
    0
  ));

  SELECT
    EXISTS(
      SELECT 1
      FROM platform.asset_tag_bindings AS binding
      JOIN platform.semantic_tags AS tag
        ON tag.id=binding.tag_id
       AND tag.tenant_id=binding.tenant_id
       AND tag.category='SENSITIVITY'
      WHERE binding.tenant_id=NEW.tenant_id
        AND binding.asset_type='DATASET_FIELD'
        AND binding.dataset_id=NEW.dataset_id
        AND binding.dataset_version_id=NEW.dataset_version_id
        AND binding.dataset_field_id=NEW.field_id
        AND binding.status='APPROVED'
    )
    OR EXISTS(
      SELECT 1
      FROM platform.semantic_dimensions AS prior_dimension
      WHERE prior_dimension.tenant_id=NEW.tenant_id
        AND prior_dimension.dataset_id=NEW.dataset_id
        AND prior_dimension.dataset_version_id=NEW.dataset_version_id
        AND prior_dimension.field_id=NEW.field_id
        AND prior_dimension.status<>'DEPRECATED'
        AND prior_dimension.sensitive
    )
    OR EXISTS(
      SELECT 1
      FROM platform.dimension_survey_candidates AS prior_candidate
      WHERE prior_candidate.tenant_id=NEW.tenant_id
        AND prior_candidate.dataset_id=NEW.dataset_id
        AND prior_candidate.dataset_version_id=NEW.dataset_version_id
        AND prior_candidate.field_id=NEW.field_id
        AND (
          prior_candidate.risk_sensitive
          OR prior_candidate.proposed_sensitive
        )
    )
    OR EXISTS(
      SELECT 1
      FROM platform.dimension_profile_jobs AS prior_profile
      WHERE prior_profile.tenant_id=NEW.tenant_id
        AND prior_profile.dataset_id=NEW.dataset_id
        AND prior_profile.dataset_version_id=NEW.dataset_version_id
        AND prior_profile.field_id=NEW.field_id
        AND prior_profile.result_code='SENSITIVE_FIELD_PROFILE_SKIPPED'
    )
  INTO field_is_sensitive;

  IF field_is_sensitive OR NEW.sensitive THEN
    RAISE EXCEPTION '敏感字段只能使用 NONE 成员索引策略'
      USING ERRCODE='23514';
  END IF;

  SELECT profile.id,profile.status,profile.risk_high_cardinality,
    profile.recommended_member_index_policy
  INTO selected_profile
  FROM platform.dataset_materializations AS materialization
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=materialization.tenant_id
   AND version.dataset_id=materialization.dataset_id
   AND version.id=materialization.dataset_version_id
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=version.tenant_id
   AND dataset.id=version.dataset_id
  JOIN platform.dimension_profile_jobs AS profile
    ON profile.tenant_id=materialization.tenant_id
   AND profile.dataset_id=materialization.dataset_id
   AND profile.dataset_version_id=materialization.dataset_version_id
   AND profile.materialization_id=materialization.id
   AND profile.schema_hash=materialization.schema_hash
   AND profile.materialization_snapshot_hash=materialization.snapshot_hash
   AND profile.field_id=NEW.field_id
   AND profile.profile_version='dws-dimension-profile-v1'
   AND profile.policy_version='dimension-member-policy-v1'
   AND profile.status IN ('SUCCEEDED','SKIPPED_POLICY')
  WHERE materialization.tenant_id=NEW.tenant_id
    AND materialization.dataset_id=NEW.dataset_id
    AND materialization.dataset_version_id=NEW.dataset_version_id
    AND materialization.layer='DWS'
    AND materialization.status='ACTIVE'
    AND materialization.schema_hash=version.schema_hash
    AND version.layer='DWS'
    AND version.status='PUBLISHED'
    AND dataset.layer='DWS'
    AND dataset.status='PUBLISHED'
    AND dataset.current_published_version_id=version.id
    AND dataset.deleted_at IS NULL
  ORDER BY profile.completed_at DESC,profile.id
  LIMIT 1
  FOR SHARE OF materialization,profile,version,dataset;

  IF NOT FOUND THEN
    RAISE EXCEPTION '正式维度的非 NONE 策略必须等待当前 DWS 画像成功'
      USING ERRCODE='23514';
  END IF;
  IF NEW.member_index_policy='FULL'
    AND (
      selected_profile.recommended_member_index_policy<>'FULL'
      OR selected_profile.status<>'SUCCEEDED'
    ) THEN
    RAISE EXCEPTION 'FULL 策略超过当前 DWS 画像允许的风险下限'
      USING ERRCODE='23514';
  END IF;
  IF NEW.member_index_policy='EXACT_ONLY'
    AND selected_profile.recommended_member_index_policy NOT IN ('FULL','EXACT_ONLY') THEN
    RAISE EXCEPTION 'EXACT_ONLY 策略超过当前 DWS 画像允许的风险下限'
      USING ERRCODE='23514';
  END IF;
  IF selected_profile.risk_high_cardinality AND NOT NEW.high_cardinality THEN
    RAISE EXCEPTION '实测高基数字段必须保留高基数风险标记'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.guard_published_dimension_profile_policy()
  FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_guard_profile_policy
BEFORE INSERT OR UPDATE OF
  status,member_index_policy,high_cardinality,sensitive
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.guard_published_dimension_profile_policy();

-- 成员刷新入队也必须复核当前发布版本与当前 ACTIVE 物化的成功 FULL 画像，
-- 不能依赖曾经通过的维度写入门禁。
CREATE OR REPLACE FUNCTION platform.enforce_dimension_member_refresh_source()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  current_dimension_version bigint;
  current_dimension_status text;
  current_policy text;
  current_field_code text;
  current_sensitive boolean;
BEGIN
  IF NEW.member_index_policy='FULL' THEN
    PERFORM 1
    FROM platform.dataset_materializations AS materialization
    WHERE materialization.id=NEW.materialization_id
      AND materialization.tenant_id=NEW.tenant_id
      AND materialization.dataset_id=NEW.dataset_id
      AND materialization.dataset_version_id=NEW.dataset_version_id
      AND materialization.layer='DWS'
      AND materialization.status='ACTIVE'
    FOR SHARE OF materialization;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'FULL 成员刷新必须固定精确的 ACTIVE DWS 物化'
        USING ERRCODE='23514';
    END IF;
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-dataset-profile:'||NEW.tenant_id::text||
    ':'||NEW.dataset_id::text,
    0
  ));
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-field-risk:'||NEW.tenant_id::text||
    ':'||NEW.dataset_version_id::text||':'||NEW.field_id,
    0
  ));

  SELECT dimension.version,dimension.status,dimension.member_index_policy,
    field.field_code::text,dimension.sensitive
  INTO current_dimension_version,current_dimension_status,current_policy,
    current_field_code,current_sensitive
  FROM platform.semantic_dimensions AS dimension
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=dimension.tenant_id
   AND version.id=dimension.dataset_version_id
   AND version.dataset_id=dimension.dataset_id
   AND version.layer='DWS'
   AND version.status='PUBLISHED'
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=version.tenant_id
   AND dataset.id=version.dataset_id
   AND dataset.layer='DWS'
   AND dataset.status='PUBLISHED'
   AND dataset.current_published_version_id=version.id
   AND dataset.deleted_at IS NULL
  JOIN platform.dataset_fields AS field
    ON field.tenant_id=dimension.tenant_id
   AND field.dataset_version_id=dimension.dataset_version_id
   AND field.field_id=dimension.field_id
   AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  WHERE dimension.id=NEW.dimension_id
    AND dimension.dataset_id=NEW.dataset_id
    AND dimension.dataset_version_id=NEW.dataset_version_id
    AND dimension.field_id=NEW.field_id
    AND dimension.tenant_id=NEW.tenant_id
  FOR SHARE OF dimension,version,dataset;

  IF NOT FOUND
    OR current_dimension_version<>NEW.dimension_version
    OR current_dimension_status<>'PUBLISHED'
    OR current_policy<>NEW.member_index_policy
    OR current_field_code<>NEW.field_code THEN
    RAISE EXCEPTION '成员刷新任务与当前已发布维度不一致'
      USING ERRCODE='23514';
  END IF;

  IF NEW.member_index_policy='FULL' THEN
    IF current_sensitive
      OR EXISTS(
        SELECT 1
        FROM platform.asset_tag_bindings AS binding
        JOIN platform.semantic_tags AS tag
          ON tag.id=binding.tag_id
         AND tag.tenant_id=binding.tenant_id
         AND tag.category='SENSITIVITY'
        WHERE binding.tenant_id=NEW.tenant_id
          AND binding.asset_type='DATASET_FIELD'
          AND binding.dataset_id=NEW.dataset_id
          AND binding.dataset_version_id=NEW.dataset_version_id
          AND binding.dataset_field_id=NEW.field_id
          AND binding.status='APPROVED'
      )
      OR EXISTS(
        SELECT 1
        FROM platform.dimension_survey_candidates AS candidate
        WHERE candidate.tenant_id=NEW.tenant_id
          AND candidate.dataset_id=NEW.dataset_id
          AND candidate.dataset_version_id=NEW.dataset_version_id
          AND candidate.field_id=NEW.field_id
          AND (candidate.risk_sensitive OR candidate.proposed_sensitive)
      )
      OR EXISTS(
        SELECT 1
        FROM platform.dimension_profile_jobs AS prior_profile
        WHERE prior_profile.tenant_id=NEW.tenant_id
          AND prior_profile.dataset_id=NEW.dataset_id
          AND prior_profile.dataset_version_id=NEW.dataset_version_id
          AND prior_profile.field_id=NEW.field_id
          AND prior_profile.result_code='SENSITIVE_FIELD_PROFILE_SKIPPED'
      ) THEN
      RAISE EXCEPTION '敏感维度禁止 FULL 成员扫描'
        USING ERRCODE='23514';
    END IF;

    PERFORM 1
    FROM platform.dataset_materializations AS materialization
    JOIN platform.dimension_profile_jobs AS profile
      ON profile.tenant_id=materialization.tenant_id
     AND profile.dataset_id=materialization.dataset_id
     AND profile.dataset_version_id=materialization.dataset_version_id
     AND profile.materialization_id=materialization.id
     AND profile.schema_hash=materialization.schema_hash
     AND profile.materialization_snapshot_hash=materialization.snapshot_hash
     AND profile.field_id=NEW.field_id
     AND profile.profile_version='dws-dimension-profile-v1'
     AND profile.policy_version='dimension-member-policy-v1'
     AND profile.status='SUCCEEDED'
     AND profile.recommended_member_index_policy='FULL'
    WHERE materialization.id=NEW.materialization_id
      AND materialization.dataset_id=NEW.dataset_id
      AND materialization.dataset_version_id=NEW.dataset_version_id
      AND materialization.tenant_id=NEW.tenant_id
      AND materialization.layer='DWS'
      AND materialization.status='ACTIVE'
    FOR SHARE OF materialization,profile;
    IF NOT FOUND THEN
      RAISE EXCEPTION
        'FULL 成员刷新必须固定当前成功画像允许的 ACTIVE DWS 物化'
        USING ERRCODE='23514';
    END IF;
  ELSIF NEW.materialization_id IS NOT NULL THEN
    RAISE EXCEPTION '非 FULL 策略不能携带物化身份' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dimension_member_refresh_source()
  FROM PUBLIC;

-- 别名只能绑定当前可公开枚举的 ACTIVE 成员快照；直接 SQL 不能把历史成员
-- 重新挂回别名。公开读取还会重复相同门禁，以覆盖并发物化切换。
CREATE OR REPLACE FUNCTION platform.enforce_dimension_member_alias_owner()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM 1
  FROM platform.dimension_members AS member
  JOIN platform.semantic_dimensions AS dimension
    ON dimension.tenant_id=member.tenant_id
   AND dimension.id=member.dimension_id
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=dimension.tenant_id
   AND version.id=dimension.dataset_version_id
   AND version.dataset_id=dimension.dataset_id
   AND version.layer='DWS'
   AND version.status='PUBLISHED'
  JOIN platform.datasets AS dataset
    ON dataset.tenant_id=version.tenant_id
   AND dataset.id=version.dataset_id
   AND dataset.layer='DWS'
   AND dataset.status='PUBLISHED'
   AND dataset.current_published_version_id=version.id
   AND dataset.deleted_at IS NULL
  JOIN platform.dataset_materializations AS materialization
    ON materialization.tenant_id=dimension.tenant_id
   AND materialization.dataset_id=dimension.dataset_id
   AND materialization.dataset_version_id=dimension.dataset_version_id
   AND materialization.layer='DWS'
   AND materialization.status='ACTIVE'
   AND materialization.schema_hash=version.schema_hash
  JOIN platform.dimension_profile_jobs AS profile
    ON profile.tenant_id=materialization.tenant_id
   AND profile.dataset_id=materialization.dataset_id
   AND profile.dataset_version_id=materialization.dataset_version_id
   AND profile.materialization_id=materialization.id
   AND profile.schema_hash=materialization.schema_hash
   AND profile.materialization_snapshot_hash=materialization.snapshot_hash
   AND profile.field_id=dimension.field_id
   AND profile.profile_version='dws-dimension-profile-v1'
   AND profile.policy_version='dimension-member-policy-v1'
   AND profile.status='SUCCEEDED'
   AND profile.recommended_member_index_policy='FULL'
  JOIN platform.dimension_member_refresh_jobs AS refresh_job
    ON refresh_job.tenant_id=dimension.tenant_id
   AND refresh_job.dimension_id=dimension.id
   AND refresh_job.id=dimension.last_member_refresh_job_id
   AND refresh_job.materialization_id=materialization.id
   AND refresh_job.refresh_generation=dimension.member_refresh_generation
   AND refresh_job.dimension_version=dimension.version
   AND refresh_job.status='SUCCEEDED'
  WHERE member.id=NEW.dimension_member_id
    AND member.dimension_id=NEW.dimension_id
    AND member.tenant_id=NEW.tenant_id
    AND member.status='ACTIVE'
    AND member.refresh_generation=dimension.member_refresh_generation
    AND member.last_refresh_job_id=refresh_job.id
    AND dimension.status='PUBLISHED'
    AND dimension.member_index_policy='FULL'
    AND NOT dimension.sensitive;
  IF NOT FOUND THEN
    RAISE EXCEPTION '维度成员别名只能绑定当前可用的 ACTIVE 成员快照'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dimension_member_alias_owner()
  FROM PUBLIC;

DROP TRIGGER dimension_member_aliases_enforce_owner
  ON platform.dimension_member_aliases;
CREATE TRIGGER dimension_member_aliases_enforce_owner
BEFORE INSERT OR UPDATE ON platform.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_member_alias_owner();

-- 当前 ACTIVE DWS 的历史数据回填画像任务；只登记，不在迁移事务扫描业务表。
DO $$
DECLARE
  materialization_record record;
BEGIN
  FOR materialization_record IN
    SELECT tenant_id,dataset_id,dataset_version_id,id
    FROM platform.dataset_materializations
    WHERE layer='DWS' AND status='ACTIVE'
    ORDER BY tenant_id,dataset_id
  LOOP
    PERFORM platform.enqueue_dws_dimension_profiles(
      materialization_record.tenant_id,
      materialization_record.dataset_id,
      materialization_record.dataset_version_id,
      materialization_record.id
    );
  END LOOP;
END
$$;

-- 直接 SQL 在行级 BEFORE trigger 之前已经取得目标行锁。为使数据库角色绕过
-- Go 服务时仍无 row -> field 反序，所有语义治理写语句先取得租户级写门，
-- 再进入精确的 materialization -> dataset -> field -> row 锁序。
CREATE OR REPLACE FUNCTION platform.lock_semantic_governance_write_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'semantic-governance-write:'||platform.current_tenant_id()::text,
    0
  ));
  RETURN NULL;
END
$$;

REVOKE ALL ON FUNCTION platform.lock_semantic_governance_write_scope()
  FROM PUBLIC;

CREATE TRIGGER dataset_materializations_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dataset_materializations
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER semantic_dimensions_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.semantic_dimensions
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_survey_candidates_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dimension_survey_candidates
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_profile_jobs_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dimension_profile_jobs
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_member_refresh_jobs_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dimension_member_refresh_jobs
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_members_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dimension_members
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_member_aliases_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.dimension_member_aliases
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER asset_tag_bindings_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.asset_tag_bindings
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER semantic_tags_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.semantic_tags
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER semantic_tag_aliases_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE ON platform.semantic_tag_aliases
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

CREATE TRIGGER dimension_metric_compatibility_00_lock_governance_write
BEFORE INSERT OR UPDATE OR DELETE
ON platform.dimension_metric_compatibility
FOR EACH STATEMENT EXECUTE FUNCTION
  platform.lock_semantic_governance_write_scope();

ALTER TABLE platform.dimension_profile_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_profile_jobs FORCE ROW LEVEL SECURITY;

CREATE POLICY dimension_profile_jobs_tenant_isolation
  ON platform.dimension_profile_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 画像是 worker 生成的治理证据，应用角色只能读取，不能伪造成功状态。
-- 部署脚本在通用 platform DML 授权之后还会按可配置 app role 重复收回。
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    REVOKE INSERT,UPDATE,DELETE
      ON platform.dimension_profile_jobs FROM report_app;
  END IF;
END
$$;

COMMENT ON TABLE platform.dimension_profile_jobs IS
  '固定 ACTIVE DWS 物化与字段的异步画像；只保存聚合计数和策略结论，不保存业务值';
COMMENT ON COLUMN platform.dimension_profile_jobs.distinct_count IS
  '不超过 distinct_cap 的精确值；distinct_overflow=true 时表示实际 NDV 大于该上限';
COMMENT ON COLUMN platform.dimension_profile_jobs.evidence_hash IS
  '画像身份、阈值、聚合计数和策略结论的规范 SHA-256，不包含样本值';
