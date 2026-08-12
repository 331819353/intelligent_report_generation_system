package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AskDataBudgetOverride is a complete, domain-scoped replacement for one of
// the governed AskData budget classes. Partial overrides are deliberately not
// accepted: every effective limit remains visible and reviewable in config.
type AskDataBudgetOverride struct {
	DomainID             string
	BudgetClass          string
	MaxLLMCalls          int
	MaxToolCalls         int
	MaxPrimaryQueries    int
	MaxValidationQueries int
	MaxCandidateCompares int
	MaxJoinHops          int
	HardTimeout          time.Duration
	P95Target            time.Duration
	MaxConcurrentPlans   int
}

type askDataBudgetOverrideJSON struct {
	DomainID             string `json:"domainId"`
	BudgetClass          string `json:"budgetClass"`
	MaxLLMCalls          int    `json:"maxLlmCalls"`
	MaxToolCalls         int    `json:"maxToolCalls"`
	MaxPrimaryQueries    int    `json:"maxPrimaryQueries"`
	MaxValidationQueries int    `json:"maxValidationQueries"`
	MaxCandidateCompares int    `json:"maxCandidateCompares"`
	MaxJoinHops          int    `json:"maxJoinHops"`
	HardTimeout          string `json:"hardTimeout"`
	P95Target            string `json:"p95Target"`
	MaxConcurrentPlans   int    `json:"maxConcurrentPlans"`
}

// ParseAskDataBudgetOverrides decodes ASKDATA_RUN_BUDGET_OVERRIDES. The value
// is a strict JSON array so an unknown/misspelled limit cannot be ignored.
func ParseAskDataBudgetOverrides(value string) ([]AskDataBudgetOverride, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var encoded []askDataBudgetOverrideJSON
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("parse ASKDATA_RUN_BUDGET_OVERRIDES: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("parse ASKDATA_RUN_BUDGET_OVERRIDES: %w", err)
	}
	if len(encoded) > 256 {
		return nil, errors.New("ASKDATA_RUN_BUDGET_OVERRIDES must contain at most 256 entries")
	}

	result := make([]AskDataBudgetOverride, 0, len(encoded))
	seen := make(map[string]struct{}, len(encoded))
	for index, item := range encoded {
		hardTimeout, err := time.ParseDuration(strings.TrimSpace(item.HardTimeout))
		if err != nil {
			return nil, fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d].hardTimeout is invalid: %w", index, err)
		}
		p95Target, err := time.ParseDuration(strings.TrimSpace(item.P95Target))
		if err != nil {
			return nil, fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d].p95Target is invalid: %w", index, err)
		}
		override := AskDataBudgetOverride{
			DomainID:    strings.ToLower(strings.TrimSpace(item.DomainID)),
			BudgetClass: strings.ToUpper(strings.TrimSpace(item.BudgetClass)),
			MaxLLMCalls: item.MaxLLMCalls, MaxToolCalls: item.MaxToolCalls,
			MaxPrimaryQueries:    item.MaxPrimaryQueries,
			MaxValidationQueries: item.MaxValidationQueries,
			MaxCandidateCompares: item.MaxCandidateCompares,
			MaxJoinHops:          item.MaxJoinHops, HardTimeout: hardTimeout,
			P95Target: p95Target, MaxConcurrentPlans: item.MaxConcurrentPlans,
		}
		if err := validateAskDataBudgetOverride(override); err != nil {
			return nil, fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d]: %w", index, err)
		}
		key := override.DomainID + ":" + override.BudgetClass
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d] duplicates domain and budget class", index)
		}
		seen[key] = struct{}{}
		result = append(result, override)
	}
	return result, nil
}

func validateAskDataBudgetOverride(value AskDataBudgetOverride) error {
	if uuid.Validate(value.DomainID) != nil {
		return errors.New("domainId must be a UUID")
	}
	switch value.BudgetClass {
	case "SINGLE_QUERY_FAST", "SINGLE_QUERY_COMPLEX", "BUNDLE", "DEFINITION":
	default:
		return errors.New("budgetClass is invalid")
	}
	if value.MaxLLMCalls < 1 || value.MaxLLMCalls > 16 ||
		value.MaxToolCalls < 0 || value.MaxToolCalls > 16 ||
		value.MaxPrimaryQueries < 0 || value.MaxPrimaryQueries > 6 ||
		value.MaxValidationQueries < 0 || value.MaxValidationQueries > 3 ||
		value.MaxCandidateCompares < 0 || value.MaxCandidateCompares > 2 ||
		value.MaxJoinHops < 0 || value.MaxJoinHops > 4 ||
		value.HardTimeout < 100*time.Millisecond || value.HardTimeout > 120*time.Second ||
		value.P95Target <= 0 || value.P95Target > value.HardTimeout {
		return errors.New("budget limits exceed the governed bounds")
	}
	if value.BudgetClass == "BUNDLE" {
		if value.MaxConcurrentPlans < 1 || value.MaxConcurrentPlans > 4 {
			return errors.New("BUNDLE maxConcurrentPlans must be between 1 and 4")
		}
	} else if value.MaxConcurrentPlans != 0 {
		return errors.New("maxConcurrentPlans is only valid for BUNDLE")
	}
	return nil
}

func validateAskDataBudgetOverrides(values []AskDataBudgetOverride) error {
	if len(values) > 256 {
		return errors.New("ASKDATA_RUN_BUDGET_OVERRIDES must contain at most 256 entries")
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateAskDataBudgetOverride(value); err != nil {
			return fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d]: %w", index, err)
		}
		key := strings.ToLower(value.DomainID) + ":" + strings.ToUpper(value.BudgetClass)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("ASKDATA_RUN_BUDGET_OVERRIDES[%d] duplicates domain and budget class", index)
		}
		seen[key] = struct{}{}
	}
	return nil
}
