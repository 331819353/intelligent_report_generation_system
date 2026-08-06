package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const MaxLoopTranscriptBytes = 512 << 10

var (
	ErrInvalidLoop          = errors.New("question cognition loop is invalid")
	ErrLoopBudgetExhausted  = errors.New("question cognition loop budget is exhausted")
	ErrLoopTimeout          = errors.New("question cognition loop timed out")
	ErrLoopNoProgress       = errors.New("question cognition loop made no progress")
	ErrLoopEvidenceRejected = errors.New("question cognition loop rejected action evidence")
	ErrLoopToolUnavailable  = errors.New("question cognition loop tool is unavailable")
	ErrLoopToolBlocked      = errors.New("question cognition loop tool was blocked")
	ErrLoopToolFailed       = errors.New("question cognition loop tool failed")
	ErrLoopCognitionFailed  = errors.New("question cognition loop model call failed")
)

type CognitionRunner interface {
	Execute(context.Context, cognition.RoundRequest) (cognition.RoundResult, error)
}

type GovernedToolRegistry interface {
	AvailableTools(toolhost.AuthorizationContext, toolhost.BudgetAllowance) ([]toolhost.ToolName, error)
	Execute(context.Context, toolhost.Invocation) (toolhost.Execution, error)
}

type LoopOptions struct {
	PromptVersion   string
	PreferredModel  string
	MaxOutputTokens int
	ResourceType    string
}

func DefaultLoopOptions() LoopOptions {
	return LoopOptions{
		PromptVersion: "askdata-question-loop-v1", MaxOutputTokens: 8192,
		ResourceType: "ASKDATA_QUESTION_RUN",
	}
}

func (options LoopOptions) Validate() error {
	if strings.TrimSpace(options.PromptVersion) == "" || len(options.PromptVersion) > 128 ||
		options.MaxOutputTokens < 1 || options.MaxOutputTokens > 8192 ||
		askdata.ID(options.ResourceType).Validate() != nil {
		return ErrInvalidLoop
	}
	return nil
}

type GovernedFact struct {
	Fact     cognition.PromptFact
	Evidence askdata.EvidenceRef
}

type LoopRequest struct {
	Run              Run
	Stage            cognition.Stage
	Facts            []GovernedFact
	Authorization    toolhost.AuthorizationContext
	SeenActionHashes []askdata.ContentHash
	SeenToolCallIDs  []askdata.ID
}

type LoopResult struct {
	Decision         cognition.RoundResult
	CognitionRounds  []CognitionExecution
	Usage            BudgetUsage
	ToolExecutions   []toolhost.Execution
	Transcript       []ai.Message
	SeenActionHashes []askdata.ContentHash
	SeenToolCallIDs  []askdata.ID
}

// CognitionExecution is the durable-safe summary of one successful model
// round. It contains the structured action and provider audit identifiers, but
// never the prompt, raw provider response or hidden reasoning.
type CognitionExecution struct {
	Round      cognition.RoundResult
	DurationMS int64
}

type Loop struct {
	cognition CognitionRunner
	tools     GovernedToolRegistry
	options   LoopOptions
}

func NewLoop(
	cognitionRunner CognitionRunner,
	tools GovernedToolRegistry,
	configured ...LoopOptions,
) (*Loop, error) {
	if cognitionRunner == nil || tools == nil || len(configured) > 1 {
		return nil, ErrInvalidLoop
	}
	options := DefaultLoopOptions()
	if len(configured) == 1 {
		options = configured[0]
	}
	if options.Validate() != nil {
		return nil, ErrInvalidLoop
	}
	return &Loop{cognition: cognitionRunner, tools: tools, options: options}, nil
}

func (loop *Loop) Run(ctx context.Context, request LoopRequest) (LoopResult, error) {
	result := LoopResult{
		Usage: request.Run.Usage, CognitionRounds: []CognitionExecution{},
		ToolExecutions: []toolhost.Execution{}, Transcript: []ai.Message{},
		SeenActionHashes: append([]askdata.ContentHash(nil), request.SeenActionHashes...),
		SeenToolCallIDs:  append([]askdata.ID(nil), request.SeenToolCallIDs...),
	}
	if loop == nil || loop.cognition == nil || loop.tools == nil || loop.options.Validate() != nil || ctx == nil {
		return result, ErrInvalidLoop
	}
	knownEvidence, facts, err := validateLoopRequest(request)
	if err != nil {
		return result, err
	}
	started := time.Now()
	remainingDuration := request.Run.Limits.MaxDurationMS - request.Run.Usage.ElapsedMS
	if remainingDuration <= 0 {
		result.Usage.Exhausted = true
		return result, ErrLoopBudgetExhausted
	}
	loopContext, cancel := context.WithTimeout(ctx, time.Duration(remainingDuration)*time.Millisecond)
	defer cancel()

	available, err := loop.availableTools(request.Stage, request.Authorization, result.Usage, request.Run.Limits)
	if err != nil {
		return result, err
	}
	messages, err := cognition.BuildMessages(cognition.PromptInput{
		Stage: request.Stage, Facts: facts, AvailableTools: available,
	})
	if err != nil {
		return result, ErrInvalidLoop
	}
	result.Transcript = cloneMessages(messages)

	for {
		if err := reserveCognitionStep(request.Run.Limits, result.Usage); err != nil {
			result.Usage.Exhausted = true
			return result, err
		}
		roundStarted := time.Now()
		round, roundErr := loop.cognition.Execute(loopContext, cognition.RoundRequest{
			TenantID: string(request.Run.TenantID), ActorID: string(request.Run.ActorID),
			Stage: request.Stage, PromptVersion: loop.options.PromptVersion,
			ResourceType: loop.options.ResourceType, ResourceID: string(request.Run.ID),
			PreferredModel: loop.options.PreferredModel, Messages: cloneMessages(result.Transcript),
			SeenActionHashes: append([]askdata.ContentHash(nil), result.SeenActionHashes...),
			SeenToolCallIDs:  append([]askdata.ID(nil), result.SeenToolCallIDs...),
			MaxOutputTokens:  loop.options.MaxOutputTokens,
		})
		result.Usage.StepCount++
		result.Usage.LLMCallsUsed++
		updateLoopElapsed(&result.Usage, request.Run.Usage.ElapsedMS, started)
		if roundErr != nil {
			classified := classifyLoopContextError(ctx, loopContext, roundErr)
			if errors.Is(classified, ErrLoopTimeout) {
				result.Usage.Exhausted = true
			}
			return result, classified
		}
		if err := validateCognitionRound(round, request.Stage, result.SeenActionHashes); err != nil {
			return result, err
		}
		if !actionUsesKnownEvidence(round.Action, knownEvidence) {
			return result, ErrLoopEvidenceRejected
		}
		result.CognitionRounds = append(result.CognitionRounds, CognitionExecution{
			Round: cloneRoundResult(round), DurationMS: boundedElapsedMilliseconds(roundStarted),
		})
		result.SeenActionHashes = append(result.SeenActionHashes, round.ActionHash)
		if round.Action.Action != cognition.ActionCallTool {
			result.Decision = cloneRoundResult(round)
			return result, nil
		}

		call := *round.Action.ToolCall
		if containsToolCallID(result.SeenToolCallIDs, call.CallID) {
			return result, ErrLoopNoProgress
		}
		available, err = loop.availableTools(request.Stage, request.Authorization, result.Usage, request.Run.Limits)
		if err != nil {
			return result, err
		}
		if !containsToolName(available, call.Tool) {
			return result, ErrLoopToolUnavailable
		}
		if result.Usage.StepCount >= request.Run.Limits.MaxSteps {
			result.Usage.Exhausted = true
			return result, ErrLoopBudgetExhausted
		}
		assistantMessage, err := cognition.AssistantMessage(round)
		if err != nil {
			return result, ErrInvalidLoop
		}
		allowance := remainingToolBudget(request.Run.Limits, result.Usage)
		execution, err := loop.tools.Execute(loopContext, toolhost.Invocation{
			Authorization: request.Authorization,
			Budget:        allowance,
			Call:          call,
		})
		if err != nil {
			classified := classifyLoopContextError(ctx, loopContext, err)
			if errors.Is(classified, ErrLoopTimeout) {
				result.Usage.Exhausted = true
			}
			return result, classified
		}
		if execution.Validate() != nil || execution.Response.CallID != call.CallID ||
			execution.Response.Tool != call.Tool || !chargeFitsAllowance(execution.Charge, allowance) {
			return result, ErrInvalidLoop
		}
		result.ToolExecutions = append(result.ToolExecutions, cloneToolExecution(execution))
		result.Usage.StepCount++
		result.Usage.ToolCallsUsed += execution.Charge.ToolCalls
		result.Usage.FormalQueriesUsed += execution.Charge.FormalQueries
		result.Usage.ValidationQueriesUsed += execution.Charge.ValidationQueries
		updateLoopElapsed(&result.Usage, request.Run.Usage.ElapsedMS, started)
		result.SeenToolCallIDs = append(result.SeenToolCallIDs, call.CallID)
		toolMessage, err := cognition.ToolMessage(execution.Response)
		if err != nil {
			return result, ErrInvalidLoop
		}
		result.Transcript = append(result.Transcript, assistantMessage, toolMessage)
		if transcriptBytes(result.Transcript) > MaxLoopTranscriptBytes {
			result.Usage.Exhausted = true
			return result, ErrLoopBudgetExhausted
		}
		for _, evidence := range execution.Response.EvidenceRefs {
			if previous, exists := knownEvidence[evidence.EvidenceID]; exists && previous != evidence {
				return result, ErrLoopEvidenceRejected
			}
			knownEvidence[evidence.EvidenceID] = evidence
		}
		if execution.Response.Status == toolhost.ResponseRejected {
			return result, ErrLoopToolBlocked
		}
		if execution.Response.Status == toolhost.ResponseFailed {
			return result, ErrLoopToolFailed
		}
		if !execution.Response.MadeProgress {
			return result, ErrLoopNoProgress
		}
	}
}

func validateLoopRequest(request LoopRequest) (map[askdata.ID]askdata.EvidenceRef, []cognition.PromptFact, error) {
	if request.Run.Validate() != nil || request.Run.Terminal() ||
		!stageAllowedForRunState(request.Stage, request.Run.State) ||
		request.Authorization.Validate() != nil ||
		request.Authorization.Scope.TenantID != request.Run.TenantID ||
		request.Authorization.Scope.ActorID != request.Run.ActorID ||
		request.Authorization.DomainID != request.Run.DomainID ||
		request.Authorization.Scope.PolicyHash != request.Run.PolicyScopeHash ||
		request.Authorization.Scope.Release != request.Run.Release ||
		len(request.Facts) < 1 || len(request.Facts) > cognition.MaxPromptFacts ||
		len(request.SeenActionHashes) > 16 || len(request.SeenToolCallIDs) > 16 {
		return nil, nil, ErrInvalidLoop
	}
	known := make(map[askdata.ID]askdata.EvidenceRef, len(request.Facts))
	facts := make([]cognition.PromptFact, len(request.Facts))
	for index, governed := range request.Facts {
		if governed.Evidence.Validate() != nil || governed.Fact.EvidenceID != governed.Evidence.EvidenceID ||
			governed.Fact.ContentHash != governed.Evidence.ContentHash {
			return nil, nil, ErrInvalidLoop
		}
		if _, duplicate := known[governed.Evidence.EvidenceID]; duplicate {
			return nil, nil, ErrInvalidLoop
		}
		known[governed.Evidence.EvidenceID] = governed.Evidence
		facts[index] = governed.Fact
	}
	seenHashes := map[askdata.ContentHash]bool{}
	for _, hash := range request.SeenActionHashes {
		if hash.Validate() != nil || seenHashes[hash] {
			return nil, nil, ErrInvalidLoop
		}
		seenHashes[hash] = true
	}
	seenCalls := map[askdata.ID]bool{}
	for _, callID := range request.SeenToolCallIDs {
		if callID.Validate() != nil || seenCalls[callID] {
			return nil, nil, ErrInvalidLoop
		}
		seenCalls[callID] = true
	}
	return known, facts, nil
}

func (loop *Loop) availableTools(
	stage cognition.Stage,
	authorization toolhost.AuthorizationContext,
	usage BudgetUsage,
	limits BudgetLimits,
) ([]toolhost.ToolName, error) {
	available, err := loop.tools.AvailableTools(authorization, remainingToolBudget(limits, usage))
	if err != nil {
		return nil, ErrInvalidLoop
	}
	allowed := allowedToolsForCognitionStage(stage)
	result := make([]toolhost.ToolName, 0, len(available))
	for _, tool := range available {
		if allowed[tool] {
			result = append(result, tool)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func remainingToolBudget(limits BudgetLimits, usage BudgetUsage) toolhost.BudgetAllowance {
	return toolhost.BudgetAllowance{
		ToolCallsRemaining:         maxInt(0, limits.MaxToolCalls-usage.ToolCallsUsed),
		FormalQueriesRemaining:     maxInt(0, limits.MaxFormalQueries-usage.FormalQueriesUsed),
		ValidationQueriesRemaining: maxInt(0, limits.MaxValidationQueries-usage.ValidationQueriesUsed),
	}
}

func reserveCognitionStep(limits BudgetLimits, usage BudgetUsage) error {
	if usage.StepCount >= limits.MaxSteps || usage.LLMCallsUsed >= limits.MaxLLMCalls ||
		usage.ElapsedMS >= limits.MaxDurationMS {
		return ErrLoopBudgetExhausted
	}
	return nil
}

func validateCognitionRound(
	round cognition.RoundResult,
	stage cognition.Stage,
	seen []askdata.ContentHash,
) error {
	if round.Action.Validate() != nil || round.Action.Stage != stage || round.ActionHash.Validate() != nil ||
		askdata.ID(round.AIRequestID).Validate() != nil || strings.TrimSpace(round.ProviderModel) == "" ||
		!utf8.ValidString(round.ProviderModel) || utf8.RuneCountInString(round.ProviderModel) > 256 ||
		round.Attempts < 1 || round.Attempts > 5 || round.Usage.PromptTokens < 1 ||
		round.Usage.CompletionTokens < 1 || round.Usage.TotalTokens < round.Usage.PromptTokens+round.Usage.CompletionTokens ||
		round.CostMicros < 0 || round.RedactionCount < 0 {
		return ErrInvalidLoop
	}
	payload, err := json.Marshal(round.Action)
	if err != nil || askdata.HashBytes(payload) != round.ActionHash {
		return ErrInvalidLoop
	}
	for _, previous := range seen {
		if previous == round.ActionHash {
			return ErrLoopNoProgress
		}
	}
	return nil
}

func chargeFitsAllowance(charge toolhost.BudgetCharge, allowance toolhost.BudgetAllowance) bool {
	return charge.ToolCalls <= allowance.ToolCallsRemaining &&
		charge.FormalQueries <= allowance.FormalQueriesRemaining &&
		charge.ValidationQueries <= allowance.ValidationQueriesRemaining
}

func actionUsesKnownEvidence(action cognition.Action, known map[askdata.ID]askdata.EvidenceRef) bool {
	values := append([]askdata.EvidenceRef(nil), action.EvidenceRefs...)
	if action.BindingProposal != nil {
		values = append(values, action.BindingProposal.Confidence.Evidence...)
	}
	if action.PlanProposal != nil {
		values = append(values, action.PlanProposal.Confidence.Evidence...)
	}
	if action.AnomalyAnalysis != nil {
		values = append(values, action.AnomalyAnalysis.EvidenceRefs...)
	}
	if action.Verification != nil {
		for _, check := range action.Verification.Checks {
			values = append(values, check.EvidenceRefs...)
		}
	}
	if action.FinalDecision != nil {
		values = append(values, action.FinalDecision.EvidenceRefs...)
	}
	if action.Clarification != nil {
		for _, option := range action.Clarification.Options {
			values = append(values, option.EvidenceRefs...)
		}
	}
	if action.Block != nil {
		values = append(values, action.Block.EvidenceRefs...)
	}
	if action.ToolCall != nil {
		for _, option := range action.ToolCall.Arguments.ClarificationOptions {
			values = append(values, option.EvidenceRefs...)
		}
	}
	for _, evidence := range values {
		expected, exists := known[evidence.EvidenceID]
		if !exists || expected != evidence {
			return false
		}
	}
	return true
}

func stageAllowedForRunState(stage cognition.Stage, state State) bool {
	switch state {
	case StateContextReady, StateUnderstanding:
		return stage == cognition.StageUnderstanding
	case StateRetrieving:
		return stage == cognition.StageCandidateJudgment
	case StateBinding:
		return stage == cognition.StageCandidateJudgment || stage == cognition.StageDisambiguation
	case StateGraphValidating, StateIRReady, StatePlanValidating:
		return stage == cognition.StagePlanSelection
	case StateResultVerifying:
		return stage == cognition.StageAnomalyAnalysis || stage == cognition.StageResultVerification
	default:
		return false
	}
}

func allowedToolsForCognitionStage(stage cognition.Stage) map[toolhost.ToolName]bool {
	commonClarification := toolhost.ToolRequestClarification
	result := map[toolhost.ToolName]bool{}
	add := func(values ...toolhost.ToolName) {
		for _, value := range values {
			result[value] = true
		}
	}
	switch stage {
	case cognition.StageUnderstanding:
		add(toolhost.ToolSearchSemanticObjects, commonClarification)
	case cognition.StageCandidateJudgment:
		add(toolhost.ToolSearchSemanticObjects, toolhost.ToolGetSemanticContracts,
			toolhost.ToolGetCertifiedExamples, toolhost.ToolGetDataQualityStatus, commonClarification)
	case cognition.StageDisambiguation:
		add(toolhost.ToolSearchSemanticObjects, toolhost.ToolGetSemanticContracts,
			toolhost.ToolLookupDimensionValues, toolhost.ToolGetCertifiedExamples,
			toolhost.ToolResolveGraphPlan, toolhost.ToolValidateSemanticBundle, commonClarification)
	case cognition.StagePlanSelection:
		add(toolhost.ToolGetSemanticContracts, toolhost.ToolResolveGraphPlan,
			toolhost.ToolValidateSemanticBundle, toolhost.ToolGetDataQualityStatus,
			toolhost.ToolCompileSemanticQuery, toolhost.ToolValidateQueryPlan,
			toolhost.ToolProbeJoinCardinality, toolhost.ToolExecuteQueryPlan, commonClarification)
	case cognition.StageAnomalyAnalysis:
		add(toolhost.ToolGetDataQualityStatus, toolhost.ToolValidateQueryPlan,
			toolhost.ToolProbeJoinCardinality, toolhost.ToolExecuteValidationQuery,
			toolhost.ToolCompareCandidateResults, commonClarification)
	case cognition.StageResultVerification:
		add(toolhost.ToolGetDataQualityStatus, toolhost.ToolExecuteValidationQuery,
			toolhost.ToolCompareCandidateResults, commonClarification)
	case cognition.StageAssetReview, cognition.StageFeedbackAttribution, cognition.StageReleaseReview:
		add(toolhost.ToolGetSemanticContracts, toolhost.ToolGetDataQualityStatus)
	}
	return result
}

func classifyLoopContextError(parent, loopContext context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(loopContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrLoopTimeout
	}
	classification := ai.ClassifyError(err)
	if classification.Code == ai.ErrorCodeToolNoProgress {
		return ErrLoopNoProgress
	}
	return ErrLoopCognitionFailed
}

func updateLoopElapsed(usage *BudgetUsage, base int64, started time.Time) {
	if usage == nil {
		return
	}
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	usage.ElapsedMS = base + elapsed
}

func boundedElapsedMilliseconds(started time.Time) int64 {
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	if elapsed > 600_000 {
		return 600_000
	}
	return elapsed
}

func transcriptBytes(messages []ai.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.ToolCallID) + len(message.ToolName)
		for _, part := range message.Parts {
			total += len(part.Text) + len(part.ImageURL)
		}
	}
	return total
}

func cloneMessages(values []ai.Message) []ai.Message {
	result := make([]ai.Message, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Parts = append([]ai.ContentPart(nil), value.Parts...)
	}
	return result
}

func cloneRoundResult(value cognition.RoundResult) cognition.RoundResult {
	copy := value
	payload, err := json.Marshal(value.Action)
	if err == nil {
		var action cognition.Action
		if askdata.DecodeStrictJSON(payload, &action) == nil {
			copy.Action = action
		}
	}
	return copy
}

func cloneToolExecution(value toolhost.Execution) toolhost.Execution {
	copy := value
	copy.Response.Result = append(json.RawMessage(nil), value.Response.Result...)
	copy.Response.EvidenceRefs = append([]askdata.EvidenceRef(nil), value.Response.EvidenceRefs...)
	if value.Response.Error != nil {
		errorCopy := *value.Response.Error
		copy.Response.Error = &errorCopy
	}
	return copy
}

func containsToolName(values []toolhost.ToolName, target toolhost.ToolName) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsToolCallID(values []askdata.ID, target askdata.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
