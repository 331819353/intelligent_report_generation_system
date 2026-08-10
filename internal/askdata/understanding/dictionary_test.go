package understanding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

type dictionaryTestLoader struct {
	mu        sync.Mutex
	loads     int
	snapshots map[askdata.ContentHash]DictionarySnapshot
}

func (loader *dictionaryTestLoader) LoadDictionary(
	_ context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
) (DictionarySnapshot, error) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.loads++
	snapshot, exists := loader.snapshots[scope.Release.ContentHash]
	if !exists {
		return DictionarySnapshot{
			TenantID: scope.TenantID, DomainID: domainID, Release: scope.Release, Terms: []DictionaryTerm{},
		}, nil
	}
	return snapshot, nil
}

func (loader *dictionaryTestLoader) loadCount() int {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return loader.loads
}

type dictionaryTestFixture struct {
	tenantID, actorID, domainID, roleID askdata.ID
	release                             askdata.ReleaseRef
	scope                               askdata.PolicyScope
}

func newDictionaryTestFixture(t *testing.T, seed string) dictionaryTestFixture {
	t.Helper()
	fixture := dictionaryTestFixture{
		tenantID: askdata.ID(uuid.NewString()), actorID: askdata.ID(uuid.NewString()),
		domainID: askdata.ID(uuid.NewString()), roleID: askdata.ID(uuid.NewString()),
		release: askdata.ReleaseRef{
			ReleaseID: askdata.ID(uuid.NewString()), ContentHash: askdata.HashBytes([]byte(seed)),
		},
	}
	var err error
	fixture.scope, err = askdata.NewPolicyScope(
		fixture.tenantID, fixture.actorID, []askdata.ID{fixture.domainID},
		[]askdata.ID{fixture.roleID}, fixture.release,
	)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func dictionaryTermFixture(
	fixture dictionaryTestFixture,
	term string,
	priority int,
) DictionaryTerm {
	return DictionaryTerm{
		TenantID: fixture.tenantID, DomainID: fixture.domainID,
		TermVersionID: askdata.ID(uuid.NewString()), TargetVersionID: askdata.ID(uuid.NewString()),
		TargetObjectType: "METRIC", Term: term, TermType: "METRIC",
		MatchMode: DictionaryMatchExact, Priority: priority,
		ContentHash: askdata.HashBytes([]byte("term:" + term + ":" + uuid.NewString())),
	}
}

func dictionaryMatcherFixture(
	t *testing.T,
	fixture dictionaryTestFixture,
	terms []DictionaryTerm,
) (*DictionaryMatcher, *dictionaryTestLoader) {
	t.Helper()
	loader := &dictionaryTestLoader{snapshots: map[askdata.ContentHash]DictionarySnapshot{
		fixture.release.ContentHash: {
			TenantID: fixture.tenantID, DomainID: fixture.domainID,
			Release: fixture.release, Terms: terms,
		},
	}}
	matcher, err := NewDictionaryMatcher(loader, NewDictionaryCache())
	if err != nil {
		t.Fatal(err)
	}
	return matcher, loader
}

func TestDictionaryLongestPriorityAndResidualMatching(t *testing.T) {
	fixture := newDictionaryTestFixture(t, "longest")
	short := dictionaryTermFixture(fixture, "销售", 900)
	long := dictionaryTermFixture(fixture, "销售额", 100)
	duplicateLow := dictionaryTermFixture(fixture, "毛利", 100)
	duplicateHigh := dictionaryTermFixture(fixture, "毛利", 200)
	prefixCovered := dictionaryTermFixture(fixture, "销售", 100)
	prefixCovered.MatchMode = DictionaryMatchPrefix
	prefix := dictionaryTermFixture(fixture, "收入", 100)
	prefix.MatchMode = DictionaryMatchPrefix
	suffix := dictionaryTermFixture(fixture, "增长", 100)
	suffix.MatchMode = DictionaryMatchSuffix
	matcher, _ := dictionaryMatcherFixture(
		t, fixture, []DictionaryTerm{short, long, duplicateLow, duplicateHigh, prefixCovered, prefix, suffix},
	)
	result, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: fixture.scope, Question: "销售额、毛利和收入增长",
	})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if len(result.Hits) != 4 {
		t.Fatalf("hits = %#v, dropped = %#v", result.Hits, result.Dropped)
	}
	if result.Hits[0].TermVersionID != long.TermVersionID || result.Hits[0].OriginalText != "销售额" {
		t.Fatalf("longest hit = %#v", result.Hits[0])
	}
	if result.Hits[1].TermVersionID != duplicateHigh.TermVersionID {
		t.Fatalf("priority hit = %#v", result.Hits[1])
	}
	if result.Hits[2].TermVersionID != prefix.TermVersionID || result.Hits[2].OriginalText != "收入" {
		t.Fatalf("residual prefix hit = %#v", result.Hits[2])
	}
	if result.Hits[3].TermVersionID != suffix.TermVersionID || result.Hits[3].OriginalText != "增长" {
		t.Fatalf("residual suffix hit = %#v", result.Hits[3])
	}
	assertDictionaryDrop(t, result.Dropped, short.TermVersionID, DictionaryDropOverlap)
	assertDictionaryDrop(t, result.Dropped, duplicateLow.TermVersionID, DictionaryDropOverlap)
	assertDictionaryDrop(t, result.Dropped, prefixCovered.TermVersionID, DictionaryDropResidualCovered)
}

func TestDictionaryNegativeValidityAndRolePruningRecordsReasons(t *testing.T) {
	fixture := newDictionaryTestFixture(t, "pruning")
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	negative := dictionaryTermFixture(fixture, "华东", 100)
	negative.NegativeContexts = []string{"物流"}
	expired := dictionaryTermFixture(fixture, "历史收入", 100)
	validTo := now
	expired.ValidTo = &validTo
	restricted := dictionaryTermFixture(fixture, "董事会收入", 100)
	restricted.ApplicableRoleIDs = []askdata.ID{askdata.ID(uuid.NewString())}
	matcher, _ := dictionaryMatcherFixture(t, fixture, []DictionaryTerm{negative, expired, restricted})
	result, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: fixture.scope, Question: "华东物流、历史收入和董事会收入", Now: now,
	})
	if err != nil || len(result.Hits) != 0 {
		t.Fatalf("pruned result = %#v/%v", result, err)
	}
	assertDictionaryDrop(t, result.Dropped, negative.TermVersionID, DictionaryDropNegativeContext)
	assertDictionaryDrop(t, result.Dropped, expired.TermVersionID, DictionaryDropOutsideValidity)
	assertDictionaryDrop(t, result.Dropped, restricted.TermVersionID, DictionaryDropRoleRestricted)

	kept, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: fixture.scope, Question: "华东销售", Now: now,
	})
	if err != nil || len(kept.Hits) != 1 || kept.Hits[0].TermVersionID != negative.TermVersionID {
		t.Fatalf("sales-context match = %#v/%v", kept, err)
	}
}

func TestDictionaryRegexpAndNormalizationSpanProjection(t *testing.T) {
	fixture := newDictionaryTestFixture(t, "regexp-span")
	regexpTerm := dictionaryTermFixture(fixture, "订单编号", 100)
	regexpTerm.MatchMode = DictionaryMatchRegexpSafe
	regexpTerm.MatchPattern = `^订单[0-9]{1,8}$`
	spanTerm := dictionaryTermFixture(fixture, "abc 100万元", 100)
	matcher, _ := dictionaryMatcherFixture(t, fixture, []DictionaryTerm{regexpTerm, spanTerm})
	matched, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: fixture.scope, Question: "订单２０２６",
	})
	if err != nil || len(matched.Hits) != 1 || matched.Hits[0].MatchMode != DictionaryMatchRegexpSafe {
		t.Fatalf("regexp result = %#v/%v", matched, err)
	}
	projected, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: fixture.scope, Question: "请问ＡＢＣ １００ 万元销售额",
	})
	if err != nil || len(projected.Hits) != 1 {
		t.Fatalf("projection result = %#v/%v", projected, err)
	}
	if projected.Hits[0].NormalizedText != "abc 100万元" || projected.Hits[0].OriginalText != "ＡＢＣ １００ 万元" {
		t.Fatalf("projected hit = %#v", projected.Hits[0])
	}
	originalRunes := []rune(projected.Normalization.Original)
	span := projected.Hits[0].OriginalSpan
	if string(originalRunes[span.Start:span.End]) != projected.Hits[0].OriginalText {
		t.Fatalf("original span = %#v", projected.Hits[0])
	}
}

func TestDictionaryRegexpPropagatesCancelledRequest(t *testing.T) {
	fixture := newDictionaryTestFixture(t, "regexp-timeout")
	regexpTerm := dictionaryTermFixture(fixture, "订单编号", 100)
	regexpTerm.MatchMode = DictionaryMatchRegexpSafe
	regexpTerm.MatchPattern = `^订单[0-9]{1,8}$`
	matcher, _ := dictionaryMatcherFixture(t, fixture, []DictionaryTerm{regexpTerm})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := matcher.Match(ctx, DictionaryMatchRequest{
		Scope: fixture.scope, Question: "订单2026",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled regexp error = %v", err)
	}
}

func TestDictionaryCacheUsesReleaseHashAndRebuildsOnSwitch(t *testing.T) {
	fixture := newDictionaryTestFixture(t, "release-v1")
	first := dictionaryTermFixture(fixture, "收入", 100)
	second := dictionaryTermFixture(fixture, "收入", 200)
	secondRelease := fixture.release
	secondRelease.ContentHash = askdata.HashBytes([]byte("release-v2"))
	secondScope, err := askdata.NewPolicyScope(
		fixture.tenantID, fixture.actorID, fixture.scope.DomainIDs, fixture.scope.RoleIDs, secondRelease,
	)
	if err != nil {
		t.Fatal(err)
	}
	loader := &dictionaryTestLoader{snapshots: map[askdata.ContentHash]DictionarySnapshot{
		fixture.release.ContentHash: {
			TenantID: fixture.tenantID, DomainID: fixture.domainID,
			Release: fixture.release, Terms: []DictionaryTerm{first},
		},
		secondRelease.ContentHash: {
			TenantID: fixture.tenantID, DomainID: fixture.domainID,
			Release: secondRelease, Terms: []DictionaryTerm{second},
		},
	}}
	cache := NewDictionaryCache()
	matcher, _ := NewDictionaryMatcher(loader, cache)
	for index := 0; index < 2; index++ {
		result, err := matcher.Match(context.Background(), DictionaryMatchRequest{
			Scope: fixture.scope, Question: "收入",
		})
		if err != nil || len(result.Hits) != 1 || result.Hits[0].TermVersionID != first.TermVersionID {
			t.Fatalf("first release match %d = %#v/%v", index, result, err)
		}
	}
	if loader.loadCount() != 1 || cache.Len() != 1 {
		t.Fatalf("first release cache = loads:%d entries:%d", loader.loadCount(), cache.Len())
	}
	result, err := matcher.Match(context.Background(), DictionaryMatchRequest{
		Scope: secondScope, Question: "收入",
	})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].TermVersionID != second.TermVersionID {
		t.Fatalf("second release match = %#v/%v", result, err)
	}
	if loader.loadCount() != 2 || cache.Len() != 1 {
		t.Fatalf("release switch cache = loads:%d entries:%d", loader.loadCount(), cache.Len())
	}
}

func TestDictionaryTenThousandTermsWarmMatchUnderOneMillisecond(t *testing.T) {
	if testing.Short() {
		t.Skip("performance contract")
	}
	if dictionaryRaceEnabled {
		t.Skip("race instrumentation invalidates the sub-millisecond latency contract")
	}
	fixture := newDictionaryTestFixture(t, "performance")
	terms := make([]DictionaryTerm, 10_000)
	for index := range terms {
		terms[index] = dictionaryTermFixture(fixture, fmt.Sprintf("词条%05d", index), 100)
	}
	matcher, _ := dictionaryMatcherFixture(t, fixture, terms)
	question := strings.Repeat("无", 490) + "词条09999"
	request := DictionaryMatchRequest{Scope: fixture.scope, Question: question}
	if result, err := matcher.Match(context.Background(), request); err != nil || len(result.Hits) != 1 {
		t.Fatalf("warm match = %d/%v", len(result.Hits), err)
	}
	durations := make([]time.Duration, 25)
	for index := range durations {
		started := time.Now()
		result, err := matcher.Match(context.Background(), request)
		durations[index] = time.Since(started)
		if err != nil || len(result.Hits) != 1 {
			t.Fatalf("measured match %d = %d/%v", index, len(result.Hits), err)
		}
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	if median := durations[len(durations)/2]; median >= time.Millisecond {
		t.Fatalf("median warm match = %s, want < 1ms", median)
	}
}

func assertDictionaryDrop(
	t *testing.T,
	drops []DictionaryDrop,
	termVersionID askdata.ID,
	reason string,
) {
	t.Helper()
	for _, drop := range drops {
		if drop.TermVersionID == termVersionID && drop.Reason == reason {
			return
		}
	}
	t.Fatalf("missing drop %s/%s in %#v", termVersionID, reason, drops)
}
