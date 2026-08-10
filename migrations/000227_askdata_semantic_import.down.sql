DROP FUNCTION IF EXISTS askdata.heartbeat_semantic_import(uuid,uuid,text,uuid,integer);
DROP FUNCTION IF EXISTS askdata.claim_semantic_import(uuid,text,integer);
DROP FUNCTION IF EXISTS askdata.list_semantic_import_tenants();
DROP TRIGGER IF EXISTS askdata_semantic_import_rows_transition_guard
  ON askdata.semantic_import_rows;
DROP TRIGGER IF EXISTS askdata_semantic_imports_transition_guard
  ON askdata.semantic_imports;
DROP FUNCTION IF EXISTS askdata.enforce_semantic_import_row_transition();
DROP FUNCTION IF EXISTS askdata.enforce_semantic_import_transition();
DROP TABLE IF EXISTS askdata.semantic_import_rows;
DROP TABLE IF EXISTS askdata.semantic_imports;
DROP FUNCTION IF EXISTS askdata.semantic_import_errors_valid(jsonb);
