package datasetai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The modeling session is the persisted source of truth for one conversational
// modeling workflow: the user's business goal, the confirmed dataset type and table
// scope, every clarification asked and answered, and the lifecycle of each staged
// proposal. Planning turns read it as trusted structured context and append to it;
// the client renders it and can restore a conversation from it after a reload.
//
// The session never contains graph state. The canvas (or its saved draft) remains
// the single modeling baseline; the session records decisions *about* it.

const (
	SessionStatusActive = "ACTIVE"
	SessionStatusClosed = "CLOSED"

	ModelKindSourceKeywordRule   = "KEYWORD_RULE"
	ModelKindSourceLLMIntent     = "LLM_INTENT"
	ModelKindSourceUserConfirmed = "USER_CONFIRMED"

	ScopeTableSourceRetrieved = "RETRIEVED"
	ScopeTableSourceUserAdded = "USER_ADDED"

	ProposalStatusStaged     = "STAGED"
	ProposalStatusSuperseded = "SUPERSEDED"
	ProposalStatusApplied    = "APPLIED"
	ProposalStatusReverted   = "REVERTED"

	maxSessionScopeTables       = maxPlanNodes
	maxSessionClarifications    = 16
	maxSessionProposals         = 16
	maxSessionReasonRunes       = 300
	maxClarificationAnswerRunes = 1000
)

var (
	ErrSessionNotFound = errors.New("dataset AI modeling session not found")
	ErrSessionConflict = errors.New("dataset AI modeling session was updated concurrently")
)

type ModelingSession struct {
	ID        string               `json:"id"`
	TenantID  string               `json:"-"`
	ActorID   string               `json:"-"`
	DatasetID string               `json:"datasetId,omitempty"`
	Status    string               `json:"status"`
	Revision  int64                `json:"revision"`
	State     ModelingSessionState `json:"state"`
	CreatedAt time.Time            `json:"createdAt"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

// ModelingSessionState is the JSONB document. Every list is append-mostly and
// bounded, so the document stays small enough to load on each turn.
type ModelingSessionState struct {
	// DomainID is the business domain the modeling belongs to (the dataset's
	// domain for a saved dataset, the selected domain for a new one). It scopes
	// business-knowledge lookups; it is advisory, never an access boundary.
	DomainID        string `json:"domainId,omitempty"`
	Goal            string `json:"goal,omitempty"`
	ModelKind       string `json:"modelKind,omitempty"`
	ModelKindSource string `json:"modelKindSource,omitempty"`
	// Intent is the structured business-language interpretation produced before
	// any physical table is selected. It is decision history, never graph state.
	Intent *StructuredModelingIntent `json:"intent,omitempty"`
	Scope  *ConfirmedScope           `json:"scope,omitempty"`
	// Blueprint holds the per-stage modeling decisions proposed after the scope was
	// confirmed and resolved by the user (workflow.go). It is cleared whenever the
	// scope changes: decisions about tables that are no longer in scope are void.
	Blueprint      *ModelingBlueprint    `json:"blueprint,omitempty"`
	Clarifications []ClarificationRecord `json:"clarifications,omitempty"`
	Proposals      []ProposalRecord      `json:"proposals,omitempty"`
	// ExecutionFindings are the conclusions of the latest preview of an applied
	// proposal; they are replaced on every execution and injected into the next
	// planning turn as trusted context.
	ExecutionFindings []ExecutionFinding `json:"executionFindings,omitempty"`
	// SourceScreenings are the latest evidence-based screening passes per role
	// (screening.go); the client restores the confirmation card from them.
	SourceScreenings []SourceScreening `json:"sourceScreenings,omitempty"`
}

// SetSourceScreening replaces the screening of the same role.
func (state *ModelingSessionState) SetSourceScreening(screening SourceScreening) {
	kept := make([]SourceScreening, 0, len(state.SourceScreenings)+1)
	for _, existing := range state.SourceScreenings {
		if existing.Role != screening.Role {
			kept = append(kept, existing)
		}
	}
	state.SourceScreenings = append(kept, screening)
}

// SourceScreeningFor returns the latest screening of one role.
func (state ModelingSessionState) SourceScreeningFor(role string) (SourceScreening, bool) {
	for _, screening := range state.SourceScreenings {
		if screening.Role == role {
			return screening, true
		}
	}
	return SourceScreening{}, false
}

// StructuredModelingIntent keeps the business concepts used to drive source
// retrieval. Values are business phrases, not physical table or column ids.
type StructuredModelingIntent struct {
	Entities        []string `json:"entities,omitempty"`
	Measures        []string `json:"measures,omitempty"`
	Dimensions      []string `json:"dimensions,omitempty"`
	TimeExpressions []string `json:"timeExpressions,omitempty"`
	Filters         []string `json:"filters,omitempty"`
}

// ConfirmedScope is a user decision, not a retrieval result: once present, CREATE
// catalog loading is restricted to exactly these tables.
type ConfirmedScope struct {
	Tables        []ScopedTable `json:"tables"`
	AutoConfirmed bool          `json:"autoConfirmed,omitempty"`
	// Reason records why the scope was settled without (or with) the confirmation
	// card, so the workflow can always explain how it got here.
	Reason      string    `json:"reason,omitempty"`
	ConfirmedAt time.Time `json:"confirmedAt"`
	// SourceDecisions preserves the two distinct confirmation gates even though
	// the final catalog boundary is the combined Tables list.
	SourceDecisions []SourceScopeDecision `json:"sourceDecisions,omitempty"`
}

type SourceScopeDecision struct {
	Role          string    `json:"role"`
	TableIDs      []string  `json:"tableIds"`
	Status        string    `json:"status"`
	AutoConfirmed bool      `json:"autoConfirmed,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	ConfirmedAt   time.Time `json:"confirmedAt"`
}

// ScopedTable is one confirmed source. Role separates the PRIMARY_SOURCE and
// DIMENSION_SOURCE workflow stages inside a single confirmation: the primary
// (entity, fact or upstream summary) tables versus the dimension tables joined to
// them. Role defaults to PRIMARY for clients that predate the workflow.
type ScopedTable struct {
	TableID string `json:"tableId"`
	Source  string `json:"source"`
	Role    string `json:"role,omitempty"`
}

type ClarificationRecord struct {
	Question          string                   `json:"question"`
	Candidates        []ClarificationCandidate `json:"candidates,omitempty"`
	AskedAt           time.Time                `json:"askedAt"`
	Answer            string                   `json:"answer,omitempty"`
	SelectedComponent *ComponentRef            `json:"selectedComponent,omitempty"`
	AnsweredAt        *time.Time               `json:"answeredAt,omitempty"`
}

type ProposalRecord struct {
	RequestID   string    `json:"requestId"`
	Mode        string    `json:"mode"`
	Summary     string    `json:"summary"`
	Instruction string    `json:"instruction"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// StageUpdates / ReopenStages carry the blueprint re-sync a MODIFY proposal
	// implies (blueprint_sync.go). They are applied only when the proposal is
	// applied, so a discarded proposal leaves the blueprint untouched.
	StageUpdates []StageDecision `json:"stageUpdates,omitempty"`
	ReopenStages []string        `json:"reopenStages,omitempty"`
	// Execution is the outcome of previewing the applied proposal's end node.
	Execution *ProposalExecution `json:"execution,omitempty"`
}

// ProposalExecution is the client-reported result of running the applied
// proposal through the candidate preview (docs/08 §6.10). Only counts, warning
// codes and an error category are recorded — never data values.
type ProposalExecution struct {
	RowCount   int                `json:"rowCount"`
	DurationMs int64              `json:"durationMs,omitempty"`
	Warnings   []ExecutionWarning `json:"warnings,omitempty"`
	Error      string             `json:"error,omitempty"`
	ExecutedAt time.Time          `json:"executedAt"`
}

type ExecutionWarning struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	JoinID  string `json:"joinId,omitempty"`
}

// ExecutionFinding is a modeling-relevant conclusion drawn from an execution:
// it becomes trusted context for the next turn and may reopen blueprint stages.
type ExecutionFinding struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	JoinID    string    `json:"joinId,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	At        time.Time `json:"at"`
}

const (
	ExecutionFindingEmptyResult    = "EMPTY_RESULT"
	ExecutionFindingExecutionError = "EXECUTION_ERROR"
	maxExecutionFindings           = 8
	maxExecutionWarnings           = 16
)

// ClarificationAnswer is the structured reply to the latest open clarification.
// Exactly one of SelectedComponent or Text carries the answer; both may be present
// when the user picked a candidate and added detail.
type ClarificationAnswer struct {
	Question          string        `json:"question"`
	Text              string        `json:"text,omitempty"`
	SelectedComponent *ComponentRef `json:"selectedComponent,omitempty"`
}

func (answer ClarificationAnswer) normalized() ClarificationAnswer {
	answer.Question = strings.TrimSpace(answer.Question)
	answer.Text = strings.TrimSpace(answer.Text)
	if answer.SelectedComponent != nil {
		component := ComponentRef{
			ComponentKind: strings.ToUpper(strings.TrimSpace(answer.SelectedComponent.ComponentKind)),
			ComponentID:   strings.TrimSpace(answer.SelectedComponent.ComponentID),
		}
		answer.SelectedComponent = &component
	}
	return answer
}

func (answer ClarificationAnswer) validate() error {
	if !boundedText(answer.Question, 1, 500) {
		return fmt.Errorf("%w: clarification answer must reference its question", ErrInvalidRequest)
	}
	if answer.Text == "" && answer.SelectedComponent == nil {
		return fmt.Errorf("%w: clarification answer must contain a text or a component", ErrInvalidRequest)
	}
	if !boundedText(answer.Text, 0, maxClarificationAnswerRunes) {
		return fmt.Errorf("%w: clarification answer text is too long", ErrInvalidRequest)
	}
	if answer.SelectedComponent != nil {
		if _, err := componentKey(answer.SelectedComponent.ComponentKind, answer.SelectedComponent.ComponentID); err != nil {
			return fmt.Errorf("%w: clarification answer references an invalid component", ErrInvalidRequest)
		}
	}
	return nil
}

// ConfirmScope validates and records the user's modeling scope decision. It replaces
// any earlier scope: re-confirmation is the intended way to change course.
func (state *ModelingSessionState) ConfirmScope(goal, modelKind, modelKindSource string, tables []ScopedTable, autoConfirmed bool, reason string, now time.Time) error {
	goal = strings.TrimSpace(goal)
	modelKind = strings.ToUpper(strings.TrimSpace(modelKind))
	modelKindSource = strings.ToUpper(strings.TrimSpace(modelKindSource))
	reason = strings.TrimSpace(reason)
	if !boundedText(goal, 1, maxInstructionRunes) {
		return fmt.Errorf("%w: scope confirmation requires the business goal", ErrInvalidRequest)
	}
	switch modelKind {
	case "DIM", "DWD", "DWS", "ADS":
	default:
		return fmt.Errorf("%w: scope model kind must be DIM, DWD, DWS or ADS", ErrInvalidRequest)
	}
	switch modelKindSource {
	case ModelKindSourceKeywordRule, ModelKindSourceLLMIntent, ModelKindSourceUserConfirmed:
	default:
		return fmt.Errorf("%w: scope model kind source is invalid", ErrInvalidRequest)
	}
	if len(tables) == 0 || len(tables) > maxSessionScopeTables {
		return fmt.Errorf("%w: scope must confirm 1 to %d tables", ErrInvalidRequest, maxSessionScopeTables)
	}
	if !boundedText(reason, 0, maxSessionReasonRunes) {
		return fmt.Errorf("%w: scope reason is too long", ErrInvalidRequest)
	}
	seen := make(map[string]bool, len(tables))
	normalized := make([]ScopedTable, 0, len(tables))
	primaryIDs := []string{}
	dimensionIDs := []string{}
	for _, table := range tables {
		tableID := strings.TrimSpace(table.TableID)
		source := strings.ToUpper(strings.TrimSpace(table.Source))
		if tableID == "" || seen[tableID] {
			return fmt.Errorf("%w: scope tables must be unique and non-empty", ErrInvalidRequest)
		}
		if source != ScopeTableSourceRetrieved && source != ScopeTableSourceUserAdded {
			return fmt.Errorf("%w: scope table source is invalid", ErrInvalidRequest)
		}
		role := strings.ToUpper(strings.TrimSpace(table.Role))
		if role == "" {
			role = ScopeTableRolePrimary
		}
		if role != ScopeTableRolePrimary && role != ScopeTableRoleDimension {
			return fmt.Errorf("%w: scope table role is invalid", ErrInvalidRequest)
		}
		seen[tableID] = true
		normalized = append(normalized, ScopedTable{TableID: tableID, Source: source, Role: role})
		if role == ScopeTableRoleDimension {
			dimensionIDs = append(dimensionIDs, tableID)
		} else {
			primaryIDs = append(primaryIDs, tableID)
		}
	}
	if len(primaryIDs) == 0 {
		return fmt.Errorf("%w: scope requires at least one PRIMARY source", ErrInvalidRequest)
	}
	state.Goal = goal
	state.ModelKind = modelKind
	state.ModelKindSource = modelKindSource
	state.Scope = &ConfirmedScope{
		Tables: normalized, AutoConfirmed: autoConfirmed, Reason: reason, ConfirmedAt: now.UTC(),
		SourceDecisions: []SourceScopeDecision{
			{Role: ScopeTableRolePrimary, TableIDs: primaryIDs, Status: StageStatusUserConfirmed, AutoConfirmed: autoConfirmed, Reason: reason, ConfirmedAt: now.UTC()},
			{Role: ScopeTableRoleDimension, TableIDs: dimensionIDs, Status: sourceDecisionStatus(dimensionIDs), AutoConfirmed: autoConfirmed, Reason: sourceDecisionReason(dimensionIDs, reason), ConfirmedAt: now.UTC()},
		},
	}
	// A new scope invalidates physical decisions, but the business decisions
	// deliberately confirmed before retrieval remain the input to source binding.
	if state.Blueprint == nil || state.Blueprint.Phase != BlueprintPhaseBusiness {
		state.Blueprint = nil
	}
	return nil
}

func sourceDecisionStatus(tableIDs []string) string {
	if len(tableIDs) == 0 {
		return StageStatusSkipped
	}
	return StageStatusUserConfirmed
}

func sourceDecisionReason(tableIDs []string, fallback string) string {
	if len(tableIDs) == 0 {
		return "本次目标不需要额外维度表"
	}
	return fallback
}

// RecordClarificationAsked appends the question the intent phase raised this turn.
// The list is bounded by dropping the oldest answered records first, so an open
// question is never silently discarded.
func (state *ModelingSessionState) RecordClarificationAsked(question string, candidates []ClarificationCandidate, now time.Time) {
	record := ClarificationRecord{
		Question:   strings.TrimSpace(question),
		Candidates: append([]ClarificationCandidate(nil), candidates...),
		AskedAt:    now.UTC(),
	}
	state.Clarifications = append(state.Clarifications, record)
	if len(state.Clarifications) <= maxSessionClarifications {
		return
	}
	trimmed := make([]ClarificationRecord, 0, maxSessionClarifications)
	overflow := len(state.Clarifications) - maxSessionClarifications
	for _, existing := range state.Clarifications {
		if overflow > 0 && existing.AnsweredAt != nil {
			overflow--
			continue
		}
		trimmed = append(trimmed, existing)
	}
	if overflow > 0 {
		trimmed = trimmed[overflow:]
	}
	state.Clarifications = trimmed
}

// AnswerOpenClarification records the reply to the newest unanswered clarification.
// The answer must quote the question it replies to: a stale card from an earlier
// context must not be able to answer a newer, different question.
func (state *ModelingSessionState) AnswerOpenClarification(answer ClarificationAnswer, now time.Time) error {
	answer = answer.normalized()
	if err := answer.validate(); err != nil {
		return err
	}
	for index := len(state.Clarifications) - 1; index >= 0; index-- {
		record := &state.Clarifications[index]
		if record.AnsweredAt != nil {
			continue
		}
		if record.Question != answer.Question {
			return fmt.Errorf("%w: clarification answer does not match the open question", ErrInvalidRequest)
		}
		if answer.SelectedComponent != nil && len(record.Candidates) > 0 {
			matched := false
			for _, candidate := range record.Candidates {
				if candidate.ComponentKind == answer.SelectedComponent.ComponentKind && candidate.ComponentID == answer.SelectedComponent.ComponentID {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("%w: clarification answer selects a component outside the offered candidates", ErrInvalidRequest)
			}
		}
		answeredAt := now.UTC()
		record.Answer = answer.Text
		record.SelectedComponent = answer.SelectedComponent
		record.AnsweredAt = &answeredAt
		return nil
	}
	return fmt.Errorf("%w: there is no open clarification to answer", ErrInvalidRequest)
}

// OpenClarification returns the newest unanswered question, if any.
func (state ModelingSessionState) OpenClarification() (ClarificationRecord, bool) {
	for index := len(state.Clarifications) - 1; index >= 0; index-- {
		if state.Clarifications[index].AnsweredAt == nil {
			return state.Clarifications[index], true
		}
	}
	return ClarificationRecord{}, false
}

// StageProposal records a successful planning turn. At most one proposal is STAGED:
// staging a new one supersedes the previous, mirroring the client's single staged
// candidate.
func (state *ModelingSessionState) StageProposal(requestID, mode, summary, instruction string, stageUpdates []StageDecision, reopenStages []string, now time.Time) {
	timestamp := now.UTC()
	for index := range state.Proposals {
		if state.Proposals[index].Status == ProposalStatusStaged {
			state.Proposals[index].Status = ProposalStatusSuperseded
			state.Proposals[index].UpdatedAt = timestamp
			// A superseded proposal's re-sync can no longer be applied; drop it.
			state.Proposals[index].StageUpdates = nil
			state.Proposals[index].ReopenStages = nil
		}
	}
	state.Proposals = append(state.Proposals, ProposalRecord{
		RequestID: strings.TrimSpace(requestID), Mode: mode, Summary: summary,
		Instruction: instruction, Status: ProposalStatusStaged,
		StageUpdates: stageUpdates, ReopenStages: reopenStages,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	})
	if len(state.Proposals) > maxSessionProposals {
		state.Proposals = state.Proposals[len(state.Proposals)-maxSessionProposals:]
	}
}

// ResolveProposal applies a client-reported lifecycle outcome. Only the transitions
// the editor can actually perform are accepted.
func (state *ModelingSessionState) ResolveProposal(requestID, status string, now time.Time) error {
	requestID = strings.TrimSpace(requestID)
	status = strings.ToUpper(strings.TrimSpace(status))
	if requestID == "" {
		return fmt.Errorf("%w: proposal request id is required", ErrInvalidRequest)
	}
	for index := range state.Proposals {
		proposal := &state.Proposals[index]
		if proposal.RequestID != requestID {
			continue
		}
		valid := status == ProposalStatusApplied && proposal.Status == ProposalStatusStaged ||
			status == ProposalStatusReverted && proposal.Status == ProposalStatusApplied
		if !valid {
			return fmt.Errorf("%w: proposal %s cannot move from %s to %s", ErrInvalidRequest, requestID, proposal.Status, status)
		}
		proposal.Status = status
		proposal.UpdatedAt = now.UTC()
		if status == ProposalStatusApplied && (len(proposal.StageUpdates) > 0 || len(proposal.ReopenStages) > 0) {
			state.ApplyProposalStageUpdates(proposal.StageUpdates, proposal.ReopenStages, proposal.Summary, now)
			// The re-sync is consumed: reverting the canvas later restores the
			// graph, and the next MODIFY turn re-derives from whatever it sees.
			proposal.StageUpdates = nil
			proposal.ReopenStages = nil
		}
		return nil
	}
	return fmt.Errorf("%w: proposal %s is not part of this session", ErrInvalidRequest, requestID)
}

// RecordProposalExecution stores a preview outcome on the proposal, derives the
// findings that matter for modeling, and reopens the blueprint stages those
// findings implicate: a fan-out or cardinality warning reopens JOIN; an empty
// result reopens JOIN and FILTER, the two places a zero-row model usually goes
// wrong. Values are never recorded, only counts and codes.
func (state *ModelingSessionState) RecordProposalExecution(requestID string, execution ProposalExecution, now time.Time) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("%w: proposal request id is required", ErrInvalidRequest)
	}
	if execution.RowCount < 0 || len(execution.Warnings) > maxExecutionWarnings || !boundedText(execution.Error, 0, 500) {
		return fmt.Errorf("%w: execution report is out of bounds", ErrInvalidRequest)
	}
	var proposal *ProposalRecord
	for index := range state.Proposals {
		if state.Proposals[index].RequestID == requestID {
			proposal = &state.Proposals[index]
			break
		}
	}
	if proposal == nil {
		return fmt.Errorf("%w: proposal %s is not part of this session", ErrInvalidRequest, requestID)
	}
	if proposal.Status != ProposalStatusApplied {
		return fmt.Errorf("%w: only an applied proposal can report an execution", ErrInvalidRequest)
	}
	timestamp := now.UTC()
	normalized := ProposalExecution{RowCount: execution.RowCount, DurationMs: execution.DurationMs, Error: strings.TrimSpace(execution.Error), ExecutedAt: timestamp}
	for _, warning := range execution.Warnings {
		code := strings.ToUpper(strings.TrimSpace(warning.Code))
		if code == "" || !boundedText(warning.Message, 0, 500) || !boundedText(warning.JoinID, 0, 128) {
			continue
		}
		normalized.Warnings = append(normalized.Warnings, ExecutionWarning{Code: code, Message: strings.TrimSpace(warning.Message), JoinID: strings.TrimSpace(warning.JoinID)})
	}
	proposal.Execution = &normalized
	proposal.UpdatedAt = timestamp

	findings := []ExecutionFinding{}
	reopen := map[string]string{}
	if normalized.Error != "" {
		findings = append(findings, ExecutionFinding{Code: ExecutionFindingExecutionError, Message: "上一版方案预览执行失败：" + normalized.Error, RequestID: requestID, At: timestamp})
	} else if normalized.RowCount == 0 {
		findings = append(findings, ExecutionFinding{Code: ExecutionFindingEmptyResult, Message: "上一版方案预览返回 0 行：关联键或过滤条件可能不正确", RequestID: requestID, At: timestamp})
		reopen[StageJoin] = "预览返回 0 行，请核对关联键"
		reopen[StageFilter] = "预览返回 0 行，请核对过滤条件"
	}
	for _, warning := range normalized.Warnings {
		switch warning.Code {
		case "JOIN_FANOUT_RISK", "JOIN_CARDINALITY_MISMATCH", "JOIN_MANY_TO_MANY":
			message := warning.Message
			if message == "" {
				message = "预览发现关联可能扇出或基数不符"
			}
			findings = append(findings, ExecutionFinding{Code: warning.Code, Message: message, JoinID: warning.JoinID, RequestID: requestID, At: timestamp})
			reopen[StageJoin] = "预览发现关联可能扇出或基数不符，请重新确认关联键或先聚合再关联"
		}
	}
	if len(findings) > maxExecutionFindings {
		findings = findings[:maxExecutionFindings]
	}
	state.ExecutionFindings = findings
	if state.Blueprint != nil {
		for index := range state.Blueprint.Stages {
			target := &state.Blueprint.Stages[index]
			reason, ok := reopen[target.Stage]
			if !ok || target.Status == StageStatusSkipped || !StageApplicable(state.ModelKind, target.Stage) {
				continue
			}
			target.Status = StageStatusProposed
			target.NeedsUserConfirmation = true
			target.Reason = reason
			target.DecidedAt = timestamp
		}
	}
	return nil
}

// ScopeTableIDs returns the confirmed table ids in confirmation order.
func (state ModelingSessionState) ScopeTableIDs() []string {
	if state.Scope == nil {
		return nil
	}
	result := make([]string, 0, len(state.Scope.Tables))
	for _, table := range state.Scope.Tables {
		result = append(result, table.TableID)
	}
	return result
}

// SessionStore persists modeling sessions. Implementations must scope every read
// and write by tenant and actor: sessions are personal working state.
type SessionStore interface {
	// Create persists a new ACTIVE session, closing any previous ACTIVE session of
	// the same actor and dataset in the same transaction.
	Create(ctx context.Context, session *ModelingSession) error
	Get(ctx context.Context, tenantID, actorID, sessionID string) (ModelingSession, error)
	// FindActiveByDataset returns the actor's ACTIVE session for a saved dataset.
	FindActiveByDataset(ctx context.Context, tenantID, actorID, datasetID string) (ModelingSession, bool, error)
	// Update persists the full state document using the revision the caller read;
	// a concurrent write surfaces as ErrSessionConflict.
	Update(ctx context.Context, session *ModelingSession) error
}
