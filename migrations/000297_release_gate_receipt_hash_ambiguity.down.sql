DO $$
DECLARE
  definition text;
  restored text;
BEGIN
  definition := pg_get_functiondef(
    'askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid)'::regprocedure
  );
  IF position('DECLARE computed_receipt_hash text;' IN definition)=0 THEN
    RAISE EXCEPTION 'release evaluation gate definition is not the expected repaired version';
  END IF;
  restored := replace(definition,
    'DECLARE computed_receipt_hash text;',
    'DECLARE receipt_hash text;');
  restored := replace(restored,
    'computed_receipt_hash := encode',
    'receipt_hash := encode');
  restored := replace(restored,
    'facts_hash,computed_receipt_hash,selected_actor_id',
    'facts_hash,receipt_hash,selected_actor_id');
  restored := replace(restored,
    'cardinality(failures)=0,computed_receipt_hash,failures,facts',
    'cardinality(failures)=0,receipt_hash,failures,facts');
  EXECUTE restored;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid)
FROM PUBLIC;
