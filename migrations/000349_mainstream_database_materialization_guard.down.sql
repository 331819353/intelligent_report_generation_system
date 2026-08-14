-- The preceding definition is retained on rollback. Migration 000348 prevents
-- legacy application versions from creating rows for the additional database
-- types, so the broader non-Excel predicate is behaviorally equivalent.
SELECT 1;
