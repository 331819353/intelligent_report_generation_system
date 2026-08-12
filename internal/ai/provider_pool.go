package ai

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	ProviderSelectionPrimary    = "primary"
	ProviderSelectionRoundRobin = "round_robin"
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

// ModelProviderChainSelector exposes the ordered models after the primary.
// Domain workflows use it to restart a failed stateful operation from round
// one, with a fresh executor and a separate immutable audit record.
type ModelProviderChainSelector interface {
	FallbackModels() []string
}

// ModelProviderCatalog exposes the complete configured model order. Domain
// workflows use it only to choose a different, already allowlisted model after
// a structured-output failure; it never expands the provider allowlist.
type ModelProviderCatalog interface {
	Models() []string
}

// PrimaryFallbackProvider owns an ordered provider pool. Its selection mode is
// either fixed-primary or round-robin; explicit model routing never advances
// the round-robin cursor.
type PrimaryFallbackProvider struct {
	providers []Provider
	selection string
	next      atomic.Uint64
}

type ProviderEndpoint struct {
	Name            string
	BaseURL         string
	APIKey          string
	Models          []string
	ThinkingEnabled bool
	ReasoningEffort string
	ResponseFormat  string
	MaxOutputTokens int
}

// NewPrimaryFallbackProvider creates an ordered provider pool and drops nil or
// unconfigured entries.
func NewPrimaryFallbackProvider(providers ...Provider) *PrimaryFallbackProvider {
	return newProviderPool(ProviderSelectionPrimary, providers...)
}

// NewRoundRobinProvider distributes unpinned invocations equally across all
// configured providers. Retries remain pinned by Service to the selected model.
func NewRoundRobinProvider(providers ...Provider) *PrimaryFallbackProvider {
	return newProviderPool(ProviderSelectionRoundRobin, providers...)
}

func newProviderPool(selection string, providers ...Provider) *PrimaryFallbackProvider {
	configured := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil && provider.Configured() {
			configured = append(configured, provider)
		}
	}
	return &PrimaryFallbackProvider{
		providers: configured,
		selection: strings.ToLower(strings.TrimSpace(selection)),
	}
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

// NewMultiEndpointProviderPool supports independent DeepSeek, GLM and MiniMax
// credentials/endpoints while preserving the configured provider/model order.
func NewMultiEndpointProviderPool(
	endpoints []ProviderEndpoint,
	selection string,
	client *http.Client,
) Provider {
	providers := []Provider{}
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		for _, model := range endpoint.Models {
			key := strings.ToLower(
				strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/") +
					"\x00" + strings.TrimSpace(model),
			)
			if seen[key] {
				continue
			}
			seen[key] = true
			providers = append(
				providers,
				NewOpenAICompatibleProviderWithOptions(
					endpoint.BaseURL,
					endpoint.APIKey,
					model,
					ProviderOptions{
						ThinkingEnabled: endpoint.ThinkingEnabled,
						ReasoningEffort: endpoint.ReasoningEffort,
						ResponseFormat:  endpoint.ResponseFormat,
						MaxOutputTokens: endpoint.MaxOutputTokens,
					},
					client,
				),
			)
		}
	}
	if len(providers) == 1 {
		return providers[0]
	}
	if strings.EqualFold(selection, ProviderSelectionRoundRobin) {
		return NewRoundRobinProvider(providers...)
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
	if p.selection == ProviderSelectionRoundRobin && len(p.providers) > 1 {
		index := (p.next.Add(1) - 1) % uint64(len(p.providers))
		return p.providers[index]
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

func (p *PrimaryFallbackProvider) FallbackModels() []string {
	if p == nil || len(p.providers) < 2 {
		return []string{}
	}
	result := make([]string, 0, len(p.providers)-1)
	for _, provider := range p.providers[1:] {
		result = append(result, provider.Model())
	}
	return result
}

func (p *PrimaryFallbackProvider) Models() []string {
	if p == nil {
		return []string{}
	}
	result := make([]string, 0, len(p.providers))
	for _, provider := range p.providers {
		if model := strings.TrimSpace(provider.Model()); model != "" {
			result = append(result, model)
		}
	}
	return result
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
