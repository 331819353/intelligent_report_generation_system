-- SEM-CTX-001 (final gap): business-level row access policies.
--
-- The platform already trims by permission before retrieval, before binding and
-- before execution, and every control table carries RLS. What none of that can
-- express is a BUSINESS rule about rows - "a sales rep sees only their own
-- region" - because the platform has no idea which region a person owns.
--
-- Design decisions, and why:
--
--   * A policy is a predicate in the EXISTING semantic AST, not a new language.
--     The compiler already translates that AST for metric default filters, with
--     a boolean-root check and parameter binding, so a policy inherits a
--     translator that is already exercised in production rather than a second
--     one written for this feature.
--
--   * A policy must reference at least one subject attribute. A predicate with
--     no subject reference is a constant filter, which is what a metric default
--     filter already is; allowing it here would let something be called row
--     access control while granting everyone the same rows.
--
--   * There is deliberately no "allow when unmatched" option. If the reader has
--     no value for an attribute the policy references, the policy denies every
--     row. A row access control with an open failure mode is not a control, and
--     offering the choice would eventually see it chosen.
--
-- Subject attribute VALUES are per-reader runtime facts, so they live in
-- platform and are never part of a Release. The policy - which is a governed
-- business rule - is versioned, content-hashed, certified and released like any
-- other semantic object.
BEGIN;

-- Who the reader is, in business terms. Administered, never self-asserted, and
-- never accepted from a request body.
CREATE TABLE platform.subject_attributes(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  user_id uuid NOT NULL,
  attribute_key text NOT NULL CHECK(attribute_key ~ '^[a-z][a-z0-9_]{0,63}$'),
  attribute_values text[] NOT NULL CHECK(
    cardinality(attribute_values) BETWEEN 1 AND 256
    AND array_position(attribute_values,NULL) IS NULL
  ),
  granted_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT platform_subject_attributes_identity_key UNIQUE(tenant_id,user_id,attribute_key),
  CONSTRAINT platform_subject_attributes_user_fk
    FOREIGN KEY(user_id,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT platform_subject_attributes_granted_by_fk
    FOREIGN KEY(granted_by,tenant_id) REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX platform_subject_attributes_key_idx
  ON platform.subject_attributes(tenant_id,attribute_key,user_id);

CREATE TRIGGER platform_subject_attributes_set_updated_at
BEFORE UPDATE ON platform.subject_attributes
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.subject_attributes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.subject_attributes FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_subject_attributes_tenant_isolation
  ON platform.subject_attributes
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.subject_attributes IS
  'Administered business attributes of a reader (region, cost centre, ...) consumed by governed row access policies; never self-asserted and never part of a Release';

-- The governed business rule.
CREATE TABLE askdata.row_access_policies(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  policy_id uuid NOT NULL,
  version_no integer NOT NULL CHECK(version_no>0),
  model_version_id uuid NOT NULL,
  code citext NOT NULL,
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
  predicate_ast jsonb NOT NULL CHECK(
    jsonb_typeof(predicate_ast)='object'
    AND pg_column_size(predicate_ast)<=65536
    AND askdata.json_is_safe(predicate_ast)
  ),
  -- Denormalised from the predicate so coverage can be reported without
  -- parsing the AST in SQL. The Go validator keeps the two in step.
  subject_attribute_keys text[] NOT NULL CHECK(
    cardinality(subject_attribute_keys) BETWEEN 1 AND 16
    AND array_position(subject_attribute_keys,NULL) IS NULL
  ),
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','CERTIFIED','DEPRECATED')),
  content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_row_access_policies_identity_key UNIQUE(tenant_id,policy_id,version_no),
  CONSTRAINT askdata_row_access_policies_code_version_key UNIQUE(tenant_id,domain_id,code,version_no),
  CONSTRAINT askdata_row_access_policies_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_row_access_policies_identity_domain_tenant_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_row_access_policies_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_row_access_policies_model_fk
    FOREIGN KEY(model_version_id,domain_id,tenant_id)
    REFERENCES askdata.semantic_models(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_row_access_policies_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX askdata_row_access_policies_model_idx
  ON askdata.row_access_policies(tenant_id,domain_id,model_version_id,status);

ALTER TABLE askdata.row_access_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.row_access_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_row_access_policies_domain_isolation
  ON askdata.row_access_policies
  USING(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id))
  WITH CHECK(askdata.tenant_matches(tenant_id) AND askdata.domain_can_access(domain_id));

COMMENT ON TABLE askdata.row_access_policies IS
  'Versioned governed row access predicates over a semantic model, expressed in the semantic AST and resolved against the reading actor subject attributes; a reader with no value for a referenced attribute is denied every row';

-- Resolving the reader's own attributes. The actor is checked against the
-- session actor so this can never be used to look up somebody else's scope, and
-- it is the only path the compiler uses.
CREATE OR REPLACE FUNCTION askdata.resolve_subject_attributes(
  selected_tenant_id uuid, selected_actor_id uuid
)
RETURNS TABLE(attribute_key text, attribute_values text[])
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR NOT askdata.tenant_matches(selected_tenant_id) THEN
    RETURN;
  END IF;
  RETURN QUERY
    SELECT attribute.attribute_key,attribute.attribute_values
    FROM platform.subject_attributes AS attribute
    WHERE attribute.tenant_id=selected_tenant_id
      AND attribute.user_id=selected_actor_id
    ORDER BY attribute.attribute_key;
END
$$;

-- Operational coverage. A certified policy whose attribute nobody has been
-- granted denies every row to everyone, which is correct but silent; this makes
-- it visible before the model becomes unqueryable.
CREATE OR REPLACE FUNCTION askdata.row_access_policy_coverage(
  selected_tenant_id uuid, selected_domain_id uuid
)
RETURNS TABLE(
  attribute_key text, policy_count bigint,
  member_count bigint, covered_member_count bigint
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id) THEN
    RETURN;
  END IF;
  RETURN QUERY
    WITH referenced AS(
      SELECT DISTINCT unnest(policy.subject_attribute_keys) AS attribute_key,
        policy.policy_id
      FROM askdata.row_access_policies AS policy
      WHERE policy.tenant_id=selected_tenant_id AND policy.domain_id=selected_domain_id
        AND policy.status='CERTIFIED'
    ), members AS(
      SELECT membership.user_id
      FROM platform.domain_memberships AS membership
      JOIN platform.users AS member
        ON member.id=membership.user_id AND member.tenant_id=membership.tenant_id
      WHERE membership.tenant_id=selected_tenant_id
        AND membership.domain_id=selected_domain_id
        AND membership.status='ACTIVE' AND member.status='ACTIVE'
        AND member.deleted_at IS NULL
    )
    SELECT referenced.attribute_key,
      count(DISTINCT referenced.policy_id),
      (SELECT count(*) FROM members),
      (SELECT count(*) FROM members
        WHERE EXISTS(
          SELECT 1 FROM platform.subject_attributes AS attribute
          WHERE attribute.tenant_id=selected_tenant_id
            AND attribute.user_id=members.user_id
            AND attribute.attribute_key=referenced.attribute_key
        ))
    FROM referenced
    GROUP BY referenced.attribute_key
    ORDER BY referenced.attribute_key;
END
$$;

-- Extends the 000228 list; every existing type is preserved verbatim.
ALTER TABLE askdata.release_objects
  DROP CONSTRAINT release_objects_object_type_check,
  ADD CONSTRAINT release_objects_object_type_check CHECK(object_type IN (
    'DOMAIN','ENTITY','SEMANTIC_MODEL','MEASURE','METRIC','METRIC_DIMENSION',
    'DIMENSION','MEMBER','HIERARCHY','RELATIONSHIP','QUALITY_RULE','BUSINESS_TERM',
    'CERTIFIED_EXAMPLE','TIME_CONTRACT','KPI_BUNDLE','EVAL_CASE','ROW_ACCESS_POLICY'
  ));

REVOKE ALL ON FUNCTION
  askdata.resolve_subject_attributes(uuid,uuid),
  askdata.row_access_policy_coverage(uuid,uuid)
FROM PUBLIC;
REVOKE ALL ON TABLE platform.subject_attributes,askdata.row_access_policies FROM PUBLIC;

COMMIT;
