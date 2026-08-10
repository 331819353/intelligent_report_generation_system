package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	semanticregistry "intelligent-report-generation-system/internal/askdata/registry"
)

const (
	RegistryVersion  = "semantic-tool-registry-v1"
	MaxSchemaBytes   = 64 << 10
	MaxToolTimeoutMS = 20_000
)

var (
	ErrInvalidRegistry       = errors.New("typed tool registry is invalid")
	ErrUnknownRegisteredTool = errors.New("typed tool is not registered")
	ErrInvalidInvocation     = errors.New("typed tool invocation is invalid")
)

type Permission string

const (
	PermissionSemanticRead           Permission = "SEMANTIC_READ"
	PermissionDimensionValueRead     Permission = "DIMENSION_VALUE_READ"
	PermissionGraphResolve           Permission = "GRAPH_RESOLVE"
	PermissionQualityRead            Permission = "QUALITY_READ"
	PermissionQueryCompile           Permission = "QUERY_COMPILE"
	PermissionQueryValidate          Permission = "QUERY_VALIDATE"
	PermissionCardinalityProbe       Permission = "CARDINALITY_PROBE"
	PermissionQueryExecute           Permission = "QUERY_EXECUTE"
	PermissionValidationQueryExecute Permission = "VALIDATION_QUERY_EXECUTE"
	PermissionClarificationRequest   Permission = "CLARIFICATION_REQUEST"
)

var validPermissions = map[Permission]struct{}{
	PermissionSemanticRead: {}, PermissionDimensionValueRead: {}, PermissionGraphResolve: {},
	PermissionQualityRead: {}, PermissionQueryCompile: {}, PermissionQueryValidate: {},
	PermissionCardinalityProbe: {}, PermissionQueryExecute: {}, PermissionValidationQueryExecute: {},
	PermissionClarificationRequest: {},
}

type BudgetCharge struct {
	ToolCalls         int `json:"toolCalls"`
	FormalQueries     int `json:"formalQueries"`
	ValidationQueries int `json:"validationQueries"`
}

func (charge BudgetCharge) Validate() error {
	if charge.ToolCalls != 1 || charge.FormalQueries < 0 || charge.FormalQueries > 2 ||
		charge.ValidationQueries < 0 || charge.ValidationQueries > 1 ||
		charge.FormalQueries > 0 && charge.ValidationQueries > 0 {
		return ErrInvalidRegistry
	}
	return nil
}

type BudgetAllowance struct {
	ToolCallsRemaining         int
	FormalQueriesRemaining     int
	ValidationQueriesRemaining int
}

func (allowance BudgetAllowance) Validate() error {
	if allowance.ToolCallsRemaining < 0 || allowance.ToolCallsRemaining > 8 ||
		allowance.FormalQueriesRemaining < 0 || allowance.FormalQueriesRemaining > 2 ||
		allowance.ValidationQueriesRemaining < 0 || allowance.ValidationQueriesRemaining > 3 {
		return ErrInvalidInvocation
	}
	return nil
}

func (allowance BudgetAllowance) allows(charge BudgetCharge) bool {
	return allowance.ToolCallsRemaining >= charge.ToolCalls &&
		allowance.FormalQueriesRemaining >= charge.FormalQueries &&
		allowance.ValidationQueriesRemaining >= charge.ValidationQueries
}

// AllowsTool exposes the immutable catalog cost categories to security
// boundaries that must independently reject a forged or stale allowlist.
func (allowance BudgetAllowance) AllowsTool(tool ToolName) bool {
	if allowance.Validate() != nil {
		return false
	}
	charge, known := RequiredBudgetCharge(tool)
	return known && allowance.allows(charge)
}

func RequiredBudgetCharge(tool ToolName) (BudgetCharge, bool) {
	charge := BudgetCharge{ToolCalls: 1}
	switch tool {
	case ToolExecuteQueryPlan:
		charge.FormalQueries = 1
	case ToolCompareCandidateResults:
		charge.FormalQueries = 2
	case ToolProbeJoinCardinality, ToolExecuteValidationQuery:
		charge.ValidationQueries = 1
	default:
		if !IsKnownTool(tool) {
			return BudgetCharge{}, false
		}
	}
	return charge, true
}

type AuthorizationContext struct {
	Scope       askdata.PolicyScope
	DomainID    askdata.ID
	Permissions []Permission
}

func (authorization AuthorizationContext) Validate() error {
	if authorization.Scope.Validate() != nil || authorization.DomainID.Validate() != nil ||
		!containsStableID(authorization.Scope.DomainIDs, authorization.DomainID) ||
		len(authorization.Permissions) < 1 || len(authorization.Permissions) > len(validPermissions) {
		return ErrInvalidInvocation
	}
	seen := map[Permission]bool{}
	for _, permission := range authorization.Permissions {
		if _, valid := validPermissions[permission]; !valid || seen[permission] {
			return ErrInvalidInvocation
		}
		seen[permission] = true
	}
	return nil
}

func (authorization AuthorizationContext) permits(permission Permission) bool {
	for _, candidate := range authorization.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type Definition struct {
	Version            string              `json:"version"`
	Name               ToolName            `json:"name"`
	Permission         Permission          `json:"permission"`
	Charge             BudgetCharge        `json:"charge"`
	TimeoutMS          int                 `json:"timeoutMs"`
	MaxResultBytes     int                 `json:"maxResultBytes"`
	ArgumentSchema     json.RawMessage     `json:"argumentSchema"`
	ArgumentSchemaHash askdata.ContentHash `json:"argumentSchemaHash"`
	ResultSchema       json.RawMessage     `json:"resultSchema"`
	ResultSchemaHash   askdata.ContentHash `json:"resultSchemaHash"`
	DefinitionHash     askdata.ContentHash `json:"definitionHash"`
}

func (definition Definition) Validate() error {
	if definition.Version != RegistryVersion || !IsKnownTool(definition.Name) ||
		definition.Charge.Validate() != nil || definition.TimeoutMS < 10 || definition.TimeoutMS > MaxToolTimeoutMS ||
		definition.MaxResultBytes < 256 || definition.MaxResultBytes > MaxToolResultBytes ||
		len(definition.ArgumentSchema) == 0 || len(definition.ArgumentSchema) > MaxSchemaBytes ||
		len(definition.ResultSchema) == 0 || len(definition.ResultSchema) > MaxSchemaBytes ||
		definition.ArgumentSchemaHash.Validate() != nil || definition.ResultSchemaHash.Validate() != nil ||
		definition.DefinitionHash.Validate() != nil {
		return ErrInvalidRegistry
	}
	if _, valid := validPermissions[definition.Permission]; !valid {
		return ErrInvalidRegistry
	}
	argumentSchema, err := semanticregistry.CanonicalJSON(definition.ArgumentSchema)
	if err != nil || askdata.HashBytes(argumentSchema) != definition.ArgumentSchemaHash {
		return ErrInvalidRegistry
	}
	resultSchema, err := semanticregistry.CanonicalJSON(definition.ResultSchema)
	if err != nil || askdata.HashBytes(resultSchema) != definition.ResultSchemaHash {
		return ErrInvalidRegistry
	}
	expected, err := definitionContentHash(definition)
	if err != nil || expected != definition.DefinitionHash {
		return ErrInvalidRegistry
	}
	return nil
}

type Invocation struct {
	Authorization AuthorizationContext
	Budget        BudgetAllowance
	Call          CallRequest
}

type Execution struct {
	DefinitionHash askdata.ContentHash `json:"definitionHash"`
	Charge         BudgetCharge        `json:"charge"`
	QueryScanBytes int64               `json:"queryScanBytes,omitempty"`
	DurationMS     int64               `json:"durationMs"`
	TimedOut       bool                `json:"timedOut"`
	Response       Response            `json:"response"`
}

func (execution Execution) Validate() error {
	zeroCharge := execution.Charge == (BudgetCharge{})
	if execution.DefinitionHash.Validate() != nil || execution.QueryScanBytes < 0 || execution.QueryScanBytes > 1<<50 ||
		execution.QueryScanBytes > 0 && execution.Charge.FormalQueries == 0 && execution.Charge.ValidationQueries == 0 ||
		execution.DurationMS < 0 || execution.DurationMS > 600_000 ||
		execution.Response.Validate() != nil || (!zeroCharge && execution.Charge.Validate() != nil) ||
		execution.Response.Status == ResponseSuccess && zeroCharge ||
		execution.Response.Status == ResponseFailed && zeroCharge ||
		execution.TimedOut != (execution.Response.Error != nil && execution.Response.Error.Code == "TOOL_TIMEOUT") {
		return ErrInvalidInvocation
	}
	return nil
}

type toolExecutionOutput struct {
	result         resultContract
	evidenceRefs   []askdata.EvidenceRef
	madeProgress   bool
	queryScanBytes int64
}

type resultContract interface {
	ValidateResult(map[askdata.ID]askdata.EvidenceRef) error
}

type registeredTool struct {
	definition Definition
	execute    func(context.Context, AuthorizationContext, ToolArguments) (toolExecutionOutput, error)
}

type Registry struct {
	tools map[ToolName]registeredTool
}

func NewRegistry(handlers Handlers) (*Registry, error) {
	registrations, err := catalogRegistrations(handlers)
	if err != nil {
		return nil, err
	}
	registry := &Registry{tools: make(map[ToolName]registeredTool, len(registrations))}
	for _, registration := range registrations {
		if registration.execute == nil || registration.definition.Validate() != nil {
			return nil, ErrInvalidRegistry
		}
		if _, duplicate := registry.tools[registration.definition.Name]; duplicate {
			return nil, ErrInvalidRegistry
		}
		registry.tools[registration.definition.Name] = registeredTool{
			definition: registration.definition,
			execute:    registration.execute,
		}
	}
	if len(registry.tools) != len(validTools) {
		return nil, ErrInvalidRegistry
	}
	for tool := range validTools {
		if _, exists := registry.tools[tool]; !exists {
			return nil, ErrInvalidRegistry
		}
	}
	return registry, nil
}

func (registry *Registry) Definitions() ([]Definition, error) {
	if registry == nil || len(registry.tools) != len(validTools) {
		return nil, ErrInvalidRegistry
	}
	result := make([]Definition, 0, len(registry.tools))
	for _, tool := range registry.tools {
		if tool.definition.Validate() != nil {
			return nil, ErrInvalidRegistry
		}
		result = append(result, cloneDefinition(tool.definition))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (registry *Registry) Definition(name ToolName) (Definition, bool) {
	if registry == nil {
		return Definition{}, false
	}
	tool, exists := registry.tools[name]
	if !exists || tool.definition.Validate() != nil {
		return Definition{}, false
	}
	return cloneDefinition(tool.definition), true
}

func (registry *Registry) AvailableTools(
	authorization AuthorizationContext,
	budget BudgetAllowance,
) ([]ToolName, error) {
	if registry == nil || len(registry.tools) != len(validTools) ||
		authorization.Validate() != nil || budget.Validate() != nil {
		return nil, ErrInvalidInvocation
	}
	result := make([]ToolName, 0, len(registry.tools))
	for name, tool := range registry.tools {
		if tool.definition.Validate() != nil {
			return nil, ErrInvalidRegistry
		}
		if authorization.permits(tool.definition.Permission) && budget.allows(tool.definition.Charge) {
			result = append(result, name)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (registry *Registry) Execute(ctx context.Context, invocation Invocation) (Execution, error) {
	if registry == nil || ctx == nil || len(registry.tools) != len(validTools) {
		return Execution{}, ErrInvalidRegistry
	}
	tool, exists := registry.tools[invocation.Call.Tool]
	if !exists {
		return Execution{}, ErrUnknownRegisteredTool
	}
	definition := tool.definition
	if definition.Validate() != nil || invocation.Authorization.Validate() != nil || invocation.Budget.Validate() != nil {
		return Execution{}, ErrInvalidInvocation
	}
	if invocation.Call.SchemaVersion != SchemaVersion || invocation.Call.CallID.Validate() != nil {
		return Execution{}, ErrInvalidInvocation
	}
	started := time.Now()
	reject := func(code, message string) (Execution, error) {
		response := failedToolResponse(invocation.Call, ResponseRejected, code, message, false)
		if err := response.Validate(); err != nil {
			return Execution{}, ErrInvalidRegistry
		}
		return Execution{
			DefinitionHash: definition.DefinitionHash, Charge: BudgetCharge{},
			DurationMS: elapsedMilliseconds(started), Response: response,
		}, nil
	}
	if err := ValidateCall(invocation.Call, DefaultArgumentValidator{}); err != nil {
		return reject("TOOL_ARGUMENTS_REJECTED", "工具参数未通过受控 schema 校验。")
	}
	if invocation.Call.Arguments.Release != invocation.Authorization.Scope.Release {
		return reject("TOOL_RELEASE_MISMATCH", "工具调用未绑定当前发布版本。")
	}
	for _, domainID := range invocation.Call.Arguments.DomainIDs {
		if domainID != invocation.Authorization.DomainID {
			return reject("TOOL_DOMAIN_FORBIDDEN", "工具调用超出当前授权业务域。")
		}
	}
	if !invocation.Authorization.permits(definition.Permission) {
		return reject("TOOL_PERMISSION_DENIED", "当前权限不允许执行该工具。")
	}
	if !invocation.Budget.allows(definition.Charge) {
		return reject("TOOL_BUDGET_EXHAUSTED", "工具调用预算不足。")
	}

	callContext, cancel := context.WithTimeout(ctx, time.Duration(definition.TimeoutMS)*time.Millisecond)
	defer cancel()
	output, executeErr := tool.execute(callContext, invocation.Authorization, invocation.Call.Arguments)
	duration := elapsedMilliseconds(started)
	if executeErr != nil {
		status, code, message, retryable, timedOut := normalizeToolExecutionError(callContext, executeErr)
		response := failedToolResponse(invocation.Call, status, code, message, retryable)
		if err := response.Validate(); err != nil {
			return Execution{}, ErrInvalidRegistry
		}
		return Execution{
			DefinitionHash: definition.DefinitionHash, Charge: definition.Charge,
			DurationMS: duration, TimedOut: timedOut, Response: response,
		}, nil
	}
	if output.queryScanBytes < 0 || output.queryScanBytes > 1<<50 ||
		output.queryScanBytes > 0 && definition.Charge.FormalQueries == 0 && definition.Charge.ValidationQueries == 0 {
		response := failedToolResponse(
			invocation.Call, ResponseFailed, "TOOL_RESULT_REJECTED", "工具结果未通过脱敏合同校验。", false,
		)
		if response.Validate() != nil {
			return Execution{}, ErrInvalidRegistry
		}
		return Execution{
			DefinitionHash: definition.DefinitionHash, Charge: definition.Charge,
			DurationMS: duration, Response: response,
		}, nil
	}
	result, evidence, err := sanitizeToolResult(output, definition.MaxResultBytes)
	if err != nil {
		response := failedToolResponse(
			invocation.Call, ResponseFailed, "TOOL_RESULT_REJECTED", "工具结果未通过脱敏合同校验。", false,
		)
		if response.Validate() != nil {
			return Execution{}, ErrInvalidRegistry
		}
		return Execution{
			DefinitionHash: definition.DefinitionHash, Charge: definition.Charge,
			DurationMS: duration, Response: response,
		}, nil
	}
	response := Response{
		SchemaVersion: SchemaVersion, CallID: invocation.Call.CallID, Tool: invocation.Call.Tool,
		Status: ResponseSuccess, Result: result, EvidenceRefs: evidence,
		ResultHash: askdata.HashBytes(result), MadeProgress: output.madeProgress,
	}
	if err := response.Validate(); err != nil {
		return Execution{}, ErrInvalidRegistry
	}
	return Execution{
		DefinitionHash: definition.DefinitionHash, Charge: definition.Charge,
		QueryScanBytes: output.queryScanBytes, DurationMS: duration, Response: response,
	}, nil
}

type HandlerError struct {
	Code          string
	PublicMessage string
	Retryable     bool
	Rejected      bool
}

func (toolError *HandlerError) Error() string {
	if toolError == nil {
		return ""
	}
	return toolError.Code
}

func sanitizeToolResult(output toolExecutionOutput, maxBytes int) (json.RawMessage, []askdata.EvidenceRef, error) {
	if output.result == nil || len(output.evidenceRefs) < 1 || len(output.evidenceRefs) > 64 {
		return nil, nil, ErrInvalidInvocation
	}
	evidence := normalizeToolEvidence(output.evidenceRefs)
	if len(evidence) != len(output.evidenceRefs) {
		return nil, nil, ErrInvalidInvocation
	}
	known := make(map[askdata.ID]askdata.EvidenceRef, len(evidence))
	for _, reference := range evidence {
		if reference.Validate() != nil {
			return nil, nil, ErrInvalidInvocation
		}
		known[reference.EvidenceID] = reference
	}
	if err := output.result.ValidateResult(known); err != nil {
		return nil, nil, err
	}
	payload, err := semanticregistry.CanonicalValue(output.result)
	if err != nil || len(payload) > maxBytes || len(payload) > MaxToolResultBytes {
		return nil, nil, ErrInvalidInvocation
	}
	if err := rejectUnsafeResultKeys(payload); err != nil {
		return nil, nil, err
	}
	return payload, evidence, nil
}

func rejectUnsafeResultKeys(payload []byte) error {
	var value any
	if err := askdata.DecodeStrictJSON(payload, &value); err != nil {
		return err
	}
	forbidden := map[string]bool{
		"sql": true, "rawsql": true, "ngql": true, "querytext": true,
		"args": true, "parameters": true, "rows": true, "rawrows": true,
		"password": true, "passwd": true, "apikey": true, "credential": true,
		"secret": true, "accesstoken": true, "refreshtoken": true,
		"prompt": true, "messages": true, "reasoning": true, "chainofthought": true,
	}
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
				if forbidden[normalized] {
					return ErrInvalidInvocation
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func normalizeToolExecutionError(
	ctx context.Context,
	err error,
) (ResponseStatus, string, string, bool, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ResponseFailed, "TOOL_TIMEOUT", "工具执行超时。", true, true
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return ResponseFailed, "TOOL_CANCELED", "工具执行已取消。", true, false
	}
	var governed *HandlerError
	if errors.As(err, &governed) && governed != nil && isUpperCode(governed.Code) &&
		boundedText(governed.PublicMessage, 512) {
		status := ResponseFailed
		if governed.Rejected {
			status = ResponseRejected
		}
		return status, governed.Code, governed.PublicMessage, governed.Retryable, false
	}
	return ResponseFailed, "TOOL_EXECUTION_FAILED", "工具执行失败。", true, false
}

func failedToolResponse(
	call CallRequest,
	status ResponseStatus,
	code, message string,
	retryable bool,
) Response {
	return Response{
		SchemaVersion: SchemaVersion, CallID: call.CallID, Tool: call.Tool, Status: status,
		Result: nil, EvidenceRefs: []askdata.EvidenceRef{}, MadeProgress: false,
		Error: &ToolError{Code: code, Message: message, Retryable: retryable},
	}
}

func elapsedMilliseconds(started time.Time) int64 {
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func definitionContentHash(definition Definition) (askdata.ContentHash, error) {
	copy := definition
	copy.DefinitionHash = ""
	payload, err := semanticregistry.CanonicalValue(copy)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func cloneDefinition(definition Definition) Definition {
	copy := definition
	copy.ArgumentSchema = append(json.RawMessage(nil), definition.ArgumentSchema...)
	copy.ResultSchema = append(json.RawMessage(nil), definition.ResultSchema...)
	return copy
}

func normalizeToolEvidence(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceID < result[j].EvidenceID })
	compact := result[:0]
	for _, value := range result {
		if len(compact) == 0 || compact[len(compact)-1].EvidenceID != value.EvidenceID {
			compact = append(compact, value)
		}
	}
	return compact
}

func containsStableID(values []askdata.ID, target askdata.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func newDefinition(
	name ToolName,
	permission Permission,
	charge BudgetCharge,
	timeoutMS, maxResultBytes int,
	argumentSchema, resultSchema json.RawMessage,
) (Definition, error) {
	canonicalArguments, err := semanticregistry.CanonicalJSON(argumentSchema)
	if err != nil {
		return Definition{}, err
	}
	canonicalResult, err := semanticregistry.CanonicalJSON(resultSchema)
	if err != nil {
		return Definition{}, err
	}
	definition := Definition{
		Version: RegistryVersion, Name: name, Permission: permission, Charge: charge,
		TimeoutMS: timeoutMS, MaxResultBytes: maxResultBytes,
		ArgumentSchema: canonicalArguments, ArgumentSchemaHash: askdata.HashBytes(canonicalArguments),
		ResultSchema: canonicalResult, ResultSchemaHash: askdata.HashBytes(canonicalResult),
	}
	definition.DefinitionHash, err = definitionContentHash(definition)
	if err != nil || definition.Validate() != nil {
		return Definition{}, fmt.Errorf("%w: %s", ErrInvalidRegistry, name)
	}
	return definition, nil
}
