-- 维度值决策图只允许真实业务成员进入分支。
-- DWS 建模默认值 UNKNOWN / 999999999 / 1970-01-01 在数据库边界统一排除；
-- 布尔 False / True 是有效业务值，明确不属于本规则。

CREATE OR REPLACE FUNCTION platform.is_reserved_dimension_default(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
RETURNS NULL ON NULL INPUT
SET search_path=pg_catalog
AS $$
  SELECT lower(pg_catalog.btrim(value)) ~
    '^(unknown|\+?999999999(\.0+)?|1970-01-01([ t]00:00:00(\.0+)?(z|[+-]00(:?00)?)?)?)$'
$$;

REVOKE ALL ON FUNCTION platform.is_reserved_dimension_default(text) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    GRANT EXECUTE ON FUNCTION platform.is_reserved_dimension_default(text)
      TO report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION platform.is_reserved_dimension_default(text)
      TO report_worker;
  END IF;
END
$$;

-- 历史快照不物理删除，保留为 DEPRECATED 审计证据；读取和检索接口只返回 ACTIVE。
UPDATE platform.dimension_members
SET status='DEPRECATED',updated_at=clock_timestamp()
WHERE status='ACTIVE'
  AND platform.is_reserved_dimension_default(normalized_value);

UPDATE platform.semantic_dimensions AS dimension
SET member_count=(
      SELECT count(*)::bigint
      FROM platform.dimension_members AS member
      WHERE member.tenant_id=dimension.tenant_id
        AND member.dimension_id=dimension.id
        AND member.status='ACTIVE'
        AND member.refresh_generation=dimension.member_refresh_generation
    ),
    updated_at=clock_timestamp()
WHERE dimension.member_refresh_generation IS NOT NULL;

ALTER TABLE platform.dimension_members
  ADD CONSTRAINT dimension_members_reserved_default_inactive_check
  CHECK(
    status<>'ACTIVE'
    OR NOT platform.is_reserved_dimension_default(normalized_value)
  ) NOT VALID;

ALTER TABLE platform.dimension_members
  VALIDATE CONSTRAINT dimension_members_reserved_default_inactive_check;

-- 000116 取消了人工候选发现时连带移除的画像触发器。程序分类成为正式入口后，
-- 重新启用“只画像、不建候选”的成员策略链路。
DROP TRIGGER IF EXISTS dataset_materializations_00_enqueue_dimension_profiles
  ON platform.dataset_materializations;
CREATE TRIGGER dataset_materializations_00_enqueue_dimension_profiles
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_active_dws_dimension_profiles();

-- 当前 ACTIVE DWS 只登记画像任务；业务值扫描仍由有界 worker 异步执行。
DO $$
DECLARE
  materialization_record record;
BEGIN
  FOR materialization_record IN
    SELECT tenant_id,dataset_id,dataset_version_id,id
    FROM platform.dataset_materializations
    WHERE layer='DWS' AND status='ACTIVE'
    ORDER BY tenant_id,dataset_id,id
  LOOP
    PERFORM platform.enqueue_dws_dimension_profiles(
      materialization_record.tenant_id,
      materialization_record.dataset_id,
      materialization_record.dataset_version_id,
      materialization_record.id
    );
  END LOOP;
END
$$;

COMMENT ON FUNCTION platform.is_reserved_dimension_default(text) IS
  '维度成员门禁：排除 UNKNOWN、999999999 和 1970-01-01；False/True 保留';
