-- 000249 replaced this trigger function to add REPORT_ASSET support and
-- unintentionally reset the SECURITY DEFINER and fixed search_path attributes
-- established by 000223.
ALTER FUNCTION askdata.validate_search_document_subject()
  SECURITY DEFINER
  SET search_path=pg_catalog,askdata,platform;
REVOKE ALL ON FUNCTION askdata.validate_search_document_subject() FROM PUBLIC;
