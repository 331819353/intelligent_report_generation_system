-- Durable release references and read-only retention. PostgreSQL remains the
-- authority for lifecycle transitions so API, worker and future report/saved
-- question callers cannot race a release retirement.

ALTER TABLE askdata.releases
  DROP CONSTRAINT releases_status_check,
  DROP CONSTRAINT askdata_releases_ready_shape_check,
  DROP CONSTRAINT askdata_releases_activation_shape_check;

ALTER TABLE askdata.releases
  ADD COLUMN retained_at timestamptz,
  ADD COLUMN retention_until timestamptz,
  ADD COLUMN retired_at timestamptz,
  ADD CONSTRAINT askdata_releases_status_check CHECK(status IN (
    'DRAFT','VALIDATING','PROJECTING','READY','ACTIVE','BLOCKED',
    'SUPERSEDED','RETAINED','RETIRED'
  )),
  ADD CONSTRAINT askdata_releases_ready_shape_check CHECK(
    (status IN ('READY','ACTIVE','SUPERSEDED','RETAINED','RETIRED') AND ready_at IS NOT NULL)
    OR status NOT IN ('READY','ACTIVE','SUPERSEDED','RETAINED','RETIRED')
  ),
  ADD CONSTRAINT askdata_releases_activation_shape_check CHECK(
    (status IN ('ACTIVE','SUPERSEDED','RETAINED','RETIRED')
      AND activated_by IS NOT NULL AND activated_at IS NOT NULL)
    OR status NOT IN ('ACTIVE','SUPERSEDED','RETAINED','RETIRED')
  ),
  ADD CONSTRAINT askdata_releases_retention_shape_check CHECK(
    (
      status='RETAINED'
      AND retained_at IS NOT NULL
      AND retention_until IS NOT NULL
      AND retention_until>retained_at
      AND retired_at IS NULL
    ) OR (
      status='RETIRED'
      AND retired_at IS NOT NULL
      AND (
        (retained_at IS NULL AND retention_until IS NULL)
        OR (retained_at IS NOT NULL AND retention_until IS NOT NULL
          AND retention_until>retained_at)
      )
    ) OR (
      status NOT IN ('RETAINED','RETIRED')
      AND retained_at IS NULL
      AND retention_until IS NULL
      AND retired_at IS NULL
    )
  );

ALTER TABLE askdata.release_events
  DROP CONSTRAINT release_events_event_type_check;
ALTER TABLE askdata.release_events
  ADD CONSTRAINT askdata_release_events_event_type_check CHECK(event_type IN (
    'CREATED','VALIDATING','PROJECTING','PROJECTION_READY','PROJECTION_FAILED',
    'READY','ACTIVATED','SUPERSEDED','RETAINED','RETIRED','BLOCKED'
  ));

CREATE TABLE askdata.release_references(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  release_id uuid NOT NULL,
  reference_type text NOT NULL CHECK(reference_type IN (
    'REPORT_VERSION','CERTIFIED_EXAMPLE','SAVED_QUESTION','KPI_BUNDLE','EVALUATION_CASE'
  )),
  reference_id uuid NOT NULL,
  -- A bounded snapshot makes a blocked-retirement response complete even if
  -- the source asset is later renamed or its owning bounded context is absent.
  reference_name text NOT NULL CHECK(
    length(btrim(reference_name)) BETWEEN 1 AND 200
    AND reference_name=btrim(reference_name)
    AND reference_name !~ '[[:cntrl:]]'
  ),
  owner_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  CONSTRAINT askdata_release_references_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_references_subject_key
    UNIQUE(release_id,reference_type,reference_id),
  CONSTRAINT askdata_release_references_release_fk
    FOREIGN KEY(release_id,tenant_id)
    REFERENCES askdata.releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_references_owner_fk
    FOREIGN KEY(owner_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_references_release_time_check CHECK(
    released_at IS NULL OR released_at>=created_at
  )
);

CREATE INDEX askdata_release_references_active_idx
  ON askdata.release_references(release_id,reference_type,reference_id)
  WHERE released_at IS NULL;
CREATE INDEX askdata_release_references_tenant_active_idx
  ON askdata.release_references(tenant_id,release_id,created_at,id)
  WHERE released_at IS NULL;

CREATE OR REPLACE FUNCTION askdata.validate_release_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_status text;
DECLARE selected_domain_id uuid;
BEGIN
  IF TG_OP='INSERT' OR NEW.release_id IS DISTINCT FROM OLD.release_id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR (OLD.released_at IS NOT NULL AND NEW.released_at IS NULL) THEN
    SELECT status,domain_id INTO selected_status,selected_domain_id
    FROM askdata.releases
    WHERE id=NEW.release_id AND tenant_id=NEW.tenant_id
    FOR UPDATE;
    IF selected_status IS NULL THEN
      RAISE EXCEPTION 'release reference target was not found'
        USING ERRCODE='23503';
    END IF;
    IF NOT askdata.system_access()
      AND (
        askdata.current_actor_id() IS NULL
        OR NOT askdata.tenant_matches(NEW.tenant_id)
        OR NOT askdata.domain_can_access(selected_domain_id)
      ) THEN
      RAISE EXCEPTION 'release reference access denied'
        USING ERRCODE='42501';
    END IF;
    IF selected_status NOT IN ('READY','ACTIVE','SUPERSEDED','RETAINED') THEN
      RAISE EXCEPTION 'RELEASE_NOT_RUNNABLE: release cannot accept references in its current state'
        USING ERRCODE='23514';
    END IF;
  END IF;

  IF TG_OP='UPDATE' THEN
    IF NEW.id IS DISTINCT FROM OLD.id
      OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
      OR NEW.release_id IS DISTINCT FROM OLD.release_id
      OR NEW.reference_type IS DISTINCT FROM OLD.reference_type
      OR NEW.reference_id IS DISTINCT FROM OLD.reference_id THEN
      RAISE EXCEPTION 'release reference identity is immutable'
        USING ERRCODE='55000';
    END IF;
    IF OLD.released_at IS NULL AND NEW.released_at IS NULL THEN
      NEW.created_at=OLD.created_at;
    ELSIF OLD.released_at IS NULL AND NEW.released_at IS NOT NULL THEN
      NEW.reference_name=OLD.reference_name;
      NEW.owner_id=OLD.owner_id;
      NEW.created_at=OLD.created_at;
      NEW.released_at=GREATEST(NEW.released_at,OLD.created_at);
    ELSIF OLD.released_at IS NOT NULL AND NEW.released_at IS NULL THEN
      NEW.created_at=clock_timestamp();
    ELSE
      RAISE EXCEPTION 'released reference can only be reactivated'
        USING ERRCODE='55000';
    END IF;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_release_retention_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE active_reference_count bigint;
DECLARE retained_timestamp timestamptz;
BEGIN
  IF OLD.status='RETIRED' THEN
    RAISE EXCEPTION 'retired release is immutable' USING ERRCODE='55000';
  END IF;

  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;

  SELECT count(*) INTO active_reference_count
  FROM askdata.release_references
  WHERE tenant_id=OLD.tenant_id AND release_id=OLD.id AND released_at IS NULL;

  IF NEW.status='SUPERSEDED' AND active_reference_count>0 THEN
    retained_timestamp=clock_timestamp();
    NEW.status='RETAINED';
    NEW.retained_at=retained_timestamp;
    NEW.retention_until=retained_timestamp+interval '24 months';
    NEW.retired_at=NULL;
  ELSIF NEW.status='RETAINED' THEN
    IF OLD.status<>'SUPERSEDED' OR active_reference_count=0 THEN
      RAISE EXCEPTION 'RETAINED requires a referenced SUPERSEDED release'
        USING ERRCODE='23514';
    END IF;
    retained_timestamp=clock_timestamp();
    NEW.retained_at=retained_timestamp;
    NEW.retention_until=retained_timestamp+interval '24 months';
    NEW.retired_at=NULL;
  ELSIF NEW.status='RETIRED' THEN
    IF OLD.status NOT IN ('SUPERSEDED','RETAINED') THEN
      RAISE EXCEPTION 'release can only retire after SUPERSEDED or RETAINED'
        USING ERRCODE='23514';
    END IF;
    IF active_reference_count>0 THEN
      RAISE EXCEPTION 'RELEASE_RETIRE_BLOCKED: release has active references'
        USING ERRCODE='23514';
    END IF;
    IF OLD.status='RETAINED' AND clock_timestamp()<OLD.retention_until THEN
      RAISE EXCEPTION 'RELEASE_RETENTION_NOT_EXPIRED: retained release is still within its retention window'
        USING ERRCODE='23514';
    END IF;
    NEW.retained_at=OLD.retained_at;
    NEW.retention_until=OLD.retention_until;
    NEW.retired_at=clock_timestamp();
  ELSIF OLD.status='RETAINED' THEN
    RAISE EXCEPTION 'RETAINED release can only transition to RETIRED'
      USING ERRCODE='23514';
  ELSE
    NEW.retained_at=NULL;
    NEW.retention_until=NULL;
    NEW.retired_at=NULL;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.retain_referenced_release()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.released_at IS NULL
    AND (TG_OP='INSERT' OR OLD.released_at IS NOT NULL) THEN
    UPDATE askdata.releases SET
      status='RETAINED',version=version+1
    WHERE id=NEW.release_id AND tenant_id=NEW.tenant_id
      AND status='SUPERSEDED';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.record_release_retention_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status
    AND NEW.status IN ('RETAINED','RETIRED') THEN
    INSERT INTO askdata.release_events(
      tenant_id,domain_id,release_id,event_type,actor_id,detail
    ) VALUES(
      NEW.tenant_id,NEW.domain_id,NEW.id,NEW.status,askdata.current_actor_id(),
      jsonb_build_object(
        'activeReferenceCount',(
          SELECT count(*) FROM askdata.release_references
          WHERE tenant_id=NEW.tenant_id AND release_id=NEW.id AND released_at IS NULL
        ),
        'retentionUntil',NEW.retention_until
      )
    );
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.retire_release(selected_release_id uuid)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE active_reference_count bigint;
BEGIN
  SELECT * INTO selected_release
  FROM askdata.releases
  WHERE id=selected_release_id
    AND tenant_id=askdata.current_tenant_id()
    AND domain_id=askdata.current_domain_id()
  FOR UPDATE;
  IF selected_release.id IS NULL THEN
    RETURN 'NOT_FOUND';
  END IF;
  IF NOT askdata.system_access()
    AND (
      askdata.current_actor_id() IS NULL
      OR NOT (
        platform.user_is_platform_administrator()
        OR platform.user_is_domain_administrator(selected_release.domain_id)
      )
    ) THEN
    RAISE EXCEPTION 'release retirement requires domain administration'
      USING ERRCODE='42501';
  END IF;
  SELECT count(*) INTO active_reference_count
  FROM askdata.release_references
  WHERE tenant_id=selected_release.tenant_id
    AND release_id=selected_release.id
    AND released_at IS NULL;
  IF active_reference_count>0 THEN
    RETURN 'BLOCKED';
  END IF;
  IF selected_release.status='RETAINED'
    AND clock_timestamp()<selected_release.retention_until THEN
    RETURN 'NOT_EXPIRED';
  END IF;
  IF selected_release.status NOT IN ('SUPERSEDED','RETAINED') THEN
    RETURN 'INVALID_STATE';
  END IF;
  UPDATE askdata.releases SET status='RETIRED',version=version+1
  WHERE id=selected_release.id AND tenant_id=selected_release.tenant_id;
  RETURN 'RETIRED';
END
$$;

CREATE OR REPLACE FUNCTION askdata.release_registry_facts_complete(selected_release_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.id=selected_release_id
      AND release.tenant_id=askdata.current_tenant_id()
      AND release.object_count>0
      AND release.object_count=(
        SELECT count(*) FROM askdata.release_objects AS object
        WHERE object.tenant_id=release.tenant_id
          AND object.domain_id=release.domain_id
          AND object.release_id=release.id
      )
      AND askdata.release_manifest_hash(release.id)=release.content_hash
  )
$$;

CREATE OR REPLACE FUNCTION askdata.cleanup_retained_release_projections(selected_release_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE retained_contract_projections integer;
BEGIN
  SELECT * INTO selected_release FROM askdata.releases
  WHERE id=selected_release_id AND tenant_id=askdata.current_tenant_id()
  FOR UPDATE;
  IF selected_release.id IS NULL OR selected_release.status<>'RETAINED' THEN
    RETURN false;
  END IF;
  IF askdata.release_registry_facts_complete(selected_release.id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'retained release registry facts are incomplete'
      USING ERRCODE='23514';
  END IF;
  SELECT count(*) INTO retained_contract_projections
  FROM askdata.release_projections
  WHERE tenant_id=selected_release.tenant_id
    AND domain_id=selected_release.domain_id
    AND release_id=selected_release.id
    AND target IN ('POSTGRES_REGISTRY','EXECUTION_SEMANTIC_LAYER')
    AND status='READY'
    AND expected_content_hash=selected_release.content_hash
    AND applied_content_hash=selected_release.content_hash
    AND object_count=selected_release.object_count;
  IF retained_contract_projections<>2 THEN
    RAISE EXCEPTION 'retained release compilation projections are incomplete'
      USING ERRCODE='23514';
  END IF;

  DELETE FROM askdata.graph_plan_cache
  WHERE tenant_id=selected_release.tenant_id
    AND domain_id=selected_release.domain_id
    AND release_id=selected_release.id;
  DELETE FROM askdata.release_projection_artifacts
  WHERE tenant_id=selected_release.tenant_id
    AND domain_id=selected_release.domain_id
    AND release_id=selected_release.id
    AND target='SEARCH_INDEX';
  UPDATE askdata.release_projections SET
    status='STALE',applied_content_hash='',resource_version='',object_count=0,
    detail=jsonb_build_object('cleanup','RETAINED_RELEASE'),attempt=0,
    next_attempt_at='infinity'::timestamptz,lease_owner='',lease_token=NULL,
    lease_expires_at=NULL,error_code='',started_at=NULL,completed_at=NULL,
    version=version+1
  WHERE tenant_id=selected_release.tenant_id
    AND domain_id=selected_release.domain_id
    AND release_id=selected_release.id
    AND target IN ('SEARCH_INDEX','NEBULA_GRAPH');
  RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION askdata.reject_non_runnable_question_release()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_status text;
BEGIN
  SELECT status INTO selected_status FROM askdata.releases
  WHERE id=NEW.release_id AND tenant_id=NEW.tenant_id
    AND domain_id=NEW.domain_id AND content_hash=NEW.release_content_hash;
  IF selected_status IN ('SUPERSEDED','RETAINED','RETIRED') THEN
    RAISE EXCEPTION 'RELEASE_NOT_RUNNABLE: semantic release cannot create a new question run'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

-- Certified assets that are authored before REL-005 can be retained safely:
-- certification binds to a compatible current ACTIVE release when one exists,
-- and a later activation backfills all already-certified compatible assets.
CREATE OR REPLACE FUNCTION askdata.upsert_release_reference(
  selected_tenant_id uuid,
  selected_release_id uuid,
  selected_reference_type text,
  selected_reference_id uuid,
  selected_reference_name text,
  selected_owner_id uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  INSERT INTO askdata.release_references(
    tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
  ) VALUES(
    selected_tenant_id,selected_release_id,selected_reference_type,
    selected_reference_id,selected_reference_name,selected_owner_id
  )
  ON CONFLICT(release_id,reference_type,reference_id) DO UPDATE SET
    reference_name=EXCLUDED.reference_name,
    owner_id=EXCLUDED.owner_id,
    released_at=NULL;
END
$$;

CREATE OR REPLACE FUNCTION askdata.sync_certified_asset_release_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release_id uuid;
DECLARE selected_name text;
DECLARE selected_reference_type text;
BEGIN
  IF OLD.status='CERTIFIED' AND NEW.status='DEPRECATED' THEN
    selected_reference_type=CASE TG_TABLE_NAME
      WHEN 'certified_example_versions' THEN 'CERTIFIED_EXAMPLE'
      WHEN 'kpi_bundle_versions' THEN 'KPI_BUNDLE'
      WHEN 'evaluation_case_versions' THEN 'EVALUATION_CASE'
    END;
    UPDATE askdata.release_references SET released_at=clock_timestamp()
    WHERE tenant_id=NEW.tenant_id
      AND reference_type=selected_reference_type
      AND reference_id=NEW.id
      AND released_at IS NULL;
    RETURN NEW;
  END IF;
  IF NEW.status<>'CERTIFIED' OR OLD.status='CERTIFIED' THEN
    RETURN NEW;
  END IF;

  IF TG_TABLE_NAME='certified_example_versions' THEN
    selected_reference_type='CERTIFIED_EXAMPLE';
    selected_name='Certified example '||NEW.certified_example_id::text;
    SELECT release.id INTO selected_release_id
    FROM askdata.releases AS release
    WHERE release.tenant_id=NEW.tenant_id AND release.domain_id=NEW.domain_id
      AND release.status='ACTIVE'
      AND NOT EXISTS(
        SELECT 1 FROM unnest(NEW.expected_metric_version_ids) AS dependency(id)
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='METRIC' AND object.object_version_id=dependency.id
        )
      )
      AND NOT EXISTS(
        SELECT 1 FROM unnest(NEW.expected_dimension_version_ids) AS dependency(id)
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='DIMENSION' AND object.object_version_id=dependency.id
        )
      );
  ELSIF TG_TABLE_NAME='kpi_bundle_versions' THEN
    selected_reference_type='KPI_BUNDLE';
    SELECT name INTO selected_name FROM askdata.kpi_bundles
    WHERE id=NEW.kpi_bundle_id AND tenant_id=NEW.tenant_id;
    SELECT release.id INTO selected_release_id
    FROM askdata.releases AS release
    WHERE release.tenant_id=NEW.tenant_id AND release.domain_id=NEW.domain_id
      AND release.status='ACTIVE'
      AND NOT EXISTS(
        SELECT 1 FROM jsonb_array_elements(NEW.items) AS item
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='METRIC'
            AND object.object_version_id=(item->>'metricVersionId')::uuid
        )
      )
      AND NOT EXISTS(
        SELECT 1 FROM (
          SELECT jsonb_array_elements_text(
            COALESCE(item->'groupByDimensionVersionIds','[]'::jsonb)
          )::uuid AS id
          FROM jsonb_array_elements(NEW.items) AS item
          UNION
          SELECT unnest(NEW.default_dimension_version_ids)
        ) AS dependency
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='DIMENSION' AND object.object_version_id=dependency.id
        )
      );
  ELSE
    selected_reference_type='EVALUATION_CASE';
    selected_name='Evaluation case '||NEW.evaluation_case_asset_id::text;
    SELECT release.id INTO selected_release_id
    FROM askdata.releases AS release
    WHERE release.tenant_id=NEW.tenant_id AND release.domain_id=NEW.domain_id
      AND release.status='ACTIVE'
      AND NOT EXISTS(
        SELECT 1 FROM unnest(NEW.expected_metric_version_ids) AS dependency(id)
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='METRIC' AND object.object_version_id=dependency.id
        )
      )
      AND NOT EXISTS(
        SELECT 1 FROM unnest(NEW.expected_dimension_version_ids) AS dependency(id)
        WHERE NOT EXISTS(
          SELECT 1 FROM askdata.release_objects AS object
          WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
            AND object.object_type='DIMENSION' AND object.object_version_id=dependency.id
        )
      );
  END IF;
  IF selected_release_id IS NOT NULL THEN
    PERFORM askdata.upsert_release_reference(
      NEW.tenant_id,selected_release_id,selected_reference_type,
      NEW.id,selected_name,NEW.owner_id
    );
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.sync_sealed_evaluation_set_release_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.status='SEALED' AND OLD.status='DRAFT' THEN
    PERFORM askdata.upsert_release_reference(
      NEW.tenant_id,NEW.target_release_id,'EVALUATION_CASE',NEW.id,NEW.name,NEW.created_by
    );
  ELSIF NEW.status='RETIRED' AND OLD.status='SEALED' THEN
    UPDATE askdata.release_references SET released_at=clock_timestamp()
    WHERE tenant_id=NEW.tenant_id AND release_id=NEW.target_release_id
      AND reference_type='EVALUATION_CASE' AND reference_id=NEW.id
      AND released_at IS NULL;
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.backfill_active_release_asset_references(
  selected_release_id uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
BEGIN
  SELECT * INTO selected_release FROM askdata.releases
  WHERE id=selected_release_id AND status='ACTIVE';
  IF selected_release.id IS NULL THEN
    RETURN;
  END IF;

  INSERT INTO askdata.release_references(
    tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
  )
  SELECT version.tenant_id,selected_release.id,'CERTIFIED_EXAMPLE',version.id,
    'Certified example '||version.certified_example_id::text,version.owner_id
  FROM askdata.certified_example_versions AS version
  WHERE version.tenant_id=selected_release.tenant_id
    AND version.domain_id=selected_release.domain_id
    AND version.status='CERTIFIED'
    AND NOT EXISTS(
      SELECT 1 FROM unnest(version.expected_metric_version_ids) AS dependency(id)
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='METRIC'
          AND object.object_version_id=dependency.id
      )
    )
    AND NOT EXISTS(
      SELECT 1 FROM unnest(version.expected_dimension_version_ids) AS dependency(id)
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='DIMENSION'
          AND object.object_version_id=dependency.id
      )
    )
  ON CONFLICT(release_id,reference_type,reference_id) DO UPDATE SET
    reference_name=EXCLUDED.reference_name,owner_id=EXCLUDED.owner_id,released_at=NULL;

  INSERT INTO askdata.release_references(
    tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
  )
  SELECT version.tenant_id,selected_release.id,'KPI_BUNDLE',version.id,
    bundle.name,version.owner_id
  FROM askdata.kpi_bundle_versions AS version
  JOIN askdata.kpi_bundles AS bundle
    ON bundle.id=version.kpi_bundle_id AND bundle.tenant_id=version.tenant_id
  WHERE version.tenant_id=selected_release.tenant_id
    AND version.domain_id=selected_release.domain_id
    AND version.status='CERTIFIED'
    AND NOT EXISTS(
      SELECT 1 FROM jsonb_array_elements(version.items) AS item
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='METRIC'
          AND object.object_version_id=(item->>'metricVersionId')::uuid
      )
    )
    AND NOT EXISTS(
      SELECT 1 FROM (
        SELECT jsonb_array_elements_text(
          COALESCE(item->'groupByDimensionVersionIds','[]'::jsonb)
        )::uuid AS id
        FROM jsonb_array_elements(version.items) AS item
        UNION
        SELECT unnest(version.default_dimension_version_ids)
      ) AS dependency
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='DIMENSION'
          AND object.object_version_id=dependency.id
      )
    )
  ON CONFLICT(release_id,reference_type,reference_id) DO UPDATE SET
    reference_name=EXCLUDED.reference_name,owner_id=EXCLUDED.owner_id,released_at=NULL;

  INSERT INTO askdata.release_references(
    tenant_id,release_id,reference_type,reference_id,reference_name,owner_id
  )
  SELECT version.tenant_id,selected_release.id,'EVALUATION_CASE',version.id,
    'Evaluation case '||version.evaluation_case_asset_id::text,version.owner_id
  FROM askdata.evaluation_case_versions AS version
  WHERE version.tenant_id=selected_release.tenant_id
    AND version.domain_id=selected_release.domain_id
    AND version.status='CERTIFIED'
    AND NOT EXISTS(
      SELECT 1 FROM unnest(version.expected_metric_version_ids) AS dependency(id)
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='METRIC'
          AND object.object_version_id=dependency.id
      )
    )
    AND NOT EXISTS(
      SELECT 1 FROM unnest(version.expected_dimension_version_ids) AS dependency(id)
      WHERE NOT EXISTS(
        SELECT 1 FROM askdata.release_objects AS object
        WHERE object.tenant_id=selected_release.tenant_id
          AND object.release_id=selected_release.id
          AND object.object_type='DIMENSION'
          AND object.object_version_id=dependency.id
      )
    )
  ON CONFLICT(release_id,reference_type,reference_id) DO UPDATE SET
    reference_name=EXCLUDED.reference_name,owner_id=EXCLUDED.owner_id,released_at=NULL;
END
$$;

CREATE OR REPLACE FUNCTION askdata.sync_active_release_asset_references()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF NEW.status='ACTIVE'
    AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status) THEN
    PERFORM askdata.backfill_active_release_asset_references(NEW.id);
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.validate_release_reference(),
  askdata.enforce_release_retention_lifecycle(),
  askdata.retain_referenced_release(),
  askdata.record_release_retention_event(),
  askdata.retire_release(uuid),
  askdata.release_registry_facts_complete(uuid),
  askdata.cleanup_retained_release_projections(uuid),
  askdata.reject_non_runnable_question_release(),
  askdata.upsert_release_reference(uuid,uuid,text,uuid,text,uuid),
  askdata.sync_certified_asset_release_reference(),
  askdata.sync_sealed_evaluation_set_release_reference(),
  askdata.backfill_active_release_asset_references(uuid),
  askdata.sync_active_release_asset_references()
FROM PUBLIC;

CREATE TRIGGER askdata_release_references_validate
BEFORE INSERT OR UPDATE ON askdata.release_references
FOR EACH ROW EXECUTE FUNCTION askdata.validate_release_reference();
CREATE TRIGGER askdata_release_references_retain
AFTER INSERT OR UPDATE OF released_at ON askdata.release_references
FOR EACH ROW EXECUTE FUNCTION askdata.retain_referenced_release();
CREATE TRIGGER askdata_releases_retention_lifecycle
BEFORE UPDATE ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_release_retention_lifecycle();
CREATE TRIGGER askdata_releases_retention_events
AFTER UPDATE ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.record_release_retention_event();
CREATE TRIGGER askdata_question_runs_00_runnable_release
BEFORE INSERT ON askdata.question_runs
FOR EACH ROW EXECUTE FUNCTION askdata.reject_non_runnable_question_release();
CREATE TRIGGER askdata_certified_example_versions_release_reference
AFTER UPDATE OF status ON askdata.certified_example_versions
FOR EACH ROW EXECUTE FUNCTION askdata.sync_certified_asset_release_reference();
CREATE TRIGGER askdata_kpi_bundle_versions_release_reference
AFTER UPDATE OF status ON askdata.kpi_bundle_versions
FOR EACH ROW EXECUTE FUNCTION askdata.sync_certified_asset_release_reference();
CREATE TRIGGER askdata_evaluation_case_versions_release_reference
AFTER UPDATE OF status ON askdata.evaluation_case_versions
FOR EACH ROW EXECUTE FUNCTION askdata.sync_certified_asset_release_reference();
CREATE TRIGGER askdata_evaluation_sets_release_reference
AFTER UPDATE OF status ON askdata.evaluation_sets
FOR EACH ROW EXECUTE FUNCTION askdata.sync_sealed_evaluation_set_release_reference();
CREATE TRIGGER askdata_releases_active_asset_references_insert
AFTER INSERT ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.sync_active_release_asset_references();
CREATE TRIGGER askdata_releases_active_asset_references_update
AFTER UPDATE OF status ON askdata.releases
FOR EACH ROW EXECUTE FUNCTION askdata.sync_active_release_asset_references();

ALTER TABLE askdata.release_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_references FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_references_domain_isolation
ON askdata.release_references
USING(
  askdata.tenant_matches(release_references.tenant_id)
  AND EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.id=release_references.release_id
      AND release.tenant_id=release_references.tenant_id
      AND askdata.domain_can_access(release.domain_id)
  )
)
WITH CHECK(
  askdata.tenant_matches(release_references.tenant_id)
  AND EXISTS(
    SELECT 1 FROM askdata.releases AS release
    WHERE release.id=release_references.release_id
      AND release.tenant_id=release_references.tenant_id
      AND askdata.domain_can_access(release.domain_id)
  )
);

COMMENT ON TABLE askdata.release_references IS
  'Auditable active/released references that prevent retirement of immutable semantic releases';
COMMENT ON COLUMN askdata.release_references.reference_name IS
  'Bounded display snapshot used only for governed retirement impact lists';
COMMENT ON COLUMN askdata.releases.retention_until IS
  'Earliest retirement time after a referenced SUPERSEDED release enters RETAINED; defaults to 24 calendar months';
