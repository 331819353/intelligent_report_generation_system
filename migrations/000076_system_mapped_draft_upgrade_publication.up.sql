-- LLM 失败时会先创建可人工编辑的 ODS 降级草稿。后续增量补全只允许系统
-- 刷新未被人工触碰的同一草稿；当元数据最终完整时，该草稿的 record_version
-- 可能大于 1。发布来源事实不能再用 dataset.version=1 判断“初始草稿”，而应
-- 复核当前草稿身份、精确修订链、哈希和零审批历史。
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
      JOIN platform.dataset_versions AS draft
        ON draft.id=dataset.current_draft_version_id
       AND draft.dataset_id=dataset.id
       AND draft.tenant_id=dataset.tenant_id
      WHERE dataset.id=candidate.dataset_id
        AND dataset.tenant_id=candidate.tenant_id
        AND dataset.origin_table_id IS NOT NULL
        AND dataset.current_draft_version_id=candidate.source_draft_version_id
        AND dataset.current_published_version_id IS NULL
        AND dataset.status='DRAFT'
        AND dataset.version=candidate.source_draft_record_version
        AND dataset.deleted_at IS NULL
        AND draft.status='DRAFT'
        AND draft.record_version=candidate.source_draft_record_version
        AND draft.schema_hash=candidate.schema_hash
        AND draft.plan_hash=candidate.plan_hash
        AND candidate.source_draft_record_version>0
        AND candidate.layer='ODS'
        AND candidate.version_no=1
        AND (
          SELECT count(*)
          FROM platform.dataset_draft_revisions AS revision
          WHERE revision.tenant_id=candidate.tenant_id
            AND revision.dataset_id=candidate.dataset_id
            AND revision.draft_version_id=candidate.source_draft_version_id
        )=candidate.source_draft_record_version
        AND (
          SELECT count(*)
          FROM platform.dataset_draft_revisions AS revision
          WHERE revision.tenant_id=candidate.tenant_id
            AND revision.dataset_id=candidate.dataset_id
            AND revision.draft_version_id=candidate.source_draft_version_id
            AND revision.operation_type='CREATE'
        )=1
        AND NOT EXISTS(
          SELECT 1
          FROM platform.dataset_draft_revisions AS revision
          WHERE revision.tenant_id=candidate.tenant_id
            AND revision.dataset_id=candidate.dataset_id
            AND revision.draft_version_id=candidate.source_draft_version_id
            AND revision.operation_type='ROLLBACK'
        )
        AND NOT EXISTS(
          SELECT 1 FROM platform.dataset_publication_requests AS request
          WHERE request.tenant_id=candidate.tenant_id
            AND request.dataset_id=candidate.dataset_id
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

COMMENT ON FUNCTION
  platform.dataset_publication_origin_facts_match(platform.dataset_versions)
IS '以关系事实验证数据集发布来源；SYSTEM_MAPPED_DEFAULT 允许零人工操作的系统增量草稿升级';
