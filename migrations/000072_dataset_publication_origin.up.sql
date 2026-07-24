-- 将数据集发布来源提升为不可变版本事实。audit_logs 只用于追踪和历史回填，
-- 运行时授权不得再依赖可追加的审计记录。
LOCK TABLE
  platform.datasets,
  platform.dataset_versions,
  platform.dataset_publication_requests,
  platform.audit_logs
IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM platform.dataset_versions WHERE status='PUBLISHING'
  ) THEN
    RAISE EXCEPTION '存在未完成的 PUBLISHING 数据集版本，不能回填发布来源';
  END IF;
END
$$;

ALTER TABLE platform.dataset_versions
  ADD COLUMN publication_origin text;

-- 回填不应改变业务对象更新时间、产生派生任务或触发旧不可变约束。
-- 整张表已由迁移锁独占写入，回填完成后在增加约束前恢复全部用户触发器。
ALTER TABLE platform.dataset_versions DISABLE TRIGGER USER;

UPDATE platform.dataset_versions
SET publication_origin=CASE
  WHEN status='DRAFT' THEN 'UNPUBLISHED'
  ELSE 'LEGACY'
END;

-- 已批准申请是优先级最高的人工发布证明；即使同时存在伪造 AUTO 审计，
-- 也绝不能把人工发布升级成系统可自动覆盖的来源。
UPDATE platform.dataset_versions AS version
SET publication_origin='HUMAN_APPROVAL'
FROM platform.dataset_publication_requests AS request
WHERE request.tenant_id=version.tenant_id
  AND request.dataset_id=version.dataset_id
  AND request.published_version_id=version.id
  AND request.status='APPROVED'
  AND version.status IN ('PUBLISHED','STALE','DEPRECATED');

-- 只有内容与不可变版本逐字段一致、且只指向一种系统来源的完整历史审计，
-- 才能用于一次性前向回填。回填结束后运行时不再读取 audit_logs 作授权判断。
WITH system_matches AS (
  SELECT
    version.id AS version_id,
    CASE audit.action
      WHEN 'AUTO_PUBLISH_MAPPED_DEFAULT' THEN 'SYSTEM_MAPPED_DEFAULT'
      WHEN 'AUTO_REFRESH_MAPPED_DATASET' THEN 'SYSTEM_MAPPED_REFRESH'
      WHEN 'AUTO_REGENERATE_MAPPED_DATASET' THEN 'SYSTEM_MAPPED_REGENERATE'
    END AS publication_origin
  FROM platform.dataset_versions AS version
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
  JOIN platform.audit_logs AS audit
    ON audit.tenant_id=version.tenant_id
   AND audit.resource_type='DATASET'
   AND audit.resource_id=version.dataset_id::text
  WHERE version.status IN ('PUBLISHED','STALE','DEPRECATED')
    AND version.publication_origin='LEGACY'
    AND dataset.origin_table_id IS NOT NULL
    AND NOT EXISTS(
      SELECT 1
      FROM platform.dataset_publication_requests AS approved
      WHERE approved.tenant_id=version.tenant_id
        AND approved.dataset_id=version.dataset_id
        AND approved.published_version_id=version.id
        AND approved.status='APPROVED'
    )
    AND audit.detail->>'originTableId'=dataset.origin_table_id::text
    AND audit.detail->>'publishedVersionId'=version.id::text
    AND audit.detail->>'versionNo'=version.version_no::text
    AND audit.detail->>'dslHash'=version.schema_hash
    AND audit.detail->>'planHash'=version.plan_hash
    AND (
      audit.action='AUTO_PUBLISH_MAPPED_DEFAULT'
        AND audit.detail->>'publicationSource'='SYSTEM_MAPPED_DEFAULT'
      OR audit.action='AUTO_REFRESH_MAPPED_DATASET'
        AND audit.detail->>'publicationSource'='SYSTEM_MAPPED_REFRESH'
      OR audit.action='AUTO_REGENERATE_MAPPED_DATASET'
        AND audit.detail->>'publicationSource'='SYSTEM_MAPPED_REGENERATE'
    )
),
unambiguous_system_matches AS (
  SELECT version_id,min(publication_origin) AS publication_origin
  FROM system_matches
  GROUP BY version_id
  HAVING count(DISTINCT publication_origin)=1
)
UPDATE platform.dataset_versions AS version
SET publication_origin=matched.publication_origin
FROM unambiguous_system_matches AS matched
WHERE version.id=matched.version_id
  AND version.publication_origin='LEGACY';

ALTER TABLE platform.dataset_versions ENABLE TRIGGER USER;

ALTER TABLE platform.dataset_versions
  ALTER COLUMN publication_origin SET DEFAULT 'UNPUBLISHED',
  ALTER COLUMN publication_origin SET NOT NULL,
  ADD CONSTRAINT dataset_versions_publication_origin_check CHECK(
    publication_origin IN (
      'UNPUBLISHED',
      'DIRECT',
      'HUMAN_APPROVAL',
      'SYSTEM_MAPPED_DEFAULT',
      'SYSTEM_MAPPED_REFRESH',
      'SYSTEM_MAPPED_REGENERATE',
      'LEGACY'
    )
  ),
  ADD CONSTRAINT dataset_versions_status_publication_origin_check CHECK(
    status='DRAFT' AND publication_origin='UNPUBLISHED'
    OR status='PUBLISHING' AND publication_origin IN (
      'DIRECT',
      'HUMAN_APPROVAL',
      'SYSTEM_MAPPED_DEFAULT',
      'SYSTEM_MAPPED_REFRESH',
      'SYSTEM_MAPPED_REGENERATE'
    )
    OR status IN ('PUBLISHED','STALE','DEPRECATED') AND publication_origin IN (
      'DIRECT',
      'HUMAN_APPROVAL',
      'SYSTEM_MAPPED_DEFAULT',
      'SYSTEM_MAPPED_REFRESH',
      'SYSTEM_MAPPED_REGENERATE',
      'LEGACY'
    )
  );

CREATE OR REPLACE FUNCTION platform.dataset_publication_origin_facts_match(
  candidate platform.dataset_versions
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT CASE candidate.publication_origin
    WHEN 'DIRECT' THEN true
    WHEN 'HUMAN_APPROVAL' THEN EXISTS(
      SELECT 1
      FROM platform.dataset_publication_requests AS request
      JOIN platform.datasets AS dataset
        ON dataset.id=request.dataset_id AND dataset.tenant_id=request.tenant_id
      WHERE request.tenant_id=candidate.tenant_id
        AND request.dataset_id=candidate.dataset_id
        AND request.status='PENDING'
        AND request.reserved_published_version_id=candidate.id
        AND request.draft_version_id=candidate.source_draft_version_id
        AND request.expected_dataset_version=dataset.version
        AND request.expected_draft_record_version=candidate.source_draft_record_version
        AND request.expected_dsl_hash=candidate.schema_hash
        AND request.expected_plan_hash=candidate.plan_hash
        AND dataset.current_draft_version_id=candidate.source_draft_version_id
        AND dataset.deleted_at IS NULL
    )
    WHEN 'SYSTEM_MAPPED_DEFAULT' THEN EXISTS(
      SELECT 1
      FROM platform.datasets AS dataset
      WHERE dataset.id=candidate.dataset_id
        AND dataset.tenant_id=candidate.tenant_id
        AND dataset.origin_table_id IS NOT NULL
        AND dataset.current_draft_version_id=candidate.source_draft_version_id
        AND dataset.current_published_version_id IS NULL
        AND dataset.status='DRAFT'
        AND dataset.version=1
        AND dataset.deleted_at IS NULL
        AND candidate.layer='ODS'
        AND candidate.version_no=1
        AND NOT EXISTS(
          SELECT 1 FROM platform.dataset_publication_requests AS pending
          WHERE pending.tenant_id=candidate.tenant_id
            AND pending.dataset_id=candidate.dataset_id
            AND pending.status='PENDING'
        )
        AND NOT EXISTS(
          SELECT 1 FROM platform.dataset_versions AS history
          WHERE history.tenant_id=candidate.tenant_id
            AND history.dataset_id=candidate.dataset_id
            AND history.status IN ('PUBLISHED','STALE','DEPRECATED')
        )
    )
    WHEN 'SYSTEM_MAPPED_REFRESH' THEN EXISTS(
      SELECT 1
      FROM platform.datasets AS dataset
      JOIN platform.dataset_versions AS previous
        ON previous.id=dataset.current_published_version_id
       AND previous.dataset_id=dataset.id
       AND previous.tenant_id=dataset.tenant_id
      WHERE dataset.id=candidate.dataset_id
        AND dataset.tenant_id=candidate.tenant_id
        AND dataset.origin_table_id IS NOT NULL
        AND dataset.current_draft_version_id=candidate.source_draft_version_id
        AND dataset.status='PUBLISHED'
        AND dataset.deleted_at IS NULL
        AND candidate.layer='ODS'
        AND previous.status='PUBLISHED'
        AND previous.publication_origin IN (
          'SYSTEM_MAPPED_DEFAULT',
          'SYSTEM_MAPPED_REFRESH',
          'SYSTEM_MAPPED_REGENERATE'
        )
        AND NOT EXISTS(
          SELECT 1 FROM platform.dataset_publication_requests AS pending
          WHERE pending.tenant_id=candidate.tenant_id
            AND pending.dataset_id=candidate.dataset_id
            AND pending.status='PENDING'
        )
    )
    WHEN 'SYSTEM_MAPPED_REGENERATE' THEN EXISTS(
      SELECT 1
      FROM platform.datasets AS dataset
      WHERE dataset.id=candidate.dataset_id
        AND dataset.tenant_id=candidate.tenant_id
        AND dataset.origin_table_id IS NOT NULL
        AND dataset.current_draft_version_id=candidate.source_draft_version_id
        AND dataset.current_published_version_id IS NULL
        AND dataset.status='DRAFT'
        AND dataset.deleted_at IS NULL
        AND candidate.layer='ODS'
        AND (
          SELECT previous.publication_origin
          FROM platform.dataset_versions AS previous
          WHERE previous.tenant_id=candidate.tenant_id
            AND previous.dataset_id=candidate.dataset_id
            AND previous.status IN ('PUBLISHED','STALE','DEPRECATED')
          ORDER BY previous.version_no DESC,previous.id DESC
          LIMIT 1
        ) IN (
          'SYSTEM_MAPPED_DEFAULT',
          'SYSTEM_MAPPED_REFRESH',
          'SYSTEM_MAPPED_REGENERATE'
        )
        AND NOT EXISTS(
          SELECT 1 FROM platform.dataset_publication_requests AS pending
          WHERE pending.tenant_id=candidate.tenant_id
            AND pending.dataset_id=candidate.dataset_id
            AND pending.status='PENDING'
        )
    )
    ELSE false
  END
$$;

REVOKE ALL ON FUNCTION
  platform.dataset_publication_origin_facts_match(platform.dataset_versions)
FROM PUBLIC;

-- publication_origin 进入发布构建态、完成态和终态的不可变比较。
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
    IF NEW.status='PUBLISHING'
      AND (
        NOT platform.dataset_publication_source_matches(NEW)
        OR NOT platform.dataset_publication_origin_facts_match(NEW)
      ) THEN
      RAISE EXCEPTION '发布副本的来源草稿或发布来源事实无效' USING ERRCODE='23514';
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
      NEW.source_draft_version_id,NEW.source_draft_record_version,NEW.publication_origin
    ) IS DISTINCT FROM ROW(
      OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.layer,OLD.dsl_version,OLD.dsl_json,
      OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
      OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
      OLD.source_draft_version_id,OLD.source_draft_record_version,OLD.publication_origin
    ) THEN
      RAISE EXCEPTION '发布完成时不能改写版本快照';
    END IF;
    IF NOT platform.dataset_publication_source_matches(NEW)
      OR NOT platform.dataset_publication_origin_facts_match(NEW) THEN
      RAISE EXCEPTION '发布完成前草稿或发布来源事实已经变化' USING ERRCODE='23514';
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
    NEW.source_draft_version_id,NEW.source_draft_record_version,NEW.publication_origin
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.version_no,OLD.layer,OLD.dsl_version,OLD.dsl_json,
    OLD.schema_hash,OLD.logical_plan_json,OLD.plan_hash,OLD.record_version,
    OLD.created_by,OLD.created_at,OLD.published_at,OLD.published_by,
    OLD.source_draft_version_id,OLD.source_draft_record_version,OLD.publication_origin
  ) THEN
    RAISE EXCEPTION '已发布数据集版本内容不可修改';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_dataset_version_publication() FROM PUBLIC;

COMMENT ON COLUMN platform.dataset_versions.publication_origin IS
  '不可变发布来源：草稿 UNPUBLISHED；新发布为 DIRECT、HUMAN_APPROVAL 或三类 SYSTEM_MAPPED_*；无法证明的历史发布为 LEGACY';
COMMENT ON FUNCTION platform.dataset_publication_origin_facts_match(platform.dataset_versions) IS
  '发布构建态来源事实门禁；不读取 audit_logs，不接受客户端声明作为授权';
