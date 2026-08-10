package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

type bindingCatalogFixture struct {
	release  ReleaseBindingState
	objects  map[askdata.ID]SemanticObjectState
	datasets map[askdata.ID]DatasetVersionState
}

func (catalog bindingCatalogFixture) ReleaseState(context.Context, askdata.ID) (ReleaseBindingState, error) {
	return catalog.release, nil
}

func (catalog bindingCatalogFixture) SemanticObjectState(_ context.Context, id askdata.ID) (SemanticObjectState, error) {
	state, exists := catalog.objects[id]
	if !exists {
		return SemanticObjectState{}, errors.New("semantic object is missing")
	}
	return state, nil
}

func (catalog bindingCatalogFixture) DatasetVersionState(_ context.Context, id askdata.ID) (DatasetVersionState, error) {
	state, exists := catalog.datasets[id]
	if !exists {
		return DatasetVersionState{}, errors.New("dataset version is missing")
	}
	return state, nil
}

func TestValidateComponentBindingSixStableRejections(t *testing.T) {
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	semanticDefinition := runtimeDefinition(t, "ask-data-report.json")
	semantic := semanticDefinition.Components[0]
	semanticCatalog := catalogForSemantic(semantic)
	datasetDefinition := runtimeDefinition(t, "simple-report.json")
	dataset := datasetDefinition.Components[0]
	contexts := contextIndex(datasetDefinition)
	datasetID := datasetDefinition.DataContexts[0].DatasetVersionID
	datasetCatalog := bindingCatalogFixture{datasets: map[askdata.ID]DatasetVersionState{datasetID: {Status: "ACTIVE"}}}

	tests := []struct {
		name      string
		component report.Component
		contexts  map[askdata.ID]report.DataContext
		catalog   bindingCatalogFixture
		wantCode  string
	}{
		{
			name: "ambiguous mode",
			component: func() report.Component {
				value := cloneComponent(t, dataset)
				value.DataBinding.SemanticQueryRef = semantic.DataBinding.SemanticQueryRef
				return value
			}(),
			contexts: contexts, catalog: datasetCatalog, wantCode: "REPORT_BINDING_MODE_AMBIGUOUS",
		},
		{
			name:      "retired release",
			component: semantic, contexts: map[askdata.ID]report.DataContext{},
			catalog:  func() bindingCatalogFixture { value := semanticCatalog; value.release.Status = "RETIRED"; return value }(),
			wantCode: "REPORT_BINDING_RELEASE_RETIRED",
		},
		{
			name:      "uncertified object",
			component: semantic, contexts: map[askdata.ID]report.DataContext{},
			catalog: func() bindingCatalogFixture {
				value := semanticCatalog
				metricID := semantic.DataBinding.SemanticQueryRef.SemanticIR.Metrics[0].MetricVersionID
				value.objects = cloneObjectStates(value.objects)
				value.objects[metricID] = SemanticObjectState{Certified: false}
				return value
			}(),
			wantCode: "REPORT_BINDING_OBJECT_NOT_CERTIFIED",
		},
		{
			name: "incompatible unit",
			component: func() report.Component {
				value := semantic
				reference := *value.DataBinding.SemanticQueryRef
				reference.SemanticIR.Metrics = append(reference.SemanticIR.Metrics, ircontract.Metric{MetricVersionID: "metric_profit_v2", Alias: "profit"})
				value.DataBinding = &report.DataBinding{BindingMode: report.BindingSemanticIR, SemanticQueryRef: &reference}
				return value
			}(),
			contexts: map[askdata.ID]report.DataContext{},
			catalog: func() bindingCatalogFixture {
				value := semanticCatalog
				value.objects = cloneObjectStates(value.objects)
				metricID := semantic.DataBinding.SemanticQueryRef.SemanticIR.Metrics[0].MetricVersionID
				value.objects[metricID] = SemanticObjectState{Certified: true, Unit: "CURRENCY", Currency: "CNY"}
				value.objects["metric_profit_v2"] = SemanticObjectState{Certified: true, Unit: "CURRENCY", Currency: "USD"}
				return value
			}(),
			wantCode: "INCOMPATIBLE_UNIT",
		},
		{
			name: "manifest data contract",
			component: func() report.Component {
				value := cloneComponent(t, dataset)
				value.DataBinding.Measures = append(value.DataBinding.Measures,
					report.FieldBinding{Role: report.RoleYAxis, Field: "measure_two"},
					report.FieldBinding{Role: report.RoleYAxis, Field: "measure_three"},
					report.FieldBinding{Role: report.RoleYAxis, Field: "measure_four"},
					report.FieldBinding{Role: report.RoleYAxis, Field: "measure_five"},
					report.FieldBinding{Role: report.RoleYAxis, Field: "measure_six"})
				return value
			}(),
			contexts: contexts, catalog: datasetCatalog, wantCode: "REPORT_BINDING_CONTRACT_VIOLATION",
		},
		{
			name:      "dataset inactive",
			component: dataset, contexts: contexts,
			catalog:  bindingCatalogFixture{datasets: map[askdata.ID]DatasetVersionState{datasetID: {Status: "PUBLISHED"}}},
			wantCode: "REPORT_BINDING_DATASET_NOT_ACTIVE",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateComponentBinding(context.Background(), test.component, test.contexts, registry, test.catalog)
			var bindingErr *BindingError
			if !errors.As(err, &bindingErr) || bindingErr.Code != test.wantCode {
				t.Fatalf("binding error = %#v, %v; want %s", bindingErr, err, test.wantCode)
			}
		})
	}
}

func TestValidateBindingsAllowsRetainedAndAnnotatesComponent(t *testing.T) {
	definition := runtimeDefinition(t, "ask-data-report.json")
	catalog := catalogForSemantic(definition.Components[0])
	catalog.release.Status = "RETAINED"
	if issues := ValidateBindings(context.Background(), definition, nil, catalog); len(issues) != 0 {
		t.Fatalf("RETAINED release was rejected: %#v", issues)
	}
	catalog.release.Status = "RETIRED"
	issues := ValidateBindings(context.Background(), definition, nil, catalog)
	if len(issues) != 1 || issues[0].ComponentID != definition.Components[0].ID || issues[0].Code != "REPORT_BINDING_RELEASE_RETIRED" {
		t.Fatalf("annotated issues = %#v", issues)
	}
}

func runtimeDefinition(t *testing.T, name string) report.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime fixture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", name))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := decodeRuntimeFixture(raw)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func decodeRuntimeFixture(raw []byte) (report.ReportDefinition, error) {
	return report.Decode(raw)
}

func contextIndex(definition report.ReportDefinition) map[askdata.ID]report.DataContext {
	result := make(map[askdata.ID]report.DataContext, len(definition.DataContexts))
	for _, dataContext := range definition.DataContexts {
		result[dataContext.ID] = dataContext
	}
	return result
}

func catalogForSemantic(component report.Component) bindingCatalogFixture {
	reference := component.DataBinding.SemanticQueryRef
	objects := map[askdata.ID]SemanticObjectState{
		reference.SemanticIR.ModelVersionID: {Certified: true},
	}
	for _, metric := range reference.SemanticIR.Metrics {
		objects[metric.MetricVersionID] = SemanticObjectState{Certified: true, Unit: "CNY", Currency: "CNY"}
	}
	for _, group := range reference.SemanticIR.GroupBy {
		objects[group.DimensionVersionID] = SemanticObjectState{Certified: true}
	}
	for _, filter := range reference.SemanticIR.Filters {
		objects[filter.DimensionVersionID] = SemanticObjectState{Certified: true}
		for _, memberID := range filter.MemberVersionIDs {
			objects[memberID] = SemanticObjectState{Certified: true}
		}
	}
	if reference.SemanticIR.TimeRange != nil {
		objects[reference.SemanticIR.TimeRange.DimensionVersionID] = SemanticObjectState{Certified: true}
	}
	return bindingCatalogFixture{
		release: ReleaseBindingState{Status: "ACTIVE", ContentHash: reference.SemanticContentHash}, objects: objects,
	}
}

func cloneObjectStates(values map[askdata.ID]SemanticObjectState) map[askdata.ID]SemanticObjectState {
	result := make(map[askdata.ID]SemanticObjectState, len(values))
	for id, value := range values {
		result[id] = value
	}
	return result
}

func cloneComponent(t *testing.T, component report.Component) report.Component {
	t.Helper()
	raw, err := json.Marshal(component)
	if err != nil {
		t.Fatal(err)
	}
	var result report.Component
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBindingSchemaKeepsClosedUnion(t *testing.T) {
	definition := runtimeDefinition(t, "simple-report.json")
	raw, err := json.Marshal(definition.Components[0].DataBinding)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("binding union did not serialize")
	}
}
