package observability

import (
	"math"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestEvaluateQuotaUsesStrictestOfFourScopes(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	request := quotaRequest(now)
	request.Reserve = QuotaUsage{LLMTokens: 5, Runs: 1, CostCents: 2}
	snapshots := []QuotaSnapshot{
		quotaSnapshot(QuotaScopeTenant, request.TenantID, QuotaPeriodMonth, 1000, 100, 100, QuotaUsage{LLMTokens: 100, Runs: 10, CostCents: 10}, now),
		quotaSnapshot(QuotaScopeDomain, request.DomainID, QuotaPeriodDay, 100, 100, 100, QuotaUsage{LLMTokens: 90, Runs: 10, CostCents: 10}, now),
		quotaSnapshot(QuotaScopeUser, request.ActorID, QuotaPeriodDay, 1000, 10, 100, QuotaUsage{LLMTokens: 10, Runs: 9, CostCents: 10}, now),
		quotaSnapshot(QuotaScopeRun, request.RunID, QuotaPeriodRun, 1000, 100, 3, QuotaUsage{CostCents: 2}, now),
	}

	decision, err := EvaluateQuota(request, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != QuotaRunCostExceeded || decision.Allowed || !decision.RequireClarification {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if len(decision.Limiters) != 3 || decision.Limiters[0].Scope != QuotaScopeRun {
		t.Fatalf("strictest limiter ordering is wrong: %+v", decision.Limiters)
	}
}

func TestEvaluateQuotaWarningAndHardLimit(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	request := quotaRequest(now)
	snapshot := quotaSnapshot(QuotaScopeTenant, request.TenantID, QuotaPeriodMonth, 100, 100, 100, QuotaUsage{LLMTokens: 79}, now)
	request.Reserve.LLMTokens = 1

	decision, err := EvaluateQuota(request, []QuotaSnapshot{snapshot})
	if err != nil || decision.Status != QuotaWarning || !decision.Allowed || len(decision.Limiters) != 1 {
		t.Fatalf("80 percent must warn: decision=%+v err=%v", decision, err)
	}
	request.Reserve.LLMTokens = 21
	decision, err = EvaluateQuota(request, []QuotaSnapshot{snapshot})
	if err != nil || decision.Status != QuotaExceeded || decision.Allowed {
		t.Fatalf("100 percent must reject: decision=%+v err=%v", decision, err)
	}
}

func TestEvaluateQuotaCertifiedFastPathAndRunCostBoundary(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	request := quotaRequest(now)
	request.CertifiedFastPath = true
	request.Reserve.Runs = 1
	tenant := quotaSnapshot(QuotaScopeTenant, request.TenantID, QuotaPeriodDay, 100, 10, 100, QuotaUsage{Runs: 10}, now)

	decision, err := EvaluateQuota(request, []QuotaSnapshot{tenant})
	if err != nil || !decision.Allowed || !decision.FastPathBypass || decision.Status != QuotaWarning {
		t.Fatalf("certified fast path must bypass aggregate exhaustion: decision=%+v err=%v", decision, err)
	}
	run := quotaSnapshot(QuotaScopeRun, request.RunID, QuotaPeriodRun, 100, 100, 5, QuotaUsage{CostCents: 4}, now)
	request.Reserve.CostCents = 1
	decision, err = EvaluateQuota(request, []QuotaSnapshot{tenant, run})
	if err != nil || decision.Allowed || !decision.RequireClarification || decision.Status != QuotaRunCostExceeded {
		t.Fatalf("run cost ceiling must fail closed even for fast path: decision=%+v err=%v", decision, err)
	}
}

func TestEvaluateQuotaIsOverflowSafeAndTenantBound(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	request := quotaRequest(now)
	request.Reserve.LLMTokens = math.MaxInt64
	snapshot := quotaSnapshot(QuotaScopeTenant, request.TenantID, QuotaPeriodMonth, math.MaxInt64, 1, 1, QuotaUsage{}, now)
	decision, err := EvaluateQuota(request, []QuotaSnapshot{snapshot})
	if err != nil || decision.Allowed || decision.Status != QuotaExceeded {
		t.Fatalf("overflow-sized reservation must reject deterministically: decision=%+v err=%v", decision, err)
	}
	snapshot.ScopeID = askdata.ID("00000000-0000-4000-8000-000000000099")
	if _, err = EvaluateQuota(request, []QuotaSnapshot{snapshot}); err == nil {
		t.Fatal("cross-tenant quota snapshot must be rejected")
	}
}

func quotaRequest(now time.Time) QuotaCheckRequest {
	return QuotaCheckRequest{
		TenantID: askdata.ID("00000000-0000-4000-8000-000000000001"),
		DomainID: askdata.ID("00000000-0000-4000-8000-000000000002"),
		ActorID:  askdata.ID("00000000-0000-4000-8000-000000000003"),
		RunID:    askdata.ID("00000000-0000-4000-8000-000000000004"),
		At:       now,
	}
}

func quotaSnapshot(scope QuotaScope, scopeID askdata.ID, period QuotaPeriod, tokens, runs, cost int64, usage QuotaUsage, now time.Time) QuotaSnapshot {
	return QuotaSnapshot{
		Scope: scope, ScopeID: scopeID, Period: period,
		Limits: QuotaLimits{LLMTokens: int64Pointer(tokens), Runs: int64Pointer(runs), CostCents: int64Pointer(cost)},
		Usage:  usage, ResetAt: now.Add(24 * time.Hour),
	}
}

func int64Pointer(value int64) *int64 { return &value }
