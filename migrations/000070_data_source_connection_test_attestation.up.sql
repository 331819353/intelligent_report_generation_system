-- 连接测试改为独立 worker 异步执行。API 只能入队和读取结果，只有专用
-- connection-test worker 可以通过受限函数领取任务、续租并形成可信证明。
-- 历史 data_source_test_runs 保留只读兼容，但不再允许写入，也不能用于新发布。

CREATE TABLE platform.data_source_connection_test_jobs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  data_source_version_id uuid NOT NULL,
  config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
  idempotency_key_hash text
    CHECK(idempotency_key_hash IS NULL OR idempotency_key_hash ~ '^[0-9a-f]{64}$'),
  requested_by uuid,
  status text NOT NULL DEFAULT 'QUEUED'
    CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 5),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text,
  lease_token uuid,
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  error_code text NOT NULL DEFAULT ''
    CHECK(length(error_code)<=64 AND (error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]*$')),
  error_message text NOT NULL DEFAULT '' CHECK(length(error_message)<=256),
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_version_id,data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id),
  FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (requested_by),
  CONSTRAINT data_source_connection_test_job_state_check CHECK(
    (
      status='QUEUED'
      AND lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL
      AND completed_at IS NULL
    )
    OR
    (
      status='RUNNING'
      AND lease_owner IS NOT NULL AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND heartbeat_at IS NOT NULL
      AND started_at IS NOT NULL AND completed_at IS NULL
    )
    OR
    (
      status IN ('SUCCEEDED','FAILED','CANCELLED')
      AND lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL
      AND completed_at IS NOT NULL
    )
  ),
  UNIQUE(id,tenant_id)
);

CREATE UNIQUE INDEX data_source_connection_test_jobs_active_exact_idx
  ON platform.data_source_connection_test_jobs(
    tenant_id,data_source_id,data_source_version_id,config_hash
  )
  WHERE status IN ('QUEUED','RUNNING');

CREATE UNIQUE INDEX data_source_connection_test_jobs_idempotency_idx
  ON platform.data_source_connection_test_jobs(
    tenant_id,data_source_id,data_source_version_id,idempotency_key_hash
  )
  WHERE idempotency_key_hash IS NOT NULL;

CREATE INDEX data_source_connection_test_jobs_claim_idx
  ON platform.data_source_connection_test_jobs(
    tenant_id,status,next_attempt_at,created_at
  )
  WHERE status IN ('QUEUED','RUNNING');

CREATE TABLE platform.data_source_connection_test_attestations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  connection_test_job_id uuid NOT NULL,
  data_source_id uuid NOT NULL,
  data_source_version_id uuid NOT NULL,
  config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
  attestation_version text NOT NULL DEFAULT 'connection-test-worker-v1'
    CHECK(attestation_version='connection-test-worker-v1'),
  executor_id text NOT NULL CHECK(length(executor_id) BETWEEN 1 AND 128),
  server_version text NOT NULL DEFAULT '' CHECK(length(server_version)<=256),
  latency_ms bigint NOT NULL CHECK(latency_ms BETWEEN 0 AND 900000),
  started_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(connection_test_job_id,tenant_id)
    REFERENCES platform.data_source_connection_test_jobs(id,tenant_id),
  FOREIGN KEY(data_source_version_id,data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id),
  CONSTRAINT data_source_connection_test_attestation_window_check CHECK(
    started_at<=completed_at
    AND started_at>=completed_at-interval '15 minutes'
    AND expires_at=completed_at+interval '30 minutes'
  ),
  UNIQUE(connection_test_job_id),
  UNIQUE(id,tenant_id)
);

CREATE INDEX data_source_connection_test_attestations_publish_idx
  ON platform.data_source_connection_test_attestations(
    tenant_id,data_source_id,data_source_version_id,config_hash,completed_at DESC
  );

ALTER TABLE platform.data_source_connection_test_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_connection_test_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_connection_test_attestations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_connection_test_attestations FORCE ROW LEVEL SECURITY;

CREATE POLICY data_source_connection_test_jobs_tenant_isolation
  ON platform.data_source_connection_test_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE POLICY data_source_connection_test_attestations_tenant_isolation
  ON platform.data_source_connection_test_attestations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 旧同步证据不能驱动 070 之后的新发布。已经上线且草稿指针等于发布指针的
-- 运行态保持不变；其余待切换草稿必须重新经过专用 worker。
UPDATE platform.data_sources
SET validation_status='UNTESTED',
    last_tested_at=NULL,
    last_tested_version_id=NULL,
    last_tested_config_hash=NULL,
    test_expires_at=NULL
WHERE current_draft_version_id IS DISTINCT FROM current_published_version_id
  AND validation_status='PASSED';

CREATE OR REPLACE FUNCTION platform.enforce_data_source_connection_test_job_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.data_source_id IS DISTINCT FROM OLD.data_source_id
    OR NEW.data_source_version_id IS DISTINCT FROM OLD.data_source_version_id
    OR NEW.config_hash IS DISTINCT FROM OLD.config_hash
    OR NEW.idempotency_key_hash IS DISTINCT FROM OLD.idempotency_key_hash
    OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
    OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '连接测试任务身份字段不可修改' USING ERRCODE='23514';
  END IF;

  IF OLD.status IN ('SUCCEEDED','FAILED','CANCELLED') THEN
    RAISE EXCEPTION '连接测试终态任务不可修改' USING ERRCODE='23514';
  END IF;

  IF NEW.status IS DISTINCT FROM OLD.status
    AND NOT (
      (OLD.status='QUEUED' AND NEW.status IN ('RUNNING','CANCELLED'))
      OR
      (OLD.status='RUNNING' AND NEW.status IN ('QUEUED','SUCCEEDED','FAILED','CANCELLED'))
    ) THEN
    RAISE EXCEPTION '非法连接测试任务状态转换' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_connection_test_job_transition()
  FROM PUBLIC;

CREATE TRIGGER data_source_connection_test_jobs_guard_transition
BEFORE UPDATE ON platform.data_source_connection_test_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_data_source_connection_test_job_transition();

CREATE OR REPLACE FUNCTION platform.reject_data_source_connection_test_attestation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '连接测试证明不可修改或删除' USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_data_source_connection_test_attestation_mutation()
  FROM PUBLIC;

CREATE TRIGGER data_source_connection_test_attestations_immutable
BEFORE UPDATE OR DELETE ON platform.data_source_connection_test_attestations
FOR EACH ROW EXECUTE FUNCTION platform.reject_data_source_connection_test_attestation_mutation();

-- v58-v69 的同步测试记录仅作为历史审计事实保留。即使未来误授表权限，
-- 也不能再往旧表补写一条 PASSED 来伪造新发布凭据。
CREATE OR REPLACE FUNCTION platform.enforce_data_source_test_evidence()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  RAISE EXCEPTION
    '历史连接测试记录只读；请通过异步连接测试任务生成可信证明'
    USING ERRCODE='42501';
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_test_evidence() FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.enqueue_data_source_connection_test(
  p_data_source_id uuid,
  p_requested_by uuid,
  p_idempotency_key_hash text DEFAULT NULL
)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
  v_version_id uuid;
  v_config_hash text;
  v_job_id uuid;
BEGIN
  IF v_tenant_id IS NULL THEN
    RAISE EXCEPTION '缺少租户上下文' USING ERRCODE='22023';
  END IF;
  IF p_idempotency_key_hash IS NOT NULL
    AND p_idempotency_key_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION '幂等键摘要无效' USING ERRCODE='22023';
  END IF;

  SELECT source.current_draft_version_id,version.config_hash
  INTO v_version_id,v_config_hash
  FROM platform.data_sources AS source
  JOIN platform.data_source_versions AS version
    ON version.id=source.current_draft_version_id
   AND version.data_source_id=source.id
   AND version.tenant_id=source.tenant_id
  WHERE source.id=p_data_source_id
    AND source.tenant_id=v_tenant_id
    AND source.deleted_at IS NULL
  FOR UPDATE OF source;

  IF NOT FOUND THEN
    RAISE EXCEPTION '数据源不存在' USING ERRCODE='P0002';
  END IF;

  IF p_idempotency_key_hash IS NOT NULL THEN
    SELECT job.id
    INTO v_job_id
    FROM platform.data_source_connection_test_jobs AS job
    WHERE job.tenant_id=v_tenant_id
      AND job.data_source_id=p_data_source_id
      AND job.data_source_version_id=v_version_id
      AND job.idempotency_key_hash=p_idempotency_key_hash;
    IF FOUND THEN
      RETURN v_job_id;
    END IF;
  END IF;

  SELECT job.id
  INTO v_job_id
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.tenant_id=v_tenant_id
    AND job.data_source_id=p_data_source_id
    AND job.data_source_version_id=v_version_id
    AND job.config_hash=v_config_hash
    AND job.status IN ('QUEUED','RUNNING')
  ORDER BY job.created_at DESC
  LIMIT 1;
  IF FOUND THEN
    RETURN v_job_id;
  END IF;

  INSERT INTO platform.data_source_connection_test_jobs(
    tenant_id,data_source_id,data_source_version_id,config_hash,
    idempotency_key_hash,requested_by
  )
  VALUES(
    v_tenant_id,p_data_source_id,v_version_id,v_config_hash,
    p_idempotency_key_hash,p_requested_by
  )
  ON CONFLICT DO NOTHING
  RETURNING id INTO v_job_id;

  IF v_job_id IS NULL THEN
    SELECT job.id
    INTO v_job_id
    FROM platform.data_source_connection_test_jobs AS job
    WHERE job.tenant_id=v_tenant_id
      AND job.data_source_id=p_data_source_id
      AND job.data_source_version_id=v_version_id
      AND (
        (
          p_idempotency_key_hash IS NOT NULL
          AND job.idempotency_key_hash=p_idempotency_key_hash
        )
        OR
        (
          job.config_hash=v_config_hash
          AND job.status IN ('QUEUED','RUNNING')
        )
      )
    ORDER BY job.created_at DESC
    LIMIT 1;
  END IF;

  IF v_job_id IS NULL THEN
    RAISE EXCEPTION '连接测试任务入队冲突' USING ERRCODE='40001';
  END IF;
  RETURN v_job_id;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_data_source_connection_test(uuid,uuid,text)
  FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.list_connection_test_job_tenant_ids()
RETURNS SETOF uuid
LANGUAGE sql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT DISTINCT job.tenant_id
  FROM platform.data_source_connection_test_jobs AS job
  WHERE (
       job.status='QUEUED'
       AND job.next_attempt_at<=clock_timestamp()
     )
     OR (
       job.status='RUNNING'
       AND job.lease_expires_at<=clock_timestamp()
     )
  ORDER BY job.tenant_id
$$;

REVOKE ALL ON FUNCTION platform.list_connection_test_job_tenant_ids()
  FROM PUBLIC;

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
          last_tested_config_hash=v_exhausted.config_hash,test_expires_at=NULL,
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

CREATE OR REPLACE FUNCTION platform.heartbeat_data_source_connection_test(
  p_job_id uuid,
  p_lease_token uuid,
  p_lease_seconds integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
BEGIN
  IF v_tenant_id IS NULL OR p_lease_seconds NOT BETWEEN 10 AND 300 THEN
    RETURN false;
  END IF;
  UPDATE platform.data_source_connection_test_jobs AS job
  SET heartbeat_at=clock_timestamp(),
      lease_expires_at=clock_timestamp()+make_interval(secs=>p_lease_seconds),
      updated_at=clock_timestamp()
  WHERE job.id=p_job_id
    AND job.tenant_id=v_tenant_id
    AND job.status='RUNNING'
    AND job.lease_token=p_lease_token
    AND job.lease_expires_at>clock_timestamp();
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION platform.heartbeat_data_source_connection_test(uuid,uuid,integer)
  FROM PUBLIC;

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
    started_at,completed_at,expires_at
  )
  VALUES(
    v_job.tenant_id,v_job.id,v_job.data_source_id,v_job.data_source_version_id,
    v_job.config_hash,v_executor_id,v_server_version,
    LEAST(GREATEST(COALESCE(p_latency_ms,0),0),900000),
    GREATEST(v_job.started_at,v_completed_at-interval '15 minutes'),
    v_completed_at,v_completed_at+interval '30 minutes'
  );

  UPDATE platform.data_sources AS source
  SET validation_status='PASSED',last_tested_at=v_completed_at,
      last_tested_version_id=v_job.data_source_version_id,
      last_tested_config_hash=v_job.config_hash,
      test_expires_at=v_completed_at+interval '30 minutes',
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
  IF v_tenant_id IS NULL THEN
    RETURN '';
  END IF;

  -- Match the data-source edit trigger's source -> job lock order.
  SELECT job.data_source_id
  INTO v_data_source_id
  FROM platform.data_source_connection_test_jobs AS job
  WHERE job.id=p_job_id AND job.tenant_id=v_tenant_id;
  IF NOT FOUND THEN
    RETURN '';
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
    OR v_job.lease_expires_at<=v_now THEN
    RETURN '';
  END IF;

  IF v_error_code NOT IN (
    'CONNECTION_TIMEOUT','CONNECTION_FAILED','CREDENTIAL_UNAVAILABLE',
    'SOURCE_UNAVAILABLE','FILE_UNAVAILABLE'
  ) THEN
    v_error_code := 'CONNECTION_FAILED';
  END IF;
  v_error_message := CASE v_error_code
    WHEN 'CONNECTION_TIMEOUT' THEN '连接测试超时，请检查网络后重试'
    WHEN 'CREDENTIAL_UNAVAILABLE' THEN '连接凭据不可用，请更新配置后重试'
    WHEN 'SOURCE_UNAVAILABLE' THEN '数据源暂时不可用，请稍后重试'
    WHEN 'FILE_UNAVAILABLE' THEN '文件版本不可读取，请重新上传后重试'
    ELSE '连接测试失败，请检查配置后重试'
  END;

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
        completed_at=v_now,updated_at=v_now
    WHERE id=v_job.id;
    RETURN 'CANCELLED';
  END IF;

  IF COALESCE(p_retryable,false) AND v_job.attempt<v_job.max_attempts THEN
    v_next_status := 'QUEUED';
    UPDATE platform.data_source_connection_test_jobs
    SET status='QUEUED',next_attempt_at=v_now+interval '5 seconds',
        error_code=v_error_code,error_message=v_error_message,
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        updated_at=v_now
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
        last_tested_config_hash=v_job.config_hash,test_expires_at=NULL,
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

CREATE OR REPLACE FUNCTION platform.cancel_stale_data_source_connection_test_jobs()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.current_draft_version_id IS DISTINCT FROM OLD.current_draft_version_id
    OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL) THEN
    UPDATE platform.data_source_connection_test_jobs AS job
    SET status='CANCELLED',error_code='VERSION_CHANGED',
        error_message='数据源配置已变化，请重新测试',
        lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
        completed_at=clock_timestamp(),updated_at=clock_timestamp()
    WHERE job.tenant_id=NEW.tenant_id
      AND job.data_source_id=NEW.id
      AND job.status IN ('QUEUED','RUNNING')
      AND (
        NEW.deleted_at IS NOT NULL
        OR job.data_source_version_id IS DISTINCT FROM NEW.current_draft_version_id
      );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.cancel_stale_data_source_connection_test_jobs()
  FROM PUBLIC;

CREATE TRIGGER data_sources_cancel_stale_connection_tests
AFTER UPDATE OF current_draft_version_id,deleted_at ON platform.data_sources
FOR EACH ROW EXECUTE FUNCTION platform.cancel_stale_data_source_connection_test_jobs();

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
    OR NEW.test_expires_at IS NULL
    OR NEW.test_expires_at<=clock_timestamp()
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
        AND attestation.expires_at=attestation.completed_at+interval '30 minutes'
        AND attestation.expires_at IS NOT DISTINCT FROM NEW.test_expires_at
        AND attestation.expires_at>clock_timestamp()
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

COMMENT ON TABLE platform.data_source_connection_test_jobs IS
  'Frozen exact-version connection tests; API may enqueue/read while only the dedicated worker may lease and finish through guarded functions';
COMMENT ON TABLE platform.data_source_connection_test_attestations IS
  'Immutable successful connection-test attestations whose timestamps and fixed 30-minute expiry are generated by PostgreSQL';
COMMENT ON FUNCTION platform.enqueue_data_source_connection_test(uuid,uuid,text) IS
  'Enqueues or idempotently returns a test for the tenant current draft without accepting caller-supplied version, hash or timestamps';
COMMENT ON FUNCTION platform.complete_data_source_connection_test(uuid,uuid,text,bigint) IS
  'Finalizes the current leased job and creates a database-timed 30-minute attestation';
