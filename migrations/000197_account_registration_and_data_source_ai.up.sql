-- 自助注册只创建租户配置的受限编辑角色，并自动加入默认业务域。
-- 这两个键可由租户管理员关闭或替换，避免把注册与硬编码角色耦合。
INSERT INTO platform.roles(tenant_id,code,name,description,is_system)
SELECT id,'data_source_editor','数据源配置员',
       '可配置和测试数据源，但不能审批发布',true
FROM platform.tenants
WHERE status='ACTIVE' AND deleted_at IS NULL
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,description=EXCLUDED.description,
  status='ACTIVE',deleted_at=NULL;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT role.tenant_id,role.id,permission.id
FROM platform.roles AS role
JOIN platform.permissions AS permission ON permission.tenant_id=role.tenant_id
WHERE role.code='data_source_editor'
  AND permission.code::text IN (
    'data_source.manage','data_asset.read','data_asset.manage','dataset.read'
  )
ON CONFLICT DO NOTHING;

UPDATE platform.tenants
SET settings=jsonb_set(
  jsonb_set(
    COALESCE(settings,'{}'::jsonb),'{selfRegistrationEnabled}',
    COALESCE(settings->'selfRegistrationEnabled','true'::jsonb),true
  ),
  '{selfRegistrationRoleCode}',
  COALESCE(settings->'selfRegistrationRoleCode',to_jsonb('data_source_editor'::text)),true
)
WHERE NOT (COALESCE(settings,'{}'::jsonb) ? 'selfRegistrationEnabled')
   OR NOT (COALESCE(settings,'{}'::jsonb) ? 'selfRegistrationRoleCode');

ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT ai_tenant_policies_purposes_check;

-- 已启用 AI 的租户自动开放数据源配置助手；禁用租户继续保持禁用。
UPDATE platform.ai_tenant_policies
SET allowed_purposes=(
  SELECT ARRAY(
    SELECT DISTINCT purpose
    FROM unnest(
      allowed_purposes || CASE WHEN enabled
        THEN ARRAY['DATA_SOURCE_CONFIGURATION']::text[]
        ELSE ARRAY[]::text[] END
    ) AS purpose
    WHERE purpose IN (
      'METADATA_COMPLETION','DATASET_DAG_GENERATION',
      'DATASET_TAG_SUGGESTION','DATASET_SEMANTIC_NAMING',
      'DATA_SOURCE_CONFIGURATION'
    )
    ORDER BY purpose
  )
);

UPDATE platform.ai_tenant_policies
SET allowed_purposes=ARRAY['METADATA_COMPLETION']::text[]
WHERE cardinality(allowed_purposes)=0;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 5
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION','DATASET_DAG_GENERATION',
      'DATASET_TAG_SUGGESTION','DATASET_SEMANTIC_NAMING',
      'DATA_SOURCE_CONFIGURATION'
    ]::text[]
  );

ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

-- 保留历史用途以保证不可变审计记录仍满足约束。
ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION','REPORT_GENERATION','BLOCK_EDIT',
    'CONCLUSION_GENERATION','DATASET_DAG_GENERATION','METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION','SEMANTIC_QUERY_PLANNING',
    'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION'
  ));

COMMENT ON COLUMN platform.ai_tenant_policies.allowed_purposes IS
  '租户显式授权的 AI 用途；DATA_SOURCE_CONFIGURATION 只生成配置草稿和诊断，不能代替连接测试、发布审批或用户确认';

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
    'NETWORK_UNREACHABLE','CREDENTIAL_UNAVAILABLE',
    'SOURCE_UNAVAILABLE','FILE_UNAVAILABLE'
  ) THEN
    v_error_code := 'CONNECTION_FAILED';
  END IF;
  v_error_message := CASE v_error_code
    WHEN 'CONNECTION_AUTH_FAILED' THEN '用户名或密码校验失败，请确认账号、密码和远程登录权限'
    WHEN 'DATABASE_NOT_FOUND' THEN '目标数据库或服务名不存在，请确认名称和账号访问权限'
    WHEN 'CONNECTION_DNS_FAILED' THEN 'Host 无法解析，请检查域名或改用可路由的内网地址'
    WHEN 'CONNECTION_REFUSED' THEN '目标地址拒绝连接，请检查端口、数据库服务、防火墙和端口映射'
    WHEN 'NETWORK_UNREACHABLE' THEN '目标网络不可达，请检查路由、安全组、防火墙和出站策略'
    WHEN 'CONNECTION_TIMEOUT' THEN '连接测试超时，请检查网络、防火墙和目标地址'
    WHEN 'CREDENTIAL_UNAVAILABLE' THEN '连接凭据不可用，请更新配置后重试'
    WHEN 'SOURCE_UNAVAILABLE' THEN '数据源暂时不可用，请稍后重试'
    WHEN 'FILE_UNAVAILABLE' THEN '文件版本不可读取，请重新上传后重试'
    ELSE '连接测试失败，请检查配置后重试'
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
