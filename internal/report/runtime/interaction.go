package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

// MaxSelections bounds how many source components a viewer can have active at
// once. It matches the interaction link ceiling in the definition model.
const MaxSelections = report.MaxInteractionLinks

// MaxSelectionValues bounds the values carried by one selection.
const MaxSelectionValues = 32

// ReportSelection is a viewer's interaction with one source component: the
// dimension values under the point they clicked or brushed.
//
// It deliberately carries no filter, field mapping or target. A client that
// could name its own target field would be able to filter a component on a
// field the report never declared, bypassing both the interaction graph and the
// scoping rules that govern global filters. The server derives all of that from
// the definition's own Interactions.
type ReportSelection struct {
	ComponentID askdata.ID                 `json:"componentId"`
	Values      map[string]json.RawMessage `json:"values"`
}

func (selection ReportSelection) validate() error {
	if selection.ComponentID.Validate() != nil {
		return errors.New("selection componentId is invalid")
	}
	if len(selection.Values) == 0 || len(selection.Values) > MaxSelectionValues {
		return fmt.Errorf("selection values count must be between 1 and %d", MaxSelectionValues)
	}
	for field, raw := range selection.Values {
		if strings.TrimSpace(field) == "" || len(raw) == 0 || len(raw) > 4<<10 {
			return fmt.Errorf("selection value %q is invalid", field)
		}
	}
	return nil
}

// SelectionFilterID is the stable identity of an interaction-derived filter. It
// is namespaced so it can never collide with a declared global filter's ID, and
// it stays deterministic so batch deduplication keeps working.
func SelectionFilterID(interactionID askdata.ID, targetField string) askdata.ID {
	return askdata.ID("interaction:" + string(interactionID) + ":" + targetField)
}

// ResolveSelections turns viewer interactions into per-target temporary
// filters, using only what the definition declares.
//
// A selection on a component with no declared interaction produces nothing:
// cross-filtering is a property of the report, not something a caller can
// assert. Only FILTER and DRILL_DOWN narrow a query; HIGHLIGHT and
// NAVIGATE_PAGE are presentation effects the client applies itself.
func ResolveSelections(
	definition report.ReportDefinition,
	selections []ReportSelection,
) (map[askdata.ID][]ResolvedFilter, error) {
	if len(selections) > MaxSelections {
		return nil, fmt.Errorf("selections exceed %d items", MaxSelections)
	}
	components := make(map[askdata.ID]report.Component, len(definition.Components))
	for _, component := range definition.Components {
		components[component.ID] = component
	}
	bySource := make(map[askdata.ID][]report.Interaction, len(definition.Interactions))
	for _, interaction := range definition.Interactions {
		bySource[interaction.SourceComponentID] = append(bySource[interaction.SourceComponentID], interaction)
	}

	result := map[askdata.ID][]ResolvedFilter{}
	seen := map[askdata.ID]struct{}{}
	for _, selection := range selections {
		if err := selection.validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[selection.ComponentID]; duplicate {
			return nil, fmt.Errorf("selection for component %q is duplicated", selection.ComponentID)
		}
		seen[selection.ComponentID] = struct{}{}
		if _, exists := components[selection.ComponentID]; !exists {
			return nil, fmt.Errorf("selection component %q does not exist", selection.ComponentID)
		}
		for _, interaction := range bySource[selection.ComponentID] {
			if interaction.Action != report.InteractionFilter && interaction.Action != report.InteractionDrillDown {
				continue
			}
			for _, mapping := range interaction.FieldMappings {
				raw, provided := selection.Values[mapping.SourceField]
				if !provided {
					continue
				}
				value, err := decodeSelectionValue(raw)
				if err != nil {
					return nil, fmt.Errorf("selection %q field %q: %w", selection.ComponentID, mapping.SourceField, err)
				}
				for _, targetID := range interaction.TargetComponentIDs {
					target, exists := components[targetID]
					if !exists {
						return nil, fmt.Errorf("interaction %q targets missing component %q", interaction.ID, targetID)
					}
					// A pinned semantic query cannot be narrowed without changing
					// its fixed plan hash, so it never receives an interaction
					// filter. Publication validation rejects such an interaction
					// outright; skipping here keeps an older definition safe.
					if target.DataBinding == nil ||
						target.DataBinding.BindingMode != report.BindingDatasetField ||
						target.DataBinding.DataContextID == nil {
						continue
					}
					result[targetID] = append(result[targetID], ResolvedFilter{
						ID:            SelectionFilterID(interaction.ID, mapping.TargetField),
						Field:         mapping.TargetField,
						DataContextID: *target.DataBinding.DataContextID,
						Value:         value,
						Temporary:     true,
					})
				}
			}
		}
	}
	for targetID := range result {
		filters := result[targetID]
		sort.Slice(filters, func(left, right int) bool { return filters[left].ID < filters[right].ID })
		result[targetID] = filters
	}
	return result, nil
}

// decodeSelectionValue accepts only the scalar and scalar-array shapes the
// dataset predicate builder understands, so an interaction can never smuggle a
// structured payload into a governed query.
func decodeSelectionValue(raw json.RawMessage) (any, error) {
	// UseNumber keeps identifiers and measures exact; decoding into float64
	// would silently reshape a large key before it reaches a predicate.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("value is not valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("value has trailing JSON")
	}
	switch value := decoded.(type) {
	case string, bool, json.Number:
		return value, nil
	case []any:
		if len(value) == 0 || len(value) > 1_000 {
			return nil, errors.New("value list length is out of range")
		}
		items := make([]string, 0, len(value))
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				items = append(items, typed)
			case json.Number:
				items = append(items, typed.String())
			default:
				return nil, errors.New("value list must contain scalars")
			}
		}
		return items, nil
	default:
		return nil, errors.New("value must be a scalar or a list of scalars")
	}
}
