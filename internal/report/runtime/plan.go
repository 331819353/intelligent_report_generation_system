package runtime

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

type PlanRequest struct {
	PageID           askdata.ID
	PageIDs          []askdata.ID
	VisibleBlockIDs  []askdata.ID
	ExpandedSlotIDs  []askdata.ID
	Mobile           bool
	Export           bool
	PolicyScopeHash  string
	FilterValues     map[askdata.ID]any
	TemporaryFilters []ResolvedFilter

	// Filters is retained as an internal compatibility alias for callers that
	// already hold server-resolved temporary filters. Public HTTP requests are
	// decoded into FilterValues and never populate this field directly.
	Filters []ResolvedFilter
}

func BuildExecutionPlan(definition report.ReportDefinition, request PlanRequest) (ExecutionPlan, error) {
	components := map[askdata.ID]report.Component{}
	contexts := map[askdata.ID]report.DataContext{}
	for _, component := range definition.Components {
		components[component.ID] = component
	}
	for _, dataContext := range definition.DataContexts {
		contexts[dataContext.ID] = dataContext
	}
	result := ExecutionPlan{Components: []ComponentPlan{}}
	for _, page := range definition.Pages {
		if !request.Export && page.ID != request.PageID {
			continue
		}
		if request.Export && len(request.PageIDs) != 0 && !slices.Contains(request.PageIDs, page.ID) {
			continue
		}
		mobileAllowed := map[askdata.ID]struct{}{}
		if request.Mobile {
			for _, block := range compiler.ToMobileLayout(page).Blocks {
				for _, slot := range block.Slots {
					mobileAllowed[slot.ID] = struct{}{}
				}
			}
		}
		for _, section := range page.Sections {
			for _, block := range section.Blocks {
				if !request.Export && len(request.VisibleBlockIDs) != 0 && !slices.Contains(request.VisibleBlockIDs, block.ID) {
					continue
				}
				for _, zone := range block.Zones {
					if zone.Layout.HeightMode == report.ZoneHeightHidden {
						continue
					}
					for slotIndex, slot := range zone.Slots {
						if request.Mobile {
							if _, allowed := mobileAllowed[slot.ID]; !allowed {
								continue
							}
						}
						if request.Mobile && !request.Export && block.Layout.Mobile.SlotMode == report.MobileSlotCollapse && slotIndex > 0 && !slices.Contains(request.ExpandedSlotIDs, slot.ID) {
							continue
						}
						if slot.ComponentID == "" {
							continue
						}
						component, exists := components[slot.ComponentID]
						if !exists {
							return ExecutionPlan{}, fmt.Errorf("component %q is missing", slot.ComponentID)
						}
						temporary := append([]ResolvedFilter(nil), request.TemporaryFilters...)
						temporary = append(temporary, request.Filters...)
						filters, err := ResolveFilters(
							definition, page.ID, section.ID, block.ID, component.ID,
							request.FilterValues, temporary,
						)
						if err != nil {
							return ExecutionPlan{}, fmt.Errorf("component %q filters: %w", component.ID, err)
						}
						query, err := buildQuery(component, contexts, request, filters)
						if err != nil {
							return ExecutionPlan{}, fmt.Errorf("component %q: %w", component.ID, err)
						}
						result.Components = append(result.Components, ComponentPlan{ComponentID: component.ID, PageID: page.ID, BlockID: block.ID, SlotID: slot.ID, Query: query})
					}
				}
			}
		}
	}
	return result, nil
}

func buildQuery(
	component report.Component,
	contexts map[askdata.ID]report.DataContext,
	request PlanRequest,
	filters []ResolvedFilter,
) (*QueryRequest, error) {
	if component.DataBinding == nil {
		return nil, nil
	}
	binding := component.DataBinding
	query := &QueryRequest{
		BindingMode: binding.BindingMode, PolicyScopeHash: request.PolicyScopeHash,
		Filters: append([]ResolvedFilter(nil), filters...),
	}
	switch binding.BindingMode {
	case report.BindingSemanticIR:
		if binding.SemanticQueryRef == nil {
			return nil, fmt.Errorf("SEMANTIC_IR binding is missing semanticQueryRef")
		}
		raw, err := json.Marshal(binding.SemanticQueryRef.SemanticIR)
		if err != nil {
			return nil, err
		}
		query.SemanticReleaseID = binding.SemanticQueryRef.SemanticReleaseID
		query.SemanticContentHash = binding.SemanticQueryRef.SemanticContentHash
		query.FixedQueryPlanHash = binding.SemanticQueryRef.QueryPlanHash
		if binding.SemanticQueryRef.SourceQuestionRunID != nil {
			query.SourceQuestionRunID = *binding.SemanticQueryRef.SourceQuestionRunID
		}
		if binding.SemanticQueryRef.CompilationArtifactID != nil {
			query.CompilationArtifactID = *binding.SemanticQueryRef.CompilationArtifactID
		}
		if (query.SourceQuestionRunID == "") == (query.CompilationArtifactID == "") {
			return nil, fmt.Errorf("SEMANTIC_IR binding requires exactly one governed compilation source")
		}
		query.SemanticIR = raw
		if binding.SemanticQueryRef.ResolvedTimeSpec != nil {
			query.ResolvedTimeSpec, err = json.Marshal(binding.SemanticQueryRef.ResolvedTimeSpec)
			if err != nil {
				return nil, err
			}
		}
		query.Limit = binding.SemanticQueryRef.SemanticIR.Limit
		query.Timeout = 5 * time.Second
	case report.BindingDatasetField:
		if binding.DataContextID == nil {
			return nil, fmt.Errorf("DATASET_FIELD binding is missing dataContextId")
		}
		dataContext, exists := contexts[*binding.DataContextID]
		if !exists {
			return nil, fmt.Errorf("data context %q does not exist", *binding.DataContextID)
		}
		query.DataContextID = dataContext.ID
		query.DatasetID = dataContext.DatasetID
		query.DatasetVersionID = dataContext.DatasetVersionID
		query.Dimensions = append([]report.FieldBinding(nil), binding.Dimensions...)
		query.Measures = append([]report.FieldBinding(nil), binding.Measures...)
		query.Parameters = append([]report.DefaultParameter(nil), dataContext.DefaultParameters...)
		query.Limit = dataContext.QueryPolicy.MaxRows
		query.Timeout = time.Duration(dataContext.QueryPolicy.TimeoutMS) * time.Millisecond
		query.UncertifiedDefinition = true
	default:
		return nil, fmt.Errorf("unsupported binding mode %q", binding.BindingMode)
	}
	return query, nil
}
