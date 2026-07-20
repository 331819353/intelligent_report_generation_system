package metadataai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// EnrichmentCommitSink 在元数据表完成整表补全的同一事务内提交下游产物。
// 实现方不得自行提交或回滚 tx。
type EnrichmentCommitSink interface {
	EnsureMappedDatasetTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, tableID string) error
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

// LoadInput 加载目标表及字段的技术元数据、业务版本和人工锁定状态。
func (s *PostgresStore) LoadInput(ctx context.Context, tenantID, tableID string) (input CompletionInput, err error) {
	input.SchemaVersion = SchemaVersion
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var primaryKeys, constraints, indexes []byte
		err := tx.QueryRow(ctx, `SELECT id::text,structure_hash,catalog_name,schema_name,table_name,table_type,source_comment,
			primary_key_columns,constraints_json,indexes_json,business_name,business_description,tags,sensitivity_level::text,manual_locked,business_version
			FROM platform.metadata_tables WHERE id=$1 AND asset_status='ACTIVE'`, tableID).
			Scan(&input.Table.ID, &input.StructureHash, &input.Table.CatalogName, &input.Table.SchemaName, &input.Table.Name, &input.Table.TableType,
				&input.Table.SourceComment, &primaryKeys, &constraints, &indexes, &input.Table.CurrentBusinessName, &input.Table.CurrentDescription,
				&input.Table.CurrentTags, &input.Table.CurrentSensitivity, &input.Table.ManualLocked, &input.Table.BusinessVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		input.Table.Kind = "TABLE"
		input.Table.StructureHash = input.StructureHash
		if err := json.Unmarshal(primaryKeys, &input.Table.PrimaryKeyColumns); err != nil {
			return err
		}
		input.Table.Constraints = append(json.RawMessage(nil), constraints...)
		input.Table.Indexes = append(json.RawMessage(nil), indexes...)
		rows, err := tx.Query(ctx, `SELECT id::text,column_name,ordinal_position,source_comment,native_type,canonical_type,
			length,numeric_precision,numeric_scale,nullable,default_value,is_primary_key,is_foreign_key,is_unique,
			business_name,business_description,tags,semantic_type,sensitivity_level::text,manual_locked,business_version,structure_hash
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
				&target.ManualLocked, &target.BusinessVersion, &target.StructureHash); err != nil {
				return err
			}
			input.Columns = append(input.Columns, target)
		}
		return rows.Err()
	})
	return
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

// persistSuggestion 锁定目标记录，决定自动应用或转人工确认，并保存建议快照。
func (s *PostgresStore) persistSuggestion(ctx context.Context, tx pgx.Tx, tenantID, jobID string, target Target, value SuggestionValue, threshold float64) (Suggestion, error) {
	var locked bool
	var currentVersion int64
	var currentStructureHash string
	table := target.Kind == "TABLE"
	query := `SELECT manual_locked,business_version,structure_hash FROM platform.metadata_columns WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	if table {
		query = `SELECT manual_locked,business_version,structure_hash FROM platform.metadata_tables WHERE id=$1 AND asset_status='ACTIVE' FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, query, target.ID).Scan(&locked, &currentVersion, &currentStructureHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Suggestion{}, ErrConflict
		}
		return Suggestion{}, err
	}
	if target.StructureHash == "" || currentStructureHash != target.StructureHash {
		return Suggestion{}, ErrStructureChanged
	}
	status, reason := suggestionDisposition(locked, currentVersion, target.BusinessVersion, value.Confidence, threshold)
	if status == "APPLIED" {
		var command string
		if table {
			command = `UPDATE platform.metadata_tables SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,business_version=business_version+1 WHERE id=$5 AND business_version=$6 AND manual_locked=false`
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
		}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return Suggestion{}, err
	}
	suggestion := Suggestion{JobID: jobID, TargetType: target.Kind, TargetID: target.ID, Value: value, Confidence: value.Confidence, Status: status, PendingReason: reason}
	err = tx.QueryRow(ctx, `INSERT INTO platform.ai_metadata_suggestions(tenant_id,job_id,target_type,target_id,proposed_value,confidence,expected_business_version,expected_structure_hash,status,pending_reason)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text,created_at::text`, tenantID, jobID, target.Kind, target.ID, payload, value.Confidence, target.BusinessVersion, target.StructureHash, status, reason).
		Scan(&suggestion.ID, &suggestion.CreatedAt)
	return suggestion, err
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
		rows, err := tx.Query(ctx, `SELECT id::text,job_id::text,target_type,target_id::text,proposed_value,confidence::float8,status,pending_reason,created_at::text,COALESCE(decided_at::text,'')
			FROM platform.ai_metadata_suggestions WHERE ($1='' OR job_id=$1::uuid) AND ($2='' OR status=$2) ORDER BY created_at DESC,id LIMIT $3`, jobID, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Suggestion
			var payload []byte
			if err := rows.Scan(&item.ID, &item.JobID, &item.TargetType, &item.TargetID, &payload, &item.Confidence, &item.Status, &item.PendingReason, &item.CreatedAt, &item.DecidedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(payload, &item.Value); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return
}

// DecideSuggestion 锁定待处理建议，接受时以期望版本原子更新目标元数据。
func (s *PostgresStore) DecideSuggestion(ctx context.Context, tenantID, actorID, suggestionID, decision string) (item Suggestion, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var payload []byte
		var expectedVersion int64
		var expectedStructureHash string
		if err := tx.QueryRow(ctx, `SELECT id::text,job_id::text,target_type,target_id::text,proposed_value,confidence::float8,status,pending_reason,expected_business_version,expected_structure_hash,created_at::text
			FROM platform.ai_metadata_suggestions WHERE id=$1 FOR UPDATE`, suggestionID).
			Scan(&item.ID, &item.JobID, &item.TargetType, &item.TargetID, &payload, &item.Confidence, &item.Status, &item.PendingReason, &expectedVersion, &expectedStructureHash, &item.CreatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 只有待处理建议可决策，避免重复接受或拒绝。
		if item.Status != "PENDING" {
			return ErrConflict
		}
		if err := json.Unmarshal(payload, &item.Value); err != nil {
			return err
		}
		newStatus := "REJECTED"
		if decision == "ACCEPT" {
			newStatus = "ACCEPTED"
			var command string
			var args []any
			if item.TargetType == "TABLE" {
				command = `UPDATE platform.metadata_tables SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,business_version=business_version+1
					WHERE id=$5 AND business_version=$6 AND structure_hash=$7 AND asset_status='ACTIVE' AND manual_locked=false`
				args = []any{item.Value.BusinessName, item.Value.BusinessDescription, item.Value.Tags, item.Value.SensitivityLevel, item.TargetID, expectedVersion, expectedStructureHash}
			} else {
				command = `UPDATE platform.metadata_columns SET business_name=$1,business_description=$2,tags=$3,sensitivity_level=$4,semantic_type=$5,business_version=business_version+1
					WHERE id=$6 AND business_version=$7 AND structure_hash=$8 AND asset_status='ACTIVE' AND manual_locked=false`
				args = []any{item.Value.BusinessName, item.Value.BusinessDescription, item.Value.Tags, item.Value.SensitivityLevel, item.Value.SemanticType, item.TargetID, expectedVersion, expectedStructureHash}
			}
			tag, err := tx.Exec(ctx, command, args...)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		item.Status = newStatus
		item.PendingReason = ""
		if err := tx.QueryRow(ctx, `UPDATE platform.ai_metadata_suggestions SET status=$1,pending_reason='',decided_by=$2,decided_at=now() WHERE id=$3 RETURNING decided_at::text`, newStatus, actorID, suggestionID).Scan(&item.DecidedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, tenantID, actorID, decision+"_METADATA_AI_SUGGESTION", "AI_METADATA_SUGGESTION", suggestionID, "SUCCESS", map[string]any{
			"jobId": item.JobID, "targetType": item.TargetType, "targetId": item.TargetID,
		})
	})
	return
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
