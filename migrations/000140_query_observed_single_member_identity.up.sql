BEGIN;

UPDATE platform.dimension_where_decisions AS decision
SET dimension_member_id=member.id
FROM platform.dimension_members AS member
WHERE decision.tenant_id=member.tenant_id
  AND decision.dimension_id=member.dimension_id
  AND decision.source_type='QUERY_OBSERVED'
  AND decision.selected_member_count=1
  AND decision.dimension_member_id IS NULL
  AND member.status='ACTIVE'
  AND decision.selected_member_set_hash=encode(public.digest(
    convert_to(member.normalized_value,'UTF8'),'sha256'
  ),'hex');

COMMENT ON COLUMN platform.dimension_where_decisions.dimension_member_id IS
  '单成员预计算或问答观察决策关联的规范成员；多成员问答决策为空';

COMMIT;
