package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type poolTestProvider struct {
	model  string
	calls  int
	errors []error
}

func (*poolTestProvider) Name() string     { return "test-provider" }
func (p *poolTestProvider) Model() string  { return p.model }
func (*poolTestProvider) Configured() bool { return true }
func (p *poolTestProvider) Complete(
	_ context.Context,
	_ ProviderRequest,
) (ProviderResult, error) {
	p.calls++
	if index := p.calls - 1; index < len(p.errors) && p.errors[index] != nil {
		return ProviderResult{}, p.errors[index]
	}
	return ProviderResult{
		Content: json.RawMessage(`{}`),
		Model:   p.model,
		Usage:   Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

func TestPrimaryFallbackProviderDefaultsToPrimaryAndResolvesFallback(t *testing.T) {
	first := &poolTestProvider{model: "MiniMax-M2"}
	second := &poolTestProvider{model: "deepseek-v3"}
	pool := NewPrimaryFallbackProvider(first, second)
	if !pool.Configured() || pool.Model() != "MiniMax-M2,deepseek-v3" {
		t.Fatalf("pool=%#v model=%q", pool, pool.Model())
	}
	for index := 0; index < 4; index++ {
		if got := pool.SelectProvider(); got != first {
			t.Fatalf("selection %d = %#v, want primary %#v", index, got, first)
		}
	}
	if pool.FallbackModel() != "deepseek-v3" ||
		pool.SelectProviderModel("DEEPSEEK-V3") != second ||
		pool.SelectProviderModel("unknown") != nil {
		t.Fatalf("fallback resolution failed: %#v", pool)
	}
}

func TestServicePinsPrimaryAcrossRetriesAndAuditsExplicitFallbackSeparately(t *testing.T) {
	retryable := newProviderError(
		ErrorCodeRateLimited,
		"模型限流",
		429,
		true,
		0,
		nil,
	)
	first := &poolTestProvider{
		model:  "MiniMax-M2",
		errors: []error{retryable},
	}
	second := &poolTestProvider{model: "deepseek-v3"}
	pool := NewPrimaryFallbackProvider(first, second)
	store := &serviceStore{}
	service := newTestService(t, store, pool)

	if _, err := service.Invoke(context.Background(), testInvocation("first")); err != nil {
		t.Fatal(err)
	}
	if store.startInput.Model != "MiniMax-M2" || first.calls != 2 || second.calls != 0 {
		t.Fatalf(
			"first audit=%q calls=(%d,%d)",
			store.startInput.Model,
			first.calls,
			second.calls,
		)
	}

	fallbackInvocation := testInvocation("second")
	fallbackInvocation.PreferredModel = "deepseek-v3"
	if _, err := service.Invoke(context.Background(), fallbackInvocation); err != nil {
		t.Fatal(err)
	}
	if store.startInput.Model != "deepseek-v3" || first.calls != 2 || second.calls != 1 {
		t.Fatalf(
			"second audit=%q calls=(%d,%d)",
			store.startInput.Model,
			first.calls,
			second.calls,
		)
	}
}

func TestServiceRejectsUnconfiguredPreferredModelBeforeAudit(t *testing.T) {
	first := &poolTestProvider{model: "MiniMax-M2"}
	second := &poolTestProvider{model: "deepseek-v3"}
	store := &serviceStore{}
	service := newTestService(
		t,
		store,
		NewPrimaryFallbackProvider(first, second),
	)
	invocation := testInvocation("unknown")
	invocation.PreferredModel = "other-model"
	_, err := service.Invoke(context.Background(), invocation)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) ||
		providerErr.Code != ErrorCodeProviderUnavailable {
		t.Fatalf("error=%v, want provider unavailable", err)
	}
	if store.startInput.Model != "" || first.calls != 0 || second.calls != 0 {
		t.Fatalf(
			"unexpected audit/calls: audit=%q calls=(%d,%d)",
			store.startInput.Model,
			first.calls,
			second.calls,
		)
	}
}
