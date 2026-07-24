-- DWS 维度勘测最小闭环。
--
-- 发布事务只登记等待物化的 survey；ACTIVE DWS 物化在同一事务把精确
-- materialization/schema/snapshot 证据固定到候选。候选仅来自 dataset_fields
-- 的维度角色，不读取 warehouse 行、样本值、SQL、表达式或凭据。

CREATE TABLE platform.dimension_survey_runs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  survey_version text NOT NULL CHECK(
    length(survey_version) BETWEEN 1 AND 128
    AND survey_version=btrim(survey_version)
    AND survey_version !~ '[[:cntrl:]]'
  ),
  materialization_id uuid,
  materialization_snapshot_hash text NOT NULL DEFAULT '' CHECK(
    materialization_snapshot_hash=''
    OR materialization_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  materialization_row_count bigint CHECK(materialization_row_count>=0),
  status text NOT NULL CHECK(
    status IN ('WAITING_MATERIALIZATION','SUCCEEDED','STALE')
  ),
  candidate_count integer NOT NULL DEFAULT 0 CHECK(candidate_count>=0),
  requested_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT dimension_survey_runs_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,schema_hash)
    REFERENCES platform.dataset_versions(
      id,dataset_id,tenant_id,schema_hash
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_runs_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_runs_requested_by_fk
    FOREIGN KEY(requested_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_runs_status_shape_check CHECK(
    (status='WAITING_MATERIALIZATION'
      AND materialization_id IS NULL
      AND materialization_snapshot_hash=''
      AND materialization_row_count IS NULL
      AND candidate_count=0
      AND completed_at IS NULL)
    OR
    (status='SUCCEEDED'
      AND materialization_id IS NOT NULL
      AND materialization_snapshot_hash ~ '^[0-9a-f]{64}$'
      AND materialization_row_count IS NOT NULL
      AND completed_at IS NOT NULL
      AND completed_at>=created_at)
    OR
    (status='STALE'
      AND completed_at IS NOT NULL
      AND completed_at>=created_at)
  ),
  CONSTRAINT dimension_survey_runs_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE UNIQUE INDEX dimension_survey_runs_waiting_key
  ON platform.dimension_survey_runs(
    tenant_id,dataset_version_id,survey_version
  )
  WHERE status='WAITING_MATERIALIZATION';

CREATE UNIQUE INDEX dimension_survey_runs_materialization_key
  ON platform.dimension_survey_runs(
    tenant_id,dataset_version_id,materialization_id,survey_version
  )
  WHERE materialization_id IS NOT NULL;

CREATE INDEX dimension_survey_runs_dataset_time_idx
  ON platform.dimension_survey_runs(
    tenant_id,dataset_id,created_at DESC,id
  );

CREATE TABLE platform.dimension_survey_candidates(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  survey_run_id uuid NOT NULL,
  dataset_id uuid NOT NULL,
  dataset_version_id uuid NOT NULL,
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  materialization_id uuid NOT NULL,
  materialization_snapshot_hash text NOT NULL CHECK(
    materialization_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  field_id text NOT NULL CHECK(length(field_id) BETWEEN 1 AND 256),
  field_code text NOT NULL CHECK(
    length(field_code) BETWEEN 1 AND 128
    AND field_code=btrim(field_code)
    AND field_code !~ '[[:cntrl:]]'
  ),
  field_role text NOT NULL CHECK(
    field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
  ),
  canonical_type text NOT NULL CHECK(
    length(canonical_type) BETWEEN 1 AND 64
    AND canonical_type=btrim(canonical_type)
    AND canonical_type !~ '[[:cntrl:]]'
  ),
  semantic_type text NOT NULL DEFAULT '' CHECK(
    length(semantic_type)<=100
    AND semantic_type=btrim(semantic_type)
    AND semantic_type !~ '[[:cntrl:]]'
  ),
  risk_high_cardinality boolean NOT NULL,
  risk_sensitive boolean NOT NULL,
  evidence_json jsonb NOT NULL CHECK(
    jsonb_typeof(evidence_json)='object'
    AND pg_column_size(evidence_json)<=65536
    AND platform.materialization_json_is_safe(evidence_json)
  ),
  proposed_code text NOT NULL CHECK(
    length(proposed_code) BETWEEN 1 AND 128
    AND proposed_code=btrim(proposed_code)
    AND proposed_code !~ '[[:cntrl:]]'
  ),
  proposed_name text NOT NULL CHECK(
    length(proposed_name) BETWEEN 1 AND 256
    AND proposed_name=btrim(proposed_name)
    AND proposed_name !~ '[[:cntrl:]]'
  ),
  proposed_description text NOT NULL DEFAULT '' CHECK(
    length(proposed_description)<=4096
    AND proposed_description !~ '[[:cntrl:]]'
  ),
  proposed_dimension_type text NOT NULL CHECK(
    proposed_dimension_type IN (
      'STANDARD','TIME','GEOGRAPHY','ORGANIZATION',
      'PRODUCT','CUSTOMER','OTHER'
    )
  ),
  proposed_member_index_policy text NOT NULL CHECK(
    proposed_member_index_policy IN ('FULL','EXACT_ONLY','NONE')
  ),
  proposed_high_cardinality boolean NOT NULL,
  proposed_sensitive boolean NOT NULL,
  status text NOT NULL DEFAULT 'SUGGESTED' CHECK(
    status IN ('SUGGESTED','ACCEPTED','REJECTED','STALE')
  ),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  accepted_dimension_id uuid,
  decision_reason text NOT NULL DEFAULT '' CHECK(
    length(decision_reason)<=2000
    AND decision_reason !~ '[[:cntrl:]]'
  ),
  generated_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  reviewed_by uuid,
  reviewed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dimension_survey_candidates_run_fk
    FOREIGN KEY(survey_run_id,tenant_id)
    REFERENCES platform.dimension_survey_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_dataset_version_fk
    FOREIGN KEY(dataset_version_id,dataset_id,tenant_id,schema_hash)
    REFERENCES platform.dataset_versions(
      id,dataset_id,tenant_id,schema_hash
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_field_fk
    FOREIGN KEY(tenant_id,dataset_version_id,field_id)
    REFERENCES platform.dataset_fields(
      tenant_id,dataset_version_id,field_id
    ) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_dimension_fk
    FOREIGN KEY(accepted_dimension_id,tenant_id)
    REFERENCES platform.semantic_dimensions(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_generated_by_fk
    FOREIGN KEY(generated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_reviewed_by_fk
    FOREIGN KEY(reviewed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dimension_survey_candidates_risk_floor_check CHECK(
    (NOT risk_high_cardinality OR proposed_high_cardinality)
    AND (NOT risk_sensitive OR proposed_sensitive)
    AND (
      NOT (proposed_high_cardinality OR proposed_sensitive)
      OR proposed_member_index_policy<>'FULL'
    )
  ),
  CONSTRAINT dimension_survey_candidates_decision_shape_check CHECK(
    (status='SUGGESTED'
      AND accepted_dimension_id IS NULL
      AND reviewed_by IS NULL AND reviewed_at IS NULL
      AND decision_reason='')
    OR
    (status='ACCEPTED'
      AND accepted_dimension_id IS NOT NULL
      AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
      AND decision_reason='')
    OR
    (status='REJECTED'
      AND accepted_dimension_id IS NULL
      AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
      AND btrim(decision_reason)<>'')
    OR
    (status='STALE'
      AND accepted_dimension_id IS NULL
      AND reviewed_by IS NULL AND reviewed_at IS NULL
      AND btrim(decision_reason)<>'')
  ),
  CONSTRAINT dimension_survey_candidates_run_field_key
    UNIQUE(tenant_id,survey_run_id,field_id),
  CONSTRAINT dimension_survey_candidates_materialization_field_key
    UNIQUE(tenant_id,materialization_id,field_id),
  CONSTRAINT dimension_survey_candidates_identity_tenant_key UNIQUE(id,tenant_id)
);

CREATE INDEX dimension_survey_candidates_review_idx
  ON platform.dimension_survey_candidates(
    tenant_id,status,updated_at DESC,id
  );

CREATE INDEX dimension_survey_candidates_dataset_idx
  ON platform.dimension_survey_candidates(
    tenant_id,dataset_version_id,field_role,updated_at DESC,id
  );

-- 高基数与敏感维度均不能触发无界 FULL DISTINCT 扫描。先安全收紧历史记录，
-- v65 的 privacy guard 会同步停用旧成员并跳过未完成刷新任务。
UPDATE platform.semantic_dimensions
SET member_index_policy='EXACT_ONLY',
    definition_hash=encode(public.digest(
      convert_to(
        concat_ws(E'\x1f',dataset_id::text,dataset_version_id::text,field_id,
          code::text,name,description,dimension_type,'EXACT_ONLY',
          high_cardinality::text,sensitive::text,status
        ),
        'UTF8'
      ),
      'sha256'
    ),'hex'),
    version=version+1,
    updated_at=now()
WHERE high_cardinality AND member_index_policy='FULL';

ALTER TABLE platform.semantic_dimensions
  ADD CONSTRAINT semantic_dimensions_high_cardinality_index_policy_check
  CHECK(NOT high_cardinality OR member_index_policy<>'FULL') NOT VALID;

ALTER TABLE platform.semantic_dimensions
  VALIDATE CONSTRAINT semantic_dimensions_high_cardinality_index_policy_check;

-- 维度接受和字段敏感标签审批使用同一事务级 advisory lock。无论两个事务
-- 谁先提交，后提交者都必须看到最新风险事实：标签后到会立即收紧现有维度，
-- 维度后到则必须显式按敏感策略发布。
CREATE OR REPLACE FUNCTION platform.guard_published_dimension_field_sensitivity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  field_is_sensitive boolean;
BEGIN
  IF NEW.status='PUBLISHED' THEN
    PERFORM pg_advisory_xact_lock(hashtextextended(
      'dimension-field-risk:'||NEW.tenant_id::text||
      ':'||NEW.dataset_version_id::text||':'||NEW.field_id,
      0
    ));
    SELECT EXISTS(
      SELECT 1
      FROM platform.asset_tag_bindings AS binding
      JOIN platform.semantic_tags AS tag
        ON tag.id=binding.tag_id
       AND tag.tenant_id=binding.tenant_id
       AND tag.status='ACTIVE'
       AND tag.category='SENSITIVITY'
      WHERE binding.tenant_id=NEW.tenant_id
        AND binding.asset_type='DATASET_FIELD'
        AND binding.dataset_id=NEW.dataset_id
        AND binding.dataset_version_id=NEW.dataset_version_id
        AND binding.dataset_field_id=NEW.field_id
        AND binding.status='APPROVED'
    ) INTO field_is_sensitive;
    IF field_is_sensitive
      AND (NOT NEW.sensitive OR NEW.member_index_policy='FULL') THEN
      RAISE EXCEPTION '已批准敏感标签的字段必须按敏感维度策略发布'
        USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  platform.guard_published_dimension_field_sensitivity() FROM PUBLIC;

CREATE TRIGGER semantic_dimensions_guard_field_sensitivity_insert
BEFORE INSERT ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION
  platform.guard_published_dimension_field_sensitivity();

CREATE TRIGGER semantic_dimensions_guard_field_sensitivity_update
BEFORE UPDATE OF status,member_index_policy,sensitive
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION
  platform.guard_published_dimension_field_sensitivity();

CREATE OR REPLACE FUNCTION platform.tighten_sensitive_field_dimensions(
  selected_tenant_id uuid,
  selected_dataset_id uuid,
  selected_dataset_version_id uuid,
  selected_field_id text,
  selected_actor uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(
    'dimension-field-risk:'||selected_tenant_id::text||
    ':'||selected_dataset_version_id::text||':'||selected_field_id,
    0
  ));
  UPDATE platform.semantic_dimensions AS dimension
  SET sensitive=true,
      member_index_policy='NONE',
      definition_hash=encode(public.digest(
        convert_to(
          concat_ws(E'\x1f',
            dimension.dataset_id::text,
            dimension.dataset_version_id::text,
            dimension.field_id,
            dimension.code::text,
            dimension.name,
            dimension.description,
            dimension.dimension_type,
            'NONE',
            dimension.high_cardinality::text,
            'true',
            dimension.status
          ),
          'UTF8'
        ),
        'sha256'
      ),'hex'),
      version=dimension.version+1,
      updated_by=selected_actor,
      updated_at=clock_timestamp()
  WHERE dimension.tenant_id=selected_tenant_id
    AND dimension.dataset_id=selected_dataset_id
    AND dimension.dataset_version_id=selected_dataset_version_id
    AND dimension.field_id=selected_field_id
    AND dimension.status<>'DEPRECATED'
    AND (
      NOT dimension.sensitive
      OR dimension.member_index_policy='FULL'
    );
END
$$;

REVOKE ALL ON FUNCTION platform.tighten_sensitive_field_dimensions(
  uuid,uuid,uuid,text,uuid
) FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.apply_approved_field_sensitivity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  tag_is_sensitivity boolean;
BEGIN
  IF NEW.asset_type='DATASET_FIELD' AND NEW.status='APPROVED' THEN
    SELECT EXISTS(
      SELECT 1
      FROM platform.semantic_tags AS tag
      WHERE tag.id=NEW.tag_id
        AND tag.tenant_id=NEW.tenant_id
        AND tag.status='ACTIVE'
        AND tag.category='SENSITIVITY'
    ) INTO tag_is_sensitivity;
    IF tag_is_sensitivity THEN
      PERFORM platform.tighten_sensitive_field_dimensions(
        NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,
        NEW.dataset_field_id,NEW.approved_by
      );
    END IF;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.apply_approved_field_sensitivity() FROM PUBLIC;

CREATE TRIGGER asset_tag_bindings_apply_field_sensitivity_insert
AFTER INSERT ON platform.asset_tag_bindings
FOR EACH ROW EXECUTE FUNCTION platform.apply_approved_field_sensitivity();

CREATE TRIGGER asset_tag_bindings_apply_field_sensitivity_update
AFTER UPDATE OF status,tag_id ON platform.asset_tag_bindings
FOR EACH ROW EXECUTE FUNCTION platform.apply_approved_field_sensitivity();

-- 已批准绑定可以先于标签启用存在。标签后来被编辑为 ACTIVE SENSITIVITY
-- 时也必须走相同精确字段锁，不能只在 binding 写入时收紧风险。
CREATE OR REPLACE FUNCTION platform.apply_activated_sensitivity_tag()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  approved_binding record;
BEGIN
  IF NEW.status='ACTIVE'
    AND NEW.category='SENSITIVITY'
    AND ROW(OLD.status,OLD.category) IS DISTINCT FROM
      ROW(NEW.status,NEW.category) THEN
    FOR approved_binding IN
      SELECT binding.tenant_id,binding.dataset_id,
        binding.dataset_version_id,binding.dataset_field_id
      FROM platform.asset_tag_bindings AS binding
      WHERE binding.tenant_id=NEW.tenant_id
        AND binding.tag_id=NEW.id
        AND binding.asset_type='DATASET_FIELD'
        AND binding.status='APPROVED'
      ORDER BY binding.dataset_version_id,binding.dataset_field_id
    LOOP
      PERFORM platform.tighten_sensitive_field_dimensions(
        approved_binding.tenant_id,approved_binding.dataset_id,
        approved_binding.dataset_version_id,
        approved_binding.dataset_field_id,NEW.updated_by
      );
    END LOOP;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.apply_activated_sensitivity_tag() FROM PUBLIC;

CREATE TRIGGER semantic_tags_apply_activated_sensitivity
AFTER UPDATE OF status,category ON platform.semantic_tags
FOR EACH ROW EXECUTE FUNCTION platform.apply_activated_sensitivity_tag();

CREATE OR REPLACE FUNCTION platform.enforce_dimension_survey_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '维度勘测运行不可删除' USING ERRCODE='23514';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,
    NEW.schema_hash,NEW.survey_version,NEW.requested_by,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.dataset_id,OLD.dataset_version_id,
    OLD.schema_hash,OLD.survey_version,OLD.requested_by,OLD.created_at
  ) THEN
    RAISE EXCEPTION '维度勘测运行身份不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status='WAITING_MATERIALIZATION'
    AND NEW.status IN ('SUCCEEDED','STALE') THEN
    RETURN NEW;
  END IF;
  IF OLD.status='SUCCEEDED' AND NEW.status='STALE' THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '维度勘测运行状态转换无效'
    USING ERRCODE='23514';
END
$$;

CREATE TRIGGER dimension_survey_runs_transition
BEFORE UPDATE OR DELETE ON platform.dimension_survey_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_survey_run_transition();

CREATE OR REPLACE FUNCTION platform.enforce_dimension_survey_candidate_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '维度勘测候选不可删除' USING ERRCODE='23514';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.survey_run_id,NEW.dataset_id,
    NEW.dataset_version_id,NEW.schema_hash,NEW.materialization_id,
    NEW.materialization_snapshot_hash,NEW.field_id,NEW.field_code,
    NEW.field_role,NEW.canonical_type,NEW.semantic_type,
    NEW.risk_high_cardinality,NEW.risk_sensitive,NEW.evidence_json,
    NEW.generated_by,NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.survey_run_id,OLD.dataset_id,
    OLD.dataset_version_id,OLD.schema_hash,OLD.materialization_id,
    OLD.materialization_snapshot_hash,OLD.field_id,OLD.field_code,
    OLD.field_role,OLD.canonical_type,OLD.semantic_type,
    OLD.risk_high_cardinality,OLD.risk_sensitive,OLD.evidence_json,
    OLD.generated_by,OLD.created_at
  ) THEN
    RAISE EXCEPTION '维度勘测候选证据不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status<>'SUGGESTED'
    OR NEW.status NOT IN ('SUGGESTED','ACCEPTED','REJECTED','STALE')
    OR NEW.version<>OLD.version+1
    OR NEW.updated_at<=OLD.updated_at THEN
    RAISE EXCEPTION '维度勘测候选状态转换无效' USING ERRCODE='23514';
  END IF;
  IF (OLD.proposed_high_cardinality AND NOT NEW.proposed_high_cardinality)
    OR (OLD.proposed_sensitive AND NOT NEW.proposed_sensitive)
    OR (
      CASE NEW.proposed_member_index_policy
        WHEN 'FULL' THEN 1 WHEN 'EXACT_ONLY' THEN 2 ELSE 3
      END
      <
      CASE OLD.proposed_member_index_policy
        WHEN 'FULL' THEN 1 WHEN 'EXACT_ONLY' THEN 2 ELSE 3
      END
    ) THEN
    RAISE EXCEPTION '维度勘测风险策略只能收紧' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dimension_survey_candidates_transition
BEFORE UPDATE OR DELETE ON platform.dimension_survey_candidates
FOR EACH ROW EXECUTE FUNCTION platform.enforce_dimension_survey_candidate_transition();

CREATE OR REPLACE FUNCTION platform.enqueue_dws_dimension_survey()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.status='PUBLISHED'
    AND OLD.status IS DISTINCT FROM 'PUBLISHED'
    AND NEW.layer='DWS' THEN
    UPDATE platform.dimension_survey_candidates AS candidate
    SET status='STALE',version=version+1,
        decision_reason='DATASET_VERSION_SUPERSEDED',
        updated_by=NEW.published_by,updated_at=clock_timestamp()
    WHERE candidate.tenant_id=NEW.tenant_id
      AND candidate.dataset_id=NEW.dataset_id
      AND candidate.dataset_version_id<>NEW.id
      AND candidate.status='SUGGESTED';

    UPDATE platform.dimension_survey_runs AS run
    SET status='STALE',completed_at=clock_timestamp()
    WHERE run.tenant_id=NEW.tenant_id
      AND run.dataset_id=NEW.dataset_id
      AND run.dataset_version_id<>NEW.id
      AND run.status IN ('WAITING_MATERIALIZATION','SUCCEEDED');

    INSERT INTO platform.dimension_survey_runs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,survey_version,
      status,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      'dws-dimension-survey-v1','WAITING_MATERIALIZATION',NEW.published_by
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,survey_version
    ) WHERE status='WAITING_MATERIALIZATION' DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dws_dimension_survey() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_dimension_survey
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_dimension_survey();

CREATE OR REPLACE FUNCTION platform.materialize_dws_dimension_survey(
  selected_tenant_id uuid,
  selected_dataset_id uuid,
  selected_dataset_version_id uuid,
  selected_materialization_id uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  selected_schema_hash text;
  selected_snapshot_hash text;
  selected_row_count bigint;
  selected_actor uuid;
  selected_run_id uuid;
  selected_candidate_count integer;
BEGIN
  SELECT version.schema_hash,materialization.snapshot_hash,
    materialization.row_count,version.published_by
  INTO selected_schema_hash,selected_snapshot_hash,
    selected_row_count,selected_actor
  FROM platform.dataset_materializations AS materialization
  JOIN platform.dataset_versions AS version
    ON version.id=materialization.dataset_version_id
   AND version.dataset_id=materialization.dataset_id
   AND version.tenant_id=materialization.tenant_id
  JOIN platform.datasets AS dataset
    ON dataset.id=version.dataset_id
   AND dataset.tenant_id=version.tenant_id
  WHERE materialization.id=selected_materialization_id
    AND materialization.tenant_id=selected_tenant_id
    AND materialization.dataset_id=selected_dataset_id
    AND materialization.dataset_version_id=selected_dataset_version_id
    AND materialization.layer='DWS'
    AND materialization.status='ACTIVE'
    AND materialization.schema_hash=version.schema_hash
    AND version.layer='DWS'
    AND version.status='PUBLISHED'
    AND dataset.layer='DWS'
    AND dataset.status='PUBLISHED'
    AND dataset.current_published_version_id=version.id
    AND dataset.deleted_at IS NULL
  FOR SHARE OF materialization,version,dataset;

  IF NOT FOUND THEN
    RETURN;
  END IF;

  SELECT count(*)::integer
  INTO selected_candidate_count
  FROM platform.dataset_fields AS field
  WHERE field.tenant_id=selected_tenant_id
    AND field.dataset_version_id=selected_dataset_version_id
    AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
    AND NOT EXISTS(
      SELECT 1
      FROM platform.semantic_dimensions AS dimension
      WHERE dimension.tenant_id=field.tenant_id
        AND dimension.dataset_version_id=field.dataset_version_id
        AND dimension.field_id=field.field_id
        AND dimension.status<>'DEPRECATED'
    );

  UPDATE platform.dimension_survey_runs
  SET materialization_id=selected_materialization_id,
      materialization_snapshot_hash=selected_snapshot_hash,
      materialization_row_count=selected_row_count,
      status='SUCCEEDED',candidate_count=selected_candidate_count,
      completed_at=clock_timestamp()
  WHERE id=(
    SELECT id
    FROM platform.dimension_survey_runs
    WHERE tenant_id=selected_tenant_id
      AND dataset_version_id=selected_dataset_version_id
      AND survey_version='dws-dimension-survey-v1'
      AND status='WAITING_MATERIALIZATION'
    ORDER BY created_at,id
    FOR UPDATE
    LIMIT 1
  )
  RETURNING id INTO selected_run_id;

  IF selected_run_id IS NULL THEN
    INSERT INTO platform.dimension_survey_runs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,survey_version,
      materialization_id,materialization_snapshot_hash,
      materialization_row_count,status,candidate_count,requested_by,completed_at
    ) VALUES(
      selected_tenant_id,selected_dataset_id,selected_dataset_version_id,
      selected_schema_hash,'dws-dimension-survey-v1',
      selected_materialization_id,selected_snapshot_hash,selected_row_count,
      'SUCCEEDED',selected_candidate_count,selected_actor,clock_timestamp()
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,materialization_id,survey_version
    ) WHERE materialization_id IS NOT NULL DO NOTHING
    RETURNING id INTO selected_run_id;

    IF selected_run_id IS NULL THEN
      SELECT id
      INTO selected_run_id
      FROM platform.dimension_survey_runs
      WHERE tenant_id=selected_tenant_id
        AND dataset_version_id=selected_dataset_version_id
        AND materialization_id=selected_materialization_id
        AND survey_version='dws-dimension-survey-v1';
    END IF;
  END IF;

  UPDATE platform.dimension_survey_candidates AS candidate
  SET status='STALE',version=version+1,
      decision_reason='MATERIALIZATION_SUPERSEDED',
      updated_by=selected_actor,updated_at=clock_timestamp()
  WHERE candidate.tenant_id=selected_tenant_id
    AND candidate.dataset_id=selected_dataset_id
    AND candidate.survey_run_id<>selected_run_id
    AND candidate.status='SUGGESTED';

  UPDATE platform.dimension_survey_runs AS run
  SET status='STALE',completed_at=clock_timestamp()
  WHERE run.tenant_id=selected_tenant_id
    AND run.dataset_id=selected_dataset_id
    AND run.id<>selected_run_id
    AND run.status IN ('WAITING_MATERIALIZATION','SUCCEEDED');

  INSERT INTO platform.dimension_survey_candidates(
    tenant_id,survey_run_id,dataset_id,dataset_version_id,schema_hash,
    materialization_id,materialization_snapshot_hash,
    field_id,field_code,field_role,canonical_type,semantic_type,
    risk_high_cardinality,risk_sensitive,evidence_json,
    proposed_code,proposed_name,proposed_description,
    proposed_dimension_type,proposed_member_index_policy,
    proposed_high_cardinality,proposed_sensitive,
    generated_by,updated_by
  )
  SELECT
    selected_tenant_id,selected_run_id,selected_dataset_id,
    selected_dataset_version_id,selected_schema_hash,
    selected_materialization_id,selected_snapshot_hash,
    field.field_id,field.field_code::text,field.field_role,
    field.canonical_type,field.semantic_type,
    risk.high_cardinality,risk.sensitive,
    jsonb_build_object(
      'surveyVersion','dws-dimension-survey-v1',
      'containsBusinessSamples',false,
      'datasetVersionId',selected_dataset_version_id::text,
      'schemaHash',selected_schema_hash,
      'materializationId',selected_materialization_id::text,
      'materializationSnapshotHash',selected_snapshot_hash,
      'materializationRowCount',selected_row_count,
      'fieldId',field.field_id,
      'fieldCode',field.field_code::text,
      'fieldRole',field.field_role,
      'canonicalType',field.canonical_type,
      'semanticType',field.semantic_type,
      'cardinalityAssessment',CASE
        WHEN risk.high_cardinality THEN 'IDENTIFIER_ROLE'
        ELSE 'NOT_PROFILED'
      END,
      'sensitivityTagCount',risk.sensitivity_tag_count
    ),
    field.field_code::text,field.field_name,
    concat('DWS 字段 ',field.field_name,' 的维度勘测候选。'),
    CASE WHEN field.field_role='TIME' THEN 'TIME' ELSE 'STANDARD' END,
    CASE
      WHEN risk.sensitive THEN 'NONE'
      WHEN risk.high_cardinality THEN 'EXACT_ONLY'
      ELSE 'FULL'
    END,
    risk.high_cardinality,risk.sensitive,
    selected_actor,selected_actor
  FROM platform.dataset_fields AS field
  CROSS JOIN LATERAL (
    SELECT
      (
        field.field_role='IDENTIFIER'
        OR upper(field.semantic_type)='IDENTIFIER'
      ) AS high_cardinality,
      count(tag.id)>0 AS sensitive,
      count(tag.id)::integer AS sensitivity_tag_count
    FROM platform.asset_tag_bindings AS binding
    JOIN platform.semantic_tags AS tag
      ON tag.id=binding.tag_id
     AND tag.tenant_id=binding.tenant_id
     AND tag.category='SENSITIVITY'
     AND tag.status='ACTIVE'
    WHERE binding.tenant_id=field.tenant_id
      AND binding.asset_type='DATASET_FIELD'
      AND binding.dataset_id=selected_dataset_id
      AND binding.dataset_version_id=field.dataset_version_id
      AND binding.dataset_field_id=field.field_id
      AND binding.status='APPROVED'
  ) AS risk
  WHERE field.tenant_id=selected_tenant_id
    AND field.dataset_version_id=selected_dataset_version_id
    AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
    AND NOT EXISTS(
      SELECT 1
      FROM platform.semantic_dimensions AS dimension
      WHERE dimension.tenant_id=field.tenant_id
        AND dimension.dataset_version_id=field.dataset_version_id
        AND dimension.field_id=field.field_id
        AND dimension.status<>'DEPRECATED'
    )
  ON CONFLICT(tenant_id,materialization_id,field_id) DO NOTHING;
END
$$;

REVOKE ALL ON FUNCTION platform.materialize_dws_dimension_survey(
  uuid,uuid,uuid,uuid
) FROM PUBLIC;

CREATE OR REPLACE FUNCTION platform.complete_dws_dimension_survey()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.layer='DWS'
    AND NEW.status='ACTIVE'
    AND (
      TG_OP='INSERT'
      OR OLD.status IS DISTINCT FROM 'ACTIVE'
    ) THEN
    PERFORM platform.materialize_dws_dimension_survey(
      NEW.tenant_id,NEW.dataset_id,NEW.dataset_version_id,NEW.id
    );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.complete_dws_dimension_survey() FROM PUBLIC;

CREATE TRIGGER dataset_materializations_complete_dimension_survey
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.complete_dws_dimension_survey();

-- 当前发布 DWS 先登记 waiting run；已有 ACTIVE 物化随后通过同一 helper 回填。
INSERT INTO platform.dimension_survey_runs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,survey_version,
  status,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  'dws-dimension-survey-v1','WAITING_MATERIALIZATION',version.published_by
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
WHERE version.status='PUBLISHED'
  AND version.layer='DWS'
  AND dataset.status='PUBLISHED'
  AND dataset.layer='DWS'
  AND dataset.deleted_at IS NULL
ON CONFLICT(
  tenant_id,dataset_version_id,survey_version
) WHERE status='WAITING_MATERIALIZATION' DO NOTHING;

DO $$
DECLARE
  materialization_record record;
BEGIN
  FOR materialization_record IN
    SELECT materialization.tenant_id,materialization.dataset_id,
      materialization.dataset_version_id,materialization.id
    FROM platform.dataset_materializations AS materialization
    JOIN platform.datasets AS dataset
      ON dataset.id=materialization.dataset_id
     AND dataset.tenant_id=materialization.tenant_id
     AND dataset.current_published_version_id=materialization.dataset_version_id
    WHERE materialization.layer='DWS'
      AND materialization.status='ACTIVE'
      AND dataset.status='PUBLISHED'
      AND dataset.deleted_at IS NULL
    ORDER BY materialization.tenant_id,materialization.dataset_id
  LOOP
    PERFORM platform.materialize_dws_dimension_survey(
      materialization_record.tenant_id,
      materialization_record.dataset_id,
      materialization_record.dataset_version_id,
      materialization_record.id
    );
  END LOOP;
END
$$;

ALTER TABLE platform.dimension_survey_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_survey_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_survey_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dimension_survey_candidates FORCE ROW LEVEL SECURITY;

CREATE POLICY dimension_survey_runs_tenant_isolation
  ON platform.dimension_survey_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE POLICY dimension_survey_candidates_tenant_isolation
  ON platform.dimension_survey_candidates
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dimension_survey_runs IS
  'DWS 发布到精确 ACTIVE 物化之间的可审计维度勘测运行；不读取业务行';
COMMENT ON TABLE platform.dimension_survey_candidates IS
  '固定字段、版本、schema、物化证据的可编辑维度候选；人工接受前不是正式维度';
COMMENT ON COLUMN platform.dimension_survey_candidates.evidence_json IS
  '只含控制面元数据和物化摘要，禁止样本、SQL、表达式和凭据';
COMMENT ON CONSTRAINT semantic_dimensions_high_cardinality_index_policy_check
  ON platform.semantic_dimensions IS
  '高基数维度禁止 FULL DISTINCT 扫描，只能精确匹配或关闭成员索引';
