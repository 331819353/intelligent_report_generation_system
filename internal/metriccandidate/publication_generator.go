package metriccandidate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

// PublicationGenerator derives a rule-only candidate batch from the exact immutable
// draft revision loaded by the background preparation worker.
type PublicationGenerator struct{}

func NewPublicationGenerator() *PublicationGenerator { return &PublicationGenerator{} }

func (g *PublicationGenerator) GeneratePublicationCandidates(
	ctx context.Context,
	tenantID, actorID string,
	version dataset.VersionRecord,
) (dataset.PublicationCandidatePreparation, error) {
	if version.ID == "" || version.DatasetID == "" || version.DraftVersionID == "" ||
		version.DraftRecordVersion < 1 || version.Status != "PUBLISHED" ||
		version.DSLHash == "" || version.PlanHash == "" || len(version.DSL) == 0 {
		return dataset.PublicationCandidatePreparation{}, dataset.ErrPublicationRequestConflict
	}
	result, err := Extract(version)
	if err != nil {
		return dataset.PublicationCandidatePreparation{}, err
	}
	result = attachDefaultSemantics(version, result)
	raw, err := json.Marshal(result)
	if err != nil {
		return dataset.PublicationCandidatePreparation{}, err
	}
	preparation := dataset.PublicationCandidatePreparation{
		Status: string(result.Status), Result: raw, Total: len(result.Candidates),
		Warning: boundedPreparationWarning(result.Warnings),
	}
	for _, candidate := range result.Candidates {
		switch candidate.Status {
		case CandidateStatusReady:
			preparation.Ready++
		case CandidateStatusNeedsReview:
			preparation.Review++
		case CandidateStatusBlocked:
			preparation.Blocked++
		default:
			return dataset.PublicationCandidatePreparation{}, fmt.Errorf("%w: unsupported candidate status", ErrInvalidRequest)
		}
	}
	if preparation.Status != dataset.PublicationCandidateSucceeded &&
		preparation.Status != dataset.PublicationCandidatePartial {
		return dataset.PublicationCandidatePreparation{}, fmt.Errorf("%w: unsupported extraction status", ErrInvalidRequest)
	}
	return preparation, nil
}

func boundedPreparationWarning(warnings []string) string {
	value := strings.TrimSpace(strings.Join(nonEmptyUnique(warnings, 16, 2000), "；"))
	runes := []rune(value)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return value
}
