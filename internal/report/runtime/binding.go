package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

type ReleaseBindingState struct {
	Status      string
	ContentHash askdata.ContentHash
}

type SemanticObjectState struct {
	Certified bool
	Unit      string
	Currency  string
}

type DatasetVersionState struct {
	Status string
}

type BindingCatalog interface {
	ReleaseState(context.Context, askdata.ID) (ReleaseBindingState, error)
	SemanticObjectState(context.Context, askdata.ID) (SemanticObjectState, error)
	DatasetVersionState(context.Context, askdata.ID) (DatasetVersionState, error)
}

type BindingError struct {
	Code        string     `json:"code"`
	ComponentID askdata.ID `json:"componentId,omitempty"`
	Message     string     `json:"message"`
}

func (err *BindingError) Error() string { return err.Code + ": " + err.Message }

func ValidateComponentBinding(ctx context.Context, component report.Component, contexts map[askdata.ID]report.DataContext, registry *template.Registry, catalog BindingCatalog) error {
	if component.DataBinding == nil {
		return nil
	}
	binding := component.DataBinding
	semanticShape := binding.SemanticQueryRef != nil
	datasetShape := binding.DataContextID != nil || binding.Dimensions != nil || binding.Measures != nil
	if (binding.BindingMode == report.BindingSemanticIR && (!semanticShape || datasetShape)) ||
		(binding.BindingMode == report.BindingDatasetField && (semanticShape || binding.DataContextID == nil || binding.Dimensions == nil || binding.Measures == nil)) ||
		(binding.BindingMode != report.BindingSemanticIR && binding.BindingMode != report.BindingDatasetField) {
		return &BindingError{Code: "REPORT_BINDING_MODE_AMBIGUOUS", Message: "component contains both semantic and dataset bindings"}
	}
	if registry == nil {
		var err error
		registry, err = template.NewDefaultRegistry()
		if err != nil {
			return err
		}
	}
	if err := registry.ValidateComponent(component, 24, 100); err != nil {
		return &BindingError{Code: "REPORT_BINDING_CONTRACT_VIOLATION", Message: err.Error()}
	}
	if catalog == nil {
		return fmt.Errorf("binding catalog is required")
	}
	switch binding.BindingMode {
	case report.BindingSemanticIR:
		reference := binding.SemanticQueryRef
		release, err := catalog.ReleaseState(ctx, reference.SemanticReleaseID)
		if err != nil {
			return err
		}
		if release.Status == "RETIRED" || release.ContentHash != reference.SemanticContentHash {
			return &BindingError{Code: "REPORT_BINDING_RELEASE_RETIRED", Message: "semantic release is retired or its content hash changed"}
		}
		units := map[string]struct{}{}
		currencies := map[string]struct{}{}
		objectIDs := map[askdata.ID]string{reference.SemanticIR.ModelVersionID: "semantic model"}
		for _, metric := range reference.SemanticIR.Metrics {
			objectIDs[metric.MetricVersionID] = "metric"
			state, err := catalog.SemanticObjectState(ctx, metric.MetricVersionID)
			if err != nil {
				return err
			}
			if !state.Certified {
				return &BindingError{Code: "REPORT_BINDING_OBJECT_NOT_CERTIFIED", Message: fmt.Sprintf("metric %q is not certified", metric.MetricVersionID)}
			}
			if unit := strings.ToUpper(strings.TrimSpace(state.Unit)); unit != "" {
				units[unit] = struct{}{}
			}
			if currency := strings.ToUpper(strings.TrimSpace(state.Currency)); currency != "" {
				currencies[currency] = struct{}{}
			}
		}
		for _, group := range reference.SemanticIR.GroupBy {
			objectIDs[group.DimensionVersionID] = "dimension"
		}
		for _, filter := range reference.SemanticIR.Filters {
			objectIDs[filter.DimensionVersionID] = "dimension"
			for _, memberID := range filter.MemberVersionIDs {
				objectIDs[memberID] = "member"
			}
		}
		if reference.SemanticIR.TimeRange != nil {
			objectIDs[reference.SemanticIR.TimeRange.DimensionVersionID] = "dimension"
		}
		ids := make([]askdata.ID, 0, len(objectIDs))
		for id := range objectIDs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
		for _, id := range ids {
			// Metric certification was already read above to collect comparable
			// units. Re-reading is harmless but avoidable and keeps fake/DB
			// catalogs bounded to one lookup per object.
			if objectIDs[id] == "metric" {
				continue
			}
			state, err := catalog.SemanticObjectState(ctx, id)
			if err != nil {
				return err
			}
			if !state.Certified {
				return &BindingError{Code: "REPORT_BINDING_OBJECT_NOT_CERTIFIED", Message: fmt.Sprintf("%s %q is not certified", objectIDs[id], id)}
			}
		}
		if len(units) > 1 || len(currencies) > 1 {
			return &BindingError{Code: "INCOMPATIBLE_UNIT", Message: "one component cannot mix incomparable units or currencies"}
		}
	case report.BindingDatasetField:
		dataContext, exists := contexts[*binding.DataContextID]
		if !exists {
			return &BindingError{Code: "REPORT_BINDING_CONTRACT_VIOLATION", Message: "data context does not exist"}
		}
		state, err := catalog.DatasetVersionState(ctx, dataContext.DatasetVersionID)
		if err != nil {
			return err
		}
		if state.Status != "ACTIVE" {
			return &BindingError{Code: "REPORT_BINDING_DATASET_NOT_ACTIVE", Message: "dataset version is not active"}
		}
	}
	return nil
}

func ValidateBindings(ctx context.Context, definition report.ReportDefinition, registry *template.Registry, catalog BindingCatalog) []BindingError {
	contexts := make(map[askdata.ID]report.DataContext, len(definition.DataContexts))
	for _, dataContext := range definition.DataContexts {
		contexts[dataContext.ID] = dataContext
	}
	issues := []BindingError{}
	for _, component := range definition.Components {
		if err := ValidateComponentBinding(ctx, component, contexts, registry, catalog); err != nil {
			if bindingErr, ok := err.(*BindingError); ok {
				bindingErr.ComponentID = component.ID
				issues = append(issues, *bindingErr)
			} else {
				issues = append(issues, BindingError{Code: "REPORT_BINDING_LOOKUP_FAILED", Message: err.Error()})
			}
		}
	}
	return issues
}
