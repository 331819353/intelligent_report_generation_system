package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type dimensionWherePolicyAIInvoker interface {
	Configured() bool
	Model() string
	Invoke(
		context.Context,
		aiplatform.Invocation,
	) (aiplatform.InvocationResult, error)
}

type DimensionWherePolicyDesigner interface {
	Design(
		context.Context,
		DimensionWherePolicyClaim,
	) (DimensionWherePolicyDecision, error)
}

type OrchestratedDimensionWherePolicyDesigner struct {
	ai      dimensionWherePolicyAIInvoker
	timeout time.Duration
}

func NewOrchestratedDimensionWherePolicyDesigner(
	ai dimensionWherePolicyAIInvoker,
	timeout time.Duration,
) *OrchestratedDimensionWherePolicyDesigner {
	return &OrchestratedDimensionWherePolicyDesigner{
		ai: ai, timeout: timeout,
	}
}

type dimensionWherePolicyOutput struct {
	DimensionFieldName string  `json:"dimensionFieldName"`
	Operator           string  `json:"operator"`
	Reason             string  `json:"reason"`
	Confidence         float64 `json:"confidence"`
}

func (d *OrchestratedDimensionWherePolicyDesigner) Design(
	ctx context.Context,
	claim DimensionWherePolicyClaim,
) (DimensionWherePolicyDecision, error) {
	if d == nil || d.ai == nil || !d.ai.Configured() ||
		strings.TrimSpace(claim.DimensionFieldName) == "" ||
		strings.TrimSpace(claim.DimensionDescription) == "" ||
		strings.TrimSpace(claim.MetricFieldID) == "" ||
		len(claim.SampleValues) == 0 {
		return DimensionWherePolicyDecision{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(map[string]any{
		"dimensionFieldName":   claim.DimensionFieldName,
		"dimensionDescription": claim.DimensionDescription,
		"sampleValues":         claim.SampleValues,
		"metricCode":           claim.MetricCode,
		"metricFieldName":      claim.MetricFieldID,
		"tableName":            claim.TableSchema + "." + claim.TableName,
	})
	if err != nil {
		return DimensionWherePolicyDecision{}, err
	}
	invokeCtx := ctx
	cancel := func() {}
	if d.timeout > 0 {
		invokeCtx, cancel = context.WithTimeout(ctx, d.timeout)
	}
	defer cancel()
	temperature := 0.0
	invocation := aiplatform.Invocation{
		TenantID:      claim.TenantID,
		ActorID:       claim.ActorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "dws-dimension-where-policy-v1",
		ResourceType:  "DWS_DIMENSION_WHERE_POLICY",
		ResourceID:    claim.ID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读DWS维度WHERE策略设计器。本次只判断一个正式维度。输入包含真实维度字段名、字段描述、非空规范值样本、指标字段和目标表。若每个单元格是一个原子枚举值，选择EQUALS；只有当样本明确显示一个单元格由逗号、分号或竖线分隔的多个业务标签组成时才选择CONTAINS。不得输出SQL、不得改写字段名、不得发明值。返回可审计的简短原因和置信度。`,
					}},
				},
				{
					Role: aiplatform.MessageRoleUser,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: string(payload),
					}},
				},
			},
			ResponseSchema: aiplatform.JSONSchema{
				Name: "dws_dimension_where_policy",
				Schema: json.RawMessage(`{
					"type":"object",
					"additionalProperties":false,
					"required":[
						"dimensionFieldName","operator","reason","confidence"
					],
					"properties":{
						"dimensionFieldName":{
							"type":"string","minLength":1,"maxLength":256
						},
						"operator":{"enum":["EQUALS","CONTAINS"]},
						"reason":{
							"type":"string","minLength":1,"maxLength":500
						},
						"confidence":{
							"type":"number","minimum":0.8,"maximum":1
						}
					}
				}`),
			},
			Temperature:     &temperature,
			MaxOutputTokens: 500,
		},
	}
	result, err := d.ai.Invoke(invokeCtx, invocation)
	decision, validationErr := decodeDimensionWherePolicyDecision(
		claim, result,
	)
	if err == nil && validationErr == nil {
		return decision, nil
	}
	if err == nil {
		err = validationErr
	}
	if !dimensionWherePolicyRepairEligible(invokeCtx, err) {
		return DimensionWherePolicyDecision{}, err
	}

	invalidContent := result.ProviderResult.Content
	if content, _, ok := aiplatform.InvalidOutputDetails(err); ok {
		invalidContent = content
	}
	repairInvocation := invocation
	if fallbackModel := configuredDimensionWhereFallbackModel(
		d.ai.Model(),
	); fallbackModel != "" {
		repairInvocation.PreferredModel = fallbackModel
	}
	repairMessages := append(
		[]aiplatform.Message(nil),
		invocation.Request.Messages...,
	)
	if len(invalidContent) > 0 {
		repairMessages = append(
			repairMessages,
			aiplatform.Message{
				Role: aiplatform.MessageRoleAssistant,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText,
					Text: string(invalidContent),
				}},
			},
		)
	}
	repairMessages = append(
		repairMessages,
		aiplatform.Message{
			Role: aiplatform.MessageRoleUser,
			Parts: []aiplatform.ContentPart{{
				Type: aiplatform.ContentTypeText,
				Text: `上一份输出未通过严格校验。请重新判断：字段名必须与输入完全一致；operator只能是EQUALS或CONTAINS；只有样本单元格明确含逗号、分号或竖线分隔的多个标签时才能用CONTAINS，否则必须用EQUALS；reason必须简短非空；confidence必须在0.8到1之间。只返回符合既定JSON Schema的对象。`,
			}},
		},
	)
	repairInvocation.Request.Messages = repairMessages
	repaired, repairErr := d.ai.Invoke(invokeCtx, repairInvocation)
	if repairErr != nil {
		return DimensionWherePolicyDecision{}, repairErr
	}
	return decodeDimensionWherePolicyDecision(claim, repaired)
}

func decodeDimensionWherePolicyDecision(
	claim DimensionWherePolicyClaim,
	result aiplatform.InvocationResult,
) (DimensionWherePolicyDecision, error) {
	var output dimensionWherePolicyOutput
	if err := json.Unmarshal(
		result.ProviderResult.Content, &output,
	); err != nil {
		return DimensionWherePolicyDecision{}, err
	}
	if output.DimensionFieldName != claim.DimensionFieldName {
		return DimensionWherePolicyDecision{}, ErrInvalidRequest
	}
	decision := DimensionWherePolicyDecision{
		PredicateOperator: strings.ToUpper(strings.TrimSpace(
			output.Operator,
		)),
		LLMModel:   strings.TrimSpace(result.ProviderResult.Model),
		LLMReason:  strings.TrimSpace(output.Reason),
		Confidence: output.Confidence,
	}
	if err := validDimensionWherePolicyDecision(
		claim, decision,
	); err != nil {
		return DimensionWherePolicyDecision{}, err
	}
	return decision, nil
}

func configuredDimensionWhereFallbackModel(models string) string {
	parts := strings.Split(models, ",")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func dimensionWherePolicyRepairEligible(
	ctx context.Context,
	err error,
) bool {
	if err == nil || ctx.Err() != nil ||
		errors.Is(err, aiplatform.ErrTenantAIForbidden) ||
		errors.Is(err, aiplatform.ErrQuotaExceeded) {
		return false
	}
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return errors.Is(err, ErrInvalidRequest)
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeCanceled,
		aiplatform.ErrorCodeInvalidRequest,
		aiplatform.ErrorCodeAuthentication:
		return false
	default:
		return true
	}
}

type DimensionWhereDecisionWorker struct {
	store    DimensionWhereDecisionBuildStore
	designer DimensionWherePolicyDesigner
}

func NewDimensionWhereDecisionWorker(
	store DimensionWhereDecisionBuildStore,
	designer DimensionWherePolicyDesigner,
) *DimensionWhereDecisionWorker {
	return &DimensionWhereDecisionWorker{
		store: store, designer: designer,
	}
}

func (w *DimensionWhereDecisionWorker) TenantIDs(
	ctx context.Context,
) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListDimensionDecisionTenantIDs(ctx)
}

func (w *DimensionWhereDecisionWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if w == nil || w.store == nil || w.designer == nil ||
		!validUUID(tenantID) || !validWorkerName(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	reconciled, err := w.store.ReconcileDimensionWherePolicies(
		ctx, tenantID,
	)
	if err != nil {
		return false, err
	}
	claim, err := w.store.ClaimDimensionWherePolicy(
		ctx, tenantID, workerID, lease,
	)
	if err != nil {
		return false, err
	}
	if claim != nil {
		decision, designErr := w.designer.Design(ctx, *claim)
		if designErr == nil {
			designErr = w.store.CompleteDimensionWherePolicy(
				ctx, *claim, decision,
			)
		}
		if designErr == nil {
			return true, nil
		}
		code := dimensionWherePolicyFailureCode(designErr)
		failErr := w.store.FailDimensionWherePolicy(
			context.WithoutCancel(ctx), *claim, code,
		)
		if failErr != nil && !errors.Is(failErr, ErrRefreshLeaseLost) {
			return true, errors.Join(designErr, failErr)
		}
		return true, designErr
	}
	materialized, err := w.store.MaterializeDimensionWhereDecisions(
		ctx, tenantID, 5000,
	)
	if err != nil {
		return false, err
	}
	removed, err := w.store.CleanupDimensionWhereDecisions(ctx, tenantID)
	if err != nil {
		return materialized > 0, err
	}
	return reconciled > 0 || materialized > 0 || removed > 0, nil
}

func dimensionWherePolicyFailureCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "LLM_TIMEOUT"
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		return "LLM_QUOTA_EXCEEDED"
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		return "LLM_FORBIDDEN"
	case errors.Is(err, ErrInvalidRequest):
		return "LLM_OUTPUT_INVALID"
	default:
		return "LLM_PROVIDER_FAILED"
	}
}
