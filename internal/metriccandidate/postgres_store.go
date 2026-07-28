package metriccandidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresStore 持久化发布 outbox、worker 租约以及租户隔离的候选审核状态。
type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// TriggerManualIdentification compares the current governed dataset inventory with the
// existing metric/candidate catalog and schedules only an explicit, user-initiated scan.
// DWS field roles and descriptions are authoritative because DWS save already completed
// semantic naming. MEASURE fields are handled by the Go rule extractor, while
// non-measure roles are inserted directly as approved dimension assets.
func (s *PostgresStore) TriggerManualIdentification(
	ctx context.Context,
	tenantID, actorID string,
) (result IdentificationResult, err error) {
	if s == nil || tenantID == "" || !canonicalUUID(actorID) {
		return IdentificationResult{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM platform.metrics
			WHERE deleted_at IS NULL AND status<>'DELETED'`).Scan(
			&result.HistoricalMetricCount,
		); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM platform.metric_candidates`).Scan(
			&result.ExistingCandidateCount,
		); err != nil {
			return err
		}
		rows, queryErr := tx.Query(ctx, `WITH candidates AS (
				SELECT version.tenant_id,version.dataset_id,
					version.id AS dataset_version_id,version.schema_hash
				FROM platform.datasets AS dataset
				JOIN platform.dataset_versions AS version
				  ON version.tenant_id=dataset.tenant_id
				 AND version.dataset_id=dataset.id
				 AND version.id=dataset.current_published_version_id
				WHERE dataset.status='PUBLISHED'
				  AND dataset.deleted_at IS NULL
				  AND version.status='PUBLISHED'
				  AND version.layer='DWS'
			), activated AS (
				INSERT INTO platform.metric_extraction_jobs(
					tenant_id,dataset_id,dataset_version_id,dsl_hash,
					requested_by,extractor_version
				)
				SELECT tenant_id,dataset_id,dataset_version_id,schema_hash,
					$1::uuid,$2
				FROM candidates
				ON CONFLICT(tenant_id,dataset_version_id,extractor_version)
				DO UPDATE SET
					requested_by=EXCLUDED.requested_by,
					status='PENDING',total=0,ready_count=0,review_count=0,
					blocked_count=0,error_code='',error_message='',
					lease_owner='',lease_expires_at=NULL,heartbeat_at=NULL,
					attempt=0,next_attempt_at=now(),prepared_result=NULL,
					started_at=NULL,completed_at=NULL
				WHERE platform.metric_extraction_jobs.status IN (
					'SUCCEEDED','PARTIAL','FAILED'
				)
				RETURNING id
			)
			SELECT
				(SELECT count(*)::int FROM candidates),
				(SELECT count(*)::int FROM activated)`,
			actorID, CodeIdentificationVersion)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		if !rows.Next() {
			return ErrInvalidRequest
		}
		if err := rows.Scan(
			&result.EligibleDatasetCount, &result.EnqueuedJobCount,
		); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Release the result set before writing the audit row. pgx keeps the
		// transaction connection busy while Rows is open, even after its only
		// row has been scanned; attempting the audit INSERT first therefore
		// made every manual identification request fail with "conn busy" and
		// rolled back the newly enqueued jobs.
		rows.Close()
		result.DimensionDatasetCount, result.DimensionAssetCount, err =
			approveDWSFieldDimensionsTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		result.Datasets, err = loadIdentificationDatasetIndexesTx(ctx, tx)
		if err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(
			platform.current_tenant_id(),$1::uuid,
			'TRIGGER_METRIC_IDENTIFICATION','METRIC_CATALOG',$1::uuid,
			jsonb_build_object(
				'eligibleDatasetCount',$2::int,
				'enqueuedJobCount',$3::int,
				'historicalMetricCount',$4::int,
				'existingCandidateCount',$5::int
				,'dimensionDatasetCount',$6::int
				,'dimensionAssetCount',$7::int
			)
		)`, actorID, result.EligibleDatasetCount, result.EnqueuedJobCount,
			result.HistoricalMetricCount, result.ExistingCandidateCount,
			result.DimensionDatasetCount, result.DimensionAssetCount)
		return err
	})
	return result, err
}

// approveDWSFieldDimensionsTx uses only immutable, already-enriched DWS field metadata
// and approved sensitivity tags. It deliberately does not call an LLM or create
// survey candidates.
func approveDWSFieldDimensionsTx(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
) (datasetCount, assetCount int, err error) {
	err = tx.QueryRow(ctx, `WITH current_dws AS (
			SELECT dataset.tenant_id,dataset.id AS dataset_id,
				version.id AS dataset_version_id
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dataset.tenant_id
			 AND version.dataset_id=dataset.id
			 AND version.id=dataset.current_published_version_id
			 AND version.status='PUBLISHED'
			 AND version.layer='DWS'
			WHERE dataset.tenant_id=platform.current_tenant_id()
			  AND dataset.status='PUBLISHED'
			  AND dataset.deleted_at IS NULL
		), classified AS (
			SELECT current_dws.tenant_id,current_dws.dataset_id,
				current_dws.dataset_version_id,field.field_id,
				field.field_code::text AS code,field.field_name AS name,
				field.description,
				field.field_role,field.semantic_type,
				(
				  field.field_role='IDENTIFIER'
				  OR upper(field.semantic_type)='IDENTIFIER'
				) AS high_cardinality,
				EXISTS(
				  SELECT 1
				  FROM platform.asset_tag_bindings AS binding
				  JOIN platform.semantic_tags AS tag
				    ON tag.tenant_id=binding.tenant_id
				   AND tag.id=binding.tag_id
				   AND tag.category='SENSITIVITY'
				   AND tag.status='ACTIVE'
				  WHERE binding.tenant_id=field.tenant_id
				    AND binding.asset_type='DATASET_FIELD'
				    AND binding.dataset_id=current_dws.dataset_id
				    AND binding.dataset_version_id=field.dataset_version_id
				    AND binding.dataset_field_id=field.field_id
				    AND binding.status='APPROVED'
				) AS sensitive
			FROM current_dws
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=current_dws.tenant_id
			 AND field.dataset_version_id=current_dws.dataset_version_id
			WHERE field.field_role IN (
				'DIMENSION','ATTRIBUTE','TIME','IDENTIFIER'
			)
		), inserted AS (
			INSERT INTO platform.semantic_dimensions(
				tenant_id,dataset_id,dataset_version_id,field_id,
				code,name,description,dimension_type,member_index_policy,
				high_cardinality,sensitive,status,definition_hash,
				created_by,updated_by
			)
			SELECT classified.tenant_id,classified.dataset_id,
				classified.dataset_version_id,classified.field_id,
				classified.code,classified.name,
				classified.description,
				CASE WHEN classified.field_role='TIME'
				  THEN 'TIME' ELSE 'STANDARD' END,
				-- Programmatic approval establishes dimension identity immediately.
				-- Member enumeration remains fail-closed until a separate governed
				-- profile/index policy explicitly enables it.
				'NONE',
				classified.high_cardinality,classified.sensitive,
				'PUBLISHED',
				encode(public.digest(convert_to(concat_ws(E'\x1f',
					classified.dataset_id::text,
					classified.dataset_version_id::text,
					classified.field_id,classified.code,classified.name,
					classified.description,classified.field_role,classified.semantic_type,
					'NONE',
					classified.high_cardinality::text,
					classified.sensitive::text,'PUBLISHED'
				),'UTF8'),'sha256'),'hex'),
				$1::uuid,$1::uuid
			FROM classified
			ON CONFLICT(tenant_id,dataset_version_id,field_id) DO NOTHING
			RETURNING id,dataset_id,dataset_version_id,field_id
		), audited AS (
			INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			)
			SELECT platform.current_tenant_id(),$1::uuid,
				'PROGRAM_APPROVE_DWS_DIMENSION','SEMANTIC_DIMENSION',
				inserted.id::text,jsonb_build_object(
					'datasetId',inserted.dataset_id::text,
					'datasetVersionId',inserted.dataset_version_id::text,
					'fieldId',inserted.field_id,
					'classification','DATASET_FIELD_ROLE'
				)
			FROM inserted
		), retired_candidates AS (
			UPDATE platform.dimension_survey_candidates AS candidate
			SET status='STALE',version=candidate.version+1,
				decision_reason='PROGRAM_FIELD_CLASSIFICATION_ENABLED',
				updated_by=$1::uuid,updated_at=clock_timestamp()
			WHERE candidate.status='SUGGESTED'
			RETURNING candidate.id
		)
		SELECT
			(SELECT count(*)::int FROM current_dws),
			(SELECT count(*)::int FROM inserted)`,
		actorID,
	).Scan(&datasetCount, &assetCount)
	return datasetCount, assetCount, err
}

func loadIdentificationDatasetIndexesTx(
	ctx context.Context,
	tx pgx.Tx,
) ([]IdentificationDatasetIndex, error) {
	rows, err := tx.Query(ctx, `SELECT
			dataset.id::text,version.id::text,dataset.code::text,dataset.name,
			version.layer,platform.dataset_version_effective_domain(version.id)
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=dataset.tenant_id
		 AND version.dataset_id=dataset.id
		 AND version.id=dataset.current_published_version_id
		WHERE dataset.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL
		  AND version.status='PUBLISHED'
		  AND version.layer='DWS'
		ORDER BY version.layer,dataset.code,dataset.id`)
	if err != nil {
		return nil, err
	}
	items := []IdentificationDatasetIndex{}
	byVersion := map[string]int{}
	for rows.Next() {
		var item IdentificationDatasetIndex
		if err := rows.Scan(
			&item.DatasetID, &item.DatasetVersionID, &item.Code, &item.Name,
			&item.Layer, &item.Domain,
		); err != nil {
			rows.Close()
			return nil, err
		}
		item.Metrics = []IdentificationMetric{}
		item.Dimensions = []IdentificationDimension{}
		items = append(items, item)
		byVersion[item.DatasetVersionID] = len(items) - 1
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(items) == 0 {
		return items, nil
	}

	metricRows, err := tx.Query(ctx, `SELECT
			version.dataset_version_id::text,metric.code::text,metric.name,
			'PUBLISHED'::text,'METRIC_VERSION'::text,
			COALESCE(ARRAY(
			  SELECT dimension->>'fieldId'
			  FROM jsonb_array_elements(
			    COALESCE(version.definition_json->'allowedDimensions','[]'::jsonb)
			  ) AS dimension
			  WHERE COALESCE(dimension->>'fieldId','')<>''
			  ORDER BY dimension->>'fieldId'
			),'{}'::text[]),
			COALESCE(document.embedding_status,'PENDING')
		FROM platform.metric_versions AS version
		JOIN platform.metrics AS metric
		  ON metric.tenant_id=version.tenant_id
		 AND metric.id=version.metric_id
		 AND metric.current_published_version_id=version.id
		 AND metric.status='PUBLISHED'
		 AND metric.deleted_at IS NULL
		LEFT JOIN platform.metric_semantic_documents AS document
		  ON document.tenant_id=version.tenant_id
		 AND document.subject_type='METRIC_VERSION'
		 AND document.metric_version_id=version.id
		JOIN platform.dataset_versions AS dataset_version
		  ON dataset_version.tenant_id=version.tenant_id
		 AND dataset_version.id=version.dataset_version_id
		 AND dataset_version.status='PUBLISHED'
		 AND dataset_version.layer='DWS'
		ORDER BY version.dataset_version_id,metric.code`)
	if err != nil {
		return nil, err
	}
	for metricRows.Next() {
		var versionID string
		var metric IdentificationMetric
		if err := metricRows.Scan(
			&versionID, &metric.Code, &metric.Name, &metric.Status,
			&metric.Source, &metric.AllowedFieldIDs, &metric.VectorStatus,
		); err != nil {
			metricRows.Close()
			return nil, err
		}
		if index, ok := byVersion[versionID]; ok {
			items[index].Metrics = append(items[index].Metrics, metric)
		}
	}
	if err := metricRows.Err(); err != nil {
		metricRows.Close()
		return nil, err
	}
	metricRows.Close()

	candidateRows, err := tx.Query(ctx, `SELECT
			candidate.dataset_version_id::text,candidate.code::text,candidate.name,
			candidate.status,'CANDIDATE'::text,
			COALESCE(ARRAY(
			  SELECT dimension->>'fieldId'
			  FROM jsonb_array_elements(
			    COALESCE(candidate.proposed_definition->'allowedDimensions','[]'::jsonb)
			  ) AS dimension
			  WHERE COALESCE(dimension->>'fieldId','')<>''
			  ORDER BY dimension->>'fieldId'
			),'{}'::text[]),
			COALESCE(document.embedding_status,'PENDING')
		FROM platform.metric_candidates AS candidate
		JOIN platform.dataset_versions AS dataset_version
		  ON dataset_version.tenant_id=candidate.tenant_id
		 AND dataset_version.id=candidate.dataset_version_id
		 AND dataset_version.status='PUBLISHED'
		 AND dataset_version.layer='DWS'
		LEFT JOIN platform.metric_semantic_documents AS document
		  ON document.tenant_id=candidate.tenant_id
		 AND document.subject_type='CANDIDATE'
		 AND document.candidate_id=candidate.id
		WHERE candidate.status IN ('READY','NEEDS_REVIEW','BLOCKED')
		ORDER BY candidate.dataset_version_id,candidate.code,candidate.id`)
	if err != nil {
		return nil, err
	}
	for candidateRows.Next() {
		var versionID string
		var metric IdentificationMetric
		if err := candidateRows.Scan(
			&versionID, &metric.Code, &metric.Name, &metric.Status,
			&metric.Source, &metric.AllowedFieldIDs, &metric.VectorStatus,
		); err != nil {
			candidateRows.Close()
			return nil, err
		}
		if index, ok := byVersion[versionID]; ok {
			items[index].Metrics = append(items[index].Metrics, metric)
		}
	}
	if err := candidateRows.Err(); err != nil {
		candidateRows.Close()
		return nil, err
	}
	candidateRows.Close()

	dimensionRows, err := tx.Query(ctx, `SELECT
			field.dataset_version_id::text,field.field_id,field.field_code::text,
			field.field_name,COALESCE(dimension.member_index_policy,'NONE'),
			COALESCE(dimension.sensitive,false),
			COALESCE(member_totals.total,0)::int,
			COALESCE(member_totals.vectorized,0)::int,
			COALESCE(member_values.values,'{}'::text[])
		FROM platform.dataset_fields AS field
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=field.tenant_id
		 AND version.id=field.dataset_version_id
		 AND version.status='PUBLISHED'
		 AND version.layer='DWS'
		LEFT JOIN platform.semantic_dimensions AS dimension
		  ON dimension.tenant_id=field.tenant_id
		 AND dimension.dataset_version_id=field.dataset_version_id
		 AND dimension.field_id=field.field_id
		 AND dimension.status='PUBLISHED'
		LEFT JOIN LATERAL (
		  SELECT count(DISTINCT member.normalized_value) AS total,
		    count(DISTINCT semantic.dimension_member_id) FILTER(
		      WHERE semantic.embedding_status='SUCCEEDED'
		    ) AS vectorized
		  FROM platform.dimension_members AS member
		  LEFT JOIN platform.dimension_member_semantic_documents AS semantic
		    ON semantic.tenant_id=member.tenant_id
		   AND semantic.dimension_member_id=member.id
		  WHERE member.tenant_id=dimension.tenant_id
		    AND member.dimension_id=dimension.id
		    AND member.status='ACTIVE'
		    AND dimension.member_index_policy='FULL'
		    AND NOT dimension.sensitive
		    AND (member.valid_from IS NULL OR member.valid_from<=now())
		    AND (member.valid_to IS NULL OR member.valid_to>now())
		) AS member_totals ON true
		LEFT JOIN LATERAL (
		  SELECT array_agg(value.canonical_label ORDER BY value.canonical_label) AS values
		  FROM (
		    SELECT DISTINCT ON (member.normalized_value)
		      member.normalized_value,member.canonical_label
		    FROM platform.dimension_members AS member
		    WHERE member.tenant_id=dimension.tenant_id
		      AND member.dimension_id=dimension.id
		      AND member.status='ACTIVE'
		      AND dimension.member_index_policy='FULL'
		      AND NOT dimension.sensitive
		      AND (member.valid_from IS NULL OR member.valid_from<=now())
		      AND (member.valid_to IS NULL OR member.valid_to>now())
		    ORDER BY member.normalized_value,member.canonical_label
		    LIMIT 200
		  ) AS value
		) AS member_values ON true
		WHERE field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
		ORDER BY field.dataset_version_id,field.ordinal_position`)
	if err != nil {
		return nil, err
	}
	for dimensionRows.Next() {
		var versionID string
		var dimension IdentificationDimension
		if err := dimensionRows.Scan(
			&versionID, &dimension.FieldID, &dimension.Code, &dimension.Name,
			&dimension.MemberIndexPolicy, &dimension.Sensitive,
			&dimension.MemberValueCount, &dimension.VectorizedMemberCount,
			&dimension.MemberValues,
		); err != nil {
			dimensionRows.Close()
			return nil, err
		}
		dimension.ValuesTruncated = dimension.MemberValueCount > len(dimension.MemberValues)
		if index, ok := byVersion[versionID]; ok {
			items[index].Dimensions = append(items[index].Dimensions, dimension)
		}
	}
	if err := dimensionRows.Err(); err != nil {
		dimensionRows.Close()
		return nil, err
	}
	dimensionRows.Close()

	for index := range items {
		sort.Slice(items[index].Metrics, func(left, right int) bool {
			if items[index].Metrics[left].Code != items[index].Metrics[right].Code {
				return items[index].Metrics[left].Code < items[index].Metrics[right].Code
			}
			return items[index].Metrics[left].Source < items[index].Metrics[right].Source
		})
		document := struct {
			Domain           string                    `json:"domain"`
			DatasetVersionID string                    `json:"datasetVersionId"`
			Metrics          []IdentificationMetric    `json:"metrics"`
			Dimensions       []IdentificationDimension `json:"dimensions"`
			Retrieval        []string                  `json:"retrieval"`
		}{
			Domain: items[index].Domain, DatasetVersionID: items[index].DatasetVersionID,
			Metrics: items[index].Metrics, Dimensions: items[index].Dimensions,
			Retrieval: []string{
				"DOMAIN_PARTITION", "METRIC_VECTOR", "DIMENSION_MEMBER_INVERTED",
			},
		}
		raw, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			return nil, marshalErr
		}
		items[index].IndexDocument = raw
	}
	return items, nil
}

func (s *PostgresStore) ListAutomaticApprovalCandidates(
	ctx context.Context,
	tenantID string,
	limit int,
) (items []AutomaticApprovalCandidate, err error) {
	if s == nil || tenantID == "" || limit < 1 || limit > automaticApprovalBatchSize {
		return nil, ErrInvalidRequest
	}
	items = []AutomaticApprovalCandidate{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+candidateSelect+`,
				COALESCE(job.requested_by::text,version.published_by::text)
			FROM platform.metric_candidates AS candidate
			JOIN platform.metric_extraction_jobs AS job
			  ON job.tenant_id=candidate.tenant_id
			 AND job.id=candidate.job_id
			 AND job.extractor_version=$1
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=candidate.tenant_id
			 AND version.id=candidate.dataset_version_id
			 AND version.status='PUBLISHED'
			 AND version.layer='DWS'
			`+candidateSemanticJoin+`
			WHERE candidate.status IN ('READY','NEEDS_REVIEW')
			  AND cardinality(candidate.block_reasons)=0
			  AND COALESCE(job.requested_by,version.published_by) IS NOT NULL
			ORDER BY candidate.created_at,candidate.id
			LIMIT $2`,
			CodeIdentificationVersion, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item AutomaticApprovalCandidate
			if scanErr := scanCandidateWithTrailingActor(
				rows, &item.Candidate, &item.ActorID,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// LoadMetricSemanticContext returns reviewed catalog metadata for the exact physical
// tables and projected columns used by a dataset. It deliberately excludes samples,
// credentials and connection details.
func (s *PostgresStore) LoadMetricSemanticContext(
	ctx context.Context,
	tenantID string,
	requests []SemanticContextRequest,
) (items []SemanticTableContext, err error) {
	if tenantID == "" || len(requests) > 16 {
		return nil, ErrInvalidRequest
	}
	requestedColumns := map[string]map[string]bool{}
	tableIDs := make([]string, 0, len(requests))
	for _, request := range requests {
		tableID := strings.TrimSpace(request.TableID)
		if !canonicalUUID(tableID) {
			return nil, ErrInvalidRequest
		}
		if _, exists := requestedColumns[tableID]; exists {
			continue
		}
		columns := map[string]bool{}
		for _, column := range request.ColumnNames {
			column = strings.TrimSpace(column)
			if column != "" && len([]rune(column)) <= 128 {
				columns[column] = true
			}
		}
		requestedColumns[tableID] = columns
		tableIDs = append(tableIDs, tableID)
	}
	if len(tableIDs) == 0 {
		return []SemanticTableContext{}, nil
	}
	items = []SemanticTableContext{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT
			table_asset.id::text,
			COALESCE(NULLIF(table_asset.business_name,''),table_asset.table_name),
			COALESCE(table_asset.business_description,''),
			column_asset.column_name,
			COALESCE(NULLIF(column_asset.business_name,''),column_asset.column_name),
			COALESCE(column_asset.business_description,''),
			COALESCE(column_asset.semantic_type,''),
			COALESCE(column_asset.canonical_type,'')
		FROM platform.metadata_tables AS table_asset
		LEFT JOIN platform.metadata_columns AS column_asset
		  ON column_asset.tenant_id=table_asset.tenant_id AND column_asset.table_id=table_asset.id
		WHERE table_asset.id=ANY($1::uuid[])
		ORDER BY table_asset.id,column_asset.ordinal_position
		LIMIT 512`, tableIDs)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		index := map[string]int{}
		for rows.Next() {
			var tableID, tableName, tableDescription string
			var columnCode, columnName, columnDescription, semanticType, canonicalType *string
			if scanErr := rows.Scan(
				&tableID, &tableName, &tableDescription, &columnCode, &columnName,
				&columnDescription, &semanticType, &canonicalType,
			); scanErr != nil {
				return scanErr
			}
			position, exists := index[tableID]
			if !exists {
				position = len(items)
				index[tableID] = position
				items = append(items, SemanticTableContext{
					TableID: tableID, Name: tableName, Description: tableDescription,
					Columns: []SemanticColumnContext{},
				})
			}
			if columnCode == nil || !requestedColumns[tableID][*columnCode] {
				continue
			}
			items[position].Columns = append(items[position].Columns, SemanticColumnContext{
				Code: *columnCode, Name: dereferenceText(columnName),
				Description: dereferenceText(columnDescription), SemanticType: dereferenceText(semanticType),
				CanonicalType: dereferenceText(canonicalType),
			})
		}
		return rows.Err()
	})
	return items, err
}

func dereferenceText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// EnqueueDatasetMetricExtractionTx 与数据集发布复用同一事务，避免出现“发布成功但
// 未提取”或“未发布版本被提取”的双写裂缝。
func (s *PostgresStore) EnqueueDatasetMetricExtractionTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	version dataset.VersionRecord,
) error {
	if tenantID == "" || version.Status != "PUBLISHED" || version.DatasetID == "" || version.ID == "" || version.DSLHash == "" {
		return ErrInvalidRequest
	}
	if version.Layer == dataset.LayerADS {
		return nil
	}
	extractorVersion := JobVersion
	if version.Layer == dataset.LayerDWS {
		// DWS save-time semantic naming has already fixed field roles, names and
		// descriptions. Publish the dimension assets in the same transaction and
		// let the worker accept only explicit MEASURE fields.
		if _, _, err := approveDWSFieldDimensionsTx(ctx, tx, actorID); err != nil {
			return err
		}
		extractorVersion = CodeIdentificationVersion
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.metric_extraction_jobs(
		tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version,prepared_result
	) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,
		CASE WHEN $6=$7 THEN NULL ELSE (
			SELECT request.metric_candidate_result
			FROM platform.dataset_publication_requests AS request
			WHERE request.tenant_id=$1 AND request.dataset_id=$2
			  AND request.reserved_published_version_id=$3
			  AND request.status='PENDING'
			  AND request.metric_candidate_generation_status IN ('SUCCEEDED','PARTIAL')
		) END
	)
	ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING`,
		tenantID, version.DatasetID, version.ID, version.DSLHash, actorID,
		extractorVersion, CodeIdentificationVersion)
	return err
}

// JobClaim 是 worker 对一个精确发布版本的短期租约。
type JobClaim struct {
	ID               string
	TenantID         string
	DatasetID        string
	DatasetVersionID string
	DSLHash          string
	RequestedBy      string
	ExtractorVersion string
	PreparedResult   json.RawMessage
}

// ListJobTenantIDs 只读取未启用 RLS 的租户目录；实际 claim 和写入仍逐租户进入 RLS 事务。
func (s *PostgresStore) ListJobTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM platform.tenants WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimJob 以 SKIP LOCKED 和过期租约实现多 worker 安全恢复。
func (s *PostgresStore) ClaimJob(ctx context.Context, tenantID, workerID string, lease time.Duration) (claim *JobClaim, err error) {
	if tenantID == "" || workerID == "" || lease < time.Second {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// An expired third lease means the worker died without reaching FailJob. Close it before
		// claiming more work so crashes cannot bypass the three-attempt budget indefinitely.
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status='FAILED',error_code='LEASE_EXPIRED',
			error_message='worker lease expired after maximum attempts',completed_at=now(),
			heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			WHERE attempt>=3 AND (
				(status='RUNNING' AND lease_expires_at<=now())
				OR (status='PENDING' AND next_attempt_at<=now())
			)`); err != nil {
			return err
		}
		var item JobClaim
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id FROM platform.metric_extraction_jobs
			WHERE attempt<3 AND (
				(status='PENDING' AND next_attempt_at<=now())
				OR (status='RUNNING' AND lease_expires_at<=now())
			)
			ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		) UPDATE platform.metric_extraction_jobs AS job SET
			status='RUNNING',lease_owner=$1,
			lease_expires_at=now()+($2 * interval '1 second'),heartbeat_at=now(),
			attempt=attempt+1,started_at=COALESCE(started_at,now()),
			error_code='',error_message=''
		FROM candidate WHERE job.id=candidate.id
		RETURNING job.id::text,job.dataset_id::text,job.dataset_version_id::text,
			job.dsl_hash,COALESCE(job.requested_by::text,''),job.extractor_version,
			COALESCE(job.prepared_result,'null'::jsonb)`,
			workerID, int64(lease/time.Second)).
			Scan(&item.ID, &item.DatasetID, &item.DatasetVersionID, &item.DSLHash,
				&item.RequestedBy, &item.ExtractorVersion, &item.PreparedResult)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		item.TenantID = tenantID
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) LoadExactDatasetVersion(ctx context.Context, claim JobClaim) (LoadedDatasetVersion, error) {
	datasetStore := dataset.NewPostgresStore(s.pool)
	version, err := datasetStore.GetVersion(ctx, claim.TenantID, claim.DatasetID, claim.DatasetVersionID)
	if err != nil {
		return LoadedDatasetVersion{}, fmt.Errorf("load immutable dataset snapshot: %w", err)
	}
	if version.DSLHash != claim.DSLHash {
		return LoadedDatasetVersion{}, fmt.Errorf("metric extraction job dataset hash drift")
	}
	if err := datasetStore.ValidateVersionDependencies(ctx, claim.TenantID, claim.DatasetID, claim.DatasetVersionID); err != nil {
		// Preserve the immutable DSL for fact extraction when only runtime dependencies are
		// unavailable. The worker will persist candidates as BLOCKED instead of dropping the
		// published dataset from the metric-center inventory.
		if errors.Is(err, dataset.ErrVersionUnavailable) {
			return LoadedDatasetVersion{Version: version, DependencyUnavailable: true}, nil
		}
		return LoadedDatasetVersion{}, fmt.Errorf("validate dataset runtime dependencies: %w", err)
	}
	return LoadedDatasetVersion{Version: version}, nil
}

type persistedDraft struct {
	draft       CandidateDraft
	definition  json.RawMessage
	evidence    json.RawMessage
	lineage     json.RawMessage
	document    string
	confidence  float64
	assumptions []string
	method      string
}

// FinishJob 在一个事务内保存全部候选并收口任务；worker 崩溃不会留下部分候选批次。
func (s *PostgresStore) FinishJob(ctx context.Context, claim JobClaim, workerID string, result ExtractionResult) error {
	if result.DatasetID != claim.DatasetID || result.DatasetVersionID != claim.DatasetVersionID || result.DSLHash != claim.DSLHash ||
		(result.Status != TaskStatusSucceeded && result.Status != TaskStatusPartial) {
		return ErrInvalidRequest
	}
	persisted := make([]persistedDraft, 0, len(result.Candidates))
	dependencyBlocked := extractionBlockedByUnavailable(result)
	ready, review, blocked := 0, 0, 0
	for _, draft := range result.Candidates {
		draft.Semantic = normalizeSemanticForPersistence(draft, claim)
		definition, err := json.Marshal(draft.Definition)
		if err != nil {
			return err
		}
		evidenceItems := make([]CandidateEvidence, 0, len(draft.Evidence))
		for _, item := range draft.Evidence {
			evidenceItems = append(evidenceItems, CandidateEvidence{Property: item.Code, Source: item.Path, Detail: item.Value})
		}
		evidence, err := json.Marshal(evidenceItems)
		if err != nil {
			return err
		}
		lineage, err := json.Marshal(draft.Semantic.Lineage)
		if err != nil {
			return err
		}
		method := "RULE"
		if draft.Semantic.Source == "HYBRID" {
			method = "HYBRID"
		}
		assumptions := []string{}
		switch draft.Status {
		case CandidateStatusReady:
			ready++
		case CandidateStatusNeedsReview:
			review++
			assumptions = append(assumptions, "源字段没有可直接采用的显式度量聚合，当前聚合方式仅为待确认建议。")
		case CandidateStatusBlocked:
			blocked++
		default:
			return ErrInvalidRequest
		}
		persisted = append(persisted, persistedDraft{
			draft: draft, definition: definition, evidence: evidence, lineage: lineage,
			document: EmbeddingDocument(draft.Semantic), method: method,
			confidence: confidenceScore(draft.Confidence), assumptions: assumptions,
		})
	}

	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metric_extraction_jobs
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND lease_expires_at>now()
			  AND dataset_id=$3 AND dataset_version_id=$4 AND dsl_hash=$5
		)`, claim.ID, workerID, claim.DatasetID, claim.DatasetVersionID, claim.DSLHash).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return errors.New("metric extraction job lease was lost")
		}
		// The source can be disabled or made stale after extraction begins. Revalidate under
		// row locks. A snapshot already known to be unavailable may enter the inventory only
		// when every candidate is explicitly dependency-blocked and therefore unreviewable.
		if err := dataset.ValidateVersionDependenciesInTx(ctx, tx, claim.DatasetID, claim.DatasetVersionID); err != nil &&
			claim.ExtractorVersion != CodeIdentificationVersion {
			if !errors.Is(err, dataset.ErrVersionUnavailable) || !dependencyBlocked {
				return err
			}
		}
		for _, item := range persisted {
			definition := item.draft.Definition
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_candidates(
				tenant_id,job_id,dataset_id,dataset_version_id,dsl_hash,name,code,description,
				status,method,confidence,proposed_definition,source_field_ids,evidence,
				assumptions,warnings,block_reasons,fingerprint
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT(tenant_id,fingerprint) DO NOTHING`,
				claim.TenantID, claim.ID, claim.DatasetID, claim.DatasetVersionID, claim.DSLHash,
				item.draft.Semantic.Name, definition.Metric.Code, item.draft.Semantic.Description,
				item.draft.Status, item.method, item.confidence, item.definition, []string{item.draft.SourceFieldID},
				item.evidence, item.assumptions, item.draft.Warnings, item.draft.BlockReasons,
				item.draft.Fingerprint); err != nil {
				return err
			}
			var candidateID string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.metric_candidates
				WHERE tenant_id=$1 AND fingerprint=$2`, claim.TenantID, item.draft.Fingerprint).Scan(&candidateID); err != nil {
				return err
			}
			semantic := item.draft.Semantic
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_semantic_documents(
				tenant_id,subject_type,candidate_id,dataset_id,dataset_version_id,name,description,
				caliber,dimensions,period,period_description,lineage,lineage_summary,tags,document,
				semantic_source,llm_model,prompt_version,semantic_input_hash,ai_request_id,enrichment_error_code
			) VALUES($1,'CANDIDATE',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,'')::uuid,$20)
			ON CONFLICT(tenant_id,candidate_id) WHERE subject_type='CANDIDATE' DO UPDATE SET
				name=EXCLUDED.name,description=EXCLUDED.description,caliber=EXCLUDED.caliber,
				dimensions=EXCLUDED.dimensions,period=EXCLUDED.period,
				period_description=EXCLUDED.period_description,lineage=EXCLUDED.lineage,
				lineage_summary=EXCLUDED.lineage_summary,tags=EXCLUDED.tags,document=EXCLUDED.document,
				semantic_source=EXCLUDED.semantic_source,llm_model=EXCLUDED.llm_model,
				prompt_version=EXCLUDED.prompt_version,semantic_input_hash=EXCLUDED.semantic_input_hash,
				ai_request_id=EXCLUDED.ai_request_id,enrichment_error_code=EXCLUDED.enrichment_error_code,
				embedding=NULL,embedding_model='',embedding_input_hash='',embedding_status='PENDING',
				embedding_attempt=0,embedding_error_code='',next_attempt_at=now(),lease_owner='',
				lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
			WHERE platform.metric_semantic_documents.semantic_source<>'HYBRID'`,
				claim.TenantID, candidateID, claim.DatasetID, claim.DatasetVersionID,
				semantic.Name, semantic.Description, semantic.Caliber, semantic.Dimensions,
				semantic.Period, semantic.PeriodDescription, item.lineage, semantic.LineageSummary,
				semantic.Tags, item.document, semantic.Source, semantic.Model, semantic.PromptVersion,
				semantic.InputHash, semantic.RequestID, semantic.ErrorCode); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status=$1,total=$2,ready_count=$3,review_count=$4,blocked_count=$5,
			completed_at=now(),heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			WHERE id=$6 AND status='RUNNING' AND lease_owner=$7 AND lease_expires_at>now()`,
			result.Status, len(persisted), ready, review, blocked, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric extraction job finalization lost its lease")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,NULLIF($2,'')::uuid,'EXTRACT_METRIC_CANDIDATES','DATASET',$3,
			jsonb_build_object('jobId',$4::text,'datasetVersionId',$5::text,'dslHash',$6::text,
			'total',$7::int,'ready',$8::int,'needsReview',$9::int,'blocked',$10::int))`,
			claim.TenantID, claim.RequestedBy, claim.DatasetID, claim.ID, claim.DatasetVersionID,
			claim.DSLHash, len(persisted), ready, review, blocked)
		return err
	})
}

func (s *PostgresStore) FailJob(ctx context.Context, claim JobClaim, workerID, code, message string) error {
	message = strings.ToValidUTF8(message, "�")
	if runes := []rune(message); len(runes) > 2000 {
		message = string(runes[:2000])
	}
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status=CASE WHEN attempt>=3 THEN 'FAILED' ELSE 'PENDING' END,
			error_code=$1,error_message=$2,
			next_attempt_at=CASE WHEN attempt=1 THEN now()+interval '30 seconds'
				WHEN attempt=2 THEN now()+interval '2 minutes' ELSE next_attempt_at END,
			completed_at=CASE WHEN attempt>=3 THEN now() ELSE NULL END,heartbeat_at=now(),
			lease_owner='',lease_expires_at=NULL
			WHERE id=$3 AND status='RUNNING' AND lease_owner=$4`,
			code, message, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric extraction job failure update lost its lease")
		}
		return nil
	})
}

const candidateSelect = `candidate.id::text,candidate.dataset_id::text,candidate.dataset_version_id::text,
	candidate.dsl_hash,candidate.name,candidate.code::text,candidate.description,candidate.status,
	candidate.method,candidate.confidence::float8,candidate.proposed_definition,candidate.source_field_ids,
	candidate.evidence,candidate.assumptions,candidate.warnings,candidate.block_reasons,candidate.fingerprint,
	candidate.version,COALESCE(candidate.accepted_metric_id::text,''),candidate.decision_reason,
	to_char(candidate.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	to_char(candidate.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	COALESCE(semantic.name,candidate.name),COALESCE(semantic.description,candidate.description),
	COALESCE(semantic.caliber,''),COALESCE(semantic.dimensions,'{}'::text[]),
	COALESCE(semantic.period,'NONE'),COALESCE(semantic.period_description,'无固定统计周期'),
	COALESCE(semantic.lineage,'{}'::jsonb),COALESCE(semantic.lineage_summary,''),
	COALESCE(semantic.tags,'{}'::text[]),COALESCE(semantic.semantic_source,'RULE'),
	COALESCE(semantic.llm_model,''),COALESCE(semantic.prompt_version,''),
	COALESCE(semantic.semantic_input_hash,''),COALESCE(semantic.ai_request_id::text,''),
	COALESCE(semantic.enrichment_error_code,'')`

const candidateSemanticJoin = ` LEFT JOIN platform.metric_semantic_documents AS semantic
	ON semantic.tenant_id=candidate.tenant_id AND semantic.subject_type='CANDIDATE'
	AND semantic.candidate_id=candidate.id `

func (s *PostgresStore) List(ctx context.Context, tenantID string, filter ListFilter) (items []Candidate, total int, err error) {
	items = []Candidate{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM platform.metric_candidates AS candidate
			JOIN platform.metric_extraction_jobs AS job
			  ON job.tenant_id=candidate.tenant_id AND job.id=candidate.job_id
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=candidate.tenant_id
			 AND version.id=candidate.dataset_version_id
			WHERE ($1='' OR candidate.status=$1)
			  AND ($2='' OR candidate.dataset_id::text=$2)
				  AND (version.layer IN ('DIM','DWD')
				    OR job.extractor_version IN (
				      'metric-candidate-manual-v1','metric-candidate-code-v1'
				    ))
			  AND NOT(candidate.status='BLOCKED' AND candidate.block_reasons &&
			    ARRAY['AGGREGATED_DATASET_UNSUPPORTED','PRE_AGGREGATION_UNSUPPORTED',
			          'AGGREGATE_EXPRESSION_UNSUPPORTED']::text[])`,
			filter.Status, filter.DatasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+candidateSelect+`
			FROM platform.metric_candidates AS candidate
			JOIN platform.metric_extraction_jobs AS job
			  ON job.tenant_id=candidate.tenant_id AND job.id=candidate.job_id
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=candidate.tenant_id
			 AND version.id=candidate.dataset_version_id`+candidateSemanticJoin+`
			WHERE ($1='' OR candidate.status=$1) AND ($2='' OR candidate.dataset_id::text=$2)
				  AND (version.layer IN ('DIM','DWD')
				    OR job.extractor_version IN (
				      'metric-candidate-manual-v1','metric-candidate-code-v1'
				    ))
			  AND NOT(candidate.status='BLOCKED' AND candidate.block_reasons &&
			    ARRAY['AGGREGATED_DATASET_UNSUPPORTED','PRE_AGGREGATION_UNSUPPORTED',
			          'AGGREGATE_EXPRESSION_UNSUPPORTED']::text[])
			ORDER BY candidate.updated_at DESC,candidate.id LIMIT $3 OFFSET $4`,
			filter.Status, filter.DatasetID, filter.Limit, filter.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Candidate
			if err := scanCandidate(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (candidate Candidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, ErrNotFound
	}
	return candidate, err
}

func (s *PostgresStore) Reject(ctx context.Context, tenantID, actorID, id string, input RejectInput) (candidate Candidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var status string
		var version int64
		var priorReason string
		err := tx.QueryRow(ctx, `SELECT status,version,decision_reason FROM platform.metric_candidates WHERE id::text=$1 FOR UPDATE`, id).
			Scan(&status, &version, &priorReason)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == string(CandidateStatusRejected) && priorReason == input.Reason {
			return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
		}
		if status == string(CandidateStatusAccepted) || status == string(CandidateStatusRejected) {
			return ErrNotReviewable
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_candidates SET
			status='REJECTED',decision_reason=$1,reviewed_by=$2,reviewed_at=now(),
			version=version+1,updated_at=now() WHERE id::text=$3`, input.Reason, actorID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'REJECT','METRIC_CANDIDATE',$3,jsonb_build_object('reason',$4::text,'fromVersion',$5::bigint))`,
			tenantID, actorID, id, input.Reason, version); err != nil {
			return err
		}
		return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
	})
	return candidate, err
}

func scanCandidate(row interface{ Scan(...any) error }, candidate *Candidate) error {
	return scanCandidateWithTrailingActor(row, candidate, nil)
}

func scanCandidateWithTrailingActor(
	row interface{ Scan(...any) error },
	candidate *Candidate,
	actorID *string,
) error {
	var status string
	var evidenceRaw json.RawMessage
	var lineageRaw json.RawMessage
	destinations := []any{
		&candidate.ID, &candidate.DatasetID, &candidate.DatasetVersionID, &candidate.DSLHash,
		&candidate.Name, &candidate.Code, &candidate.Description, &status, &candidate.Method,
		&candidate.Confidence, &candidate.ProposedDefinition, &candidate.SourceFieldIDs,
		&evidenceRaw, &candidate.Assumptions, &candidate.Warnings, &candidate.BlockReasons,
		&candidate.Fingerprint, &candidate.Version, &candidate.AcceptedMetricID,
		&candidate.DecisionReason, &candidate.CreatedAt, &candidate.UpdatedAt,
		&candidate.Semantic.Name, &candidate.Semantic.Description, &candidate.Semantic.Caliber,
		&candidate.Semantic.Dimensions, &candidate.Semantic.Period, &candidate.Semantic.PeriodDescription,
		&lineageRaw, &candidate.Semantic.LineageSummary, &candidate.Semantic.Tags,
		&candidate.Semantic.Source, &candidate.Semantic.Model, &candidate.Semantic.PromptVersion,
		&candidate.Semantic.InputHash, &candidate.Semantic.RequestID, &candidate.Semantic.ErrorCode,
	}
	if actorID != nil {
		destinations = append(destinations, actorID)
	}
	if err := row.Scan(destinations...); err != nil {
		return err
	}
	candidate.Status = CandidateStatus(status)
	if err := json.Unmarshal(evidenceRaw, &candidate.Evidence); err != nil {
		return err
	}
	if err := json.Unmarshal(lineageRaw, &candidate.Semantic.Lineage); err != nil {
		return err
	}
	if candidate.SourceFieldIDs == nil {
		candidate.SourceFieldIDs = []string{}
	}
	if candidate.Evidence == nil {
		candidate.Evidence = []CandidateEvidence{}
	}
	if candidate.Assumptions == nil {
		candidate.Assumptions = []string{}
	}
	if candidate.Warnings == nil {
		candidate.Warnings = []string{}
	}
	if candidate.BlockReasons == nil {
		candidate.BlockReasons = []string{}
	}
	if candidate.Semantic.Dimensions == nil {
		candidate.Semantic.Dimensions = []string{}
	}
	if candidate.Semantic.Tags == nil {
		candidate.Semantic.Tags = []string{}
	}
	return nil
}

func normalizeSemanticForPersistence(draft CandidateDraft, claim JobClaim) SemanticMetadata {
	semantic := draft.Semantic
	if strings.TrimSpace(semantic.Name) == "" {
		semantic.Name = draft.Definition.Metric.Name
	}
	if strings.TrimSpace(semantic.Description) == "" {
		semantic.Description = draft.Definition.Metric.Description
	}
	if strings.TrimSpace(semantic.Caliber) == "" {
		semantic.Caliber = fmt.Sprintf("基于字段 %s，按 %s 聚合；空值处理为 %s", draft.SourceFieldCode, draft.Definition.Aggregation, draft.Definition.NullHandling)
	}
	if semantic.Dimensions == nil {
		semantic.Dimensions = []string{}
		for _, dimension := range draft.Definition.AllowedDimensions {
			semantic.Dimensions = append(semantic.Dimensions, dimension.Name)
		}
	}
	if semantic.Period == "" {
		semantic.Period = draft.Definition.TimeGrain
		if semantic.Period == "" {
			semantic.Period = "NONE"
		}
	}
	if semantic.PeriodDescription == "" {
		semantic.PeriodDescription = periodDescription(semantic.Period)
	}
	if semantic.Lineage.DatasetID == "" {
		semantic.Lineage = LineageMetadata{
			DatasetID: claim.DatasetID, DatasetVersionID: claim.DatasetVersionID,
			SourceFieldID: draft.SourceFieldID, Aggregation: draft.Definition.Aggregation,
			DimensionFieldIDs: []string{}, DependencyMetricVersionIDs: []string{},
		}
		for _, dimension := range draft.Definition.AllowedDimensions {
			semantic.Lineage.DimensionFieldIDs = append(semantic.Lineage.DimensionFieldIDs, dimension.FieldID)
		}
	}
	if semantic.LineageSummary == "" {
		semantic.LineageSummary = fmt.Sprintf("来自发布数据集版本 %s 的字段 %s，按 %s 聚合", claim.DatasetVersionID, draft.SourceFieldCode, draft.Definition.Aggregation)
	}
	semantic.Tags = nonEmptyUnique(append(semantic.Tags, semantic.Name, draft.Definition.Metric.Code, draft.Definition.Aggregation), 16, 32)
	if semantic.Source == "" {
		semantic.Source = "RULE"
	}
	if semantic.PromptVersion == "" {
		semantic.PromptVersion = MetricEnrichmentPromptVersion
	}
	if semantic.InputHash == "" {
		document := EmbeddingDocument(semantic)
		sum := sha256.Sum256([]byte(document))
		semantic.InputHash = hex.EncodeToString(sum[:])
	}
	return semantic
}

func confidenceScore(confidence Confidence) float64 {
	switch confidence {
	case ConfidenceHigh:
		return 0.95
	case ConfidenceMedium:
		return 0.75
	default:
		return 0.45
	}
}
