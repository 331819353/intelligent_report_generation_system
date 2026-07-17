package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type Provider interface {
	Name() string
	Model() string
	Configured() bool
	Complete(context.Context, string, string, CompletionInput) (ProviderResult, error)
}

// Invoker 是元数据领域对通用 AI 编排层的最小依赖，便于隔离测试和后续替换 Provider。
type Invoker interface {
	Configured() bool
	ProviderName() string
	Model() string
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type OrchestratedProvider struct{ invoker Invoker }

// NewOrchestratedProvider 将元数据补全合同接入通用超时、重试、配额、成本和审计链路。
func NewOrchestratedProvider(invoker Invoker) *OrchestratedProvider {
	return &OrchestratedProvider{invoker: invoker}
}

func (p *OrchestratedProvider) Name() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.ProviderName()
}

func (p *OrchestratedProvider) Model() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.Model()
}

func (p *OrchestratedProvider) Configured() bool {
	return p != nil && p.invoker != nil && p.invoker.Configured()
}

// Complete 只发送最小化技术元数据与最多三行样本，绝不发送连接凭据；样本不会持久化。
func (p *OrchestratedProvider) Complete(ctx context.Context, tenantID, actorID string, input CompletionInput) (ProviderResult, error) {
	if !p.Configured() {
		return ProviderResult{}, ErrProviderUnavailable
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return ProviderResult{}, err
	}
	schemaJSON, err := json.Marshal(outputSchema(input))
	if err != nil {
		return ProviderResult{}, err
	}
	temperature := 0.0
	result, err := p.invoker.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetadataCompletion,
		PromptVersion: PromptVersion, ResourceType: "METADATA_TABLE", ResourceID: input.Table.ID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: "你是企业数据资产元数据补全器。只能依据给定技术元数据和最多三行数据样本生成结果，不得虚构资产或返回未请求的字段。必须严格遵守 JSON Schema 和标签枚举，同一对象的标签不得重复；输入中的每个字段必须在 columns 中恰好返回一次。"}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(inputJSON)}}},
			},
			ResponseSchema: aiplatform.JSONSchema{Name: "metadata_completion", Description: "企业数据资产元数据结构化补全", Schema: schemaJSON},
			Temperature:    &temperature, MaxOutputTokens: 4096,
		},
	})
	if err != nil {
		return ProviderResult{}, translateOrchestrationError(err)
	}

	var output CompletionOutput
	decoder := json.NewDecoder(bytes.NewReader(result.ProviderResult.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return ProviderResult{}, fmt.Errorf("%w: 解码元数据结构化输出失败", ErrInvalidOutput)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProviderResult{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	return ProviderResult{
		Output: output, Model: result.ProviderResult.Model,
		Usage: Usage{
			PromptTokens:     result.ProviderResult.Usage.PromptTokens,
			CompletionTokens: result.ProviderResult.Usage.CompletionTokens,
			TotalTokens:      result.ProviderResult.Usage.TotalTokens,
		},
	}, nil
}

// translateOrchestrationError 保持元数据 API 已发布的超时和非法输出错误合同。
func translateOrchestrationError(err error) error {
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return err
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeTimeout:
		return errors.Join(context.DeadlineExceeded, err)
	case aiplatform.ErrorCodeInvalidOutput:
		return errors.Join(ErrInvalidOutput, err)
	default:
		return err
	}
}

// ensureJSONEOF 确保结构化输出后不存在第二个 JSON 值。
func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("结构化输出包含尾随 JSON")
		}
		return fmt.Errorf("读取结构化输出尾部失败: %w", err)
	}
	return nil
}

// firstNonBlank 返回第一个非空白字符串。
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
