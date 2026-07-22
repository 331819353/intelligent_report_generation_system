-- 为确定性语义类型相容性门禁增加可审计的待确认原因。
ALTER TABLE platform.ai_metadata_suggestions
  DROP CONSTRAINT ai_metadata_suggestions_pending_reason_check;

ALTER TABLE platform.ai_metadata_suggestions
  ADD CONSTRAINT ai_metadata_suggestions_pending_reason_check
  CHECK(pending_reason IN ('','LOW_CONFIDENCE','MANUAL_LOCKED','VERSION_CHANGED','SEMANTIC_TYPE_INCOMPATIBLE'));

COMMENT ON CONSTRAINT ai_metadata_suggestions_pending_reason_check
  ON platform.ai_metadata_suggestions IS
  'Incompatible semantic and canonical types remain pending and cannot be auto-applied';
