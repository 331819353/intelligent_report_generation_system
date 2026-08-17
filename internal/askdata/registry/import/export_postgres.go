package registryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresExportCatalog struct{ pool *pgxpool.Pool }

func NewPostgresExportCatalog(pool *pgxpool.Pool) *PostgresExportCatalog {
	return &PostgresExportCatalog{pool: pool}
}

type exportVersionContract struct {
	table, identityColumn, releaseType string
	// filter 是可选的版本行过滤片段（不带 AND 前缀）。TERM 与 KNOWLEDGE
	// 共用 business_term_versions，靠 knowledge_kind 划分各自的导出范围。
	filter string
}

var exportVersionContracts = map[AssetType]exportVersionContract{
	AssetModel:            {"semantic_models", "model_id", "SEMANTIC_MODEL", ""},
	AssetMeasure:          {"measures", "measure_id", "MEASURE", ""},
	AssetMetric:           {"metric_versions", "metric_id", "METRIC", ""},
	AssetMetricDimension:  {"metric_dimension_versions", "metric_dimension_id", "METRIC_DIMENSION", ""},
	AssetDimension:        {"dimensions", "dimension_id", "DIMENSION", ""},
	AssetMember:           {"dimension_members", "member_id", "MEMBER", ""},
	AssetHierarchy:        {"hierarchies", "hierarchy_id", "HIERARCHY", ""},
	AssetRelationship:     {"relationships", "relationship_id", "RELATIONSHIP", ""},
	AssetTerm:             {"business_term_versions", "business_term_id", "BUSINESS_TERM", "knowledge_kind='ALIAS'"},
	AssetCertifiedExample: {"certified_example_versions", "certified_example_id", "CERTIFIED_EXAMPLE", ""},
	AssetKPIBundle:        {"kpi_bundle_versions", "kpi_bundle_id", "KPI_BUNDLE", ""},
	AssetEvalCase:         {"evaluation_case_versions", "evaluation_case_asset_id", "EVAL_CASE", ""},
	AssetKnowledge:        {"business_term_versions", "business_term_id", "BUSINESS_TERM", "knowledge_kind<>'ALIAS'"},
}

func (catalog *PostgresExportCatalog) CountExportRows(
	ctx context.Context,
	selection ExportSelection,
) (int, error) {
	if catalog == nil || catalog.pool == nil || validateExportSelection(selection) != nil {
		return 0, ErrExportInvalid
	}
	count := 0
	err := database.WithTenantTx(ctx, catalog.pool, selection.TenantID, func(tx pgx.Tx) error {
		if err := authorizeExport(ctx, tx, selection); err != nil {
			return err
		}
		if err := validateExportRelease(ctx, tx, selection); err != nil {
			return err
		}
		for _, assetType := range CanonicalAssetTypes(selection.AssetTypes) {
			ids, err := selectedExportVersionIDs(ctx, tx, selection, assetType)
			if err != nil {
				return err
			}
			assetCount := len(ids)
			if assetType == AssetHierarchy && len(ids) > 0 {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.hierarchy_levels
					WHERE hierarchy_version_id=ANY($1::uuid[])`, ids).Scan(&assetCount); err != nil {
					return err
				}
			}
			count += assetCount
			if count > MaxSemanticExportRows {
				return ErrExportTooLarge
			}
		}
		return nil
	})
	return count, normalizeExportError(err)
}

func (catalog *PostgresExportCatalog) LoadExportDataset(
	ctx context.Context,
	selection ExportSelection,
) (ExportDataset, error) {
	if catalog == nil || catalog.pool == nil || validateExportSelection(selection) != nil {
		return ExportDataset{}, ErrExportInvalid
	}
	dataset := ExportDataset{}
	err := database.WithTenantTx(ctx, catalog.pool, selection.TenantID, func(tx pgx.Tx) error {
		if err := authorizeExport(ctx, tx, selection); err != nil {
			return err
		}
		if err := validateExportRelease(ctx, tx, selection); err != nil {
			return err
		}
		for _, assetType := range CanonicalAssetTypes(selection.AssetTypes) {
			ids, err := selectedExportVersionIDs(ctx, tx, selection, assetType)
			if err != nil {
				return err
			}
			rows, omitted, err := loadExportRows(ctx, tx, assetType, ids)
			if err != nil {
				return err
			}
			dataset.Sheets = append(dataset.Sheets, ExportSheet{AssetType: assetType, Rows: rows})
			dataset.OmittedSensitiveMembers += omitted
		}
		return nil
	})
	return dataset, normalizeExportError(err)
}

func authorizeExport(ctx context.Context, tx pgx.Tx, selection ExportSelection) error {
	if selection.System {
		if _, authenticated := database.AccessContextFromContext(ctx); authenticated {
			return ErrExportInvalid
		}
		return nil
	}
	return requireImportOwner(ctx, tx, selection.TenantID, selection.DomainID, selection.ActorID)
}

func validateExportRelease(ctx context.Context, tx pgx.Tx, selection ExportSelection) error {
	if selection.ReleaseID == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM askdata.releases
		WHERE id=$1 AND domain_id=$2)`, selection.ReleaseID, selection.DomainID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrExportNotFound
	}
	return nil
}

func selectedExportVersionIDs(
	ctx context.Context,
	tx pgx.Tx,
	selection ExportSelection,
	assetType AssetType,
) ([]string, error) {
	contract, exists := exportVersionContracts[assetType]
	if !exists {
		return nil, ErrExportInvalid
	}
	if selection.PinnedVersionIDs != nil {
		pinned, selected := selection.PinnedVersionIDs[assetType]
		if !selected {
			return nil, ErrExportInvalid
		}
		if len(pinned) == 0 {
			return []string{}, nil
		}
		query := fmt.Sprintf(`SELECT id::text FROM askdata.%s
			WHERE domain_id=$1 AND status='CERTIFIED' AND id=ANY($2::uuid[])%s
			ORDER BY id`, contract.table, exportContractFilter(contract))
		rows, err := tx.Query(ctx, query, selection.DomainID, pinned)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		resolved := make([]string, 0, len(pinned))
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			resolved = append(resolved, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(resolved) != len(pinned) {
			return nil, ErrExportNotFound
		}
		return resolved, nil
	}
	var query string
	var args []any
	if selection.ReleaseID == "" {
		query = fmt.Sprintf(`SELECT DISTINCT ON (%s) id::text
			FROM askdata.%s WHERE domain_id=$1 AND status='CERTIFIED'%s
			ORDER BY %s,version_no DESC,id DESC`, contract.identityColumn, contract.table,
			exportContractFilter(contract), contract.identityColumn)
		args = []any{selection.DomainID}
	} else {
		query = fmt.Sprintf(`SELECT version.id::text
			FROM askdata.release_objects AS manifest
			JOIN askdata.%s AS version ON version.id=manifest.object_version_id
			WHERE manifest.release_id=$1 AND manifest.domain_id=$2 AND manifest.object_type=$3%s
			ORDER BY version.%s,version.version_no,version.id`, contract.table,
			exportContractFilter(contract), contract.identityColumn)
		args = []any{selection.ReleaseID, selection.DomainID, contract.releaseType}
	}
	rows, err := tx.Query(ctx, query, args...)
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
		if len(ids) > MaxSemanticExportRows {
			return nil, ErrExportTooLarge
		}
	}
	return ids, rows.Err()
}

func loadExportRows(
	ctx context.Context,
	tx pgx.Tx,
	assetType AssetType,
	ids []string,
) ([]map[string]string, int, error) {
	if len(ids) == 0 {
		return []map[string]string{}, 0, nil
	}
	switch assetType {
	case AssetModel:
		return loadModelExportRows(ctx, tx, ids)
	case AssetMeasure:
		return loadMeasureExportRows(ctx, tx, ids)
	case AssetMetric:
		return loadMetricExportRows(ctx, tx, ids)
	case AssetMetricDimension:
		return loadMetricDimensionExportRows(ctx, tx, ids)
	case AssetDimension:
		return loadDimensionExportRows(ctx, tx, ids)
	case AssetMember:
		return loadMemberExportRows(ctx, tx, ids)
	case AssetHierarchy:
		return loadHierarchyExportRows(ctx, tx, ids)
	case AssetRelationship:
		return loadRelationshipExportRows(ctx, tx, ids)
	case AssetTerm:
		return loadTermExportRows(ctx, tx, ids)
	case AssetCertifiedExample:
		return loadCertifiedExampleExportRows(ctx, tx, ids)
	case AssetKPIBundle:
		return loadKPIBundleExportRows(ctx, tx, ids)
	case AssetEvalCase:
		return loadEvaluationCaseExportRows(ctx, tx, ids)
	case AssetKnowledge:
		return loadKnowledgeExportRows(ctx, tx, ids)
	default:
		return nil, 0, ErrExportInvalid
	}
}

func exportContractFilter(contract exportVersionContract) string {
	if contract.filter == "" {
		return ""
	}
	return " AND " + contract.filter
}

func loadModelExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT model.code::text,model.name,model.dataset_version_id::text,
		COALESCE(entity.code::text,''),model.grain_contract,
		COALESCE(primary_dimension.code::text,''),COALESCE(contract.code::text,''),owner.email::text
		FROM askdata.semantic_models AS model
		LEFT JOIN askdata.entities AS entity ON entity.id=model.entity_version_id
		LEFT JOIN askdata.dimensions AS primary_dimension
		  ON primary_dimension.semantic_model_version_id=model.id
		 AND primary_dimension.logical_field_id=model.primary_time_field_id
		LEFT JOIN askdata.time_contract_versions AS contract_version
		  ON contract_version.id=model.time_contract_version_id
		LEFT JOIN askdata.time_contracts AS contract ON contract.id=contract_version.time_contract_id
		JOIN platform.users AS owner ON owner.id=model.owner_id AND owner.tenant_id=model.tenant_id
		WHERE model.id=ANY($1::uuid[])
		ORDER BY model.code,model.version_no,primary_dimension.code NULLS LAST`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var code, name, datasetVersionID, entityCode, primaryCode, contractCode, ownerEmail string
		var grain json.RawMessage
		if err := rows.Scan(&code, &name, &datasetVersionID, &entityCode, &grain,
			&primaryCode, &contractCode, &ownerEmail); err != nil {
			return nil, 0, err
		}
		if _, duplicate := seen[code]; duplicate {
			return nil, 0, ErrExportContract
		}
		seen[code] = struct{}{}
		var grainValue struct {
			Description string   `json:"description"`
			KeyFields   []string `json:"keyFields"`
		}
		if json.Unmarshal(grain, &grainValue) != nil {
			return nil, 0, ErrExportContract
		}
		result = append(result, exportRow(AssetModel,
			"code", code, "name", name, "datasetVersionId", datasetVersionID,
			"entityCode", entityCode, "grainDescription", grainValue.Description,
			"grainKeyFields", pipeJoin(grainValue.KeyFields),
			"primaryTimeDimensionCode", primaryCode, "timeContractCode", contractCode,
			"ownerEmail", ownerEmail))
	}
	return result, 0, rows.Err()
}

func loadMeasureExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT model.code::text,measure.code::text,measure.name,
		COALESCE(measure.formula_ast->>'logicalFieldId',''),measure.aggregation,
		COALESCE(measure.additivity,''),measure.unit,COALESCE(measure.currency,''),measure.null_policy
		FROM askdata.measures AS measure
		JOIN askdata.semantic_models AS model ON model.id=measure.semantic_model_version_id
		WHERE measure.id=ANY($1::uuid[]) ORDER BY measure.code,measure.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var modelCode, code, name, field, aggregation, additivity, unit, currency, nullPolicy string
		if err := rows.Scan(&modelCode, &code, &name, &field, &aggregation, &additivity,
			&unit, &currency, &nullPolicy); err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetMeasure,
			"modelCode", modelCode, "code", code, "name", name, "logicalFieldId", field,
			"defaultAggregation", aggregation, "additivity", additivity, "unit", unit,
			"currency", currency, "nullPolicy", nullPolicy))
	}
	return result, 0, rows.Err()
}

func loadMetricExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT metric.code::text,
		COALESCE(NULLIF(version.name,''),metric.name),
		COALESCE(NULLIF(version.description,''),metric.description),
		model.code::text,version.formula_ast,version.default_filters_ast,version.unit,
		COALESCE(version.currency,''),COALESCE(version.additivity,''),
		COALESCE(version.semi_additive_time_aggregation,''),COALESCE(version.aggregation_restriction,''),
		ARRAY(SELECT dimension.code::text FROM unnest(version.non_additive_dimensions) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.dimensions AS dimension ON dimension.id=item.id ORDER BY item.ordinal),
		version.time_grain,version.dedup_key,version.display_precision::text,
		version.zero_denominator_policy,COALESCE(version.incomplete_period_policy_override,''),
		version.positive_examples,version.negative_examples,owner.email::text
		FROM askdata.metric_versions AS version
		JOIN askdata.metrics AS metric ON metric.id=version.metric_id
		JOIN askdata.semantic_models AS model ON model.id=version.semantic_model_version_id
		JOIN platform.users AS owner ON owner.id=version.owner_id AND owner.tenant_id=version.tenant_id
		WHERE version.id=ANY($1::uuid[]) ORDER BY metric.code,version.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var code, name, description, modelCode, unit, currency, additivity, semi, restriction string
		var timeGrain, dedupKey, precision, zeroPolicy, incomplete, ownerEmail string
		var formula, filter json.RawMessage
		var nonAdditive, positive, negative []string
		if err := rows.Scan(&code, &name, &description, &modelCode, &formula, &filter, &unit,
			&currency, &additivity, &semi, &restriction, &nonAdditive, &timeGrain, &dedupKey,
			&precision, &zeroPolicy, &incomplete, &positive, &negative, &ownerEmail); err != nil {
			return nil, 0, err
		}
		formulaText, err := exportJSON(formula, "")
		if err != nil {
			return nil, 0, err
		}
		filterText, err := exportJSON(filter, "")
		if err != nil {
			return nil, 0, err
		}
		if filterText == `{"type":"TRUE"}` {
			filterText = ""
		}
		result = append(result, exportRow(AssetMetric,
			"code", code, "name", name, "description", description, "modelCode", modelCode,
			"formula", formulaText, "defaultFilter", filterText, "unit", unit, "currency", currency,
			"additivity", additivity, "semiAdditiveTimeAggregation", semi,
			"aggregationRestriction", restriction, "nonAdditiveDimensionCodes", pipeJoin(nonAdditive),
			"timeGrain", timeGrain, "dedupKey", dedupKey, "displayPrecision", precision,
			"zeroDenominatorPolicy", zeroPolicy, "incompletePeriodPolicyOverride", incomplete,
			"positiveExamples", pipeJoin(positive), "negativeExamples", pipeJoin(negative),
			"ownerEmail", ownerEmail))
	}
	return result, 0, rows.Err()
}

func loadMetricDimensionExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT metric.code::text,dimension.code::text,
		compatibility.compatible,compatibility.role
		FROM askdata.metric_dimension_versions AS compatibility
		JOIN askdata.metric_versions AS metric_version ON metric_version.id=compatibility.metric_version_id
		JOIN askdata.metrics AS metric ON metric.id=metric_version.metric_id
		JOIN askdata.dimensions AS dimension ON dimension.id=compatibility.dimension_version_id
		WHERE compatibility.id=ANY($1::uuid[]) ORDER BY metric.code,dimension.code,compatibility.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var metricCode, dimensionCode, role string
		var compatible bool
		if err := rows.Scan(&metricCode, &dimensionCode, &compatible, &role); err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetMetricDimension,
			"metricCode", metricCode, "dimensionCode", dimensionCode,
			"compatible", strings.ToUpper(strconv.FormatBool(compatible)), "role", role))
	}
	return result, 0, rows.Err()
}

func loadDimensionExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT model.code::text,dimension.code::text,dimension.name,
		dimension.description,dimension.dimension_kind,dimension.logical_field_id,
		dimension.sensitivity,dimension.member_index_policy,dimension.groupable,
		dimension.filterable,dimension.sortable,
		COALESCE((SELECT hierarchy.code::text FROM askdata.hierarchy_levels AS level
		  JOIN askdata.hierarchies AS hierarchy ON hierarchy.id=level.hierarchy_version_id
		  WHERE level.dimension_version_id=dimension.id AND hierarchy.status='CERTIFIED'
		  ORDER BY hierarchy.code,hierarchy.version_no DESC LIMIT 1),''),owner.email::text
		FROM askdata.dimensions AS dimension
		JOIN askdata.semantic_models AS model ON model.id=dimension.semantic_model_version_id
		JOIN platform.users AS owner ON owner.id=dimension.owner_id AND owner.tenant_id=dimension.tenant_id
		WHERE dimension.id=ANY($1::uuid[]) ORDER BY dimension.code,dimension.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var modelCode, code, name, description, kind, field, sensitivity, memberPolicy string
		var hierarchyCode, ownerEmail string
		var groupable, filterable, sortable bool
		if err := rows.Scan(&modelCode, &code, &name, &description, &kind, &field,
			&sensitivity, &memberPolicy, &groupable, &filterable, &sortable,
			&hierarchyCode, &ownerEmail); err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetDimension,
			"modelCode", modelCode, "code", code, "name", name, "description", description,
			"kind", kind, "logicalFieldId", field, "sensitivity", sensitivity,
			"memberIndexPolicy", memberPolicy,
			"groupable", strings.ToUpper(strconv.FormatBool(groupable)),
			"filterable", strings.ToUpper(strconv.FormatBool(filterable)),
			"sortable", strings.ToUpper(strconv.FormatBool(sortable)),
			"hierarchyCode", hierarchyCode, "ownerEmail", ownerEmail))
	}
	return result, 0, rows.Err()
}

func loadMemberExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	var omitted int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.dimension_members
		WHERE id=ANY($1::uuid[]) AND sensitivity IN ('CONFIDENTIAL','RESTRICTED')`, ids).Scan(&omitted); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE selected AS (
		SELECT member.* FROM askdata.dimension_members AS member
		WHERE member.id=ANY($1::uuid[]) AND member.sensitivity IN ('PUBLIC','INTERNAL')
	), ancestors(root_id,id,parent_member_version_id,member_key,depth) AS (
		SELECT selected.id,selected.id,selected.parent_member_version_id,selected.member_key,0 FROM selected
		UNION ALL
		SELECT ancestors.root_id,parent.id,parent.parent_member_version_id,parent.member_key,ancestors.depth+1
		FROM ancestors JOIN askdata.dimension_members AS parent ON parent.id=ancestors.parent_member_version_id
		WHERE ancestors.depth<31
	)
	SELECT selected.id::text,dimension.code::text,selected.member_key,selected.canonical_label,
		ARRAY(SELECT alias.alias FROM askdata.dimension_member_aliases AS alias
		  WHERE alias.member_version_id=selected.id AND alias.status='CERTIFIED'
		    AND alias.normalized_alias<>lower(selected.canonical_label)
		  ORDER BY alias.priority DESC,alias.normalized_alias,alias.id),
		ARRAY(SELECT ancestor.member_key FROM ancestors AS ancestor
		  WHERE ancestor.root_id=selected.id AND ancestor.depth>0 ORDER BY ancestor.depth DESC),
		selected.valid_from,selected.valid_to,selected.sensitivity
	FROM selected JOIN askdata.dimensions AS dimension ON dimension.id=selected.dimension_version_id
	ORDER BY dimension.code,selected.member_key,selected.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var id, dimensionCode, canonicalValue, label, sensitivity string
		var aliases, hierarchyPath []string
		var validFrom time.Time
		var validTo *time.Time
		if err := rows.Scan(&id, &dimensionCode, &canonicalValue, &label, &aliases,
			&hierarchyPath, &validFrom, &validTo, &sensitivity); err != nil {
			return nil, 0, err
		}
		_ = id
		result = append(result, exportRow(AssetMember,
			"dimensionCode", dimensionCode, "canonicalValue", canonicalValue,
			"displayLabel", label, "aliases", pipeJoin(aliases),
			"hierarchyPath", pipeJoin(hierarchyPath), "validFrom", exportTime(&validFrom),
			"validTo", exportTime(validTo), "sensitivity", sensitivity))
	}
	return result, omitted, rows.Err()
}

func loadHierarchyExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT hierarchy.code::text,hierarchy.name,level.ordinal,
		dimension.code::text,COALESCE(parent_dimension.code::text,'')
		FROM askdata.hierarchies AS hierarchy
		JOIN askdata.hierarchy_levels AS level ON level.hierarchy_version_id=hierarchy.id
		JOIN askdata.dimensions AS dimension ON dimension.id=level.dimension_version_id
		LEFT JOIN askdata.hierarchy_levels AS parent_level
		  ON parent_level.hierarchy_version_id=hierarchy.id AND parent_level.ordinal=level.ordinal-1
		LEFT JOIN askdata.dimensions AS parent_dimension ON parent_dimension.id=parent_level.dimension_version_id
		WHERE hierarchy.id=ANY($1::uuid[]) ORDER BY hierarchy.code,hierarchy.version_no,level.ordinal`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var code, name, dimensionCode, parentCode string
		var ordinal int
		if err := rows.Scan(&code, &name, &ordinal, &dimensionCode, &parentCode); err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetHierarchy,
			"code", code, "name", name, "levelOrder", strconv.Itoa(ordinal),
			"dimensionCode", dimensionCode, "parentDimensionCode", parentCode))
	}
	return result, 0, rows.Err()
}

func loadRelationshipExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT left_model.code::text,right_model.code::text,
		relationship.join_ast,relationship.join_type,relationship.cardinality,
		relationship.fanout_policy,COALESCE(bridge.code::text,''),
		relationship.valid_from,relationship.valid_to
		FROM askdata.relationships AS relationship
		JOIN askdata.semantic_models AS left_model ON left_model.id=relationship.left_model_version_id
		JOIN askdata.semantic_models AS right_model ON right_model.id=relationship.right_model_version_id
		LEFT JOIN askdata.semantic_models AS bridge ON bridge.id=relationship.bridge_model_version_id
		WHERE relationship.id=ANY($1::uuid[])
		ORDER BY left_model.code,right_model.code,relationship.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var left, right, joinType, cardinality, fanout, bridge string
		var joinAST json.RawMessage
		var validFrom time.Time
		var validTo *time.Time
		if err := rows.Scan(&left, &right, &joinAST, &joinType, &cardinality, &fanout,
			&bridge, &validFrom, &validTo); err != nil {
			return nil, 0, err
		}
		joinText, err := exportJSON(joinAST, "")
		if err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetRelationship,
			"leftModelCode", left, "rightModelCode", right, "joinAst", joinText,
			"joinType", joinType, "cardinality", cardinality, "fanoutPolicy", fanout,
			"bridgeModelCode", bridge, "validFrom", exportTime(&validFrom), "validTo", exportTime(validTo)))
	}
	return result, 0, rows.Err()
}

func loadTermExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT identity.term,identity.term_type,version.target_object_type,
		CASE version.target_object_type
		  WHEN 'METRIC' THEN (SELECT metric.code::text FROM askdata.metric_versions AS target
		    JOIN askdata.metrics AS metric ON metric.id=target.metric_id WHERE target.id=version.target_version_id)
		  WHEN 'DIMENSION' THEN (SELECT target.code::text FROM askdata.dimensions AS target
		    WHERE target.id=version.target_version_id)
		  WHEN 'MEMBER' THEN askdata.resolve_member_export_target(version.target_version_id)
		  WHEN 'TIME_CONTRACT' THEN (SELECT contract.code::text
		    FROM askdata.time_contract_versions AS target JOIN askdata.time_contracts AS contract
		      ON contract.id=target.time_contract_id WHERE target.id=version.target_version_id)
		  ELSE version.target_code END,
		version.match_mode,
		COALESCE(version.match_pattern,''),version.priority,version.negative_contexts,
		version.valid_from,version.valid_to,version.source
		FROM askdata.business_term_versions AS version
		JOIN askdata.business_terms AS identity ON identity.id=version.business_term_id
		WHERE version.id=ANY($1::uuid[]) ORDER BY identity.term,identity.term_type,version.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	omitted := 0
	for rows.Next() {
		var term, termType, targetType, matchMode, matchPattern, source string
		var targetCode *string
		var priority int
		var negatives []string
		var validFrom, validTo *time.Time
		if err := rows.Scan(&term, &termType, &targetType, &targetCode,
			&matchMode, &matchPattern, &priority, &negatives, &validFrom, &validTo,
			&source); err != nil {
			return nil, 0, err
		}
		// MEMBER 目标经 SECURITY DEFINER 解析：敏感成员返回 NULL，与导出的
		// 敏感省略策略同一边界；其余目标解析为空是合同错误。
		if targetType == "MEMBER" && (targetCode == nil || *targetCode == "") {
			omitted++
			continue
		}
		if targetCode == nil || *targetCode == "" {
			return nil, 0, fmt.Errorf("%w: unresolved %s target", ErrExportContract, targetType)
		}
		_ = matchPattern
		result = append(result, exportRow(AssetTerm,
			"term", term, "termType", termType, "targetCode", *targetCode,
			"matchMode", matchMode, "priority", strconv.Itoa(priority),
			"negativeContexts", pipeJoin(negatives), "validFrom", exportTime(validFrom),
			"validTo", exportTime(validTo), "source", source))
	}
	return result, omitted, rows.Err()
}

// loadKnowledgeExportRows 导出业务知识词条（knowledge_kind<>'ALIAS'）。目标
// code 按当前对象解析，CONCEPT 目标导出为空 targetType/targetCode；敏感成员
// 目标与词条导出同一省略策略。
func loadKnowledgeExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT version.code::text,version.name,version.knowledge_kind,
		version.authority,version.definition,version.aliases,version.relation,
		version.target_object_type,
		CASE version.target_object_type
		  WHEN 'METRIC' THEN (SELECT metric.code::text FROM askdata.metric_versions AS target
		    JOIN askdata.metrics AS metric ON metric.id=target.metric_id WHERE target.id=version.target_version_id)
		  WHEN 'DIMENSION' THEN (SELECT target.code::text FROM askdata.dimensions AS target
		    WHERE target.id=version.target_version_id)
		  WHEN 'MEMBER' THEN askdata.resolve_member_export_target(version.target_version_id)
		  WHEN 'TIME_CONTRACT' THEN (SELECT contract.code::text
		    FROM askdata.time_contract_versions AS target JOIN askdata.time_contracts AS contract
		      ON contract.id=target.time_contract_id WHERE target.id=version.target_version_id)
		  ELSE '' END,
		version.match_mode,version.priority,version.negative_contexts,
		version.valid_from,version.valid_to
		FROM askdata.business_term_versions AS version
		WHERE version.id=ANY($1::uuid[])
		ORDER BY version.code,version.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	omitted := 0
	for rows.Next() {
		var code, name, knowledgeKind, authority, body, relation string
		var targetObjectType, matchMode string
		var targetCode *string
		var synonyms, negatives []string
		var priority int
		var validFrom, validTo *time.Time
		if err := rows.Scan(&code, &name, &knowledgeKind, &authority, &body, &synonyms,
			&relation, &targetObjectType, &targetCode, &matchMode, &priority,
			&negatives, &validFrom, &validTo); err != nil {
			return nil, 0, err
		}
		// 敏感成员目标由 SECURITY DEFINER 解析为 NULL：整条知识按省略处理。
		if targetObjectType == "MEMBER" && (targetCode == nil || *targetCode == "") {
			omitted++
			continue
		}
		targetType := targetObjectType
		switch targetObjectType {
		case "CONCEPT", "OPERATOR", "LEGACY":
			targetType = ""
		case "TIME_CONTRACT":
			targetType = "TIME"
		}
		resolvedTarget := ""
		if targetType != "" {
			if targetCode == nil || *targetCode == "" {
				return nil, 0, fmt.Errorf("%w: unresolved %s target", ErrExportContract, targetObjectType)
			}
			resolvedTarget = *targetCode
		}
		result = append(result, exportRow(AssetKnowledge,
			"code", code, "name", name, "knowledgeKind", knowledgeKind,
			"authority", authority, "body", body, "synonyms", pipeJoin(synonyms),
			"targetType", targetType, "targetCode", resolvedTarget,
			"relation", relation, "matchMode", matchMode,
			"priority", strconv.Itoa(priority),
			"negativeContexts", pipeJoin(negatives),
			"validFrom", exportTime(validFrom), "validTo", exportTime(validTo)))
	}
	return result, omitted, rows.Err()
}

func loadCertifiedExampleExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT example.question,
		ARRAY(SELECT metric.code::text FROM unnest(example.expected_metric_version_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.metric_versions AS version ON version.id=item.id JOIN askdata.metrics AS metric ON metric.id=version.metric_id ORDER BY item.ordinal),
		ARRAY(SELECT dimension.code::text FROM unnest(example.expected_dimension_version_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.dimensions AS dimension ON dimension.id=item.id ORDER BY item.ordinal),
		example.expected_member_values,example.expected_time_expression,
		ARRAY(SELECT role.code::text FROM unnest(example.applicable_role_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN platform.roles AS role ON role.id=item.id ORDER BY item.ordinal),example.notes
		FROM askdata.certified_example_versions AS example
		WHERE example.id=ANY($1::uuid[]) ORDER BY example.question,example.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var question, timeExpression, notes string
		var metrics, dimensions, roles []string
		var membersJSON json.RawMessage
		if err := rows.Scan(&question, &metrics, &dimensions, &membersJSON,
			&timeExpression, &roles, &notes); err != nil {
			return nil, 0, err
		}
		members, err := exportStringArrayJSON(membersJSON)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetCertifiedExample,
			"question", question, "expectedMetricCodes", pipeJoin(metrics),
			"expectedDimensionCodes", pipeJoin(dimensions), "expectedMemberValues", pipeJoin(members),
			"expectedTimeExpression", timeExpression, "applicableRoles", pipeJoin(roles), "notes", notes))
	}
	return result, 0, rows.Err()
}

func loadKPIBundleExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT bundle.code::text,bundle.name,
		ARRAY(SELECT metric.code::text FROM jsonb_array_elements(version.items) WITH ORDINALITY AS item(value,ordinal)
		  JOIN askdata.metric_versions AS metric_version ON metric_version.id=(item.value->>'metricVersionId')::uuid
		  JOIN askdata.metrics AS metric ON metric.id=metric_version.metric_id ORDER BY item.ordinal),
		ARRAY(SELECT dimension.code::text FROM unnest(version.default_dimension_version_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.dimensions AS dimension ON dimension.id=item.id ORDER BY item.ordinal),
		version.default_time_expression,version.default_chart_types,version.role_mapping,
		version.applicable_question_patterns
		FROM askdata.kpi_bundle_versions AS version JOIN askdata.kpi_bundles AS bundle ON bundle.id=version.kpi_bundle_id
		WHERE version.id=ANY($1::uuid[]) ORDER BY bundle.code,version.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var code, name, timeExpression string
		var roleMapping json.RawMessage
		var metrics, dimensions, chartTypes, questionPatterns []string
		if err := rows.Scan(&code, &name, &metrics, &dimensions, &timeExpression,
			&chartTypes, &roleMapping, &questionPatterns); err != nil {
			return nil, 0, err
		}
		roleText, err := exportJSON(roleMapping, `{}`)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, exportRow(AssetKPIBundle,
			"code", code, "name", name, "metricCodes", pipeJoin(metrics),
			"defaultDimensionCodes", pipeJoin(dimensions), "defaultTimeExpression", timeExpression,
			"defaultChartTypes", pipeJoin(chartTypes), "roleMapping", roleText,
			"applicableQuestionTypes", pipeJoin(questionPatterns)))
	}
	return result, 0, rows.Err()
}

func loadEvaluationCaseExportRows(ctx context.Context, tx pgx.Tx, ids []string) ([]map[string]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT evaluation.question,evaluation.actor_role,evaluation.expected_outcome,
		ARRAY(SELECT metric.code::text FROM unnest(evaluation.expected_metric_version_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.metric_versions AS version ON version.id=item.id JOIN askdata.metrics AS metric ON metric.id=version.metric_id ORDER BY item.ordinal),
		ARRAY(SELECT dimension.code::text FROM unnest(evaluation.expected_dimension_version_ids) WITH ORDINALITY AS item(id,ordinal)
		  JOIN askdata.dimensions AS dimension ON dimension.id=item.id ORDER BY item.ordinal),
		evaluation.expected_member_values,evaluation.expected_time_expression,
		evaluation.expected_result_hint,evaluation.set_type,evaluation.shard_id
		FROM askdata.evaluation_case_versions AS evaluation
		WHERE evaluation.id=ANY($1::uuid[]) ORDER BY evaluation.question,evaluation.version_no`, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var question, actorRole, outcome, timeExpression, resultHint, setType string
		var metrics, dimensions []string
		var membersJSON json.RawMessage
		var shard int
		if err := rows.Scan(&question, &actorRole, &outcome, &metrics, &dimensions,
			&membersJSON, &timeExpression, &resultHint, &setType, &shard); err != nil {
			return nil, 0, err
		}
		members, err := exportStringArrayJSON(membersJSON)
		if err != nil {
			return nil, 0, err
		}
		if resultHint != "" {
			resultHint, err = exportJSON(json.RawMessage(resultHint), "")
			if err != nil {
				return nil, 0, err
			}
		}
		result = append(result, exportRow(AssetEvalCase,
			"question", question, "actorRole", actorRole, "expectedOutcome", outcome,
			"expectedMetricCodes", pipeJoin(metrics), "expectedDimensionCodes", pipeJoin(dimensions),
			"expectedMemberValues", pipeJoin(members), "expectedTimeExpression", timeExpression,
			"expectedResultHint", resultHint, "setType", setType, "shardId", strconv.Itoa(shard)))
	}
	return result, 0, rows.Err()
}

func exportStringArrayJSON(raw json.RawMessage) ([]string, error) {
	values := []string{}
	if json.Unmarshal(raw, &values) != nil {
		return nil, ErrExportContract
	}
	return values, nil
}

func exportRow(assetType AssetType, pairs ...string) map[string]string {
	definition, _ := TemplateDefinitionFor(assetType)
	result := make(map[string]string, len(definition.Columns))
	for _, column := range definition.Columns {
		result[column.Name] = ""
	}
	for index := 0; index+1 < len(pairs); index += 2 {
		result[pairs[index]] = pairs[index+1]
	}
	return result
}

func normalizeExportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrExportInvalid) || errors.Is(err, ErrExportNotFound) ||
		errors.Is(err, ErrExportTooLarge) || errors.Is(err, ErrExportContract) ||
		errors.Is(err, ErrImportPermission) {
		return err
	}
	return err
}
