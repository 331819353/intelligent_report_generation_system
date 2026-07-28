ALTER TABLE platform.ai_requests
  DROP CONSTRAINT ai_requests_purpose_check;

ALTER TABLE platform.ai_requests
  ADD CONSTRAINT ai_requests_purpose_check CHECK(purpose IN (
    'METADATA_COMPLETION',
    'REPORT_GENERATION',
    'BLOCK_EDIT',
    'CONCLUSION_GENERATION',
    'DATASET_DAG_GENERATION',
    'METRIC_AUTHORING',
    'DATASET_TAG_SUGGESTION',
    'SEMANTIC_QUERY_PLANNING'
  ));

-- 人力资源受控词条可能已经被人工批准；降级不删除治理资产或绑定。
