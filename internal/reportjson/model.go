package reportjson

import (
	"encoding/json"
	"errors"
)

const SchemaVersion = "1.0"

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
	ID           string         `json:"id"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	DataType     string         `json:"dataType"`
	Required     bool           `json:"required"`
	MultiValue   bool           `json:"multiValue"`
	DefaultValue any            `json:"defaultValue,omitempty"`
	Scope        string         `json:"scope"`
	PageID       string         `json:"pageId,omitempty"`
	OptionSource map[string]any `json:"optionSource,omitempty"`
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
	Grid             Grid              `json:"grid"`
	InnerGrid        InnerGrid         `json:"innerGrid"`
	ZIndex           int               `json:"zIndex,omitempty"`
	Locks            Locks             `json:"locks"`
	Sticky           Sticky            `json:"sticky"`
	Style            map[string]any    `json:"style,omitempty"`
	PermissionPolicy *PermissionPolicy `json:"permissionPolicy,omitempty"`
	Components       []Component       `json:"components"`
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
	Enabled     bool   `json:"enabled"`
	Top         int    `json:"top,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	ZIndex      int    `json:"zIndex,omitempty"`
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
	Sticky           Sticky            `json:"sticky"`
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
