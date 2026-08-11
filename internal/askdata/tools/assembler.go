package tools

import (
	"context"
	"fmt"

	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// Assembler builds one question run's execution context: a run-scoped tool
// binding, the governed tool registry over it, and a Loop joining that registry
// to the cognition runner.
//
// It exists so the orchestrator can stay free of any dependency on the tool
// adapter layer — the worker asks for a Loop and gets one, without knowing what
// backs the tools.
type Assembler struct {
	services  Services
	cognition *CognitionRunner
	options   orchestrator.LoopOptions
}

func NewAssembler(
	services Services,
	cognitionRunner *CognitionRunner,
	options orchestrator.LoopOptions,
) (*Assembler, error) {
	if cognitionRunner == nil {
		return nil, ErrCognitionUnavailable
	}
	if options.PromptVersion == "" {
		options = orchestrator.DefaultLoopOptions()
	}
	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("invalid loop options: %w", err)
	}
	return &Assembler{services: services, cognition: cognitionRunner, options: options}, nil
}

// Assemble prepares the Loop and authorization context for a single run.
//
// A fresh binding is built per run: plan hashes are run-scoped, so reusing a
// binding across runs would let one run validate or execute a plan compiled
// under another actor's policy scope.
func (assembler *Assembler) Assemble(
	_ context.Context,
	request orchestrator.RunAssembly,
) (*orchestrator.Loop, toolhost.AuthorizationContext, error) {
	if assembler == nil || assembler.cognition == nil {
		return nil, toolhost.AuthorizationContext{}, ErrCognitionUnavailable
	}
	binding, err := NewBinding(assembler.services, RunContext{
		Scope: request.Scope, DomainID: request.DomainID, RunID: request.RunID,
	})
	if err != nil {
		return nil, toolhost.AuthorizationContext{}, err
	}
	registry, err := binding.NewRegistry()
	if err != nil {
		return nil, toolhost.AuthorizationContext{}, err
	}
	loop, err := orchestrator.NewLoop(assembler.cognition, registry, assembler.options)
	if err != nil {
		return nil, toolhost.AuthorizationContext{}, err
	}
	return loop, toolhost.AuthorizationContext{
		Scope: request.Scope, DomainID: request.DomainID,
		Permissions: questionRunPermissions(),
	}, nil
}

// questionRunPermissions is the tool permission set a question run may use.
//
// It is the full governed set because each tool re-checks its own authority at
// execution time: the reader enforces release pinning, domain scope and
// sensitivity; the compiler refuses any IR not pinned to this run's release;
// and the executor runs under the viewer's own warehouse permissions. Narrowing
// here would only hide tools from the model, not grant it anything, and a tool
// the model cannot see is a capability the run silently loses.
func questionRunPermissions() []toolhost.Permission {
	return []toolhost.Permission{
		toolhost.PermissionSemanticRead,
		toolhost.PermissionDimensionValueRead,
		toolhost.PermissionGraphResolve,
		toolhost.PermissionQualityRead,
		toolhost.PermissionQueryCompile,
		toolhost.PermissionQueryValidate,
		toolhost.PermissionCardinalityProbe,
		toolhost.PermissionQueryExecute,
		toolhost.PermissionValidationQueryExecute,
		toolhost.PermissionClarificationRequest,
	}
}

// Compile-time proof that the assembler satisfies what the worker requires.
var _ orchestrator.LoopAssembler = (*Assembler)(nil)
