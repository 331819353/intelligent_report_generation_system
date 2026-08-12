package compiler

import (
	"errors"

	"intelligent-report-generation-system/internal/askdata"
)

const ReportQuerySnapshotVersion = "add-to-report-query-snapshot-v1"

var ErrInvalidReportQuerySnapshot = errors.New("add-to-report query snapshot is invalid")

// ReportQuerySnapshot is the durable, audit-safe subset of QueryArtifact that
// the report product needs after an AskData worker has exited. It deliberately
// omits Dataset DSL documents and parameter definitions: the report binds the
// immutable semantic IR and plan hash, while its runtime recompiles under the
// current actor's authorization before execution.
type ReportQuerySnapshot struct {
	SchemaVersion      string                      `json:"schemaVersion"`
	PlanHash           askdata.ContentHash         `json:"planHash"`
	SemanticIRHash     askdata.ContentHash         `json:"semanticIrHash"`
	GraphPlanHash      askdata.ContentHash         `json:"graphPlanHash"`
	ResolvedTimeSpec   *ResolvedTimeSpec           `json:"resolvedTimeSpec,omitempty"`
	MetricAggregations []MetricAggregationContract `json:"metricAggregations"`
	Sources            []ReportQuerySource         `json:"sources"`
}

type ReportQuerySource struct {
	Role             QueryRole  `json:"role"`
	DatasetVersionID askdata.ID `json:"datasetVersionId"`
}

func NewReportQuerySnapshot(artifact QueryArtifact) (ReportQuerySnapshot, error) {
	if artifact.Validate() != nil {
		return ReportQuerySnapshot{}, ErrInvalidReportQuerySnapshot
	}
	result := ReportQuerySnapshot{
		SchemaVersion: ReportQuerySnapshotVersion,
		PlanHash:      artifact.PlanHash, SemanticIRHash: artifact.IRHash,
		GraphPlanHash:      artifact.GraphPlanHash,
		MetricAggregations: append([]MetricAggregationContract(nil), artifact.MetricAggregations...),
		Sources:            make([]ReportQuerySource, len(artifact.Plans)),
	}
	if artifact.ResolvedTimeSpec != nil {
		value := *artifact.ResolvedTimeSpec
		result.ResolvedTimeSpec = &value
	}
	for index, plan := range artifact.Plans {
		result.Sources[index] = ReportQuerySource{Role: plan.Role, DatasetVersionID: plan.Source.DatasetVersionID}
	}
	if result.Validate() != nil {
		return ReportQuerySnapshot{}, ErrInvalidReportQuerySnapshot
	}
	return result, nil
}

func (snapshot ReportQuerySnapshot) Validate() error {
	if snapshot.SchemaVersion != ReportQuerySnapshotVersion || snapshot.PlanHash.Validate() != nil ||
		snapshot.SemanticIRHash.Validate() != nil || snapshot.GraphPlanHash.Validate() != nil ||
		validateMetricAggregationContracts(snapshot.MetricAggregations) != nil || len(snapshot.Sources) < 1 ||
		len(snapshot.Sources) > 2 {
		return ErrInvalidReportQuerySnapshot
	}
	if snapshot.ResolvedTimeSpec != nil && validateResolvedTimeSpec(*snapshot.ResolvedTimeSpec) != nil {
		return ErrInvalidReportQuerySnapshot
	}
	for index, source := range snapshot.Sources {
		if source.DatasetVersionID.Validate() != nil ||
			(index == 0 && source.Role != QueryRoleCurrent) ||
			(index == 1 && source.Role != QueryRoleBaseline) {
			return ErrInvalidReportQuerySnapshot
		}
	}
	return nil
}
