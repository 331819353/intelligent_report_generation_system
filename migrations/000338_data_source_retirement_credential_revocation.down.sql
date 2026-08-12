BEGIN;

CREATE OR REPLACE FUNCTION platform.reject_data_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'data source versions are immutable';
END
$$;

REVOKE ALL ON FUNCTION platform.reject_data_source_version_mutation() FROM PUBLIC;

COMMIT;
