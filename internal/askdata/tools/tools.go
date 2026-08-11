// Package tools binds the frozen Tool Host contract to the platform's real
// query-time services.
//
// Design note — why handlers are built per run:
//
// toolhost.Invocation deliberately carries no run identity, so a handler cannot
// reach for "the current question". That is the right boundary for the tools
// themselves, but compile_semantic_query / validate_query_plan /
// execute_query_plan form a chain that refers to a plan by hash. Those hashes
// must not be globally resolvable: a plan compiled inside one run must never be
// executable from another, or a caller could execute a plan compiled under a
// different actor's policy scope.
//
// So Services holds the shared, run-independent dependencies, and Handlers are
// constructed per run over a run-scoped plan cache. A plan hash is meaningful
// only within the run that compiled it.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/validator"
)

// ErrToolUnavailable marks a tool whose backing capability is not configured in
// this deployment. It is returned instead of a fabricated empty success so the
// orchestrator can degrade explicitly rather than treat "not wired" as "no
// data".
var ErrToolUnavailable = errors.New("tool capability is not available")

// Embedder produces a query embedding for hybrid retrieval. It is optional:
// when absent, retrieval runs lexical + exact only and reports degradation.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, string, error)
}

// Services holds the run-independent capabilities the tools are built on.
// Nil members disable their tools rather than panicking: a deployment without a
// graph or without an embedding provider stays usable and degrades visibly.
type Services struct {
	Reader    *registry.QueryReader
	Retriever *search.Retriever
	Embedder  Embedder
	Graph     *graph.Resolver
	Compiler  *compiler.PinnedIRCompiler
	Validator *validator.Validator
	Executor  *validator.Executor
}

// RunContext identifies the question run a handler set belongs to.
type RunContext struct {
	Scope    askdata.PolicyScope
	DomainID askdata.ID
	RunID    askdata.ID
}

func (run RunContext) validate() error {
	if run.Scope.Validate() != nil || run.Scope.Release.Validate() != nil ||
		run.DomainID.Validate() != nil || run.RunID.Validate() != nil {
		return fmt.Errorf("%w: run context is incomplete", toolhost.ErrInvalidInvocation)
	}
	return nil
}

// planEntry is one plan's progress through compile → validate → execute.
type planEntry struct {
	artifact   compiler.QueryArtifact
	validation validator.ValidationArtifact
	validated  bool
}

// planCache holds the plans compiled during one run. It is intentionally not
// backed by storage: plan hashes are run-scoped by construction.
type planCache struct {
	mutex sync.Mutex
	plans map[askdata.ContentHash]planEntry
}

func newPlanCache() *planCache {
	return &planCache{plans: map[askdata.ContentHash]planEntry{}}
}

func (cache *planCache) put(hash askdata.ContentHash, artifact compiler.QueryArtifact) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.plans[hash] = planEntry{artifact: artifact}
}

func (cache *planCache) get(hash askdata.ContentHash) (compiler.QueryArtifact, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, ok := cache.plans[hash]
	return entry.artifact, ok
}

// markValidated records the validation artifact a plan passed with. Execution
// requires it, so a plan can never be executed without having been validated
// in this same run.
func (cache *planCache) markValidated(hash askdata.ContentHash, validation validator.ValidationArtifact) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, ok := cache.plans[hash]
	if !ok {
		return
	}
	entry.validation, entry.validated = validation, true
	cache.plans[hash] = entry
}

func (cache *planCache) getValidated(
	hash askdata.ContentHash,
) (compiler.QueryArtifact, validator.ValidationArtifact, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, ok := cache.plans[hash]
	if !ok || !entry.validated {
		return compiler.QueryArtifact{}, validator.ValidationArtifact{}, false
	}
	return entry.artifact, entry.validation, true
}

// resultCache holds the result hash each executed plan produced, so candidate
// comparison can work from hashes without holding result rows across calls.
type resultCache struct {
	mutex   sync.Mutex
	results map[askdata.ContentHash]askdata.ContentHash
}

func newResultCache() *resultCache {
	return &resultCache{results: map[askdata.ContentHash]askdata.ContentHash{}}
}

func (cache *resultCache) put(planHash, resultHash askdata.ContentHash) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.results[planHash] = resultHash
}

func (cache *resultCache) get(planHash askdata.ContentHash) (askdata.ContentHash, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	resultHash, ok := cache.results[planHash]
	return resultHash, ok
}

// Binding is one run's handler set together with the state the chain needs.
type Binding struct {
	services Services
	run      RunContext
	plans    *planCache
	results  *resultCache
}

// NewBinding prepares the tool handlers for a single question run.
func NewBinding(services Services, run RunContext) (*Binding, error) {
	if err := run.validate(); err != nil {
		return nil, err
	}
	return &Binding{
		services: services, run: run,
		plans: newPlanCache(), results: newResultCache(),
	}, nil
}

// Handlers exposes this run's tool implementations in the shape the Tool Host
// registry expects. Every handler re-checks that the invocation belongs to this
// run before touching a service.
func (binding *Binding) Handlers() toolhost.Handlers {
	return toolhost.Handlers{
		SearchSemanticObjects:   binding.searchSemanticObjects,
		GetSemanticContracts:    binding.getSemanticContracts,
		LookupDimensionValues:   binding.lookupDimensionValues,
		GetCertifiedExamples:    binding.getCertifiedExamples,
		ResolveGraphPlan:        binding.resolveGraphPlan,
		ValidateSemanticBundle:  binding.validateSemanticBundle,
		GetDataQualityStatus:    binding.getDataQualityStatus,
		CompileSemanticQuery:    binding.compileSemanticQuery,
		ValidateQueryPlan:       binding.validateQueryPlan,
		ProbeJoinCardinality:    binding.probeJoinCardinality,
		ExecuteQueryPlan:        binding.executeQueryPlan,
		ExecuteValidationQuery:  binding.executeValidationQuery,
		CompareCandidateResults: binding.compareCandidateResults,
		RequestClarification:    binding.requestClarification,
	}
}

// NewRegistry builds the governed Tool Host registry for this run.
func (binding *Binding) NewRegistry() (*toolhost.Registry, error) {
	return toolhost.NewRegistry(binding.Handlers())
}

// authorize rejects any invocation whose authorization context does not match
// the run this binding was built for. The Tool Host already checks permissions,
// release and domain; this is the second, run-level check that stops a handler
// from ever acting outside its own run's policy scope.
func (binding *Binding) authorize(authorization toolhost.AuthorizationContext) error {
	if authorization.Validate() != nil {
		return toolhost.ErrInvalidInvocation
	}
	if authorization.DomainID != binding.run.DomainID ||
		authorization.Scope.PolicyHash != binding.run.Scope.PolicyHash ||
		authorization.Scope.Release != binding.run.Scope.Release {
		return toolhost.ErrInvalidInvocation
	}
	return nil
}

// evidence builds a run-scoped evidence reference. Evidence IDs are derived
// from the run, the tool and the content so they are stable across a replay of
// the same run and never collide across runs.
func (binding *Binding) evidence(kind askdata.EvidenceKind, source askdata.ID, content []byte) askdata.EvidenceRef {
	hash := askdata.HashBytes(content)
	id := askdata.HashBytes([]byte(string(binding.run.RunID) + "\x00" + string(kind) + "\x00" + string(hash)))
	return askdata.EvidenceRef{
		EvidenceID: askdata.ID(id), Kind: kind, SourceID: source, ContentHash: hash,
	}
}

func stableIDs(values []string) []askdata.ID {
	result := make([]askdata.ID, 0, len(values))
	for _, value := range values {
		result = append(result, askdata.ID(value))
	}
	return result
}

func plainIDs(values []askdata.ID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
