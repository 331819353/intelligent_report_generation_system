package reportjson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

var codePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

var componentTypes = map[string]bool{
	"TITLE": true, "RICH_TEXT": true, "FILTER": true, "KPI": true,
	"ADDITIONAL_INFO": true, "TABLE": true, "CROSSTAB": true,
	"CHART": true, "RANKING": true, "IMAGE": true, "ATTACHMENT_LIST": true,
	"DATA_SOURCE": true, "UPDATED_AT": true, "CONCLUSION": true,
	"DIVIDER": true, "DECORATION": true,
}

// Prepare 严格解析、迁移、校验并生成确定性的规范 JSON 与内容哈希。
func Prepare(raw []byte) (Prepared, error) {
	document, err := DecodeAndNormalize(raw)
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if err := Validate(document); err != nil {
		return Prepared{}, err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return Prepared{}, err
	}
	sum := sha256.Sum256(payload)
	return Prepared{Document: document, JSON: payload, Hash: hex.EncodeToString(sum[:])}, nil
}

// DecodeAndNormalize 兼容 0.9 画布字段，并对当前版本执行未知字段拒绝。
func DecodeAndNormalize(raw []byte) (Document, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Document{}, fmt.Errorf("文档不能为空")
	}
	var header struct {
		SchemaVersion string                     `json:"schemaVersion"`
		Canvas        map[string]json.RawMessage `json:"canvas"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return Document{}, fmt.Errorf("读取 schemaVersion: %w", err)
	}
	if header.SchemaVersion == "" {
		header.SchemaVersion = "0.9"
	}
	if header.SchemaVersion != "0.9" && header.SchemaVersion != SchemaVersion {
		return Document{}, fmt.Errorf("不支持的报告 JSON 版本 %q", header.SchemaVersion)
	}
	if header.SchemaVersion == SchemaVersion {
		_, hasLogicalHeight := header.Canvas["logicalHeight"]
		_, hasGridRows := header.Canvas["gridRows"]
		if hasLogicalHeight || hasGridRows {
			return Document{}, fmt.Errorf("1.0 文档不能包含 logicalHeight 或 gridRows 旧画布字段")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("解析报告 JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Document{}, err
	}
	legacy := header.SchemaVersion == "0.9"
	if legacy {
		migrateLegacyCanvas(&document.Canvas)
	}
	document.SchemaVersion = SchemaVersion
	document.Canvas.LogicalHeight = nil
	document.Canvas.GridRows = nil
	normalizeSlices(&document)
	for pageIndex := range document.Pages {
		page := &document.Pages[pageIndex]
		computedRows := document.Canvas.MinContentGridRows
		if computedRows == 0 {
			computedRows = 10
		}
		for blockIndex := range page.Blocks {
			block := &page.Blocks[blockIndex]
			if legacy && block.InnerGrid.Columns == 0 && block.InnerGrid.Rows == 0 {
				block.InnerGrid = InnerGrid{Columns: block.Grid.W * 4, Rows: block.Grid.H * 4}
			}
			if bottom := block.Grid.Y + block.Grid.H; bottom > computedRows {
				computedRows = bottom
			}
			if block.Components == nil {
				block.Components = []Component{}
			}
			for componentIndex := range block.Components {
				if block.Components[componentIndex].SourceTrace == nil {
					block.Components[componentIndex].SourceTrace = []SourceTrace{}
				}
			}
		}
		// contentGridRows 是派生值；省略时补齐，显式错误值留给校验器报告。
		if page.ContentGridRows == 0 {
			page.ContentGridRows = computedRows
		}
	}
	return document, nil
}

func migrateLegacyCanvas(canvas *Canvas) {
	if canvas.ViewportHeight == 0 && canvas.LogicalHeight != nil {
		canvas.ViewportHeight = *canvas.LogicalHeight
	}
	if canvas.ViewportGridRows == 0 && canvas.GridRows != nil {
		canvas.ViewportGridRows = *canvas.GridRows
	}
	if canvas.ContentGridRows == "" {
		canvas.ContentGridRows = "AUTO"
	}
	if canvas.MinContentGridRows == 0 {
		canvas.MinContentGridRows = canvas.ViewportGridRows
	}
	if canvas.InnerGridMultiplier == 0 {
		canvas.InnerGridMultiplier = 4
	}
	if canvas.ScaleMode == "" {
		canvas.ScaleMode = "FIT_WIDTH"
	}
	if canvas.VerticalOverflow == "" {
		canvas.VerticalOverflow = "SCROLL"
	}
}

func normalizeSlices(document *Document) {
	if document.Parameters == nil {
		document.Parameters = []Parameter{}
	}
	if document.DataRequirements == nil {
		document.DataRequirements = []DataRequirement{}
	}
	if document.Pages == nil {
		document.Pages = []Page{}
	}
	for index := range document.DataRequirements {
		requirement := &document.DataRequirements[index]
		if requirement.RequiredMetrics == nil {
			requirement.RequiredMetrics = []string{}
		}
		if requirement.RequiredDimensions == nil {
			requirement.RequiredDimensions = []string{}
		}
		if requirement.PreferredDatasetIDs == nil {
			requirement.PreferredDatasetIDs = []string{}
		}
		if requirement.ResolvedMetricIDs == nil {
			requirement.ResolvedMetricIDs = []string{}
		}
		if requirement.Warnings == nil {
			requirement.Warnings = []string{}
		}
	}
	if document.Generation != nil {
		if document.Generation.SourceFiles == nil {
			document.Generation.SourceFiles = []string{}
		}
		if document.Generation.Warnings == nil {
			document.Generation.Warnings = []string{}
		}
	}
}

// Validate 收集可以同时发现的结构、枚举、引用和二维布局问题。
func Validate(document Document) error {
	issues := make([]ValidationIssue, 0)
	add := func(path, reason string) { issues = append(issues, ValidationIssue{Path: path, Reason: reason}) }
	if document.SchemaVersion != SchemaVersion {
		add("schemaVersion", "必须为 1.0")
	}
	validateReport(&issues, document.Report)
	validateCanvas(&issues, document.Canvas)
	pageIDs := map[string]bool{}
	pageOrders := map[int]bool{}
	blockIDs := map[string]bool{}
	componentIDs := map[string]bool{}
	parameterIDs := map[string]bool{}
	parameterCodes := map[string]bool{}
	for index, parameter := range document.Parameters {
		path := fmt.Sprintf("parameters[%d]", index)
		validateID(&issues, path+".id", parameter.ID)
		if parameterIDs[parameter.ID] {
			add(path+".id", "参数标识重复")
		}
		parameterIDs[parameter.ID] = true
		validateCode(&issues, path+".code", parameter.Code)
		if parameterCodes[parameter.Code] {
			add(path+".code", "参数编码重复")
		}
		parameterCodes[parameter.Code] = true
		if parameter.Name == "" {
			add(path+".name", "不能为空")
		}
		if !oneOf(parameter.DataType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME", "DATE_RANGE", "DATE_YEAR", "DATE_MONTH", "DATE_QUARTER", "SINGLE_SELECT", "MULTI_SELECT", "ORG_TREE", "CASCADE", "SYSTEM") {
			add(path+".dataType", "不支持的参数类型")
		}
		if !oneOf(parameter.Scope, "REPORT", "PAGE") {
			add(path+".scope", "必须为 REPORT 或 PAGE")
		}
		if parameter.Scope == "PAGE" && parameter.PageID == "" {
			add(path+".pageId", "页面参数必须引用 pageId")
		}
	}
	if len(document.Pages) == 0 {
		add("pages", "至少需要一个页面")
	}
	for pageIndex, page := range document.Pages {
		pagePath := fmt.Sprintf("pages[%d]", pageIndex)
		validateID(&issues, pagePath+".id", page.ID)
		if pageIDs[page.ID] {
			add(pagePath+".id", "页面标识重复")
		}
		pageIDs[page.ID] = true
		if page.Name == "" {
			add(pagePath+".name", "不能为空")
		}
		if page.Order < 1 {
			add(pagePath+".order", "必须大于等于 1")
		}
		if pageOrders[page.Order] {
			add(pagePath+".order", "页面顺序重复")
		}
		pageOrders[page.Order] = true
		computedRows := document.Canvas.MinContentGridRows
		for blockIndex, block := range page.Blocks {
			blockPath := fmt.Sprintf("%s.blocks[%d]", pagePath, blockIndex)
			validateID(&issues, blockPath+".id", block.ID)
			if blockIDs[block.ID] {
				add(blockPath+".id", "分块标识在报告内重复")
			}
			blockIDs[block.ID] = true
			validateGrid(&issues, blockPath+".grid", block.Grid, document.Canvas.GridColumns, -1)
			if block.ZIndex < 0 {
				add(blockPath+".zIndex", "不能小于 0")
			}
			if bottom := block.Grid.Y + block.Grid.H; bottom > computedRows {
				computedRows = bottom
			}
			if block.InnerGrid.Columns != block.Grid.W*document.Canvas.InnerGridMultiplier {
				add(blockPath+".innerGrid.columns", "必须等于分块宽度乘以内网格倍数")
			}
			if block.InnerGrid.Rows != block.Grid.H*document.Canvas.InnerGridMultiplier {
				add(blockPath+".innerGrid.rows", "必须等于分块高度乘以内网格倍数")
			}
			validateSticky(&issues, blockPath+".sticky", block.Sticky, "PAGE", "CONTAINER")
			validatePermission(&issues, blockPath+".permissionPolicy", block.PermissionPolicy)
			for previous := 0; previous < blockIndex; previous++ {
				if overlaps(block.Grid, page.Blocks[previous].Grid) {
					add(blockPath+".grid", fmt.Sprintf("与 blocks[%d] 发生碰撞", previous))
				}
			}
			for componentIndex, component := range block.Components {
				componentPath := fmt.Sprintf("%s.components[%d]", blockPath, componentIndex)
				validateID(&issues, componentPath+".id", component.ID)
				if componentIDs[component.ID] {
					add(componentPath+".id", "组件标识在报告内重复")
				}
				componentIDs[component.ID] = true
				if !componentTypes[component.Type] {
					add(componentPath+".type", "不支持的组件类型")
				}
				if component.Name == "" {
					add(componentPath+".name", "不能为空")
				}
				validateGrid(&issues, componentPath+".grid", component.Grid, block.InnerGrid.Columns, block.InnerGrid.Rows)
				if component.ZIndex < 0 {
					add(componentPath+".zIndex", "不能小于 0")
				}
				validateSticky(&issues, componentPath+".sticky", component.Sticky, "PAGE", "BLOCK", "CONTAINER")
				validatePermission(&issues, componentPath+".permissionPolicy", component.PermissionPolicy)
				for previous := 0; previous < componentIndex; previous++ {
					if overlaps(component.Grid, block.Components[previous].Grid) {
						add(componentPath+".grid", fmt.Sprintf("与 components[%d] 发生碰撞", previous))
					}
				}
				for traceIndex, trace := range component.SourceTrace {
					validateSourceTrace(&issues, fmt.Sprintf("%s.sourceTrace[%d]", componentPath, traceIndex), trace)
				}
			}
		}
		if page.ContentGridRows != computedRows {
			add(pagePath+".contentGridRows", fmt.Sprintf("必须等于动态内容行数 %d", computedRows))
		}
	}
	for index, parameter := range document.Parameters {
		if parameter.Scope == "PAGE" && !pageIDs[parameter.PageID] {
			add(fmt.Sprintf("parameters[%d].pageId", index), "引用的页面不存在")
		}
	}
	validateRequirements(&issues, document.DataRequirements)
	if document.Generation != nil {
		if !oneOf(document.Generation.Mode, "MANUAL", "TEMPLATE", "AI_INITIAL", "AI_BLOCK_EDIT") {
			add("generation.mode", "不支持的生成模式")
		}
		if document.Generation.Confidence < 0 || document.Generation.Confidence > 1 {
			add("generation.confidence", "必须在 0 到 1 之间")
		}
		if document.Generation.GeneratedAt != "" {
			if _, err := time.Parse(time.RFC3339, document.Generation.GeneratedAt); err != nil {
				add("generation.generatedAt", "必须为 RFC3339 时间")
			}
		}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func validateReport(issues *[]ValidationIssue, report Report) {
	add := func(path, reason string) { *issues = append(*issues, ValidationIssue{Path: path, Reason: reason}) }
	validateCode(issues, "report.code", report.Code)
	if report.Name == "" {
		add("report.name", "不能为空")
	}
	if !oneOf(report.Type, "DASHBOARD", "REPORT") {
		add("report.type", "必须为 DASHBOARD 或 REPORT")
	}
	if report.Language == "" {
		add("report.language", "不能为空")
	}
	if !oneOf(report.Status, "DRAFT", "PUBLISHED", "ARCHIVED") {
		add("report.status", "不支持的报告状态")
	}
	if !oneOf(report.Visibility, "PRIVATE", "TENANT", "PUBLIC") {
		add("report.visibility", "不支持的可见性")
	}
	if !oneOf(report.DefaultRefreshPolicy, "REALTIME", "CACHE", "MATERIALIZED", "SNAPSHOT") {
		add("report.defaultRefreshPolicy", "不支持的刷新策略")
	}
	if report.Timezone == "" {
		add("report.timezone", "不能为空")
	}
}

func validateCanvas(issues *[]ValidationIssue, canvas Canvas) {
	add := func(path, reason string) { *issues = append(*issues, ValidationIssue{Path: path, Reason: reason}) }
	if canvas.LogicalWidth != 1920 {
		add("canvas.logicalWidth", "V1 必须为 1920")
	}
	if canvas.ViewportHeight != 1080 {
		add("canvas.viewportHeight", "V1 必须为 1080")
	}
	if canvas.GridColumns != 12 {
		add("canvas.gridColumns", "V1 必须为 12")
	}
	if canvas.ViewportGridRows != 10 {
		add("canvas.viewportGridRows", "V1 必须为 10")
	}
	if canvas.ContentGridRows != "AUTO" {
		add("canvas.contentGridRows", "V1 必须为 AUTO")
	}
	if canvas.MinContentGridRows != 10 {
		add("canvas.minContentGridRows", "V1 必须为 10")
	}
	if canvas.InnerGridMultiplier != 4 {
		add("canvas.innerGridMultiplier", "V1 必须为 4")
	}
	if canvas.ScaleMode != "FIT_WIDTH" {
		add("canvas.scaleMode", "V1 必须为 FIT_WIDTH")
	}
	if canvas.VerticalOverflow != "SCROLL" {
		add("canvas.verticalOverflow", "V1 必须为 SCROLL")
	}
}

func validateGrid(issues *[]ValidationIssue, path string, grid Grid, maxColumns, maxRows int) {
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	if grid.X < 0 {
		add(".x", "不能小于 0")
	}
	if grid.Y < 0 {
		add(".y", "不能小于 0")
	}
	if grid.W < 1 {
		add(".w", "必须大于等于 1")
	}
	if grid.H < 1 {
		add(".h", "必须大于等于 1")
	}
	if maxColumns >= 0 && grid.X+grid.W > maxColumns {
		add("", "横向越界")
	}
	if maxRows >= 0 && grid.Y+grid.H > maxRows {
		add("", "纵向越界")
	}
}

func validateSticky(issues *[]ValidationIssue, path string, sticky Sticky, scopes ...string) {
	if !sticky.Enabled {
		if sticky.Top != 0 || sticky.Scope != "" || sticky.ContainerID != "" || sticky.ZIndex != 0 {
			*issues = append(*issues, ValidationIssue{Path: path, Reason: "未启用冻结时不能携带冻结参数"})
		}
		return
	}
	if sticky.Top < 0 {
		*issues = append(*issues, ValidationIssue{Path: path + ".top", Reason: "不能小于 0"})
	}
	if !oneOf(sticky.Scope, scopes...) {
		*issues = append(*issues, ValidationIssue{Path: path + ".scope", Reason: "不支持的冻结范围"})
	}
	if sticky.Scope == "CONTAINER" && sticky.ContainerID == "" {
		*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "容器冻结必须指定 containerId"})
	}
	if sticky.Scope != "CONTAINER" && sticky.ContainerID != "" {
		*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "仅 CONTAINER 范围允许指定 containerId"})
	}
	if sticky.ZIndex < 1 {
		*issues = append(*issues, ValidationIssue{Path: path + ".zIndex", Reason: "启用冻结时必须大于等于 1"})
	}
}

func validatePermission(issues *[]ValidationIssue, path string, policy *PermissionPolicy) {
	if policy == nil {
		return
	}
	seen := map[string]bool{}
	for index, role := range policy.AllowedRoleCodes {
		if !codePattern.MatchString(role) {
			*issues = append(*issues, ValidationIssue{Path: fmt.Sprintf("%s.allowedRoleCodes[%d]", path, index), Reason: "角色编码格式无效"})
		}
		if seen[role] {
			*issues = append(*issues, ValidationIssue{Path: fmt.Sprintf("%s.allowedRoleCodes[%d]", path, index), Reason: "角色编码重复"})
		}
		seen[role] = true
	}
}

func validateSourceTrace(issues *[]ValidationIssue, path string, trace SourceTrace) {
	if !oneOf(trace.SourceType, "USER_REQUIREMENT", "ATTACHMENT", "IMAGE", "TABLE_ASSET", "DATASET", "METRIC", "REPORT_TEMPLATE", "MANUAL_EDIT") {
		*issues = append(*issues, ValidationIssue{Path: path + ".sourceType", Reason: "不支持的来源类型"})
	}
	if trace.SourceID == "" {
		*issues = append(*issues, ValidationIssue{Path: path + ".sourceId", Reason: "不能为空"})
	}
	if trace.Usage == "" {
		*issues = append(*issues, ValidationIssue{Path: path + ".usage", Reason: "不能为空"})
	}
}

func validateRequirements(issues *[]ValidationIssue, requirements []DataRequirement) {
	seen := map[string]bool{}
	for index, requirement := range requirements {
		path := fmt.Sprintf("dataRequirements[%d]", index)
		validateID(issues, path+".id", requirement.ID)
		if seen[requirement.ID] {
			*issues = append(*issues, ValidationIssue{Path: path + ".id", Reason: "数据需求标识重复"})
		}
		seen[requirement.ID] = true
		if requirement.Intent == "" {
			*issues = append(*issues, ValidationIssue{Path: path + ".intent", Reason: "不能为空"})
		}
		if !oneOf(requirement.ResolutionStatus, "RESOLVED", "PARTIAL", "UNRESOLVED", "INVALID") {
			*issues = append(*issues, ValidationIssue{Path: path + ".resolutionStatus", Reason: "不支持的解析状态"})
		}
		if requirement.Confidence < 0 || requirement.Confidence > 1 {
			*issues = append(*issues, ValidationIssue{Path: path + ".confidence", Reason: "必须在 0 到 1 之间"})
		}
	}
}

func validateID(issues *[]ValidationIssue, path, value string) {
	if value == "" {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "不能为空"})
	} else if len(value) > 128 {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "长度不能超过 128"})
	}
}

func validateCode(issues *[]ValidationIssue, path, value string) {
	if !codePattern.MatchString(value) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须以字母开头且只能包含字母、数字和下划线"})
	}
}

func overlaps(left, right Grid) bool {
	return left.X < right.X+right.W && left.X+left.W > right.X && left.Y < right.Y+right.H && left.Y+left.H > right.Y
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("解析报告 JSON 尾部: %w", err)
	}
	return fmt.Errorf("报告 JSON 后存在额外内容")
}
