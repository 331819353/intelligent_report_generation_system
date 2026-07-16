package ai

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

const maxPostgresInteger = int64(1<<31 - 1)

var stableAIErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)

// PostgresStore 使用租户事务完成 AI 授权、配额预留和调用审计状态收口。
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建通用 AI 编排的 PostgreSQL 存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Start 在调用 Provider 前原子校验操作者、租户策略和配额，并登记 RUNNING 预留。
func (s *PostgresStore) Start(ctx context.Context, input StartRequest) (record RequestRecord, err error) {
	input, err = normalizeStartRequest(input)
	if err != nil {
		return RequestRecord{}, err
	}
	err = database.WithTenantTx(ctx, s.pool, input.TenantID, func(tx pgx.Tx) error {
		var actorActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.users
			WHERE tenant_id=$1 AND id=$2 AND status='ACTIVE' AND deleted_at IS NULL
		)`, input.TenantID, input.ActorID).Scan(&actorActive); err != nil {
			return err
		}
		if !actorActive {
			return ErrTenantAIForbidden
		}

		// 策略行锁把并发请求的配额检查与预留串行化，避免多个请求同时越过上限。
		var enabled bool
		var allowedPurposes []string
		var maxRequestsPerDay, maxTokensPerMonth, maxCostMicrosPerMonth int64
		err := tx.QueryRow(ctx, `SELECT enabled,allowed_purposes,max_requests_per_day,max_tokens_per_month,max_cost_micros_per_month
			FROM platform.ai_tenant_policies WHERE tenant_id=$1 FOR UPDATE`, input.TenantID).
			Scan(&enabled, &allowedPurposes, &maxRequestsPerDay, &maxTokensPerMonth, &maxCostMicrosPerMonth)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTenantAIForbidden
		}
		if err != nil {
			return err
		}
		if !enabled || !containsPurpose(allowedPurposes, input.Purpose) {
			return ErrTenantAIForbidden
		}

		// 进程崩溃可能来不及调用 Fail；新请求先收口过期租约，预算仍按失败关闭规则保留。
		if _, err := tx.Exec(ctx, `UPDATE platform.ai_requests SET
			status='FAILED',error_code='AI_ORCHESTRATION_ABANDONED',completed_at=now()
			WHERE tenant_id=$1 AND status='RUNNING' AND expires_at<=now()`, input.TenantID); err != nil {
			return err
		}

		// 仅成功请求采用可信实耗；失败或取消也可能产生供应商费用，因此按预留量失败关闭。
		var requestsToday, tokensThisMonth, costThisMonth int64
		if err := tx.QueryRow(ctx, `WITH boundaries AS (
			SELECT
				date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS day_start,
				date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS month_start
		)
		SELECT
			count(*) FILTER(WHERE request.created_at>=boundaries.day_start),
			LEAST(COALESCE(sum(request.accounted_tokens),0),9223372036854775807)::bigint,
			LEAST(COALESCE(sum(request.accounted_cost_micros),0),9223372036854775807)::bigint
		FROM platform.ai_requests request CROSS JOIN boundaries
		WHERE request.tenant_id=$1 AND request.created_at>=boundaries.month_start`, input.TenantID).
			Scan(&requestsToday, &tokensThisMonth, &costThisMonth); err != nil {
			return err
		}
		if requestsToday >= maxRequestsPerDay || exceedsQuota(tokensThisMonth, int64(input.ReservedTokens), maxTokensPerMonth) || exceedsQuota(costThisMonth, input.ReservedCostMicros, maxCostMicrosPerMonth) {
			return ErrQuotaExceeded
		}

		return tx.QueryRow(ctx, `INSERT INTO platform.ai_requests(
				tenant_id,actor_user_id,purpose,resource_type,resource_id,provider,model_name,prompt_version,
				input_hash,input_bytes,redaction_count,reserved_tokens,reserved_cost_micros,max_attempts,status,expires_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'RUNNING',now()+interval '5 minutes') RETURNING id::text`,
			input.TenantID, input.ActorID, input.Purpose, input.ResourceType, input.ResourceID,
			input.Provider, input.Model, input.PromptVersion, input.InputHash, input.InputBytes,
			input.RedactionCount, input.ReservedTokens, input.ReservedCostMicros, input.MaxAttempts).
			Scan(&record.ID)
	})
	return record, err
}

// Complete 将一个仍在运行的请求一次性收口为成功，并保存可信 Provider 计量。
func (s *PostgresStore) Complete(ctx context.Context, tenantID, requestID string, completion CompletionRecord) error {
	if uuid.Validate(tenantID) != nil || uuid.Validate(requestID) != nil {
		return ErrRequestNotFound
	}
	completion, err := normalizeCompletionRecord(completion)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// 与 Start 共用策略行锁，使实际结算和新的配额预留按租户串行观察。
		var policyTenant string
		if err := tx.QueryRow(ctx, `SELECT tenant_id::text FROM platform.ai_tenant_policies
			WHERE tenant_id=$1 FOR UPDATE`, tenantID).Scan(&policyTenant); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTenantAIForbidden
			}
			return err
		}
		if err := lockRunningRequest(ctx, tx, tenantID, requestID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.ai_requests SET
			status='SUCCEEDED',provider_model=model_name,provider_request_id=$1,
			finish_reason=$2,attempts=$3,prompt_tokens=$4,completion_tokens=$5,total_tokens=$6,
			cost_micros=$7,latency_ms=$8,completed_at=now()
			WHERE tenant_id=$9 AND id=$10`, completion.ProviderRequestID,
			completion.FinishReason, completion.Attempts, completion.PromptTokens,
			completion.CompletionTokens, completion.TotalTokens, completion.CostMicros,
			completion.LatencyMS, tenantID, requestID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRequestConflict
		}
		return nil
	})
}

// Fail 将一个仍在运行的请求一次性收口为失败，只保存稳定错误码而不保存错误正文。
func (s *PostgresStore) Fail(ctx context.Context, tenantID, requestID string, failure FailureRecord) error {
	if uuid.Validate(tenantID) != nil || uuid.Validate(requestID) != nil {
		return ErrRequestNotFound
	}
	failure, err := normalizeFailureRecord(failure)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockRunningRequest(ctx, tx, tenantID, requestID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.ai_requests SET
			status=CASE WHEN $2='AI_REQUEST_CANCELED' THEN 'CANCELED' ELSE 'FAILED' END,
			attempts=$1,error_code=$2,latency_ms=$3,completed_at=now()
			WHERE tenant_id=$4 AND id=$5`, failure.Attempts, failure.ErrorCode, failure.LatencyMS, tenantID, requestID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRequestConflict
		}
		return nil
	})
}

// lockRunningRequest 先锁定请求并区分“不可见/不存在”和“已经终结”两类错误。
func lockRunningRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM platform.ai_requests WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, requestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRequestNotFound
	}
	if err != nil {
		return err
	}
	if status != "RUNNING" {
		return ErrRequestConflict
	}
	return nil
}

func normalizeStartRequest(input StartRequest) (StartRequest, error) {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.Purpose = strings.ToUpper(strings.TrimSpace(input.Purpose))
	input.PromptVersion = strings.TrimSpace(input.PromptVersion)
	input.Provider = strings.TrimSpace(input.Provider)
	input.Model = strings.TrimSpace(input.Model)
	input.InputHash = strings.TrimSpace(input.InputHash)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	if uuid.Validate(input.TenantID) != nil || uuid.Validate(input.ActorID) != nil || !allowedPurpose(input.Purpose) {
		return StartRequest{}, ErrTenantAIForbidden
	}
	if !boundedRequired(input.Provider, 128) || !boundedRequired(input.Model, 256) || !boundedRequired(input.PromptVersion, 128) || !validSHA256(input.InputHash) {
		return StartRequest{}, fmt.Errorf("%w: Provider、模型、提示词版本或输入摘要无效", ErrInvalidInvocation)
	}
	pairedResource := input.ResourceType == "" && input.ResourceID == "" || input.ResourceType != "" && input.ResourceID != ""
	if !pairedResource || !boundedOptional(input.ResourceType, 64) || !boundedOptional(input.ResourceID, 256) {
		return StartRequest{}, fmt.Errorf("%w: 资源引用必须成对出现且不能超过长度上限", ErrInvalidInvocation)
	}
	if input.InputBytes <= 0 || input.RedactionCount < 0 || int64(input.RedactionCount) > maxPostgresInteger || input.ReservedTokens <= 0 || input.ReservedCostMicros < 0 || input.MaxAttempts < 1 || input.MaxAttempts > 5 {
		return StartRequest{}, fmt.Errorf("%w: 输入计量或配额预留无效", ErrInvalidInvocation)
	}
	return input, nil
}

func normalizeCompletionRecord(input CompletionRecord) (CompletionRecord, error) {
	input.ProviderModel = strings.TrimSpace(input.ProviderModel)
	input.ProviderRequestID = strings.TrimSpace(input.ProviderRequestID)
	input.FinishReason = strings.TrimSpace(input.FinishReason)
	validRequestID := input.ProviderRequestID == "" || validSHA256(input.ProviderRequestID)
	if !boundedOptional(input.ProviderModel, 256) || !validRequestID || !validFinishReason(input.FinishReason) {
		return CompletionRecord{}, fmt.Errorf("%w: Provider 完成摘要超过长度上限", ErrInvalidInvocation)
	}
	if input.Attempts < 1 || input.Attempts > 5 || input.PromptTokens <= 0 || input.CompletionTokens <= 0 || input.TotalTokens <= 0 || input.CostMicros < 0 || input.LatencyMS < 0 {
		return CompletionRecord{}, fmt.Errorf("%w: AI 完成计量无效", ErrInvalidInvocation)
	}
	prompt, completion, total := int64(input.PromptTokens), int64(input.CompletionTokens), int64(input.TotalTokens)
	if prompt > maxPostgresInteger || completion > maxPostgresInteger || total > maxPostgresInteger || prompt > maxPostgresInteger-completion || total < prompt+completion {
		return CompletionRecord{}, fmt.Errorf("%w: Token 计量超出 PostgreSQL 审计边界", ErrInvalidInvocation)
	}
	return input, nil
}

func validFinishReason(value string) bool {
	switch value {
	case "", "stop", "length", "content_filter", "tool_calls", "function_call", "other":
		return true
	default:
		return false
	}
}

func normalizeFailureRecord(input FailureRecord) (FailureRecord, error) {
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if input.Attempts < 1 || input.Attempts > 5 || input.LatencyMS < 0 || !stableAIErrorCodePattern.MatchString(input.ErrorCode) {
		return FailureRecord{}, fmt.Errorf("%w: AI 失败摘要无效", ErrInvalidInvocation)
	}
	return input, nil
}

func containsPurpose(values []string, purpose string) bool {
	for _, value := range values {
		if value == purpose {
			return true
		}
	}
	return false
}

// exceedsQuota 使用减法比较避免 used+reserved 在极端输入下发生整数溢出。
func exceedsQuota(used, reserved, limit int64) bool {
	return used < 0 || reserved < 0 || limit < 0 || reserved > limit || used > limit-reserved
}

func boundedRequired(value string, maximum int) bool {
	return value != "" && boundedOptional(value, maximum)
}

func boundedOptional(value string, maximum int) bool {
	if utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
