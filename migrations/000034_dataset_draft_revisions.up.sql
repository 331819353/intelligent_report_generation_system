-- 为每次数据集草稿创建、保存和回滚保留完整、不可变的可恢复快照。
CREATE TABLE platform.dataset_draft_revisions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  version_no bigint NOT NULL CHECK(version_no>0),
  operation_type text NOT NULL CHECK(operation_type IN ('CREATE','SAVE','ROLLBACK')),
  source_revision_id uuid,
  name text NOT NULL CHECK(btrim(name)<>''),
  description text NOT NULL DEFAULT '',
  dataset_type text NOT NULL CHECK(dataset_type IN ('SINGLE_SOURCE','CROSS_SOURCE')),
  draft_version_id uuid NOT NULL,
  draft_record_version bigint NOT NULL CHECK(draft_record_version>0),
  dsl_version text NOT NULL CHECK(dsl_version='1.0'),
  dsl_json jsonb NOT NULL CHECK(jsonb_typeof(dsl_json)='object'),
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  logical_plan_json jsonb NOT NULL CHECK(jsonb_typeof(logical_plan_json)='object'),
  plan_hash text NOT NULL CHECK(plan_hash ~ '^[0-9a-f]{64}$'),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dataset_draft_revisions_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_draft_revisions_draft_fk
    FOREIGN KEY(draft_version_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_versions(id,dataset_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dataset_draft_revisions_actor_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (created_by),
  CONSTRAINT dataset_draft_revisions_identity_key
    UNIQUE(id,dataset_id,tenant_id),
  CONSTRAINT dataset_draft_revisions_dataset_version_key
    UNIQUE(tenant_id,dataset_id,version_no),
  CONSTRAINT dataset_draft_revisions_rollback_source_check CHECK(
    (operation_type='ROLLBACK' AND source_revision_id IS NOT NULL)
    OR (operation_type IN ('CREATE','SAVE') AND source_revision_id IS NULL)
  ),
  CONSTRAINT dataset_draft_revisions_source_fk
    FOREIGN KEY(source_revision_id,dataset_id,tenant_id)
    REFERENCES platform.dataset_draft_revisions(id,dataset_id,tenant_id) ON DELETE RESTRICT
);

-- 迁移前的历史草稿正文已经被原地覆盖，无法可信补造；只回填当前可恢复快照，
-- 并沿用数据集聚合版本号。后续发布和生命周期操作造成的版本号间隙会被保留。
INSERT INTO platform.dataset_draft_revisions(
  tenant_id,dataset_id,version_no,operation_type,name,description,dataset_type,
  draft_version_id,draft_record_version,dsl_version,dsl_json,schema_hash,
  logical_plan_json,plan_hash,created_by,created_at
)
SELECT
  dataset.tenant_id,dataset.id,dataset.version,'SAVE',dataset.name,dataset.description,dataset.dataset_type,
  draft.id,draft.record_version,draft.dsl_version,draft.dsl_json,draft.schema_hash,
  draft.logical_plan_json,draft.plan_hash,
  COALESCE(draft.updated_by,draft.created_by,dataset.updated_by,dataset.created_by),
  GREATEST(dataset.updated_at,draft.updated_at)
FROM platform.datasets AS dataset
JOIN platform.dataset_versions AS draft
  ON draft.id=dataset.current_draft_version_id
 AND draft.dataset_id=dataset.id
 AND draft.tenant_id=dataset.tenant_id
WHERE draft.status='DRAFT';

CREATE INDEX dataset_draft_revisions_history_idx
  ON platform.dataset_draft_revisions(tenant_id,dataset_id,version_no DESC,id);
CREATE INDEX dataset_draft_revisions_source_idx
  ON platform.dataset_draft_revisions(tenant_id,source_revision_id)
  WHERE source_revision_id IS NOT NULL;

CREATE OR REPLACE FUNCTION platform.reject_dataset_draft_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '数据集草稿历史修订不可修改或删除';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_dataset_draft_revision_mutation() FROM PUBLIC;

CREATE TRIGGER dataset_draft_revisions_immutable
BEFORE UPDATE OR DELETE ON platform.dataset_draft_revisions
FOR EACH ROW EXECUTE FUNCTION platform.reject_dataset_draft_revision_mutation();

ALTER TABLE platform.dataset_draft_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dataset_draft_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY dataset_draft_revisions_tenant_isolation
  ON platform.dataset_draft_revisions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dataset_draft_revisions IS '每次数据集草稿创建、保存或回滚产生的不可变完整快照';
COMMENT ON COLUMN platform.dataset_draft_revisions.version_no IS '产生快照时的数据集聚合版本号，允许发布和生命周期操作造成间隙';
COMMENT ON COLUMN platform.dataset_draft_revisions.source_revision_id IS 'ROLLBACK 修订所恢复的精确历史修订；历史修订自身保持不可变';
