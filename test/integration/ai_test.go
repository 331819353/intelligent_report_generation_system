//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestAIOrchestrationTenantPolicyQuotaAndAuditLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "ai-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "ai-foreign-"+suffix)
	var actorID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'AI tester','integration-hash') RETURNING id::text`, tenantID, "ai-"+suffix+"@it.test").Scan(&actorID)
	})
	if err != nil {
		t.Fatal(err)
	}

	store := aiplatform.NewPostgresStore(pool)
	start := aiplatform.StartRequest{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeReportGeneration,
		PromptVersion: "report-v1", Provider: "test-provider", Model: "test-model",
		InputHash: strings.Repeat("a", 64), ResourceType: "REPORT", ResourceID: "report-1",
		InputBytes: 128, RedactionCount: 1, ReservedTokens: 1000, ReservedCostMicros: 500, MaxAttempts: 2,
	}
	// 迁移后创建的新租户默认禁用，必须由可信管理流程显式授权。
	if _, err := store.Start(ctx, start); !errors.Is(err, aiplatform.ErrTenantAIForbidden) {
		t.Fatalf("disabled tenant Start error=%v", err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.ai_tenant_policies SET enabled=true,
			allowed_purposes=ARRAY['REPORT_GENERATION']::text[],max_requests_per_day=10,
			max_tokens_per_month=3200,max_cost_micros_per_month=10000 WHERE tenant_id=$1`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Start(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, tenantID, first.ID, aiplatform.CompletionRecord{
		ProviderModel: "ignored-upstream-model", ProviderRequestID: strings.Repeat("e", 64), FinishReason: "stop",
		Attempts: 2, PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200, CostMicros: 40, LatencyMS: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, tenantID, first.ID, aiplatform.CompletionRecord{Attempts: 1, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}); !errors.Is(err, aiplatform.ErrRequestConflict) {
		t.Fatalf("terminal replay error=%v", err)
	}

	start.InputHash = strings.Repeat("b", 64)
	second, err := store.Start(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(ctx, tenantID, second.ID, aiplatform.FailureRecord{Attempts: 1, ErrorCode: "AI_PROVIDER_TIMEOUT", LatencyMS: 80}); err != nil {
		t.Fatal(err)
	}
	start.InputHash = strings.Repeat("c", 64)
	third, err := store.Start(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(ctx, tenantID, third.ID, aiplatform.FailureRecord{Attempts: 1, ErrorCode: string(aiplatform.ErrorCodeCanceled), LatencyMS: 10}); err != nil {
		t.Fatal(err)
	}
	start.InputHash = strings.Repeat("d", 64)
	if _, err := store.Start(ctx, start); !errors.Is(err, aiplatform.ErrQuotaExceeded) {
		t.Fatalf("monthly quota error=%v", err)
	}
	if err := store.Fail(ctx, foreignTenantID, second.ID, aiplatform.FailureRecord{Attempts: 1, ErrorCode: "AI_PROVIDER_TIMEOUT"}); !errors.Is(err, aiplatform.ErrRequestNotFound) {
		t.Fatalf("cross-tenant terminal update error=%v", err)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var succeeded, failed, canceled int
		if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER(WHERE status='SUCCEEDED'),count(*) FILTER(WHERE status='FAILED'),
			count(*) FILTER(WHERE status='CANCELED')
			FROM platform.ai_requests WHERE tenant_id=$1`, tenantID).Scan(&succeeded, &failed, &canceled); err != nil {
			return err
		}
		if succeeded != 1 || failed != 1 || canceled != 1 {
			return fmt.Errorf("terminal audit succeeded=%d failed=%d canceled=%d", succeeded, failed, canceled)
		}
		var inputHash, providerModel, errorCode string
		var promptTokens, completionTokens, totalTokens int
		if err := tx.QueryRow(ctx, `SELECT input_hash,provider_model,error_code,prompt_tokens,completion_tokens,total_tokens
			FROM platform.ai_requests WHERE id=$1`, first.ID).Scan(&inputHash, &providerModel, &errorCode, &promptTokens, &completionTokens, &totalTokens); err != nil {
			return err
		}
		if inputHash != strings.Repeat("a", 64) || providerModel != "test-model" || errorCode != "" || promptTokens != 1000 || completionTokens != 200 || totalTokens != 1200 {
			return fmt.Errorf("unexpected success audit digest/model/usage")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetricAuthoringUsesGeneralTenantAIEnablement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metric-ai-policy-it-"+suffix)
	var actorID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash)
			VALUES($1,$2,'Metric AI tester','integration-hash') RETURNING id::text`, tenantID, "metric-ai-policy-"+suffix+"@it.test").Scan(&actorID)
	})
	if err != nil {
		t.Fatal(err)
	}

	store := aiplatform.NewPostgresStore(pool)
	start := aiplatform.StartRequest{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetricAuthoring,
		PromptVersion: "metric-authoring-v2", Provider: "test-provider", Model: "test-model",
		InputHash: strings.Repeat("f", 64), ResourceType: "METRIC_AUTHORING", ResourceID: "context-1",
		InputBytes: 128, ReservedTokens: 100, ReservedCostMicros: 10, MaxAttempts: 1,
	}
	if _, err := store.Start(ctx, start); !errors.Is(err, aiplatform.ErrTenantAIForbidden) {
		t.Fatalf("disabled tenant metric authoring error=%v", err)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.ai_tenant_policies SET enabled=true,
			allowed_purposes=ARRAY['METADATA_COMPLETION']::text[],max_requests_per_day=1,
			max_tokens_per_month=1000,max_cost_micros_per_month=1000 WHERE tenant_id=$1`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := store.Start(ctx, start)
	if err != nil {
		t.Fatalf("metric authoring must not require a purpose opt-in: %v", err)
	}
	if err := store.Fail(ctx, tenantID, record.ID, aiplatform.FailureRecord{Attempts: 1, ErrorCode: "AI_PROVIDER_TIMEOUT"}); err != nil {
		t.Fatal(err)
	}

	var auditedPurpose, auditedStatus string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT purpose,status FROM platform.ai_requests WHERE id=$1`, record.ID).Scan(&auditedPurpose, &auditedStatus)
	})
	if err != nil {
		t.Fatal(err)
	}
	if auditedPurpose != aiplatform.PurposeMetricAuthoring || auditedStatus != "FAILED" {
		t.Fatalf("unexpected metric authoring audit purpose=%q status=%q", auditedPurpose, auditedStatus)
	}
	start.InputHash = strings.Repeat("e", 64)
	if _, err := store.Start(ctx, start); !errors.Is(err, aiplatform.ErrQuotaExceeded) {
		t.Fatalf("metric authoring must retain common quota enforcement: %v", err)
	}
}
