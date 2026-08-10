// Package template owns the versioned Component Manifest registry used by all
// report producers and consumers.
package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	MaxOptionProperties = 64
	MaxRoles            = 16
	MaxInteractions     = 16
)

var (
	manifestTypePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	optionNamePattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	migratorIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,127}$`)
)

type Manifest struct {
	Type                     string                     `json:"type"`
	Version                  string                     `json:"version"`
	Renderer                 Renderer                   `json:"renderer"`
	DisplayName              string                     `json:"displayName"`
	Category                 Category                   `json:"category"`
	MinSize                  GridSize                   `json:"minSize"`
	RecommendedSize          GridSize                   `json:"recommendedSize"`
	DataContract             DataContract               `json:"dataContract"`
	StackingRequiresAdditive bool                       `json:"stackingRequiresAdditive"`
	OptionSchema             OptionSchema               `json:"optionSchema"`
	DefaultOptions           map[string]json.RawMessage `json:"defaultOptions"`
	MobilePolicy             MobilePolicy               `json:"mobilePolicy"`
	SupportedInteractions    []InteractionKind          `json:"supportedInteractions"`
	Migration                *Migration                 `json:"migration,omitempty"`
}

type Renderer string

const (
	RendererECharts Renderer = "ECHARTS"
	RendererReact   Renderer = "REACT"
	RendererText    Renderer = "TEXT"
	RendererImage   Renderer = "IMAGE"
	RendererControl Renderer = "CONTROL"
)

type Category string

const (
	CategoryChart   Category = "CHART"
	CategoryTable   Category = "TABLE"
	CategoryContent Category = "CONTENT"
	CategoryControl Category = "CONTROL"
)

type GridSize struct {
	W int `json:"w"`
	H int `json:"h"`
}

type DataContract struct {
	Dimensions Cardinality   `json:"dimensions"`
	Measures   Cardinality   `json:"measures"`
	TimeField  TimeField     `json:"timeField"`
	Roles      []BindingRole `json:"roles"`
}

type Cardinality struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type TimeField struct {
	Required bool `json:"required"`
}

type BindingRole string

const (
	RoleDimension BindingRole = "DIMENSION"
	RoleSeries    BindingRole = "SERIES"
	RoleXAxis     BindingRole = "X_AXIS"
	RoleYAxis     BindingRole = "Y_AXIS"
	RoleValue     BindingRole = "VALUE"
	RoleCategory  BindingRole = "CATEGORY"
	RoleTime      BindingRole = "TIME"
	RoleColor     BindingRole = "COLOR"
	RoleSize      BindingRole = "SIZE"
	RoleLabel     BindingRole = "LABEL"
	RoleDetail    BindingRole = "DETAIL"
	RoleTooltip   BindingRole = "TOOLTIP"
)

type OptionSchema struct {
	Type                 string                          `json:"type"`
	AdditionalProperties bool                            `json:"additionalProperties"`
	Required             []string                        `json:"required"`
	Properties           map[string]OptionPropertySchema `json:"properties"`
}

type OptionPropertySchema struct {
	Type        OptionValueType `json:"type"`
	Description string          `json:"description,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Minimum     *float64        `json:"minimum,omitempty"`
	Maximum     *float64        `json:"maximum,omitempty"`
}

type OptionValueType string

const (
	OptionBoolean OptionValueType = "boolean"
	OptionString  OptionValueType = "string"
	OptionInteger OptionValueType = "integer"
	OptionNumber  OptionValueType = "number"
)

type MobilePolicy struct {
	Supported         bool             `json:"supported"`
	DefaultLegendMode LegendMode       `json:"defaultLegendMode"`
	LabelDegradation  LabelDegradation `json:"labelDegradation"`
}

type LegendMode string

const (
	LegendVisible LegendMode = "VISIBLE"
	LegendHidden  LegendMode = "HIDDEN"
	LegendScroll  LegendMode = "SCROLL"
)

type LabelDegradation string

const (
	LabelNone          LabelDegradation = "NONE"
	LabelHideWhenDense LabelDegradation = "HIDE_WHEN_DENSE"
	LabelEllipsis      LabelDegradation = "ELLIPSIS"
	LabelRotate        LabelDegradation = "ROTATE"
)

type InteractionKind string

const (
	InteractionClickFilter InteractionKind = "CLICK_FILTER"
	InteractionDrillDown   InteractionKind = "DRILL_DOWN"
	InteractionBrush       InteractionKind = "BRUSH"
	InteractionZoom        InteractionKind = "ZOOM"
	InteractionSort        InteractionKind = "SORT"
	InteractionPaginate    InteractionKind = "PAGINATE"
)

type Migration struct {
	From       string `json:"from"`
	MigratorID string `json:"migratorId"`
}

func DecodeManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := askdata.DecodeStrictJSON(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if !manifestTypePattern.MatchString(manifest.Type) {
		return errors.New("type is invalid")
	}
	if _, err := parseSemver(manifest.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if !validRenderer(manifest.Renderer) {
		return errors.New("renderer is invalid")
	}
	if strings.TrimSpace(manifest.DisplayName) == "" || len([]rune(manifest.DisplayName)) > 128 {
		return errors.New("displayName must contain 1..128 characters")
	}
	if !validCategory(manifest.Category) {
		return errors.New("category is invalid")
	}
	if err := manifest.MinSize.validate(); err != nil {
		return fmt.Errorf("minSize: %w", err)
	}
	if err := manifest.RecommendedSize.validate(); err != nil {
		return fmt.Errorf("recommendedSize: %w", err)
	}
	if manifest.RecommendedSize.W < manifest.MinSize.W || manifest.RecommendedSize.H < manifest.MinSize.H {
		return errors.New("recommendedSize must not be smaller than minSize")
	}
	if err := manifest.DataContract.validate(); err != nil {
		return fmt.Errorf("dataContract: %w", err)
	}
	if err := manifest.OptionSchema.validate(); err != nil {
		return fmt.Errorf("optionSchema: %w", err)
	}
	if manifest.DefaultOptions == nil {
		return errors.New("defaultOptions must be a JSON object")
	}
	if err := manifest.OptionSchema.ValidateObject(manifest.DefaultOptions); err != nil {
		return fmt.Errorf("defaultOptions: %w", err)
	}
	if !validLegendMode(manifest.MobilePolicy.DefaultLegendMode) {
		return errors.New("mobilePolicy.defaultLegendMode is invalid")
	}
	if !validLabelDegradation(manifest.MobilePolicy.LabelDegradation) {
		return errors.New("mobilePolicy.labelDegradation is invalid")
	}
	if len(manifest.SupportedInteractions) > MaxInteractions {
		return fmt.Errorf("supportedInteractions exceeds %d items", MaxInteractions)
	}
	seenInteractions := map[InteractionKind]struct{}{}
	for index, interaction := range manifest.SupportedInteractions {
		if !validInteraction(interaction) {
			return fmt.Errorf("supportedInteractions[%d] is invalid", index)
		}
		if _, exists := seenInteractions[interaction]; exists {
			return fmt.Errorf("supportedInteractions[%d] is duplicated", index)
		}
		seenInteractions[interaction] = struct{}{}
	}
	if manifest.SupportedInteractions == nil {
		return errors.New("supportedInteractions must be a JSON array, not null")
	}
	if manifest.Migration != nil {
		if _, err := parseSemver(manifest.Migration.From); err != nil {
			return fmt.Errorf("migration.from: %w", err)
		}
		if !migratorIDPattern.MatchString(manifest.Migration.MigratorID) {
			return errors.New("migration.migratorId is invalid")
		}
	}
	return nil
}

func (size GridSize) validate() error {
	if size.W < 1 || size.W > 24 || size.H < 1 || size.H > 100 {
		return errors.New("w/h are outside the 24x100 logical grid")
	}
	return nil
}

func (contract DataContract) validate() error {
	if err := contract.Dimensions.validate(); err != nil {
		return fmt.Errorf("dimensions: %w", err)
	}
	if err := contract.Measures.validate(); err != nil {
		return fmt.Errorf("measures: %w", err)
	}
	if contract.Roles == nil {
		return errors.New("roles must be a JSON array, not null")
	}
	if len(contract.Roles) > MaxRoles {
		return fmt.Errorf("roles exceeds %d items", MaxRoles)
	}
	seen := map[BindingRole]struct{}{}
	for index, role := range contract.Roles {
		if !validBindingRole(role) {
			return fmt.Errorf("roles[%d] is invalid", index)
		}
		if _, exists := seen[role]; exists {
			return fmt.Errorf("roles[%d] is duplicated", index)
		}
		seen[role] = struct{}{}
	}
	return nil
}

func (cardinality Cardinality) validate() error {
	if cardinality.Min < 0 || cardinality.Max < cardinality.Min || cardinality.Max > 32 {
		return errors.New("min/max must satisfy 0 <= min <= max <= 32")
	}
	return nil
}

func (schema OptionSchema) validate() error {
	if schema.Type != "object" {
		return errors.New("type must be object")
	}
	if schema.AdditionalProperties {
		return errors.New("additionalProperties must be false")
	}
	if schema.Required == nil || schema.Properties == nil {
		return errors.New("required and properties must not be null")
	}
	if len(schema.Properties) > MaxOptionProperties {
		return fmt.Errorf("properties exceeds %d items", MaxOptionProperties)
	}
	for name, property := range schema.Properties {
		if !optionNamePattern.MatchString(name) {
			return fmt.Errorf("property %q has an invalid name", name)
		}
		if err := property.validate(); err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}
	}
	seenRequired := map[string]struct{}{}
	for index, name := range schema.Required {
		if _, exists := schema.Properties[name]; !exists {
			return fmt.Errorf("required[%d] references unknown property %q", index, name)
		}
		if _, exists := seenRequired[name]; exists {
			return fmt.Errorf("required[%d] is duplicated", index)
		}
		seenRequired[name] = struct{}{}
	}
	return nil
}

func (property OptionPropertySchema) validate() error {
	if property.Type != OptionBoolean && property.Type != OptionString && property.Type != OptionInteger && property.Type != OptionNumber {
		return errors.New("type is invalid")
	}
	if len([]rune(property.Description)) > 4096 {
		return errors.New("description exceeds 4096 characters")
	}
	if len(property.Enum) > 64 {
		return errors.New("enum exceeds 64 items")
	}
	if len(property.Enum) > 0 && property.Type != OptionString {
		return errors.New("enum is only supported for string options")
	}
	seen := map[string]struct{}{}
	for index, value := range property.Enum {
		if len([]rune(value)) > 4096 {
			return fmt.Errorf("enum[%d] exceeds 4096 characters", index)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("enum[%d] is duplicated", index)
		}
		seen[value] = struct{}{}
	}
	if property.Minimum != nil && (math.IsNaN(*property.Minimum) || math.IsInf(*property.Minimum, 0)) {
		return errors.New("minimum must be finite")
	}
	if property.Maximum != nil && (math.IsNaN(*property.Maximum) || math.IsInf(*property.Maximum, 0)) {
		return errors.New("maximum must be finite")
	}
	if property.Minimum != nil && property.Maximum != nil && *property.Minimum > *property.Maximum {
		return errors.New("minimum must not exceed maximum")
	}
	return nil
}

func (schema OptionSchema) ValidateJSON(raw []byte) error {
	var options map[string]json.RawMessage
	if err := askdata.DecodeStrictJSON(raw, &options); err != nil {
		return err
	}
	if options == nil {
		return errors.New("options must be a JSON object")
	}
	return schema.ValidateObject(options)
}

func (schema OptionSchema) ValidateObject(options map[string]json.RawMessage) error {
	if err := schema.validate(); err != nil {
		return err
	}
	for _, required := range schema.Required {
		if _, exists := options[required]; !exists {
			return fmt.Errorf("required option %q is missing", required)
		}
	}
	keys := make([]string, 0, len(options))
	for name := range options {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		property, exists := schema.Properties[name]
		if !exists {
			return fmt.Errorf("unknown option %q", name)
		}
		if err := property.validateValue(options[name]); err != nil {
			return fmt.Errorf("option %q: %w", name, err)
		}
	}
	return nil
}

func (property OptionPropertySchema) validateValue(raw json.RawMessage) error {
	switch property.Type {
	case OptionBoolean:
		var value bool
		if err := decodeOptionValue(raw, &value); err != nil {
			return errors.New("must be a boolean")
		}
	case OptionString:
		var value string
		if err := decodeOptionValue(raw, &value); err != nil {
			return errors.New("must be a string")
		}
		if len([]rune(value)) > 4096 {
			return errors.New("exceeds 4096 characters")
		}
		if len(property.Enum) > 0 && !containsString(property.Enum, value) {
			return fmt.Errorf("must be one of %v", property.Enum)
		}
	case OptionInteger:
		var value int64
		if err := decodeOptionValue(raw, &value); err != nil {
			return errors.New("must be an integer")
		}
		if err := property.validateNumber(float64(value)); err != nil {
			return err
		}
	case OptionNumber:
		var value float64
		if err := decodeOptionValue(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("must be a finite number")
		}
		if err := property.validateNumber(value); err != nil {
			return err
		}
	default:
		return errors.New("has an invalid schema type")
	}
	return nil
}

func decodeOptionValue(raw json.RawMessage, destination any) error {
	return askdata.DecodeStrictJSON(raw, destination)
}

func (property OptionPropertySchema) validateNumber(value float64) error {
	if property.Minimum != nil && value < *property.Minimum {
		return fmt.Errorf("must be >= %v", *property.Minimum)
	}
	if property.Maximum != nil && value > *property.Maximum {
		return fmt.Errorf("must be <= %v", *property.Maximum)
	}
	return nil
}

func validRenderer(value Renderer) bool {
	return value == RendererECharts || value == RendererReact || value == RendererText || value == RendererImage || value == RendererControl
}

func validCategory(value Category) bool {
	return value == CategoryChart || value == CategoryTable || value == CategoryContent || value == CategoryControl
}

func validBindingRole(value BindingRole) bool {
	switch value {
	case RoleDimension, RoleSeries, RoleXAxis, RoleYAxis, RoleValue, RoleCategory, RoleTime, RoleColor, RoleSize, RoleLabel, RoleDetail, RoleTooltip:
		return true
	default:
		return false
	}
}

func validLegendMode(value LegendMode) bool {
	return value == LegendVisible || value == LegendHidden || value == LegendScroll
}

func validLabelDegradation(value LabelDegradation) bool {
	return value == LabelNone || value == LabelHideWhenDense || value == LabelEllipsis || value == LabelRotate
}

func validInteraction(value InteractionKind) bool {
	switch value {
	case InteractionClickFilter, InteractionDrillDown, InteractionBrush, InteractionZoom, InteractionSort, InteractionPaginate:
		return true
	default:
		return false
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
