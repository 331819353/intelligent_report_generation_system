package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestExamplesDecodeAndRoundTrip(t *testing.T) {
	for _, name := range []string{"simple-report.json", "multi-page-report.json", "ask-data-report.json"} {
		t.Run(name, func(t *testing.T) {
			raw := readExample(t, name)
			definition, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			roundTrip, err := json.Marshal(definition)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			replayed, err := Decode(roundTrip)
			if err != nil {
				t.Fatalf("Decode(roundTrip) error = %v", err)
			}
			if !reflect.DeepEqual(definition, replayed) {
				t.Fatal("round-trip Report Definition differs")
			}
		})
	}
}

func TestDecodeRejectsStructuralLimits(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		raw := strings.Replace(string(readExample(t, "simple-report.json")), `"schemaVersion": "1.0",`, `"schemaVersion": "1.0", "unexpected": true,`, 1)
		assertDecodeErrorContains(t, []byte(raw), "unknown field")
	})

	t.Run("depth", func(t *testing.T) {
		raw := `{"x":` + strings.Repeat(`{"x":`, MaxJSONDepth) + `null` + strings.Repeat(`}`, MaxJSONDepth+1)
		assertDecodeErrorContains(t, []byte(raw), "maximum JSON depth")
	})

	t.Run("pages", func(t *testing.T) {
		definition := mustDecodeExample(t, "simple-report.json")
		page := definition.Pages[0]
		page.Sections = []Section{}
		definition.Pages = make([]Page, MaxPages+1)
		for index := range definition.Pages {
			definition.Pages[index] = page
			definition.Pages[index].ID = askdata.ID("page_" + string(rune('a'+index)))
			definition.Pages[index].Order = index + 1
		}
		assertDefinitionErrorContains(t, definition, "pages count")
	})

	t.Run("bytes", func(t *testing.T) {
		raw := make([]byte, MaxDefinitionBytes+1)
		for index := range raw {
			raw[index] = ' '
		}
		assertDecodeErrorContains(t, raw, "exceeds")
	})
}

func TestValidateRejectsAllBoundedCollectionAndStringLimits(t *testing.T) {
	tests := map[string]struct {
		mutate func(*ReportDefinition)
		want   string
	}{
		"sections": {
			mutate: func(definition *ReportDefinition) {
				definition.Pages[0].Sections = make([]Section, MaxSectionsPerPage+1)
			},
			want: "sections exceeds",
		},
		"blocks": {
			mutate: func(definition *ReportDefinition) {
				definition.Pages[0].Sections[0].Blocks = make([]Block, MaxBlocks+1)
			},
			want: "report blocks exceeds",
		},
		"slots": {
			mutate: func(definition *ReportDefinition) {
				zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
				zone.Slots = make([]Slot, MaxSlotsPerBlock+1)
				for index := range zone.Slots {
					zone.Slots[index] = Slot{
						ID:          askdata.ID("slot_limit_" + string(rune('a'+index))),
						Grid:        SlotGrid{X: 0, Y: 0, W: 1, H: 1},
						ComponentID: definition.Components[0].ID,
					}
				}
			},
			want: "slots across block exceeds",
		},
		"components": {
			mutate: func(definition *ReportDefinition) {
				definition.Components = make([]Component, MaxComponents+1)
			},
			want: "components exceeds",
		},
		"ordinary string": {
			mutate: func(definition *ReportDefinition) {
				definition.Metadata.Description = strings.Repeat("界", MaxStringLength+1)
			},
			want: "exceeds 4096 characters",
		},
		"rich text": {
			mutate: func(definition *ReportDefinition) {
				definition.Components[0].Options.RichText = strings.Repeat("x", MaxRichTextBytes+1)
			},
			want: "rich text",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			definition := mustDecodeExample(t, "simple-report.json")
			test.mutate(&definition)
			assertDefinitionErrorContains(t, definition, test.want)
		})
	}
}

func TestDecodeRejectsForbiddenFieldsAndContent(t *testing.T) {
	for _, field := range []string{"sql", "script", "onclick"} {
		t.Run(field, func(t *testing.T) {
			raw := strings.Replace(string(readExample(t, "simple-report.json")), `"schemaVersion": "1.0",`, `"schemaVersion": "1.0", "`+field+`": "blocked",`, 1)
			assertDecodeErrorContains(t, []byte(raw), "forbidden field")
		})
	}

	tests := map[string]string{
		"SQL value":         "SELECT * FROM orders",
		"connection string": "postgres://user:pass@database/reports",
		"script tag":        "<script>alert(1)</script>",
		"event attribute":   `<p onclick="alert(1)">unsafe</p>`,
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			definition := mustDecodeExample(t, "multi-page-report.json")
			definition.Components[3].Options.RichText = value
			assertDefinitionErrorContains(t, definition, "forbidden")
		})
	}
}

func TestValidateRejectsMissingComponentReference(t *testing.T) {
	definition := mustDecodeExample(t, "simple-report.json")
	definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots[0].ComponentID = "component_missing"
	assertDefinitionErrorContains(t, definition, "does not reference a component")
}

func TestValidateRequiresExactlyOneSlotPerComponent(t *testing.T) {
	t.Run("unplaced", func(t *testing.T) {
		definition := mustDecodeExample(t, "simple-report.json")
		definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots = []Slot{}
		assertDefinitionErrorContains(t, definition, "must be placed in exactly one slot")
	})
	t.Run("placed twice", func(t *testing.T) {
		definition := mustDecodeExample(t, "simple-report.json")
		zone := definition.Pages[0].Sections[0].Blocks[0].Zones[0]
		zone.Slots = append([]Slot(nil), zone.Slots...)
		zone.ID = "zone_duplicate_component"
		zone.Slots[0].ID = "slot_duplicate_component"
		definition.Pages[0].Sections[0].Blocks[0].Zones = append(
			definition.Pages[0].Sections[0].Blocks[0].Zones, zone,
		)
		assertDefinitionErrorContains(t, definition, "must be placed in exactly one slot")
	})
}

func TestValidateRejectsDuplicateIDsByNamespace(t *testing.T) {
	tests := map[string]func(*ReportDefinition){
		"page": func(definition *ReportDefinition) {
			page := definition.Pages[0]
			page.Sections = []Section{}
			definition.Pages = append(definition.Pages, page)
		},
		"section": func(definition *ReportDefinition) {
			section := definition.Pages[0].Sections[0]
			section.Blocks = []Block{}
			definition.Pages[0].Sections = append(definition.Pages[0].Sections, section)
		},
		"block": func(definition *ReportDefinition) {
			block := definition.Pages[0].Sections[0].Blocks[0]
			block.Zones = []Zone{}
			definition.Pages[0].Sections[0].Blocks = append(definition.Pages[0].Sections[0].Blocks, block)
		},
		"zone": func(definition *ReportDefinition) {
			zone := definition.Pages[0].Sections[0].Blocks[0].Zones[0]
			zone.Slots = []Slot{}
			definition.Pages[0].Sections[0].Blocks[0].Zones = append(definition.Pages[0].Sections[0].Blocks[0].Zones, zone)
		},
		"slot": func(definition *ReportDefinition) {
			slot := definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots[0]
			definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots = append(definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots, slot)
		},
		"component": func(definition *ReportDefinition) {
			definition.Components = append(definition.Components, definition.Components[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			definition := mustDecodeExample(t, "simple-report.json")
			mutate(&definition)
			assertDefinitionErrorContains(t, definition, "duplicates ID")
		})
	}
}

func TestReportDefinitionSchemaIsClosedAndDefinesCoreNodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "schemas", "report-definition-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema does not define $defs")
	}
	for _, name := range []string{"block", "zone", "slot", "component"} {
		if _, exists := definitions[name]; !exists {
			t.Fatalf("schema does not define $defs.%s", name)
		}
	}
	assertClosedSchemaObjects(t, schema, "$")
}

func assertClosedSchemaObjects(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if objectType, ok := typed["type"].(string); ok && objectType == "object" {
			closed, exists := typed["additionalProperties"]
			if !exists || closed != false {
				t.Fatalf("schema object %s must set additionalProperties=false", path)
			}
		}
		for key, child := range typed {
			assertClosedSchemaObjects(t, child, path+"."+key)
		}
	case []any:
		for index, child := range typed {
			assertClosedSchemaObjects(t, child, path+"["+string(rune('0'+index))+" ]")
		}
	}
}

func mustDecodeExample(t *testing.T, name string) ReportDefinition {
	t.Helper()
	definition, err := Decode(readExample(t, name))
	if err != nil {
		t.Fatalf("Decode(%s) error = %v", name, err)
	}
	return definition
}

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "api", "examples", "report-definition", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func assertDecodeErrorContains(t *testing.T, raw []byte, want string) {
	t.Helper()
	if _, err := Decode(raw); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("Decode() error = %v, want substring %q", err, want)
	}
}

func assertDefinitionErrorContains(t *testing.T, definition ReportDefinition, want string) {
	t.Helper()
	if err := definition.Validate(); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("Validate() error = %v, want substring %q", err, want)
	}
}
