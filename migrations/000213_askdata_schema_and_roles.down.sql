DROP TABLE IF EXISTS askdata.audit_events;
DROP FUNCTION IF EXISTS askdata.reject_immutable_mutation();
DROP FUNCTION IF EXISTS askdata.set_updated_at();
DROP FUNCTION IF EXISTS askdata.json_is_safe(jsonb);
DROP FUNCTION IF EXISTS askdata.domain_can_access(uuid);
DROP FUNCTION IF EXISTS askdata.tenant_matches(uuid);
DROP FUNCTION IF EXISTS askdata.system_access();
DROP FUNCTION IF EXISTS askdata.current_domain_id();
DROP FUNCTION IF EXISTS askdata.current_actor_id();
DROP FUNCTION IF EXISTS askdata.current_tenant_id();
DROP SCHEMA IF EXISTS askdata;

-- Keep immutable SEMANTIC_QUESTION request history valid, but revoke the
-- runtime tenant authorization introduced by the up migration.
ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT ai_tenant_policies_purposes_check;

UPDATE platform.ai_tenant_policies
SET allowed_purposes=array_remove(allowed_purposes,'SEMANTIC_QUESTION');

UPDATE platform.ai_tenant_policies
SET allowed_purposes=ARRAY['METADATA_COMPLETION']::text[]
WHERE cardinality(allowed_purposes)=0;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_purposes_check CHECK(
    cardinality(allowed_purposes) BETWEEN 1 AND 5
    AND array_position(allowed_purposes,NULL) IS NULL
    AND allowed_purposes <@ ARRAY[
      'METADATA_COMPLETION','DATASET_DAG_GENERATION',
      'DATASET_TAG_SUGGESTION','DATASET_SEMANTIC_NAMING',
      'DATA_SOURCE_CONFIGURATION'
    ]::text[]
  );

COMMENT ON COLUMN platform.ai_tenant_policies.allowed_purposes IS
  '租户显式授权的 AI 用途；DATA_SOURCE_CONFIGURATION 只生成配置草稿和诊断，不能代替连接测试、发布审批或用户确认';
