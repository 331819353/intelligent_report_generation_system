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
	ErrInvalidRequest = errors.New("semantic asset request is invalid")
	ErrNotFound       = errors.New("semantic asset was not found")
	ErrConflict       = errors.New("semantic asset changed or conflicts")
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

type Asset struct {
	ID                 string     `json:"id"`
	CommonTerm         string     `json:"commonTerm"`
	MappingValue       string     `json:"mappingValue"`
	KnowledgeType      string     `json:"knowledgeType"`
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

type Store interface {
	List(context.Context, string, Filter) ([]Asset, int, error)
	ListKnowledgeTypes(context.Context, string) ([]string, error)
	Create(context.Context, string, string, UpsertInput) (Asset, error)
	Update(context.Context, string, string, string, UpdateInput) (Asset, error)
	Deprecate(context.Context, string, string, string, int64) (Asset, error)
	Import(context.Context, string, string, []UpsertInput) (ImportResult, error)
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

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
