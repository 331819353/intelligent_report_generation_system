package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/store"
)

const maxTabularExportCells = 1_000_000

// RuntimeExportResultSource executes every selected page through the same
// immutable, viewer-scoped runtime used by the API. Heterogeneous component
// tables are represented as deterministic long-form cells so CSV and XLSX do
// not silently discard components or invent a union schema.
type RuntimeExportResultSource struct {
	pool     *pgxpool.Pool
	loader   reportruntime.Loader
	executor reportruntime.QueryExecutor
}

func NewRuntimeExportResultSource(
	pool *pgxpool.Pool,
	loader reportruntime.Loader,
	executor reportruntime.QueryExecutor,
) (*RuntimeExportResultSource, error) {
	if pool == nil || loader.Versions == nil || executor == nil {
		return nil, errors.New("report tabular export runtime is unavailable")
	}
	return &RuntimeExportResultSource{pool: pool, loader: loader, executor: executor}, nil
}

func (source *RuntimeExportResultSource) Rows(
	ctx context.Context,
	claim ExportClaim,
) (ExportRows, error) {
	identity := store.Identity{
		TenantID: claim.TenantID, ActorID: claim.RequestedBy, DomainID: claim.DomainID,
	}
	if source == nil || source.pool == nil || source.executor == nil || identity.Validate() != nil ||
		claim.ReportID.Validate() != nil || claim.ReportVersionID.Validate() != nil || claim.AsOf.IsZero() {
		return ExportRows{}, errors.New("report tabular export claim is invalid")
	}
	versionNo := 0
	if err := database.WithTenantTx(ctx, source.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version_no FROM platform.report_versions
			WHERE id=$1 AND report_id=$2`, claim.ReportVersionID, claim.ReportID).Scan(&versionNo)
	}); err != nil {
		return ExportRows{}, err
	}
	executionContext := database.WithAccessContext(
		ctx, string(identity.ActorID), string(identity.DomainID),
	)
	loaded, err := source.loader.Load(executionContext, identity, claim.ReportID, &versionNo)
	if err != nil || loaded.VersionID != claim.ReportVersionID {
		return ExportRows{}, errors.Join(err, errors.New("report export version is unavailable"))
	}
	location, err := reportruntime.RuntimeTimezone(loaded.Definition)
	if err != nil || location.String() != claim.Timezone {
		return ExportRows{}, errors.Join(err, errors.New("report export timezone does not match the version"))
	}
	if len(loaded.Definition.Pages) == 0 {
		return ExportRows{}, errors.New("report export contains no pages")
	}
	rawFilters, err := exportFilterValues(claim.FilterSummary)
	if err != nil {
		return ExportRows{}, err
	}
	// An export runs the same session as the interactive runtime, so a PDF or
	// XLSX can never be produced by a pipeline that skipped a step the viewer
	// path applies.
	session, err := reportruntime.NewSession(identity, reportruntime.PublishedTarget(loaded), claim.AsOf)
	if err != nil {
		return ExportRows{}, err
	}
	resolved, err := (reportruntime.HTTPPlanInput{
		PageID: loaded.Definition.Pages[0].ID, FilterValues: rawFilters,
	}).Resolve(loaded.Definition, session.AsOf, session.Location, session.PolicyScopeHash)
	if err != nil {
		return ExportRows{}, err
	}
	resolved.Export = true
	resolved.PageIDs = append([]askdata.ID(nil), claim.PageIDs...)
	plan, err := session.PlanResolved(resolved)
	if err != nil {
		return ExportRows{}, err
	}
	results := session.Execute(
		executionContext, plan, source.executor,
		loaded.Definition.RuntimePolicy.MaxConcurrentQueries,
	)
	return flattenExportResults(plan, results)
}

func exportFilterValues(values map[string]any) (map[askdata.ID]json.RawMessage, error) {
	result := make(map[askdata.ID]json.RawMessage, len(values))
	for key, value := range values {
		id := askdata.ID(key)
		if id.Validate() != nil {
			return nil, errors.New("report export filter ID is invalid")
		}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > 16<<10 {
			return nil, errors.New("report export filter value is invalid")
		}
		result[id] = raw
	}
	return result, nil
}

func flattenExportResults(
	plan reportruntime.ExecutionPlan,
	results []reportruntime.ComponentResult,
) (ExportRows, error) {
	positions := make(map[askdata.ID]reportruntime.ComponentPlan, len(plan.Components))
	for _, component := range plan.Components {
		positions[component.ComponentID] = component
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ComponentID < results[j].ComponentID })
	rows := [][]any{}
	for _, component := range results {
		position, exists := positions[component.ComponentID]
		if !exists {
			return ExportRows{}, errors.New("report export result has an unknown component")
		}
		switch component.State {
		case reportruntime.StateReady, reportruntime.StateEmpty, reportruntime.StatePartial:
		default:
			return ExportRows{}, fmt.Errorf("report export component %s failed with %s", component.ComponentID, component.State)
		}
		if component.Result == nil {
			continue
		}
		plans := component.Result.Plans
		if len(plans) == 0 {
			plans = []reportruntime.QueryPlanResult{{
				Role: "CURRENT", Columns: component.Result.Columns, Rows: component.Result.Rows,
			}}
		}
		for _, resultPlan := range plans {
			for rowIndex, row := range resultPlan.Rows {
				if len(row) != len(resultPlan.Columns) {
					return ExportRows{}, errors.New("report export query result shape is invalid")
				}
				for columnIndex, value := range row {
					if len(rows) >= maxTabularExportCells {
						return ExportRows{}, errors.New("report export exceeds the tabular cell limit")
					}
					rows = append(rows, []any{
						position.PageID, component.ComponentID, resultPlan.Role,
						rowIndex + 1, resultPlan.Columns[columnIndex], value, component.Result.Partial,
					})
				}
			}
		}
	}
	return ExportRows{
		Columns: []string{"page_id", "component_id", "plan_role", "row_number", "column", "value", "partial"},
		Rows:    rows,
	}, nil
}
