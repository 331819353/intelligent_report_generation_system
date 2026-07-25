-- 智能问答语义控制面。
--
-- 本迁移只保存结构化合同、变更操作、冻结图生成和查询证据；自然语言问题仅
-- 保存 SHA-256 摘要。LLM 不直接写 SQL、DDL 或物理表，所有变更仍需经过
-- Dataset DSL 本地校验、乐观锁和现有发布审批。

CREATE TABLE platform.semantic_qa_settings(
  tenant_id uuid PRIMARY KEY REFERENCES platform.tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  graph_projection_enabled boolean NOT NULL DEFAULT false,
  question_change_enabled boolean NOT NULL DEFAULT false,
  minimum_path_confidence numeric(5,4) NOT NULL DEFAULT 0.8000
    CHECK(minimum_path_confidence BETWEEN 0 AND 1),
  maximum_path_hops integer NOT NULL DEFAULT 8 CHECK(maximum_path_hops BETWEEN 1 AND 16),
  updated_by uuid NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform.semantic_consumer_contracts(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code text NOT NULL CHECK(
    code ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'
  ),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
  purpose text NOT NULL CHECK(length(purpose) BETWEEN 1 AND 2000),
  output_grain_json jsonb NOT NULL CHECK(
    jsonb_typeof(output_grain_json)='object'
    AND pg_column_size(output_grain_json)<=65536
    AND platform.materialization_json_is_safe(output_grain_json)
  ),
  service_level_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(
    jsonb_typeof(service_level_json)='object'
    AND pg_column_size(service_level_json)<=65536
    AND platform.materialization_json_is_safe(service_level_json)
  ),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','PUBLISHED','RETIRED')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>=1),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  published_by uuid,
  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_consumer_contracts_tenant_code_key UNIQUE(tenant_id,code),
  CONSTRAINT semantic_consumer_contracts_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT semantic_consumer_contracts_publish_shape_check CHECK(
    (status='PUBLISHED' AND published_by IS NOT NULL AND published_at IS NOT NULL)
    OR status<>'PUBLISHED'
  )
);

CREATE TABLE platform.semantic_consumer_contract_inputs(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  contract_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  required boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,contract_id,dataset_version_id),
  CONSTRAINT semantic_consumer_contract_inputs_contract_fk
    FOREIGN KEY(contract_id,tenant_id)
    REFERENCES platform.semantic_consumer_contracts(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_consumer_contract_inputs_version_fk
    FOREIGN KEY(dataset_version_id,tenant_id)
    REFERENCES platform.dataset_versions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_consumer_contract_inputs_dataset_fk
    FOREIGN KEY(dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE platform.warehouse_dag_change_sets(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  target_dataset_id uuid,
  trigger_type text NOT NULL
    CHECK(trigger_type IN ('AUTOMATION','QUESTION','MANUAL')),
  change_kind text NOT NULL
    CHECK(change_kind IN ('CREATE_DATASET','MODIFY_DATASET','REPAIR_DAG')),
  target_layer text NOT NULL
    CHECK(target_layer IN ('DIM','DWD','DWS','ADS')),
  title text NOT NULL CHECK(length(title) BETWEEN 1 AND 300),
  question_hash text NOT NULL DEFAULT ''
    CHECK(question_hash='' OR question_hash ~ '^[0-9a-f]{64}$'),
  baseline_dataset_version bigint,
  baseline_dsl_hash text NOT NULL DEFAULT ''
    CHECK(baseline_dsl_hash='' OR baseline_dsl_hash ~ '^[0-9a-f]{64}$'),
  request_key text NOT NULL CHECK(
    length(request_key) BETWEEN 1 AND 200
    AND request_key=btrim(request_key)
    AND request_key !~ '[[:cntrl:]]'
  ),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN (
      'DRAFT','VALIDATING','VALIDATED','APPLYING','APPLIED',
      'CONFLICT','REJECTED','FAILED'
    )),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  expected_operation_count integer NOT NULL
    CHECK(expected_operation_count BETWEEN 1 AND 256),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>=1),
  created_by uuid NOT NULL,
  validated_by uuid,
  applied_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT warehouse_dag_change_sets_request_key
    UNIQUE(tenant_id,request_key),
  CONSTRAINT warehouse_dag_change_sets_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT warehouse_dag_change_sets_target_fk
    FOREIGN KEY(target_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT warehouse_dag_change_sets_target_shape_check CHECK(
    (change_kind='CREATE_DATASET' AND target_dataset_id IS NULL
      AND baseline_dataset_version IS NULL AND baseline_dsl_hash='')
    OR
    (change_kind<>'CREATE_DATASET' AND target_dataset_id IS NOT NULL
      AND baseline_dataset_version IS NOT NULL AND baseline_dataset_version>=1
      AND baseline_dsl_hash<>'')
  )
);

CREATE TABLE platform.warehouse_dag_change_operations(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  change_set_id uuid NOT NULL,
  operation_index integer NOT NULL CHECK(operation_index BETWEEN 0 AND 255),
  operation text NOT NULL CHECK(operation IN ('ADD','REPLACE','REMOVE')),
  path text NOT NULL CHECK(
    length(path) BETWEEN 1 AND 512
    AND path ~ '^/(dataset|nodes|joins|preAggregations|factContract|analysisContract|fields|filters|groupBy|having|sorts|parameters|outputGrain|executionPolicy)(/.*)?$'
    AND path !~ '[[:cntrl:]]'
  ),
  value_json jsonb CHECK(
    value_json IS NULL OR (
      pg_column_size(value_json)<=262144
      AND platform.materialization_json_is_safe(value_json)
    )
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,change_set_id,operation_index),
  CONSTRAINT warehouse_dag_change_operations_change_set_fk
    FOREIGN KEY(change_set_id,tenant_id)
    REFERENCES platform.warehouse_dag_change_sets(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT warehouse_dag_change_operations_value_shape_check CHECK(
    (operation='REMOVE' AND value_json IS NULL)
    OR (operation<>'REMOVE' AND value_json IS NOT NULL)
  )
);

CREATE TABLE platform.warehouse_dag_change_validations(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  change_set_id uuid NOT NULL,
  validation_index integer NOT NULL CHECK(validation_index BETWEEN 0 AND 1023),
  severity text NOT NULL CHECK(severity IN ('INFO','WARNING','ERROR')),
  code text NOT NULL CHECK(
    length(code) BETWEEN 1 AND 128 AND code ~ '^[A-Z][A-Z0-9_]*$'
  ),
  path text NOT NULL DEFAULT '' CHECK(length(path)<=512),
  message text NOT NULL CHECK(length(message) BETWEEN 1 AND 2000),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT warehouse_dag_change_validations_change_set_fk
    FOREIGN KEY(change_set_id,tenant_id)
    REFERENCES platform.warehouse_dag_change_sets(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT warehouse_dag_change_validations_order_key
    UNIQUE(tenant_id,change_set_id,validation_index)
);

CREATE TABLE platform.warehouse_dag_runs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  change_set_id uuid,
  trigger_type text NOT NULL
    CHECK(trigger_type IN ('AUTOMATION','QUESTION','MANUAL','BUILD')),
  root_dataset_id uuid,
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','CANCELED')),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  current_stage text NOT NULL DEFAULT 'CONTEXT'
    CHECK(current_stage IN (
      'CONTEXT','CLASSIFY','DESIGN','VALIDATE','APPLY','BUILD','VERIFY','COMPLETE'
    )),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
  max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),
  checkpoint_version integer NOT NULL DEFAULT 0 CHECK(checkpoint_version>=0),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT warehouse_dag_runs_change_set_fk
    FOREIGN KEY(change_set_id,tenant_id)
    REFERENCES platform.warehouse_dag_change_sets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT warehouse_dag_runs_root_dataset_fk
    FOREIGN KEY(root_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT warehouse_dag_runs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT warehouse_dag_runs_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL)
    OR (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  )
);

CREATE TABLE platform.warehouse_dag_stage_runs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dag_run_id uuid NOT NULL,
  stage text NOT NULL CHECK(stage IN (
    'CONTEXT','CLASSIFY','DESIGN','VALIDATE','APPLY','BUILD','VERIFY'
  )),
  subject_ref text NOT NULL DEFAULT '' CHECK(
    length(subject_ref)<=256 AND subject_ref !~ '[[:cntrl:]]'
  ),
  input_hash text NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
  output_hash text NOT NULL DEFAULT ''
    CHECK(output_hash='' OR output_hash ~ '^[0-9a-f]{64}$'),
  output_json jsonb CHECK(
    output_json IS NULL OR (
      jsonb_typeof(output_json)='object'
      AND pg_column_size(output_json)<=2097152
      AND platform.materialization_json_is_safe(output_json)
    )
  ),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 10),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT warehouse_dag_stage_runs_run_fk
    FOREIGN KEY(dag_run_id,tenant_id)
    REFERENCES platform.warehouse_dag_runs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT warehouse_dag_stage_runs_checkpoint_key
    UNIQUE(tenant_id,dag_run_id,stage,subject_ref,input_hash),
  CONSTRAINT warehouse_dag_stage_runs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_graph_generations(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  generation bigint NOT NULL CHECK(generation>=1),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL CHECK(status IN ('BUILDING','READY','FAILED','SUPERSEDED')),
  node_count integer NOT NULL DEFAULT 0 CHECK(node_count>=0),
  edge_count integer NOT NULL DEFAULT 0 CHECK(edge_count>=0),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  created_at timestamptz NOT NULL DEFAULT now(),
  ready_at timestamptz,
  CONSTRAINT semantic_graph_generations_number_key UNIQUE(tenant_id,generation),
  CONSTRAINT semantic_graph_generations_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_graph_nodes(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  generation_id uuid NOT NULL,
  node_key text NOT NULL CHECK(
    length(node_key) BETWEEN 1 AND 400
    AND node_key=btrim(node_key) AND node_key !~ '[[:cntrl:]]'
  ),
  node_type text NOT NULL CHECK(node_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET','SOURCE','TAG'
  )),
  subject_ref text NOT NULL CHECK(
    length(subject_ref) BETWEEN 1 AND 256
    AND subject_ref=btrim(subject_ref) AND subject_ref !~ '[[:cntrl:]]'
  ),
  label text NOT NULL CHECK(length(label) BETWEEN 1 AND 500),
  normalized_label text NOT NULL CHECK(length(normalized_label) BETWEEN 1 AND 500),
  payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
  payload_json jsonb NOT NULL CHECK(
    jsonb_typeof(payload_json)='object'
    AND pg_column_size(payload_json)<=262144
    AND platform.materialization_json_is_safe(payload_json)
  ),
  PRIMARY KEY(tenant_id,generation_id,node_key),
  CONSTRAINT semantic_graph_nodes_generation_fk
    FOREIGN KEY(generation_id,tenant_id)
    REFERENCES platform.semantic_graph_generations(id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE platform.semantic_graph_edges(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  generation_id uuid NOT NULL,
  edge_key text NOT NULL CHECK(
    length(edge_key) BETWEEN 1 AND 500
    AND edge_key=btrim(edge_key) AND edge_key !~ '[[:cntrl:]]'
  ),
  from_node_key text NOT NULL,
  to_node_key text NOT NULL,
  relation_type text NOT NULL CHECK(relation_type IN (
    'MEMBER_OF','DIMENSION_FIELD','METRIC_DIMENSION','METRIC_DATASET',
    'FIELD_DATASET','DATASET_VERSION_OF','DATASET_DEPENDS_ON',
    'DATASET_SOURCE','TAGGED_AS','ALIAS_OF'
  )),
  confidence numeric(5,4) NOT NULL CHECK(confidence BETWEEN 0 AND 1),
  authority text NOT NULL CHECK(authority IN (
    'CONTROL_PLANE','RULE','VERIFIED','LLM_PROPOSAL'
  )),
  evidence_hash text NOT NULL CHECK(evidence_hash ~ '^[0-9a-f]{64}$'),
  evidence_json jsonb NOT NULL CHECK(
    jsonb_typeof(evidence_json)='object'
    AND pg_column_size(evidence_json)<=262144
    AND platform.materialization_json_is_safe(evidence_json)
  ),
  PRIMARY KEY(tenant_id,generation_id,edge_key),
  CONSTRAINT semantic_graph_edges_generation_fk
    FOREIGN KEY(generation_id,tenant_id)
    REFERENCES platform.semantic_graph_generations(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_graph_edges_from_fk
    FOREIGN KEY(tenant_id,generation_id,from_node_key)
    REFERENCES platform.semantic_graph_nodes(tenant_id,generation_id,node_key)
    ON DELETE CASCADE,
  CONSTRAINT semantic_graph_edges_to_fk
    FOREIGN KEY(tenant_id,generation_id,to_node_key)
    REFERENCES platform.semantic_graph_nodes(tenant_id,generation_id,node_key)
    ON DELETE CASCADE,
  CONSTRAINT semantic_graph_edges_no_self_loop CHECK(from_node_key<>to_node_key)
);

CREATE TABLE platform.semantic_graph_projection_state(
  tenant_id uuid PRIMARY KEY REFERENCES platform.tenants(id) ON DELETE CASCADE,
  current_generation_id uuid,
  requested_event_version bigint NOT NULL DEFAULT 1 CHECK(requested_event_version>=1),
  applied_event_version bigint NOT NULL DEFAULT 0 CHECK(applied_event_version>=0),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','RUNNING','READY','FAILED')),
  lease_owner text NOT NULL DEFAULT '' CHECK(length(lease_owner)<=128),
  lease_token uuid,
  lease_expires_at timestamptz,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 20),
  max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  error_code text NOT NULL DEFAULT '' CHECK(length(error_code)<=128),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_graph_projection_state_generation_fk
    FOREIGN KEY(current_generation_id,tenant_id)
    REFERENCES platform.semantic_graph_generations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_graph_projection_state_version_check
    CHECK(applied_event_version<=requested_event_version),
  CONSTRAINT semantic_graph_projection_state_lease_shape_check CHECK(
    (status='RUNNING' AND lease_owner<>'' AND lease_token IS NOT NULL
      AND lease_expires_at IS NOT NULL)
    OR (status<>'RUNNING' AND lease_owner='' AND lease_token IS NULL
      AND lease_expires_at IS NULL)
  )
);

CREATE TABLE platform.semantic_query_plans(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  graph_generation_id uuid NOT NULL,
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  intent text NOT NULL CHECK(intent IN (
    'LOOKUP','METRIC','TREND','COMPARISON','RANKING','DRILLDOWN',
    'DISTRIBUTION','FUNNEL','RETENTION','ANOMALY','UNKNOWN'
  )),
  normalized_request_json jsonb NOT NULL CHECK(
    jsonb_typeof(normalized_request_json)='object'
    AND pg_column_size(normalized_request_json)<=262144
    AND platform.materialization_json_is_safe(normalized_request_json)
  ),
  status text NOT NULL CHECK(status IN (
    'READY','AMBIGUOUS','GAP','REJECTED','EXECUTED','FAILED'
  )),
  confidence numeric(5,4) NOT NULL CHECK(confidence BETWEEN 0 AND 1),
  selected_metric_version_id uuid,
  selected_dimension_id uuid,
  selected_dataset_version_id uuid,
  path_hash text NOT NULL DEFAULT ''
    CHECK(path_hash='' OR path_hash ~ '^[0-9a-f]{64}$'),
  failure_code text NOT NULL DEFAULT '' CHECK(length(failure_code)<=128),
  change_set_id uuid,
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  executed_at timestamptz,
  CONSTRAINT semantic_query_plans_generation_fk
    FOREIGN KEY(graph_generation_id,tenant_id)
    REFERENCES platform.semantic_graph_generations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_query_plans_change_set_fk
    FOREIGN KEY(change_set_id,tenant_id)
    REFERENCES platform.warehouse_dag_change_sets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_query_plans_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_query_plan_evidence(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  query_plan_id uuid NOT NULL,
  evidence_index integer NOT NULL CHECK(evidence_index BETWEEN 0 AND 255),
  node_key text NOT NULL,
  relation_type text NOT NULL DEFAULT '',
  subject_type text NOT NULL CHECK(subject_type IN (
    'MEMBER','DIMENSION','METRIC','FIELD','DATASET_VERSION','DATASET','SOURCE','TAG'
  )),
  subject_ref text NOT NULL CHECK(length(subject_ref) BETWEEN 1 AND 256),
  authority text NOT NULL CHECK(authority IN (
    'CONTROL_PLANE','RULE','VERIFIED','LLM_PROPOSAL'
  )),
  confidence numeric(5,4) NOT NULL CHECK(confidence BETWEEN 0 AND 1),
  evidence_hash text NOT NULL CHECK(evidence_hash ~ '^[0-9a-f]{64}$'),
  PRIMARY KEY(tenant_id,query_plan_id,evidence_index),
  CONSTRAINT semantic_query_plan_evidence_plan_fk
    FOREIGN KEY(query_plan_id,tenant_id)
    REFERENCES platform.semantic_query_plans(id,tenant_id) ON DELETE CASCADE
);

CREATE TABLE platform.semantic_question_templates(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code text NOT NULL CHECK(code ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
  intent text NOT NULL CHECK(intent IN (
    'LOOKUP','METRIC','TREND','COMPARISON','RANKING','DRILLDOWN',
    'DISTRIBUTION','FUNNEL','RETENTION','ANOMALY'
  )),
  required_slots_json jsonb NOT NULL CHECK(
    jsonb_typeof(required_slots_json)='array'
    AND pg_column_size(required_slots_json)<=65536
    AND platform.materialization_json_is_safe(required_slots_json)
  ),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DISABLED')),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_question_templates_code_key UNIQUE(tenant_id,code),
  CONSTRAINT semantic_question_templates_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE TABLE platform.semantic_golden_questions(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  template_id uuid,
  expected_path_hash text NOT NULL CHECK(expected_path_hash ~ '^[0-9a-f]{64}$'),
  expected_status text NOT NULL
    CHECK(expected_status IN ('READY','AMBIGUOUS','GAP','REJECTED')),
  fixture_json jsonb NOT NULL CHECK(
    jsonb_typeof(fixture_json)='object'
    AND pg_column_size(fixture_json)<=262144
    AND platform.materialization_json_is_safe(fixture_json)
  ),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DISABLED')),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_golden_questions_hash_key UNIQUE(tenant_id,question_hash),
  CONSTRAINT semantic_golden_questions_template_fk
    FOREIGN KEY(template_id,tenant_id)
    REFERENCES platform.semantic_question_templates(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_golden_questions_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX warehouse_dag_change_sets_status_idx
  ON platform.warehouse_dag_change_sets(tenant_id,status,updated_at,id);
CREATE INDEX warehouse_dag_runs_claim_idx
  ON platform.warehouse_dag_runs(tenant_id,status,updated_at,id);
CREATE INDEX warehouse_dag_stage_runs_resume_idx
  ON platform.warehouse_dag_stage_runs(tenant_id,dag_run_id,status,stage);
CREATE INDEX semantic_graph_nodes_label_idx
  ON platform.semantic_graph_nodes(tenant_id,generation_id,normalized_label,node_type);
CREATE INDEX semantic_graph_nodes_subject_idx
  ON platform.semantic_graph_nodes(tenant_id,generation_id,node_type,subject_ref);
CREATE INDEX semantic_graph_edges_from_idx
  ON platform.semantic_graph_edges(tenant_id,generation_id,from_node_key,relation_type);
CREATE INDEX semantic_graph_edges_to_idx
  ON platform.semantic_graph_edges(tenant_id,generation_id,to_node_key,relation_type);
CREATE INDEX semantic_query_plans_lookup_idx
  ON platform.semantic_query_plans(tenant_id,question_hash,created_at DESC);

CREATE TRIGGER semantic_consumer_contracts_set_updated_at
BEFORE UPDATE ON platform.semantic_consumer_contracts
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER warehouse_dag_change_sets_set_updated_at
BEFORE UPDATE ON platform.warehouse_dag_change_sets
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER warehouse_dag_runs_set_updated_at
BEFORE UPDATE ON platform.warehouse_dag_runs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER warehouse_dag_stage_runs_set_updated_at
BEFORE UPDATE ON platform.warehouse_dag_stage_runs
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_question_templates_set_updated_at
BEFORE UPDATE ON platform.semantic_question_templates
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER semantic_golden_questions_set_updated_at
BEFORE UPDATE ON platform.semantic_golden_questions
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.wake_semantic_graph_projection()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  should_wake boolean := false;
BEGIN
  IF TG_OP='INSERT' THEN
    should_wake := NEW.enabled AND NEW.graph_projection_enabled;
  ELSE
    should_wake := NEW.enabled AND NEW.graph_projection_enabled
      AND (NOT OLD.enabled OR NOT OLD.graph_projection_enabled);
  END IF;
  IF should_wake THEN
    INSERT INTO platform.semantic_graph_projection_state(
      tenant_id,status,next_attempt_at
    ) VALUES(NEW.tenant_id,'PENDING',now())
    ON CONFLICT(tenant_id) DO UPDATE SET
      status=CASE
        WHEN platform.semantic_graph_projection_state.status='RUNNING'
          THEN 'RUNNING'
        ELSE 'PENDING'
      END,
      next_attempt_at=now(),error_code='',updated_at=now();
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.wake_semantic_graph_projection() FROM PUBLIC;

CREATE TRIGGER semantic_qa_settings_wake_graph_projection
AFTER INSERT OR UPDATE OF enabled,graph_projection_enabled
ON platform.semantic_qa_settings
FOR EACH ROW EXECUTE FUNCTION platform.wake_semantic_graph_projection();

CREATE OR REPLACE FUNCTION platform.mark_semantic_graph_dirty()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  INSERT INTO platform.semantic_graph_projection_state(
    tenant_id,requested_event_version,status,next_attempt_at
  ) VALUES(NEW.tenant_id,1,'PENDING',now())
  ON CONFLICT(tenant_id) DO UPDATE SET
    requested_event_version=
      platform.semantic_graph_projection_state.requested_event_version+1,
    status=CASE
      WHEN platform.semantic_graph_projection_state.status='RUNNING'
        THEN 'RUNNING'
      ELSE 'PENDING'
    END,
    next_attempt_at=now(),error_code='',updated_at=now();
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.mark_semantic_graph_dirty() FROM PUBLIC;

CREATE TRIGGER semantic_change_outbox_mark_graph_dirty
AFTER INSERT OR UPDATE OF event_version ON platform.semantic_change_outbox
FOR EACH ROW EXECUTE FUNCTION platform.mark_semantic_graph_dirty();

-- 所有现有租户都进入待投影状态；开关关闭时 worker 不会认领。
INSERT INTO platform.semantic_qa_settings(tenant_id,updated_by)
SELECT tenant.id,'00000000-0000-0000-0000-000000000000'::uuid
FROM platform.tenants AS tenant
ON CONFLICT(tenant_id) DO NOTHING;

INSERT INTO platform.semantic_graph_projection_state(tenant_id)
SELECT tenant.id FROM platform.tenants AS tenant
ON CONFLICT(tenant_id) DO NOTHING;

CREATE OR REPLACE FUNCTION platform.enforce_ads_consumer_contract()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  contract_id_text text;
  contract_id uuid;
  node_count integer;
  matched_count integer;
BEGIN
  IF NEW.layer<>'ADS' OR NEW.status<>'PUBLISHED' THEN
    RETURN NEW;
  END IF;
  contract_id_text := NEW.dsl_json #>> '{dataset,consumerContractId}';
  IF contract_id_text IS NULL OR contract_id_text !~
    '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$' THEN
    RAISE EXCEPTION 'ADS 发布必须绑定规范 UUID 的消费合同'
      USING ERRCODE='23514';
  END IF;
  contract_id := contract_id_text::uuid;
  IF NOT EXISTS(
    SELECT 1 FROM platform.semantic_consumer_contracts
    WHERE tenant_id=NEW.tenant_id AND id=contract_id AND status='PUBLISHED'
  ) THEN
    RAISE EXCEPTION 'ADS 消费合同不存在或未发布' USING ERRCODE='23514';
  END IF;

  SELECT count(*) INTO node_count
  FROM jsonb_array_elements(NEW.dsl_json->'nodes') AS node
  WHERE node->>'type'='DATASET';
  SELECT count(*) INTO matched_count
  FROM jsonb_array_elements(NEW.dsl_json->'nodes') AS node
  JOIN platform.semantic_consumer_contract_inputs AS input
    ON input.tenant_id=NEW.tenant_id
   AND input.contract_id=contract_id
   AND input.dataset_version_id=(node->>'datasetVersionId')::uuid
  JOIN platform.dataset_versions AS version
    ON version.tenant_id=input.tenant_id
   AND version.id=input.dataset_version_id
   AND version.dataset_id=input.dataset_id
   AND version.layer='DWS' AND version.status='PUBLISHED'
  WHERE node->>'type'='DATASET'
    AND (node->>'datasetVersionId') ~
      '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$';
  IF node_count=0 OR matched_count<>node_count OR EXISTS(
    SELECT 1 FROM platform.semantic_consumer_contract_inputs AS input
    WHERE input.tenant_id=NEW.tenant_id AND input.contract_id=contract_id
      AND input.required
      AND NOT EXISTS(
        SELECT 1 FROM jsonb_array_elements(NEW.dsl_json->'nodes') AS node
        WHERE node->>'type'='DATASET'
          AND node->>'datasetVersionId'=input.dataset_version_id::text
      )
  ) THEN
    RAISE EXCEPTION 'ADS 输入必须精确满足已发布消费合同中的 DWS 版本'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_ads_consumer_contract() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enforce_ads_consumer_contract
BEFORE INSERT OR UPDATE OF layer,status,dsl_json ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_ads_consumer_contract();

ALTER TABLE platform.semantic_qa_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_qa_settings FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_consumer_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_consumer_contracts FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_consumer_contract_inputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_consumer_contract_inputs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_sets FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_validations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_change_validations FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_stage_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.warehouse_dag_stage_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_generations FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_nodes FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_edges FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_projection_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_graph_projection_state FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_plans FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_plan_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_plan_evidence FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_question_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_question_templates FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_golden_questions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_golden_questions FORCE ROW LEVEL SECURITY;

CREATE POLICY semantic_qa_settings_tenant_isolation
  ON platform.semantic_qa_settings
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_consumer_contracts_tenant_isolation
  ON platform.semantic_consumer_contracts
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_consumer_contract_inputs_tenant_isolation
  ON platform.semantic_consumer_contract_inputs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY warehouse_dag_change_sets_tenant_isolation
  ON platform.warehouse_dag_change_sets
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY warehouse_dag_change_operations_tenant_isolation
  ON platform.warehouse_dag_change_operations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY warehouse_dag_change_validations_tenant_isolation
  ON platform.warehouse_dag_change_validations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY warehouse_dag_runs_tenant_isolation
  ON platform.warehouse_dag_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY warehouse_dag_stage_runs_tenant_isolation
  ON platform.warehouse_dag_stage_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_graph_generations_tenant_isolation
  ON platform.semantic_graph_generations
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_graph_nodes_tenant_isolation
  ON platform.semantic_graph_nodes
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_graph_edges_tenant_isolation
  ON platform.semantic_graph_edges
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_graph_projection_state_tenant_isolation
  ON platform.semantic_graph_projection_state
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_query_plans_tenant_isolation
  ON platform.semantic_query_plans
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_query_plan_evidence_tenant_isolation
  ON platform.semantic_query_plan_evidence
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_question_templates_tenant_isolation
  ON platform.semantic_question_templates
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_golden_questions_tenant_isolation
  ON platform.semantic_golden_questions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.warehouse_dag_change_sets IS
  '自动化、人工问题和人工编辑共享的有界结构化 DAG 变更，不保存整份 DSL 覆盖';
COMMENT ON TABLE platform.warehouse_dag_stage_runs IS
  '可独立重试的 DAG 阶段检查点；相同输入哈希只生成一个有效阶段输出';
COMMENT ON TABLE platform.semantic_graph_generations IS
  '从权威控制面原子投影出的不可变语义图生成；查询只能固定到一个 READY generation';
COMMENT ON TABLE platform.semantic_query_plan_evidence IS
  '成员值->维度->指标->数据表->数据源路径的逐跳权威证据';
