package observability

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const quotaWarningNumerator int64 = 80
const quotaWarningDenominator int64 = 100

type QuotaScope string

const (
	QuotaScopeTenant QuotaScope = "TENANT"
	QuotaScopeDomain QuotaScope = "DOMAIN"
	QuotaScopeUser   QuotaScope = "USER"
	QuotaScopeRun    QuotaScope = "RUN"
)

type QuotaPeriod string

const (
	QuotaPeriodDay   QuotaPeriod = "DAY"
	QuotaPeriodMonth QuotaPeriod = "MONTH"
	QuotaPeriodRun   QuotaPeriod = "RUN"
)

type QuotaDimension string

const (
	QuotaDimensionLLMTokens QuotaDimension = "LLM_TOKENS"
	QuotaDimensionRuns      QuotaDimension = "RUNS"
	QuotaDimensionCostCents QuotaDimension = "COST_CENTS"
)

type QuotaStatus string

const (
	QuotaAvailable       QuotaStatus = "AVAILABLE"
	QuotaWarning         QuotaStatus = "WARNING"
	QuotaExceeded        QuotaStatus = "QUOTA_EXCEEDED"
	QuotaRunCostExceeded QuotaStatus = "RUN_COST_EXCEEDED"
)

type QuotaLimits struct {
	LLMTokens *int64
	Runs      *int64
	CostCents *int64
}

type QuotaUsage struct {
	LLMTokens int64
	Runs      int64
	CostCents int64
}

type QuotaSnapshot struct {
	Scope   QuotaScope
	ScopeID askdata.ID
	Period  QuotaPeriod
	Limits  QuotaLimits
	Usage   QuotaUsage
	ResetAt time.Time
}

type QuotaCheckRequest struct {
	TenantID          askdata.ID
	DomainID          askdata.ID
	ActorID           askdata.ID
	RunID             askdata.ID
	Reserve           QuotaUsage
	CertifiedFastPath bool
	At                time.Time
}

type QuotaLimiter struct {
	Scope       QuotaScope     `json:"scope"`
	ScopeID     askdata.ID     `json:"scopeId"`
	Period      QuotaPeriod    `json:"period"`
	Dimension   QuotaDimension `json:"dimension"`
	Used        int64          `json:"used"`
	Reserved    int64          `json:"reserved"`
	Limit       int64          `json:"limit"`
	Remaining   int64          `json:"remaining"`
	PercentUsed int64          `json:"percentUsed"`
	ResetAt     time.Time      `json:"resetAt"`
	Exceeded    bool           `json:"exceeded"`
}

type QuotaDecision struct {
	Status               QuotaStatus    `json:"status"`
	Allowed              bool           `json:"allowed"`
	FastPathBypass       bool           `json:"fastPathBypass"`
	RequireClarification bool           `json:"requireClarification"`
	Limiters             []QuotaLimiter `json:"limiters,omitempty"`
}

func EvaluateQuota(request QuotaCheckRequest, snapshots []QuotaSnapshot) (QuotaDecision, error) {
	if err := validateQuotaRequest(request); err != nil {
		return QuotaDecision{}, err
	}
	seen := make(map[string]struct{}, len(snapshots))
	limiters := make([]QuotaLimiter, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if err := validateQuotaSnapshot(request, snapshot); err != nil {
			return QuotaDecision{}, err
		}
		key := string(snapshot.Scope) + "\x00" + string(snapshot.ScopeID) + "\x00" + string(snapshot.Period)
		if _, duplicate := seen[key]; duplicate {
			return QuotaDecision{}, errors.New("duplicate quota snapshot")
		}
		seen[key] = struct{}{}
		limiters = appendQuotaLimiter(limiters, snapshot, QuotaDimensionLLMTokens, snapshot.Usage.LLMTokens, request.Reserve.LLMTokens, snapshot.Limits.LLMTokens)
		limiters = appendQuotaLimiter(limiters, snapshot, QuotaDimensionRuns, snapshot.Usage.Runs, request.Reserve.Runs, snapshot.Limits.Runs)
		limiters = appendQuotaLimiter(limiters, snapshot, QuotaDimensionCostCents, snapshot.Usage.CostCents, request.Reserve.CostCents, snapshot.Limits.CostCents)
	}
	sort.Slice(limiters, func(i, j int) bool {
		if limiters[i].Exceeded != limiters[j].Exceeded {
			return limiters[i].Exceeded
		}
		if limiters[i].PercentUsed != limiters[j].PercentUsed {
			return limiters[i].PercentUsed > limiters[j].PercentUsed
		}
		if limiters[i].Scope != limiters[j].Scope {
			return quotaScopeRank(limiters[i].Scope) > quotaScopeRank(limiters[j].Scope)
		}
		return limiters[i].Dimension < limiters[j].Dimension
	})
	decision := QuotaDecision{Status: QuotaAvailable, Allowed: true, Limiters: limiters}
	hasExceeded := false
	for _, limiter := range limiters {
		if limiter.Exceeded {
			hasExceeded = true
			if limiter.Scope == QuotaScopeRun && limiter.Dimension == QuotaDimensionCostCents {
				decision.Status = QuotaRunCostExceeded
				decision.Allowed = false
				decision.RequireClarification = true
				return decision, nil
			}
		}
	}
	if hasExceeded {
		if request.CertifiedFastPath {
			decision.Status = QuotaWarning
			decision.FastPathBypass = true
			return decision, nil
		}
		decision.Status = QuotaExceeded
		decision.Allowed = false
		return decision, nil
	}
	if len(limiters) > 0 {
		decision.Status = QuotaWarning
	}
	return decision, nil
}

func appendQuotaLimiter(
	destination []QuotaLimiter,
	snapshot QuotaSnapshot,
	dimension QuotaDimension,
	used int64,
	reserved int64,
	limit *int64,
) []QuotaLimiter {
	if limit == nil || !atLeastRatio(used, reserved, *limit, quotaWarningNumerator, quotaWarningDenominator) {
		return destination
	}
	remaining := *limit - used
	if remaining < 0 {
		remaining = 0
	}
	return append(destination, QuotaLimiter{
		Scope: snapshot.Scope, ScopeID: snapshot.ScopeID, Period: snapshot.Period,
		Dimension: dimension, Used: used, Reserved: reserved, Limit: *limit,
		Remaining: remaining, PercentUsed: cappedPercent(used, reserved, *limit),
		ResetAt: snapshot.ResetAt.UTC(), Exceeded: wouldReachLimit(used, reserved, *limit),
	})
}

func validateQuotaRequest(request QuotaCheckRequest) error {
	for label, id := range map[string]askdata.ID{
		"tenant": request.TenantID, "domain": request.DomainID,
		"actor": request.ActorID, "run": request.RunID,
	} {
		if id.Validate() != nil {
			return fmt.Errorf("%s quota identity is invalid", label)
		}
	}
	if request.At.IsZero() || request.Reserve.LLMTokens < 0 || request.Reserve.Runs < 0 || request.Reserve.CostCents < 0 {
		return errors.New("quota request is invalid")
	}
	return nil
}

func validateQuotaSnapshot(request QuotaCheckRequest, snapshot QuotaSnapshot) error {
	expectedID := map[QuotaScope]askdata.ID{
		QuotaScopeTenant: request.TenantID,
		QuotaScopeDomain: request.DomainID,
		QuotaScopeUser:   request.ActorID,
		QuotaScopeRun:    request.RunID,
	}[snapshot.Scope]
	if expectedID == "" || snapshot.ScopeID != expectedID || snapshot.ScopeID.Validate() != nil {
		return errors.New("quota snapshot scope is invalid")
	}
	if (snapshot.Scope == QuotaScopeRun) != (snapshot.Period == QuotaPeriodRun) ||
		(snapshot.Period != QuotaPeriodDay && snapshot.Period != QuotaPeriodMonth && snapshot.Period != QuotaPeriodRun) {
		return errors.New("quota snapshot period is invalid")
	}
	if snapshot.ResetAt.IsZero() || !snapshot.ResetAt.After(request.At) ||
		snapshot.Usage.LLMTokens < 0 || snapshot.Usage.Runs < 0 || snapshot.Usage.CostCents < 0 {
		return errors.New("quota snapshot usage is invalid")
	}
	configured := 0
	for _, limit := range []*int64{snapshot.Limits.LLMTokens, snapshot.Limits.Runs, snapshot.Limits.CostCents} {
		if limit != nil {
			configured++
			if *limit <= 0 {
				return errors.New("quota snapshot limit is invalid")
			}
		}
	}
	if configured == 0 {
		return errors.New("quota snapshot has no limit")
	}
	return nil
}

func atLeastRatio(used, reserved, limit, numerator, denominator int64) bool {
	if used >= limit || reserved >= limit-used {
		return true
	}
	projected := used + reserved
	threshold := limit / denominator * numerator
	threshold += (limit%denominator*numerator + denominator - 1) / denominator
	return projected >= threshold
}

func wouldReachLimit(used, reserved, limit int64) bool {
	return used >= limit || reserved >= limit-used
}

func cappedPercent(used, reserved, limit int64) int64 {
	if wouldReachLimit(used, reserved, limit) {
		return 100
	}
	// The exact threshold decision above is integer based. Percentage is display
	// metadata; float conversion avoids overflowing projected*100 near MaxInt64.
	return int64(float64(used+reserved) * 100 / float64(limit))
}

func quotaScopeRank(scope QuotaScope) int {
	switch scope {
	case QuotaScopeRun:
		return 4
	case QuotaScopeUser:
		return 3
	case QuotaScopeDomain:
		return 2
	case QuotaScopeTenant:
		return 1
	default:
		return 0
	}
}
