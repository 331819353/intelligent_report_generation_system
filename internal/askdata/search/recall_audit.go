package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	DefaultRecallAuditInterval   = 24 * time.Hour
	DefaultRecallAuditWindow     = 7 * 24 * time.Hour
	DefaultRecallSampleRetention = 30 * 24 * time.Hour
	DefaultRecallAuditSampleSize = 100
	DefaultRecallAuditEFSearch   = 100
	DefaultRecallAuditThreshold  = 0.99
	SearchEmbeddingDimension     = 2_560
)

var (
	ErrInvalidRecallAudit = errors.New("search recall audit is invalid")
	defaultRecallAuditKs  = [...]int{10, 20, 30}
)

type QueryVectorSample struct {
	ID                 string
	TenantID           string
	DomainID           string
	Release            askdata.ReleaseRef
	DocumentType       ObjectType
	Embedding          []float32
	EmbeddingModel     string
	EmbeddingDimension int
	CapturedAt         time.Time
}

func (sample QueryVectorSample) Validate() error {
	if uuid.Validate(sample.ID) != nil || uuid.Validate(sample.TenantID) != nil ||
		uuid.Validate(sample.DomainID) != nil || sample.Release.Validate() != nil ||
		!ValidRetrievalObjectType(sample.DocumentType) ||
		strings.TrimSpace(sample.EmbeddingModel) == "" || len(sample.EmbeddingModel) > 128 ||
		sample.EmbeddingDimension != SearchEmbeddingDimension ||
		len(sample.Embedding) != sample.EmbeddingDimension || sample.CapturedAt.IsZero() {
		return ErrInvalidRecallAudit
	}
	return nil
}

type RecallAuditResult struct {
	TenantID           string
	DomainID           string
	RunAt              time.Time
	DocumentType       ObjectType
	K                  int
	SampleSize         int
	Recall             float64
	P95LatencyANN      time.Duration
	P95LatencyExact    time.Duration
	EmbeddingModel     string
	EmbeddingDimension int
	EFSearch           int
	Threshold          float64
	BelowThreshold     bool
}

func (result RecallAuditResult) Validate() error {
	if uuid.Validate(result.TenantID) != nil || uuid.Validate(result.DomainID) != nil ||
		result.RunAt.IsZero() || !ValidRetrievalObjectType(result.DocumentType) ||
		!validRecallK(result.K) || result.SampleSize < 1 ||
		math.IsNaN(result.Recall) || math.IsInf(result.Recall, 0) ||
		result.Recall < 0 || result.Recall > 1 || result.P95LatencyANN < 0 ||
		result.P95LatencyExact < 0 || strings.TrimSpace(result.EmbeddingModel) == "" ||
		len(result.EmbeddingModel) > 128 || result.EmbeddingDimension != SearchEmbeddingDimension ||
		result.EFSearch < 1 || result.EFSearch > 10_000 ||
		math.IsNaN(result.Threshold) || math.IsInf(result.Threshold, 0) ||
		result.Threshold <= 0 || result.Threshold > 1 ||
		result.BelowThreshold != (result.Recall < result.Threshold) {
		return ErrInvalidRecallAudit
	}
	return nil
}

type RecallAuditOptions struct {
	Interval           time.Duration
	Window             time.Duration
	SampleRetention    time.Duration
	SampleSizePerGroup int
	Ks                 []int
	EFSearch           int
	Threshold          float64
	EmbeddingModel     string
	EmbeddingDimension int
}

func DefaultRecallAuditOptions(model string, dimension int) RecallAuditOptions {
	return RecallAuditOptions{
		Interval: DefaultRecallAuditInterval, Window: DefaultRecallAuditWindow,
		SampleRetention:    DefaultRecallSampleRetention,
		SampleSizePerGroup: DefaultRecallAuditSampleSize,
		Ks:                 append([]int(nil), defaultRecallAuditKs[:]...), EFSearch: DefaultRecallAuditEFSearch,
		Threshold:      DefaultRecallAuditThreshold,
		EmbeddingModel: strings.TrimSpace(model), EmbeddingDimension: dimension,
	}
}

func (options RecallAuditOptions) Validate() error {
	if options.Interval < time.Hour || options.Interval > 30*24*time.Hour ||
		options.Window < time.Hour || options.Window > 90*24*time.Hour ||
		options.SampleRetention < options.Window || options.SampleRetention > 365*24*time.Hour ||
		options.SampleSizePerGroup < 1 || options.SampleSizePerGroup > 10_000 ||
		options.EFSearch < 1 || options.EFSearch > 10_000 ||
		math.IsNaN(options.Threshold) || math.IsInf(options.Threshold, 0) ||
		options.Threshold <= 0 || options.Threshold > 1 ||
		strings.TrimSpace(options.EmbeddingModel) == "" || len(options.EmbeddingModel) > 128 ||
		options.EmbeddingDimension != SearchEmbeddingDimension || len(options.Ks) != 3 {
		return ErrInvalidRecallAudit
	}
	ks := append([]int(nil), options.Ks...)
	sort.Ints(ks)
	for index, k := range ks {
		if !validRecallK(k) || index > 0 && ks[index-1] == k {
			return ErrInvalidRecallAudit
		}
	}
	return nil
}

type RecallAuditStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	LastRunAt(context.Context, string) (time.Time, bool, error)
	LoadSamples(context.Context, string, time.Time, int, string, int) ([]QueryVectorSample, error)
	SearchANN(context.Context, QueryVectorSample, int, int) ([]askdata.ID, time.Duration, error)
	SearchExact(context.Context, QueryVectorSample, int) ([]askdata.ID, time.Duration, error)
	SaveRecallAudits(context.Context, string, []RecallAuditResult) error
	PurgeSamplesBefore(context.Context, string, time.Time) error
}

type RecallAlert func(context.Context, RecallAuditResult)

type RecallAuditor struct {
	store   RecallAuditStore
	options RecallAuditOptions
	alert   RecallAlert
}

func NewRecallAuditor(
	store RecallAuditStore, options RecallAuditOptions, alert RecallAlert,
) (*RecallAuditor, error) {
	if store == nil {
		return nil, ErrInvalidRecallAudit
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &RecallAuditor{store: store, options: options, alert: alert}, nil
}

func (auditor *RecallAuditor) TenantIDs(ctx context.Context) ([]string, error) {
	if auditor == nil || auditor.store == nil {
		return nil, ErrInvalidRecallAudit
	}
	return auditor.store.ListTenantIDs(ctx)
}

func (auditor *RecallAuditor) RunTenant(
	ctx context.Context, tenantID string, runAt time.Time,
) ([]RecallAuditResult, error) {
	if auditor == nil || auditor.store == nil || uuid.Validate(tenantID) != nil || runAt.IsZero() {
		return nil, ErrInvalidRecallAudit
	}
	lastRun, present, err := auditor.store.LastRunAt(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if present && runAt.Before(lastRun.Add(auditor.options.Interval)) {
		return []RecallAuditResult{}, nil
	}
	samples, err := auditor.store.LoadSamples(
		ctx, tenantID, runAt.Add(-auditor.options.Window), auditor.options.SampleSizePerGroup,
		auditor.options.EmbeddingModel, auditor.options.EmbeddingDimension,
	)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		if err := auditor.store.PurgeSamplesBefore(
			ctx, tenantID, runAt.Add(-auditor.options.SampleRetention),
		); err != nil {
			return nil, err
		}
		return []RecallAuditResult{}, nil
	}

	maxK := auditor.options.Ks[0]
	for _, k := range auditor.options.Ks[1:] {
		if k > maxK {
			maxK = k
		}
	}
	type groupKey struct {
		domainID     string
		documentType ObjectType
	}
	type aggregate struct {
		recallSums   map[int]float64
		annLatency   []time.Duration
		exactLatency []time.Duration
		samples      int
	}
	aggregates := map[groupKey]*aggregate{}
	for _, sample := range samples {
		if err := sample.Validate(); err != nil || sample.TenantID != tenantID ||
			sample.EmbeddingModel != auditor.options.EmbeddingModel ||
			sample.EmbeddingDimension != auditor.options.EmbeddingDimension {
			return nil, ErrInvalidRecallAudit
		}
		ann, annLatency, err := auditor.store.SearchANN(
			ctx, sample, maxK, auditor.options.EFSearch,
		)
		if err != nil {
			return nil, fmt.Errorf("ANN recall sample %s: %w", sample.ID, err)
		}
		exact, exactLatency, err := auditor.store.SearchExact(ctx, sample, maxK)
		if err != nil {
			return nil, fmt.Errorf("exact recall sample %s: %w", sample.ID, err)
		}
		key := groupKey{domainID: sample.DomainID, documentType: sample.DocumentType}
		group := aggregates[key]
		if group == nil {
			group = &aggregate{recallSums: map[int]float64{}}
			aggregates[key] = group
		}
		group.samples++
		group.annLatency = append(group.annLatency, annLatency)
		group.exactLatency = append(group.exactLatency, exactLatency)
		for _, k := range auditor.options.Ks {
			recall, err := RecallAtK(ann, exact, k)
			if err != nil {
				return nil, err
			}
			group.recallSums[k] += recall
		}
	}

	results := make([]RecallAuditResult, 0, len(aggregates)*len(auditor.options.Ks))
	for key, group := range aggregates {
		for _, k := range auditor.options.Ks {
			recall := group.recallSums[k] / float64(group.samples)
			result := RecallAuditResult{
				TenantID: tenantID, DomainID: key.domainID, RunAt: runAt,
				DocumentType: key.documentType, K: k, SampleSize: group.samples,
				Recall: recall, P95LatencyANN: percentile95(group.annLatency),
				P95LatencyExact:    percentile95(group.exactLatency),
				EmbeddingModel:     auditor.options.EmbeddingModel,
				EmbeddingDimension: auditor.options.EmbeddingDimension,
				EFSearch:           auditor.options.EFSearch, Threshold: auditor.options.Threshold,
				BelowThreshold: recall < auditor.options.Threshold,
			}
			if err := result.Validate(); err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}
	sort.Slice(results, func(left, right int) bool {
		if results[left].DomainID != results[right].DomainID {
			return results[left].DomainID < results[right].DomainID
		}
		if results[left].DocumentType != results[right].DocumentType {
			return results[left].DocumentType < results[right].DocumentType
		}
		return results[left].K < results[right].K
	})
	if err := auditor.store.SaveRecallAudits(ctx, tenantID, results); err != nil {
		return nil, err
	}
	if err := auditor.store.PurgeSamplesBefore(
		ctx, tenantID, runAt.Add(-auditor.options.SampleRetention),
	); err != nil {
		return nil, err
	}
	if auditor.alert != nil {
		for _, result := range results {
			if result.BelowThreshold {
				auditor.alert(ctx, result)
			}
		}
	}
	return results, nil
}

func RecallAtK(ann, exact []askdata.ID, k int) (float64, error) {
	if !validRecallK(k) {
		return 0, ErrInvalidRecallAudit
	}
	exactSet, err := topKSet(exact, k)
	if err != nil {
		return 0, err
	}
	annSet, err := topKSet(ann, k)
	if err != nil {
		return 0, err
	}
	intersection := 0
	for id := range annSet {
		if _, present := exactSet[id]; present {
			intersection++
		}
	}
	return float64(intersection) / float64(k), nil
}

func topKSet(ids []askdata.ID, k int) (map[askdata.ID]struct{}, error) {
	result := make(map[askdata.ID]struct{}, min(k, len(ids)))
	for index, id := range ids {
		if index >= k {
			break
		}
		if err := id.Validate(); err != nil {
			return nil, ErrInvalidRecallAudit
		}
		if _, duplicate := result[id]; duplicate {
			return nil, ErrInvalidRecallAudit
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func validRecallK(k int) bool { return k == 10 || k == 20 || k == 30 }

func percentile95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
