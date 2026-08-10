package security

import (
	"encoding/json"
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

var (
	ErrInvalidToolSecurityContext = errors.New("tool security context is invalid")
	ErrToolCallRefused            = errors.New("tool call was refused by the security boundary")
)

// ToolSecurityContext is issued by the trusted orchestration loop. Available
// tools have already been intersected with the current stage, permissions and
// remaining budget; none of these fields can originate in an LLM action.
type ToolSecurityContext struct {
	Authorization  toolhost.AuthorizationContext
	Budget         toolhost.BudgetAllowance
	AvailableTools []toolhost.ToolName
}

func (securityContext ToolSecurityContext) Validate() error {
	authorization := securityContext.Authorization
	if authorization.Scope.Validate() != nil || authorization.DomainID.Validate() != nil ||
		!containsToolSecurityID(authorization.Scope.DomainIDs, authorization.DomainID) ||
		len(authorization.Permissions) == 0 || len(authorization.Permissions) > 10 ||
		securityContext.Budget.ToolCallsRemaining < 0 || securityContext.Budget.ToolCallsRemaining > 8 ||
		securityContext.Budget.FormalQueriesRemaining < 0 || securityContext.Budget.FormalQueriesRemaining > 2 ||
		securityContext.Budget.ValidationQueriesRemaining < 0 || securityContext.Budget.ValidationQueriesRemaining > 3 {
		return ErrInvalidToolSecurityContext
	}
	seenPermissions := map[toolhost.Permission]bool{}
	for _, permission := range authorization.Permissions {
		if !validToolSecurityPermission(permission) || seenPermissions[permission] {
			return ErrInvalidToolSecurityContext
		}
		seenPermissions[permission] = true
	}
	for index, tool := range securityContext.AvailableTools {
		if !toolhost.IsKnownTool(tool) ||
			(index > 0 && securityContext.AvailableTools[index-1] >= tool) {
			return ErrInvalidToolSecurityContext
		}
	}
	return nil
}

// SanitizeToolCall creates an isolated copy and replays every closed argument
// rule immediately before Tool Host execution. It refuses any attempt to use
// an unadvertised tool, change the pinned release/domain, smuggle instruction
// text into a free-text argument, or act after the trusted budget removed a
// tool from the allowlist.
func SanitizeToolCall(
	securityContext ToolSecurityContext,
	request toolhost.CallRequest,
) (toolhost.CallRequest, error) {
	if securityContext.Validate() != nil {
		return toolhost.CallRequest{}, ErrInvalidToolSecurityContext
	}
	if !containsToolSecurityName(securityContext.AvailableTools, request.Tool) ||
		!securityContext.Budget.AllowsTool(request.Tool) ||
		toolhost.ValidateCall(request, toolhost.DefaultArgumentValidator{}) != nil ||
		request.Arguments.Release != securityContext.Authorization.Scope.Release {
		return toolhost.CallRequest{}, ErrToolCallRefused
	}
	for _, domainID := range request.Arguments.DomainIDs {
		if domainID != securityContext.Authorization.DomainID {
			return toolhost.CallRequest{}, ErrToolCallRefused
		}
	}
	if toolArgumentTextWasInjected(request.Arguments) {
		return toolhost.CallRequest{}, ErrToolCallRefused
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return toolhost.CallRequest{}, ErrToolCallRefused
	}
	var sanitized toolhost.CallRequest
	if err := askdata.DecodeStrictJSON(raw, &sanitized); err != nil ||
		toolhost.ValidateCall(sanitized, toolhost.DefaultArgumentValidator{}) != nil ||
		sanitized.Arguments.Release != securityContext.Authorization.Scope.Release {
		return toolhost.CallRequest{}, ErrToolCallRefused
	}
	for _, domainID := range sanitized.Arguments.DomainIDs {
		if domainID != securityContext.Authorization.DomainID {
			return toolhost.CallRequest{}, ErrToolCallRefused
		}
	}
	return sanitized, nil
}

func toolArgumentTextWasInjected(arguments toolhost.ToolArguments) bool {
	values := []*string{
		arguments.Mention, arguments.QuestionSummary,
		arguments.ConflictCode, arguments.ClarificationQuestion,
	}
	for _, option := range arguments.ClarificationOptions {
		label := option.Label
		values = append(values, &label)
	}
	for _, value := range values {
		if value == nil {
			continue
		}
		payload, err := json.Marshal(map[string]string{"value": *value})
		if err != nil {
			return true
		}
		assessment, err := AssessUntrustedPromptData("TOOL_ARGUMENT", payload)
		if err != nil || assessment.Enforce() != nil {
			return true
		}
	}
	return false
}

func containsToolSecurityID(values []askdata.ID, want askdata.ID) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= want })
	return index < len(values) && values[index] == want
}

func containsToolSecurityName(values []toolhost.ToolName, want toolhost.ToolName) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= want })
	return index < len(values) && values[index] == want
}

func validToolSecurityPermission(value toolhost.Permission) bool {
	switch value {
	case toolhost.PermissionSemanticRead, toolhost.PermissionDimensionValueRead,
		toolhost.PermissionGraphResolve, toolhost.PermissionQualityRead,
		toolhost.PermissionQueryCompile, toolhost.PermissionQueryValidate,
		toolhost.PermissionCardinalityProbe, toolhost.PermissionQueryExecute,
		toolhost.PermissionValidationQueryExecute, toolhost.PermissionClarificationRequest:
		return true
	default:
		return false
	}
}
