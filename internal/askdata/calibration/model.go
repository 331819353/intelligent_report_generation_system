// Package calibration contains the dependency-neutral feature and example
// contracts shared by evaluation producers and binding calibrators.
package calibration

import (
	"fmt"
	"math"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

const (
	MaxCalibrationExamples = 100_000
	MaxCalibrationRank     = 10_000
)

type ComplexityClass string

const (
	ComplexitySimple     ComplexityClass = "SIMPLE"
	ComplexityComposite  ComplexityClass = "COMPOSITE"
	ComplexityContextual ComplexityClass = "CONTEXTUAL"
	ComplexityRelational ComplexityClass = "RELATIONAL"
)

type AmbiguityClass string

const (
	AmbiguityNone        AmbiguityClass = "NONE"
	AmbiguityMetric      AmbiguityClass = "METRIC"
	AmbiguityDimension   AmbiguityClass = "DIMENSION"
	AmbiguityMember      AmbiguityClass = "MEMBER"
	AmbiguityCrossDomain AmbiguityClass = "CROSS_DOMAIN"
	AmbiguityMultiple    AmbiguityClass = "MULTIPLE"
)

type MentionKind string

const (
	MentionMetric    MentionKind = "METRIC"
	MentionDimension MentionKind = "DIMENSION"
	MentionMember    MentionKind = "MEMBER"
)

// CalibrationFeatures are trusted, normalized system features. There is no
// field for a model-reported confidence value.
type CalibrationFeatures struct {
	CandidateScore  float64 `json:"candidateScore"`
	CandidateMargin float64 `json:"candidateMargin"`
	ExactScore      float64 `json:"exactScore"`
	LexicalScore    float64 `json:"lexicalScore"`
	VectorScore     float64 `json:"vectorScore"`
	GraphScore      float64 `json:"graphScore"`
	RuleScore       float64 `json:"ruleScore"`
	RetrievalRank   int     `json:"retrievalRank"`
}

func (features CalibrationFeatures) Validate() error {
	values := []struct {
		name  string
		value float64
	}{
		{"candidateScore", features.CandidateScore},
		{"candidateMargin", features.CandidateMargin},
		{"exactScore", features.ExactScore},
		{"lexicalScore", features.LexicalScore},
		{"vectorScore", features.VectorScore},
		{"graphScore", features.GraphScore},
		{"ruleScore", features.RuleScore},
	}
	for _, feature := range values {
		if math.IsNaN(feature.value) || math.IsInf(feature.value, 0) || feature.value < 0 || feature.value > 1 {
			return fmt.Errorf("%s must be a finite number between 0 and 1", feature.name)
		}
	}
	if features.RetrievalRank < 1 || features.RetrievalRank > MaxCalibrationRank {
		return fmt.Errorf("retrievalRank must be between 1 and %d", MaxCalibrationRank)
	}
	return nil
}

// CalibrationExample omits question text and keeps only stable audit
// identities, the original mention span, trusted features and its label.
type CalibrationExample struct {
	CaseID                   askdata.ID                   `json:"caseId"`
	DomainID                 askdata.ID                   `json:"domainId"`
	Complexity               ComplexityClass              `json:"complexity"`
	Ambiguity                AmbiguityClass               `json:"ambiguity"`
	MentionKind              MentionKind                  `json:"mentionKind"`
	MentionSpan              understanding.Span           `json:"mentionSpan"`
	ObjectVersionID          askdata.ID                   `json:"objectVersionId"`
	ParentDimensionVersionID *askdata.ID                  `json:"parentDimensionVersionId"`
	Role                     *understanding.DimensionRole `json:"role"`
	Features                 CalibrationFeatures          `json:"features"`
	Correct                  bool                         `json:"correct"`
}

type CalibrationInputs struct {
	Training   []CalibrationExample `json:"training"`
	Validation []CalibrationExample `json:"validation"`
}
