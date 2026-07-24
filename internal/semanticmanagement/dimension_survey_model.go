package semanticmanagement

import (
	"context"
	"encoding/json"
	"time"
)

const DimensionSurveyVersion = "dws-dimension-survey-v1"

type DimensionSurveyCandidate struct {
	ID                          string           `json:"id"`
	SurveyRunID                 string           `json:"surveyRunId"`
	DatasetID                   string           `json:"datasetId"`
	DatasetVersionID            string           `json:"datasetVersionId"`
	SchemaHash                  string           `json:"schemaHash"`
	MaterializationID           string           `json:"materializationId"`
	MaterializationSnapshotHash string           `json:"materializationSnapshotHash"`
	MaterializationRowCount     int64            `json:"materializationRowCount"`
	FieldID                     string           `json:"fieldId"`
	FieldCode                   string           `json:"fieldCode"`
	FieldRole                   string           `json:"fieldRole"`
	CanonicalType               string           `json:"canonicalType"`
	SemanticType                string           `json:"semanticType"`
	RiskHighCardinality         bool             `json:"riskHighCardinality"`
	RiskSensitive               bool             `json:"riskSensitive"`
	Evidence                    json.RawMessage  `json:"evidence"`
	ProposedCode                string           `json:"proposedCode"`
	ProposedName                string           `json:"proposedName"`
	ProposedDescription         string           `json:"proposedDescription"`
	ProposedDimensionType       string           `json:"proposedDimensionType"`
	ProposedMemberIndexPolicy   string           `json:"proposedMemberIndexPolicy"`
	ProposedHighCardinality     bool             `json:"proposedHighCardinality"`
	ProposedSensitive           bool             `json:"proposedSensitive"`
	Status                      string           `json:"status"`
	Version                     int64            `json:"version"`
	AcceptedDimensionID         string           `json:"acceptedDimensionId,omitempty"`
	DecisionReason              string           `json:"decisionReason,omitempty"`
	GeneratedBy                 string           `json:"generatedBy"`
	UpdatedBy                   string           `json:"updatedBy"`
	ReviewedBy                  string           `json:"reviewedBy,omitempty"`
	ReviewedAt                  *time.Time       `json:"reviewedAt,omitempty"`
	CreatedAt                   time.Time        `json:"createdAt"`
	UpdatedAt                   time.Time        `json:"updatedAt"`
	Profile                     DimensionProfile `json:"profile"`
}

type DimensionSurveyFilter struct {
	Page
	DatasetID        string
	DatasetVersionID string
	Status           string
	FieldRole        string
}

type UpdateDimensionSurveyCandidateInput struct {
	ExpectedVersion   int64  `json:"expectedVersion"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	DimensionType     string `json:"dimensionType"`
	MemberIndexPolicy string `json:"memberIndexPolicy"`
	HighCardinality   bool   `json:"highCardinality"`
	Sensitive         bool   `json:"sensitive"`
}

type DimensionSurveyDecisionInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason,omitempty"`
}

type DimensionSurveyAcceptance struct {
	Candidate         DimensionSurveyCandidate `json:"candidate"`
	Dimension         Dimension                `json:"dimension"`
	MemberRefreshJob  *RefreshJob              `json:"memberRefreshJob,omitempty"`
	MemberSearchReady bool                     `json:"memberSearchReady"`
	NextAction        string                   `json:"nextAction"`
}

type DimensionSurveyStore interface {
	ListDimensionSurveyCandidates(
		context.Context,
		string,
		DimensionSurveyFilter,
	) ([]DimensionSurveyCandidate, int, error)
	GetDimensionSurveyCandidate(
		context.Context,
		string,
		string,
	) (DimensionSurveyCandidate, error)
	UpdateDimensionSurveyCandidate(
		context.Context,
		string,
		string,
		string,
		int64,
		PreparedDimension,
	) (DimensionSurveyCandidate, error)
	AcceptDimensionSurveyCandidate(
		context.Context,
		string,
		string,
		string,
		int64,
		PreparedDimension,
	) (DimensionSurveyAcceptance, error)
	RejectDimensionSurveyCandidate(
		context.Context,
		string,
		string,
		string,
		int64,
		string,
	) (DimensionSurveyCandidate, error)
}
