-- 移除连接测试收据的 30 分钟 TTL。成功测试永久绑定精确配置版本+摘要：
-- 配置未变化时不要求也不触发重复测试；任何连接配置变化都会生成新草稿版本，
-- 旧证明因版本/摘要不匹配自然失效，发布前必须重新测试。

-- 1) 完成函数：不再生成 expires_at，也不再回写 test_expires_at。
CREATE OR REPLACE FUNCTION platform.complete_data_source_connection_test(
  p_job_id uuid,
  p_lease_token uuid,
  p_server_version text,
  p_latency_ms bigint
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
  v_job platform.data_source_connection_test_jobs%ROWTYPE;
  v_data_source_id uuid;
  v_source_draft_version_id uuid;
  v_source_deleted_at timestamptz;
  v_source_found boolean := false;
  v_draft_hash text;
  v_completed_at timestamptz := clock_timestamp();
  v_server_version text;
  v_executor_id text;
BEGIN
  IF v_tenant_id IS NULL THEN
    RETURN false;
  END IF;

  -- All source/job mutations use source -> job row order. Data-source edits
  -- already hold the source row before their stale-job trigger runs; taking the
  -- job first here would deadlock with a concurrent draft edit.
  SELECT job.data_source_id
  INTO v_data_source_id
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.id=p_job_id AND job.tenant_id=v_tenant_id;
  IF NOT FOUND THEN
    RETURN false;
  END IF;

  SELECT source.current_draft_version_id,source.deleted_at
  INTO v_source_draft_version_id,v_source_deleted_at
  FROM platform.data_sources AS source
  WHERE source.id=v_data_source_id AND source.tenant_id=v_tenant_id
  FOR UPDATE;
  v_source_found := FOUND;

  SELECT job.*
  INTO v_job
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.id=p_job_id AND job.tenant_id=v_tenant_id
  FOR UPDATE;

  IF NOT FOUND
    OR v_job.status<>'RUNNING'
    OR v_job.lease_token IS DISTINCT FROM p_lease_token
    OR v_job.lease_expires_at<=v_completed_at THEN
    RETURN false;
  END IF;

  SELECT version.config_hash
  INTO v_draft_hash
  FROM platform.data_source_versions AS version
  WHERE version.id=v_source_draft_version_id
    AND version.data_source_id=v_job.data_source_id
    AND version.tenant_id=v_job.tenant_id;

  IF NOT v_source_found
    OR v_source_deleted_at IS NOT NULL
    OR v_source_draft_version_id IS DISTINCT FROM v_job.data_source_version_id
    OR v_draft_hash IS DISTINCT FROM v_job.config_hash THEN
    UPDATE platform.data_source_connection_test_jobs
    SET status='CANCELLED',error_code='VERSION_CHANGED',
        error_message='数据源配置已变化，请重新测试',
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=v_completed_at,updated_at=v_completed_at
    WHERE id=v_job.id;
    RETURN false;
  END IF;

  v_server_version := left(
    regexp_replace(COALESCE(p_server_version,''),'[[:cntrl:]]','','g'),
    256
  );
  v_executor_id := left(session_user||':'||v_job.lease_owner,128);

  UPDATE platform.data_source_connection_test_jobs
  SET status='SUCCEEDED',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
      error_code='',error_message='',completed_at=v_completed_at,updated_at=v_completed_at
  WHERE id=v_job.id;

  INSERT INTO platform.data_source_connection_test_attestations(
    tenant_id,connection_test_job_id,data_source_id,data_source_version_id,
    config_hash,executor_id,server_version,latency_ms,
    started_at,completed_at
  )
  VALUES(
    v_job.tenant_id,v_job.id,v_job.data_source_id,v_job.data_source_version_id,
    v_job.config_hash,v_executor_id,v_server_version,
    LEAST(GREATEST(COALESCE(p_latency_ms,0),0),900000),
    GREATEST(v_job.started_at,v_completed_at-interval '15 minutes'),
    v_completed_at
  );

  UPDATE platform.data_sources AS source
  SET validation_status='PASSED',last_tested_at=v_completed_at,
      last_tested_version_id=v_job.data_source_version_id,
      last_tested_config_hash=v_job.config_hash,
      status=CASE WHEN source.current_published_version_id IS NULL
        THEN 'DRAFT'::platform.data_source_status ELSE source.status END,
      last_error=CASE WHEN source.current_published_version_id IS NULL
        THEN NULL ELSE source.last_error END
  WHERE source.id=v_job.data_source_id
    AND source.tenant_id=v_job.tenant_id
    AND source.current_draft_version_id=v_job.data_source_version_id;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION platform.complete_data_source_connection_test(uuid,uuid,text,bigint)
  FROM PUBLIC;

-- 2) 失败函数：不再清空 test_expires_at（列即将删除）。
CREATE OR REPLACE FUNCTION platform.fail_data_source_connection_test(
  p_job_id uuid,
  p_lease_token uuid,
  p_error_code text,
  p_retryable boolean DEFAULT false
)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
  v_job platform.data_source_connection_test_jobs%ROWTYPE;
  v_data_source_id uuid;
  v_source_draft_version_id uuid;
  v_source_deleted_at timestamptz;
  v_source_found boolean := false;
  v_draft_hash text;
  v_now timestamptz := clock_timestamp();
  v_error_code text := upper(btrim(COALESCE(p_error_code,'')));
  v_error_message text;
  v_next_status text;
BEGIN
  IF v_tenant_id IS NULL THEN RETURN ''; END IF;

  SELECT job.data_source_id INTO v_data_source_id
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.id=p_job_id AND job.tenant_id=v_tenant_id;
  IF NOT FOUND THEN RETURN ''; END IF;

  SELECT source.current_draft_version_id,source.deleted_at
  INTO v_source_draft_version_id,v_source_deleted_at
  FROM platform.data_sources AS source
  WHERE source.id=v_data_source_id AND source.tenant_id=v_tenant_id
  FOR UPDATE;
  v_source_found := FOUND;

  SELECT job.* INTO v_job
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.id=p_job_id AND job.tenant_id=v_tenant_id
  FOR UPDATE;

  IF NOT FOUND OR v_job.status<>'RUNNING'
    OR v_job.lease_token IS DISTINCT FROM p_lease_token
    OR v_job.lease_expires_at<=v_now THEN RETURN ''; END IF;

  IF v_error_code NOT IN (
    'CONNECTION_TIMEOUT','CONNECTION_FAILED','CONNECTION_AUTH_FAILED',
    'DATABASE_NOT_FOUND','CONNECTION_DNS_FAILED','CONNECTION_REFUSED',
    'NETWORK_UNREACHABLE','ADDRESS_RESOLUTION_FAILED','ADDRESS_UNREACHABLE',
    'PORT_REFUSED','PORT_TIMEOUT','DATABASE_HANDSHAKE_TIMEOUT',
    'CREDENTIAL_UNAVAILABLE','SOURCE_UNAVAILABLE','FILE_UNAVAILABLE'
  ) THEN
    v_error_code := 'CONNECTION_FAILED';
  END IF;
  v_error_message := CASE v_error_code
    WHEN 'ADDRESS_RESOLUTION_FAILED' THEN '地址检查失败：Host 无法解析，请确认域名或容器内 DNS 配置'
    WHEN 'ADDRESS_UNREACHABLE' THEN '地址检查失败：目标地址或路由不可达，请检查出站白名单和网络路由'
    WHEN 'PORT_REFUSED' THEN '地址检查已通过，但目标端口拒绝连接；请确认监听端口和端口映射'
    WHEN 'PORT_TIMEOUT' THEN '地址检查已通过，但目标端口连接超时；请检查防火墙、安全组和网络 ACL'
    WHEN 'DATABASE_NOT_FOUND' THEN '地址和端口已通过，但数据库、Oracle Service Name 或 SID 不存在'
    WHEN 'DATABASE_HANDSHAKE_TIMEOUT' THEN '地址和端口已通过，但数据库握手超时；请检查监听协议、服务名和 TLS 要求'
    WHEN 'CONNECTION_AUTH_FAILED' THEN '地址、端口和数据库/服务名已通过，但用户名或密码认证失败'
    WHEN 'CONNECTION_DNS_FAILED' THEN 'Host 无法解析，请检查域名或改用可路由的内网地址'
    WHEN 'CONNECTION_REFUSED' THEN '目标地址拒绝连接，请检查端口、数据库服务、防火墙和端口映射'
    WHEN 'NETWORK_UNREACHABLE' THEN '目标网络不可达，请检查路由、安全组、防火墙和出站策略'
    WHEN 'CONNECTION_TIMEOUT' THEN '连接测试超时，请检查网络、防火墙和目标地址'
    WHEN 'CREDENTIAL_UNAVAILABLE' THEN '连接凭据不可用，请更新配置后重试'
    WHEN 'SOURCE_UNAVAILABLE' THEN '数据源暂时不可用，请稍后重试'
    WHEN 'FILE_UNAVAILABLE' THEN '文件版本不可读取，请重新上传后重试'
    ELSE '连接测试失败，请按地址、端口、数据库/服务名和认证顺序检查'
  END;

  SELECT version.config_hash INTO v_draft_hash
  FROM platform.data_source_versions AS version
  WHERE version.id=v_source_draft_version_id
    AND version.data_source_id=v_job.data_source_id
    AND version.tenant_id=v_job.tenant_id;

  IF NOT v_source_found OR v_source_deleted_at IS NOT NULL
    OR v_source_draft_version_id IS DISTINCT FROM v_job.data_source_version_id
    OR v_draft_hash IS DISTINCT FROM v_job.config_hash THEN
    UPDATE platform.data_source_connection_test_jobs
    SET status='CANCELLED',error_code='VERSION_CHANGED',
        error_message='数据源配置已变化，请重新测试',
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=v_now,updated_at=v_now
    WHERE id=v_job.id;
    RETURN 'CANCELLED';
  END IF;

  IF COALESCE(p_retryable,false) AND v_job.attempt<v_job.max_attempts THEN
    v_next_status := 'QUEUED';
    UPDATE platform.data_source_connection_test_jobs
    SET status='QUEUED',next_attempt_at=v_now+interval '5 seconds',
        error_code=v_error_code,error_message=v_error_message,
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=v_now
    WHERE id=v_job.id;
  ELSE
    v_next_status := 'FAILED';
    UPDATE platform.data_source_connection_test_jobs
    SET status='FAILED',error_code=v_error_code,error_message=v_error_message,
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=v_now,updated_at=v_now
    WHERE id=v_job.id;

    UPDATE platform.data_sources AS source
    SET validation_status='FAILED',last_tested_at=v_now,
        last_tested_version_id=v_job.data_source_version_id,
        last_tested_config_hash=v_job.config_hash,
        status=CASE WHEN source.current_published_version_id IS NULL
          THEN 'ERROR'::platform.data_source_status ELSE source.status END,
        last_error=CASE WHEN source.current_published_version_id IS NULL
          THEN 'connection test failed' ELSE source.last_error END
    WHERE source.id=v_job.data_source_id
      AND source.tenant_id=v_job.tenant_id
      AND source.current_draft_version_id=v_job.data_source_version_id;
  END IF;
  RETURN v_next_status;
END
$$;

REVOKE ALL ON FUNCTION platform.fail_data_source_connection_test(uuid,uuid,text,boolean)
  FROM PUBLIC;

-- 3) 领取函数：租约耗尽的终态回写不再触碰 test_expires_at。
CREATE OR REPLACE FUNCTION platform.claim_data_source_connection_test(
  p_worker_id text,
  p_lease_seconds integer
)
RETURNS TABLE(
  job_id uuid,
  tenant_id uuid,
  data_source_id uuid,
  data_source_version_id uuid,
  config_hash text,
  source_type text,
  config jsonb,
  secret_ref text,
  file_asset_id text,
  file_version_id text,
  max_excel_file_bytes bigint,
  lease_token uuid,
  attempt integer,
  max_attempts integer
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
  v_job_id uuid;
  v_lease_token uuid := gen_random_uuid();
  v_exhausted record;
  v_stale record;
  v_updated integer;
BEGIN
  IF v_tenant_id IS NULL
    OR btrim(COALESCE(p_worker_id,''))=''
    OR length(p_worker_id)>128
    OR p_lease_seconds NOT BETWEEN 10 AND 300 THEN
    RAISE EXCEPTION '连接测试领取参数无效' USING ERRCODE='22023';
  END IF;

  -- Expired terminal attempts also follow source -> job. The prior writable
  -- CTE locked jobs first and only then updated data_sources, which could
  -- deadlock with a draft edit whose stale-job trigger takes the inverse path.
  FOR v_exhausted IN
    SELECT job.id,job.data_source_id,job.data_source_version_id,job.config_hash
    FROM platform.data_source_connection_test_jobs AS job
    WHERE job.tenant_id=v_tenant_id
      AND job.status='RUNNING'
      AND job.lease_expires_at<=clock_timestamp()
      AND job.attempt>=job.max_attempts
    ORDER BY job.data_source_id,job.id
  LOOP
    PERFORM 1
    FROM platform.data_sources AS source
    WHERE source.id=v_exhausted.data_source_id
      AND source.tenant_id=v_tenant_id
    FOR UPDATE;

    UPDATE platform.data_source_connection_test_jobs AS job
    SET status='FAILED',error_code='WORKER_LEASE_EXHAUSTED',
        error_message='连接测试执行中断，请重试',
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=clock_timestamp(),updated_at=clock_timestamp()
    WHERE job.id=v_exhausted.id
      AND job.tenant_id=v_tenant_id
      AND job.status='RUNNING'
      AND job.lease_expires_at<=clock_timestamp()
      AND job.attempt>=job.max_attempts;
    GET DIAGNOSTICS v_updated = ROW_COUNT;

    IF v_updated=1 THEN
      UPDATE platform.data_sources AS source
      SET validation_status='FAILED',last_tested_at=clock_timestamp(),
          last_tested_version_id=v_exhausted.data_source_version_id,
          last_tested_config_hash=v_exhausted.config_hash,
          status=CASE WHEN source.current_published_version_id IS NULL
            THEN 'ERROR'::platform.data_source_status ELSE source.status END,
          last_error=CASE WHEN source.current_published_version_id IS NULL
            THEN 'connection test failed' ELSE source.last_error END
      WHERE source.id=v_exhausted.data_source_id
        AND source.tenant_id=v_tenant_id
        AND source.current_draft_version_id=v_exhausted.data_source_version_id;
    END IF;
  END LOOP;

  -- The stale repair sweep is also source -> job and has a deterministic
  -- source/job order. The data-source trigger normally cancels these jobs
  -- synchronously; this loop only repairs rows left by an interrupted rollout.
  FOR v_stale IN
    SELECT job.id,job.data_source_id
    FROM platform.data_source_connection_test_jobs AS job
    WHERE job.tenant_id=v_tenant_id
      AND job.status IN ('QUEUED','RUNNING')
      AND NOT EXISTS(
        SELECT 1
        FROM platform.data_sources AS source
        JOIN platform.data_source_versions AS version
          ON version.id=source.current_draft_version_id
         AND version.data_source_id=source.id
         AND version.tenant_id=source.tenant_id
        WHERE source.id=job.data_source_id
          AND source.tenant_id=job.tenant_id
          AND source.deleted_at IS NULL
          AND source.current_draft_version_id=job.data_source_version_id
          AND version.config_hash=job.config_hash
      )
    ORDER BY job.data_source_id,job.id
  LOOP
    PERFORM 1
    FROM platform.data_sources AS source
    WHERE source.id=v_stale.data_source_id
      AND source.tenant_id=v_tenant_id
    FOR UPDATE;

    UPDATE platform.data_source_connection_test_jobs AS job
    SET status='CANCELLED',error_code='VERSION_CHANGED',
        error_message='数据源配置已变化，请重新测试',
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=clock_timestamp(),updated_at=clock_timestamp()
    WHERE job.id=v_stale.id
      AND job.tenant_id=v_tenant_id
      AND job.status IN ('QUEUED','RUNNING')
      AND NOT EXISTS(
        SELECT 1
        FROM platform.data_sources AS source
        JOIN platform.data_source_versions AS version
          ON version.id=source.current_draft_version_id
         AND version.data_source_id=source.id
         AND version.tenant_id=source.tenant_id
        WHERE source.id=job.data_source_id
          AND source.tenant_id=job.tenant_id
          AND source.deleted_at IS NULL
          AND source.current_draft_version_id=job.data_source_version_id
          AND version.config_hash=job.config_hash
      );
  END LOOP;

  SELECT candidate.id
  INTO v_job_id
  FROM platform.data_source_connection_test_jobs AS candidate
  WHERE candidate.tenant_id=v_tenant_id
    AND candidate.attempt<candidate.max_attempts
    AND (
      (
        candidate.status='QUEUED'
        AND candidate.next_attempt_at<=clock_timestamp()
      )
      OR
      (
        candidate.status='RUNNING'
        AND candidate.lease_expires_at<=clock_timestamp()
      )
    )
  ORDER BY candidate.created_at,candidate.id
  FOR UPDATE SKIP LOCKED
  LIMIT 1;

  IF v_job_id IS NULL THEN
    RETURN;
  END IF;

  UPDATE platform.data_source_connection_test_jobs AS claimed
  SET status='RUNNING',attempt=claimed.attempt+1,
      lease_owner=btrim(p_worker_id),lease_token=v_lease_token,
      lease_expires_at=clock_timestamp()+make_interval(secs=>p_lease_seconds),
      heartbeat_at=clock_timestamp(),started_at=COALESCE(claimed.started_at,clock_timestamp()),
      error_code='',error_message='',updated_at=clock_timestamp()
  WHERE claimed.id=v_job_id;

  RETURN QUERY
  SELECT job.id,job.tenant_id,job.data_source_id,job.data_source_version_id,
         job.config_hash,version.source_type::text,version.config,
         COALESCE(version.secret_ref,''),
         COALESCE(version.file_asset_id::text,''),
         COALESCE(version.file_version_id::text,''),
         COALESCE(quota.max_excel_file_bytes,52428800),
         job.lease_token,job.attempt,job.max_attempts
  FROM platform.data_source_connection_test_jobs AS job
  JOIN platform.data_source_versions AS version
    ON version.id=job.data_source_version_id
   AND version.data_source_id=job.data_source_id
   AND version.tenant_id=job.tenant_id
  LEFT JOIN platform.tenant_data_source_quotas AS quota
    ON quota.tenant_id=job.tenant_id
  WHERE job.id=v_job_id;
END
$$;

REVOKE ALL ON FUNCTION platform.claim_data_source_connection_test(text,integer)
  FROM PUBLIC;

-- 4) 发布证据触发器：仍要求同版本、同摘要且 worker 形成的成功证明，只是不再限时。
CREATE OR REPLACE FUNCTION platform.enforce_data_source_publication_evidence()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  published_hash text;
BEGIN
  IF NEW.current_published_version_id IS NOT DISTINCT FROM OLD.current_published_version_id
    OR NEW.current_published_version_id IS NULL THEN
    RETURN NEW;
  END IF;

  SELECT version.config_hash
  INTO published_hash
  FROM platform.data_source_versions AS version
  WHERE version.id=NEW.current_published_version_id
    AND version.data_source_id=NEW.id
    AND version.tenant_id=NEW.tenant_id
  FOR SHARE;

  IF NOT FOUND
    OR NEW.current_draft_version_id IS DISTINCT FROM NEW.current_published_version_id
    OR NEW.publication_status<>'PUBLISHED'
    OR NEW.validation_status<>'PASSED'
    OR NEW.last_tested_version_id IS DISTINCT FROM NEW.current_published_version_id
    OR NEW.last_tested_config_hash IS DISTINCT FROM published_hash
    OR NOT EXISTS(
      SELECT 1
      FROM platform.data_source_connection_test_attestations AS attestation
      JOIN platform.data_source_connection_test_jobs AS job
        ON job.id=attestation.connection_test_job_id
       AND job.tenant_id=attestation.tenant_id
      WHERE attestation.data_source_id=NEW.id
        AND attestation.tenant_id=NEW.tenant_id
        AND attestation.data_source_version_id=NEW.current_published_version_id
        AND attestation.config_hash=published_hash
        AND attestation.attestation_version='connection-test-worker-v1'
        AND job.status='SUCCEEDED'
        AND job.data_source_id=attestation.data_source_id
        AND job.data_source_version_id=attestation.data_source_version_id
        AND job.config_hash=attestation.config_hash
    ) THEN
    RAISE EXCEPTION
      '发布指针只能切换到具有专用 worker 可信证明的当前草稿'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_publication_evidence()
  FROM PUBLIC;

-- 5) 删除 TTL 专用列；引用这些列的旧 CHECK 约束随列一起删除后按无 TTL 语义重建。
ALTER TABLE platform.data_sources DROP COLUMN test_expires_at;
ALTER TABLE platform.data_sources ADD CONSTRAINT data_sources_test_binding_check CHECK(
  (last_tested_version_id IS NULL AND last_tested_config_hash IS NULL)
  OR
  (last_tested_version_id IS NOT NULL AND last_tested_config_hash ~ '^[0-9a-f]{64}$')
);

ALTER TABLE platform.data_source_connection_test_attestations DROP COLUMN expires_at;
ALTER TABLE platform.data_source_connection_test_attestations
  ADD CONSTRAINT data_source_connection_test_attestation_window_check CHECK(
    started_at<=completed_at
    AND started_at>=completed_at-interval '15 minutes'
  );

ALTER TABLE platform.data_source_test_runs DROP COLUMN expires_at;

COMMENT ON TABLE platform.data_source_connection_test_attestations IS
  'Immutable successful connection-test attestations permanently bound to one exact data source version and config hash';
COMMENT ON FUNCTION platform.complete_data_source_connection_test(uuid,uuid,text,bigint) IS
  'Finalizes the current leased job and creates a database-timed attestation bound to the exact tested version';
COMMENT ON FUNCTION platform.enforce_data_source_publication_evidence() IS
  'Rejects publication-pointer changes without a matching exact-version worker attestation; tests never expire while the configuration is unchanged';
