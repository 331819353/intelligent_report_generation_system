-- 数据源发布必须由当前不可变配置版本上、由受信任应用记录的测试证据驱动。
-- 应用服务仍负责编排 test -> publish，但直接更新发布指针也不能绕过版本、摘要
-- 和有效期约束；数据库约束本身不证明外部网络连接已经发生。

CREATE OR REPLACE FUNCTION platform.enforce_data_source_test_evidence()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  expected_hash text;
BEGIN
  SELECT version.config_hash
  INTO expected_hash
  FROM platform.data_source_versions AS version
  JOIN platform.data_sources AS source
    ON source.id=version.data_source_id
   AND source.tenant_id=version.tenant_id
  WHERE version.id=NEW.data_source_version_id
    AND version.data_source_id=NEW.data_source_id
    AND version.tenant_id=NEW.tenant_id
    AND source.current_draft_version_id=version.id
    AND source.deleted_at IS NULL
  FOR SHARE OF version,source;

  IF NOT FOUND
    OR expected_hash IS DISTINCT FROM NEW.config_hash
    OR NEW.started_at>NEW.completed_at THEN
    RAISE EXCEPTION
      '连接测试证据必须绑定当前草稿的精确配置版本和摘要'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_test_evidence() FROM PUBLIC;

CREATE TRIGGER data_source_test_runs_validate_evidence
BEFORE INSERT ON platform.data_source_test_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_data_source_test_evidence();

CREATE OR REPLACE FUNCTION platform.reject_data_source_test_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '连接测试证据不可修改或删除' USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_data_source_test_run_mutation() FROM PUBLIC;

CREATE TRIGGER data_source_test_runs_immutable
BEFORE UPDATE OR DELETE ON platform.data_source_test_runs
FOR EACH ROW EXECUTE FUNCTION platform.reject_data_source_test_run_mutation();

CREATE OR REPLACE FUNCTION platform.reject_direct_published_data_source_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.publication_status<>'UNPUBLISHED'
    OR NEW.current_published_version_id IS NOT NULL THEN
    RAISE EXCEPTION
      '新数据源必须先保存为未发布草稿，再通过测试证据切换发布指针'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_direct_published_data_source_insert() FROM PUBLIC;

CREATE TRIGGER data_sources_reject_direct_published_insert
BEFORE INSERT ON platform.data_sources
FOR EACH ROW EXECUTE FUNCTION platform.reject_direct_published_data_source_insert();

CREATE OR REPLACE FUNCTION platform.enforce_data_source_publication_evidence()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  published_hash text;
BEGIN
  IF NEW.current_published_version_id IS NOT DISTINCT FROM OLD.current_published_version_id
    OR NEW.current_published_version_id IS NULL THEN
    RETURN NEW;
  END IF;

  SELECT version.config_hash
  INTO published_hash
  FROM platform.data_source_versions AS version
  WHERE version.id=NEW.current_published_version_id
    AND version.data_source_id=NEW.id
    AND version.tenant_id=NEW.tenant_id
  FOR SHARE;

  IF NOT FOUND
    OR NEW.current_draft_version_id IS DISTINCT FROM NEW.current_published_version_id
    OR NEW.publication_status<>'PUBLISHED'
    OR NEW.validation_status<>'PASSED'
    OR NEW.last_tested_version_id IS DISTINCT FROM NEW.current_published_version_id
    OR NEW.last_tested_config_hash IS DISTINCT FROM published_hash
    OR NEW.test_expires_at IS NULL
    OR NEW.test_expires_at<=clock_timestamp()
    OR NOT EXISTS(
      SELECT 1
      FROM platform.data_source_test_runs AS test_run
      WHERE test_run.data_source_id=NEW.id
        AND test_run.tenant_id=NEW.tenant_id
        AND test_run.data_source_version_id=NEW.current_published_version_id
        AND test_run.config_hash=published_hash
        AND test_run.status='PASSED'
        AND test_run.expires_at>clock_timestamp()
    ) THEN
    RAISE EXCEPTION
      '发布指针只能切换到当前草稿且必须具备同版本、同摘要、未过期的成功测试'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_publication_evidence() FROM PUBLIC;

CREATE TRIGGER data_sources_require_publication_evidence
BEFORE UPDATE OF current_published_version_id ON platform.data_sources
FOR EACH ROW EXECUTE FUNCTION platform.enforce_data_source_publication_evidence();

COMMENT ON FUNCTION platform.enforce_data_source_publication_evidence() IS
  'Rejects direct publication-pointer changes without an exact current-version, current-hash and unexpired passed connection test';
