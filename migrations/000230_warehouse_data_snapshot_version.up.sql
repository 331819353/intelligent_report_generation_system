-- Separate the stable, schema-bound materialization identity from refresh
-- snapshots. A same-schema refresh updates the stable ACTIVE pointer while
-- preserving every refresh as an append-only snapshot fact.
CREATE TABLE platform.materialization_snapshots(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  materialization_id uuid NOT NULL,
  build_run_id uuid NOT NULL,
  schema_hash text NOT NULL CHECK(schema_hash ~ '^[0-9a-f]{64}$'),
  snapshot_version text NOT NULL CHECK(
    length(snapshot_version) BETWEEN 1 AND 128
    AND snapshot_version=btrim(snapshot_version)
    AND snapshot_version !~ '[[:cntrl:]]'
  ),
  snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
  physical_schema text NOT NULL CHECK(
    physical_schema IN (
      'warehouse_ods','warehouse_dim','warehouse_dwd','warehouse_dws','warehouse_ads'
    )
  ),
  physical_name text NOT NULL CHECK(physical_name ~ '^[a-z][a-z0-9_]{0,62}$'),
  snapshot_started_at timestamptz NOT NULL,
  snapshot_completed_at timestamptz,
  data_available_through timestamptz,
  row_count bigint CHECK(row_count>=0),
  size_bytes bigint CHECK(size_bytes>=0),
  quality_status text NOT NULL CHECK(quality_status IN ('OK','WARN','FAIL')),
  CONSTRAINT materialization_snapshots_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT materialization_snapshots_build_run_fk
    FOREIGN KEY(build_run_id,tenant_id)
    REFERENCES platform.dataset_build_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT materialization_snapshots_materialization_version_key
    UNIQUE(materialization_id,snapshot_version),
  CONSTRAINT materialization_snapshots_build_run_key
    UNIQUE(tenant_id,build_run_id),
  CONSTRAINT materialization_snapshots_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT materialization_snapshots_completion_shape_check CHECK(
    (snapshot_completed_at IS NULL
      AND data_available_through IS NULL
      AND row_count IS NULL
      AND size_bytes IS NULL
      AND quality_status='WARN')
    OR
    (snapshot_completed_at IS NOT NULL
      AND snapshot_completed_at>=snapshot_started_at
      AND (
        (quality_status IN ('OK','WARN')
          AND row_count IS NOT NULL AND size_bytes IS NOT NULL)
        OR quality_status='FAIL'
      ))
  )
);

CREATE INDEX materialization_snapshots_latest_idx
  ON platform.materialization_snapshots(
    tenant_id,materialization_id,snapshot_completed_at DESC,id DESC
  );
CREATE INDEX materialization_snapshots_completed_idx
  ON platform.materialization_snapshots(
    tenant_id,snapshot_completed_at DESC,id DESC
  ) WHERE snapshot_completed_at IS NOT NULL;

-- Existing materializations become the first snapshot of their stable
-- materialization identity. BUILDING rows remain incomplete; terminal FAILED
-- rows are completed failures and are ignored by latest-success reads.
INSERT INTO platform.materialization_snapshots(
  tenant_id,materialization_id,build_run_id,schema_hash,snapshot_version,
  snapshot_hash,physical_schema,physical_name,snapshot_started_at,
  snapshot_completed_at,data_available_through,row_count,size_bytes,quality_status
)
SELECT
  materialization.tenant_id,materialization.id,materialization.build_run_id,
  materialization.schema_hash,materialization.build_run_id::text,
  materialization.snapshot_hash,materialization.physical_schema,
  materialization.physical_name,
  COALESCE(run.started_at,run.created_at,materialization.created_at),
  CASE
    WHEN materialization.status IN ('ACTIVE','RETIRED')
      THEN materialization.activated_at
    WHEN materialization.status='FAILED'
      THEN COALESCE(run.completed_at,materialization.created_at)
    ELSE NULL
  END,
  NULL,
  CASE WHEN materialization.status IN ('ACTIVE','RETIRED')
    THEN materialization.row_count ELSE NULL END,
  CASE WHEN materialization.status IN ('ACTIVE','RETIRED')
    THEN materialization.size_bytes ELSE NULL END,
  CASE
    WHEN materialization.status IN ('ACTIVE','RETIRED') THEN 'OK'
    WHEN materialization.status='FAILED' THEN 'FAIL'
    ELSE 'WARN'
  END
FROM platform.dataset_materializations AS materialization
JOIN platform.dataset_build_runs AS run
  ON run.id=materialization.build_run_id
 AND run.tenant_id=materialization.tenant_id;

-- A stable materialization can serve quality observations from many build
-- runs, so the materialization and build-run identities are validated by
-- their independent tenant-safe foreign keys instead of one mutable pair.
ALTER TABLE platform.data_quality_results
  DROP CONSTRAINT data_quality_results_materialization_fk,
  ADD CONSTRAINT data_quality_results_materialization_fk
    FOREIGN KEY(materialization_id,tenant_id)
    REFERENCES platform.dataset_materializations(id,tenant_id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION platform.enforce_materialization_snapshot_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '物化快照不可删除' USING ERRCODE='23514';
  END IF;
  IF OLD.snapshot_completed_at IS NOT NULL THEN
    RAISE EXCEPTION '已完成物化快照不可修改' USING ERRCODE='23514';
  END IF;
  IF ROW(
    NEW.id,NEW.tenant_id,NEW.materialization_id,NEW.build_run_id,
    NEW.schema_hash,NEW.snapshot_version,NEW.physical_schema,
    NEW.physical_name,NEW.snapshot_started_at
  ) IS DISTINCT FROM ROW(
    OLD.id,OLD.tenant_id,OLD.materialization_id,OLD.build_run_id,
    OLD.schema_hash,OLD.snapshot_version,OLD.physical_schema,
    OLD.physical_name,OLD.snapshot_started_at
  ) THEN
    RAISE EXCEPTION '物化快照身份和起始事实不可修改' USING ERRCODE='23514';
  END IF;
  IF NEW.snapshot_completed_at IS NULL THEN
    RAISE EXCEPTION '物化快照只允许从进行中转换为完成' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_materialization_snapshot_transition()
  FROM PUBLIC;

CREATE TRIGGER materialization_snapshots_immutable
BEFORE UPDATE OR DELETE ON platform.materialization_snapshots
FOR EACH ROW EXECUTE FUNCTION
  platform.enforce_materialization_snapshot_transition();

-- NOTIFY is an active invalidation hint for QUERY-010. Correctness never
-- depends on delivery because snapshot_version also participates in cache keys.
CREATE OR REPLACE FUNCTION platform.notify_materialization_snapshot_completed()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF OLD.snapshot_completed_at IS NULL AND NEW.snapshot_completed_at IS NOT NULL THEN
    PERFORM pg_notify(
      'materialization_snapshot_completed',
      json_build_object(
        'tenantId',NEW.tenant_id,
        'materializationId',NEW.materialization_id,
        'snapshotVersion',NEW.snapshot_version,
        'qualityStatus',NEW.quality_status
      )::text
    );
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.notify_materialization_snapshot_completed()
  FROM PUBLIC;

CREATE TRIGGER materialization_snapshots_notify_completion
AFTER UPDATE OF snapshot_completed_at ON platform.materialization_snapshots
FOR EACH ROW EXECUTE FUNCTION
  platform.notify_materialization_snapshot_completed();

-- The original table is now the current stable pointer for one schema. Same
-- schema refreshes may replace only build/snapshot/physical facts; schema
-- changes still require BUILDING -> ACTIVE and retire the prior identity.
CREATE OR REPLACE FUNCTION platform.enforce_materialization_transition()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '物化记录不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.layer IS DISTINCT FROM OLD.layer
    OR NEW.relation_kind IS DISTINCT FROM OLD.relation_kind
    OR NEW.published_schema IS DISTINCT FROM OLD.published_schema
    OR NEW.published_name IS DISTINCT FROM OLD.published_name
    OR NEW.schema_hash IS DISTINCT FROM OLD.schema_hash
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '物化稳定身份、发布位置和 schema 不可修改'
      USING ERRCODE='23514';
  END IF;
  IF OLD.status='BUILDING' AND NEW.status IN ('ACTIVE','FAILED') THEN
    IF NEW.build_run_id IS DISTINCT FROM OLD.build_run_id
      OR NEW.refresh_mode IS DISTINCT FROM OLD.refresh_mode
      OR NEW.physical_schema IS DISTINCT FROM OLD.physical_schema
      OR NEW.physical_name IS DISTINCT FROM OLD.physical_name THEN
      RAISE EXCEPTION '构建中物化的运行和物理身份不可修改'
        USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='ACTIVE' AND NEW.status='RETIRED' THEN
    IF ROW(
      NEW.build_run_id,NEW.refresh_mode,NEW.physical_schema,NEW.physical_name,
      NEW.snapshot_hash,NEW.row_count,NEW.size_bytes,NEW.watermark_json,
      NEW.activated_at
    ) IS DISTINCT FROM ROW(
      OLD.build_run_id,OLD.refresh_mode,OLD.physical_schema,OLD.physical_name,
      OLD.snapshot_hash,OLD.row_count,OLD.size_bytes,OLD.watermark_json,
      OLD.activated_at
    ) THEN
      RAISE EXCEPTION '退役只能修改物化状态和退役时间'
        USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='ACTIVE' AND NEW.status='ACTIVE' THEN
    IF NEW.retired_at IS NOT NULL
      OR NEW.activated_at IS NULL
      OR NEW.row_count IS NULL
      OR NEW.size_bytes IS NULL
      OR NOT EXISTS(
        SELECT 1
        FROM platform.dataset_build_runs AS run
        JOIN platform.materialization_snapshots AS snapshot
          ON snapshot.build_run_id=run.id
         AND snapshot.tenant_id=run.tenant_id
         AND snapshot.materialization_id=OLD.id
         AND snapshot.schema_hash=OLD.schema_hash
         AND snapshot.snapshot_completed_at IS NULL
        WHERE run.id=NEW.build_run_id
          AND run.tenant_id=NEW.tenant_id
          AND run.dataset_id=NEW.dataset_id
          AND run.dataset_version_id=NEW.dataset_version_id
          AND run.layer=NEW.layer
          AND run.run_mode=NEW.refresh_mode
          AND run.status='RUNNING'
      ) THEN
      RAISE EXCEPTION '同 schema 刷新缺少匹配的进行中快照或运行'
        USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '非法的物化状态转换：% -> %',OLD.status,NEW.status
    USING ERRCODE='23514';
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_materialization_transition() FROM PUBLIC;

ALTER TABLE platform.materialization_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.materialization_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY materialization_snapshots_tenant_isolation
  ON platform.materialization_snapshots
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.materialization_snapshots IS
  'append-only refresh snapshots for one stable schema-bound materialization identity';
COMMENT ON COLUMN platform.materialization_snapshots.snapshot_version IS
  'refresh batch identity; changes invalidate result caches but never stale a release';
COMMENT ON COLUMN platform.materialization_snapshots.data_available_through IS
  'warehouse-computed maximum business time watermark; never derived by scanning facts on reads';
