package tools

import (
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata/cognition"
)

// ErrCognitionUnavailable marks a deployment with no usable model provider.
// AskData genuinely cannot answer without one — unlike the report chain, there
// is no deterministic path from a natural-language question to a bound query —
// so this fails loudly instead of degrading.
var ErrCognitionUnavailable = errors.New("cognition provider is not configured")

// ModelProvider is the subset of the AI platform service the cognition runner
// needs. Depending on the interface rather than *ai.Service keeps the runner
// testable and stops the tool layer from reaching for unrelated provider APIs.
type ModelProvider interface {
	Invoke(context.Context, ai.Invocation) (ai.InvocationResult, error)
	Configured() bool
}

// CognitionRunner performs one cognition round against the configured provider.
//
// It exists to satisfy orchestrator.CognitionRunner while keeping two
// guarantees the Loop depends on: the provider is bound to the closed action
// protocol, and an unconfigured provider is reported as such rather than
// surfacing as a malformed model response.
type CognitionRunner struct {
	executor *cognition.Executor
	provider ModelProvider
}

// NewCognitionRunner binds the embedded action schema to the model provider.
// It fails at construction if the schema is not a valid provider contract, so a
// broken protocol is a startup error rather than a per-question failure.
func NewCognitionRunner(provider ModelProvider, options cognition.ExecutorOptions) (*CognitionRunner, error) {
	if provider == nil {
		return nil, ErrCognitionUnavailable
	}
	executor, err := cognition.NewExecutor(provider, cognition.ActionSchema(), options)
	if err != nil {
		return nil, fmt.Errorf("build cognition executor: %w", err)
	}
	return &CognitionRunner{executor: executor, provider: provider}, nil
}

// Execute runs one cognition round.
func (runner *CognitionRunner) Execute(
	ctx context.Context,
	request cognition.RoundRequest,
) (cognition.RoundResult, error) {
	if runner == nil || runner.executor == nil || runner.provider == nil {
		return cognition.RoundResult{}, ErrCognitionUnavailable
	}
	// Checked per round, not only at startup: provider configuration is runtime
	// state, and "no provider" must never be reported as an invalid action.
	if !runner.provider.Configured() {
		return cognition.RoundResult{}, ErrCognitionUnavailable
	}
	return runner.executor.Execute(ctx, request)
}

// BatchEmbeddingProvider is the platform's embedding client shape.
type BatchEmbeddingProvider interface {
	Embed(ctx context.Context, values []string) ([][]float32, error)
	Model() string
	Configured() bool
}

// BatchEmbedder adapts the batch embedding client to the single-mention
// Embedder the retrieval tool needs.
//
// An unconfigured or failing provider reports an error rather than an empty
// vector: the caller degrades retrieval to lexical + exact and marks the result,
// which is honest, whereas a zero vector would silently poison ranking.
type BatchEmbedder struct{ Provider BatchEmbeddingProvider }

func (embedder BatchEmbedder) Embed(ctx context.Context, text string) ([]float32, string, error) {
	if embedder.Provider == nil || !embedder.Provider.Configured() {
		return nil, "", ErrToolUnavailable
	}
	vectors, err := embedder.Provider.Embed(ctx, []string{text})
	if err != nil {
		return nil, "", err
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, "", fmt.Errorf("%w: embedding provider returned no vector", ErrToolUnavailable)
	}
	return vectors[0], embedder.Provider.Model(), nil
}
