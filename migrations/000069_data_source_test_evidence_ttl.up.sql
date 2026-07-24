-- 收紧连接测试证据的数据库时间边界。应用记录的成功测试固定为 30 分钟，
-- 不能通过直接写入任意远期 expires_at 延长发布窗口。

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
    OR NEW.started_at>NEW.completed_at
    OR NEW.started_at<NEW.completed_at-interval '15 minutes'
    OR NEW.completed_at>clock_timestamp()+interval '5 seconds'
    OR (
      NEW.status='PASSED'
      AND NEW.expires_at IS DISTINCT FROM
        NEW.completed_at+interval '30 minutes'
    )
    OR (NEW.status='FAILED' AND NEW.expires_at IS NOT NULL) THEN
    RAISE EXCEPTION
      '连接测试证据必须绑定当前草稿的精确配置，并使用可信的短期时间窗口'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_test_evidence() FROM PUBLIC;

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
        AND test_run.expires_at IS NOT DISTINCT FROM
          test_run.completed_at+interval '30 minutes'
        AND test_run.expires_at IS NOT DISTINCT FROM NEW.test_expires_at
        AND test_run.completed_at<=clock_timestamp()+interval '5 seconds'
        AND test_run.expires_at>clock_timestamp()
    ) THEN
    RAISE EXCEPTION
      '发布指针只能切换到当前草稿且必须具备同版本、同摘要、30 分钟内的成功测试'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_publication_evidence() FROM PUBLIC;

COMMENT ON FUNCTION platform.enforce_data_source_test_evidence() IS
  'Validates exact current-version evidence and a fixed 30-minute successful-test TTL';
COMMENT ON FUNCTION platform.enforce_data_source_publication_evidence() IS
  'Rejects publication-pointer changes without matching current-version evidence in its fixed 30-minute window';
