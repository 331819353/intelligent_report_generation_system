package answer

import (
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type VerifyCode string

const (
	AnswerNumberUnverified   VerifyCode = "ANSWER_NUMBER_UNVERIFIED"
	AnswerTimeMismatch       VerifyCode = "ANSWER_TIME_MISMATCH"
	AnswerUnitMismatch       VerifyCode = "ANSWER_UNIT_MISMATCH"
	AnswerObjectHallucinated VerifyCode = "ANSWER_OBJECT_HALLUCINATED"
	AnswerForbiddenAssertion VerifyCode = "ANSWER_FORBIDDEN_ASSERTION"
	AnswerExternalFact       VerifyCode = "ANSWER_EXTERNAL_FACT"
)

type VerifyFailure struct {
	Element  ElementKind     `json:"element"`
	Text     string          `json:"text"`
	Span     shared.TextSpan `json:"span"`
	Reason   VerifyCode      `json:"reason"`
	Expected []string        `json:"expected"`
}

type VerifyReport struct {
	Passed                bool   `json:"passed"`
	VerifierVersion       string `json:"verifierVersion"`
	PolicyWordlistVersion string `json:"policyWordlistVersion"`
	// Source records which catalog grade backed this verification, so a
	// dataset-verified narrative is never presented as a certified-metric one.
	Source   BindingSource   `json:"source,omitempty"`
	Failures []VerifyFailure `json:"failures"`
}

type ReleaseVerifierPolicy struct {
	VerifierVersion       string
	PolicyWordlistVersion string
	ContributionMode      bool
}

// VerificationNarrative is the common, pipeline-neutral input to the fact
// verifier. Ask Data and Report adapt their immutable artifacts to this shape
// before entering the same verification implementation.
type VerificationNarrative struct {
	Text                  string
	Citations             []shared.Citation
	VerifierVersion       string
	PolicyWordlistVersion string
	ReferenceHash         askdata.ContentHash
	// Source and CatalogID pin the object catalog the prose was written
	// against. They must match the BindingEvidence supplied at verification,
	// so a narrative cannot be checked against a catalog it never saw.
	Source    BindingSource
	CatalogID askdata.ID
}

type Verifier struct {
	policy           policyWordlist
	contributionMode bool
}

func NewVerifier(releasePolicy ReleaseVerifierPolicy) (*Verifier, error) {
	wordlist, err := loadPolicyWordlist()
	if err != nil {
		return nil, err
	}
	if releasePolicy.VerifierVersion != VerifierVersion {
		return nil, fmt.Errorf("release verifier version %q is unavailable", releasePolicy.VerifierVersion)
	}
	if releasePolicy.PolicyWordlistVersion != wordlist.Version {
		return nil, fmt.Errorf("release policy wordlist version %q is unavailable", releasePolicy.PolicyWordlistVersion)
	}
	return &Verifier{policy: wordlist, contributionMode: releasePolicy.ContributionMode}, nil
}

func DefaultReleaseVerifierPolicy(contributionMode bool) ReleaseVerifierPolicy {
	wordlist, err := loadPolicyWordlist()
	if err != nil {
		return ReleaseVerifierPolicy{}
	}
	return ReleaseVerifierPolicy{
		VerifierVersion: VerifierVersion, PolicyWordlistVersion: wordlist.Version,
		ContributionMode: contributionMode,
	}
}

// Verify is the single Ask Data and Report narrative verification boundary.
// It consumes only exact result cells, release-pinned bindings and the resolved
// time contract. It never searches arbitrary cell combinations.
func (verifier *Verifier) Verify(
	artifact AnswerArtifact,
	result ResultEvidence,
	binding BindingEvidence,
	timeSpec compiler.ResolvedTimeSpec,
) VerifyReport {
	return verifier.VerifyNarrative(VerificationNarrative{
		Text:                  artifact.Layers.Narrative.CanonicalText(),
		Citations:             artifact.Layers.Narrative.Citations,
		VerifierVersion:       artifact.Verification.VerifierVersion,
		PolicyWordlistVersion: artifact.Verification.PolicyWordlistVersion,
		ReferenceHash:         artifact.Provenance.ResultHash,
		// An Ask Data answer is always written against its semantic release.
		Source:    BindingSourceSemanticRelease,
		CatalogID: artifact.Provenance.SemanticReleaseID,
	}, result, binding, timeSpec)
}

// VerifyNarrative is the shared Ask Data/Report verification path. Callers
// must provide result cells and derivations explicitly; the verifier never
// searches arbitrary value combinations.
func (verifier *Verifier) VerifyNarrative(
	narrative VerificationNarrative,
	result ResultEvidence,
	binding BindingEvidence,
	timeSpec compiler.ResolvedTimeSpec,
) VerifyReport {
	report := VerifyReport{Failures: []VerifyFailure{}}
	if verifier == nil {
		return invalidInputReport(report, "initialized verifier")
	}
	report.VerifierVersion = VerifierVersion
	report.PolicyWordlistVersion = verifier.policy.Version
	report.Source = binding.Source
	text := narrative.Text
	if narrative.VerifierVersion != VerifierVersion ||
		narrative.PolicyWordlistVersion != verifier.policy.Version {
		report.Failures = append(report.Failures, VerifyFailure{
			Element: ElementAssertion, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
			Expected: []string{VerifierVersion, verifier.policy.Version},
		})
	}
	if narrative.ReferenceHash != result.ReferenceHash ||
		narrative.Source != binding.Source || narrative.CatalogID != binding.CatalogID() {
		report.Failures = append(report.Failures, VerifyFailure{
			Element: ElementAssertion, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
			Expected: []string{"result reference hash and object catalog pinned by the narrative artifact"},
		})
	}
	if err := shared.ValidateCitations(text, narrative.Citations); err != nil {
		report.Failures = append(report.Failures, VerifyFailure{
			Element: ElementAssertion, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
			Expected: []string{"valid non-overlapping citations: " + err.Error()},
		})
	}
	if err := result.Validate(); err != nil {
		report.Failures = append(report.Failures, VerifyFailure{
			Element: ElementNumber, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
			Expected: []string{"valid result evidence: " + err.Error()},
		})
	}
	if err := binding.Validate(); err != nil {
		report.Failures = append(report.Failures, VerifyFailure{
			Element: ElementObject, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
			Expected: []string{"valid binding catalog: " + err.Error()},
		})
	}
	if len(report.Failures) != 0 {
		return finalizeReport(report)
	}

	index := newEvidenceIndex(result)
	for _, element := range extractElements(text, binding, verifier.policy, verifier.contributionMode) {
		switch element.Kind {
		case ElementNumber:
			citation, ok := coveringCitation(narrative.Citations, element.Span, shared.CitationResultCell)
			if !ok {
				report.Failures = append(report.Failures, failureFor(element, AnswerNumberUnverified, []string{"RESULT_CELL citation"}))
				continue
			}
			if matched, expected := index.matchNumber(element, citation, result); !matched {
				report.Failures = append(report.Failures, failureFor(element, AnswerNumberUnverified, expected))
			}
		case ElementTime:
			if _, ok := coveringCitation(narrative.Citations, element.Span, shared.CitationTimeSpec); !ok {
				report.Failures = append(report.Failures, failureFor(element, AnswerTimeMismatch, []string{"TIME_SPEC citation"}))
				continue
			}
			if matched, expected := timeMatches(element.Text, timeSpec); !matched {
				report.Failures = append(report.Failures, failureFor(element, AnswerTimeMismatch, expected))
			}
		case ElementUnit:
			citation, ok := coveringCitationAny(narrative.Citations, element.Span, shared.CitationResultCell, shared.CitationContract)
			if !ok {
				report.Failures = append(report.Failures, failureFor(element, AnswerUnitMismatch, []string{"RESULT_CELL or CONTRACT citation"}))
				continue
			}
			if matched, expected := index.matchUnit(element, citation); !matched {
				report.Failures = append(report.Failures, failureFor(element, AnswerUnitMismatch, expected))
			}
		case ElementObject:
			if element.Object == nil || !element.Object.Bound {
				report.Failures = append(report.Failures, failureFor(element, AnswerObjectHallucinated, boundObjectNames(binding)))
				continue
			}
			citation, ok := coveringCitation(narrative.Citations, element.Span, shared.CitationContract)
			if !ok || citation.ContractID == nil || *citation.ContractID != element.Object.VersionID {
				report.Failures = append(report.Failures, failureFor(element, AnswerExternalFact, []string{"CONTRACT citation for " + string(element.Object.VersionID)}))
			}
		case ElementAssertion:
			code := AnswerForbiddenAssertion
			if element.Policy != nil && element.Policy.Category == PolicyExternalFact {
				code = AnswerExternalFact
			}
			report.Failures = append(report.Failures, failureFor(element, code, []string{"evidence-limited descriptive wording"}))
		}
	}
	return finalizeReport(report)
}

func invalidInputReport(report VerifyReport, expected string) VerifyReport {
	report.Failures = append(report.Failures, VerifyFailure{
		Element: ElementAssertion, Span: shared.TextSpan{}, Reason: AnswerExternalFact,
		Expected: []string{expected},
	})
	return finalizeReport(report)
}

func failureFor(element ExtractedElement, code VerifyCode, expected []string) VerifyFailure {
	if expected == nil {
		expected = []string{}
	}
	return VerifyFailure{
		Element: element.Kind, Text: element.Text, Span: element.Span,
		Reason: code, Expected: expected,
	}
}

func finalizeReport(report VerifyReport) VerifyReport {
	sort.SliceStable(report.Failures, func(left, right int) bool {
		if report.Failures[left].Span.Start != report.Failures[right].Span.Start {
			return report.Failures[left].Span.Start < report.Failures[right].Span.Start
		}
		if report.Failures[left].Span.End != report.Failures[right].Span.End {
			return report.Failures[left].Span.End < report.Failures[right].Span.End
		}
		return report.Failures[left].Reason < report.Failures[right].Reason
	})
	report.Passed = len(report.Failures) == 0
	if report.Failures == nil {
		report.Failures = []VerifyFailure{}
	}
	return report
}

func coveringCitation(citations []shared.Citation, span shared.TextSpan, kind shared.CitationKind) (shared.Citation, bool) {
	return coveringCitationAny(citations, span, kind)
}

func coveringCitationAny(citations []shared.Citation, span shared.TextSpan, kinds ...shared.CitationKind) (shared.Citation, bool) {
	for _, citation := range citations {
		if citation.TextSpan.Start > span.Start || citation.TextSpan.End < span.End {
			continue
		}
		for _, kind := range kinds {
			if citation.Kind == kind {
				return citation, true
			}
		}
	}
	return shared.Citation{}, false
}

func boundObjectNames(binding BindingEvidence) []string {
	result := make([]string, 0)
	for _, object := range binding.Objects {
		if object.Bound {
			result = append(result, object.Names...)
		}
	}
	sort.Strings(result)
	return result
}
