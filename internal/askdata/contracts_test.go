package askdata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContractRoundTrip(t *testing.T) {
	release := ReleaseRef{ReleaseID: "release-2026-08", ContentHash: HashBytes([]byte("release"))}
	scope, err := NewPolicyScope("tenant-1", "actor-1", []ID{"sales", "finance", "sales"}, []ID{"analyst", "viewer"}, release)
	if err != nil {
		t.Fatalf("NewPolicyScope() error = %v", err)
	}
	evidence := EvidenceRef{
		EvidenceID:  "evidence-1",
		Kind:        EvidenceKindSemanticContract,
		SourceID:    "metric-version-1",
		ContentHash: HashBytes([]byte("metric contract")),
	}
	original := struct {
		Version    VersionRef         `json:"version"`
		Release    ReleaseRef         `json:"release"`
		Scope      PolicyScope        `json:"scope"`
		Confidence ConfidenceEvidence `json:"confidence"`
	}{
		Version: VersionRef{ObjectID: "metric-sales", VersionID: "metric-sales@v1", ContentHash: HashBytes([]byte("v1"))},
		Release: release,
		Scope:   scope,
		Confidence: ConfidenceEvidence{
			Score: 0.97, Margin: 0.31, Evidence: []EvidenceRef{evidence},
			ReasonCodes: []string{"EXACT_ALIAS_MATCH"},
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded struct {
		Version    VersionRef         `json:"version"`
		Release    ReleaseRef         `json:"release"`
		Scope      PolicyScope        `json:"scope"`
		Confidence ConfidenceEvidence `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := decoded.Version.Validate(); err != nil {
		t.Fatalf("decoded Version.Validate() error = %v", err)
	}
	if err := decoded.Release.Validate(); err != nil {
		t.Fatalf("decoded Release.Validate() error = %v", err)
	}
	if err := decoded.Scope.Validate(); err != nil {
		t.Fatalf("decoded Scope.Validate() error = %v", err)
	}
	if err := decoded.Confidence.Validate(); err != nil {
		t.Fatalf("decoded Confidence.Validate() error = %v", err)
	}
}

func TestContractValidationRejectsEmptyIDsAndInvalidHashes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "empty ID", err: ID("").Validate(), want: "invalid stable ID"},
		{name: "ID with whitespace", err: ID("sales metric").Validate(), want: "invalid stable ID"},
		{name: "short hash", err: ContentHash("abc").Validate(), want: "64 lowercase"},
		{name: "uppercase hash", err: ContentHash(strings.Repeat("A", 64)).Validate(), want: "64 lowercase"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil || !strings.Contains(test.err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", test.err, test.want)
			}
		})
	}
}

func TestPolicyScopeRejectsTamperedHash(t *testing.T) {
	scope, err := NewPolicyScope(
		"tenant-1",
		"actor-1",
		[]ID{"sales"},
		[]ID{"analyst"},
		ReleaseRef{ReleaseID: "release-1", ContentHash: HashBytes([]byte("release"))},
	)
	if err != nil {
		t.Fatalf("NewPolicyScope() error = %v", err)
	}
	scope.DomainIDs = []ID{"finance"}
	if err := scope.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want hash mismatch", err)
	}
}
