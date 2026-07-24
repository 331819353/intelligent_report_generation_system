package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errMappedDatasetUnsupportedColumn = errors.New("mapped dataset contains an unsupported physical column name")

// MappedDatasetTable 是构建 LLM 映射表默认数据集所需的最小表快照。
// 它不是第二份元数据模型，仅在同一事务内把已完善资产转为 DSL。
type MappedDatasetTable struct {
	ID                  string
	DataSourceID        string
	DataSourceName      string
	FileVersionID       string
	MetadataVersion     int64
	StructureHash       string
	TableName           string
	BusinessName        string
	BusinessDescription string
}

// MappedDatasetColumn 保留真实物理字段名作为 projection/FIELD_REF，
// 对外字段 code 和 id 则由 builder 生成稳定标识符。
type MappedDatasetColumn struct {
	ColumnName          string
	BusinessName        string
	BusinessDescription string
	CanonicalType       string
	SemanticType        string
	Nullable            bool
	PrimaryKey          bool
}

// BuildMappedDatasetDocument 生成“数据节点 -> 结束节点”的默认单表 DSL。
// 输出标识符按列顺序稳定消歧；真实列名始终保留在物理引用中。
func BuildMappedDatasetDocument(table MappedDatasetTable, columns []MappedDatasetColumn) (Document, error) {
	tableUUID, err := uuid.Parse(strings.TrimSpace(table.ID))
	if err != nil {
		return Document{}, fmt.Errorf("mapped dataset table ID is invalid: %w", err)
	}
	if strings.TrimSpace(table.DataSourceID) == "" {
		return Document{}, errors.New("mapped dataset data source ID is required")
	}
	if len(columns) == 0 {
		return Document{}, errors.New("mapped dataset requires at least one active column")
	}

	name := mappedDatasetDisplayName(table)
	if name == "" {
		return Document{}, errors.New("mapped dataset table name is required")
	}
	code := "mapped_" + strings.ReplaceAll(tableUUID.String(), "-", "")
	projection := make([]string, 0, len(columns))
	fields := make([]Field, 0, len(columns))
	endOutputs := make([]map[string]any, 0, len(columns))
	usedCodes, usedIDs := map[string]bool{}, map[string]bool{}
	grainCodes := []string{}
	defaultTimeField := ""

	for index, column := range columns {
		physicalName := strings.TrimSpace(column.ColumnName)
		// 查询编译器只允许经物理白名单校验的数据库标识符。不丢列、
		// 不改写物理名；整表不可安全执行时由 Ensure 路径跳过创建。
		if !physicalIdentifierPattern.MatchString(physicalName) {
			return Document{}, fmt.Errorf("%w: column %d", errMappedDatasetUnsupportedColumn, index+1)
		}
		projection = append(projection, physicalName)
		logicalName := physicalName
		if table.FileVersionID != "" && identifierPattern.MatchString(strings.TrimSpace(column.BusinessName)) {
			logicalName = strings.TrimSpace(column.BusinessName)
		}
		fieldCode := uniqueMappedIdentifier(logicalName, fmt.Sprintf("field_%d", index+1), usedCodes)
		fieldID := uniqueMappedIdentifier("field_t1_"+fieldCode, fmt.Sprintf("field_t1_%d", index+1), usedIDs)
		fieldName := strings.TrimSpace(column.BusinessName)
		if table.FileVersionID != "" && containsHan(physicalName) {
			fieldName = physicalName
		} else if fieldName == "" {
			fieldName = physicalName
		}
		role := mappedDatasetFieldRole(column)
		visible := true
		fields = append(fields, Field{
			ID:            fieldID,
			Code:          fieldCode,
			Name:          fieldName,
			Description:   strings.TrimSpace(column.BusinessDescription),
			Role:          role,
			Expression:    Expression{Type: "FIELD_REF", NodeID: "node_1", Field: physicalName},
			CanonicalType: mappedDatasetCanonicalType(column.CanonicalType),
			SemanticType:  strings.ToUpper(strings.TrimSpace(column.SemanticType)),
			Nullable:      column.Nullable,
			Visible:       &visible,
		})
		endOutputs = append(endOutputs, map[string]any{
			"key":  "node_1." + physicalName,
			"name": fieldName,
			"code": fieldCode,
		})
		if column.PrimaryKey {
			grainCodes = append(grainCodes, fieldCode)
		}
		if role == "TIME" && defaultTimeField == "" {
			defaultTimeField = fieldCode
		}
	}
	if len(grainCodes) == 0 {
		grainCodes = []string{fields[0].Code}
	}

	return Document{
		DSLVersion: DSLVersion,
		Dataset: Descriptor{
			Code:        code,
			Name:        name,
			Description: strings.TrimSpace(table.BusinessDescription),
			Type:        "SINGLE_SOURCE",
			Layer:       LayerODS,
		},
		Nodes: []Node{{
			ID:            "node_1",
			Type:          "TABLE",
			DataSourceID:  strings.TrimSpace(table.DataSourceID),
			TableID:       tableUUID.String(),
			FileVersionID: strings.TrimSpace(table.FileVersionID),
			Alias:         "t1",
			Projection:    projection,
			SourceFilters: []SourceFilter{},
		}},
		Joins:           []Join{},
		PreAggregations: []PreAggregation{},
		Fields:          fields,
		Filters:         []Filter{},
		GroupBy:         []string{},
		Having:          []Filter{},
		Sorts:           []Sort{},
		Parameters:      []Parameter{},
		OutputGrain: OutputGrain{
			Description: "每行代表" + name + "中的一条记录",
			KeyFields:   grainCodes,
			TimeField:   defaultTimeField,
			DefaultTimeGrain: func() string {
				if defaultTimeField != "" {
					return "DAY"
				}
				return ""
			}(),
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode:            "MATERIALIZED_PREFERRED",
			TimeoutMS:       5000,
			PreviewLimit:    200,
			ResultLimit:     10000,
			CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{
				Enabled: true, RefreshMode: "ON_DEMAND",
			},
		},
		Designer: map[string]any{
			"version":       "1.0",
			"nodePositions": map[string]any{"node_1": map[string]any{"x": 42, "y": 58}},
			"nodeNames":     map[string]any{"node_1": name},
			"joins":         []map[string]any{},
			"groups":        []map[string]any{},
			"end": map[string]any{
				"id":       "end_1",
				"name":     "最终输出",
				"input":    map[string]any{"kind": "NODE", "id": "node_1"},
				"position": map[string]any{"x": 382, "y": 58},
				"outputs":  endOutputs,
			},
		},
	}, nil
}

const mappedDatasetDefaultPublishKey = "system-mapped-default-v1"

type mappedDatasetState struct {
	ID                   string
	Deleted              bool
	Status               string
	Version              int64
	DraftVersionID       string
	DraftVersionNo       int
	DraftRecordVersion   int64
	DSLHash              string
	PlanHash             string
	PublishedCount       int
	RevisionCount        int
	ExactCreateCount     int
	PendingApprovalCount int
	MappedAfterDeletion  bool
}

type mappedDatasetPublicationFence struct {
	VersionID                string
	SourceDraftVersionID     string
	SourceDraftRecordVersion int64
	SchemaHash               string
	PlanHash                 string
}

// EnsureMappedDatasetTx 在已有租户事务内幂等创建并默认发布映射表数据集。
// 公开签名供元数据完善事务复用；启动对账通过内部返回值区分真实变更和安全跳过。
func (s *PostgresStore) EnsureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error {
	_, err := s.ensureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID, true)
	return err
}

func (s *PostgresStore) ensureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string, regenerateDeleted bool) (bool, error) {
	if tx == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(tableID) == "" {
		return false, errors.New("mapped dataset transaction, tenant ID, and table ID are required")
	}
	if _, err := uuid.Parse(strings.TrimSpace(actorID)); err != nil {
		return false, errors.New("mapped dataset default publication requires a valid actor ID")
	}
	lockKey := "mapped_dataset:" + strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(tableID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, lockKey); err != nil {
		return false, err
	}
	state, exists, err := loadMappedDatasetStateTx(ctx, tx, tenantID, tableID)
	if err != nil {
		return false, err
	}
	// 显式停用对象始终尊重人工生命周期操作。软删除对象只有在删除后再次完成
	// 映射时才复用原主对象生成新发布版本，避免来源表唯一约束让后续映射静默无结果。
	if exists && state.Status == "DISABLED" {
		return false, nil
	}
	if exists && state.Deleted && !regenerateDeleted && !state.MappedAfterDeletion {
		return false, nil
	}

	table := MappedDatasetTable{}
	err = tx.QueryRow(ctx, `SELECT t.id::text,t.data_source_id::text,source.name,
		COALESCE((SELECT fv.id::text
			FROM platform.file_assets fa
			JOIN platform.file_asset_versions fv
			  ON fv.file_asset_id=fa.id AND fv.tenant_id=fa.tenant_id AND fv.version=fa.current_version
			WHERE fa.id=source.file_asset_id),''),
		t.table_name,t.metadata_version,t.structure_hash,t.business_name,t.business_description
		FROM platform.metadata_tables t
		JOIN platform.data_sources source ON source.id=t.data_source_id AND source.tenant_id=t.tenant_id
		WHERE t.id::text=$1 AND t.tenant_id::text=$2
		  AND source.status='ACTIVE' AND source.deleted_at IS NULL
		  AND t.asset_status='ACTIVE' AND t.management_status='ENABLED'
		  AND t.last_enriched_structure_hash<>''
		  AND t.last_enriched_structure_hash=t.structure_hash
		  AND t.last_enriched_table_structure_hash=t.table_structure_hash
		  AND EXISTS(SELECT 1 FROM platform.metadata_columns c
			WHERE c.table_id=t.id AND c.tenant_id=t.tenant_id AND c.asset_status='ACTIVE')
		  AND NOT EXISTS(SELECT 1 FROM platform.metadata_columns c
			WHERE c.table_id=t.id AND c.tenant_id=t.tenant_id AND c.asset_status='ACTIVE'
			  AND (c.last_enriched_structure_hash='' OR c.last_enriched_structure_hash<>c.structure_hash))
		FOR SHARE OF t`, tableID, tenantID).Scan(
		&table.ID, &table.DataSourceID, &table.DataSourceName, &table.FileVersionID, &table.TableName,
		&table.MetadataVersion, &table.StructureHash,
		&table.BusinessName, &table.BusinessDescription,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	rows, err := tx.Query(ctx, `SELECT column_name,business_name,business_description,
		canonical_type,semantic_type,nullable,is_primary_key
		FROM platform.metadata_columns
		WHERE table_id=$1 AND tenant_id=$2 AND asset_status='ACTIVE'
		ORDER BY ordinal_position,id
		FOR SHARE`, table.ID, tenantID)
	if err != nil {
		return false, err
	}
	columns := []MappedDatasetColumn{}
	for rows.Next() {
		var column MappedDatasetColumn
		if err := rows.Scan(&column.ColumnName, &column.BusinessName, &column.BusinessDescription,
			&column.CanonicalType, &column.SemanticType, &column.Nullable, &column.PrimaryKey); err != nil {
			rows.Close()
			return false, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if len(columns) == 0 {
		return false, nil
	}

	document, err := BuildMappedDatasetDocument(table, columns)
	if errors.Is(err, errMappedDatasetUnsupportedColumn) {
		// 元数据补全不应因当前查询编译器不支持的物理名永久失败。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return false, err
	}
	prepared, err := Prepare(raw)
	if err != nil {
		return false, err
	}
	if exists && state.Deleted {
		return s.regenerateDeletedMappedDatasetTx(ctx, tx, tenantID, actorID, table, state, prepared)
	}
	if exists && state.PublishedCount > 0 {
		return s.refreshMappedDatasetTx(ctx, tx, tenantID, actorID, table, state, prepared)
	}
	input := CreateInput{
		Code: document.Dataset.Code, Name: document.Dataset.Name,
		Description: document.Dataset.Description, Type: document.Dataset.Type, DSL: raw,
	}
	if !exists {
		datasetID, createErr := createDatasetTx(ctx, tx, tenantID, actorID, input, prepared, table.ID)
		if createErr != nil {
			return false, createErr
		}
		state, exists, err = loadMappedDatasetStateTx(ctx, tx, tenantID, table.ID)
		if err != nil {
			return false, err
		}
		if !exists || state.ID != datasetID {
			return false, errors.New("mapped dataset was not readable after creation")
		}
	}
	if !state.canDefaultPublish(prepared) {
		return false, nil
	}
	return s.publishMappedDatasetDefaultTx(ctx, tx, tenantID, actorID, table.ID, state, prepared)
}

// regenerateDeletedMappedDatasetTx 在来源表重新完成映射时恢复同一个数据集主对象，
// 保留已废弃的历史发布快照，并从现有可变草稿生成一个新的不可变发布版本。
func (s *PostgresStore) regenerateDeletedMappedDatasetTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	table MappedDatasetTable,
	state mappedDatasetState,
	prepared Prepared,
) (bool, error) {
	if !state.Deleted || state.Status == "DISABLED" || state.DraftVersionID == "" || state.DraftRecordVersion < 1 {
		return false, nil
	}
	publication, exists, err := loadMappedDatasetPublicationFenceTx(
		ctx, tx, tenantID, state.ID, true,
	)
	if err != nil {
		return false, err
	}
	if !exists || !state.canSystemAdvance(publication) {
		// 删除不会授权系统丢弃用户已经保存或提交审批的草稿。只有当前草稿仍是
		// 最近一次不可变发布所使用的精确修订时，重新映射才可恢复并发布。
		return false, nil
	}
	var draftRecordVersion int64
	if err := tx.QueryRow(ctx, `UPDATE platform.dataset_versions SET
			layer=$1,dsl_json=$2,schema_hash=$3,logical_plan_json=$4,plan_hash=$5,
			record_version=record_version+1,updated_by=$6
			WHERE id=$7 AND dataset_id=$8 AND tenant_id=$9 AND status='DRAFT'
			RETURNING record_version`, prepared.Document.Dataset.Layer, prepared.DSLJSON, prepared.DSLHash,
		prepared.LogicalPlanJSON, prepared.PlanHash, actorID, state.DraftVersionID, state.ID,
		tenantID).Scan(&draftRecordVersion); err != nil {
		return false, err
	}
	var datasetVersion int64
	if err := tx.QueryRow(ctx, `UPDATE platform.datasets SET
			name=$1,description=$2,dataset_type=$3,layer=$4,status='DRAFT',current_published_version_id=NULL,
			disabled_from_status=NULL,disabled_published_version_id=NULL,deleted_at=NULL,
			version=version+1,updated_by=$5,updated_at=now()
			WHERE id=$6 AND tenant_id=$7 AND origin_table_id=$8 AND deleted_at IS NOT NULL
			RETURNING version`, prepared.Document.Dataset.Name, prepared.Document.Dataset.Description,
		prepared.Document.Dataset.Type, prepared.Document.Dataset.Layer, actorID, state.ID, tenantID,
		table.ID).Scan(&datasetVersion); err != nil {
		return false, err
	}
	if err := replaceDerived(ctx, tx, tenantID, state.ID, state.DraftVersionID, prepared.Document, true); err != nil {
		return false, err
	}
	if err := insertDraftRevisionTx(ctx, tx, tenantID, state.ID, actorID, state.DraftVersionID,
		datasetVersion, draftRecordVersion, "SAVE", "", prepared); err != nil {
		return false, err
	}
	input := PublishInput{
		DraftVersionID: state.DraftVersionID, ExpectedVersion: datasetVersion,
		ExpectedDraftRecordVersion: draftRecordVersion, ExpectedDSLHash: prepared.DSLHash,
		ValidationParameters: map[string]any{},
	}
	requestHash, err := publicationRequestHash(state.ID, input)
	if err != nil {
		return false, err
	}
	dslPrefix := prepared.DSLHash
	if len(dslPrefix) > 16 {
		dslPrefix = dslPrefix[:16]
	}
	plan := PublishPlan{
		IdempotencyKey: fmt.Sprintf("system-mapped-regenerate-%d-%d-%s", state.Version, draftRecordVersion, dslPrefix),
		RequestHash:    requestHash, ExpectedVersion: datasetVersion, DraftVersionID: state.DraftVersionID,
		ExpectedDraftRecordVersion: draftRecordVersion, ExpectedDSLHash: prepared.DSLHash, Prepared: prepared,
	}
	var published VersionRecord
	if err := s.publishTx(
		ctx, tx, tenantID, actorID, state.ID,
		PublicationOriginSystemMappedRegenerate, plan, &published,
	); err != nil {
		return false, err
	}
	if err := s.enqueueMappedMaterializationTx(ctx, tx, tenantID, published); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,$2,'AUTO_REGENERATE_MAPPED_DATASET','DATASET',$3,
		jsonb_build_object('publicationSource','SYSTEM_MAPPED_REGENERATE','originTableId',$4::text,
			'metadataVersion',$5::bigint,'structureHash',$6::text,'publishedVersionId',$7::text,
			'versionNo',$8::int,'dslHash',$9::text,'planHash',$10::text))`,
		tenantID, actorID, state.ID, table.ID, table.MetadataVersion, table.StructureHash,
		published.ID, published.VersionNo, published.DSLHash, published.PlanHash); err != nil {
		return false, err
	}
	return true, nil
}

// refreshMappedDatasetTx advances only a system-owned mapped dataset. It writes a new draft
// revision and a new immutable publication whenever the authoritative table snapshot changed.
func (s *PostgresStore) refreshMappedDatasetTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	table MappedDatasetTable,
	state mappedDatasetState,
	prepared Prepared,
) (bool, error) {
	if state.Status != "PUBLISHED" || state.DraftVersionID == "" || table.MetadataVersion < 1 || table.StructureHash == "" {
		return false, nil
	}
	publication, exists, err := loadMappedDatasetPublicationFenceTx(
		ctx, tx, tenantID, state.ID, false,
	)
	if err != nil {
		return false, err
	}
	if !exists || !state.canSystemAdvance(publication) {
		// 自动刷新不是第二条人工发布旁路。草稿只要偏离当前发布来源修订，或已有
		// 待审批申请，就保持草稿、发布指针、build 和审计事实完全不变。
		return false, nil
	}
	if publication.SchemaHash == prepared.DSLHash {
		if err := validateDependencySnapshotsTx(ctx, tx, publication.VersionID); err == nil {
			var published VersionRecord
			if err := scanVersionTx(ctx, tx, state.ID, publication.VersionID, &published); err != nil {
				return false, err
			}
			// 启动对账也经过这里：若发布已提交而旧版本尚未登记 build，
			// sink 会按冻结输入生成确定幂等键；已有任务则保持原 requested_by。
			if err := s.enqueueMappedMaterializationTx(
				ctx, tx, tenantID, published,
			); err != nil {
				return false, err
			}
			return false, nil
		}
	}

	var draftRecordVersion int64
	if err := tx.QueryRow(ctx, `UPDATE platform.dataset_versions SET
			layer=$1,dsl_json=$2,schema_hash=$3,logical_plan_json=$4,plan_hash=$5,
			record_version=record_version+1,updated_by=$6
			WHERE id=$7 AND dataset_id=$8 AND tenant_id=$9 AND status='DRAFT'
			RETURNING record_version`, prepared.Document.Dataset.Layer, prepared.DSLJSON, prepared.DSLHash,
		prepared.LogicalPlanJSON, prepared.PlanHash, actorID, state.DraftVersionID, state.ID,
		tenantID).Scan(&draftRecordVersion); err != nil {
		return false, err
	}
	var datasetVersion int64
	if err := tx.QueryRow(ctx, `UPDATE platform.datasets SET
			name=$1,description=$2,dataset_type=$3,layer=$4,version=version+1,updated_by=$5,updated_at=now()
			WHERE id=$6 AND tenant_id=$7 AND origin_table_id=$8 AND status='PUBLISHED'
			RETURNING version`, prepared.Document.Dataset.Name, prepared.Document.Dataset.Description,
		prepared.Document.Dataset.Type, prepared.Document.Dataset.Layer, actorID, state.ID, tenantID,
		table.ID).Scan(&datasetVersion); err != nil {
		return false, err
	}
	if err := replaceDerived(ctx, tx, tenantID, state.ID, state.DraftVersionID, prepared.Document, true); err != nil {
		return false, err
	}
	if err := insertDraftRevisionTx(ctx, tx, tenantID, state.ID, actorID, state.DraftVersionID,
		datasetVersion, draftRecordVersion, "SAVE", "", prepared); err != nil {
		return false, err
	}
	input := PublishInput{
		DraftVersionID: state.DraftVersionID, ExpectedVersion: datasetVersion,
		ExpectedDraftRecordVersion: draftRecordVersion, ExpectedDSLHash: prepared.DSLHash,
		ValidationParameters: map[string]any{},
	}
	requestHash, err := publicationRequestHash(state.ID, input)
	if err != nil {
		return false, err
	}
	structurePrefix := table.StructureHash
	if len(structurePrefix) > 16 {
		structurePrefix = structurePrefix[:16]
	}
	dslPrefix := prepared.DSLHash
	if len(dslPrefix) > 16 {
		dslPrefix = dslPrefix[:16]
	}
	plan := PublishPlan{
		IdempotencyKey: fmt.Sprintf("system-mapped-refresh-%d-%s-%s", table.MetadataVersion, structurePrefix, dslPrefix),
		RequestHash:    requestHash, ExpectedVersion: datasetVersion, DraftVersionID: state.DraftVersionID,
		ExpectedDraftRecordVersion: draftRecordVersion, ExpectedDSLHash: prepared.DSLHash, Prepared: prepared,
	}
	var published VersionRecord
	if err := s.publishTx(
		ctx, tx, tenantID, actorID, state.ID,
		PublicationOriginSystemMappedRefresh, plan, &published,
	); err != nil {
		return false, err
	}
	if err := s.enqueueMappedMaterializationTx(ctx, tx, tenantID, published); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,$2,'AUTO_REFRESH_MAPPED_DATASET','DATASET',$3,
		jsonb_build_object('publicationSource','SYSTEM_MAPPED_REFRESH','originTableId',$4::text,
			'metadataVersion',$5::bigint,'structureHash',$6::text,'previousPublishedVersionId',$7::text,
			'publishedVersionId',$8::text,'versionNo',$9::int,'dslHash',$10::text,'planHash',$11::text))`,
		tenantID, actorID, state.ID, table.ID, table.MetadataVersion, table.StructureHash,
		publication.VersionID, published.ID, published.VersionNo, published.DSLHash, published.PlanHash); err != nil {
		return false, err
	}
	return true, nil
}

func loadMappedDatasetStateTx(ctx context.Context, tx pgx.Tx, tenantID, tableID string) (state mappedDatasetState, exists bool, err error) {
	err = tx.QueryRow(ctx, `SELECT dataset.id::text,dataset.deleted_at IS NOT NULL,dataset.status,dataset.version,
		COALESCE(draft.id::text,''),COALESCE(draft.version_no,0),COALESCE(draft.record_version,0),
		COALESCE(draft.schema_hash,''),COALESCE(draft.plan_hash,''),
		(SELECT count(*) FROM platform.dataset_versions AS published
		 WHERE published.dataset_id=dataset.id AND published.tenant_id=dataset.tenant_id
		   AND published.status IN ('PUBLISHED','STALE','DEPRECATED')),
		(SELECT count(*) FROM platform.dataset_draft_revisions AS revision
		 WHERE revision.dataset_id=dataset.id AND revision.tenant_id=dataset.tenant_id),
		(SELECT count(*) FROM platform.dataset_draft_revisions AS revision
		 WHERE revision.dataset_id=dataset.id AND revision.tenant_id=dataset.tenant_id
		   AND revision.operation_type='CREATE' AND revision.draft_version_id=draft.id
		   AND revision.draft_record_version=draft.record_version
		   AND revision.schema_hash=draft.schema_hash AND revision.plan_hash=draft.plan_hash),
		EXISTS(SELECT 1 FROM platform.ai_metadata_jobs AS job
		 WHERE job.tenant_id=dataset.tenant_id AND job.table_id=dataset.origin_table_id
		   AND job.status='SUCCEEDED' AND dataset.deleted_at IS NOT NULL
		   AND job.completed_at>dataset.deleted_at)
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
		WHERE dataset.tenant_id::text=$1 AND dataset.origin_table_id::text=$2
		FOR UPDATE OF dataset,draft`, tenantID, tableID).Scan(
		&state.ID, &state.Deleted, &state.Status, &state.Version,
		&state.DraftVersionID, &state.DraftVersionNo, &state.DraftRecordVersion,
		&state.DSLHash, &state.PlanHash, &state.PublishedCount,
		&state.RevisionCount, &state.ExactCreateCount, &state.MappedAfterDeletion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mappedDatasetState{}, false, nil
	}
	if err != nil {
		return mappedDatasetState{}, false, err
	}
	// dataset/draft 已由上一条语句 FOR UPDATE。审批提交需要 FOR SHARE 同一组行，
	// 因此此处的新 READ COMMITTED 语句既能看见锁等待期间刚提交的申请，也能阻止
	// 新申请在本事务完成资格复核后插入，避免单语句旧快照漏掉 PENDING。
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.dataset_publication_requests
		WHERE dataset_id=$1 AND tenant_id=$2 AND status='PENDING'`,
		state.ID, tenantID).Scan(&state.PendingApprovalCount); err != nil {
		return mappedDatasetState{}, false, err
	}
	return state, true, nil
}

func loadMappedDatasetPublicationFenceTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, datasetID string,
	deleted bool,
) (fence mappedDatasetPublicationFence, exists bool, err error) {
	query := `SELECT version.id::text,version.source_draft_version_id::text,
			version.source_draft_record_version,version.schema_hash,version.plan_hash
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS version
		  ON version.id=dataset.current_published_version_id
		 AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
			WHERE dataset.id=$1 AND dataset.tenant_id=$2 AND dataset.deleted_at IS NULL
			  AND version.status='PUBLISHED'
			  AND version.publication_origin IN (
				'SYSTEM_MAPPED_DEFAULT',
				'SYSTEM_MAPPED_REFRESH',
				'SYSTEM_MAPPED_REGENERATE'
			  )
			FOR SHARE OF version`
	if deleted {
		// 软删除会清空 current_published_version_id 并把历史发布版本标为
		// DEPRECATED；最近版本是唯一可用于证明草稿仍未被人工修改的基线。
		query = `SELECT version.id::text,version.source_draft_version_id::text,
				version.source_draft_record_version,version.schema_hash,version.plan_hash
			FROM platform.dataset_versions AS version
			WHERE version.dataset_id=$1 AND version.tenant_id=$2
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
			  AND version.version_no=(
				SELECT max(latest.version_no)
				FROM platform.dataset_versions AS latest
				WHERE latest.dataset_id=version.dataset_id
				  AND latest.tenant_id=version.tenant_id
					  AND latest.status IN ('PUBLISHED','STALE','DEPRECATED')
				  )
				  AND version.publication_origin IN (
					'SYSTEM_MAPPED_DEFAULT',
					'SYSTEM_MAPPED_REFRESH',
					'SYSTEM_MAPPED_REGENERATE'
				  )
			ORDER BY version.version_no DESC,version.id DESC
			LIMIT 1
			FOR SHARE OF version`
	}
	err = tx.QueryRow(ctx, query, datasetID, tenantID).Scan(
		&fence.VersionID, &fence.SourceDraftVersionID, &fence.SourceDraftRecordVersion,
		&fence.SchemaHash, &fence.PlanHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mappedDatasetPublicationFence{}, false, nil
	}
	return fence, err == nil, err
}

func mappedDatasetDisplayName(table MappedDatasetTable) string {
	businessName := strings.TrimSpace(table.BusinessName)
	tableName := strings.TrimSpace(table.TableName)
	dataSourceName := strings.TrimSpace(table.DataSourceName)
	if table.FileVersionID != "" {
		for _, candidate := range []string{businessName, tableName, dataSourceName} {
			if containsHan(candidate) {
				return candidate
			}
		}
	}
	for _, candidate := range []string{businessName, tableName, dataSourceName} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func containsHan(value string) bool {
	for _, character := range value {
		if character >= '\u3400' && character <= '\u4dbf' || character >= '\u4e00' && character <= '\u9fff' || character >= '\uf900' && character <= '\ufaff' {
			return true
		}
	}
	return false
}

func (state mappedDatasetState) canDefaultPublish(prepared Prepared) bool {
	return !state.Deleted && state.Status == "DRAFT" && state.Version == 1 &&
		state.DraftVersionID != "" && state.DraftVersionNo == 1 && state.DraftRecordVersion == 1 &&
		state.PublishedCount == 0 && state.RevisionCount == 1 && state.ExactCreateCount == 1 &&
		state.PendingApprovalCount == 0 && state.DSLHash == prepared.DSLHash && state.PlanHash == prepared.PlanHash
}

func (state mappedDatasetState) canSystemAdvance(publication mappedDatasetPublicationFence) bool {
	return state.PendingApprovalCount == 0 &&
		state.DraftVersionID != "" &&
		state.DraftVersionID == publication.SourceDraftVersionID &&
		state.DraftRecordVersion > 0 &&
		state.DraftRecordVersion == publication.SourceDraftRecordVersion &&
		state.DSLHash != "" && state.DSLHash == publication.SchemaHash &&
		state.PlanHash != "" && state.PlanHash == publication.PlanHash
}

// publishMappedDatasetDefaultTx 是人工审批发布边界之外唯一允许移动发布指针的系统路径。
// 它只接受 canDefaultPublish 验证过的初始映射草稿，并用额外审计明确记录系统来源。
func (s *PostgresStore) publishMappedDatasetDefaultTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, tableID string,
	state mappedDatasetState,
	prepared Prepared,
) (bool, error) {
	input := PublishInput{
		DraftVersionID: state.DraftVersionID, ExpectedVersion: state.Version,
		ExpectedDraftRecordVersion: state.DraftRecordVersion, ExpectedDSLHash: state.DSLHash,
		ValidationParameters: map[string]any{},
	}
	requestHash, err := publicationRequestHash(state.ID, input)
	if err != nil {
		return false, err
	}
	plan := PublishPlan{
		IdempotencyKey: mappedDatasetDefaultPublishKey, RequestHash: requestHash,
		ExpectedVersion: state.Version, DraftVersionID: state.DraftVersionID,
		ExpectedDraftRecordVersion: state.DraftRecordVersion, ExpectedDSLHash: state.DSLHash,
		Prepared: prepared,
	}
	var published VersionRecord
	if err := s.publishTx(
		ctx, tx, tenantID, actorID, state.ID,
		PublicationOriginSystemMappedDefault, plan, &published,
	); err != nil {
		return false, err
	}
	if err := s.enqueueMappedMaterializationTx(ctx, tx, tenantID, published); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,$2,'AUTO_PUBLISH_MAPPED_DEFAULT','DATASET',$3,
		jsonb_build_object('publicationSource','SYSTEM_MAPPED_DEFAULT','originTableId',$4::text,
			'publishedVersionId',$5::text,'versionNo',$6::int,'dslHash',$7::text,'planHash',$8::text))`,
		tenantID, actorID, state.ID, tableID, published.ID, published.VersionNo,
		published.DSLHash, published.PlanHash); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) enqueueMappedMaterializationTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	published VersionRecord,
) error {
	if s.mappedPublicationSink == nil {
		return errors.New("mapped dataset materialization commit sink is not configured")
	}
	requestedBy := strings.TrimSpace(published.PublishedBy)
	if _, err := uuid.Parse(requestedBy); err != nil {
		return errors.New("mapped dataset published version has no valid publisher")
	}
	return s.mappedPublicationSink.EnqueueMappedDatasetMaterializationTx(
		ctx, tx, tenantID, requestedBy, published,
	)
}

func mappedDatasetFieldRole(column MappedDatasetColumn) string {
	if column.PrimaryKey {
		return "IDENTIFIER"
	}
	switch strings.ToUpper(strings.TrimSpace(column.SemanticType)) {
	case "IDENTIFIER":
		return "IDENTIFIER"
	case "DATE", "TIME", "DATETIME":
		return "TIME"
	case "AMOUNT", "QUANTITY", "PERCENTAGE":
		return "MEASURE"
	default:
		return "DIMENSION"
	}
}

func mappedDatasetCanonicalType(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "NUMBER", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT":
		return "INTEGER"
	case "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL":
		return "DECIMAL"
	case "BOOLEAN", "BOOL":
		return "BOOLEAN"
	case "DATE":
		return "DATE"
	case "DATETIME", "TIMESTAMP":
		return "DATETIME"
	default:
		return "STRING"
	}
}

func uniqueMappedIdentifier(value, fallback string, used map[string]bool) string {
	base := mappedIdentifier(value, fallback)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			ending := fmt.Sprintf("_%d", suffix)
			candidate = truncateASCII(base, 128-len(ending)) + ending
		}
		key := strings.ToLower(candidate)
		if !used[key] {
			used[key] = true
			return candidate
		}
	}
}

func mappedIdentifier(value, fallback string) string {
	var result strings.Builder
	for _, character := range strings.TrimSpace(value) {
		switch {
		case character >= 'A' && character <= 'Z', character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '_':
			result.WriteRune(character)
		default:
			result.WriteByte('_')
		}
	}
	identifier := result.String()
	for len(identifier) > 0 && !isASCIILetter(identifier[0]) {
		identifier = identifier[1:]
	}
	if identifier == "" {
		identifier = fallback
	}
	identifier = truncateASCII(identifier, 128)
	if identifier == "" || !isASCIILetter(identifier[0]) {
		return "field"
	}
	return identifier
}

func truncateASCII(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
