package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/report"
)

func TestApplyV1ToV2RequiresExplicitMigratorAndDoesNotMutateInput(t *testing.T) {
	definition := report.ReportDefinition{SchemaVersion: report.SchemaVersion}
	if _, err := ApplyV1ToV2(definition, nil); !errors.Is(err, ErrV1ToV2MigratorRequired) {
		t.Fatalf("nil migrator error = %v", err)
	}
	original, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := ApplyV1ToV2(definition, func(clone report.ReportDefinition) (json.RawMessage, error) {
		clone.SchemaVersion = "mutated-clone"
		return json.RawMessage(`{"schemaVersion":"2.0","metadata":{"id":"future"}}`), nil
	})
	if err != nil || !bytes.Contains(migrated, []byte(`"schemaVersion":"2.0"`)) {
		t.Fatalf("ApplyV1ToV2() = %s, %v", migrated, err)
	}
	after, err := json.Marshal(definition)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("input was mutated: %s -> %s, %v", original, after, err)
	}
	if _, err := ApplyV1ToV2(definition, func(report.ReportDefinition) (json.RawMessage, error) {
		return json.RawMessage(`{"schemaVersion":"1.0"}`), nil
	}); err == nil {
		t.Fatal("migrator output with the wrong major version succeeded")
	}
}
