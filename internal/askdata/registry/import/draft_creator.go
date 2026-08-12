package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var ErrImportDraftContract = errors.New("semantic import row cannot be mapped to a DRAFT contract")

// PostgresDraftCreator is the only production mapper from validated template
// rows to authoritative semantic version tables. It never changes status to
// CERTIFIED and it resolves every code again inside the commit transaction.
type PostgresDraftCreator struct{}

func NewPostgresDraftCreator() *PostgresDraftCreator { return &PostgresDraftCreator{} }

func (creator *PostgresDraftCreator) CreateDraft(
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	row ImportRow,
) (DraftReference, error) {
	if creator == nil || tx == nil || row.ValidationState != RowValid || len(row.NormalizedJSON) == 0 {
		return DraftReference{}, ErrImportDraftContract
	}
	values := map[string]string{}
	if err := json.Unmarshal(row.NormalizedJSON, &values); err != nil || values == nil {
		return DraftReference{}, ErrImportDraftContract
	}
	var reference DraftReference
	var err error
	switch batch.AssetType {
	case AssetModel:
		reference, err = createModelDraft(ctx, tx, batch, values)
	case AssetMeasure:
		reference, err = createMeasureDraft(ctx, tx, batch, values)
	case AssetMetric:
		reference, err = createMetricDraft(ctx, tx, batch, values)
	case AssetMetricDimension:
		reference, err = createMetricDimensionDraft(ctx, tx, batch, values)
	case AssetDimension:
		reference, err = createDimensionDraft(ctx, tx, batch, values)
	case AssetMember:
		reference, err = createMemberDraft(ctx, tx, batch, values)
	case AssetHierarchy:
		reference, err = createHierarchyDraft(ctx, tx, batch, values)
	case AssetRelationship:
		reference, err = createRelationshipDraft(ctx, tx, batch, values)
	case AssetTerm:
		reference, err = createTermDraft(ctx, tx, batch, values)
	case AssetCertifiedExample:
		reference, err = createCertifiedExampleDraft(ctx, tx, batch, values)
	case AssetKPIBundle:
		reference, err = createKPIBundleDraft(ctx, tx, batch, values)
	case AssetEvalCase:
		reference, err = createEvaluationCaseDraft(ctx, tx, batch, values)
	default:
		err = ErrImportDraftContract
	}
	if err != nil {
		return DraftReference{}, err
	}
	if reference.Status != "DRAFT" || !canonicalUUID(reference.ObjectID) || !canonicalUUID(reference.VersionID) {
		return DraftReference{}, ErrImportDraftContract
	}
	return reference, nil
}

func createModelDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	ownerID, err := resolveOwner(ctx, tx, batch, values["ownerEmail"])
	if err != nil {
		return DraftReference{}, fmt.Errorf("model owner: %w", err)
	}
	var datasetID, schemaHash, layer, materializationID string
	err = tx.QueryRow(ctx, `SELECT dataset.id::text,version.schema_hash,version.layer,
		materialization.id::text
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id AND dataset.tenant_id=version.tenant_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.dataset_id=dataset.id AND materialization.dataset_version_id=version.id
		 AND materialization.tenant_id=dataset.tenant_id
		WHERE version.id=$1 AND version.tenant_id=$2 AND dataset.domain_id=$3
		  AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
		  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
		  AND dataset.current_published_version_id=version.id
		  AND materialization.status='ACTIVE' AND materialization.layer=version.layer
		  AND materialization.schema_hash=version.schema_hash
		ORDER BY materialization.activated_at DESC,materialization.created_at DESC,
			materialization.id LIMIT 1`,
		values["datasetVersionId"], batch.TenantID, batch.DomainID).
		Scan(&datasetID, &schemaHash, &layer, &materializationID)
	if err != nil {
		return DraftReference{}, fmt.Errorf("model dataset: %w", ErrImportDraftContract)
	}
	entityVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "ENTITY", values["entityCode"])
	if err != nil {
		return DraftReference{}, fmt.Errorf("model entity: %w", err)
	}
	primaryTimeFieldID, primaryTimeDimensionVersionID := "", ""
	if values["primaryTimeDimensionCode"] != "" {
		primaryTimeDimensionVersionID, _, err = resolveCertifiedCode(
			ctx, tx, batch, "DIMENSION", values["primaryTimeDimensionCode"],
		)
		if err != nil {
			return DraftReference{}, fmt.Errorf("model primary time dimension: %w", err)
		}
		err = tx.QueryRow(ctx, `SELECT dimension.logical_field_id
			FROM askdata.dimensions AS dimension
			JOIN askdata.semantic_models AS model
			  ON model.id=dimension.semantic_model_version_id
			 AND model.tenant_id=dimension.tenant_id AND model.domain_id=dimension.domain_id
			WHERE dimension.id=$1 AND model.dataset_version_id=$2`,
			primaryTimeDimensionVersionID, values["datasetVersionId"]).Scan(&primaryTimeFieldID)
		if err != nil {
			return DraftReference{}, fmt.Errorf("model primary time field: %w", ErrImportDraftContract)
		}
	}
	timeContractVersionID := ""
	if values["timeContractCode"] != "" {
		timeContractVersionID, _, err = resolveCertifiedCode(ctx, tx, batch, "TIME_CONTRACT", values["timeContractCode"])
		if err != nil {
			return DraftReference{}, fmt.Errorf("model time contract: %w", err)
		}
	}
	objectID, versionNo, err := nextCodeVersion(ctx, tx, batch, AssetModel, values["code"])
	if err != nil {
		return DraftReference{}, fmt.Errorf("model version: %w", err)
	}
	versionID := uuid.NewString()
	grain := map[string]any{
		"description": values["grainDescription"], "keyFields": splitPipe(values["grainKeyFields"]),
	}
	grainJSON, err := registry.CanonicalValue(grain)
	if err != nil {
		return DraftReference{}, err
	}
	hash, err := draftContentHash(values, map[string]any{
		"datasetId": datasetID, "materializationId": materializationID,
		"entityVersionId": entityVersionID, "timeContractVersionId": timeContractVersionID,
		"primaryTimeDimensionVersionId": primaryTimeDimensionVersionID,
	})
	if err != nil {
		return DraftReference{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.semantic_models(
		id,tenant_id,domain_id,model_id,version_no,code,name,description,
		entity_version_id,dataset_id,dataset_version_id,materialization_id,
		dataset_schema_hash,layer,grain_contract,primary_time_field_id,
		time_contract_version_id,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,'',NULLIF($8,'')::uuid,$9,$10,$11,$12,$13,$14,
		$15,NULLIF($16,'')::uuid,'DRAFT',$17,$18)`, versionID, batch.TenantID, batch.DomainID,
		objectID, versionNo, values["code"], values["name"], entityVersionID,
		datasetID, values["datasetVersionId"], materializationID, schemaHash, layer,
		grainJSON, primaryTimeFieldID, timeContractVersionID, hash, ownerID)
	return draftRef(objectID, versionID, err)
}

func createMeasureDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	modelVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "MODEL", values["modelCode"])
	if err != nil {
		return DraftReference{}, err
	}
	objectID, versionNo, err := nextCodeVersion(ctx, tx, batch, AssetMeasure, values["code"])
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	formula, _ := registry.CanonicalValue(map[string]any{"type": "FIELD_REF", "logicalFieldId": values["logicalFieldId"]})
	hash, err := draftContentHash(values, map[string]any{"semanticModelVersionId": modelVersionID})
	if err != nil {
		return DraftReference{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.measures(
		id,tenant_id,domain_id,measure_id,version_no,semantic_model_version_id,
		code,name,description,formula_ast,aggregation,data_type,unit,currency,
		additivity,semi_additive_time_aggregation,aggregation_restriction,
		null_policy,zero_denominator_policy,display_precision,
		additivity_confirmed_by,additivity_confirmed_at,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'',$9,$10,'DECIMAL',$11,NULLIF($12,'')::text,
		$13,NULLIF($14,'')::text,NULLIF($15,'')::text,$16,'NULL',2,$17,now(),'DRAFT',$18,$17)`,
		versionID, batch.TenantID, batch.DomainID, objectID, versionNo, modelVersionID,
		values["code"], values["name"], formula, values["defaultAggregation"], values["unit"],
		values["currency"], values["additivity"], values["semiAdditiveTimeAggregation"],
		values["aggregationRestriction"], values["nullPolicy"], batch.CreatedBy, hash)
	return draftRef(objectID, versionID, err)
}

func createMetricDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	ownerID, err := resolveOwner(ctx, tx, batch, values["ownerEmail"])
	if err != nil {
		return DraftReference{}, err
	}
	modelVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "MODEL", values["modelCode"])
	if err != nil {
		return DraftReference{}, err
	}
	metricID, versionNo, err := nextMetricVersion(ctx, tx, batch, values, ownerID)
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	formula := json.RawMessage(values["formula"])
	filter := json.RawMessage(`{"type":"TRUE"}`)
	if values["defaultFilter"] != "" {
		filter = json.RawMessage(values["defaultFilter"])
	}
	nonAdditive, err := resolveCodeList(ctx, tx, batch, "DIMENSION", values["nonAdditiveDimensionCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	precision, _ := strconv.Atoi(values["displayPrecision"])
	hash, err := draftContentHash(values, map[string]any{"semanticModelVersionId": modelVersionID})
	if err != nil {
		return DraftReference{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
		id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
		name,description,formula_ast,default_filters_ast,unit,currency,time_grain,additivity,
		semi_additive_time_aggregation,aggregation_restriction,non_additive_dimensions,
		zero_denominator_policy,display_precision,additivity_confirmed_by,
		additivity_confirmed_at,null_policy,incomplete_period_policy_override,
		dedup_key,positive_examples,negative_examples,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,'')::text,$13,$14,
		NULLIF($15,'')::text,NULLIF($16,'')::text,$17,$18,$19,$20,now(),'PRESERVE',
		NULLIF($21,'')::text,$22,$23,$24,'DRAFT',$25,$26)`, versionID, batch.TenantID,
		batch.DomainID, metricID, versionNo, modelVersionID, values["name"], values["description"],
		formula, filter, values["unit"], values["currency"], values["timeGrain"], values["additivity"],
		values["semiAdditiveTimeAggregation"], values["aggregationRestriction"], nonAdditive,
		values["zeroDenominatorPolicy"], precision, batch.CreatedBy,
		values["incompletePeriodPolicyOverride"], values["dedupKey"],
		splitPipe(values["positiveExamples"]), splitPipe(values["negativeExamples"]), hash, ownerID)
	if err != nil {
		return DraftReference{}, err
	}
	measureCodes := collectStringFields(formula, "measureCode")
	for index, code := range measureCodes {
		measureVersionID, _, resolveErr := resolveCertifiedCode(ctx, tx, batch, "MEASURE", code)
		if resolveErr != nil {
			return DraftReference{}, resolveErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_version_measures(
			tenant_id,domain_id,metric_version_id,measure_version_id,ordinal
		) VALUES($1,$2,$3,$4,$5)`, batch.TenantID, batch.DomainID,
			versionID, measureVersionID, index+1); err != nil {
			return DraftReference{}, err
		}
	}
	return draftRef(metricID, versionID, nil)
}

func createMetricDimensionDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	metricVersionID, metricID, err := resolveCertifiedCode(ctx, tx, batch, "METRIC", values["metricCode"])
	if err != nil {
		return DraftReference{}, err
	}
	dimensionVersionID, dimensionID, err := resolveCertifiedCode(ctx, tx, batch, "DIMENSION", values["dimensionCode"])
	if err != nil {
		return DraftReference{}, err
	}
	var objectID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM askdata.metric_dimensions
		WHERE domain_id=$1 AND metric_id=$2 AND dimension_id=$3`, batch.DomainID, metricID, dimensionID).
		Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO askdata.metric_dimensions(
			id,tenant_id,domain_id,metric_id,dimension_id,created_by
		) VALUES($1,$2,$3,$4,$5,$6)`, objectID, batch.TenantID, batch.DomainID,
			metricID, dimensionID, batch.CreatedBy)
	}
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "metric_dimension_versions", "metric_dimension_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	hash, _ := draftContentHash(values, map[string]any{
		"metricVersionId": metricVersionID, "dimensionVersionId": dimensionVersionID,
	})
	_, err = tx.Exec(ctx, `INSERT INTO askdata.metric_dimension_versions(
		id,tenant_id,domain_id,metric_dimension_id,version_no,metric_version_id,
		dimension_version_id,compatible,role,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'DRAFT',$10,$11)`, versionID,
		batch.TenantID, batch.DomainID, objectID, versionNo, metricVersionID,
		dimensionVersionID, values["compatible"] == "TRUE", values["role"], hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func createDimensionDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	ownerID, err := resolveOwner(ctx, tx, batch, values["ownerEmail"])
	if err != nil {
		return DraftReference{}, err
	}
	modelVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "MODEL", values["modelCode"])
	if err != nil {
		return DraftReference{}, err
	}
	objectID, versionNo, err := nextCodeVersion(ctx, tx, batch, AssetDimension, values["code"])
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	hash, _ := draftContentHash(values, map[string]any{"semanticModelVersionId": modelVersionID})
	_, err = tx.Exec(ctx, `INSERT INTO askdata.dimensions(
		id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
		logical_field_id,code,name,description,dimension_kind,sensitivity,
		member_index_policy,high_cardinality,groupable,filterable,sortable,
		status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,false,$14,$15,$16,
		'DRAFT',$17,$18)`, versionID, batch.TenantID, batch.DomainID, objectID, versionNo,
		modelVersionID, values["logicalFieldId"], values["code"], values["name"],
		values["description"], values["kind"], values["sensitivity"], values["memberIndexPolicy"],
		values["groupable"] == "TRUE", values["filterable"] == "TRUE", values["sortable"] == "TRUE",
		hash, ownerID)
	return draftRef(objectID, versionID, err)
}

func createMemberDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	dimensionVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "DIMENSION", values["dimensionCode"])
	if err != nil {
		return DraftReference{}, err
	}
	lookupHash := boundValueHash(dimensionVersionID, values["canonicalValue"])
	var objectID string
	err = tx.QueryRow(ctx, `SELECT member_id::text FROM askdata.dimension_members
		WHERE domain_id=$1 AND dimension_version_id=$2 AND member_key_hash=$3
		ORDER BY version_no DESC LIMIT 1`, batch.DomainID, dimensionVersionID, lookupHash).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID, err = uuid.NewString(), nil
	}
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "dimension_members", "member_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	validFrom, validTo, err := parseValidity(values["validFrom"], values["validTo"])
	if err != nil {
		return DraftReference{}, err
	}
	var parentVersionID string
	path := splitPipe(values["hierarchyPath"])
	if len(path) > 0 {
		parentHash := boundValueHash(dimensionVersionID, path[len(path)-1])
		err = tx.QueryRow(ctx, `SELECT id::text FROM askdata.dimension_members
			WHERE domain_id=$1 AND dimension_version_id=$2 AND member_key_hash=$3
			  AND status IN ('DRAFT','CERTIFIED') ORDER BY version_no DESC LIMIT 1`,
			batch.DomainID, dimensionVersionID, parentHash).Scan(&parentVersionID)
		if err != nil {
			return DraftReference{}, ErrImportDraftContract
		}
	}
	hash, _ := draftContentHash(values, map[string]any{"dimensionVersionId": dimensionVersionID})
	_, err = tx.Exec(ctx, `INSERT INTO askdata.dimension_members(
		id,tenant_id,domain_id,member_id,version_no,dimension_version_id,
		member_key,member_key_hash,canonical_label,parent_member_version_id,
		sensitivity,valid_from,valid_to,definition,status,content_hash,created_by
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11,$12,$13,$14,
		'DRAFT',$15,$16)`, versionID, batch.TenantID, batch.DomainID, objectID, versionNo,
		dimensionVersionID, values["canonicalValue"], lookupHash, values["displayLabel"],
		parentVersionID, values["sensitivity"], validFrom, validTo,
		values["definition"], hash, batch.CreatedBy)
	if err != nil {
		return DraftReference{}, err
	}
	aliases := append([]string{values["displayLabel"]}, splitPipe(values["aliases"])...)
	aliases = uniqueStrings(aliases)
	for _, alias := range aliases {
		aliasHash, _ := draftContentHash(map[string]string{"alias": alias}, map[string]any{"memberVersionId": versionID})
		_, err = tx.Exec(ctx, `INSERT INTO askdata.dimension_member_aliases(
			id,tenant_id,domain_id,dimension_version_id,member_version_id,alias,
			normalized_alias,source,priority,status,content_hash,created_by
		) VALUES($1,$2,$3,$4,$5,$6,$7,'IMPORT',100,'DRAFT',$8,$9)`, uuid.NewString(),
			batch.TenantID, batch.DomainID, dimensionVersionID, versionID, alias,
			normalizedLookup(alias), aliasHash, batch.CreatedBy)
		if err != nil {
			return DraftReference{}, err
		}
	}
	return draftRef(objectID, versionID, nil)
}

func createHierarchyDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	dimensionVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "DIMENSION", values["dimensionCode"])
	if err != nil {
		return DraftReference{}, err
	}
	if values["parentDimensionCode"] != "" {
		if _, _, err := resolveCertifiedCode(ctx, tx, batch, "DIMENSION", values["parentDimensionCode"]); err != nil {
			return DraftReference{}, err
		}
	}
	ordinal, _ := strconv.Atoi(values["levelOrder"])
	var objectID, versionID, existingName string
	err = tx.QueryRow(ctx, `SELECT hierarchy.hierarchy_id::text,hierarchy.id::text,hierarchy.name
		FROM askdata.semantic_import_rows AS import_row
		JOIN askdata.hierarchies AS hierarchy
		  ON hierarchy.id=import_row.created_version_id
		 AND hierarchy.domain_id=$2 AND hierarchy.status='DRAFT'
		WHERE import_row.import_id=$1 AND import_row.validation_state='COMMITTED'
		  AND hierarchy.code=$3
		ORDER BY import_row.row_no LIMIT 1 FOR UPDATE OF hierarchy`,
		batch.ID, batch.DomainID, values["code"]).Scan(&objectID, &versionID, &existingName)
	if errors.Is(err, pgx.ErrNoRows) {
		var versionNo int
		objectID, versionNo, err = nextCodeVersion(ctx, tx, batch, AssetHierarchy, values["code"])
		if err != nil {
			return DraftReference{}, err
		}
		versionID = uuid.NewString()
		hash, hashErr := draftContentHash(values, map[string]any{"dimensionVersionId": dimensionVersionID})
		if hashErr != nil {
			return DraftReference{}, hashErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO askdata.hierarchies(
			id,tenant_id,domain_id,hierarchy_id,version_no,code,name,description,
			status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,'','DRAFT',$8,$9)`, versionID, batch.TenantID,
			batch.DomainID, objectID, versionNo, values["code"], values["name"], hash, batch.CreatedBy)
	} else if err == nil && existingName != values["name"] {
		return DraftReference{}, ErrImportDraftContract
	}
	if err != nil {
		return DraftReference{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.hierarchy_levels(
		tenant_id,domain_id,hierarchy_version_id,dimension_version_id,ordinal
	) VALUES($1,$2,$3,$4,$5)`, batch.TenantID, batch.DomainID, versionID, dimensionVersionID, ordinal)
	if err != nil {
		return DraftReference{}, err
	}
	if err := refreshHierarchyHash(ctx, tx, versionID, values["code"], values["name"]); err != nil {
		return DraftReference{}, err
	}
	return draftRef(objectID, versionID, nil)
}

func refreshHierarchyHash(ctx context.Context, tx pgx.Tx, versionID, code, name string) error {
	rows, err := tx.Query(ctx, `SELECT dimension_version_id::text,ordinal
		FROM askdata.hierarchy_levels WHERE hierarchy_version_id=$1 ORDER BY ordinal`, versionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	levels := []map[string]any{}
	for rows.Next() {
		var dimensionVersionID string
		var ordinal int
		if err := rows.Scan(&dimensionVersionID, &ordinal); err != nil {
			return err
		}
		levels = append(levels, map[string]any{
			"dimensionVersionId": dimensionVersionID, "ordinal": ordinal,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	hash, err := draftContentHash(map[string]string{"code": code, "name": name}, map[string]any{"levels": levels})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE askdata.hierarchies SET content_hash=$1
		WHERE id=$2 AND status='DRAFT'`, hash, versionID)
	return err
}

func createRelationshipDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	leftVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "MODEL", values["leftModelCode"])
	if err != nil {
		return DraftReference{}, err
	}
	rightVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "MODEL", values["rightModelCode"])
	if err != nil {
		return DraftReference{}, err
	}
	bridgeVersionID := ""
	if values["bridgeModelCode"] != "" {
		bridgeVersionID, _, err = resolveCertifiedCode(ctx, tx, batch, "MODEL", values["bridgeModelCode"])
		if err != nil {
			return DraftReference{}, err
		}
	}
	var objectID string
	err = tx.QueryRow(ctx, `SELECT relationship_id::text FROM askdata.relationships
		WHERE domain_id=$1 AND left_model_version_id=$2 AND right_model_version_id=$3
		ORDER BY version_no DESC LIMIT 1`, batch.DomainID, leftVersionID, rightVersionID).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID, err = uuid.NewString(), nil
	}
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "relationships", "relationship_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	validFrom, validTo, err := parseValidity(values["validFrom"], values["validTo"])
	if err != nil {
		return DraftReference{}, err
	}
	hash, _ := draftContentHash(values, map[string]any{"left": leftVersionID, "right": rightVersionID})
	_, err = tx.Exec(ctx, `INSERT INTO askdata.relationships(
		id,tenant_id,domain_id,relationship_id,version_no,left_model_version_id,
		right_model_version_id,relationship_type,join_type,cardinality,join_ast,
		fanout_policy,bridge_model_version_id,valid_from,valid_to,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,'MODEL_JOIN',$8,$9,$10,$11,NULLIF($12,'')::uuid,
		$13,$14,'DRAFT',$15,$16)`, versionID, batch.TenantID, batch.DomainID, objectID,
		versionNo, leftVersionID, rightVersionID, values["joinType"], values["cardinality"],
		json.RawMessage(values["joinAst"]), values["fanoutPolicy"], bridgeVersionID,
		validFrom, validTo, hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func createTermDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	targetKind := values["termType"]
	if targetKind == "TIME" {
		targetKind = "TIME_CONTRACT"
	}
	targetVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, targetKind, values["targetCode"])
	if err != nil && targetKind != "OPERATOR" {
		return DraftReference{}, err
	}
	if targetKind == "OPERATOR" {
		targetVersionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("askdata/operator/"+values["targetCode"])).String()
	}
	var objectID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM askdata.business_terms
		WHERE domain_id=$1 AND term=$2 AND term_type=$3`, batch.DomainID, values["term"], values["termType"]).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO askdata.business_terms(
			id,tenant_id,domain_id,term,term_type,created_by
		) VALUES($1,$2,$3,$4,$5,$6)`, objectID, batch.TenantID, batch.DomainID,
			values["term"], values["termType"], batch.CreatedBy)
	}
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "business_term_versions", "business_term_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	priority, _ := strconv.Atoi(values["priority"])
	validFrom, validTo, err := parseValidity(values["validFrom"], values["validTo"])
	if err != nil {
		return DraftReference{}, err
	}
	hash, _ := draftContentHash(values, map[string]any{"targetVersionId": targetVersionID})
	matchPattern := ""
	if values["matchMode"] == "REGEX_SAFE" {
		matchPattern = values["term"]
	}
	_, err = tx.Exec(ctx, `INSERT INTO askdata.business_term_versions(
		id,tenant_id,domain_id,business_term_id,version_no,status,
		target_object_type,target_version_id,target_code,match_mode,match_pattern,
		priority,negative_contexts,valid_from,valid_to,source,review_status,
		code,name,definition,aliases,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,
		'PENDING',$16,$17,'','{}'::text[],$18,$19)`, versionID, batch.TenantID,
		batch.DomainID, objectID, versionNo, targetKind, targetVersionID, values["targetCode"],
		values["matchMode"], matchPattern, priority, splitPipe(values["negativeContexts"]), validFrom, validTo,
		normalizeTermSource(values["source"]), values["term"], values["term"], hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func createCertifiedExampleDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	questionHash := string(askdata.HashBytes([]byte(values["question"])))
	objectID, err := ensureQuestionIdentity(ctx, tx, batch, "certified_examples", questionHash)
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "certified_example_versions", "certified_example_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	metricIDs, err := resolveCodeList(ctx, tx, batch, "METRIC", values["expectedMetricCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	dimensionIDs, err := resolveCodeList(ctx, tx, batch, "DIMENSION", values["expectedDimensionCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	roleIDs, err := resolveRoleList(ctx, tx, batch, values["applicableRoles"])
	if err != nil {
		return DraftReference{}, err
	}
	versionID := uuid.NewString()
	members, _ := json.Marshal(splitPipe(values["expectedMemberValues"]))
	hash, _ := draftContentHash(values, nil)
	_, err = tx.Exec(ctx, `INSERT INTO askdata.certified_example_versions(
		id,tenant_id,domain_id,certified_example_id,version_no,question,
		expected_metric_version_ids,expected_dimension_version_ids,expected_member_values,
		expected_time_expression,applicable_role_ids,notes,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'DRAFT',$13,$14)`, versionID,
		batch.TenantID, batch.DomainID, objectID, versionNo, values["question"], metricIDs,
		dimensionIDs, members, values["expectedTimeExpression"], roleIDs, values["notes"], hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func createKPIBundleDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	metricIDs, err := resolveCodeList(ctx, tx, batch, "METRIC", values["metricCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	dimensionIDs, err := resolveCodeList(ctx, tx, batch, "DIMENSION", values["defaultDimensionCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	var objectID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM askdata.kpi_bundles
		WHERE domain_id=$1 AND code=$2`, batch.DomainID, values["code"]).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO askdata.kpi_bundles(
			id,tenant_id,domain_id,code,name,owner_user_id
		) VALUES($1,$2,$3,$4,$5,$6)`, objectID, batch.TenantID, batch.DomainID,
			values["code"], values["name"], batch.CreatedBy)
	}
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "kpi_bundle_versions", "kpi_bundle_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	items := make([]registry.KPIBundleItem, len(metricIDs))
	chartTypes := splitPipe(values["defaultChartTypes"])
	for index, metricID := range metricIDs {
		role := "BREAKDOWN"
		if index == 0 {
			role = "HEADLINE"
		}
		chartType := firstOrEmpty(chartTypes)
		if index < len(chartTypes) {
			chartType = chartTypes[index]
		}
		items[index] = registry.KPIBundleItem{
			MetricVersionID: metricID, Role: role, GroupByDimensionVersionIDs: dimensionIDs,
			ChartType: chartType, Order: index + 1,
		}
	}
	itemsJSON, _ := registry.CanonicalValue(items)
	roleMapping := json.RawMessage(`{}`)
	if values["roleMapping"] != "" {
		roleMapping = json.RawMessage(values["roleMapping"])
	}
	versionID := uuid.NewString()
	defaultTime := values["defaultTimeExpression"]
	if defaultTime == "" {
		return DraftReference{}, ErrImportDraftContract
	}
	bundle := registry.KPIBundle{
		VersionIdentity: registry.VersionIdentity{
			ID: versionID, TenantID: batch.TenantID, DomainID: batch.DomainID,
			ObjectID: objectID, VersionNo: versionNo, Status: registry.VersionStatusDraft,
			OwnerID: batch.CreatedBy,
		},
		Code: values["code"], Name: values["name"], Items: items,
		DefaultDimensionVersionIDs: dimensionIDs, DefaultTimeExpression: defaultTime,
		DefaultChartTypes: splitPipe(values["defaultChartTypes"]), RoleMapping: roleMapping,
		ApplicableQuestionPatterns: splitPipe(values["applicableQuestionTypes"]),
	}
	hash := registry.KPIBundleContentHash(bundle)
	_, err = tx.Exec(ctx, `INSERT INTO askdata.kpi_bundle_versions(
		id,tenant_id,domain_id,kpi_bundle_id,version_no,status,items,
		default_dimension_version_ids,default_time_expression,default_chart_types,
		role_mapping,applicable_question_patterns,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$8,$9,$10,$11,$12,$13)`, versionID,
		batch.TenantID, batch.DomainID, objectID, versionNo, itemsJSON, dimensionIDs,
		defaultTime, splitPipe(values["defaultChartTypes"]), roleMapping,
		splitPipe(values["applicableQuestionTypes"]), hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func createEvaluationCaseDraft(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string) (DraftReference, error) {
	questionHash := string(askdata.HashBytes([]byte(values["question"])))
	objectID, err := ensureQuestionIdentity(ctx, tx, batch, "evaluation_case_assets", questionHash)
	if err != nil {
		return DraftReference{}, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "evaluation_case_versions", "evaluation_case_asset_id", objectID)
	if err != nil {
		return DraftReference{}, err
	}
	metricIDs, err := resolveCodeList(ctx, tx, batch, "METRIC", values["expectedMetricCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	dimensionIDs, err := resolveCodeList(ctx, tx, batch, "DIMENSION", values["expectedDimensionCodes"])
	if err != nil {
		return DraftReference{}, err
	}
	shardID, _ := strconv.Atoi(values["shardId"])
	members, _ := json.Marshal(splitPipe(values["expectedMemberValues"]))
	resultHint := json.RawMessage(`{}`)
	if values["expectedResultHint"] != "" {
		resultHint = json.RawMessage(values["expectedResultHint"])
	}
	versionID := uuid.NewString()
	hash, _ := draftContentHash(values, nil)
	_, err = tx.Exec(ctx, `INSERT INTO askdata.evaluation_case_versions(
		id,tenant_id,domain_id,evaluation_case_asset_id,version_no,question,
		actor_role,expected_outcome,expected_metric_version_ids,
		expected_dimension_version_ids,expected_member_values,expected_time_expression,
		expected_result_hint,set_type,shard_id,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::text,$14,$15,
		'DRAFT',$16,$17)`, versionID, batch.TenantID, batch.DomainID, objectID, versionNo,
		values["question"], values["actorRole"], values["expectedOutcome"], metricIDs,
		dimensionIDs, members, values["expectedTimeExpression"], string(resultHint),
		values["setType"], shardID, hash, batch.CreatedBy)
	return draftRef(objectID, versionID, err)
}

func resolveOwner(ctx context.Context, tx pgx.Tx, batch SemanticImport, email string) (string, error) {
	if email == "" {
		return batch.CreatedBy, nil
	}
	var ownerID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM platform.users
		WHERE tenant_id=$1 AND lower(email::text)=lower($2) AND status='ACTIVE'
		  AND deleted_at IS NULL`, batch.TenantID, email).Scan(&ownerID)
	if err != nil {
		return "", ErrImportDraftContract
	}
	return ownerID, nil
}

func resolveCertifiedCode(
	ctx context.Context,
	tx pgx.Tx,
	batch SemanticImport,
	kind, code string,
) (string, string, error) {
	if kind == "MEMBER" {
		dimensionCode, memberValue, err := parseMemberTargetCode(code)
		if err != nil {
			return "", "", ErrImportDraftContract
		}
		dimensionVersionID, _, err := resolveCertifiedCode(ctx, tx, batch, "DIMENSION", dimensionCode)
		if err != nil {
			return "", "", err
		}
		var versionID, objectID string
		err = tx.QueryRow(ctx, `SELECT member_version_id::text,member_id::text
			FROM askdata.resolve_governed_import_member($1,$2,$3)`, batch.DomainID,
			dimensionVersionID, boundValueHash(dimensionVersionID, memberValue)).
			Scan(&versionID, &objectID)
		if err != nil {
			return "", "", ErrImportDraftContract
		}
		return versionID, objectID, nil
	}
	var versionID, objectID string
	var query string
	switch kind {
	case "ENTITY":
		query = `SELECT id::text,entity_id::text FROM askdata.entities WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED' ORDER BY version_no DESC LIMIT 1`
	case "MODEL":
		query = `SELECT id::text,model_id::text FROM askdata.semantic_models WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED' ORDER BY version_no DESC LIMIT 1`
	case "MEASURE":
		query = `SELECT id::text,measure_id::text FROM askdata.measures WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED' ORDER BY version_no DESC LIMIT 1`
	case "METRIC":
		query = `SELECT version.id::text,metric.id::text FROM askdata.metrics AS metric JOIN askdata.metric_versions AS version ON version.metric_id=metric.id AND version.tenant_id=metric.tenant_id WHERE metric.domain_id=$1 AND metric.code=$2 AND version.status='CERTIFIED' ORDER BY version.version_no DESC LIMIT 1`
	case "DIMENSION":
		query = `SELECT id::text,dimension_id::text FROM askdata.dimensions WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED' ORDER BY version_no DESC LIMIT 1`
	case "HIERARCHY":
		query = `SELECT id::text,hierarchy_id::text FROM askdata.hierarchies WHERE domain_id=$1 AND code=$2 AND status='CERTIFIED' ORDER BY version_no DESC LIMIT 1`
	case "TIME_CONTRACT":
		query = `SELECT version.id::text,contract.id::text FROM askdata.time_contracts AS contract JOIN askdata.time_contract_versions AS version ON version.time_contract_id=contract.id AND version.tenant_id=contract.tenant_id WHERE contract.domain_id=$1 AND contract.code=$2 AND version.status='CERTIFIED' ORDER BY version.version_no DESC LIMIT 1`
	default:
		return "", "", ErrImportDraftContract
	}
	if err := tx.QueryRow(ctx, query, batch.DomainID, code).Scan(&versionID, &objectID); err != nil {
		return "", "", ErrImportDraftContract
	}
	return versionID, objectID, nil
}

func resolveCodeList(ctx context.Context, tx pgx.Tx, batch SemanticImport, kind, value string) ([]string, error) {
	codes := splitPipe(value)
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		versionID, _, err := resolveCertifiedCode(ctx, tx, batch, kind, code)
		if err != nil {
			return nil, err
		}
		result = append(result, versionID)
	}
	return result, nil
}

func resolveRoleList(ctx context.Context, tx pgx.Tx, batch SemanticImport, value string) ([]string, error) {
	roles := splitPipe(value)
	result := make([]string, 0, len(roles))
	for _, code := range roles {
		var roleID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.roles
			WHERE tenant_id=$1 AND code=$2 AND status='ACTIVE' AND deleted_at IS NULL`,
			batch.TenantID, code).Scan(&roleID); err != nil {
			return nil, ErrImportDraftContract
		}
		result = append(result, roleID)
	}
	return result, nil
}

func nextCodeVersion(ctx context.Context, tx pgx.Tx, batch SemanticImport, asset AssetType, code string) (string, int, error) {
	table, identityColumn := "", ""
	switch asset {
	case AssetModel:
		table, identityColumn = "semantic_models", "model_id"
	case AssetMeasure:
		table, identityColumn = "measures", "measure_id"
	case AssetDimension:
		table, identityColumn = "dimensions", "dimension_id"
	case AssetHierarchy:
		table, identityColumn = "hierarchies", "hierarchy_id"
	default:
		return "", 0, ErrImportDraftContract
	}
	query := fmt.Sprintf(`SELECT %s::text FROM askdata.%s WHERE domain_id=$1 AND code=$2 ORDER BY version_no DESC LIMIT 1`, identityColumn, table)
	var objectID string
	err := tx.QueryRow(ctx, query, batch.DomainID, code).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID, err = uuid.NewString(), nil
	}
	if err != nil {
		return "", 0, err
	}
	versionNo, err := nextVersionNo(ctx, tx, table, identityColumn, objectID)
	return objectID, versionNo, err
}

func nextMetricVersion(ctx context.Context, tx pgx.Tx, batch SemanticImport, values map[string]string, ownerID string) (string, int, error) {
	var metricID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM askdata.metrics WHERE domain_id=$1 AND code=$2`,
		batch.DomainID, values["code"]).Scan(&metricID)
	if errors.Is(err, pgx.ErrNoRows) {
		metricID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO askdata.metrics(
			id,tenant_id,domain_id,code,name,description,status,owner_id
		) VALUES($1,$2,$3,$4,$5,$6,'DRAFT',$7)`, metricID, batch.TenantID,
			batch.DomainID, values["code"], values["name"], values["description"], ownerID)
	}
	if err != nil {
		return "", 0, err
	}
	versionNo, err := nextVersionNo(ctx, tx, "metric_versions", "metric_id", metricID)
	return metricID, versionNo, err
}

func nextVersionNo(ctx context.Context, tx pgx.Tx, table, identityColumn, objectID string) (int, error) {
	allowed := map[string]string{
		"semantic_models": "model_id", "measures": "measure_id", "metric_versions": "metric_id",
		"dimensions": "dimension_id", "dimension_members": "member_id", "hierarchies": "hierarchy_id",
		"relationships": "relationship_id", "business_term_versions": "business_term_id",
		"metric_dimension_versions": "metric_dimension_id", "certified_example_versions": "certified_example_id",
		"kpi_bundle_versions": "kpi_bundle_id", "evaluation_case_versions": "evaluation_case_asset_id",
	}
	if allowed[table] != identityColumn {
		return 0, ErrImportDraftContract
	}
	query := fmt.Sprintf(`SELECT COALESCE(max(version_no),0)+1 FROM askdata.%s WHERE %s=$1`, table, identityColumn)
	var versionNo int
	if err := tx.QueryRow(ctx, query, objectID).Scan(&versionNo); err != nil || versionNo < 1 {
		return 0, ErrImportDraftContract
	}
	var draftExists bool
	query = fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM askdata.%s WHERE %s=$1 AND status='DRAFT')`, table, identityColumn)
	if err := tx.QueryRow(ctx, query, objectID).Scan(&draftExists); err != nil {
		return 0, err
	}
	if draftExists {
		return 0, ErrImportDraftContract
	}
	return versionNo, nil
}

func ensureQuestionIdentity(ctx context.Context, tx pgx.Tx, batch SemanticImport, table, questionHash string) (string, error) {
	if table != "certified_examples" && table != "evaluation_case_assets" {
		return "", ErrImportDraftContract
	}
	query := fmt.Sprintf(`SELECT id::text FROM askdata.%s WHERE domain_id=$1 AND question_hash=$2`, table)
	var objectID string
	err := tx.QueryRow(ctx, query, batch.DomainID, questionHash).Scan(&objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		objectID = uuid.NewString()
		query = fmt.Sprintf(`INSERT INTO askdata.%s(id,tenant_id,domain_id,question_hash,created_by) VALUES($1,$2,$3,$4,$5)`, table)
		_, err = tx.Exec(ctx, query, objectID, batch.TenantID, batch.DomainID, questionHash, batch.CreatedBy)
	}
	return objectID, err
}

func draftContentHash(values map[string]string, resolved map[string]any) (string, error) {
	payload := map[string]any{"values": values}
	if resolved != nil {
		payload["resolved"] = resolved
	}
	canonical, err := registry.CanonicalValue(payload)
	if err != nil {
		return "", err
	}
	return string(askdata.HashBytes(canonical)), nil
}

func draftRef(objectID, versionID string, err error) (DraftReference, error) {
	if err != nil {
		return DraftReference{}, err
	}
	return DraftReference{ObjectID: objectID, VersionID: versionID, Status: "DRAFT"}, nil
}

func splitPipe(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, "|")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return uniqueStrings(result)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := normalizedLookup(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func normalizedLookup(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func boundValueHash(dimensionVersionID, value string) string {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(dimensionVersionID), []byte(normalizedLookup(value)),
	}, []byte{0}))
	return hex.EncodeToString(digest[:])
}

func parseValidity(from, to string) (time.Time, *time.Time, error) {
	start, err := parseImportTime(from)
	if err != nil {
		return time.Time{}, nil, ErrImportDraftContract
	}
	if to == "" {
		return start, nil, nil
	}
	end, err := parseImportTime(to)
	if err != nil || !end.After(start) {
		return time.Time{}, nil, ErrImportDraftContract
	}
	return start, &end, nil
}

func parseImportTime(value string) (time.Time, error) {
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func collectStringFields(raw json.RawMessage, key string) []string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	found := []string{}
	var visit func(any)
	visit = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for field, child := range typed {
				if field == key {
					if text, ok := child.(string); ok && text != "" {
						found = append(found, text)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	sort.Strings(found)
	return uniqueStrings(found)
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func normalizeTermSource(source string) string {
	switch source {
	case "MANUAL", "IMPORT", "CERTIFIED_EXAMPLE", "FEEDBACK_CANDIDATE":
		return source
	default:
		return "IMPORT"
	}
}
