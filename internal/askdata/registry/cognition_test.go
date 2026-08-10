package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
)

type assetReviewInvoker struct {
	invocation ai.Invocation
	result     ai.InvocationResult
}

func (invoker *assetReviewInvoker) Invoke(
	_ context.Context,
	invocation ai.Invocation,
) (ai.InvocationResult, error) {
	invoker.invocation = invocation
	return invoker.result, nil
}

func TestAssetSuggestionReviewerValidatesAllKindsAndCannotPublish(t *testing.T) {
	request, evidence := validAssetReviewRequest()
	suggestions := validAssetSuggestions(t, request, evidence)
	raw, err := json.Marshal(AssetSuggestionEnvelope{
		SchemaVersion: AssetSuggestionSchemaVersion, Suggestions: suggestions,
	})
	if err != nil {
		t.Fatal(err)
	}
	invoker := &assetReviewInvoker{result: ai.InvocationResult{
		RequestID: "ai-review-1", Attempts: 1, CostMicros: 42,
		ProviderResult: ai.ProviderResult{Content: raw, Model: "fixture-model"},
	}}
	reviewer, err := NewAssetSuggestionReviewer(invoker)
	if err != nil {
		t.Fatalf("NewAssetSuggestionReviewer() error = %v", err)
	}
	result, err := reviewer.Review(context.Background(), request)
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if len(result.Suggestions) != 5 || result.AIRequestID != "ai-review-1" ||
		invoker.invocation.Purpose != ai.PurposeSemanticQuestion ||
		invoker.invocation.ResourceType != "SEMANTIC_ASSET_REVIEW" {
		t.Fatalf("review result/invocation = %+v / %+v", result, invoker.invocation)
	}
	for _, suggestion := range result.Suggestions {
		if suggestion.State != AssetSuggestionState || suggestion.State == "PUBLISHED" ||
			suggestion.State == "CERTIFIED" || suggestion.ContentHash.Validate() != nil {
			t.Fatalf("validated suggestion = %+v", suggestion)
		}
	}
	serialized, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), `"state":"PUBLISHED"`) ||
		strings.Contains(string(serialized), `"state":"CERTIFIED"`) {
		t.Fatalf("review crossed publication boundary: %s", serialized)
	}
	if len(invoker.invocation.Request.Messages) != 2 ||
		!strings.Contains(invoker.invocation.Request.Messages[0].Parts[0].Text, "不得发布") ||
		!strings.Contains(invoker.invocation.Request.Messages[1].Parts[0].Text, `"executable":false`) {
		t.Fatalf("bounded review messages = %+v", invoker.invocation.Request.Messages)
	}
}

func TestAssetSuggestionReviewerRejectsEvidenceDriftAndAuthorityFacts(t *testing.T) {
	request, evidence := validAssetReviewRequest()
	suggestions := validAssetSuggestions(t, request, evidence)

	t.Run("evidence drift", func(t *testing.T) {
		value := suggestions[0]
		value.Evidence[0].ContentHash = askdata.HashBytes([]byte("drift"))
		_, err := ValidateAssetSuggestions(AssetSuggestionEnvelope{
			SchemaVersion: AssetSuggestionSchemaVersion, Suggestions: []AssetSuggestion{value},
		}, request, request.Facts)
		if err == nil || !strings.Contains(err.Error(), "changed evidence") {
			t.Fatalf("evidence drift error = %v", err)
		}
	})

	t.Run("metric authority fact", func(t *testing.T) {
		var input MetricVersionDraftInput
		if err := json.Unmarshal([]byte(suggestions[0].PayloadJSON), &input); err != nil {
			t.Fatal(err)
		}
		input.Additivity = FullyAdditive
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		value := suggestions[0]
		value.PayloadJSON = string(payload)
		_, err = ValidateAssetSuggestions(AssetSuggestionEnvelope{
			SchemaVersion: AssetSuggestionSchemaVersion, Suggestions: []AssetSuggestion{value},
		}, request, request.Facts)
		if err == nil || !strings.Contains(err.Error(), "cannot confirm additivity") {
			t.Fatalf("authority fact error = %v", err)
		}
	})
}

func validAssetReviewRequest() (AssetReviewRequest, AssetSuggestionEvidence) {
	payload := json.RawMessage(`{"candidateCount":5,"source":"published-dataset"}`)
	hash := askdata.HashBytes(payload)
	evidence := AssetSuggestionEvidence{EvidenceID: "asset-evidence-1", ContentHash: hash}
	return AssetReviewRequest{
		TenantID: uuid.NewString(), ActorID: uuid.NewString(), DomainID: uuid.NewString(),
		PromptVersion: "semantic-asset-review-v1", ResourceID: "import-review-1",
		Facts: []AssetReviewFact{{
			EvidenceID: evidence.EvidenceID, Kind: "ASSET_EVIDENCE",
			ContentHash: hash, Payload: payload,
		}},
	}, evidence
}

func validAssetSuggestions(
	t *testing.T,
	request AssetReviewRequest,
	evidence AssetSuggestionEvidence,
) []AssetSuggestion {
	t.Helper()
	metricID, modelID, measureID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	metric := MetricVersionDraftInput{
		VersionedDraftInput: VersionedDraftInput{ObjectID: metricID, VersionNo: 1, OwnerID: request.ActorID},
		MetricID:            metricID, SemanticModelVersionID: modelID,
		FormulaAST:        json.RawMessage(`{"type":"MEASURE_REF","measureId":"` + measureID + `"}`),
		DefaultFiltersAST: json.RawMessage(`{"type":"TRUE"}`), Unit: "CURRENCY", TimeGrain: "NONE",
		ZeroDenominatorPolicy: ZeroDenominatorNull, DisplayPrecision: 2,
		AdditivitySuggestion: NonAdditive, NullPolicy: "PRESERVE",
		MeasureVersionIDs: []string{measureID},
	}
	dimension := DimensionDraftInput{
		VersionedDraftInput:    VersionedDraftInput{ObjectID: uuid.NewString(), VersionNo: 1, OwnerID: request.ActorID},
		SemanticModelVersionID: modelID, LogicalFieldID: "region_code", Code: "region",
		Name: "区域", Kind: DimensionCategorical, Sensitivity: SensitivityInternal,
		MemberIndexPolicy: MemberIndexFull,
	}
	relationship := RelationshipDraftInput{
		VersionedDraftInput: VersionedDraftInput{ObjectID: uuid.NewString(), VersionNo: 1, OwnerID: request.ActorID},
		LeftModelVersionID:  modelID, RightModelVersionID: uuid.NewString(),
		Type: RelationshipModelJoin, JoinType: JoinInner, Cardinality: CardinalityManyToOne,
		JoinAST:      json.RawMessage(`{"type":"EQUAL","leftFieldId":"customer_id","rightFieldId":"customer_id"}`),
		FanoutPolicy: FanoutSafe,
	}
	alias := aliasSuggestionPayload{TargetVersionID: metricID, Alias: "成交金额"}
	conflict := conflictSuggestionPayload{
		Code: "METRIC_NAME_CONFLICT", ObjectVersionIDs: []string{metricID, uuid.NewString()},
	}
	values := []struct {
		kind    AssetSuggestionType
		payload any
	}{
		{AssetSuggestionMetric, metric}, {AssetSuggestionDimension, dimension},
		{AssetSuggestionAlias, alias}, {AssetSuggestionRelationship, relationship},
		{AssetSuggestionConflict, conflict},
	}
	result := make([]AssetSuggestion, len(values))
	for index, value := range values {
		raw, err := json.Marshal(value.payload)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = AssetSuggestion{
			Type: value.kind, PayloadJSON: string(raw), Evidence: []AssetSuggestionEvidence{evidence},
		}
	}
	return result
}
