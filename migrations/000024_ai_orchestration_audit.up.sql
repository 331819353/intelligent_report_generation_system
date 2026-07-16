-- 建立通用 AI 编排的租户授权策略、配额预留和脱敏调用审计。
-- 锁住租户表可消除“回填已有租户”与“安装未来租户触发器”之间的并发空窗。
LOCK TABLE platform.tenants IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE platform.ai_tenant_policies(
  tenant_id uuid PRIMARY KEY REFERENCES platform.tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  allowed_purposes text[] NOT NULL DEFAULT ARRAY['METADATA_COMPLETION']::text[],
  max_requests_per_day integer NOT NULL DEFAULT 1000 CHECK(max_requests_per_day>0),
  max_tokens_per_month bigint NOT NULL DEFAULT 10000000 CHECK(max_tokens_per_month>0),
  max_cost_micros_per_month bigint NOT NULL DEFAULT 100000000 CHECK(max_cost_micros_per_month>0),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 4
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION',
      'REPORT_GENERATION',
      'BLOCK_EDIT',
      'CONCLUSION_GENERATION'
    ]::text[]
  )
);

-- ai_requests 只保存不可逆摘要和计量信息，禁止出现原始提示词、响应正文或错误正文列。
CREATE TABLE platform.ai_requests(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  actor_user_id uuid NOT NULL,
  purpose text NOT NULL,
  resource_type text NOT NULL DEFAULT '',
  resource_id text NOT NULL DEFAULT '',
  provider text NOT NULL,
  model_name text NOT NULL,
  provider_model text NOT NULL DEFAULT '',
  provider_request_id text NOT NULL DEFAULT '',
  prompt_version text NOT NULL,
  input_hash text NOT NULL,
  input_bytes bigint NOT NULL CHECK(input_bytes>0),
  redaction_count integer NOT NULL DEFAULT 0 CHECK(redaction_count>=0),
  reserved_tokens bigint NOT NULL CHECK(reserved_tokens>0),
  reserved_cost_micros bigint NOT NULL DEFAULT 0 CHECK(reserved_cost_micros>=0),
  max_attempts integer NOT NULL CHECK(max_attempts BETWEEN 1 AND 5),
  attempts integer NOT NULL DEFAULT 1,
  status text NOT NULL DEFAULT 'RUNNING' CHECK(status IN ('RUNNING','SUCCEEDED','FAILED','CANCELED')),
  error_code text NOT NULL DEFAULT '',
  finish_reason text NOT NULL DEFAULT '',
  prompt_tokens integer NOT NULL DEFAULT 0 CHECK(prompt_tokens>=0),
  completion_tokens integer NOT NULL DEFAULT 0 CHECK(completion_tokens>=0),
  total_tokens integer NOT NULL DEFAULT 0 CHECK(total_tokens>=0),
  cost_micros bigint NOT NULL DEFAULT 0 CHECK(cost_micros>=0),
  -- 失败和取消请求可能仍被供应商计费；无法取得可信实耗时按预留量失败关闭。
  accounted_tokens bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN GREATEST(total_tokens::bigint,reserved_tokens) ELSE reserved_tokens END
  ) STORED,
  accounted_cost_micros bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN GREATEST(cost_micros,reserved_cost_micros) ELSE reserved_cost_micros END
  ) STORED,
  latency_ms bigint NOT NULL DEFAULT 0 CHECK(latency_ms>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '5 minutes',
  completed_at timestamptz,
  CONSTRAINT ai_requests_actor_fk FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION','REPORT_GENERATION','BLOCK_EDIT','CONCLUSION_GENERATION'
  )),
  CONSTRAINT ai_requests_resource_pair_check CHECK(
    (resource_type='' AND resource_id='') OR (btrim(resource_type)<>'' AND btrim(resource_id)<>'')
  ),
  CONSTRAINT ai_requests_text_bounds_check CHECK(
    length(provider) BETWEEN 1 AND 128
    AND length(model_name) BETWEEN 1 AND 256
    AND length(provider_model)<=256
    AND length(provider_request_id)<=256
    AND length(prompt_version) BETWEEN 1 AND 128
    AND length(resource_type)<=64
    AND length(resource_id)<=256
    AND length(error_code)<=128
    AND length(finish_reason)<=128
  ),
  CONSTRAINT ai_requests_text_trimmed_check CHECK(
    provider=btrim(provider)
    AND model_name=btrim(model_name)
    AND provider_model=btrim(provider_model)
    AND provider_request_id=btrim(provider_request_id)
    AND prompt_version=btrim(prompt_version)
    AND resource_type=btrim(resource_type)
    AND resource_id=btrim(resource_id)
    AND error_code=btrim(error_code)
    AND finish_reason=btrim(finish_reason)
  ),
  CONSTRAINT ai_requests_text_control_check CHECK(
    provider !~ '[[:cntrl:]]'
    AND model_name !~ '[[:cntrl:]]'
    AND provider_model !~ '[[:cntrl:]]'
    AND provider_request_id !~ '[[:cntrl:]]'
    AND prompt_version !~ '[[:cntrl:]]'
    AND resource_type !~ '[[:cntrl:]]'
    AND resource_id !~ '[[:cntrl:]]'
    AND error_code !~ '[[:cntrl:]]'
    AND finish_reason !~ '[[:cntrl:]]'
  ),
  CONSTRAINT ai_requests_input_hash_check CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ai_requests_error_code_check CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{1,127}$'
  ),
  CONSTRAINT ai_requests_provider_request_id_check CHECK(
    provider_request_id='' OR provider_request_id ~ '^[0-9a-f]{64}$'
  ),
  CONSTRAINT ai_requests_finish_reason_check CHECK(
    finish_reason IN ('','stop','length','content_filter','tool_calls','function_call','other')
  ),
  CONSTRAINT ai_requests_token_total_check CHECK(total_tokens::bigint>=prompt_tokens::bigint+completion_tokens::bigint),
  CONSTRAINT ai_requests_attempts_check CHECK(attempts BETWEEN 1 AND max_attempts),
  CONSTRAINT ai_requests_expiry_check CHECK(expires_at>created_at AND expires_at<=created_at+interval '1 hour'),
  CONSTRAINT ai_requests_lifecycle_check CHECK(
    (status='RUNNING' AND completed_at IS NULL AND error_code='' AND provider_model=''
      AND provider_request_id='' AND finish_reason='' AND prompt_tokens=0 AND completion_tokens=0
      AND total_tokens=0 AND cost_micros=0 AND latency_ms=0 AND attempts=1)
    OR (status='SUCCEEDED' AND completed_at IS NOT NULL AND error_code=''
      AND provider_model=model_name AND prompt_tokens>0 AND completion_tokens>0 AND total_tokens>0)
    OR (status='FAILED' AND completed_at IS NOT NULL AND btrim(error_code)<>'' AND provider_model=''
      AND provider_request_id='' AND finish_reason='' AND prompt_tokens=0 AND completion_tokens=0
      AND total_tokens=0 AND cost_micros=0)
    OR (status='CANCELED' AND completed_at IS NOT NULL AND error_code='AI_REQUEST_CANCELED'
      AND provider_model='' AND provider_request_id='' AND finish_reason='' AND prompt_tokens=0
      AND completion_tokens=0 AND total_tokens=0 AND cost_micros=0)
  ),
  UNIQUE(id,tenant_id)
);

CREATE INDEX ai_requests_tenant_time_idx
  ON platform.ai_requests(tenant_id,created_at DESC);
CREATE INDEX ai_requests_tenant_status_idx
  ON platform.ai_requests(tenant_id,status,created_at DESC);
CREATE INDEX ai_requests_running_expiry_idx
  ON platform.ai_requests(tenant_id,expires_at)
  WHERE status='RUNNING';
CREATE INDEX ai_requests_tenant_purpose_idx
  ON platform.ai_requests(tenant_id,purpose,created_at DESC);
CREATE INDEX ai_requests_resource_idx
  ON platform.ai_requests(tenant_id,resource_type,resource_id,created_at DESC)
  WHERE resource_type<>'';

CREATE OR REPLACE FUNCTION platform.bump_ai_tenant_policy_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.version=OLD.version+1;
  NEW.updated_at=now();
  RETURN NEW;
END
$$;

CREATE TRIGGER ai_tenant_policies_bump_version
BEFORE UPDATE ON platform.ai_tenant_policies
FOR EACH ROW EXECUTE FUNCTION platform.bump_ai_tenant_policy_version();

-- 运行中记录只能转换一次为终态；请求身份、输入摘要和配额预留均不可被事后改写。
CREATE OR REPLACE FUNCTION platform.enforce_ai_request_audit_immutability()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'AI request audit records are immutable';
  END IF;
  IF OLD.status<>'RUNNING' OR NEW.status='RUNNING' THEN
    RAISE EXCEPTION 'AI request terminal state is immutable';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.actor_user_id IS DISTINCT FROM OLD.actor_user_id
    OR NEW.purpose IS DISTINCT FROM OLD.purpose
    OR NEW.resource_type IS DISTINCT FROM OLD.resource_type
    OR NEW.resource_id IS DISTINCT FROM OLD.resource_id
    OR NEW.provider IS DISTINCT FROM OLD.provider
    OR NEW.model_name IS DISTINCT FROM OLD.model_name
    OR NEW.prompt_version IS DISTINCT FROM OLD.prompt_version
    OR NEW.input_hash IS DISTINCT FROM OLD.input_hash
    OR NEW.input_bytes IS DISTINCT FROM OLD.input_bytes
    OR NEW.redaction_count IS DISTINCT FROM OLD.redaction_count
    OR NEW.reserved_tokens IS DISTINCT FROM OLD.reserved_tokens
    OR NEW.reserved_cost_micros IS DISTINCT FROM OLD.reserved_cost_micros
    OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'AI request audit identity is immutable';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER ai_requests_audit_immutable
BEFORE UPDATE OR DELETE ON platform.ai_requests
FOR EACH ROW EXECUTE FUNCTION platform.enforce_ai_request_audit_immutability();

-- 触发器函数以迁移所有者身份写入默认策略，避免新租户创建发生在租户上下文建立之前时被 RLS 拒绝。
CREATE OR REPLACE FUNCTION platform.insert_default_ai_tenant_policy()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  INSERT INTO platform.ai_tenant_policies(tenant_id,enabled)
  VALUES(NEW.id,false)
  ON CONFLICT(tenant_id) DO NOTHING;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.insert_default_ai_tenant_policy() FROM PUBLIC;

CREATE TRIGGER tenants_insert_default_ai_policy
AFTER INSERT ON platform.tenants
FOR EACH ROW EXECUTE FUNCTION platform.insert_default_ai_tenant_policy();

-- 已有租户只保持当前元数据补全能力；其他用途和新租户都需要可信流程显式授权。
INSERT INTO platform.ai_tenant_policies(tenant_id,enabled,allowed_purposes)
SELECT id,true,ARRAY['METADATA_COMPLETION']::text[] FROM platform.tenants
ON CONFLICT(tenant_id) DO NOTHING;

ALTER TABLE platform.ai_tenant_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_tenant_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.ai_requests FORCE ROW LEVEL SECURITY;

CREATE POLICY ai_tenant_policies_tenant_isolation ON platform.ai_tenant_policies
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY ai_requests_tenant_isolation ON platform.ai_requests
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.ai_tenant_policies IS '租户级 AI 授权用途和日/月配额策略；已有租户仅保留元数据补全，新租户默认禁用';
COMMENT ON TABLE platform.ai_requests IS '不含提示词与响应正文的通用 AI 请求、配额预留和用量审计';
COMMENT ON COLUMN platform.ai_requests.input_hash IS '发送前完成最小化与脱敏后的规范请求 SHA-256';
COMMENT ON COLUMN platform.ai_requests.redaction_count IS '发送前被本地规则替换的敏感片段数量';
COMMENT ON COLUMN platform.ai_requests.provider_request_id IS 'Provider 请求标识的 SHA-256；不保存上游原文';
COMMENT ON COLUMN platform.ai_requests.max_attempts IS '预留预算时使用的最大 Provider 尝试次数';
COMMENT ON COLUMN platform.ai_requests.reserved_tokens IS '按最大尝试次数计算并计入月度配额的保守 Token 预留';
COMMENT ON COLUMN platform.ai_requests.reserved_cost_micros IS '按最大尝试次数计算并计入月度配额的保守费用预留';
COMMENT ON COLUMN platform.ai_requests.accounted_tokens IS '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭 Token 计量';
COMMENT ON COLUMN platform.ai_requests.accounted_cost_micros IS '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭费用计量';
COMMENT ON COLUMN platform.ai_requests.expires_at IS '进程异常退出后把 RUNNING 审计收口为失败的服务端租约截止时间；预留仍按失败关闭规则计量';
