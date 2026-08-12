// Package orchestrator owns the durable question state machine. It mirrors the
// database lifecycle exactly and never permits a run to change its actor,
// policy scope or semantic release after creation.
package orchestrator

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

var (
	ErrInvalidRun                = errors.New("invalid question run")
	ErrIllegalTransition         = errors.New("illegal question run state transition")
	ErrTerminalRun               = errors.New("question run is terminal")
	ErrVersionConflict           = errors.New("question run version conflict")
	ErrRunNotFound               = errors.New("question run was not found")
	ErrIdempotencyConflict       = errors.New("question run idempotency conflict")
	ErrReplayCorrupt             = errors.New("question run replay is corrupt")
	ErrPinnedScopeMismatch       = errors.New("question run pinned scope does not match")
	ErrInvalidAccessContext      = errors.New("question run requires an authenticated actor and domain")
	ErrReleaseNotRunnable        = errors.New("semantic release cannot create a new question run")
	ErrReleaseProjectionMismatch = errors.New("semantic release projections do not match")
	ErrNoProgress                = errors.New("question run checkpoint made no progress")
	ErrPersistence               = errors.New("question run persistence failed")
)

var completionCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type State string

const (
	StateReceived              State = "RECEIVED"
	StateAuthorized            State = "AUTHORIZED"
	StateContextReady          State = "CONTEXT_READY"
	StateUnderstanding         State = "UNDERSTANDING"
	StateRetrieving            State = "RETRIEVING"
	StateBinding               State = "BINDING"
	StateGraphValidating       State = "GRAPH_VALIDATING"
	StateIRReady               State = "IR_READY"
	StatePlanValidating        State = "PLAN_VALIDATING"
	StateExecuting             State = "EXECUTING"
	StateResultVerifying       State = "RESULT_VERIFYING"
	StateAnswerVerifying       State = "ANSWER_VERIFYING"
	StateClarificationRequired State = "CLARIFICATION_REQUIRED"
	StateClarificationExpired  State = "CLARIFICATION_EXPIRED"
	StateOutOfScope            State = "OUT_OF_SCOPE"
	StateAnswered              State = "ANSWERED"
	StateBlocked               State = "BLOCKED"
)

var validStates = map[State]struct{}{
	StateReceived: {}, StateAuthorized: {}, StateContextReady: {},
	StateUnderstanding: {}, StateRetrieving: {}, StateBinding: {},
	StateGraphValidating: {}, StateIRReady: {}, StatePlanValidating: {},
	StateExecuting: {}, StateResultVerifying: {}, StateAnswerVerifying: {},
	StateClarificationRequired: {}, StateClarificationExpired: {}, StateOutOfScope: {},
	StateAnswered: {}, StateBlocked: {},
}

type Disposition string

const (
	DispositionPending Disposition = "PENDING"
	DispositionDirect  Disposition = "DIRECT"
	DispositionClarify Disposition = "CLARIFY"
	DispositionRefuse  Disposition = "REFUSE"
)

type EventType string

const (
	EventStateTransition  EventType = "STATE_TRANSITION"
	EventLLMDecision      EventType = "LLM_DECISION"
	EventToolResult       EventType = "TOOL_RESULT"
	EventArtifactRecorded EventType = "ARTIFACT_RECORDED"
	EventBudgetUpdated    EventType = "BUDGET_UPDATED"
	EventCorrection       EventType = "CORRECTION"
	EventError            EventType = "ERROR"
	EventProgress         EventType = "PROGRESS"
)

type EventStatus string

const (
	EventStarted   EventStatus = "STARTED"
	EventSucceeded EventStatus = "SUCCEEDED"
	EventBlocked   EventStatus = "BLOCKED"
	EventFailed    EventStatus = "FAILED"
	EventCanceled  EventStatus = "CANCELED"
)

type ArtifactType string

const (
	ArtifactUnderstanding      ArtifactType = "UNDERSTANDING"
	ArtifactCandidateSet       ArtifactType = "CANDIDATE_SET"
	ArtifactBindingBundle      ArtifactType = "BINDING_BUNDLE"
	ArtifactGraphPlan          ArtifactType = "GRAPH_PLAN"
	ArtifactSemanticIR         ArtifactType = "SEMANTIC_IR"
	ArtifactQueryPlan          ArtifactType = "QUERY_PLAN"
	ArtifactResultSummary      ArtifactType = "RESULT_SUMMARY"
	ArtifactResultVerification ArtifactType = "RESULT_VERIFICATION"
	ArtifactEvidence           ArtifactType = "EVIDENCE"
	ArtifactAnswer             ArtifactType = "ANSWER"
	ArtifactClarification      ArtifactType = "CLARIFICATION"
	ArtifactBlock              ArtifactType = "BLOCK"
)

var validArtifactTypes = map[ArtifactType]struct{}{
	ArtifactUnderstanding: {}, ArtifactCandidateSet: {}, ArtifactBindingBundle: {},
	ArtifactGraphPlan: {}, ArtifactSemanticIR: {}, ArtifactQueryPlan: {},
	ArtifactResultSummary: {}, ArtifactResultVerification: {}, ArtifactEvidence: {},
	ArtifactAnswer: {}, ArtifactClarification: {}, ArtifactBlock: {},
}

// BudgetLimits are immutable after the run is created. The maxima deliberately
// match migration 000246 and the architecture's governed budget envelope.
type BudgetLimits struct {
	MaxSteps             int   `json:"maxSteps"`
	MaxLLMCalls          int   `json:"maxLlmCalls"`
	MaxToolCalls         int   `json:"maxToolCalls"`
	MaxFormalQueries     int   `json:"maxFormalQueries"`
	MaxValidationQueries int   `json:"maxValidationQueries"`
	MaxDurationMS        int64 `json:"maxDurationMs"`
}

func DefaultBudgetLimits() BudgetLimits {
	return BudgetLimits{
		MaxSteps: 48, MaxLLMCalls: 16, MaxToolCalls: 16,
		MaxFormalQueries: 2, MaxValidationQueries: 3, MaxDurationMS: 600_000,
	}
}

func (limits BudgetLimits) Validate() error {
	if limits.MaxSteps < 1 || limits.MaxSteps > 48 ||
		limits.MaxLLMCalls < 1 || limits.MaxLLMCalls > 16 ||
		limits.MaxToolCalls < 0 || limits.MaxToolCalls > 16 ||
		limits.MaxFormalQueries < 0 || limits.MaxFormalQueries > 6 ||
		limits.MaxValidationQueries < 0 || limits.MaxValidationQueries > 3 ||
		limits.MaxDurationMS < 100 || limits.MaxDurationMS > 600_000 {
		return fmt.Errorf("%w: budget limits exceed the governed bounds", ErrInvalidRun)
	}
	return nil
}

func (limits BudgetLimits) IsZero() bool { return limits == (BudgetLimits{}) }

type BudgetUsage struct {
	StepCount             int   `json:"stepCount"`
	LLMCallsUsed          int   `json:"llmCallsUsed"`
	ToolCallsUsed         int   `json:"toolCallsUsed"`
	FormalQueriesUsed     int   `json:"formalQueriesUsed"`
	ValidationQueriesUsed int   `json:"validationQueriesUsed"`
	ElapsedMS             int64 `json:"elapsedMs"`
	Exhausted             bool  `json:"exhausted"`
}

func (usage BudgetUsage) validate(limits BudgetLimits) error {
	if usage.StepCount < 0 || usage.StepCount > limits.MaxSteps ||
		usage.LLMCallsUsed < 0 || usage.LLMCallsUsed > limits.MaxLLMCalls ||
		usage.ToolCallsUsed < 0 || usage.ToolCallsUsed > limits.MaxToolCalls ||
		usage.FormalQueriesUsed < 0 || usage.FormalQueriesUsed > limits.MaxFormalQueries ||
		usage.ValidationQueriesUsed < 0 || usage.ValidationQueriesUsed > limits.MaxValidationQueries ||
		usage.ElapsedMS < 0 || usage.ElapsedMS > 600_000 {
		return fmt.Errorf("%w: budget usage is outside the governed bounds", ErrInvalidRun)
	}
	return nil
}

func (usage BudgetUsage) monotonicFrom(previous BudgetUsage) bool {
	return usage.StepCount >= previous.StepCount &&
		usage.LLMCallsUsed >= previous.LLMCallsUsed &&
		usage.ToolCallsUsed >= previous.ToolCallsUsed &&
		usage.FormalQueriesUsed >= previous.FormalQueriesUsed &&
		usage.ValidationQueriesUsed >= previous.ValidationQueriesUsed &&
		usage.ElapsedMS >= previous.ElapsedMS &&
		(!previous.Exhausted || usage.Exhausted)
}

type RunHashes struct {
	Understanding askdata.ContentHash `json:"understandingHash,omitempty"`
	BindingBundle askdata.ContentHash `json:"bindingBundleHash,omitempty"`
	GraphPlan     askdata.ContentHash `json:"graphPlanHash,omitempty"`
	SemanticIR    askdata.ContentHash `json:"semanticIrHash,omitempty"`
	QueryPlan     askdata.ContentHash `json:"queryPlanHash,omitempty"`
	Result        askdata.ContentHash `json:"resultHash,omitempty"`
}

func (hashes RunHashes) validate() error {
	for name, hash := range map[string]askdata.ContentHash{
		"understanding": hashes.Understanding, "binding bundle": hashes.BindingBundle,
		"graph plan": hashes.GraphPlan, "semantic IR": hashes.SemanticIR,
		"query plan": hashes.QueryPlan, "result": hashes.Result,
	} {
		if hash != "" {
			if err := hash.Validate(); err != nil {
				return fmt.Errorf("%w: %s hash: %v", ErrInvalidRun, name, err)
			}
		}
	}
	if hashes.BindingBundle != "" && hashes.Understanding == "" ||
		hashes.GraphPlan != "" && hashes.BindingBundle == "" ||
		hashes.SemanticIR != "" && hashes.GraphPlan == "" ||
		hashes.QueryPlan != "" && hashes.SemanticIR == "" ||
		hashes.Result != "" && hashes.QueryPlan == "" {
		return fmt.Errorf("%w: run hashes must form a contiguous governed chain", ErrInvalidRun)
	}
	return nil
}

func (hashes RunHashes) completeAnswerChain() bool {
	return hashes.Understanding != "" && hashes.BindingBundle != "" &&
		hashes.GraphPlan != "" && hashes.SemanticIR != "" &&
		hashes.QueryPlan != "" && hashes.Result != ""
}

type HashUpdates struct {
	Understanding *askdata.ContentHash
	BindingBundle *askdata.ContentHash
	GraphPlan     *askdata.ContentHash
	SemanticIR    *askdata.ContentHash
	QueryPlan     *askdata.ContentHash
	Result        *askdata.ContentHash
}

type Run struct {
	ID                    askdata.ID
	TenantID              askdata.ID
	DomainID              askdata.ID
	ActorID               askdata.ID
	ConversationID        askdata.ID
	ParentRunID           askdata.ID
	TraceID               askdata.ID
	IdempotencyKeyHash    askdata.ContentHash
	QuestionHash          askdata.ContentHash
	PolicyScopeHash       askdata.ContentHash
	Release               askdata.ReleaseRef
	State                 State
	Disposition           Disposition
	CompletionCode        string
	CompletionArtifact    askdata.ContentHash
	Hashes                RunHashes
	Limits                BudgetLimits
	Usage                 BudgetUsage
	ClarificationDeadline *time.Time
	BudgetFrozenAt        *time.Time
	BudgetConsumed        *BudgetUsage
	RecordVersion         int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           *time.Time
}

func (run Run) Terminal() bool {
	return run.State == StateClarificationRequired || run.State == StateClarificationExpired ||
		run.State == StateOutOfScope || run.State == StateAnswered || run.State == StateBlocked
}

func (run Run) PinnedRelease() askdata.ReleaseRef { return run.Release }

func (run Run) Validate() error {
	for name, id := range map[string]askdata.ID{
		"id": run.ID, "tenantId": run.TenantID, "domainId": run.DomainID,
		"actorId": run.ActorID, "traceId": run.TraceID,
	} {
		if uuid.Validate(string(id)) != nil {
			return fmt.Errorf("%w: %s must be a UUID", ErrInvalidRun, name)
		}
	}
	for name, id := range map[string]askdata.ID{
		"conversationId": run.ConversationID, "parentRunId": run.ParentRunID,
	} {
		if id != "" && uuid.Validate(string(id)) != nil {
			return fmt.Errorf("%w: %s must be a UUID", ErrInvalidRun, name)
		}
	}
	if run.ParentRunID != "" && run.ParentRunID == run.ID {
		return fmt.Errorf("%w: parent run cannot reference itself", ErrInvalidRun)
	}
	for name, hash := range map[string]askdata.ContentHash{
		"idempotency key": run.IdempotencyKeyHash, "question": run.QuestionHash,
		"policy scope": run.PolicyScopeHash,
	} {
		if err := hash.Validate(); err != nil {
			return fmt.Errorf("%w: %s hash: %v", ErrInvalidRun, name, err)
		}
	}
	if err := run.Release.Validate(); err != nil || uuid.Validate(string(run.Release.ReleaseID)) != nil {
		return fmt.Errorf("%w: release pin is invalid", ErrInvalidRun)
	}
	if _, ok := validStates[run.State]; !ok {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidRun, run.State)
	}
	if err := run.Hashes.validate(); err != nil {
		return err
	}
	if err := run.Limits.Validate(); err != nil {
		return err
	}
	if err := run.Usage.validate(run.Limits); err != nil {
		return err
	}
	if run.RecordVersion < 1 {
		return fmt.Errorf("%w: record version must be positive", ErrInvalidRun)
	}
	clarificationState := run.State == StateClarificationRequired || run.State == StateClarificationExpired
	if clarificationState {
		if run.ClarificationDeadline == nil || run.BudgetFrozenAt == nil || run.BudgetConsumed == nil ||
			run.ClarificationDeadline.IsZero() || run.BudgetFrozenAt.IsZero() ||
			!run.ClarificationDeadline.After(*run.BudgetFrozenAt) ||
			run.BudgetConsumed.validate(run.Limits) != nil || *run.BudgetConsumed != run.Usage {
			return fmt.Errorf("%w: clarification budget snapshot is invalid", ErrInvalidRun)
		}
	} else if run.ClarificationDeadline != nil || run.BudgetFrozenAt != nil || run.BudgetConsumed != nil {
		return fmt.Errorf("%w: non-clarification run carries a frozen budget", ErrInvalidRun)
	}
	if run.Usage.Exhausted && !run.Terminal() {
		return fmt.Errorf("%w: exhausted budget requires a terminal state", ErrInvalidRun)
	}
	if !run.Terminal() {
		if run.Disposition != DispositionPending || run.CompletionCode != "" ||
			run.CompletionArtifact != "" || run.CompletedAt != nil {
			return fmt.Errorf("%w: nonterminal completion shape is invalid", ErrInvalidRun)
		}
		return nil
	}
	if !completionCodePattern.MatchString(run.CompletionCode) {
		return fmt.Errorf("%w: completion code is invalid", ErrInvalidRun)
	}
	if run.CompletedAt == nil {
		return fmt.Errorf("%w: terminal completion timestamp is missing", ErrInvalidRun)
	}
	if err := run.CompletionArtifact.Validate(); err != nil {
		return fmt.Errorf("%w: completion artifact hash is invalid", ErrInvalidRun)
	}
	switch run.State {
	case StateAnswered:
		if run.Disposition != DispositionDirect || !run.Hashes.completeAnswerChain() {
			return fmt.Errorf("%w: answered run is missing its governed completion chain", ErrInvalidRun)
		}
	case StateClarificationRequired, StateClarificationExpired:
		if run.Disposition != DispositionClarify {
			return fmt.Errorf("%w: clarification disposition is invalid", ErrInvalidRun)
		}
	case StateBlocked, StateOutOfScope:
		if run.Disposition != DispositionRefuse {
			return fmt.Errorf("%w: blocked disposition is invalid", ErrInvalidRun)
		}
	}
	return nil
}

// CanTransition mirrors askdata.valid_question_run_transition. A nonterminal
// same-state transition is a checkpoint and must still make durable progress.
func CanTransition(from, to State) bool {
	if _, ok := validStates[from]; !ok {
		return false
	}
	if _, ok := validStates[to]; !ok {
		return false
	}
	if from == StateClarificationRequired {
		return to == StateClarificationExpired
	}
	if isTerminalState(from) {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case StateReceived:
		return to == StateAuthorized || to == StateBlocked
	case StateAuthorized:
		return to == StateContextReady || to == StateBlocked
	case StateContextReady:
		return to == StateUnderstanding || to == StateBlocked
	case StateUnderstanding:
		return to == StateRetrieving || to == StateClarificationRequired ||
			to == StateOutOfScope || to == StateBlocked
	case StateRetrieving:
		return to == StateBinding || to == StateClarificationRequired || to == StateBlocked
	case StateBinding:
		return to == StateGraphValidating || to == StateClarificationRequired ||
			to == StateOutOfScope || to == StateBlocked
	case StateGraphValidating:
		return to == StateIRReady || to == StateClarificationRequired || to == StateBlocked
	case StateIRReady:
		return to == StatePlanValidating || to == StateClarificationRequired || to == StateBlocked
	case StatePlanValidating:
		return to == StateExecuting || to == StateBinding ||
			to == StateClarificationRequired || to == StateBlocked
	case StateExecuting:
		return to == StateResultVerifying || to == StateBlocked
	case StateResultVerifying:
		return to == StateAnswerVerifying || to == StateBinding ||
			to == StateClarificationRequired || to == StateBlocked
	case StateAnswerVerifying:
		return to == StateAnswered || to == StateClarificationRequired || to == StateBlocked
	default:
		return false
	}
}

func isTerminalState(state State) bool {
	return state == StateClarificationRequired || state == StateClarificationExpired ||
		state == StateOutOfScope || state == StateAnswered || state == StateBlocked
}

type CompletionRef struct {
	Code         string
	ArtifactType ArtifactType
	ArtifactHash askdata.ContentHash
}

type Transition struct {
	ExpectedVersion      int64
	TargetState          State
	Usage                BudgetUsage
	Hashes               HashUpdates
	Completion           *CompletionRef
	ClarificationTimeout time.Duration
}

// Apply validates and constructs the next durable run snapshot. Budget usage
// is absolute rather than a delta so a caller can safely resume without double
// charging a retried command.
func Apply(current Run, transition Transition) (Run, error) {
	if err := current.Validate(); err != nil {
		return Run{}, err
	}
	if current.Terminal() {
		return Run{}, ErrTerminalRun
	}
	if transition.ExpectedVersion != current.RecordVersion {
		return Run{}, ErrVersionConflict
	}
	if !CanTransition(current.State, transition.TargetState) {
		return Run{}, ErrIllegalTransition
	}
	if err := transition.Usage.validate(current.Limits); err != nil {
		return Run{}, err
	}
	if !transition.Usage.monotonicFrom(current.Usage) {
		return Run{}, fmt.Errorf("%w: budget usage cannot decrease", ErrInvalidRun)
	}

	next := current
	next.State = transition.TargetState
	next.Usage = transition.Usage
	next.RecordVersion++
	next.UpdatedAt = time.Now().UTC()
	beforeHashes := next.Hashes
	if next.State == StateClarificationRequired {
		timeout := transition.ClarificationTimeout
		if timeout == 0 {
			timeout = DefaultClarificationTimeout
		}
		frozen, err := FreezeBudget(next.Usage, next.Limits, next.UpdatedAt, timeout)
		if err != nil {
			return Run{}, err
		}
		next.ClarificationDeadline = &frozen.Deadline
		next.BudgetFrozenAt = &frozen.FrozenAt
		next.BudgetConsumed = &frozen.Consumed
	}

	correction := transition.TargetState == StateBinding &&
		(current.State == StatePlanValidating || current.State == StateResultVerifying)
	if correction {
		if transition.Hashes.BindingBundle != nil || transition.Hashes.GraphPlan != nil ||
			transition.Hashes.SemanticIR != nil || transition.Hashes.QueryPlan != nil ||
			transition.Hashes.Result != nil {
			return Run{}, fmt.Errorf("%w: correction cannot retain downstream hashes", ErrInvalidRun)
		}
		next.Hashes.BindingBundle = ""
		next.Hashes.GraphPlan = ""
		next.Hashes.SemanticIR = ""
		next.Hashes.QueryPlan = ""
		next.Hashes.Result = ""
	}

	updates := []struct {
		name    string
		stage   State
		current *askdata.ContentHash
		update  *askdata.ContentHash
	}{
		{"understanding", StateUnderstanding, &next.Hashes.Understanding, transition.Hashes.Understanding},
		{"binding bundle", StateBinding, &next.Hashes.BindingBundle, transition.Hashes.BindingBundle},
		{"graph plan", StateGraphValidating, &next.Hashes.GraphPlan, transition.Hashes.GraphPlan},
		{"semantic IR", StateIRReady, &next.Hashes.SemanticIR, transition.Hashes.SemanticIR},
		{"query plan", StatePlanValidating, &next.Hashes.QueryPlan, transition.Hashes.QueryPlan},
		{"result", StateResultVerifying, &next.Hashes.Result, transition.Hashes.Result},
	}
	for _, update := range updates {
		if update.update == nil {
			continue
		}
		if err := update.update.Validate(); err != nil {
			return Run{}, fmt.Errorf("%w: %s hash is invalid", ErrInvalidRun, update.name)
		}
		if *update.current != "" && *update.current != *update.update {
			return Run{}, fmt.Errorf("%w: %s hash cannot be overwritten", ErrInvalidRun, update.name)
		}
		if *update.current == "" && current.State != update.stage && next.State != update.stage {
			return Run{}, fmt.Errorf(
				"%w: %s hash cannot first appear outside %s",
				ErrInvalidRun, update.name, update.stage,
			)
		}
		*update.current = *update.update
	}
	if err := next.Hashes.validate(); err != nil {
		return Run{}, err
	}

	if isTerminalState(next.State) {
		if transition.Completion == nil {
			return Run{}, fmt.Errorf("%w: terminal transition requires a completion artifact", ErrInvalidRun)
		}
		if !completionCodePattern.MatchString(transition.Completion.Code) ||
			transition.Completion.ArtifactHash.Validate() != nil ||
			transition.Completion.ArtifactType != completionArtifactType(next.State) {
			return Run{}, fmt.Errorf("%w: terminal completion reference is invalid", ErrInvalidRun)
		}
		next.CompletionCode = transition.Completion.Code
		next.CompletionArtifact = transition.Completion.ArtifactHash
		switch next.State {
		case StateAnswered:
			next.Disposition = DispositionDirect
		case StateClarificationRequired, StateClarificationExpired:
			next.Disposition = DispositionClarify
		case StateBlocked, StateOutOfScope:
			next.Disposition = DispositionRefuse
		}
		completed := next.UpdatedAt
		next.CompletedAt = &completed
	} else if transition.Completion != nil {
		return Run{}, fmt.Errorf("%w: nonterminal transition cannot complete a run", ErrInvalidRun)
	}

	if current.State == next.State && current.Usage == next.Usage && beforeHashes == next.Hashes {
		return Run{}, ErrNoProgress
	}
	if err := next.Validate(); err != nil {
		return Run{}, err
	}
	return next, nil
}

func completionArtifactType(state State) ArtifactType {
	switch state {
	case StateAnswered:
		return ArtifactAnswer
	case StateClarificationRequired, StateClarificationExpired:
		return ArtifactClarification
	case StateBlocked, StateOutOfScope:
		return ArtifactBlock
	default:
		return ""
	}
}
