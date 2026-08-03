BEGIN;

ALTER TABLE platform.semantic_query_feedback
  ADD COLUMN issue_type text NOT NULL DEFAULT 'NONE'
    CHECK(issue_type IN (
      'NONE','METRIC_DEFINITION','FILTER','RESULT_VALUE','PERMISSION',
      'FRESHNESS','EXPRESSION','OTHER'
    ));

UPDATE platform.semantic_query_feedback
SET issue_type='OTHER'
WHERE rating='INACCURATE' AND issue_type='NONE';

ALTER TABLE platform.semantic_query_feedback
  ADD CONSTRAINT semantic_query_feedback_issue_shape_check CHECK(
    (rating='ACCURATE' AND issue_type='NONE')
    OR
    (rating='INACCURATE' AND issue_type<>'NONE')
  );

CREATE INDEX semantic_query_feedback_issue_idx
  ON platform.semantic_query_feedback(
    tenant_id,issue_type,updated_at DESC
  ) WHERE rating='INACCURATE';

COMMENT ON COLUMN platform.semantic_query_feedback.issue_type IS
  '结构化运营信号：口径、筛选、数字、权限、新鲜度、表达或其他；不作为黄金准确率标签';

COMMIT;
