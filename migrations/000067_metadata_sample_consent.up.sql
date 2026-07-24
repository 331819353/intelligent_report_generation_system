-- 原始业务样本进入外部模型前必须经过独立租户策略和逐任务明确同意。
-- 默认 DENY；MASK 只发送格式保持的占位值；RAW 需要租户策略显式设为 RAW。

ALTER TABLE platform.ai_tenant_policies
  ADD COLUMN metadata_sample_mode text NOT NULL DEFAULT 'DENY'
    CHECK(metadata_sample_mode IN ('DENY','MASK','RAW'));

COMMENT ON COLUMN platform.ai_tenant_policies.metadata_sample_mode IS
  '元数据补全样本上限：DENY 不采样，MASK 仅发送格式占位值，RAW 允许明确同意的原值';

ALTER TABLE platform.data_source_metadata_jobs
  ADD COLUMN sample_data_mode text NOT NULL DEFAULT 'DENY'
    CHECK(sample_data_mode IN ('DENY','MASK','RAW')),
  ADD COLUMN sample_policy_version bigint NOT NULL DEFAULT 1
    CHECK(sample_policy_version>0),
  ADD COLUMN sample_consent_by uuid,
  ADD COLUMN sample_consent_at timestamptz,
  ADD CONSTRAINT data_source_metadata_jobs_sample_consent_by_fk
    FOREIGN KEY(sample_consent_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT data_source_metadata_jobs_sample_consent_shape_check CHECK(
    (sample_data_mode='DENY'
      AND sample_consent_by IS NULL AND sample_consent_at IS NULL)
    OR
    (sample_data_mode IN ('MASK','RAW')
      AND sample_consent_by IS NOT NULL AND sample_consent_at IS NOT NULL)
  );

-- 迁移前仍在队列中的任务只继承 DENY，并绑定迁移时的当前策略版本；
-- 不能用硬编码 version=1 误判已多次调整过 AI 策略的租户。
UPDATE platform.data_source_metadata_jobs AS job
SET sample_policy_version=policy.version
FROM platform.ai_tenant_policies AS policy
WHERE policy.tenant_id=job.tenant_id;

CREATE OR REPLACE FUNCTION platform.enforce_metadata_sample_consent()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  policy_mode text;
  policy_version bigint;
  policy_rank integer;
  requested_rank integer;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF ROW(
      NEW.sample_data_mode,NEW.sample_policy_version,
      NEW.sample_consent_by,NEW.sample_consent_at
    ) IS DISTINCT FROM ROW(
      OLD.sample_data_mode,OLD.sample_policy_version,
      OLD.sample_consent_by,OLD.sample_consent_at
    ) THEN
      RAISE EXCEPTION '元数据样本授权快照不可修改'
        USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  SELECT metadata_sample_mode,version
  INTO policy_mode,policy_version
  FROM platform.ai_tenant_policies
  WHERE tenant_id=NEW.tenant_id
  FOR SHARE;
  IF NOT FOUND OR NEW.sample_policy_version<>policy_version THEN
    RAISE EXCEPTION '元数据样本策略版本已变化'
      USING ERRCODE='23514';
  END IF;

  policy_rank := CASE policy_mode WHEN 'DENY' THEN 0 WHEN 'MASK' THEN 1 ELSE 2 END;
  requested_rank := CASE NEW.sample_data_mode
    WHEN 'DENY' THEN 0 WHEN 'MASK' THEN 1 ELSE 2 END;
  IF requested_rank>policy_rank THEN
    RAISE EXCEPTION '请求的元数据样本模式超出租户策略'
      USING ERRCODE='23514';
  END IF;
  IF NEW.sample_data_mode<>'DENY' THEN
    IF NEW.requested_by IS NULL
      OR NEW.sample_consent_by IS DISTINCT FROM NEW.requested_by THEN
      RAISE EXCEPTION '元数据样本任务必须由请求人逐任务明确同意'
        USING ERRCODE='23514';
    END IF;
    PERFORM 1
    FROM platform.users
    WHERE id=NEW.sample_consent_by
      AND tenant_id=NEW.tenant_id
      AND status='ACTIVE' AND deleted_at IS NULL
    FOR SHARE;
    IF NOT FOUND THEN
      RAISE EXCEPTION '元数据样本授权人不可用'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_metadata_sample_consent() FROM PUBLIC;

CREATE TRIGGER data_source_metadata_jobs_enforce_sample_consent
BEFORE INSERT OR UPDATE OF
  sample_data_mode,sample_policy_version,sample_consent_by,sample_consent_at
ON platform.data_source_metadata_jobs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_metadata_sample_consent();

COMMENT ON COLUMN platform.data_source_metadata_jobs.sample_data_mode IS
  '创建任务时冻结的实际样本处理模式；默认 DENY，不可修改';
COMMENT ON COLUMN platform.data_source_metadata_jobs.sample_policy_version IS
  '创建任务时冻结的 ai_tenant_policies.version，worker 在执行前重新校验撤权';
COMMENT ON COLUMN platform.data_source_metadata_jobs.sample_consent_by IS
  'MASK/RAW 的逐任务授权人；必须与 requested_by 相同';
