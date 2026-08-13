package goldenset

import (
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/evaluation/suites"
)

// NarrativeSubmissionVersion is the intake contract version. The narrative
// review suite is the one suite whose content cannot be synthesized: it scores
// human judgement about generated prose, so two named reviewers must actually
// have read it. What the platform owns is the channel — the schema, the
// integrity binding and the deterministic gate — and that is what lives here.
const NarrativeSubmissionVersion = "askdata-narrative-review-submission-v1"

var ErrNarrativeSubmission = errors.New("narrative review submission is invalid")

// NarrativeReviewSubmission is the file two reviewers' verdicts arrive in.
// There is deliberately no field for the narrative text: the suite scores four
// booleans against stable identities, and carrying the prose would turn a
// review ledger into a second copy of generated content.
type NarrativeReviewSubmission struct {
	SchemaVersion string                       `json:"schemaVersion"`
	SuiteVersion  string                       `json:"suiteVersion"`
	ReleaseID     askdata.ID                   `json:"releaseId"`
	Cases         []suites.NarrativeReviewCase `json:"cases"`
}

// NarrativeReviewHash binds a reviewer's declaration to the case it is about.
// Without it a verdict could be edited after the fact and the ledger would
// still validate, which would make the 2% failure budget unfalsifiable.
func NarrativeReviewHash(
	caseID askdata.ID,
	caseContentHash askdata.ContentHash,
	review suites.HumanNarrativeReview,
) askdata.ContentHash {
	payload := fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%d\x00%t\x00%t\x00%t\x00%t",
		NarrativeSubmissionVersion, caseID, caseContentHash, review.ReviewerID, review.Slot,
		review.Verdicts[suites.NarrativeNumericConsistency],
		review.Verdicts[suites.NarrativeSemanticConsistency],
		review.Verdicts[suites.NarrativeNoExternalFact],
		review.Verdicts[suites.NarrativeNoCausalAssertion],
	)
	return askdata.HashBytes([]byte(payload))
}

// Validate enforces everything the suite cannot see from its own contract: the
// submission identity, the per-review integrity binding, and the requirement
// that the two reviewers have already been adjudicated into one verdict set.
func (submission NarrativeReviewSubmission) Validate() error {
	if submission.SchemaVersion != NarrativeSubmissionVersion {
		return fmt.Errorf("%w: schemaVersion must be %q", ErrNarrativeSubmission, NarrativeSubmissionVersion)
	}
	if submission.SuiteVersion != suites.NarrativeReviewSchemaVersion {
		return fmt.Errorf(
			"%w: suiteVersion must be %q", ErrNarrativeSubmission, suites.NarrativeReviewSchemaVersion,
		)
	}
	if submission.ReleaseID.Validate() != nil {
		return fmt.Errorf("%w: releaseId is not a stable identifier", ErrNarrativeSubmission)
	}
	if len(submission.Cases) == 0 {
		return fmt.Errorf("%w: no reviewed cases", ErrNarrativeSubmission)
	}
	seen := make(map[askdata.ID]struct{}, len(submission.Cases))
	for _, reviewed := range submission.Cases {
		if _, duplicate := seen[reviewed.CaseID]; duplicate {
			return fmt.Errorf("%w: case %s appears twice", ErrNarrativeSubmission, reviewed.CaseID)
		}
		seen[reviewed.CaseID] = struct{}{}
		for _, review := range reviewed.Reviews {
			if err := review.Verdicts.Validate(); err != nil {
				return fmt.Errorf(
					"%w: case %s reviewer %s did not judge every dimension",
					ErrNarrativeSubmission, reviewed.CaseID, review.ReviewerID,
				)
			}
			expected := NarrativeReviewHash(reviewed.CaseID, reviewed.CaseContentHash, review)
			if review.ReviewHash != expected {
				return fmt.Errorf(
					"%w: case %s reviewer %s review hash does not bind its verdicts",
					ErrNarrativeSubmission, reviewed.CaseID, review.ReviewerID,
				)
			}
		}
		// The suite treats the two slots as one adjudicated judgement, so a live
		// disagreement is not a low score — it is an unfinished review, and
		// reporting it as a failure would quietly count it against the budget.
		for _, dimension := range []suites.NarrativeDimension{
			suites.NarrativeNumericConsistency, suites.NarrativeSemanticConsistency,
			suites.NarrativeNoExternalFact, suites.NarrativeNoCausalAssertion,
		} {
			if reviewed.Reviews[0].Verdicts[dimension] != reviewed.Reviews[1].Verdicts[dimension] {
				return fmt.Errorf(
					"%w: case %s is unadjudicated on %s; both reviewers must record the agreed verdict",
					ErrNarrativeSubmission, reviewed.CaseID, dimension,
				)
			}
		}
	}
	return nil
}

// EvaluateNarrativeSubmission validates the ledger and then recomputes the gate
// from it. The report is never taken from the submission: a gate that accepts a
// supplied verdict is not a gate.
func EvaluateNarrativeSubmission(
	submission NarrativeReviewSubmission,
) (suites.NarrativeReviewReport, error) {
	if err := submission.Validate(); err != nil {
		return suites.NarrativeReviewReport{}, err
	}
	return suites.EvaluateNarrativeReviews(submission.Cases)
}
