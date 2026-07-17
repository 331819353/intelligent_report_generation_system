-- 修复早期开发库已登记 000024、但 ai_requests 仍保留旧版结构的迁移漂移。
-- 全新数据库已由 000024 创建最终结构，本迁移只在缺少 max_attempts 时执行数据结构修复。

DROP TRIGGER IF EXISTS ai_requests_audit_immutable ON platform.ai_requests;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema='platform'
      AND table_name='ai_requests'
      AND column_name='max_attempts'
  ) THEN
    ALTER TABLE platform.ai_requests ADD COLUMN max_attempts integer;

    -- 历史记录未保存配置上限，只能用实际尝试次数作为可证下界，且不改写既有计量结果。
    UPDATE platform.ai_requests SET max_attempts=attempts;
    ALTER TABLE platform.ai_requests ALTER COLUMN max_attempts SET NOT NULL;
    ALTER TABLE platform.ai_requests
      ADD CONSTRAINT ai_requests_max_attempts_check CHECK(max_attempts BETWEEN 1 AND 5);

    ALTER TABLE platform.ai_requests DROP CONSTRAINT IF EXISTS ai_requests_attempts_check;
    ALTER TABLE platform.ai_requests
      ADD CONSTRAINT ai_requests_attempts_check CHECK(attempts BETWEEN 1 AND max_attempts);

    -- 计费审计必须失败关闭：成功请求也至少保留调用前预留的 Token 与费用。
    ALTER TABLE platform.ai_requests
      DROP COLUMN IF EXISTS accounted_tokens,
      DROP COLUMN IF EXISTS accounted_cost_micros;
    ALTER TABLE platform.ai_requests
      ADD COLUMN accounted_tokens bigint GENERATED ALWAYS AS (
        CASE WHEN status='SUCCEEDED' THEN GREATEST(total_tokens::bigint,reserved_tokens) ELSE reserved_tokens END
      ) STORED,
      ADD COLUMN accounted_cost_micros bigint GENERATED ALWAYS AS (
        CASE WHEN status='SUCCEEDED' THEN GREATEST(cost_micros,reserved_cost_micros) ELSE reserved_cost_micros END
      ) STORED;

    -- 旧记录保持不可变；NOT VALID 仍会立即约束迁移后的新增和更新记录。
    ALTER TABLE platform.ai_requests DROP CONSTRAINT IF EXISTS ai_requests_provider_request_id_check;
    ALTER TABLE platform.ai_requests
      ADD CONSTRAINT ai_requests_provider_request_id_check CHECK(
        provider_request_id='' OR provider_request_id ~ '^[0-9a-f]{64}$'
      ) NOT VALID;

    ALTER TABLE platform.ai_requests DROP CONSTRAINT IF EXISTS ai_requests_finish_reason_check;
    ALTER TABLE platform.ai_requests
      ADD CONSTRAINT ai_requests_finish_reason_check CHECK(
        finish_reason IN ('','stop','length','content_filter','tool_calls','function_call','other')
      ) NOT VALID;

    ALTER TABLE platform.ai_requests DROP CONSTRAINT IF EXISTS ai_requests_lifecycle_check;
    ALTER TABLE platform.ai_requests
      ADD CONSTRAINT ai_requests_lifecycle_check CHECK(
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
      ) NOT VALID;
  END IF;
END
$$;

-- 运行中记录只能转换一次为终态；补充保护新增的最大尝试次数字段。
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

COMMENT ON COLUMN platform.ai_requests.max_attempts IS '预留预算时使用的最大 Provider 尝试次数；旧记录以实际尝试次数作为可证下界';
COMMENT ON COLUMN platform.ai_requests.accounted_tokens IS '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭 Token 计量';
COMMENT ON COLUMN platform.ai_requests.accounted_cost_micros IS '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭费用计量';
