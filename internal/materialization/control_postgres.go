package materialization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

type publishedBuildTarget struct {
	DatasetID  string
	VersionID  string
	VersionNo  int
	Layer      Layer
	SchemaHash string
	Document   dataset.Document
}

type sourceTableSnapshot struct {
	DataSourceID        string
	DataSourceVersionID string
	DataSourceVersionNo int64
	SourceType          string
	MetadataTableID     string
	MetadataVersion     int64
	MetadataHash        string
	MetadataLastSyncAt  time.Time
	EstimatedRowCount   *int64
	FileVersionID       string
}

var _ ControlStore = (*PostgresStore)(nil)
var _ dataset.MappedPublicationCommitSink = (*PostgresStore)(nil)

// RegisterCurrent loads the target and every upstream under one tenant-scoped
// transaction, derives an immutable plan and inserts the run, input snapshots,
// node runs and audit event atomically. The v62 triggers are the final race
// fence if a publication pointer changes while this transaction is committing.
func (store *PostgresStore) RegisterCurrent(
	ctx context.Context,
	tenantID, actorID, datasetID string,
	control RegisterCurrentRequest,
) (run Run, created bool, err error) {
	if store == nil || store.pool == nil ||
		!validUUID(tenantID) || !validUUID(actorID) || !validUUID(datasetID) ||
		control.Mode != RunModeFull || control.PartitionKey != "" ||
		control.MaxAttempts < 1 || control.MaxAttempts > 10 {
		return Run{}, false, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var txErr error
		run, created, txErr = store.registerCurrentTx(
			ctx, tx, tenantID, actorID, datasetID, "", control, false,
		)
		return txErr
	})
	if err != nil {
		return Run{}, false, mapControlRegistrationError(err)
	}
	return run, created, nil
}

// EnqueueMappedDatasetMaterializationTx implements dataset.MappedPublicationCommitSink.
// It freezes the exact just-published ODS version in the caller's transaction and
// writes only QUEUED control-plane rows. Source extraction remains worker-owned.
func (store *PostgresStore) EnqueueMappedDatasetMaterializationTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	version dataset.VersionRecord,
) error {
	if store == nil || tx == nil ||
		!validUUID(tenantID) || !validUUID(actorID) ||
		!validUUID(version.DatasetID) || !validUUID(version.ID) ||
		version.Status != "PUBLISHED" || version.Layer != dataset.LayerODS {
		return ErrInvalidRequest
	}
	var mapped bool
	if err := tx.QueryRow(ctx, `SELECT origin_table_id IS NOT NULL
		FROM platform.datasets
		WHERE id=$1 AND current_published_version_id=$2
		  AND status='PUBLISHED' AND deleted_at IS NULL
		FOR SHARE`, version.DatasetID, version.ID).Scan(&mapped); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if !mapped {
		return ErrInvalidRequest
	}
	_, _, err := store.registerCurrentTx(
		ctx, tx, tenantID, actorID, version.DatasetID, version.ID,
		RegisterCurrentRequest{Mode: RunModeFull, MaxAttempts: 3},
		true,
	)
	return err
}

func (store *PostgresStore) registerCurrentTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID, expectedVersionID string,
	control RegisterCurrentRequest,
	allowSystemReplay bool,
) (run Run, created bool, err error) {
	target, err := loadPublishedBuildTargetTx(ctx, tx, datasetID)
	if err != nil {
		return Run{}, false, err
	}
	if expectedVersionID != "" && target.VersionID != expectedVersionID {
		return Run{}, false, ErrConflict
	}
	inputs, ordinals, err := deriveCurrentInputsTx(ctx, tx, datasetID, target)
	if err != nil {
		return Run{}, false, err
	}
	plan, err := deriveBuildPlan(target, control.Mode, ordinals)
	if err != nil {
		return Run{}, false, err
	}
	prepared, err := Prepare(RegisterRequest{
		Plan: plan, Inputs: inputs,
		PartitionKey: control.PartitionKey, MaxAttempts: control.MaxAttempts,
	})
	if err != nil {
		return Run{}, false, err
	}
	acceptExisting := func(existing Run) bool {
		if existing.DatasetID != datasetID ||
			existing.DatasetVersionID != target.VersionID ||
			existing.Layer != target.Layer ||
			existing.Mode != control.Mode ||
			existing.PlanHash != prepared.PlanHash ||
			existing.InputSnapshotHash != prepared.InputSnapshotHash ||
			existing.IdempotencyKey != prepared.IdempotencyKey {
			return false
		}
		if allowSystemReplay {
			// A historical manual registration may have chosen another retry
			// budget or actor. It is still the same frozen build; reuse it
			// instead of creating or requiring a parallel system task.
			return true
		}
		return existing.RequestHash == prepared.RequestHash &&
			existing.RequestedBy == actorID
	}

	existing, lookupErr := getRunByIdempotencyTx(
		ctx, tx, target.VersionID, prepared.IdempotencyKey,
	)
	if lookupErr == nil {
		if !acceptExisting(existing) {
			return Run{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(lookupErr, ErrNotFound) {
		return Run{}, false, lookupErr
	}

	row := tx.QueryRow(ctx, `INSERT INTO platform.dataset_build_runs(
		tenant_id,dataset_id,dataset_version_id,layer,run_mode,
		plan_version,plan_json,plan_hash,input_snapshot_hash,request_hash,
		idempotency_key,partition_key,requested_by,max_attempts
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
	)
	ON CONFLICT(tenant_id,dataset_version_id,idempotency_key) DO NOTHING
	RETURNING `+runSelectColumns,
		tenantID, datasetID, target.VersionID, target.Layer, control.Mode,
		PlanVersion, prepared.PlanJSON, prepared.PlanHash,
		prepared.InputSnapshotHash, prepared.RequestHash,
		prepared.IdempotencyKey, control.PartitionKey, actorID,
		control.MaxAttempts)
	inserted, scanErr := scanRun(row)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		existing, loadErr := getRunByIdempotencyTx(
			ctx, tx, target.VersionID, prepared.IdempotencyKey,
		)
		if loadErr != nil {
			return Run{}, false, loadErr
		}
		if !acceptExisting(existing) {
			return Run{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if scanErr != nil {
		return Run{}, false, scanErr
	}
	run, created = inserted, true

	for _, input := range prepared.Inputs {
		snapshot := input.SnapshotJSON
		if len(snapshot) == 0 {
			snapshot = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.build_run_inputs(
			tenant_id,build_run_id,ordinal_position,source_type,input_layer,
			input_data_source_id,input_data_source_version_id,
			metadata_table_id,file_version_id,input_dataset_id,input_dataset_version_id,
			input_materialization_id,source_version,schema_hash,snapshot_hash,snapshot_json,row_count
		) VALUES(
			$1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,
			NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,
			NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,
			$13,$14,$15,$16,$17
		)`, tenantID, run.ID, input.Ordinal, input.Type, input.Layer,
			input.DataSourceID, input.DataSourceVersionID,
			input.MetadataTableID, input.FileVersionID, input.DatasetID,
			input.DatasetVersionID, input.MaterializationID,
			input.SourceVersion, input.SchemaHash, input.SnapshotHash,
			snapshot, input.RowCount); err != nil {
			return Run{}, false, err
		}
	}
	for _, node := range prepared.Plan.Nodes {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.build_node_runs(
			tenant_id,build_run_id,node_id,node_kind,execution_engine
		) VALUES($1,$2,$3,$4,$5)`,
			tenantID, run.ID, node.ID, node.Kind, node.Engine); err != nil {
			return Run{}, false, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES(
		$1,$2,'REGISTER_MATERIALIZATION_BUILD','DATASET_BUILD_RUN',$3,
		jsonb_build_object(
			'datasetId',$4::text,'datasetVersionId',$5::text,
			'layer',$6::text,'mode',$7::text,'planHash',$8::text,
			'inputSnapshotHash',$9::text,'maxAttempts',$10::integer
		)
	)`, tenantID, actorID, run.ID, datasetID, target.VersionID,
		target.Layer, control.Mode, prepared.PlanHash,
		prepared.InputSnapshotHash, control.MaxAttempts)
	if err != nil {
		return Run{}, false, err
	}
	return run, created, nil
}

func loadPublishedBuildTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	datasetID string,
) (publishedBuildTarget, error) {
	var currentVersionID, ownerStatus, ownerLayer string
	err := tx.QueryRow(ctx, `SELECT COALESCE(current_published_version_id::text,''),
		status,layer
		FROM platform.datasets
		WHERE id=$1 AND deleted_at IS NULL
		FOR SHARE`, datasetID).
		Scan(&currentVersionID, &ownerStatus, &ownerLayer)
	if errors.Is(err, pgx.ErrNoRows) {
		return publishedBuildTarget{}, ErrNotFound
	}
	if err != nil {
		return publishedBuildTarget{}, err
	}
	if currentVersionID == "" || ownerStatus != "PUBLISHED" {
		return publishedBuildTarget{}, ErrConflict
	}

	var target publishedBuildTarget
	target.DatasetID = datasetID
	var dslJSON []byte
	var storedLayer string
	err = tx.QueryRow(ctx, `SELECT id::text,version_no,layer,schema_hash,dsl_json
		FROM platform.dataset_versions
		WHERE id=$1 AND dataset_id=$2 AND status='PUBLISHED'
		FOR SHARE`, currentVersionID, datasetID).
		Scan(
			&target.VersionID, &target.VersionNo, &storedLayer,
			&target.SchemaHash, &dslJSON,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return publishedBuildTarget{}, ErrConflict
	}
	if err != nil {
		return publishedBuildTarget{}, err
	}
	target.Layer = Layer(storedLayer)
	prepared, err := dataset.Prepare(dslJSON)
	if err != nil ||
		prepared.DSLHash != target.SchemaHash ||
		string(prepared.Document.Dataset.Layer) != storedLayer ||
		ownerLayer != storedLayer ||
		!prepared.Document.ExecutionPolicy.Materialization.Enabled {
		return publishedBuildTarget{}, ErrConflict
	}
	target.Document = prepared.Document
	return target, nil
}

func deriveCurrentInputsTx(
	ctx context.Context,
	tx pgx.Tx,
	targetDatasetID string,
	target publishedBuildTarget,
) ([]InputSnapshot, []int, error) {
	switch target.Layer {
	case LayerODS:
		if len(target.Document.Nodes) != 1 ||
			target.Document.Nodes[0].Type != "TABLE" ||
			len(target.Document.Joins) != 0 {
			return nil, nil, ErrInvalidRequest
		}
		input, err := deriveSourceInputTx(ctx, tx, target.Document.Nodes[0])
		if err != nil {
			return nil, nil, err
		}
		return []InputSnapshot{input}, []int{1}, nil
	case LayerDWD, LayerDWS:
		expectedLayer := LayerODS
		if target.Layer == LayerDWS {
			expectedLayer = LayerDWD
		}
		inputs := make([]InputSnapshot, 0, len(target.Document.Nodes))
		ordinals := make([]int, len(target.Document.Nodes))
		byVersion := make(map[string]int, len(target.Document.Nodes))
		for index, node := range target.Document.Nodes {
			if node.Type != "DATASET" || !validUUID(node.DatasetVersionID) {
				return nil, nil, ErrInvalidRequest
			}
			if ordinal, found := byVersion[node.DatasetVersionID]; found {
				ordinals[index] = ordinal
				continue
			}
			input, upstreamDatasetID, err := deriveDatasetInputTx(
				ctx, tx, node.DatasetVersionID, expectedLayer,
			)
			if err != nil {
				return nil, nil, err
			}
			if upstreamDatasetID == targetDatasetID {
				return nil, nil, ErrConflict
			}
			input.Ordinal = len(inputs) + 1
			inputs = append(inputs, input)
			byVersion[node.DatasetVersionID] = input.Ordinal
			ordinals[index] = input.Ordinal
		}
		if len(inputs) == 0 {
			return nil, nil, ErrInvalidRequest
		}
		return inputs, ordinals, nil
	default:
		return nil, nil, ErrInvalidRequest
	}
}

func deriveSourceInputTx(
	ctx context.Context,
	tx pgx.Tx,
	node dataset.Node,
) (InputSnapshot, error) {
	if !validUUID(node.DataSourceID) || !validUUID(node.TableID) {
		return InputSnapshot{}, ErrInvalidRequest
	}
	var snapshot sourceTableSnapshot
	var estimatedRows pgtype.Int8
	err := tx.QueryRow(ctx, `SELECT source.id::text,source_version.id::text,
			source_version.version_no,source.source_type::text,
			metadata_table.id::text,metadata_table.metadata_version,
			metadata_table.structure_hash,metadata_table.last_sync_at,
			metadata_table.estimated_row_count,
			COALESCE(source_version.file_version_id::text,'')
		FROM platform.metadata_tables AS metadata_table
		JOIN platform.data_sources AS source
		  ON source.id=metadata_table.data_source_id
		 AND source.tenant_id=metadata_table.tenant_id
		JOIN platform.data_source_versions AS source_version
		  ON source_version.id=source.current_published_version_id
		 AND source_version.data_source_id=source.id
		 AND source_version.tenant_id=source.tenant_id
		WHERE metadata_table.id=$1
		  AND metadata_table.data_source_id=$2
		  AND metadata_table.asset_status='ACTIVE'
		  AND metadata_table.management_status='ENABLED'
		  AND source.status='ACTIVE'
		  AND source.publication_status='PUBLISHED'
		  AND source.deleted_at IS NULL
		  AND source_version.source_type=source.source_type
		FOR SHARE OF metadata_table,source,source_version`,
		node.TableID, node.DataSourceID).
		Scan(
			&snapshot.DataSourceID, &snapshot.DataSourceVersionID,
			&snapshot.DataSourceVersionNo, &snapshot.SourceType,
			&snapshot.MetadataTableID, &snapshot.MetadataVersion,
			&snapshot.MetadataHash, &snapshot.MetadataLastSyncAt,
			&estimatedRows, &snapshot.FileVersionID,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return InputSnapshot{}, ErrConflict
	}
	if err != nil {
		return InputSnapshot{}, err
	}
	if estimatedRows.Valid {
		value := estimatedRows.Int64
		snapshot.EstimatedRowCount = &value
	}
	if !hashPattern.MatchString(snapshot.MetadataHash) {
		return InputSnapshot{}, ErrConflict
	}

	sourceVersion := fmt.Sprintf(
		"data-source-version:%d/metadata-version:%d",
		snapshot.DataSourceVersionNo, snapshot.MetadataVersion,
	)
	baseDocument := map[string]any{
		"contract":            "source-metadata-v1",
		"dataSourceVersionId": snapshot.DataSourceVersionID,
		"metadataTableId":     snapshot.MetadataTableID,
		"metadataVersion":     snapshot.MetadataVersion,
		"metadataLastSyncAt":  snapshot.MetadataLastSyncAt.UTC().Format(time.RFC3339Nano),
	}
	if snapshot.SourceType == "EXCEL" {
		if !validUUID(node.FileVersionID) ||
			node.FileVersionID != snapshot.FileVersionID {
			return InputSnapshot{}, ErrConflict
		}
		var fileVersion int
		var fileHash string
		err := tx.QueryRow(ctx, `SELECT version,sha256
			FROM platform.file_asset_versions
			WHERE id=$1
			FOR SHARE`, snapshot.FileVersionID).
			Scan(&fileVersion, &fileHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return InputSnapshot{}, ErrConflict
		}
		if err != nil {
			return InputSnapshot{}, err
		}
		if !hashPattern.MatchString(fileHash) {
			return InputSnapshot{}, ErrConflict
		}
		baseDocument["contract"] = "excel-file-metadata-v1"
		baseDocument["fileVersionId"] = snapshot.FileVersionID
		baseDocument["fileVersion"] = fileVersion
		snapshotJSON, err := json.Marshal(baseDocument)
		if err != nil {
			return InputSnapshot{}, err
		}
		return InputSnapshot{
			Ordinal: 1, Type: InputFileVersion, Layer: "SOURCE",
			DataSourceID:        snapshot.DataSourceID,
			DataSourceVersionID: snapshot.DataSourceVersionID,
			FileVersionID:       snapshot.FileVersionID,
			SourceVersion:       sourceVersion, SchemaHash: snapshot.MetadataHash,
			SnapshotHash: fileHash, SnapshotJSON: snapshotJSON,
			RowCount: snapshot.EstimatedRowCount,
		}, nil
	}
	if snapshot.SourceType != "MYSQL" && snapshot.SourceType != "ORACLE" ||
		node.FileVersionID != "" || snapshot.FileVersionID != "" {
		return InputSnapshot{}, ErrConflict
	}
	snapshotJSON, err := json.Marshal(baseDocument)
	if err != nil {
		return InputSnapshot{}, err
	}
	return InputSnapshot{
		Ordinal: 1, Type: InputSourceTable, Layer: "SOURCE",
		DataSourceID:        snapshot.DataSourceID,
		DataSourceVersionID: snapshot.DataSourceVersionID,
		MetadataTableID:     snapshot.MetadataTableID,
		SourceVersion:       sourceVersion, SchemaHash: snapshot.MetadataHash,
		SnapshotHash: sha256Hex(snapshotJSON), SnapshotJSON: snapshotJSON,
		RowCount: snapshot.EstimatedRowCount,
	}, nil
}

func deriveDatasetInputTx(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
	expectedLayer Layer,
) (InputSnapshot, string, error) {
	var datasetID, storedVersionID, storedLayer, versionHash string
	var materializationID, materializationLayer, materializationHash string
	var snapshotHash string
	var versionNo int
	var rowCount int64
	var activatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT owner.id::text,version.id::text,
			version.version_no,version.layer,version.schema_hash,
			materialization.id::text,materialization.layer,
			materialization.schema_hash,materialization.snapshot_hash,
			materialization.row_count,materialization.activated_at
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS owner
		  ON owner.id=version.dataset_id AND owner.tenant_id=version.tenant_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.dataset_id=owner.id
		 AND materialization.dataset_version_id=version.id
		 AND materialization.tenant_id=owner.tenant_id
		 AND materialization.status='ACTIVE'
		WHERE version.id=$1
		  AND version.status='PUBLISHED'
		  AND owner.status='PUBLISHED'
		  AND owner.current_published_version_id=version.id
		  AND owner.deleted_at IS NULL
		FOR SHARE OF version,owner,materialization`, versionID).
		Scan(
			&datasetID, &storedVersionID, &versionNo, &storedLayer, &versionHash,
			&materializationID, &materializationLayer, &materializationHash,
			&snapshotHash, &rowCount, &activatedAt,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return InputSnapshot{}, "", ErrConflict
	}
	if err != nil {
		return InputSnapshot{}, "", err
	}
	if storedLayer != string(expectedLayer) ||
		materializationLayer != string(expectedLayer) ||
		versionHash != materializationHash ||
		!hashPattern.MatchString(versionHash) ||
		!hashPattern.MatchString(snapshotHash) ||
		rowCount < 0 {
		return InputSnapshot{}, "", ErrConflict
	}
	snapshotJSON, err := json.Marshal(map[string]any{
		"contract":            "active-materialization-v1",
		"datasetVersionId":    storedVersionID,
		"materializationId":   materializationID,
		"materializationTime": activatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return InputSnapshot{}, "", err
	}
	return InputSnapshot{
		Type: InputMaterialization, Layer: string(expectedLayer),
		MaterializationID: materializationID,
		SourceVersion:     fmt.Sprintf("dataset-version:%d", versionNo),
		SchemaHash:        versionHash, SnapshotHash: snapshotHash,
		SnapshotJSON: snapshotJSON, RowCount: &rowCount,
	}, datasetID, nil
}

func deriveBuildPlan(
	target publishedBuildTarget,
	mode RunMode,
	inputOrdinals []int,
) (BuildPlan, error) {
	if mode != RunModeFull ||
		len(target.Document.Nodes) == 0 ||
		len(inputOrdinals) != len(target.Document.Nodes) {
		return BuildPlan{}, ErrInvalidRequest
	}
	plan := BuildPlan{
		Version: PlanVersion, DatasetID: target.DatasetID,
		DatasetVersionID: target.VersionID, Layer: target.Layer, Mode: mode,
		Target: TargetPlan{
			Storage: "POSTGRES", AtomicPublish: true,
			RelationKind: "TABLE", RefreshMode: string(mode), StableViewName: true,
		},
	}
	if target.Layer == LayerODS {
		plan.Nodes = []PlanNode{
			{
				ID: "extract", Kind: NodeExtract, Engine: EngineSourceDB,
				InputOrdinals: []int{inputOrdinals[0]},
			},
			{
				ID: "stage", Kind: NodeStage, Engine: EnginePostgres,
				DependsOn: []string{"extract"},
			},
			{
				ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres,
				DependsOn: []string{"stage"},
			},
		}
		return plan, nil
	}

	engine := EnginePostgres
	dependencies := make([]string, 0, len(target.Document.Nodes))
	for index, ordinal := range inputOrdinals {
		id := fmt.Sprintf("extract_%03d", index+1)
		plan.Nodes = append(plan.Nodes, PlanNode{
			ID: id, Kind: NodeExtract, Engine: engine,
			InputOrdinals: []int{ordinal},
		})
		dependencies = append(dependencies, id)
	}
	appendStep := func(id string, kind NodeKind) {
		plan.Nodes = append(plan.Nodes, PlanNode{
			ID: id, Kind: kind, Engine: engine,
			DependsOn: append([]string(nil), dependencies...),
		})
		dependencies = []string{id}
	}
	if len(target.Document.Joins) > 0 {
		appendStep("join", NodeJoin)
	}
	hasFilter := len(target.Document.Filters) > 0
	for _, node := range target.Document.Nodes {
		hasFilter = hasFilter || len(node.SourceFilters) > 0
	}
	if hasFilter {
		appendStep("filter", NodeFilter)
	}
	if target.Layer == LayerDWS {
		appendStep("aggregate", NodeAggregate)
		if len(target.Document.Having) > 0 {
			appendStep("having", NodeFilter)
		}
	}
	appendStep("project", NodeProject)
	appendStep("materialize", NodeMaterialize)
	return plan, nil
}

func (store *PostgresStore) ListBuilds(
	ctx context.Context,
	tenantID, datasetID string,
	limit, offset int,
) (items []Run, total int, err error) {
	if store == nil || store.pool == nil ||
		!validUUID(tenantID) || !validUUID(datasetID) ||
		limit < 1 || limit > MaxBuildPageLimit || offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	items = []Run{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.datasets WHERE id=$1 AND deleted_at IS NULL
		)`, datasetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.dataset_build_runs WHERE dataset_id=$1`,
			datasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+runSelectColumns+`
			FROM platform.dataset_build_runs
			WHERE dataset_id=$1
			ORDER BY created_at DESC,id DESC
			LIMIT $2 OFFSET $3`, datasetID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanRun(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, mapStoreError(err)
	}
	return items, total, nil
}

func (store *PostgresStore) GetBuild(
	ctx context.Context,
	tenantID, datasetID, buildID string,
) (detail BuildDetail, err error) {
	if store == nil || store.pool == nil ||
		!validUUID(tenantID) || !validUUID(datasetID) || !validUUID(buildID) {
		return BuildDetail{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runReturningColumns+`
			FROM platform.dataset_build_runs AS run
			JOIN platform.datasets AS owner
			  ON owner.id=run.dataset_id AND owner.tenant_id=run.tenant_id
			WHERE run.id=$1 AND run.dataset_id=$2 AND owner.deleted_at IS NULL`,
			buildID, datasetID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		detail.Build = buildFromRun(run)
		inputs, err := loadInputsTx(ctx, tx, buildID)
		if err != nil {
			return err
		}
		detail.Inputs = make([]BuildInput, len(inputs))
		for index, input := range inputs {
			detail.Inputs[index] = BuildInput{
				Ordinal: input.Ordinal, Type: input.Type, Layer: input.Layer,
				DataSourceID:        input.DataSourceID,
				DataSourceVersionID: input.DataSourceVersionID,
				MetadataTableID:     input.MetadataTableID,
				FileVersionID:       input.FileVersionID,
				DatasetID:           input.DatasetID, DatasetVersionID: input.DatasetVersionID,
				MaterializationID: input.MaterializationID,
				SourceVersion:     input.SourceVersion, SchemaHash: input.SchemaHash,
				SnapshotHash: input.SnapshotHash, RowCount: input.RowCount,
			}
		}
		nodes, err := loadBuildNodesTx(ctx, tx, buildID)
		if err != nil {
			return err
		}
		detail.Nodes = nodes
		materialized, err := loadBuildMaterializationTx(ctx, tx, buildID)
		if err != nil {
			return err
		}
		detail.Materialization = materialized
		return nil
	})
	if err != nil {
		return BuildDetail{}, mapStoreError(err)
	}
	if detail.Inputs == nil {
		detail.Inputs = []BuildInput{}
	}
	if detail.Nodes == nil {
		detail.Nodes = []BuildNode{}
	}
	return detail, nil
}

func loadBuildNodesTx(
	ctx context.Context,
	tx pgx.Tx,
	buildID string,
) ([]BuildNode, error) {
	rows, err := tx.Query(ctx, `SELECT node_id,node_kind,execution_engine,status,attempt,
		input_row_count,output_row_count,output_size_bytes,error_code,error_message,
		started_at,completed_at
		FROM platform.build_node_runs
		WHERE build_run_id=$1
		ORDER BY created_at,id`, buildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BuildNode{}
	for rows.Next() {
		var item BuildNode
		var inputRows, outputRows, outputBytes pgtype.Int8
		var startedAt, completedAt pgtype.Timestamptz
		if err := rows.Scan(
			&item.ID, &item.Kind, &item.Engine, &item.Status, &item.Attempt,
			&inputRows, &outputRows, &outputBytes,
			&item.ErrorCode, &item.ErrorMessage, &startedAt, &completedAt,
		); err != nil {
			return nil, err
		}
		item.InputRowCount = int64Pointer(inputRows)
		item.OutputRowCount = int64Pointer(outputRows)
		item.OutputSizeBytes = int64Pointer(outputBytes)
		item.StartedAt = timePointer(startedAt)
		item.CompletedAt = timePointer(completedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadBuildMaterializationTx(
	ctx context.Context,
	tx pgx.Tx,
	buildID string,
) (*BuildMaterialization, error) {
	var item BuildMaterialization
	var rowCount, sizeBytes pgtype.Int8
	var activatedAt pgtype.Timestamptz
	err := tx.QueryRow(ctx, `SELECT id::text,dataset_version_id::text,layer,status,
		schema_hash,snapshot_hash,row_count,size_bytes,activated_at
		FROM platform.dataset_materializations
		WHERE build_run_id=$1`, buildID).
		Scan(
			&item.ID, &item.DatasetVersionID, &item.Layer, &item.Status,
			&item.SchemaHash, &item.SnapshotHash, &rowCount, &sizeBytes, &activatedAt,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.RowCount = int64Pointer(rowCount)
	item.SizeBytes = int64Pointer(sizeBytes)
	item.ActivatedAt = timePointer(activatedAt)
	return &item, nil
}

func (store *PostgresStore) CancelQueued(
	ctx context.Context,
	tenantID, actorID, datasetID, buildID string,
) (run Run, err error) {
	if store == nil || store.pool == nil ||
		!validUUID(tenantID) || !validUUID(actorID) ||
		!validUUID(datasetID) || !validUUID(buildID) {
		return Run{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		current, err := scanRun(tx.QueryRow(ctx, `SELECT `+runReturningColumns+`
			FROM platform.dataset_build_runs AS run
			JOIN platform.datasets AS owner
			  ON owner.id=run.dataset_id AND owner.tenant_id=run.tenant_id
			WHERE run.id=$1 AND run.dataset_id=$2 AND owner.deleted_at IS NULL
			FOR UPDATE OF run`, buildID, datasetID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.Status != RunQueued {
			return ErrInvalidTransition
		}
		run, err = scanRun(tx.QueryRow(ctx, `UPDATE platform.dataset_build_runs
			SET status='CANCELLED',completed_at=now(),updated_at=now()
			WHERE id=$1 AND dataset_id=$2 AND status='QUEUED'
			RETURNING `+runSelectColumns, buildID, datasetID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidTransition
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(
			$1,$2,'CANCEL_MATERIALIZATION_BUILD','DATASET_BUILD_RUN',$3,
			jsonb_build_object(
				'datasetId',$4::text,'datasetVersionId',$5::text,
				'fromStatus','QUEUED','toStatus','CANCELLED'
			)
		)`, tenantID, actorID, run.ID, datasetID, run.DatasetVersionID)
		return err
	})
	if err != nil {
		return Run{}, mapStoreError(err)
	}
	return run, nil
}

func int64Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func mapControlRegistrationError(err error) error {
	if err == nil ||
		errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrIdempotencyConflict) {
		return err
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23514" {
		// Every request value was generated by this package. A late CHECK
		// failure therefore means a publication/source fence changed or the
		// persisted contract is no longer buildable, not malformed client SQL.
		return ErrConflict
	}
	return mapStoreError(err)
}
