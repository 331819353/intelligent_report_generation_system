package reportasset

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
)

type GraphObjectRef struct {
	Type                string
	ObjectID, VersionID askdata.ID
	Version             int
}
type ReportGraphProjection struct {
	TenantID, DomainID, ReportID, ReportVersionID, AssetID, ComponentID askdata.ID
	ReleaseHash, ComponentContentHash                                   askdata.ContentHash
	Model                                                               GraphObjectRef
	Metrics, Dimensions, Members                                        []GraphObjectRef
}

func (projection ReportGraphProjection) Validate() error {
	for _, id := range []askdata.ID{projection.TenantID, projection.DomainID, projection.ReportID, projection.ReportVersionID, projection.AssetID, projection.ComponentID} {
		if id.Validate() != nil {
			return ErrAssetIneligible
		}
	}
	if projection.ReleaseHash.Validate() != nil || projection.ComponentContentHash.Validate() != nil || projection.Model.Version < 1 {
		return ErrAssetIneligible
	}
	return nil
}

type NebulaReportGraphWriter struct{ executor graph.QueryExecutor }

func NewNebulaReportGraphWriter(executor graph.QueryExecutor) (*NebulaReportGraphWriter, error) {
	if executor == nil {
		return nil, ErrAssetIneligible
	}
	return &NebulaReportGraphWriter{executor: executor}, nil
}
func (writer *NebulaReportGraphWriter) Upsert(ctx context.Context, p ReportGraphProjection) error {
	if writer == nil || p.Validate() != nil {
		return ErrAssetIneligible
	}
	reportVID, componentVID := reportGraphVID(p.TenantID, "report_version", p.ReportVersionID), reportGraphVID(p.TenantID, "report_component", p.AssetID)
	scope := map[string]interface{}{"tenant": string(p.TenantID), "domain": string(p.DomainID), "release": string(p.ReleaseHash), "report": string(p.ReportID), "report_version": string(p.ReportVersionID), "asset": string(p.AssetID), "component": string(p.ComponentID), "content": string(p.ComponentContentHash)}
	statements := []string{fmt.Sprintf("INSERT VERTEX report_version(tenant_id,domain_id,release_hash,report_id,report_version_id) VALUES %s:($tenant,$domain,$release,$report,$report_version)", strconv.Quote(reportVID)), fmt.Sprintf("INSERT VERTEX report_component(tenant_id,domain_id,release_hash,report_id,report_version_id,asset_id,component_id,content_hash) VALUES %s:($tenant,$domain,$release,$report,$report_version,$asset,$component,$content)", strconv.Quote(componentVID)), fmt.Sprintf("INSERT EDGE REPORT_HAS_COMPONENT(tenant_id,domain_id,release_hash) VALUES %s->%s:($tenant,$domain,$release)", strconv.Quote(reportVID), strconv.Quote(componentVID))}
	appendEdges := func(edge string, refs []GraphObjectRef) error {
		for _, ref := range refs {
			vid, err := semanticVID(p.TenantID, ref)
			if err != nil {
				return err
			}
			statements = append(statements, fmt.Sprintf("INSERT EDGE %s(tenant_id,domain_id,release_hash) VALUES %s->%s:($tenant,$domain,$release)", edge, strconv.Quote(componentVID), strconv.Quote(vid)))
		}
		return nil
	}
	if err := appendEdges("REPORT_USES_MODEL", []GraphObjectRef{p.Model}); err != nil {
		return err
	}
	if err := appendEdges("REPORT_USES_METRIC", p.Metrics); err != nil {
		return err
	}
	if err := appendEdges("REPORT_GROUPS_BY_DIMENSION", p.Dimensions); err != nil {
		return err
	}
	if err := appendEdges("REPORT_FILTERS_MEMBER", p.Members); err != nil {
		return err
	}
	for _, statement := range statements {
		if err := writer.execute(ctx, statement, scope); err != nil {
			return err
		}
	}
	return nil
}
func (writer *NebulaReportGraphWriter) Remove(ctx context.Context, p ReportGraphProjection) error {
	if writer == nil || p.TenantID.Validate() != nil || p.AssetID.Validate() != nil {
		return ErrAssetIneligible
	}
	return writer.execute(ctx, "DELETE VERTEX "+strconv.Quote(reportGraphVID(p.TenantID, "report_component", p.AssetID))+" WITH EDGE", map[string]interface{}{})
}
func (writer *NebulaReportGraphWriter) execute(ctx context.Context, statement string, parameters map[string]interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := writer.executor.ExecuteWithParameter(statement, parameters)
	if err != nil {
		return err
	}
	if result == nil || !result.IsSucceed() {
		return errors.New("report graph projection failed")
	}
	return ctx.Err()
}
func reportGraphVID(tenant askdata.ID, kind string, id askdata.ID) string {
	return strings.Join([]string{string(tenant), kind, string(id)}, ":")
}
func semanticVID(tenant askdata.ID, ref GraphObjectRef) (string, error) {
	var kind graph.ObjectType
	switch ref.Type {
	case "SEMANTIC_MODEL":
		kind = graph.ObjectTypeSemanticModel
	case "METRIC":
		kind = graph.ObjectTypeMetric
	case "DIMENSION":
		kind = graph.ObjectTypeDimension
	case "MEMBER":
		kind = graph.ObjectTypeMember
	default:
		return "", ErrAssetIneligible
	}
	return graph.BuildVID(tenant, kind, graph.ObjectVersionRef{ObjectID: ref.ObjectID, VersionID: ref.VersionID, Version: ref.Version})
}
