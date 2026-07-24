package semanticmanagement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	defaultRefreshMaxMembers = 100000
	defaultRefreshTimeout    = 60
)

var dimensionTypes = values("STANDARD", "TIME", "GEOGRAPHY", "ORGANIZATION", "PRODUCT", "CUSTOMER", "OTHER")
var memberIndexPolicies = values("FULL", "EXACT_ONLY", "NONE")
var writableDimensionStatuses = values("DRAFT", "PUBLISHED")
var dimensionAliasTypes = values("CODE", "BUSINESS", "ABBREVIATION", "LEGACY", "LLM", "USER")
var compatibilityTypes = values("DIRECT", "BRIDGE", "DERIVED")
var fanoutPolicies = values("SAFE", "DEDUPLICATE", "UNSAFE")
var evidenceSources = values("RULE", "PROFILE", "LLM", "HUMAN")
var compatibilityStatuses = values("PROPOSED", "VERIFIED", "REJECTED")
var refreshStatuses = values("QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "SKIPPED")

type DimensionService struct {
	store  DimensionStore
	survey DimensionSurveyStore
}

func NewDimensionService(store DimensionStore) *DimensionService {
	service := &DimensionService{store: store}
	if survey, ok := store.(DimensionSurveyStore); ok {
		service.survey = survey
	}
	return service
}

func (s *DimensionService) ListDimensions(ctx context.Context, tenantID string, filter DimensionFilter) ([]Dimension, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.Query = strings.TrimSpace(filter.Query)
	filter.DatasetVersionID = strings.TrimSpace(filter.DatasetVersionID)
	filter.DimensionType = strings.ToUpper(strings.TrimSpace(filter.DimensionType))
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if !validOptionalText(filter.Query, 256) ||
		(filter.DatasetVersionID != "" && !validUUID(filter.DatasetVersionID)) ||
		!validOptionalEnum(filter.DimensionType, dimensionTypes) ||
		!validOptionalEnum(filter.Status, values("DRAFT", "PUBLISHED", "DEPRECATED")) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListDimensions(ctx, tenantID, filter)
}

func (s *DimensionService) GetDimension(ctx context.Context, tenantID, id string) (Dimension, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !validUUID(id) {
		return Dimension{}, ErrInvalidRequest
	}
	return s.store.GetDimension(ctx, tenantID, id)
}

func (s *DimensionService) CreateDimension(ctx context.Context, tenantID, actorID string, input CreateDimensionInput) (Dimension, error) {
	input = normalizeDimension(input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validDimension(input) {
		return Dimension{}, ErrInvalidRequest
	}
	prepared, err := prepareDimension(input)
	if err != nil {
		return Dimension{}, err
	}
	return s.store.CreateDimension(ctx, tenantID, actorID, prepared)
}

func (s *DimensionService) UpdateDimension(ctx context.Context, tenantID, actorID, id string, input UpdateDimensionInput) (Dimension, error) {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) || input.ExpectedVersion < 1 {
		return Dimension{}, ErrInvalidRequest
	}
	current, err := s.store.GetDimension(ctx, tenantID, id)
	if err != nil {
		return Dimension{}, err
	}
	normalized := normalizeDimension(CreateDimensionInput{
		DatasetID: current.DatasetID, DatasetVersionID: current.DatasetVersionID,
		FieldID: current.FieldID, Code: input.Code, Name: input.Name,
		Description: input.Description, DimensionType: input.DimensionType,
		MemberIndexPolicy: input.MemberIndexPolicy, HighCardinality: input.HighCardinality,
		Sensitive: input.Sensitive, Status: input.Status,
	})
	if !validDimension(normalized) {
		return Dimension{}, ErrInvalidRequest
	}
	prepared, err := prepareDimension(normalized)
	if err != nil {
		return Dimension{}, err
	}
	return s.store.UpdateDimension(ctx, tenantID, actorID, id, input.ExpectedVersion, prepared)
}

func (s *DimensionService) DeprecateDimension(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) (Dimension, error) {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) || expectedVersion < 1 {
		return Dimension{}, ErrInvalidRequest
	}
	return s.store.DeprecateDimension(ctx, tenantID, actorID, id, expectedVersion)
}

func normalizeDimension(input CreateDimensionInput) CreateDimensionInput {
	input.DatasetID = strings.TrimSpace(input.DatasetID)
	input.DatasetVersionID = strings.TrimSpace(input.DatasetVersionID)
	input.FieldID = strings.TrimSpace(input.FieldID)
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.DimensionType = strings.ToUpper(strings.TrimSpace(input.DimensionType))
	input.MemberIndexPolicy = strings.ToUpper(strings.TrimSpace(input.MemberIndexPolicy))
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	if input.MemberIndexPolicy == "" {
		input.MemberIndexPolicy = "FULL"
	}
	if input.Status == "" {
		input.Status = "DRAFT"
	}
	return input
}

func validDimension(input CreateDimensionInput) bool {
	return validUUID(input.DatasetID) && validUUID(input.DatasetVersionID) &&
		validText(input.FieldID, 1, 256) && validText(input.Code, 1, 128) &&
		validText(input.Name, 1, 256) && validOptionalText(input.Description, 4096) &&
		has(dimensionTypes, input.DimensionType) &&
		has(memberIndexPolicies, input.MemberIndexPolicy) &&
		has(writableDimensionStatuses, input.Status) &&
		(!input.Sensitive || input.MemberIndexPolicy != "FULL") &&
		(!input.HighCardinality || input.MemberIndexPolicy != "FULL")
}

func prepareDimension(input CreateDimensionInput) (PreparedDimension, error) {
	payload, err := json.Marshal(struct {
		DatasetID, DatasetVersionID, FieldID, Code, Name, Description string
		DimensionType, MemberIndexPolicy, Status                      string
		HighCardinality, Sensitive                                    bool
	}{
		input.DatasetID, input.DatasetVersionID, input.FieldID,
		input.Code, input.Name, input.Description, input.DimensionType,
		input.MemberIndexPolicy, input.Status, input.HighCardinality, input.Sensitive,
	})
	if err != nil {
		return PreparedDimension{}, err
	}
	return PreparedDimension{CreateDimensionInput: input, DefinitionHash: hashBytes(payload)}, nil
}

func (s *DimensionService) ListDimensionMembers(ctx context.Context, tenantID, actorID string, filter DimensionMemberFilter) ([]DimensionMember, int, error) {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.DimensionID = strings.TrimSpace(filter.DimensionID)
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if !validUUID(filter.DimensionID) || !validOptionalText(filter.Query, 1024) ||
		!validOptionalEnum(filter.Status, values("ACTIVE", "DEPRECATED")) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListDimensionMembers(ctx, tenantID, actorID, filter)
}

func (s *DimensionService) ListDimensionMemberAliases(ctx context.Context, tenantID, actorID string, filter DimensionMemberAliasFilter) ([]DimensionMemberAlias, int, error) {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.DimensionID = strings.TrimSpace(filter.DimensionID)
	filter.DimensionMemberID = strings.TrimSpace(filter.DimensionMemberID)
	filter.Query = strings.TrimSpace(filter.Query)
	filter.AliasType = strings.ToUpper(strings.TrimSpace(filter.AliasType))
	if (filter.DimensionID != "" && !validUUID(filter.DimensionID)) ||
		(filter.DimensionMemberID != "" && !validUUID(filter.DimensionMemberID)) ||
		!validOptionalText(filter.Query, 1024) ||
		!validOptionalEnum(filter.AliasType, dimensionAliasTypes) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListDimensionMemberAliases(ctx, tenantID, actorID, filter)
}

func (s *DimensionService) CreateDimensionMemberAlias(ctx context.Context, tenantID, actorID string, input CreateDimensionMemberAliasInput) (DimensionMemberAlias, error) {
	normalizeDimensionAliasCreate(&input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validDimensionAlias(input) {
		return DimensionMemberAlias{}, ErrInvalidRequest
	}
	return s.store.CreateDimensionMemberAlias(ctx, tenantID, actorID, input, normalizeSearchValue(input.Alias))
}

func (s *DimensionService) UpdateDimensionMemberAlias(ctx context.Context, tenantID, actorID, id string, input UpdateDimensionMemberAliasInput) (DimensionMemberAlias, error) {
	input.Alias = strings.TrimSpace(input.Alias)
	input.AliasType = strings.ToUpper(strings.TrimSpace(input.AliasType))
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		input.ExpectedVersion < 1 || !validText(input.Alias, 1, 1024) ||
		!has(dimensionAliasTypes, input.AliasType) || !validTimeRange(input.ValidFrom, input.ValidTo) {
		return DimensionMemberAlias{}, ErrInvalidRequest
	}
	normalized := normalizeSearchValue(input.Alias)
	if !validText(normalized, 1, 1024) {
		return DimensionMemberAlias{}, ErrInvalidRequest
	}
	return s.store.UpdateDimensionMemberAlias(ctx, tenantID, actorID, id, input, normalized)
}

func (s *DimensionService) DeleteDimensionMemberAlias(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) error {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) || expectedVersion < 1 {
		return ErrInvalidRequest
	}
	return s.store.DeleteDimensionMemberAlias(ctx, tenantID, actorID, id, expectedVersion)
}

func normalizeDimensionAliasCreate(input *CreateDimensionMemberAliasInput) {
	input.DimensionID = strings.TrimSpace(input.DimensionID)
	input.DimensionMemberID = strings.TrimSpace(input.DimensionMemberID)
	input.Alias = strings.TrimSpace(input.Alias)
	input.AliasType = strings.ToUpper(strings.TrimSpace(input.AliasType))
	if input.AliasType == "" {
		input.AliasType = "BUSINESS"
	}
}

func validDimensionAlias(input CreateDimensionMemberAliasInput) bool {
	normalized := normalizeSearchValue(input.Alias)
	return validUUID(input.DimensionID) && validUUID(input.DimensionMemberID) &&
		validText(input.Alias, 1, 1024) && validText(normalized, 1, 1024) &&
		has(dimensionAliasTypes, input.AliasType) && validTimeRange(input.ValidFrom, input.ValidTo)
}

func validTimeRange(from, to *time.Time) bool {
	return to == nil || (from != nil && to.After(*from))
}

func (s *DimensionService) ListCompatibilities(ctx context.Context, tenantID string, filter CompatibilityFilter) ([]DimensionMetricCompatibility, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.DimensionID = strings.TrimSpace(filter.DimensionID)
	filter.MetricVersionID = strings.TrimSpace(filter.MetricVersionID)
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if (filter.DimensionID != "" && !validUUID(filter.DimensionID)) ||
		(filter.MetricVersionID != "" && !validUUID(filter.MetricVersionID)) ||
		!validOptionalEnum(filter.Status, compatibilityStatuses) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListCompatibilities(ctx, tenantID, filter)
}

func (s *DimensionService) ProposeCompatibility(ctx context.Context, tenantID, actorID string, input ProposeCompatibilityInput) (DimensionMetricCompatibility, error) {
	normalizeCompatibility(&input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validCompatibility(input) {
		return DimensionMetricCompatibility{}, ErrInvalidRequest
	}
	return s.store.ProposeCompatibility(ctx, tenantID, actorID, input)
}

func (s *DimensionService) UpdateCompatibility(ctx context.Context, tenantID, actorID, id string, input UpdateCompatibilityInput) (DimensionMetricCompatibility, error) {
	input.CompatibilityType = strings.ToUpper(strings.TrimSpace(input.CompatibilityType))
	input.FanoutPolicy = strings.ToUpper(strings.TrimSpace(input.FanoutPolicy))
	input.EvidenceSource = strings.ToUpper(strings.TrimSpace(input.EvidenceSource))
	input.JoinPath = normalizeJSONArray(input.JoinPath)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		input.ExpectedVersion < 1 || !has(compatibilityTypes, input.CompatibilityType) ||
		!has(fanoutPolicies, input.FanoutPolicy) || !has(evidenceSources, input.EvidenceSource) ||
		!validConfidence(input.Confidence) || !validSafeJSONArray(input.JoinPath, 262144) {
		return DimensionMetricCompatibility{}, ErrInvalidRequest
	}
	return s.store.UpdateCompatibility(ctx, tenantID, actorID, id, input)
}

func (s *DimensionService) DecideCompatibility(ctx context.Context, tenantID, actorID, id string, expectedVersion int64, decision string) (DimensionMetricCompatibility, error) {
	decision = strings.ToUpper(strings.TrimSpace(decision))
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		expectedVersion < 1 || (decision != "VERIFIED" && decision != "REJECTED") {
		return DimensionMetricCompatibility{}, ErrInvalidRequest
	}
	return s.store.DecideCompatibility(ctx, tenantID, actorID, id, expectedVersion, decision)
}

func normalizeCompatibility(input *ProposeCompatibilityInput) {
	input.DimensionID = strings.TrimSpace(input.DimensionID)
	input.MetricID = strings.TrimSpace(input.MetricID)
	input.MetricVersionID = strings.TrimSpace(input.MetricVersionID)
	input.MetricDatasetVersionID = strings.TrimSpace(input.MetricDatasetVersionID)
	input.CompatibilityType = strings.ToUpper(strings.TrimSpace(input.CompatibilityType))
	input.FanoutPolicy = strings.ToUpper(strings.TrimSpace(input.FanoutPolicy))
	input.EvidenceSource = strings.ToUpper(strings.TrimSpace(input.EvidenceSource))
	input.JoinPath = normalizeJSONArray(input.JoinPath)
}

func validCompatibility(input ProposeCompatibilityInput) bool {
	return validUUID(input.DimensionID) && validUUID(input.MetricID) &&
		validUUID(input.MetricVersionID) && validUUID(input.MetricDatasetVersionID) &&
		has(compatibilityTypes, input.CompatibilityType) && has(fanoutPolicies, input.FanoutPolicy) &&
		has(evidenceSources, input.EvidenceSource) && validConfidence(input.Confidence) &&
		validSafeJSONArray(input.JoinPath, 262144)
}

func normalizeJSONArray(value json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`[]`)
	}
	return value
}

func validSafeJSONArray(value json.RawMessage, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	var decoded []any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return false
	}
	if !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return false
	}
	return safeSemanticJSON(decoded)
}

func safeSemanticJSON(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if !safeSemanticJSON(item) {
				return false
			}
		}
	case map[string]any:
		for key, item := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			switch normalizedKey {
			case "sql", "rawsql", "query", "statement", "password", "secret",
				"credentials", "rows", "samplerows", "rawdata":
				return false
			}
			if !safeSemanticJSON(item) {
				return false
			}
		}
	}
	return true
}

func (s *DimensionService) CreateRefreshJob(
	ctx context.Context,
	tenantID, actorID, dimensionID, idempotencyKey string,
	input CreateRefreshJobInput,
) (RefreshJob, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if input.MaxMembers == 0 {
		input.MaxMembers = defaultRefreshMaxMembers
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultRefreshTimeout
	}
	if s == nil || s.store == nil || !validActor(tenantID, actorID) ||
		!validUUID(dimensionID) || input.ExpectedDimensionVersion < 1 ||
		input.MaxMembers < 1 || input.MaxMembers > 1000000 ||
		input.TimeoutSeconds < 1 || input.TimeoutSeconds > 300 ||
		!validText(idempotencyKey, 1, 128) {
		return RefreshJob{}, false, ErrInvalidRequest
	}
	payload, err := json.Marshal(struct {
		DimensionID              string
		ExpectedDimensionVersion int64
		MaxMembers               int
		TimeoutSeconds           int
	}{
		dimensionID, input.ExpectedDimensionVersion, input.MaxMembers, input.TimeoutSeconds,
	})
	if err != nil {
		return RefreshJob{}, false, err
	}
	prepared := PreparedRefreshJob{
		CreateRefreshJobInput: input, DimensionID: dimensionID,
		RequestHash: hashBytes(payload), IdempotencyKey: hashBytes([]byte(idempotencyKey)),
	}
	return s.store.CreateRefreshJob(ctx, tenantID, actorID, prepared)
}

func (s *DimensionService) ListRefreshJobs(ctx context.Context, tenantID string, filter RefreshJobFilter) ([]RefreshJob, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.DimensionID = strings.TrimSpace(filter.DimensionID)
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if (filter.DimensionID != "" && !validUUID(filter.DimensionID)) ||
		!validOptionalEnum(filter.Status, refreshStatuses) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListRefreshJobs(ctx, tenantID, filter)
}

func (s *DimensionService) SearchMemberMetrics(ctx context.Context, tenantID, actorID, query string, limit int) ([]MemberMetricSearchResult, error) {
	query = normalizeSearchValue(query)
	if limit == 0 {
		limit = 20
	}
	if s == nil || s.store == nil || !validActor(tenantID, actorID) ||
		!validText(query, 1, 1024) || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	return s.store.SearchMemberMetrics(ctx, tenantID, actorID, query, limit)
}

func normalizeSearchValue(value string) string {
	return strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// DimensionRefreshWorker executes one bounded, fully atomic member snapshot.
type DimensionRefreshWorker struct{ store DimensionRefreshStore }

func NewDimensionRefreshWorker(store DimensionRefreshStore) *DimensionRefreshWorker {
	return &DimensionRefreshWorker{store: store}
}

func (w *DimensionRefreshWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListRefreshTenantIDs(ctx)
}

func (w *DimensionRefreshWorker) ProcessNext(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	if w == nil || w.store == nil || !validUUID(tenantID) || !validWorkerName(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	claim, err := w.store.ClaimDimensionRefresh(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(claim.TimeoutSeconds)*time.Second)
	runErr := w.store.RefreshDimensionMembers(runCtx, *claim, workerID)
	cancel()
	if runErr == nil {
		return true, nil
	}
	code := refreshFailureCode(runErr)
	message := "dimension member refresh failed"
	failCtx, failCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer failCancel()
	if failErr := w.store.FailDimensionRefresh(failCtx, *claim, code, message); failErr != nil &&
		!errors.Is(failErr, ErrRefreshLeaseLost) {
		return true, errors.Join(runErr, failErr)
	}
	return true, runErr
}

func refreshFailureCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrRefreshTimeout):
		return "REFRESH_TIMEOUT"
	case errors.Is(err, ErrRefreshCardinality):
		return "CARDINALITY_LIMIT_EXCEEDED"
	case errors.Is(err, ErrRefreshUnsafeView):
		return "PUBLISHED_VIEW_UNTRUSTED"
	case errors.Is(err, ErrRefreshInvalidValue):
		return "MEMBER_VALUE_INVALID"
	case errors.Is(err, ErrRefreshSourceChanged):
		return "REFRESH_SOURCE_CHANGED"
	case errors.Is(err, ErrRefreshLeaseLost):
		return "LEASE_LOST"
	default:
		return "REFRESH_FAILED"
	}
}

func validWorkerName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
