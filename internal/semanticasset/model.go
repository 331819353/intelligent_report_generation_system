// Package semanticasset owns the tenant-scoped common-term mapping dictionary.
// The common term is the only embedding input; mapping values and knowledge
// types remain deterministic resolution output.
package semanticasset

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
	MaxImportItems   = 1000
	VectorDimensions = 2560
	MaxBatchSize     = 32
)

var (
	ErrInvalidRequest  = errors.New("semantic asset request is invalid")
	ErrNotFound        = errors.New("semantic asset was not found")
	ErrConflict        = errors.New("semantic asset changed or conflicts")
	ErrReleaseNotReady = errors.New("semantic release projections are not ready")
)

type Page struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type Filter struct {
	Page
	Query           string
	KnowledgeType   string
	Status          string
	EmbeddingStatus string
}

type CatalogFilter struct {
	Page
	Query      string
	ObjectType string
	Status     string
	Ready      string
}

// CatalogObject is the common governance projection consumed by the asset
// control plane and Question Orchestrator. It does not replace the native
// metric/dimension contracts; it gives every object one lifecycle/readiness
// shape so the UI no longer reconciles unrelated APIs.
type CatalogObject struct {
	ObjectType        string    `json:"objectType"`
	ID                string    `json:"id"`
	Code              string    `json:"code"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	DomainID          string    `json:"domainId,omitempty"`
	SharingScope      string    `json:"sharingScope,omitempty"`
	Status            string    `json:"status"`
	Certification     string    `json:"certification"`
	Version           int64     `json:"version"`
	ContentHash       string    `json:"contentHash,omitempty"`
	OwnerID           string    `json:"ownerId,omitempty"`
	Sensitivity       string    `json:"sensitivity"`
	ExecutionEligible bool      `json:"executionEligible"`
	ReadinessCode     string    `json:"readinessCode"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type CatalogView struct {
	SemanticVersion string           `json:"semanticVersion,omitempty"`
	Readiness       CatalogReadiness `json:"readiness"`
	Items           []CatalogObject  `json:"items"`
	Total           int              `json:"total"`
	Limit           int              `json:"limit"`
	Offset          int              `json:"offset"`
}

type Asset struct {
	ID                 string     `json:"id"`
	CommonTerm         string     `json:"commonTerm"`
	MappingValue       string     `json:"mappingValue"`
	KnowledgeType      string     `json:"knowledgeType"`
	DomainID           string     `json:"domainId"`
	SharingScope       string     `json:"sharingScope"`
	Status             string     `json:"status"`
	Version            int64      `json:"version"`
	EmbeddingStatus    string     `json:"embeddingStatus"`
	EmbeddingModel     string     `json:"embeddingModel,omitempty"`
	EmbeddingErrorCode string     `json:"embeddingErrorCode,omitempty"`
	EmbeddedAt         *time.Time `json:"embeddedAt,omitempty"`
	CreatedBy          string     `json:"createdBy"`
	UpdatedBy          string     `json:"updatedBy"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type UpsertInput struct {
	CommonTerm    string `json:"commonTerm"`
	MappingValue  string `json:"mappingValue"`
	KnowledgeType string `json:"knowledgeType"`
}

type UpdateInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	CommonTerm      string `json:"commonTerm"`
	MappingValue    string `json:"mappingValue"`
	KnowledgeType   string `json:"knowledgeType"`
}

type DeprecateInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type ImportInput struct {
	Items []UpsertInput `json:"items"`
}

type ImportResult struct {
	Inserted  int `json:"inserted"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Total     int `json:"total"`
}

type ParsingRule struct {
	ID            string    `json:"id"`
	RuleType      string    `json:"ruleType"`
	Pattern       string    `json:"pattern"`
	MatchMode     string    `json:"matchMode"`
	Action        string    `json:"action"`
	OutputName    string    `json:"outputName,omitempty"`
	OutputCode    string    `json:"outputCode,omitempty"`
	MinimumLength int       `json:"minimumLength"`
	MaximumLength int       `json:"maximumLength"`
	Priority      int       `json:"priority"`
	Scope         string    `json:"scope"`
	Status        string    `json:"status"`
	Version       int64     `json:"version"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	UpdatedBy     string    `json:"updatedBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ParsingRuleFilter struct {
	Page
	Query    string
	RuleType string
	Status   string
}

type ParsingRuleInput struct {
	RuleType      string `json:"ruleType"`
	Pattern       string `json:"pattern"`
	MatchMode     string `json:"matchMode"`
	Action        string `json:"action"`
	OutputName    string `json:"outputName,omitempty"`
	OutputCode    string `json:"outputCode,omitempty"`
	MinimumLength int    `json:"minimumLength"`
	MaximumLength int    `json:"maximumLength"`
	Priority      int    `json:"priority"`
}

type ParsingRuleUpdateInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	ParsingRuleInput
}

type Store interface {
	Readiness(context.Context, string) (ReadinessSnapshot, error)
	Catalog(context.Context, string, CatalogFilter) ([]CatalogObject, int, ReadinessSnapshot, error)
	List(context.Context, string, Filter) ([]Asset, int, error)
	ListKnowledgeTypes(context.Context, string) ([]string, error)
	Create(context.Context, string, string, UpsertInput) (Asset, error)
	Update(context.Context, string, string, string, UpdateInput) (Asset, error)
	Deprecate(context.Context, string, string, string, int64) (Asset, error)
	Import(context.Context, string, string, []UpsertInput) (ImportResult, error)
	ListParsingRules(
		context.Context, string, ParsingRuleFilter,
	) ([]ParsingRule, int, error)
	CreateParsingRule(
		context.Context, string, string, ParsingRuleInput,
	) (ParsingRule, error)
	UpdateParsingRule(
		context.Context, string, string, string, ParsingRuleUpdateInput,
	) (ParsingRule, error)
	DeprecateParsingRule(
		context.Context, string, string, string, int64,
	) (ParsingRule, error)
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

// Readiness returns the one authoritative, transactionally consistent view of
// the semantic contracts that are allowed to enter the question runtime. The
// frontend must render these checks rather than reconstructing readiness from
// independently paged management APIs.
func (service *Service) Readiness(
	ctx context.Context,
	tenantID string,
) (CatalogReadiness, error) {
	if service == nil || service.store == nil || !validUUID(tenantID) {
		return CatalogReadiness{}, ErrInvalidRequest
	}
	snapshot, err := service.store.Readiness(ctx, tenantID)
	if err != nil {
		return CatalogReadiness{}, err
	}
	return evaluateCatalogReadiness(snapshot, time.Now().UTC()), nil
}

func (service *Service) Catalog(
	ctx context.Context,
	tenantID string,
	filter CatalogFilter,
) (CatalogView, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	filter.ObjectType = strings.ToUpper(strings.TrimSpace(filter.ObjectType))
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	filter.Ready = strings.ToUpper(strings.TrimSpace(filter.Ready))
	if service == nil || service.store == nil || !validUUID(tenantID) ||
		!normalizePage(&filter.Page) || !validOptionalText(filter.Query, 256) ||
		!validOptionalValue(filter.ObjectType, "METRIC", "DIMENSION", "TERM", "PARSING_RULE") ||
		!validOptionalText(filter.Status, 64) ||
		!validOptionalValue(filter.Ready, "READY", "NOT_READY") {
		return CatalogView{}, ErrInvalidRequest
	}
	items, total, snapshot, err := service.store.Catalog(ctx, tenantID, filter)
	if err != nil {
		return CatalogView{}, err
	}
	readiness := evaluateCatalogReadiness(snapshot, time.Now().UTC())
	return CatalogView{
		SemanticVersion: readiness.SemanticVersion,
		Readiness:       readiness, Items: items, Total: total,
		Limit: filter.Limit, Offset: filter.Offset,
	}, nil
}

func (service *Service) List(
	ctx context.Context,
	tenantID string,
	filter Filter,
) ([]Asset, int, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	filter.KnowledgeType = strings.TrimSpace(filter.KnowledgeType)
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	filter.EmbeddingStatus = strings.ToUpper(strings.TrimSpace(filter.EmbeddingStatus))
	if service == nil || service.store == nil || !validUUID(tenantID) ||
		!normalizePage(&filter.Page) || !validOptionalText(filter.Query, 256) ||
		!validOptionalText(filter.KnowledgeType, 128) ||
		!validOptionalValue(filter.Status, "ACTIVE", "DEPRECATED") ||
		!validOptionalValue(
			filter.EmbeddingStatus,
			"PENDING", "SUCCEEDED", "FAILED", "SKIPPED",
		) {
		return nil, 0, ErrInvalidRequest
	}
	return service.store.List(ctx, tenantID, filter)
}

func (service *Service) ListKnowledgeTypes(
	ctx context.Context,
	tenantID string,
) ([]string, error) {
	if service == nil || service.store == nil || !validUUID(tenantID) {
		return nil, ErrInvalidRequest
	}
	return service.store.ListKnowledgeTypes(ctx, tenantID)
}

func (service *Service) Create(
	ctx context.Context,
	tenantID string,
	actorID string,
	input UpsertInput,
) (Asset, error) {
	input = normalizeInput(input)
	if service == nil || service.store == nil ||
		!validActor(tenantID, actorID) || !validInput(input) {
		return Asset{}, ErrInvalidRequest
	}
	return service.store.Create(ctx, tenantID, actorID, input)
}

func (service *Service) Update(
	ctx context.Context,
	tenantID string,
	actorID string,
	id string,
	input UpdateInput,
) (Asset, error) {
	normalized := normalizeInput(UpsertInput{
		CommonTerm: input.CommonTerm, MappingValue: input.MappingValue,
		KnowledgeType: input.KnowledgeType,
	})
	input.CommonTerm = normalized.CommonTerm
	input.MappingValue = normalized.MappingValue
	input.KnowledgeType = normalized.KnowledgeType
	if service == nil || service.store == nil || !validActor(tenantID, actorID) ||
		!validUUID(id) || input.ExpectedVersion < 1 || !validInput(normalized) {
		return Asset{}, ErrInvalidRequest
	}
	return service.store.Update(ctx, tenantID, actorID, id, input)
}

func (service *Service) Deprecate(
	ctx context.Context,
	tenantID string,
	actorID string,
	id string,
	expectedVersion int64,
) (Asset, error) {
	if service == nil || service.store == nil || !validActor(tenantID, actorID) ||
		!validUUID(id) || expectedVersion < 1 {
		return Asset{}, ErrInvalidRequest
	}
	return service.store.Deprecate(
		ctx, tenantID, actorID, id, expectedVersion,
	)
}

func (service *Service) Import(
	ctx context.Context,
	tenantID string,
	actorID string,
	input ImportInput,
) (ImportResult, error) {
	if service == nil || service.store == nil || !validActor(tenantID, actorID) ||
		len(input.Items) < 1 || len(input.Items) > MaxImportItems {
		return ImportResult{}, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(input.Items))
	for index := range input.Items {
		input.Items[index] = normalizeInput(input.Items[index])
		if !validInput(input.Items[index]) {
			return ImportResult{}, ErrInvalidRequest
		}
		identity := strings.ToLower(
			input.Items[index].KnowledgeType + "\x00" +
				input.Items[index].CommonTerm,
		)
		if _, exists := seen[identity]; exists {
			return ImportResult{}, ErrConflict
		}
		seen[identity] = struct{}{}
	}
	return service.store.Import(ctx, tenantID, actorID, input.Items)
}

func normalizeInput(input UpsertInput) UpsertInput {
	input.CommonTerm = strings.TrimSpace(input.CommonTerm)
	input.MappingValue = strings.TrimSpace(input.MappingValue)
	input.KnowledgeType = strings.TrimSpace(input.KnowledgeType)
	return input
}

func validInput(input UpsertInput) bool {
	return validText(input.CommonTerm, 1, 256) &&
		validText(input.MappingValue, 1, 1024) &&
		validText(input.KnowledgeType, 1, 128)
}

func normalizePage(page *Page) bool {
	if page.Limit == 0 {
		page.Limit = DefaultPageLimit
	}
	return page.Limit >= 1 && page.Limit <= MaxPageLimit && page.Offset >= 0
}

func validActor(tenantID, actorID string) bool {
	return validUUID(tenantID) && validUUID(actorID)
}

func validUUID(value string) bool {
	_, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil
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

func validOptionalValue(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
