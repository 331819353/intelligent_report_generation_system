package reportjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const SchemaVersion = "1.0"

const (
	// MaxStickyTop 和 MaxStickyZIndex 同时约束编辑器输入与运行态样式，避免异常文档制造超大位移或层级。
	MaxStickyTop    = 10_000
	MaxStickyZIndex = 100_000
)

var ErrInvalidDocument = errors.New("report JSON document is invalid")

// Document 是设计器、在线查看器和导出渲染器共享的正式报告合同。
type Document struct {
	SchemaVersion    string            `json:"schemaVersion"`
	Report           Report            `json:"report"`
	Canvas           Canvas            `json:"canvas"`
	Theme            map[string]any    `json:"theme,omitempty"`
	Parameters       []Parameter       `json:"parameters,omitempty"`
	DataRequirements []DataRequirement `json:"dataRequirements,omitempty"`
	Pages            []Page            `json:"pages"`
	Generation       *Generation       `json:"generation,omitempty"`
	Extensions       map[string]any    `json:"extensions,omitempty"`
}

// Report 保存不依赖数据库记录的报告基本属性和默认运行策略。
type Report struct {
	ID                   string         `json:"id,omitempty"`
	Code                 string         `json:"code"`
	Name                 string         `json:"name"`
	Description          string         `json:"description,omitempty"`
	Type                 string         `json:"type"`
	Language             string         `json:"language"`
	Status               string         `json:"status"`
	Visibility           string         `json:"visibility"`
	PublicAccessPolicy   map[string]any `json:"publicAccessPolicy,omitempty"`
	OnlineEnabled        bool           `json:"onlineEnabled"`
	PDFArchiveEnabled    bool           `json:"pdfArchiveEnabled"`
	DefaultRefreshPolicy string         `json:"defaultRefreshPolicy"`
	Timezone             string         `json:"timezone"`
}

// Canvas 固定首屏基准，只保存逻辑网格，不保存运行时像素高度。
type Canvas struct {
	LogicalWidth        int    `json:"logicalWidth"`
	ViewportHeight      int    `json:"viewportHeight"`
	GridColumns         int    `json:"gridColumns"`
	ViewportGridRows    int    `json:"viewportGridRows"`
	ContentGridRows     string `json:"contentGridRows"`
	MinContentGridRows  int    `json:"minContentGridRows"`
	InnerGridMultiplier int    `json:"innerGridMultiplier"`
	ScaleMode           string `json:"scaleMode"`
	VerticalOverflow    string `json:"verticalOverflow"`
	// 以下字段仅用于读取 0.9 文档，规范化输出时会被移除。
	LogicalHeight *int `json:"logicalHeight,omitempty"`
	GridRows      *int `json:"gridRows,omitempty"`
}

type Parameter struct {
	ID              string           `json:"id"`
	Code            string           `json:"code"`
	Name            string           `json:"name"`
	DataType        string           `json:"dataType"`
	Required        bool             `json:"required"`
	MultiValue      bool             `json:"multiValue"`
	DefaultValue    any              `json:"defaultValue,omitempty"`
	Scope           string           `json:"scope"`
	PageID          string           `json:"pageId,omitempty"`
	OptionSource    map[string]any   `json:"optionSource,omitempty"`
	SemanticBinding *SemanticBinding `json:"semanticBinding,omitempty"`
}

// SemanticBinding 把一个报告参数显式映射到各数据集版本自己的字段和参数，禁止仅凭同名猜测。
type SemanticBinding struct {
	SemanticFieldCode string                `json:"semanticFieldCode"`
	DatasetFields     []DatasetFieldBinding `json:"datasetFields"`
}

type DatasetFieldBinding struct {
	DatasetVersionID     string `json:"datasetVersionId"`
	FieldID              string `json:"fieldId"`
	DatasetParameterCode string `json:"datasetParameterCode"`
}

type DataRequirement struct {
	ID                       string   `json:"id"`
	Intent                   string   `json:"intent"`
	RequiredMetrics          []string `json:"requiredMetrics"`
	RequiredDimensions       []string `json:"requiredDimensions"`
	PreferredDatasetIDs      []string `json:"preferredDatasetIds"`
	ResolvedDatasetVersionID string   `json:"resolvedDatasetVersionId,omitempty"`
	ResolvedMetricIDs        []string `json:"resolvedMetricIds"`
	ResolutionStatus         string   `json:"resolutionStatus"`
	Confidence               float64  `json:"confidence"`
	Warnings                 []string `json:"warnings"`
}

type Page struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Order           int            `json:"order"`
	Background      map[string]any `json:"background,omitempty"`
	ContentGridRows int            `json:"contentGridRows"`
	Blocks          []Block        `json:"blocks"`
}

type Block struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind,omitempty"`
	Name             string            `json:"name,omitempty"`
	Visible          *bool             `json:"visible,omitempty"`
	Grid             Grid              `json:"grid"`
	InnerGrid        InnerGrid         `json:"innerGrid"`
	ZIndex           int               `json:"zIndex,omitempty"`
	Locks            Locks             `json:"locks"`
	Sticky           *Sticky           `json:"sticky"`
	Style            map[string]any    `json:"style,omitempty"`
	PermissionPolicy *PermissionPolicy `json:"permissionPolicy,omitempty"`
	MenuLayout       *MenuLayout       `json:"menuLayout,omitempty"`
	ContentLayout    *ContentLayout    `json:"contentLayout,omitempty"`
	Components       []Component       `json:"components"`
}

type RatioPair [2]float64

type MenuRatios struct {
	TopColumns    RatioPair `json:"topColumns"`
	BottomColumns RatioPair `json:"bottomColumns"`
	RowHeights    RatioPair `json:"rowHeights"`
}

type MenuLogoTitleCell struct {
	Visible  bool   `json:"visible"`
	LogoText string `json:"logoText"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
}

type MenuItemsCell struct {
	Visible bool     `json:"visible"`
	Items   []string `json:"items"`
}

type MenuFiltersCell struct {
	Visible      bool     `json:"visible"`
	ParameterIDs []string `json:"parameterIds"`
}

type MenuNavigationItem struct {
	Label         string `json:"label"`
	TargetBlockID string `json:"targetBlockId,omitempty"`
}

type MenuNavigationCell struct {
	Visible bool                 `json:"visible"`
	Items   []MenuNavigationItem `json:"items"`
}

type MenuCells struct {
	LogoTitle     MenuLogoTitleCell  `json:"logoTitle"`
	Actions       MenuItemsCell      `json:"actions"`
	GlobalFilters MenuFiltersCell    `json:"globalFilters"`
	Navigation    MenuNavigationCell `json:"navigation"`
}

type MenuLayout struct {
	Visible           bool       `json:"visible"`
	DefaultRatios     MenuRatios `json:"defaultRatios"`
	Ratios            MenuRatios `json:"ratios"`
	UsesDefaultRatios bool       `json:"usesDefaultRatios"`
	Cells             MenuCells  `json:"cells"`
}

type ContentArea struct {
	Visible      bool     `json:"visible"`
	ComponentIDs []string `json:"componentIds"`
}

type ContentAreas struct {
	Title      ContentArea `json:"title"`
	Conclusion ContentArea `json:"conclusion"`
	Components ContentArea `json:"components"`
}

type ContentLayout struct {
	Visible bool         `json:"visible"`
	Areas   ContentAreas `json:"areas"`
}

type Grid struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type InnerGrid struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

type Locks struct {
	Layout       bool `json:"layout"`
	Config       bool `json:"config"`
	DataSnapshot bool `json:"dataSnapshot"`
}

// Sticky 只描述浏览态悬浮，不与设计态锁定语义混用。
type Sticky struct {
	Enabled     bool   `json:"-"`
	Top         int    `json:"-"`
	Scope       string `json:"-"`
	ContainerID string `json:"-"`
	ZIndex      int    `json:"-"`

	decoded        bool
	hasEnabled     bool
	hasTop         bool
	hasScope       bool
	hasContainerID bool
	hasZIndex      bool
}

// UnmarshalJSON 记录字段是否真实出现，使 Go 校验能够区分 top:0 与缺少 top。
// 自定义解码会绕过外层 DisallowUnknownFields，因此这里必须同步拒绝未知字段。
func (sticky *Sticky) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("sticky 必须为对象")
	}
	*sticky = Sticky{decoded: true}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s: 不能为 null", key)
		}
		switch key {
		case "enabled":
			sticky.hasEnabled = true
			if err := json.Unmarshal(value, &sticky.Enabled); err != nil {
				return fmt.Errorf("enabled: %w", err)
			}
		case "top":
			sticky.hasTop = true
			if err := json.Unmarshal(value, &sticky.Top); err != nil {
				return fmt.Errorf("top: %w", err)
			}
		case "scope":
			sticky.hasScope = true
			if err := json.Unmarshal(value, &sticky.Scope); err != nil {
				return fmt.Errorf("scope: %w", err)
			}
		case "containerId":
			sticky.hasContainerID = true
			if err := json.Unmarshal(value, &sticky.ContainerID); err != nil {
				return fmt.Errorf("containerId: %w", err)
			}
		case "zIndex":
			sticky.hasZIndex = true
			if err := json.Unmarshal(value, &sticky.ZIndex); err != nil {
				return fmt.Errorf("zIndex: %w", err)
			}
		default:
			return fmt.Errorf("未知字段 %q", key)
		}
	}
	return nil
}

// MarshalJSON 只生成禁用态或完整启用态，保证 Prepare 输出可再次通过同一份 Schema。
func (sticky Sticky) MarshalJSON() ([]byte, error) {
	if !sticky.Enabled {
		return json.Marshal(struct {
			Enabled bool `json:"enabled"`
		}{Enabled: false})
	}
	payload := struct {
		Enabled     bool   `json:"enabled"`
		Top         int    `json:"top"`
		Scope       string `json:"scope"`
		ContainerID string `json:"containerId,omitempty"`
		ZIndex      int    `json:"zIndex"`
	}{
		Enabled: sticky.Enabled, Top: sticky.Top, Scope: sticky.Scope,
		ContainerID: sticky.ContainerID, ZIndex: sticky.ZIndex,
	}
	return json.Marshal(payload)
}

type PermissionPolicy struct {
	RequiredPermission string   `json:"requiredPermission,omitempty"`
	AllowedRoleCodes   []string `json:"allowedRoleCodes,omitempty"`
	DenyDownload       bool     `json:"denyDownload,omitempty"`
}

type Component struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Name             string            `json:"name"`
	Grid             Grid              `json:"grid"`
	ZIndex           int               `json:"zIndex,omitempty"`
	Visible          bool              `json:"visible"`
	ManualLocked     bool              `json:"manualLocked"`
	Style            map[string]any    `json:"style,omitempty"`
	Binding          map[string]any    `json:"binding,omitempty"`
	Interaction      map[string]any    `json:"interaction,omitempty"`
	Sticky           *Sticky           `json:"sticky"`
	RefreshPolicy    map[string]any    `json:"refreshPolicy,omitempty"`
	PermissionPolicy *PermissionPolicy `json:"permissionPolicy,omitempty"`
	SourceTrace      []SourceTrace     `json:"sourceTrace"`
	Conclusion       map[string]any    `json:"conclusion,omitempty"`
	Extensions       map[string]any    `json:"extensions,omitempty"`
}

type SourceTrace struct {
	SourceType  string `json:"sourceType"`
	SourceID    string `json:"sourceId"`
	Location    string `json:"location,omitempty"`
	ExcerptHash string `json:"excerptHash,omitempty"`
	Usage       string `json:"usage"`
}

type Generation struct {
	Mode             string   `json:"mode"`
	JobID            string   `json:"jobId,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	ModelRef         string   `json:"modelRef,omitempty"`
	PromptTemplateID string   `json:"promptTemplateId,omitempty"`
	SourceFiles      []string `json:"sourceFiles"`
	RequirementText  string   `json:"requirementText,omitempty"`
	Confidence       float64  `json:"confidence"`
	Warnings         []string `json:"warnings"`
	GeneratedAt      string   `json:"generatedAt,omitempty"`
}

type ValidationIssue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ValidationError struct {
	Issues []ValidationIssue `json:"details"`
}

func (e *ValidationError) Error() string { return "report JSON validation failed" }

type Prepared struct {
	Document Document
	JSON     json.RawMessage
	Hash     string
}
