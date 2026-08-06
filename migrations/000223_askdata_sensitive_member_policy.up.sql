-- Keep sensitive dimension values inside the authoritative PostgreSQL
-- boundary. Runtime callers submit only a dimension-bound hash and receive an
-- opaque member version; canonical values and aliases never cross this API.

-- Take the parent tables before the alias table's AccessExclusive ALTER lock.
-- This preserves the member-delete -> alias-cascade lock order and closes the
-- sensitivity audit/trigger-installation window for concurrent writers.
LOCK TABLE askdata.dimensions,askdata.dimension_members
IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE askdata.dimension_member_aliases
  ADD COLUMN alias_key_hash text;

CREATE OR REPLACE FUNCTION askdata.stamp_dimension_member_alias_key_hash()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  -- PostgreSQL text cannot contain NUL, so concatenate UTF-8 bytea values to
  -- exactly match SHA256(dimension_version_id || NUL || normalized_alias).
  NEW.alias_key_hash=pg_catalog.encode(
    public.digest(
      pg_catalog.convert_to(NEW.dimension_version_id::text,'UTF8')
        || pg_catalog.decode('00','hex')
        || pg_catalog.convert_to(NEW.normalized_alias,'UTF8'),
      'sha256'
    ),
    'hex'
  );
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.stamp_dimension_member_alias_key_hash()
FROM PUBLIC;

-- Backfill immutable CERTIFIED aliases without weakening their normal
-- application-time mutation guard. Both trigger changes are transactional.
ALTER TABLE askdata.dimension_member_aliases
  DISABLE TRIGGER askdata_dimension_member_aliases_protect_certified;
ALTER TABLE askdata.dimension_member_aliases
  DISABLE TRIGGER askdata_dimension_member_aliases_set_updated_at;

UPDATE askdata.dimension_member_aliases
SET alias_key_hash=pg_catalog.encode(
  public.digest(
    pg_catalog.convert_to(dimension_version_id::text,'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(normalized_alias,'UTF8'),
    'sha256'
  ),
  'hex'
);

ALTER TABLE askdata.dimension_member_aliases
  ENABLE TRIGGER askdata_dimension_member_aliases_set_updated_at;
ALTER TABLE askdata.dimension_member_aliases
  ENABLE TRIGGER askdata_dimension_member_aliases_protect_certified;

ALTER TABLE askdata.dimension_member_aliases
  ALTER COLUMN alias_key_hash SET NOT NULL,
  ADD CONSTRAINT askdata_dimension_member_aliases_key_hash_check
    CHECK(alias_key_hash ~ '^[0-9a-f]{64}$');

CREATE TRIGGER askdata_dimension_member_aliases_stamp_key_hash
BEFORE INSERT OR UPDATE OF dimension_version_id,normalized_alias,alias_key_hash
ON askdata.dimension_member_aliases
FOR EACH ROW EXECUTE FUNCTION askdata.stamp_dimension_member_alias_key_hash();

CREATE INDEX askdata_dimension_member_aliases_hash_lookup_idx
  ON askdata.dimension_member_aliases(
    tenant_id,domain_id,dimension_version_id,alias_key_hash,status,
    priority,member_version_id
  );

-- A member may be stricter than its dimension, but never less sensitive. The
-- reverse trigger prevents a DRAFT dimension edit from invalidating existing
-- members without touching those immutable member versions.
CREATE OR REPLACE FUNCTION askdata.enforce_dimension_member_sensitivity_floor()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE dimension_sensitivity text;
BEGIN
  SELECT dimension.sensitivity
  INTO dimension_sensitivity
  FROM askdata.dimensions AS dimension
  WHERE dimension.tenant_id=NEW.tenant_id
    AND dimension.domain_id=NEW.domain_id
    AND dimension.id=NEW.dimension_version_id
  FOR SHARE;

  -- The existing dependency trigger owns the missing/cross-domain error. This
  -- trigger adds only the sensitivity invariant and therefore remains
  -- composable with that validation.
  IF dimension_sensitivity IS NULL THEN
    RETURN NEW;
  END IF;

  IF (CASE NEW.sensitivity
        WHEN 'PUBLIC' THEN 1
        WHEN 'INTERNAL' THEN 2
        WHEN 'CONFIDENTIAL' THEN 3
        WHEN 'RESTRICTED' THEN 4
        ELSE 0
      END)
     < (CASE dimension_sensitivity
          WHEN 'PUBLIC' THEN 1
          WHEN 'INTERNAL' THEN 2
          WHEN 'CONFIDENTIAL' THEN 3
          WHEN 'RESTRICTED' THEN 4
          ELSE 5
        END) THEN
    RAISE EXCEPTION 'dimension member sensitivity cannot be weaker than its dimension'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_dimension_sensitivity_floor()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM askdata.dimension_members AS member
    WHERE member.tenant_id=NEW.tenant_id
      AND member.domain_id=NEW.domain_id
      AND member.dimension_version_id=NEW.id
      AND (CASE member.sensitivity
             WHEN 'PUBLIC' THEN 1
             WHEN 'INTERNAL' THEN 2
             WHEN 'CONFIDENTIAL' THEN 3
             WHEN 'RESTRICTED' THEN 4
             ELSE 0
           END)
          < (CASE NEW.sensitivity
               WHEN 'PUBLIC' THEN 1
               WHEN 'INTERNAL' THEN 2
               WHEN 'CONFIDENTIAL' THEN 3
               WHEN 'RESTRICTED' THEN 4
               ELSE 5
             END)
  ) THEN
    RAISE EXCEPTION 'dimension sensitivity cannot exceed an existing member sensitivity'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.enforce_dimension_member_sensitivity_floor(),
  askdata.enforce_dimension_sensitivity_floor()
FROM PUBLIC;

DO $sensitivity_floor$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM askdata.dimension_members AS member
    JOIN askdata.dimensions AS dimension
      ON dimension.tenant_id=member.tenant_id
     AND dimension.domain_id=member.domain_id
     AND dimension.id=member.dimension_version_id
    WHERE (CASE member.sensitivity
             WHEN 'PUBLIC' THEN 1
             WHEN 'INTERNAL' THEN 2
             WHEN 'CONFIDENTIAL' THEN 3
             WHEN 'RESTRICTED' THEN 4
             ELSE 0
           END)
          < (CASE dimension.sensitivity
               WHEN 'PUBLIC' THEN 1
               WHEN 'INTERNAL' THEN 2
               WHEN 'CONFIDENTIAL' THEN 3
               WHEN 'RESTRICTED' THEN 4
               ELSE 5
             END)
  ) THEN
    RAISE EXCEPTION 'existing dimension member violates the sensitivity floor'
      USING ERRCODE='23514';
  END IF;
END
$sensitivity_floor$;

CREATE TRIGGER askdata_dimension_members_enforce_sensitivity_floor
BEFORE INSERT OR UPDATE ON askdata.dimension_members
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_dimension_member_sensitivity_floor();

CREATE TRIGGER askdata_dimensions_enforce_member_sensitivity_floor
BEFORE UPDATE OF tenant_id,domain_id,id,sensitivity ON askdata.dimensions
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_dimension_sensitivity_floor();

-- These existing dependency triggers must keep working after runtime roles
-- lose direct SELECT on raw member tables. Their definitions already pin every
-- tenant/domain/object reference; this migration changes only execution
-- identity and fixes the trusted search path.
ALTER FUNCTION askdata.validate_search_document_subject()
  SECURITY DEFINER
  SET search_path TO pg_catalog,askdata;
ALTER FUNCTION askdata.validate_member_dependency()
  SECURITY DEFINER
  SET search_path TO pg_catalog,askdata;
ALTER FUNCTION askdata.validate_release_object()
  SECURITY DEFINER
  SET search_path TO pg_catalog,askdata;

-- validate_release_object now runs as its owner so dependency checks continue
-- after raw-table SELECT is revoked. Keep its legacy DOMAIN branch from using
-- that identity to admit another domain in the same tenant.
CREATE OR REPLACE FUNCTION askdata.validate_release_domain_object_identity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.object_type='DOMAIN'
    AND (
      NEW.object_id<>NEW.domain_id
      OR NEW.object_version_id<>NEW.domain_id
    ) THEN
    RAISE EXCEPTION 'DOMAIN release object identity must equal its release domain'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.validate_release_domain_object_identity()
FROM PUBLIC;

LOCK TABLE askdata.release_objects IN SHARE ROW EXCLUSIVE MODE;

DO $release_domain_identity_backfill_guard$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM askdata.release_objects AS release_object
    WHERE release_object.object_type='DOMAIN'
      AND (
        release_object.object_id<>release_object.domain_id
        OR release_object.object_version_id<>release_object.domain_id
      )
  ) THEN
    RAISE EXCEPTION 'existing DOMAIN release object identity differs from its release domain'
      USING ERRCODE='23514';
  END IF;
END
$release_domain_identity_backfill_guard$;

CREATE TRIGGER askdata_release_objects_00_validate_domain_identity
BEFORE INSERT OR UPDATE OF
  domain_id,object_type,object_id,object_version_id
ON askdata.release_objects
FOR EACH ROW EXECUTE FUNCTION
  askdata.validate_release_domain_object_identity();

-- MEMBER manifest contracts are deliberately tiny and label-free because
-- release_objects remains readable by runtime roles. The only member-specific
-- metadata allowed here is the owning dimension version ID and a bounded,
-- stable list of opaque alias version IDs. Neither member_key_hash nor alias
-- content hashes are release contract fields because either can become a
-- low-entropy dictionary oracle.
CREATE OR REPLACE FUNCTION askdata.member_release_contract_is_safe(
  selected_contract jsonb,
  selected_dimension_version_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
SET search_path=pg_catalog
AS $$
DECLARE alias_hash_count integer;
DECLARE distinct_alias_version_count integer;
BEGIN
  IF pg_catalog.jsonb_typeof(selected_contract)<>'object'
    OR (
      SELECT pg_catalog.count(*)
      FROM pg_catalog.jsonb_object_keys(selected_contract)
    )<>4
    OR NOT selected_contract ?& ARRAY[
      'schemaVersion','type','dimensionVersionId','aliasVersionIds'
    ]
    OR selected_contract->>'schemaVersion'<>
       'askdata-member-release-v1'
    OR selected_contract->>'type'<>'MEMBER'
    OR selected_contract->>'dimensionVersionId'<>
       selected_dimension_version_id::text
    OR pg_catalog.jsonb_typeof(
         selected_contract->'aliasVersionIds'
       )<>'array' THEN
    RETURN false;
  END IF;

  alias_hash_count=pg_catalog.jsonb_array_length(
    selected_contract->'aliasVersionIds'
  );
  IF alias_hash_count>64 THEN
    RETURN false;
  END IF;

  IF EXISTS(
    SELECT 1
    FROM pg_catalog.jsonb_array_elements(
      selected_contract->'aliasVersionIds'
    ) AS alias_version(value)
    WHERE pg_catalog.jsonb_typeof(alias_version.value)<>'string'
  ) THEN
    RETURN false;
  END IF;

  SELECT pg_catalog.count(*),
         pg_catalog.count(DISTINCT alias_version.value)
  INTO alias_hash_count,distinct_alias_version_count
  FROM pg_catalog.jsonb_array_elements_text(
    selected_contract->'aliasVersionIds'
  ) AS alias_version(value)
  WHERE alias_version.value ~
    '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

  RETURN alias_hash_count=
         pg_catalog.jsonb_array_length(
           selected_contract->'aliasVersionIds'
         )
    AND distinct_alias_version_count=alias_hash_count
    AND NOT EXISTS(
      SELECT 1
      FROM (
        SELECT alias_version.value,
               pg_catalog.lag(alias_version.value) OVER (
                 ORDER BY alias_version.ordinality
               ) AS prior_value
        FROM pg_catalog.jsonb_array_elements_text(
          selected_contract->'aliasVersionIds'
        ) WITH ORDINALITY AS alias_version(value,ordinality)
      ) AS ordered_alias
      WHERE ordered_alias.prior_value>=ordered_alias.value
    );
END
$$;

CREATE OR REPLACE FUNCTION askdata.validate_member_release_contract()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE source_dimension_version_id uuid;
DECLARE source_member_sensitivity text;
DECLARE released_alias_version_count integer;
DECLARE certified_alias_version_count integer;
BEGIN
  IF NEW.object_type<>'MEMBER' THEN
    RETURN NEW;
  END IF;

  SELECT member.dimension_version_id,member.sensitivity
  INTO source_dimension_version_id,source_member_sensitivity
  FROM askdata.dimension_members AS member
  WHERE member.tenant_id=NEW.tenant_id
    AND member.domain_id=NEW.domain_id
    AND member.id=NEW.object_version_id
    AND member.member_id=NEW.object_id
    AND member.content_hash=NEW.content_hash
    AND member.status='CERTIFIED';

  IF source_dimension_version_id IS NULL
    OR NEW.sensitivity IS DISTINCT FROM source_member_sensitivity
    OR NOT
    askdata.member_release_contract_is_safe(
      NEW.contract_json,source_dimension_version_id
    ) THEN
    RAISE EXCEPTION 'MEMBER release contract must be label-free, dimension-bound, and contain at most 64 sorted unique alias version IDs'
      USING ERRCODE='23514';
  END IF;

  released_alias_version_count=pg_catalog.jsonb_array_length(
    NEW.contract_json->'aliasVersionIds'
  );
  SELECT pg_catalog.count(*)
  INTO certified_alias_version_count
  FROM pg_catalog.jsonb_array_elements_text(
    NEW.contract_json->'aliasVersionIds'
  ) AS released_alias(alias_version_id)
  JOIN askdata.dimension_member_aliases AS alias
    ON alias.id::text=released_alias.alias_version_id
   AND alias.tenant_id=NEW.tenant_id
   AND alias.domain_id=NEW.domain_id
   AND alias.dimension_version_id=source_dimension_version_id
   AND alias.member_version_id=NEW.object_version_id
   AND alias.status='CERTIFIED';

  IF certified_alias_version_count<>released_alias_version_count THEN
    RAISE EXCEPTION 'MEMBER release contract references a missing, cross-member, or uncertified alias version'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.member_release_contract_is_safe(jsonb,uuid),
  askdata.validate_member_release_contract()
FROM PUBLIC;

DO $member_release_contract_backfill_guard$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM askdata.release_objects AS release_object
    LEFT JOIN askdata.dimension_members AS member
      ON member.tenant_id=release_object.tenant_id
     AND member.domain_id=release_object.domain_id
     AND member.id=release_object.object_version_id
     AND member.member_id=release_object.object_id
     AND member.content_hash=release_object.content_hash
     AND member.status='CERTIFIED'
    WHERE release_object.object_type='MEMBER'
      AND (
        member.id IS NULL
        OR release_object.sensitivity IS DISTINCT FROM member.sensitivity
        OR NOT askdata.member_release_contract_is_safe(
          release_object.contract_json,member.dimension_version_id
        )
        OR CASE
          WHEN askdata.member_release_contract_is_safe(
            release_object.contract_json,member.dimension_version_id
          ) THEN EXISTS(
            SELECT 1
            FROM pg_catalog.jsonb_array_elements_text(
              release_object.contract_json->'aliasVersionIds'
            ) AS released_alias(alias_version_id)
            LEFT JOIN askdata.dimension_member_aliases AS alias
              ON alias.id::text=released_alias.alias_version_id
             AND alias.tenant_id=release_object.tenant_id
             AND alias.domain_id=release_object.domain_id
             AND alias.dimension_version_id=member.dimension_version_id
             AND alias.member_version_id=member.id
             AND alias.status='CERTIFIED'
            WHERE alias.id IS NULL
          )
          ELSE false
        END
      )
  ) THEN
    RAISE EXCEPTION 'existing MEMBER release contract is not label-free and alias-version-only'
      USING ERRCODE='23514';
  END IF;
END
$member_release_contract_backfill_guard$;

CREATE TRIGGER askdata_release_objects_validate_member_contract
BEFORE INSERT OR UPDATE OF
  tenant_id,domain_id,object_type,object_id,object_version_id,content_hash,
  sensitivity,contract_json
ON askdata.release_objects
FOR EACH ROW EXECUTE FUNCTION askdata.validate_member_release_contract();

-- Exact-only lookup deliberately has a single non-disclosing outcome shape:
-- wrong context, missing data, ambiguity, expired versions and denied access
-- all return zero rows. A successful lookup returns only opaque IDs/hash and
-- source content hashes; no canonical value, alias or evidence label exists
-- in the result contract.
CREATE OR REPLACE FUNCTION askdata.lookup_exact_dimension_member(
  selected_release_id uuid,
  selected_release_content_hash text,
  selected_dimension_version_id uuid,
  selected_lookup_key_hash text
)
RETURNS TABLE(
  member_version_id uuid,
  dimension_version_id uuid,
  member_content_hash text,
  dimension_content_hash text
)
LANGUAGE sql
STABLE
STRICT
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
  WITH request_context AS (
    SELECT
      askdata.current_tenant_id() AS tenant_id,
      askdata.current_actor_id() AS actor_id,
      askdata.current_domain_id() AS domain_id
  ),
  valid_context AS (
    SELECT context.tenant_id,context.actor_id,context.domain_id
    FROM request_context AS context
    JOIN platform.tenants AS tenant ON tenant.id=context.tenant_id
    JOIN platform.users AS actor
      ON actor.tenant_id=tenant.id
     AND actor.id=context.actor_id
     AND actor.status='ACTIVE'
     AND actor.deleted_at IS NULL
    JOIN platform.business_domains AS domain
      ON domain.tenant_id=tenant.id
     AND domain.id=context.domain_id
     AND domain.status='ACTIVE'
     AND domain.deleted_at IS NULL
    WHERE tenant.id=context.tenant_id
      AND tenant.status='ACTIVE'
      AND tenant.deleted_at IS NULL
      AND context.tenant_id IS NOT NULL
      AND context.actor_id IS NOT NULL
      AND context.domain_id IS NOT NULL
      AND NOT askdata.system_access()
      AND pg_catalog.current_setting('app.access_mode',true)='USER'
      AND platform.user_has_active_domain_membership(context.domain_id)
      AND selected_release_content_hash ~ '^[0-9a-f]{64}$'
      AND selected_lookup_key_hash ~ '^[0-9a-f]{64}$'
  ),
  pinned_dimension AS (
    SELECT
      dimension.id AS dimension_version_id,
      dimension.dimension_id,
      dimension.content_hash AS dimension_content_hash,
      dimension.sensitivity AS dimension_sensitivity,
      context.tenant_id,
      context.actor_id,
      context.domain_id
    FROM valid_context AS context
    JOIN askdata.releases AS release
      ON release.tenant_id=context.tenant_id
     AND release.domain_id=context.domain_id
     AND release.id=selected_release_id
     AND release.content_hash=selected_release_content_hash
     AND release.status IN ('READY','ACTIVE','SUPERSEDED')
    JOIN askdata.release_projections AS projection
      ON projection.tenant_id=release.tenant_id
     AND projection.domain_id=release.domain_id
     AND projection.release_id=release.id
     AND projection.target='POSTGRES_REGISTRY'
     AND projection.status='READY'
     AND projection.expected_content_hash=release.content_hash
     AND projection.applied_content_hash=release.content_hash
    JOIN askdata.dimensions AS dimension
      ON dimension.tenant_id=release.tenant_id
     AND dimension.domain_id=release.domain_id
     AND dimension.id=selected_dimension_version_id
     AND dimension.status='CERTIFIED'
     AND dimension.member_index_policy='EXACT_ONLY'
    JOIN askdata.release_objects AS dimension_release_object
      ON dimension_release_object.tenant_id=release.tenant_id
     AND dimension_release_object.domain_id=release.domain_id
     AND dimension_release_object.release_id=release.id
     AND dimension_release_object.object_type='DIMENSION'
     AND dimension_release_object.object_id=dimension.dimension_id
     AND dimension_release_object.object_version_id=dimension.id
     AND dimension_release_object.content_hash=dimension.content_hash
     AND dimension_release_object.sensitivity=dimension.sensitivity
  ),
  candidate_ids AS (
    SELECT member.id AS member_version_id
    FROM pinned_dimension AS dimension
    JOIN askdata.dimension_members AS member
      ON member.tenant_id=dimension.tenant_id
     AND member.domain_id=dimension.domain_id
     AND member.dimension_version_id=dimension.dimension_version_id
     AND member.member_key_hash=selected_lookup_key_hash
    UNION
    SELECT alias.member_version_id
    FROM pinned_dimension AS dimension
    JOIN askdata.dimension_member_aliases AS alias
      ON alias.tenant_id=dimension.tenant_id
     AND alias.domain_id=dimension.domain_id
     AND alias.dimension_version_id=dimension.dimension_version_id
     AND alias.alias_key_hash=selected_lookup_key_hash
     AND alias.status='CERTIFIED'
    JOIN askdata.dimension_members AS member
      ON member.tenant_id=alias.tenant_id
     AND member.domain_id=alias.domain_id
     AND member.dimension_version_id=alias.dimension_version_id
     AND member.id=alias.member_version_id
    JOIN askdata.release_objects AS member_release_object
      ON member_release_object.tenant_id=member.tenant_id
     AND member_release_object.domain_id=member.domain_id
     AND member_release_object.release_id=selected_release_id
     AND member_release_object.object_type='MEMBER'
     AND member_release_object.object_id=member.member_id
     AND member_release_object.object_version_id=member.id
     AND member_release_object.content_hash=member.content_hash
     AND member_release_object.sensitivity=member.sensitivity
     AND EXISTS(
       SELECT 1
       FROM pg_catalog.jsonb_array_elements_text(
         CASE
           WHEN pg_catalog.jsonb_typeof(
             member_release_object.contract_json->'aliasVersionIds'
           )='array'
            THEN member_release_object.contract_json->'aliasVersionIds'
           ELSE '[]'::jsonb
         END
       ) AS released_alias(alias_version_id)
       WHERE released_alias.alias_version_id=alias.id::text
     )
  ),
  eligible_candidates AS (
    SELECT
      member.id AS member_version_id,
      member.dimension_version_id,
      member.content_hash AS member_content_hash,
      member.sensitivity AS effective_sensitivity,
      dimension.dimension_id,
      dimension.dimension_content_hash,
      dimension.tenant_id,
      dimension.actor_id
    FROM candidate_ids AS candidate
    JOIN pinned_dimension AS dimension ON true
    JOIN askdata.dimension_members AS member
      ON member.tenant_id=dimension.tenant_id
     AND member.domain_id=dimension.domain_id
     AND member.dimension_version_id=dimension.dimension_version_id
     AND member.id=candidate.member_version_id
     AND member.status='CERTIFIED'
     AND member.valid_from<=pg_catalog.transaction_timestamp()
     AND (member.valid_to IS NULL OR pg_catalog.transaction_timestamp()<member.valid_to)
     AND (CASE member.sensitivity
            WHEN 'PUBLIC' THEN 1
            WHEN 'INTERNAL' THEN 2
            WHEN 'CONFIDENTIAL' THEN 3
            WHEN 'RESTRICTED' THEN 4
            ELSE 0
          END)
         >= (CASE dimension.dimension_sensitivity
               WHEN 'PUBLIC' THEN 1
               WHEN 'INTERNAL' THEN 2
               WHEN 'CONFIDENTIAL' THEN 3
               WHEN 'RESTRICTED' THEN 4
               ELSE 5
             END)
    JOIN askdata.release_objects AS member_release_object
      ON member_release_object.tenant_id=member.tenant_id
     AND member_release_object.domain_id=member.domain_id
     AND member_release_object.release_id=selected_release_id
     AND member_release_object.object_type='MEMBER'
     AND member_release_object.object_id=member.member_id
     AND member_release_object.object_version_id=member.id
     AND member_release_object.content_hash=member.content_hash
     AND member_release_object.sensitivity=member.sensitivity
  ),
  unique_candidate AS (
    SELECT candidate.*
    FROM eligible_candidates AS candidate
    WHERE (SELECT pg_catalog.count(*) FROM eligible_candidates)=1
  )
  SELECT
    candidate.member_version_id,
    candidate.dimension_version_id,
    candidate.member_content_hash,
    candidate.dimension_content_hash
  FROM unique_candidate AS candidate
  WHERE candidate.effective_sensitivity IN ('PUBLIC','INTERNAL')
     OR EXISTS(
       SELECT 1
       FROM platform.object_permissions AS permission
       WHERE permission.tenant_id=candidate.tenant_id
         AND permission.object_type='ASKDATA_DIMENSION'
         AND permission.object_id=candidate.dimension_id
         AND permission.action=CASE candidate.effective_sensitivity
           WHEN 'CONFIDENTIAL' THEN 'LOOKUP_CONFIDENTIAL_MEMBER'
           WHEN 'RESTRICTED' THEN 'LOOKUP_RESTRICTED_MEMBER'
           ELSE ''
         END
         AND (
           (
             permission.subject_type='USER'
             AND permission.subject_id=candidate.actor_id
           )
           OR (
             permission.subject_type='ROLE'
             AND EXISTS(
               SELECT 1
               FROM platform.user_roles AS assignment
               JOIN platform.roles AS role
                 ON role.tenant_id=assignment.tenant_id
                AND role.id=assignment.role_id
                AND role.status='ACTIVE'
                AND role.deleted_at IS NULL
               WHERE assignment.tenant_id=candidate.tenant_id
                 AND assignment.user_id=candidate.actor_id
                 AND assignment.role_id=permission.subject_id
             )
           )
         )
     )
$$;

REVOKE ALL ON FUNCTION askdata.lookup_exact_dimension_member(
  uuid,text,uuid,text
) FROM PUBLIC;

COMMENT ON COLUMN askdata.dimension_member_aliases.alias_key_hash IS
  'DB-stamped SHA-256 of dimension version UUID, NUL, and normalized alias; runtime roles must not read this dictionary-attackable key directly';
COMMENT ON FUNCTION askdata.lookup_exact_dimension_member(
  uuid,text,uuid,text
) IS
  'Release-pinned EXACT_ONLY member lookup. Returns zero rows for missing, ambiguous, expired or unauthorized values and never returns a member label or alias';
