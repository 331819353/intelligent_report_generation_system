package reportai

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report/operation"
)

//go:embed schemas/report-plan-v1.schema.json
var reportPlanSchema json.RawMessage

//go:embed schemas/report-context-v1.schema.json
var reportContextSchema json.RawMessage

//go:embed schemas/report-operation-v1.schema.json
var reportOperationSchema json.RawMessage

//go:embed schemas/report-publish-review-v1.schema.json
var reportPublishReviewSchema json.RawMessage

//go:embed schemas/report-card-binding-v1.schema.json
var reportCardBindingSchema json.RawMessage

// The embedded schema files double as public API documents and therefore carry
// "$schema"/"$id" metadata. The AI platform's strict structured-output contract
// rejects unknown top-level keywords, so the model-facing copies are stripped
// of that metadata once at start-up. Structural rules stay identical.
func init() {
	reportPlanSchema = aiFacingSchema(reportPlanSchema)
	reportContextSchema = aiFacingSchema(reportContextSchema)
	reportOperationSchema = aiFacingSchema(reportOperationSchema)
	reportPublishReviewSchema = aiFacingSchema(reportPublishReviewSchema)
	reportCardBindingSchema = aiFacingSchema(reportCardBindingSchema)
}

func aiFacingSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return raw
	}
	delete(root, "$schema")
	delete(root, "$id")
	stripped, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return stripped
}

type invocationIdentityKey struct{}

type InvocationIdentity struct {
	TenantID askdata.ID
	ActorID  askdata.ID
	ReportID askdata.ID
}

func WithInvocationIdentity(ctx context.Context, identity InvocationIdentity) context.Context {
	return context.WithValue(ctx, invocationIdentityKey{}, identity)
}

type OrchestratedGenerator struct{ AI *aiplatform.Service }

func NewOrchestratedGenerator(aiService *aiplatform.Service) (*OrchestratedGenerator, error) {
	if aiService == nil {
		return nil, errors.New("report AI service is required")
	}
	return &OrchestratedGenerator{AI: aiService}, nil
}

func (generator *OrchestratedGenerator) GenerateReportPlan(ctx context.Context, request PlanRequest) (Plan, error) {
	identity, err := invocationIdentity(ctx)
	if err != nil || generator == nil || generator.AI == nil {
		return Plan{}, errors.New("report AI plan generator is unavailable")
	}
	request.AllowedFieldNames = normalizedStrings(request.AllowedFieldNames)
	request.AllowedComponents = normalizedStrings(request.AllowedComponents)
	request.TemplateVersions = normalizedStrings(request.TemplateVersions)
	if strings.TrimSpace(request.Intent) == "" || len(request.AllowedFieldNames) > 2000 ||
		len(request.AllowedComponents) > 100 || len(request.AllowedMethods) > 100 {
		return Plan{}, errors.New("report AI plan request exceeds safe bounds")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Plan{}, err
	}
	result, err := generator.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
		Purpose: aiplatform.PurposeReportGeneration, PromptVersion: "report-plan-v1",
		ResourceType: "REPORT", ResourceID: string(identity.ReportID),
		Request: structuredRequest(
			"You create a report plan only from the supplied allowlists. Never emit SQL, data values, fields, components, methods, or template versions outside those lists.",
			payload, "report_plan_v1", reportPlanSchema,
		),
	})
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal(result.ProviderResult.Content, &plan); err != nil {
		return Plan{}, fmt.Errorf("decode report AI plan: %w", err)
	}
	return plan, nil
}

func (generator *OrchestratedGenerator) SelectDataContext(ctx context.Context, request DataContextSelectionRequest) (DataContextSelection, error) {
	identity, err := invocationIdentity(ctx)
	if err != nil || generator == nil || generator.AI == nil || strings.TrimSpace(request.Intent) == "" ||
		len(request.Candidates) == 0 || len(request.Candidates) > 30 {
		return DataContextSelection{}, errors.New("report AI data context selector is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > 512<<10 {
		return DataContextSelection{}, errors.New("report AI data context request exceeds safe bounds")
	}
	result, err := generator.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
		Purpose: aiplatform.PurposeReportGeneration, PromptVersion: "report-context-v1",
		ResourceType: "REPORT", ResourceID: string(identity.ReportID),
		Request: structuredRequest(
			"Choose exactly one governed dataContextId from candidates for the user's report intent. Base the choice only on candidate names, descriptions and allowed fields. Return a concise report name and rationale. Never emit SQL or raw data.",
			payload, "report_context_v1", reportContextSchema,
		),
	})
	if err != nil {
		return DataContextSelection{}, err
	}
	var selection DataContextSelection
	if err := json.Unmarshal(result.ProviderResult.Content, &selection); err != nil {
		return DataContextSelection{}, fmt.Errorf("decode report AI data context selection: %w", err)
	}
	return selection, nil
}

func (generator *OrchestratedGenerator) GenerateScopedOperations(ctx context.Context, request ScopedContext) (operation.Bundle, error) {
	identity, err := invocationIdentity(ctx)
	if err != nil || generator == nil || generator.AI == nil {
		return operation.Bundle{}, errors.New("report AI scoped editor is unavailable")
	}
	if request.AIRunID.Validate() != nil || request.BaseRevision < 0 || strings.TrimSpace(request.Intent) == "" ||
		len(request.AllowedOperations) == 0 || len(request.AllowedFields) > 2000 || len(request.Selection) == 0 {
		return operation.Bundle{}, errors.New("report AI scoped edit request exceeds safe bounds")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return operation.Bundle{}, err
	}
	result, err := generator.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
		Purpose: aiplatform.PurposeReportBlockEdit, PromptVersion: "report-scoped-edit-v1",
		ResourceType: "REPORT", ResourceID: string(identity.ReportID),
		Request: structuredRequest(
			"Return one AI Report Operation bundle for the supplied selection only. Use only allowedOperations and allowedFields. Preserve reportId, baseRevision, aiRunId and scope exactly. Never emit SQL or modify anything outside the selection.",
			payload, "report_operation_v1", reportOperationSchema,
		),
	})
	if err != nil {
		return operation.Bundle{}, err
	}
	var bundle operation.Bundle
	if err := json.Unmarshal(result.ProviderResult.Content, &bundle); err != nil {
		return operation.Bundle{}, fmt.Errorf("decode report AI operations: %w", err)
	}
	return bundle, nil
}

func (generator *OrchestratedGenerator) ReviewPublication(ctx context.Context, request PublishReviewRequest) (PublishReview, error) {
	identity, err := invocationIdentity(ctx)
	if err != nil || generator == nil || generator.AI == nil {
		return PublishReview{}, errors.New("report AI publication reviewer is unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > 256<<10 {
		return PublishReview{}, errors.New("report AI publication review request exceeds safe bounds")
	}
	result, err := generator.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
		Purpose: aiplatform.PurposeReportGeneration, PromptVersion: "report-publish-review-v1",
		ResourceType: "REPORT", ResourceID: string(identity.ReportID),
		Request: structuredRequest(
			"You are the cognitive reviewer for report publication. Explain only the supplied deterministic gates, issue codes, pinned dependency references and aggregate impact counts. Never invent raw data, SQL, users or dependencies. BLOCK when blockerCodes is non-empty, CONDITIONAL when only warningCodes is non-empty, otherwise ALLOW. Return one risk for every supplied blocker or warning code and no other risks. Human reviewers make the final decision.",
			payload, "report_publish_review_v1", reportPublishReviewSchema,
		),
	})
	if err != nil {
		return PublishReview{}, err
	}
	var review PublishReview
	if err := json.Unmarshal(result.ProviderResult.Content, &review); err != nil {
		return PublishReview{}, fmt.Errorf("decode report AI publication review: %w", err)
	}
	return review, nil
}

// InvocationIdentityFrom exposes the identity carried on ctx so callers
// assembling their own generators can pass it to the AI boundary.
func InvocationIdentityFrom(ctx context.Context) (InvocationIdentity, error) {
	return invocationIdentity(ctx)
}

func invocationIdentity(ctx context.Context) (InvocationIdentity, error) {
	identity, ok := ctx.Value(invocationIdentityKey{}).(InvocationIdentity)
	if !ok || identity.TenantID.Validate() != nil || identity.ActorID.Validate() != nil || identity.ReportID.Validate() != nil {
		return InvocationIdentity{}, errors.New("report AI invocation identity is missing")
	}
	return identity, nil
}

// SuggestCardBinding asks the model to pick dimensions and measures for one
// card from the governed field catalog; the answer is validated against the
// same catalog and contract before it is returned.
func (generator *OrchestratedGenerator) SuggestCardBinding(ctx context.Context, request CardBindingRequest) (CardBindingSuggestion, error) {
	identity, err := invocationIdentity(ctx)
	if err != nil || generator == nil || generator.AI == nil {
		return CardBindingSuggestion{}, errors.New("report AI card binding is unavailable")
	}
	if err := ValidateCardBindingRequest(request); err != nil {
		return CardBindingSuggestion{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return CardBindingSuggestion{}, err
	}
	result, err := generator.AI.Invoke(ctx, aiplatform.Invocation{
		TenantID: string(identity.TenantID), ActorID: string(identity.ActorID),
		Purpose: aiplatform.PurposeReportBlockEdit, PromptVersion: "report-card-binding-v1",
		ResourceType: "REPORT", ResourceID: string(identity.ReportID),
		Request: structuredRequest(cardBindingPrompt, payload, "report_card_binding_v1", reportCardBindingSchema),
	})
	if err != nil {
		return CardBindingSuggestion{}, err
	}
	var suggestion CardBindingSuggestion
	if err := json.Unmarshal(result.ProviderResult.Content, &suggestion); err != nil {
		return CardBindingSuggestion{}, fmt.Errorf("decode report AI card binding: %w", err)
	}
	return ValidateCardBindingSuggestion(request, suggestion)
}

func structuredRequest(system string, payload []byte, schemaName string, schema json.RawMessage) aiplatform.ProviderRequest {
	temperature := 0.0
	return aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: system}}},
			{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(payload)}}},
		},
		ResponseSchema: aiplatform.JSONSchema{Name: schemaName, Schema: schema},
		Temperature:    &temperature, MaxOutputTokens: 12_000,
	}
}

func normalizedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}

var _ PlanGenerator = (*OrchestratedGenerator)(nil)
var _ ScopedEditGenerator = (*OrchestratedGenerator)(nil)
var _ DataContextSelector = (*OrchestratedGenerator)(nil)
var _ PublishReviewGenerator = (*OrchestratedGenerator)(nil)
