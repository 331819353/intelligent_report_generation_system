DROP FUNCTION IF EXISTS platform.fail_semantic_runtime_projection(
  uuid,uuid,text,uuid,text,jsonb
);
DROP FUNCTION IF EXISTS platform.complete_semantic_runtime_projection(
  uuid,uuid,text,uuid,text,text,integer,jsonb
);
DROP FUNCTION IF EXISTS platform.claim_semantic_runtime_projection(uuid,text,integer);
DROP FUNCTION IF EXISTS platform.list_semantic_runtime_projection_tenants();
DROP TABLE IF EXISTS platform.semantic_release_search_documents;
DROP TABLE IF EXISTS platform.semantic_execution_registry;
