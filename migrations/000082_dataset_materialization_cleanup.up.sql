-- DWD/DWS 数据集采用控制面软删除、仓库物理清理的跨库 outbox。
-- API 只登记任务，不持有仓库写权限；worker 使用 warehouse worker 账号删除
-- 稳定视图、历史视图和精确匹配的物理表。
CREATE TABLE platform.dataset_materialization_cleanup_jobs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  layer text NOT NULL CHECK(layer IN ('DWD','DWS')),
  requested_by uuid NOT NULL,
  status text NOT NULL DEFAULT 'QUEUED'
    CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
  expected_count integer NOT NULL CHECK(expected_count>0),
  deleted_count integer NOT NULL DEFAULT 0 CHECK(
    deleted_count>=0 AND deleted_count<=expected_count
  ),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt>=0),
  max_attempts integer NOT NULL DEFAULT 3 CHECK(max_attempts BETWEEN 1 AND 10),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128
    AND lease_owner=btrim(lease_owner)
    AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  error_code text NOT NULL DEFAULT '' CHECK(
    error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{1,127}$'
  ),
  error_message text NOT NULL DEFAULT '' CHECK(
    length(error_message)<=2048
    AND error_message !~ '[[:cntrl:]]'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  CONSTRAINT dataset_materialization_cleanup_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_materialization_cleanup_actor_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_materialization_cleanup_attempt_budget_check
    CHECK(attempt<=max_attempts),
  CONSTRAINT dataset_materialization_cleanup_lease_shape_check CHECK(
    (status='QUEUED'
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND completed_at IS NULL)
    OR
    (status='RUNNING'
      AND attempt>0 AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL AND started_at IS NOT NULL
      AND completed_at IS NULL)
    OR
    (status IN ('SUCCEEDED','FAILED')
      AND lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL
      AND completed_at IS NOT NULL)
  ),
  CONSTRAINT dataset_materialization_cleanup_dataset_key
    UNIQUE(tenant_id,dataset_id),
  CONSTRAINT dataset_materialization_cleanup_identity_tenant_key
    UNIQUE(id,tenant_id)
);

CREATE INDEX dataset_materialization_cleanup_claim_idx
  ON platform.dataset_materialization_cleanup_jobs(
    tenant_id,status,next_attempt_at,lease_expires_at,created_at,id
  )
  WHERE status IN ('QUEUED','RUNNING');

CREATE TRIGGER dataset_materialization_cleanup_set_updated_at
BEFORE UPDATE ON platform.dataset_materialization_cleanup_jobs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dataset_materialization_cleanup_jobs
  ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_materialization_cleanup_jobs
  FORCE ROW LEVEL SECURITY;

CREATE POLICY dataset_materialization_cleanup_tenant_isolation
  ON platform.dataset_materialization_cleanup_jobs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dataset_materialization_cleanup_jobs IS
  'DWD/DWS 数据集删除后，由仓库 worker 清理稳定视图、历史视图和物理表的跨库 outbox';
COMMENT ON COLUMN platform.dataset_materialization_cleanup_jobs.expected_count IS
  '删除事务中捕获的 ACTIVE/RETIRED 物化记录数，用于清理完整性核对';
