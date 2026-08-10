package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

// HTTPPlanInput is the closed public request contract for report runtime
// planning and execution. PolicyScopeHash and resolved field references are
// deliberately absent: both are derived by the server.
type HTTPPlanInput struct {
	PageID          askdata.ID                     `json:"pageId"`
	VisibleBlockIDs []askdata.ID                   `json:"visibleBlockIds,omitempty"`
	ExpandedSlotIDs []askdata.ID                   `json:"expandedSlotIds,omitempty"`
	Mobile          bool                           `json:"mobile,omitempty"`
	FilterValues    map[askdata.ID]json.RawMessage `json:"filterValues,omitempty"`
}

func (input HTTPPlanInput) Resolve(
	definition report.ReportDefinition,
	asOf time.Time,
	location *time.Location,
	policyScopeHash string,
) (PlanRequest, error) {
	if input.PageID.Validate() != nil || asOf.IsZero() || location == nil ||
		askdata.ContentHash(policyScopeHash).Validate() != nil ||
		len(input.VisibleBlockIDs) > report.MaxBlocks || len(input.ExpandedSlotIDs) > report.MaxComponents ||
		len(input.FilterValues) > report.MaxGlobalFilters {
		return PlanRequest{}, errors.New("report runtime request is invalid")
	}
	if !definitionHasPage(definition, input.PageID) ||
		validateUniqueIDs(input.VisibleBlockIDs) != nil || validateUniqueIDs(input.ExpandedSlotIDs) != nil {
		return PlanRequest{}, errors.New("report runtime request references an invalid layout target")
	}
	values, err := resolveRuntimeFilterValues(definition, input.FilterValues, asOf, location)
	if err != nil {
		return PlanRequest{}, err
	}
	return PlanRequest{
		PageID: input.PageID, VisibleBlockIDs: append([]askdata.ID(nil), input.VisibleBlockIDs...),
		ExpandedSlotIDs: append([]askdata.ID(nil), input.ExpandedSlotIDs...), Mobile: input.Mobile,
		PolicyScopeHash: policyScopeHash, FilterValues: values,
	}, nil
}

// RuntimeTimezone derives the immutable report business timezone. Semantic
// bindings carry it in their pinned time contract. A dataset-only report has
// no semantic calendar contract and therefore uses UTC. Conflicting pinned
// timezones fail closed instead of applying different date boundaries to
// components in the same report.
func RuntimeTimezone(definition report.ReportDefinition) (*time.Location, error) {
	zone := ""
	for _, component := range definition.Components {
		if component.DataBinding == nil || component.DataBinding.SemanticQueryRef == nil {
			continue
		}
		ref := component.DataBinding.SemanticQueryRef
		candidate := ""
		if ref.ResolvedTimeSpec != nil {
			candidate = strings.TrimSpace(ref.ResolvedTimeSpec.Timezone)
		} else if ref.SemanticIR.TimeRange != nil {
			candidate = strings.TrimSpace(ref.SemanticIR.TimeRange.Timezone)
		}
		if candidate == "" {
			continue
		}
		if zone != "" && zone != candidate {
			return nil, errors.New("report runtime contains conflicting pinned timezones")
		}
		zone = candidate
	}
	if zone == "" {
		zone = "UTC"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, errors.New("report runtime timezone is invalid")
	}
	return location, nil
}

// ViewerPolicyHash scopes report batch deduplication to the authenticated
// viewer and immutable report version. It is not accepted from public input.
func ViewerPolicyHash(identity store.Identity, loaded LoadedReport) (string, error) {
	if identity.Validate() != nil || loaded.ReportID.Validate() != nil || loaded.VersionID.Validate() != nil {
		return "", errors.New("report runtime viewer identity is invalid")
	}
	return string(askdata.HashBytes([]byte(
		"report-runtime-policy-v1\x00" + string(identity.TenantID) + "\x00" +
			string(identity.DomainID) + "\x00" + string(identity.ActorID) + "\x00" +
			string(loaded.ReportID) + "\x00" + string(loaded.VersionID),
	))), nil
}

func resolveRuntimeFilterValues(
	definition report.ReportDefinition,
	raw map[askdata.ID]json.RawMessage,
	asOf time.Time,
	location *time.Location,
) (map[askdata.ID]any, error) {
	values, err := ResolveDefaultFilterValues(definition, asOf, location)
	if err != nil {
		return nil, err
	}
	filters := make(map[askdata.ID]report.GlobalFilter, len(definition.GlobalFilters))
	for _, filter := range definition.GlobalFilters {
		filters[filter.ID] = filter
	}
	for id, payload := range raw {
		filter, exists := filters[id]
		if !exists || id.Validate() != nil || len(payload) == 0 || len(payload) > 16<<10 {
			return nil, fmt.Errorf("report runtime filter %q is invalid", id)
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
			delete(values, id)
			continue
		}
		value, err := ParseFilterValue(filter.Type, payload)
		if err != nil {
			return nil, fmt.Errorf("report runtime filter %q: %w", id, err)
		}
		if relative, ok := value.(RelativeFilterValue); ok {
			start, end, err := relativeWindow(asOf.In(location), relative.Unit, relative.Offset)
			if err != nil {
				return nil, fmt.Errorf("report runtime filter %q: %w", id, err)
			}
			value = RelativeTimeWindow{
				Start: start, EndExclusive: end, Unit: relative.Unit, Offset: relative.Offset,
			}
		}
		values[id] = value
	}
	return values, nil
}

func definitionHasPage(definition report.ReportDefinition, pageID askdata.ID) bool {
	for _, page := range definition.Pages {
		if page.ID == pageID {
			return true
		}
	}
	return false
}

func validateUniqueIDs(values []askdata.ID) error {
	seen := make(map[askdata.ID]struct{}, len(values))
	for _, value := range values {
		if value.Validate() != nil {
			return errors.New("invalid ID")
		}
		if _, duplicate := seen[value]; duplicate {
			return errors.New("duplicate ID")
		}
		seen[value] = struct{}{}
	}
	return nil
}
