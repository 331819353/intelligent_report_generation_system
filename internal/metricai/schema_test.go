package metricai

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type orchestrationStoreStub struct{}

func (orchestrationStoreStub) Start(context.Context, aiplatform.StartRequest) (aiplatform.RequestRecord, error) {
	return aiplatform.RequestRecord{ID: "request-1"}, nil
}

func TestProposalSchemaAllowsDraftEvidenceButKeepsDraftOutOfCandidateDefinition(t *testing.T) {
	root := proposalOutputSchema(retrievalWithModifiableDraft())
	properties := root["properties"].(map[string]any)
	targetIDs := properties["targetDatasetId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(targetIDs, testDatasetID) || !slices.Contains(targetIDs, testServiceDraftDatasetID) {
		t.Fatalf("target dataset enum must include published and draft datasets: %#v", targetIDs)
	}
	evidence := properties["retrievalEvidence"].(map[string]any)["items"].(map[string]any)
	evidenceIDs := evidence["properties"].(map[string]any)["datasetId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(evidenceIDs, testServiceDraftDatasetID) {
		t.Fatalf("evidence dataset enum excludes authorized draft: %#v", evidenceIDs)
	}

	definitions := root["$defs"].(map[string]any)
	metricDefinition := definitions["metricDefinition"].(map[string]any)
	candidateDatasetIDs := metricDefinition["properties"].(map[string]any)["datasetId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(candidateDatasetIDs, testDatasetID) || slices.Contains(candidateDatasetIDs, testServiceDraftDatasetID) {
		t.Fatalf("candidate dataset enum crossed the published boundary: %#v", candidateDatasetIDs)
	}
	expression := definitions["expression"].(map[string]any)
	fieldReference := expression["oneOf"].([]any)[0].(map[string]any)
	candidateFieldIDs := fieldReference["properties"].(map[string]any)["fieldId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(candidateFieldIDs, "field_amount") || slices.Contains(candidateFieldIDs, "draft_amount") {
		t.Fatalf("candidate expression field enum crossed the published boundary: %#v", candidateFieldIDs)
	}
}

func TestProposalSchemaKeepsAggregatedDraftAsEvidenceOnly(t *testing.T) {
	retrieval := retrievalWithModifiableDraft()
	aggregatedDatasetID := "77777777-7777-4777-8777-777777777777"
	aggregatedVersionID := "88888888-8888-4888-8888-888888888888"
	retrieval.ModifiableDraftDatasets = append(retrieval.ModifiableDraftDatasets, AuthorizedDataset{
		ID: aggregatedDatasetID, VersionID: aggregatedVersionID, VersionNo: 1,
		Name: "客户购买统计", Status: "DRAFT", DSLHash: strings.Repeat("c", 64),
		Aggregated: true, Manageable: true,
	})
	root := proposalOutputSchema(retrieval)
	properties := root["properties"].(map[string]any)
	targetIDs := properties["targetDatasetId"].(map[string]any)["enum"].([]string)
	if slices.Contains(targetIDs, aggregatedDatasetID) {
		t.Fatalf("aggregated draft leaked into action target enum: %#v", targetIDs)
	}
	evidence := properties["retrievalEvidence"].(map[string]any)["items"].(map[string]any)
	evidenceProperties := evidence["properties"].(map[string]any)
	evidenceDatasetIDs := evidenceProperties["datasetId"].(map[string]any)["enum"].([]string)
	evidenceSourceIDs := evidenceProperties["sourceId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(evidenceDatasetIDs, aggregatedDatasetID) || !slices.Contains(evidenceSourceIDs, aggregatedDatasetID) {
		t.Fatalf("aggregated draft should remain available as evidence: dataset=%#v source=%#v", evidenceDatasetIDs, evidenceSourceIDs)
	}
}

func TestProposalSchemaKeepsMappedDatasetsAsCreateDatasetEvidenceOnly(t *testing.T) {
	retrieval := mappedRetrievalContext()
	root := proposalOutputSchema(retrieval)
	properties := root["properties"].(map[string]any)
	strategies := properties["strategy"].(map[string]any)["enum"].([]string)
	if !slices.Contains(strategies, StrategyCreateDataset) {
		t.Fatalf("CREATE_DATASET missing from strategy enum: %#v", strategies)
	}
	targetIDs := properties["targetDatasetId"].(map[string]any)["enum"].([]string)
	if slices.Contains(targetIDs, testDatasetID) {
		t.Fatalf("mapped dataset leaked into a direct action target: %#v", targetIDs)
	}
	evidence := properties["retrievalEvidence"].(map[string]any)["items"].(map[string]any)
	evidenceIDs := evidence["properties"].(map[string]any)["datasetId"].(map[string]any)["enum"].([]string)
	if !slices.Contains(evidenceIDs, testDatasetID) {
		t.Fatalf("mapped dataset missing from CREATE_DATASET evidence: %#v", evidenceIDs)
	}
	request := validRequest()
	promptRequest, err := buildProviderRequest(request, analyzeMetricIntent(request), retrieval)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptRequest.Messages[0].Parts[0].Text, "retrieval.mappedDatasets/mappedFields") ||
		!strings.Contains(promptRequest.Messages[0].Parts[0].Text, "retrieval.atomicFacts") ||
		!strings.Contains(promptRequest.Messages[0].Parts[0].Text, "不能直接绑定报表") ||
		!strings.Contains(promptRequest.Messages[0].Parts[0].Text, "不得把这些内部标识当作说明文字") {
		t.Fatalf("system prompt is missing mapped safety or readability rules: %s", promptRequest.Messages[0].Parts[0].Text)
	}
}
func (orchestrationStoreStub) Complete(context.Context, string, string, aiplatform.CompletionRecord) error {
	return nil
}
func (orchestrationStoreStub) Fail(context.Context, string, string, aiplatform.FailureRecord) error {
	return nil
}

type orchestrationProviderStub struct{ content json.RawMessage }

func (orchestrationProviderStub) Name() string     { return "stub" }
func (orchestrationProviderStub) Model() string    { return "stub-model" }
func (orchestrationProviderStub) Configured() bool { return true }
func (s orchestrationProviderStub) Complete(context.Context, aiplatform.ProviderRequest) (aiplatform.ProviderResult, error) {
	return aiplatform.ProviderResult{
		Content: s.content, Model: "stub-model", FinishReason: "stop", RequestID: "provider-1",
		Usage: aiplatform.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func TestProposalSchemaPassesGenericOrchestrationValidation(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyDataGap, Summary: "授权上下文没有所需数据",
		RetrievalEvidence: []RetrievalEvidence{}, ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	content, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	providerRequest, err := buildProviderRequest(request, analyzeMetricIntent(request), RetrievalContext{Datasets: []AuthorizedDataset{}, Fields: []AuthorizedField{}, ExistingMetrics: []AuthorizedMetric{}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := aiplatform.NewService(orchestrationStoreStub{}, orchestrationProviderStub{content: content}, aiplatform.ServiceOptions{
		Timeout: time.Second, AttemptTimeout: time.Second, MaxAttempts: 1,
		BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Millisecond, MaxInputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Invoke(context.Background(), aiplatform.Invocation{
		TenantID: "tenant-1", ActorID: "actor-1", Purpose: aiplatform.PurposeMetricAuthoring,
		PromptVersion: PromptVersion, Request: providerRequest,
	})
	if err != nil {
		t.Fatalf("generic orchestration rejected metric proposal schema: %v", err)
	}
}
