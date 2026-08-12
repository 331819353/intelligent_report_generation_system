package registry

import (
	"errors"
	"testing"
)

func TestParseEvaluationExpectedContractRequiresPinnedDirectHashes(t *testing.T) {
	contract, err := parseEvaluationExpectedContract("DIRECT", `{
		"expectedIrHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"expectedResultHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"priority":"P0","complexity":"SIMPLE","ambiguity":"NONE"
	}`)
	if err != nil || contract.Priority != "P0" || contract.SecurityExpectation != "NONE" {
		t.Fatalf("contract = %#v, %v", contract, err)
	}
	if _, err := parseEvaluationExpectedContract("DIRECT", `{}`); !errors.Is(err, ErrEvaluationSetHintInvalid) {
		t.Fatalf("missing direct hashes error = %v", err)
	}
	if _, err := parseEvaluationExpectedContract("CLARIFY", `{}`); err != nil {
		t.Fatalf("clarify contract = %v", err)
	}
}

func TestParseEvaluationExpectedContractRequiresRefusalForSecurityCases(t *testing.T) {
	if _, err := parseEvaluationExpectedContract("DIRECT", `{
		"expectedIrHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"expectedResultHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"securityExpectation":"UNAUTHORIZED_BLOCK"
	}`); !errors.Is(err, ErrEvaluationSetHintInvalid) {
		t.Fatalf("security-direct error = %v", err)
	}
	contract, err := parseEvaluationExpectedContract("REFUSE", `{"securityExpectation":"UNAUTHORIZED_BLOCK"}`)
	if err != nil || contract.SecurityExpectation != "UNAUTHORIZED_BLOCK" {
		t.Fatalf("security-refuse contract = %#v, %v", contract, err)
	}
}
