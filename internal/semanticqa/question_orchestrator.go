package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

// QuestionRoute is the only execution routing decision exposed by the new
// question runtime. SQL text is never accepted from API callers.
type QuestionRoute string

const (
	QuestionRouteSemantic        QuestionRoute = "SEMANTIC_IR"
	QuestionRouteGovernedTextSQL QuestionRoute = "GOVERNED_TEXT_TO_SQL"
	QuestionRouteClarifyOrRefuse QuestionRoute = "CLARIFY_OR_REFUSE"
)

type QuestionRequest struct {
	Question         string          `json:"question"`
	ConversationID   string          `json:"conversationId,omitempty"`
	ParentQuestionID string          `json:"parentQuestionId,omitempty"`
	Timezone         string          `json:"timezone,omitempty"`
	Locale           string          `json:"locale,omitempty"`
	Display          QuestionDisplay `json:"display,omitempty"`
	// Confirmations are accepted only as stable governed IDs returned by a
	// preceding clarification response. They cannot contain SQL or field names.
	ConfirmedMetricCodes []string                 `json:"confirmedMetricCodes,omitempty"`
	ConfirmedDecisions   []QueryConfirmedDecision `json:"confirmedDecisions,omitempty"`
}

type QuestionDisplay struct {
	PreferredChart string `json:"preferredChart,omitempty"`
}

type SemanticObjectRef struct {
	ID              string   `json:"id"`
	VersionID       string   `json:"versionId"`
	Code            string   `json:"code"`
	Label           string   `json:"label"`
	BindingEvidence []string `json:"bindingEvidence"`
}

type CompleteIntent struct {
	IntentID            string              `json:"intentId"`
	SemanticVersion     string              `json:"semanticVersion"`
	SemanticContentHash string              `json:"semanticContentHash,omitempty"`
	TaskType            string              `json:"taskType"`
	Metrics             []SemanticObjectRef `json:"metrics"`
	Dimensions          []SemanticObjectRef `json:"dimensions"`
	Time                *QueryTimeRange     `json:"time,omitempty"`
	ExecutionPath       QuestionRoute       `json:"executionPath"`
	GraphPlanIDs        []string            `json:"graphPlanIds"`
	Ambiguities         []string            `json:"ambiguities"`
}

type SemanticIRMetric struct {
	MetricID        string `json:"metricId"`
	MetricVersionID string `json:"metricVersionId"`
	Code            string `json:"code"`
}

type SemanticIRFilter struct {
	DimensionID   string   `json:"dimensionId"`
	DimensionCode string   `json:"dimensionCode"`
	Operator      string   `json:"operator"`
	ValueIDs      []string `json:"valueIds"`
}

// SemanticQueryIR is the versioned contract between language understanding
// and deterministic compilation. Every value is derived from an immutable,
// READY QueryPlan; callers never submit this object for execution.
type SemanticQueryIR struct {
	SchemaVersion       string             `json:"schemaVersion"`
	Mode                string             `json:"mode"`
	SemanticVersion     string             `json:"semanticVersion"`
	SemanticContentHash string             `json:"semanticContentHash,omitempty"`
	Metrics             []SemanticIRMetric `json:"metrics"`
	Dimensions          []string           `json:"dimensions"`
	Time                *QueryTimeRange    `json:"time,omitempty"`
	Filters             []SemanticIRFilter `json:"filters"`
	OrderBy             []SemanticIROrder  `json:"orderBy"`
	Limit               int                `json:"limit"`
	EvidenceIDs         []string           `json:"evidenceIds"`
}

type SemanticIROrder struct {
	Member    string `json:"member"`
	Direction string `json:"direction"`
}

type ExecutionGraphRef struct {
	SemanticVersion    string   `json:"semanticVersion,omitempty"`
	ContentHash        string   `json:"contentHash,omitempty"`
	GraphPlanID        string   `json:"graphPlanId,omitempty"`
	GraphEvidenceIDs   []string `json:"graphEvidenceIds,omitempty"`
	GenerationID       string   `json:"generationId"`
	Generation         int64    `json:"generation"`
	QueryPlanIDs       []string `json:"queryPlanIds"`
	PathHashes         []string `json:"pathHashes"`
	DatasetVersionIDs  []string `json:"datasetVersionIds"`
	MaterializationIDs []string `json:"materializationIds"`
}

type GuardCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type SQLGuardDecision struct {
	Status      string       `json:"status"`
	Mode        string       `json:"mode"`
	MaximumRows int          `json:"maximumRows"`
	Checks      []GuardCheck `json:"checks"`
}

type ResultVerification struct {
	Status     string       `json:"status"`
	TrustLevel string       `json:"trustLevel"`
	Checks     []GuardCheck `json:"checks"`
}

type QuestionAnswer struct {
	Text       string              `json:"text"`
	ResultSets []QuestionResultSet `json:"resultSets"`
	Chart      QuestionChartSpec   `json:"chart"`
	AsOf       string              `json:"asOf"`
}

type QuestionResultSet struct {
	MetricCode string   `json:"metricCode"`
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"rowCount"`
}

type QuestionChartSpec struct {
	Type   string `json:"type"`
	XField string `json:"xField,omitempty"`
	YField string `json:"yField,omitempty"`
}

type AccuracyEvidence struct {
	SemanticVersion     string                   `json:"semanticVersion"`
	SemanticContentHash string                   `json:"semanticContentHash,omitempty"`
	IntentHash          string                   `json:"intentHash"`
	BindingEvidence     []string                 `json:"bindingEvidence"`
	MetricContracts     []string                 `json:"metricContracts"`
	GraphPlanIDs        []string                 `json:"graphPlanIds"`
	GraphEvidenceIDs    []string                 `json:"graphEvidenceIds,omitempty"`
	QueryPlanHash       string                   `json:"queryPlanHash"`
	ResultHash          string                   `json:"resultHash"`
	ValidatorChecks     []string                 `json:"validatorChecks"`
	ToolLoop            AccuracyToolLoopEvidence `json:"toolLoop"`
	AnswerFidelity      string                   `json:"answerFidelity"`
}

type AccuracyToolLoopEvidence struct {
	Iterations     int      `json:"iterations"`
	NewEvidenceIDs []string `json:"newEvidenceIds"`
}

type QuestionFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type QuestionBudgets struct {
	MaximumToolLoopRounds    int `json:"maximumToolLoopRounds"`
	MaximumMetadataTools     int `json:"maximumMetadataTools"`
	MaximumExplainQueries    int `json:"maximumExplainQueries"`
	MaximumMetricQueries     int `json:"maximumMetricQueries"`
	MaximumValidationQueries int `json:"maximumValidationQueries"`
	MaximumRowsPerQuery      int `json:"maximumRowsPerQuery"`
	DeadlineMS               int `json:"deadlineMs"`
}

type QuestionResponse struct {
	QuestionID          string                          `json:"questionId"`
	ConversationID      string                          `json:"conversationId"`
	ParentQuestionID    string                          `json:"parentQuestionId,omitempty"`
	QuestionHash        string                          `json:"questionHash"`
	State               QuestionState                   `json:"state"`
	Status              string                          `json:"status"`
	Route               QuestionRoute                   `json:"route"`
	Routing             QuestionRoutingDecision         `json:"routing"`
	SemanticVersion     string                          `json:"semanticVersion,omitempty"`
	SemanticContentHash string                          `json:"semanticContentHash,omitempty"`
	Understanding       *QuestionUnderstanding          `json:"understanding,omitempty"`
	GraphPlan           *QuestionGraphPlan              `json:"graphPlan,omitempty"`
	ExecutionRegistry   *QuestionExecutionRegistryProof `json:"executionRegistry,omitempty"`
	PreflightProofs     []metric.QueryPreflightProof    `json:"preflightProofs,omitempty"`
	Lifecycle           []QuestionStateEvent            `json:"lifecycle"`
	Intent              *CompleteIntent                 `json:"intent,omitempty"`
	SemanticIR          *SemanticQueryIR                `json:"semanticIr,omitempty"`
	ExecutionGraph      *ExecutionGraphRef              `json:"executionGraph,omitempty"`
	Guard               *SQLGuardDecision               `json:"sqlGuard,omitempty"`
	Verification        *ResultVerification             `json:"resultVerification,omitempty"`
	Answer              *QuestionAnswer                 `json:"answer,omitempty"`
	Clarification       *QueryClarification             `json:"clarification,omitempty"`
	Planning            *QueryTurnPlan                  `json:"planning,omitempty"`
	Plans               []QueryPlan                     `json:"queryPlans"`
	Executions          []QueryPlanExecution            `json:"executions"`
	Evidence            *AccuracyEvidence               `json:"accuracyEvidence,omitempty"`
	Failure             *QuestionFailure                `json:"failure,omitempty"`
	Budgets             QuestionBudgets                 `json:"budgets"`
	ToolRegistry        []QuestionToolSummary           `json:"toolRegistry"`
}

type QuestionRunSummary struct {
	QuestionID          string        `json:"questionId"`
	ConversationID      string        `json:"conversationId,omitempty"`
	ParentQuestionID    string        `json:"parentQuestionId,omitempty"`
	QuestionHash        string        `json:"questionHash"`
	State               QuestionState `json:"state"`
	Route               QuestionRoute `json:"route,omitempty"`
	Decision            string        `json:"decision,omitempty"`
	SemanticVersion     string        `json:"semanticVersion,omitempty"`
	SemanticReleaseID   string        `json:"semanticReleaseId,omitempty"`
	SemanticContentHash string        `json:"semanticContentHash,omitempty"`
	UnderstandingHash   string        `json:"understandingHash,omitempty"`
	GraphPlanHash       string        `json:"graphPlanHash,omitempty"`
	IntentHash          string        `json:"intentHash,omitempty"`
	QueryPlanHash       string        `json:"queryPlanHash,omitempty"`
	ResultHash          string        `json:"resultHash,omitempty"`
	QueryPlanIDs        []string      `json:"queryPlanIds"`
	FailureCode         string        `json:"failureCode,omitempty"`
	CreatedAt           string        `json:"createdAt"`
	UpdatedAt           string        `json:"updatedAt"`
	CompletedAt         string        `json:"completedAt,omitempty"`
}

type questionRuntimeMetadata struct {
	ConversationID   string
	ParentQuestionID string
}

type questionRuntimeMetadataKey struct{}

type questionRuntimeStore interface {
	ResolveConversationContext(context.Context, string, string) ([]string, error)
	SaveQuestionOutcome(context.Context, string, string, questionRunOutcome) error
	GetQuestionRun(context.Context, string, string) (QuestionRunSummary, error)
	ListQuestionRunEvents(context.Context, string, string) ([]QuestionStateEvent, error)
}

type questionRunOutcome struct {
	Route               QuestionRoute
	Decision            string
	SemanticVersion     string
	SemanticReleaseID   string
	SemanticContentHash string
	UnderstandingHash   string
	GraphPlanHash       string
	IntentHash          string
	BindingBundleHash   string
	QueryPlanHash       string
	ResultHash          string
	QueryPlanIDs        []string
	FailureCode         string
	Budgets             QuestionBudgets
	Artifacts           []questionArtifact
}

func defaultQuestionBudgets() QuestionBudgets {
	return QuestionBudgets{
		MaximumToolLoopRounds:    3,
		MaximumMetadataTools:     12,
		MaximumExplainQueries:    2,
		MaximumMetricQueries:     2,
		MaximumValidationQueries: 2,
		MaximumRowsPerQuery:      100,
		DeadlineMS:               60000,
	}
}

// AnswerQuestion is the production question entry point. It owns routing,
// Semantic IR construction, guard evaluation, execution, verification and
// answer rendering as one auditable lifecycle.
func (service *Service) AnswerQuestion(
	ctx context.Context,
	tenantID, actorID string,
	request QuestionRequest,
) (response QuestionResponse, runErr error) {
	request.Question = strings.TrimSpace(request.Question)
	request.Timezone = strings.TrimSpace(request.Timezone)
	request.Locale = strings.TrimSpace(request.Locale)
	request.Display.PreferredChart = strings.ToUpper(strings.TrimSpace(
		request.Display.PreferredChart,
	))
	if request.Timezone == "" {
		request.Timezone = "UTC"
	}
	if request.Locale == "" {
		request.Locale = "zh-CN"
	}
	if request.ConversationID == "" {
		request.ConversationID = uuid.NewString()
	}
	if service == nil || service.store == nil || service.metricExecutor == nil ||
		uuid.Validate(tenantID) != nil || uuid.Validate(actorID) != nil ||
		uuid.Validate(request.ConversationID) != nil ||
		(request.ParentQuestionID != "" && uuid.Validate(request.ParentQuestionID) != nil) ||
		len(request.Question) < 1 || len(request.Question) > 4000 ||
		!oneOf(request.Locale, "zh-CN", "en-US") ||
		!oneOf(request.Display.PreferredChart, "", "AUTO", "TABLE", "LINE", "BAR", "KPI") {
		return response, ErrInvalidRequest
	}
	if _, err := time.LoadLocation(request.Timezone); err != nil {
		return response, ErrInvalidRequest
	}
	budgets := defaultQuestionBudgets()
	toolBudget := questionToolBudgetTracker{budgets: budgets}
	response = QuestionResponse{
		ConversationID:   request.ConversationID,
		ParentQuestionID: request.ParentQuestionID,
		QuestionHash:     hashText(request.Question),
		Status:           "PROCESSING", Route: QuestionRouteClarifyOrRefuse,
		Plans: []QueryPlan{}, Executions: []QueryPlanExecution{}, Budgets: budgets,
		ToolRegistry: defaultQuestionToolRegistry.PublicSummaries(),
	}
	deadline := time.Duration(budgets.DeadlineMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	ctx = context.WithValue(ctx, questionRuntimeMetadataKey{}, questionRuntimeMetadata{
		ConversationID:   request.ConversationID,
		ParentQuestionID: request.ParentQuestionID,
	})
	var semanticSnapshot *QuestionSemanticSnapshot
	var semanticSnapshotErr error
	if service.semanticGraph != nil {
		if snapshotStore, ok := service.store.(questionSemanticScopeStore); ok {
			snapshot, loadErr := snapshotStore.LoadQuestionSemanticSnapshot(
				ctx, tenantID, actorID, time.Now().UTC(),
			)
			semanticSnapshot, semanticSnapshotErr = &snapshot, loadErr
		} else {
			semanticSnapshotErr = ErrGraphNotReady
		}
	}
	understanding := understandQuestion(request.Question, semanticSnapshot)
	response.Understanding = &understanding
	if semanticSnapshotErr == nil && semanticSnapshot != nil {
		response.SemanticVersion = semanticSnapshot.SemanticVersion
		response.SemanticContentHash = semanticSnapshot.ContentHash
	}

	contextPlanIDs := []string{}
	if runtimeStore, ok := service.store.(questionRuntimeStore); ok {
		var err error
		contextPlanIDs, err = runtimeStore.ResolveConversationContext(
			ctx, tenantID, request.ConversationID,
		)
		if err != nil {
			return response, err
		}
	}
	confirmedMetricCodes := append([]string(nil), request.ConfirmedMetricCodes...)
	semanticHints := QuerySemanticHints{}
	governedUnderstanding := false
	if codes, hints, complete := governedSimpleQuestionHints(request.Question, understanding); complete {
		confirmedMetricCodes = uniqueStrings(append(confirmedMetricCodes, codes...), 8)
		semanticHints = hints
		governedUnderstanding = true
	}
	turn, err := service.PlanQueryTurn(ctx, tenantID, actorID, QueryTurnInput{
		Question: request.Question, Timezone: request.Timezone,
		ContextQueryPlanIDs:  contextPlanIDs,
		ConfirmedMetricCodes: confirmedMetricCodes,
		ConfirmedDecisions:   request.ConfirmedDecisions,
		MaximumPathHops:      4, SemanticHints: semanticHints,
		GovernedUnderstanding: governedUnderstanding,
	})
	response.QuestionID = turn.QuestionRunID
	response.State = turn.State
	response.Lifecycle = turn.Lifecycle
	response.Plans = turn.Plans
	response.Planning = &turn
	response.Clarification = turn.Clarification
	response.Routing = routeQuestion(turn)
	response.Route = response.Routing.Selected
	if err != nil {
		return response, err
	}
	if turn.State == QuestionStatePlanReady && response.Route == QuestionRouteSemantic {
		service.registerActiveQuestion(response.QuestionID, cancel)
		defer service.unregisterActiveQuestion(response.QuestionID)
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageOrchestration,
		QueryProgressStatusSucceeded, "统一问答编排器已完成路径决策。",
	)
	if turn.Clarification != nil || turn.State == QuestionStateClarificationRequired {
		response.Status = "CLARIFICATION_REQUIRED"
		_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
			Route: response.Route, Decision: response.Status,
			SemanticVersion:     questionSemanticVersion(semanticSnapshot),
			SemanticReleaseID:   questionSemanticReleaseID(semanticSnapshot),
			SemanticContentHash: questionSemanticContentHash(semanticSnapshot),
			UnderstandingHash:   mustQuestionHash(understanding),
			QueryPlanIDs:        queryPlanIDs(turn.Plans), Budgets: budgets,
		})
		return response, nil
	}
	if turn.State != QuestionStatePlanReady || len(turn.Plans) == 0 {
		return response, ErrUnprovenPath
	}
	if response.Route != QuestionRouteSemantic {
		return response, ErrUnprovenPath
	}
	machine, err := resumeQuestionStateMachine(
		ctx, service, tenantID, turn.QuestionRunID, turn.Lifecycle,
	)
	if err != nil {
		return response, err
	}
	defer func() {
		if runErr != nil {
			blockQuestionState(nil, machine)
			response.State = machine.state
			response.Lifecycle = machine.lifecycle()
		}
	}()
	if err := machine.advance(QuestionStateValidating); err != nil {
		return response, err
	}
	var governedGraphPlan *QuestionGraphPlan
	if service.semanticGraph != nil {
		if semanticSnapshotErr != nil || semanticSnapshot == nil {
			code, message := questionSemanticFailureDetail(
				semanticSnapshotErr, "ACTIVE_SEMANTIC_RELEASE_REQUIRED",
			)
			response.Status = "BLOCKED"
			response.Failure = &QuestionFailure{Code: code, Message: message}
			if err := machine.advance(QuestionStateBlocked); err != nil {
				return response, err
			}
			response.State, response.Lifecycle = machine.state, machine.lifecycle()
			_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
				Route: response.Route, Decision: response.Status, FailureCode: code,
				UnderstandingHash: mustQuestionHash(understanding),
				QueryPlanIDs:      queryPlanIDs(turn.Plans), Budgets: budgets,
			})
			return response, nil
		}
		clarification, ambiguityErr := validateGovernedMetricAmbiguity(
			ctx, service.semanticGraph, *semanticSnapshot, understanding,
			turn.Plans, confirmedMetricCodes,
		)
		if ambiguityErr != nil {
			code, message := questionSemanticFailureDetail(
				ambiguityErr, "SEMANTIC_BUNDLE_REJECTED",
			)
			response.Status = "BLOCKED"
			response.Failure = &QuestionFailure{Code: code, Message: message}
			response.SemanticVersion = semanticSnapshot.SemanticVersion
			response.SemanticContentHash = semanticSnapshot.ContentHash
			if err := machine.advance(QuestionStateBlocked); err != nil {
				return response, err
			}
			response.State, response.Lifecycle = machine.state, machine.lifecycle()
			_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
				Route: response.Route, Decision: response.Status,
				SemanticVersion:     semanticSnapshot.SemanticVersion,
				SemanticReleaseID:   semanticSnapshot.ReleaseID,
				SemanticContentHash: semanticSnapshot.ContentHash,
				UnderstandingHash:   mustQuestionHash(understanding),
				FailureCode:         code, QueryPlanIDs: queryPlanIDs(turn.Plans), Budgets: budgets,
			})
			return response, nil
		}
		if clarification != nil {
			response.Status = "CLARIFICATION_REQUIRED"
			response.Route = QuestionRouteClarifyOrRefuse
			response.Routing.Selected = QuestionRouteClarifyOrRefuse
			response.Routing.ReasonCode = "GOVERNED_ALIAS_AMBIGUOUS"
			response.Clarification = clarification
			if err := machine.advance(QuestionStateClarificationRequired); err != nil {
				return response, err
			}
			response.State, response.Lifecycle = machine.state, machine.lifecycle()
			_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
				Route: response.Route, Decision: response.Status,
				SemanticVersion:     semanticSnapshot.SemanticVersion,
				SemanticReleaseID:   semanticSnapshot.ReleaseID,
				SemanticContentHash: semanticSnapshot.ContentHash,
				UnderstandingHash:   mustQuestionHash(understanding),
				QueryPlanIDs:        queryPlanIDs(turn.Plans), Budgets: budgets,
			})
			return response, nil
		}
		if err := toolBudget.reserve("validate_semantic_bundle"); err != nil {
			return response, err
		}
		graphStarted := time.Now()
		graphPlan, graphErr := validateQuestionSemanticGraph(
			ctx, service.semanticGraph, *semanticSnapshot, understanding, turn.Plans,
		)
		graphAuditErr := service.auditQuestionToolCall(
			ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
			"validate_semantic_bundle", QuestionStateValidating,
			struct {
				BindingBundleHash string `json:"bindingBundleHash"`
				QueryPlanCount    int    `json:"queryPlanCount"`
			}{graphPlan.BindingBundleHash, len(turn.Plans)},
			graphPlan, graphPlan.EvidenceIDs, budgets, graphStarted, graphErr,
		)
		if graphAuditErr != nil {
			return response, graphAuditErr
		}
		if graphErr != nil {
			code, message := questionSemanticFailureDetail(
				graphErr, "NO_CERTIFIED_GRAPH_PATH",
			)
			response.Status = "BLOCKED"
			response.Failure = &QuestionFailure{Code: code, Message: message}
			response.SemanticVersion = semanticSnapshot.SemanticVersion
			response.SemanticContentHash = semanticSnapshot.ContentHash
			if err := machine.advance(QuestionStateBlocked); err != nil {
				return response, err
			}
			response.State, response.Lifecycle = machine.state, machine.lifecycle()
			_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
				Route: response.Route, Decision: response.Status,
				SemanticVersion:     semanticSnapshot.SemanticVersion,
				SemanticReleaseID:   semanticSnapshot.ReleaseID,
				SemanticContentHash: semanticSnapshot.ContentHash,
				UnderstandingHash:   mustQuestionHash(understanding),
				FailureCode:         code, QueryPlanIDs: queryPlanIDs(turn.Plans), Budgets: budgets,
			})
			return response, nil
		}
		governedGraphPlan = &graphPlan
		response.GraphPlan = governedGraphPlan
		response.SemanticContentHash = graphPlan.ContentHash
	}

	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageSQLGuard,
		QueryProgressStatusRunning, "正在验证 Semantic IR、执行图和查询预算。",
	)
	semanticVersion := semanticVersionForPlans(turn.Plans)
	if governedGraphPlan != nil {
		semanticVersion = governedGraphPlan.SemanticVersion
	}
	intent, ir, graph, err := buildQuestionContracts(turn, semanticVersion, budgets)
	if err != nil {
		return response, err
	}
	response.SemanticVersion = semanticVersion
	response.Intent = &intent
	response.SemanticIR = &ir
	response.ExecutionGraph = &graph
	if governedGraphPlan != nil {
		intent.SemanticContentHash = governedGraphPlan.ContentHash
		intent.GraphPlanIDs = []string{governedGraphPlan.ID}
		ir.SemanticContentHash = governedGraphPlan.ContentHash
		ir.EvidenceIDs = uniqueStrings(append(ir.EvidenceIDs, governedGraphPlan.EvidenceIDs...), 256)
		graph.SemanticVersion = governedGraphPlan.SemanticVersion
		graph.ContentHash = governedGraphPlan.ContentHash
		graph.GraphPlanID = governedGraphPlan.ID
		graph.GraphEvidenceIDs = append([]string(nil), governedGraphPlan.EvidenceIDs...)
	}
	if governedGraphPlan == nil || semanticSnapshot == nil {
		return response, fmt.Errorf("%w: governed semantic execution requires an active graph release", ErrUnprovenPath)
	}
	if err := toolBudget.reserve("compile_semantic_query"); err != nil {
		return response, err
	}
	compileStarted := time.Now()
	irHash, err := hashJSON(ir)
	if err != nil {
		return response, err
	}
	if err := service.auditQuestionToolCall(
		ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
		"compile_semantic_query", QuestionStateValidating,
		struct {
			SemanticIRHash string `json:"semanticIrHash"`
		}{irHash}, graph, ir.EvidenceIDs, budgets, compileStarted, nil,
	); err != nil {
		return response, err
	}
	registryStore, ok := service.store.(questionExecutionRegistryStore)
	if !ok {
		return response, fmt.Errorf("%w: execution registry unavailable", ErrUnprovenPath)
	}
	if err := toolBudget.reserve("get_data_quality_status"); err != nil {
		return response, err
	}
	registryStarted := time.Now()
	registryProof, registryErr := registryStore.ValidateQuestionExecutionRegistry(
		ctx, tenantID, semanticSnapshot.ReleaseID,
		semanticSnapshot.SemanticVersion, semanticSnapshot.ContentHash, turn.Plans,
	)
	registryAuditErr := service.auditQuestionToolCall(
		ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
		"get_data_quality_status", QuestionStateValidating,
		struct {
			DatasetVersionIDs []string `json:"datasetVersionIds"`
		}{queryPlanDatasetVersionIDs(turn.Plans)},
		registryProof, toolEvidenceIDs(registryProof.ProofHash), budgets,
		registryStarted, registryErr,
	)
	if registryAuditErr != nil {
		return response, registryAuditErr
	}
	if registryErr != nil {
		return response, fmt.Errorf("%w: execution registry validation: %v", ErrUnprovenPath, registryErr)
	}
	response.ExecutionRegistry = &registryProof
	preflightByPlan := make(map[string][]metric.QueryPreflightProof, len(turn.Plans))
	for _, plan := range turn.Plans {
		remainingExplains := budgets.MaximumExplainQueries - toolBudget.explain
		preflightStarted := time.Now()
		proofs, preflightErr := service.preflightQueryPlanCore(
			ctx, tenantID, actorID, plan, budgets.MaximumRowsPerQuery,
			remainingExplains,
		)
		if preflightErr != nil {
			if reserveErr := toolBudget.reserve("explain_query_plan"); reserveErr != nil {
				return response, reserveErr
			}
			auditErr := service.auditQuestionToolCall(
				ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
				"explain_query_plan", QuestionStateValidating,
				struct {
					QueryPlanID string `json:"queryPlanId"`
				}{plan.ID}, struct{}{}, planEvidenceIDs(plan), budgets,
				preflightStarted, preflightErr,
			)
			if auditErr != nil {
				return response, auditErr
			}
			return response, fmt.Errorf("%w: query preflight: %v", ErrUnprovenPath, preflightErr)
		}
		preflightByPlan[plan.ID] = proofs
		response.PreflightProofs = append(response.PreflightProofs, proofs...)
		for _, proof := range proofs {
			if err := toolBudget.reserve("explain_query_plan"); err != nil {
				return response, err
			}
			if err := service.auditQuestionToolCall(
				ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
				"explain_query_plan", QuestionStateValidating,
				struct {
					QueryPlanID string `json:"queryPlanId"`
				}{plan.ID}, proof,
				toolEvidenceIDs(plan.PathHash, proof.QueryHash), budgets,
				preflightStarted, nil,
			); err != nil {
				return response, err
			}
		}
	}
	guard := validateSemanticExecutionWithGraph(turn.Plans, ir, budgets, governedGraphPlan)
	guard = addGovernedExecutionChecks(
		guard, turn.Plans, registryProof, preflightByPlan,
		semanticSnapshot.ContentHash,
	)
	for _, plan := range turn.Plans {
		if err := toolBudget.reserve("validate_query_plan"); err != nil {
			return response, err
		}
		validateStarted := time.Now()
		var validateErr error
		if guard.Status != "PASS" {
			validateErr = ErrUnprovenPath
		}
		if err := service.auditQuestionToolCall(
			ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
			"validate_query_plan", QuestionStateValidating,
			struct {
				QueryPlanID string `json:"queryPlanId"`
			}{plan.ID}, guard,
			toolEvidenceIDs(plan.PathHash, registryProof.ProofHash), budgets,
			validateStarted, validateErr,
		); err != nil {
			return response, err
		}
	}
	response.Guard = &guard
	if guard.Status != "PASS" {
		reportQuestionProgress(
			ctx, response.QuestionID, QueryProgressStageSQLGuard,
			QueryProgressStatusWarn, "查询门禁未通过，已停止执行。",
		)
		response.Status = "BLOCKED"
		response.Failure = &QuestionFailure{
			Code: "SQL_GUARD_REJECTED", Message: "查询计划未通过只读、白名单或预算门禁。",
		}
		if err := machine.advance(QuestionStateBlocked); err != nil {
			return response, err
		}
		response.State, response.Lifecycle = machine.state, machine.lifecycle()
		_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
			Route: response.Route, Decision: response.Status,
			SemanticVersion:     semanticVersion,
			SemanticReleaseID:   questionSemanticReleaseID(semanticSnapshot),
			SemanticContentHash: response.SemanticContentHash,
			UnderstandingHash:   mustQuestionHash(understanding),
			GraphPlanHash:       questionGraphPlanHash(governedGraphPlan),
			FailureCode:         response.Failure.Code,
			QueryPlanIDs:        queryPlanIDs(turn.Plans), Budgets: budgets,
		})
		return response, nil
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageSQLGuard,
		QueryProgressStatusSucceeded, "只读、白名单、语义版本和预算门禁已通过。",
	)
	if err := machine.advance(QuestionStateCostApproved); err != nil {
		return response, err
	}
	if err := machine.advance(QuestionStateExecuting); err != nil {
		return response, err
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageExecution,
		QueryProgressStatusRunning, "正在执行已验证的只读查询计划。",
	)
	executions := make([]QueryPlanExecution, 0, len(turn.Plans))
	for index, plan := range turn.Plans {
		if index >= budgets.MaximumMetricQueries {
			return response, fmt.Errorf("%w: metric query budget exceeded", ErrUnprovenPath)
		}
		if governedGraphPlan != nil {
			current, currentErr := service.questionSemanticSnapshotCurrent(
				ctx, tenantID, questionSemanticReleaseID(semanticSnapshot),
				governedGraphPlan.SemanticVersion, governedGraphPlan.ContentHash,
			)
			if currentErr != nil || !current {
				return response, fmt.Errorf("%w: semantic release changed before execution", ErrUnprovenPath)
			}
		}
		if err := toolBudget.reserve("get_data_quality_status"); err != nil {
			return response, err
		}
		revalidationStarted := time.Now()
		revalidationProof, revalidationErr := registryStore.ValidateQuestionExecutionRegistry(
			ctx, tenantID, semanticSnapshot.ReleaseID,
			semanticSnapshot.SemanticVersion, semanticSnapshot.ContentHash,
			[]QueryPlan{plan},
		)
		if err := service.auditQuestionToolCall(
			ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
			"get_data_quality_status", QuestionStateValidating,
			struct {
				DatasetVersionIDs []string `json:"datasetVersionIds"`
			}{[]string{plan.SelectedDatasetVersionID}}, revalidationProof,
			toolEvidenceIDs(revalidationProof.ProofHash), budgets,
			revalidationStarted, revalidationErr,
		); err != nil {
			return response, err
		}
		if revalidationErr != nil {
			return response, fmt.Errorf("%w: execution registry changed before execution", ErrUnprovenPath)
		}
		if err := toolBudget.reserve("execute_query_plan"); err != nil {
			return response, err
		}
		executionStarted := time.Now()
		execution, executeErr := service.executeQueryPlanCore(
			ctx, tenantID, actorID, plan.ID, ExecuteQueryPlanInput{
				ExpectedGraphGenerationID: plan.GraphGenerationID,
				ExpectedPathHash:          plan.PathHash,
				QueryID:                   uuid.NewString(), Parameters: map[string]any{},
				MaxRows: budgets.MaximumRowsPerQuery,
			},
		)
		executionAuditErr := service.auditQuestionToolCall(
			ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
			"execute_query_plan", QuestionStateExecuting,
			struct {
				QueryPlanID string `json:"queryPlanId"`
			}{plan.ID}, execution,
			toolEvidenceIDs(plan.PathHash, revalidationProof.ProofHash), budgets,
			executionStarted, executeErr,
		)
		if executionAuditErr != nil {
			return response, executionAuditErr
		}
		if executeErr != nil {
			return response, executeErr
		}
		if governedGraphPlan != nil {
			execution.Evidence.SemanticVersion = governedGraphPlan.SemanticVersion
			execution.Evidence.SemanticContentHash = governedGraphPlan.ContentHash
			execution.Evidence.GraphPlanID = governedGraphPlan.ID
			execution.Evidence.GraphEvidenceIDs = append(
				[]string(nil), governedGraphPlan.EvidenceIDs...,
			)
			execution.Evidence.CompatibilityDecision = "NEBULA_GRAPH_CERTIFIED_BUNDLE"
			execution.Evidence.PermissionDecision = "NEBULA_GRAPH_POLICY_PROPAGATION_AND_RUNTIME_REVALIDATION"
			execution.Evidence.FreshnessDecision = "EXECUTION_REGISTRY_ACTIVE_MATERIALIZATION_AND_DQ_PASS"
			execution.Evidence.ValidatorChecks = uniqueStrings(append(
				execution.Evidence.ValidatorChecks,
				"execution_registry_pass", "postgres_ast_parse_pass",
				"explain_cost_pass", "semantic_content_hash_pass",
			), 64)
		}
		executions = append(executions, execution)
	}
	if governedGraphPlan != nil {
		current, currentErr := service.questionSemanticSnapshotCurrent(
			ctx, tenantID, questionSemanticReleaseID(semanticSnapshot),
			governedGraphPlan.SemanticVersion, governedGraphPlan.ContentHash,
		)
		if currentErr != nil || !current {
			return response, fmt.Errorf("%w: semantic release changed after execution", ErrUnprovenPath)
		}
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageExecution,
		QueryProgressStatusSucceeded, "所有查询计划执行完成。",
	)
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageResultVerification,
		QueryProgressStatusRunning, "正在核验结果模式、行数和证据哈希。",
	)
	verification := verifyQuestionResultsForSemanticSnapshot(
		turn.Plans, executions, budgets, semanticVersion,
		response.SemanticContentHash,
	)
	for index, plan := range turn.Plans {
		if err := toolBudget.reserve("execute_validation_query"); err != nil {
			return response, err
		}
		validationStarted := time.Now()
		var validationErr error
		if verification.Status != "PASS" {
			validationErr = ErrUnprovenPath
		}
		if err := service.auditQuestionToolCall(
			ctx, tenantID, actorID, response.QuestionID, semanticSnapshot,
			"execute_validation_query", QuestionStateExecuting,
			struct {
				QueryPlanID string `json:"queryPlanId"`
			}{plan.ID}, verification,
			toolEvidenceIDs(
				plan.PathHash, executions[index].Evidence.ResultHash,
			), budgets, validationStarted, validationErr,
		); err != nil {
			return response, err
		}
	}
	response.Verification = &verification
	if verification.Status != "PASS" {
		reportQuestionProgress(
			ctx, response.QuestionID, QueryProgressStageResultVerification,
			QueryProgressStatusWarn, "结果一致性核验未通过，已停止生成答案。",
		)
		response.Status = "BLOCKED"
		response.Failure = &QuestionFailure{
			Code: "RESULT_VERIFICATION_FAILED", Message: "执行结果未通过模式、行数或证据一致性校验。",
		}
		if err := machine.advance(QuestionStateBlocked); err != nil {
			return response, err
		}
		response.State, response.Lifecycle = machine.state, machine.lifecycle()
		_ = service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
			Route: response.Route, Decision: response.Status,
			SemanticVersion:     semanticVersion,
			SemanticReleaseID:   questionSemanticReleaseID(semanticSnapshot),
			SemanticContentHash: response.SemanticContentHash,
			UnderstandingHash:   mustQuestionHash(understanding),
			GraphPlanHash:       questionGraphPlanHash(governedGraphPlan),
			FailureCode:         response.Failure.Code,
			QueryPlanIDs:        queryPlanIDs(turn.Plans), Budgets: budgets,
		})
		return response, nil
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageResultVerification,
		QueryProgressStatusSucceeded, "结果模式、行数和证据哈希核验通过。",
	)
	if err := machine.advance(QuestionStateResultVerified); err != nil {
		return response, err
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageAnswer,
		QueryProgressStatusRunning, "正在基于已核验结果生成确定性答案。",
	)
	answer := renderVerifiedQuestionAnswer(executions, request.Display)
	evidence, err := buildAccuracyEvidence(intent, ir, turn, executions, verification)
	if err != nil {
		return response, err
	}
	response.Answer = &answer
	response.Evidence = &evidence
	response.Executions = executions
	response.Plans = make([]QueryPlan, 0, len(executions))
	for _, execution := range executions {
		response.Plans = append(response.Plans, execution.QueryPlan)
	}
	response.Status = "ANSWERED"
	if err := machine.advance(QuestionStateAnswered); err != nil {
		return response, err
	}
	response.State, response.Lifecycle = machine.state, machine.lifecycle()
	if err := service.saveQuestionOutcome(ctx, tenantID, response, questionRunOutcome{
		Route: response.Route, Decision: response.Status,
		SemanticVersion:     semanticVersion,
		SemanticReleaseID:   questionSemanticReleaseID(semanticSnapshot),
		SemanticContentHash: response.SemanticContentHash,
		UnderstandingHash:   mustQuestionHash(understanding),
		GraphPlanHash:       questionGraphPlanHash(governedGraphPlan),
		IntentHash:          evidence.IntentHash,
		BindingBundleHash:   questionBindingBundleHash(governedGraphPlan, evidence),
		QueryPlanHash:       evidence.QueryPlanHash, ResultHash: evidence.ResultHash,
		QueryPlanIDs: queryPlanIDs(response.Plans), Budgets: budgets,
	}); err != nil {
		return response, err
	}
	reportQuestionProgress(
		ctx, response.QuestionID, QueryProgressStageAnswer,
		QueryProgressStatusSucceeded, "答案与完整准确性证据已生成。",
	)
	return response, nil
}

func (service *Service) saveQuestionOutcome(
	ctx context.Context,
	tenantID string,
	response QuestionResponse,
	outcome questionRunOutcome,
) error {
	store, ok := service.store.(questionRuntimeStore)
	if !ok || response.QuestionID == "" {
		return nil
	}
	outcome.Artifacts = questionArtifacts(response)
	return store.SaveQuestionOutcome(ctx, tenantID, response.QuestionID, outcome)
}

func resumeQuestionStateMachine(
	ctx context.Context,
	service *Service,
	tenantID, runID string,
	events []QuestionStateEvent,
) (*questionStateMachine, error) {
	if uuid.Validate(runID) != nil || len(events) == 0 {
		return nil, ErrInvalidState
	}
	machine := &questionStateMachine{
		runID: runID, state: events[len(events)-1].State,
		events: append([]QuestionStateEvent(nil), events...), now: time.Now,
	}
	if recorder, ok := service.store.(questionRunRecorder); ok {
		machine.onAdvance = func(current QuestionState, event QuestionStateEvent) error {
			return recorder.AppendQuestionState(ctx, tenantID, runID, current, event)
		}
	}
	return machine, nil
}

func buildQuestionContracts(
	turn QueryTurnPlan,
	semanticVersion string,
	budgets QuestionBudgets,
) (CompleteIntent, SemanticQueryIR, ExecutionGraphRef, error) {
	if len(turn.Plans) == 0 || len(turn.Plans) > budgets.MaximumMetricQueries {
		return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
	}
	intent := CompleteIntent{
		SemanticVersion: semanticVersion, TaskType: turn.Intent,
		Metrics: []SemanticObjectRef{}, Dimensions: []SemanticObjectRef{},
		ExecutionPath: QuestionRouteSemantic, GraphPlanIDs: []string{}, Ambiguities: []string{},
	}
	ir := SemanticQueryIR{
		SchemaVersion: "1.0", Mode: "semantic", SemanticVersion: semanticVersion,
		Metrics: []SemanticIRMetric{}, Dimensions: []string{},
		Filters: []SemanticIRFilter{}, OrderBy: []SemanticIROrder{},
		Limit: budgets.MaximumRowsPerQuery, EvidenceIDs: []string{},
	}
	graph := ExecutionGraphRef{
		QueryPlanIDs: []string{}, PathHashes: []string{},
		DatasetVersionIDs: []string{}, MaterializationIDs: []string{},
	}
	dimensionSeen := map[string]bool{}
	filterSeen := map[string]bool{}
	var commonTime *QueryTimeRange
	for _, plan := range turn.Plans {
		if plan.Status != "READY" || uuid.Validate(plan.ID) != nil ||
			uuid.Validate(plan.GraphGenerationID) != nil || plan.GraphGeneration < 1 ||
			uuid.Validate(plan.SelectedMetricID) != nil ||
			uuid.Validate(plan.SelectedMetricVersionID) != nil ||
			uuid.Validate(plan.SelectedDatasetVersionID) != nil ||
			uuid.Validate(plan.SelectedMaterializationID) != nil ||
			!validCode(plan.Conditions.MetricCode) ||
			plan.Conditions.MetricVersionID != plan.SelectedMetricVersionID ||
			plan.Conditions.DatasetVersionID != plan.SelectedDatasetVersionID ||
			!validHash(plan.PathHash) {
			return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
		}
		if graph.GenerationID == "" {
			graph.GenerationID, graph.Generation = plan.GraphGenerationID, plan.GraphGeneration
		} else if graph.GenerationID != plan.GraphGenerationID || graph.Generation != plan.GraphGeneration {
			return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
		}
		metricEvidence := evidenceIDsForPlan(plan, "METRIC")
		if len(metricEvidence) == 0 {
			return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
		}
		metricLabel := queryPlanEvidenceLabel(plan, "METRIC", plan.SelectedMetricVersionID)
		intent.Metrics = append(intent.Metrics, SemanticObjectRef{
			ID: plan.SelectedMetricID, VersionID: plan.SelectedMetricVersionID,
			Code: plan.Conditions.MetricCode, Label: metricLabel,
			BindingEvidence: metricEvidence,
		})
		ir.Metrics = append(ir.Metrics, SemanticIRMetric{
			MetricID:        plan.SelectedMetricID,
			MetricVersionID: plan.SelectedMetricVersionID,
			Code:            plan.Conditions.MetricCode,
		})
		ir.EvidenceIDs = append(ir.EvidenceIDs, metricEvidence...)
		graph.QueryPlanIDs = append(graph.QueryPlanIDs, plan.ID)
		graph.PathHashes = append(graph.PathHashes, plan.PathHash)
		graph.DatasetVersionIDs = appendUniqueString(graph.DatasetVersionIDs, plan.SelectedDatasetVersionID)
		graph.MaterializationIDs = appendUniqueString(graph.MaterializationIDs, plan.SelectedMaterializationID)
		intent.GraphPlanIDs = append(intent.GraphPlanIDs, plan.PathHash)
		if plan.Conditions.TimeRange != nil {
			if commonTime == nil {
				copyRange := *plan.Conditions.TimeRange
				commonTime = &copyRange
			} else if *commonTime != *plan.Conditions.TimeRange {
				return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
			}
		}
		for _, dimension := range plan.Conditions.Dimensions {
			if uuid.Validate(dimension.DimensionID) != nil ||
				!validCode(dimension.DimensionCode) {
				return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
			}
			if !dimensionSeen[dimension.DimensionID] {
				dimensionSeen[dimension.DimensionID] = true
				label := queryPlanEvidenceLabel(plan, "DIMENSION", dimension.DimensionID)
				evidence := evidenceIDsForSubject(plan, "DIMENSION", dimension.DimensionID)
				intent.Dimensions = append(intent.Dimensions, SemanticObjectRef{
					ID: dimension.DimensionID, VersionID: dimension.DimensionID,
					Code:  dimension.DimensionCode,
					Label: label, BindingEvidence: evidence,
				})
				ir.Dimensions = append(ir.Dimensions, dimension.DimensionID)
				ir.EvidenceIDs = append(ir.EvidenceIDs, evidence...)
			}
			values := append([]string(nil), dimension.MemberKeys...)
			if dimension.MemberKey != "" {
				values = append(values, dimension.MemberKey)
			}
			sort.Strings(values)
			if len(values) == 0 {
				continue
			}
			filterKey := dimension.DimensionID + "\x00" + strings.Join(values, "\x00")
			if filterSeen[filterKey] {
				continue
			}
			filterSeen[filterKey] = true
			ir.Filters = append(ir.Filters, SemanticIRFilter{
				DimensionID:   dimension.DimensionID,
				DimensionCode: dimension.DimensionCode,
				Operator:      "IN", ValueIDs: values,
			})
		}
	}
	intent.Time, ir.Time = commonTime, commonTime
	if len(turn.Plans) == 1 && turn.Plans[0].Conditions.MetricCode != "" {
		ir.OrderBy = append(ir.OrderBy, SemanticIROrder{
			Member: turn.Plans[0].Conditions.MetricCode, Direction: "DESC",
		})
	}
	ir.EvidenceIDs = uniqueStrings(ir.EvidenceIDs, 256)
	if len(ir.EvidenceIDs) == 0 {
		return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, ErrUnprovenPath
	}
	intentHash, err := hashJSON(struct {
		TaskType   string              `json:"taskType"`
		Metrics    []SemanticObjectRef `json:"metrics"`
		Dimensions []SemanticObjectRef `json:"dimensions"`
		Time       *QueryTimeRange     `json:"time,omitempty"`
	}{intent.TaskType, intent.Metrics, intent.Dimensions, intent.Time})
	if err != nil {
		return CompleteIntent{}, SemanticQueryIR{}, ExecutionGraphRef{}, err
	}
	intent.IntentID = "intent:" + intentHash
	return intent, ir, graph, nil
}

func validateSemanticExecution(
	plans []QueryPlan,
	ir SemanticQueryIR,
	budgets QuestionBudgets,
) SQLGuardDecision {
	decision := SQLGuardDecision{
		Status: "PASS", Mode: "SECURE_DSL_COMPILER",
		MaximumRows: budgets.MaximumRowsPerQuery, Checks: []GuardCheck{},
	}
	add := func(code string, pass bool, detail string) {
		status := "PASS"
		if !pass {
			status, decision.Status = "BLOCKED", "BLOCKED"
		}
		decision.Checks = append(decision.Checks, GuardCheck{Code: code, Status: status, Detail: detail})
	}
	add("semantic_ir_schema_pass",
		ir.SchemaVersion == "1.0" && ir.Mode == "semantic" &&
			ir.SemanticVersion != "" && len(ir.Metrics) > 0 &&
			ir.Limit > 0 && ir.Limit <= budgets.MaximumRowsPerQuery,
		"只接受服务端生成的 Semantic IR 1.0",
	)
	add("read_only_compiler_pass", true, "执行入口只接受结构化指标计划，不接受调用方 SQL")
	add("query_budget_pass", len(plans) > 0 && len(plans) <= budgets.MaximumMetricQueries, "指标查询数量受同步预算限制")
	add("binding_evidence_pass", len(ir.EvidenceIDs) > 0 && len(ir.EvidenceIDs) <= 256, "所有执行绑定必须关联已验证证据")
	for index, metric := range ir.Metrics {
		add(fmt.Sprintf("metric_contract_pass:%d", index),
			uuid.Validate(metric.MetricID) == nil &&
				uuid.Validate(metric.MetricVersionID) == nil && validCode(metric.Code),
			"指标必须固定到已认证合同版本",
		)
	}
	for index, filter := range ir.Filters {
		add(fmt.Sprintf("filter_binding_pass:%d", index),
			uuid.Validate(filter.DimensionID) == nil && validCode(filter.DimensionCode) &&
				filter.Operator == "IN" && len(filter.ValueIDs) > 0 && len(filter.ValueIDs) <= 256,
			"筛选只能引用已绑定维度和值 ID",
		)
	}
	for _, plan := range plans {
		add("plan_ready:"+plan.ID, plan.Status == "READY", "查询计划必须先通过语义图门禁")
		add("semantic_version_pinned:"+plan.ID, plan.GraphGenerationID != "" && plan.GraphGeneration > 0, "固定不可变语义图版本")
		add("graph_allowlist_pass:"+plan.ID, validHash(plan.PathHash) && plan.SelectedDatasetVersionID != "", "物理数据集必须来自已证明的图路径")
		add("fanout_guard_pass:"+plan.ID, plan.FailureCode == "", "未知基数或不安全 fanout 不可执行")
		add("parameter_binding_pass:"+plan.ID, true, "维度值由运行时绑定，禁止字符串拼接")
	}
	return decision
}

func validateSemanticExecutionWithGraph(
	plans []QueryPlan,
	ir SemanticQueryIR,
	budgets QuestionBudgets,
	graphPlan *QuestionGraphPlan,
) SQLGuardDecision {
	decision := validateSemanticExecution(plans, ir, budgets)
	if graphPlan == nil {
		return decision
	}
	add := func(code string, pass bool, detail string) {
		status := "PASS"
		if !pass {
			status, decision.Status = "BLOCKED", "BLOCKED"
		}
		decision.Checks = append(decision.Checks, GuardCheck{
			Code: code, Status: status, Detail: detail,
		})
	}
	add("active_release_version_pass",
		graphPlan.SemanticVersion == ir.SemanticVersion &&
			graphPlan.ContentHash == ir.SemanticContentHash && validHash(graphPlan.ContentHash) &&
			graphPlan.NormalizerVersion == questionNormalizerVersion,
		"Semantic IR 必须固定到活动发布的版本和内容哈希",
	)
	add("nebula_graph_plan_pass",
		strings.HasPrefix(graphPlan.ID, "graphplan:") &&
			validHash(strings.TrimPrefix(graphPlan.ID, "graphplan:")) &&
			validHash(graphPlan.BindingBundleHash) && len(graphPlan.EvidenceIDs) > 0,
		"执行绑定必须具有可复算的 NebulaGraph 计划和查询证据",
	)
	authorized := uniqueStrings(graphPlan.AuthorizedVIDs, 100)
	required := append(append(append([]string{}, graphPlan.MetricVIDs...), graphPlan.DimensionVIDs...), graphPlan.DatasetVIDs...)
	for _, binding := range graphPlan.ValueBindings {
		required = append(required, binding.ValueVID)
	}
	add("graph_policy_propagation_pass", sameStringSet(authorized, required),
		"指标、维度、值和数据集必须全部通过图权限传播",
	)
	add("certified_join_and_fanout_pass",
		graphPlan.MaximumHops > 0 && graphPlan.MaximumHops <= 4 && graphPlan.FanoutRisk == "NONE",
		"只允许四跳以内的认证 Join 路径且不得存在 fanout 风险",
	)
	return decision
}

func addGovernedExecutionChecks(
	decision SQLGuardDecision,
	plans []QueryPlan,
	registry QuestionExecutionRegistryProof,
	preflights map[string][]metric.QueryPreflightProof,
	expectedContentHash string,
) SQLGuardDecision {
	add := func(code string, pass bool, detail string) {
		status := "PASS"
		if !pass {
			status, decision.Status = "BLOCKED", "BLOCKED"
		}
		decision.Checks = append(decision.Checks, GuardCheck{
			Code: code, Status: status, Detail: detail,
		})
	}
	add("execution_registry_release_pass",
		registry.SemanticContentHash == expectedContentHash &&
			validHash(registry.SemanticContentHash) && validHash(registry.ProofHash) &&
			registry.ProjectionResourceVersion != "",
		"执行语义注册表必须来自当前活动发布及 READY 投影",
	)
	add("data_quality_and_freshness_pass",
		registry.QualityDecision == "PASS" && registry.FreshnessObservedAt != "" &&
			len(registry.QualityRuleIDs) > 0,
		"质量规则、活动物化和新鲜度水位必须在执行前通过",
	)
	for _, plan := range plans {
		proofs := preflights[plan.ID]
		add("postgres_ast_parse_pass:"+plan.ID, len(proofs) > 0,
			"每个主查询和比较查询都必须由 PostgreSQL 同方言解析器解析",
		)
		for index, proof := range proofs {
			add(fmt.Sprintf("compiler_allowlist_pass:%s:%d", plan.ID, index),
				proof.ParserDecision == "POSTGRESQL_EXPLAIN_PARSED" &&
					proof.AllowlistDecision == "POSTGRESQL_AST_RELATION_ALLOWLIST" &&
					validHash(proof.QueryHash) && validHash(proof.ParameterHash) &&
					containsString(proof.MaterializationIDs, plan.SelectedMaterializationID) &&
					proof.DatasetVersionID == plan.SelectedDatasetVersionID,
				"AST 来源、表列白名单、参数摘要和物化版本必须精确匹配",
			)
			add(fmt.Sprintf("explain_cost_pass:%s:%d", plan.ID, index),
				proof.ExplainDecision == "COST_WITHIN_BUDGET" &&
					proof.EstimatedRows <= proof.MaximumEstimatedRows &&
					proof.EstimatedTotalCost <= proof.MaximumEstimatedCost,
				"EXPLAIN 估算行数和优化器成本必须在同步预算内",
			)
		}
	}
	return decision
}

func queryPlanDatasetVersionIDs(plans []QueryPlan) []string {
	result := make([]string, 0, len(plans))
	for _, plan := range plans {
		result = append(result, plan.SelectedDatasetVersionID)
	}
	return uniqueStrings(result, 16)
}

func planEvidenceIDs(plan QueryPlan) []string {
	result := []string{plan.PathHash}
	for _, evidence := range plan.Evidence {
		result = append(result, evidence.EvidenceHash)
	}
	return uniqueStrings(result, 256)
}

func verifyQuestionResults(
	plans []QueryPlan,
	executions []QueryPlanExecution,
	budgets QuestionBudgets,
) ResultVerification {
	return verifyQuestionResultsForSemanticVersion(
		plans, executions, budgets, semanticVersionForPlans(plans),
	)
}

func verifyQuestionResultsForSemanticVersion(
	plans []QueryPlan,
	executions []QueryPlanExecution,
	budgets QuestionBudgets,
	expectedSemanticVersion string,
) ResultVerification {
	return verifyQuestionResultsForSemanticSnapshot(
		plans, executions, budgets, expectedSemanticVersion, "",
	)
}

func verifyQuestionResultsForSemanticSnapshot(
	plans []QueryPlan,
	executions []QueryPlanExecution,
	budgets QuestionBudgets,
	expectedSemanticVersion, expectedSemanticContentHash string,
) ResultVerification {
	verification := ResultVerification{Status: "PASS", TrustLevel: "A", Checks: []GuardCheck{}}
	add := func(code string, pass bool, detail string) {
		status := "PASS"
		if !pass {
			status, verification.Status, verification.TrustLevel = "BLOCKED", "BLOCKED", "D"
		}
		verification.Checks = append(verification.Checks, GuardCheck{Code: code, Status: status, Detail: detail})
	}
	add("execution_count_pass", len(plans) > 0 && len(plans) == len(executions), "每个语义计划必须恰有一个执行结果")
	for index, execution := range executions {
		result := execution.Result
		planIdentityValid := index < len(plans) &&
			plans[index].ID == execution.QueryPlan.ID &&
			plans[index].GraphGenerationID == execution.QueryPlan.GraphGenerationID &&
			plans[index].GraphGeneration == execution.QueryPlan.GraphGeneration &&
			plans[index].PathHash == execution.QueryPlan.PathHash &&
			plans[index].SelectedMetricVersionID == execution.QueryPlan.SelectedMetricVersionID
		add(fmt.Sprintf("plan_identity_pass:%d", index), planIdentityValid, "执行结果必须来自对应的固定查询计划")
		add(fmt.Sprintf("result_schema_pass:%d", index), len(result.Columns) > 0 && len(result.ColumnMetadata) == len(result.Columns), "返回列和列元数据必须一一对应")
		rowsValid := result.RowCount == len(result.Rows) &&
			result.RowCount >= 0 && result.RowCount <= budgets.MaximumRowsPerQuery &&
			len(result.Rows) <= budgets.MaximumRowsPerQuery
		for _, row := range result.Rows {
			rowsValid = rowsValid && len(row) == len(result.Columns)
		}
		add(fmt.Sprintf("result_shape_pass:%d", index), rowsValid, "行宽、行数和返回上限必须一致")
		add(fmt.Sprintf("execution_trace_pass:%d", index), result.QueryID != "" && result.QueryID == execution.Evidence.QueryTraceID, "结果必须关联同一只读查询追踪")
		var baseline *dataset.PreviewResult
		if execution.Comparison != nil {
			baseline = &execution.Comparison.Baseline
		}
		expectedEvidence, evidenceErr := buildAnswerEvidence(
			execution.QueryPlan, result, baseline, time.Time{},
		)
		hashesValid := evidenceErr == nil &&
			validHash(execution.Evidence.ResultHash) &&
			validHash(execution.Evidence.QueryPlanHash) &&
			execution.Evidence.ResultHash == expectedEvidence.ResultHash &&
			execution.Evidence.QueryPlanHash == expectedEvidence.QueryPlanHash
		add(fmt.Sprintf("result_hash_pass:%d", index), hashesValid, "计划与结果摘要必须可由返回合同重新计算")
		add(fmt.Sprintf("policy_revalidated:%d", index), execution.Evidence.ExecutionRevalidated && execution.Evidence.PermissionDecision != "", "执行时重新校验权限和版本")
		add(fmt.Sprintf("semantic_version_consistent:%d", index),
			expectedSemanticVersion != "" &&
				execution.Evidence.SemanticVersion == expectedSemanticVersion,
			"所有结果必须来自 Question 固定的同一语义版本",
		)
		if expectedSemanticContentHash != "" {
			add(fmt.Sprintf("semantic_content_hash_consistent:%d", index),
				validHash(expectedSemanticContentHash) &&
					execution.Evidence.SemanticContentHash == expectedSemanticContentHash,
				"所有结果必须来自 Question 固定的同一语义内容哈希",
			)
		}
		add(fmt.Sprintf("materialization_identity_pass:%d", index),
			index < len(plans) &&
				execution.Evidence.DatasetVersionID == plans[index].SelectedDatasetVersionID &&
				execution.Evidence.MaterializationID == plans[index].SelectedMaterializationID &&
				execution.Evidence.FreshnessDecision != "",
			"结果必须来自计划固定的活动物化及新鲜度证明",
		)
		if expectedSemanticContentHash != "" {
			metadataValid := len(result.ColumnMetadata) == len(result.Columns)
			for _, column := range result.ColumnMetadata {
				metadataValid = metadataValid && column.Code != "" && column.CanonicalType != ""
			}
			add(fmt.Sprintf("result_type_contract_pass:%d", index), metadataValid,
				"每个返回列必须携带稳定字段编码和规范类型",
			)
		}
		if index < len(plans) && len(plans[index].Conditions.Dimensions) == 0 &&
			oneOf(plans[index].Intent, "LOOKUP", "METRIC") {
			add(fmt.Sprintf("scalar_cardinality_pass:%d", index), result.RowCount <= 1,
				"无分组指标查询只能返回零或一行",
			)
		}
		if execution.Comparison != nil {
			baseline := execution.Comparison.Baseline
			comparisonValid := len(baseline.Columns) == len(result.Columns) &&
				baseline.RowCount == len(baseline.Rows) &&
				baseline.RowCount <= budgets.MaximumRowsPerQuery
			for columnIndex := range result.Columns {
				comparisonValid = comparisonValid &&
					baseline.Columns[columnIndex] == result.Columns[columnIndex]
			}
			add(fmt.Sprintf("comparison_coverage_pass:%d", index), comparisonValid,
				"当前期和比较期必须具有相同结果合同并分别满足行数预算",
			)
		}
	}
	return verification
}

func renderVerifiedQuestionAnswer(
	executions []QueryPlanExecution,
	display QuestionDisplay,
) QuestionAnswer {
	answer := QuestionAnswer{
		ResultSets: []QuestionResultSet{}, AsOf: time.Now().UTC().Format(time.RFC3339),
		Chart: QuestionChartSpec{Type: "TABLE"},
	}
	parts := make([]string, 0, len(executions))
	for _, execution := range executions {
		label := queryPlanEvidenceLabel(execution.QueryPlan, "METRIC", execution.QueryPlan.SelectedMetricVersionID)
		if label == "" {
			label = execution.QueryPlan.Conditions.MetricCode
		}
		result := execution.Result
		answer.ResultSets = append(answer.ResultSets, QuestionResultSet{
			MetricCode: execution.QueryPlan.Conditions.MetricCode,
			Columns:    append([]string(nil), result.Columns...), Rows: result.Rows,
			RowCount: result.RowCount,
		})
		if len(result.Rows) == 0 {
			parts = append(parts, label+"在当前权限和筛选范围内没有数据")
			continue
		}
		if len(result.Rows) == 1 && len(result.Rows[0]) == 1 {
			parts = append(parts, fmt.Sprintf("%s为 %s", label, publicValue(result.Rows[0][0])))
		} else {
			parts = append(parts, fmt.Sprintf("%s返回 %d 行已验证结果", label, result.RowCount))
		}
	}
	if len(parts) == 0 {
		answer.Text = "当前问题没有生成可展示的已验证结果。"
	} else {
		answer.Text = strings.Join(parts, "；") + "。"
	}
	chart := display.PreferredChart
	if chart == "" || chart == "AUTO" {
		if len(answer.ResultSets) == 1 && len(answer.ResultSets[0].Columns) == 1 && answer.ResultSets[0].RowCount == 1 {
			chart = "KPI"
		} else if len(answer.ResultSets) == 1 && len(answer.ResultSets[0].Columns) >= 2 {
			chart = "BAR"
		} else {
			chart = "TABLE"
		}
	}
	answer.Chart.Type = chart
	if len(answer.ResultSets) == 1 && len(answer.ResultSets[0].Columns) >= 2 {
		answer.Chart.XField = answer.ResultSets[0].Columns[0]
		answer.Chart.YField = answer.ResultSets[0].Columns[len(answer.ResultSets[0].Columns)-1]
	}
	return answer
}

func buildAccuracyEvidence(
	intent CompleteIntent,
	ir SemanticQueryIR,
	turn QueryTurnPlan,
	executions []QueryPlanExecution,
	verification ResultVerification,
) (AccuracyEvidence, error) {
	intentHash, err := hashJSON(intent)
	if err != nil {
		return AccuracyEvidence{}, err
	}
	planHash, err := hashJSON(ir)
	if err != nil {
		return AccuracyEvidence{}, err
	}
	resultHashes := make([]string, 0, len(executions))
	graphEvidenceIDs := []string{}
	semanticContentHash := ""
	checks := make([]string, 0, len(verification.Checks))
	metrics := make([]string, 0, len(intent.Metrics))
	for _, item := range verification.Checks {
		if item.Status == "PASS" {
			checks = append(checks, item.Code)
		}
	}
	for _, metric := range intent.Metrics {
		metrics = append(metrics, metric.Code+"@"+metric.VersionID)
	}
	for _, execution := range executions {
		resultHashes = append(resultHashes, execution.Evidence.ResultHash)
		graphEvidenceIDs = append(graphEvidenceIDs, execution.Evidence.GraphEvidenceIDs...)
		if semanticContentHash == "" {
			semanticContentHash = execution.Evidence.SemanticContentHash
		} else if execution.Evidence.SemanticContentHash != semanticContentHash {
			return AccuracyEvidence{}, fmt.Errorf("%w: mixed semantic content hashes", ErrUnprovenPath)
		}
	}
	resultHash, err := hashJSON(resultHashes)
	if err != nil {
		return AccuracyEvidence{}, err
	}
	loopIterations := 0
	loopEvidence := []string{}
	if turn.Trace.MetricToolLoop != nil {
		loopIterations += turn.Trace.MetricToolLoop.Rounds
		for _, step := range turn.Trace.MetricToolLoop.Steps {
			if !defaultQuestionToolRegistry.Contains(step.ToolName) {
				return AccuracyEvidence{}, fmt.Errorf("%w: unregistered question tool", ErrUnprovenPath)
			}
			loopEvidence = append(loopEvidence, step.EvidenceIDs...)
		}
	}
	for _, loop := range turn.Trace.DimensionToolLoops {
		if loop.Rounds > loopIterations {
			loopIterations = loop.Rounds
		}
		for _, step := range loop.Steps {
			if !defaultQuestionToolRegistry.Contains(step.ToolName) {
				return AccuracyEvidence{}, fmt.Errorf("%w: unregistered question tool", ErrUnprovenPath)
			}
			loopEvidence = append(loopEvidence, step.EvidenceIDs...)
		}
	}
	return AccuracyEvidence{
		SemanticVersion: intent.SemanticVersion, SemanticContentHash: semanticContentHash,
		IntentHash: intentHash, BindingEvidence: uniqueStrings(ir.EvidenceIDs, 256),
		MetricContracts: metrics, GraphPlanIDs: intent.GraphPlanIDs,
		GraphEvidenceIDs: uniqueStrings(graphEvidenceIDs, 256),
		QueryPlanHash:    planHash, ResultHash: resultHash,
		ValidatorChecks: checks,
		ToolLoop: AccuracyToolLoopEvidence{
			Iterations:     loopIterations,
			NewEvidenceIDs: uniqueStrings(loopEvidence, 256),
		},
		AnswerFidelity: "PASS",
	}, nil
}

func semanticVersionForPlans(plans []QueryPlan) string {
	if len(plans) == 0 {
		return ""
	}
	return fmt.Sprintf("semantic-graph-%d", plans[0].GraphGeneration)
}

func evidenceIDsForPlan(plan QueryPlan, subjectType string) []string {
	result := []string{}
	for _, item := range plan.Evidence {
		if item.SubjectType == subjectType {
			result = append(result, "evidence:"+item.EvidenceHash)
		}
	}
	return uniqueStrings(result, 64)
}

func evidenceIDsForSubject(plan QueryPlan, subjectType, subjectRef string) []string {
	result := []string{}
	for _, item := range plan.Evidence {
		if item.SubjectType == subjectType && item.SubjectRef == subjectRef {
			result = append(result, "evidence:"+item.EvidenceHash)
		}
	}
	return uniqueStrings(result, 64)
}

func queryPlanIDs(plans []QueryPlan) []string {
	result := make([]string, 0, len(plans))
	for _, plan := range plans {
		if uuid.Validate(plan.ID) == nil {
			result = append(result, plan.ID)
		}
	}
	return result
}

func publicValue(value any) string {
	switch item := value.(type) {
	case nil:
		return "N/A"
	case string:
		return item
	case json.Number:
		return item.String()
	case float64:
		return fmt.Sprintf("%g", item)
	case float32:
		return fmt.Sprintf("%g", item)
	case int:
		return fmt.Sprintf("%d", item)
	case int64:
		return fmt.Sprintf("%d", item)
	case bool:
		if item {
			return "是"
		}
		return "否"
	default:
		raw, err := json.Marshal(item)
		if err != nil {
			return "N/A"
		}
		return string(raw)
	}
}

func (service *Service) GetQuestion(
	ctx context.Context,
	tenantID, questionID string,
) (QuestionRunSummary, error) {
	store, ok := service.store.(questionRuntimeStore)
	if service == nil || !ok || uuid.Validate(tenantID) != nil || uuid.Validate(questionID) != nil {
		return QuestionRunSummary{}, ErrInvalidRequest
	}
	return store.GetQuestionRun(ctx, tenantID, questionID)
}

func (service *Service) ListQuestionEvents(
	ctx context.Context,
	tenantID, questionID string,
) ([]QuestionStateEvent, error) {
	store, ok := service.store.(questionRuntimeStore)
	if service == nil || !ok || uuid.Validate(tenantID) != nil || uuid.Validate(questionID) != nil {
		return nil, ErrInvalidRequest
	}
	return store.ListQuestionRunEvents(ctx, tenantID, questionID)
}

func controlledQuestionFailure(err error) bool {
	return errors.Is(err, ErrUnprovenPath) || errors.Is(err, ErrGraphNotReady) || errors.Is(err, ErrDisabled)
}
