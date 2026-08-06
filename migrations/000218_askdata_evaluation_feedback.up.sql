-- Governed AskData evaluation sets, independent review evidence, immutable
-- evaluation outcomes and actor-owned feedback. This migration deliberately
-- does not add a release activation or evaluation-gate function: DB-007 and
-- DB-008 remain responsible for database-recomputed gates and approvals.

ALTER TABLE askdata.releases
  ADD CONSTRAINT askdata_releases_evaluation_pin_key UNIQUE(
    id,semantic_version,content_hash,domain_id,tenant_id
  );

CREATE OR REPLACE FUNCTION askdata.evaluation_control_can_access(
  selected_tenant_id uuid,
  selected_domain_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
  SELECT askdata.tenant_matches(selected_tenant_id)
    AND askdata.domain_can_access(selected_domain_id)
    AND (
      askdata.system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(selected_domain_id)
    )
$$;

CREATE TABLE askdata.evaluation_sets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  code citext NOT NULL CHECK(
    code::text ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'
  ),
  version_no integer NOT NULL CHECK(version_no>0),
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK(
    length(description)<=4000 AND description !~ '[[:cntrl:]]'
  ),
  dataset_split text NOT NULL CHECK(dataset_split IN (
    'TRAIN','VALIDATION','SEALED','PRODUCTION_REGRESSION'
  )),
  evaluation_mode text NOT NULL CHECK(evaluation_mode IN (
    'FIXTURE_REGRESSION','END_TO_END_RESULT_EQUIVALENCE'
  )),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN (
    'DRAFT','SEALED','RETIRED'
  )),
  target_release_id uuid,
  target_semantic_version text CHECK(
    target_semantic_version IS NULL OR (
      length(target_semantic_version) BETWEEN 3 AND 128
      AND target_semantic_version=btrim(target_semantic_version)
      AND target_semantic_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{2,127}$'
    )
  ),
  target_release_content_hash text CHECK(
    target_release_content_hash IS NULL
    OR target_release_content_hash ~ '^[0-9a-f]{64}$'
  ),
  sealed_content_hash text CHECK(
    sealed_content_hash IS NULL OR sealed_content_hash ~ '^[0-9a-f]{64}$'
  ),
  sealed_case_count integer NOT NULL DEFAULT 0 CHECK(
    sealed_case_count BETWEEN 0 AND 100000
  ),
  sealed_review_count integer NOT NULL DEFAULT 0 CHECK(
    sealed_review_count BETWEEN 0 AND 200000
  ),
  notes text NOT NULL DEFAULT '' CHECK(
    length(notes)<=4096 AND notes !~ '[[:cntrl:]]'
  ),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  sealed_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  sealed_at timestamptz,
  CONSTRAINT askdata_evaluation_sets_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_sets_identity_domain_tenant_key
    UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_evaluation_sets_code_version_key
    UNIQUE(tenant_id,domain_id,code,version_no),
  CONSTRAINT askdata_evaluation_sets_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_sets_target_release_fk FOREIGN KEY(
    target_release_id,target_semantic_version,target_release_content_hash,
    domain_id,tenant_id
  ) REFERENCES askdata.releases(
    id,semantic_version,content_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_sets_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_sets_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_sets_sealed_by_fk
    FOREIGN KEY(sealed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_sets_release_pin_shape_check CHECK(
    (
      target_release_id IS NULL
      AND target_semantic_version IS NULL
      AND target_release_content_hash IS NULL
    ) OR (
      target_release_id IS NOT NULL
      AND target_semantic_version IS NOT NULL
      AND target_release_content_hash IS NOT NULL
    )
  ),
  CONSTRAINT askdata_evaluation_sets_required_release_pin_check CHECK(
    (
      dataset_split NOT IN ('SEALED','PRODUCTION_REGRESSION')
      AND evaluation_mode<>'END_TO_END_RESULT_EQUIVALENCE'
    ) OR target_release_id IS NOT NULL
  ),
  CONSTRAINT askdata_evaluation_sets_seal_shape_check CHECK(
    (
      status='DRAFT'
      AND sealed_content_hash IS NULL
      AND sealed_case_count=0
      AND sealed_review_count=0
      AND sealed_by IS NULL
      AND sealed_at IS NULL
    ) OR (
      status IN ('SEALED','RETIRED')
      AND sealed_content_hash IS NOT NULL
      AND sealed_case_count>0
      AND sealed_review_count=sealed_case_count*2
      AND sealed_by IS NOT NULL
      AND sealed_at IS NOT NULL
    )
  )
);

CREATE INDEX askdata_evaluation_sets_lifecycle_idx
  ON askdata.evaluation_sets(
    tenant_id,domain_id,dataset_split,evaluation_mode,status,created_at DESC,id
  );
CREATE INDEX askdata_evaluation_sets_release_idx
  ON askdata.evaluation_sets(
    tenant_id,domain_id,target_release_id,target_release_content_hash,status,id
  ) WHERE target_release_id IS NOT NULL;

CREATE TABLE askdata.evaluation_cases(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  case_key text NOT NULL CHECK(
    length(case_key) BETWEEN 1 AND 128
    AND case_key=btrim(case_key)
    AND case_key !~ '[[:cntrl:]]'
  ),
  schema_version text NOT NULL CHECK(
    length(schema_version) BETWEEN 1 AND 128
    AND schema_version=btrim(schema_version)
    AND schema_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'
  ),
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  approved_question text NOT NULL DEFAULT '' CHECK(
    length(approved_question)<=4000
    AND approved_question !~ '[[:cntrl:]]'
  ),
  question_redaction_policy_hash text CHECK(
    question_redaction_policy_hash IS NULL
    OR question_redaction_policy_hash ~ '^[0-9a-f]{64}$'
  ),
  priority text NOT NULL DEFAULT 'P1' CHECK(priority IN ('P0','P1','P2')),
  answerable boolean NOT NULL DEFAULT true,
  expected_disposition text NOT NULL CHECK(expected_disposition IN (
    'DIRECT','CLARIFY','REFUSE'
  )),
  security_expectation text NOT NULL DEFAULT 'NONE' CHECK(
    security_expectation IN (
      'NONE','UNAUTHORIZED_BLOCK','PROMPT_INJECTION_BLOCK',
      'SENSITIVE_DATA_BLOCK','CACHE_ISOLATION_BLOCK'
    )
  ),
  complexity text NOT NULL CHECK(complexity IN (
    'SIMPLE','COMPOSITE','CONTEXTUAL','RELATIONAL'
  )),
  ambiguity text NOT NULL CHECK(ambiguity IN (
    'NONE','METRIC','DIMENSION','MEMBER','CROSS_DOMAIN','MULTIPLE'
  )),
  expected_path_hash text CHECK(
    expected_path_hash IS NULL OR expected_path_hash ~ '^[0-9a-f]{64}$'
  ),
  expected_ir_hash text CHECK(
    expected_ir_hash IS NULL OR expected_ir_hash ~ '^[0-9a-f]{64}$'
  ),
  expected_result_hash text CHECK(
    expected_result_hash IS NULL OR expected_result_hash ~ '^[0-9a-f]{64}$'
  ),
  fixture_artifact_ref text NOT NULL DEFAULT '' CHECK(
    length(fixture_artifact_ref)<=512
    AND fixture_artifact_ref=btrim(fixture_artifact_ref)
    AND fixture_artifact_ref !~ '[[:cntrl:]]'
    AND (
      fixture_artifact_ref=''
      OR (
        fixture_artifact_ref ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$'
        AND fixture_artifact_ref !~ '(^|/)\.{1,2}(/|$)'
      )
    )
  ),
  fixture_artifact_hash text CHECK(
    fixture_artifact_hash IS NULL OR fixture_artifact_hash ~ '^[0-9a-f]{64}$'
  ),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  independent_review_count smallint NOT NULL DEFAULT 0 CHECK(
    independent_review_count BETWEEN 0 AND 2
  ),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_by uuid NOT NULL,
  content_updated_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_cases_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_cases_identity_set_domain_tenant_key
    UNIQUE(id,evaluation_set_id,domain_id,tenant_id),
  CONSTRAINT askdata_evaluation_cases_set_case_key
    UNIQUE(tenant_id,evaluation_set_id,case_key),
  CONSTRAINT askdata_evaluation_cases_set_question_key
    UNIQUE(tenant_id,evaluation_set_id,question_hash),
  CONSTRAINT askdata_evaluation_cases_set_fk
    FOREIGN KEY(evaluation_set_id,domain_id,tenant_id)
    REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_cases_created_by_fk
    FOREIGN KEY(created_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_cases_content_updated_by_fk
    FOREIGN KEY(content_updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_cases_updated_by_fk
    FOREIGN KEY(updated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_cases_direct_shape_check CHECK(
    expected_disposition<>'DIRECT' OR (
      expected_ir_hash IS NOT NULL AND expected_result_hash IS NOT NULL
    )
  ),
  CONSTRAINT askdata_evaluation_cases_answerability_shape_check CHECK(
    answerable OR expected_disposition='REFUSE'
  ),
  CONSTRAINT askdata_evaluation_cases_relational_path_check CHECK(
    complexity<>'RELATIONAL'
    OR expected_disposition<>'DIRECT'
    OR expected_path_hash IS NOT NULL
  ),
  CONSTRAINT askdata_evaluation_cases_redaction_shape_check CHECK(
    (approved_question='' AND question_redaction_policy_hash IS NULL)
    OR (
      approved_question<>'' AND question_redaction_policy_hash IS NOT NULL
    )
  ),
  CONSTRAINT askdata_evaluation_cases_security_shape_check CHECK(
    security_expectation='NONE'
    OR (NOT answerable AND expected_disposition='REFUSE')
  ),
  CONSTRAINT askdata_evaluation_cases_fixture_shape_check CHECK(
    (fixture_artifact_ref='' AND fixture_artifact_hash IS NULL)
    OR (fixture_artifact_ref<>'' AND fixture_artifact_hash IS NOT NULL)
  )
);

CREATE INDEX askdata_evaluation_cases_set_idx
  ON askdata.evaluation_cases(
    tenant_id,domain_id,evaluation_set_id,priority,case_key,id
  );
CREATE INDEX askdata_evaluation_cases_gate_idx
  ON askdata.evaluation_cases(
    tenant_id,evaluation_set_id,priority,answerable,
    security_expectation,independent_review_count,id
  );

CREATE TABLE askdata.evaluation_case_reviews(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  evaluation_case_id uuid NOT NULL,
  review_slot smallint NOT NULL CHECK(review_slot BETWEEN 1 AND 2),
  reviewer_id uuid NOT NULL,
  decision text NOT NULL CHECK(decision IN ('APPROVED','REJECTED')),
  reviewed_case_content_hash text NOT NULL CHECK(
    reviewed_case_content_hash ~ '^[0-9a-f]{64}$'
  ),
  review_comment text NOT NULL DEFAULT '' CHECK(
    length(review_comment)<=2000
    AND review_comment !~ '[[:cntrl:]]'
  ),
  review_hash text NOT NULL CHECK(review_hash ~ '^[0-9a-f]{64}$'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_case_reviews_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_case_reviews_slot_key
    UNIQUE(tenant_id,evaluation_case_id,review_slot),
  CONSTRAINT askdata_evaluation_case_reviews_reviewer_key
    UNIQUE(tenant_id,evaluation_case_id,reviewer_id),
  CONSTRAINT askdata_evaluation_case_reviews_case_fk FOREIGN KEY(
    evaluation_case_id,evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_cases(
    id,evaluation_set_id,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_case_reviews_reviewer_fk
    FOREIGN KEY(reviewer_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_evaluation_case_reviews_case_idx
  ON askdata.evaluation_case_reviews(
    tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot
  );

CREATE TABLE askdata.evaluation_runs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  evaluation_batch_id uuid NOT NULL,
  evaluation_set_id uuid NOT NULL,
  evaluation_case_id uuid NOT NULL,
  evaluation_set_content_hash text NOT NULL CHECK(
    evaluation_set_content_hash ~ '^[0-9a-f]{64}$'
  ),
  case_content_hash text NOT NULL CHECK(case_content_hash ~ '^[0-9a-f]{64}$'),
  release_id uuid NOT NULL,
  semantic_version text NOT NULL CHECK(
    length(semantic_version) BETWEEN 3 AND 128
    AND semantic_version=btrim(semantic_version)
    AND semantic_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{2,127}$'
  ),
  release_content_hash text NOT NULL CHECK(
    release_content_hash ~ '^[0-9a-f]{64}$'
  ),
  evaluation_mode text NOT NULL CHECK(evaluation_mode IN (
    'FIXTURE_REGRESSION','END_TO_END_RESULT_EQUIVALENCE'
  )),
  runner_version text NOT NULL CHECK(
    length(runner_version) BETWEEN 1 AND 128
    AND runner_version=btrim(runner_version)
    AND runner_version ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'
  ),
  run_key_hash text NOT NULL CHECK(run_key_hash ~ '^[0-9a-f]{64}$'),
  warehouse_snapshot_hash text CHECK(
    warehouse_snapshot_hash IS NULL
    OR warehouse_snapshot_hash ~ '^[0-9a-f]{64}$'
  ),
  warehouse_freshness_at timestamptz,
  status text NOT NULL CHECK(status IN ('PASSED','FAILED','ERROR')),
  expected_disposition text NOT NULL CHECK(expected_disposition IN (
    'DIRECT','CLARIFY','REFUSE'
  )),
  actual_disposition text NOT NULL CHECK(actual_disposition IN (
    'DIRECT','CLARIFY','REFUSE','ERROR'
  )),
  direct_answer boolean GENERATED ALWAYS AS (
    actual_disposition='DIRECT'
  ) STORED,
  clarification boolean GENERATED ALWAYS AS (
    actual_disposition='CLARIFY'
  ) STORED,
  refusal boolean GENERATED ALWAYS AS (
    actual_disposition='REFUSE'
  ) STORED,
  expected_ir_hash text CHECK(
    expected_ir_hash IS NULL OR expected_ir_hash ~ '^[0-9a-f]{64}$'
  ),
  actual_ir_hash text CHECK(
    actual_ir_hash IS NULL OR actual_ir_hash ~ '^[0-9a-f]{64}$'
  ),
  expected_path_hash text CHECK(
    expected_path_hash IS NULL OR expected_path_hash ~ '^[0-9a-f]{64}$'
  ),
  actual_path_hash text CHECK(
    actual_path_hash IS NULL OR actual_path_hash ~ '^[0-9a-f]{64}$'
  ),
  expected_result_hash text CHECK(
    expected_result_hash IS NULL OR expected_result_hash ~ '^[0-9a-f]{64}$'
  ),
  actual_result_hash text CHECK(
    actual_result_hash IS NULL OR actual_result_hash ~ '^[0-9a-f]{64}$'
  ),
  ir_equivalent boolean NOT NULL DEFAULT false,
  path_equivalent boolean NOT NULL DEFAULT false,
  result_equivalent boolean NOT NULL DEFAULT false,
  strict_correct boolean NOT NULL,
  security_passed boolean NOT NULL,
  sensitive_leak_detected boolean NOT NULL,
  failure_stage text NOT NULL DEFAULT '' CHECK(
    failure_stage='' OR failure_stage IN (
      'INTENT','RECALL','BINDING','GRAPH','IR','PLAN',
      'EXECUTION','VALIDATION','SECURITY'
    )
  ),
  failure_code text NOT NULL DEFAULT '' CHECK(
    failure_code='' OR failure_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
  ),
  comparison_report_hash text CHECK(
    comparison_report_hash IS NULL
    OR comparison_report_hash ~ '^[0-9a-f]{64}$'
  ),
  duration_ms bigint NOT NULL CHECK(duration_ms BETWEEN 0 AND 600000),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_evaluation_runs_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_evaluation_runs_batch_case_key
    UNIQUE(tenant_id,evaluation_batch_id,evaluation_case_id),
  CONSTRAINT askdata_evaluation_runs_idempotency_key
    UNIQUE(tenant_id,run_key_hash),
  CONSTRAINT askdata_evaluation_runs_set_fk
    FOREIGN KEY(evaluation_set_id,domain_id,tenant_id)
    REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_runs_case_fk FOREIGN KEY(
    evaluation_case_id,evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_cases(
    id,evaluation_set_id,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_runs_release_fk FOREIGN KEY(
    release_id,semantic_version,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(
    id,semantic_version,content_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_evaluation_runs_warehouse_shape_check CHECK(
    (warehouse_snapshot_hash IS NULL AND warehouse_freshness_at IS NULL)
    OR (
      warehouse_snapshot_hash IS NOT NULL
      AND warehouse_freshness_at IS NOT NULL
    )
  ),
  CONSTRAINT askdata_evaluation_runs_outcome_shape_check CHECK(
    (
      status='PASSED'
      AND strict_correct
      AND security_passed
      AND NOT sensitive_leak_detected
      AND actual_disposition=expected_disposition
      AND failure_stage=''
      AND failure_code=''
    ) OR (
      status IN ('FAILED','ERROR')
      AND NOT strict_correct
      AND failure_stage<>''
      AND failure_code<>''
      AND (status<>'ERROR' OR actual_disposition='ERROR')
    )
  ),
  CONSTRAINT askdata_evaluation_runs_path_shape_check CHECK(
    NOT path_equivalent
    OR (expected_path_hash IS NOT NULL AND actual_path_hash IS NOT NULL)
  ),
  CONSTRAINT askdata_evaluation_runs_equivalence_evidence_check CHECK(
    (
      NOT ir_equivalent OR (
        expected_ir_hash IS NOT NULL AND actual_ir_hash IS NOT NULL
        AND (
          expected_ir_hash=actual_ir_hash OR comparison_report_hash IS NOT NULL
        )
      )
    ) AND (
      NOT path_equivalent OR (
        expected_path_hash IS NOT NULL AND actual_path_hash IS NOT NULL
        AND (
          expected_path_hash=actual_path_hash
          OR comparison_report_hash IS NOT NULL
        )
      )
    ) AND (
      NOT result_equivalent OR (
        expected_result_hash IS NOT NULL AND actual_result_hash IS NOT NULL
        AND (
          expected_result_hash=actual_result_hash
          OR comparison_report_hash IS NOT NULL
        )
      )
    )
  ),
  CONSTRAINT askdata_evaluation_runs_security_shape_check CHECK(
    NOT sensitive_leak_detected
    OR (NOT security_passed AND failure_stage='SECURITY')
  ),
  CONSTRAINT askdata_evaluation_runs_direct_pass_shape_check CHECK(
    status<>'PASSED' OR expected_disposition<>'DIRECT' OR (
      expected_ir_hash IS NOT NULL
      AND actual_ir_hash IS NOT NULL
      AND expected_result_hash IS NOT NULL
      AND actual_result_hash IS NOT NULL
      AND ir_equivalent
      AND (
        expected_path_hash IS NULL
        OR (actual_path_hash IS NOT NULL AND path_equivalent)
      )
      AND result_equivalent
    )
  )
);

CREATE INDEX askdata_evaluation_runs_latest_case_idx
  ON askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_set_id,evaluation_case_id,
    created_at DESC,id DESC
  );
CREATE INDEX askdata_evaluation_runs_release_gate_idx
  ON askdata.evaluation_runs(
    tenant_id,domain_id,release_id,release_content_hash,
    evaluation_set_id,status,created_at DESC,id DESC
  );
CREATE INDEX askdata_evaluation_runs_batch_idx
  ON askdata.evaluation_runs(
    tenant_id,evaluation_batch_id,status,evaluation_case_id
  );

CREATE TABLE askdata.query_feedback(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  question_run_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(
    release_content_hash ~ '^[0-9a-f]{64}$'
  ),
  policy_scope_hash text NOT NULL CHECK(
    policy_scope_hash ~ '^[0-9a-f]{64}$'
  ),
  rating text NOT NULL CHECK(rating IN ('ACCURATE','INACCURATE')),
  issue_type text NOT NULL DEFAULT 'NONE' CHECK(issue_type IN (
    'NONE','METRIC','DIMENSION','MEMBER','TIME','RELATIONSHIP',
    'DATA','PERMISSION','EXPRESSION','OTHER'
  )),
  comment text NOT NULL DEFAULT '' CHECK(
    length(comment)<=2000 AND comment !~ '[[:cntrl:]]'
  ),
  feedback_hash text NOT NULL CHECK(feedback_hash ~ '^[0-9a-f]{64}$'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_query_feedback_identity_tenant_key
    UNIQUE(id,tenant_id),
  CONSTRAINT askdata_query_feedback_actor_run_key
    UNIQUE(tenant_id,question_run_id,actor_id),
  CONSTRAINT askdata_query_feedback_run_fk FOREIGN KEY(
    question_run_id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) REFERENCES askdata.question_runs(
    id,actor_id,release_id,release_content_hash,
    policy_scope_hash,domain_id,tenant_id
  ) ON DELETE RESTRICT,
  CONSTRAINT askdata_query_feedback_issue_shape_check CHECK(
    (rating='ACCURATE' AND issue_type='NONE')
    OR (rating='INACCURATE' AND issue_type<>'NONE')
  )
);

CREATE INDEX askdata_query_feedback_issue_idx
  ON askdata.query_feedback(
    tenant_id,domain_id,issue_type,updated_at DESC,id
  ) WHERE rating='INACCURATE';

CREATE OR REPLACE FUNCTION askdata.evaluation_case_can_access(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_set_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform,askdata
AS $$
  SELECT askdata.evaluation_control_can_access(
      selected_tenant_id,selected_domain_id
    )
    AND (
      askdata.system_access()
      OR EXISTS(
        SELECT 1
        FROM askdata.evaluation_sets AS evaluation_set
        WHERE evaluation_set.id=selected_set_id
          AND evaluation_set.tenant_id=selected_tenant_id
          AND evaluation_set.domain_id=selected_domain_id
          AND evaluation_set.status='DRAFT'
      )
    )
$$;

CREATE OR REPLACE FUNCTION askdata.evaluation_set_manifest_hash(
  selected_set_id uuid
)
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  WITH selected_set AS (
    SELECT evaluation_set.*
    FROM askdata.evaluation_sets AS evaluation_set
    WHERE evaluation_set.id=selected_set_id
      AND evaluation_set.tenant_id=askdata.current_tenant_id()
  ), case_lines AS (
    SELECT string_agg(
      evaluation_case.case_key||':'||evaluation_case.id::text||':'
      ||evaluation_case.content_hash||':'||COALESCE((
        SELECT string_agg(
          review.review_slot::text||':'||review.reviewer_id::text||':'
          ||review.decision||':'||review.review_hash,
          ',' ORDER BY review.review_slot,review.reviewer_id
        )
        FROM askdata.evaluation_case_reviews AS review
        WHERE review.tenant_id=evaluation_case.tenant_id
          AND review.evaluation_case_id=evaluation_case.id
      ),''),
      E'\n' ORDER BY evaluation_case.case_key COLLATE "C",evaluation_case.id
    ) AS manifest
    FROM askdata.evaluation_cases AS evaluation_case
    JOIN selected_set
      ON selected_set.id=evaluation_case.evaluation_set_id
     AND selected_set.domain_id=evaluation_case.domain_id
     AND selected_set.tenant_id=evaluation_case.tenant_id
  )
  SELECT encode(public.digest(
    'SET:'||selected_set.id::text||':'||selected_set.domain_id::text||':'
    ||selected_set.code::text||':'||selected_set.version_no::text||':'
    ||selected_set.dataset_split||':'||selected_set.evaluation_mode||':'
    ||COALESCE(selected_set.target_release_id::text,'-')||':'
    ||COALESCE(selected_set.target_semantic_version,'-')||':'
    ||COALESCE(selected_set.target_release_content_hash,'-')||E'\n'
    ||COALESCE(case_lines.manifest,''),
    'sha256'
  ),'hex')
  FROM selected_set CROSS JOIN case_lines
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_evaluation_set_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_actor_id uuid;
  selected_case_count integer;
  selected_review_count integer;
  selected_manifest_hash text;
BEGIN
  IF TG_OP='DELETE' THEN
    IF OLD.status<>'DRAFT' THEN
      RAISE EXCEPTION 'sealed evaluation set cannot be deleted'
        USING ERRCODE='55000';
    END IF;
    RETURN OLD;
  END IF;

  selected_actor_id := askdata.current_actor_id();
  IF selected_actor_id IS NULL THEN
    RAISE EXCEPTION 'evaluation set mutation requires an actor'
      USING ERRCODE='23514';
  END IF;

  IF TG_OP='INSERT' THEN
    IF NEW.status<>'DRAFT' OR NEW.record_version<>1
      OR NEW.sealed_content_hash IS NOT NULL
      OR NEW.sealed_case_count<>0 OR NEW.sealed_review_count<>0
      OR NEW.sealed_by IS NOT NULL OR NEW.sealed_at IS NOT NULL
      OR NEW.created_by<>selected_actor_id OR NEW.updated_by<>selected_actor_id THEN
      RAISE EXCEPTION 'evaluation set initial shape is invalid'
        USING ERRCODE='23514';
    END IF;
    IF NEW.target_release_id IS NOT NULL AND NOT EXISTS(
      SELECT 1 FROM askdata.releases AS release
      WHERE release.id=NEW.target_release_id
        AND release.semantic_version=NEW.target_semantic_version
        AND release.content_hash=NEW.target_release_content_hash
        AND release.domain_id=NEW.domain_id
        AND release.tenant_id=NEW.tenant_id
        AND release.status IN ('READY','ACTIVE','SUPERSEDED')
    ) THEN
      RAISE EXCEPTION 'evaluation set requires an exact READY release pin'
        USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp();
    NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
    OR NEW.code IS DISTINCT FROM OLD.code
    OR NEW.version_no IS DISTINCT FROM OLD.version_no
    OR NEW.dataset_split IS DISTINCT FROM OLD.dataset_split
    OR NEW.evaluation_mode IS DISTINCT FROM OLD.evaluation_mode
    OR NEW.target_release_id IS DISTINCT FROM OLD.target_release_id
    OR NEW.target_semantic_version IS DISTINCT FROM OLD.target_semantic_version
    OR NEW.target_release_content_hash IS DISTINCT FROM OLD.target_release_content_hash
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'evaluation set identity, split, mode and release pin are immutable'
      USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'evaluation set record_version must increase by exactly one'
      USING ERRCODE='40001';
  END IF;
  IF NEW.updated_by<>selected_actor_id THEN
    RAISE EXCEPTION 'evaluation set updated_by must match the current actor'
      USING ERRCODE='23514';
  END IF;

  IF OLD.status='RETIRED'
    OR (OLD.status='SEALED' AND NEW.status<>'RETIRED') THEN
    RAISE EXCEPTION 'sealed evaluation set is immutable'
      USING ERRCODE='55000';
  END IF;
  IF OLD.status='SEALED' THEN
    IF NEW.name IS DISTINCT FROM OLD.name
      OR NEW.description IS DISTINCT FROM OLD.description
      OR NEW.notes IS DISTINCT FROM OLD.notes
      OR NEW.sealed_content_hash IS DISTINCT FROM OLD.sealed_content_hash
      OR NEW.sealed_case_count IS DISTINCT FROM OLD.sealed_case_count
      OR NEW.sealed_review_count IS DISTINCT FROM OLD.sealed_review_count
      OR NEW.sealed_by IS DISTINCT FROM OLD.sealed_by
      OR NEW.sealed_at IS DISTINCT FROM OLD.sealed_at THEN
      RAISE EXCEPTION 'retiring a set cannot alter sealed content'
        USING ERRCODE='55000';
    END IF;
    NEW.updated_at=clock_timestamp();
    RETURN NEW;
  END IF;

  IF NEW.status NOT IN ('DRAFT','SEALED') THEN
    RAISE EXCEPTION 'illegal evaluation set lifecycle transition'
      USING ERRCODE='23514';
  END IF;
  IF NEW.status='SEALED' THEN
    IF NEW.target_release_id IS NULL OR NOT EXISTS(
      SELECT 1 FROM askdata.releases AS release
      WHERE release.id=NEW.target_release_id
        AND release.semantic_version=NEW.target_semantic_version
        AND release.content_hash=NEW.target_release_content_hash
        AND release.domain_id=NEW.domain_id
        AND release.tenant_id=NEW.tenant_id
        AND release.status IN ('READY','ACTIVE','SUPERSEDED')
    ) THEN
      RAISE EXCEPTION 'sealing requires an exact READY release pin'
        USING ERRCODE='23514';
    END IF;

    PERFORM 1 FROM askdata.evaluation_cases AS evaluation_case
    WHERE evaluation_case.evaluation_set_id=NEW.id
      AND evaluation_case.tenant_id=NEW.tenant_id
    FOR UPDATE;
    PERFORM 1 FROM askdata.evaluation_case_reviews AS review
    WHERE review.evaluation_set_id=NEW.id
      AND review.tenant_id=NEW.tenant_id
    FOR UPDATE;

    SELECT count(*) INTO selected_case_count
    FROM askdata.evaluation_cases AS evaluation_case
    WHERE evaluation_case.evaluation_set_id=NEW.id
      AND evaluation_case.domain_id=NEW.domain_id
      AND evaluation_case.tenant_id=NEW.tenant_id;
    IF selected_case_count<1 THEN
      RAISE EXCEPTION 'cannot seal an empty evaluation set'
        USING ERRCODE='23514';
    END IF;
    IF EXISTS(
      SELECT 1
      FROM askdata.evaluation_cases AS evaluation_case
      WHERE evaluation_case.evaluation_set_id=NEW.id
        AND evaluation_case.domain_id=NEW.domain_id
        AND evaluation_case.tenant_id=NEW.tenant_id
        AND (
          evaluation_case.independent_review_count<>2
          OR 2<>(
            SELECT count(*)
            FROM askdata.evaluation_case_reviews AS review
            WHERE review.evaluation_case_id=evaluation_case.id
              AND review.tenant_id=evaluation_case.tenant_id
              AND review.decision='APPROVED'
              AND review.reviewed_case_content_hash=evaluation_case.content_hash
              AND review.reviewer_id<>evaluation_case.created_by
              AND review.reviewer_id<>evaluation_case.content_updated_by
          )
        )
    ) THEN
      RAISE EXCEPTION 'every sealed case requires two current independent approvals'
        USING ERRCODE='23514';
    END IF;
    selected_review_count := selected_case_count*2;
    selected_manifest_hash := askdata.evaluation_set_manifest_hash(NEW.id);
    IF selected_manifest_hash IS NULL
      OR selected_manifest_hash !~ '^[0-9a-f]{64}$' THEN
      RAISE EXCEPTION 'evaluation set manifest hash could not be computed'
        USING ERRCODE='23514';
    END IF;
    NEW.sealed_content_hash=selected_manifest_hash;
    NEW.sealed_case_count=selected_case_count;
    NEW.sealed_review_count=selected_review_count;
    NEW.sealed_by=selected_actor_id;
    NEW.sealed_at=clock_timestamp();
  ELSE
    IF NEW.sealed_content_hash IS NOT NULL
      OR NEW.sealed_case_count<>0 OR NEW.sealed_review_count<>0
      OR NEW.sealed_by IS NOT NULL OR NEW.sealed_at IS NOT NULL THEN
      RAISE EXCEPTION 'draft evaluation set cannot carry sealed facts'
        USING ERRCODE='23514';
    END IF;
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_evaluation_case_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_set_status text;
  selected_evaluation_mode text;
  selected_actor_id uuid;
BEGIN
  SELECT evaluation_set.status,evaluation_set.evaluation_mode
    INTO selected_set_status,selected_evaluation_mode
  FROM askdata.evaluation_sets AS evaluation_set
  WHERE evaluation_set.id=COALESCE(NEW.evaluation_set_id,OLD.evaluation_set_id)
    AND evaluation_set.domain_id=COALESCE(NEW.domain_id,OLD.domain_id)
    AND evaluation_set.tenant_id=COALESCE(NEW.tenant_id,OLD.tenant_id)
  FOR SHARE;
  IF selected_set_status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'evaluation cases can change only while their set is DRAFT'
      USING ERRCODE='55000';
  END IF;
  selected_actor_id := askdata.current_actor_id();
  IF selected_actor_id IS NULL THEN
    RAISE EXCEPTION 'evaluation case mutation requires an actor'
      USING ERRCODE='23514';
  END IF;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.record_version<>1
      OR NEW.created_by<>selected_actor_id
      OR NEW.updated_by<>selected_actor_id
      OR NEW.independent_review_count<>0 THEN
      RAISE EXCEPTION 'evaluation case initial shape is invalid'
        USING ERRCODE='23514';
    END IF;
    NEW.content_updated_by=selected_actor_id;
    NEW.created_at=clock_timestamp();
  ELSE
    IF NEW.id IS DISTINCT FROM OLD.id
      OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
      OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
      OR NEW.evaluation_set_id IS DISTINCT FROM OLD.evaluation_set_id
      OR NEW.case_key IS DISTINCT FROM OLD.case_key
      OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
      OR NEW.created_by IS DISTINCT FROM OLD.created_by
      OR NEW.content_updated_by IS DISTINCT FROM OLD.content_updated_by
      OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'evaluation case identity is immutable'
        USING ERRCODE='55000';
    END IF;
    IF NEW.record_version<>OLD.record_version+1 THEN
      RAISE EXCEPTION 'evaluation case record_version must increase by exactly one'
        USING ERRCODE='40001';
    END IF;
    IF NEW.updated_by<>selected_actor_id THEN
      RAISE EXCEPTION 'evaluation case updated_by must match the current actor'
        USING ERRCODE='23514';
    END IF;
  END IF;

  IF selected_evaluation_mode='END_TO_END_RESULT_EQUIVALENCE' THEN
    IF btrim(NEW.approved_question)='' THEN
      RAISE EXCEPTION 'end-to-end evaluation requires an approved question'
        USING ERRCODE='23514';
    END IF;
    IF NEW.expected_disposition='DIRECT'
      AND NEW.expected_result_hash IS NULL THEN
      RAISE EXCEPTION 'direct end-to-end case requires an expected result hash'
        USING ERRCODE='23514';
    END IF;
  END IF;

  NEW.content_hash := encode(public.digest(
    jsonb_build_array(
      NEW.case_key,NEW.schema_version,NEW.question_hash,
      NEW.approved_question,NEW.question_redaction_policy_hash,
      NEW.priority,NEW.answerable,
      NEW.expected_disposition,NEW.security_expectation,
      NEW.complexity,NEW.ambiguity,NEW.expected_path_hash,
      NEW.expected_ir_hash,NEW.expected_result_hash,
      NEW.fixture_artifact_ref,NEW.fixture_artifact_hash
    )::text,
    'sha256'
  ),'hex');
  IF TG_OP='UPDATE' AND NEW.content_hash IS DISTINCT FROM OLD.content_hash THEN
    NEW.content_updated_by=selected_actor_id;
  END IF;
  SELECT count(*)::smallint INTO NEW.independent_review_count
  FROM askdata.evaluation_case_reviews AS review
  WHERE review.evaluation_case_id=NEW.id
    AND review.tenant_id=NEW.tenant_id
    AND review.decision='APPROVED'
    AND review.reviewed_case_content_hash=NEW.content_hash;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_evaluation_case_review()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_set_status text;
  selected_case_hash text;
  selected_case_author uuid;
  selected_case_content_author uuid;
  selected_actor_id uuid;
BEGIN
  SELECT evaluation_set.status INTO selected_set_status
  FROM askdata.evaluation_sets AS evaluation_set
  WHERE evaluation_set.id=COALESCE(
      NEW.evaluation_set_id,OLD.evaluation_set_id
    )
    AND evaluation_set.domain_id=COALESCE(NEW.domain_id,OLD.domain_id)
    AND evaluation_set.tenant_id=COALESCE(NEW.tenant_id,OLD.tenant_id)
  FOR SHARE;
  SELECT evaluation_case.content_hash,evaluation_case.created_by,
    evaluation_case.content_updated_by
    INTO selected_case_hash,selected_case_author,selected_case_content_author
  FROM askdata.evaluation_cases AS evaluation_case
  WHERE evaluation_case.id=COALESCE(
      NEW.evaluation_case_id,OLD.evaluation_case_id
    )
    AND evaluation_case.evaluation_set_id=COALESCE(
      NEW.evaluation_set_id,OLD.evaluation_set_id
    )
    AND evaluation_case.domain_id=COALESCE(NEW.domain_id,OLD.domain_id)
    AND evaluation_case.tenant_id=COALESCE(NEW.tenant_id,OLD.tenant_id)
  FOR UPDATE;
  IF selected_set_status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'case reviews can change only while their set is DRAFT'
      USING ERRCODE='55000';
  END IF;
  IF selected_case_hash IS NULL THEN
    RAISE EXCEPTION 'case review requires an exact evaluation case'
      USING ERRCODE='23514';
  END IF;
  selected_actor_id := askdata.current_actor_id();
  IF selected_actor_id IS NULL THEN
    RAISE EXCEPTION 'case review requires an actor'
      USING ERRCODE='23514';
  END IF;
  IF TG_OP='DELETE' THEN
    IF OLD.reviewer_id<>selected_actor_id THEN
      RAISE EXCEPTION 'reviewer can delete only their own DRAFT review'
        USING ERRCODE='42501';
    END IF;
    RETURN OLD;
  END IF;
  IF NEW.reviewer_id<>selected_actor_id
    OR NEW.reviewer_id=selected_case_author
    OR NEW.reviewer_id=selected_case_content_author THEN
    RAISE EXCEPTION 'case review must be independent and actor-owned'
      USING ERRCODE='23514';
  END IF;
  IF NEW.reviewed_case_content_hash<>selected_case_hash THEN
    RAISE EXCEPTION 'case review must bind the current case content hash'
      USING ERRCODE='23514';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.record_version<>1 THEN
      RAISE EXCEPTION 'case review initial record_version must be one'
        USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp();
  ELSE
    IF NEW.id IS DISTINCT FROM OLD.id
      OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
      OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
      OR NEW.evaluation_set_id IS DISTINCT FROM OLD.evaluation_set_id
      OR NEW.evaluation_case_id IS DISTINCT FROM OLD.evaluation_case_id
      OR NEW.review_slot IS DISTINCT FROM OLD.review_slot
      OR NEW.reviewer_id IS DISTINCT FROM OLD.reviewer_id
      OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'case review identity is immutable'
        USING ERRCODE='55000';
    END IF;
    IF NEW.record_version<>OLD.record_version+1 THEN
      RAISE EXCEPTION 'case review record_version must increase by exactly one'
        USING ERRCODE='40001';
    END IF;
  END IF;
  NEW.reviewed_at=clock_timestamp();
  NEW.updated_at=NEW.reviewed_at;
  NEW.review_hash := encode(public.digest(
    jsonb_build_array(
      NEW.evaluation_case_id,NEW.review_slot,NEW.reviewer_id,
      NEW.decision,NEW.reviewed_case_content_hash,NEW.review_comment,
      NEW.reviewed_at
    )::text,
    'sha256'
  ),'hex');
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.refresh_evaluation_case_review_count()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_case_id uuid;
  selected_tenant_id uuid;
  selected_actor_id uuid;
BEGIN
  selected_case_id := COALESCE(NEW.evaluation_case_id,OLD.evaluation_case_id);
  selected_tenant_id := COALESCE(NEW.tenant_id,OLD.tenant_id);
  selected_actor_id := askdata.current_actor_id();
  UPDATE askdata.evaluation_cases AS evaluation_case SET
    independent_review_count=(
      SELECT count(*)::smallint
      FROM askdata.evaluation_case_reviews AS review
      WHERE review.evaluation_case_id=evaluation_case.id
        AND review.tenant_id=evaluation_case.tenant_id
        AND review.decision='APPROVED'
        AND review.reviewed_case_content_hash=evaluation_case.content_hash
    ),
    record_version=evaluation_case.record_version+1,
    updated_by=selected_actor_id
  WHERE evaluation_case.id=selected_case_id
    AND evaluation_case.tenant_id=selected_tenant_id;
  IF TG_OP='DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_evaluation_run_append()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_set askdata.evaluation_sets%ROWTYPE;
  selected_case askdata.evaluation_cases%ROWTYPE;
  current_set_hash text;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'evaluation run facts are immutable'
      USING ERRCODE='55000';
  END IF;
  SELECT * INTO selected_set
  FROM askdata.evaluation_sets AS evaluation_set
  WHERE evaluation_set.id=NEW.evaluation_set_id
    AND evaluation_set.domain_id=NEW.domain_id
    AND evaluation_set.tenant_id=NEW.tenant_id
  FOR SHARE;
  SELECT * INTO selected_case
  FROM askdata.evaluation_cases AS evaluation_case
  WHERE evaluation_case.id=NEW.evaluation_case_id
    AND evaluation_case.evaluation_set_id=NEW.evaluation_set_id
    AND evaluation_case.domain_id=NEW.domain_id
    AND evaluation_case.tenant_id=NEW.tenant_id
  FOR SHARE;
  IF selected_set.id IS NULL OR selected_case.id IS NULL THEN
    RAISE EXCEPTION 'evaluation run requires a matching set and case'
      USING ERRCODE='23514';
  END IF;
  IF selected_set.status='RETIRED' THEN
    RAISE EXCEPTION 'retired evaluation set cannot accept new runs'
      USING ERRCODE='55000';
  END IF;
  IF selected_set.dataset_split IN ('SEALED','PRODUCTION_REGRESSION')
    AND selected_set.status<>'SEALED' THEN
    RAISE EXCEPTION 'sealed and production regression splits must be sealed before running'
      USING ERRCODE='23514';
  END IF;
  current_set_hash := CASE
    WHEN selected_set.status IN ('SEALED','RETIRED')
      THEN selected_set.sealed_content_hash
    ELSE askdata.evaluation_set_manifest_hash(selected_set.id)
  END;
  IF NEW.evaluation_set_content_hash<>current_set_hash
    OR NEW.case_content_hash<>selected_case.content_hash THEN
    RAISE EXCEPTION 'evaluation run set or case content hash is stale'
      USING ERRCODE='23514';
  END IF;
  IF NEW.evaluation_mode<>selected_set.evaluation_mode
    OR NEW.expected_disposition<>selected_case.expected_disposition
    OR NEW.expected_path_hash IS DISTINCT FROM selected_case.expected_path_hash
    OR NEW.expected_ir_hash IS DISTINCT FROM selected_case.expected_ir_hash
    OR NEW.expected_result_hash IS DISTINCT FROM selected_case.expected_result_hash THEN
    RAISE EXCEPTION 'evaluation run expected facts must match the case'
      USING ERRCODE='23514';
  END IF;
  IF selected_set.target_release_id IS NOT NULL AND (
    NEW.release_id<>selected_set.target_release_id
    OR NEW.semantic_version<>selected_set.target_semantic_version
    OR NEW.release_content_hash<>selected_set.target_release_content_hash
  ) THEN
    RAISE EXCEPTION 'evaluation run does not match the set release pin'
      USING ERRCODE='23514';
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.id=NEW.release_id
      AND release.semantic_version=NEW.semantic_version
      AND release.content_hash=NEW.release_content_hash
      AND release.domain_id=NEW.domain_id
      AND release.tenant_id=NEW.tenant_id
      AND release.status IN ('READY','ACTIVE','SUPERSEDED')
  ) THEN
    RAISE EXCEPTION 'evaluation run requires an exact READY release pin'
      USING ERRCODE='23514';
  END IF;
  IF NEW.evaluation_mode='END_TO_END_RESULT_EQUIVALENCE'
    AND (
      NEW.warehouse_snapshot_hash IS NULL
      OR NEW.warehouse_freshness_at IS NULL
    ) THEN
    RAISE EXCEPTION 'end-to-end evaluation must pin warehouse freshness'
      USING ERRCODE='23514';
  END IF;
  NEW.created_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_query_feedback()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  selected_state text;
  selected_actor_id uuid;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'query feedback cannot be deleted'
      USING ERRCODE='55000';
  END IF;
  selected_actor_id := askdata.current_actor_id();
  IF selected_actor_id IS NULL OR NEW.actor_id<>selected_actor_id THEN
    RAISE EXCEPTION 'query feedback must be submitted by the run actor'
      USING ERRCODE='42501';
  END IF;
  SELECT question_run.current_state INTO selected_state
  FROM askdata.question_runs AS question_run
  WHERE question_run.id=NEW.question_run_id
    AND question_run.actor_id=NEW.actor_id
    AND question_run.release_id=NEW.release_id
    AND question_run.release_content_hash=NEW.release_content_hash
    AND question_run.policy_scope_hash=NEW.policy_scope_hash
    AND question_run.domain_id=NEW.domain_id
    AND question_run.tenant_id=NEW.tenant_id;
  IF selected_state IS NULL OR selected_state NOT IN (
    'CLARIFICATION_REQUIRED','ANSWERED','BLOCKED'
  ) THEN
    RAISE EXCEPTION 'query feedback requires a terminal question run'
      USING ERRCODE='23514';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.record_version<>1 THEN
      RAISE EXCEPTION 'query feedback initial record_version must be one'
        USING ERRCODE='23514';
    END IF;
    NEW.created_at=clock_timestamp();
  ELSE
    IF NEW.id IS DISTINCT FROM OLD.id
      OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
      OR NEW.domain_id IS DISTINCT FROM OLD.domain_id
      OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
      OR NEW.question_run_id IS DISTINCT FROM OLD.question_run_id
      OR NEW.release_id IS DISTINCT FROM OLD.release_id
      OR NEW.release_content_hash IS DISTINCT FROM OLD.release_content_hash
      OR NEW.policy_scope_hash IS DISTINCT FROM OLD.policy_scope_hash
      OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'query feedback run binding is immutable'
        USING ERRCODE='55000';
    END IF;
    IF NEW.record_version<>OLD.record_version+1 THEN
      RAISE EXCEPTION 'query feedback record_version must increase by exactly one'
        USING ERRCODE='40001';
    END IF;
  END IF;
  NEW.feedback_hash := encode(public.digest(
    jsonb_build_array(
      NEW.question_run_id,NEW.actor_id,NEW.release_id,
      NEW.release_content_hash,NEW.policy_scope_hash,
      NEW.rating,NEW.issue_type,NEW.comment
    )::text,
    'sha256'
  ),'hex');
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.seal_evaluation_set(
  selected_set_id uuid,
  selected_actor_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE
  changed integer;
BEGIN
  IF selected_set_id IS NULL
    OR selected_actor_id IS NULL
    OR selected_actor_id<>askdata.current_actor_id() THEN
    RAISE EXCEPTION 'invalid evaluation set sealing identity'
      USING ERRCODE='22023';
  END IF;
  IF askdata.evaluation_control_can_access(
      askdata.current_tenant_id(),askdata.current_domain_id()
    ) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'evaluation set sealing requires domain administration'
      USING ERRCODE='42501';
  END IF;
  UPDATE askdata.evaluation_sets SET
    status='SEALED',updated_by=selected_actor_id,
    record_version=record_version+1
  WHERE id=selected_set_id
    AND tenant_id=askdata.current_tenant_id()
    AND domain_id=askdata.current_domain_id()
    AND status='DRAFT';
  GET DIAGNOSTICS changed=ROW_COUNT;
  RETURN changed=1;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.evaluation_control_can_access(uuid,uuid),
  askdata.evaluation_case_can_access(uuid,uuid,uuid),
  askdata.evaluation_set_manifest_hash(uuid),
  askdata.enforce_evaluation_set_lifecycle(),
  askdata.enforce_evaluation_case_lifecycle(),
  askdata.enforce_evaluation_case_review(),
  askdata.refresh_evaluation_case_review_count(),
  askdata.enforce_evaluation_run_append(),
  askdata.enforce_query_feedback(),
  askdata.seal_evaluation_set(uuid,uuid)
FROM PUBLIC;

CREATE TRIGGER askdata_evaluation_sets_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.evaluation_sets
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_evaluation_set_lifecycle();

CREATE TRIGGER askdata_evaluation_cases_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.evaluation_cases
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_evaluation_case_lifecycle();

CREATE TRIGGER askdata_evaluation_case_reviews_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.evaluation_case_reviews
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_evaluation_case_review();
CREATE TRIGGER askdata_evaluation_case_reviews_refresh_count
AFTER INSERT OR UPDATE OR DELETE ON askdata.evaluation_case_reviews
FOR EACH ROW EXECUTE FUNCTION askdata.refresh_evaluation_case_review_count();

CREATE TRIGGER askdata_evaluation_runs_immutable
BEFORE INSERT OR UPDATE OR DELETE ON askdata.evaluation_runs
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_evaluation_run_append();

CREATE TRIGGER askdata_query_feedback_lifecycle
BEFORE INSERT OR UPDATE OR DELETE ON askdata.query_feedback
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_query_feedback();

ALTER TABLE askdata.evaluation_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.evaluation_sets FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_evaluation_sets_management_isolation
  ON askdata.evaluation_sets
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

ALTER TABLE askdata.evaluation_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.evaluation_cases FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_evaluation_cases_sealed_isolation
  ON askdata.evaluation_cases
  USING(askdata.evaluation_case_can_access(
    tenant_id,domain_id,evaluation_set_id
  ))
  WITH CHECK(askdata.evaluation_case_can_access(
    tenant_id,domain_id,evaluation_set_id
  ));

ALTER TABLE askdata.evaluation_case_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.evaluation_case_reviews FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_evaluation_case_reviews_sealed_isolation
  ON askdata.evaluation_case_reviews
  USING(askdata.evaluation_case_can_access(
    tenant_id,domain_id,evaluation_set_id
  ))
  WITH CHECK(askdata.evaluation_case_can_access(
    tenant_id,domain_id,evaluation_set_id
  ));

ALTER TABLE askdata.evaluation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.evaluation_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_evaluation_runs_management_isolation
  ON askdata.evaluation_runs
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

ALTER TABLE askdata.query_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.query_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_query_feedback_actor_isolation
  ON askdata.query_feedback
  USING(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id))
  WITH CHECK(askdata.question_runtime_can_access(
    tenant_id,domain_id,actor_id
  ));

COMMENT ON TABLE askdata.evaluation_sets IS
  'Versioned evaluation manifests; sealing database-recomputes two independent approvals per case and a stable content hash';
COMMENT ON TABLE askdata.evaluation_cases IS
  'Governed de-identified gold cases with a redaction-policy hash and expected path/semantic/result hashes; no SQL, parameters or result rows are stored';
COMMENT ON COLUMN askdata.evaluation_cases.question_redaction_policy_hash IS
  'Required for every approved question so de-identification policy is an explicit, sealed case fact';
COMMENT ON COLUMN askdata.evaluation_cases.content_updated_by IS
  'Database-maintained author of the current case content hash; that actor cannot independently review the same hash';
COMMENT ON TABLE askdata.evaluation_case_reviews IS
  'At most two actor-owned independent reviews bound to the exact current case content hash';
COMMENT ON TABLE askdata.evaluation_runs IS
  'Append-only per-case evaluation facts pinned to set, case, semantic release version/hash, governed path and warehouse freshness';
COMMENT ON COLUMN askdata.evaluation_runs.sensitive_leak_detected IS
  'Explicit required security fact with no default; DB-007 release gates must database-recompute and require zero leaks';
COMMENT ON TABLE askdata.query_feedback IS
  'Actor-owned structured feedback for one terminal question run; it never mutates an answer or production semantic object';
