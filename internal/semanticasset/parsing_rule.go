package semanticasset

import (
	"context"
	"regexp"
	"strings"
)

var parsingRuleCodePattern = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_-]{0,127}$`,
)

func (service *Service) ListParsingRules(
	ctx context.Context,
	tenantID string,
	filter ParsingRuleFilter,
) ([]ParsingRule, int, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	filter.RuleType = strings.ToUpper(strings.TrimSpace(filter.RuleType))
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if service == nil || service.store == nil || !validUUID(tenantID) ||
		!normalizePage(&filter.Page) ||
		!validOptionalText(filter.Query, 256) ||
		!validOptionalValue(
			filter.RuleType,
			"METRIC_NAME_SUFFIX", "ADMIN_REGION_SUFFIX",
			"QUERY_RESIDUAL_TERM", "BROAD_METRIC_PHRASE",
		) || !validOptionalValue(filter.Status, "ACTIVE", "DEPRECATED") {
		return nil, 0, ErrInvalidRequest
	}
	return service.store.ListParsingRules(ctx, tenantID, filter)
}

func (service *Service) CreateParsingRule(
	ctx context.Context,
	tenantID, actorID string,
	input ParsingRuleInput,
) (ParsingRule, error) {
	input = normalizeParsingRuleInput(input)
	if service == nil || service.store == nil ||
		!validActor(tenantID, actorID) || !validParsingRuleInput(input) {
		return ParsingRule{}, ErrInvalidRequest
	}
	return service.store.CreateParsingRule(ctx, tenantID, actorID, input)
}

func (service *Service) UpdateParsingRule(
	ctx context.Context,
	tenantID, actorID, id string,
	input ParsingRuleUpdateInput,
) (ParsingRule, error) {
	input.ParsingRuleInput = normalizeParsingRuleInput(input.ParsingRuleInput)
	if service == nil || service.store == nil ||
		!validActor(tenantID, actorID) || !validUUID(id) ||
		input.ExpectedVersion < 1 ||
		!validParsingRuleInput(input.ParsingRuleInput) {
		return ParsingRule{}, ErrInvalidRequest
	}
	return service.store.UpdateParsingRule(
		ctx, tenantID, actorID, id, input,
	)
}

func (service *Service) DeprecateParsingRule(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
) (ParsingRule, error) {
	if service == nil || service.store == nil ||
		!validActor(tenantID, actorID) || !validUUID(id) ||
		expectedVersion < 1 {
		return ParsingRule{}, ErrInvalidRequest
	}
	return service.store.DeprecateParsingRule(
		ctx, tenantID, actorID, id, expectedVersion,
	)
}

func normalizeParsingRuleInput(input ParsingRuleInput) ParsingRuleInput {
	input.RuleType = strings.ToUpper(strings.TrimSpace(input.RuleType))
	input.Pattern = strings.TrimSpace(input.Pattern)
	input.MatchMode = strings.ToUpper(strings.TrimSpace(input.MatchMode))
	input.Action = strings.ToUpper(strings.TrimSpace(input.Action))
	input.OutputName = strings.TrimSpace(input.OutputName)
	input.OutputCode = strings.TrimSpace(input.OutputCode)
	return input
}

func validParsingRuleInput(input ParsingRuleInput) bool {
	if !validText(input.Pattern, 1, 256) ||
		input.Priority < 0 || input.Priority > 1000 {
		return false
	}
	switch input.RuleType {
	case "METRIC_NAME_SUFFIX":
		return input.MatchMode == "SUFFIX" && input.Action == "STRIP_SUFFIX" &&
			input.OutputName == "" && input.OutputCode == "" &&
			input.MinimumLength >= 1 && input.MinimumLength <= 32 &&
			input.MaximumLength == 0
	case "ADMIN_REGION_SUFFIX":
		return input.MatchMode == "SUFFIX" &&
			input.Action == "MAP_ADMIN_REGION" &&
			validText(input.OutputName, 1, 128) &&
			parsingRuleCodePattern.MatchString(input.OutputCode) &&
			input.MinimumLength >= 1 && input.MinimumLength <= 32 &&
			input.MaximumLength >= input.MinimumLength &&
			input.MaximumLength <= 256
	case "QUERY_RESIDUAL_TERM":
		return input.MatchMode == "EXACT" &&
			input.Action == "ALLOW_DETERMINISTIC" &&
			input.OutputName == "" && input.OutputCode == "" &&
			input.MinimumLength == 0 && input.MaximumLength == 0
	case "BROAD_METRIC_PHRASE":
		return input.MatchMode == "CONTAINS" &&
			input.Action == "REQUIRE_METRIC_CONFIRMATION" &&
			input.OutputName == "" && input.OutputCode == "" &&
			input.MinimumLength == 0 && input.MaximumLength == 0
	default:
		return false
	}
}
