BEGIN;

UPDATE platform.dimension_where_design_policies
SET status='FAILED',attempt=3,error_code='RETRY_BUDGET_REDUCED',
    predicate_operator='',llm_model='',llm_reason='',confidence=NULL,
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE attempt>3 OR (
  status='PENDING' AND attempt=0 AND created_at<updated_at
);

ALTER TABLE platform.dimension_where_design_policies
  DROP CONSTRAINT dimension_where_design_policies_attempt_check;

ALTER TABLE platform.dimension_where_design_policies
  ADD CONSTRAINT dimension_where_design_policies_attempt_check
  CHECK(attempt BETWEEN 0 AND 3);

COMMIT;
