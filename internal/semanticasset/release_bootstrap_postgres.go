package semanticasset

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) LoadLegacyReleaseSnapshot(
	ctx context.Context,
	tenantID string,
) (snapshot legacyReleaseSnapshot, err error) {
	snapshot = legacyReleaseSnapshot{
		Metrics: []legacyMetric{}, Datasets: []legacyDataset{},
		Dimensions: []legacyDimension{}, Compatibilities: []legacyCompatibility{},
		Members: []legacyDimensionMember{}, RoleCodes: []string{},
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := loadLegacyReleaseDatasets(ctx, tx, &snapshot); err != nil {
			return err
		}
		if err := loadLegacyReleaseMetrics(ctx, tx, &snapshot); err != nil {
			return err
		}
		if err := loadLegacyReleaseDimensions(ctx, tx, &snapshot); err != nil {
			return err
		}
		if err := loadLegacyReleaseCompatibilities(ctx, tx, &snapshot); err != nil {
			return err
		}
		if err := loadLegacyReleaseMembers(ctx, tx, &snapshot); err != nil {
			return err
		}
		return loadLegacyReleaseRoles(ctx, tx, &snapshot)
	})
	return snapshot, err
}

func loadLegacyReleaseDatasets(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT dataset.id::text,version.id::text,
		dataset.code::text,dataset.name,dataset.description,
		COALESCE(dataset.domain_id::text,''),
		COALESCE(version.published_by,dataset.updated_by,dataset.created_by)::text,
		version.version_no,version.published_at,version.dsl_json,
		COALESCE(materialization.published_schema,materialization.physical_schema,''),
		COALESCE(materialization.published_name,materialization.physical_name,'')
	FROM platform.datasets AS dataset
	JOIN platform.dataset_versions AS version
	  ON version.tenant_id=dataset.tenant_id
	 AND version.id=dataset.current_published_version_id
	LEFT JOIN LATERAL(
	  SELECT item.published_schema,item.physical_schema,
	    item.published_name,item.physical_name
	  FROM platform.dataset_materializations AS item
	  WHERE item.tenant_id=dataset.tenant_id
	    AND item.dataset_version_id=version.id AND item.status='ACTIVE'
	  ORDER BY item.activated_at DESC NULLS LAST,item.created_at DESC,item.id
	  LIMIT 1
	) AS materialization ON true
	WHERE dataset.tenant_id=platform.current_tenant_id()
	  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
	  AND version.status='PUBLISHED'
	ORDER BY dataset.code,dataset.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item legacyDataset
		var raw []byte
		if err := rows.Scan(
			&item.ID, &item.VersionID, &item.Code, &item.Name, &item.Description,
			&item.DomainID, &item.OwnerID, &item.VersionNo, &item.PublishedAt, &raw,
			&item.PhysicalSchema, &item.PhysicalName,
		); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &item.DSL); err != nil || item.DSL == nil {
			return ErrInvalidRequest
		}
		snapshot.Datasets = append(snapshot.Datasets, item)
	}
	return rows.Err()
}

func loadLegacyReleaseMetrics(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT metric.id::text,version.id::text,
		metric.dataset_id::text,version.dataset_version_id::text,
		metric.code::text,metric.name,metric.description,metric.metric_type,
		COALESCE(metric.domain_id::text,''),
		COALESCE(version.published_by,metric.updated_by,metric.created_by)::text,
		version.version_no,version.published_at,version.definition_json
	FROM platform.metrics AS metric
	JOIN platform.metric_versions AS version
	  ON version.tenant_id=metric.tenant_id
	 AND version.id=metric.current_published_version_id
	WHERE metric.tenant_id=platform.current_tenant_id()
	  AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
	  AND version.status='PUBLISHED'
	ORDER BY metric.code,metric.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item legacyMetric
		var raw []byte
		if err := rows.Scan(
			&item.ID, &item.VersionID, &item.DatasetID, &item.DatasetVersionID,
			&item.Code, &item.Name, &item.Description, &item.MetricType,
			&item.DomainID, &item.OwnerID, &item.VersionNo, &item.PublishedAt, &raw,
		); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &item.Definition); err != nil || item.Definition == nil {
			return ErrInvalidRequest
		}
		snapshot.Metrics = append(snapshot.Metrics, item)
	}
	return rows.Err()
}

func loadLegacyReleaseDimensions(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT dimension.id::text,
		dimension.dataset_id::text,dimension.dataset_version_id::text,
		dimension.field_id,dimension.code::text,dimension.name,
		dimension.description,dimension.dimension_type,
		dimension.definition_hash,COALESCE(dimension.domain_id::text,''),
		dimension.updated_by::text,dimension.sensitive,dimension.high_cardinality,
		dimension.member_index_policy,dimension.updated_at
	FROM platform.semantic_dimensions AS dimension
	JOIN platform.datasets AS dataset
	  ON dataset.tenant_id=dimension.tenant_id
	 AND dataset.id=dimension.dataset_id
	 AND dataset.current_published_version_id=dimension.dataset_version_id
	WHERE dimension.tenant_id=platform.current_tenant_id()
	  AND dimension.status='PUBLISHED'
	  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
	ORDER BY dimension.code,dimension.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item legacyDimension
		if err := rows.Scan(
			&item.ID, &item.DatasetID, &item.DatasetVersionID, &item.FieldID,
			&item.Code, &item.Name, &item.Description, &item.DimensionType,
			&item.DefinitionHash, &item.DomainID, &item.OwnerID,
			&item.Sensitive, &item.HighCardinality, &item.MemberIndexPolicy,
			&item.UpdatedAt,
		); err != nil {
			return err
		}
		snapshot.Dimensions = append(snapshot.Dimensions, item)
	}
	return rows.Err()
}

func loadLegacyReleaseCompatibilities(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT compatibility.metric_version_id::text,
		compatibility.dimension_id::text,compatibility.fanout_policy
	FROM platform.dimension_metric_compatibility AS compatibility
	JOIN platform.metrics AS metric
	  ON metric.tenant_id=compatibility.tenant_id
	 AND metric.id=compatibility.metric_id
	 AND metric.current_published_version_id=compatibility.metric_version_id
	JOIN platform.semantic_dimensions AS dimension
	  ON dimension.tenant_id=compatibility.tenant_id
	 AND dimension.id=compatibility.dimension_id
	WHERE compatibility.tenant_id=platform.current_tenant_id()
	  AND compatibility.status='VERIFIED'
	  AND compatibility.fanout_policy<>'UNSAFE'
	  AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
	  AND dimension.status='PUBLISHED'
	ORDER BY compatibility.metric_version_id,compatibility.dimension_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		item := legacyCompatibility{Cardinality: "NOT_APPLICABLE"}
		if err := rows.Scan(&item.MetricVersionID, &item.DimensionID, &item.FanoutPolicy); err != nil {
			return err
		}
		snapshot.Compatibilities = append(snapshot.Compatibilities, item)
	}
	return rows.Err()
}

func loadLegacyReleaseMembers(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT member.id::text,member.dimension_id::text,
		member.member_key,member.canonical_label,member.valid_from,member.valid_to,
		COALESCE(array_agg(alias.alias ORDER BY alias.alias)
		  FILTER(WHERE alias.id IS NOT NULL),'{}'::text[])
	FROM platform.dimension_members AS member
	JOIN platform.semantic_dimensions AS dimension
	  ON dimension.tenant_id=member.tenant_id
	 AND dimension.id=member.dimension_id AND dimension.status='PUBLISHED'
	LEFT JOIN platform.dimension_member_aliases AS alias
	  ON alias.tenant_id=member.tenant_id
	 AND alias.dimension_id=member.dimension_id
	 AND alias.dimension_member_id=member.id
	 AND (alias.valid_from IS NULL OR alias.valid_from<=now())
	 AND (alias.valid_to IS NULL OR alias.valid_to>now())
	WHERE member.tenant_id=platform.current_tenant_id()
	  AND member.status='ACTIVE'
	  AND (member.valid_from IS NULL OR member.valid_from<=now())
	  AND (member.valid_to IS NULL OR member.valid_to>now())
	GROUP BY member.id,member.dimension_id,member.member_key,
		member.canonical_label,member.valid_from,member.valid_to
	ORDER BY member.dimension_id,member.member_key,member.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item legacyDimensionMember
		if err := rows.Scan(
			&item.ID, &item.DimensionID, &item.MemberKey, &item.CanonicalLabel,
			&item.ValidFrom, &item.ValidTo, &item.Aliases,
		); err != nil {
			return err
		}
		snapshot.Members = append(snapshot.Members, item)
	}
	return rows.Err()
}

func loadLegacyReleaseRoles(ctx context.Context, tx pgx.Tx, snapshot *legacyReleaseSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT role.code::text
	FROM platform.roles AS role
	WHERE role.tenant_id=platform.current_tenant_id()
	  AND role.status='ACTIVE' AND role.deleted_at IS NULL
	  AND EXISTS(
	    SELECT 1 FROM platform.role_permissions AS binding
	    JOIN platform.permissions AS permission
	      ON permission.tenant_id=binding.tenant_id
	     AND permission.id=binding.permission_id
	    WHERE binding.tenant_id=role.tenant_id AND binding.role_id=role.id
	      AND permission.resource_type='METRIC' AND permission.action='READ'
	  )
	  AND EXISTS(
	    SELECT 1 FROM platform.role_permissions AS binding
	    JOIN platform.permissions AS permission
	      ON permission.tenant_id=binding.tenant_id
	     AND permission.id=binding.permission_id
	    WHERE binding.tenant_id=role.tenant_id AND binding.role_id=role.id
	      AND permission.resource_type='DATASET' AND permission.action='READ'
	  )
	ORDER BY role.code`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return err
		}
		snapshot.RoleCodes = append(snapshot.RoleCodes, code)
	}
	return rows.Err()
}

var _ semanticReleaseBootstrapStore = (*PostgresStore)(nil)
