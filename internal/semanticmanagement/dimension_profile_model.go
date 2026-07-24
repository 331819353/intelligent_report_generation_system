package semanticmanagement

import (
	"context"
	"time"
)

const (
	DimensionProfileVersion = "dws-dimension-profile-v1"
	DimensionPolicyVersion  = "dimension-member-policy-v1"
)

type DimensionProfile struct {
	ID                           string     `json:"id"`
	Status                       string     `json:"status"`
	ProfileVersion               string     `json:"profileVersion"`
	PolicyVersion                string     `json:"policyVersion"`
	MaterializationID            string     `json:"materializationId"`
	MaterializationSnapshotHash  string     `json:"materializationSnapshotHash"`
	ExpectedRowCount             int64      `json:"expectedRowCount"`
	RowCount                     *int64     `json:"rowCount,omitempty"`
	NonNullCount                 *int64     `json:"nonNullCount,omitempty"`
	NullCount                    *int64     `json:"nullCount,omitempty"`
	DistinctCount                *int64     `json:"distinctCount,omitempty"`
	DistinctOverflow             bool       `json:"distinctOverflow"`
	DistinctCap                  int        `json:"distinctCap"`
	DistinctRatio                *float64   `json:"distinctRatio,omitempty"`
	RiskHighCardinality          bool       `json:"riskHighCardinality"`
	RecommendedMemberIndexPolicy string     `json:"recommendedMemberIndexPolicy,omitempty"`
	ResultCode                   string     `json:"resultCode,omitempty"`
	EvidenceHash                 string     `json:"evidenceHash,omitempty"`
	Attempt                      int        `json:"attempt"`
	MaxAttempts                  int        `json:"maxAttempts"`
	CreatedAt                    time.Time  `json:"createdAt"`
	UpdatedAt                    time.Time  `json:"updatedAt"`
	StartedAt                    *time.Time `json:"startedAt,omitempty"`
	CompletedAt                  *time.Time `json:"completedAt,omitempty"`
}

type DimensionProfileJob struct {
	DimensionProfile
	TenantID            string
	DatasetID           string
	DatasetVersionID    string
	SchemaHash          string
	FieldID             string
	FieldCode           string
	FieldRole           string
	CanonicalType       string
	SemanticType        string
	HighRatioThreshold  float64
	HighRatioMinNonNull int64
	TimeoutSeconds      int
	WorkMemKB           int
	TempFileLimitKB     int
	RequestedBy         string
	NextAttemptAt       time.Time
	LeaseOwner          string
	LeaseToken          string
	LeaseExpiresAt      time.Time
}

type DimensionProfileObservation struct {
	RowCount            int64
	NonNullCount        int64
	NullCount           int64
	DistinctCount       int64
	DistinctOverflow    bool
	DistinctRatio       float64
	RiskHighCardinality bool
	EvidenceHash        string
	PolicySkipped       bool
	PolicySkipCode      string
}

type DimensionProfileStore interface {
	ListProfileTenantIDs(context.Context) ([]string, error)
	ClaimDimensionProfile(
		context.Context,
		string,
		string,
		time.Duration,
	) (*DimensionProfileJob, error)
	HeartbeatDimensionProfile(
		context.Context,
		DimensionProfileJob,
		time.Duration,
	) (DimensionProfileJob, error)
	MeasureDimensionProfile(
		context.Context,
		DimensionProfileJob,
	) (DimensionProfileObservation, error)
	CompleteDimensionProfile(
		context.Context,
		DimensionProfileJob,
		DimensionProfileObservation,
	) error
	FailDimensionProfile(
		context.Context,
		DimensionProfileJob,
		string,
	) error
}
