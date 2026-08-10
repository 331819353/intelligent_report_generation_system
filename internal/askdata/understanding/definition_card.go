package understanding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const DefinitionCardSchemaVersion = "metric-definition-card-v1"

var (
	ErrDefinitionCardInvalid     = errors.New("metric definition card is invalid")
	ErrDefinitionRegistryMissing = errors.New("metric definition registry is unavailable")
	ErrQuestionNotDefinition     = errors.New("question is not a definition request")
)

// MetricDefinitionContract is the minimum governed Registry projection needed
// for the no-query definition short path. Formula is display-safe business
// notation, never SQL or a physical expression.
type MetricDefinitionContract struct {
	MetricVersionID askdata.ID            `json:"metricVersionId"`
	Name            string                `json:"name"`
	Definition      string                `json:"definition"`
	Formula         string                `json:"formula,omitempty"`
	Unit            string                `json:"unit,omitempty"`
	OwnerID         askdata.ID            `json:"ownerId"`
	SemanticVersion string                `json:"semanticVersion"`
	Status          string                `json:"status"`
	EvidenceRefs    []askdata.EvidenceRef `json:"evidenceRefs"`
}

type MetricDefinitionRegistry interface {
	GetMetricDefinition(context.Context, askdata.ID) (MetricDefinitionContract, error)
}

type DefinitionCard struct {
	SchemaVersion   string                `json:"schemaVersion"`
	MetricVersionID askdata.ID            `json:"metricVersionId"`
	Name            string                `json:"name"`
	Definition      string                `json:"definition"`
	Formula         string                `json:"formula,omitempty"`
	Unit            string                `json:"unit,omitempty"`
	OwnerID         askdata.ID            `json:"ownerId"`
	SemanticVersion string                `json:"semanticVersion"`
	Status          string                `json:"status"`
	EvidenceRefs    []askdata.EvidenceRef `json:"evidenceRefs"`
	DataQueryIssued bool                  `json:"dataQueryIssued"`
	MaxLLMCalls     int                   `json:"maxLlmCalls"`
}

// DefinitionCardService has intentionally no warehouse/query dependency. A
// definition request can only read a governed Registry contract and therefore
// cannot accidentally start compilation or data execution.
type DefinitionCardService struct {
	classifier *ScopeClassifier
	registry   MetricDefinitionRegistry
}

func NewDefinitionCardService(classifier *ScopeClassifier, registry MetricDefinitionRegistry) (*DefinitionCardService, error) {
	if classifier == nil {
		return nil, ErrInvalidScopeVerdict
	}
	if registry == nil {
		return nil, ErrDefinitionRegistryMissing
	}
	return &DefinitionCardService{classifier: classifier, registry: registry}, nil
}

func (service *DefinitionCardService) Render(
	ctx context.Context,
	understanding QuestionUnderstanding,
	metricVersionID askdata.ID,
) (DefinitionCard, ScopeVerdict, error) {
	questionType, verdict := service.classifier.Classify(ctx, understanding)
	if questionType != QuestionTypeDefinition || verdict.Outcome != ScopeOutcomeDefinition {
		return DefinitionCard{}, verdict, ErrQuestionNotDefinition
	}
	if err := metricVersionID.Validate(); err != nil {
		return DefinitionCard{}, verdict, fmt.Errorf("%w: metric version: %v", ErrDefinitionCardInvalid, err)
	}
	contract, err := service.registry.GetMetricDefinition(ctx, metricVersionID)
	if err != nil {
		return DefinitionCard{}, verdict, err
	}
	if contract.MetricVersionID != metricVersionID {
		return DefinitionCard{}, verdict, fmt.Errorf("%w: registry returned another metric version", ErrDefinitionCardInvalid)
	}
	card, err := BuildDefinitionCard(contract)
	return card, verdict, err
}

func BuildDefinitionCard(contract MetricDefinitionContract) (DefinitionCard, error) {
	card := DefinitionCard{
		SchemaVersion:   DefinitionCardSchemaVersion,
		MetricVersionID: contract.MetricVersionID,
		Name:            strings.TrimSpace(contract.Name),
		Definition:      strings.TrimSpace(contract.Definition),
		Formula:         strings.TrimSpace(contract.Formula),
		Unit:            strings.TrimSpace(contract.Unit),
		OwnerID:         contract.OwnerID,
		SemanticVersion: strings.TrimSpace(contract.SemanticVersion),
		Status:          strings.ToUpper(strings.TrimSpace(contract.Status)),
		EvidenceRefs:    append([]askdata.EvidenceRef(nil), contract.EvidenceRefs...),
		DataQueryIssued: false,
		MaxLLMCalls:     1,
	}
	if err := card.Validate(); err != nil {
		return DefinitionCard{}, err
	}
	return card, nil
}

func (card DefinitionCard) Validate() error {
	if card.SchemaVersion != DefinitionCardSchemaVersion || card.MetricVersionID.Validate() != nil ||
		card.OwnerID.Validate() != nil || card.DataQueryIssued || card.MaxLLMCalls != 1 ||
		!boundedDefinitionCardText(card.Name, 1, 256) ||
		!boundedDefinitionCardText(card.Definition, 1, 4_096) ||
		!boundedDefinitionCardText(card.SemanticVersion, 1, 128) ||
		!boundedDefinitionCardText(card.Status, 1, 64) ||
		!boundedDefinitionCardText(card.Formula, 0, 2_048) ||
		!boundedDefinitionCardText(card.Unit, 0, 128) {
		return ErrDefinitionCardInvalid
	}
	if physicalQueryTextPattern.MatchString(card.Name) || physicalQueryTextPattern.MatchString(card.Definition) ||
		physicalQueryTextPattern.MatchString(card.Formula) {
		return fmt.Errorf("%w: physical query text", ErrDefinitionCardInvalid)
	}
	if len(card.EvidenceRefs) == 0 || len(card.EvidenceRefs) > 16 {
		return fmt.Errorf("%w: evidence refs", ErrDefinitionCardInvalid)
	}
	seen := map[askdata.ID]struct{}{}
	for index, ref := range card.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: evidenceRefs[%d]: %v", ErrDefinitionCardInvalid, index, err)
		}
		if _, exists := seen[ref.EvidenceID]; exists {
			return fmt.Errorf("%w: duplicate evidence", ErrDefinitionCardInvalid)
		}
		seen[ref.EvidenceID] = struct{}{}
	}
	return nil
}

func boundedDefinitionCardText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}
