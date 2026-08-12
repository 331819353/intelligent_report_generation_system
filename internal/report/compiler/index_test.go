package compiler

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/report"
)

func TestBuildIndexesPropertyCoversAndDeduplicatesDefinition(t *testing.T) {
	property := func(seed uint64, requested uint8) bool {
		random := rand.New(rand.NewSource(int64(seed)))
		componentCount := 1 + int(requested%40)
		definition := indexedPropertyDefinition(random, componentCount)
		indexes := BuildIndexes(definition)
		if len(indexes.Components) != componentCount {
			return false
		}
		seen := map[askdata.ID]struct{}{}
		for _, item := range indexes.Components {
			if _, duplicate := seen[item.ComponentID]; duplicate {
				return false
			}
			seen[item.ComponentID] = struct{}{}
		}
		for _, component := range definition.Components {
			if _, exists := seen[component.ID]; !exists {
				return false
			}
		}
		dependencies := dependencyMap(indexes.Dependencies)
		datasetKey := "DATASET_VERSION\x00dataset_shared_v1"
		if _, exists := dependencies[datasetKey]; !exists {
			return false
		}
		metricIDs := []askdata.ID{}
		for index, component := range definition.Components {
			if index%2 == 1 {
				metricIDs = append(metricIDs, component.ID)
			}
		}
		metricDependency, metricExists := dependencies["METRIC_VERSION\x00metric_shared_v1"]
		if len(metricIDs) == 0 {
			if metricExists {
				return false
			}
		} else if !metricExists || !reflect.DeepEqual(metricDependency, metricIDs) {
			return false
		}
		for _, dependency := range indexes.Dependencies {
			if !sort.SliceIsSorted(dependency.ComponentIDs, func(left, right int) bool {
				return dependency.ComponentIDs[left] < dependency.ComponentIDs[right]
			}) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexesIncludesUnusedContextAndSemanticDataset(t *testing.T) {
	definition := indexedPropertyDefinition(rand.New(rand.NewSource(7)), 2)
	semanticDatasetID := askdata.ID("semantic_dataset_v4")
	definition.Components[1].DataBinding.SemanticQueryRef.DatasetVersionID = &semanticDatasetID
	indexes := dependencyMap(BuildIndexes(definition).Dependencies)
	if _, exists := indexes["DATASET_VERSION\x00dataset_shared_v1"]; !exists {
		t.Fatal("declared data context dependency is missing")
	}
	if got := indexes["DATASET_VERSION\x00semantic_dataset_v4"]; !reflect.DeepEqual(got, []askdata.ID{"component_001"}) {
		t.Fatalf("semantic dataset component IDs = %v", got)
	}
}

func indexedPropertyDefinition(random *rand.Rand, componentCount int) report.ReportDefinition {
	components := make([]report.Component, 0, componentCount)
	slots := make([]report.Slot, 0, componentCount)
	for index := 0; index < componentCount; index++ {
		componentID := askdata.ID(fmt.Sprintf("component_%03d", index))
		componentType := fmt.Sprintf("test-component-%d", random.Intn(5))
		component := report.Component{
			ID:          componentID,
			TemplateRef: report.ComponentTemplateReference{Type: componentType, Version: "1.0.0"},
		}
		if index%2 == 0 {
			contextID := askdata.ID("context_shared")
			component.DataBinding = &report.DataBinding{
				BindingMode: report.BindingDatasetField, DataContextID: &contextID,
			}
		} else {
			component.DataBinding = &report.DataBinding{
				BindingMode: report.BindingSemanticIR,
				SemanticQueryRef: &report.SemanticQueryRef{
					SemanticReleaseID: "release_shared_v1",
					SemanticIR: ircontract.SemanticIR{
						Metrics: []ircontract.Metric{{MetricVersionID: "metric_shared_v1"}},
						GroupBy: []ircontract.GroupBy{{DimensionVersionID: "dimension_shared_v1"}},
						Filters: []ircontract.Filter{{
							DimensionVersionID: "dimension_shared_v1",
							MemberVersionIDs:   []askdata.ID{"member_shared_v1"},
						}},
					},
				},
			}
		}
		components = append(components, component)
		slots = append(slots, report.Slot{
			ID: askdata.ID(fmt.Sprintf("slot_%03d", index)), ComponentID: componentID,
		})
	}
	return report.ReportDefinition{
		TemplateRef: report.TemplateReference{
			ReportTemplateID: "report_template", ReportTemplateVersion: "1.0.0",
			StructureTemplateVersion: "1.1.0", LayoutTemplateVersion: "1.2.0",
			NarrativeTemplateVersion: "1.3.0",
		},
		ThemeRef: report.ThemeReference{ThemeID: "theme", Version: "2.0.0"},
		DataContexts: []report.DataContext{{
			ID: "context_shared", DatasetVersionID: "dataset_shared_v1",
		}},
		Pages: []report.Page{{ID: "page", Sections: []report.Section{{
			ID: "section", Blocks: []report.Block{{ID: "block", Zones: []report.Zone{{
				Order: 1,
				ID:    "zone", Slots: slots,
			}}}},
		}}}},
		Components: components,
	}
}

func dependencyMap(items []DependencyIndex) map[string][]askdata.ID {
	result := make(map[string][]askdata.ID, len(items))
	for _, item := range items {
		result[item.DependencyType+"\x00"+item.DependencyID] = item.ComponentIDs
	}
	return result
}
