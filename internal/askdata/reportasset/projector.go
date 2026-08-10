package reportasset

import (
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/search"
)

var ErrAssetIneligible = errors.New("report semantic asset is not eligible for projection")

type GraphVertex struct {
	Type        string              `json:"type"`
	ID          askdata.ID          `json:"id"`
	ReleaseID   askdata.ID          `json:"releaseId"`
	ReleaseHash askdata.ContentHash `json:"releaseHash"`
}

type GraphEdge struct {
	Type        string              `json:"type"`
	FromID      askdata.ID          `json:"fromId"`
	ToID        askdata.ID          `json:"toId"`
	ReleaseID   askdata.ID          `json:"releaseId"`
	ReleaseHash askdata.ContentHash `json:"releaseHash"`
}

type Projection struct {
	SearchDocument search.Document `json:"searchDocument"`
	Vertices       []GraphVertex   `json:"vertices"`
	Edges          []GraphEdge     `json:"edges"`
}

func BuildProjection(candidate Candidate) (Projection, Validation, error) {
	validation := Validate(candidate)
	if !validation.Eligible {
		return Projection{}, validation, ErrAssetIneligible
	}
	metrics, dimensions, members := semanticObjectIDs(candidate)
	document, err := search.BuildReportAssetDocument(search.ReportAssetDocumentInput{
		ObjectVersionID: candidate.ID, ReportID: candidate.ReportID,
		ReportVersionID: candidate.ReportVersionID, SemanticReleaseID: candidate.SemanticIR.SemanticReleaseID,
		ReportTitle: candidate.ReportTitle, ReportDescription: candidate.ReportDescription,
		SectionPurpose: candidate.SectionPurpose, BlockTitle: candidate.BlockTitle,
		ComponentType: candidate.ComponentType, ComponentVersion: candidate.ComponentVersion,
		NarrativeRole: candidate.NarrativeRole, MetricVersionIDs: metrics,
		DimensionVersionIDs: dimensions, MemberVersionIDs: members, Sensitivity: candidate.Sensitivity,
	})
	if err != nil {
		return Projection{}, validation, err
	}
	releaseID, releaseHash := candidate.SemanticIR.SemanticReleaseID, candidate.SemanticIR.SemanticContentHash
	projection := Projection{SearchDocument: document, Vertices: []GraphVertex{
		{Type: "REPORT_VERSION", ID: candidate.ReportVersionID, ReleaseID: releaseID, ReleaseHash: releaseHash},
		{Type: "REPORT_COMPONENT", ID: candidate.ID, ReleaseID: releaseID, ReleaseHash: releaseHash},
	}}
	projection.Edges = append(projection.Edges, GraphEdge{Type: "REPORT_HAS_COMPONENT", FromID: candidate.ReportVersionID, ToID: candidate.ID, ReleaseID: releaseID, ReleaseHash: releaseHash})
	for _, id := range metrics {
		projection.Edges = append(projection.Edges, GraphEdge{Type: "REPORT_USES_METRIC", FromID: candidate.ID, ToID: id, ReleaseID: releaseID, ReleaseHash: releaseHash})
	}
	for _, id := range dimensions {
		projection.Edges = append(projection.Edges, GraphEdge{Type: "REPORT_GROUPS_BY_DIMENSION", FromID: candidate.ID, ToID: id, ReleaseID: releaseID, ReleaseHash: releaseHash})
	}
	for _, id := range members {
		projection.Edges = append(projection.Edges, GraphEdge{Type: "REPORT_FILTERS_MEMBER", FromID: candidate.ID, ToID: id, ReleaseID: releaseID, ReleaseHash: releaseHash})
	}
	projection.Edges = append(projection.Edges, GraphEdge{Type: "REPORT_USES_MODEL", FromID: candidate.ID, ToID: candidate.SemanticIR.ModelVersionID, ReleaseID: releaseID, ReleaseHash: releaseHash})
	return projection, validation, nil
}

func semanticObjectIDs(candidate Candidate) (metrics, dimensions, members []askdata.ID) {
	metricSet, dimensionSet, memberSet := map[askdata.ID]struct{}{}, map[askdata.ID]struct{}{}, map[askdata.ID]struct{}{}
	for _, item := range candidate.SemanticIR.Metrics {
		metricSet[item.MetricVersionID] = struct{}{}
	}
	for _, item := range candidate.SemanticIR.GroupBy {
		dimensionSet[item.DimensionVersionID] = struct{}{}
	}
	for _, item := range candidate.SemanticIR.Filters {
		dimensionSet[item.DimensionVersionID] = struct{}{}
		for _, id := range item.MemberVersionIDs {
			memberSet[id] = struct{}{}
		}
	}
	if candidate.SemanticIR.TimeRange != nil {
		dimensionSet[candidate.SemanticIR.TimeRange.DimensionVersionID] = struct{}{}
	}
	metrics, dimensions, members = mapKeys(metricSet), mapKeys(dimensionSet), mapKeys(memberSet)
	return
}

func mapKeys(values map[askdata.ID]struct{}) []askdata.ID {
	result := make([]askdata.ID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
