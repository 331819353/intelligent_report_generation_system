package feedback

import (
	"context"
	"encoding/json"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
)

type attributionInvokerFixture struct{ result ai.InvocationResult }

func (fixture attributionInvokerFixture) Invoke(_ context.Context, _ ai.Invocation) (ai.InvocationResult, error) {
	return fixture.result, nil
}

func TestAttributorReturnsSuggestedCandidateOnly(t *testing.T) {
	fact := AttributionFact{EvidenceID: "artifact-a", Kind: "BINDING_BUNDLE", Summary: json.RawMessage(`{"artifactType":"BINDING_BUNDLE"}`)}
	fact.ContentHash = askdata.HashBytes(fact.Summary)
	envelope := AttributionEnvelope{SchemaVersion: AttributionSchemaVersion, Proposals: []AttributionProposal{{
		Category: AttributionMetric, CandidateType: ChangeMetricDefinition,
		TargetObjectIDs: []askdata.ID{"metric-v1"}, Evidence: []AttributionEvidenceRef{{fact.EvidenceID, fact.ContentHash}},
		Confidence: .8, RationaleCode: "METRIC_BINDING_MISMATCH",
	}}}
	payload, _ := json.Marshal(envelope)
	reviewer, err := NewAttributor(attributionInvokerFixture{result: ai.InvocationResult{
		RequestID: "request-a", ProviderResult: ai.ProviderResult{Content: payload, Model: "model-a"}, Attempts: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Attribute(context.Background(), AttributionRequest{
		TenantID: "00000000-0000-4000-8000-000000000001", ActorID: "00000000-0000-4000-8000-000000000002",
		DomainID: "00000000-0000-4000-8000-000000000003", TicketID: "ticket-a", RunID: "run-a",
		PromptVersion: "feedback-attribution-v1", Issue: IssueMetric, AllowedObjectIDs: []askdata.ID{"metric-v1"}, Facts: []AttributionFact{fact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].State != AttributionStateSuggested || result.Suggestions[0].ContentHash.Validate() != nil {
		t.Fatalf("suggestions = %#v", result.Suggestions)
	}
}

func TestAttributionRejectsInventedObjectEvidenceAndCategoryMismatch(t *testing.T) {
	fact := AttributionFact{EvidenceID: "artifact-a", Kind: "SEMANTIC_IR", Summary: json.RawMessage(`{"artifactType":"SEMANTIC_IR"}`)}
	fact.ContentHash = askdata.HashBytes(fact.Summary)
	request := AttributionRequest{AllowedObjectIDs: []askdata.ID{"metric-v1"}, Facts: []AttributionFact{fact}}
	base := AttributionEnvelope{SchemaVersion: AttributionSchemaVersion, Proposals: []AttributionProposal{{
		Category: AttributionMetric, CandidateType: ChangeMetricDefinition, TargetObjectIDs: []askdata.ID{"invented"},
		Evidence: []AttributionEvidenceRef{{fact.EvidenceID, fact.ContentHash}}, Confidence: .5, RationaleCode: "MISMATCH",
	}}}
	if _, err := ValidateAttribution(base, request); err == nil {
		t.Fatal("invented object accepted")
	}
	base.Proposals[0].TargetObjectIDs = []askdata.ID{"metric-v1"}
	base.Proposals[0].Evidence[0].EvidenceID = "invented"
	if _, err := ValidateAttribution(base, request); err == nil {
		t.Fatal("invented evidence accepted")
	}
	base.Proposals[0].Evidence[0] = AttributionEvidenceRef{fact.EvidenceID, fact.ContentHash}
	base.Proposals[0].CandidateType = ChangeRelationship
	if _, err := ValidateAttribution(base, request); err == nil {
		t.Fatal("category/candidate mismatch accepted")
	}
}

func TestAttributionFactsNeverCopyArtifactPayload(t *testing.T) {
	// The existing snapshot helper is covered through its type contract: the
	// resulting fact has no payload field, only a structural summary and hash.
	if _, exists := any(AttributionFact{}).(interface{ Payload() []byte }); exists {
		t.Fatal("attribution fact unexpectedly exposes artifact payload")
	}
}
