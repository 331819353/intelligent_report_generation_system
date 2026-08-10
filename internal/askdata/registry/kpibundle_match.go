package registry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	DefaultKPIBundlePatternWeight = 0.65
	DefaultKPIBundleMetricWeight  = 0.35
	DefaultKPIBundleMinScore      = 0.25
	DefaultKPIBundleMinMargin     = 0.10
	MaxKPIBundleCandidates        = 8
)

var ErrInvalidKPIBundleMatch = errors.New("invalid governed KPI bundle match")

type KPIBundleMatchInput struct {
	Question         string       `json:"question"`
	MetricMentions   []string     `json:"metricMentions"`
	MetricVersionIDs []askdata.ID `json:"metricVersionIds"`
}

type KPIBundleMatchDocument struct {
	Bundle       KPIBundle
	MetricLabels map[string][]string
}

type KPIBundleMatchSnapshot struct {
	TenantID askdata.ID
	DomainID askdata.ID
	Release  askdata.ReleaseRef
	Bundles  []KPIBundleMatchDocument
}

type KPIBundleMatchLoader interface {
	LoadKPIBundleSnapshot(context.Context, askdata.PolicyScope, askdata.ID) (KPIBundleMatchSnapshot, error)
}

type KPIBundleMatchConfig struct {
	PatternWeight float64
	MetricWeight  float64
	MinScore      float64
	MinMargin     float64
}

func DefaultKPIBundleMatchConfig() KPIBundleMatchConfig {
	return KPIBundleMatchConfig{
		PatternWeight: DefaultKPIBundlePatternWeight,
		MetricWeight:  DefaultKPIBundleMetricWeight,
		MinScore:      DefaultKPIBundleMinScore,
		MinMargin:     DefaultKPIBundleMinMargin,
	}
}

type KPIBundleCandidate struct {
	KPIBundleID       askdata.ID `json:"kpiBundleId"`
	BundleVersionID   askdata.ID `json:"bundleVersionId"`
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	Score             float64    `json:"score"`
	PatternScore      float64    `json:"patternScore"`
	MetricCoverage    float64    `json:"metricCoverage"`
	MatchedPatterns   []string   `json:"matchedPatterns"`
	MatchedMetricIDs  []string   `json:"matchedMetricVersionIds"`
	DefaultTime       string     `json:"defaultTimeExpression"`
	ItemCount         int        `json:"itemCount"`
	ReleaseEvidenceID askdata.ID `json:"releaseEvidenceId"`
}

type KPIBundleMatchResult struct {
	Candidates            []KPIBundleCandidate `json:"candidates"`
	Selected              *KPIBundleCandidate  `json:"selected,omitempty"`
	ClarificationRequired bool                 `json:"clarificationRequired"`
}

type KPIBundleMatcher struct {
	loader KPIBundleMatchLoader
	config KPIBundleMatchConfig
}

func NewKPIBundleMatcher(loader KPIBundleMatchLoader, config KPIBundleMatchConfig) (*KPIBundleMatcher, error) {
	if loader == nil || !validKPIBundleMatchConfig(config) {
		return nil, ErrInvalidKPIBundleMatch
	}
	return &KPIBundleMatcher{loader: loader, config: config}, nil
}

func validKPIBundleMatchConfig(config KPIBundleMatchConfig) bool {
	values := []float64{config.PatternWeight, config.MetricWeight, config.MinScore, config.MinMargin}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return false
		}
	}
	return config.PatternWeight+config.MetricWeight > 0
}

func (matcher *KPIBundleMatcher) MatchBundle(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	input KPIBundleMatchInput,
) (KPIBundleMatchResult, error) {
	if matcher == nil || matcher.loader == nil || ctx == nil || scope.Validate() != nil ||
		domainID.Validate() != nil || !scopeContainsKPIBundleDomain(scope, domainID) {
		return KPIBundleMatchResult{}, ErrInvalidKPIBundleMatch
	}
	normalizedQuestion, err := normalizeKPIBundleMatchText(input.Question)
	if err != nil || len(input.MetricMentions) > 16 || len(input.MetricVersionIDs) > 16 {
		return KPIBundleMatchResult{}, ErrInvalidKPIBundleMatch
	}
	normalizedMentions := make([]string, len(input.MetricMentions))
	for index, mention := range input.MetricMentions {
		normalizedMentions[index], err = normalizeKPIBundleMatchText(mention)
		if err != nil {
			return KPIBundleMatchResult{}, ErrInvalidKPIBundleMatch
		}
	}
	boundMetrics := map[string]struct{}{}
	for _, metricID := range input.MetricVersionIDs {
		if metricID.Validate() != nil {
			return KPIBundleMatchResult{}, ErrInvalidKPIBundleMatch
		}
		boundMetrics[string(metricID)] = struct{}{}
	}
	snapshot, err := matcher.loader.LoadKPIBundleSnapshot(ctx, scope, domainID)
	if err != nil {
		return KPIBundleMatchResult{}, err
	}
	if snapshot.TenantID != scope.TenantID || snapshot.DomainID != domainID || snapshot.Release != scope.Release {
		return KPIBundleMatchResult{}, ErrInvalidKPIBundleMatch
	}
	result := KPIBundleMatchResult{Candidates: []KPIBundleCandidate{}}
	for _, document := range snapshot.Bundles {
		candidate, matched, err := scoreKPIBundleDocument(
			document, normalizedQuestion, normalizedMentions, boundMetrics, scope.Release.ReleaseID, matcher.config,
		)
		if err != nil {
			return KPIBundleMatchResult{}, err
		}
		if matched {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		if result.Candidates[left].Score != result.Candidates[right].Score {
			return result.Candidates[left].Score > result.Candidates[right].Score
		}
		if result.Candidates[left].Code != result.Candidates[right].Code {
			return result.Candidates[left].Code < result.Candidates[right].Code
		}
		return result.Candidates[left].BundleVersionID < result.Candidates[right].BundleVersionID
	})
	if len(result.Candidates) > MaxKPIBundleCandidates {
		result.Candidates = result.Candidates[:MaxKPIBundleCandidates]
	}
	if len(result.Candidates) == 0 {
		return result, nil
	}
	if len(result.Candidates) > 1 && result.Candidates[0].Score-result.Candidates[1].Score < matcher.config.MinMargin {
		result.ClarificationRequired = true
		return result, nil
	}
	selected := result.Candidates[0]
	result.Selected = &selected
	return result, nil
}

func scoreKPIBundleDocument(
	document KPIBundleMatchDocument,
	question string,
	mentions []string,
	boundMetrics map[string]struct{},
	releaseID askdata.ID,
	config KPIBundleMatchConfig,
) (KPIBundleCandidate, bool, error) {
	bundle := document.Bundle
	if bundle.Status != VersionStatusCertified || bundle.Validate() != nil ||
		KPIBundleContentHash(bundle) != bundle.ContentHash {
		return KPIBundleCandidate{}, false, ErrInvalidKPIBundleMatch
	}
	matchedPatterns := []string{}
	for _, pattern := range bundle.ApplicableQuestionPatterns {
		normalized, err := normalizeKPIBundleMatchText(pattern)
		if err != nil {
			return KPIBundleCandidate{}, false, ErrInvalidKPIBundleMatch
		}
		if strings.Contains(question, normalized) {
			matchedPatterns = append(matchedPatterns, pattern)
		}
	}
	patternScore := 0.0
	if len(matchedPatterns) > 0 {
		patternScore = 1
	}
	sort.Strings(matchedPatterns)
	matchedMetricIDs := []string{}
	matchedItemCount := 0
	for _, item := range bundle.Items {
		labels := document.MetricLabels[item.MetricVersionID]
		if len(labels) == 0 {
			return KPIBundleCandidate{}, false, ErrInvalidKPIBundleMatch
		}
		matched := false
		if _, exists := boundMetrics[item.MetricVersionID]; exists {
			matched = true
		}
		for _, label := range labels {
			normalized, err := normalizeKPIBundleMatchText(label)
			if err != nil {
				return KPIBundleCandidate{}, false, ErrInvalidKPIBundleMatch
			}
			for _, mention := range mentions {
				if mention == normalized || strings.Contains(mention, normalized) || strings.Contains(normalized, mention) {
					matched = true
					break
				}
			}
		}
		if matched {
			matchedItemCount++
			matchedMetricIDs = append(matchedMetricIDs, item.MetricVersionID)
		}
	}
	matchedMetricIDs = uniqueSortedStrings(matchedMetricIDs)
	metricCoverage := float64(matchedItemCount) / float64(len(bundle.Items))
	weightTotal := config.PatternWeight + config.MetricWeight
	score := (config.PatternWeight*patternScore + config.MetricWeight*metricCoverage) / weightTotal
	if score < config.MinScore {
		return KPIBundleCandidate{}, false, nil
	}
	return KPIBundleCandidate{
		KPIBundleID: askdata.ID(bundle.ObjectID), BundleVersionID: askdata.ID(bundle.ID),
		Code: bundle.Code, Name: bundle.Name, Score: score, PatternScore: patternScore,
		MetricCoverage: metricCoverage, MatchedPatterns: matchedPatterns,
		MatchedMetricIDs: matchedMetricIDs, DefaultTime: bundle.DefaultTimeExpression,
		ItemCount: len(bundle.Items), ReleaseEvidenceID: releaseID,
	}, true, nil
}

func uniqueSortedStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func normalizeKPIBundleMatchText(value string) (string, error) {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 4096 {
		return "", ErrInvalidKPIBundleMatch
	}
	value = cases.Fold().String(norm.NFKC.String(value))
	value = strings.Join(strings.FieldsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsPunct(character) || unicode.IsSymbol(character)
	}), "")
	if value == "" {
		return "", ErrInvalidKPIBundleMatch
	}
	return value, nil
}

type PostgresKPIBundleLoader struct{ pool *pgxpool.Pool }

func NewPostgresKPIBundleLoader(pool *pgxpool.Pool) *PostgresKPIBundleLoader {
	return &PostgresKPIBundleLoader{pool: pool}
}

func (loader *PostgresKPIBundleLoader) LoadKPIBundleSnapshot(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
) (KPIBundleMatchSnapshot, error) {
	if loader == nil || loader.pool == nil || ctx == nil || scope.Validate() != nil ||
		domainID.Validate() != nil || !scopeContainsKPIBundleDomain(scope, domainID) {
		return KPIBundleMatchSnapshot{}, ErrInvalidKPIBundleMatch
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(scope.ActorID) || access.DomainID != string(domainID) {
		return KPIBundleMatchSnapshot{}, ErrInvalidKPIBundleMatch
	}
	snapshot := KPIBundleMatchSnapshot{
		TenantID: scope.TenantID, DomainID: domainID, Release: scope.Release,
		Bundles: []KPIBundleMatchDocument{},
	}
	err := database.WithTenantTx(ctx, loader.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		metricLabels, dimensionVersions, err := loadKPIBundleReleaseAssets(ctx, tx, scope, domainID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT
			version.id::text,version.tenant_id::text,version.domain_id::text,
			version.kpi_bundle_id::text,version.version_no,version.status,version.content_hash,
			version.owner_id::text,version.created_at,version.updated_at,identity.code::text,
			identity.name,version.items,version.default_dimension_version_ids::text[],
			version.default_time_expression,version.default_chart_types,version.role_mapping,
			version.applicable_question_patterns
			FROM askdata.releases AS release
			JOIN askdata.release_objects AS object
			  ON object.tenant_id=release.tenant_id AND object.domain_id=release.domain_id
			 AND object.release_id=release.id AND object.object_type='KPI_BUNDLE'
			JOIN askdata.kpi_bundle_versions AS version
			  ON version.tenant_id=object.tenant_id AND version.domain_id=object.domain_id
			 AND version.id=object.object_version_id AND version.content_hash=object.content_hash
			JOIN askdata.kpi_bundles AS identity
			  ON identity.id=version.kpi_bundle_id AND identity.tenant_id=version.tenant_id
			 AND identity.domain_id=version.domain_id
			WHERE release.tenant_id=askdata.current_tenant_id()
			  AND release.id=$1 AND release.content_hash=$2 AND release.domain_id=$3
			  AND release.status IN ('READY','ACTIVE') AND version.status='CERTIFIED'
			ORDER BY identity.code,version.version_no,version.id`,
			scope.Release.ReleaseID, scope.Release.ContentHash, domainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var bundle KPIBundle
			if err := scanKPIBundle(rows, &bundle); err != nil {
				return err
			}
			if err := validateLoadedKPIBundleClosure(bundle, metricLabels, dimensionVersions); err != nil {
				return err
			}
			snapshot.Bundles = append(snapshot.Bundles, KPIBundleMatchDocument{
				Bundle: bundle, MetricLabels: metricLabels,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return KPIBundleMatchSnapshot{}, err
	}
	return snapshot, nil
}

func loadKPIBundleReleaseAssets(
	ctx context.Context,
	tx pgx.Tx,
	scope askdata.PolicyScope,
	domainID askdata.ID,
) (map[string][]string, map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT object.object_type,object.object_version_id::text,
		COALESCE(metric.code::text,''),COALESCE(metric.name,'')
		FROM askdata.releases AS release
		JOIN askdata.release_objects AS object
		  ON object.tenant_id=release.tenant_id AND object.domain_id=release.domain_id
		 AND object.release_id=release.id AND object.object_type IN ('METRIC','DIMENSION')
		LEFT JOIN askdata.metric_versions AS version
		  ON object.object_type='METRIC' AND version.id=object.object_version_id
		 AND version.content_hash=object.content_hash
		LEFT JOIN askdata.metrics AS metric ON metric.id=version.metric_id
		WHERE release.tenant_id=askdata.current_tenant_id()
		  AND release.id=$1 AND release.content_hash=$2 AND release.domain_id=$3
		  AND release.status IN ('READY','ACTIVE')
		ORDER BY object.object_type,object.object_version_id`,
		scope.Release.ReleaseID, scope.Release.ContentHash, domainID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	metricLabels := map[string][]string{}
	dimensionVersions := map[string]struct{}{}
	for rows.Next() {
		var objectType, versionID, code, name string
		if err := rows.Scan(&objectType, &versionID, &code, &name); err != nil {
			return nil, nil, err
		}
		if objectType == "METRIC" && code != "" && name != "" {
			metricLabels[versionID] = []string{code, name}
		}
		if objectType == "DIMENSION" {
			dimensionVersions[versionID] = struct{}{}
		}
	}
	return metricLabels, dimensionVersions, rows.Err()
}

func validateLoadedKPIBundleClosure(
	bundle KPIBundle,
	metricLabels map[string][]string,
	dimensionVersions map[string]struct{},
) error {
	for itemIndex, item := range bundle.Items {
		if len(metricLabels[item.MetricVersionID]) == 0 {
			return fmt.Errorf("%w: release omits KPI bundle metric at items[%d]", ErrInvalidKPIBundleMatch, itemIndex)
		}
		for dimensionIndex, dimensionID := range item.GroupByDimensionVersionIDs {
			if _, exists := dimensionVersions[dimensionID]; !exists {
				return fmt.Errorf("%w: release omits KPI bundle dimension at items[%d].groupByDimensionVersionIds[%d]",
					ErrInvalidKPIBundleMatch, itemIndex, dimensionIndex)
			}
		}
	}
	for index, dimensionID := range bundle.DefaultDimensionVersionIDs {
		if _, exists := dimensionVersions[dimensionID]; !exists {
			return fmt.Errorf("%w: release omits default KPI bundle dimension at index %d", ErrInvalidKPIBundleMatch, index)
		}
	}
	return nil
}

func scopeContainsKPIBundleDomain(scope askdata.PolicyScope, domainID askdata.ID) bool {
	for _, candidate := range scope.DomainIDs {
		if candidate == domainID {
			return true
		}
	}
	return false
}

var _ KPIBundleMatchLoader = (*PostgresKPIBundleLoader)(nil)
