package insight

import (
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type NarrativePrompt struct {
	Evidence      EvidenceBundle `json:"evidence"`
	PromptVersion string         `json:"promptVersion"`
	ModelPolicy   string         `json:"modelPolicy"`
}

type NarrativeDraft struct {
	Content   InsightContent    `json:"content"`
	Citations []shared.Citation `json:"citations"`
}

// NarrativeModel may phrase evidence, but it cannot create or alter facts.
// Implementations normally call an LLM with the closed NarrativePrompt.
type NarrativeModel interface {
	GenerateInsightNarrative(context.Context, NarrativePrompt) (NarrativeDraft, error)
}

type NarrativeGenerationRequest struct {
	ArtifactID            askdata.ID
	Evidence              EvidenceBundle
	ResultEvidence        answer.ResultEvidence
	BindingEvidence       answer.BindingEvidence
	ResolvedTimeSpec      compiler.ResolvedTimeSpec
	PromptVersion         string
	ModelPolicy           string
	VerifierVersion       string
	PolicyWordlistVersion string
}

type NarrativeVerificationError struct {
	Report answer.VerifyReport
}

func (err *NarrativeVerificationError) Error() string {
	return "generated report insight failed evidence verification"
}

type NarrativeGenerator struct {
	Model    NarrativeModel
	Verifier *answer.Verifier
}

// Generate is the mandatory report narrative boundary: deterministic evidence
// enters the model, and the resulting prose cannot leave without passing the
// shared ANS-002 verifier.
func (generator NarrativeGenerator) Generate(
	ctx context.Context,
	request NarrativeGenerationRequest,
) (InsightArtifact, answer.VerifyReport, error) {
	if generator.Model == nil || generator.Verifier == nil {
		return InsightArtifact{}, answer.VerifyReport{}, errors.New("report narrative generator is not configured")
	}
	if request.ArtifactID.Validate() != nil || request.Evidence.Validate() != nil ||
		request.PromptVersion == "" || request.ModelPolicy == "" || request.VerifierVersion == "" ||
		request.PolicyWordlistVersion == "" {
		return InsightArtifact{}, answer.VerifyReport{}, errors.New("report narrative generation request is invalid")
	}
	evidenceHash, err := request.Evidence.Hash()
	if err != nil {
		return InsightArtifact{}, answer.VerifyReport{}, err
	}
	draft, err := generator.Model.GenerateInsightNarrative(ctx, NarrativePrompt{
		Evidence: request.Evidence.Normalize(), PromptVersion: request.PromptVersion, ModelPolicy: request.ModelPolicy,
	})
	if err != nil {
		return InsightArtifact{}, answer.VerifyReport{}, fmt.Errorf("generate report insight narrative: %w", err)
	}
	artifact := InsightArtifact{
		SchemaVersion: InsightSchemaVersion, ID: request.ArtifactID, EvidenceHash: evidenceHash,
		PromptVersion: request.PromptVersion, ModelPolicy: request.ModelPolicy,
		VerifierVersion: request.VerifierVersion, PolicyWordlistVersion: request.PolicyWordlistVersion,
		Content: draft.Content, Citations: draft.Citations, Status: InsightCurrent,
	}.Normalize()
	if err := artifact.ValidateAgainst(request.Evidence); err != nil {
		return InsightArtifact{}, answer.VerifyReport{}, fmt.Errorf("validate generated report insight: %w", err)
	}
	report, err := (VerifiableInsight{Artifact: artifact, Evidence: request.Evidence}).Verify(
		generator.Verifier, request.ResultEvidence, request.BindingEvidence, request.ResolvedTimeSpec,
	)
	if err != nil {
		return InsightArtifact{}, answer.VerifyReport{}, err
	}
	if !report.Passed {
		return InsightArtifact{}, report, &NarrativeVerificationError{Report: report}
	}
	return artifact, report, nil
}
