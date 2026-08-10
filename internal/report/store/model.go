package store

import (
	"encoding/json"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

var (
	ErrNotFound            = errors.New("report not found")
	ErrRevisionConflict    = errors.New("report draft revision conflict")
	ErrAIEditForbidden     = errors.New("report AI edit permission is required")
	ErrInboundConflict     = errors.New("report inbound idempotency conflict")
	ErrPublicationConflict = errors.New("report publication idempotency conflict")
	ErrRevisionUnavailable = errors.New("report draft revision snapshot is unavailable")
	ErrReportOffline       = errors.New("report is offline")
)

type Identity struct {
	TenantID askdata.ID
	ActorID  askdata.ID
	DomainID askdata.ID
}

func (identity Identity) Validate() error {
	if err := identity.TenantID.Validate(); err != nil {
		return err
	}
	if err := identity.ActorID.Validate(); err != nil {
		return err
	}
	if identity.DomainID != "" {
		return identity.DomainID.Validate()
	}
	return nil
}

type Report struct {
	ID                        askdata.ID        `json:"id"`
	TenantID                  askdata.ID        `json:"tenantId"`
	DomainID                  askdata.ID        `json:"domainId,omitempty"`
	Code                      string            `json:"code"`
	Name                      string            `json:"name"`
	ReportType                report.ReportType `json:"reportType"`
	OwnerUserID               askdata.ID        `json:"ownerUserId"`
	CurrentPublishedVersionID askdata.ID        `json:"currentPublishedVersionId,omitempty"`
	Status                    string            `json:"status"`
	CreatedAt                 time.Time         `json:"createdAt"`
	UpdatedAt                 time.Time         `json:"updatedAt"`
}

type Draft struct {
	ReportID       askdata.ID              `json:"reportId"`
	TenantID       askdata.ID              `json:"tenantId"`
	Definition     report.ReportDefinition `json:"definition"`
	DefinitionRaw  json.RawMessage         `json:"-"`
	DefinitionHash string                  `json:"definitionHash"`
	SchemaVersion  string                  `json:"schemaVersion"`
	RevisionNo     int64                   `json:"revisionNo"`
	UpdatedBy      askdata.ID              `json:"updatedBy"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}

type Revision struct {
	ID                  askdata.ID      `json:"id"`
	ReportID            askdata.ID      `json:"reportId"`
	RevisionNo          int64           `json:"revisionNo"`
	BaseRevisionNo      int64           `json:"baseRevisionNo"`
	Source              string          `json:"source"`
	OperationJSON       json.RawMessage `json:"operationJson"`
	BeforeHash          string          `json:"beforeHash"`
	AfterHash           string          `json:"afterHash"`
	BeforeSnapshot      json.RawMessage `json:"beforeSnapshot,omitempty"`
	InverseOfRevisionNo *int64          `json:"inverseOfRevisionNo,omitempty"`
	ActorUserID         askdata.ID      `json:"actorUserId"`
	AIRunID             askdata.ID      `json:"aiRunId,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
}

type Version struct {
	ID                        askdata.ID              `json:"id"`
	ReportID                  askdata.ID              `json:"reportId"`
	VersionNo                 int                     `json:"versionNo"`
	SourceRevisionNo          int64                   `json:"sourceRevisionNo"`
	Definition                report.ReportDefinition `json:"definition"`
	DefinitionRaw             json.RawMessage         `json:"-"`
	DefinitionHash            string                  `json:"definitionHash"`
	SchemaVersion             string                  `json:"schemaVersion"`
	ObjectURI                 string                  `json:"objectUri"`
	PublishedBy               askdata.ID              `json:"publishedBy"`
	PublishedAt               time.Time               `json:"publishedAt"`
	RollbackOfVersionNo       *int                    `json:"rollbackOfVersionNo,omitempty"`
	RollbackReason            string                  `json:"rollbackReason,omitempty"`
	StaleInsightsAcknowledged bool                    `json:"staleInsightsAcknowledged"`
	ArtifactState             string                  `json:"artifactState"`
	ArtifactAttempt           int                     `json:"artifactAttempt"`
	ArtifactNextAttemptAt     time.Time               `json:"artifactNextAttemptAt"`
	Replayed                  bool                    `json:"replayed,omitempty"`
}

type PreparedDefinition struct {
	Definition report.ReportDefinition
	Canonical  []byte
	Hash       string
	Indexes    compiler.Indexes
}

func Prepare(definition report.ReportDefinition) (PreparedDefinition, error) {
	canonical, hash, err := compiler.Normalize(definition)
	if err != nil {
		return PreparedDefinition{}, err
	}
	var normalized report.ReportDefinition
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return PreparedDefinition{}, err
	}
	return PreparedDefinition{Definition: normalized, Canonical: canonical, Hash: hash, Indexes: compiler.BuildIndexes(normalized)}, nil
}

type RevisionConflict struct {
	Expected  int64    `json:"expectedRevision"`
	Current   int64    `json:"currentRevision"`
	Summaries []string `json:"operationSummaries"`
}

func (conflict *RevisionConflict) Error() string { return ErrRevisionConflict.Error() }
func (conflict *RevisionConflict) Unwrap() error { return ErrRevisionConflict }
