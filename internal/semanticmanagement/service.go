package semanticmanagement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

var languageCodePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

var tagCategories = values(
	"BUSINESS_DOMAIN", "BUSINESS_ENTITY", "TABLE_FUNCTION", "USAGE_SCOPE",
	"DATA_GRAIN", "JOIN_ROLE", "SENSITIVITY", "FREEFORM",
)
var tagGovernance = values("CONTROLLED", "FREEFORM")
var writableTagStatuses = values("DRAFT", "ACTIVE")
var aliasTypes = values("BUSINESS", "ABBREVIATION", "LEGACY", "LLM", "USER")
var assetTypes = values("DATASET_VERSION", "DATASET_FIELD", "DIMENSION", "DIMENSION_MEMBER", "METRIC_VERSION")
var bindingOrigins = values("USER", "LLM", "RULE", "IMPORT")
var bindingStatuses = values("SUGGESTED", "APPROVED", "REJECTED")

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) ListTags(ctx context.Context, tenantID string, filter TagFilter) ([]Tag, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Category = strings.ToUpper(strings.TrimSpace(filter.Category))
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if !validOptionalText(filter.Query, 256) || !validOptionalEnum(filter.Category, tagCategories) ||
		!validOptionalEnum(filter.Status, values("DRAFT", "ACTIVE", "DEPRECATED")) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListTags(ctx, tenantID, filter)
}

func (s *Service) CreateTag(ctx context.Context, tenantID, actorID string, input CreateTagInput) (Tag, error) {
	input = normalizeTagCreate(input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validTag(input, "") {
		return Tag{}, ErrInvalidRequest
	}
	return s.store.CreateTag(ctx, tenantID, actorID, input)
}

func (s *Service) UpdateTag(ctx context.Context, tenantID, actorID, id string, input UpdateTagInput) (Tag, error) {
	input = normalizeTagUpdate(input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		input.ExpectedVersion < 1 || !validTag(CreateTagInput{
		ParentTagID: input.ParentTagID, Code: input.Code, Name: input.Name,
		Description: input.Description, Category: input.Category,
		Governance: input.Governance, Status: input.Status,
	}, id) {
		return Tag{}, ErrInvalidRequest
	}
	return s.store.UpdateTag(ctx, tenantID, actorID, id, input)
}

func (s *Service) DeprecateTag(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) (Tag, error) {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) || expectedVersion < 1 {
		return Tag{}, ErrInvalidRequest
	}
	return s.store.DeprecateTag(ctx, tenantID, actorID, id, expectedVersion)
}

func (s *Service) ListTagAliases(ctx context.Context, tenantID string, filter AliasFilter) ([]TagAlias, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.TagID = strings.TrimSpace(filter.TagID)
	filter.Query = strings.TrimSpace(filter.Query)
	filter.AliasType = strings.ToUpper(strings.TrimSpace(filter.AliasType))
	if (filter.TagID != "" && !validUUID(filter.TagID)) || !validOptionalText(filter.Query, 256) ||
		!validOptionalEnum(filter.AliasType, aliasTypes) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListTagAliases(ctx, tenantID, filter)
}

func (s *Service) CreateTagAlias(ctx context.Context, tenantID, actorID string, input CreateTagAliasInput) (TagAlias, error) {
	input = normalizeAliasCreate(input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validAlias(input) {
		return TagAlias{}, ErrInvalidRequest
	}
	return s.store.CreateTagAlias(ctx, tenantID, actorID, input)
}

func (s *Service) UpdateTagAlias(ctx context.Context, tenantID, actorID, id string, input UpdateTagAliasInput) (TagAlias, error) {
	input.TagID = strings.TrimSpace(input.TagID)
	input.Alias = strings.TrimSpace(input.Alias)
	input.AliasType = strings.ToUpper(strings.TrimSpace(input.AliasType))
	input.LanguageCode = strings.TrimSpace(input.LanguageCode)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		!validRecordVersion(input.ExpectedRecordVersion) || !validAlias(CreateTagAliasInput{
		TagID: input.TagID, Alias: input.Alias, AliasType: input.AliasType,
		LanguageCode: input.LanguageCode,
	}) {
		return TagAlias{}, ErrInvalidRequest
	}
	return s.store.UpdateTagAlias(ctx, tenantID, actorID, id, input)
}

func (s *Service) DeleteTagAlias(ctx context.Context, tenantID, actorID, id, expectedRecordVersion string) error {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		!validRecordVersion(expectedRecordVersion) {
		return ErrInvalidRequest
	}
	return s.store.DeleteTagAlias(ctx, tenantID, actorID, id, expectedRecordVersion)
}

func (s *Service) ListAssetTagBindings(ctx context.Context, tenantID string, filter BindingFilter) ([]AssetTagBinding, int, error) {
	if s == nil || s.store == nil || !validTenant(tenantID) || !normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.TagID = strings.TrimSpace(filter.TagID)
	filter.AssetType = strings.ToUpper(strings.TrimSpace(filter.AssetType))
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	if (filter.TagID != "" && !validUUID(filter.TagID)) ||
		!validOptionalEnum(filter.AssetType, assetTypes) ||
		!validOptionalEnum(filter.Status, bindingStatuses) {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListAssetTagBindings(ctx, tenantID, filter)
}

func (s *Service) CreateAssetTagBinding(ctx context.Context, tenantID, actorID string, input CreateAssetTagBindingInput) (AssetTagBinding, error) {
	normalizeBindingCreate(&input)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validBinding(input) {
		return AssetTagBinding{}, ErrInvalidRequest
	}
	return s.store.CreateAssetTagBinding(ctx, tenantID, actorID, input)
}

func (s *Service) UpdateAssetTagBinding(ctx context.Context, tenantID, actorID, id string, input UpdateAssetTagBindingInput) (AssetTagBinding, error) {
	input.ExpectedRecordVersion = strings.TrimSpace(input.ExpectedRecordVersion)
	input.Origin = strings.ToUpper(strings.TrimSpace(input.Origin))
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	input.Evidence = normalizeEvidence(input.Evidence)
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		!validRecordVersion(input.ExpectedRecordVersion) ||
		!has(bindingOrigins, input.Origin) || !has(bindingStatuses, input.Status) ||
		!validConfidence(input.Confidence) || !validEvidence(input.Evidence) {
		return AssetTagBinding{}, ErrInvalidRequest
	}
	return s.store.UpdateAssetTagBinding(ctx, tenantID, actorID, id, input)
}

func (s *Service) DeleteAssetTagBinding(ctx context.Context, tenantID, actorID, id, expectedRecordVersion string) error {
	if s == nil || s.store == nil || !validActor(tenantID, actorID) || !validUUID(id) ||
		!validRecordVersion(expectedRecordVersion) {
		return ErrInvalidRequest
	}
	return s.store.DeleteAssetTagBinding(ctx, tenantID, actorID, id, expectedRecordVersion)
}

func normalizeTagCreate(input CreateTagInput) CreateTagInput {
	input.ParentTagID = strings.TrimSpace(input.ParentTagID)
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Category = strings.ToUpper(strings.TrimSpace(input.Category))
	input.Governance = strings.ToUpper(strings.TrimSpace(input.Governance))
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	if input.Governance == "" {
		input.Governance = "CONTROLLED"
	}
	if input.Status == "" {
		input.Status = "DRAFT"
	}
	return input
}

func normalizeTagUpdate(input UpdateTagInput) UpdateTagInput {
	normalized := normalizeTagCreate(CreateTagInput{
		ParentTagID: input.ParentTagID, Code: input.Code, Name: input.Name,
		Description: input.Description, Category: input.Category,
		Governance: input.Governance, Status: input.Status,
	})
	input.ParentTagID, input.Code, input.Name = normalized.ParentTagID, normalized.Code, normalized.Name
	input.Description, input.Category = normalized.Description, normalized.Category
	input.Governance, input.Status = normalized.Governance, normalized.Status
	return input
}

func validTag(input CreateTagInput, id string) bool {
	return (input.ParentTagID == "" || validUUID(input.ParentTagID)) &&
		(id == "" || input.ParentTagID != id) &&
		validText(input.Code, 1, 128) && validText(input.Name, 1, 256) &&
		validOptionalText(input.Description, 4096) && has(tagCategories, input.Category) &&
		has(tagGovernance, input.Governance) && has(writableTagStatuses, input.Status)
}

func normalizeAliasCreate(input CreateTagAliasInput) CreateTagAliasInput {
	input.TagID = strings.TrimSpace(input.TagID)
	input.Alias = strings.TrimSpace(input.Alias)
	input.AliasType = strings.ToUpper(strings.TrimSpace(input.AliasType))
	input.LanguageCode = strings.TrimSpace(input.LanguageCode)
	if input.AliasType == "" {
		input.AliasType = "BUSINESS"
	}
	if input.LanguageCode == "" {
		input.LanguageCode = "zh-CN"
	}
	return input
}

func validAlias(input CreateTagAliasInput) bool {
	return validUUID(input.TagID) && validText(input.Alias, 1, 256) &&
		has(aliasTypes, input.AliasType) && languageCodePattern.MatchString(input.LanguageCode)
}

func normalizeBindingCreate(input *CreateAssetTagBindingInput) {
	input.TagID = strings.TrimSpace(input.TagID)
	input.AssetType = strings.ToUpper(strings.TrimSpace(input.AssetType))
	input.DatasetID = strings.TrimSpace(input.DatasetID)
	input.DatasetVersionID = strings.TrimSpace(input.DatasetVersionID)
	input.DatasetFieldID = strings.TrimSpace(input.DatasetFieldID)
	input.DimensionID = strings.TrimSpace(input.DimensionID)
	input.DimensionMemberID = strings.TrimSpace(input.DimensionMemberID)
	input.MetricID = strings.TrimSpace(input.MetricID)
	input.MetricVersionID = strings.TrimSpace(input.MetricVersionID)
	input.MetricDatasetVersionID = strings.TrimSpace(input.MetricDatasetVersionID)
	input.Origin = strings.ToUpper(strings.TrimSpace(input.Origin))
	input.Status = strings.ToUpper(strings.TrimSpace(input.Status))
	input.Evidence = normalizeEvidence(input.Evidence)
	if input.Origin == "" {
		input.Origin = "USER"
	}
	if input.Status == "" {
		input.Status = "SUGGESTED"
	}
}

func validBinding(input CreateAssetTagBindingInput) bool {
	if !validUUID(input.TagID) || !has(assetTypes, input.AssetType) ||
		!has(bindingOrigins, input.Origin) || !has(bindingStatuses, input.Status) ||
		!validConfidence(input.Confidence) || !validEvidence(input.Evidence) {
		return false
	}
	uuidFields := []string{
		input.DatasetID, input.DatasetVersionID, input.DimensionID, input.DimensionMemberID,
		input.MetricID, input.MetricVersionID, input.MetricDatasetVersionID,
	}
	for _, value := range uuidFields {
		if value != "" && !validUUID(value) {
			return false
		}
	}
	if input.DatasetFieldID != "" && !validText(input.DatasetFieldID, 1, 256) {
		return false
	}
	switch input.AssetType {
	case "DATASET_VERSION":
		return input.DatasetID != "" && input.DatasetVersionID != "" &&
			empty(input.DatasetFieldID, input.DimensionID, input.DimensionMemberID, input.MetricID, input.MetricVersionID, input.MetricDatasetVersionID)
	case "DATASET_FIELD":
		return input.DatasetID != "" && input.DatasetVersionID != "" && input.DatasetFieldID != "" &&
			empty(input.DimensionID, input.DimensionMemberID, input.MetricID, input.MetricVersionID, input.MetricDatasetVersionID)
	case "DIMENSION":
		return input.DimensionID != "" &&
			empty(input.DatasetID, input.DatasetVersionID, input.DatasetFieldID, input.DimensionMemberID, input.MetricID, input.MetricVersionID, input.MetricDatasetVersionID)
	case "DIMENSION_MEMBER":
		return input.DimensionID != "" && input.DimensionMemberID != "" &&
			empty(input.DatasetID, input.DatasetVersionID, input.DatasetFieldID, input.MetricID, input.MetricVersionID, input.MetricDatasetVersionID)
	case "METRIC_VERSION":
		return input.MetricID != "" && input.MetricVersionID != "" && input.MetricDatasetVersionID != "" &&
			empty(input.DatasetID, input.DatasetVersionID, input.DatasetFieldID, input.DimensionID, input.DimensionMemberID)
	default:
		return false
	}
}

func normalizePage(page *Page) bool {
	if page.Limit == 0 {
		page.Limit = defaultPageLimit
	}
	return page.Limit >= 1 && page.Limit <= maxPageLimit && page.Offset >= 0
}

func validActor(tenantID, actorID string) bool { return validTenant(tenantID) && validUUID(actorID) }
func validTenant(tenantID string) bool         { return validUUID(tenantID) }

func validUUID(value string) bool {
	_, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil
}

func validRecordVersion(value string) bool {
	value = strings.TrimSpace(value)
	_, err := strconv.ParseUint(value, 10, 32)
	return value != "" && err == nil
}

func validText(value string, minimum, maximum int) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, 1, maximum)
}

func validOptionalEnum(value string, allowed map[string]struct{}) bool {
	return value == "" || has(allowed, value)
}

func validConfidence(value *float64) bool {
	return value == nil || (*value >= 0 && *value <= 1)
}

func normalizeEvidence(value json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func validEvidence(value json.RawMessage) bool {
	if len(value) > 65536 {
		return false
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func values(items ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		result[item] = struct{}{}
	}
	return result
}

func has(items map[string]struct{}, value string) bool {
	_, exists := items[value]
	return exists
}

func empty(values ...string) bool {
	for _, value := range values {
		if value != "" {
			return false
		}
	}
	return true
}
