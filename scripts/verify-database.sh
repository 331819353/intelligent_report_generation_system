#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# 与迁移脚本保持相同的环境文件选择规则。
if [ -z "${ENV_FILE:-}" ]; then
  if [ -f "$ROOT_DIR/.env" ]; then
    ENV_FILE="$ROOT_DIR/.env"
  else
    ENV_FILE="$ROOT_DIR/.env.example"
  fi
fi

cd "$ROOT_DIR"
set -a
. "$ENV_FILE"
set +a

# 使用应用账号运行事务化验证，覆盖 RLS、审计不可变性与策略隔离。
docker compose --env-file "$ENV_FILE" exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U "${POSTGRES_APP_USER:-report_app}" -d "${POSTGRES_DB:-intelligent_report}" <<'SQL'
BEGIN;

DO $$
DECLARE
  tenant_a uuid;
  tenant_b uuid;
  user_a uuid;
  user_b uuid;
  role_a uuid;
  visible_count integer;
  cross_tenant_rejected boolean := false;
  audit_immutable boolean := false;
  report_revision_immutable boolean := false;
  report_revision_constraint boolean := false;
  row_policy_visible integer;
  report_visible integer;
  report_a uuid;
  invalid_subject_rejected boolean := false;
  ai_request_a uuid;
  ai_request_failed uuid;
  ai_request_canceled uuid;
  ai_request_status text;
  ai_policy_enabled boolean;
  ai_policy_purpose_count integer;
  ai_policy_default_only boolean;
  ai_policy_version bigint;
  ai_policy_visible integer;
  ai_request_visible integer;
  ai_request_immutable boolean := false;
  ai_request_constraint boolean := false;
  ai_accounted_tokens bigint;
  ai_accounted_cost_micros bigint;
  metric_dataset_a uuid;
  metric_dataset_version_a uuid;
  metric_a uuid;
  metric_version_a uuid;
  metric_visible integer;
  metric_semantic_visible integer;
  metric_semantic_shape_rejected boolean := false;
  metric_cross_tenant_rejected boolean := false;
  asset_source_a uuid;
  asset_table_a uuid;
  asset_column_a uuid;
  asset_embedding_visible integer;
  asset_outbox_visible integer;
  table_event_version_before bigint;
  column_event_version_before bigint;
  table_event_version_after bigint;
  column_event_version_after bigint;
BEGIN
  -- 构造两个租户的最小数据集，验证跨租户访问始终被拒绝。
  INSERT INTO platform.tenants(code, name) VALUES ('verify-a', 'Verify Tenant A') RETURNING id INTO tenant_a;
  INSERT INTO platform.tenants(code, name) VALUES ('verify-b', 'Verify Tenant B') RETURNING id INTO tenant_b;

  PERFORM set_config('app.tenant_id', tenant_a::text, true);
  INSERT INTO platform.users(tenant_id, email, display_name, password_hash)
  VALUES (tenant_a, 'a@example.test', 'User A', 'test-hash') RETURNING id INTO user_a;
  INSERT INTO platform.roles(tenant_id, code, name)
  VALUES (tenant_a, 'viewer', 'Viewer') RETURNING id INTO role_a;

  PERFORM set_config('app.tenant_id', tenant_b::text, true);
  INSERT INTO platform.users(tenant_id, email, display_name, password_hash)
  VALUES (tenant_b, 'b@example.test', 'User B', 'test-hash') RETURNING id INTO user_b;

  PERFORM set_config('app.tenant_id', tenant_a::text, true);
  SELECT count(*) INTO visible_count FROM platform.users;
  IF visible_count <> 1 THEN
    RAISE EXCEPTION 'tenant isolation failed: expected 1 visible user, got %', visible_count;
  END IF;

  -- 构造最小指标草稿，验证数据集归属复合外键、草稿指针和指标表 RLS。
  INSERT INTO platform.datasets(tenant_id,code,name,dataset_type,created_by,updated_by)
  VALUES (tenant_a,'verify_metric_dataset','Verify Metric Dataset','SINGLE_SOURCE',user_a,user_a)
  RETURNING id INTO metric_dataset_a;
  INSERT INTO platform.dataset_versions(
    tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,schema_hash,
    logical_plan_json,plan_hash,created_by,updated_by
  ) VALUES (
    tenant_a,metric_dataset_a,1,'DRAFT','1.0','{}',repeat('1',64),'{}',repeat('2',64),user_a,user_a
  ) RETURNING id INTO metric_dataset_version_a;
  UPDATE platform.datasets SET current_draft_version_id=metric_dataset_version_a
  WHERE id=metric_dataset_a;
  INSERT INTO platform.metrics(
    tenant_id,dataset_id,code,name,metric_type,created_by,updated_by
  ) VALUES (
    tenant_a,metric_dataset_a,'verify_metric','Verify Metric','ATOMIC',user_a,user_a
  ) RETURNING id INTO metric_a;
  INSERT INTO platform.metric_versions(
    tenant_id,metric_id,dataset_id,dataset_version_id,version_no,status,
    definition_version,definition_json,definition_hash,created_by,updated_by
  ) VALUES (
    tenant_a,metric_a,metric_dataset_a,metric_dataset_version_a,1,'DRAFT',
    '1.0','{}',repeat('3',64),user_a,user_a
  ) RETURNING id INTO metric_version_a;
  UPDATE platform.metrics SET current_draft_version_id=metric_version_a WHERE id=metric_a;
  SELECT count(*) INTO metric_visible FROM platform.metrics;
  IF metric_visible <> 1 THEN
    RAISE EXCEPTION 'metric draft was not visible in its tenant';
  END IF;
  INSERT INTO platform.metric_semantic_documents(
    tenant_id,subject_type,metric_id,metric_version_id,dataset_id,dataset_version_id,
    name,description,caliber,dimensions,period,period_description,lineage,lineage_summary,
    tags,document,semantic_source,prompt_version,semantic_input_hash
  ) VALUES (
    tenant_a,'METRIC_VERSION',metric_a,metric_version_a,metric_dataset_a,metric_dataset_version_a,
    'Verify Metric','verification document','SUM verification caliber','{}','NONE',
    'no fixed period','{}','verification lineage',ARRAY['verify','metric'],
    'metric verification document','RULE','verify-semantic-v1',repeat('4',64)
  );
  SELECT count(*) INTO metric_semantic_visible FROM platform.metric_semantic_documents;
  IF metric_semantic_visible <> 1 THEN
    RAISE EXCEPTION 'metric semantic document was not visible in its tenant';
  END IF;
  BEGIN
    INSERT INTO platform.metric_semantic_documents(
      tenant_id,subject_type,metric_id,metric_version_id,dataset_id,dataset_version_id,
      name,caliber,period_description,lineage,lineage_summary,document,
      semantic_source,prompt_version,semantic_input_hash
    ) VALUES (
      tenant_a,'CANDIDATE',metric_a,metric_version_a,metric_dataset_a,metric_dataset_version_a,
      'Invalid Shape','invalid','none','{}','invalid','invalid',
      'RULE','verify-semantic-v1',repeat('5',64)
    );
  EXCEPTION WHEN check_violation THEN
    metric_semantic_shape_rejected := true;
  END;
  IF NOT metric_semantic_shape_rejected THEN
    RAISE EXCEPTION 'invalid metric semantic subject shape was accepted';
  END IF;

  -- 构造已完成补全的映射表，验证事务 outbox 合并、选择性重建和向量文档 RLS。
  INSERT INTO platform.data_sources(tenant_id,code,name,source_type,status,config,secret_ref)
  VALUES (tenant_a,'verify_asset_source','Verify Asset Source','MYSQL','ACTIVE','{}','env://VERIFY_ASSET_SECRET')
  RETURNING id INTO asset_source_a;
  INSERT INTO platform.metadata_tables(
    tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at,
    business_name,business_description,tags,table_structure_hash,last_enriched_table_structure_hash,
    last_enriched_structure_hash
  ) VALUES (
    tenant_a,asset_source_a,'verify','orders','TABLE',repeat('6',64),now(),
    '验证订单表','仅用于回滚事务内验证',ARRAY['订单','销售'],repeat('7',64),repeat('7',64),repeat('6',64)
  ) RETURNING id INTO asset_table_a;
  INSERT INTO platform.metadata_columns(
    tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,
    structure_hash,last_sync_at,business_name,business_description,tags,semantic_type,
    last_enriched_structure_hash
  ) VALUES (
    tenant_a,asset_table_a,'amount',1,'decimal(18,2)','DECIMAL',false,
    repeat('8',64),now(),'销售额','订单销售金额',ARRAY['销售','金额'],'AMOUNT',repeat('8',64)
  ) RETURNING id INTO asset_column_a;
  SELECT count(*) INTO asset_outbox_visible FROM platform.asset_embedding_outbox;
  IF asset_outbox_visible <> 2 THEN
    RAISE EXCEPTION 'asset outbox did not merge table and column events: %', asset_outbox_visible;
  END IF;
  SELECT event_version INTO table_event_version_before FROM platform.asset_embedding_outbox
  WHERE asset_type='TABLE' AND asset_id=asset_table_a;
  SELECT event_version INTO column_event_version_before FROM platform.asset_embedding_outbox
  WHERE asset_type='COLUMN' AND asset_id=asset_column_a;
  UPDATE platform.metadata_columns SET tags=ARRAY['金额','销售','经营分析'] WHERE id=asset_column_a;
  SELECT event_version INTO table_event_version_after FROM platform.asset_embedding_outbox
  WHERE asset_type='TABLE' AND asset_id=asset_table_a;
  SELECT event_version INTO column_event_version_after FROM platform.asset_embedding_outbox
  WHERE asset_type='COLUMN' AND asset_id=asset_column_a;
  SELECT count(*) INTO asset_outbox_visible FROM platform.asset_embedding_outbox;
  IF asset_outbox_visible <> 2 OR table_event_version_after <> table_event_version_before+1
     OR column_event_version_after <> column_event_version_before+1 THEN
    RAISE EXCEPTION 'asset tag update was not limited to its table and column';
  END IF;
  INSERT INTO platform.asset_embeddings(
    tenant_id,asset_type,asset_id,table_id,document_version,document,input_hash,status
  ) VALUES (
    tenant_a,'TABLE',asset_table_a,asset_table_a,'verify-v1','verification asset document',repeat('9',64),'PENDING'
  );
  SELECT count(*) INTO asset_embedding_visible FROM platform.asset_embeddings;
  IF asset_embedding_visible <> 1 THEN
    RAISE EXCEPTION 'asset embedding document was not visible in its tenant';
  END IF;

  -- 新租户必须保持默认禁用且只预置元数据用途；数据集 DAG 能力必须显式授权。
  SELECT enabled,cardinality(allowed_purposes),
    allowed_purposes=ARRAY['METADATA_COMPLETION']::text[],version
  INTO ai_policy_enabled,ai_policy_purpose_count,ai_policy_default_only,ai_policy_version
  FROM platform.ai_tenant_policies
  WHERE tenant_id=tenant_a;
  IF ai_policy_enabled OR ai_policy_purpose_count <> 1 OR NOT ai_policy_default_only OR ai_policy_version <> 1 THEN
    RAISE EXCEPTION 'new tenant AI policy default is invalid';
  END IF;
  UPDATE platform.ai_tenant_policies SET enabled=true,
    allowed_purposes=ARRAY['REPORT_GENERATION','BLOCK_EDIT','DATASET_DAG_GENERATION']::text[]
  WHERE tenant_id=tenant_a;
  SELECT version INTO ai_policy_version FROM platform.ai_tenant_policies WHERE tenant_id=tenant_a;
  IF ai_policy_version <> 2 THEN
    RAISE EXCEPTION 'AI policy version was not incremented';
  END IF;
  INSERT INTO platform.ai_requests(
    tenant_id,actor_user_id,purpose,resource_type,resource_id,provider,model_name,prompt_version,
    input_hash,input_bytes,redaction_count,reserved_tokens,reserved_cost_micros,max_attempts
  ) VALUES (
    tenant_a,user_a,'DATASET_DAG_GENERATION','DATASET',metric_dataset_a::text,'verify-provider','verify-model','verify-v1',
    repeat('a',64),128,2,256,100,1
  ) RETURNING id INTO ai_request_a;
  UPDATE platform.ai_requests SET
    status='SUCCEEDED',provider_model='verify-model',provider_request_id=repeat('c',64),
    finish_reason='stop',attempts=1,prompt_tokens=10,completion_tokens=5,total_tokens=15,
    cost_micros=25,latency_ms=50,completed_at=now()
  WHERE id=ai_request_a;
  BEGIN
    UPDATE platform.ai_requests SET cost_micros=26 WHERE id=ai_request_a;
  EXCEPTION WHEN raise_exception THEN
    ai_request_immutable := true;
  END;
  IF NOT ai_request_immutable THEN
    RAISE EXCEPTION 'terminal AI request audit update was accepted';
  END IF;
  SELECT accounted_tokens,accounted_cost_micros
  INTO ai_accounted_tokens,ai_accounted_cost_micros
  FROM platform.ai_requests WHERE id=ai_request_a;
  IF ai_accounted_tokens <> 256 OR ai_accounted_cost_micros <> 100 THEN
    RAISE EXCEPTION 'successful AI request released its conservative reservation';
  END IF;
  INSERT INTO platform.ai_requests(
    tenant_id,actor_user_id,purpose,provider,model_name,prompt_version,input_hash,input_bytes,
    reserved_tokens,reserved_cost_micros,max_attempts
  ) VALUES (
    tenant_a,user_a,'BLOCK_EDIT','verify-provider','verify-model','verify-v1',repeat('b',64),128,256,100,2
  ) RETURNING id INTO ai_request_failed;
  UPDATE platform.ai_requests SET
    status='FAILED',error_code='AI_PROVIDER_FAILED',attempts=2,latency_ms=50,completed_at=now()
  WHERE id=ai_request_failed;
  SELECT accounted_tokens,accounted_cost_micros
  INTO ai_accounted_tokens,ai_accounted_cost_micros
  FROM platform.ai_requests WHERE id=ai_request_failed;
  IF ai_accounted_tokens <> 256 OR ai_accounted_cost_micros <> 100 THEN
    RAISE EXCEPTION 'failed AI request did not consume reserved quota';
  END IF;
  INSERT INTO platform.ai_requests(
    tenant_id,actor_user_id,purpose,provider,model_name,prompt_version,input_hash,input_bytes,
    reserved_tokens,reserved_cost_micros,max_attempts
  ) VALUES (
    tenant_a,user_a,'BLOCK_EDIT','verify-provider','verify-model','verify-v1',repeat('d',64),128,256,100,1
  ) RETURNING id INTO ai_request_canceled;
  UPDATE platform.ai_requests SET
    status='CANCELED',error_code='AI_REQUEST_CANCELED',attempts=1,latency_ms=10,completed_at=now()
  WHERE id=ai_request_canceled;
  SELECT status INTO ai_request_status FROM platform.ai_requests WHERE id=ai_request_canceled;
  IF ai_request_status <> 'CANCELED' THEN
    RAISE EXCEPTION 'canceled AI request did not reach canceled terminal state';
  END IF;
  BEGIN
    INSERT INTO platform.ai_requests(
      tenant_id,actor_user_id,purpose,provider,model_name,prompt_version,input_hash,input_bytes,
      reserved_tokens,reserved_cost_micros,max_attempts
    ) VALUES (
      tenant_a,user_a,'REPORT_GENERATION','verify-provider','verify-model','verify-v1','raw-prompt',128,256,100,1
    );
  EXCEPTION WHEN check_violation THEN
    ai_request_constraint := true;
  END;
  IF NOT ai_request_constraint THEN
    RAISE EXCEPTION 'invalid AI request digest was accepted';
  END IF;

  BEGIN
    INSERT INTO platform.user_roles(tenant_id, user_id, role_id, assigned_by)
    VALUES (tenant_a, user_b, role_a, user_a);
  EXCEPTION WHEN foreign_key_violation THEN
    cross_tenant_rejected := true;
  END;
  IF NOT cross_tenant_rejected THEN
    RAISE EXCEPTION 'cross-tenant user-role assignment was accepted';
  END IF;

  INSERT INTO platform.audit_logs(tenant_id, actor_user_id, action, resource_type, resource_id)
  VALUES (tenant_a, user_a, 'VERIFY', 'SYSTEM', 'database-verification');
  BEGIN
    UPDATE platform.audit_logs SET result = 'FAILURE' WHERE resource_id = 'database-verification';
  EXCEPTION WHEN raise_exception THEN
    audit_immutable := true;
  END;
  IF NOT audit_immutable THEN
    RAISE EXCEPTION 'audit log update was accepted';
  END IF;

  -- 报告草稿、语义修订和派生索引必须共享租户边界，修订写入后不可变。
  INSERT INTO platform.reports(tenant_id,code,name,report_type,created_by,updated_by)
  VALUES (tenant_a,'verify_report','Verify Report','REPORT',user_a,user_a) RETURNING id INTO report_a;
  INSERT INTO platform.report_drafts(report_id,tenant_id,schema_version,definition_json,definition_hash,revision_no,editor_state_json,updated_by)
  VALUES (report_a,tenant_a,'1.0',jsonb_build_object('schemaVersion','1.0','report',jsonb_build_object('id',report_a::text)),repeat('a',64),1,'{"minimumRowsByPage":{}}',user_a);
  INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id)
  VALUES (tenant_a,report_a,0,1,'verify-create',repeat('b',64),1,1,'REPORT_CREATE','USER','{}','[{"op":"add","path":"","value":{}}]',1,repeat('c',64),repeat('0',64),repeat('a',64),user_a);
  INSERT INTO platform.report_draft_component_indexes(tenant_id,report_id,revision_no,page_id,block_id,component_id,component_type)
  VALUES (tenant_a,report_a,1,'page_verify','block_verify','component_verify','TITLE');
  BEGIN
    UPDATE platform.report_revisions SET operation_type='BLOCK_MOVE' WHERE report_id=report_a;
  EXCEPTION WHEN raise_exception THEN
    report_revision_immutable := true;
  END;
  IF NOT report_revision_immutable THEN
    RAISE EXCEPTION 'report revision update was accepted';
  END IF;
  BEGIN
    INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,client_operation_id,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id)
    VALUES (tenant_a,report_a,1,3,'verify-invalid-step',repeat('d',64),1,1,gen_random_uuid(),'BLOCK_MOVE','USER','{"pageId":"page_verify","blockId":"block_verify"}','[{"op":"replace","path":"/pages/0/blocks/0/grid/x","value":1}]',1,repeat('e',64),repeat('a',64),repeat('f',64),user_a);
  EXCEPTION WHEN check_violation THEN
    report_revision_constraint := true;
  END;
  IF NOT report_revision_constraint THEN
    RAISE EXCEPTION 'invalid report revision sequence was accepted';
  END IF;

  INSERT INTO platform.data_row_policies(tenant_id, object_type, object_id, name, expression_dsl)
  VALUES (tenant_a, 'DATASET', gen_random_uuid(), 'region scope', '{"type":"EQUALS","left":{"type":"FIELD_REF","fieldCode":"region_code"},"right":{"type":"USER_ATTRIBUTE_REF","attribute":"region_code"}}');
  SELECT count(*) INTO row_policy_visible FROM platform.data_row_policies;
  IF row_policy_visible <> 1 THEN
    RAISE EXCEPTION 'row policy tenant visibility failed';
  END IF;

  PERFORM set_config('app.tenant_id', tenant_b::text, true);
  SELECT count(*) INTO report_visible FROM platform.reports;
  IF report_visible <> 0 THEN
    RAISE EXCEPTION 'report draft leaked across tenants';
  END IF;
  SELECT count(*) INTO row_policy_visible FROM platform.data_row_policies;
  IF row_policy_visible <> 0 THEN
    RAISE EXCEPTION 'row policy leaked across tenants';
  END IF;
  SELECT count(*) INTO metric_visible FROM platform.metrics;
  IF metric_visible <> 0 THEN
    RAISE EXCEPTION 'metric draft leaked across tenants';
  END IF;
  SELECT count(*) INTO metric_semantic_visible FROM platform.metric_semantic_documents;
  IF metric_semantic_visible <> 0 THEN
    RAISE EXCEPTION 'metric semantic document leaked across tenants';
  END IF;
  SELECT count(*) INTO asset_embedding_visible FROM platform.asset_embeddings;
  SELECT count(*) INTO asset_outbox_visible FROM platform.asset_embedding_outbox;
  IF asset_embedding_visible <> 0 OR asset_outbox_visible <> 0 THEN
    RAISE EXCEPTION 'asset embedding state leaked across tenants';
  END IF;
  BEGIN
    INSERT INTO platform.metrics(tenant_id,dataset_id,code,name,metric_type)
    VALUES (tenant_b,metric_dataset_a,'verify_cross_metric','Invalid Cross Metric','ATOMIC');
  EXCEPTION WHEN foreign_key_violation THEN
    metric_cross_tenant_rejected := true;
  END;
  IF NOT metric_cross_tenant_rejected THEN
    RAISE EXCEPTION 'cross-tenant metric dataset reference was accepted';
  END IF;
  SELECT count(*),bool_or(enabled)::boolean
  INTO ai_policy_visible,ai_policy_enabled
  FROM platform.ai_tenant_policies;
  IF ai_policy_visible <> 1 OR ai_policy_enabled THEN
    RAISE EXCEPTION 'tenant B AI policy default or isolation failed';
  END IF;
  SELECT count(*) INTO ai_request_visible FROM platform.ai_requests;
  IF ai_request_visible <> 0 THEN
    RAISE EXCEPTION 'AI request audit leaked across tenants';
  END IF;

  BEGIN
    INSERT INTO platform.object_permissions(tenant_id, subject_type, subject_id, object_type, object_id, action)
    VALUES (tenant_b, 'USER', user_a, 'REPORT', gen_random_uuid(), 'READ');
  EXCEPTION WHEN foreign_key_violation THEN
    invalid_subject_rejected := true;
  END;
  IF NOT invalid_subject_rejected THEN
    RAISE EXCEPTION 'cross-tenant object permission subject was accepted';
  END IF;

  PERFORM set_config('app.tenant_id', '', true);
  SELECT count(*) INTO visible_count FROM platform.users;
  IF visible_count <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed % users', visible_count;
  END IF;
  SELECT count(*) INTO ai_policy_visible FROM platform.ai_tenant_policies;
  SELECT count(*) INTO ai_request_visible FROM platform.ai_requests;
  IF ai_policy_visible <> 0 OR ai_request_visible <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed AI policy or request audit rows';
  END IF;
  SELECT count(*) INTO metric_visible FROM platform.metrics;
  IF metric_visible <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed metric rows';
  END IF;
  SELECT count(*) INTO metric_semantic_visible FROM platform.metric_semantic_documents;
  IF metric_semantic_visible <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed metric semantic rows';
  END IF;
  SELECT count(*) INTO asset_embedding_visible FROM platform.asset_embeddings;
  SELECT count(*) INTO asset_outbox_visible FROM platform.asset_embedding_outbox;
  IF asset_embedding_visible <> 0 OR asset_outbox_visible <> 0 THEN
    RAISE EXCEPTION 'RLS without tenant context exposed asset embedding state';
  END IF;
END
$$;

ROLLBACK;
SELECT 'database verification passed' AS result;
SQL
