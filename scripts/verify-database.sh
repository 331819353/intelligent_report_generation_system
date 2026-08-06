#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
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

compose() {
  docker compose --env-file "$ROOT_DIR/.env.example" --env-file "$ENV_FILE" "$@"
}

APP_ROLE=${POSTGRES_APP_USER:-report_app}
WORKER_ROLE=${POSTGRES_WORKER_USER:-report_worker}
CONNECTION_TEST_ROLE=${POSTGRES_CONNECTION_TEST_USER:-report_connection_tester}
ADMIN_ROLE=${POSTGRES_USER:-report_admin}
if [ "$APP_ROLE" = "$WORKER_ROLE" ] ||
  [ "$APP_ROLE" = "$CONNECTION_TEST_ROLE" ] ||
  [ "$WORKER_ROLE" = "$CONNECTION_TEST_ROLE" ] ||
  [ "$APP_ROLE" = "$ADMIN_ROLE" ] ||
  [ "$WORKER_ROLE" = "$ADMIN_ROLE" ] ||
  [ "$CONNECTION_TEST_ROLE" = "$ADMIN_ROLE" ]; then
  echo "admin and all runtime database roles must be distinct" >&2
  exit 1
fi

compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 \
  -U "${POSTGRES_USER:-report_admin}" \
  -d "${POSTGRES_DB:-intelligent_report_control}" \
  --set=app_user="$APP_ROLE" \
  --set=worker_user="$WORKER_ROLE" \
  --set=connection_test_user="$CONNECTION_TEST_ROLE" <<'SQL'
SELECT (
  count(*)=3
  AND bool_and(
    rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
    AND NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit
  )
) AS dedicated_roles_secure
FROM pg_roles
WHERE rolname IN (:'app_user',:'worker_user',:'connection_test_user')
\gset
\if :dedicated_roles_secure
\else
  \echo 'dedicated database role attributes are unsafe'
  \quit 1
\endif

DO $$
DECLARE
  relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'tenants','users','roles','permissions','role_permissions','user_roles',
    'business_domains','domain_memberships','domain_access_applications',
    'data_sources','data_source_versions','data_source_publication_requests',
    'data_source_connection_test_jobs','metadata_tables','metadata_columns',
    'datasets','dataset_versions','dataset_draft_revisions',
    'dataset_publication_requests','dataset_dependencies',
    'dataset_materializations','dataset_build_runs',
    'dws_modeling_jobs','dws_modeling_outputs'
  ] LOOP
    IF to_regclass('platform.'||relation_name) IS NULL THEN
      RAISE EXCEPTION 'missing retained relation platform.%', relation_name;
    END IF;
  END LOOP;
END
$$;

DO $$
DECLARE
  relation_name text;
  member_lookup_definition text;
  member_lookup_result text;
BEGIN
  IF to_regnamespace('askdata') IS NULL THEN
    RAISE EXCEPTION 'missing askdata control-plane schema';
  END IF;
  FOREACH relation_name IN ARRAY ARRAY[
    'audit_events','domains','entities','semantic_models','measures','metrics',
    'metric_versions','metric_version_measures','dimensions','hierarchies',
    'hierarchy_levels','relationships','quality_rules','business_terms',
    'dimension_members','dimension_member_aliases','semantic_aliases',
    'search_documents','embedding_outbox','releases','release_objects',
    'release_projections','release_projection_artifacts','release_state',
    'release_events','graph_plan_cache','dimension_profile_jobs',
    'dimension_profiles','dimension_profile_members','question_runs',
    'question_run_events','question_artifacts','tool_calls','evaluation_sets',
    'evaluation_cases','evaluation_case_reviews','evaluation_runs',
    'query_feedback'
  ] LOOP
    IF to_regclass('askdata.'||relation_name) IS NULL THEN
      RAISE EXCEPTION 'missing askdata relation: askdata.%', relation_name;
    END IF;
    IF NOT EXISTS(
      SELECT 1
      FROM pg_class AS relation
      JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
      WHERE namespace.nspname='askdata' AND relation.relname=relation_name
        AND relation.relrowsecurity AND relation.relforcerowsecurity
    ) THEN
      RAISE EXCEPTION 'askdata.% must enable and force RLS', relation_name;
    END IF;
  END LOOP;

  IF EXISTS(
    SELECT 1
    FROM pg_constraint AS constraint_record
    JOIN pg_class AS relation ON relation.oid=constraint_record.conrelid
    JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
    WHERE namespace.nspname='askdata' AND constraint_record.contype='f'
      AND NOT EXISTS(
        SELECT 1
        FROM unnest(constraint_record.conkey) AS key(attnum)
        JOIN pg_attribute AS attribute
          ON attribute.attrelid=relation.oid AND attribute.attnum=key.attnum
        WHERE attribute.attname='tenant_id'
      )
  ) THEN
    RAISE EXCEPTION 'every askdata foreign key must include tenant_id';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.search_documents'::regclass
      AND attribute.attname='embedding'
      AND format_type(attribute.atttypid,attribute.atttypmod)='halfvec(2560)'
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_index AS index_record
    JOIN pg_class AS index_relation ON index_relation.oid=index_record.indexrelid
    JOIN pg_am AS access_method ON access_method.oid=index_relation.relam
    WHERE index_record.indrelid='askdata.search_documents'::regclass
      AND access_method.amname='hnsw'
  ) THEN
    RAISE EXCEPTION 'askdata halfvec(2560) HNSW index is missing';
  END IF;

  IF to_regprocedure('askdata.start_release_projection(uuid,uuid,jsonb)') IS NULL
    OR to_regprocedure('askdata.claim_release_projection(uuid,text,integer)') IS NULL
    OR to_regprocedure('askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb)') IS NULL
    OR to_regprocedure('askdata.fail_release_projection(uuid,uuid,text,uuid,text,boolean)') IS NULL
    OR to_regprocedure('askdata.activate_release(uuid,uuid)') IS NOT NULL THEN
    RAISE EXCEPTION 'askdata projection boundary is incomplete or unsafe activation exists before evaluation gates';
  END IF;

  IF to_regprocedure('askdata.question_audit_json_is_safe(jsonb)') IS NULL
    OR to_regprocedure(
      'askdata.question_runtime_can_access(uuid,uuid,uuid)'
    ) IS NULL
    OR to_regprocedure(
      'askdata.lock_active_question_release(uuid,uuid,uuid,text)'
    ) IS NULL
    OR to_regprocedure(
      'askdata.valid_question_run_transition(text,text)'
    ) IS NULL
    OR to_regprocedure('askdata.enforce_question_run_lifecycle()') IS NULL
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.question_runs'::regclass
        AND tgname='askdata_question_runs_lifecycle' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.question_run_events'::regclass
        AND tgname='askdata_question_run_events_immutable' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.question_artifacts'::regclass
        AND tgname='askdata_question_artifacts_immutable' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.tool_calls'::regclass
        AND tgname='askdata_tool_calls_immutable' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.question_runs'::regclass
        AND conname='askdata_question_runs_completion_artifact_fk'
        AND contype='f'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.tool_calls'::regclass
        AND conname='askdata_tool_calls_question_call_key'
        AND contype='u'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.question_run_events'::regclass
        AND conname='askdata_question_run_events_type_shape_check'
        AND contype='c'
    ) THEN
    RAISE EXCEPTION 'askdata question runtime audit boundary is incomplete';
  END IF;

  IF position(
       'terminal state is immutable' IN pg_get_functiondef(
         'askdata.enforce_question_run_lifecycle()'::regprocedure
       )
     )=0 OR position(
       'lock_active_question_release' IN pg_get_functiondef(
         'askdata.enforce_question_run_lifecycle()'::regprocedure
       )
     )=0 OR position(
       'run hashes must form a contiguous governed chain' IN pg_get_functiondef(
         'askdata.enforce_question_run_lifecycle()'::regprocedure
       )
     )=0 OR position(
       'FOR SHARE' IN pg_get_functiondef(
         'askdata.lock_active_question_release(uuid,uuid,uuid,text)'::regprocedure
       )
     )=0 OR position(
       '''RESULT_VERIFYING''' IN pg_get_functiondef(
         'askdata.valid_question_run_transition(text,text)'::regprocedure
       )
     )=0 OR position(
       '''SEMANTIC_QUESTION''' IN pg_get_functiondef(
         'askdata.stamp_question_runtime_fact()'::regprocedure
       )
     )=0 OR position(
       'FOR SHARE' IN pg_get_functiondef(
         'askdata.stamp_question_runtime_fact()'::regprocedure
       )
     )=0 OR position(
       'tool result event requires its exact tool outcome' IN pg_get_functiondef(
         'askdata.stamp_question_runtime_fact()'::regprocedure
       )
     )=0 OR position(
       'terminal question run accepts only its unique completion event' IN pg_get_functiondef(
         'askdata.stamp_question_runtime_fact()'::regprocedure
       )
     )=0 OR askdata.question_audit_json_is_safe(
       '{"prompt":"forbidden"}'::jsonb
     ) OR askdata.question_audit_json_is_safe(
       '{"nested":{"rows":[]}}'::jsonb
     ) OR askdata.question_audit_json_is_safe(
       '{"toolArguments":{"sqlText":"forbidden"}}'::jsonb
     ) OR NOT askdata.question_audit_json_is_safe(
       '{"code":"SAFE","resultHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb
     ) THEN
    RAISE EXCEPTION 'askdata runtime lifecycle or sanitized JSON guard is unsafe';
  END IF;

  IF to_regprocedure(
       'askdata.evaluation_control_can_access(uuid,uuid)'
     ) IS NULL
    OR to_regprocedure(
       'askdata.evaluation_case_can_access(uuid,uuid,uuid)'
     ) IS NULL
    OR to_regprocedure('askdata.evaluation_set_manifest_hash(uuid)') IS NULL
    OR to_regprocedure('askdata.seal_evaluation_set(uuid,uuid)') IS NULL
    OR to_regprocedure('askdata.enforce_evaluation_run_append()') IS NULL
    OR to_regprocedure('askdata.enforce_query_feedback()') IS NULL
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.evaluation_sets'::regclass
        AND tgname='askdata_evaluation_sets_lifecycle' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.evaluation_cases'::regclass
        AND tgname='askdata_evaluation_cases_lifecycle' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.evaluation_case_reviews'::regclass
        AND tgname='askdata_evaluation_case_reviews_lifecycle'
        AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.evaluation_case_reviews'::regclass
        AND tgname='askdata_evaluation_case_reviews_refresh_count'
        AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.evaluation_runs'::regclass
        AND tgname='askdata_evaluation_runs_immutable' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='askdata.query_feedback'::regclass
        AND tgname='askdata_query_feedback_lifecycle' AND NOT tgisinternal
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.releases'::regclass
        AND conname='askdata_releases_evaluation_pin_key' AND contype='u'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_case_reviews'::regclass
        AND conname='askdata_evaluation_case_reviews_slot_key'
        AND contype='u'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_case_reviews'::regclass
        AND conname='askdata_evaluation_case_reviews_reviewer_key'
        AND contype='u'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_runs'::regclass
        AND conname='askdata_evaluation_runs_path_shape_check'
        AND contype='c'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_runs'::regclass
        AND conname='askdata_evaluation_runs_security_shape_check'
        AND contype='c'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_cases'::regclass
        AND conname='askdata_evaluation_cases_answerability_shape_check'
        AND contype='c'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_cases'::regclass
        AND conname='askdata_evaluation_cases_relational_path_check'
        AND contype='c'
    )
    OR NOT EXISTS(
      SELECT 1 FROM pg_constraint
      WHERE conrelid='askdata.evaluation_runs'::regclass
        AND conname='askdata_evaluation_runs_equivalence_evidence_check'
        AND contype='c'
    )
    OR EXISTS(
      SELECT 1 FROM information_schema.columns
      WHERE table_schema='askdata' AND table_name='evaluation_runs'
        AND column_name='sensitive_leak_detected'
        AND column_default IS NOT NULL
    ) THEN
    RAISE EXCEPTION 'askdata evaluation and feedback boundary is incomplete';
  END IF;

  IF position(
       'every sealed case requires two current independent approvals' IN
       pg_get_functiondef(
         'askdata.enforce_evaluation_set_lifecycle()'::regprocedure
       )
     )=0 OR position(
       'evaluation_control_can_access' IN pg_get_functiondef(
         'askdata.seal_evaluation_set(uuid,uuid)'::regprocedure
       )
     )=0 OR position(
       'expected_path_hash' IN pg_get_functiondef(
         'askdata.enforce_evaluation_run_append()'::regprocedure
       )
     )=0 OR position(
       'retired evaluation set cannot accept new runs' IN pg_get_functiondef(
         'askdata.enforce_evaluation_run_append()'::regprocedure
       )
     )=0 OR position(
       'FOR SHARE' IN pg_get_functiondef(
         'askdata.enforce_evaluation_run_append()'::regprocedure
       )
     )=0 OR position(
       'query feedback requires a terminal question run' IN
       pg_get_functiondef('askdata.enforce_query_feedback()'::regprocedure)
     )=0 THEN
    RAISE EXCEPTION 'askdata evaluation lifecycle guards are unsafe';
  END IF;

  IF EXISTS(
    SELECT 1
    FROM unnest(ARRAY[
      'askdata.evaluation_control_can_access(uuid,uuid)'::regprocedure,
      'askdata.evaluation_case_can_access(uuid,uuid,uuid)'::regprocedure,
      'askdata.evaluation_set_manifest_hash(uuid)'::regprocedure,
      'askdata.enforce_evaluation_set_lifecycle()'::regprocedure,
      'askdata.enforce_evaluation_case_lifecycle()'::regprocedure,
      'askdata.enforce_evaluation_case_review()'::regprocedure,
      'askdata.refresh_evaluation_case_review_count()'::regprocedure,
      'askdata.enforce_evaluation_run_append()'::regprocedure,
      'askdata.enforce_query_feedback()'::regprocedure,
      'askdata.seal_evaluation_set(uuid,uuid)'::regprocedure
    ]) AS required_function(oid)
    JOIN pg_proc AS procedure ON procedure.oid=required_function.oid
    WHERE NOT procedure.prosecdef
      OR NOT EXISTS(
        SELECT 1 FROM unnest(procedure.proconfig) AS setting
        WHERE setting LIKE 'search_path=%pg_catalog%askdata%'
      )
  ) THEN
    RAISE EXCEPTION 'askdata evaluation functions must be SECURITY DEFINER with a pinned search path';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_proc AS procedure
    WHERE procedure.oid=
      'askdata.lock_active_question_release(uuid,uuid,uuid,text)'::regprocedure
      AND procedure.prosecdef
      AND procedure.provolatile='v'
      AND EXISTS(
        SELECT 1 FROM unnest(procedure.proconfig) AS setting
        WHERE setting LIKE 'search_path=%pg_catalog%askdata%'
      )
  ) THEN
    RAISE EXCEPTION 'ACTIVE release lock must be volatile, SECURITY DEFINER and search-path pinned';
  END IF;

  IF position(
       'count(*)=4' IN
       pg_get_functiondef(
         'askdata.complete_release_projection(uuid,uuid,text,uuid,text,text,integer,jsonb)'::regprocedure
       )
     )=0 OR NOT EXISTS(
       SELECT 1 FROM pg_indexes
       WHERE schemaname='askdata' AND indexname='askdata_releases_one_active_idx'
         AND indexdef LIKE '%WHERE (status = ''ACTIVE''%'
  ) THEN
    RAISE EXCEPTION 'release four-projection or single-active invariant is missing';
  END IF;

  IF to_regprocedure(
       'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)'
     ) IS NULL THEN
    RAISE EXCEPTION 'sensitive member exact lookup function is missing';
  END IF;

  SELECT pg_get_functiondef(procedure.oid),
         pg_get_function_result(procedure.oid)
  INTO member_lookup_definition,member_lookup_result
  FROM pg_proc AS procedure
  WHERE procedure.oid=
    'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)'::regprocedure;

  IF member_lookup_result<>
       'TABLE(member_version_id uuid, dimension_version_id uuid, member_content_hash text, dimension_content_hash text)'
    OR position('askdata.current_tenant_id()' IN member_lookup_definition)=0
    OR position('askdata.current_actor_id()' IN member_lookup_definition)=0
    OR position('askdata.current_domain_id()' IN member_lookup_definition)=0
    OR position(
         'release.status IN (''READY'',''ACTIVE'',''SUPERSEDED'')'
         IN member_lookup_definition
       )=0
    OR position('projection.target=''POSTGRES_REGISTRY''' IN member_lookup_definition)=0
    OR position('dimension.member_index_policy=''EXACT_ONLY''' IN member_lookup_definition)=0
    OR position('dimension_release_object.object_type=''DIMENSION''' IN member_lookup_definition)=0
    OR position('member_release_object.object_type=''MEMBER''' IN member_lookup_definition)=0
    OR position('aliasVersionIds' IN member_lookup_definition)=0
    OR position('alias.id::text' IN member_lookup_definition)=0
    OR position('platform.object_permissions' IN member_lookup_definition)=0
    OR position('LOOKUP_CONFIDENTIAL_MEMBER' IN member_lookup_definition)=0
    OR position('LOOKUP_RESTRICTED_MEMBER' IN member_lookup_definition)=0
    OR position('count(*) FROM eligible_candidates)=1' IN member_lookup_definition)=0
    OR position('member.valid_from<=' IN member_lookup_definition)=0
    OR position('member.valid_to IS NULL' IN member_lookup_definition)=0 THEN
    RAISE EXCEPTION 'sensitive member lookup is missing a context, release, alias, validity, ambiguity or authorization pin';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.dimension_member_aliases'::regclass
      AND attribute.attname='alias_key_hash'
      AND attribute.attnotnull
      AND NOT attribute.attisdropped
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='askdata.dimension_member_aliases'::regclass
      AND conname='askdata_dimension_member_aliases_key_hash_check'
      AND contype='c'
      AND position('^[0-9a-f]{64}$' IN pg_get_constraintdef(oid))>0
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_trigger
    WHERE tgrelid='askdata.dimension_member_aliases'::regclass
      AND tgname='askdata_dimension_member_aliases_stamp_key_hash'
      AND NOT tgisinternal
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_indexes
    WHERE schemaname='askdata'
      AND indexname='askdata_dimension_member_aliases_hash_lookup_idx'
      AND indexdef LIKE '%alias_key_hash%'
  ) OR position(
       'public.digest' IN pg_get_functiondef(
         'askdata.stamp_dimension_member_alias_key_hash()'::regprocedure
       )
     )=0 OR position(
       'normalized_alias' IN pg_get_functiondef(
         'askdata.stamp_dimension_member_alias_key_hash()'::regprocedure
       )
     )=0 OR position(
       'decode(''00'',''hex'')' IN pg_get_functiondef(
         'askdata.stamp_dimension_member_alias_key_hash()'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dimension member alias hash is not database-stamped or constrained';
  END IF;

  IF NOT EXISTS(
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='askdata.dimension_members'::regclass
      AND tgname='askdata_dimension_members_enforce_sensitivity_floor'
      AND NOT tgisinternal
  ) OR NOT EXISTS(
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='askdata.dimensions'::regclass
      AND tgname='askdata_dimensions_enforce_member_sensitivity_floor'
      AND NOT tgisinternal
  ) OR position(
       'cannot be weaker than its dimension' IN pg_get_functiondef(
         'askdata.enforce_dimension_member_sensitivity_floor()'::regprocedure
       )
     )=0 OR position(
       'cannot exceed an existing member sensitivity' IN pg_get_functiondef(
         'askdata.enforce_dimension_sensitivity_floor()'::regprocedure
       )
     )=0 OR position(
       'FOR SHARE' IN pg_get_functiondef(
         'askdata.enforce_dimension_member_sensitivity_floor()'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dimension/member sensitivity floor is incomplete';
  END IF;

  IF to_regprocedure(
       'askdata.member_release_contract_is_safe(jsonb,uuid)'
     ) IS NULL
    OR to_regprocedure(
       'askdata.validate_member_release_contract()'
     ) IS NULL
    OR NOT EXISTS(
      SELECT 1
      FROM pg_trigger
      WHERE tgrelid='askdata.release_objects'::regclass
        AND tgname='askdata_release_objects_validate_member_contract'
        AND NOT tgisinternal
    )
    OR position(
       '''schemaVersion'',''type'',''dimensionVersionId'',''aliasVersionIds'''
       IN
       pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       'askdata-member-release-v1' IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       'alias_hash_count>64' IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
       IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       'count(DISTINCT alias_version.value)' IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       'lag(alias_version.value)' IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )=0
    OR position(
       'member_key_hash' IN pg_get_functiondef(
         'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
       )
     )>0
    OR NOT EXISTS(
      SELECT 1
      FROM pg_proc AS procedure
      WHERE procedure.oid=
        'askdata.member_release_contract_is_safe(jsonb,uuid)'::regprocedure
        AND procedure.provolatile='i'
        AND procedure.proisstrict
        AND NOT procedure.prosecdef
        AND EXISTS(
          SELECT 1 FROM unnest(procedure.proconfig) AS setting
          WHERE setting='search_path=pg_catalog'
        )
    )
    OR NOT EXISTS(
      SELECT 1
      FROM pg_proc AS procedure
      WHERE procedure.oid=
        'askdata.validate_member_release_contract()'::regprocedure
        AND procedure.prosecdef
        AND position(
          'dimension_member_aliases' IN pg_get_functiondef(procedure.oid)
        )>0
        AND position(
          'alias.status=''CERTIFIED''' IN pg_get_functiondef(procedure.oid)
        )>0
        AND position(
          'NEW.sensitivity IS DISTINCT FROM source_member_sensitivity' IN
          pg_get_functiondef(procedure.oid)
        )>0
        AND EXISTS(
          SELECT 1 FROM unnest(procedure.proconfig) AS setting
          WHERE setting LIKE 'search_path=%pg_catalog%askdata%'
        )
    ) THEN
    RAISE EXCEPTION 'MEMBER release contracts must be dimension-bound, label-free, bounded sorted alias-version arrays';
  END IF;

  IF to_regprocedure(
       'askdata.validate_release_domain_object_identity()'
     ) IS NULL
    OR NOT EXISTS(
      SELECT 1
      FROM pg_trigger
      WHERE tgrelid='askdata.release_objects'::regclass
        AND tgname='askdata_release_objects_00_validate_domain_identity'
        AND NOT tgisinternal
    )
    OR position(
       'NEW.object_id<>NEW.domain_id' IN pg_get_functiondef(
         'askdata.validate_release_domain_object_identity()'::regprocedure
       )
     )=0
    OR position(
       'NEW.object_version_id<>NEW.domain_id' IN pg_get_functiondef(
         'askdata.validate_release_domain_object_identity()'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'DOMAIN release object identity guard is missing';
  END IF;

  IF EXISTS(
    SELECT 1
    FROM unnest(ARRAY[
      'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)'::regprocedure,
      'askdata.enforce_dimension_member_sensitivity_floor()'::regprocedure,
      'askdata.enforce_dimension_sensitivity_floor()'::regprocedure,
      'askdata.validate_search_document_subject()'::regprocedure,
      'askdata.validate_member_dependency()'::regprocedure,
      'askdata.validate_release_object()'::regprocedure
    ]) AS required_function(oid)
    JOIN pg_proc AS procedure ON procedure.oid=required_function.oid
    WHERE NOT procedure.prosecdef
      OR NOT EXISTS(
        SELECT 1 FROM unnest(procedure.proconfig) AS setting
        WHERE setting LIKE 'search_path=%pg_catalog%askdata%'
      )
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_proc AS procedure
    WHERE procedure.oid=
      'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)'::regprocedure
      AND procedure.provolatile='s'
      AND procedure.proisstrict
      AND procedure.proretset
  ) THEN
    RAISE EXCEPTION 'sensitive member functions must be SECURITY DEFINER with fixed search paths and a stable set-returning lookup';
  END IF;

  IF NOT EXISTS(
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='platform.ai_tenant_policies'::regclass
      AND conname='ai_tenant_policies_purposes_check'
      AND position('SEMANTIC_QUESTION' IN pg_get_constraintdef(oid))>0
  ) OR NOT EXISTS(
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='platform.ai_requests'::regclass
      AND conname='ai_requests_purpose_check'
      AND position('SEMANTIC_QUESTION' IN pg_get_constraintdef(oid))>0
  ) OR EXISTS(
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema='platform' AND table_name='ai_tenant_policies'
      AND column_name='allowed_purposes'
      AND position('SEMANTIC_QUESTION' IN COALESCE(column_default,''))>0
  ) THEN
    RAISE EXCEPTION 'SEMANTIC_QUESTION must be policy-supported, audited and default-denied';
  END IF;
END
$$;

SELECT (
  NOT has_schema_privilege('public','askdata','USAGE')
  AND has_schema_privilege(:'app_user','askdata','USAGE')
  AND has_schema_privilege(:'worker_user','askdata','USAGE')
  AND NOT has_schema_privilege(:'connection_test_user','askdata','USAGE')
  AND has_table_privilege(:'app_user','askdata.metric_versions','INSERT')
  AND has_table_privilege(:'app_user','askdata.metric_versions','UPDATE')
  AND has_table_privilege(:'app_user','askdata.releases','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.releases','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.embedding_outbox','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.release_projections','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.release_events','INSERT')
  AND has_table_privilege(:'app_user','askdata.question_runs','INSERT')
  AND has_table_privilege(:'app_user','askdata.question_runs','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.question_runs','DELETE')
  AND has_table_privilege(:'app_user','askdata.question_run_events','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.question_run_events','UPDATE')
  AND has_table_privilege(:'app_user','askdata.question_artifacts','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.question_artifacts','UPDATE')
  AND has_table_privilege(:'app_user','askdata.tool_calls','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.tool_calls','UPDATE')
  AND has_table_privilege(:'app_user','askdata.evaluation_sets','INSERT')
  AND has_table_privilege(:'app_user','askdata.evaluation_sets','UPDATE')
  AND has_table_privilege(:'app_user','askdata.evaluation_sets','DELETE')
  AND has_table_privilege(:'app_user','askdata.evaluation_cases','INSERT')
  AND has_table_privilege(:'app_user','askdata.evaluation_cases','UPDATE')
  AND has_table_privilege(:'app_user','askdata.evaluation_cases','DELETE')
  AND has_table_privilege(
    :'app_user','askdata.evaluation_case_reviews','INSERT'
  )
  AND has_table_privilege(
    :'app_user','askdata.evaluation_case_reviews','UPDATE'
  )
  AND has_table_privilege(
    :'app_user','askdata.evaluation_case_reviews','DELETE'
  )
  AND has_table_privilege(:'app_user','askdata.query_feedback','INSERT')
  AND has_table_privilege(:'app_user','askdata.query_feedback','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.query_feedback','DELETE')
  AND NOT has_table_privilege(:'app_user','askdata.evaluation_runs','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.evaluation_runs','UPDATE')
  AND NOT has_table_privilege(:'app_user','askdata.evaluation_runs','DELETE')
  AND has_table_privilege(:'worker_user','askdata.embedding_outbox','UPDATE')
  AND has_table_privilege(:'worker_user','askdata.graph_plan_cache','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.dimension_profile_jobs','INSERT')
  AND NOT has_table_privilege(:'app_user','askdata.dimension_profile_jobs','UPDATE')
  AND has_table_privilege(:'worker_user','askdata.dimension_profile_jobs','INSERT')
  AND has_table_privilege(:'worker_user','askdata.dimension_profile_jobs','UPDATE')
  AND has_table_privilege(:'worker_user','askdata.dimension_profiles','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.dimension_profiles','UPDATE')
  AND has_table_privilege(:'worker_user','askdata.dimension_profile_members','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.dimension_profile_members','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.entities','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.release_objects','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.release_projections','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.question_runs','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.question_runs','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.question_run_events','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.question_artifacts','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.tool_calls','INSERT')
  AND has_table_privilege(:'worker_user','askdata.evaluation_runs','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_runs','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_runs','DELETE')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_sets','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_sets','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_sets','DELETE')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_cases','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_cases','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.evaluation_cases','DELETE')
  AND NOT has_table_privilege(
    :'worker_user','askdata.evaluation_case_reviews','INSERT'
  )
  AND NOT has_table_privilege(
    :'worker_user','askdata.evaluation_case_reviews','UPDATE'
  )
  AND NOT has_table_privilege(
    :'worker_user','askdata.evaluation_case_reviews','DELETE'
  )
  AND NOT has_table_privilege(:'worker_user','askdata.query_feedback','INSERT')
  AND NOT has_table_privilege(:'worker_user','askdata.query_feedback','UPDATE')
  AND NOT has_table_privilege(:'worker_user','askdata.query_feedback','DELETE')
  AND NOT has_table_privilege(:'connection_test_user','askdata.domains','SELECT')
  AND has_function_privilege(
    :'app_user','askdata.start_release_projection(uuid,uuid,jsonb)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user','askdata.start_release_projection(uuid,uuid,jsonb)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','askdata.claim_release_projection(uuid,text,integer)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'app_user','askdata.claim_release_projection(uuid,text,integer)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user','askdata.release_manifest_hash(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','askdata.question_audit_json_is_safe(jsonb)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'app_user',
    'askdata.lock_active_question_release(uuid,uuid,uuid,text)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user',
    'askdata.lock_active_question_release(uuid,uuid,uuid,text)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user',
    'askdata.lock_active_question_release(uuid,uuid,uuid,text)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','askdata.question_runtime_can_access(uuid,uuid,uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','askdata.evaluation_control_can_access(uuid,uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','askdata.evaluation_control_can_access(uuid,uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','askdata.evaluation_case_can_access(uuid,uuid,uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','askdata.evaluation_case_can_access(uuid,uuid,uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','askdata.seal_evaluation_set(uuid,uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user','askdata.seal_evaluation_set(uuid,uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'app_user','askdata.evaluation_set_manifest_hash(uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user','askdata.evaluation_set_manifest_hash(uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user',
    'askdata.question_runtime_can_access(uuid,uuid,uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user',
    'askdata.evaluation_control_can_access(uuid,uuid)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user','askdata.seal_evaluation_set(uuid,uuid)','EXECUTE'
  )
  AND NOT EXISTS(
    SELECT 1
    FROM unnest(ARRAY[
      :'app_user'::text, :'worker_user'::text,
      :'connection_test_user'::text
    ]) AS runtime_role(role_name)
    CROSS JOIN unnest(ARRAY[
      'askdata.enforce_evaluation_set_lifecycle()'::regprocedure,
      'askdata.enforce_evaluation_case_lifecycle()'::regprocedure,
      'askdata.enforce_evaluation_case_review()'::regprocedure,
      'askdata.refresh_evaluation_case_review_count()'::regprocedure,
      'askdata.enforce_evaluation_run_append()'::regprocedure,
      'askdata.enforce_query_feedback()'::regprocedure
    ]) AS lifecycle_helper(function_oid)
    WHERE has_function_privilege(
      runtime_role.role_name,lifecycle_helper.function_oid,'EXECUTE'
    )
  )
) AS askdata_runtime_privileges_secure
\gset
\if :askdata_runtime_privileges_secure
\else
  \echo 'askdata runtime privileges are unsafe'
  \quit 1
\endif

SELECT (
  NOT has_table_privilege(
    :'app_user','askdata.dimension_members','SELECT'
  )
  AND NOT has_table_privilege(
    :'app_user','askdata.dimension_member_aliases','SELECT'
  )
  AND NOT has_table_privilege(
    :'app_user','askdata.dimension_profile_members','SELECT'
  )
  AND ARRAY(
    SELECT attribute.attname::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid=
      'askdata.dimension_profile_members'::regclass
      AND attribute.attnum>0 AND NOT attribute.attisdropped
      AND has_column_privilege(
        :'app_user','askdata.dimension_profile_members',
        attribute.attname,'SELECT'
      )
    ORDER BY attribute.attname
  )='{}'::text[]
  AND ARRAY(
    SELECT attribute.attname::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.dimension_members'::regclass
      AND attribute.attnum>0 AND NOT attribute.attisdropped
      AND has_column_privilege(
        :'app_user','askdata.dimension_members',attribute.attname,'SELECT'
      )
    ORDER BY attribute.attname
  )=ARRAY[
    'content_hash','created_at','created_by','dimension_version_id','domain_id',
    'id','member_id','parent_member_version_id','sensitivity','status',
    'tenant_id','updated_at','valid_from','valid_to','version_no'
  ]::text[]
  AND ARRAY(
    SELECT attribute.attname::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.dimension_member_aliases'::regclass
      AND attribute.attnum>0 AND NOT attribute.attisdropped
      AND has_column_privilege(
        :'app_user','askdata.dimension_member_aliases',
        attribute.attname,'SELECT'
      )
    ORDER BY attribute.attname
  )=ARRAY[
    'content_hash','created_at','created_by','dimension_version_id','domain_id',
    'id','member_version_id','priority','source','status','tenant_id',
    'updated_at'
  ]::text[]
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_members','member_key','SELECT'
  )
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_members','member_key_hash','SELECT'
  )
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_members','canonical_label','SELECT'
  )
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_member_aliases','alias','SELECT'
  )
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_member_aliases','normalized_alias','SELECT'
  )
  AND NOT has_column_privilege(
    :'app_user','askdata.dimension_member_aliases','alias_key_hash','SELECT'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_members','INSERT'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_members','UPDATE'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_members','DELETE'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_member_aliases','INSERT'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_member_aliases','UPDATE'
  )
  AND has_table_privilege(
    :'app_user','askdata.dimension_member_aliases','DELETE'
  )
  AND NOT has_table_privilege(
    :'worker_user','askdata.dimension_profile_members','SELECT'
  )
  AND ARRAY(
    SELECT attribute.attname::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='askdata.dimension_profile_members'::regclass
      AND attribute.attnum>0 AND NOT attribute.attisdropped
      AND has_column_privilege(
        :'worker_user','askdata.dimension_profile_members',
        attribute.attname,'SELECT'
      )
    ORDER BY attribute.attname
  )=ARRAY['member_key_hash','profile_id']::text[]
  AND NOT has_column_privilege(
    :'worker_user','askdata.dimension_profile_members',
    'canonical_label','SELECT'
  )
  AND NOT has_column_privilege(
    :'worker_user','askdata.dimension_profile_members',
    'normalized_value','SELECT'
  )
  AND NOT has_column_privilege(
    :'worker_user','askdata.dimension_profile_members',
    'observed_aliases','SELECT'
  )
  AND ARRAY(
    SELECT attribute.attname::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid=
      'askdata.dimension_profile_members'::regclass
      AND attribute.attnum>0 AND NOT attribute.attisdropped
      AND has_column_privilege(
        :'connection_test_user','askdata.dimension_profile_members',
        attribute.attname,'SELECT'
      )
    ORDER BY attribute.attname
  )='{}'::text[]
  AND NOT EXISTS(
    SELECT 1
    FROM unnest(ARRAY[
      :'app_user'::text,:'worker_user'::text,
      :'connection_test_user'::text
    ]) AS runtime_role(role_name)
    CROSS JOIN unnest(ARRAY[
      'askdata.dimension_members'::regclass,
      'askdata.dimension_member_aliases'::regclass
    ]) AS raw_relation(relation_oid)
    JOIN pg_attribute AS attribute
      ON attribute.attrelid=raw_relation.relation_oid
     AND attribute.attnum>0 AND NOT attribute.attisdropped
    WHERE runtime_role.role_name<>:'app_user'
      AND has_column_privilege(
        runtime_role.role_name,raw_relation.relation_oid,
        attribute.attname,'SELECT'
      )
  )
  AND has_function_privilege(
    :'app_user',
    'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'worker_user',
    'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)','EXECUTE'
  )
  AND NOT has_function_privilege(
    :'connection_test_user',
    'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)','EXECUTE'
  )
  AND NOT EXISTS(
    SELECT 1
    FROM pg_proc AS procedure
    CROSS JOIN LATERAL aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE procedure.oid=
      'askdata.lookup_exact_dimension_member(uuid,text,uuid,text)'::regprocedure
      AND privilege.grantee=0
      AND privilege.privilege_type='EXECUTE'
  )
) AS askdata_sensitive_member_privileges_secure
\gset
\if :askdata_sensitive_member_privileges_secure
\else
  \echo 'askdata sensitive member privileges are unsafe'
  \quit 1
\endif

-- Exercise the SEC-003 exact-only boundary with real report_app calls. The
-- semantic-model FK is intentionally bypassed only while bootstrapping this
-- synthetic dimension: this fixture tests member/release/permission behavior,
-- not the already-covered DWS import path. Every tested member, alias, release
-- object and permission is otherwise created through its live constraints and
-- triggers, and the outer transaction leaves no control-plane data behind.
BEGIN;
INSERT INTO platform.tenants(code,name)
VALUES('verify_askdata_sensitive','askdata sensitive member verification tenant')
RETURNING id AS tenant_id
\gset askdata_sensitive_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_sensitive_tenant_id','ASKSENSITIVE001',
  'askdata.sensitive@example.invalid','askdata sensitive verification actor',
  'verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS actor_id
\gset askdata_sensitive_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_sensitive_tenant_id','verify_sensitive_domain',
  'verify sensitive domain',true,:'askdata_sensitive_actor_id'
)
RETURNING id AS domain_id
\gset askdata_sensitive_
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,status,member_role,assigned_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_actor_id','ACTIVE','MEMBER',
  :'askdata_sensitive_actor_id'
);
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_sensitive_domain_id',:'askdata_sensitive_tenant_id',
  'verify_sensitive_domain','verify sensitive domain',
  :'askdata_sensitive_actor_id'
);

SET LOCAL session_replication_role=replica;
INSERT INTO askdata.dimensions(
  tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
  logical_field_id,code,name,description,dimension_kind,sensitivity,
  member_index_policy,high_cardinality,status,content_hash,owner_id
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,gen_random_uuid(),'fixture_sensitive_key',
  'fixture_sensitive_dimension','fixture sensitive dimension',
  'synthetic exact-only member verification','CATEGORICAL','CONFIDENTIAL',
  'EXACT_ONLY',false,'CERTIFIED',repeat('d',64),
  :'askdata_sensitive_actor_id'
)
RETURNING id AS dimension_id,dimension_id AS stable_dimension_id
\gset askdata_sensitive_
SET LOCAL session_replication_role=origin;
SELECT set_config(
  'verify.askdata_sensitive_tenant_id',:'askdata_sensitive_tenant_id',true
);
SELECT set_config(
  'verify.askdata_sensitive_domain_id',:'askdata_sensitive_domain_id',true
);
SELECT set_config(
  'verify.askdata_sensitive_actor_id',:'askdata_sensitive_actor_id',true
);
SELECT set_config(
  'verify.askdata_sensitive_dimension_id',:'askdata_sensitive_dimension_id',true
);

SELECT
  pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(:'askdata_sensitive_dimension_id','UTF8')
        ||pg_catalog.decode('00','hex')
        ||pg_catalog.convert_to('fixture-confidential-canonical','UTF8'),
      'sha256'
    ),'hex'
  ) AS confidential_hash,
  pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(:'askdata_sensitive_dimension_id','UTF8')
        ||pg_catalog.decode('00','hex')
        ||pg_catalog.convert_to('fixture-confidential-alias','UTF8'),
      'sha256'
    ),'hex'
  ) AS confidential_alias_hash,
  pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(:'askdata_sensitive_dimension_id','UTF8')
        ||pg_catalog.decode('00','hex')
        ||pg_catalog.convert_to('fixture-restricted-canonical','UTF8'),
      'sha256'
    ),'hex'
  ) AS restricted_hash,
  pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(:'askdata_sensitive_dimension_id','UTF8')
        ||pg_catalog.decode('00','hex')
        ||pg_catalog.convert_to('fixture-ambiguous-alias','UTF8'),
      'sha256'
    ),'hex'
  ) AS ambiguous_hash,
  pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(:'askdata_sensitive_dimension_id','UTF8')
        ||pg_catalog.decode('00','hex')
        ||pg_catalog.convert_to('fixture-expired-canonical','UTF8'),
      'sha256'
    ),'hex'
  ) AS expired_hash,
  repeat('f',64) AS missing_hash
\gset askdata_sensitive_

DO $$
BEGIN
  INSERT INTO askdata.dimension_members(
    tenant_id,domain_id,member_id,version_no,dimension_version_id,
    member_key,member_key_hash,canonical_label,sensitivity,valid_from,
    status,content_hash,created_by
  ) VALUES(
    current_setting('verify.askdata_sensitive_tenant_id')::uuid,
    current_setting('verify.askdata_sensitive_domain_id')::uuid,
    gen_random_uuid(),1,
    current_setting('verify.askdata_sensitive_dimension_id')::uuid,
    'fixture-invalid-floor',repeat('0',64),'fixture invalid floor',
    'PUBLIC',now()-interval '1 day','CERTIFIED',repeat('0',64),
    current_setting('verify.askdata_sensitive_actor_id')::uuid
  );
  RAISE EXCEPTION 'a member weaker than its dimension was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

INSERT INTO askdata.dimension_members(
  tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,sensitivity,valid_from,
  status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,:'askdata_sensitive_dimension_id',
  'fixture-confidential-canonical',:'askdata_sensitive_confidential_hash',
  'fixture confidential canonical','CONFIDENTIAL',now()-interval '1 day',
  'CERTIFIED',repeat('1',64),:'askdata_sensitive_actor_id'
)
RETURNING id AS confidential_member_id,member_id AS confidential_stable_member_id
\gset askdata_sensitive_
INSERT INTO askdata.dimension_member_aliases(
  tenant_id,domain_id,dimension_version_id,member_version_id,alias,
  normalized_alias,source,priority,status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_member_id',
  'fixture confidential alias','fixture-confidential-alias','MANUAL',10,
  'CERTIFIED',repeat('a',64),:'askdata_sensitive_actor_id'
)
RETURNING id AS confidential_alias_id
\gset askdata_sensitive_

INSERT INTO askdata.dimension_members(
  tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,sensitivity,valid_from,
  status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,:'askdata_sensitive_dimension_id',
  'fixture-restricted-canonical',:'askdata_sensitive_restricted_hash',
  'fixture restricted canonical','RESTRICTED',now()-interval '1 day',
  'CERTIFIED',repeat('2',64),:'askdata_sensitive_actor_id'
)
RETURNING id AS restricted_member_id,member_id AS restricted_stable_member_id
\gset askdata_sensitive_

INSERT INTO askdata.dimension_members(
  tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,sensitivity,valid_from,
  status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,:'askdata_sensitive_dimension_id',
  'fixture-ambiguous-one',repeat('3',64),'fixture ambiguous one',
  'CONFIDENTIAL',now()-interval '1 day','CERTIFIED',repeat('3',64),
  :'askdata_sensitive_actor_id'
)
RETURNING id AS ambiguous_one_member_id,member_id AS ambiguous_one_stable_member_id
\gset askdata_sensitive_
INSERT INTO askdata.dimension_member_aliases(
  tenant_id,domain_id,dimension_version_id,member_version_id,alias,
  normalized_alias,source,priority,status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_dimension_id',:'askdata_sensitive_ambiguous_one_member_id',
  'fixture ambiguous alias','fixture-ambiguous-alias','MANUAL',10,
  'CERTIFIED',repeat('b',64),:'askdata_sensitive_actor_id'
)
RETURNING id AS ambiguous_one_alias_id
\gset askdata_sensitive_

INSERT INTO askdata.dimension_members(
  tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,sensitivity,valid_from,
  status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,:'askdata_sensitive_dimension_id',
  'fixture-ambiguous-two',repeat('4',64),'fixture ambiguous two',
  'CONFIDENTIAL',now()-interval '1 day','CERTIFIED',repeat('4',64),
  :'askdata_sensitive_actor_id'
)
RETURNING id AS ambiguous_two_member_id,member_id AS ambiguous_two_stable_member_id
\gset askdata_sensitive_
INSERT INTO askdata.dimension_member_aliases(
  tenant_id,domain_id,dimension_version_id,member_version_id,alias,
  normalized_alias,source,priority,status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_dimension_id',:'askdata_sensitive_ambiguous_two_member_id',
  'fixture ambiguous alias','fixture-ambiguous-alias','MANUAL',10,
  'CERTIFIED',repeat('c',64),:'askdata_sensitive_actor_id'
)
RETURNING id AS ambiguous_two_alias_id
\gset askdata_sensitive_

INSERT INTO askdata.dimension_members(
  tenant_id,domain_id,member_id,version_no,dimension_version_id,
  member_key,member_key_hash,canonical_label,sensitivity,valid_from,valid_to,
  status,content_hash,created_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  gen_random_uuid(),1,:'askdata_sensitive_dimension_id',
  'fixture-expired-canonical',:'askdata_sensitive_expired_hash',
  'fixture expired canonical','CONFIDENTIAL',now()-interval '2 days',
  now()-interval '1 day','CERTIFIED',repeat('5',64),
  :'askdata_sensitive_actor_id'
)
RETURNING id AS expired_member_id,member_id AS expired_stable_member_id
\gset askdata_sensitive_

INSERT INTO askdata.releases(
  tenant_id,domain_id,semantic_version,content_hash,status,created_by,updated_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  'verify-sensitive-v1',repeat('e',64),'DRAFT',
  :'askdata_sensitive_actor_id',:'askdata_sensitive_actor_id'
)
RETURNING id AS release_id
\gset askdata_sensitive_
INSERT INTO askdata.release_objects(
  tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
  content_hash,sensitivity,contract_json
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_release_id','DIMENSION',
  :'askdata_sensitive_stable_dimension_id',:'askdata_sensitive_dimension_id',
  repeat('d',64),'CONFIDENTIAL','{}'::jsonb
);
INSERT INTO askdata.release_objects(
  tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
  content_hash,sensitivity,contract_json
) VALUES
  (
    :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
    :'askdata_sensitive_release_id','MEMBER',
    :'askdata_sensitive_confidential_stable_member_id',
    :'askdata_sensitive_confidential_member_id',repeat('1',64),'CONFIDENTIAL',
    jsonb_build_object(
      'schemaVersion','askdata-member-release-v1','type','MEMBER',
      'dimensionVersionId',:'askdata_sensitive_dimension_id',
      'aliasVersionIds',jsonb_build_array(:'askdata_sensitive_confidential_alias_id')
    )
  ),
  (
    :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
    :'askdata_sensitive_release_id','MEMBER',
    :'askdata_sensitive_restricted_stable_member_id',
    :'askdata_sensitive_restricted_member_id',repeat('2',64),'RESTRICTED',
    jsonb_build_object(
      'schemaVersion','askdata-member-release-v1','type','MEMBER',
      'dimensionVersionId',:'askdata_sensitive_dimension_id',
      'aliasVersionIds','[]'::jsonb
    )
  ),
  (
    :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
    :'askdata_sensitive_release_id','MEMBER',
    :'askdata_sensitive_ambiguous_one_stable_member_id',
    :'askdata_sensitive_ambiguous_one_member_id',repeat('3',64),'CONFIDENTIAL',
    jsonb_build_object(
      'schemaVersion','askdata-member-release-v1','type','MEMBER',
      'dimensionVersionId',:'askdata_sensitive_dimension_id',
      'aliasVersionIds',jsonb_build_array(:'askdata_sensitive_ambiguous_one_alias_id')
    )
  ),
  (
    :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
    :'askdata_sensitive_release_id','MEMBER',
    :'askdata_sensitive_ambiguous_two_stable_member_id',
    :'askdata_sensitive_ambiguous_two_member_id',repeat('4',64),'CONFIDENTIAL',
    jsonb_build_object(
      'schemaVersion','askdata-member-release-v1','type','MEMBER',
      'dimensionVersionId',:'askdata_sensitive_dimension_id',
      'aliasVersionIds',jsonb_build_array(:'askdata_sensitive_ambiguous_two_alias_id')
    )
  ),
  (
    :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
    :'askdata_sensitive_release_id','MEMBER',
    :'askdata_sensitive_expired_stable_member_id',
    :'askdata_sensitive_expired_member_id',repeat('5',64),'CONFIDENTIAL',
    jsonb_build_object(
      'schemaVersion','askdata-member-release-v1','type','MEMBER',
      'dimensionVersionId',:'askdata_sensitive_dimension_id',
      'aliasVersionIds','[]'::jsonb
    )
  );

SELECT pg_catalog.encode(public.digest(COALESCE(pg_catalog.string_agg(
  object_type||':'||object_id::text||':'||object_version_id::text||':'||content_hash,
  E'\n' ORDER BY object_type,object_id,object_version_id
),''),'sha256'),'hex') AS release_hash,count(*)::integer AS object_count
FROM askdata.release_objects
WHERE tenant_id=:'askdata_sensitive_tenant_id'
  AND release_id=:'askdata_sensitive_release_id'
\gset askdata_sensitive_
UPDATE askdata.releases
SET content_hash=:'askdata_sensitive_release_hash',status='READY',
  object_count=:'askdata_sensitive_object_count',ready_at=now()
WHERE id=:'askdata_sensitive_release_id';
INSERT INTO askdata.release_projections(
  tenant_id,domain_id,release_id,target,status,expected_content_hash,
  applied_content_hash,resource_version,object_count,completed_at
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_release_id','POSTGRES_REGISTRY','READY',
  :'askdata_sensitive_release_hash',:'askdata_sensitive_release_hash',
  'verify-sensitive-v1',:'askdata_sensitive_object_count',now()
);

-- Keep a DRAFT manifest row so an update that changes only sensitivity proves
-- the MEMBER trigger's UPDATE OF list cannot be bypassed.
INSERT INTO askdata.releases(
  tenant_id,domain_id,semantic_version,content_hash,status,created_by,updated_by
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  'verify-sensitive-draft-v1',repeat('9',64),'DRAFT',
  :'askdata_sensitive_actor_id',:'askdata_sensitive_actor_id'
)
RETURNING id AS draft_release_id
\gset askdata_sensitive_
SELECT set_config(
  'verify.askdata_sensitive_draft_release_id',
  :'askdata_sensitive_draft_release_id',true
);
INSERT INTO askdata.release_objects(
  tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
  content_hash,sensitivity,contract_json
) VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_domain_id',
  :'askdata_sensitive_draft_release_id','MEMBER',
  :'askdata_sensitive_confidential_stable_member_id',
  :'askdata_sensitive_confidential_member_id',repeat('1',64),'CONFIDENTIAL',
  jsonb_build_object(
    'schemaVersion','askdata-member-release-v1','type','MEMBER',
    'dimensionVersionId',:'askdata_sensitive_dimension_id',
    'aliasVersionIds','[]'::jsonb
  )
);
DO $$
BEGIN
  UPDATE askdata.release_objects
  SET sensitivity='PUBLIC'
  WHERE release_id=current_setting(
      'verify.askdata_sensitive_draft_release_id'
    )::uuid
    AND object_type='MEMBER';
  RAISE EXCEPTION 'a MEMBER release object accepted a sensitivity-only mismatch';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;
SELECT count(*)=1 AS sensitivity_only_update_rejected
FROM askdata.release_objects
WHERE release_id=:'askdata_sensitive_draft_release_id'
  AND object_type='MEMBER' AND sensitivity='CONFIDENTIAL'
\gset askdata_sensitive_
\if :askdata_sensitive_sensitivity_only_update_rejected
\else
  \echo 'MEMBER release sensitivity-only update bypassed its source pin'
  \quit 1
\endif

SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_sensitive_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_sensitive_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_sensitive_domain_id',true);
SELECT set_config('app.access_mode','USER',true);

SELECT
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_hash'
  ))=0
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_missing_hash'
  ))=0 AS missing_and_unauthorized_are_indistinguishable
\gset askdata_sensitive_
\if :askdata_sensitive_missing_and_unauthorized_are_indistinguishable
\else
  \echo 'missing and unauthorized sensitive member lookups diverged'
  \quit 1
\endif

SELECT (
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',repeat('0',64),
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_hash'
  ))=0
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    gen_random_uuid(),:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_hash'
  ))=0
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_ambiguous_hash'
  ))=0
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_expired_hash'
  ))=0
) AS unpinned_ambiguous_and_expired_fail_closed
\gset askdata_sensitive_
\if :askdata_sensitive_unpinned_ambiguous_and_expired_fail_closed
\else
  \echo 'an unpinned, ambiguous or expired member lookup did not fail closed'
  \quit 1
\endif

SELECT set_config(
  'verify.askdata_sensitive_confidential_member_id',
  :'askdata_sensitive_confidential_member_id',true
);
DO $$
BEGIN
  INSERT INTO askdata.search_documents(
    tenant_id,domain_id,object_type,object_version_id,view_type,sensitivity,
    index_policy,document,input_hash,embedding_status
  ) VALUES(
    current_setting('verify.askdata_sensitive_tenant_id')::uuid,
    current_setting('verify.askdata_sensitive_domain_id')::uuid,'MEMBER',
    current_setting('verify.askdata_sensitive_confidential_member_id')::uuid,
    'DIMENSION_VALUE',
    'CONFIDENTIAL','LEXICAL','synthetic sensitive member document',
    repeat('8',64),'PENDING'
  );
  RAISE EXCEPTION 'a sensitive MEMBER search document was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

RESET ROLE;
INSERT INTO platform.object_permissions(
  tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
) VALUES(
  :'askdata_sensitive_tenant_id','USER',:'askdata_sensitive_actor_id',
  'ASKDATA_DIMENSION',:'askdata_sensitive_stable_dimension_id',
  'LOOKUP_CONFIDENTIAL_MEMBER',:'askdata_sensitive_actor_id'
);
SET LOCAL ROLE :"app_user";
SELECT set_config('app.access_mode','USER',true);
SELECT (
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_hash'
  ))=1
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_alias_hash'
  ))=1
  AND
  (SELECT count(*) FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_restricted_hash'
  ))=0
) AS confidential_alias_action_ok
\gset askdata_sensitive_
\if :askdata_sensitive_confidential_alias_action_ok
\else
  \echo 'confidential canonical/alias or restricted action enforcement failed'
  \quit 1
\endif

RESET ROLE;
INSERT INTO platform.roles(tenant_id,code,name,status)
VALUES(
  :'askdata_sensitive_tenant_id','verify_sensitive_lookup_role',
  'verify sensitive lookup role','ACTIVE'
)
RETURNING id AS restricted_role_id
\gset askdata_sensitive_
INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by)
VALUES(
  :'askdata_sensitive_tenant_id',:'askdata_sensitive_actor_id',
  :'askdata_sensitive_restricted_role_id',:'askdata_sensitive_actor_id'
);
INSERT INTO platform.object_permissions(
  tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
) VALUES(
  :'askdata_sensitive_tenant_id','ROLE',:'askdata_sensitive_restricted_role_id',
  'ASKDATA_DIMENSION',:'askdata_sensitive_stable_dimension_id',
  'LOOKUP_RESTRICTED_MEMBER',:'askdata_sensitive_actor_id'
);
SET LOCAL ROLE :"app_user";
SELECT set_config('app.access_mode','USER',true);
SELECT (
  SELECT count(*)=1
  FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_restricted_hash'
  )
) AS active_role_can_use_restricted_action
\gset askdata_sensitive_
\if :askdata_sensitive_active_role_can_use_restricted_action
\else
  \echo 'active ROLE restricted-member action was not honored'
  \quit 1
\endif

RESET ROLE;
UPDATE askdata.releases
SET status='SUPERSEDED',activated_by=:'askdata_sensitive_actor_id',
  activated_at=now()
WHERE id=:'askdata_sensitive_release_id';
SET LOCAL ROLE :"app_user";
SELECT set_config('app.access_mode','USER',true);
SELECT (
  SELECT count(*)=1
  FROM askdata.lookup_exact_dimension_member(
    :'askdata_sensitive_release_id',:'askdata_sensitive_release_hash',
    :'askdata_sensitive_dimension_id',:'askdata_sensitive_confidential_hash'
  )
) AS superseded_release_remains_replayable
\gset askdata_sensitive_
\if :askdata_sensitive_superseded_release_remains_replayable
\else
  \echo 'a pinned SUPERSEDED release is not replayable'
  \quit 1
\endif
RESET ROLE;
ROLLBACK;

SELECT NOT EXISTS(
  SELECT 1 FROM platform.tenants WHERE code='verify_askdata_sensitive'
) AS askdata_sensitive_fixture_rolled_back
\gset
\if :askdata_sensitive_fixture_rolled_back
\else
  \echo 'sensitive member verification fixture left persistent data'
  \quit 1
\endif

BEGIN;
INSERT INTO platform.tenants(code,name)
VALUES('verify_askdata_rls','askdata verification tenant')
RETURNING id AS tenant_id
\gset askdata_ctx_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_ctx_tenant_id','ASKVERIFY001','askdata.verify@example.invalid',
  'askdata verification user','verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS user_id
\gset askdata_ctx_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_ctx_tenant_id','verify_current','verify current domain',true,
  :'askdata_ctx_user_id'
)
RETURNING id AS domain_id
\gset askdata_ctx_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_ctx_tenant_id','verify_other','verify other domain',false,
  :'askdata_ctx_user_id'
)
RETURNING id
\gset askdata_other_
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,status,member_role,assigned_by
) VALUES(
  :'askdata_ctx_tenant_id',:'askdata_ctx_domain_id',:'askdata_ctx_user_id',
  'ACTIVE','MEMBER',:'askdata_ctx_user_id'
);
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_ctx_domain_id',:'askdata_ctx_tenant_id',
  'verify_current_domain','verify current domain',:'askdata_ctx_user_id'
)
ON CONFLICT(id) DO NOTHING;
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_other_id',:'askdata_ctx_tenant_id',
  'verify_other_domain','verify other domain',:'askdata_ctx_user_id'
)
ON CONFLICT(id) DO NOTHING;
SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_ctx_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_ctx_user_id',true);
SELECT set_config('app.domain_id',:'askdata_ctx_domain_id',true);
SELECT set_config('app.access_mode','USER',true);
SELECT count(*)=1 AS askdata_domain_rls_isolated
FROM askdata.domains
WHERE id IN (:'askdata_ctx_domain_id',:'askdata_other_id')
\gset
\if :askdata_domain_rls_isolated
\else
  \echo 'askdata domain RLS isolation failed'
  \quit 1
\endif
ROLLBACK;

-- Exercise the AskData question runtime as the API role. The transaction is
-- rolled back so verification never leaves synthetic control-plane facts.
BEGIN;
INSERT INTO platform.tenants(code,name)
VALUES('verify_askdata_runtime','askdata runtime verification tenant')
RETURNING id AS tenant_id
\gset askdata_runtime_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_runtime_tenant_id','ASKRUNTIME001',
  'askdata.runtime@example.invalid','askdata runtime actor',
  'verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS actor_id
\gset askdata_runtime_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_runtime_tenant_id','ASKRUNTIME002',
  'askdata.runtime.observer@example.invalid','askdata runtime observer',
  'verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS observer_id
\gset askdata_runtime_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_runtime_tenant_id','ASKRUNTIME003',
  'askdata.runtime.reviewer1@example.invalid','askdata evaluation reviewer one',
  'verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS reviewer_one_id
\gset askdata_runtime_
INSERT INTO platform.users(
  tenant_id,employee_no,email,display_name,password_hash,status
) VALUES(
  :'askdata_runtime_tenant_id','ASKRUNTIME004',
  'askdata.runtime.reviewer2@example.invalid','askdata evaluation reviewer two',
  'verification-only-not-a-login-secret','ACTIVE'
)
RETURNING id AS reviewer_two_id
\gset askdata_runtime_
INSERT INTO platform.business_domains(
  tenant_id,code,name,is_default,created_by
) VALUES(
  :'askdata_runtime_tenant_id','verify_runtime_domain',
  'verify runtime domain',true,:'askdata_runtime_actor_id'
)
RETURNING id AS domain_id
\gset askdata_runtime_
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,status,member_role,assigned_by
) VALUES
  (
    :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
    :'askdata_runtime_actor_id','ACTIVE','DOMAIN_ADMIN',
    :'askdata_runtime_actor_id'
  ),
  (
    :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
    :'askdata_runtime_observer_id','ACTIVE','MEMBER',
    :'askdata_runtime_actor_id'
  ),
  (
    :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
    :'askdata_runtime_reviewer_one_id','ACTIVE','DOMAIN_ADMIN',
    :'askdata_runtime_actor_id'
  ),
  (
    :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
    :'askdata_runtime_reviewer_two_id','ACTIVE','DOMAIN_ADMIN',
    :'askdata_runtime_actor_id'
  );
INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
VALUES(
  :'askdata_runtime_domain_id',:'askdata_runtime_tenant_id',
  'verify_runtime_domain','verify runtime domain',
  :'askdata_runtime_actor_id'
);
INSERT INTO askdata.releases(
  tenant_id,domain_id,semantic_version,content_hash,status,object_count,
  created_by,updated_by,activated_by,ready_at,activated_at
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  'verify-runtime-v1',repeat('a',64),'ACTIVE',0,
  :'askdata_runtime_actor_id',:'askdata_runtime_actor_id',
  :'askdata_runtime_actor_id',now(),now()
)
RETURNING id AS release_id
\gset askdata_runtime_
INSERT INTO askdata.releases(
  tenant_id,domain_id,semantic_version,content_hash,status,object_count,
  created_by,updated_by,ready_at
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  'verify-runtime-v2',repeat('0',64),'READY',0,
  :'askdata_runtime_actor_id',:'askdata_runtime_actor_id',now()
)
RETURNING id AS other_release_id
\gset askdata_runtime_

SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_runtime_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_runtime_domain_id',true);
SELECT set_config('app.access_mode','USER',true);

INSERT INTO askdata.question_runs(
  tenant_id,domain_id,actor_id,idempotency_key_hash,question_hash,
  policy_scope_hash,release_id,release_content_hash
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  :'askdata_runtime_actor_id',repeat('b',64),repeat('c',64),
  repeat('d',64),:'askdata_runtime_release_id',repeat('a',64)
)
RETURNING id AS run_id
\gset askdata_runtime_

DO $$
BEGIN
  INSERT INTO askdata.query_feedback(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,rating,issue_type,feedback_hash
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,'ACCURATE','NONE',repeat('0',64)
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'feedback was accepted for a nonterminal question run';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

INSERT INTO askdata.question_run_events(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,event_index,run_version,state,
  event_type,status,code,summary_json,event_hash
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,1,record_version,current_state,
  'STATE_TRANSITION','SUCCEEDED','RECEIVED',
  jsonb_build_object('state','RECEIVED'),repeat('e',64)
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id';

DO $$
BEGIN
  UPDATE askdata.question_runs
  SET result_hash=repeat('9',64),record_version=record_version+1
  WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'result hash was accepted before RESULT_VERIFYING';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.question_run_events(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,event_index,run_version,state,
    event_type,status,code,action_hash,summary_json,event_hash
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,2,record_version,current_state,
    'LLM_DECISION','SUCCEEDED','DECISION_READY',repeat('1',64),
    '{}'::jsonb,repeat('7',64)
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'LLM decision without AI request was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  UPDATE askdata.question_runs
  SET current_state='EXECUTING',record_version=record_version+1
  WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'illegal RECEIVED to EXECUTING transition was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.question_runs(
    tenant_id,domain_id,actor_id,idempotency_key_hash,question_hash,
    policy_scope_hash,release_id,release_content_hash
  )
  SELECT
    release.tenant_id,release.domain_id,askdata.current_actor_id(),
    repeat('9',64),repeat('8',64),repeat('7',64),release.id,repeat('0',64)
  FROM askdata.releases AS release
  WHERE release.tenant_id=askdata.current_tenant_id()
    AND release.domain_id=askdata.current_domain_id()
    AND release.status='ACTIVE';
  RAISE EXCEPTION 'mismatched release content hash was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

UPDATE askdata.question_runs
SET current_state='AUTHORIZED',record_version=record_version+1
WHERE id=:'askdata_runtime_run_id';
UPDATE askdata.question_runs
SET current_state='CONTEXT_READY',record_version=record_version+1
WHERE id=:'askdata_runtime_run_id';
UPDATE askdata.question_runs
SET current_state='UNDERSTANDING',understanding_hash=repeat('1',64),
  record_version=record_version+1
WHERE id=:'askdata_runtime_run_id';
UPDATE askdata.question_runs
SET current_state='RETRIEVING',step_count=1,llm_calls_used=1,
  elapsed_ms=5,record_version=record_version+1
WHERE id=:'askdata_runtime_run_id';

INSERT INTO askdata.tool_calls(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,run_version,tool_call_id,
  tool_name,state,status,request_hash,result_hash,call_hash,evidence_ids,
  budget_json,duration_ms
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,record_version,'verify-call-1','search_semantic_objects',
  current_state,'SUCCEEDED',repeat('2',64),repeat('3',64),repeat('4',64),
  ARRAY['evidence-verify'],
  jsonb_build_object('stepCount',step_count,'toolCallsUsed',tool_calls_used),4
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id';

-- An exact retry does not create a second audit outcome. The immutable
-- call_hash lets the Go store distinguish the same replay from an ID collision.
INSERT INTO askdata.tool_calls(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,run_version,tool_call_id,
  tool_name,state,status,request_hash,result_hash,call_hash,evidence_ids,
  budget_json,duration_ms
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,record_version,'verify-call-1','search_semantic_objects',
  current_state,'SUCCEEDED',repeat('2',64),repeat('3',64),repeat('4',64),
  ARRAY['evidence-verify'],
  jsonb_build_object('stepCount',step_count,'toolCallsUsed',tool_calls_used),4
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id'
ON CONFLICT ON CONSTRAINT askdata_tool_calls_question_call_key DO NOTHING;

SELECT count(*)=1 AS askdata_tool_call_retry_idempotent
FROM askdata.tool_calls
WHERE question_run_id=:'askdata_runtime_run_id'
  AND tool_call_id='verify-call-1'
\gset
\if :askdata_tool_call_retry_idempotent
\else
  \echo 'askdata tool_call_id retry is not idempotent'
  \quit 1
\endif

INSERT INTO askdata.question_run_events(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,event_index,run_version,state,
  event_type,status,code,tool_call_id,summary_json,event_hash
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,2,record_version,current_state,
  'TOOL_RESULT','SUCCEEDED','TOOL_SUCCEEDED','verify-call-1',
  '{}'::jsonb,repeat('7',64)
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id';

DO $$
BEGIN
  INSERT INTO askdata.question_run_events(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,event_index,run_version,state,
    event_type,status,code,tool_call_id,summary_json,event_hash
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,3,record_version,current_state,
    'TOOL_RESULT','SUCCEEDED','TOOL_SUCCEEDED','missing-call',
    '{}'::jsonb,repeat('8',64)
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'tool event without an exact outcome was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.tool_calls(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,run_version,tool_call_id,
    tool_name,state,status,request_hash,result_hash,call_hash,
    budget_json,duration_ms
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,record_version+1,'verify-call-bad-version',
    'search_semantic_objects',current_state,'SUCCEEDED',
    repeat('2',64),repeat('3',64),repeat('5',64),'{}'::jsonb,1
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'tool call with a future run version was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

INSERT INTO askdata.question_artifacts(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,artifact_index,run_version,
  artifact_type,schema_version,artifact_hash,evidence_ids,payload_json
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,1,record_version,'BLOCK','block-v1',repeat('f',64),
  ARRAY['evidence-verify'],jsonb_build_object('code','VERIFY_BLOCK')
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id';

UPDATE askdata.question_runs
SET current_state='BLOCKED',disposition='REFUSE',
  completion_code='VERIFY_BLOCK',completion_artifact_hash=repeat('f',64),
  step_count=2,tool_calls_used=1,elapsed_ms=12,
  record_version=record_version+1
WHERE id=:'askdata_runtime_run_id';

INSERT INTO askdata.question_run_events(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,event_index,run_version,state,
  event_type,stage,status,code,artifact_hash,summary_json,event_hash
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,3,record_version,current_state,
  'STATE_TRANSITION',current_state,'BLOCKED','VERIFY_BLOCK',completion_artifact_hash,
  jsonb_build_object('state','BLOCKED','code','VERIFY_BLOCK'),repeat('6',64)
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id';

DO $$
BEGIN
  INSERT INTO askdata.question_artifacts(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,artifact_index,run_version,
    artifact_type,schema_version,artifact_hash,payload_json
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,2,record_version,'EVIDENCE','evidence-v1',repeat('8',64),
    '{}'::jsonb
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'artifact was appended after terminal completion';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.question_run_events(
    tenant_id,domain_id,actor_id,question_run_id,release_id,
    release_content_hash,policy_scope_hash,event_index,run_version,state,
    event_type,stage,status,code,summary_json,event_hash
  )
  SELECT
    tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
    policy_scope_hash,4,record_version,current_state,
    'PROGRESS',current_state,'SUCCEEDED','LATE_PROGRESS','{}'::jsonb,
    repeat('9',64)
  FROM askdata.question_runs WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'event was appended after terminal completion';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

SELECT (
  current_state='BLOCKED' AND disposition='REFUSE'
  AND completion_code='VERIFY_BLOCK' AND completed_at IS NOT NULL
  AND record_version=6 AND step_count=2 AND tool_calls_used=1
) AS askdata_question_terminal_shape_valid
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id'
\gset
\if :askdata_question_terminal_shape_valid
\else
  \echo 'askdata terminal question shape is invalid'
  \quit 1
\endif

INSERT INTO askdata.query_feedback(
  tenant_id,domain_id,actor_id,question_run_id,release_id,
  release_content_hash,policy_scope_hash,rating,issue_type,comment,feedback_hash
)
SELECT
  tenant_id,domain_id,actor_id,id,release_id,release_content_hash,
  policy_scope_hash,'ACCURATE','NONE','',repeat('0',64)
FROM askdata.question_runs WHERE id=:'askdata_runtime_run_id'
RETURNING id AS feedback_id,feedback_hash AS initial_hash
\gset askdata_feedback_

UPDATE askdata.query_feedback
SET rating='INACCURATE',issue_type='METRIC',comment='wrong metric binding',
  record_version=record_version+1
WHERE id=:'askdata_feedback_feedback_id'
RETURNING feedback_hash AS updated_hash
\gset askdata_feedback_

SELECT (
  record_version=2 AND rating='INACCURATE' AND issue_type='METRIC'
  AND feedback_hash=:'askdata_feedback_updated_hash'
  AND feedback_hash<>:'askdata_feedback_initial_hash'
) AS askdata_feedback_update_valid
FROM askdata.query_feedback WHERE id=:'askdata_feedback_feedback_id'
\gset
\if :askdata_feedback_update_valid
\else
  \echo 'askdata feedback optimistic update or hash stamping failed'
  \quit 1
\endif

DO $$
BEGIN
  UPDATE askdata.query_feedback
  SET comment='stale update'
  WHERE question_run_id=(
    SELECT id FROM askdata.question_runs
    WHERE idempotency_key_hash=repeat('b',64)
  );
  RAISE EXCEPTION 'stale feedback update was accepted';
EXCEPTION WHEN serialization_failure THEN
  NULL;
END
$$;

DO $$
BEGIN
  UPDATE askdata.query_feedback
  SET rating='ACCURATE',issue_type='METRIC',record_version=record_version+1
  WHERE question_run_id=(
    SELECT id FROM askdata.question_runs
    WHERE idempotency_key_hash=repeat('b',64)
  );
  RAISE EXCEPTION 'inconsistent feedback issue shape was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

INSERT INTO askdata.evaluation_sets(
  tenant_id,domain_id,code,version_no,name,description,dataset_split,
  evaluation_mode,target_release_id,target_semantic_version,
  target_release_content_hash,notes,created_by,updated_by
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  'verify_eval',1,'verify governed evaluation set','rollback-only fixture',
  'PRODUCTION_REGRESSION','END_TO_END_RESULT_EQUIVALENCE',
  :'askdata_runtime_release_id','verify-runtime-v1',repeat('a',64),'',
  :'askdata_runtime_actor_id',:'askdata_runtime_actor_id'
)
RETURNING id AS set_id
\gset askdata_eval_

INSERT INTO askdata.evaluation_cases(
  tenant_id,domain_id,evaluation_set_id,case_key,schema_version,
  question_hash,approved_question,question_redaction_policy_hash,priority,
  answerable,expected_disposition,security_expectation,complexity,ambiguity,
  expected_path_hash,expected_ir_hash,expected_result_hash,content_hash,
  created_by,updated_by
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  :'askdata_eval_set_id','relational-direct','evaluation-case-v1',
  repeat('1',64),'approved deidentified aggregated revenue question',
  repeat('2',64),'P0',true,'DIRECT','NONE','RELATIONAL','NONE',
  repeat('3',64),repeat('4',64),repeat('5',64),repeat('0',64),
  :'askdata_runtime_actor_id',:'askdata_runtime_actor_id'
)
RETURNING id AS case_id,content_hash AS case_content_hash,
  content_updated_by AS case_content_updated_by
\gset askdata_eval_

SELECT (
  :'askdata_eval_case_content_updated_by'::uuid=:'askdata_runtime_actor_id'::uuid
) AS askdata_case_content_author_stamped
\gset
\if :askdata_case_content_author_stamped
\else
  \echo 'evaluation case current-content author was not database-stamped'
  \quit 1
\endif

DO $$
BEGIN
  INSERT INTO askdata.evaluation_case_reviews(
    tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,
    reviewer_id,decision,reviewed_case_content_hash,review_hash
  )
  SELECT
    evaluation_case.tenant_id,evaluation_case.domain_id,
    evaluation_case.evaluation_set_id,evaluation_case.id,1,
    askdata.current_actor_id(),'APPROVED',evaluation_case.content_hash,
    repeat('0',64)
  FROM askdata.evaluation_cases AS evaluation_case
  WHERE evaluation_case.case_key='relational-direct';
  RAISE EXCEPTION 'case author self-review was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

SELECT set_config('verify.evaluation_set_id',:'askdata_eval_set_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_observer_id',true);
DO $$
BEGIN
  PERFORM askdata.seal_evaluation_set(
    current_setting('verify.evaluation_set_id')::uuid,
    askdata.current_actor_id()
  );
  RAISE EXCEPTION 'ordinary domain member bypassed evaluation management RLS';
EXCEPTION WHEN insufficient_privilege THEN
  NULL;
END
$$;

SELECT set_config('app.user_id',:'askdata_runtime_reviewer_one_id',true);
UPDATE askdata.evaluation_cases
SET priority='P2',updated_by=:'askdata_runtime_reviewer_one_id',
  record_version=record_version+1
WHERE id=:'askdata_eval_case_id'
RETURNING (content_updated_by=:'askdata_runtime_reviewer_one_id')
  AS content_author_rotated
\gset askdata_eval_
\if :askdata_eval_content_author_rotated
\else
  \echo 'evaluation case content edit did not rotate the current-content author'
  \quit 1
\endif

DO $$
BEGIN
  INSERT INTO askdata.evaluation_case_reviews(
    tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,
    reviewer_id,decision,reviewed_case_content_hash,review_hash
  )
  SELECT
    evaluation_case.tenant_id,evaluation_case.domain_id,
    evaluation_case.evaluation_set_id,evaluation_case.id,1,
    askdata.current_actor_id(),'APPROVED',evaluation_case.content_hash,
    repeat('0',64)
  FROM askdata.evaluation_cases AS evaluation_case
  WHERE evaluation_case.case_key='relational-direct';
  RAISE EXCEPTION 'current case content author self-review was accepted';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
UPDATE askdata.evaluation_cases
SET priority='P0',updated_by=:'askdata_runtime_actor_id',
  record_version=record_version+1
WHERE id=:'askdata_eval_case_id'
RETURNING content_hash AS case_content_hash
\gset askdata_eval_

SELECT set_config('app.user_id',:'askdata_runtime_reviewer_one_id',true);
INSERT INTO askdata.evaluation_case_reviews(
  tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,
  reviewer_id,decision,reviewed_case_content_hash,review_comment,review_hash
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  :'askdata_eval_set_id',:'askdata_eval_case_id',1,
  :'askdata_runtime_reviewer_one_id','APPROVED',
  :'askdata_eval_case_content_hash','review one',repeat('0',64)
);

SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
UPDATE askdata.evaluation_cases
SET priority='P1',updated_by=:'askdata_runtime_actor_id',
  record_version=record_version+1
WHERE id=:'askdata_eval_case_id'
RETURNING content_hash AS case_content_hash,
  (independent_review_count=0) AS stale_review_invalidated
\gset askdata_eval_
\if :askdata_eval_stale_review_invalidated
\else
  \echo 'evaluation case edit did not invalidate the stale review hash'
  \quit 1
\endif

SELECT set_config('app.user_id',:'askdata_runtime_reviewer_one_id',true);
UPDATE askdata.evaluation_case_reviews
SET reviewed_case_content_hash=:'askdata_eval_case_content_hash',
  review_comment='review one refreshed',record_version=record_version+1
WHERE evaluation_case_id=:'askdata_eval_case_id' AND review_slot=1;

SELECT independent_review_count=1 AS askdata_current_review_restored
FROM askdata.evaluation_cases WHERE id=:'askdata_eval_case_id'
\gset
\if :askdata_current_review_restored
\else
  \echo 'evaluation review refresh did not bind the current case hash'
  \quit 1
\endif

DO $$
BEGIN
  INSERT INTO askdata.evaluation_case_reviews(
    tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,
    reviewer_id,decision,reviewed_case_content_hash,review_hash
  )
  SELECT
    evaluation_case.tenant_id,evaluation_case.domain_id,
    evaluation_case.evaluation_set_id,evaluation_case.id,2,
    askdata.current_actor_id(),'APPROVED',evaluation_case.content_hash,
    repeat('0',64)
  FROM askdata.evaluation_cases AS evaluation_case
  WHERE evaluation_case.case_key='relational-direct';
  RAISE EXCEPTION 'one reviewer occupied both review slots';
EXCEPTION WHEN unique_violation THEN
  NULL;
END
$$;

SELECT set_config('app.user_id',:'askdata_runtime_reviewer_two_id',true);
INSERT INTO askdata.evaluation_case_reviews(
  tenant_id,domain_id,evaluation_set_id,evaluation_case_id,review_slot,
  reviewer_id,decision,reviewed_case_content_hash,review_comment,review_hash
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',
  :'askdata_eval_set_id',:'askdata_eval_case_id',2,
  :'askdata_runtime_reviewer_two_id','APPROVED',
  :'askdata_eval_case_content_hash','review two',repeat('0',64)
);

SELECT independent_review_count=2 AS askdata_two_current_reviews_recorded
FROM askdata.evaluation_cases WHERE id=:'askdata_eval_case_id'
\gset
\if :askdata_two_current_reviews_recorded
\else
  \echo 'evaluation case did not record exactly two current approvals'
  \quit 1
\endif

RESET ROLE;
SET LOCAL ROLE :"worker_user";
SELECT set_config('app.tenant_id',:'askdata_runtime_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_runtime_domain_id',true);
SELECT set_config('app.access_mode','SYSTEM',true);
DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
    comparison_report_hash,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,repeat('0',64),
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
    evaluation_case.expected_path_hash,evaluation_case.expected_path_hash,
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,true,true,false,repeat('8',64),1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.id=evaluation_set.target_release_id
   AND release.tenant_id=evaluation_set.tenant_id
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'DRAFT production regression set accepted a run';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

RESET ROLE;
SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_runtime_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_runtime_domain_id',true);
SELECT set_config('app.access_mode','USER',true);
SELECT askdata.seal_evaluation_set(
  :'askdata_eval_set_id',:'askdata_runtime_actor_id'
) AS succeeded
\gset askdata_eval_seal_
\if :askdata_eval_seal_succeeded
\else
  \echo 'evaluation set sealing did not report success'
  \quit 1
\endif

SELECT sealed_content_hash AS set_content_hash,
  (
    status='SEALED' AND sealed_case_count=1 AND sealed_review_count=2
    AND sealed_content_hash ~ '^[0-9a-f]{64}$'
  ) AS shape_valid
FROM askdata.evaluation_sets WHERE id=:'askdata_eval_set_id'
\gset askdata_eval_
\if :askdata_eval_shape_valid
\else
  \echo 'sealed evaluation set hash or counts are invalid'
  \quit 1
\endif

SELECT (
  (SELECT count(*) FROM askdata.evaluation_cases
   WHERE id=:'askdata_eval_case_id')=0
  AND (SELECT count(*) FROM askdata.evaluation_case_reviews
       WHERE evaluation_case_id=:'askdata_eval_case_id')=0
) AS askdata_sealed_gold_hidden_from_user_mode
\gset
\if :askdata_sealed_gold_hidden_from_user_mode
\else
  \echo 'sealed evaluation gold content is visible in USER mode'
  \quit 1
\endif

RESET ROLE;
DO $$
BEGIN
  UPDATE askdata.evaluation_cases SET priority='P2'
  WHERE case_key='relational-direct';
  RAISE EXCEPTION 'sealed evaluation case mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;
DO $$
BEGIN
  UPDATE askdata.evaluation_case_reviews SET review_comment='late edit'
  WHERE review_comment='review one refreshed';
  RAISE EXCEPTION 'sealed evaluation review mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;
SET LOCAL ROLE :"worker_user";
SELECT set_config('app.tenant_id',:'askdata_runtime_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_runtime_domain_id',true);
SELECT set_config('app.access_mode','SYSTEM',true);

SELECT (
  (SELECT count(*) FROM askdata.evaluation_cases
   WHERE id=:'askdata_eval_case_id')=1
  AND (SELECT count(*) FROM askdata.evaluation_case_reviews
       WHERE evaluation_case_id=:'askdata_eval_case_id')=2
) AS askdata_worker_can_read_sealed_gold
\gset
\if :askdata_worker_can_read_sealed_gold
\else
  \echo 'evaluation worker cannot read sealed gold content in SYSTEM mode'
  \quit 1
\endif

INSERT INTO askdata.evaluation_runs(
  tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
  evaluation_case_id,evaluation_set_content_hash,case_content_hash,
  release_id,semantic_version,release_content_hash,evaluation_mode,
  runner_version,run_key_hash,warehouse_snapshot_hash,
  warehouse_freshness_at,status,expected_disposition,actual_disposition,
  expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
  expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
  result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
  comparison_report_hash,duration_ms
) VALUES(
  :'askdata_runtime_tenant_id',:'askdata_runtime_domain_id',gen_random_uuid(),
  :'askdata_eval_set_id',:'askdata_eval_case_id',
  :'askdata_eval_set_content_hash',:'askdata_eval_case_content_hash',
  :'askdata_runtime_release_id','verify-runtime-v1',repeat('a',64),
  'END_TO_END_RESULT_EQUIVALENCE','verify-runner-v1',repeat('6',64),
  repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
  repeat('3',64),repeat('3',64),repeat('4',64),repeat('4',64),
  repeat('5',64),repeat('5',64),true,true,true,true,true,false,
  repeat('8',64),12
)
RETURNING id AS run_id
\gset askdata_eval_

DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
    comparison_report_hash,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,evaluation_set.sealed_content_hash,
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
    evaluation_case.expected_path_hash,evaluation_case.expected_path_hash,
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,true,true,false,repeat('8',64),1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.tenant_id=evaluation_set.tenant_id
   AND release.domain_id=evaluation_set.domain_id
   AND release.semantic_version='verify-runtime-v2'
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'evaluation run escaped the exact set release pin';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
    comparison_report_hash,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,evaluation_set.sealed_content_hash,
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
    repeat('f',64),repeat('f',64),
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,true,true,false,repeat('8',64),1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.id=evaluation_set.target_release_id
   AND release.tenant_id=evaluation_set.tenant_id
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'evaluation run escaped the exact expected path pin';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
    comparison_report_hash,failure_stage,failure_code,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,evaluation_set.sealed_content_hash,
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'FAILED','DIRECT','DIRECT',
    evaluation_case.expected_path_hash,evaluation_case.expected_path_hash,
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,false,true,true,repeat('8',64),'SECURITY','LEAK',1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.id=evaluation_set.target_release_id
   AND release.tenant_id=evaluation_set.tenant_id
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'leak fact was accepted with security_passed=true';
EXCEPTION WHEN check_violation THEN
  NULL;
END
$$;

DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,
    comparison_report_hash,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,evaluation_set.sealed_content_hash,
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
    evaluation_case.expected_path_hash,evaluation_case.expected_path_hash,
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,true,true,repeat('8',64),1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.id=evaluation_set.target_release_id
   AND release.tenant_id=evaluation_set.tenant_id
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'evaluation run silently defaulted the leak fact';
EXCEPTION WHEN not_null_violation THEN
  NULL;
END
$$;

RESET ROLE;
SET LOCAL ROLE :"app_user";
SELECT set_config('app.tenant_id',:'askdata_runtime_tenant_id',true);
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.domain_id',:'askdata_runtime_domain_id',true);
SELECT set_config('app.access_mode','USER',true);
UPDATE askdata.evaluation_sets
SET status='RETIRED',updated_by=:'askdata_runtime_actor_id',
  record_version=record_version+1
WHERE id=:'askdata_eval_set_id';

RESET ROLE;
SET LOCAL ROLE :"worker_user";
SELECT set_config('app.access_mode','SYSTEM',true);
DO $$
BEGIN
  INSERT INTO askdata.evaluation_runs(
    tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,
    evaluation_case_id,evaluation_set_content_hash,case_content_hash,
    release_id,semantic_version,release_content_hash,evaluation_mode,
    runner_version,run_key_hash,warehouse_snapshot_hash,
    warehouse_freshness_at,status,expected_disposition,actual_disposition,
    expected_path_hash,actual_path_hash,expected_ir_hash,actual_ir_hash,
    expected_result_hash,actual_result_hash,ir_equivalent,path_equivalent,
    result_equivalent,strict_correct,security_passed,sensitive_leak_detected,
    comparison_report_hash,duration_ms
  )
  SELECT
    evaluation_set.tenant_id,evaluation_set.domain_id,gen_random_uuid(),
    evaluation_set.id,evaluation_case.id,evaluation_set.sealed_content_hash,
    evaluation_case.content_hash,release.id,release.semantic_version,
    release.content_hash,evaluation_set.evaluation_mode,'verify-runner-v1',
    repeat('9',64),repeat('7',64),now(),'PASSED','DIRECT','DIRECT',
    evaluation_case.expected_path_hash,evaluation_case.expected_path_hash,
    evaluation_case.expected_ir_hash,evaluation_case.expected_ir_hash,
    evaluation_case.expected_result_hash,evaluation_case.expected_result_hash,
    true,true,true,true,true,false,repeat('8',64),1
  FROM askdata.evaluation_sets AS evaluation_set
  JOIN askdata.evaluation_cases AS evaluation_case
    ON evaluation_case.evaluation_set_id=evaluation_set.id
   AND evaluation_case.tenant_id=evaluation_set.tenant_id
  JOIN askdata.releases AS release
    ON release.id=evaluation_set.target_release_id
   AND release.tenant_id=evaluation_set.tenant_id
  WHERE evaluation_set.code='verify_eval';
  RAISE EXCEPTION 'retired evaluation set accepted a new run';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

RESET ROLE;
SET LOCAL ROLE :"app_user";
SELECT set_config('app.user_id',:'askdata_runtime_actor_id',true);
SELECT set_config('app.access_mode','USER',true);

SELECT set_config('app.user_id',:'askdata_runtime_observer_id',true);
SELECT (
  (SELECT count(*) FROM askdata.question_runs
   WHERE id=:'askdata_runtime_run_id')=0
  AND (SELECT count(*) FROM askdata.query_feedback
       WHERE id=:'askdata_feedback_feedback_id')=0
) AS askdata_question_actor_rls_isolated
\gset
\if :askdata_question_actor_rls_isolated
\else
  \echo 'askdata question actor RLS isolation failed'
  \quit 1
\endif

RESET ROLE;

DO $$
BEGIN
  UPDATE askdata.evaluation_runs SET duration_ms=duration_ms
  WHERE run_key_hash=repeat('6',64);
  RAISE EXCEPTION 'evaluation run mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  DELETE FROM askdata.evaluation_runs WHERE run_key_hash=repeat('6',64);
  RAISE EXCEPTION 'evaluation run deletion was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  DELETE FROM askdata.query_feedback
  WHERE question_run_id=(
    SELECT id FROM askdata.question_runs
    WHERE idempotency_key_hash=repeat('b',64)
  );
  RAISE EXCEPTION 'query feedback deletion was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  UPDATE askdata.question_runs
  SET record_version=record_version+1
  WHERE idempotency_key_hash=repeat('b',64);
  RAISE EXCEPTION 'terminal question run mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  UPDATE askdata.question_run_events SET code=code
  WHERE event_hash=repeat('e',64);
  RAISE EXCEPTION 'question run event mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  UPDATE askdata.question_artifacts SET schema_version=schema_version
  WHERE artifact_hash=repeat('f',64);
  RAISE EXCEPTION 'question artifact mutation was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;

DO $$
BEGIN
  DELETE FROM askdata.tool_calls WHERE call_hash=repeat('4',64);
  RAISE EXCEPTION 'tool call audit deletion was accepted';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
  NULL;
END
$$;
ROLLBACK;

DO $$
DECLARE
  relation_name text;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[
    'metrics','metric_versions','metric_candidates',
    'reports','report_drafts','report_versions',
    'semantic_question_runs','semantic_releases','semantic_graph_nodes',
    'ads_modeling_jobs'
  ] LOOP
    IF to_regclass('platform.'||relation_name) IS NOT NULL THEN
      RAISE EXCEPTION 'decommissioned relation still exists: platform.%', relation_name;
    END IF;
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM platform.permissions
    WHERE resource_type IN ('METRIC','REPORT','AI')
  ) THEN
    RAISE EXCEPTION 'decommissioned permission resources still exist';
  END IF;
  IF EXISTS (
    SELECT 1 FROM platform.roles
    WHERE is_system AND code IN ('analyst','report_designer','viewer')
  ) THEN
    RAISE EXCEPTION 'decommissioned system roles still exist';
  END IF;
  IF to_regprocedure(
    'platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)'
  ) IS NOT NULL THEN
    RAISE EXCEPTION 'decommissioned dimension profile resource helper still exists';
  END IF;
  IF to_regprocedure('platform.enqueue_data_source_connection_test(uuid,uuid,text)') IS NULL THEN
    RAISE EXCEPTION 'data-source connection-test boundary is missing';
  END IF;
  IF EXISTS(
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='platform.tenants'::regclass
      AND tgname='tenants_initialize_semantic_release_state'
      AND NOT tgisinternal
  ) OR to_regprocedure(
    'platform.initialize_semantic_release_state()'
  ) IS NOT NULL THEN
    RAISE EXCEPTION 'retired semantic tenant trigger or function still exists';
  END IF;
  IF to_regprocedure('platform.trigger_manual_dim_modeling(uuid)') IS NULL OR
     to_regprocedure('platform.trigger_manual_dwd_modeling(uuid)') IS NULL THEN
    RAISE EXCEPTION 'retained dataset modeling boundaries are missing';
  END IF;
  IF to_regprocedure(
       'platform.modeling_actor_can_access_current_domain(uuid)'
     ) IS NULL OR
     position(
       'modeling_actor_can_access_current_domain(actor_id)' IN
       pg_get_functiondef(
         'platform.trigger_manual_dim_modeling(uuid,uuid[])'::regprocedure
       )
     )=0 OR
     position(
       'modeling_actor_can_access_current_domain(actor_id)' IN
       pg_get_functiondef(
         'platform.trigger_manual_dwd_modeling(uuid)'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dataset modeling domain authorization boundary is missing';
  END IF;
  IF position(
       'dataset-tag-suggestion-v7' IN
       pg_get_functiondef(
         'platform.enqueue_dataset_tag_suggestion()'::regprocedure
       )
     )=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v7';
  END IF;
END
$$;

SELECT (
  has_function_privilege(
    :'app_user','platform.semantic_tag_can_read(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'app_user','platform.semantic_tag_can_write(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','platform.semantic_tag_can_read(uuid)','EXECUTE'
  )
  AND has_function_privilege(
    :'worker_user','platform.semantic_tag_can_write(uuid)','EXECUTE'
  )
) AS dataset_tag_policy_helpers_executable
\gset
\if :dataset_tag_policy_helpers_executable
\else
  \echo 'dataset tag policy helper runtime privileges are missing'
  \quit 1
\endif

SELECT 'retained platform and askdata permission/schema checks passed' AS result;
SQL
