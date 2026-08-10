package template

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestDefaultRegistryContainsAllMVPManifests(t *testing.T) {
	registry := mustDefaultRegistry(t)
	want := []string{
		"area-stacked", "bar-comparison", "bar-horizontal", "data-table", "filter-control",
		"funnel", "image", "insight-text", "line-trend", "metric-card", "pie-donut",
		"rich-text", "scatter",
	}
	manifests := registry.List()
	if len(manifests) != len(want) {
		t.Fatalf("registry has %d manifests, want %d", len(manifests), len(want))
	}
	got := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		got = append(got, manifest.Type)
		if manifest.Version != "1.0.0" {
			t.Fatalf("%s version = %s, want 1.0.0", manifest.Type, manifest.Version)
		}
		if err := manifest.Validate(); err != nil {
			t.Fatalf("%s Validate() error = %v", manifest.Type, err)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("registered types = %v, want %v", got, want)
	}

	// Get returns a defensive copy; consumers cannot mutate registry truth.
	copyValue, ok := registry.Get("bar-comparison", "1.0.0")
	if !ok {
		t.Fatal("bar-comparison manifest missing")
	}
	copyValue.OptionSchema.Properties["untrusted"] = OptionPropertySchema{Type: OptionBoolean}
	replayed, _ := registry.Get("bar-comparison", "1.0.0")
	if _, leaked := replayed.OptionSchema.Properties["untrusted"]; leaked {
		t.Fatal("Registry.Get leaked a mutable manifest reference")
	}
}

func TestCompareSemverUsesNumericOrdering(t *testing.T) {
	comparison, err := CompareSemver("1.10.0", "1.9.0")
	if err != nil || comparison <= 0 {
		t.Fatalf("CompareSemver(1.10.0, 1.9.0) = %d, %v", comparison, err)
	}
	if _, err := CompareSemver("01.0.0", "1.0.0"); err == nil {
		t.Fatal("CompareSemver() accepted a leading-zero version")
	}
}

func TestRegistryCachesExactManifestByContentHashWithoutFallback(t *testing.T) {
	registry := mustDefaultRegistry(t)
	hash, exists := registry.ContentHash("bar-comparison", "1.0.0")
	if !exists || hash.Validate() != nil {
		t.Fatalf("content hash = %q, %t", hash, exists)
	}
	manifest, exists := registry.GetByContentHash(hash)
	if !exists || manifest.Type != "bar-comparison" || manifest.Version != "1.0.0" {
		t.Fatalf("manifest by hash = %#v, %t", manifest, exists)
	}
	if registry.Has("bar-comparison", "9.9.9") {
		t.Fatal("exact lookup silently fell back to another version")
	}
	if _, exists := registry.GetByContentHash(askdata.HashBytes([]byte("missing"))); exists {
		t.Fatal("unknown content hash was resolved")
	}
}

func TestComponentManifestSchemaAndDocumentsAreStrictJSON(t *testing.T) {
	schemaRaw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "schemas", "component-manifest-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatalf("manifest schema is invalid JSON: %v", err)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("manifest schema does not define $defs")
	}
	for _, name := range []string{"dataContract", "optionSchema", "optionPropertySchema", "mobilePolicy", "migration"} {
		if _, exists := definitions[name]; !exists {
			t.Fatalf("manifest schema does not define $defs.%s", name)
		}
	}

	manifestRaw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "internal", "report", "template", "manifests", "bar-comparison.json"))
	if err != nil {
		t.Fatal(err)
	}
	malicious := strings.Replace(string(manifestRaw), `"version": "1.0.0",`, `"version": "1.0.0", "unknown": true,`, 1)
	if _, err := DecodeManifest([]byte(malicious)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeManifest() error = %v, want unknown field", err)
	}
}

func TestRegistryUsesOneOptionSchemaForDefaultsAndRuntimeOptions(t *testing.T) {
	registry := mustDefaultRegistry(t)
	if err := registry.ValidateOptions("bar-comparison", "1.0.0", []byte(`{
		"showLegend": true,
		"orientation": "VERTICAL"
	}`)); err != nil {
		t.Fatalf("ValidateOptions(valid) error = %v", err)
	}
	if err := registry.ValidateOptions("bar-comparison", "1.0.0", []byte(`{"series": []}`)); err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("ValidateOptions(unknown) error = %v, want unknown option", err)
	}
	if err := registry.ValidateOptions("bar-comparison", "1.0.0", []byte(`{"orientation": "DIAGONAL"}`)); err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("ValidateOptions(enum) error = %v, want enum rejection", err)
	}
	if err := registry.ValidateOptions("bar-comparison", "1.0.0", []byte(`{"topN": 1.5}`)); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("ValidateOptions(integer) error = %v, want integer rejection", err)
	}
}

func TestRegistryValidatesDataContractAndMinimumSize(t *testing.T) {
	registry := mustDefaultRegistry(t)
	component := validBarComponent()
	if err := registry.ValidateComponent(component, 6, 5); err != nil {
		t.Fatalf("ValidateComponent(valid) error = %v", err)
	}

	tooFewDimensions := component
	tooFewDimensions.DataBinding = cloneDataBinding(component.DataBinding)
	tooFewDimensions.DataBinding.Dimensions = []report.FieldBinding{}
	if err := registry.ValidateComponent(tooFewDimensions, 6, 5); err == nil || !strings.Contains(err.Error(), "dimensions count") {
		t.Fatalf("ValidateComponent(dimensions) error = %v", err)
	}

	tooFewMeasures := component
	tooFewMeasures.DataBinding = cloneDataBinding(component.DataBinding)
	tooFewMeasures.DataBinding.Measures = []report.FieldBinding{}
	if err := registry.ValidateComponent(tooFewMeasures, 6, 5); err == nil || !strings.Contains(err.Error(), "measures count") {
		t.Fatalf("ValidateComponent(measures) error = %v", err)
	}

	unsupportedRole := component
	unsupportedRole.DataBinding = cloneDataBinding(component.DataBinding)
	unsupportedRole.DataBinding.Measures[0].Role = report.RoleDetail
	if err := registry.ValidateComponent(unsupportedRole, 6, 5); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("ValidateComponent(role) error = %v", err)
	}

	if err := registry.ValidateComponent(component, 3, 5); err == nil || !strings.Contains(err.Error(), "below manifest minimum") {
		t.Fatalf("ValidateComponent(size) error = %v", err)
	}
}

func TestBundledRegistryAcceptsReportDefinitionExamples(t *testing.T) {
	registry := mustDefaultRegistry(t)
	for _, name := range []string{"simple-report.json", "multi-page-report.json", "ask-data-report.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "examples", "report-definition", name))
			if err != nil {
				t.Fatal(err)
			}
			definition, err := report.Decode(raw)
			if err != nil {
				t.Fatalf("report.Decode() error = %v", err)
			}
			components := make(map[askdata.ID]report.Component, len(definition.Components))
			for _, component := range definition.Components {
				components[component.ID] = component
			}
			validated := map[askdata.ID]struct{}{}
			for _, page := range definition.Pages {
				for _, section := range page.Sections {
					for _, block := range section.Blocks {
						for _, zone := range block.Zones {
							for _, slot := range zone.Slots {
								component := components[slot.ComponentID]
								if err := registry.ValidateComponent(component, block.Layout.Desktop.W, block.Layout.Desktop.H); err != nil {
									t.Fatalf("ValidateComponent(%s) error = %v", component.ID, err)
								}
								validated[component.ID] = struct{}{}
							}
						}
					}
				}
			}
			if len(validated) != len(definition.Components) {
				t.Fatalf("validated %d components, definition has %d", len(validated), len(definition.Components))
			}
		})
	}
}

func TestSemverCompatibilityAndMigratorGate(t *testing.T) {
	registry := mustDefaultRegistry(t)
	previous, ok := registry.Get("bar-comparison", "1.0.0")
	if !ok {
		t.Fatal("bar-comparison manifest missing")
	}

	t.Run("minor adds optional property", func(t *testing.T) {
		next := cloneManifest(previous)
		next.Version = "1.1.0"
		next.OptionSchema.Properties["newOption"] = OptionPropertySchema{Type: OptionBoolean}
		if err := CheckUpgrade(previous, next, nil); err != nil {
			t.Fatalf("CheckUpgrade() error = %v", err)
		}
	})

	t.Run("minor adds required property", func(t *testing.T) {
		next := cloneManifest(previous)
		next.Version = "1.1.0"
		next.OptionSchema.Properties["newOption"] = OptionPropertySchema{Type: OptionBoolean}
		next.OptionSchema.Required = append(next.OptionSchema.Required, "newOption")
		next.DefaultOptions["newOption"] = json.RawMessage(`true`)
		if err := CheckUpgrade(previous, next, nil); err == nil || !strings.Contains(err.Error(), "adds required option") {
			t.Fatalf("CheckUpgrade(required) error = %v", err)
		}
	})

	t.Run("major missing migration", func(t *testing.T) {
		next := cloneManifest(previous)
		next.Version = "2.0.0"
		if err := CheckUpgrade(previous, next, nil); err == nil || !strings.Contains(err.Error(), "requires migration") {
			t.Fatalf("CheckUpgrade(major) error = %v", err)
		}
	})

	t.Run("major missing registered migrator", func(t *testing.T) {
		next := cloneManifest(previous)
		next.Version = "2.0.0"
		next.Migration = &Migration{From: previous.Version, MigratorID: "bar-comparison-1-to-2"}
		if err := CheckUpgrade(previous, next, nil); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("CheckUpgrade(migrator) error = %v", err)
		}
	})

	t.Run("major with registered migrator", func(t *testing.T) {
		next := cloneManifest(previous)
		next.Version = "2.0.0"
		next.Migration = &Migration{From: previous.Version, MigratorID: "bar-comparison-1-to-2"}
		migrators := map[string]Migrator{
			"bar-comparison-1-to-2": func(options json.RawMessage) (json.RawMessage, error) { return options, nil },
		}
		if err := CheckUpgrade(previous, next, migrators); err != nil {
			t.Fatalf("CheckUpgrade(valid major) error = %v", err)
		}
	})
}

func TestMigrateComponentBuildsExplicitReplacementAndKeepsInputImmutable(t *testing.T) {
	baseRegistry := mustDefaultRegistry(t)
	previous, _ := baseRegistry.Get("bar-comparison", "1.0.0")
	next := cloneManifest(previous)
	next.Version = "2.0.0"
	next.Migration = &Migration{From: previous.Version, MigratorID: "bar-comparison-1-to-2"}
	called := 0
	registry, err := NewRegistry([]Manifest{previous, next}, map[string]Migrator{
		"bar-comparison-1-to-2": func(options json.RawMessage) (json.RawMessage, error) {
			called++
			var values map[string]json.RawMessage
			if err := json.Unmarshal(options, &values); err != nil {
				return nil, err
			}
			values["title"] = json.RawMessage(`"Migrated title"`)
			return json.Marshal(values)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	component := validBarComponent()
	component.Options.Title = "Original title"
	before, _ := json.Marshal(component)
	migrated, err := registry.MigrateComponent(component, "2.0.0")
	if err != nil || called != 1 || migrated.TemplateRef.Version != "2.0.0" || migrated.Options.Title != "Migrated title" {
		t.Fatalf("MigrateComponent() = %#v, called=%d, err=%v", migrated, called, err)
	}
	after, _ := json.Marshal(component)
	if string(before) != string(after) {
		t.Fatal("component migration mutated the published input")
	}
}

func validBarComponent() report.Component {
	contextID := askdata.ID("sales_context")
	showLegend := true
	return report.Component{
		ID: "component_bar",
		TemplateRef: report.ComponentTemplateReference{
			Type:    "bar-comparison",
			Version: "1.0.0",
		},
		DataBinding: &report.DataBinding{
			BindingMode:   report.BindingDatasetField,
			DataContextID: &contextID,
			Dimensions: []report.FieldBinding{
				{Role: report.RoleXAxis, Field: "region"},
			},
			Measures: []report.FieldBinding{
				{Role: report.RoleYAxis, Field: "sales_amount"},
			},
		},
		Options: report.ComponentOptions{
			ShowLegend:  &showLegend,
			Orientation: report.OrientationVertical,
		},
	}
}

func cloneDataBinding(binding *report.DataBinding) *report.DataBinding {
	clone := *binding
	clone.Dimensions = append([]report.FieldBinding(nil), binding.Dimensions...)
	clone.Measures = append([]report.FieldBinding(nil), binding.Measures...)
	return &clone
}

func mustDefaultRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry() error = %v", err)
	}
	return registry
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
