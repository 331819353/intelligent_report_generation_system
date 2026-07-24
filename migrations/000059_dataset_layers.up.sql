-- 数据集层级与物理来源类型正交。历史 DSL 不改写正文或摘要，避免破坏已发布
-- 版本、草稿修订及指标候选引用；列值使用与服务端 InferLayer 相同的确定性规则回填。
ALTER TABLE platform.dataset_versions ADD COLUMN layer text;
ALTER TABLE platform.datasets ADD COLUMN layer text;

-- 层级回填是结构迁移，不应伪造业务对象的最近修改时间。
-- 已发布/失效/废弃版本的不可变触发器会拒绝同状态 UPDATE；仅在本次受控
-- 回填窗口暂停，回填后立即恢复，再由本迁移下方的新函数把 layer 纳入快照。
ALTER TABLE platform.dataset_versions DISABLE TRIGGER dataset_versions_set_updated_at;
ALTER TABLE platform.dataset_versions DISABLE TRIGGER dataset_versions_enforce_publication;
ALTER TABLE platform.datasets DISABLE TRIGGER datasets_set_updated_at;

UPDATE platform.dataset_versions
SET layer=CASE
  WHEN COALESCE(jsonb_array_length(dsl_json->'groupBy'),0)>0
    OR COALESCE(jsonb_array_length(dsl_json->'having'),0)>0
    OR COALESCE(jsonb_array_length(dsl_json->'preAggregations'),0)>0
    OR jsonb_path_exists(dsl_json, '$.** ? (@.type == "AGGREGATE")')
    THEN 'DWS'
  WHEN COALESCE(jsonb_array_length(dsl_json->'nodes'),0)=1
    AND dsl_json#>>'{nodes,0,type}'='TABLE'
    AND COALESCE(jsonb_array_length(dsl_json->'joins'),0)=0
    THEN 'ODS'
  ELSE 'DWD'
END;

UPDATE platform.datasets AS dataset
SET layer=draft.layer
FROM platform.dataset_versions AS draft
WHERE draft.id=dataset.current_draft_version_id
  AND draft.dataset_id=dataset.id
  AND draft.tenant_id=dataset.tenant_id;

ALTER TABLE platform.dataset_versions ENABLE TRIGGER dataset_versions_set_updated_at;
ALTER TABLE platform.dataset_versions ENABLE TRIGGER dataset_versions_enforce_publication;
ALTER TABLE platform.datasets ENABLE TRIGGER datasets_set_updated_at;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM platform.dataset_versions WHERE layer IS NULL)
    OR EXISTS(SELECT 1 FROM platform.datasets WHERE layer IS NULL) THEN
    RAISE EXCEPTION '无法为历史数据集确定性回填层级';
  END IF;
END
$$;

ALTER TABLE platform.dataset_versions
  ALTER COLUMN layer SET NOT NULL,
  ADD CONSTRAINT dataset_versions_layer_check CHECK(layer IN ('ODS','DWD','DWS'));

ALTER TABLE platform.datasets
  ALTER COLUMN layer SET NOT NULL,
  ADD CONSTRAINT datasets_layer_check CHECK(layer IN ('ODS','DWD','DWS'));

CREATE INDEX datasets_tenant_layer_status_idx
  ON platform.datasets(tenant_id,layer,status,updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX dataset_versions_tenant_layer_status_idx
  ON platform.dataset_versions(tenant_id,layer,status,updated_at DESC);

-- PUBLISHING 快照必须和指定草稿的层级一致。
CREATE OR REPLACE FUNCTION platform.dataset_publication_source_matches(candidate platform.dataset_versions)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.dataset_versions AS draft
    WHERE draft.id=candidate.source_draft_version_id
      AND draft.dataset_id=candidate.dataset_id
      AND draft.tenant_id=candidate.tenant_id
      AND draft.status='DRAFT'
      AND draft.record_version=candidate.source_draft_record_version
      AND draft.layer=candidate.layer
      AND draft.dsl_version=candidate.dsl_version
      AND draft.dsl_json=candidate.dsl_json
      AND draft.schema_hash=candidate.schema_hash
      AND draft.logical_plan_json=candidate.logical_plan_json
      AND draft.plan_hash=candidate.plan_hash
  )
$$;

REVOKE ALL ON FUNCTION platform.dataset_publication_source_matches(platform.dataset_versions) FROM PUBLIC;

-- 把 layer 纳入发布版本的不可变内容；草稿仍可通过保存新修订调整层级。
CREATE OR REPLACE FUNCTION platform.enforce_dataset_version_publication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NEW.status NOT IN ('DRAFT','PUBLISHING') THEN
      RAISE EXCEPTION '数据集版本必须从草稿或发布构建态创建' USING ERRCODE='23514';
    END IF;
    IF NEW.status='PUBLISHING' AND NOT platform.dataset_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '发布副本与指定草稿修订不一致' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP='DELETE' THEN
    IF OLD.status IN ('PUBLISHED','STALE','DEPRECATED') THEN
      RAISE EXCEPTION '已发布数据集版本不可删除';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '数据集版本身份不可修改';
  END IF;

  IF OLD.status='DRAFT' THEN
    IF NEW.status<>'DRAFT' THEN
      RAISE EXCEPTION '草稿不能原地转换为发布版本' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status='PUBLISHING' THEN
    IF NEW.status<>'PUBLISHED' THEN
      RAISE EXCEPTION '发布构建态只能转换为已发布状态' USING ERRCODE='23514';
    END IF;
    IF ROW(
      NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.version_no,NEW.layer,NEW.dsl_version,NEW.dsl_json,
      NEW.schema_hash,NEW.logical_plan_json,NEW.plan_hash,NEW.record_version,
      NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
      NEW.source_draft_version_id,NEW.source_draft_record_version
    ) IS DISTINCT FROM ROW(
      OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.layer,OLD.dsl_version,OLD.dsl_json,
      OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
      OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
      OLD.source_draft_version_id,OLD.source_draft_record_version
    ) THEN
      RAISE EXCEPTION '发布完成时不能改写版本快照';
    END IF;
    IF NOT platform.dataset_publication_source_matches(NEW) THEN
      RAISE EXCEPTION '发布完成前草稿已经变化' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  IF NOT (
    OLD.status='PUBLISHED' AND NEW.status IN ('STALE','DEPRECATED')
    OR OLD.status='STALE' AND NEW.status='DEPRECATED'
  ) THEN
    RAISE EXCEPTION '数据集发布版本状态转换无效' USING ERRCODE='23514';
  END IF;

  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.version_no,NEW.layer,NEW.dsl_version,NEW.dsl_json,
    NEW.schema_hash,NEW.logical_plan_json,NEW.plan_hash,NEW.record_version,
    NEW.created_by,NEW.created_at,NEW.published_at,NEW.published_by,
    NEW.source_draft_version_id,NEW.source_draft_record_version
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.layer,OLD.dsl_version,OLD.dsl_json,
    OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
    OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
    OLD.source_draft_version_id,OLD.source_draft_record_version
  ) THEN
    RAISE EXCEPTION '已发布数据集版本内容不可修改';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_version_publication() FROM PUBLIC;

COMMENT ON COLUMN platform.datasets.layer IS '当前草稿的数仓加工层级摘要：ODS、DWD 或 DWS';
COMMENT ON COLUMN platform.dataset_versions.layer IS '该精确数据集版本的不可变数仓加工层级';
