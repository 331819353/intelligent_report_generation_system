package savedquestion

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
)

type Visibility string

const (
	Private            Visibility = "PRIVATE"
	Team               Visibility = "TEAM"
	CertifiedCandidate Visibility = "CERTIFIED_CANDIDATE"
)

type Status string

const (
	Active         Status = "ACTIVE"
	NeedsMigration Status = "NEEDS_MIGRATION"
	Archived       Status = "ARCHIVED"
)

type PrincipalType string

const (
	PrincipalUser   PrincipalType = "USER"
	PrincipalRole   PrincipalType = "ROLE"
	PrincipalDomain PrincipalType = "DOMAIN"
)

var (
	ErrInvalid          = errors.New("saved question input is invalid")
	ErrNotFound         = errors.New("saved question was not found")
	ErrPermissionDenied = errors.New("saved question permission denied")
)

type Identity struct {
	TenantID askdata.ID
	DomainID askdata.ID
	ActorID  askdata.ID
}

func (identity Identity) Validate() error {
	for _, value := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ActorID} {
		if value.Validate() != nil {
			return ErrInvalid
		}
	}
	return nil
}

type CreateInput struct {
	Name                string
	QuestionText        string
	Visibility          Visibility
	SemanticIR          ircontract.SemanticIR
	SourceQuestionRunID askdata.ID
}

func (input CreateInput) Validate() error {
	if !validText(input.Name, 1, 200) || !validText(input.QuestionText, 1, 4000) ||
		(input.Visibility != Private && input.Visibility != Team && input.Visibility != CertifiedCandidate) {
		return ErrInvalid
	}
	if input.SourceQuestionRunID != "" && input.SourceQuestionRunID.Validate() != nil {
		return ErrInvalid
	}
	return input.SemanticIR.Validate()
}

type SavedQuestion struct {
	ID                         askdata.ID            `json:"id"`
	TenantID                   askdata.ID            `json:"tenantId"`
	DomainID                   askdata.ID            `json:"domainId"`
	OwnerUserID                askdata.ID            `json:"ownerUserId"`
	Visibility                 Visibility            `json:"visibility"`
	Name                       string                `json:"name"`
	QuestionText               string                `json:"questionText"`
	SemanticIR                 ircontract.SemanticIR `json:"semanticIr"`
	SemanticIRHash             askdata.ContentHash   `json:"semanticIrHash"`
	SemanticReleaseID          askdata.ID            `json:"semanticReleaseId"`
	SemanticReleaseContentHash askdata.ContentHash   `json:"semanticReleaseContentHash"`
	SourceQuestionRunID        askdata.ID            `json:"sourceQuestionRunId,omitempty"`
	Status                     Status                `json:"status"`
	MigrationReason            string                `json:"migrationReason,omitempty"`
	CreatedAt                  time.Time             `json:"createdAt"`
	UpdatedAt                  time.Time             `json:"updatedAt"`
}

type ShareInput struct {
	PrincipalType PrincipalType
	PrincipalID   askdata.ID
}

func (input ShareInput) Validate() error {
	if input.PrincipalID.Validate() != nil {
		return ErrInvalid
	}
	switch input.PrincipalType {
	case PrincipalUser, PrincipalRole, PrincipalDomain:
		return nil
	}
	return ErrInvalid
}

type Dependency struct {
	Type string
	ID   string
}

func Dependencies(ir ircontract.SemanticIR) []Dependency {
	seen := map[string]Dependency{}
	add := func(kind, id string) {
		if id != "" {
			seen[kind+"\x00"+id] = Dependency{Type: kind, ID: id}
		}
	}
	add("SEMANTIC_RELEASE", string(ir.SemanticReleaseID))
	for _, metric := range ir.Metrics {
		add("METRIC_VERSION", string(metric.MetricVersionID))
	}
	for _, group := range ir.GroupBy {
		add("DIMENSION_VERSION", string(group.DimensionVersionID))
	}
	for _, filter := range ir.Filters {
		add("DIMENSION_VERSION", string(filter.DimensionVersionID))
		for _, member := range filter.MemberVersionIDs {
			add("MEMBER_VERSION", string(member))
		}
	}
	if ir.TimeRange != nil {
		add("DIMENSION_VERSION", string(ir.TimeRange.DimensionVersionID))
	}
	result := make([]Dependency, 0, len(seen))
	for _, item := range seen {
		result = append(result, item)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Type+result[j].ID < result[j-1].Type+result[j-1].ID; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func validText(value string, minimum, maximum int) bool {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return false
	}
	count := utf8.RuneCountInString(value)
	if count < minimum || count > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
