package registry

import (
	"context"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func queryReadScope(t *testing.T) (askdata.PolicyScope, askdata.ID) {
	t.Helper()
	domainID := askdata.ID("11111111-1111-4111-8111-111111111111")
	release := askdata.ReleaseRef{
		ReleaseID:   "22222222-2222-4222-8222-222222222222",
		ContentHash: askdata.HashBytes([]byte("release")),
	}
	scope, err := askdata.NewPolicyScope(
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		[]askdata.ID{domainID},
		[]askdata.ID{"55555555-5555-4555-8555-555555555555"},
		release,
	)
	if err != nil {
		t.Fatalf("build policy scope: %v", err)
	}
	return scope, domainID
}

// 读接口必须拒绝越出 PolicyScope 的领域，否则调用方可以用一个合法会话
// 读到自己无权进入的业务领域的语义资产。
func TestQueryReaderRejectsDomainOutsidePolicyScope(t *testing.T) {
	reader := NewQueryReader(nil)
	scope, domainID := queryReadScope(t)

	// 未配置连接池时同样必须报错，而不是返回空结果冒充“没有数据”。
	if _, err := reader.Contracts(context.Background(), scope, domainID, nil); err == nil {
		t.Fatal("unconfigured reader must fail instead of returning an empty result")
	}

	configured := &QueryReader{pool: nil}
	foreign := askdata.ID("66666666-6666-4666-8666-666666666666")
	for name, run := range map[string]func() error{
		"contracts": func() error {
			_, err := configured.Contracts(context.Background(), scope, foreign, nil)
			return err
		},
		"members": func() error {
			_, err := configured.DimensionMembers(context.Background(), scope, foreign,
				"77777777-7777-4777-8777-777777777777", "华东", 10)
			return err
		},
		"examples": func() error {
			_, err := configured.CertifiedExamples(context.Background(), scope, foreign, "销售额", 10)
			return err
		},
		"quality": func() error {
			_, err := configured.DataQuality(context.Background(), scope, foreign, nil)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatalf("%s: foreign domain must be rejected", name)
		}
	}
}

// 未固定 Release 的 scope 不能读取任何语义对象：问数只允许引用已发布版本。
func TestQueryReaderRequiresAPinnedRelease(t *testing.T) {
	reader := &QueryReader{}
	domainID := askdata.ID("11111111-1111-4111-8111-111111111111")
	scope := askdata.PolicyScope{
		TenantID:  "33333333-3333-4333-8333-333333333333",
		ActorID:   "44444444-4444-4444-8444-444444444444",
		DomainIDs: []askdata.ID{domainID},
	}
	if err := reader.validate(scope, domainID); err == nil {
		t.Fatal("a scope without a pinned release must be rejected")
	}
}

// 对象 ID 集合必须规范化：非法 UUID 拒绝、重复去重、超量拒绝，
// 避免把未经校验的标识拼进查询或放大一次读取的成本。
func TestCanonicalIDSetNormalisesAndBoundsInput(t *testing.T) {
	valid := "88888888-8888-4888-8888-888888888888"
	ids, err := canonicalIDSet([]string{valid, " " + valid + " ", "", valid})
	if err != nil || len(ids) != 1 || ids[0] != valid {
		t.Fatalf("expected one deduplicated ID, got %#v (%v)", ids, err)
	}
	if _, err := canonicalIDSet([]string{"not-a-uuid"}); err == nil {
		t.Fatal("non-canonical identifier must be rejected")
	}
	oversized := make([]string, maxQueryReadIDs+1)
	for index := range oversized {
		oversized[index] = valid
	}
	if _, err := canonicalIDSet(oversized); err == nil {
		t.Fatal("oversized identifier set must be rejected")
	}
}

// 成员查询的边界必须在进入数据库前拒绝：limit 越界与超长 mention
// 都不能变成一次昂贵或可放大的检索。
func TestDimensionMembersBoundsInput(t *testing.T) {
	reader := &QueryReader{}
	scope, domainID := queryReadScope(t)
	dimension := "77777777-7777-4777-8777-777777777777"
	for name, run := range map[string]func() error{
		"zero limit": func() error {
			_, err := reader.DimensionMembers(context.Background(), scope, domainID, dimension, "华东", 0)
			return err
		},
		"oversized limit": func() error {
			_, err := reader.DimensionMembers(context.Background(), scope, domainID, dimension, "华东", 101)
			return err
		},
		"non-canonical dimension": func() error {
			_, err := reader.DimensionMembers(context.Background(), scope, domainID, "dimension", "华东", 10)
			return err
		},
		"oversized mention": func() error {
			_, err := reader.DimensionMembers(context.Background(), scope, domainID, dimension,
				strings.Repeat("值", 600), 10)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// 认证问法的 limit 同样有界，避免一次取回整个问法库。
func TestCertifiedExamplesBoundsInput(t *testing.T) {
	reader := &QueryReader{}
	scope, domainID := queryReadScope(t)
	if _, err := reader.CertifiedExamples(context.Background(), scope, domainID, "销售额", 0); err == nil {
		t.Fatal("zero limit must be rejected")
	}
	if _, err := reader.CertifiedExamples(context.Background(), scope, domainID, "销售额", 51); err == nil {
		t.Fatal("oversized limit must be rejected")
	}
	if _, err := reader.CertifiedExamples(context.Background(), scope, domainID,
		strings.Repeat("问", 1100), 10); err == nil {
		t.Fatal("oversized question summary must be rejected")
	}
}

// “没有质量规则”必须是 UNKNOWN，绝不能被当成 PASSED——没有规则跑过
// 不是数据可信的证据。当前 quality_rules 没有写入通道，这条不变量决定了
// 问数在质量未知时的降级表述，因此必须显式锁住。
func TestQualityStatusNeverReportsUncheckedDataAsPassing(t *testing.T) {
	if got := qualityStatusFor(nil); got != "UNKNOWN" {
		t.Fatalf("no rules must be UNKNOWN, got %q", got)
	}
	if got := qualityStatusFor([]QualityRuleRow{}); got != "UNKNOWN" {
		t.Fatalf("empty rule set must be UNKNOWN, got %q", got)
	}
	if got := qualityStatusFor([]QualityRuleRow{{Code: "A", Passed: true}, {Code: "B", Passed: false}}); got != "PARTIAL" {
		t.Fatalf("a failing rule must be PARTIAL, got %q", got)
	}
	if got := qualityStatusFor([]QualityRuleRow{{Code: "A", Passed: true}}); got != "PASSED" {
		t.Fatalf("all rules passing must be PASSED, got %q", got)
	}
}
