package reportinsight

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/store"
)

//go:embed schemas/report-insight-v1.schema.json
var reportInsightSchema json.RawMessage

const (
	InsightPromptVersion = "report-insight-v1"
	InsightModelPolicy   = "governed-default"
)

// narrativeSystemPrompt states the one rule that makes the output checkable.
// The model cannot write a figure even if it wants to: markers are the only way
// a number reaches the page, and the server substitutes their verified values.
const narrativeSystemPrompt = `You write a short business commentary strictly from the supplied Evidence Bundle.

Absolute rules:
- Never write a number, percentage, date or unit yourself. Reference a figure only as {{fact:<factId>}} using an id from evidence.facts.
- Never write a metric or dimension name yourself. Reference one only as {{field:<objectId>}} using an id from the supplied objects.
- State only what the evidence shows. Do not explain causes, predict, or bring in outside knowledge.
- Mention a caveat only if evidence.qualityWarnings contains it.
- Write in the same language as the field names supplied.`

// AIService is the governed model boundary. It is an interface so narrative
// generation is testable without a provider.
type AIService interface {
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

// NarrativeModel turns an Evidence Bundle into prose whose every figure and
// object name was substituted by the server from that same evidence.
type AINarrativeModel struct {
	AI       AIService
	Identity func(context.Context) (askdata.ID, askdata.ID, askdata.ID)
	// Objects are the governed objects this narrative may name.
	Objects []insight.MarkerObject
}

func (model AINarrativeModel) GenerateInsightNarrative(
	ctx context.Context, prompt insight.NarrativePrompt,
) (insight.NarrativeDraft, error) {
	if model.AI == nil || model.Identity == nil {
		return insight.NarrativeDraft{}, errors.New("report narrative model is unavailable")
	}
	tenantID, actorID, reportID := model.Identity(ctx)
	payload, err := json.Marshal(struct {
		Evidence insight.EvidenceBundle `json:"evidence"`
		Objects  []insight.MarkerObject `json:"objects"`
	}{prompt.Evidence, model.Objects})
	if err != nil {
		return insight.NarrativeDraft{}, err
	}
	temperature := 0.0
	result, err := model.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(tenantID), ActorID: string(actorID),
		Purpose: aiplatform.PurposeReportGeneration, PromptVersion: prompt.PromptVersion,
		ResourceType: "REPORT", ResourceID: string(reportID),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: narrativeSystemPrompt}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(payload)}}},
			},
			ResponseSchema:  aiplatform.JSONSchema{Name: "report_insight_v1", Schema: reportInsightSchema},
			Temperature:     &temperature,
			MaxOutputTokens: 4_000,
		},
	})
	if err != nil {
		return insight.NarrativeDraft{}, err
	}
	var marked insight.InsightContent
	if err := json.Unmarshal(result.ProviderResult.Content, &marked); err != nil {
		return insight.NarrativeDraft{}, fmt.Errorf("decode report insight narrative: %w", err)
	}
	// Substitution happens here, not in the model: a marker naming anything
	// outside this evidence fails the whole draft rather than reaching the page.
	content, citations, err := insight.RenderMarkedContent(
		marked, insight.MarkerSourcesFor(prompt.Evidence, model.Objects),
	)
	if err != nil {
		return insight.NarrativeDraft{}, fmt.Errorf("render report insight markers: %w", err)
	}
	return insight.NarrativeDraft{Content: content, Citations: citations}, nil
}

// ResultEvidenceOf adapts a bundle to the verifier's result contract. The cells
// a narrative may cite are exactly the cells its own facts came from.
func ResultEvidenceOf(bundle insight.EvidenceBundle) (answer.ResultEvidence, error) {
	hash, err := bundle.Hash()
	if err != nil {
		return answer.ResultEvidence{}, err
	}
	cells := make([]answer.ResultCell, 0, len(bundle.Facts))
	seen := map[string]bool{}
	for _, fact := range bundle.Facts {
		for _, ref := range fact.CellRefs {
			key := ref.RowKey + "\x00" + ref.ColumnKey
			if seen[key] {
				continue
			}
			seen[key] = true
			cells = append(cells, answer.ResultCell{
				Ref: ref, MetricVersionID: fact.MetricVersionID, Value: fact.CurrentValue,
				ValueKind: answer.ValueNumber, Unit: fact.Unit,
			})
		}
	}
	return answer.ResultEvidence{
		Version: answer.ResultEvidenceVersion, ReferenceHash: hash,
		Cells: cells, Derivations: []answer.DerivationEvidence{},
	}.Normalize(), nil
}

// BindingEvidenceOf declares the catalog this narrative is checked against.
func BindingEvidenceOf(bundle insight.EvidenceBundle, objects []insight.MarkerObject) answer.BindingEvidence {
	source, catalogID := bundle.BindingCatalog()
	evidence := answer.BindingEvidence{Version: answer.BindingEvidenceVersion, Source: source}
	if source == answer.BindingSourceDatasetVersion {
		evidence.DatasetVersionID = catalogID
	} else {
		evidence.SemanticReleaseID = catalogID
	}
	// Object names must be unique across the catalog, so a duplicate display
	// name is dropped rather than making the whole catalog invalid.
	evidence.Objects = make([]answer.ObjectEvidence, 0, len(objects))
	seenNames := map[string]bool{}
	seenIDs := map[askdata.ID]bool{}
	for _, object := range objects {
		if object.ObjectID.Validate() != nil || object.Name == "" ||
			seenNames[object.Name] || seenIDs[object.ObjectID] {
			continue
		}
		seenNames[object.Name], seenIDs[object.ObjectID] = true, true
		kind := answer.ObjectDimension
		if object.Measure {
			kind = answer.ObjectMetric
		}
		evidence.Objects = append(evidence.Objects, answer.ObjectEvidence{
			ObjectID: object.ObjectID, Kind: kind, Bound: true, Names: []string{object.Name},
		})
	}
	return evidence.Normalize()
}

// ArtifactWriter stores a verified conclusion against the evidence it cites.
type ArtifactWriter interface {
	AppendArtifact(
		ctx context.Context, identity store.Identity, reportID, componentID, evidenceID askdata.ID,
		artifact insight.InsightArtifact,
	) (insight.ArtifactRecord, error)
}

// NarrativeService writes and verifies conclusions for stored evidence.
//
// It is separate from Deriver because deriving facts and phrasing them are
// different responsibilities with different failure modes: derivation fails when
// data cannot be measured, narration fails when prose cannot be justified.
type NarrativeService struct {
	Model     insight.NarrativeModel
	Verifier  *answer.Verifier
	Artifacts ArtifactWriter
	// VerifierVersion and PolicyWordlistVersion must be the ones Verifier was
	// built with; the verifier rejects a narrative that claims any other.
	VerifierVersion       string
	PolicyWordlistVersion string
	NewID                 func() askdata.ID
}

// Generate writes a conclusion for one evidence record and stores it only if it
// verifies. The verifier report is returned either way so a caller can explain
// a rejection instead of reporting a generic failure.
func (service NarrativeService) Generate(
	ctx context.Context,
	identity store.Identity,
	reportID, componentID askdata.ID,
	evidence insight.EvidenceRecord,
	// objects are the governed objects this component's narrative may name.
	// They vary per component, so they are a parameter rather than service
	// state: one shared service must not hand every report the same catalog.
	objects []insight.MarkerObject,
) (insight.ArtifactRecord, answer.VerifyReport, error) {
	if service.Model == nil || service.Verifier == nil || service.Artifacts == nil || service.NewID == nil ||
		service.VerifierVersion == "" || service.PolicyWordlistVersion == "" {
		return insight.ArtifactRecord{}, answer.VerifyReport{}, errors.New("report narrative service is not configured")
	}
	resultEvidence, err := ResultEvidenceOf(evidence.Bundle)
	if err != nil {
		return insight.ArtifactRecord{}, answer.VerifyReport{}, err
	}
	model := service.Model
	if scoped, ok := model.(AINarrativeModel); ok {
		scoped.Objects = objects
		model = scoped
	}
	artifact, report, err := (insight.NarrativeGenerator{Model: model, Verifier: service.Verifier}).Generate(
		ctx, insight.NarrativeGenerationRequest{
			ArtifactID: service.NewID(), Evidence: evidence.Bundle,
			ResultEvidence:  resultEvidence,
			BindingEvidence: BindingEvidenceOf(evidence.Bundle, objects),
			// A dataset-sourced report carries no pinned semantic calendar, so
			// there is no time contract to cite against. The prompt forbids the
			// model from writing dates for exactly that reason.
			ResolvedTimeSpec:      compiler.ResolvedTimeSpec{},
			PromptVersion:         InsightPromptVersion,
			ModelPolicy:           InsightModelPolicy,
			VerifierVersion:       service.VerifierVersion,
			PolicyWordlistVersion: service.PolicyWordlistVersion,
		},
	)
	if err != nil {
		return insight.ArtifactRecord{}, report, err
	}
	record, err := service.Artifacts.AppendArtifact(ctx, identity, reportID, componentID, evidence.ID, artifact)
	return record, report, err
}
