BEGIN;

ALTER TABLE platform.warehouse_dag_change_operations
  DROP CONSTRAINT warehouse_dag_change_operations_path_check;
ALTER TABLE platform.warehouse_dag_change_operations
  ADD CONSTRAINT warehouse_dag_change_operations_path_check CHECK(
    length(path) BETWEEN 1 AND 512
    AND path ~ '^/(dataset|nodes|joins|preAggregations|factContract|analysisContract|fields|filters|groupBy|having|sorts|parameters|outputGrain|executionPolicy|designer)(/.*)?$'
    AND path !~ '[[:cntrl:]]'
  );

COMMIT;
