package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	testTenantID    = askdata.ID("11111111-1111-4111-8111-111111111111")
	testActorID     = askdata.ID("22222222-2222-4222-8222-222222222222")
	testDomainID    = askdata.ID("33333333-3333-4333-8333-333333333333")
	testReleaseID   = askdata.ID("44444444-4444-4444-8444-444444444444")
	testDimensionID = askdata.ID("55555555-5555-4555-8555-555555555555")
)

func TestDecideMemberExposureFailsClosedAcrossPolicyMatrix(t *testing.T) {
	tests := []struct {
		name        string
		sensitivity registry.Sensitivity
		policy      registry.MemberIndexPolicy
		high        bool
		wantExact   bool
		wantEmbed   bool
		wantLLM     bool
		wantGrant   bool
		wantErr     bool
	}{
		{"public full", registry.SensitivityPublic, registry.MemberIndexFull, false, false, true, true, false, false},
		{"internal exact", registry.SensitivityInternal, registry.MemberIndexExactOnly, false, true, false, false, false, false},
		{"confidential exact", registry.SensitivityConfidential, registry.MemberIndexExactOnly, false, true, false, false, true, false},
		{"restricted exact", registry.SensitivityRestricted, registry.MemberIndexExactOnly, false, true, false, false, true, false},
		{"high on demand", registry.SensitivityInternal, registry.MemberIndexOnDemand, true, false, false, false, false, false},
		{"none", registry.SensitivityRestricted, registry.MemberIndexNone, false, false, false, false, false, false},
		{"sensitive full rejected", registry.SensitivityConfidential, registry.MemberIndexFull, false, false, false, false, false, true},
		{"high exact rejected", registry.SensitivityInternal, registry.MemberIndexExactOnly, true, false, false, false, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := DecideMemberExposure(test.sensitivity, test.policy, test.high)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidMemberPolicy) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decision.DatabaseExactLookup != test.wantExact ||
				decision.Embedding != test.wantEmbed || decision.LLMContext != test.wantLLM ||
				decision.RequiresObjectGrant != test.wantGrant {
				t.Fatalf("decision = %#v", decision)
			}
			if test.policy != registry.MemberIndexFull &&
				(decision.LogLabel || decision.EvidenceLabel) {
				t.Fatalf("non-FULL policy exposed a label: %#v", decision)
			}
		})
	}
}

func TestExactMemberLookupDiscardsAndRedactsRawValue(t *testing.T) {
	scope := validMemberScope(t)
	raw := "机密客户-Ａ42"
	question := "查询" + raw + "的销售额"
	span := runeSpanForSecurityTest(question, raw)
	lookup, err := NewExactMemberLookup(scope, testDimensionID, question, span)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%+v", lookup)
	serialized, err := json.Marshal(lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{formatted, string(serialized)} {
		if strings.Contains(output, raw) || strings.Contains(output, "Ａ42") ||
			strings.Contains(output, string(lookup.redaction.lookupKeyHash)) {
			t.Fatalf("lookup diagnostic leaked member material: %s", output)
		}
	}
	if !strings.Contains(string(serialized), RedactedMemberLabel) {
		t.Fatalf("serialized lookup is not explicitly redacted: %s", serialized)
	}
	if lookup.redaction.lookupKeyHash.Validate() != nil ||
		lookup.redaction.questionHash != askdata.HashBytes([]byte(question)) ||
		lookup.redaction.span != span {
		t.Fatalf("lookup redaction binding is invalid: %+v", lookup)
	}
}

func TestExactMemberLookupRequiresExactActorAndSelectedDomain(t *testing.T) {
	question := "查询机密客户-A42的销售额"
	lookup, err := NewExactMemberLookup(
		validMemberScope(t), testDimensionID, question,
		runeSpanForSecurityTest(question, "机密客户-A42"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(testActorID), string(testDomainID))
	if _, _, _, err := validateExactMemberLookup(ctx, lookup); err != nil {
		t.Fatalf("valid lookup: %v", err)
	}
	wrongActor := database.WithAccessContext(context.Background(), "66666666-6666-4666-8666-666666666666", string(testDomainID))
	if _, _, _, err := validateExactMemberLookup(wrongActor, lookup); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("wrong actor error = %v", err)
	}
	wrongDomain := database.WithAccessContext(context.Background(), string(testActorID), "77777777-7777-4777-8777-777777777777")
	if _, _, _, err := validateExactMemberLookup(wrongDomain, lookup); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("wrong domain error = %v", err)
	}
}

func TestExactMemberLookupRejectsNonCanonicalDimensionAndWrongQuestion(t *testing.T) {
	question := "查询A42"
	span := runeSpanForSecurityTest(question, "A42")
	if _, err := NewExactMemberLookup(
		validMemberScope(t), "55555555555545558555555555555555", question, span,
	); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("non-canonical dimension error = %v", err)
	}
	lookup, err := NewExactMemberLookup(validMemberScope(t), testDimensionID, question, span)
	if err != nil {
		t.Fatal(err)
	}
	match := exactMemberMatchForSecurityTest(t, lookup)
	if _, err := match.SensitiveSpan("查询B42"); !errors.Is(err, ErrInvalidMemberRedaction) {
		t.Fatalf("wrong question error = %v", err)
	}
}

func TestRedactSensitiveMemberSpansRemovesEveryLabel(t *testing.T) {
	question := "比较客户甲与客户乙的销售额"
	redacted, err := RedactSensitiveMemberSpans(question, []RuneSpan{{Start: 2, End: 5}, {Start: 6, End: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, "客户甲") || strings.Contains(redacted, "客户乙") ||
		strings.Count(redacted, RedactedMemberLabel) != 2 {
		t.Fatalf("redacted question = %q", redacted)
	}
	if _, err := RedactSensitiveMemberSpans(question, []RuneSpan{{Start: 2, End: 7}, {Start: 6, End: 9}}); !errors.Is(err, ErrInvalidMemberRedaction) {
		t.Fatalf("overlap error = %v", err)
	}
	if _, err := RedactSensitiveMemberSpans(question, []RuneSpan{}); !errors.Is(err, ErrInvalidMemberRedaction) {
		t.Fatalf("empty redaction error = %v", err)
	}
}

func TestExactMemberMatchSchemaCannotCarryALabel(t *testing.T) {
	question := "查询A42"
	lookup, err := NewExactMemberLookup(
		validMemberScope(t), testDimensionID, question,
		runeSpanForSecurityTest(question, "A42"),
	)
	if err != nil {
		t.Fatal(err)
	}
	match := exactMemberMatchForSecurityTest(t, lookup)
	if err := match.Validate(); err != nil {
		t.Fatal(err)
	}
	if match.MemberVersionID() != "88888888-8888-4888-8888-888888888888" ||
		match.DimensionVersionID() != testDimensionID ||
		match.MemberContentHash() != askdata.HashBytes([]byte("member")) ||
		match.DimensionContentHash() != askdata.HashBytes([]byte("dimension")) ||
		match.EvidenceRef().SourceID != match.MemberVersionID() {
		t.Fatalf("read-only match accessors changed the payload contract")
	}
	payload, err := json.Marshal(match)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%+v", match)
	for _, output := range []string{string(payload), formatted} {
		for _, forbidden := range []string{
			"A42", "canonicalLabel", "aliases", "sensitivity", "lookupKeyHash",
			string(lookup.redaction.lookupKeyHash), string(lookup.redaction.questionHash),
		} {
			if strings.Contains(strings.ToLower(output), strings.ToLower(forbidden)) {
				t.Fatalf("safe match output contains %q: %s", forbidden, output)
			}
		}
	}
}

func TestExactMemberMatchRejectsCopiedPayloadTampering(t *testing.T) {
	question := "查询A42"
	lookup, err := NewExactMemberLookup(
		validMemberScope(t), testDimensionID, question,
		runeSpanForSecurityTest(question, "A42"),
	)
	if err != nil {
		t.Fatal(err)
	}
	original := exactMemberMatchForSecurityTest(t, lookup)

	changedID := original
	changedID.payload.MemberVersionID = "99999999-9999-4999-8999-999999999999"
	changedID.payload.Evidence.SourceID = changedID.payload.MemberVersionID
	if err := changedID.Validate(); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("copied match with replaced authoritative IDs validated: %v", err)
	}

	changedHash := original
	changedHash.payload.MemberContentHash = askdata.HashBytes([]byte("attacker-selected-member"))
	if err := changedHash.Validate(); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("copied match with replaced authoritative hash validated: %v", err)
	}

	changedEvidence := original
	changedEvidence.payload.Evidence.ContentHash = askdata.HashBytes([]byte("attacker-selected-evidence"))
	if err := changedEvidence.Validate(); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("copied match with replaced evidence validated: %v", err)
	}
}

func TestExactMemberRedactionRemovesAdjacentASCIIAndNormalizedVariants(t *testing.T) {
	question := "查询A42销售额"
	lookup, err := NewExactMemberLookup(
		validMemberScope(t), testDimensionID, question,
		runeSpanForSecurityTest(question, "A42"),
	)
	if err != nil {
		t.Fatal(err)
	}
	match := exactMemberMatchForSecurityTest(t, lookup)
	for _, input := range []string{"客户XA42Y", "客户Xa４２Y", "a42与A42"} {
		redacted, redactErr := match.RedactPromptText(question, input)
		if redactErr != nil {
			t.Fatal(redactErr)
		}
		if strings.Contains(strings.ToLower(redacted), "a42") ||
			strings.Contains(redacted, "a４２") || len([]rune(redacted)) != len([]rune(input)) {
			t.Fatalf("normalized value leaked or span drifted: %q -> %q", input, redacted)
		}
	}
}

func TestExactMemberEvidenceDoesNotCommitLookupHashOrQuestion(t *testing.T) {
	scope := validMemberScope(t)
	firstQuestion, secondQuestion := "查询A42", "查询B77"
	first, err := NewExactMemberLookup(
		scope, testDimensionID, firstQuestion,
		runeSpanForSecurityTest(firstQuestion, "A42"),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewExactMemberLookup(
		scope, testDimensionID, secondQuestion,
		runeSpanForSecurityTest(secondQuestion, "B77"),
	)
	if err != nil {
		t.Fatal(err)
	}
	match := exactMemberMatchForSecurityTest(t, first)
	firstEvidence, err := exactMemberEvidence(first, match)
	if err != nil {
		t.Fatal(err)
	}
	secondEvidence, err := exactMemberEvidence(second, match)
	if err != nil {
		t.Fatal(err)
	}
	if firstEvidence != secondEvidence {
		t.Fatalf("evidence depends on lookup material: %+v != %+v", firstEvidence, secondEvidence)
	}
}

func exactMemberMatchForSecurityTest(t *testing.T, lookup ExactMemberLookup) ExactMemberMatch {
	t.Helper()
	match := ExactMemberMatch{
		payload: exactMemberMatchPayload{
			MemberVersionID:      "88888888-8888-4888-8888-888888888888",
			DimensionVersionID:   testDimensionID,
			MemberContentHash:    askdata.HashBytes([]byte("member")),
			DimensionContentHash: askdata.HashBytes([]byte("dimension")),
		},
		redaction: lookup.redaction,
	}
	proof := askdata.HashBytes([]byte("proof"))
	match.payload.Evidence = askdata.EvidenceRef{
		EvidenceID: askdata.ID("member-exact:" + string(proof)),
		Kind:       askdata.EvidenceKindExactAlias, SourceID: match.payload.MemberVersionID,
		ContentHash: proof,
	}
	match.payloadProof = exactMemberPayloadProof(match.payload, match.redaction)
	if err := match.Validate(); err != nil {
		t.Fatal(err)
	}
	return match
}

func runeSpanForSecurityTest(question, fragment string) RuneSpan {
	start := strings.Index(question, fragment)
	if start < 0 {
		panic("fragment is absent")
	}
	startRunes := len([]rune(question[:start]))
	return RuneSpan{Start: startRunes, End: startRunes + len([]rune(fragment))}
}

func validMemberScope(t *testing.T) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		testTenantID, testActorID, []askdata.ID{testDomainID}, []askdata.ID{"analyst"},
		askdata.ReleaseRef{ReleaseID: testReleaseID, ContentHash: askdata.HashBytes([]byte("release"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
