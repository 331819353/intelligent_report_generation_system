package understanding_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata/understanding"
)

func TestNormalizeQuestionCanonicalizesAndMapsOriginalEntityText(t *testing.T) {
	result, err := understanding.NormalizeQuestion("  请问，ＡＣＭＥ公司的销售额呢？  ")
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != understanding.NormalizationVersion || result.Normalized != "acme公司的销售额?" {
		t.Fatalf("normalized question = %#v", result)
	}
	entity, err := result.OriginalText(understanding.Span{Start: 0, End: 4})
	if err != nil {
		t.Fatal(err)
	}
	if entity != "ＡＣＭＥ" {
		t.Fatalf("original entity = %q", entity)
	}
	entitySpan, err := result.OriginalSpan(understanding.Span{Start: 0, End: 4})
	if err != nil {
		t.Fatal(err)
	}
	if entitySpan != (understanding.Span{Start: 5, End: 9}) {
		t.Fatalf("original entity span = %#v", entitySpan)
	}
	normalizedEntity, err := result.NormalizedSpan(entitySpan)
	if err != nil {
		t.Fatal(err)
	}
	if normalizedEntity != (understanding.Span{Start: 0, End: 4}) {
		t.Fatalf("normalized entity span = %#v", normalizedEntity)
	}
	if _, err := result.NormalizedSpan(understanding.Span{Start: 2, End: 4}); !errors.Is(err, understanding.ErrRemovedOffsetSpan) {
		t.Fatalf("removed discourse span error = %v", err)
	}
	if result.Original != "  请问，ＡＣＭＥ公司的销售额呢？  " {
		t.Fatal("normalization mutated the source question")
	}
}

func TestNormalizeQuestionHandlesCompatibilityPunctuationUnitsAndCaseExpansion(t *testing.T) {
	result, err := understanding.NormalizeQuestion("比较 １０ ％、５ ㎏ 和 Straße 的 GMV。")
	if err != nil {
		t.Fatal(err)
	}
	if result.Normalized != "比较 10%,5kg 和 strasse 的 gmv." {
		t.Fatalf("normalized = %q", result.Normalized)
	}
	strasseStart := runeIndex(result.Normalized, "strasse")
	if strasseStart < 0 {
		t.Fatal("normalized expansion is missing")
	}
	firstExpandedS := understanding.Span{Start: strasseStart + 4, End: strasseStart + 5}
	original, err := result.OriginalText(firstExpandedS)
	if err != nil {
		t.Fatal(err)
	}
	if original != "ß" {
		t.Fatalf("case-fold expansion mapped to %q", original)
	}
	originalSharpS := runeSpan(result.Original, "ß")
	normalizedSharpS, err := result.NormalizedSpan(originalSharpS)
	if err != nil {
		t.Fatal(err)
	}
	if normalizedSharpS != (understanding.Span{Start: strasseStart + 4, End: strasseStart + 6}) {
		t.Fatalf("normalized sharp-s span = %#v", normalizedSharpS)
	}
}

func TestNormalizeQuestionMapsComposedUnicodeSegment(t *testing.T) {
	result, err := understanding.NormalizeQuestion("查询 Cafe\u0301 销售额")
	if err != nil {
		t.Fatal(err)
	}
	if result.Normalized != "查询 café 销售额" {
		t.Fatalf("normalized = %q", result.Normalized)
	}
	normalizedAccent := runeSpan(result.Normalized, "é")
	original, err := result.OriginalSpan(normalizedAccent)
	if err != nil {
		t.Fatal(err)
	}
	want := runeSpan(result.Original, "e\u0301")
	if original != want {
		t.Fatalf("composed segment span = %#v, want %#v", original, want)
	}
	if text, err := result.OriginalText(normalizedAccent); err != nil || text != "e\u0301" {
		t.Fatalf("composed segment text = %q, err=%v", text, err)
	}
}

func TestNormalizeQuestionPreservesSemanticCoreAndIsIdempotent(t *testing.T) {
	result, err := understanding.NormalizeQuestion("麻烦帮我 查询 C++、A/B测试、华东-1区 销售额吧！")
	if err != nil {
		t.Fatal(err)
	}
	if result.Normalized != "查询 c++,a/b测试,华东-1区 销售额!" {
		t.Fatalf("normalized = %q", result.Normalized)
	}
	for _, normalizedEntity := range []string{"c++", "a/b测试", "华东-1区"} {
		span := runeSpan(result.Normalized, normalizedEntity)
		original, err := result.OriginalText(span)
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(original, normalizedEntity) {
			continue
		}
		if original != normalizedEntity {
			t.Fatalf("semantic core %q mapped to %q", normalizedEntity, original)
		}
	}
	again, err := understanding.NormalizeQuestion(result.Normalized)
	if err != nil {
		t.Fatal(err)
	}
	if again.Normalized != result.Normalized {
		t.Fatalf("normalization is not idempotent: %q != %q", again.Normalized, result.Normalized)
	}
}

func TestNormalizeQuestionRejectsInvalidInputsAndSpans(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: " \n\t "},
		{name: "invalid utf8", input: string([]byte{0xff})},
		{name: "control", input: "销售\x00额"},
		{name: "directional override", input: "销售\u202e额"},
		{name: "punctuation only", input: "请问呢？"},
		{name: "too long", input: strings.Repeat("问", understanding.MaxQuestionRunes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := understanding.NormalizeQuestion(test.input); !errors.Is(err, understanding.ErrInvalidQuestion) {
				t.Fatalf("NormalizeQuestion error = %v", err)
			}
		})
	}

	result, err := understanding.NormalizeQuestion("销售额")
	if err != nil {
		t.Fatal(err)
	}
	invalid := []understanding.Span{{Start: -1, End: 1}, {Start: 1, End: 1}, {Start: 0, End: 4}}
	for _, span := range invalid {
		if _, err := result.OriginalSpan(span); !errors.Is(err, understanding.ErrInvalidOffsetSpan) {
			t.Fatalf("OriginalSpan(%#v) error = %v", span, err)
		}
	}
	if utf8.RuneCountInString(result.Normalized) != 3 {
		t.Fatal("test assumption about rune offsets is invalid")
	}
	tampered := result
	tampered.Normalized = "销售量"
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered normalization Validate error = %v", err)
	}
}

func FuzzNormalizeQuestionOffsetRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"请问，ＡＣＭＥ公司的销售额呢？", "比较 １０ ％、５ ㎏ 和 Straße", "查询 Cafe\u0301 销售额",
		"麻烦帮我看华东-1区销售额吧！", "😀 商品销量", "2024 年 GMV",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		result, err := understanding.NormalizeQuestion(input)
		if err != nil {
			return
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		for index := range []rune(result.Normalized) {
			normalized := understanding.Span{Start: index, End: index + 1}
			original, err := result.OriginalSpan(normalized)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := result.NormalizedSpan(original)
			if err != nil {
				t.Fatal(err)
			}
			if roundTrip.Start > index || roundTrip.End <= index {
				t.Fatalf("offset %d escaped round trip %#v -> %#v", index, original, roundTrip)
			}
		}
	})
}

func runeIndex(value, target string) int {
	byteIndex := strings.Index(value, target)
	if byteIndex < 0 {
		return -1
	}
	return utf8.RuneCountInString(value[:byteIndex])
}

func runeSpan(value, target string) understanding.Span {
	start := runeIndex(value, target)
	if start < 0 {
		return understanding.Span{Start: -1, End: -1}
	}
	return understanding.Span{Start: start, End: start + utf8.RuneCountInString(target)}
}
