package ai

import (
	"context"
	"net/http"
	"strings"
)

// ProviderSelector lets the orchestration service pin the primary provider for
// the complete lifetime of an audited invocation, including retries.
type ProviderSelector interface {
	SelectProvider() Provider
}

// ModelProviderSelector resolves an explicitly requested fallback model without
// mixing providers inside one immutable audit record.
type ModelProviderSelector interface {
	SelectProviderModel(string) Provider
	FallbackModel() string
}

// PrimaryFallbackProvider always selects the first configured model by default.
// The second configured model is available only through an explicit fallback
// request from a domain workflow after the primary call fails validation or
// reaches its failover deadline.
type PrimaryFallbackProvider struct {
	providers []Provider
}

// NewPrimaryFallbackProvider creates an ordered provider pool and drops nil or
// unconfigured entries.
func NewPrimaryFallbackProvider(providers ...Provider) *PrimaryFallbackProvider {
	configured := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil && provider.Configured() {
			configured = append(configured, provider)
		}
	}
	return &PrimaryFallbackProvider{providers: configured}
}

// NewOpenAICompatibleProviderPool creates one compatible provider per model.
func NewOpenAICompatibleProviderPool(
	baseURL, apiKey string,
	models []string,
	client *http.Client,
) Provider {
	providers := make([]Provider, 0, len(models))
	for _, model := range models {
		providers = append(
			providers,
			NewOpenAICompatibleProvider(baseURL, apiKey, model, client),
		)
	}
	if len(providers) == 1 {
		return providers[0]
	}
	return NewPrimaryFallbackProvider(providers...)
}

func (p *PrimaryFallbackProvider) Name() string {
	if p == nil || len(p.providers) == 0 {
		return ""
	}
	return p.providers[0].Name()
}

func (p *PrimaryFallbackProvider) Model() string {
	if p == nil {
		return ""
	}
	models := make([]string, 0, len(p.providers))
	for _, provider := range p.providers {
		models = append(models, provider.Model())
	}
	return strings.Join(models, ",")
}

func (p *PrimaryFallbackProvider) Configured() bool {
	return p != nil && len(p.providers) > 0
}

func (p *PrimaryFallbackProvider) SelectProvider() Provider {
	if !p.Configured() {
		return nil
	}
	return p.providers[0]
}

func (p *PrimaryFallbackProvider) SelectProviderModel(model string) Provider {
	model = strings.TrimSpace(model)
	if model == "" {
		return p.SelectProvider()
	}
	for _, provider := range p.providers {
		if strings.EqualFold(provider.Model(), model) {
			return provider
		}
	}
	return nil
}

func (p *PrimaryFallbackProvider) FallbackModel() string {
	if p == nil || len(p.providers) < 2 {
		return ""
	}
	return p.providers[1].Model()
}

func (p *PrimaryFallbackProvider) Complete(
	ctx context.Context,
	request ProviderRequest,
) (ProviderResult, error) {
	provider := p.SelectProvider()
	if provider == nil {
		return ProviderResult{}, newProviderError(
			ErrorCodeProviderUnavailable,
			"AI provider is not configured",
			0,
			false,
			0,
			nil,
		)
	}
	return provider.Complete(ctx, request)
}
