BEGIN;

UPDATE platform.dimension_where_decisions
SET dimension_member_id=NULL
WHERE source_type='QUERY_OBSERVED'
  AND selected_member_count=1;

COMMIT;
