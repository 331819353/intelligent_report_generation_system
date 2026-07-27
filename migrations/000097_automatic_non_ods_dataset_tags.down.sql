-- 回滚到 000096：标签与分层建模均不再随发布自动登记。
DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '数据集中心人工提交的精确非 ODS 发布版本标签建议 outbox；带租约、fencing 和有界重试';
