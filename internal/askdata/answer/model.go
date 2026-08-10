package answer

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

const (
	SchemaVersion         = "1.0"
	MaxCards              = 16
	MaxFindings           = 32
	MaxNarrativeRunes     = 16_384
	MaxNarrativeItemRunes = 4_096
)

var decimalPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

type MetricValue struct {
	MetricVersionID askdata.ID `json:"metricVersionId"`
	Value           string     `json:"value"`
	Unit            string     `json:"unit"`
	Currency        *string    `json:"currency"`
	Label           string     `json:"label"`
	ColumnKey       string     `json:"columnKey"`
}

type ChartType string

const (
	ChartLine  ChartType = "LINE"
	ChartBar   ChartType = "BAR"
	ChartTable ChartType = "TABLE"
	ChartKPI   ChartType = "KPI"
)

type ChartSpec struct {
	Type              ChartType  `json:"type"`
	DatasetID         askdata.ID `json:"datasetId"`
	CategoryColumnKey *string    `json:"categoryColumnKey"`
	ValueColumnKeys   []string   `json:"valueColumnKeys"`
}

type StructuredLayer struct {
	Headline  *MetricValue  `json:"headline"`
	Cards     []MetricValue `json:"cards"`
	ChartSpec *ChartSpec    `json:"chartSpec"`
	TableRef  askdata.ID    `json:"tableRef"`
}

type NarrativeLayer struct {
	Summary   string            `json:"summary"`
	Findings  []string          `json:"findings"`
	Citations []shared.Citation `json:"citations"`
}

// CanonicalText is the single citation surface: summary followed by each
// finding, separated by one newline. Spans index Unicode code points in it.
func (narrative NarrativeLayer) CanonicalText() string {
	parts := make([]string, 0, len(narrative.Findings)+1)
	parts = append(parts, narrative.Summary)
	parts = append(parts, narrative.Findings...)
	return strings.Join(parts, "\n")
}

func (narrative NarrativeLayer) Empty() bool {
	return narrative.Summary == "" && len(narrative.Findings) == 0 && len(narrative.Citations) == 0
}

type AnswerLayers struct {
	Structured StructuredLayer `json:"structured"`
	Narrative  NarrativeLayer  `json:"narrative"`
}

type Verification struct {
	VerifierVersion       string `json:"verifierVersion"`
	PolicyWordlistVersion string `json:"policyWordlistVersion"`
	Attempts              int    `json:"attempts"`
	Passed                bool   `json:"passed"`
	Degraded              bool   `json:"degraded"`
}

type Provenance struct {
	PromptVersion     string              `json:"promptVersion"`
	ModelPolicy       string              `json:"modelPolicy"`
	EvidenceHash      askdata.ContentHash `json:"evidenceHash"`
	ResultHash        askdata.ContentHash `json:"resultHash"`
	SemanticReleaseID askdata.ID          `json:"semanticReleaseId"`
	ChartRuleVersion  string              `json:"chartRuleVersion"`
}

type AnswerArtifact struct {
	SchemaVersion string       `json:"schemaVersion"`
	RunID         askdata.ID   `json:"runId"`
	Layers        AnswerLayers `json:"layers"`
	Verification  Verification `json:"verification"`
	Provenance    Provenance   `json:"provenance"`
}

func Decode(raw []byte) (AnswerArtifact, error) {
	var artifact AnswerArtifact
	if err := askdata.DecodeStrictJSON(raw, &artifact); err != nil {
		return AnswerArtifact{}, err
	}
	artifact = artifact.Normalize()
	if err := artifact.Validate(); err != nil {
		return AnswerArtifact{}, err
	}
	return artifact, nil
}

func (artifact AnswerArtifact) Normalize() AnswerArtifact {
	result := artifact
	result.Layers.Structured.Cards = append([]MetricValue(nil), artifact.Layers.Structured.Cards...)
	if result.Layers.Structured.Cards == nil {
		result.Layers.Structured.Cards = []MetricValue{}
	}
	if artifact.Layers.Structured.Headline != nil {
		headline := *artifact.Layers.Structured.Headline
		result.Layers.Structured.Headline = &headline
	}
	if artifact.Layers.Structured.ChartSpec != nil {
		chart := *artifact.Layers.Structured.ChartSpec
		chart.ValueColumnKeys = append([]string(nil), chart.ValueColumnKeys...)
		if chart.ValueColumnKeys == nil {
			chart.ValueColumnKeys = []string{}
		}
		result.Layers.Structured.ChartSpec = &chart
	}
	result.Layers.Narrative.Findings = append([]string(nil), artifact.Layers.Narrative.Findings...)
	if result.Layers.Narrative.Findings == nil {
		result.Layers.Narrative.Findings = []string{}
	}
	result.Layers.Narrative.Citations = shared.NormalizeCitations(artifact.Layers.Narrative.Citations)
	return result
}

func (artifact AnswerArtifact) Validate() error {
	if artifact.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	parsedRunID, err := uuid.Parse(string(artifact.RunID))
	if err != nil || parsedRunID.String() != string(artifact.RunID) {
		return errors.New("runId must be a canonical UUID")
	}
	if err := artifact.Layers.Structured.Validate(); err != nil {
		return fmt.Errorf("layers.structured: %w", err)
	}
	if err := artifact.Verification.Validate(); err != nil {
		return fmt.Errorf("verification: %w", err)
	}
	if err := artifact.Provenance.Validate(); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	narrative := artifact.Layers.Narrative
	if artifact.Verification.Degraded {
		if !narrative.Empty() || artifact.Verification.Passed {
			return errors.New("degraded answers require an empty narrative and passed=false")
		}
		return nil
	}
	if !artifact.Verification.Passed || strings.TrimSpace(narrative.Summary) == "" {
		return errors.New("non-degraded answers require a verified summary")
	}
	if len(narrative.Findings) > MaxFindings {
		return fmt.Errorf("layers.narrative.findings exceeds %d items", MaxFindings)
	}
	if err := validateNarrativeString(narrative.Summary); err != nil {
		return fmt.Errorf("layers.narrative.summary: %w", err)
	}
	for index, finding := range narrative.Findings {
		if err := validateNarrativeString(finding); err != nil {
			return fmt.Errorf("layers.narrative.findings[%d]: %w", index, err)
		}
	}
	if utf8.RuneCountInString(narrative.CanonicalText()) > MaxNarrativeRunes {
		return fmt.Errorf("layers.narrative exceeds %d Unicode code points", MaxNarrativeRunes)
	}
	if err := shared.ValidateCitations(narrative.CanonicalText(), narrative.Citations); err != nil {
		return fmt.Errorf("layers.narrative: %w", err)
	}
	return nil
}

func (layer StructuredLayer) Validate() error {
	if err := layer.TableRef.Validate(); err != nil {
		return fmt.Errorf("tableRef: %w", err)
	}
	if len(layer.Cards) > MaxCards {
		return fmt.Errorf("cards exceeds %d items", MaxCards)
	}
	if layer.Headline != nil {
		if err := layer.Headline.Validate(); err != nil {
			return fmt.Errorf("headline: %w", err)
		}
	}
	for index, card := range layer.Cards {
		if err := card.Validate(); err != nil {
			return fmt.Errorf("cards[%d]: %w", index, err)
		}
	}
	if layer.Headline == nil && len(layer.Cards) == 0 {
		return errors.New("headline or cards is required")
	}
	if layer.ChartSpec != nil {
		if err := layer.ChartSpec.Validate(); err != nil {
			return fmt.Errorf("chartSpec: %w", err)
		}
	}
	return nil
}

func (value MetricValue) Validate() error {
	if err := value.MetricVersionID.Validate(); err != nil {
		return fmt.Errorf("metricVersionId: %w", err)
	}
	if !decimalPattern.MatchString(value.Value) || len(value.Value) > 256 {
		return errors.New("value must be a canonical decimal string")
	}
	if err := validateShortText(value.Unit, 32); err != nil {
		return fmt.Errorf("unit: %w", err)
	}
	if value.Currency != nil {
		if err := validateShortText(*value.Currency, 16); err != nil {
			return fmt.Errorf("currency: %w", err)
		}
	}
	if err := validateShortText(value.Label, 256); err != nil {
		return fmt.Errorf("label: %w", err)
	}
	if err := askdata.ID(value.ColumnKey).Validate(); err != nil {
		return fmt.Errorf("columnKey: %w", err)
	}
	return nil
}

func (chart ChartSpec) Validate() error {
	if chart.Type != ChartLine && chart.Type != ChartBar && chart.Type != ChartTable && chart.Type != ChartKPI {
		return fmt.Errorf("unsupported type %q", chart.Type)
	}
	if err := chart.DatasetID.Validate(); err != nil {
		return fmt.Errorf("datasetId: %w", err)
	}
	if len(chart.ValueColumnKeys) == 0 || len(chart.ValueColumnKeys) > 8 {
		return errors.New("valueColumnKeys must contain between 1 and 8 items")
	}
	seen := map[string]struct{}{}
	for index, column := range chart.ValueColumnKeys {
		if err := askdata.ID(column).Validate(); err != nil {
			return fmt.Errorf("valueColumnKeys[%d]: %w", index, err)
		}
		if _, exists := seen[column]; exists {
			return fmt.Errorf("valueColumnKeys[%d] is duplicated", index)
		}
		seen[column] = struct{}{}
	}
	if chart.CategoryColumnKey != nil {
		if err := askdata.ID(*chart.CategoryColumnKey).Validate(); err != nil {
			return fmt.Errorf("categoryColumnKey: %w", err)
		}
	}
	if (chart.Type == ChartLine || chart.Type == ChartBar) && chart.CategoryColumnKey == nil {
		return errors.New("LINE and BAR require categoryColumnKey")
	}
	return nil
}

func (verification Verification) Validate() error {
	provenance := shared.Provenance{
		VerifierVersion:       verification.VerifierVersion,
		PolicyWordlistVersion: verification.PolicyWordlistVersion,
	}
	if provenance.Validate() != nil || verification.VerifierVersion == "" || verification.PolicyWordlistVersion == "" {
		return errors.New("verifierVersion and policyWordlistVersion must be stable version identifiers")
	}
	if verification.Attempts < 0 || verification.Attempts > 2 {
		return errors.New("attempts must be between 0 and 2")
	}
	if verification.Passed == verification.Degraded {
		return errors.New("exactly one of passed and degraded must be true")
	}
	return nil
}

func (provenance Provenance) Validate() error {
	sharedValue := shared.Provenance{
		PromptVersion: provenance.PromptVersion, ModelPolicy: provenance.ModelPolicy,
		EvidenceHash: provenance.EvidenceHash, ResultHash: provenance.ResultHash,
		SemanticReleaseID: provenance.SemanticReleaseID, ChartRuleVersion: provenance.ChartRuleVersion,
	}
	if sharedValue.Validate() != nil || provenance.PromptVersion == "" || provenance.ModelPolicy == "" ||
		provenance.EvidenceHash == "" || provenance.ResultHash == "" || provenance.SemanticReleaseID == "" ||
		provenance.ChartRuleVersion == "" {
		return errors.New("all answer provenance fields must be valid and non-empty")
	}
	return nil
}

func (artifact AnswerArtifact) StaleProvenance() shared.Provenance {
	return shared.Provenance{
		PromptVersion:         artifact.Provenance.PromptVersion,
		ModelPolicy:           artifact.Provenance.ModelPolicy,
		VerifierVersion:       artifact.Verification.VerifierVersion,
		PolicyWordlistVersion: artifact.Verification.PolicyWordlistVersion,
		EvidenceHash:          artifact.Provenance.EvidenceHash,
		ResultHash:            artifact.Provenance.ResultHash,
		SemanticReleaseID:     artifact.Provenance.SemanticReleaseID,
		ChartRuleVersion:      artifact.Provenance.ChartRuleVersion,
	}
}

func (artifact AnswerArtifact) IsStale(current shared.Provenance) bool {
	return shared.IsStale(artifact.StaleProvenance(), current)
}

func (artifact AnswerArtifact) MarshalCanonical() ([]byte, error) {
	normalized := artifact.Normalize()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func validateNarrativeString(value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxNarrativeItemRunes {
		return fmt.Errorf("must be non-empty valid UTF-8 with at most %d Unicode code points", MaxNarrativeItemRunes)
	}
	return nil
}

func validateShortText(value string, max int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("must be non-empty valid UTF-8 with at most %d code points and no controls", max)
	}
	return nil
}
