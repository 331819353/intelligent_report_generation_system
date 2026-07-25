package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (store *PostgresStore) GetSettings(
	ctx context.Context,
	tenantID string,
) (item Settings, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var updatedAt time.Time
		queryErr := tx.QueryRow(ctx, `SELECT enabled,graph_projection_enabled,
				question_change_enabled,minimum_path_confidence::float8,
				maximum_path_hops,updated_at
			FROM platform.semantic_qa_settings
			WHERE tenant_id=platform.current_tenant_id()`).Scan(
			&item.Enabled, &item.GraphProjectionEnabled,
			&item.QuestionChangeEnabled, &item.MinimumPathConfidence,
			&item.MaximumPathHops, &updatedAt,
		)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return queryErr
	})
	return item, err
}

func (store *PostgresStore) UpdateSettings(
	ctx context.Context,
	tenantID, actorID string,
	input Settings,
) (Settings, error) {
	var item Settings
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var updatedAt time.Time
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_qa_settings(
				tenant_id,enabled,graph_projection_enabled,question_change_enabled,
				minimum_path_confidence,maximum_path_hops,updated_by,updated_at
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6::uuid,now()
			)
			ON CONFLICT(tenant_id) DO UPDATE SET
				enabled=EXCLUDED.enabled,
				graph_projection_enabled=EXCLUDED.graph_projection_enabled,
				question_change_enabled=EXCLUDED.question_change_enabled,
				minimum_path_confidence=EXCLUDED.minimum_path_confidence,
				maximum_path_hops=EXCLUDED.maximum_path_hops,
				updated_by=EXCLUDED.updated_by,updated_at=now()
			RETURNING enabled,graph_projection_enabled,question_change_enabled,
				minimum_path_confidence::float8,maximum_path_hops,updated_at`,
			input.Enabled, input.GraphProjectionEnabled,
			input.QuestionChangeEnabled, input.MinimumPathConfidence,
			input.MaximumPathHops, actorID,
		).Scan(
			&item.Enabled, &item.GraphProjectionEnabled,
			&item.QuestionChangeEnabled, &item.MinimumPathConfidence,
			&item.MaximumPathHops, &updatedAt,
		)
		if err != nil {
			return err
		}
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
	return item, err
}

func (store *PostgresStore) CreateChangeSet(
	ctx context.Context,
	tenantID, actorID string,
	input CreateChangeSetInput,
	questionHash, requestHash string,
) (ChangeSet, error) {
	var id string
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var enabled, questionEnabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled,question_change_enabled
			FROM platform.semantic_qa_settings
			WHERE tenant_id=platform.current_tenant_id()`).
			Scan(&enabled, &questionEnabled); err != nil {
			return err
		}
		if !enabled || (input.TriggerType == "QUESTION" && !questionEnabled) {
			return ErrDisabled
		}
		err := tx.QueryRow(ctx, `INSERT INTO platform.warehouse_dag_change_sets(
				tenant_id,target_dataset_id,trigger_type,change_kind,target_layer,
				title,question_hash,baseline_dataset_version,baseline_dsl_hash,
				request_key,request_hash,expected_operation_count,created_by
			) VALUES(
				platform.current_tenant_id(),NULLIF($1,'')::uuid,$2,$3,$4,$5,$6,
				$7,$8,$9,$10,$11,$12::uuid
			)
			ON CONFLICT(tenant_id,request_key) DO NOTHING
			RETURNING id::text`,
			input.TargetDatasetID, input.TriggerType, input.ChangeKind,
			input.TargetLayer, input.Title, questionHash,
			input.BaselineDatasetVersion, input.BaselineDSLHash,
			input.RequestKey, requestHash, len(input.Operations), actorID,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var existingHash string
			err = tx.QueryRow(ctx, `SELECT id::text,request_hash
				FROM platform.warehouse_dag_change_sets
				WHERE request_key=$1`, input.RequestKey).Scan(&id, &existingHash)
			if err != nil {
				return err
			}
			if existingHash != requestHash {
				return ErrConflict
			}
			return nil
		}
		if err != nil {
			return mapPostgresError(err)
		}
		for index, operation := range input.Operations {
			var value any
			if len(operation.Value) > 0 {
				if err := json.Unmarshal(operation.Value, &value); err != nil {
					return ErrInvalidRequest
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.warehouse_dag_change_operations(
					tenant_id,change_set_id,operation_index,operation,path,value_json
				) VALUES(
					platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5
				)`, id, index, strings.ToUpper(operation.Operation),
				operation.Path, value,
			); err != nil {
				return mapPostgresError(err)
			}
		}
		var dagRunID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.warehouse_dag_runs(
				tenant_id,change_set_id,trigger_type,root_dataset_id,
				status,current_stage,created_by
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2,NULLIF($3,'')::uuid,
				'PENDING','VALIDATE',$4::uuid
			) RETURNING id::text`,
			id, input.TriggerType, input.TargetDatasetID, actorID,
		).Scan(&dagRunID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.warehouse_dag_stage_runs(
				tenant_id,dag_run_id,stage,subject_ref,input_hash,output_hash,
				output_json,status,completed_at
			) VALUES(
				platform.current_tenant_id(),$1::uuid,'CONTEXT',$2,$3,$3,
				jsonb_build_object('operationCount',$4),'SUCCEEDED',now()
			)`, dagRunID, input.TargetDatasetID, requestHash, len(input.Operations))
		return err
	})
	if err != nil {
		return ChangeSet{}, err
	}
	return store.GetChangeSet(ctx, tenantID, id)
}

const changeSetSelect = `SELECT change_set.id::text,
	COALESCE(change_set.target_dataset_id::text,''),change_set.trigger_type,
	change_set.change_kind,change_set.target_layer,change_set.title,
	change_set.question_hash,change_set.baseline_dataset_version,
	change_set.baseline_dsl_hash,change_set.request_key,change_set.status,
	change_set.error_code,change_set.record_version,
	change_set.created_at,change_set.updated_at
	FROM platform.warehouse_dag_change_sets AS change_set
	WHERE change_set.id=$1::uuid`

func (store *PostgresStore) GetChangeSet(
	ctx context.Context,
	tenantID, id string,
) (item ChangeSet, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return loadChangeSet(ctx, tx, id, &item, false)
	})
	return item, err
}

func loadChangeSet(
	ctx context.Context,
	tx pgx.Tx,
	id string,
	item *ChangeSet,
	forUpdate bool,
) error {
	query := changeSetSelect
	if forUpdate {
		query += " FOR UPDATE"
	}
	var createdAt, updatedAt time.Time
	err := tx.QueryRow(ctx, query, id).Scan(
		&item.ID, &item.TargetDatasetID, &item.TriggerType, &item.ChangeKind,
		&item.TargetLayer, &item.Title, &item.QuestionHash,
		&item.BaselineDatasetVersion, &item.BaselineDSLHash,
		&item.RequestKey, &item.Status, &item.ErrorCode, &item.RecordVersion,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	item.Operations = []ChangeOperation{}
	rows, err := tx.Query(ctx, `SELECT operation,path,value_json
		FROM platform.warehouse_dag_change_operations
		WHERE change_set_id=$1::uuid ORDER BY operation_index`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var operation ChangeOperation
		var value []byte
		if err := rows.Scan(&operation.Operation, &operation.Path, &value); err != nil {
			rows.Close()
			return err
		}
		if operation.Operation != "REMOVE" {
			operation.Value = append(json.RawMessage(nil), value...)
		}
		item.Operations = append(item.Operations, operation)
	}
	rows.Close()
	item.Validations = []ChangeValidation{}
	validationRows, err := tx.Query(ctx, `SELECT severity,code,path,message
		FROM platform.warehouse_dag_change_validations
		WHERE change_set_id=$1::uuid ORDER BY validation_index`, id)
	if err != nil {
		return err
	}
	defer validationRows.Close()
	for validationRows.Next() {
		var validation ChangeValidation
		if err := validationRows.Scan(
			&validation.Severity, &validation.Code,
			&validation.Path, &validation.Message,
		); err != nil {
			return err
		}
		item.Validations = append(item.Validations, validation)
	}
	return validationRows.Err()
}

func (store *PostgresStore) FinishChangeValidation(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	validations []ChangeValidation,
	valid bool,
) (ChangeSet, error) {
	var item ChangeSet
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := loadChangeSet(ctx, tx, id, &item, true); err != nil {
			return err
		}
		if item.RecordVersion != expectedVersion ||
			!oneOf(item.Status, "DRAFT", "FAILED", "CONFLICT") {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.warehouse_dag_change_validations
			WHERE change_set_id=$1::uuid`, id); err != nil {
			return err
		}
		for index, validation := range validations {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.warehouse_dag_change_validations(
					tenant_id,change_set_id,validation_index,severity,code,path,message
				) VALUES(platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6)`,
				id, index, validation.Severity, validation.Code,
				validation.Path, validation.Message,
			); err != nil {
				return err
			}
		}
		status, errorCode := "FAILED", "DSL_VALIDATION_FAILED"
		if valid {
			status, errorCode = "VALIDATED", ""
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.warehouse_dag_change_sets
			SET status=$1,error_code=$2,validated_by=$3::uuid,
				record_version=record_version+1,updated_at=now()
			WHERE id=$4::uuid AND record_version=$5`,
			status, errorCode, actorID, id, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		stageStatus := "FAILED"
		if valid {
			stageStatus = "SUCCEEDED"
		}
		stageInputHash := item.BaselineDSLHash
		if stageInputHash == "" {
			stageInputHash = hashText(item.ID)
		}
		_, err = tx.Exec(ctx, `WITH run AS (
				SELECT id FROM platform.warehouse_dag_runs
				WHERE change_set_id=$1::uuid ORDER BY created_at DESC LIMIT 1
			)
			INSERT INTO platform.warehouse_dag_stage_runs(
				tenant_id,dag_run_id,stage,subject_ref,input_hash,output_hash,
				output_json,status,error_code,completed_at
			)
			SELECT platform.current_tenant_id(),run.id,'VALIDATE',$1,$2,
				CASE WHEN $3 THEN $2 ELSE '' END,
				jsonb_build_object('validationCount',$4),$5,
				CASE WHEN $3 THEN '' ELSE 'DSL_VALIDATION_FAILED' END,now()
			FROM run
			ON CONFLICT(tenant_id,dag_run_id,stage,subject_ref,input_hash)
			DO UPDATE SET output_hash=EXCLUDED.output_hash,
				output_json=EXCLUDED.output_json,status=EXCLUDED.status,
				error_code=EXCLUDED.error_code,completed_at=EXCLUDED.completed_at,
				updated_at=now()`,
			id, stageInputHash, valid, len(validations), stageStatus)
		return err
	})
	if err != nil {
		return ChangeSet{}, err
	}
	return store.GetChangeSet(ctx, tenantID, id)
}

func (store *PostgresStore) FinishChangeApply(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	datasetID, dslHash string,
) (ChangeSet, error) {
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.warehouse_dag_change_sets
			SET target_dataset_id=$1::uuid,status='APPLIED',error_code='',
				applied_by=$2::uuid,record_version=record_version+1,
				completed_at=now(),updated_at=now()
			WHERE id=$3::uuid AND status='VALIDATED' AND record_version=$4`,
			datasetID, actorID, id, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `WITH run AS (
				UPDATE platform.warehouse_dag_runs
				SET root_dataset_id=$1::uuid,status='PENDING',current_stage='BUILD',
					error_code='',updated_at=now()
				WHERE change_set_id=$2::uuid
				RETURNING id
			)
			INSERT INTO platform.warehouse_dag_stage_runs(
				tenant_id,dag_run_id,stage,subject_ref,input_hash,output_hash,
				output_json,status,completed_at
			)
			SELECT platform.current_tenant_id(),run.id,'APPLY',$1,$3,$3,
				jsonb_build_object('datasetId',$1),'SUCCEEDED',now()
			FROM run
			ON CONFLICT(tenant_id,dag_run_id,stage,subject_ref,input_hash)
			DO NOTHING`, datasetID, id, dslHash)
		return err
	})
	if err != nil {
		return ChangeSet{}, err
	}
	return store.GetChangeSet(ctx, tenantID, id)
}

func (store *PostgresStore) MarkChangeConflict(
	ctx context.Context,
	tenantID, id string,
	expectedVersion int64,
	code string,
) error {
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.warehouse_dag_change_sets
			SET status='CONFLICT',error_code=$1,record_version=record_version+1,
				updated_at=now()
			WHERE id=$2::uuid AND record_version=$3`,
			code, id, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (store *PostgresStore) RejectChangeSet(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	reasonCode string,
) (ChangeSet, error) {
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.warehouse_dag_change_sets
			SET status='REJECTED',error_code=$1,
				record_version=record_version+1,completed_at=now(),updated_at=now()
			WHERE id=$2::uuid AND record_version=$3
			  AND status IN ('DRAFT','VALIDATED','FAILED','CONFLICT')`,
			reasonCode, id, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE platform.warehouse_dag_runs
			SET status='CANCELED',error_code=$1,completed_at=now(),updated_at=now(),
				lease_owner='',lease_token=NULL,lease_expires_at=NULL
			WHERE change_set_id=$2::uuid
			  AND status IN ('PENDING','RUNNING')`,
			reasonCode, id)
		return err
	})
	if err != nil {
		return ChangeSet{}, err
	}
	return store.GetChangeSet(ctx, tenantID, id)
}

func (store *PostgresStore) CreateConsumerContract(
	ctx context.Context,
	tenantID, actorID string,
	input CreateConsumerContractInput,
) (ConsumerContract, error) {
	var id string
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_consumer_contracts(
				tenant_id,code,name,purpose,output_grain_json,service_level_json,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6::uuid,$6::uuid
			) RETURNING id::text`,
			input.Code, input.Name, input.Purpose,
			input.OutputGrain, input.ServiceLevel, actorID,
		).Scan(&id); err != nil {
			return mapPostgresError(err)
		}
		for _, source := range input.Inputs {
			tag, err := tx.Exec(ctx, `INSERT INTO platform.semantic_consumer_contract_inputs(
					tenant_id,contract_id,dataset_id,dataset_version_id,required
				)
				SELECT platform.current_tenant_id(),$1::uuid,$2::uuid,$3::uuid,$4
				FROM platform.dataset_versions AS version
				JOIN platform.datasets AS dataset
				  ON dataset.tenant_id=version.tenant_id
				 AND dataset.id=version.dataset_id
				WHERE version.id=$3::uuid AND version.dataset_id=$2::uuid
				  AND version.layer='DWS' AND version.status='PUBLISHED'
				  AND dataset.current_published_version_id=version.id
				  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL`,
				id, source.DatasetID, source.DatasetVersionID, source.Required)
			if err != nil {
				return mapPostgresError(err)
			}
			if tag.RowsAffected() != 1 {
				return ErrInvalidRequest
			}
		}
		return nil
	})
	if err != nil {
		return ConsumerContract{}, err
	}
	return store.GetConsumerContract(ctx, tenantID, id)
}

func (store *PostgresStore) GetConsumerContract(
	ctx context.Context,
	tenantID, id string,
) (item ConsumerContract, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var createdAt, updatedAt time.Time
		var outputGrain, serviceLevel []byte
		err := tx.QueryRow(ctx, `SELECT id::text,code::text,name,purpose,
				output_grain_json,service_level_json,status,version,created_at,updated_at
			FROM platform.semantic_consumer_contracts WHERE id=$1::uuid`, id).
			Scan(&item.ID, &item.Code, &item.Name, &item.Purpose,
				&outputGrain, &serviceLevel, &item.Status, &item.Version,
				&createdAt, &updatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		item.OutputGrain = append(json.RawMessage(nil), outputGrain...)
		item.ServiceLevel = append(json.RawMessage(nil), serviceLevel...)
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		item.Inputs = []ConsumerContractInput{}
		rows, err := tx.Query(ctx, `SELECT dataset_id::text,dataset_version_id::text,required
			FROM platform.semantic_consumer_contract_inputs
			WHERE contract_id=$1::uuid ORDER BY dataset_version_id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var source ConsumerContractInput
			if err := rows.Scan(
				&source.DatasetID, &source.DatasetVersionID, &source.Required,
			); err != nil {
				return err
			}
			item.Inputs = append(item.Inputs, source)
		}
		return rows.Err()
	})
	return item, err
}

func (store *PostgresStore) PublishConsumerContract(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
) (ConsumerContract, error) {
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var invalid int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.semantic_consumer_contract_inputs AS input
			LEFT JOIN platform.dataset_versions AS version
			  ON version.tenant_id=input.tenant_id
			 AND version.id=input.dataset_version_id
			 AND version.dataset_id=input.dataset_id
			 AND version.layer='DWS' AND version.status='PUBLISHED'
			LEFT JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			 AND dataset.current_published_version_id=version.id
			 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			WHERE input.contract_id=$1::uuid
			  AND (version.id IS NULL OR dataset.id IS NULL)`, id).Scan(&invalid); err != nil {
			return err
		}
		if invalid > 0 {
			return ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_consumer_contracts
			SET status='PUBLISHED',version=version+1,updated_by=$1::uuid,
				published_by=$1::uuid,published_at=now(),updated_at=now()
			WHERE id=$2::uuid AND status='DRAFT' AND version=$3
			  AND EXISTS(
			    SELECT 1 FROM platform.semantic_consumer_contract_inputs
			    WHERE contract_id=$2::uuid
			  )`, actorID, id, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return ConsumerContract{}, err
	}
	return store.GetConsumerContract(ctx, tenantID, id)
}

func (store *PostgresStore) GetWarehouseDAG(
	ctx context.Context,
	tenantID, rootVersionID string,
) (result WarehouseBuildDAG, err error) {
	result.RootDatasetVersionID = rootVersionID
	result.Nodes = []WarehouseDAGNode{}
	result.Edges = []WarehouseDAGEdge{}
	result.TopologicalOrder = []string{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH RECURSIVE lineage AS (
				SELECT version.id,version.dataset_id,version.status,version.layer,
					dataset.code::text,dataset.name,0 AS depth,ARRAY[version.id] AS path
				FROM platform.dataset_versions AS version
				JOIN platform.datasets AS dataset
				  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
				WHERE version.id=$1::uuid
				UNION ALL
				SELECT upstream.id,upstream.dataset_id,upstream.status,upstream.layer,
					dataset.code::text,dataset.name,lineage.depth+1,
					lineage.path||upstream.id
				FROM lineage
				JOIN platform.dataset_dependencies AS dependency
				  ON dependency.dataset_version_id=lineage.id
				 AND dependency.source_type='DATASET_VERSION'
				JOIN platform.dataset_versions AS upstream
				  ON upstream.tenant_id=dependency.tenant_id
				 AND upstream.id=dependency.source_id::uuid
				JOIN platform.datasets AS dataset
				  ON dataset.tenant_id=upstream.tenant_id
				 AND dataset.id=upstream.dataset_id
				WHERE lineage.depth<16 AND NOT upstream.id=ANY(lineage.path)
			)
			SELECT DISTINCT id::text,dataset_id::text,code,name,layer,status
			FROM lineage ORDER BY layer,id`, rootVersionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item WarehouseDAGNode
			if err := rows.Scan(
				&item.DatasetVersionID, &item.DatasetID, &item.Code,
				&item.Name, &item.Layer, &item.Status,
			); err != nil {
				rows.Close()
				return err
			}
			result.Nodes = append(result.Nodes, item)
		}
		rows.Close()
		if len(result.Nodes) == 0 {
			return ErrNotFound
		}
		edgeRows, err := tx.Query(ctx, `WITH RECURSIVE lineage(id,path,depth) AS (
				SELECT $1::uuid,ARRAY[$1::uuid],0
				UNION ALL
				SELECT dependency.source_id::uuid,
					lineage.path||dependency.source_id::uuid,lineage.depth+1
				FROM lineage
				JOIN platform.dataset_dependencies AS dependency
				  ON dependency.dataset_version_id=lineage.id
				 AND dependency.source_type='DATASET_VERSION'
				WHERE lineage.depth<16
				  AND NOT dependency.source_id::uuid=ANY(lineage.path)
			)
			SELECT DISTINCT dependency.source_id,lineage.id::text,dependency.source_type
			FROM lineage
			JOIN platform.dataset_dependencies AS dependency
			  ON dependency.dataset_version_id=lineage.id
			 AND dependency.source_type='DATASET_VERSION'
			ORDER BY dependency.source_id,lineage.id::text`, rootVersionID)
		if err != nil {
			return err
		}
		for edgeRows.Next() {
			var edge WarehouseDAGEdge
			if err := edgeRows.Scan(
				&edge.FromDatasetVersionID, &edge.ToDatasetVersionID, &edge.SourceType,
			); err != nil {
				edgeRows.Close()
				return err
			}
			result.Edges = append(result.Edges, edge)
		}
		edgeRows.Close()
		order, err := topologicalDatasetOrder(result.Nodes, result.Edges)
		if err != nil {
			return err
		}
		result.TopologicalOrder = order
		return nil
	})
	return result, err
}

func topologicalDatasetOrder(
	nodes []WarehouseDAGNode,
	edges []WarehouseDAGEdge,
) ([]string, error) {
	indegree := map[string]int{}
	adjacent := map[string][]string{}
	for _, node := range nodes {
		indegree[node.DatasetVersionID] = 0
	}
	for _, edge := range edges {
		if _, exists := indegree[edge.FromDatasetVersionID]; !exists {
			continue
		}
		if _, exists := indegree[edge.ToDatasetVersionID]; !exists {
			continue
		}
		adjacent[edge.FromDatasetVersionID] = append(
			adjacent[edge.FromDatasetVersionID], edge.ToDatasetVersionID,
		)
		indegree[edge.ToDatasetVersionID]++
	}
	queue := []string{}
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	order := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		order = append(order, current)
		sort.Strings(adjacent[current])
		for _, downstream := range adjacent[current] {
			indegree[downstream]--
			if indegree[downstream] == 0 {
				queue = append(queue, downstream)
				sort.Strings(queue)
			}
		}
	}
	if len(order) != len(indegree) {
		return nil, fmt.Errorf("%w: dataset lineage contains a cycle", ErrUnprovenPath)
	}
	return order, nil
}

func mapPostgresError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}
	switch postgresError.Code {
	case "23503", "23514", "22P02":
		return ErrInvalidRequest
	case "23505":
		return ErrConflict
	default:
		return err
	}
}
