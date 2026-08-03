BEGIN;

ALTER TABLE platform.semantic_releases
  ADD COLUMN evaluation_set_id uuid,
  ADD COLUMN evaluation_set_content_hash text NOT NULL DEFAULT ''
    CHECK(
      evaluation_set_content_hash=''
      OR evaluation_set_content_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD CONSTRAINT semantic_releases_evaluation_set_fk
    FOREIGN KEY(evaluation_set_id,tenant_id)
    REFERENCES platform.semantic_golden_question_sets(id,tenant_id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT semantic_releases_evaluation_shape_check CHECK(
    (evaluation_set_id IS NULL AND evaluation_set_content_hash='')
    OR
    (evaluation_set_id IS NOT NULL
      AND evaluation_set_content_hash ~ '^[0-9a-f]{64}$')
  );

CREATE OR REPLACE FUNCTION platform.semantic_evaluation_set_passes(
  p_set_id uuid,
  p_semantic_version text,
  p_semantic_content_hash text
) RETURNS boolean
LANGUAGE sql
STABLE
SET search_path=pg_catalog,platform,public
AS $function$
  WITH question_cases AS (
    SELECT question.id,question.priority,question.answerable,
      question.security_expectation,question.independent_review_count,
      latest.status AS run_status,latest.direct_answer,latest.refusal,
      latest.unauthorized_blocked,latest.sensitive_leak_detected,
      latest.semantic_version,latest.semantic_content_hash
    FROM platform.semantic_golden_questions AS question
    LEFT JOIN LATERAL(
      SELECT run.status,run.direct_answer,run.refusal,
        run.unauthorized_blocked,run.sensitive_leak_detected,
        run.semantic_version,run.semantic_content_hash
      FROM platform.semantic_golden_question_runs AS run
      WHERE run.golden_question_id=question.id
        AND run.evaluation_mode='END_TO_END_RESULT_EQUIVALENCE'
      ORDER BY run.created_at DESC,run.id DESC
      LIMIT 1
    ) AS latest ON true
    WHERE question.set_id=p_set_id AND question.status='ACTIVE'
  ), aggregate_result AS (
    SELECT count(*)::float8 AS total_cases,
      count(*) FILTER(WHERE run_status IS NOT NULL)::float8 AS evaluated_cases,
      count(*) FILTER(WHERE independent_review_count=2)::float8 AS reviewed_cases,
      count(*) FILTER(WHERE answerable)::float8 AS answerable_cases,
      count(*) FILTER(WHERE answerable AND run_status='PASSED')::float8
        AS strict_passed,
      count(*) FILTER(WHERE answerable AND priority='P0')::float8 AS p0_cases,
      count(*) FILTER(WHERE answerable AND priority='P0'
        AND run_status='PASSED')::float8 AS p0_passed,
      count(*) FILTER(WHERE security_expectation<>'NONE')::float8
        AS security_cases,
      count(*) FILTER(WHERE security_expectation<>'NONE'
        AND run_status='PASSED' AND refusal)::float8 AS security_passed,
      count(*) FILTER(WHERE security_expectation='UNAUTHORIZED_BLOCK')::float8
        AS unauthorized_cases,
      count(*) FILTER(WHERE security_expectation='UNAUTHORIZED_BLOCK'
        AND run_status='PASSED' AND unauthorized_blocked)::float8
        AS unauthorized_passed,
      count(*) FILTER(WHERE answerable AND direct_answer)::float8
        AS direct_answers,
      count(*) FILTER(WHERE refusal)::float8 AS refusals,
      count(*) FILTER(WHERE refusal AND NOT answerable
        AND run_status='PASSED')::float8 AS correct_refusals,
      count(*) FILTER(WHERE sensitive_leak_detected)::float8 AS sensitive_leaks,
      bool_and(
        run_status IS NOT NULL
        AND semantic_version=p_semantic_version
        AND semantic_content_hash=p_semantic_content_hash
      ) AS release_pinned
    FROM question_cases
  ), rates AS (
    SELECT aggregate_result.*,
      strict_passed/NULLIF(answerable_cases,0) AS strict_accuracy,
      (
        strict_passed/NULLIF(answerable_cases,0)
        + 3.841458820694125/(2*NULLIF(answerable_cases,0))
        - 1.959963984540054*sqrt((
          (strict_passed/NULLIF(answerable_cases,0))
          *(1-(strict_passed/NULLIF(answerable_cases,0)))
          + 3.841458820694125/(4*NULLIF(answerable_cases,0))
        )/NULLIF(answerable_cases,0))
      )/(1+3.841458820694125/NULLIF(answerable_cases,0)) AS wilson_lower_bound
    FROM aggregate_result
  )
  SELECT COALESCE(bool_and(
    question_set.status='ACTIVE'
    AND question_set.dataset_split='SEALED'
    AND question_set.evaluation_mode='END_TO_END_RESULT_EQUIVALENCE'
    AND question_set.sealed_content_hash ~ '^[0-9a-f]{64}$'
    AND rates.total_cases>=2000
    AND rates.evaluated_cases=rates.total_cases
    AND rates.reviewed_cases=rates.total_cases
    AND rates.release_pinned
    AND rates.answerable_cases>0
    AND rates.strict_accuracy>=greatest(0.96,question_set.correctness_threshold)
    AND rates.wilson_lower_bound>=0.95
    AND rates.p0_cases>0 AND rates.p0_passed=rates.p0_cases
    AND rates.security_cases>0 AND rates.security_passed=rates.security_cases
    AND rates.unauthorized_cases>0
    AND rates.unauthorized_passed=rates.unauthorized_cases
    AND rates.sensitive_leaks=0
    AND rates.direct_answers/rates.answerable_cases>=0.85
    AND rates.refusals>0
    AND rates.correct_refusals/rates.refusals>=0.95
  ),false)
  FROM platform.semantic_golden_question_sets AS question_set
  CROSS JOIN rates
  WHERE question_set.id=p_set_id
    AND question_set.tenant_id=platform.current_tenant_id()
$function$;

REVOKE ALL ON FUNCTION platform.semantic_evaluation_set_passes(uuid,text,text)
  FROM PUBLIC;

COMMENT ON FUNCTION platform.semantic_evaluation_set_passes(uuid,text,text) IS
  '在当前租户 RLS 范围内复算 sealed E2E 门禁，并强制每条最新评测运行与待激活语义版本/内容哈希完全一致';
COMMENT ON COLUMN platform.semantic_releases.evaluation_set_id IS
  '已有活动版本后的激活必须绑定通过数据库复算门禁的 sealed E2E 黄金集';

COMMIT;
