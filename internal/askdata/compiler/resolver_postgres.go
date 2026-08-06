package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresContractStore is the authoritative QUERY-002 source. Every lookup
// is one read-only repeatable-read transaction pinned to an explicit release;
// it never consults the domain's currently active release.
type PostgresContractStore struct{ pool *pgxpool.Pool }

func NewPostgresContractStore(pool *pgxpool.Pool) *PostgresContractStore {
	return &PostgresContractStore{pool: pool}
}

func (store *PostgresContractStore) LoadContractSnapshot(
	ctx context.Context,
	lookup ContractLookup,
) (snapshot ContractSnapshot, err error) {
	if store == nil || store.pool == nil || lookup.Validate() != nil || validateDatabaseLookupIDs(lookup) != nil {
		return ContractSnapshot{}, ErrInvalidResolveRequest
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(lookup.Scope.ActorID) || access.DomainID != string(lookup.DomainID) {
		return ContractSnapshot{}, ErrInvalidResolveRequest
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return ContractSnapshot{}, fmt.Errorf("begin semantic contract transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true)`,
		lookup.Scope.TenantID, lookup.Scope.ActorID, lookup.DomainID); err != nil {
		return ContractSnapshot{}, fmt.Errorf("set semantic contract access context: %w", err)
	}
	if err := loadReleaseProof(ctx, tx, lookup, &snapshot); err != nil {
		return ContractSnapshot{}, err
	}
	if err := loadModelContract(ctx, tx, lookup, &snapshot.Model); err != nil {
		return ContractSnapshot{}, err
	}
	for _, metricVersionID := range lookup.MetricVersionIDs {
		metric, err := loadMetricContract(ctx, tx, lookup, metricVersionID)
		if err != nil {
			return ContractSnapshot{}, err
		}
		snapshot.Metrics = append(snapshot.Metrics, metric)
	}
	for _, dimensionVersionID := range lookup.DimensionVersionIDs {
		dimension, err := loadDimensionContract(ctx, tx, lookup, dimensionVersionID)
		if err != nil {
			return ContractSnapshot{}, err
		}
		snapshot.Dimensions = append(snapshot.Dimensions, dimension)
	}
	for _, memberVersionID := range lookup.MemberVersionIDs {
		member, parameterValue, err := loadMemberContract(ctx, tx, lookup, memberVersionID)
		if err != nil {
			return ContractSnapshot{}, err
		}
		snapshot.Members = append(snapshot.Members, member)
		if snapshot.memberParameterValues == nil {
			snapshot.memberParameterValues = make(map[askdata.ID]string, len(lookup.MemberVersionIDs))
		}
		snapshot.memberParameterValues[member.MemberVersionID] = parameterValue
	}
	for _, relationshipVersionID := range lookup.RelationshipVersionIDs {
		relationship, err := loadRelationshipContract(ctx, tx, lookup, relationshipVersionID)
		if err != nil {
			return ContractSnapshot{}, err
		}
		snapshot.Relationships = append(snapshot.Relationships, relationship)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContractSnapshot{}, fmt.Errorf("commit semantic contract transaction: %w", err)
	}
	return snapshot, nil
}

func validateDatabaseLookupIDs(lookup ContractLookup) error {
	values := []askdata.ID{
		lookup.Scope.TenantID, lookup.Scope.ActorID, lookup.DomainID,
		lookup.Scope.Release.ReleaseID, lookup.ModelVersionID,
	}
	values = append(values, lookup.MetricVersionIDs...)
	values = append(values, lookup.DimensionVersionIDs...)
	values = append(values, lookup.MemberVersionIDs...)
	values = append(values, lookup.RelationshipVersionIDs...)
	for _, value := range values {
		if uuid.Validate(string(value)) != nil {
			return ErrInvalidResolveRequest
		}
	}
	return nil
}

func loadReleaseProof(ctx context.Context, tx pgx.Tx, lookup ContractLookup, snapshot *ContractSnapshot) error {
	var manifestHash string
	var manifestCount, readyProjectionCount int
	err := tx.QueryRow(ctx, `SELECT release.status,release.object_count,
		askdata.release_manifest_hash(release.id),
		(SELECT count(*)::integer FROM askdata.release_objects AS object
		 WHERE object.tenant_id=release.tenant_id AND object.domain_id=release.domain_id
		   AND object.release_id=release.id),
		(SELECT count(*)::integer FROM askdata.release_projections AS projection
		 WHERE projection.tenant_id=release.tenant_id
		   AND projection.domain_id=release.domain_id
		   AND projection.release_id=release.id
		   AND projection.target IN ('POSTGRES_REGISTRY','EXECUTION_SEMANTIC_LAYER')
		   AND projection.status='READY'
		   AND projection.expected_content_hash=release.content_hash
		   AND projection.applied_content_hash=release.content_hash
		   AND projection.object_count=release.object_count)
	FROM askdata.releases AS release
	WHERE release.tenant_id=$1 AND release.domain_id=$2 AND release.id=$3
	  AND release.content_hash=$4`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID,
		lookup.Scope.Release.ContentHash).Scan(
		&snapshot.ReleaseStatus, &snapshot.ReleaseObjectCount,
		&manifestHash, &manifestCount, &readyProjectionCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: pinned release", ErrContractUnavailable)
	}
	if err != nil {
		return fmt.Errorf("load pinned release proof: %w", err)
	}
	if manifestHash != string(lookup.Scope.Release.ContentHash) ||
		manifestCount != snapshot.ReleaseObjectCount || readyProjectionCount != 2 {
		return fmt.Errorf("%w: release manifest or projection proof", ErrContractUnavailable)
	}
	snapshot.Release = lookup.Scope.Release
	return nil
}

func loadModelContract(ctx context.Context, tx pgx.Tx, lookup ContractLookup, target *ModelContract) error {
	var (
		manifestHash, modelStatus, datasetStatus, currentPublishedVersionID string
		datasetLive, versionStatus, modelLayer, versionLayer                string
		materializationLayer, versionSchemaHash                             string
		dslRaw                                                              []byte
		primaryTimeFieldID                                                  string
		rowCount                                                            *int64
	)
	err := tx.QueryRow(ctx, `SELECT model.content_hash,object.content_hash,model.status,
		model.dataset_id::text,model.dataset_version_id::text,model.materialization_id::text,
		model.dataset_schema_hash,model.layer,model.grain_contract,
		model.primary_time_field_id,
		dataset.status,CASE WHEN dataset.deleted_at IS NULL THEN 'LIVE' ELSE 'DELETED' END,
		COALESCE(dataset.current_published_version_id::text,''),
		version.status,version.layer,version.schema_hash,version.dsl_json,
		materialization.layer,materialization.status,materialization.published_schema,
		materialization.published_name,materialization.schema_hash,
		materialization.snapshot_hash,materialization.row_count
	FROM askdata.release_objects AS object
	JOIN askdata.semantic_models AS model
	  ON model.tenant_id=object.tenant_id AND model.domain_id=object.domain_id
	 AND model.id=object.object_version_id
	JOIN platform.datasets AS dataset
	  ON dataset.tenant_id=model.tenant_id AND dataset.id=model.dataset_id
	JOIN platform.dataset_versions AS version
	  ON version.tenant_id=model.tenant_id AND version.dataset_id=model.dataset_id
	 AND version.id=model.dataset_version_id
	JOIN platform.dataset_materializations AS materialization
	  ON materialization.tenant_id=model.tenant_id AND materialization.dataset_id=model.dataset_id
	 AND materialization.dataset_version_id=model.dataset_version_id
	 AND materialization.id=model.materialization_id
	WHERE object.tenant_id=$1 AND object.domain_id=$2 AND object.release_id=$3
	  AND object.object_type='SEMANTIC_MODEL' AND object.object_version_id=$4`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID,
		lookup.ModelVersionID).Scan(
		&target.ContentHash, &manifestHash, &modelStatus,
		&target.Materialization.DatasetID, &target.Materialization.DatasetVersionID,
		&target.Materialization.MaterializationID, &target.DatasetSchemaHash,
		&modelLayer, &target.GrainContract, &primaryTimeFieldID,
		&datasetStatus, &datasetLive, &currentPublishedVersionID,
		&versionStatus, &versionLayer, &versionSchemaHash, &dslRaw,
		&materializationLayer, &target.Materialization.Status, &target.Materialization.PublishedSchema,
		&target.Materialization.PublishedName, &target.Materialization.SchemaHash,
		&target.Materialization.SnapshotHash, &rowCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: pinned semantic model", ErrContractUnavailable)
	}
	if err != nil {
		return fmt.Errorf("load semantic model contract: %w", err)
	}
	target.ModelVersionID = lookup.ModelVersionID
	if rowCount != nil {
		target.Materialization.RowCount = *rowCount
	} else {
		target.Materialization.RowCount = -1
	}
	if manifestHash != string(target.ContentHash) || modelStatus != "CERTIFIED" ||
		datasetStatus != "PUBLISHED" || datasetLive != "LIVE" ||
		currentPublishedVersionID != string(target.Materialization.DatasetVersionID) ||
		versionStatus != "PUBLISHED" || (versionLayer != "DWS" && versionLayer != "ADS") ||
		modelLayer != versionLayer || materializationLayer != versionLayer ||
		versionSchemaHash != string(target.DatasetSchemaHash) ||
		target.Materialization.Status != "ACTIVE" ||
		target.Materialization.SchemaHash != target.DatasetSchemaHash || rowCount == nil {
		return ErrMaterializationStale
	}
	target.Materialization.Layer = materializationLayer
	prepared, err := dataset.Prepare(dslRaw)
	if err != nil || prepared.DSLHash != versionSchemaHash || string(prepared.Document.Dataset.Layer) != versionLayer {
		return ErrMaterializationStale
	}
	if primaryTimeFieldID != "" {
		value := askdata.ID(primaryTimeFieldID)
		target.PrimaryTimeFieldID = &value
	}
	for _, field := range prepared.Document.Fields {
		visible := true
		if field.Visible != nil {
			visible = *field.Visible
		}
		contract := FieldContract{
			FieldID: askdata.ID(field.ID), Code: field.Code, Role: field.Role,
			CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
			Nullable: field.Nullable, Visible: visible,
		}
		contract.ContractHash, err = fieldContractHash(contract)
		if err != nil {
			return fmt.Errorf("hash model field contract: %w", err)
		}
		target.Fields = append(target.Fields, contract)
	}
	return nil
}

func loadMetricContract(
	ctx context.Context, tx pgx.Tx, lookup ContractLookup, metricVersionID askdata.ID,
) (MetricContract, error) {
	var metric MetricContract
	var manifestHash, status string
	err := tx.QueryRow(ctx, `SELECT metric.id::text,metric.semantic_model_version_id::text,
		metric.content_hash,object.content_hash,metric.status,metric.formula_ast,
		metric.default_filters_ast,metric.unit,metric.time_grain,metric.additivity,metric.null_policy
	FROM askdata.release_objects AS object
	JOIN askdata.metric_versions AS metric
	  ON metric.tenant_id=object.tenant_id AND metric.domain_id=object.domain_id
	 AND metric.id=object.object_version_id
	WHERE object.tenant_id=$1 AND object.domain_id=$2 AND object.release_id=$3
	  AND object.object_type='METRIC' AND object.object_version_id=$4`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID, metricVersionID).Scan(
		&metric.MetricVersionID, &metric.ModelVersionID, &metric.ContentHash,
		&manifestHash, &status, &metric.FormulaAST, &metric.DefaultFilterAST,
		&metric.Unit, &metric.TimeGrain, &metric.Additivity, &metric.NullPolicy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MetricContract{}, fmt.Errorf("%w: metric %s", ErrContractUnavailable, metricVersionID)
	}
	if err != nil {
		return MetricContract{}, fmt.Errorf("load metric contract: %w", err)
	}
	if manifestHash != string(metric.ContentHash) || status != "CERTIFIED" {
		return MetricContract{}, fmt.Errorf("%w: metric manifest", ErrContractUnavailable)
	}
	rows, err := tx.Query(ctx, `SELECT measure.measure_id::text,measure.id::text,measure.semantic_model_version_id::text,
		measure.content_hash,object.content_hash,measure.status,measure.formula_ast,
		measure.aggregation,measure.additivity,measure.data_type,measure.unit
	FROM askdata.metric_version_measures AS link
	JOIN askdata.measures AS measure
	  ON measure.tenant_id=link.tenant_id AND measure.domain_id=link.domain_id
	 AND measure.id=link.measure_version_id
	JOIN askdata.release_objects AS object
	  ON object.tenant_id=measure.tenant_id AND object.domain_id=measure.domain_id
	 AND object.release_id=$3 AND object.object_type='MEASURE'
	 AND object.object_version_id=measure.id
	WHERE link.tenant_id=$1 AND link.domain_id=$2 AND link.metric_version_id=$4
	ORDER BY link.ordinal`, lookup.Scope.TenantID, lookup.DomainID,
		lookup.Scope.Release.ReleaseID, metricVersionID)
	if err != nil {
		return MetricContract{}, fmt.Errorf("load metric measures: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var measure MeasureContract
		var measureManifestHash, measureStatus string
		if err := rows.Scan(
			&measure.MeasureID, &measure.MeasureVersionID, &measure.ModelVersionID, &measure.ContentHash,
			&measureManifestHash, &measureStatus, &measure.FormulaAST, &measure.Aggregation,
			&measure.Additivity, &measure.DataType, &measure.Unit,
		); err != nil {
			return MetricContract{}, fmt.Errorf("scan metric measure: %w", err)
		}
		if measureManifestHash != string(measure.ContentHash) || measureStatus != "CERTIFIED" {
			return MetricContract{}, fmt.Errorf("%w: measure manifest", ErrContractUnavailable)
		}
		metric.Measures = append(metric.Measures, measure)
	}
	if err := rows.Err(); err != nil {
		return MetricContract{}, fmt.Errorf("iterate metric measures: %w", err)
	}
	if len(metric.Measures) == 0 || len(metric.Measures) > MaxResolvedMeasures {
		return MetricContract{}, fmt.Errorf("%w: metric measure set", ErrContractUnavailable)
	}
	return metric, nil
}

func loadDimensionContract(
	ctx context.Context, tx pgx.Tx, lookup ContractLookup, dimensionVersionID askdata.ID,
) (DimensionContract, error) {
	var result DimensionContract
	var manifestHash, status string
	var manifestSensitivity registry.Sensitivity
	err := tx.QueryRow(ctx, `SELECT dimension.id::text,dimension.semantic_model_version_id::text,
		dimension.logical_field_id,dimension.content_hash,object.content_hash,object.sensitivity,dimension.status,
		dimension.dimension_kind,dimension.sensitivity,dimension.member_index_policy
	FROM askdata.release_objects AS object
	JOIN askdata.dimensions AS dimension
	  ON dimension.tenant_id=object.tenant_id AND dimension.domain_id=object.domain_id
	 AND dimension.id=object.object_version_id
	WHERE object.tenant_id=$1 AND object.domain_id=$2 AND object.release_id=$3
	  AND object.object_type='DIMENSION' AND object.object_version_id=$4`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID, dimensionVersionID).Scan(
		&result.DimensionVersionID, &result.ModelVersionID, &result.LogicalFieldID,
		&result.ContentHash, &manifestHash, &manifestSensitivity, &status, &result.Kind,
		&result.Sensitivity, &result.MemberIndexPolicy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DimensionContract{}, fmt.Errorf("%w: dimension %s", ErrContractUnavailable, dimensionVersionID)
	}
	if err != nil {
		return DimensionContract{}, fmt.Errorf("load dimension contract: %w", err)
	}
	if manifestHash != string(result.ContentHash) || manifestSensitivity != result.Sensitivity || status != "CERTIFIED" {
		return DimensionContract{}, fmt.Errorf("%w: dimension manifest", ErrContractUnavailable)
	}
	return result, nil
}

func loadMemberContract(
	ctx context.Context, tx pgx.Tx, lookup ContractLookup, memberVersionID askdata.ID,
) (MemberContract, string, error) {
	var result MemberContract
	var manifestHash, status, parameterValue string
	var manifestSensitivity registry.Sensitivity
	var manifestContract json.RawMessage
	err := tx.QueryRow(ctx, `SELECT member.id::text,member.dimension_version_id::text,
		member.content_hash,object.content_hash,object.sensitivity,member.status,member.sensitivity,
		member.member_key,object.contract_json
	FROM askdata.release_objects AS object
	JOIN askdata.dimension_members AS member
	  ON member.tenant_id=object.tenant_id AND member.domain_id=object.domain_id
	 AND member.id=object.object_version_id
	WHERE object.tenant_id=$1 AND object.domain_id=$2 AND object.release_id=$3
	  AND object.object_type='MEMBER' AND object.object_version_id=$4
	  AND member.valid_from<=pg_catalog.transaction_timestamp()
	  AND (member.valid_to IS NULL OR pg_catalog.transaction_timestamp()<member.valid_to)`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID, memberVersionID).Scan(
		&result.MemberVersionID, &result.DimensionVersionID, &result.ContentHash,
		&manifestHash, &manifestSensitivity, &status, &result.Sensitivity, &parameterValue, &manifestContract,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberContract{}, "", fmt.Errorf("%w: member %s", ErrContractUnavailable, memberVersionID)
	}
	if err != nil {
		return MemberContract{}, "", fmt.Errorf("load member contract: %w", err)
	}
	if manifestHash != string(result.ContentHash) || manifestSensitivity != result.Sensitivity || status != "CERTIFIED" ||
		!safeMemberManifestContract(manifestContract, result.DimensionVersionID) ||
		!validMemberParameterValue(parameterValue) {
		return MemberContract{}, "", fmt.Errorf("%w: member manifest", ErrContractUnavailable)
	}
	return result, parameterValue, nil
}

func safeMemberManifestContract(raw []byte, dimensionVersionID askdata.ID) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 || fields["schemaVersion"] == nil ||
		fields["type"] == nil || fields["dimensionVersionId"] == nil || fields["aliasVersionIds"] == nil {
		return false
	}
	var contract struct {
		SchemaVersion      string       `json:"schemaVersion"`
		Type               string       `json:"type"`
		DimensionVersionID askdata.ID   `json:"dimensionVersionId"`
		AliasVersionIDs    []askdata.ID `json:"aliasVersionIds"`
	}
	if askdata.DecodeStrictJSON(raw, &contract) != nil || contract.DimensionVersionID != dimensionVersionID ||
		contract.SchemaVersion != "askdata-member-release-v1" || contract.Type != "MEMBER" ||
		len(contract.AliasVersionIDs) > 64 {
		return false
	}
	for index, value := range contract.AliasVersionIDs {
		if uuid.Validate(string(value)) != nil || (index > 0 && value <= contract.AliasVersionIDs[index-1]) {
			return false
		}
	}
	return true
}

func loadRelationshipContract(
	ctx context.Context, tx pgx.Tx, lookup ContractLookup, relationshipVersionID askdata.ID,
) (RelationshipContract, error) {
	var result RelationshipContract
	var manifestHash, status, relationshipType string
	err := tx.QueryRow(ctx, `SELECT relationship.id::text,relationship.content_hash,
		object.content_hash,relationship.status,relationship.relationship_type,
		relationship.left_model_version_id::text,relationship.right_model_version_id::text,
		relationship.join_ast,relationship.join_type,relationship.cardinality,
		relationship.fanout_policy
	FROM askdata.release_objects AS object
	JOIN askdata.relationships AS relationship
	  ON relationship.tenant_id=object.tenant_id AND relationship.domain_id=object.domain_id
	 AND relationship.id=object.object_version_id
	WHERE object.tenant_id=$1 AND object.domain_id=$2 AND object.release_id=$3
	  AND object.object_type='RELATIONSHIP' AND object.object_version_id=$4`,
		lookup.Scope.TenantID, lookup.DomainID, lookup.Scope.Release.ReleaseID,
		relationshipVersionID).Scan(
		&result.RelationshipVersionID, &result.ContentHash, &manifestHash, &status,
		&relationshipType, &result.LeftModelVersionID, &result.RightModelVersionID,
		&result.JoinAST, &result.JoinType, &result.Cardinality, &result.FanoutPolicy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RelationshipContract{}, fmt.Errorf("%w: relationship %s", ErrContractUnavailable, relationshipVersionID)
	}
	if err != nil {
		return RelationshipContract{}, fmt.Errorf("load relationship contract: %w", err)
	}
	if manifestHash != string(result.ContentHash) || status != "CERTIFIED" ||
		registry.RelationshipType(relationshipType) != registry.RelationshipModelJoin {
		return RelationshipContract{}, fmt.Errorf("%w: relationship manifest", ErrContractUnavailable)
	}
	return result, nil
}
