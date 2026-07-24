package metriccandidate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

// PublicationGenerator derives and enriches a candidate batch from the exact immutable draft
// revision loaded by the background preparation worker. The reserved version identity becomes
// the real immutable version on approval, so the prepared batch never needs to be rebound or
// sent to the LLM twice.
type PublicationGenerator struct {
	enricher *Enricher
}

func NewPublicationGenerator(enricher *Enricher) *PublicationGenerator {
	return &PublicationGenerator{enricher: enricher}
}

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
	if g != nil && g.enricher != nil {
		enriched, enrichmentErr := g.enricher.Enrich(ctx, tenantID, actorID, version, result)
		result = enriched
		if enrichmentErr != nil {
			result.Warnings = append(result.Warnings,
				"LLM 语义补全暂不可用，本次已保留规则候选；重新提交同一审批申请可重试补全。")
		}
	}
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
