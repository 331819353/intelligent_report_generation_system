-- 把同领域 DIM/DWD 建模拆成可恢复检查点：
--   1. ODS 角色分类；
--   2. 每张 FACT 的独立 DWD 结构与 DAG。
-- 检查点只保存已经通过本地合同校验的结构化设计，不保存提示词、供应商原始
-- 响应或业务数据行。进程中断后只补缺失阶段，避免重复消耗模型调用。

ALTER TABLE platform.dwd_modeling_jobs
  ADD COLUMN checkpoint_version integer NOT NULL DEFAULT 0
    CHECK(checkpoint_version>=0),
  ADD COLUMN claimed_checkpoint_version integer NOT NULL DEFAULT 0
    CHECK(claimed_checkpoint_version>=0),
  ADD CONSTRAINT dwd_modeling_jobs_checkpoint_claim_check
    CHECK(claimed_checkpoint_version<=checkpoint_version);

CREATE TABLE platform.dwd_modeling_checkpoints(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  job_id uuid NOT NULL,
  checkpoint_kind text NOT NULL
    CHECK(checkpoint_kind IN ('CLASSIFICATION','FACT_DESIGN')),
  subject_dataset_version_id uuid NOT NULL,
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  prompt_version text NOT NULL CHECK(
    length(prompt_version) BETWEEN 1 AND 128
    AND prompt_version=btrim(prompt_version)
    AND prompt_version !~ '[[:cntrl:]]'
  ),
  ai_request_id uuid NOT NULL,
  payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
  payload_json jsonb NOT NULL CHECK(
    jsonb_typeof(payload_json)='object'
    AND pg_column_size(payload_json)<=2097152
    AND platform.materialization_json_is_safe(payload_json)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dwd_modeling_checkpoints_job_fk
    FOREIGN KEY(job_id,tenant_id)
    REFERENCES platform.dwd_modeling_jobs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT dwd_modeling_checkpoints_subject_fk
    FOREIGN KEY(subject_dataset_version_id,tenant_id)
    REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_checkpoints_ai_request_fk
    FOREIGN KEY(ai_request_id,tenant_id)
    REFERENCES platform.ai_requests(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dwd_modeling_checkpoints_job_subject_key
    UNIQUE(
      tenant_id,job_id,checkpoint_kind,subject_dataset_version_id,
      snapshot_hash,prompt_version
    ),
  CONSTRAINT dwd_modeling_checkpoints_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dwd_modeling_checkpoints_resume_idx
  ON platform.dwd_modeling_checkpoints(
    tenant_id,job_id,snapshot_hash,checkpoint_kind,subject_dataset_version_id
  );

CREATE TRIGGER dwd_modeling_checkpoints_set_updated_at
BEFORE UPDATE ON platform.dwd_modeling_checkpoints
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dwd_modeling_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dwd_modeling_checkpoints FORCE ROW LEVEL SECURITY;

CREATE POLICY dwd_modeling_checkpoints_tenant_isolation
  ON platform.dwd_modeling_checkpoints
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的 ODS 分类和逐 FACT DWD 设计检查点；支持进程中断后的精确续跑';
COMMENT ON COLUMN platform.dwd_modeling_jobs.checkpoint_version IS
  '每次首次写入有效检查点递增；用于区分无进展失败与已有实质进展的中断';
COMMENT ON COLUMN platform.dwd_modeling_jobs.claimed_checkpoint_version IS
  '本次租约开始时的检查点版本；有新进展的中断不会耗尽任务尝试额度';
