package metadataai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/semanticquality"
)

// EnrichmentCommitSink 在元数据表完成整表补全的同一事务内提交下游产物。
// 实现方不得自行提交或回滚 tx。
type EnrichmentCommitSink interface {
	EnsureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error
	EnsureMappedDatasetDraftTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error
}

type PostgresStore struct {
	pool                 *pgxpool.Pool
	enrichmentCommitSink EnrichmentCommitSink
}

// NewPostgresStore 创建智能补全任务与建议的 PostgreSQL 存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// SetEnrichmentCommitSink 注入整表补全成功后需在当前事务内提交的下游产物。
func (s *PostgresStore) SetEnrichmentCommitSink(sink EnrichmentCommitSink) {
	s.enrichmentCommitSink = sink
}

// ensureMappedDatasetTx 将可选 sink 的调用集中在一个可独立验证的边界；nil sink 保持兼容。
func (s *PostgresStore) ensureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error {
	if s.enrichmentCommitSink == nil {
		return nil
	}
	return s.enrichmentCommitSink.EnsureMappedDatasetTx(ctx, tx, tenantID, actorID, tableID)
}

func (s *PostgresStore) ensureMappedDatasetDraftTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error {
	if s.enrichmentCommitSink == nil {
		return nil
	}
	return s.enrichmentCommitSink.EnsureMappedDatasetDraftTx(
		ctx, tx, tenantID, actorID, tableID,
	)
}

// LoadInput 加载目标表及字段的技术元数据、业务版本和人工锁定状态。
func (s *PostgresStore) LoadInput(ctx context.Context, tenantID, tableID string) (input CompletionInput, err error) {
	input.SchemaVersion = SchemaVersion
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var primaryKeys, constraints, indexes []byte
		var sourceType, sourceFilename string
		err := tx.QueryRow(ctx, `SELECT t.id::text,t.structure_hash,t.catalog_name,t.schema_name,t.table_name,t.table_type,t.source_comment,
			t.primary_key_columns,t.constraints_json,t.indexes_json,t.business_name,t.business_description,t.tags,t.sensitivity_level::text,false,t.business_version,
			t.last_enriched_table_structure_hash<>t.table_structure_hash,
			d.source_type::text,COALESCE(f.filename,'')
			FROM platform.metadata_tables t
			JOIN platform.data_sources d ON d.id=t.data_source_id AND d.tenant_id=t.tenant_id
			LEFT JOIN platform.file_assets f ON f.id=d.file_asset_id AND f.tenant_id=d.tenant_id
			WHERE t.id=$1 AND t.asset_status='ACTIVE'`, tableID).
			Scan(&input.Table.ID, &input.StructureHash, &input.Table.CatalogName, &input.Table.SchemaName, &input.Table.Name, &input.Table.TableType,
				&input.Table.SourceComment, &primaryKeys, &constraints, &indexes, &input.Table.CurrentBusinessName, &input.Table.CurrentDescription,
				&input.Table.CurrentTags, &input.Table.CurrentSensitivity, &input.Table.ManualLocked, &input.Table.BusinessVersion,
				&input.Table.NeedsCompletion, &sourceType, &sourceFilename)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		input.SourceFormat = metadataSourceFormat(sourceType, sourceFilename)
		input.Table.Kind = "TABLE"
		input.Table.StructureHash = input.StructureHash
		input.Table.CompletionTracked = true
		input.Table.MissingFields = completionMissingFields(
			input, input.Table, suggestionFromTarget(input.Table), false,
		)
		if err := json.Unmarshal(primaryKeys, &input.Table.PrimaryKeyColumns); err != nil {
			return err
		}
		input.Table.Constraints = append(json.RawMessage(nil), constraints...)
		input.Table.Indexes = append(json.RawMessage(nil), indexes...)
		rows, err := tx.Query(ctx, `SELECT id::text,column_name,ordinal_position,source_comment,native_type,canonical_type,
			length,numeric_precision,numeric_scale,nullable,default_value,is_primary_key,is_foreign_key,is_unique,
			business_name,business_description,tags,semantic_type,sensitivity_level::text,manual_locked,business_version,structure_hash
			,last_enriched_structure_hash<>structure_hash
			FROM platform.metadata_columns WHERE table_id=$1 AND asset_status='ACTIVE' ORDER BY ordinal_position,id`, tableID)
		if err != nil {
			return err
		}
		defer rows.Close()
		input.Columns = []Target{}
		for rows.Next() {
			var target Target
			target.Kind = "COLUMN"
			if err := rows.Scan(&target.ID, &target.Name, &target.OrdinalPosition, &target.SourceComment, &target.NativeType, &target.CanonicalType,
				&target.Length, &target.NumericPrecision, &target.NumericScale, &target.Nullable, &target.DefaultValue, &target.PrimaryKey, &target.ForeignKey, &target.Unique,
				&target.CurrentBusinessName, &target.CurrentDescription, &target.CurrentTags, &target.CurrentSemanticType, &target.CurrentSensitivity,
				&target.ManualLocked, &target.BusinessVersion, &target.StructureHash, &target.NeedsCompletion); err != nil {
				return err
			}
			target.CompletionTracked = true
			target.MissingFields = completionMissingFields(
				input, target, suggestionFromTarget(target), true,
			)
			input.Columns = append(input.Columns, target)
		}
		return rows.Err()
	})
	return
}

// metadataSourceFormat 只向模型公开安全的格式枚举；CSV 需由文件扩展名与 EXCEL 数据源类型共同确认。
func metadataSourceFormat(sourceType, filename string) string {
	if strings.EqualFold(strings.TrimSpace(sourceType), SourceFormatExcel) {
		if strings.EqualFold(filepath.Ext(strings.TrimSpace(filename)), ".csv") {
			return SourceFormatCSV
		}
		return SourceFormatExcel
	}
	return SourceFormatDatabase
}

// CreateJob 创建运行中任务，并在同一事务内记录启动审计。
func (s *PostgresStore) CreateJob(ctx context.Context, tenantID, actorID string, job Job) (Job, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.ai_metadata_jobs(tenant_id,table_id,metadata_structure_hash,data_source_metadata_job_item_id,provider,model_name,prompt_version,input_hash,status,created_by)
			VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,'RUNNING',$9) RETURNING id::text,created_at::text`, tenantID, job.TableID, job.StructureHash, job.ProcessingItemID, job.Provider, job.Model, job.PromptVersion, job.InputHash, actorID).
			Scan(&job.ID, &job.CreatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, tenantID, actorID, "START_METADATA_AI_COMPLETION", "AI_METADATA_JOB", job.ID, "SUCCESS", map[string]any{
			"tableId": job.TableID, "metadataStructureHash": job.StructureHash, "processingItemId": job.ProcessingItemID, "provider": job.Provider, "model": job.Model, "promptVersion": job.PromptVersion, "inputHash": job.InputHash,
		})
	})
	return job, err
}

// FailJob 终结运行中任务，保存错误分类、耗时和令牌用量。
func (s *PostgresStore) FailJob(ctx context.Context, tenantID, actorID string, job Job, errorCode string) (Job, error) {
	job.Status = "FAILED"
	job.ErrorCode = errorCode
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE platform.ai_metadata_jobs SET status='FAILED',error_code=$1,model_name=$2,model_version=$3,prompt_tokens=$4,completion_tokens=$5,total_tokens=$6,latency_ms=$7,completed_at=now()
			WHERE id=$8 AND status='RUNNING' RETURNING completed_at::text`, errorCode, job.Model, job.ModelVersion, job.PromptTokens, job.CompletionTokens, job.TotalTokens, job.LatencyMS, job.ID).
			Scan(&job.CompletedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		return insertAudit(ctx, tx, tenantID, actorID, "COMPLETE_METADATA_AI_COMPLETION", "AI_METADATA_JOB", job.ID, "FAILURE", map[string]any{
			"errorCode": errorCode, "provider": job.Provider, "model": job.Model, "inputHash": job.InputHash, "latencyMs": job.LatencyMS, "tokenUsage": usageMap(job),
		})
	})
	return job, err
}

// SaveResult 在一个事务中保存模型结果、应用合格建议并完成任务。
func (s *PostgresStore) SaveResult(ctx context.Context, tenantID, actorID string, job Job, input CompletionInput, result ProviderResult, threshold float64) (Job, []Suggestion, error) {
	parsed, err := json.Marshal(result.Output)
	if err != nil {
		return job, nil, err
	}
	suggestions := []Suggestion{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if job.ProcessingItemID != "" {
			// 迟到的旧 worker 即使模型忽略取消，也不能在租约转移后提交建议或占用成功幂等键。
			var valid bool
			err := tx.QueryRow(ctx, `SELECT true FROM platform.data_source_metadata_job_items i
				JOIN platform.data_source_metadata_jobs j ON j.id=i.job_id AND j.tenant_id=i.tenant_id
				WHERE i.id=$1 AND i.status='RUNNING' AND j.status='RUNNING'
				AND j.lease_owner=$2 AND j.lease_expires_at>now()
				FOR UPDATE OF i,j`, job.ProcessingItemID, job.ProcessingWorkerID).Scan(&valid)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProcessingLeaseLost
			}
			if err != nil {
				return err
			}
		}
		// 锁定当前技术结构直到建议与成功标记提交，防止并发刷新后误应用旧模型结果。
		var currentStructureHash string
		query := `SELECT t.structure_hash FROM platform.metadata_tables t
			WHERE t.id=$1 AND t.asset_status='ACTIVE' FOR UPDATE OF t`
		args := []any{job.TableID}
		if job.ProcessingItemID != "" {
			query = `SELECT t.structure_hash FROM platform.metadata_tables t
				JOIN platform.data_sources d ON d.id=t.data_source_id AND d.tenant_id=t.tenant_id
				WHERE t.id=$1 AND t.asset_status='ACTIVE' AND d.status='ACTIVE' AND d.deleted_at IS NULL
				AND ($2::bigint=0 OR d.version=$2) FOR UPDATE OF t,d`
			args = append(args, job.ProcessingSourceVersion)
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&currentStructureHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if job.ProcessingItemID != "" {
					return ErrSourceChanged
				}
				return ErrStructureChanged
			}
			return err
		}
		if job.StructureHash == "" || input.StructureHash != job.StructureHash || currentStructureHash != job.StructureHash {
			return ErrStructureChanged
		}
		values := make([]struct {
			target Target
			value  SuggestionValue
		}, 0, len(input.Columns)+1)
		if input.TargetTable {
			if result.Output.Table == nil {
				return ErrConflict
			}
			values = append(values, struct {
				target Target
				value  SuggestionValue
			}{input.Table, *result.Output.Table})
		}
		byID := make(map[string]Target, len(input.Columns))
		for _, target := range input.Columns {
			byID[target.ID] = target
		}
		for _, value := range result.Output.Columns {
			values = append(values, struct {
				target Target
				value  SuggestionValue
			}{byID[value.TargetID], value})
		}
		// 表建议和字段建议统一走锁定、版本与置信度判定。
		applied, pending := 0, 0
		for _, pair := range values {
			suggestion, err := s.persistSuggestion(ctx, tx, tenantID, job.ID, pair.target, pair.value, threshold)
			if err != nil {
				return err
			}
			if suggestion.Status == "APPLIED" {
				applied++
			} else {
				pending++
			}
			suggestions = append(suggestions, suggestion)
		}
		// scoped marker 与建议在同一事务推进；只有所有活动目标均已评估才发布完整结构 marker。
		if input.TargetTable {
			tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables
				SET last_enriched_table_structure_hash=table_structure_hash
				WHERE id=$1 AND structure_hash=$2 AND asset_status='ACTIVE'`, job.TableID, job.StructureHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrStructureChanged
			}
		}
		if len(input.Columns) > 0 {
			columnIDs := make([]string, 0, len(input.Columns))
			for _, target := range input.Columns {
				columnIDs = append(columnIDs, target.ID)
			}
			tag, err := tx.Exec(ctx, `UPDATE platform.metadata_columns
				SET last_enriched_structure_hash=structure_hash
				WHERE table_id=$1 AND id=ANY($2::uuid[]) AND asset_status='ACTIVE'`, job.TableID, columnIDs)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != int64(len(columnIDs)) {
				return ErrStructureChanged
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables t SET last_enriched_structure_hash=t.structure_hash
			WHERE t.id=$1 AND t.structure_hash=$2 AND t.asset_status='ACTIVE'
			AND t.last_enriched_table_structure_hash=t.table_structure_hash
			AND NOT EXISTS (SELECT 1 FROM platform.metadata_columns c WHERE c.table_id=t.id AND c.asset_status='ACTIVE'
				AND c.last_enriched_structure_hash<>c.structure_hash)`, job.TableID, job.StructureHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrStructureChanged
		}
		// 完整补全 marker 已原子推进后再生成映射数据集；sink 失败会使建议、marker
		// 与映射数据集一并回滚，且任务不会被标记为成功。
		if err := s.ensureMappedDatasetTx(ctx, tx, tenantID, actorID, job.TableID); err != nil {
			return err
		}
		job.Status = "SUCCEEDED"
		job.ErrorCode = ""
		if err := tx.QueryRow(ctx, `UPDATE platform.ai_metadata_jobs SET status='SUCCEEDED',model_name=$1,model_version=$2,parsed_result=$3,prompt_tokens=$4,completion_tokens=$5,total_tokens=$6,latency_ms=$7,completed_at=now()
			WHERE id=$8 AND status='RUNNING' RETURNING completed_at::text`, job.Model, job.ModelVersion, parsed, job.PromptTokens, job.CompletionTokens, job.TotalTokens, job.LatencyMS, job.ID).
			Scan(&job.CompletedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		return insertAudit(ctx, tx, tenantID, actorID, "COMPLETE_METADATA_AI_COMPLETION", "AI_METADATA_JOB", job.ID, "SUCCESS", map[string]any{
			"provider": job.Provider, "model": job.Model, "inputHash": job.InputHash, "latencyMs": job.LatencyMS, "tokenUsage": usageMap(job), "applied": applied, "pending": pending,
		})
	})
	return job, suggestions, err
}

// SavePartialResult 保存结构化输出中能够独立通过校验的目标，并只推进这些目标
// 的 scoped marker。整表完成 marker 与自动发布保持不动；下一次重试会据此只
// 请求尚未完成的目标，同时创建或刷新一个可人工编辑的 ODS 草稿。
func (s *PostgresStore) SavePartialResult(
	ctx context.Context,
	tenantID, actorID string,
	job Job,
	input CompletionInput,
	result ProviderResult,
	threshold float64,
) (Job, []Suggestion, error) {
	if err := validatePartialOutput(input, result.Output); err != nil {
		return job, nil, err
	}
	parsed, err := json.Marshal(result.Output)
	if err != nil {
		return job, nil, err
	}
	suggestions := []Suggestion{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if job.ProcessingItemID != "" {
			var valid bool
			err := tx.QueryRow(ctx, `SELECT true FROM platform.data_source_metadata_job_items i
				JOIN platform.data_source_metadata_jobs j ON j.id=i.job_id AND j.tenant_id=i.tenant_id
				WHERE i.id=$1 AND i.status='RUNNING' AND j.status='RUNNING'
				AND j.lease_owner=$2 AND j.lease_expires_at>now()
				FOR UPDATE OF i,j`, job.ProcessingItemID, job.ProcessingWorkerID).Scan(&valid)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProcessingLeaseLost
			}
			if err != nil {
				return err
			}
		}
		var currentStructureHash string
		query := `SELECT t.structure_hash FROM platform.metadata_tables t
			WHERE t.id=$1 AND t.asset_status='ACTIVE' FOR UPDATE OF t`
		args := []any{job.TableID}
		if job.ProcessingItemID != "" {
			query = `SELECT t.structure_hash FROM platform.metadata_tables t
				JOIN platform.data_sources d ON d.id=t.data_source_id AND d.tenant_id=t.tenant_id
				WHERE t.id=$1 AND t.asset_status='ACTIVE' AND d.status='ACTIVE' AND d.deleted_at IS NULL
				AND ($2::bigint=0 OR d.version=$2) FOR UPDATE OF t,d`
			args = append(args, job.ProcessingSourceVersion)
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&currentStructureHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if job.ProcessingItemID != "" {
					return ErrSourceChanged
				}
				return ErrStructureChanged
			}
			return err
		}
		if job.StructureHash == "" || input.StructureHash != job.StructureHash ||
			currentStructureHash != job.StructureHash {
			return ErrStructureChanged
		}
		byID := make(map[string]Target, len(input.Columns))
		for _, target := range input.Columns {
			byID[target.ID] = target
		}
		applied, pending := 0, 0
		completedTargets := 0
		if result.Output.Table != nil {
			suggestion, err := s.persistSuggestion(
				ctx, tx, tenantID, job.ID, input.Table, *result.Output.Table, threshold,
			)
			if err != nil {
				return err
			}
			suggestions = append(suggestions, suggestion)
			if suggestion.Status == "APPLIED" {
				applied++
			} else {
				pending++
			}
			if suggestion.Status == "APPLIED" && result.Output.Table.Complete {
				tag, err := tx.Exec(ctx, `UPDATE platform.metadata_tables
					SET last_enriched_table_structure_hash=table_structure_hash
					WHERE id=$1 AND structure_hash=$2 AND asset_status='ACTIVE'`,
					job.TableID, job.StructureHash)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return ErrStructureChanged
				}
				completedTargets++
			}
		}
		columnIDs := make([]string, 0, len(result.Output.Columns))
		for _, value := range result.Output.Columns {
			target := byID[value.TargetID]
			suggestion, err := s.persistSuggestion(
				ctx, tx, tenantID, job.ID, target, value, threshold,
			)
			if err != nil {
				return err
			}
			suggestions = append(suggestions, suggestion)
			if suggestion.Status == "APPLIED" {
				applied++
				if value.Complete {
					columnIDs = append(columnIDs, value.TargetID)
					completedTargets++
				}
			} else {
				pending++
			}
		}
		if len(columnIDs) > 0 {
			tag, err := tx.Exec(ctx, `UPDATE platform.metadata_columns
				SET last_enriched_structure_hash=structure_hash
				WHERE table_id=$1 AND id=ANY($2::uuid[]) AND asset_status='ACTIVE'`,
				job.TableID, columnIDs)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != int64(len(columnIDs)) {
				return ErrStructureChanged
			}
		}
		if err := s.ensureMappedDatasetDraftTx(
			ctx, tx, tenantID, actorID, job.TableID,
		); err != nil {
			return err
		}
		job.Status = "FAILED"
		job.ErrorCode = "PARTIAL_OUTPUT"
		if err := tx.QueryRow(ctx, `UPDATE platform.ai_metadata_jobs SET
			status='FAILED',error_code='PARTIAL_OUTPUT',model_name=$1,model_version=$2,
			parsed_result=$3,prompt_tokens=$4,completion_tokens=$5,total_tokens=$6,
			latency_ms=$7,completed_at=now()
			WHERE id=$8 AND status='RUNNING' RETURNING completed_at::text`,
			job.Model, job.ModelVersion, parsed, job.PromptTokens,
			job.CompletionTokens, job.TotalTokens, job.LatencyMS, job.ID,
		).Scan(&job.CompletedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		return insertAudit(
			ctx, tx, tenantID, actorID, "COMPLETE_METADATA_AI_COMPLETION",
			"AI_METADATA_JOB", job.ID, "FAILURE", map[string]any{
				"errorCode": "PARTIAL_OUTPUT", "provider": job.Provider,
				"model": job.Model, "inputHash": job.InputHash,
				"latencyMs": job.LatencyMS, "tokenUsage": usageMap(job),
				"applied": applied, "pending": pending,
				"completedTargets": completedTargets,
				"requestedTargets": len(input.Columns) + boolCount(input.TargetTable),
			},
		)
	})
	return job, suggestions, err
}

func validatePartialOutput(input CompletionInput, output CompletionOutput) error {
	if output.SchemaVersion != SchemaVersion || output.Columns == nil {
		return ErrInvalidOutput
	}
	count := 0
	complete := 0
	if output.Table != nil {
		if !input.TargetTable || output.Table.TargetID != input.Table.ID {
			return ErrInvalidOutput
		}
		if err := validatePartialSuggestionForTarget(
			input, input.Table, *output.Table, false,
		); err != nil {
			return err
		}
		count++
		if output.Table.Complete {
			complete++
		}
	}
	expected := make(map[string]Target, len(input.Columns))
	for _, target := range input.Columns {
		expected[target.ID] = target
	}
	seen := make(map[string]bool, len(output.Columns))
	for _, value := range output.Columns {
		target, exists := expected[value.TargetID]
		if !exists || seen[value.TargetID] {
			return ErrInvalidOutput
		}
		if err := validatePartialSuggestionForTarget(
			input, target, value, true,
		); err != nil {
			return err
		}
		seen[value.TargetID] = true
		count++
		if value.Complete {
			complete++
		}
	}
	if count == 0 || complete >= len(input.Columns)+boolCount(input.TargetTable) {
		return ErrInvalidOutput
	}
	return nil
}

func validatePartialSuggestionForTarget(
	input CompletionInput,
	target Target,
	value SuggestionValue,
	column bool,
) error {
	if value.TargetID != target.ID ||
		value.Confidence <= 0 || value.Confidence > 1 ||
		len(value.ProvidedFields) == 0 {
		return ErrInvalidOutput
	}
	for _, field := range value.ProvidedFields {
		switch field {
		case "businessName":
			if err := validateText("businessName", value.BusinessName, 120); err != nil {
				return err
			}
			if column && isFileSourceFormat(input.SourceFormat) &&
				!containsChinese(value.BusinessName) {
				return ErrInvalidOutput
			}
			if !column && isFileSourceFormat(input.SourceFormat) &&
				!containsChinese(value.BusinessName) {
				return ErrInvalidOutput
			}
		case "businessDescription":
			if err := validateText(
				"businessDescription", value.BusinessDescription, 1000,
			); err != nil {
				return err
			}
			if column && isFileSourceFormat(input.SourceFormat) &&
				!containsChinese(value.BusinessDescription) {
				return ErrInvalidOutput
			}
		case "tags":
			if !validControlledTags(value.Tags) {
				return ErrInvalidOutput
			}
		case "sensitivityLevel":
			if !allowedSensitivity[value.SensitivityLevel] {
				return ErrInvalidOutput
			}
		case "semanticType":
			if !column || !allowedSemanticTypes[value.SemanticType] ||
				(strings.TrimSpace(target.CanonicalType) != "" &&
					!semanticquality.Compatible(
						target.CanonicalType, value.SemanticType,
					)) {
				return ErrInvalidOutput
			}
		default:
			return ErrInvalidOutput
		}
	}
	complete := len(completionMissingFields(input, target, value, column)) == 0
	if value.Complete != complete {
		return ErrInvalidOutput
	}
	return nil
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

// persistSuggestion 锁定目标记录，决定自动应用或转人工确认，并保存建议快照。
func (s *PostgresStore) persistSuggestion(ctx context.Context, tx pgx.Tx, tenantID, jobID string, target Target, value SuggestionValue, threshold float64) (Suggestion, error) {
	var locked bool
	var currentVersion int64
	var currentStructureHash string
	var baseline SuggestionValue
	table := target.Kind == "TABLE"
	query := `SELECT manual_locked,business_version,structure_hash,business_name,business_description,tags,sensitivity_level::text,semantic_type
		FROM platform.metadata_columns WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	if table {
		query = `SELECT false,business_version,structure_hash,business_name,business_description,tags,sensitivity_level::text,''
			FROM platform.metadata_tables WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, query, target.ID).Scan(&locked, &currentVersion, &currentStructureHash,
		&baseline.BusinessName, &baseline.BusinessDescription, &baseline.Tags, &baseline.SensitivityLevel, &baseline.SemanticType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Suggestion{}, ErrConflict
		}
		return Suggestion{}, err
	}
	if target.StructureHash == "" || currentStructureHash != target.StructureHash {
		return Suggestion{}, ErrStructureChanged
	}
	status, reason := suggestionDispositionForTarget(target, value, locked, currentVersion, threshold)
	if status == "APPLIED" {
		var command string
		if table {
			command = `UPDATE platform.metadata_tables SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,manual_locked=false,business_version=business_version+1 WHERE id=$5 AND business_version=$6`
		} else {
			command = `UPDATE platform.metadata_columns SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,semantic_type=$5,business_version=business_version+1 WHERE id=$6 AND business_version=$7 AND manual_locked=false`
		}
		var tag pgconnCommandTag
		var err error
		if table {
			tag, err = execTag(ctx, tx, command, strings.TrimSpace(value.BusinessName), strings.TrimSpace(value.BusinessDescription), value.Tags, value.SensitivityLevel, target.ID, currentVersion)
		} else {
			tag, err = execTag(ctx, tx, command, strings.TrimSpace(value.BusinessName), strings.TrimSpace(value.BusinessDescription), value.Tags, value.SensitivityLevel, value.SemanticType, target.ID, currentVersion)
		}
		if err != nil {
			return Suggestion{}, err
		}
		if tag.RowsAffected() != 1 {
			status, reason = "PENDING", "VERSION_CHANGED"
		} else {
			// 自动应用后当前值就是刚写入的建议内容；基线必须一并前进，
			// 否则同一目标的后续建议会误判为"已被他人改写"。
			baseline, currentVersion = appliedBaseline(value, table), currentVersion+1
		}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return Suggestion{}, err
	}
	baselinePayload, err := json.Marshal(baseline)
	if err != nil {
		return Suggestion{}, err
	}
	suggestion := Suggestion{JobID: jobID, TargetType: target.Kind, TargetID: target.ID, Value: value, Confidence: value.Confidence, Status: status, PendingReason: reason}
	// 期望版本与基线都取本事务加锁读到的当前状态，而不是构建模型输入时的快照。
	// 写入过期快照会让 VERSION_CHANGED 的建议从入库那一刻起就永远无法被接受。
	err = tx.QueryRow(ctx, `INSERT INTO platform.ai_metadata_suggestions(tenant_id,job_id,target_type,target_id,proposed_value,confidence,expected_business_version,expected_structure_hash,status,pending_reason,baseline_value)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id::text,created_at::text`, tenantID, jobID, target.Kind, target.ID, payload, value.Confidence, currentVersion, target.StructureHash, status, reason, baselinePayload).
		Scan(&suggestion.ID, &suggestion.CreatedAt)
	return suggestion, err
}

// appliedBaseline 构造"建议刚被自动应用"后的目标业务字段快照。
func appliedBaseline(value SuggestionValue, table bool) SuggestionValue {
	baseline := SuggestionValue{
		BusinessName:        strings.TrimSpace(value.BusinessName),
		BusinessDescription: strings.TrimSpace(value.BusinessDescription),
		Tags:                value.Tags,
		SensitivityLevel:    value.SensitivityLevel,
	}
	if !table {
		baseline.SemanticType = value.SemanticType
	}
	return baseline
}

// suggestionDisposition 按人工锁定、乐观版本和置信度依次决定建议去向。
func suggestionDisposition(locked bool, currentVersion, expectedVersion int64, confidence, threshold float64) (string, string) {
	if locked {
		return "PENDING", "MANUAL_LOCKED"
	}
	if currentVersion != expectedVersion {
		return "PENDING", "VERSION_CHANGED"
	}
	if confidence < threshold {
		return "PENDING", "LOW_CONFIDENCE"
	}
	return "APPLIED", ""
}

func suggestionDispositionForTarget(target Target, value SuggestionValue, locked bool, currentVersion int64, threshold float64) (string, string) {
	if target.Kind == "COLUMN" && strings.TrimSpace(value.SemanticType) != "" &&
		!semanticquality.Compatible(target.CanonicalType, value.SemanticType) {
		return "PENDING", "SEMANTIC_TYPE_INCOMPATIBLE"
	}
	return suggestionDisposition(locked, currentVersion, target.BusinessVersion, value.Confidence, threshold)
}

// pgconnCommandTag 抽象受影响行数，简化表与字段更新的统一处理。
type pgconnCommandTag interface{ RowsAffected() int64 }

// execTag 执行更新并返回最小命令结果接口。
func execTag(ctx context.Context, tx pgx.Tx, sql string, args ...any) (pgconnCommandTag, error) {
	return tx.Exec(ctx, sql, args...)
}

// ListSuggestions 在租户范围内按任务和状态分页查询建议。
func (s *PostgresStore) ListSuggestions(ctx context.Context, tenantID, jobID, status string, limit int) (items []Suggestion, err error) {
	items = []Suggestion{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// 目标当前状态与建议一起返回，页面因此可以在用户点击之前就标出
		// 哪些建议不可应用以及原因，而不是等到点击后收到一个泛化的冲突错误。
		rows, err := tx.Query(ctx, `SELECT s.id::text,s.job_id::text,s.target_type,s.target_id::text,s.proposed_value,s.confidence::float8,
				s.status,s.pending_reason,s.expected_structure_hash,s.baseline_value,s.created_at::text,COALESCE(s.decided_at::text,''),
				COALESCE(t.business_name,c.business_name),COALESCE(t.business_description,c.business_description),
				COALESCE(t.tags,c.tags),COALESCE(t.sensitivity_level::text,c.sensitivity_level::text),COALESCE(c.semantic_type,''),
				COALESCE(t.structure_hash,c.structure_hash),COALESCE(c.canonical_type,''),
				(t.id IS NOT NULL OR c.id IS NOT NULL)
			FROM platform.ai_metadata_suggestions s
			LEFT JOIN platform.metadata_tables t ON s.target_type='TABLE' AND t.id=s.target_id AND t.tenant_id=s.tenant_id AND t.asset_status='ACTIVE'
			LEFT JOIN platform.metadata_columns c ON s.target_type='COLUMN' AND c.id=s.target_id AND c.tenant_id=s.tenant_id AND c.asset_status='ACTIVE'
			WHERE ($1='' OR s.job_id=$1::uuid) AND ($2='' OR s.status=$2) ORDER BY s.created_at DESC,s.id LIMIT $3`, jobID, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Suggestion
			var payload, baselinePayload []byte
			var current SuggestionValue
			var expectedStructureHash, structureHash, canonicalType string
			var targetExists bool
			if err := rows.Scan(&item.ID, &item.JobID, &item.TargetType, &item.TargetID, &payload, &item.Confidence,
				&item.Status, &item.PendingReason, &expectedStructureHash, &baselinePayload, &item.CreatedAt, &item.DecidedAt,
				&current.BusinessName, &current.BusinessDescription, &current.Tags, &current.SensitivityLevel, &current.SemanticType,
				&structureHash, &canonicalType, &targetExists); err != nil {
				return err
			}
			if err := json.Unmarshal(payload, &item.Value); err != nil {
				return err
			}
			var baseline SuggestionValue
			if len(baselinePayload) > 0 {
				if err := json.Unmarshal(baselinePayload, &baseline); err != nil {
					return err
				}
			}
			item.Applicable, item.BlockedReason, item.ChangedFields = suggestionApplicability(
				item, baseline, current, targetExists, expectedStructureHash, structureHash, canonicalType,
			)
			items = append(items, item)
		}
		return rows.Err()
	})
	return
}

// suggestionApplicability 用与 DecideSuggestion 相同的判定顺序预先计算可应用性，
// 保证列表上显示的原因和真正点击时得到的结果一致。
func suggestionApplicability(
	item Suggestion,
	baseline, current SuggestionValue,
	targetExists bool,
	expectedStructureHash, structureHash, canonicalType string,
) (bool, string, []string) {
	if item.Status != "PENDING" {
		return false, "", nil
	}
	if !targetExists {
		return false, SuggestionBlockedAssetRemoved, nil
	}
	if expectedStructureHash == "" || structureHash != expectedStructureHash {
		return false, SuggestionBlockedStructureChanged, nil
	}
	if item.TargetType == "COLUMN" && !semanticquality.Compatible(canonicalType, item.Value.SemanticType) {
		return false, SuggestionBlockedSemanticType, nil
	}
	if !isEmptyBaseline(baseline) {
		if changed := businessFieldsChanged(baseline, current, item.TargetType == "COLUMN"); len(changed) > 0 {
			// 内容冲突可以由用户确认后强制覆盖，因此不算完全不可应用。
			return false, SuggestionBlockedTargetEdited, changed
		}
	}
	return true, "", nil
}

// DecideSuggestion 锁定待处理建议并逐项判定可应用性。
//
// 并发安全来自"目标行 FOR UPDATE + 同事务写入"，而不是对 business_version 的精确
// 匹配：那个计数器会被人工保存、后续 AI 轮次和手工完成资产化不断自增，与这条建议
// 要覆盖的内容是否被改动无关，用它做栅栏会让完全有效的建议永久不可用。
// 真正的冲突判定改为比对基线内容，并且每种不可应用原因都单独回报给用户。
//
// 人工锁定只拦截自动应用。用户在复核面板里显式点击接受，本身就是锁要保护的那个
// 人工决定，因此不再阻断这条路径，但会记入审计。
func (s *PostgresStore) DecideSuggestion(ctx context.Context, tenantID, actorID, suggestionID, decision string, force bool) (item Suggestion, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var payload, baselinePayload []byte
		var expectedVersion int64
		var expectedStructureHash string
		if err := tx.QueryRow(ctx, `SELECT id::text,job_id::text,target_type,target_id::text,proposed_value,confidence::float8,status,pending_reason,expected_business_version,expected_structure_hash,baseline_value,created_at::text
			FROM platform.ai_metadata_suggestions WHERE id=$1 FOR UPDATE`, suggestionID).
			Scan(&item.ID, &item.JobID, &item.TargetType, &item.TargetID, &payload, &item.Confidence, &item.Status, &item.PendingReason, &expectedVersion, &expectedStructureHash, &baselinePayload, &item.CreatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 只有待处理建议可决策，避免重复接受或拒绝。
		if item.Status != "PENDING" {
			return &SuggestionConflictError{Reason: SuggestionBlockedAlreadyDecided}
		}
		if err := json.Unmarshal(payload, &item.Value); err != nil {
			return err
		}
		newStatus := "REJECTED"
		var overwritten []string
		var lockedTarget bool
		if decision == "ACCEPT" {
			newStatus = "ACCEPTED"
			current, state, err := lockSuggestionTarget(ctx, tx, item.TargetType, item.TargetID)
			if err != nil {
				return err
			}
			lockedTarget = state.manualLocked
			// 技术结构是这条建议赖以成立的前提，变化后必须重新生成。
			// 历史数据没有记录结构哈希时保持原有的失败关闭语义。
			if expectedStructureHash == "" || state.structureHash != expectedStructureHash {
				return &SuggestionConflictError{Reason: SuggestionBlockedStructureChanged}
			}
			if item.TargetType == "COLUMN" && !semanticquality.Compatible(state.canonicalType, item.Value.SemanticType) {
				return &SuggestionConflictError{Reason: SuggestionBlockedSemanticType}
			}
			// 基线为空表示历史建议未记录生成时的内容，无法证明存在冲突；
			// 此时依靠上面的结构与资产校验兜底，不再凭版本号阻断。
			if len(baselinePayload) > 0 {
				var baseline SuggestionValue
				if err := json.Unmarshal(baselinePayload, &baseline); err != nil {
					return err
				}
				if !isEmptyBaseline(baseline) {
					overwritten = businessFieldsChanged(baseline, current, item.TargetType == "COLUMN")
					if len(overwritten) > 0 && !force {
						return &SuggestionConflictError{Reason: SuggestionBlockedTargetEdited, ChangedFields: overwritten}
					}
				}
			}
			var command string
			var args []any
			if item.TargetType == "TABLE" {
				command = `UPDATE platform.metadata_tables SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,manual_locked=false,business_version=business_version+1
					WHERE id=$5 AND asset_status='ACTIVE' RETURNING business_version`
				args = []any{item.Value.BusinessName, item.Value.BusinessDescription, item.Value.Tags, item.Value.SensitivityLevel, item.TargetID}
			} else {
				command = `UPDATE platform.metadata_columns SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,semantic_type=$5,business_version=business_version+1
					WHERE id=$6 AND asset_status='ACTIVE' RETURNING business_version`
				args = []any{item.Value.BusinessName, item.Value.BusinessDescription, item.Value.Tags, item.Value.SensitivityLevel, item.Value.SemanticType, item.TargetID}
			}
			if err := tx.QueryRow(ctx, command, args...).Scan(&item.TargetBusinessVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return &SuggestionConflictError{Reason: SuggestionBlockedAssetRemoved}
				}
				return err
			}
		}
		// 已决策的建议不再是可应用的候选项，Applicable 保持 false 与列表口径一致。
		item.Status = newStatus
		item.PendingReason = ""
		if err := tx.QueryRow(ctx, `UPDATE platform.ai_metadata_suggestions SET status=$1,pending_reason='',decided_by=$2,decided_at=now() WHERE id=$3 RETURNING decided_at::text`, newStatus, actorID, suggestionID).Scan(&item.DecidedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, tenantID, actorID, decision+"_METADATA_AI_SUGGESTION", "AI_METADATA_SUGGESTION", suggestionID, "SUCCESS", map[string]any{
			"jobId": item.JobID, "targetType": item.TargetType, "targetId": item.TargetID,
			"expectedBusinessVersion": expectedVersion, "targetBusinessVersion": item.TargetBusinessVersion,
			"overwrittenFields": overwritten, "forced": force && len(overwritten) > 0,
			"targetManualLocked": lockedTarget,
		})
	})
	return
}

// suggestionTargetState 是应用建议前需要校验的目标当前技术状态。
type suggestionTargetState struct {
	manualLocked  bool
	structureHash string
	canonicalType string
}

// lockSuggestionTarget 锁定目标行并返回它当前的业务内容与技术状态。
func lockSuggestionTarget(ctx context.Context, tx pgx.Tx, targetType, targetID string) (SuggestionValue, suggestionTargetState, error) {
	var current SuggestionValue
	var state suggestionTargetState
	query := `SELECT business_name,business_description,tags,sensitivity_level::text,semantic_type,manual_locked,structure_hash,canonical_type
		FROM platform.metadata_columns WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	if targetType == "TABLE" {
		query = `SELECT business_name,business_description,tags,sensitivity_level::text,'',false,structure_hash,''
			FROM platform.metadata_tables WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, targetID).Scan(&current.BusinessName, &current.BusinessDescription, &current.Tags,
		&current.SensitivityLevel, &current.SemanticType, &state.manualLocked, &state.structureHash, &state.canonicalType)
	if errors.Is(err, pgx.ErrNoRows) {
		return current, state, &SuggestionConflictError{Reason: SuggestionBlockedAssetRemoved}
	}
	return current, state, err
}

// isEmptyBaseline 识别迁移前写入、没有内容基线的历史建议。
func isEmptyBaseline(baseline SuggestionValue) bool {
	return strings.TrimSpace(baseline.BusinessName) == "" &&
		strings.TrimSpace(baseline.BusinessDescription) == "" &&
		len(baseline.Tags) == 0 &&
		strings.TrimSpace(baseline.SensitivityLevel) == "" &&
		strings.TrimSpace(baseline.SemanticType) == ""
}

// insertAudit 在业务事务内写入智能补全审计事件。
func insertAudit(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, resource, resourceID, result string, detail any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,result,detail) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, actorID, action, resource, resourceID, result, payload)
	return err
}

// usageMap 将任务令牌统计转换为审计详情结构。
func usageMap(job Job) map[string]int {
	return map[string]int{"promptTokens": job.PromptTokens, "completionTokens": job.CompletionTokens, "totalTokens": job.TotalTokens}
}

// validateSuggestionFilter 限制查询接口可接受的建议状态。
func validateSuggestionFilter(status string) error {
	if status == "" || status == "PENDING" || status == "APPLIED" || status == "ACCEPTED" || status == "REJECTED" {
		return nil
	}
	return fmt.Errorf("invalid suggestion status %q", status)
}
