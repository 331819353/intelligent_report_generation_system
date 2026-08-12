package runtime

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/report/store"
)

// Session is the single report execution pipeline.
//
// Planning a report used to be an open-coded sequence — derive the timezone,
// derive the viewer policy hash, resolve filter values, build the plan, pin the
// version — repeated by the plan endpoint, the execute endpoint and the tabular
// export source. Three copies of a security-relevant ordering is three chances
// to omit a step, so the sequence lives here once and every caller goes through
// it.
type Session struct {
	Target          ExecutionTarget
	Identity        store.Identity
	AsOf            time.Time
	Location        *time.Location
	PolicyScopeHash string
}

func NewSession(identity store.Identity, target ExecutionTarget, asOf time.Time) (Session, error) {
	if err := target.Validate(); err != nil {
		return Session{}, err
	}
	if asOf.IsZero() {
		return Session{}, errors.New("report execution requires an as-of time")
	}
	location, err := RuntimeTimezone(target.Definition)
	if err != nil {
		return Session{}, NewError("REPORT_RUNTIME_TIMEZONE_INVALID", "报告运行时区无效", err)
	}
	policyScopeHash, err := target.PolicyScopeHash(identity)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Target: target, Identity: identity, AsOf: asOf.UTC(),
		Location: location, PolicyScopeHash: policyScopeHash,
	}, nil
}

// Plan resolves a public request into an executable plan for this target.
func (session Session) Plan(input HTTPPlanInput) (ExecutionPlan, error) {
	resolved, err := input.Resolve(session.Target.Definition, session.AsOf, session.Location, session.PolicyScopeHash)
	if err != nil {
		return ExecutionPlan{}, err
	}
	return session.PlanResolved(resolved)
}

// PlanResolved serves callers that already hold a server-resolved PlanRequest,
// such as the export source which additionally selects pages.
func (session Session) PlanResolved(resolved PlanRequest) (ExecutionPlan, error) {
	resolved.PolicyScopeHash = session.PolicyScopeHash
	resolved.Unexecutable = session.Target.executableAgainst
	plan, err := BuildExecutionPlan(session.Target.Definition, resolved)
	if err != nil {
		return ExecutionPlan{}, err
	}
	if err := session.Target.pin(&plan); err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

func (session Session) Execute(
	ctx context.Context,
	plan ExecutionPlan,
	executor QueryExecutor,
	concurrency int,
) []ComponentResult {
	return ExecuteBatch(WithViewerIdentity(ctx, session.Identity), plan, executor, concurrency)
}

// Run is the common case: resolve, plan and execute in one call.
func (session Session) Run(
	ctx context.Context,
	input HTTPPlanInput,
	executor QueryExecutor,
) ([]ComponentResult, error) {
	plan, err := session.Plan(input)
	if err != nil {
		return nil, err
	}
	return session.Execute(ctx, plan, executor, session.Target.Definition.RuntimePolicy.MaxConcurrentQueries), nil
}
