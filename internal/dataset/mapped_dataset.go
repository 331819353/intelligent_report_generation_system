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
	FileVersionID       string
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

	name := strings.TrimSpace(table.BusinessName)
	if name == "" {
		name = strings.TrimSpace(table.TableName)
	}
	if name == "" {
		return Document{}, errors.New("mapped dataset table name is required")
	}
	code := "mapped_" + strings.ReplaceAll(tableUUID.String(), "-", "")
	projection := make([]string, 0, len(columns))
	fields := make([]Field, 0, len(columns))
	endOutputs := make([]map[string]any, 0, len(columns))
	usedCodes, usedIDs := map[string]bool{}, map[string]bool{}
	grainCodes := []string{}

	for index, column := range columns {
		physicalName := strings.TrimSpace(column.ColumnName)
		// 查询编译器只允许经物理白名单校验的数据库标识符。不丢列、
		// 不改写物理名；整表不可安全执行时由 Ensure 路径跳过创建。
		if !physicalIdentifierPattern.MatchString(physicalName) {
			return Document{}, fmt.Errorf("%w: column %d", errMappedDatasetUnsupportedColumn, index+1)
		}
		projection = append(projection, physicalName)
		fieldCode := uniqueMappedIdentifier(physicalName, fmt.Sprintf("field_%d", index+1), usedCodes)
		fieldID := uniqueMappedIdentifier("field_t1_"+fieldCode, fmt.Sprintf("field_t1_%d", index+1), usedIDs)
		fieldName := strings.TrimSpace(column.BusinessName)
		if fieldName == "" {
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
		},
		ExecutionPolicy: ExecutionPolicy{
			Mode:            "REALTIME",
			TimeoutMS:       5000,
			PreviewLimit:    200,
			ResultLimit:     10000,
			CacheTTLSeconds: 300,
			Materialization: MaterializationPolicy{Enabled: false},
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
}

// EnsureMappedDatasetTx 在已有租户事务内幂等创建并默认发布映射表数据集。
// 公开签名供元数据完善事务复用；启动对账通过内部返回值区分真实变更和安全跳过。
func (s *PostgresStore) EnsureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error {
	_, err := s.ensureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
	return err
}

func (s *PostgresStore) ensureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) (bool, error) {
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
	// 软删除、显式停用、已经发布或被人工编辑过的存量对象都不由后台恢复或覆盖。
	if exists && (state.Deleted || state.Status == "DISABLED" || state.PublishedCount > 0) {
		return false, nil
	}

	table := MappedDatasetTable{}
	err = tx.QueryRow(ctx, `SELECT t.id::text,t.data_source_id::text,
		COALESCE((SELECT fv.id::text
			FROM platform.file_assets fa
			JOIN platform.file_asset_versions fv
			  ON fv.file_asset_id=fa.id AND fv.tenant_id=fa.tenant_id AND fv.version=fa.current_version
			WHERE fa.id=source.file_asset_id),''),
		t.table_name,t.business_name,t.business_description
		FROM platform.metadata_tables t
		JOIN platform.data_sources source ON source.id=t.data_source_id AND source.tenant_id=t.tenant_id
		WHERE t.id::text=$1 AND t.tenant_id::text=$2
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
		&table.ID, &table.DataSourceID, &table.FileVersionID, &table.TableName,
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
		(SELECT count(*) FROM platform.dataset_publication_requests AS request
		 WHERE request.dataset_id=dataset.id AND request.tenant_id=dataset.tenant_id AND request.status='PENDING')
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
		WHERE dataset.tenant_id::text=$1 AND dataset.origin_table_id::text=$2
		FOR UPDATE OF dataset,draft`, tenantID, tableID).Scan(
		&state.ID, &state.Deleted, &state.Status, &state.Version,
		&state.DraftVersionID, &state.DraftVersionNo, &state.DraftRecordVersion,
		&state.DSLHash, &state.PlanHash, &state.PublishedCount,
		&state.RevisionCount, &state.ExactCreateCount, &state.PendingApprovalCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mappedDatasetState{}, false, nil
	}
	return state, err == nil, err
}

func (state mappedDatasetState) canDefaultPublish(prepared Prepared) bool {
	return !state.Deleted && state.Status == "DRAFT" && state.Version == 1 &&
		state.DraftVersionID != "" && state.DraftVersionNo == 1 && state.DraftRecordVersion == 1 &&
		state.PublishedCount == 0 && state.RevisionCount == 1 && state.ExactCreateCount == 1 &&
		state.PendingApprovalCount == 0 && state.DSLHash == prepared.DSLHash && state.PlanHash == prepared.PlanHash
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
	if err := s.publishTx(ctx, tx, tenantID, actorID, state.ID, plan, &published); err != nil {
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

func mappedDatasetFieldRole(column MappedDatasetColumn) string {
	if column.PrimaryKey {
		return "IDENTIFIER"
	}
	switch strings.ToUpper(strings.TrimSpace(column.SemanticType)) {
	case "IDENTIFIER":
		return "IDENTIFIER"
	case "DATE", "TIME", "DATETIME":
		return "TIME"
	default:
		return "ATTRIBUTE"
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
