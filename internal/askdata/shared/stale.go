package shared

import (
	"fmt"
	"regexp"

	"intelligent-report-generation-system/internal/askdata"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

// Provenance is the superset used by persisted answers and report insights.
// Empty fields are allowed when a contract does not use that factor; a missing
// current value never silently matches a persisted non-empty value.
type Provenance struct {
	DatasetVersionID         askdata.ID
	DataSnapshotVersion      string
	QueryHash                askdata.ContentHash
	FilterHash               askdata.ContentHash
	AnalysisMethodVersion    string
	EvidenceAlgorithmVersion string
	PromptVersion            string
	ModelPolicy              string
	VerifierVersion          string
	PolicyWordlistVersion    string
	EvidenceHash             askdata.ContentHash
	ResultHash               askdata.ContentHash
	SemanticReleaseID        askdata.ID
	ChartRuleVersion         string
}

func (provenance Provenance) Validate() error {
	for name, value := range map[string]askdata.ID{
		"datasetVersionId":  provenance.DatasetVersionID,
		"semanticReleaseId": provenance.SemanticReleaseID,
	} {
		if value != "" {
			if err := value.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	for name, value := range map[string]askdata.ContentHash{
		"queryHash": provenance.QueryHash, "filterHash": provenance.FilterHash,
		"evidenceHash": provenance.EvidenceHash, "resultHash": provenance.ResultHash,
	} {
		if value != "" {
			if err := value.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	for name, value := range map[string]string{
		"dataSnapshotVersion":      provenance.DataSnapshotVersion,
		"analysisMethodVersion":    provenance.AnalysisMethodVersion,
		"evidenceAlgorithmVersion": provenance.EvidenceAlgorithmVersion,
		"promptVersion":            provenance.PromptVersion, "modelPolicy": provenance.ModelPolicy,
		"verifierVersion":       provenance.VerifierVersion,
		"policyWordlistVersion": provenance.PolicyWordlistVersion,
		"chartRuleVersion":      provenance.ChartRuleVersion,
	} {
		if value != "" && !versionPattern.MatchString(value) {
			return fmt.Errorf("%s is not a stable version identifier", name)
		}
	}
	return nil
}

// IsStale is the only stale predicate for Answer and Insight artifacts. It is
// deliberately fail-closed: invalid provenance is stale.
func IsStale(artifact, current Provenance) bool {
	return artifact.Validate() != nil || current.Validate() != nil || artifact != current
}
