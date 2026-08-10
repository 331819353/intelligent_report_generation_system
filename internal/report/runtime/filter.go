package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

type RelativeTimeWindow struct {
	Start        time.Time `json:"start"`
	EndExclusive time.Time `json:"endExclusive"`
	Unit         string    `json:"unit"`
	Offset       int       `json:"offset"`
}

// ResolveDefaultFilterValues evaluates publication-safe defaults at open time.
// Relative windows are half-open and resolved in the report's business
// timezone so callers never have to guess month/week boundaries in the UI.
func ResolveDefaultFilterValues(definition report.ReportDefinition, now time.Time, location *time.Location) (map[askdata.ID]any, error) {
	if location == nil {
		return nil, fmt.Errorf("report filter location is required")
	}
	result := make(map[askdata.ID]any)
	for _, filter := range definition.GlobalFilters {
		if filter.DefaultValue == nil {
			continue
		}
		value := filter.DefaultValue
		if strings.EqualFold(value.Mode, "RELATIVE") {
			offset := 0
			if value.Offset != nil {
				offset = *value.Offset
			}
			start, end, err := relativeWindow(now.In(location), value.Unit, offset)
			if err != nil {
				return nil, fmt.Errorf("filter %q: %w", filter.ID, err)
			}
			result[filter.ID] = RelativeTimeWindow{Start: start, EndExclusive: end, Unit: strings.ToUpper(value.Unit), Offset: offset}
			continue
		}
		switch {
		case len(value.Values) > 0:
			if filter.Type == report.FilterMultiSelect {
				result[filter.ID] = append([]string(nil), value.Values...)
			} else {
				result[filter.ID] = value.Values[0]
			}
		case value.Minimum != nil || value.Maximum != nil:
			result[filter.ID] = NumberRangeValue{Minimum: value.Minimum, Maximum: value.Maximum}
		case value.Boolean != nil:
			result[filter.ID] = *value.Boolean
		}
	}
	return result, nil
}

func relativeWindow(now time.Time, unit string, offset int) (time.Time, time.Time, error) {
	location := now.Location()
	unit = strings.ToUpper(strings.TrimSpace(unit))
	var start time.Time
	switch unit {
	case "DAY":
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location).AddDate(0, 0, offset)
		return start, start.AddDate(0, 0, 1), nil
	case "WEEK":
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
		weekday := (int(start.Weekday()) + 6) % 7
		start = start.AddDate(0, 0, -weekday+offset*7)
		return start, start.AddDate(0, 0, 7), nil
	case "MONTH":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location).AddDate(0, offset, 0)
		return start, start.AddDate(0, 1, 0), nil
	case "QUARTER":
		month := time.Month(((int(now.Month())-1)/3)*3 + 1)
		start = time.Date(now.Year(), month, 1, 0, 0, 0, 0, location).AddDate(0, offset*3, 0)
		return start, start.AddDate(0, 3, 0), nil
	case "YEAR":
		start = time.Date(now.Year()+offset, 1, 1, 0, 0, 0, 0, location)
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported relative unit %q", unit)
	}
}

type ResolvedFilter struct {
	ID            askdata.ID `json:"id"`
	Field         string     `json:"field"`
	DataContextID askdata.ID `json:"dataContextId"`
	Value         any        `json:"value"`
	Temporary     bool       `json:"temporary"`
}

type DateRangeValue struct {
	Start        string `json:"start"`
	EndExclusive string `json:"endExclusive"`
}

type RelativeFilterValue struct {
	Unit   string `json:"unit"`
	Offset int    `json:"offset"`
}

type NumberRangeValue struct {
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
}

// ParseFilterValue is the closed, JSON-facing value contract for the eight
// runtime filter controls. Keeping this parsing on the server prevents UI
// representations from becoming an implicit query language.
func ParseFilterValue(filterType report.FilterType, raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	decode := func(target any) error {
		if err := decoder.Decode(target); err != nil {
			return err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return fmt.Errorf("filter value has trailing JSON")
		}
		return nil
	}
	parseDate := func(value string) (string, error) {
		if len(value) != len("2006-01-02") {
			return "", fmt.Errorf("date must use YYYY-MM-DD")
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil || parsed.Format("2006-01-02") != value {
			return "", fmt.Errorf("date must use YYYY-MM-DD")
		}
		return value, nil
	}
	switch filterType {
	case report.FilterSingleSelect, report.FilterSearchSelect, report.FilterSelect:
		var value string
		if err := decode(&value); err != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s value must be a non-empty string", filterType)
		}
		return value, nil
	case report.FilterMultiSelect:
		var values []string
		if err := decode(&values); err != nil || values == nil {
			return nil, fmt.Errorf("MULTI_SELECT value must be an array")
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("MULTI_SELECT values must be non-empty strings")
			}
			if _, exists := seen[value]; exists {
				return nil, fmt.Errorf("MULTI_SELECT values must be unique")
			}
			seen[value] = struct{}{}
		}
		return values, nil
	case report.FilterDate:
		var value string
		if err := decode(&value); err != nil {
			return nil, fmt.Errorf("DATE value must be a string")
		}
		return parseDate(value)
	case report.FilterDateRange:
		var value DateRangeValue
		if err := decode(&value); err != nil {
			return nil, fmt.Errorf("DATE_RANGE value is invalid: %w", err)
		}
		start, err := parseDate(value.Start)
		if err != nil {
			return nil, err
		}
		end, err := parseDate(value.EndExclusive)
		if err != nil {
			return nil, err
		}
		if start >= end {
			return nil, fmt.Errorf("DATE_RANGE must be a non-empty half-open interval")
		}
		return value, nil
	case report.FilterRelativeTime:
		var value RelativeFilterValue
		if err := decode(&value); err != nil {
			return nil, fmt.Errorf("RELATIVE_TIME value is invalid: %w", err)
		}
		value.Unit = strings.ToUpper(strings.TrimSpace(value.Unit))
		if _, _, err := relativeWindow(time.Unix(0, 0).UTC(), value.Unit, value.Offset); err != nil {
			return nil, err
		}
		return value, nil
	case report.FilterNumberRange:
		var value NumberRangeValue
		if err := decode(&value); err != nil {
			return nil, fmt.Errorf("NUMBER_RANGE value is invalid: %w", err)
		}
		if value.Minimum == nil && value.Maximum == nil {
			return nil, fmt.Errorf("NUMBER_RANGE requires at least one bound")
		}
		if value.Minimum != nil && (math.IsNaN(*value.Minimum) || math.IsInf(*value.Minimum, 0)) {
			return nil, fmt.Errorf("NUMBER_RANGE minimum must be finite")
		}
		if value.Maximum != nil && (math.IsNaN(*value.Maximum) || math.IsInf(*value.Maximum, 0)) {
			return nil, fmt.Errorf("NUMBER_RANGE maximum must be finite")
		}
		if value.Minimum != nil && value.Maximum != nil && *value.Minimum > *value.Maximum {
			return nil, fmt.Errorf("NUMBER_RANGE minimum must not exceed maximum")
		}
		return value, nil
	case report.FilterParameterInput, report.FilterBoolean:
		var value any
		if err := decode(&value); err != nil {
			return nil, fmt.Errorf("PARAMETER_INPUT value is invalid: %w", err)
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				return nil, fmt.Errorf("PARAMETER_INPUT string must be non-empty")
			}
			return typed, nil
		case json.Number:
			number, err := typed.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("PARAMETER_INPUT number must be finite")
			}
			return typed, nil
		case bool:
			return typed, nil
		default:
			return nil, fmt.Errorf("PARAMETER_INPUT must be a string, number, or boolean")
		}
	default:
		return nil, fmt.Errorf("unsupported filter type %q", filterType)
	}
}

func SerializeFilterValue(filterType report.FilterType, value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode filter value: %w", err)
	}
	if _, err := ParseFilterValue(filterType, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

type TemporaryFilterStore struct {
	values map[askdata.ID]ResolvedFilter
}

func NewTemporaryFilterStore() *TemporaryFilterStore {
	return &TemporaryFilterStore{values: map[askdata.ID]ResolvedFilter{}}
}

func (store *TemporaryFilterStore) Set(filter ResolvedFilter) {
	if store.values == nil {
		store.values = map[askdata.ID]ResolvedFilter{}
	}
	filter.Temporary = true
	store.values[filter.ID] = filter
}

func (store *TemporaryFilterStore) Snapshot() []ResolvedFilter {
	result := make([]ResolvedFilter, 0, len(store.values))
	for _, value := range store.values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (store *TemporaryFilterStore) Restore(snapshot []ResolvedFilter) error {
	restored := make(map[askdata.ID]ResolvedFilter, len(snapshot))
	for _, filter := range snapshot {
		if filter.ID.Validate() != nil || filter.DataContextID.Validate() != nil || strings.TrimSpace(filter.Field) == "" || filter.Value == nil {
			return fmt.Errorf("temporary filter snapshot is invalid")
		}
		if _, exists := restored[filter.ID]; exists {
			return fmt.Errorf("temporary filter snapshot contains duplicate id %q", filter.ID)
		}
		filter.Temporary = true
		restored[filter.ID] = filter
	}
	store.values = restored
	return nil
}

// ResolveFilters returns filters for a component. Narrower scopes overwrite a
// report-wide filter with the same ID; absent scope never propagates.
func ResolveFilters(definition report.ReportDefinition, pageID, sectionID, blockID, componentID askdata.ID, values map[askdata.ID]any, temporary []ResolvedFilter) ([]ResolvedFilter, error) {
	type candidate struct {
		filter      ResolvedFilter
		specificity int
	}
	resolved := map[string]candidate{}
	for _, filter := range definition.GlobalFilters {
		specificity, applies := filterSpecificity(filter.Scope, pageID, sectionID, blockID, componentID)
		if !applies {
			continue
		}
		value, exists := values[filter.ID]
		if !exists && filter.DefaultValue != nil {
			value = *filter.DefaultValue
		}
		if value == nil {
			continue
		}
		key := filterBindingKey(filter.FieldRef.DataContextID, filter.FieldRef.Field)
		current, exists := resolved[key]
		if exists && (current.specificity > specificity || (current.specificity == specificity && current.filter.ID < filter.ID)) {
			continue
		}
		resolved[key] = candidate{
			filter:      ResolvedFilter{ID: filter.ID, Field: filter.FieldRef.Field, DataContextID: filter.FieldRef.DataContextID, Value: value},
			specificity: specificity,
		}
	}
	for _, filter := range temporary {
		if filter.ID == "" || filter.Field == "" || filter.DataContextID == "" {
			return nil, fmt.Errorf("temporary filter is incomplete")
		}
		filter.Temporary = true
		resolved[filterBindingKey(filter.DataContextID, filter.Field)] = candidate{filter: filter, specificity: 5}
	}
	result := make([]ResolvedFilter, 0, len(resolved))
	for _, selected := range resolved {
		result = append(result, selected.filter)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func filterBindingKey(dataContextID askdata.ID, field string) string {
	return string(dataContextID) + "\x00" + field
}

func filterSpecificity(scope report.FilterScope, pageID, sectionID, blockID, componentID askdata.ID) (int, bool) {
	var wanted askdata.ID
	var specificity int
	switch scope.Type {
	case report.FilterScopeReport:
		return 0, true
	case report.FilterScopePage:
		wanted, specificity = pageID, 1
	case report.FilterScopeSection:
		wanted, specificity = sectionID, 2
	case report.FilterScopeBlock:
		wanted, specificity = blockID, 3
	case report.FilterScopeComponent:
		wanted, specificity = componentID, 4
	default:
		return 0, false
	}
	for _, target := range scope.TargetIDs {
		if target == wanted {
			return specificity, true
		}
	}
	return 0, false
}

func filterApplies(scope report.FilterScope, pageID, sectionID, blockID, componentID askdata.ID) bool {
	_, applies := filterSpecificity(scope, pageID, sectionID, blockID, componentID)
	return applies
}
