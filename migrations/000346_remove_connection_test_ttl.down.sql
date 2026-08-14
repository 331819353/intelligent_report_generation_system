-- 回滚：恢复 30 分钟测试收据 TTL 列并按旧口径回填。历史行的原始过期时间
-- 无法精确还原，统一按 completed_at+30 分钟重建，旧行为即刻视为已过期。

ALTER TABLE platform.data_source_test_runs ADD COLUMN expires_at timestamptz;
UPDATE platform.data_source_test_runs
SET expires_at=completed_at+interval '30 minutes'
WHERE status='PASSED';
ALTER TABLE platform.data_source_test_runs
  ADD CONSTRAINT data_source_test_run_expiry_check CHECK(
    (status='PASSED' AND expires_at IS NOT NULL AND expires_at>completed_at)
    OR
    (status='FAILED' AND expires_at IS NULL)
  );

ALTER TABLE platform.data_source_connection_test_attestations DISABLE TRIGGER data_source_connection_test_attestations_immutable;
ALTER TABLE platform.data_source_connection_test_attestations ADD COLUMN expires_at timestamptz;
UPDATE platform.data_source_connection_test_attestations
SET expires_at=completed_at+interval '30 minutes';
ALTER TABLE platform.data_source_connection_test_attestations
  ALTER COLUMN expires_at SET NOT NULL;
ALTER TABLE platform.data_source_connection_test_attestations
  DROP CONSTRAINT data_source_connection_test_attestation_window_check;
ALTER TABLE platform.data_source_connection_test_attestations
  ADD CONSTRAINT data_source_connection_test_attestation_window_check CHECK(
    started_at<=completed_at
    AND started_at>=completed_at-interval '15 minutes'
    AND expires_at=completed_at+interval '30 minutes'
  );
ALTER TABLE platform.data_source_connection_test_attestations ENABLE TRIGGER data_source_connection_test_attestations_immutable;

ALTER TABLE platform.data_sources ADD COLUMN test_expires_at timestamptz;
UPDATE platform.data_sources
SET test_expires_at=last_tested_at+interval '30 minutes'
WHERE validation_status='PASSED' AND last_tested_at IS NOT NULL;
ALTER TABLE platform.data_sources DROP CONSTRAINT data_sources_test_binding_check;
ALTER TABLE platform.data_sources ADD CONSTRAINT data_sources_test_binding_check CHECK(
  (last_tested_version_id IS NULL AND last_tested_config_hash IS NULL AND test_expires_at IS NULL)
  OR
  (last_tested_version_id IS NOT NULL AND last_tested_config_hash ~ '^[0-9a-f]{64}$')
);

-- 函数按 000070/000202 的 TTL 版本恢复（此处直接内联恢复关键差异）。
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
