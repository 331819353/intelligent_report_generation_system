package ai

import "testing"

func TestSemanticQuestionPurposeIsKnownButRequiresExplicitTenantGrant(t *testing.T) {
	t.Parallel()

	if !allowedPurpose(PurposeSemanticQuestion) {
		t.Fatal("SEMANTIC_QUESTION must be accepted by the common AI service")
	}
	for _, test := range []struct {
		name     string
		enabled  bool
		purposes []string
		want     bool
	}{
		{name: "tenant AI disabled", purposes: []string{PurposeSemanticQuestion}},
		{name: "enabled but not granted", enabled: true, purposes: []string{PurposeMetadataCompletion}},
		{name: "explicitly granted", enabled: true, purposes: []string{PurposeSemanticQuestion}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := tenantPolicyAllowsPurpose(test.enabled, test.purposes, PurposeSemanticQuestion); got != test.want {
				t.Fatalf("tenantPolicyAllowsPurpose() = %v, want %v", got, test.want)
			}
		})
	}
}
