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

//go:embed schemas/report-operation-v1.schema.json
var reportOperationSchema json.RawMessage

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

func invocationIdentity(ctx context.Context) (InvocationIdentity, error) {
	identity, ok := ctx.Value(invocationIdentityKey{}).(InvocationIdentity)
	if !ok || identity.TenantID.Validate() != nil || identity.ActorID.Validate() != nil || identity.ReportID.Validate() != nil {
		return InvocationIdentity{}, errors.New("report AI invocation identity is missing")
	}
	return identity, nil
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
