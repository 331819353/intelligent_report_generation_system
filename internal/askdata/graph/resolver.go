package graph

import (
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

var (
	ErrGraphResolutionUnavailable = errors.New("graph resolution is unavailable")
	ErrCertifiedCacheInvalid      = errors.New("certified graph plan cache is invalid")
	ErrGraphPlanIncomplete        = errors.New("graph plan is incomplete")
)

type ResolutionSource string

const (
	ResolutionSourceNebula            ResolutionSource = "NEBULA"
	ResolutionSourceCertifiedCache    ResolutionSource = "CERTIFIED_CACHE"
	ResolutionSourcePostgresFallback  ResolutionSource = "POSTGRES_FALLBACK"
	DegradationNebulaUnavailable      string           = "NEBULA_UNAVAILABLE"
	DegradationNebulaResultIncomplete string           = "NEBULA_RESULT_INCOMPLETE"
)

type PrimaryPlanner interface {
	Resolve(context.Context, PlanRequest) (GraphPlan, error)
}

type CertifiedPlanCache interface {
	Load(context.Context, PlanRequest) (GraphPlan, bool, error)
}

type FallbackPlanner interface {
	Resolve(context.Context, PlanRequest) (GraphPlan, error)
}

type Resolution struct {
	Plan              GraphPlan        `json:"plan"`
	Source            ResolutionSource `json:"source"`
	Degraded          bool             `json:"degraded"`
	DegradationReason string           `json:"degradationReason,omitempty"`
}

func (resolution Resolution) Validate(request PlanRequest) error {
	normalized, requestHash, err := normalizePlanRequest(request)
	if err != nil {
		return err
	}
	if err := validatePlanForRequest(resolution.Plan, normalized, requestHash); err != nil {
		return err
	}
	switch resolution.Source {
	case ResolutionSourceNebula:
		if resolution.Degraded || resolution.DegradationReason != "" {
			return errors.New("primary graph resolution cannot be degraded")
		}
	case ResolutionSourceCertifiedCache, ResolutionSourcePostgresFallback:
		if !resolution.Degraded || (resolution.DegradationReason != DegradationNebulaUnavailable &&
			resolution.DegradationReason != DegradationNebulaResultIncomplete) {
			return errors.New("degraded graph resolution reason is invalid")
		}
	default:
		return errors.New("graph resolution source is invalid")
	}
	return nil
}

type Resolver struct {
	primary  PrimaryPlanner
	cache    CertifiedPlanCache
	fallback FallbackPlanner
}

func NewResolver(
	primary PrimaryPlanner,
	cache CertifiedPlanCache,
	fallback FallbackPlanner,
) (*Resolver, error) {
	if primary == nil || cache == nil || fallback == nil {
		return nil, ErrGraphResolutionUnavailable
	}
	return &Resolver{primary: primary, cache: cache, fallback: fallback}, nil
}

func (resolver *Resolver) Resolve(ctx context.Context, request PlanRequest) (Resolution, error) {
	if resolver == nil || resolver.primary == nil || resolver.cache == nil || resolver.fallback == nil {
		return Resolution{}, ErrGraphResolutionUnavailable
	}
	normalized, requestHash, err := normalizePlanRequest(request)
	if err != nil {
		return Resolution{}, err
	}
	if err := contextError(ctx); err != nil {
		return Resolution{}, err
	}

	primaryPlan, primaryErr := resolver.primary.Resolve(ctx, normalized)
	if primaryErr == nil {
		primaryErr = validatePlanForRequest(primaryPlan, normalized, requestHash)
	}
	if primaryErr == nil && len(primaryPlan.MetricModels) == 0 {
		primaryErr = ErrGraphPlanIncomplete
	}
	if primaryErr == nil {
		resolution := Resolution{Plan: primaryPlan, Source: ResolutionSourceNebula}
		if err := resolution.Validate(normalized); err != nil {
			return Resolution{}, fmt.Errorf("%w: primary validation", ErrGraphResolutionUnavailable)
		}
		return resolution, nil
	}
	if isContextError(primaryErr) {
		return Resolution{}, primaryErr
	}
	reason := DegradationNebulaUnavailable
	if errors.Is(primaryErr, ErrGraphPlanIncomplete) {
		reason = DegradationNebulaResultIncomplete
	}

	cachePlan, cacheHit, cacheErr := resolver.cache.Load(ctx, normalized)
	if cacheErr == nil && cacheHit {
		cacheErr = validatePlanForRequest(cachePlan, normalized, requestHash)
	}
	if cacheErr == nil && cacheHit {
		resolution := Resolution{
			Plan: cachePlan, Source: ResolutionSourceCertifiedCache,
			Degraded: true, DegradationReason: reason,
		}
		if err := resolution.Validate(normalized); err == nil {
			return resolution, nil
		}
		cacheErr = ErrCertifiedCacheInvalid
	}
	if isContextError(cacheErr) {
		return Resolution{}, cacheErr
	}

	fallbackPlan, fallbackErr := resolver.fallback.Resolve(ctx, normalized)
	if fallbackErr == nil {
		fallbackErr = validatePlanForRequest(fallbackPlan, normalized, requestHash)
	}
	if fallbackErr != nil {
		if isContextError(fallbackErr) {
			return Resolution{}, fallbackErr
		}
		return Resolution{}, errors.Join(
			ErrGraphResolutionUnavailable,
			resolutionStageError("primary", primaryErr),
			resolutionStageError("cache", cacheErr),
			resolutionStageError("postgres", fallbackErr),
		)
	}
	resolution := Resolution{
		Plan: fallbackPlan, Source: ResolutionSourcePostgresFallback,
		Degraded: true, DegradationReason: reason,
	}
	if err := resolution.Validate(normalized); err != nil {
		return Resolution{}, fmt.Errorf("%w: fallback validation", ErrGraphResolutionUnavailable)
	}
	return resolution, nil
}

func normalizePlanRequest(request PlanRequest) (PlanRequest, askdata.ContentHash, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return PlanRequest{}, "", err
	}
	hash, _, err := registry.CanonicalContentHash(normalized)
	if err != nil {
		return PlanRequest{}, "", err
	}
	return normalized, hash, nil
}

func validatePlanForRequest(
	plan GraphPlan,
	request PlanRequest,
	requestHash askdata.ContentHash,
) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidGraphResult, err)
	}
	if plan.RequestHash != requestHash || plan.DomainID != request.DomainID ||
		plan.Scope.TenantID != request.Scope.TenantID ||
		plan.Scope.ActorID != request.Scope.ActorID ||
		plan.Scope.PolicyHash != request.Scope.PolicyHash ||
		plan.Scope.Release.ReleaseID != request.Scope.Release.ReleaseID ||
		plan.Scope.Release.ContentHash != request.Scope.Release.ContentHash {
		return fmt.Errorf("%w: graph plan does not match the exact request scope", ErrInvalidGraphResult)
	}
	return nil
}

func resolutionStageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

var _ PrimaryPlanner = (*Client)(nil)
