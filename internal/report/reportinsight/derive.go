// Package reportinsight joins the report runtime to the Evidence Bundle
// contract: it executes a component and derives that component's evidence from
// the result the server itself produced.
package reportinsight

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/queryruntime"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/insight"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	"intelligent-report-generation-system/internal/report/store"
)

// EvidenceAlgorithmVersion identifies this derivation. It participates in
// staleness detection, so it must change whenever the mapping from a query
// result to analysis input changes in a way that could move a fact.
const EvidenceAlgorithmVersion = "report-evidence-derive-1.0.0"

type DraftReader interface {
	GetDraft(context.Context, store.Identity, askdata.ID) (store.Draft, error)
}

type EvidenceWriter interface {
	SaveEvidence(
		context.Context, store.Identity, askdata.ID, askdata.ID, insight.EvidenceBundle,
	) (insight.EvidenceRecord, error)
}

// MeasureContract supplies the governed metadata a fact must carry — notably
// the measure's declared unit, without which a quoted number is unverifiable.
type MeasureContract interface {
	VersionRollupContract(
		ctx context.Context, tenantID, datasetID, versionID string,
	) (queryruntime.VersionRollupContract, error)
}

// Deriver produces a component's Evidence Bundle from a live execution of that
// component against the current draft, under the caller's own data permissions.
type Deriver struct {
	Drafts   DraftReader
	Executor reportruntime.QueryExecutor
	Methods  *insight.Registry
	Evidence EvidenceWriter
	Measures MeasureContract
	// Now is injectable so derivation is testable without a clock.
	Now func() time.Time
}

type DeriveRequest struct {
	ReportID    askdata.ID
	ComponentID askdata.ID
	Method      insight.AnalysisMethod
	TopN        int
}

// Derive executes the component, computes its facts and stores the bundle.
//
// It deliberately takes no facts, values or hashes from the caller: the only
// thing a caller chooses is which component to analyse and by which registered
// method. Everything a published conclusion is later checked against comes from
// the executed result.
func (deriver Deriver) Derive(
	ctx context.Context,
	identity store.Identity,
	request DeriveRequest,
) (insight.EvidenceRecord, []insight.MarkerObject, error) {
	if deriver.Drafts == nil || deriver.Executor == nil || deriver.Methods == nil ||
		deriver.Evidence == nil || deriver.Measures == nil {
		return insight.EvidenceRecord{}, nil, errors.New("report evidence derivation is not configured")
	}
	if request.ReportID.Validate() != nil || request.ComponentID.Validate() != nil {
		return insight.EvidenceRecord{}, nil, errors.New("report evidence request is invalid")
	}
	if _, registered := deriver.Methods.Get(request.Method); !registered {
		return insight.EvidenceRecord{}, nil, fmt.Errorf("analysis method %q is not registered", request.Method)
	}

	draft, err := deriver.Drafts.GetDraft(ctx, identity, request.ReportID)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	component, page, found := locateComponent(draft.Definition, request.ComponentID)
	if !found {
		return insight.EvidenceRecord{}, nil, fmt.Errorf("component %q is not in the report", request.ComponentID)
	}
	roles, err := insight.BindingRoles(component.DataBinding)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	dataContext, err := boundDataContext(draft.Definition, component)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}

	now := time.Now().UTC()
	if deriver.Now != nil {
		now = deriver.Now().UTC()
	}
	target := reportruntime.DraftTarget(request.ReportID, draft.Definition, draft.DefinitionHash, draft.RevisionNo)
	session, err := reportruntime.NewSession(identity, target, now)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	plan, err := session.Plan(reportruntime.HTTPPlanInput{PageID: page})
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	planned := componentPlan(plan, request.ComponentID)
	if planned == nil || planned.Query == nil {
		return insight.EvidenceRecord{}, nil, errors.New("component has no executable query to derive evidence from")
	}
	results := session.Execute(ctx, reportruntime.ExecutionPlan{
		Components: []reportruntime.ComponentPlan{*planned},
	}, deriver.Executor, 1)
	if len(results) != 1 || results[0].Result == nil {
		code := "REPORT_EVIDENCE_EXECUTION_FAILED"
		if len(results) == 1 && results[0].ErrorCode != "" {
			code = results[0].ErrorCode
		}
		return insight.EvidenceRecord{}, nil, reportruntime.NewError(code, "无法执行该组件以生成证据", nil)
	}
	executed := results[0].Result
	if executed.Partial {
		// A fact computed over a truncated scan is not partial evidence, it is
		// wrong evidence, and it would then be cited as verified.
		return insight.EvidenceRecord{}, nil, reportruntime.NewError(
			"REPORT_EVIDENCE_SOURCE_TRUNCATED", "查询结果已被行数上限截断，据此生成的结论会失真", nil,
		)
	}

	// A fact must carry the measure's declared unit. If the dataset version does
	// not declare one, the number cannot be quoted as verified evidence.
	contract, err := deriver.Measures.VersionRollupContract(
		ctx, string(identity.TenantID), string(dataContext.DatasetID), string(dataContext.DatasetVersionID),
	)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	unit := strings.TrimSpace(contract.Measures[roles.Value].Unit)
	if unit == "" {
		return insight.EvidenceRecord{}, nil, reportruntime.NewError(
			"REPORT_EVIDENCE_UNIT_UNDECLARED",
			fmt.Sprintf("度量 %q 未在数据集中声明单位，无法作为可核验证据", roles.Value), nil,
		)
	}

	input, err := insight.BuildMethodInput(
		insight.ResultTable{Columns: executed.Columns, Rows: executed.Rows}, roles, request.TopN,
	)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	bundle, err := insight.BuildEvidence(deriver.Methods, insight.EvidenceRequest{
		SourceType:               insight.SourceDatasetQuery,
		DatasetVersionID:         dataContext.DatasetVersionID,
		DataSnapshotVersion:      string(executed.Hash),
		QueryPlanHash:            executed.Hash,
		FilterHash:               filterHash(planned.Query),
		AsOf:                     session.AsOf,
		ResolvedTimeRange:        reportTimeRange(session),
		MetricVersionID:          dataContext.DatasetVersionID,
		Unit:                     unit,
		Method:                   request.Method,
		EvidenceAlgorithmVersion: EvidenceAlgorithmVersion,
		Input:                    input,
	}, now)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	record, err := deriver.Evidence.SaveEvidence(ctx, identity, request.ReportID, request.ComponentID, bundle)
	if err != nil {
		return insight.EvidenceRecord{}, nil, err
	}
	return record, narrativeObjects(component, contract), nil
}

// narrativeObjects is the catalog of governed objects a narrative over this
// component may name: exactly the fields the component itself binds. Anything
// broader would let prose name a field the reader cannot see.
func narrativeObjects(
	component report.Component, contract queryruntime.VersionRollupContract,
) []insight.MarkerObject {
	if component.DataBinding == nil {
		return nil
	}
	bound := append(append([]report.FieldBinding(nil), component.DataBinding.Dimensions...),
		component.DataBinding.Measures...)
	objects := make([]insight.MarkerObject, 0, len(bound))
	seen := map[string]bool{}
	for _, binding := range bound {
		field, exists := contract.Fields[binding.Field]
		if !exists || seen[field.ID] || askdata.ID(field.ID).Validate() != nil {
			continue
		}
		seen[field.ID] = true
		objects = append(objects, insight.MarkerObject{
			ObjectID: askdata.ID(field.ID), Name: field.Name, Measure: field.Measure,
		})
	}
	return objects
}

func locateComponent(
	definition report.ReportDefinition, componentID askdata.ID,
) (report.Component, askdata.ID, bool) {
	for _, page := range definition.Pages {
		for _, section := range page.Sections {
			for _, block := range section.Blocks {
				for _, zone := range block.Zones {
					for _, slot := range zone.Slots {
						if slot.ComponentID != componentID {
							continue
						}
						for _, component := range definition.Components {
							if component.ID == componentID {
								return component, page.ID, true
							}
						}
					}
				}
			}
		}
	}
	return report.Component{}, "", false
}

func boundDataContext(
	definition report.ReportDefinition, component report.Component,
) (report.DataContext, error) {
	if component.DataBinding == nil || component.DataBinding.DataContextID == nil {
		return report.DataContext{}, errors.New("component has no bound data context")
	}
	for _, dataContext := range definition.DataContexts {
		if dataContext.ID == *component.DataBinding.DataContextID {
			return dataContext, nil
		}
	}
	return report.DataContext{}, errors.New("bound data context does not exist")
}

func componentPlan(plan reportruntime.ExecutionPlan, componentID askdata.ID) *reportruntime.ComponentPlan {
	for index := range plan.Components {
		if plan.Components[index].ComponentID == componentID {
			return &plan.Components[index]
		}
	}
	return nil
}

// filterHash records which filters the evidence was computed under, so evidence
// taken with one filter set is never treated as current for another.
func filterHash(query *reportruntime.QueryRequest) askdata.ContentHash {
	payload := ""
	for _, filter := range query.Filters {
		payload += fmt.Sprintf("%s\x00%s\x00%v\x1e", filter.DataContextID, filter.Field, filter.Value)
	}
	return askdata.HashBytes([]byte("report-evidence-filters-v1\x00" + payload))
}

// reportTimeRange records the window the evidence describes. A dataset-bound
// report carries no pinned semantic calendar, so the execution instant in the
// report's own business timezone is the honest bound.
func reportTimeRange(session reportruntime.Session) insight.ResolvedTimeRange {
	end := session.AsOf.In(session.Location)
	start := end.AddDate(-1, 0, 0)
	return insight.ResolvedTimeRange{
		Start:        start.Format(time.RFC3339Nano),
		EndExclusive: end.Format(time.RFC3339Nano),
		Timezone:     session.Location.String(),
	}
}
