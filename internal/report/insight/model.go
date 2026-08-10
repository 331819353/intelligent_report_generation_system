// Package insight freezes the deterministic Evidence Bundle and generated
// Insight Artifact contracts shared by report runtime and Ask Data evidence.
package insight

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

const (
	EvidenceSchemaVersion = "1.1"
	InsightSchemaVersion  = "1.0"
	MaxFacts              = 256
	MaxQualityWarnings    = 64
	MaxInsightItems       = 64
)

var decimalPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

type SourceType string

const (
	SourceSemanticIR   SourceType = "SEMANTIC_IR"
	SourceDatasetQuery SourceType = "DATASET_QUERY"
)

type AnalysisMethod string

const (
	AnalysisCurrentValue      AnalysisMethod = "CURRENT_VALUE"
	AnalysisPeriodComparison  AnalysisMethod = "PERIOD_COMPARISON"
	AnalysisTrend             AnalysisMethod = "TREND"
	AnalysisAnomalyPoint      AnalysisMethod = "ANOMALY_POINT"
	AnalysisTopN              AnalysisMethod = "TOP_N"
	AnalysisContribution      AnalysisMethod = "CONTRIBUTION"
	AnalysisMaxChange         AnalysisMethod = "MAX_CHANGE"
	AnalysisTargetAchievement AnalysisMethod = "TARGET_ACHIEVEMENT"
	AnalysisGroupDifference   AnalysisMethod = "GROUP_DIFFERENCE"
	AnalysisShareOfTotal      AnalysisMethod = "SHARE_OF_TOTAL"
	AnalysisDataCompleteness  AnalysisMethod = "DATA_COMPLETENESS"
	// Legacy v1 aliases remain readable for immutable historical artifacts.
	AnalysisDistribution AnalysisMethod = "DISTRIBUTION"
	AnalysisRanking      AnalysisMethod = "RANKING"
	AnalysisSnapshot     AnalysisMethod = "SNAPSHOT"
)

type ResolvedTimeRange struct {
	Start        string `json:"start"`
	EndExclusive string `json:"endExclusive"`
	Timezone     string `json:"timezone"`
}

type Fact struct {
	ID              askdata.ID       `json:"id"`
	MetricVersionID askdata.ID       `json:"metricVersionId"`
	CurrentValue    string           `json:"currentValue"`
	PreviousValue   *string          `json:"previousValue"`
	ChangeRate      *string          `json:"changeRate"`
	Unit            string           `json:"unit"`
	CellRefs        []shared.CellRef `json:"cellRefs"`
}

type WarningSeverity string

const (
	WarningInfo     WarningSeverity = "INFO"
	WarningWarning  WarningSeverity = "WARNING"
	WarningBlocking WarningSeverity = "BLOCKING"
)

type QualityWarning struct {
	Code     string          `json:"code"`
	Severity WarningSeverity `json:"severity"`
	Message  string          `json:"message"`
}

type EvidenceBundle struct {
	SchemaVersion            string               `json:"schemaVersion"`
	SourceType               SourceType           `json:"sourceType"`
	SemanticReleaseID        *askdata.ID          `json:"semanticReleaseId"`
	SemanticIRHash           *askdata.ContentHash `json:"semanticIrHash"`
	DatasetVersionID         askdata.ID           `json:"datasetVersionId"`
	DataSnapshotVersion      string               `json:"dataSnapshotVersion"`
	QueryPlanHash            askdata.ContentHash  `json:"queryPlanHash"`
	FilterHash               askdata.ContentHash  `json:"filterHash"`
	AsOf                     string               `json:"asOf"`
	ResolvedTimeRange        ResolvedTimeRange    `json:"resolvedTimeRange"`
	AnalysisMethod           AnalysisMethod       `json:"analysisMethod"`
	AnalysisMethodVersion    string               `json:"analysisMethodVersion"`
	EvidenceAlgorithmVersion string               `json:"evidenceAlgorithmVersion"`
	Facts                    []Fact               `json:"facts"`
	QualityWarnings          []QualityWarning     `json:"qualityWarnings"`
	GeneratedAt              string               `json:"generatedAt"`
}

func DecodeEvidenceBundle(raw []byte) (EvidenceBundle, error) {
	var bundle EvidenceBundle
	if err := askdata.DecodeStrictJSON(raw, &bundle); err != nil {
		return EvidenceBundle{}, err
	}
	bundle = bundle.Normalize()
	if err := bundle.Validate(); err != nil {
		return EvidenceBundle{}, err
	}
	return bundle, nil
}

func (bundle EvidenceBundle) Normalize() EvidenceBundle {
	result := bundle
	result.Facts = append([]Fact(nil), bundle.Facts...)
	if result.Facts == nil {
		result.Facts = []Fact{}
	}
	for index := range result.Facts {
		result.Facts[index].CellRefs = append([]shared.CellRef(nil), result.Facts[index].CellRefs...)
		if result.Facts[index].CellRefs == nil {
			result.Facts[index].CellRefs = []shared.CellRef{}
		}
	}
	result.QualityWarnings = append([]QualityWarning(nil), bundle.QualityWarnings...)
	if result.QualityWarnings == nil {
		result.QualityWarnings = []QualityWarning{}
	}
	return result
}

func (bundle EvidenceBundle) Validate() error {
	if bundle.SchemaVersion != EvidenceSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", EvidenceSchemaVersion)
	}
	switch bundle.SourceType {
	case SourceSemanticIR:
		if bundle.SemanticReleaseID == nil || bundle.SemanticIRHash == nil {
			return errors.New("SEMANTIC_IR requires semanticReleaseId and semanticIrHash")
		}
		if err := bundle.SemanticReleaseID.Validate(); err != nil {
			return fmt.Errorf("semanticReleaseId: %w", err)
		}
		if err := bundle.SemanticIRHash.Validate(); err != nil {
			return fmt.Errorf("semanticIrHash: %w", err)
		}
	case SourceDatasetQuery:
		if bundle.SemanticReleaseID != nil || bundle.SemanticIRHash != nil {
			return errors.New("DATASET_QUERY requires semanticReleaseId and semanticIrHash to be null")
		}
	default:
		return fmt.Errorf("unsupported sourceType %q", bundle.SourceType)
	}
	if err := bundle.DatasetVersionID.Validate(); err != nil {
		return fmt.Errorf("datasetVersionId: %w", err)
	}
	provenance := bundle.StaleProvenance()
	if err := provenance.Validate(); err != nil || bundle.DataSnapshotVersion == "" ||
		bundle.AnalysisMethodVersion == "" || bundle.EvidenceAlgorithmVersion == "" {
		return fmt.Errorf("evidence provenance is invalid: %v", err)
	}
	if err := bundle.ResolvedTimeRange.Validate(); err != nil {
		return fmt.Errorf("resolvedTimeRange: %w", err)
	}
	asOf, err := parseCanonicalTime(bundle.AsOf)
	if err != nil {
		return fmt.Errorf("asOf: %w", err)
	}
	generatedAt, err := parseCanonicalTime(bundle.GeneratedAt)
	if err != nil {
		return fmt.Errorf("generatedAt: %w", err)
	}
	if generatedAt.Before(asOf) {
		return errors.New("generatedAt cannot precede asOf")
	}
	if !validAnalysisMethod(bundle.AnalysisMethod) {
		return fmt.Errorf("unsupported analysisMethod %q", bundle.AnalysisMethod)
	}
	if len(bundle.Facts) == 0 || len(bundle.Facts) > MaxFacts {
		return fmt.Errorf("facts must contain between 1 and %d items", MaxFacts)
	}
	seenFacts := make(map[askdata.ID]struct{}, len(bundle.Facts))
	for index, fact := range bundle.Facts {
		if err := fact.Validate(bundle.AnalysisMethod); err != nil {
			return fmt.Errorf("facts[%d]: %w", index, err)
		}
		if _, exists := seenFacts[fact.ID]; exists {
			return fmt.Errorf("facts[%d] duplicates id %q", index, fact.ID)
		}
		seenFacts[fact.ID] = struct{}{}
	}
	if len(bundle.QualityWarnings) > MaxQualityWarnings {
		return fmt.Errorf("qualityWarnings exceeds %d items", MaxQualityWarnings)
	}
	for index, warning := range bundle.QualityWarnings {
		if err := warning.Validate(); err != nil {
			return fmt.Errorf("qualityWarnings[%d]: %w", index, err)
		}
	}
	return nil
}

func (rangeValue ResolvedTimeRange) Validate() error {
	start, err := parseCanonicalTime(rangeValue.Start)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	end, err := parseCanonicalTime(rangeValue.EndExclusive)
	if err != nil {
		return fmt.Errorf("endExclusive: %w", err)
	}
	if !end.After(start) {
		return errors.New("endExclusive must be after start")
	}
	if rangeValue.Timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(rangeValue.Timezone); err != nil {
		return errors.New("timezone must be a known IANA timezone")
	}
	return nil
}

func (fact Fact) Validate(method AnalysisMethod) error {
	if err := fact.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if err := fact.MetricVersionID.Validate(); err != nil {
		return fmt.Errorf("metricVersionId: %w", err)
	}
	if !validDecimal(fact.CurrentValue) {
		return errors.New("currentValue must be a canonical decimal string")
	}
	if (fact.PreviousValue == nil) != (fact.ChangeRate == nil) {
		return errors.New("previousValue and changeRate must both be null or both be decimal strings")
	}
	if fact.PreviousValue != nil && (!validDecimal(*fact.PreviousValue) || !validDecimal(*fact.ChangeRate)) {
		return errors.New("previousValue and changeRate must be canonical decimal strings")
	}
	if method == AnalysisPeriodComparison && fact.PreviousValue == nil {
		return errors.New("PERIOD_COMPARISON facts require previousValue and changeRate")
	}
	if err := validateText(fact.Unit, 32, false); err != nil {
		return fmt.Errorf("unit: %w", err)
	}
	if len(fact.CellRefs) == 0 || len(fact.CellRefs) > 32 {
		return errors.New("cellRefs must contain between 1 and 32 items")
	}
	seen := map[string]struct{}{}
	for index, ref := range fact.CellRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("cellRefs[%d]: %w", index, err)
		}
		key := ref.RowKey + "\x00" + ref.ColumnKey
		if _, exists := seen[key]; exists {
			return fmt.Errorf("cellRefs[%d] is duplicated", index)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (warning QualityWarning) Validate() error {
	if err := askdata.ID(warning.Code).Validate(); err != nil || strings.ToUpper(warning.Code) != warning.Code {
		return errors.New("code must be an uppercase stable identifier")
	}
	if warning.Severity != WarningInfo && warning.Severity != WarningWarning && warning.Severity != WarningBlocking {
		return fmt.Errorf("unsupported severity %q", warning.Severity)
	}
	return validateText(warning.Message, 1024, false)
}

func (bundle EvidenceBundle) StaleProvenance() shared.Provenance {
	return shared.Provenance{
		DatasetVersionID:         bundle.DatasetVersionID,
		DataSnapshotVersion:      bundle.DataSnapshotVersion,
		QueryHash:                bundle.QueryPlanHash,
		FilterHash:               bundle.FilterHash,
		AnalysisMethodVersion:    bundle.AnalysisMethodVersion,
		EvidenceAlgorithmVersion: bundle.EvidenceAlgorithmVersion,
	}
}

func (bundle EvidenceBundle) Hash() (askdata.ContentHash, error) {
	normalized := bundle.Normalize()
	if err := normalized.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(raw), nil
}

type InsightContent struct {
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
	Risks    []string `json:"risks"`
	Actions  []string `json:"actions"`
}

func (content InsightContent) CanonicalText() string {
	parts := make([]string, 0, 1+len(content.Findings)+len(content.Risks)+len(content.Actions))
	parts = append(parts, content.Summary)
	parts = append(parts, content.Findings...)
	parts = append(parts, content.Risks...)
	parts = append(parts, content.Actions...)
	return strings.Join(parts, "\n")
}

func (content InsightContent) Empty() bool {
	return content.Summary == "" && len(content.Findings) == 0 && len(content.Risks) == 0 && len(content.Actions) == 0
}

type InsightStatus string

const (
	InsightCurrent InsightStatus = "CURRENT"
	InsightStale   InsightStatus = "STALE"
	InsightFailed  InsightStatus = "FAILED"
)

type InsightArtifact struct {
	SchemaVersion         string              `json:"schemaVersion"`
	ID                    askdata.ID          `json:"id"`
	EvidenceHash          askdata.ContentHash `json:"evidenceHash"`
	PromptVersion         string              `json:"promptVersion"`
	ModelPolicy           string              `json:"modelPolicy"`
	VerifierVersion       string              `json:"verifierVersion"`
	PolicyWordlistVersion string              `json:"policyWordlistVersion"`
	Content               InsightContent      `json:"content"`
	Citations             []shared.Citation   `json:"citations"`
	Status                InsightStatus       `json:"status"`
	HumanEdited           bool                `json:"humanEdited"`
	HumanEditedBy         *askdata.ID         `json:"humanEditedBy"`
	HumanEditedAt         *string             `json:"humanEditedAt"`
}

func DecodeInsightArtifact(raw []byte) (InsightArtifact, error) {
	var artifact InsightArtifact
	if err := askdata.DecodeStrictJSON(raw, &artifact); err != nil {
		return InsightArtifact{}, err
	}
	artifact = artifact.Normalize()
	if err := artifact.Validate(); err != nil {
		return InsightArtifact{}, err
	}
	return artifact, nil
}

func (artifact InsightArtifact) Normalize() InsightArtifact {
	result := artifact
	result.Content.Findings = nonNilStrings(artifact.Content.Findings)
	result.Content.Risks = nonNilStrings(artifact.Content.Risks)
	result.Content.Actions = nonNilStrings(artifact.Content.Actions)
	result.Citations = shared.NormalizeCitations(artifact.Citations)
	return result
}

func (artifact InsightArtifact) Validate() error {
	if artifact.SchemaVersion != InsightSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", InsightSchemaVersion)
	}
	if err := artifact.ID.Validate(); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	provenance := artifact.versionProvenance()
	if err := provenance.Validate(); err != nil || artifact.EvidenceHash == "" || artifact.PromptVersion == "" ||
		artifact.ModelPolicy == "" || artifact.VerifierVersion == "" || artifact.PolicyWordlistVersion == "" {
		return fmt.Errorf("insight provenance is invalid: %v", err)
	}
	if artifact.Status != InsightCurrent && artifact.Status != InsightStale && artifact.Status != InsightFailed {
		return fmt.Errorf("unsupported status %q", artifact.Status)
	}
	if artifact.HumanEdited {
		if artifact.HumanEditedBy == nil || artifact.HumanEditedAt == nil {
			return errors.New("humanEdited=true requires humanEditedBy and humanEditedAt")
		}
		if err := artifact.HumanEditedBy.Validate(); err != nil {
			return fmt.Errorf("humanEditedBy: %w", err)
		}
		if _, err := parseCanonicalTime(*artifact.HumanEditedAt); err != nil {
			return fmt.Errorf("humanEditedAt: %w", err)
		}
	} else if artifact.HumanEditedBy != nil || artifact.HumanEditedAt != nil {
		return errors.New("humanEdited=false requires humanEditedBy and humanEditedAt to be null")
	}
	if artifact.Status == InsightFailed {
		if !artifact.Content.Empty() || len(artifact.Citations) != 0 || artifact.HumanEdited {
			return errors.New("FAILED insight must have empty content/citations and cannot be human edited")
		}
		return nil
	}
	if err := artifact.Content.Validate(); err != nil {
		return fmt.Errorf("content: %w", err)
	}
	if !artifact.HumanEdited && len(artifact.Citations) == 0 {
		return errors.New("generated CURRENT/STALE insight requires citations")
	}
	if err := shared.ValidateCitations(artifact.Content.CanonicalText(), artifact.Citations); err != nil {
		return err
	}
	return nil
}

func (content InsightContent) Validate() error {
	if err := validateText(content.Summary, 4096, false); err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	for name, values := range map[string][]string{
		"findings": content.Findings, "risks": content.Risks, "actions": content.Actions,
	} {
		if len(values) > MaxInsightItems {
			return fmt.Errorf("%s exceeds %d items", name, MaxInsightItems)
		}
		for index, value := range values {
			if err := validateText(value, 4096, false); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, index, err)
			}
		}
	}
	if utf8.RuneCountInString(content.CanonicalText()) > 32_768 {
		return errors.New("content exceeds 32768 Unicode code points")
	}
	return nil
}

func (artifact InsightArtifact) versionProvenance() shared.Provenance {
	return shared.Provenance{
		EvidenceHash:  artifact.EvidenceHash,
		PromptVersion: artifact.PromptVersion, ModelPolicy: artifact.ModelPolicy,
		VerifierVersion: artifact.VerifierVersion, PolicyWordlistVersion: artifact.PolicyWordlistVersion,
	}
}

func (artifact InsightArtifact) StaleProvenance(bundle EvidenceBundle) shared.Provenance {
	result := bundle.StaleProvenance()
	result.EvidenceHash = artifact.EvidenceHash
	result.PromptVersion = artifact.PromptVersion
	result.ModelPolicy = artifact.ModelPolicy
	result.VerifierVersion = artifact.VerifierVersion
	result.PolicyWordlistVersion = artifact.PolicyWordlistVersion
	return result
}

func (artifact InsightArtifact) ValidateAgainst(bundle EvidenceBundle) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	hash, err := bundle.Hash()
	if err != nil {
		return err
	}
	if artifact.EvidenceHash != hash {
		return errors.New("evidenceHash does not match Evidence Bundle")
	}
	return nil
}

func (artifact InsightArtifact) IsStale(bundle EvidenceBundle, current shared.Provenance) bool {
	if artifact.ValidateAgainst(bundle) != nil {
		return true
	}
	return shared.IsStale(artifact.StaleProvenance(bundle), current)
}

func validAnalysisMethod(value AnalysisMethod) bool {
	switch value {
	case AnalysisCurrentValue, AnalysisPeriodComparison, AnalysisTrend, AnalysisAnomalyPoint,
		AnalysisTopN, AnalysisContribution, AnalysisMaxChange, AnalysisTargetAchievement,
		AnalysisGroupDifference, AnalysisShareOfTotal, AnalysisDataCompleteness,
		AnalysisDistribution, AnalysisRanking, AnalysisSnapshot:
		return true
	default:
		return false
	}
}

func validDecimal(value string) bool {
	return len(value) <= 256 && decimalPattern.MatchString(value)
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("must be a canonical RFC3339 timestamp")
	}
	return parsed, nil
}

func validateText(value string, max int, allowEmpty bool) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "\x00\r\n") ||
		(!allowEmpty && strings.TrimSpace(value) == "") {
		return fmt.Errorf("must be valid UTF-8 with at most %d code points and no controls", max)
	}
	return nil
}

func nonNilStrings(values []string) []string {
	result := append([]string(nil), values...)
	if result == nil {
		return []string{}
	}
	return result
}
