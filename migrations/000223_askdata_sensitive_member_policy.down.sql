DROP FUNCTION IF EXISTS askdata.lookup_exact_dimension_member(
  uuid,text,uuid,text
);

DROP TRIGGER IF EXISTS askdata_release_objects_validate_member_contract
  ON askdata.release_objects;
DROP FUNCTION IF EXISTS askdata.validate_member_release_contract();
DROP FUNCTION IF EXISTS askdata.member_release_contract_is_safe(jsonb,uuid);
DROP TRIGGER IF EXISTS askdata_release_objects_00_validate_domain_identity
  ON askdata.release_objects;
DROP FUNCTION IF EXISTS askdata.validate_release_domain_object_identity();

ALTER FUNCTION askdata.validate_release_object()
  SECURITY INVOKER
  RESET search_path;
ALTER FUNCTION askdata.validate_member_dependency()
  SECURITY INVOKER
  RESET search_path;
ALTER FUNCTION askdata.validate_search_document_subject()
  SECURITY INVOKER
  RESET search_path;

DROP TRIGGER IF EXISTS askdata_dimensions_enforce_member_sensitivity_floor
  ON askdata.dimensions;
DROP TRIGGER IF EXISTS askdata_dimension_members_enforce_sensitivity_floor
  ON askdata.dimension_members;
DROP TRIGGER IF EXISTS askdata_dimension_member_aliases_stamp_key_hash
  ON askdata.dimension_member_aliases;

DROP FUNCTION IF EXISTS askdata.enforce_dimension_sensitivity_floor();
DROP FUNCTION IF EXISTS askdata.enforce_dimension_member_sensitivity_floor();
DROP FUNCTION IF EXISTS askdata.stamp_dimension_member_alias_key_hash();

DROP INDEX IF EXISTS askdata.askdata_dimension_member_aliases_hash_lookup_idx;

ALTER TABLE askdata.dimension_member_aliases
  DROP CONSTRAINT IF EXISTS askdata_dimension_member_aliases_key_hash_check,
  DROP COLUMN IF EXISTS alias_key_hash;
