-- SEM-CTX-001: governed business context on the objects that already exist.
--
-- 02 §5 lists four kinds of business supplementary information. Two of them
-- were already delivered and are deliberately NOT rebuilt here:
--
--   * enterprise jargon and abbreviations  -> askdata.business_terms already
--     carries term type, match mode, priority, negative contexts, applicable
--     roles and a validity window, and SEM-DICT-WIRE-001 wired it into query
--     time as a deterministic exact hit.
--   * metric default filters -> metric_versions.default_filters_ast is a real
--     governed Filter AST and IS applied at compile time by the semantic
--     compiler's AST translator, with a boolean-root check.
--
-- Organisation mapping is likewise already expressible: dimension members carry
-- parent_member_version_id, and askdata.hierarchies/hierarchy_levels model the
-- level structure. What was genuinely missing is the prose that says what a
-- metric or a dimension value MEANS in business terms - the part a person needs
-- in order to trust an answer, and the part retrieval and the LLM need as
-- context evidence.
--
-- Both columns are plain governed text on the existing versioned rows rather
-- than a new object family: caliber belongs to the metric version whose caliber
-- it is, and a value definition belongs to the member it defines. Splitting
-- them out would create objects that can drift from the thing they describe.
--
-- Neither participates in binding. They are retrieval documents and LLM context
-- evidence only, exactly as 02 §5 requires.
BEGIN;

ALTER TABLE askdata.metric_versions
  ADD COLUMN business_definition text NOT NULL DEFAULT ''
    CHECK(length(business_definition)<=4000 AND business_definition !~ '[[:cntrl:]]');

COMMENT ON COLUMN askdata.metric_versions.business_definition IS
  'Business caliber of this metric version in prose: what it counts, what it excludes and when it is the wrong metric. Retrieval and LLM context evidence only; never a binding fact';

ALTER TABLE askdata.dimension_members
  ADD COLUMN definition text NOT NULL DEFAULT ''
    CHECK(length(definition)<=2000 AND definition !~ '[[:cntrl:]]');

COMMENT ON COLUMN askdata.dimension_members.definition IS
  'What this dimension value means in business terms. Inherits the member sensitivity: a CONFIDENTIAL or RESTRICTED member never exposes its definition to embeddings, the LLM or logs';

COMMIT;
