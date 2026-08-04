package dataset

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

// LLMTriggerKind 是数据集中心允许人工提交的有界 LLM 后台流程。
type LLMTriggerKind string

const (
	LLMTriggerDIMModeling LLMTriggerKind = "DIM_MODELING"
	LLMTriggerDWDModeling LLMTriggerKind = "DWD_MODELING"
	LLMTriggerDWSModeling LLMTriggerKind = "DWS_MODELING"
)

const maxLLMTriggerDatasetIDs = 200

// LLMTriggerScope 为空时表示按目标层的默认上游范围执行；非空时只允许使用
// 这些当前发布数据集。服务端会在入队事务内再次解析层级，不能信任客户端标签。
type LLMTriggerScope struct {
	DatasetIDs []string `json:"datasetIds"`
}

// LLMTriggerScopeError 是可安全返回给页面的规则校验结果，不包含物理对象信息。
type LLMTriggerScopeError struct {
	Message string
}

func (err *LLMTriggerScopeError) Error() string {
	if err == nil || strings.TrimSpace(err.Message) == "" {
		return ErrLLMTriggerScopeInvalid.Error()
	}
	return err.Message
}

func (err *LLMTriggerScopeError) Unwrap() error { return ErrLLMTriggerScopeInvalid }

// LLMTriggerResult 返回当前发布资产的匹配数和本次实际激活的任务数。终态任务
// 可以通过人工操作安全重跑；未激活的资产已经存在待处理或运行中的任务。
type LLMTriggerResult struct {
	Trigger       LLMTriggerKind `json:"trigger"`
	EligibleCount int64          `json:"eligibleCount"`
	EnqueuedCount int64          `json:"enqueuedCount"`
	ExistingCount int64          `json:"existingCount"`
	BlockedCount  int64          `json:"blockedCount"`
	BlockedReason string         `json:"blockedReason,omitempty"`
}

// LLMTriggerStore 把人工操作与具体 outbox 表隔离，便于 HTTP 与服务合同测试。
type LLMTriggerStore interface {
	TriggerLLM(
		context.Context, string, string, LLMTriggerKind, LLMTriggerScope,
	) (LLMTriggerResult, error)
}

// SetLLMTriggerStore 注册人工 LLM 任务入口。没有注册时 API 以 503 失败关闭。
func (s *Service) SetLLMTriggerStore(store LLMTriggerStore) {
	if s != nil {
		s.llmTrigger = store
	}
}

// TriggerLLM 人工提交当前租户全部符合条件的当前发布版本。任务只生成可评审
// 建模草稿，不直接发布数据集。
func (s *Service) TriggerLLM(
	ctx context.Context,
	tenantID, actorID string,
	kind LLMTriggerKind,
	scope LLMTriggerScope,
) (LLMTriggerResult, error) {
	if s == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" ||
		!validLLMTriggerKind(kind) {
		return LLMTriggerResult{}, ErrInvalidDocument
	}
	normalized, err := normalizeLLMTriggerScope(scope)
	if err != nil {
		return LLMTriggerResult{}, err
	}
	if s.llmTrigger == nil {
		return LLMTriggerResult{}, ErrLLMTriggerUnavailable
	}
	return s.llmTrigger.TriggerLLM(ctx, tenantID, actorID, kind, normalized)
}

func validLLMTriggerKind(kind LLMTriggerKind) bool {
	return kind == LLMTriggerDIMModeling || kind == LLMTriggerDWDModeling ||
		kind == LLMTriggerDWSModeling
}

func normalizeLLMTriggerScope(scope LLMTriggerScope) (LLMTriggerScope, error) {
	if len(scope.DatasetIDs) > maxLLMTriggerDatasetIDs {
		return LLMTriggerScope{}, &LLMTriggerScopeError{
			Message: fmt.Sprintf("一次最多选择 %d 个数据集", maxLLMTriggerDatasetIDs),
		}
	}
	seen := make(map[string]bool, len(scope.DatasetIDs))
	normalized := make([]string, 0, len(scope.DatasetIDs))
	for _, raw := range scope.DatasetIDs {
		id := strings.ToLower(strings.TrimSpace(raw))
		if uuid.Validate(id) != nil {
			return LLMTriggerScope{}, &LLMTriggerScopeError{
				Message: "所选数据集标识无效，请刷新列表后重新选择",
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	scope.DatasetIDs = normalized
	return scope, nil
}

type llmTriggerAsset struct {
	ID    string
	Layer Layer
}

func validateLLMTriggerAssets(
	kind LLMTriggerKind,
	requested []string,
	assets []llmTriggerAsset,
) error {
	if len(requested) == 0 {
		return nil
	}
	if len(assets) != len(requested) {
		return &LLMTriggerScopeError{
			Message: "部分所选数据集不存在、不属于当前业务领域或没有当前已发布版本",
		}
	}
	allowed := map[LLMTriggerKind]map[Layer]bool{
		LLMTriggerDIMModeling: {LayerODS: true},
		LLMTriggerDWDModeling: {LayerODS: true, LayerDIM: true},
		LLMTriggerDWSModeling: {LayerDIM: true, LayerDWD: true},
	}[kind]
	layerCounts := map[Layer]int{}
	for _, asset := range assets {
		if !allowed[asset.Layer] {
			return &LLMTriggerScopeError{Message: llmTriggerLayerRule(kind)}
		}
		layerCounts[asset.Layer]++
	}
	if kind == LLMTriggerDWDModeling && layerCounts[LayerODS] == 0 {
		return &LLMTriggerScopeError{
			Message: "明细建模至少需要选择一个 ODS 数据集，DIM 只能作为可选维度输入",
		}
	}
	if kind == LLMTriggerDWSModeling && layerCounts[LayerDWD] == 0 {
		return &LLMTriggerScopeError{
			Message: "主题建模至少需要选择一个 DWD 数据集，DIM 只能作为可选维度上下文",
		}
	}
	return nil
}

func llmTriggerLayerRule(kind LLMTriggerKind) string {
	switch kind {
	case LLMTriggerDIMModeling:
		return "维度建模只能选择 ODS 数据集"
	case LLMTriggerDWDModeling:
		return "明细建模只能选择 ODS 数据集和可选的 DIM 数据集"
	case LLMTriggerDWSModeling:
		return "主题建模只能选择 DWD 数据集和可选的 DIM 数据集"
	default:
		return "所选数据集不符合建模层级规则"
	}
}

// TriggerLLM 在租户事务内把人工选择转换成现有 durable outbox。唯一约束确保
// 对同一精确发布版本只保留一个任务身份；终态任务会清理运行态后重新入队，
// 待处理或运行中的任务不会因重复点击再次提交。
func (s *PostgresStore) TriggerLLM(
	ctx context.Context,
	tenantID, actorID string,
	kind LLMTriggerKind,
	scope LLMTriggerScope,
) (result LLMTriggerResult, err error) {
	if s == nil || s.pool == nil || strings.TrimSpace(tenantID) == "" ||
		strings.TrimSpace(actorID) == "" || !validLLMTriggerKind(kind) {
		return LLMTriggerResult{}, ErrInvalidDocument
	}
	result.Trigger = kind
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if len(scope.DatasetIDs) > 0 {
			rows, queryErr := tx.Query(ctx, `SELECT
					dataset.id::text,version.layer
				FROM unnest($1::uuid[]) AS selected(dataset_id)
				JOIN platform.datasets AS dataset
				  ON dataset.id=selected.dataset_id
				 AND dataset.tenant_id=platform.current_tenant_id()
				 AND dataset.domain_id=platform.current_domain_id()
				 AND dataset.status='PUBLISHED'
				 AND dataset.deleted_at IS NULL
				JOIN platform.dataset_versions AS version
				  ON version.id=dataset.current_published_version_id
				 AND version.dataset_id=dataset.id
				 AND version.tenant_id=dataset.tenant_id
				 AND version.status='PUBLISHED'
				ORDER BY dataset.id`,
				scope.DatasetIDs,
			)
			if queryErr != nil {
				return queryErr
			}
			assets := make([]llmTriggerAsset, 0, len(scope.DatasetIDs))
			for rows.Next() {
				var asset llmTriggerAsset
				if scanErr := rows.Scan(&asset.ID, &asset.Layer); scanErr != nil {
					rows.Close()
					return scanErr
				}
				assets = append(assets, asset)
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				rows.Close()
				return rowsErr
			}
			rows.Close()
			if validationErr := validateLLMTriggerAssets(
				kind, scope.DatasetIDs, assets,
			); validationErr != nil {
				return validationErr
			}
		}
		var selectedIDs any
		if len(scope.DatasetIDs) > 0 {
			selectedIDs = scope.DatasetIDs
		}
		var triggerErr error
		switch kind {
		case LLMTriggerDIMModeling:
			triggerErr = triggerDIMModeling(
				ctx, tx, actorID, selectedIDs, &result,
			)
		case LLMTriggerDWDModeling:
			triggerErr = triggerDWDModeling(
				ctx, tx, actorID, selectedIDs, &result,
			)
		case LLMTriggerDWSModeling:
			triggerErr = triggerDWSModeling(
				ctx, tx, actorID, selectedIDs, &result,
			)
		default:
			return ErrInvalidDocument
		}
		if triggerErr != nil {
			return triggerErr
		}
		_, triggerErr = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES(
				platform.current_tenant_id(),$1::uuid,'TRIGGER_DATASET_LLM',
				'DATASET','',jsonb_build_object(
					'trigger',$2::text,
					'eligibleCount',$3::bigint,
					'enqueuedCount',$4::bigint,
					'selectedDatasetIds',COALESCE($5::uuid[],'{}'::uuid[])
				)
			)`,
			actorID, string(kind), result.EligibleCount, result.EnqueuedCount,
			selectedIDs,
		)
		return triggerErr
	})
	if err != nil {
		return LLMTriggerResult{}, err
	}
	if result.ExistingCount < 0 {
		return LLMTriggerResult{}, errors.New("dataset LLM trigger count invariant failed")
	}
	return result, nil
}

func triggerDIMModeling(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	selectedIDs any,
	result *LLMTriggerResult,
) error {
	return tx.QueryRow(ctx, `SELECT
			eligible_count,enqueued_count,existing_count,
			blocked_count,blocked_reason
		FROM platform.trigger_manual_dim_modeling(
			$1::uuid,$2::uuid[]
		)`, actorID, selectedIDs,
	).Scan(
		&result.EligibleCount, &result.EnqueuedCount, &result.ExistingCount,
		&result.BlockedCount, &result.BlockedReason,
	)
}

func triggerDWDModeling(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	selectedIDs any,
	result *LLMTriggerResult,
) error {
	err := tx.QueryRow(ctx, `SELECT
			eligible_count,enqueued_count,existing_count,
			blocked_count,blocked_reason
		FROM platform.trigger_manual_dwd_modeling(
			$1::uuid,$2::uuid[]
		)`, actorID, selectedIDs,
	).Scan(
		&result.EligibleCount, &result.EnqueuedCount, &result.ExistingCount,
		&result.BlockedCount, &result.BlockedReason,
	)
	if err == nil && result.BlockedReason == "NO_FACT_MODEL_AVAILABLE" &&
		result.EnqueuedCount+result.ExistingCount > 0 {
		// 一个纯维度领域无需 DWD，不应覆盖同一批次中其他领域已成功提交
		// 或正在运行的事实落地结果。
		result.BlockedReason = ""
	}
	return err
}

func triggerDWSModeling(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	selectedIDs any,
	result *LLMTriggerResult,
) error {
	// 主题建模曾随旧语义问答模块退役。当前入口只恢复数据集中心需要的
	// durable DWS 草稿任务：每张所选 DWD 是一个独立事实范围，所选 DIM
	// 只作为 LLM 语义上下文；任务不会发布或物化生成的数据集。
	err := tx.QueryRow(ctx, `WITH current_assets AS (
			SELECT dataset.id AS dataset_id,dataset.domain_id,
				dataset.code,dataset.name,version.id AS version_id,
				version.layer,version.schema_hash,version.dsl_json
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dataset.tenant_id
			 AND version.dataset_id=dataset.id
			 AND version.id=dataset.current_published_version_id
			 AND version.status='PUBLISHED'
			WHERE dataset.tenant_id=platform.current_tenant_id()
			  AND dataset.domain_id=platform.current_domain_id()
			  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			  AND version.layer IN ('DIM','DWD')
			  AND ($2::uuid[] IS NULL OR dataset.id=ANY($2::uuid[]))
		), scopes AS (
			SELECT fact.dataset_id,fact.version_id,
				'single-dwd:'||fact.dataset_id::text AS group_key,
				jsonb_build_object(
				  'groupKey','single-dwd:'||fact.dataset_id::text,
				  'domainId',fact.domain_id::text,
				  'subjectName',COALESCE(NULLIF(fact.dsl_json#>>'{dataset,subject}',''),fact.name),
				  'dwd',jsonb_build_array(jsonb_build_object(
				    'datasetId',fact.dataset_id::text,'versionId',fact.version_id::text,
				    'dslHash',fact.schema_hash,'code',fact.code,'name',fact.name
				  )),
				  'dim',COALESCE((SELECT jsonb_agg(jsonb_build_object(
				    'datasetId',dimension.dataset_id::text,'versionId',dimension.version_id::text,
				    'dslHash',dimension.schema_hash,'code',dimension.code,'name',dimension.name
				  ) ORDER BY dimension.code,dimension.dataset_id)
				  FROM current_assets AS dimension WHERE dimension.layer='DIM'),'[]'::jsonb)
				) AS source_scope
			FROM current_assets AS fact WHERE fact.layer='DWD'
		), normalized AS (
			SELECT scope.*,encode(public.digest(
				convert_to(scope.source_scope::text,'UTF8'),'sha256'
			),'hex') AS scope_hash
			FROM scopes AS scope
		), activated AS (
			INSERT INTO platform.dws_modeling_jobs(
				tenant_id,source_dwd_dataset_id,source_dwd_version_id,
				requested_by,group_key,source_scope,scope_hash,
				not_before,next_attempt_at
			)
			SELECT platform.current_tenant_id(),dataset_id,version_id,
				$1::uuid,group_key,source_scope,scope_hash,now(),now()
			FROM normalized
			ON CONFLICT(tenant_id,source_dwd_version_id,scope_hash) DO UPDATE
			SET requested_by=EXCLUDED.requested_by,status='PENDING',
				not_before=now(),next_attempt_at=now(),requested_at=now(),
				attempt=0,lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				ai_request_id=NULL,generated_count=0,updated_count=0,skipped_count=0,
				result_json='{}'::jsonb,error_code='',error_message='',
				started_at=NULL,completed_at=NULL,updated_at=now()
			WHERE platform.dws_modeling_jobs.status IN (
				'SUCCEEDED','PARTIAL','FAILED','SKIPPED'
			)
			RETURNING id
		), blocked AS (
			SELECT count(*)::bigint AS total
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dataset.tenant_id
			 AND version.dataset_id=dataset.id
			 AND version.id=dataset.current_draft_version_id
			WHERE $2::uuid[] IS NULL
			  AND dataset.tenant_id=platform.current_tenant_id()
			  AND dataset.domain_id=platform.current_domain_id()
			  AND dataset.current_published_version_id IS NULL
			  AND dataset.deleted_at IS NULL AND version.layer='DWD'
		)
		SELECT (SELECT count(*) FROM normalized),
			(SELECT count(*) FROM activated),(SELECT total FROM blocked)`,
		actorID, selectedIDs,
	).Scan(
		&result.EligibleCount, &result.EnqueuedCount, &result.BlockedCount,
	)
	if err != nil {
		return err
	}
	// SQL 入口用 eligible-enqueued 表示同一精确范围已经存在活跃任务。
	result.ExistingCount = result.EligibleCount - result.EnqueuedCount
	if result.ExistingCount < 0 {
		return errors.New("DWS modeling trigger count invariant failed")
	}
	if result.EligibleCount == 0 && result.BlockedCount > 0 {
		result.BlockedReason = "DWD_PUBLICATION_REQUIRED"
	}
	return nil
}
