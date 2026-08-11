package orchestrator

import (
	"context"
	"testing"
	"time"
)

// 租约边界必须在进入数据库前拒绝，且未配置的存储不能表现成「没有可领取的运行」。
func TestLeaseStoreRejectsInvalidRequestsBeforeTouchingTheDatabase(t *testing.T) {
	store := NewLeaseStore(nil)
	ctx := context.Background()

	if _, _, err := store.Claim(ctx, "tenant", "worker", time.Minute); err == nil {
		t.Fatal("an unconfigured store must fail rather than report nothing claimable")
	}
	if _, err := store.ListTenantIDs(ctx); err == nil {
		t.Fatal("an unconfigured store must fail on tenant enumeration")
	}
	if _, err := store.Heartbeat(ctx, "run", "token", time.Minute); err == nil {
		t.Fatal("an unconfigured store must fail on heartbeat")
	}
	if err := store.Release(ctx, "run", "token"); err == nil {
		t.Fatal("an unconfigured store must fail on release")
	}
}

// 租约时长与身份参数越界必须在 Go 层就被拒绝，与 SQL 守卫保持同一边界，
// 避免把非法参数送到 SECURITY DEFINER 函数上。
func TestLeaseBoundsMatchTheDatabaseGuards(t *testing.T) {
	store := &LeaseStore{}
	ctx := context.Background()
	for name, lease := range map[string]time.Duration{
		"too short": 10 * time.Second,
		"too long":  20 * time.Minute,
	} {
		if _, _, err := store.Claim(ctx, "tenant", "worker", lease); err == nil {
			t.Fatalf("claim with a %s lease must be rejected", name)
		}
		if _, err := store.Heartbeat(ctx, "run", "token", lease); err == nil {
			t.Fatalf("heartbeat with a %s lease must be rejected", name)
		}
	}
	if _, _, err := store.Claim(ctx, "", "worker", time.Minute); err == nil {
		t.Fatal("claim without a tenant must be rejected")
	}
	if _, _, err := store.Claim(ctx, "tenant", "", time.Minute); err == nil {
		t.Fatal("claim without a worker identity must be rejected")
	}
}

// 恢复语义是两分的：FRESH 可以完整执行，ABANDONED 只能被终结。
// 该判定来自数据库的持久状态，调用方不得自行推断。
func TestResumeModesAreDistinctAndExplicit(t *testing.T) {
	if ResumeFresh == ResumeAbandoned {
		t.Fatal("resume modes must be distinguishable")
	}
	if string(ResumeFresh) != "FRESH" || string(ResumeAbandoned) != "ABANDONED" {
		t.Fatalf("resume modes must match the claim function contract: %q/%q",
			ResumeFresh, ResumeAbandoned)
	}
}
