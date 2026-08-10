// Package migrate contains explicit major-version Report Definition migrators.
// Minor V1 compatibility is handled by compiler.Normalize; a major version is
// never rewritten implicitly, especially for an already published artifact.
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/report"
)

var ErrV1ToV2MigratorRequired = errors.New("an explicit Report Definition V1-to-V2 migrator is required")

type V1ToV2Migrator func(report.ReportDefinition) (json.RawMessage, error)

// ApplyV1ToV2 clones the V1 value before invoking the registered migrator and
// validates only the versioned envelope. The V2 closed model must own its
// deeper validation when that schema is introduced.
func ApplyV1ToV2(definition report.ReportDefinition, migrator V1ToV2Migrator) (json.RawMessage, error) {
	if migrator == nil {
		return nil, ErrV1ToV2MigratorRequired
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	var cloned report.ReportDefinition
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	migrated, err := migrator(cloned)
	if err != nil {
		return nil, fmt.Errorf("migrate Report Definition V1 to V2: %w", err)
	}
	if len(migrated) == 0 || len(migrated) > report.MaxDefinitionBytes {
		return nil, errors.New("migrated Report Definition V2 size is invalid")
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(migrated, &envelope); err != nil {
		return nil, fmt.Errorf("decode migrated Report Definition V2: %w", err)
	}
	if envelope.SchemaVersion != "2.0" {
		return nil, errors.New("migrated Report Definition must declare schemaVersion 2.0")
	}
	return append(json.RawMessage(nil), migrated...), nil
}
