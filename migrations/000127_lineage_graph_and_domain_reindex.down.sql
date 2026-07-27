-- 文档领域修正和历史血缘图均为安全性修复，不回写旧的错误 general 标签。
UPDATE platform.semantic_graph_projection_state AS state
SET requested_event_version=
      GREATEST(state.requested_event_version,state.applied_event_version)+1,
    status=CASE WHEN state.status='RUNNING' THEN 'RUNNING' ELSE 'PENDING' END,
    next_attempt_at=now(),error_code='',updated_at=now()
FROM platform.semantic_qa_settings AS settings
WHERE settings.tenant_id=state.tenant_id
  AND settings.enabled AND settings.graph_projection_enabled;
