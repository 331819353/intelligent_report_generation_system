package semanticqa

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type GraphWorker struct {
	store *PostgresStore
}

func NewGraphWorker(store *PostgresStore) *GraphWorker {
	return &GraphWorker{store: store}
}

func (worker *GraphWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, ErrInvalidRequest
	}
	// 租户枚举发生在进入具体租户事务之前，不能直接读取强制 RLS 的
	// semantic_qa_settings，否则 current_tenant_id 尚未设置时永远返回空集。
	// claim 会在 tenant-scoped 事务内再次校验语义问答与图投影开关。
	rows, err := worker.store.pool.Query(ctx, `SELECT tenant.id::text
		FROM platform.tenants AS tenant
		WHERE tenant.status='ACTIVE' AND tenant.deleted_at IS NULL
		ORDER BY tenant.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantIDs := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	return tenantIDs, rows.Err()
}

func (worker *GraphWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil ||
		uuid.Validate(tenantID) != nil || !validWorkerID(workerID) ||
		lease < 5*time.Second || lease > 10*time.Minute {
		return false, ErrInvalidRequest
	}
	claim, err := worker.claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	generation, err := worker.rebuild(ctx, *claim, workerID)
	if err == nil {
		slog.Info("semantic graph generation ready",
			"tenant_id", tenantID, "generation", generation.Generation,
			"nodes", generation.NodeCount, "edges", generation.EdgeCount)
		return true, nil
	}
	finishErr := worker.fail(ctx, *claim, workerID, projectionErrorCode(err))
	return true, errors.Join(err, finishErr)
}

func (worker *GraphWorker) claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *graphClaim, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		var item graphClaim
		item.TenantID = tenantID
		queryErr := tx.QueryRow(ctx, `UPDATE platform.semantic_graph_projection_state AS state
			SET status='RUNNING',attempt=state.attempt+1,error_code='',
				lease_owner=$1,lease_token=gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 millisecond'),
				updated_at=now()
			FROM platform.semantic_qa_settings AS setting
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND setting.tenant_id=state.tenant_id
			  AND setting.enabled AND setting.graph_projection_enabled
			  AND state.applied_event_version<state.requested_event_version
			  AND state.attempt<state.max_attempts
			  AND state.next_attempt_at<=now()
			  AND (
			    state.status IN ('PENDING','FAILED','READY')
			    OR (state.status='RUNNING' AND state.lease_expires_at<=now())
			  )
			RETURNING state.requested_event_version,state.lease_token::text,
				state.attempt,state.max_attempts`,
			workerID, lease.Milliseconds(),
		).Scan(
			&item.RequestedEventVersion, &item.LeaseToken,
			&item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (worker *GraphWorker) rebuild(
	ctx context.Context,
	claim graphClaim,
	workerID string,
) (generation graphGeneration, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var requested int64
		if err := tx.QueryRow(ctx, `SELECT requested_event_version
			FROM platform.semantic_graph_projection_state
			WHERE tenant_id=platform.current_tenant_id()
			  AND status='RUNNING' AND lease_owner=$1
			  AND lease_token=$2::uuid AND lease_expires_at>now()
			FOR UPDATE`, workerID, claim.LeaseToken).Scan(&requested); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProjectionLease
			}
			return err
		}
		if requested < claim.RequestedEventVersion {
			return ErrProjectionLease
		}
		var snapshotHash string
		if err := tx.QueryRow(ctx, `SELECT encode(digest(concat_ws('|',
				$1::bigint::text,
				(SELECT count(*)::text FROM platform.datasets
				  WHERE current_published_version_id IS NOT NULL AND deleted_at IS NULL),
				(SELECT count(*)::text FROM platform.dataset_fields AS field
				  JOIN platform.datasets AS dataset
				    ON dataset.current_published_version_id=field.dataset_version_id
				   AND dataset.tenant_id=field.tenant_id
				  WHERE dataset.deleted_at IS NULL),
				(SELECT count(*)::text FROM platform.semantic_dimensions WHERE status='PUBLISHED'),
				(SELECT count(*)::text FROM platform.dimension_members WHERE status='ACTIVE'),
				(SELECT count(*)::text FROM platform.metric_versions WHERE status='PUBLISHED'),
				(SELECT COALESCE(sum(event_version),0)::text FROM platform.semantic_change_outbox)
			),'sha256'),'hex')`, claim.RequestedEventVersion).Scan(&snapshotHash); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_graph_generations(
				tenant_id,generation,snapshot_hash,status
			) SELECT platform.current_tenant_id(),
				COALESCE(max(generation),0)+1,$1,'BUILDING'
			FROM platform.semantic_graph_generations
			RETURNING id::text,generation`,
			snapshotHash,
		).Scan(&generation.ID, &generation.Generation); err != nil {
			return err
		}
		if err := insertGraphNodes(ctx, tx, generation.ID); err != nil {
			return err
		}
		if err := insertGraphEdges(ctx, tx, generation.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*)::int FROM platform.semantic_graph_nodes
				  WHERE generation_id=$1::uuid),
				(SELECT count(*)::int FROM platform.semantic_graph_edges
				  WHERE generation_id=$1::uuid)`, generation.ID).
			Scan(&generation.NodeCount, &generation.EdgeCount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.semantic_graph_generations
			SET status='SUPERSEDED'
			WHERE tenant_id=platform.current_tenant_id()
			  AND status='READY' AND id<>$1::uuid`, generation.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.semantic_graph_generations
			SET status='READY',node_count=$1,edge_count=$2,ready_at=now()
			WHERE id=$3::uuid AND status='BUILDING'`,
			generation.NodeCount, generation.EdgeCount, generation.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_graph_projection_state
			SET current_generation_id=$1::uuid,
				applied_event_version=$2,
				status=CASE WHEN requested_event_version=$2 THEN 'READY' ELSE 'PENDING' END,
				attempt=CASE WHEN requested_event_version=$2 THEN 0 ELSE attempt END,
				next_attempt_at=now(),error_code='',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			WHERE tenant_id=platform.current_tenant_id()
			  AND status='RUNNING' AND lease_owner=$3 AND lease_token=$4::uuid`,
			generation.ID, claim.RequestedEventVersion, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
	return generation, err
}

func insertGraphNodes(ctx context.Context, tx pgx.Tx, generationID string) error {
	queries := []string{
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT dataset.tenant_id,$1::uuid,'dataset:'||dataset.id::text,'DATASET',
			dataset.id::text,dataset.name,lower(btrim(dataset.name)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.datasets AS dataset
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'datasetId',dataset.id::text,'datasetVersionId',
		    dataset.current_published_version_id::text,'code',dataset.code::text,
		    'name',dataset.name,'layer',dataset.layer
		  ) AS value
		) AS payload
		WHERE dataset.current_published_version_id IS NOT NULL
		  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		WITH RECURSIVE lineage AS (
		  SELECT version.tenant_id,version.id
		  FROM platform.dataset_versions AS version
		  JOIN platform.datasets AS dataset
		    ON dataset.tenant_id=version.tenant_id
		   AND dataset.id=version.dataset_id
		   AND dataset.current_published_version_id=version.id
		  WHERE version.status='PUBLISHED'
		    AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
		  UNION
		  SELECT dependency.tenant_id,
		    dependency.source_id::uuid
		  FROM lineage
		  JOIN platform.dataset_dependencies AS dependency
		    ON dependency.tenant_id=lineage.tenant_id
		   AND dependency.dataset_version_id=lineage.id
		   AND dependency.source_type='DATASET_VERSION'
		  JOIN platform.dataset_versions AS upstream
		    ON upstream.tenant_id=dependency.tenant_id
		   AND upstream.id=dependency.source_id::uuid
		   AND upstream.status='PUBLISHED'
		)
		SELECT version.tenant_id,$1::uuid,'dataset_version:'||version.id::text,
			'DATASET_VERSION',version.id::text,
			dataset.name||' ['||version.layer||']',
			lower(btrim(dataset.name||' '||dataset.code::text||' '||version.layer)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM lineage
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=lineage.tenant_id AND version.id=lineage.id
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'datasetId',dataset.id::text,'datasetVersionId',version.id::text,
		    'code',dataset.code::text,'name',dataset.name,'layer',version.layer,
		    'schemaHash',version.schema_hash
		  ) AS value
		) AS payload
		WHERE version.status='PUBLISHED'`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT materialization.tenant_id,$1::uuid,
			'materialization:'||materialization.id::text,
			'MATERIALIZATION',materialization.id::text,
			dataset.name||' active materialization',
			lower(btrim(dataset.name||' active materialization')),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.dataset_materializations AS materialization
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=materialization.tenant_id
		 AND version.id=materialization.dataset_version_id
		 AND version.dataset_id=materialization.dataset_id
		 AND version.status='PUBLISHED'
		 AND version.schema_hash=materialization.schema_hash
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id
		 AND dataset.id=version.dataset_id
		 AND dataset.current_published_version_id=version.id
		 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'materializationId',materialization.id::text,
		    'datasetId',materialization.dataset_id::text,
		    'datasetVersionId',materialization.dataset_version_id::text,
		    'layer',materialization.layer,
		    'schemaHash',materialization.schema_hash,
		    'snapshotHash',materialization.snapshot_hash,
		    'activatedAt',materialization.activated_at
		  ) AS value
		) AS payload
		WHERE materialization.status='ACTIVE'`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT field.tenant_id,$1::uuid,
			'field:'||field.dataset_version_id::text||':'||field.field_id,
			'FIELD',field.dataset_version_id::text||':'||field.field_id,
			field.field_name,lower(btrim(field.field_name||' '||field.field_code::text)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.dataset_fields AS field
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=field.tenant_id
		 AND dataset.current_published_version_id=field.dataset_version_id
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'datasetVersionId',field.dataset_version_id::text,
		    'fieldId',field.field_id,'code',field.field_code::text,
		    'name',field.field_name,'role',field.field_role,
		    'semanticType',field.semantic_type
		  ) AS value
		) AS payload
		WHERE dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT dimension.tenant_id,$1::uuid,'dimension:'||dimension.id::text,
			'DIMENSION',dimension.id::text,dimension.name,
			lower(btrim(dimension.name||' '||dimension.code::text)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.semantic_dimensions AS dimension
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=dimension.tenant_id
		 AND dataset.current_published_version_id=dimension.dataset_version_id
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'dimensionId',dimension.id::text,'code',dimension.code::text,
		    'name',dimension.name,'dimensionType',dimension.dimension_type,
		    'datasetVersionId',dimension.dataset_version_id::text,
		    'fieldId',dimension.field_id
		  ) AS value
		) AS payload
		WHERE dimension.status='PUBLISHED' AND dataset.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT member.tenant_id,$1::uuid,'member:'||member.id::text,'MEMBER',
			member.id::text,member.canonical_label,member.normalized_value,
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.dimension_members AS member
		JOIN platform.semantic_dimensions AS dimension
		  ON dimension.tenant_id=member.tenant_id
		 AND dimension.id=member.dimension_id AND dimension.status='PUBLISHED'
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=dimension.tenant_id
		 AND dataset.current_published_version_id=dimension.dataset_version_id
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'memberId',member.id::text,'dimensionId',member.dimension_id::text,
		    'memberKey',member.member_key,'label',member.canonical_label,
		    'normalizedValue',member.normalized_value,
		    'validFrom',member.valid_from,'validTo',member.valid_to,
		    'aliases',COALESCE((
		      SELECT jsonb_agg(
		        jsonb_build_object(
		          'normalizedAlias',alias.normalized_alias,
		          'validFrom',alias.valid_from,'validTo',alias.valid_to
		        )
		        ORDER BY alias.id
		      )
		      FROM platform.dimension_member_aliases AS alias
		      WHERE alias.dimension_member_id=member.id
		    ),'[]'::jsonb)
		  ) AS value
		) AS payload
		WHERE member.status='ACTIVE' AND dataset.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT version.tenant_id,$1::uuid,'metric:'||version.id::text,'METRIC',
			version.id::text,metric.name,
			lower(btrim(metric.name||' '||metric.code::text)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.metric_versions AS version
		JOIN platform.metrics AS metric
		  ON metric.tenant_id=version.tenant_id AND metric.id=version.metric_id
		 AND metric.current_published_version_id=version.id
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id
		 AND dataset.current_published_version_id=version.dataset_version_id
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'metricId',metric.id::text,'metricVersionId',version.id::text,
		    'code',metric.code::text,'name',metric.name,
		    'metricType',metric.metric_type,
		    'datasetVersionId',version.dataset_version_id::text,
		    'definitionHash',version.definition_hash
		  ) AS value
		) AS payload
		WHERE version.status='PUBLISHED' AND metric.status='PUBLISHED'
		  AND metric.deleted_at IS NULL AND dataset.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT tag.tenant_id,$1::uuid,'tag:'||tag.id::text,'TAG',tag.id::text,
			tag.name,lower(btrim(tag.name||' '||tag.code::text)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.semantic_tags AS tag
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'tagId',tag.id::text,'code',tag.code::text,'name',tag.name,
		    'category',tag.category
		  ) AS value
		) AS payload
		WHERE tag.status='ACTIVE'`,
		`INSERT INTO platform.semantic_graph_nodes(
			tenant_id,generation_id,node_key,node_type,subject_ref,label,
			normalized_label,payload_hash,payload_json
		)
		SELECT DISTINCT dependency.tenant_id,$1::uuid,
			'source:'||dependency.source_type||':'||dependency.source_id,
			'SOURCE',dependency.source_type||':'||dependency.source_id,
			CASE dependency.source_type
			  WHEN 'TABLE' THEN COALESCE(source.name||'.'||metadata.schema_name||'.'||metadata.table_name,dependency.source_id)
			  WHEN 'FILE_VERSION' THEN COALESCE(file_version.filename,dependency.source_id)
			  ELSE dependency.source_id
			END,
			lower(btrim(CASE dependency.source_type
			  WHEN 'TABLE' THEN COALESCE(source.name||' '||metadata.schema_name||' '||metadata.table_name,dependency.source_id)
			  WHEN 'FILE_VERSION' THEN COALESCE(file_version.filename,dependency.source_id)
			  ELSE dependency.source_id
			END)),
			encode(digest(payload.value::text,'sha256'),'hex'),payload.value
		FROM platform.dataset_dependencies AS dependency
		JOIN platform.semantic_graph_nodes AS version_node
		  ON version_node.tenant_id=dependency.tenant_id
		 AND version_node.generation_id=$1::uuid
		 AND version_node.node_key=
		   'dataset_version:'||dependency.dataset_version_id::text
		 AND version_node.node_type='DATASET_VERSION'
		LEFT JOIN platform.metadata_tables AS metadata
		  ON dependency.source_type='TABLE'
		 AND metadata.tenant_id=dependency.tenant_id
		 AND metadata.id=dependency.source_id::uuid
		LEFT JOIN platform.data_sources AS source
		  ON source.tenant_id=metadata.tenant_id AND source.id=metadata.data_source_id
		LEFT JOIN platform.file_asset_versions AS file_version
		  ON dependency.source_type='FILE_VERSION'
		 AND file_version.tenant_id=dependency.tenant_id
		 AND file_version.id=dependency.source_id::uuid
		CROSS JOIN LATERAL (
		  SELECT jsonb_build_object(
		    'sourceType',dependency.source_type,'sourceId',dependency.source_id,
		    'dataSourceId',source.id::text,'dataSourceName',source.name,
		    'schemaName',metadata.schema_name,'tableName',metadata.table_name,
		    'fileName',file_version.filename
		  ) AS value
		) AS payload
		WHERE dependency.source_type IN ('TABLE','FILE_VERSION')`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, generationID); err != nil {
			return err
		}
	}
	return nil
}

func insertGraphEdges(ctx context.Context, tx pgx.Tx, generationID string) error {
	queries := []string{
		graphEdgeInsert(`SELECT member.tenant_id,
			'member_of:'||member.id::text,
			'member:'||member.id::text,'dimension:'||member.dimension_id::text,
			'MEMBER_OF',1.0000,'CONTROL_PLANE',
			jsonb_build_object('memberId',member.id::text,'dimensionId',member.dimension_id::text)
			FROM platform.dimension_members AS member
			WHERE member.status='ACTIVE'`),
		graphEdgeInsert(`SELECT dimension.tenant_id,
			'dimension_field:'||dimension.id::text,
			'dimension:'||dimension.id::text,
			'field:'||dimension.dataset_version_id::text||':'||dimension.field_id,
			'DIMENSION_FIELD',1.0000,'CONTROL_PLANE',
			jsonb_build_object('definitionHash',dimension.definition_hash)
			FROM platform.semantic_dimensions AS dimension
			WHERE dimension.status='PUBLISHED'`),
		graphEdgeInsert(`SELECT field.tenant_id,
			'field_dataset:'||field.dataset_version_id::text||':'||field.field_id,
			'field:'||field.dataset_version_id::text||':'||field.field_id,
			'dataset_version:'||field.dataset_version_id::text,
			'FIELD_DATASET',1.0000,'CONTROL_PLANE',
			jsonb_build_object('fieldId',field.field_id)
			FROM platform.dataset_fields AS field`),
		graphEdgeInsert(`SELECT version.tenant_id,
			'dataset_version_of:'||version.id::text,
			'dataset_version:'||version.id::text,'dataset:'||version.dataset_id::text,
			'DATASET_VERSION_OF',1.0000,'CONTROL_PLANE',
			jsonb_build_object('schemaHash',version.schema_hash)
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.current_published_version_id=version.id
			WHERE version.status='PUBLISHED' AND dataset.status='PUBLISHED'
			  AND dataset.deleted_at IS NULL`),
		graphEdgeInsert(`SELECT materialization.tenant_id,
			'dataset_materialized_as:'||materialization.dataset_version_id::text,
			'dataset_version:'||materialization.dataset_version_id::text,
			'materialization:'||materialization.id::text,
			'DATASET_MATERIALIZED_AS',1.0000,'CONTROL_PLANE',
			jsonb_build_object(
			  'materializationId',materialization.id::text,
			  'schemaHash',materialization.schema_hash,
			  'snapshotHash',materialization.snapshot_hash,
			  'activatedAt',materialization.activated_at
			)
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=materialization.tenant_id
			 AND version.id=materialization.dataset_version_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.status='PUBLISHED'
			 AND version.schema_hash=materialization.schema_hash
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			 AND dataset.current_published_version_id=version.id
			 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			WHERE materialization.status='ACTIVE'`),
		graphEdgeInsert(`SELECT version.tenant_id,
			'metric_dataset:'||version.id::text,
			'metric:'||version.id::text,
			'dataset_version:'||version.dataset_version_id::text,
			'METRIC_DATASET',1.0000,'CONTROL_PLANE',
			jsonb_build_object('definitionHash',version.definition_hash)
			FROM platform.metric_versions AS version
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=version.tenant_id
			 AND metric.current_published_version_id=version.id
			WHERE version.status='PUBLISHED' AND metric.status='PUBLISHED'
			  AND metric.deleted_at IS NULL`),
		graphEdgeInsert(`SELECT compatibility.tenant_id,
			'metric_dimension:'||compatibility.metric_version_id::text||':'||compatibility.dimension_id::text,
			'metric:'||compatibility.metric_version_id::text,
			'dimension:'||compatibility.dimension_id::text,
			'METRIC_DIMENSION',compatibility.confidence,
			'VERIFIED',
			jsonb_build_object(
			  'compatibilityId',compatibility.id::text,
			  'compatibilityType',compatibility.compatibility_type,
			  'fanoutPolicy',compatibility.fanout_policy,
			  'joinPath',compatibility.join_path_json
			)
			FROM platform.dimension_metric_compatibility AS compatibility
			WHERE compatibility.status='VERIFIED'
			  AND compatibility.fanout_policy<>'UNSAFE'`),
		graphEdgeInsert(`SELECT dependency.tenant_id,
			'dataset_depends:'||dependency.dataset_version_id::text||':'||dependency.source_id,
			'dataset_version:'||dependency.dataset_version_id::text,
			'dataset_version:'||dependency.source_id,
			'DATASET_DEPENDS_ON',1.0000,'CONTROL_PLANE',
			jsonb_build_object('sourceType',dependency.source_type)
			FROM platform.dataset_dependencies AS dependency
			WHERE dependency.source_type='DATASET_VERSION'`),
		graphEdgeInsert(`SELECT dependency.tenant_id,
			'dataset_source:'||dependency.dataset_version_id::text||':'||
			  dependency.source_type||':'||dependency.source_id,
			'dataset_version:'||dependency.dataset_version_id::text,
			'source:'||dependency.source_type||':'||dependency.source_id,
			'DATASET_SOURCE',1.0000,'CONTROL_PLANE',
			jsonb_build_object('sourceType',dependency.source_type,'sourceId',dependency.source_id)
			FROM platform.dataset_dependencies AS dependency
			WHERE dependency.source_type IN ('TABLE','FILE_VERSION')`),
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, generationID); err != nil {
			return err
		}
	}
	return nil
}

func graphEdgeInsert(source string) string {
	return `INSERT INTO platform.semantic_graph_edges(
			tenant_id,generation_id,edge_key,from_node_key,to_node_key,
			relation_type,confidence,authority,evidence_hash,evidence_json
		)
		SELECT candidate.tenant_id,$1::uuid,candidate.edge_key,
			candidate.from_node_key,candidate.to_node_key,candidate.relation_type,
			candidate.confidence,candidate.authority,
			encode(digest(candidate.evidence::text,'sha256'),'hex'),candidate.evidence
		FROM (` + source + `) AS candidate(
			tenant_id,edge_key,from_node_key,to_node_key,relation_type,
			confidence,authority,evidence
		)
		JOIN platform.semantic_graph_nodes AS source_node
		  ON source_node.tenant_id=candidate.tenant_id
		 AND source_node.generation_id=$1::uuid
		 AND source_node.node_key=candidate.from_node_key
		JOIN platform.semantic_graph_nodes AS target_node
		  ON target_node.tenant_id=candidate.tenant_id
		 AND target_node.generation_id=$1::uuid
		 AND target_node.node_key=candidate.to_node_key
		ON CONFLICT(tenant_id,generation_id,edge_key) DO NOTHING`
}

func (worker *GraphWorker) fail(
	ctx context.Context,
	claim graphClaim,
	workerID, code string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		status := "PENDING"
		if claim.Attempt >= claim.MaxAttempts {
			status = "FAILED"
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_graph_projection_state
			SET status=$1,error_code=$2,next_attempt_at=now()+
				(LEAST(300,power(2,LEAST(attempt,8)))::text||' seconds')::interval,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,updated_at=now()
			WHERE tenant_id=platform.current_tenant_id()
			  AND status='RUNNING' AND lease_owner=$3 AND lease_token=$4::uuid`,
			status, code, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func (store *PostgresStore) GetGraphStatus(
	ctx context.Context,
	tenantID string,
) (item GraphStatus, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var updatedAt time.Time
		err := tx.QueryRow(ctx, `SELECT state.status,
				COALESCE(state.current_generation_id::text,''),
				COALESCE(generation.generation,0),
				state.requested_event_version,state.applied_event_version,
				COALESCE(generation.node_count,0),COALESCE(generation.edge_count,0),
				state.error_code,state.updated_at
			FROM platform.semantic_graph_projection_state AS state
			LEFT JOIN platform.semantic_graph_generations AS generation
			  ON generation.id=state.current_generation_id
			WHERE state.tenant_id=platform.current_tenant_id()`).
			Scan(
				&item.Status, &item.CurrentGenerationID, &item.CurrentGeneration,
				&item.RequestedEventVersion, &item.AppliedEventVersion,
				&item.NodeCount, &item.EdgeCount, &item.ErrorCode, &updatedAt,
			)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return err
	})
	return item, err
}

func projectionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrProjectionLease):
		return "LEASE_LOST"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	default:
		return "GRAPH_BUILD_FAILED"
	}
}

func validWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func RunGraphWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *GraphWorker,
	workerID string,
	pollInterval time.Duration,
) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list semantic graph tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, processErr := worker.ProcessNext(
					ctx, tenantID, workerID, 2*time.Minute,
				)
				if processErr != nil {
					logger.Error("project semantic graph",
						"tenant_id", tenantID, "error", processErr)
				}
				processed = processed || didProcess
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}
