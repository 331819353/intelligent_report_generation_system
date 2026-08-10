package compiler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type ChangeKind string

const (
	ChangeMetricVersion     ChangeKind = "METRIC_VERSION"
	ChangeDimensionVersion  ChangeKind = "DIMENSION_VERSION"
	ChangeMemberVersion     ChangeKind = "MEMBER_VERSION"
	ChangeDatasetVersion    ChangeKind = "DATASET_VERSION"
	ChangeComponentTemplate ChangeKind = "COMPONENT_TEMPLATE"
	ChangeSemanticRelease   ChangeKind = "SEMANTIC_RELEASE"
)

type ImpactSeverity string

const (
	SeverityBreaking      ImpactSeverity = "BREAKING"
	SeverityCompatible    ImpactSeverity = "COMPATIBLE"
	SeverityInformational ImpactSeverity = "INFORMATIONAL"
)

var ErrInvalidChangeSpec = errors.New("invalid report impact change spec")

type ChangeSpec struct {
	Kind          ChangeKind `json:"kind"`
	ObjectID      string     `json:"objectId"`
	ChangedFields []string   `json:"changedFields,omitempty"`
}

func (change ChangeSpec) Validate() error {
	switch change.Kind {
	case ChangeMetricVersion, ChangeDimensionVersion, ChangeMemberVersion,
		ChangeDatasetVersion, ChangeSemanticRelease:
		parsed, err := uuid.Parse(change.ObjectID)
		if err != nil || parsed.String() != change.ObjectID {
			return ErrInvalidChangeSpec
		}
	case ChangeComponentTemplate:
		parts := strings.Split(change.ObjectID, "@")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" ||
			len(change.ObjectID) > 512 {
			return ErrInvalidChangeSpec
		}
	default:
		return ErrInvalidChangeSpec
	}
	return nil
}

type ReportImpact struct {
	ReportID        askdata.ID   `json:"reportId"`
	ReportName      string       `json:"reportName"`
	OwnerID         askdata.ID   `json:"ownerId"`
	ComponentIDs    []askdata.ID `json:"componentIds"`
	VersionIDs      []askdata.ID `json:"versionIds,omitempty"`
	DraftAffected   bool         `json:"draftAffected"`
	PublishedImpact bool         `json:"publishedImpact"`
}

type ImpactSource interface {
	FindReportImpacts(context.Context, askdata.ID, ChangeSpec) ([]ReportImpact, error)
}

type ImpactAnalyzer struct{ source ImpactSource }

func NewImpactAnalyzer(source ImpactSource) *ImpactAnalyzer { return &ImpactAnalyzer{source: source} }

func (analyzer *ImpactAnalyzer) AnalyzeImpact(
	ctx context.Context, tenantID askdata.ID, change ChangeSpec,
) ([]ReportImpact, ImpactSeverity, error) {
	if analyzer == nil || analyzer.source == nil || tenantID.Validate() != nil || change.Validate() != nil {
		return nil, "", ErrInvalidChangeSpec
	}
	items, err := analyzer.source.FindReportImpacts(ctx, tenantID, change)
	if err != nil {
		return nil, "", err
	}
	return normalizeReportImpacts(items), ClassifyImpact(change.ChangedFields), nil
}

var breakingChangeFields = map[string]struct{}{
	"formula": {}, "formulaast": {}, "defaultfilter": {}, "defaultfilters": {},
	"defaultfiltersast": {}, "dedupkey": {}, "deduplicationkey": {}, "additivity": {},
	"nonadditivedimensions": {}, "timecontract": {}, "timegrain": {},
	"incompleteperiodpolicy": {}, "semiadditivetimeaggregation": {},
}

var informationalChangeFields = map[string]struct{}{
	"description": {}, "alias": {}, "aliases": {}, "label": {}, "labels": {},
}

func ClassifyImpact(fields []string) ImpactSeverity {
	if len(fields) == 0 {
		return SeverityBreaking
	}
	onlyInformational := true
	for _, field := range fields {
		normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(field)))
		if _, breaking := breakingChangeFields[normalized]; breaking {
			return SeverityBreaking
		}
		if _, informational := informationalChangeFields[normalized]; !informational {
			onlyInformational = false
		}
	}
	if onlyInformational {
		return SeverityInformational
	}
	return SeverityCompatible
}

func normalizeReportImpacts(items []ReportImpact) []ReportImpact {
	type accumulator struct {
		item       ReportImpact
		components map[askdata.ID]struct{}
		versions   map[askdata.ID]struct{}
	}
	byReport := map[askdata.ID]*accumulator{}
	for _, item := range items {
		entry := byReport[item.ReportID]
		if entry == nil {
			entry = &accumulator{item: item, components: map[askdata.ID]struct{}{}, versions: map[askdata.ID]struct{}{}}
			entry.item.ComponentIDs, entry.item.VersionIDs = nil, nil
			byReport[item.ReportID] = entry
		}
		entry.item.DraftAffected = entry.item.DraftAffected || item.DraftAffected
		entry.item.PublishedImpact = entry.item.PublishedImpact || item.PublishedImpact
		for _, id := range item.ComponentIDs {
			entry.components[id] = struct{}{}
		}
		for _, id := range item.VersionIDs {
			entry.versions[id] = struct{}{}
		}
	}
	result := make([]ReportImpact, 0, len(byReport))
	for _, entry := range byReport {
		for id := range entry.components {
			entry.item.ComponentIDs = append(entry.item.ComponentIDs, id)
		}
		for id := range entry.versions {
			entry.item.VersionIDs = append(entry.item.VersionIDs, id)
		}
		sort.Slice(entry.item.ComponentIDs, func(i, j int) bool { return entry.item.ComponentIDs[i] < entry.item.ComponentIDs[j] })
		sort.Slice(entry.item.VersionIDs, func(i, j int) bool { return entry.item.VersionIDs[i] < entry.item.VersionIDs[j] })
		result = append(result, entry.item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ReportID < result[j].ReportID })
	return result
}

type PostgresImpactSource struct{ pool *pgxpool.Pool }

func NewPostgresImpactSource(pool *pgxpool.Pool) *PostgresImpactSource {
	return &PostgresImpactSource{pool: pool}
}

// reportImpactSQL intentionally reads only the normalized dependency indexes.
// Published report definition_json and mutable draft definition_json are never
// read or scanned by impact analysis.
const reportImpactSQL = `
SELECT report.id::text,report.name,report.owner_user_id::text,
       dependency.component_ids,version.id::text,false,true
FROM platform.report_version_dependencies AS dependency
JOIN platform.report_versions AS version
  ON version.id=dependency.report_version_id AND version.report_id=dependency.report_id
 AND version.tenant_id=dependency.tenant_id
JOIN platform.reports AS report
  ON report.id=dependency.report_id AND report.tenant_id=dependency.tenant_id
WHERE dependency.tenant_id=$1 AND dependency.dependency_type=$2 AND dependency.dependency_id=$3
UNION ALL
SELECT report.id::text,report.name,report.owner_user_id::text,
       dependency.component_ids,NULL::text,true,false
FROM platform.report_draft_dependencies AS dependency
JOIN platform.reports AS report
  ON report.id=dependency.report_id AND report.tenant_id=dependency.tenant_id
WHERE dependency.tenant_id=$1 AND dependency.dependency_type=$2 AND dependency.dependency_id=$3
ORDER BY 1,6,5 NULLS LAST`

func (source *PostgresImpactSource) FindReportImpacts(
	ctx context.Context, tenantID askdata.ID, change ChangeSpec,
) (result []ReportImpact, err error) {
	if source == nil || source.pool == nil || tenantID.Validate() != nil || change.Validate() != nil {
		return nil, ErrInvalidChangeSpec
	}
	err = database.WithTenantTx(ctx, source.pool, string(tenantID), func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, reportImpactSQL, tenantID, change.Kind, change.ObjectID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item ReportImpact
			var components []string
			var versionID *string
			if scanErr := rows.Scan(&item.ReportID, &item.ReportName, &item.OwnerID, &components,
				&versionID, &item.DraftAffected, &item.PublishedImpact); scanErr != nil {
				return scanErr
			}
			for _, id := range components {
				item.ComponentIDs = append(item.ComponentIDs, askdata.ID(id))
			}
			if versionID != nil {
				item.VersionIDs = []askdata.ID{askdata.ID(*versionID)}
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("query report dependency impact: %w", err)
	}
	return normalizeReportImpacts(result), nil
}
