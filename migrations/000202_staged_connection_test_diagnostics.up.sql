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

COMMENT ON FUNCTION platform.fail_data_source_connection_test(uuid,uuid,text,boolean) IS
  'Stores safe stage-specific address, port, database/service and authentication diagnostics';
