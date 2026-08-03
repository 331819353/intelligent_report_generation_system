package reportjson

// ResponsiveLayout 是 GridStack 适配器可消费的稳定布局合同。像素状态不进入 DSL。
type ResponsiveLayout struct {
	Columns     int            `json:"columns"`
	RowHeight   int            `json:"rowHeight"`
	Margin      int            `json:"margin,omitempty"`
	Breakpoints map[string]int `json:"breakpoints"`
}

type GlobalFilter struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	Type         string         `json:"type"`
	Source       FilterSource   `json:"source"`
	Operator     string         `json:"operator"`
	DefaultValue any            `json:"defaultValue,omitempty"`
	Required     bool           `json:"required"`
	MultiValue   bool           `json:"multiValue,omitempty"`
	Options      []FilterOption `json:"options,omitempty"`
	Appearance   map[string]any `json:"appearance,omitempty"`
}

type FilterSource struct {
	SemanticModelID string `json:"semanticModelId"`
	DimensionID     string `json:"dimensionId"`
}

type FilterOption struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}

type Card struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	CardVersion  string                `json:"cardVersion"`
	Layout       map[string]Grid       `json:"layout"`
	Appearance   CardAppearance        `json:"appearance"`
	Config       map[string]any        `json:"config,omitempty"`
	Binding      CardBinding           `json:"binding"`
	Interactions []CardInteraction     `json:"interactions"`
	Permissions  *CardPermissionPolicy `json:"permissions,omitempty"`
	Extensions   map[string]any        `json:"extensions,omitempty"`
}

type CardAppearance struct {
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle,omitempty"`
	Description string `json:"description,omitempty"`
	ShowHeader  *bool  `json:"showHeader,omitempty"`
	HeightMode  string `json:"heightMode,omitempty"`
}

type CardBinding struct {
	SemanticModelID      string                `json:"semanticModelId,omitempty"`
	Metrics              []MetricBinding       `json:"metrics"`
	Dimensions           []DimensionBinding    `json:"dimensions"`
	GlobalFilterBindings []GlobalFilterBinding `json:"globalFilterBindings"`
	Filters              []CardFilter          `json:"filters"`
	Sort                 []CardSort            `json:"sort"`
	Limit                int                   `json:"limit,omitempty"`
}

type MetricBinding struct {
	ID        string `json:"id"`
	Version   int    `json:"version,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Role      string `json:"role"`
	Alias     string `json:"alias,omitempty"`
}

type DimensionBinding struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Alias string `json:"alias,omitempty"`
}

type GlobalFilterBinding struct {
	FilterID          string `json:"filterId"`
	TargetDimensionID string `json:"targetDimensionId"`
	Enabled           *bool  `json:"enabled,omitempty"`
}

type CardFilter struct {
	DimensionID string `json:"dimensionId"`
	Operator    string `json:"operator"`
	Value       any    `json:"value"`
}

type CardSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type CardInteraction struct {
	ID     string            `json:"id"`
	Event  string            `json:"event"`
	Action InteractionAction `json:"action"`
}

type InteractionAction struct {
	Type           string            `json:"type"`
	PathID         string            `json:"pathId,omitempty"`
	ToDimension    string            `json:"toDimension,omitempty"`
	TargetReportID string            `json:"targetReportId,omitempty"`
	TargetCardID   string            `json:"targetCardId,omitempty"`
	URL            string            `json:"url,omitempty"`
	ParameterMap   map[string]string `json:"parameterMap,omitempty"`
}

type CardPermissionPolicy struct {
	RequiredPermission string   `json:"requiredPermission,omitempty"`
	AllowedRoleCodes   []string `json:"allowedRoleCodes,omitempty"`
	DenyDownload       bool     `json:"denyDownload,omitempty"`
}

func (document Document) IsCardDSL() bool { return document.SchemaVersion == CardSchemaVersion }
