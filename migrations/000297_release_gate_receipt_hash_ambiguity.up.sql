-- The PL/pgSQL local variable `receipt_hash` collides with the immutable
-- receipt table column of the same name. Repair only the three variable uses;
-- keep the INSERT target column unchanged.
DO $$
DECLARE
  definition text;
  repaired text;
BEGIN
  definition := pg_get_functiondef(
    'askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid)'::regprocedure
  );
  IF position('DECLARE receipt_hash text;' IN definition)=0
    OR position('receipt_hash := encode' IN definition)=0
    OR position('facts_hash,receipt_hash,selected_actor_id' IN definition)=0
    OR position('cardinality(failures)=0,receipt_hash,failures,facts' IN definition)=0 THEN
    RAISE EXCEPTION 'release evaluation gate definition is not the expected pre-repair version';
  END IF;
  repaired := replace(definition,
    'DECLARE receipt_hash text;',
    'DECLARE computed_receipt_hash text;');
  repaired := replace(repaired,
    'receipt_hash := encode',
    'computed_receipt_hash := encode');
  repaired := replace(repaired,
    'facts_hash,receipt_hash,selected_actor_id',
    'facts_hash,computed_receipt_hash,selected_actor_id');
  repaired := replace(repaired,
    'cardinality(failures)=0,receipt_hash,failures,facts',
    'cardinality(failures)=0,computed_receipt_hash,failures,facts');
  EXECUTE repaired;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid)
FROM PUBLIC;
