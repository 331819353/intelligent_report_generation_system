-- 回滚时恢复发布后的自动任务登记。已有人工任务仍由唯一约束保持幂等。
CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

CREATE TRIGGER dataset_versions_enqueue_dwd_modeling
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_ods_dwd_modeling();

CREATE TRIGGER dataset_versions_enqueue_dws_modeling
AFTER INSERT OR UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_modeling();

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '发布事务写入的精确数据集版本标签建议 outbox；带租约、fencing 和有界重试';
COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布后执行的同领域 DIM/DWD 分层建模 outbox；带租约、幂等和有界重试';
COMMENT ON TABLE platform.dws_modeling_jobs IS
  'DWD 发布后可恢复的市场分析模板选择与 DWS 草稿任务；依赖等待不消耗失败预算';
