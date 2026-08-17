package registryimport

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

func loadSemanticReferences(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	snapshot *ValidationSnapshot,
) error {
	rows, err := tx.Query(ctx, `WITH
	latest_models AS (
		SELECT model.*,row_number() OVER(
			PARTITION BY model.model_id ORDER BY model.version_no DESC,model.id DESC
		) AS rank
		FROM askdata.semantic_models AS model WHERE model.domain_id=$1
	), latest_entities AS (
		SELECT entity.*,row_number() OVER(
			PARTITION BY entity.entity_id ORDER BY entity.version_no DESC,entity.id DESC
		) AS rank
		FROM askdata.entities AS entity WHERE entity.domain_id=$1
	), latest_measures AS (
		SELECT measure.*,row_number() OVER(
			PARTITION BY measure.measure_id ORDER BY measure.version_no DESC,measure.id DESC
		) AS rank
		FROM askdata.measures AS measure WHERE measure.domain_id=$1
	), latest_metric_versions AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.metric_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.metric_versions AS version WHERE version.domain_id=$1
	), latest_dimensions AS (
		SELECT dimension.*,row_number() OVER(
			PARTITION BY dimension.dimension_id ORDER BY dimension.version_no DESC,dimension.id DESC
		) AS rank
		FROM askdata.dimensions AS dimension WHERE dimension.domain_id=$1
	), latest_hierarchies AS (
		SELECT hierarchy.*,row_number() OVER(
			PARTITION BY hierarchy.hierarchy_id ORDER BY hierarchy.version_no DESC,hierarchy.id DESC
		) AS rank
		FROM askdata.hierarchies AS hierarchy WHERE hierarchy.domain_id=$1
	), latest_terms AS (
		SELECT term.*,identity.term AS governed_term,
			term.business_term_id AS term_id,row_number() OVER(
			PARTITION BY term.business_term_id ORDER BY term.version_no DESC,term.id DESC
		) AS rank
		FROM askdata.business_term_versions AS term
		JOIN askdata.business_terms AS identity
		  ON identity.id=term.business_term_id AND identity.tenant_id=term.tenant_id
		 AND identity.domain_id=term.domain_id
		WHERE term.domain_id=$1
	), latest_time_contracts AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.time_contract_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.time_contract_versions AS version WHERE version.domain_id=$1
	), reference_catalog AS (
		SELECT 'MODEL'::text AS kind,model.code::text AS code,model.name,
			model.model_id AS stable_id,model.status,
			false AS high_cardinality,''::text AS model_code,model.dataset_version_id::text,
			COALESCE(contract.supported_grains,'{}'::text[]) AS time_grains
		FROM latest_models AS model
		LEFT JOIN askdata.time_contract_versions AS contract
		  ON contract.id=model.time_contract_version_id
		 AND contract.domain_id=model.domain_id AND contract.tenant_id=model.tenant_id
		WHERE model.rank=1 AND model.status<>'DEPRECATED'
		UNION ALL
		SELECT 'ENTITY',entity.code::text,entity.name,entity.entity_id,entity.status,
			false,''::text,''::text,'{}'::text[]
		FROM latest_entities AS entity
		WHERE entity.rank=1 AND entity.status<>'DEPRECATED'
		UNION ALL
		SELECT 'MEASURE',measure.code::text,measure.name,measure.measure_id,measure.status,
			false,model.code::text,model.dataset_version_id::text,'{}'::text[]
		FROM latest_measures AS measure
		JOIN askdata.semantic_models AS model ON model.id=measure.semantic_model_version_id
		WHERE measure.rank=1 AND measure.status<>'DEPRECATED'
		UNION ALL
		SELECT 'METRIC',metric.code::text,metric.name,metric.id,version.status,
			false,model.code::text,model.dataset_version_id::text,'{}'::text[]
		FROM askdata.metrics AS metric
		JOIN latest_metric_versions AS version ON version.metric_id=metric.id AND version.rank=1
		JOIN askdata.semantic_models AS model ON model.id=version.semantic_model_version_id
		WHERE metric.domain_id=$1 AND metric.status<>'DEPRECATED' AND version.status<>'DEPRECATED'
		UNION ALL
		SELECT 'DIMENSION',dimension.code::text,dimension.name,dimension.dimension_id,dimension.status,
			dimension.high_cardinality,model.code::text,model.dataset_version_id::text,'{}'::text[]
		FROM latest_dimensions AS dimension
		JOIN askdata.semantic_models AS model ON model.id=dimension.semantic_model_version_id
		WHERE dimension.rank=1 AND dimension.status<>'DEPRECATED'
		UNION ALL
		SELECT 'HIERARCHY',hierarchy.code::text,hierarchy.name,hierarchy.hierarchy_id,hierarchy.status,
			false,''::text,''::text,'{}'::text[]
		FROM latest_hierarchies AS hierarchy
		WHERE hierarchy.rank=1 AND hierarchy.status<>'DEPRECATED'
		UNION ALL
		SELECT 'TERM',term.governed_term,term.name,term.term_id,term.status,
			false,''::text,''::text,'{}'::text[]
		FROM latest_terms AS term
		WHERE term.rank=1 AND term.status<>'DEPRECATED'
		UNION ALL
		SELECT 'KNOWLEDGE',knowledge.code::text,knowledge.name,knowledge.term_id,knowledge.status,
			false,''::text,''::text,'{}'::text[]
		FROM latest_terms AS knowledge
		WHERE knowledge.rank=1 AND knowledge.status<>'DEPRECATED'
		  AND knowledge.knowledge_kind<>'ALIAS'
		UNION ALL
		SELECT 'TIME_CONTRACT',contract.code::text,contract.name,contract.id,version.status,
			false,''::text,''::text,version.supported_grains
		FROM askdata.time_contracts AS contract
		JOIN latest_time_contracts AS version
		  ON version.time_contract_id=contract.id AND version.rank=1
		WHERE contract.domain_id=$1 AND version.status<>'DEPRECATED'
	)
	SELECT kind,code,name,stable_id::text,status,high_cardinality,model_code,dataset_version_id,time_grains
	FROM reference_catalog
	ORDER BY kind,code,(status='CERTIFIED') DESC,stable_id`, domainID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var reference ValidationReference
		var status string
		if err := rows.Scan(
			&reference.Kind, &reference.Code, &reference.Name, &reference.ID, &status,
			&reference.HighCardinality, &reference.ModelCode, &reference.DatasetVersionID, &reference.TimeGrains,
		); err != nil {
			return err
		}
		reference.Certified = status == "CERTIFIED"
		reference.Active = reference.Certified
		if reference.Kind == "METRIC" {
			// Metric version certification is the immutable usable boundary. The
			// parent metric may remain DRAFT while a new draft version is edited.
			reference.Active = reference.Certified
		}
		putReference(snapshot, reference)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return loadAuthoritativeDefinitions(ctx, tx, domainID, snapshot)
}

// loadAuthoritativeDefinitions 载入域内既有 AUTHORITATIVE DEFINES 词条的目标
// 与持有者 code，供 L4 强制“同一目标至多一个权威定义”。
func loadAuthoritativeDefinitions(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	snapshot *ValidationSnapshot,
) error {
	rows, err := tx.Query(ctx, `WITH latest AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.business_term_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.business_term_versions AS version WHERE version.domain_id=$1
	)
	SELECT target_object_type,target_code,code::text FROM latest
	WHERE rank=1 AND status<>'DEPRECATED'
	  AND authority='AUTHORITATIVE' AND relation='DEFINES' AND target_code<>''
	ORDER BY target_object_type,target_code`, domainID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var targetType, targetCode, code string
		if err := rows.Scan(&targetType, &targetCode, &code); err != nil {
			return err
		}
		if targetType == "TIME_CONTRACT" {
			targetType = "TIME"
		}
		key := targetType + "\x00" + canonicalLookup(targetCode)
		if _, exists := snapshot.AuthoritativeDefinitions[key]; !exists {
			snapshot.AuthoritativeDefinitions[key] = canonicalLookup(code)
		}
	}
	return rows.Err()
}

func (catalog *PostgresValidationCatalog) ResolveImportMemberTargets(
	ctx context.Context,
	tenantID, domainID string,
	targets []string,
) (map[string]ValidationReference, error) {
	if catalog == nil || catalog.pool == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || len(targets) == 0 || len(targets) > maxPreparedImportRows {
		return nil, ErrImportValidationCatalog
	}
	result := map[string]ValidationReference{}
	err := database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		for _, target := range targets {
			dimensionCode, memberValue, err := parseMemberTargetCode(target)
			if err != nil {
				continue
			}
			var dimensionVersionID string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM askdata.dimensions
				WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED'
				ORDER BY version_no DESC,id DESC LIMIT 1`, domainID, dimensionCode).
				Scan(&dimensionVersionID); err != nil {
				if err == pgx.ErrNoRows {
					continue
				}
				return err
			}
			var versionID, objectID string
			err = tx.QueryRow(ctx, `SELECT member_version_id::text,member_id::text
				FROM askdata.resolve_governed_import_member($1,$2,$3)`, domainID,
				dimensionVersionID, boundValueHash(dimensionVersionID, memberValue)).
				Scan(&versionID, &objectID)
			if err == pgx.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			result[target] = ValidationReference{
				Kind: "MEMBER", Code: target, Name: target, ID: objectID,
				Active: true, Certified: true,
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrImportValidationCatalog, err)
	}
	return result, nil
}

func loadValidationOwners(
	ctx context.Context,
	tx pgx.Tx,
	snapshot *ValidationSnapshot,
) error {
	rows, err := tx.Query(ctx, `SELECT email::text,id::text FROM platform.users
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY email,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var email, id string
		if err := rows.Scan(&email, &id); err != nil {
			return err
		}
		snapshot.Owners[canonicalLookup(email)] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}
	roleRows, err := tx.Query(ctx, `SELECT code::text,id::text FROM platform.roles
		WHERE status='ACTIVE' ORDER BY code,id`)
	if err != nil {
		return err
	}
	defer roleRows.Close()
	for roleRows.Next() {
		var code, id string
		if err := roleRows.Scan(&code, &id); err != nil {
			return err
		}
		snapshot.Roles[canonicalLookup(code)] = id
	}
	return roleRows.Err()
}

func loadValidationDatasets(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	snapshot *ValidationSnapshot,
) error {
	rows, err := tx.Query(ctx, `SELECT version.id::text,
		EXISTS(
			SELECT 1 FROM platform.dataset_materializations AS materialization
			WHERE materialization.dataset_version_id=version.id
			  AND materialization.dataset_id=dataset.id
			  AND materialization.tenant_id=dataset.tenant_id
			  AND materialization.status='ACTIVE'
		) AS active
	FROM platform.datasets AS dataset
	JOIN platform.dataset_versions AS version
	  ON version.id=dataset.current_published_version_id
	 AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
	WHERE dataset.domain_id=$1 AND dataset.deleted_at IS NULL
	  AND dataset.status='PUBLISHED' AND version.status='PUBLISHED'
	  AND version.layer IN ('DWS','ADS')`, domainID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var active bool
		if err := rows.Scan(&id, &active); err != nil {
			rows.Close()
			return err
		}
		snapshot.Datasets[id] = active
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	fieldRows, err := tx.Query(ctx, `SELECT field.dataset_version_id::text,field.field_id
	FROM platform.dataset_fields AS field
	JOIN platform.dataset_versions AS version
	  ON version.id=field.dataset_version_id AND version.tenant_id=field.tenant_id
	JOIN platform.datasets AS dataset
	  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
	WHERE dataset.domain_id=$1 AND dataset.deleted_at IS NULL
	  AND dataset.status='PUBLISHED' AND version.status='PUBLISHED'
	  AND dataset.current_published_version_id=version.id`, domainID)
	if err != nil {
		return err
	}
	defer fieldRows.Close()
	for fieldRows.Next() {
		var versionID, fieldID string
		if err := fieldRows.Scan(&versionID, &fieldID); err != nil {
			return err
		}
		if snapshot.Fields[versionID] == nil {
			snapshot.Fields[versionID] = map[string]bool{}
		}
		snapshot.Fields[versionID][strings.TrimSpace(fieldID)] = true
	}
	if err := fieldRows.Err(); err != nil {
		return err
	}
	fieldRows.Close()
	profileRows, err := tx.Query(ctx, `SELECT DISTINCT ON(model.dataset_version_id,dimension.logical_field_id)
		model.dataset_version_id::text,dimension.logical_field_id,
		COALESCE((profile.policy_decision_json->>'highCardinality')::boolean,job.high_cardinality_hint)
	FROM askdata.dimension_profiles AS profile
	JOIN askdata.dimension_profile_jobs AS job
	  ON job.id=profile.job_id AND job.tenant_id=profile.tenant_id
	JOIN askdata.dimensions AS dimension
	  ON dimension.id=profile.dimension_version_id
	 AND dimension.tenant_id=profile.tenant_id AND dimension.domain_id=profile.domain_id
	JOIN askdata.semantic_models AS model
	  ON model.id=dimension.semantic_model_version_id
	 AND model.tenant_id=dimension.tenant_id AND model.domain_id=dimension.domain_id
	WHERE profile.domain_id=$1 AND job.status='SUCCEEDED'
	ORDER BY model.dataset_version_id,dimension.logical_field_id,profile.generation DESC,profile.id DESC`, domainID)
	if err != nil {
		return err
	}
	defer profileRows.Close()
	for profileRows.Next() {
		var versionID, fieldID string
		var high bool
		if err := profileRows.Scan(&versionID, &fieldID, &high); err != nil {
			return err
		}
		if snapshot.HighCardinalityFields[versionID] == nil {
			snapshot.HighCardinalityFields[versionID] = map[string]bool{}
		}
		snapshot.HighCardinalityFields[versionID][fieldID] = high
	}
	return profileRows.Err()
}

func loadValidationAliases(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	snapshot *ValidationSnapshot,
) error {
	rows, err := tx.Query(ctx, `SELECT normalized_alias FROM askdata.semantic_aliases
		WHERE domain_id=$1 AND status IN ('DRAFT','CERTIFIED')
		ORDER BY normalized_alias`, domainID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return err
		}
		snapshot.Aliases[canonicalLookup(alias)] = struct{}{}
	}
	return rows.Err()
}
