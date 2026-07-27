package dataset

import (
	"context"
	"errors"
	"strings"

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
	TriggerLLM(context.Context, string, string, LLMTriggerKind) (LLMTriggerResult, error)
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
) (LLMTriggerResult, error) {
	if s == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" ||
		!validLLMTriggerKind(kind) {
		return LLMTriggerResult{}, ErrInvalidDocument
	}
	if s.llmTrigger == nil {
		return LLMTriggerResult{}, ErrLLMTriggerUnavailable
	}
	return s.llmTrigger.TriggerLLM(ctx, tenantID, actorID, kind)
}

func validLLMTriggerKind(kind LLMTriggerKind) bool {
	return kind == LLMTriggerDIMModeling ||
		kind == LLMTriggerDWDModeling ||
		kind == LLMTriggerDWSModeling
}

// TriggerLLM 在租户事务内把人工选择转换成现有 durable outbox。唯一约束确保
// 对同一精确发布版本只保留一个任务身份；终态任务会清理运行态后重新入队，
// 待处理或运行中的任务不会因重复点击再次提交。
func (s *PostgresStore) TriggerLLM(
	ctx context.Context,
	tenantID, actorID string,
	kind LLMTriggerKind,
) (result LLMTriggerResult, err error) {
	if s == nil || s.pool == nil || strings.TrimSpace(tenantID) == "" ||
		strings.TrimSpace(actorID) == "" || !validLLMTriggerKind(kind) {
		return LLMTriggerResult{}, ErrInvalidDocument
	}
	result.Trigger = kind
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var triggerErr error
		switch kind {
		case LLMTriggerDIMModeling:
			triggerErr = triggerDIMModeling(ctx, tx, actorID, &result)
		case LLMTriggerDWDModeling:
			triggerErr = triggerDWDModeling(ctx, tx, actorID, &result)
		case LLMTriggerDWSModeling:
			triggerErr = triggerDWSModeling(ctx, tx, actorID, &result)
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
					'enqueuedCount',$4::bigint
				)
			)`,
			actorID, string(kind), result.EligibleCount, result.EnqueuedCount,
		)
		return triggerErr
	})
	if err != nil {
		return LLMTriggerResult{}, err
	}
	if kind == LLMTriggerDWSModeling {
		result.ExistingCount = result.EligibleCount - result.EnqueuedCount
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
	result *LLMTriggerResult,
) error {
	return tx.QueryRow(ctx, `SELECT
			eligible_count,enqueued_count,existing_count,
			blocked_count,blocked_reason
		FROM platform.trigger_manual_dim_modeling($1::uuid)`, actorID,
	).Scan(
		&result.EligibleCount, &result.EnqueuedCount, &result.ExistingCount,
		&result.BlockedCount, &result.BlockedReason,
	)
}

func triggerDWDModeling(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	result *LLMTriggerResult,
) error {
	err := tx.QueryRow(ctx, `SELECT
			eligible_count,enqueued_count,existing_count,
			blocked_count,blocked_reason
		FROM platform.trigger_manual_dwd_modeling($1::uuid)`, actorID,
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
	result *LLMTriggerResult,
) error {
	err := tx.QueryRow(ctx, `SELECT eligible_count,enqueued_count,blocked_count
		FROM platform.trigger_manual_dws_modeling($1::uuid)`, actorID,
	).Scan(&result.EligibleCount, &result.EnqueuedCount, &result.BlockedCount)
	if err != nil {
		return err
	}
	if result.EligibleCount == 0 && result.BlockedCount > 0 {
		result.BlockedReason = "DWD_PUBLICATION_REQUIRED"
	}
	return nil
}
