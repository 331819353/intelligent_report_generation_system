package observability

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

// OperationalSnapshot is a privacy-safe control-plane view. It contains only
// aggregate counters and stable error codes; prompts, answers and row data are
// never copied into the platform-management surface.
type OperationalSnapshot struct {
	GeneratedAt  time.Time          `json:"generatedAt"`
	Window       string             `json:"window"`
	Health       string             `json:"health"`
	AI           AIUsage            `json:"ai"`
	AskData      AskDataUsage       `json:"askData"`
	Queues       []QueueHealth      `json:"queues"`
	FailureCodes []FailureCodeCount `json:"failureCodes"`
	Purposes     []PurposeUsage     `json:"purposes"`
}

type AIUsage struct {
	Enabled                bool    `json:"enabled"`
	RequestsToday          int64   `json:"requestsToday"`
	RequestsDailyLimit     int64   `json:"requestsDailyLimit"`
	RequestUtilization     float64 `json:"requestUtilization"`
	TokensThisMonth        int64   `json:"tokensThisMonth"`
	TokensMonthlyLimit     int64   `json:"tokensMonthlyLimit"`
	TokenUtilization       float64 `json:"tokenUtilization"`
	CostMicrosThisMonth    int64   `json:"costMicrosThisMonth"`
	CostMicrosMonthlyLimit int64   `json:"costMicrosMonthlyLimit"`
	CostUtilization        float64 `json:"costUtilization"`
	RequestsInWindow       int64   `json:"requestsInWindow"`
	SucceededInWindow      int64   `json:"succeededInWindow"`
	FailedInWindow         int64   `json:"failedInWindow"`
	RunningInWindow        int64   `json:"runningInWindow"`
	SuccessRate            float64 `json:"successRate"`
	AverageLatencyMs       int64   `json:"averageLatencyMs"`
	P95LatencyMs           int64   `json:"p95LatencyMs"`
}

type AskDataUsage struct {
	RunsInWindow          int64   `json:"runsInWindow"`
	AnsweredInWindow      int64   `json:"answeredInWindow"`
	BlockedInWindow       int64   `json:"blockedInWindow"`
	ClarificationInWindow int64   `json:"clarificationInWindow"`
	ActiveInWindow        int64   `json:"activeInWindow"`
	AnswerRate            float64 `json:"answerRate"`
	AverageDurationMs     int64   `json:"averageDurationMs"`
	P95DurationMs         int64   `json:"p95DurationMs"`
}

type QueueHealth struct {
	Code                 string `json:"code"`
	Name                 string `json:"name"`
	Pending              int64  `json:"pending"`
	Running              int64  `json:"running"`
	Failed               int64  `json:"failed"`
	OldestPendingSeconds int64  `json:"oldestPendingSeconds"`
	Status               string `json:"status"`
}

type FailureCodeCount struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Count  int64  `json:"count"`
}

type PurposeUsage struct {
	Purpose    string `json:"purpose"`
	Count      int64  `json:"count"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"costMicros"`
}

// Store reads operational counters after the HTTP layer has completed the
// platform-administrator authorization. SYSTEM mode is required because queue
// rows are intentionally invisible to ordinary business-domain sessions.
type Store struct{ pool *pgxpool.Pool }

func NewOperationalStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Snapshot(
	ctx context.Context, tenantID, windowLabel string, since time.Time,
) (snapshot OperationalSnapshot, err error) {
	snapshot = OperationalSnapshot{
		GeneratedAt: time.Now().UTC(), Window: windowLabel,
		Queues: []QueueHealth{}, FailureCodes: []FailureCodeCount{}, Purposes: []PurposeUsage{},
	}
	err = database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		if err := readAIUsage(ctx, tx, tenantID, since, &snapshot.AI); err != nil {
			return err
		}
		if err := readAskDataUsage(ctx, tx, tenantID, since, &snapshot.AskData); err != nil {
			return err
		}
		queues, err := readQueueHealth(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		snapshot.Queues = queues
		failures, err := readFailureCodes(ctx, tx, tenantID, since)
		if err != nil {
			return err
		}
		snapshot.FailureCodes = failures
		purposes, err := readPurposeUsage(ctx, tx, tenantID, since)
		if err != nil {
			return err
		}
		snapshot.Purposes = purposes
		return nil
	})
	if err != nil {
		return OperationalSnapshot{}, err
	}
	snapshot.Health = snapshotHealth(snapshot)
	return snapshot, nil
}

func readAIUsage(ctx context.Context, tx pgx.Tx, tenantID string, since time.Time, out *AIUsage) error {
	var averageLatency, p95Latency float64
	err := tx.QueryRow(ctx, `WITH policy AS (
		SELECT enabled,max_requests_per_day,max_tokens_per_month,max_cost_micros_per_month
		FROM platform.ai_tenant_policies WHERE tenant_id=$1::uuid
	), usage AS (
		SELECT
			count(*) FILTER (WHERE created_at>=date_trunc('day',now())) requests_today,
			COALESCE(sum(accounted_tokens) FILTER (WHERE created_at>=date_trunc('month',now())),0) tokens_month,
			COALESCE(sum(accounted_cost_micros) FILTER (WHERE created_at>=date_trunc('month',now())),0) cost_month,
			count(*) FILTER (WHERE created_at >= $2) requests_window,
			count(*) FILTER (WHERE created_at >= $2 AND status='SUCCEEDED') succeeded_window,
			count(*) FILTER (WHERE created_at >= $2 AND status='FAILED') failed_window,
			count(*) FILTER (WHERE created_at >= $2 AND status='RUNNING') running_window,
			COALESCE(avg(latency_ms) FILTER (WHERE created_at >= $2 AND status='SUCCEEDED'),0) avg_latency,
			COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
				FILTER (WHERE created_at >= $2 AND status='SUCCEEDED'),0) p95_latency
		FROM platform.ai_requests WHERE tenant_id=$1::uuid
	)
	SELECT policy.enabled,policy.max_requests_per_day,policy.max_tokens_per_month,
		policy.max_cost_micros_per_month,usage.requests_today,usage.tokens_month,
		usage.cost_month,usage.requests_window,usage.succeeded_window,
		usage.failed_window,usage.running_window,usage.avg_latency,usage.p95_latency
	FROM policy CROSS JOIN usage`, tenantID, since).Scan(
		&out.Enabled, &out.RequestsDailyLimit, &out.TokensMonthlyLimit,
		&out.CostMicrosMonthlyLimit, &out.RequestsToday, &out.TokensThisMonth,
		&out.CostMicrosThisMonth, &out.RequestsInWindow, &out.SucceededInWindow,
		&out.FailedInWindow, &out.RunningInWindow, &averageLatency, &p95Latency,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	out.RequestUtilization = ratio(out.RequestsToday, out.RequestsDailyLimit)
	out.TokenUtilization = ratio(out.TokensThisMonth, out.TokensMonthlyLimit)
	out.CostUtilization = ratio(out.CostMicrosThisMonth, out.CostMicrosMonthlyLimit)
	out.SuccessRate = ratio(out.SucceededInWindow, out.SucceededInWindow+out.FailedInWindow)
	out.AverageLatencyMs = int64(math.Round(averageLatency))
	out.P95LatencyMs = int64(math.Round(p95Latency))
	return nil
}

func readAskDataUsage(ctx context.Context, tx pgx.Tx, tenantID string, since time.Time, out *AskDataUsage) error {
	var averageDuration, p95Duration float64
	err := tx.QueryRow(ctx, `SELECT
		count(*),
		count(*) FILTER (WHERE current_state='ANSWERED'),
		count(*) FILTER (WHERE current_state='BLOCKED'),
		count(*) FILTER (WHERE current_state='CLARIFICATION_REQUIRED'),
		count(*) FILTER (WHERE current_state NOT IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED')),
		COALESCE(avg(elapsed_ms) FILTER (WHERE current_state IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED')),0),
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY elapsed_ms)
			FILTER (WHERE current_state IN('ANSWERED','BLOCKED','CLARIFICATION_REQUIRED')),0)
	FROM askdata.question_runs WHERE tenant_id=$1::uuid AND created_at >= $2`, tenantID, since).Scan(
		&out.RunsInWindow, &out.AnsweredInWindow, &out.BlockedInWindow,
		&out.ClarificationInWindow, &out.ActiveInWindow, &averageDuration, &p95Duration,
	)
	if err != nil {
		return err
	}
	out.AnswerRate = ratio(out.AnsweredInWindow, out.AnsweredInWindow+out.BlockedInWindow+out.ClarificationInWindow)
	out.AverageDurationMs = int64(math.Round(averageDuration))
	out.P95DurationMs = int64(math.Round(p95Duration))
	return nil
}

func readQueueHealth(ctx context.Context, tx pgx.Tx, tenantID string) ([]QueueHealth, error) {
	rows, err := tx.Query(ctx, `SELECT code,name,pending,running,failed,oldest_pending_seconds FROM (
		SELECT 'SEMANTIC_EMBEDDING' code,'语义索引' name,
			count(*) FILTER (WHERE status='PENDING') pending,
			count(*) FILTER (WHERE status='RUNNING') running,
			count(*) FILTER (WHERE status='FAILED') failed,
			COALESCE(EXTRACT(EPOCH FROM now()-min(created_at) FILTER (WHERE status='PENDING')),0)::bigint oldest_pending_seconds
		FROM askdata.embedding_outbox WHERE tenant_id=$1::uuid
		UNION ALL SELECT 'REPORT_EXTRACTION','报表资产提取',
			count(*) FILTER (WHERE state='PENDING'),count(*) FILTER (WHERE state='RUNNING'),
			count(*) FILTER (WHERE state='FAILED'),
			COALESCE(EXTRACT(EPOCH FROM now()-min(created_at) FILTER (WHERE state='PENDING')),0)::bigint
		FROM askdata.report_asset_extraction_outbox WHERE tenant_id=$1::uuid
		UNION ALL SELECT 'REPORT_PROJECTION','报表资产投影',
			count(*) FILTER (WHERE state='PENDING'),count(*) FILTER (WHERE state='RUNNING'),
			count(*) FILTER (WHERE state='FAILED'),
			COALESCE(EXTRACT(EPOCH FROM now()-min(created_at) FILTER (WHERE state='PENDING')),0)::bigint
		FROM askdata.report_asset_projection_outbox WHERE tenant_id=$1::uuid
		UNION ALL SELECT 'REPORT_WRITEBACK','问数写回报表',
			count(*) FILTER (WHERE state='PENDING'),count(*) FILTER (WHERE state='RUNNING'),
			count(*) FILTER (WHERE state='FAILED'),
			COALESCE(EXTRACT(EPOCH FROM now()-min(created_at) FILTER (WHERE state='PENDING')),0)::bigint
		FROM askdata.add_to_report_outbox WHERE tenant_id=$1::uuid
	) queue ORDER BY code`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]QueueHealth, 0, 4)
	for rows.Next() {
		var item QueueHealth
		if err := rows.Scan(&item.Code, &item.Name, &item.Pending, &item.Running, &item.Failed, &item.OldestPendingSeconds); err != nil {
			return nil, err
		}
		item.Status = queueStatus(item)
		items = append(items, item)
	}
	return items, rows.Err()
}

func readFailureCodes(ctx context.Context, tx pgx.Tx, tenantID string, since time.Time) ([]FailureCodeCount, error) {
	rows, err := tx.Query(ctx, `SELECT source,code,count(*) FROM (
		SELECT 'AI' source,COALESCE(NULLIF(error_code,''),'UNKNOWN') code
		FROM platform.ai_requests WHERE tenant_id=$1::uuid AND created_at >= $2 AND status='FAILED'
		UNION ALL
		SELECT 'ASK_DATA',COALESCE(NULLIF(completion_code,''),'UNKNOWN')
		FROM askdata.question_runs WHERE tenant_id=$1::uuid AND created_at >= $2 AND current_state='BLOCKED'
	) failures GROUP BY source,code ORDER BY count(*) DESC,source,code LIMIT 8`, tenantID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FailureCodeCount, 0)
	for rows.Next() {
		var item FailureCodeCount
		if err := rows.Scan(&item.Source, &item.Code, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func readPurposeUsage(ctx context.Context, tx pgx.Tx, tenantID string, since time.Time) ([]PurposeUsage, error) {
	rows, err := tx.Query(ctx, `SELECT purpose,count(*),COALESCE(sum(accounted_tokens),0),COALESCE(sum(accounted_cost_micros),0)
		FROM platform.ai_requests WHERE tenant_id=$1::uuid AND created_at >= $2
		GROUP BY purpose ORDER BY count(*) DESC,purpose`, tenantID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PurposeUsage, 0)
	for rows.Next() {
		var item PurposeUsage
		if err := rows.Scan(&item.Purpose, &item.Count, &item.Tokens, &item.CostMicros); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func ratio(used, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return math.Round(float64(used)/float64(limit)*1000) / 10
}

func queueStatus(item QueueHealth) string {
	if item.Failed > 0 || item.OldestPendingSeconds >= 900 {
		return "CRITICAL"
	}
	if item.Pending > 0 || item.Running > 0 || item.OldestPendingSeconds >= 300 {
		return "ATTENTION"
	}
	return "HEALTHY"
}

func snapshotHealth(snapshot OperationalSnapshot) string {
	if snapshot.AI.RequestUtilization >= 90 || snapshot.AI.TokenUtilization >= 90 || snapshot.AI.CostUtilization >= 90 || snapshot.AI.FailedInWindow > 0 || snapshot.AskData.BlockedInWindow > 0 {
		return "CRITICAL"
	}
	for _, queue := range snapshot.Queues {
		if queue.Status == "CRITICAL" {
			return "CRITICAL"
		}
	}
	if snapshot.AI.RequestUtilization >= 75 || snapshot.AI.TokenUtilization >= 75 || snapshot.AI.CostUtilization >= 75 || snapshot.AskData.ActiveInWindow > 0 {
		return "ATTENTION"
	}
	for _, queue := range snapshot.Queues {
		if queue.Status == "ATTENTION" {
			return "ATTENTION"
		}
	}
	return "HEALTHY"
}
