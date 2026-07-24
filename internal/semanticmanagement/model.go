// Package semanticmanagement exposes the governed, tenant-scoped write model
// for tags, aliases and asset bindings. Embeddings are deliberately outside
// this package: database triggers enqueue semantic_change_outbox work.
package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidRequest = errors.New("semantic management request is invalid")
	ErrNotFound       = errors.New("semantic management resource was not found")
	ErrConflict       = errors.New("semantic management resource changed or conflicts")
)

type Page struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type TagFilter struct {
	Page
	Query    string
	Category string
	Status   string
}

type AliasFilter struct {
	Page
	TagID     string
	Query     string
	AliasType string
}

type BindingFilter struct {
	Page
	TagID     string
	AssetType string
	Status    string
}

type Tag struct {
	ID          string    `json:"id"`
	ParentTagID string    `json:"parentTagId,omitempty"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Governance  string    `json:"governance"`
	Status      string    `json:"status"`
	Version     int64     `json:"version"`
	CreatedBy   string    `json:"createdBy"`
	UpdatedBy   string    `json:"updatedBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type TagAlias struct {
	ID           string    `json:"id"`
	TagID        string    `json:"tagId"`
	Alias        string    `json:"alias"`
	AliasType    string    `json:"aliasType"`
	LanguageCode string    `json:"languageCode"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	// recordVersion is an opaque PostgreSQL row token used because the v60
	// alias table intentionally has no mutable business version column.
	RecordVersion string `json:"recordVersion"`
}

type AssetTagBinding struct {
	ID                     string          `json:"id"`
	TagID                  string          `json:"tagId"`
	AssetType              string          `json:"assetType"`
	DatasetID              string          `json:"datasetId,omitempty"`
	DatasetVersionID       string          `json:"datasetVersionId,omitempty"`
	DatasetFieldID         string          `json:"datasetFieldId,omitempty"`
	DimensionID            string          `json:"dimensionId,omitempty"`
	DimensionMemberID      string          `json:"dimensionMemberId,omitempty"`
	MetricID               string          `json:"metricId,omitempty"`
	MetricVersionID        string          `json:"metricVersionId,omitempty"`
	MetricDatasetVersionID string          `json:"metricDatasetVersionId,omitempty"`
	Origin                 string          `json:"origin"`
	Status                 string          `json:"status"`
	Confidence             *float64        `json:"confidence,omitempty"`
	Evidence               json.RawMessage `json:"evidence"`
	AssignedBy             string          `json:"assignedBy,omitempty"`
	ApprovedBy             string          `json:"approvedBy,omitempty"`
	ApprovedAt             *time.Time      `json:"approvedAt,omitempty"`
	CreatedAt              time.Time       `json:"createdAt"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	RecordVersion          string          `json:"recordVersion"`
}

type CreateTagInput struct {
	ParentTagID string `json:"parentTagId,omitempty"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Governance  string `json:"governance"`
	Status      string `json:"status"`
}

type UpdateTagInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	ParentTagID     string `json:"parentTagId,omitempty"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Category        string `json:"category"`
	Governance      string `json:"governance"`
	Status          string `json:"status"`
}

type DeprecateTagInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type CreateTagAliasInput struct {
	TagID        string `json:"tagId"`
	Alias        string `json:"alias"`
	AliasType    string `json:"aliasType"`
	LanguageCode string `json:"languageCode"`
}

type UpdateTagAliasInput struct {
	ExpectedRecordVersion string `json:"expectedRecordVersion"`
	TagID                 string `json:"tagId"`
	Alias                 string `json:"alias"`
	AliasType             string `json:"aliasType"`
	LanguageCode          string `json:"languageCode"`
}

type DeleteRecordInput struct {
	ExpectedRecordVersion string `json:"expectedRecordVersion"`
}

type CreateAssetTagBindingInput struct {
	TagID                  string          `json:"tagId"`
	AssetType              string          `json:"assetType"`
	DatasetID              string          `json:"datasetId,omitempty"`
	DatasetVersionID       string          `json:"datasetVersionId,omitempty"`
	DatasetFieldID         string          `json:"datasetFieldId,omitempty"`
	DimensionID            string          `json:"dimensionId,omitempty"`
	DimensionMemberID      string          `json:"dimensionMemberId,omitempty"`
	MetricID               string          `json:"metricId,omitempty"`
	MetricVersionID        string          `json:"metricVersionId,omitempty"`
	MetricDatasetVersionID string          `json:"metricDatasetVersionId,omitempty"`
	Origin                 string          `json:"origin"`
	Status                 string          `json:"status"`
	Confidence             *float64        `json:"confidence,omitempty"`
	Evidence               json.RawMessage `json:"evidence"`
}

// UpdateAssetTagBindingInput edits review metadata only. Tag and subject
// identity are immutable because changing them in place would leave the old
// semantic subject without a rebuild event; delete and recreate instead.
type UpdateAssetTagBindingInput struct {
	ExpectedRecordVersion string          `json:"expectedRecordVersion"`
	Origin                string          `json:"origin"`
	Status                string          `json:"status"`
	Confidence            *float64        `json:"confidence,omitempty"`
	Evidence              json.RawMessage `json:"evidence"`
}

type Store interface {
	ListTags(context.Context, string, TagFilter) ([]Tag, int, error)
	CreateTag(context.Context, string, string, CreateTagInput) (Tag, error)
	UpdateTag(context.Context, string, string, string, UpdateTagInput) (Tag, error)
	DeprecateTag(context.Context, string, string, string, int64) (Tag, error)

	ListTagAliases(context.Context, string, AliasFilter) ([]TagAlias, int, error)
	CreateTagAlias(context.Context, string, string, CreateTagAliasInput) (TagAlias, error)
	UpdateTagAlias(context.Context, string, string, string, UpdateTagAliasInput) (TagAlias, error)
	DeleteTagAlias(context.Context, string, string, string, string) error

	ListAssetTagBindings(context.Context, string, BindingFilter) ([]AssetTagBinding, int, error)
	CreateAssetTagBinding(context.Context, string, string, CreateAssetTagBindingInput) (AssetTagBinding, error)
	UpdateAssetTagBinding(context.Context, string, string, string, UpdateAssetTagBindingInput) (AssetTagBinding, error)
	DeleteAssetTagBinding(context.Context, string, string, string, string) error
}
