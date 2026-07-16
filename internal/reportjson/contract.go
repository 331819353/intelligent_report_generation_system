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

type componentSize struct{ W, H int }

var componentMinimumSizes = map[string]componentSize{
	"TITLE": {4, 2}, "RICH_TEXT": {4, 3}, "FILTER": {4, 3}, "KPI": {4, 4},
	"ADDITIONAL_INFO": {4, 4}, "TABLE": {4, 4}, "CHART": {4, 4}, "IMAGE": {4, 4},
	"ATTACHMENT_LIST": {4, 4}, "DATA_SOURCE": {4, 2}, "UPDATED_AT": {4, 2}, "CONCLUSION": {4, 4},
}

type conclusionReference struct {
	Path              string
	MetricIDs         []string
	ChartComponentIDs []string
}

type interactionReference struct {
	Path          string
	SourcePageID  string
	SourceBlockID string
	ParameterID   string
	EffectScope   map[string]any
	Required      bool
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
			if legacy {
				migrateLegacySticky(&block.Sticky)
			}
			if bottom := block.Grid.Y + block.Grid.H; bottom > computedRows {
				computedRows = bottom
			}
			if block.Components == nil {
				block.Components = []Component{}
			}
			for componentIndex := range block.Components {
				component := &block.Components[componentIndex]
				if legacy {
					migrateLegacySticky(&component.Sticky)
				}
				if component.SourceTrace == nil {
					component.SourceTrace = []SourceTrace{}
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

func migrateLegacySticky(target **Sticky) {
	if *target == nil {
		*target = &Sticky{}
		return
	}
	legacy := **target
	// 0.9 按字段零值校验，无法区分“缺少 top”和“显式 top:0”。重建对象既保留旧语义，也输出 1.0 唯一规范态。
	*target = &Sticky{
		Enabled: legacy.Enabled, Top: legacy.Top, Scope: legacy.Scope,
		ContainerID: legacy.ContainerID, ZIndex: legacy.ZIndex,
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
	componentKinds := map[string]string{}
	componentPageIDs := map[string]string{}
	conclusionReferences := make([]conclusionReference, 0)
	interactionReferences := make([]interactionReference, 0)
	parameterIDs := map[string]bool{}
	parameterCodes := map[string]bool{}
	resolvedMetricIDs := map[string]bool{}
	for _, requirement := range document.DataRequirements {
		for _, metricID := range requirement.ResolvedMetricIDs {
			resolvedMetricIDs[metricID] = true
		}
	}
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
		validateSemanticBinding(&issues, path+".semanticBinding", parameter.SemanticBinding)
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
			validateSticky(&issues, blockPath+".sticky", block.Sticky, []string{"PAGE", "CONTAINER"}, page.ID)
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
				componentKinds[component.ID] = component.Type
				componentPageIDs[component.ID] = page.ID
				if !componentTypes[component.Type] {
					add(componentPath+".type", "不支持的组件类型")
				}
				if component.Name == "" {
					add(componentPath+".name", "不能为空")
				}
				validateGrid(&issues, componentPath+".grid", component.Grid, block.InnerGrid.Columns, block.InnerGrid.Rows)
				if minimum, exists := componentMinimumSizes[component.Type]; exists && (component.Grid.W < minimum.W || component.Grid.H < minimum.H) {
					add(componentPath+".grid", fmt.Sprintf("%s 组件最小尺寸为 %d×%d", component.Type, minimum.W, minimum.H))
				}
				validateComponentConfig(&issues, componentPath, component)
				if reference, ok := componentInteractionReference(componentPath, page.ID, block.ID, component); ok {
					interactionReferences = append(interactionReferences, reference)
				}
				if component.Type == "CONCLUSION" {
					conclusionReferences = append(conclusionReferences, conclusionReference{
						Path: componentPath, MetricIDs: stringSlice(component.Binding["metricIds"]), ChartComponentIDs: stringSlice(component.Binding["chartComponentIds"]),
					})
				}
				if component.ZIndex < 0 {
					add(componentPath+".zIndex", "不能小于 0")
				}
				validateSticky(&issues, componentPath+".sticky", component.Sticky, []string{"PAGE", "BLOCK", "CONTAINER"}, page.ID, block.ID)
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
	validateInteractionReferences(&issues, interactionReferences, document.Parameters, componentIDs, componentPageIDs)
	// 结论引用在所有页面和组件完成收集后再校验，允许合法的向后引用。
	for _, reference := range conclusionReferences {
		for index, metricID := range reference.MetricIDs {
			if !resolvedMetricIDs[metricID] {
				add(fmt.Sprintf("%s.binding.metricIds[%d]", reference.Path, index), "引用的已解析指标不存在")
			}
		}
		for index, componentID := range reference.ChartComponentIDs {
			path := fmt.Sprintf("%s.binding.chartComponentIds[%d]", reference.Path, index)
			if !componentIDs[componentID] {
				add(path, "引用的组件不存在")
			} else if componentKinds[componentID] != "CHART" {
				add(path, "引用的组件必须为图表组件")
			}
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

// validateSemanticBinding 要求每个数据集版本只有一条显式映射，避免运行时按名称猜测或命中歧义字段。
func validateSemanticBinding(issues *[]ValidationIssue, path string, binding *SemanticBinding) {
	if binding == nil {
		return
	}
	validateCode(issues, path+".semanticFieldCode", binding.SemanticFieldCode)
	if len(binding.DatasetFields) == 0 {
		*issues = append(*issues, ValidationIssue{Path: path + ".datasetFields", Reason: "至少需要一个数据集字段映射"})
		return
	}
	seenDatasetVersions := make(map[string]bool, len(binding.DatasetFields))
	for index, field := range binding.DatasetFields {
		fieldPath := fmt.Sprintf("%s.datasetFields[%d]", path, index)
		validateID(issues, fieldPath+".datasetVersionId", field.DatasetVersionID)
		validateID(issues, fieldPath+".fieldId", field.FieldID)
		validateCode(issues, fieldPath+".datasetParameterCode", field.DatasetParameterCode)
		if seenDatasetVersions[field.DatasetVersionID] {
			*issues = append(*issues, ValidationIssue{Path: fieldPath + ".datasetVersionId", Reason: "同一数据集版本不能存在歧义映射"})
		}
		seenDatasetVersions[field.DatasetVersionID] = true
	}
}

func componentInteractionReference(path, pageID, blockID string, component Component) (interactionReference, bool) {
	if component.Type == "FILTER" {
		return interactionReference{
			Path: path + ".binding", SourcePageID: pageID, SourceBlockID: blockID,
			ParameterID: stringValue(component.Binding["parameterId"]), EffectScope: objectValue(component.Binding["effectScope"]),
		}, true
	}
	if component.Type != "CHART" {
		return interactionReference{}, false
	}
	linkage := objectValue(component.Interaction["linkage"])
	if linkage == nil {
		return interactionReference{}, false
	}
	return interactionReference{
		Path: path + ".interaction.linkage", SourcePageID: pageID, SourceBlockID: blockID,
		ParameterID: stringValue(linkage["parameterId"]), EffectScope: objectValue(linkage["effectScope"]), Required: true,
	}, true
}

// validateInteractionReferences 在完整收集页面和组件后统一校验，因而允许合法的向后引用。
func validateInteractionReferences(issues *[]ValidationIssue, references []interactionReference, parameters []Parameter, componentIDs map[string]bool, componentPageIDs map[string]string) {
	parametersByID := make(map[string]Parameter, len(parameters))
	for _, parameter := range parameters {
		parametersByID[parameter.ID] = parameter
	}
	for _, reference := range references {
		if reference.ParameterID == "" {
			if reference.Required {
				*issues = append(*issues, ValidationIssue{Path: reference.Path + ".parameterId", Reason: "必须引用报告参数"})
			}
			continue
		}
		parameter, exists := parametersByID[reference.ParameterID]
		if !exists {
			*issues = append(*issues, ValidationIssue{Path: reference.Path + ".parameterId", Reason: "引用的报告参数不存在"})
			continue
		}
		if parameter.Scope == "PAGE" && parameter.PageID != reference.SourcePageID {
			*issues = append(*issues, ValidationIssue{Path: reference.Path + ".parameterId", Reason: "页面参数不能从其他页面触发"})
		}
		kind := stringValue(reference.EffectScope["kind"])
		if parameter.Scope == "PAGE" && kind == "REPORT" {
			*issues = append(*issues, ValidationIssue{Path: reference.Path + ".effectScope.kind", Reason: "页面参数不能影响整份报告"})
		}
		if kind != "COMPONENTS" {
			continue
		}
		for index, componentID := range stringSlice(reference.EffectScope["componentIds"]) {
			targetPath := fmt.Sprintf("%s.effectScope.componentIds[%d]", reference.Path, index)
			if !componentIDs[componentID] {
				*issues = append(*issues, ValidationIssue{Path: targetPath, Reason: "引用的目标组件不存在"})
				continue
			}
			if parameter.Scope == "PAGE" && componentPageIDs[componentID] != parameter.PageID {
				*issues = append(*issues, ValidationIssue{Path: targetPath, Reason: "页面参数不能影响其他页面组件"})
			}
		}
	}
}

// validateComponentConfig 与前端 JSON Schema 对齐关键语义，服务端不能依赖浏览器完成授权前校验。
func validateComponentConfig(issues *[]ValidationIssue, path string, component Component) {
	allowed := map[string]struct {
		style, binding, interaction []string
	}{
		"TITLE":           {[]string{"fontSize", "fontWeight"}, []string{"text"}, nil},
		"RICH_TEXT":       {[]string{"align"}, []string{"text", "blocks"}, nil},
		"FILTER":          {[]string{"layout"}, []string{"parameterId", "placeholder", "control", "operator", "effectScope", "options"}, []string{"autoSubmit"}},
		"KPI":             {[]string{"trendVisible"}, []string{"datasetVersionId", "metricId", "format"}, nil},
		"ADDITIONAL_INFO": {[]string{"tone"}, []string{"title", "text"}, nil},
		"TABLE":           {[]string{"striped", "compact"}, []string{"datasetVersionId", "columns"}, nil},
		"CHART":           {[]string{"legendPosition"}, []string{"datasetVersionId", "metricIds", "dimensions", "chart"}, []string{"clickFilter", "linkage", "drill"}},
		"IMAGE":           {[]string{"fit"}, []string{"url", "alt"}, nil},
		"ATTACHMENT_LIST": {[]string{"showSize"}, []string{"attachments"}, nil},
		"DATA_SOURCE":     {[]string{"compact"}, []string{"datasetVersionIds"}, nil},
		"UPDATED_AT":      {[]string{"format"}, []string{"label"}, nil},
		"CONCLUSION":      {[]string{"showEvidenceLinks"}, []string{"metricIds", "chartComponentIds"}, []string{"showRevisionHistory"}},
	}
	spec, formal := allowed[component.Type]
	if !formal {
		return
	}
	validateAllowedKeys(issues, path+".style", component.Style, spec.style)
	validateAllowedKeys(issues, path+".binding", component.Binding, spec.binding)
	validateAllowedKeys(issues, path+".interaction", component.Interaction, spec.interaction)
	validateAllowedKeys(issues, path+".refreshPolicy", component.RefreshPolicy, []string{"mode"})
	refreshModes := map[string][]string{
		"TITLE": {"NONE"}, "RICH_TEXT": {"NONE"}, "ADDITIONAL_INFO": {"NONE"}, "IMAGE": {"NONE"}, "ATTACHMENT_LIST": {"NONE"},
		"FILTER": {"INHERIT", "ON_REPORT_RUN"}, "KPI": {"INHERIT", "ON_REPORT_RUN"}, "TABLE": {"INHERIT", "ON_REPORT_RUN"}, "CHART": {"INHERIT", "ON_REPORT_RUN"},
		"DATA_SOURCE": {"ON_REPORT_RUN"}, "UPDATED_AT": {"ON_REPORT_RUN"}, "CONCLUSION": {"ON_REPORT_RUN"},
	}
	if mode, exists := component.RefreshPolicy["mode"]; exists && !oneOf(stringValue(mode), refreshModes[component.Type]...) {
		*issues = append(*issues, ValidationIssue{Path: path + ".refreshPolicy.mode", Reason: "当前组件类型不支持该刷新策略"})
	}
	validateComponentConfigValues(issues, path, component)
	if component.Type != "CONCLUSION" && len(component.Conclusion) > 0 {
		*issues = append(*issues, ValidationIssue{Path: path + ".conclusion", Reason: "仅结论组件允许 conclusion 配置"})
	}
	if component.Type == "CHART" && component.Binding != nil {
		chart, ok := component.Binding["chart"].(map[string]any)
		if !ok {
			*issues = append(*issues, ValidationIssue{Path: path + ".binding.chart", Reason: "必须为图表配置对象"})
		} else {
			validateAllowedKeys(issues, path+".binding.chart", chart, []string{"type"})
			if !oneOf(stringValue(chart["type"]), "COLUMN", "BAR", "LINE", "PIE") {
				*issues = append(*issues, ValidationIssue{Path: path + ".binding.chart.type", Reason: "必须为 COLUMN、BAR、LINE 或 PIE"})
			}
		}
	}
	if component.Type == "CONCLUSION" {
		validateAllowedKeys(issues, path+".conclusion", component.Conclusion, []string{"mode", "allowManualEdit", "requireEvidence", "maxLength"})
	}
}

// validateComponentConfigValues 校验开放 map 中的值类型和必填键，避免服务端只拒绝字段名却接受错误值。
func validateComponentConfigValues(issues *[]ValidationIssue, path string, component Component) {
	switch component.Type {
	case "TITLE":
		requireConfigKeys(issues, path+".binding", component.Binding, "text")
		validateConfigNumber(issues, path+".style", component.Style, "fontSize")
		validateConfigNumber(issues, path+".style", component.Style, "fontWeight")
		validateConfigString(issues, path+".binding", component.Binding, "text")
	case "RICH_TEXT":
		if component.Binding != nil && component.Binding["text"] == nil && component.Binding["blocks"] == nil {
			*issues = append(*issues, ValidationIssue{Path: path + ".binding", Reason: "至少需要 text 或 blocks"})
		}
		validateConfigEnum(issues, path+".style", component.Style, "align", "LEFT", "CENTER", "RIGHT")
		validateConfigString(issues, path+".binding", component.Binding, "text")
		validateObjectArray(issues, path+".binding", component.Binding, "blocks", func(blockPath string, item map[string]any) {
			validateAllowedKeys(issues, blockPath, item, []string{"type", "text"})
			requireConfigKeys(issues, blockPath, item, "type", "text")
			validateConfigEnum(issues, blockPath, item, "type", "PARAGRAPH", "HEADING", "QUOTE", "LIST_ITEM")
			validateConfigString(issues, blockPath, item, "text")
		})
	case "FILTER":
		requireConfigKeys(issues, path+".binding", component.Binding, "parameterId")
		validateConfigEnum(issues, path+".style", component.Style, "layout", "HORIZONTAL", "VERTICAL")
		validateConfigString(issues, path+".binding", component.Binding, "parameterId")
		validateConfigString(issues, path+".binding", component.Binding, "placeholder")
		validateConfigEnum(issues, path+".binding", component.Binding, "control", "TEXT", "NUMBER", "DATE", "DATETIME", "MONTH", "QUARTER", "SELECT", "MULTI_SELECT", "BOOLEAN")
		validateConfigEnum(issues, path+".binding", component.Binding, "operator", "EQUALS", "IN", "BETWEEN", "GTE", "LTE")
		if scope, exists := component.Binding["effectScope"]; exists {
			validateEffectScope(issues, path+".binding.effectScope", scope)
		}
		validateFilterOptions(issues, path+".binding.options", component.Binding["options"])
		validateConfigBool(issues, path+".interaction", component.Interaction, "autoSubmit")
	case "KPI":
		requireConfigKeys(issues, path+".binding", component.Binding, "metricId")
		validateConfigBool(issues, path+".style", component.Style, "trendVisible")
		validateConfigString(issues, path+".binding", component.Binding, "datasetVersionId")
		validateConfigString(issues, path+".binding", component.Binding, "metricId")
		validateConfigEnum(issues, path+".binding", component.Binding, "format", "AUTO", "NUMBER", "PERCENT", "CURRENCY")
	case "ADDITIONAL_INFO":
		validateConfigEnum(issues, path+".style", component.Style, "tone", "NEUTRAL", "INFO", "WARNING", "SUCCESS")
		validateConfigString(issues, path+".binding", component.Binding, "title")
		validateConfigString(issues, path+".binding", component.Binding, "text")
	case "TABLE":
		requireConfigKeys(issues, path+".binding", component.Binding, "datasetVersionId", "columns")
		validateConfigBool(issues, path+".style", component.Style, "striped")
		validateConfigBool(issues, path+".style", component.Style, "compact")
		validateConfigString(issues, path+".binding", component.Binding, "datasetVersionId")
		validateObjectArray(issues, path+".binding", component.Binding, "columns", func(columnPath string, item map[string]any) {
			validateAllowedKeys(issues, columnPath, item, []string{"key", "label"})
			requireConfigKeys(issues, columnPath, item, "key", "label")
			validateConfigString(issues, columnPath, item, "key")
			validateConfigString(issues, columnPath, item, "label")
		})
	case "CHART":
		requireConfigKeys(issues, path+".binding", component.Binding, "datasetVersionId", "metricIds", "dimensions", "chart")
		validateConfigEnum(issues, path+".style", component.Style, "legendPosition", "TOP", "RIGHT", "BOTTOM", "NONE")
		validateConfigString(issues, path+".binding", component.Binding, "datasetVersionId")
		validateStringArray(issues, path+".binding", component.Binding, "metricIds")
		validateObjectArray(issues, path+".binding", component.Binding, "dimensions", func(dimensionPath string, item map[string]any) {
			validateAllowedKeys(issues, dimensionPath, item, []string{"fieldId", "role"})
			requireConfigKeys(issues, dimensionPath, item, "fieldId", "role")
			validateConfigString(issues, dimensionPath, item, "fieldId")
			validateConfigEnum(issues, dimensionPath, item, "role", "CATEGORY", "TIME", "SERIES")
		})
		validateConfigBool(issues, path+".interaction", component.Interaction, "clickFilter")
		validateChartInteraction(issues, path+".interaction", component.Interaction)
	case "IMAGE":
		requireConfigKeys(issues, path+".binding", component.Binding, "url", "alt")
		validateConfigEnum(issues, path+".style", component.Style, "fit", "CONTAIN", "COVER", "FILL")
		validateConfigString(issues, path+".binding", component.Binding, "url")
		validateConfigString(issues, path+".binding", component.Binding, "alt")
	case "ATTACHMENT_LIST":
		requireConfigKeys(issues, path+".binding", component.Binding, "attachments")
		validateConfigBool(issues, path+".style", component.Style, "showSize")
		validateObjectArray(issues, path+".binding", component.Binding, "attachments", func(attachmentPath string, item map[string]any) {
			validateAllowedKeys(issues, attachmentPath, item, []string{"name", "url", "sizeLabel"})
			requireConfigKeys(issues, attachmentPath, item, "name", "url")
			validateConfigString(issues, attachmentPath, item, "name")
			validateConfigString(issues, attachmentPath, item, "url")
			validateConfigString(issues, attachmentPath, item, "sizeLabel")
		})
	case "DATA_SOURCE":
		requireConfigKeys(issues, path+".binding", component.Binding, "datasetVersionIds")
		validateConfigBool(issues, path+".style", component.Style, "compact")
		validateStringArray(issues, path+".binding", component.Binding, "datasetVersionIds")
	case "UPDATED_AT":
		validateConfigEnum(issues, path+".style", component.Style, "format", "DATE", "DATETIME", "RELATIVE")
		validateConfigString(issues, path+".binding", component.Binding, "label")
	case "CONCLUSION":
		requireConfigKeys(issues, path+".binding", component.Binding, "metricIds", "chartComponentIds")
		validateConfigBool(issues, path+".style", component.Style, "showEvidenceLinks")
		validateStringArray(issues, path+".binding", component.Binding, "metricIds")
		validateStringArray(issues, path+".binding", component.Binding, "chartComponentIds")
		validateConfigBool(issues, path+".interaction", component.Interaction, "showRevisionHistory")
		if component.Conclusion != nil {
			requireConfigKeys(issues, path+".conclusion", component.Conclusion, "mode", "allowManualEdit", "requireEvidence", "maxLength")
			validateConfigEnum(issues, path+".conclusion", component.Conclusion, "mode", "TEMPLATE", "LLM_WITH_TEMPLATE", "MANUAL")
			validateConfigBool(issues, path+".conclusion", component.Conclusion, "allowManualEdit")
			validateConfigBool(issues, path+".conclusion", component.Conclusion, "requireEvidence")
			validateConfigNumber(issues, path+".conclusion", component.Conclusion, "maxLength")
		}
	}
}

func requireConfigKeys(issues *[]ValidationIssue, path string, value map[string]any, keys ...string) {
	if value == nil {
		return
	}
	for _, key := range keys {
		if _, exists := value[key]; !exists {
			*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "缺少必填配置"})
		}
	}
}

func validateConfigString(issues *[]ValidationIssue, path string, value map[string]any, key string) {
	if candidate, exists := value[key]; exists {
		if _, ok := candidate.(string); !ok {
			*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "必须为字符串"})
		}
	}
}

func validateConfigBool(issues *[]ValidationIssue, path string, value map[string]any, key string) {
	if candidate, exists := value[key]; exists {
		if _, ok := candidate.(bool); !ok {
			*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "必须为布尔值"})
		}
	}
}

func validateConfigNumber(issues *[]ValidationIssue, path string, value map[string]any, key string) {
	if candidate, exists := value[key]; exists {
		switch candidate.(type) {
		case json.Number, float64, float32, int, int32, int64:
		default:
			*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "必须为数字"})
		}
	}
}

func validateConfigEnum(issues *[]ValidationIssue, path string, value map[string]any, key string, allowed ...string) {
	if candidate, exists := value[key]; exists && !oneOf(stringValue(candidate), allowed...) {
		*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "不在允许的枚举范围内"})
	}
}

func validateStringArray(issues *[]ValidationIssue, path string, value map[string]any, key string) {
	candidate, exists := value[key]
	if !exists {
		return
	}
	switch items := candidate.(type) {
	case []string:
		return
	case []any:
		for index, item := range items {
			if _, ok := item.(string); !ok {
				*issues = append(*issues, ValidationIssue{Path: fmt.Sprintf("%s.%s[%d]", path, key, index), Reason: "必须为字符串"})
			}
		}
	default:
		*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "必须为数组"})
	}
}

func validateObjectArray(issues *[]ValidationIssue, path string, value map[string]any, key string, validate func(string, map[string]any)) {
	candidate, exists := value[key]
	if !exists {
		return
	}
	items, ok := candidate.([]any)
	if !ok {
		*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "必须为对象数组"})
		return
	}
	for index, item := range items {
		record, ok := item.(map[string]any)
		itemPath := fmt.Sprintf("%s.%s[%d]", path, key, index)
		if !ok {
			*issues = append(*issues, ValidationIssue{Path: itemPath, Reason: "必须为对象"})
			continue
		}
		validate(itemPath, record)
	}
}

func validateEffectScope(issues *[]ValidationIssue, path string, value any) {
	scope := objectValue(value)
	if scope == nil {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须为联动作用域对象"})
		return
	}
	validateAllowedKeys(issues, path, scope, []string{"kind", "componentIds"})
	requireConfigKeys(issues, path, scope, "kind")
	validateConfigEnum(issues, path, scope, "kind", "REPORT", "PAGE", "BLOCK", "COMPONENTS")
	kind := stringValue(scope["kind"])
	_, hasComponentIDs := scope["componentIds"]
	if kind == "COMPONENTS" && !hasComponentIDs {
		*issues = append(*issues, ValidationIssue{Path: path + ".componentIds", Reason: "指定组件作用域必须声明目标"})
		return
	}
	if kind != "COMPONENTS" && hasComponentIDs {
		*issues = append(*issues, ValidationIssue{Path: path + ".componentIds", Reason: "仅指定组件作用域允许声明目标"})
		return
	}
	if !hasComponentIDs {
		return
	}
	validateStringArray(issues, path, scope, "componentIds")
	componentIDs := stringSlice(scope["componentIds"])
	if len(componentIDs) == 0 {
		*issues = append(*issues, ValidationIssue{Path: path + ".componentIds", Reason: "至少需要一个目标组件"})
	}
	if len(componentIDs) > 100 {
		*issues = append(*issues, ValidationIssue{Path: path + ".componentIds", Reason: "目标组件不能超过 100 个"})
	}
	seen := make(map[string]bool, len(componentIDs))
	for index, componentID := range componentIDs {
		itemPath := fmt.Sprintf("%s.componentIds[%d]", path, index)
		validateID(issues, itemPath, componentID)
		if seen[componentID] {
			*issues = append(*issues, ValidationIssue{Path: itemPath, Reason: "目标组件不能重复"})
		}
		seen[componentID] = true
	}
}

func validateFilterOptions(issues *[]ValidationIssue, path string, value any) {
	if value == nil {
		return
	}
	items, ok := value.([]any)
	if !ok {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须为选项数组"})
		return
	}
	if len(items) > 1000 {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "选项不能超过 1000 个"})
	}
	for index, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		option := objectValue(item)
		if option == nil {
			*issues = append(*issues, ValidationIssue{Path: itemPath, Reason: "必须为选项对象"})
			continue
		}
		validateAllowedKeys(issues, itemPath, option, []string{"label", "value"})
		requireConfigKeys(issues, itemPath, option, "label", "value")
		validateConfigString(issues, itemPath, option, "label")
		if label, exists := option["label"].(string); exists && label == "" {
			*issues = append(*issues, ValidationIssue{Path: itemPath + ".label", Reason: "不能为空"})
		}
		if candidate, exists := option["value"]; exists {
			switch candidate.(type) {
			case string, bool, json.Number, float64, float32, int, int32, int64:
			default:
				*issues = append(*issues, ValidationIssue{Path: itemPath + ".value", Reason: "必须为字符串、数字或布尔值"})
			}
		}
	}
}

func validateChartInteraction(issues *[]ValidationIssue, path string, interaction map[string]any) {
	linkage := objectValue(interaction["linkage"])
	if interaction["clickFilter"] == true && linkage == nil {
		*issues = append(*issues, ValidationIssue{Path: path + ".linkage", Reason: "启用点击联动时必须声明联动配置"})
	}
	if linkage != nil {
		linkagePath := path + ".linkage"
		validateAllowedKeys(issues, linkagePath, linkage, []string{"parameterId", "operator", "effectScope"})
		requireConfigKeys(issues, linkagePath, linkage, "parameterId", "operator", "effectScope")
		validateConfigString(issues, linkagePath, linkage, "parameterId")
		validateConfigEnum(issues, linkagePath, linkage, "operator", "EQUALS", "IN", "BETWEEN", "GTE", "LTE")
		validateEffectScope(issues, linkagePath+".effectScope", linkage["effectScope"])
	}
	if _, exists := interaction["drill"]; !exists {
		return
	}
	drillPath := path + ".drill"
	drill := objectValue(interaction["drill"])
	if drill == nil {
		*issues = append(*issues, ValidationIssue{Path: drillPath, Reason: "必须为下钻配置对象"})
		return
	}
	validateAllowedKeys(issues, drillPath, drill, []string{"levels"})
	requireConfigKeys(issues, drillPath, drill, "levels")
	levels, ok := drill["levels"].([]any)
	if !ok {
		*issues = append(*issues, ValidationIssue{Path: drillPath + ".levels", Reason: "必须为下钻层级数组"})
		return
	}
	if len(levels) == 0 || len(levels) > 10 {
		*issues = append(*issues, ValidationIssue{Path: drillPath + ".levels", Reason: "下钻层级数量必须在 1 到 10 之间"})
	}
	for index, item := range levels {
		levelPath := fmt.Sprintf("%s.levels[%d]", drillPath, index)
		level := objectValue(item)
		if level == nil {
			*issues = append(*issues, ValidationIssue{Path: levelPath, Reason: "必须为下钻层级对象"})
			continue
		}
		validateAllowedKeys(issues, levelPath, level, []string{"fieldId", "semanticFieldCode", "label"})
		requireConfigKeys(issues, levelPath, level, "fieldId", "semanticFieldCode", "label")
		if fieldID := stringValue(level["fieldId"]); fieldID != "" {
			validateID(issues, levelPath+".fieldId", fieldID)
		} else {
			validateConfigString(issues, levelPath, level, "fieldId")
		}
		if semanticCode := stringValue(level["semanticFieldCode"]); semanticCode != "" {
			validateCode(issues, levelPath+".semanticFieldCode", semanticCode)
		} else {
			validateConfigString(issues, levelPath, level, "semanticFieldCode")
		}
		validateConfigString(issues, levelPath, level, "label")
	}
}

func validateAllowedKeys(issues *[]ValidationIssue, path string, value map[string]any, allowed []string) {
	allowedSet := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range value {
		if !allowedSet[key] {
			*issues = append(*issues, ValidationIssue{Path: path + "." + key, Reason: "包含当前组件类型不支持的配置字段"})
		}
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func objectValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
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

func validateSticky(issues *[]ValidationIssue, path string, sticky *Sticky, scopes []string, ancestorContainerIDs ...string) {
	if sticky == nil {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "缺少冻结配置"})
		return
	}
	if sticky.decoded && !sticky.hasEnabled {
		*issues = append(*issues, ValidationIssue{Path: path + ".enabled", Reason: "缺少必填字段"})
	}
	if !sticky.Enabled {
		// 解码态必须检查字段存在性，否则 top:0 会被误判为没有夹带冻结参数。
		carriesDecodedParameters := sticky.decoded && (sticky.hasTop || sticky.hasScope || sticky.hasContainerID || sticky.hasZIndex)
		if carriesDecodedParameters || sticky.Top != 0 || sticky.Scope != "" || sticky.ContainerID != "" || sticky.ZIndex != 0 {
			*issues = append(*issues, ValidationIssue{Path: path, Reason: "未启用冻结时不能携带冻结参数"})
		}
		return
	}
	if sticky.decoded && !sticky.hasTop {
		*issues = append(*issues, ValidationIssue{Path: path + ".top", Reason: "缺少必填字段"})
	}
	if sticky.decoded && !sticky.hasScope {
		*issues = append(*issues, ValidationIssue{Path: path + ".scope", Reason: "缺少必填字段"})
	}
	if sticky.decoded && !sticky.hasZIndex {
		*issues = append(*issues, ValidationIssue{Path: path + ".zIndex", Reason: "缺少必填字段"})
	}
	if sticky.Top < 0 {
		*issues = append(*issues, ValidationIssue{Path: path + ".top", Reason: "不能小于 0"})
	} else if sticky.Top > MaxStickyTop {
		*issues = append(*issues, ValidationIssue{Path: path + ".top", Reason: fmt.Sprintf("不能大于 %d", MaxStickyTop)})
	}
	if !oneOf(sticky.Scope, scopes...) {
		*issues = append(*issues, ValidationIssue{Path: path + ".scope", Reason: "不支持的冻结范围"})
	}
	if sticky.Scope == "CONTAINER" && sticky.ContainerID == "" {
		*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "容器冻结必须指定 containerId"})
	}
	if sticky.Scope != "CONTAINER" && (sticky.ContainerID != "" || sticky.decoded && sticky.hasContainerID) {
		*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "仅 CONTAINER 范围允许指定 containerId"})
	}
	if sticky.Scope == "CONTAINER" && sticky.ContainerID != "" {
		validateID(issues, path+".containerId", sticky.ContainerID)
		ancestorMatches := 0
		for _, ancestorID := range ancestorContainerIDs {
			if sticky.ContainerID == ancestorID {
				ancestorMatches++
			}
		}
		if ancestorMatches == 0 {
			// 浏览态只能受同页真实祖先约束，拒绝未知、跨页和旁系容器引用。
			*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "必须引用同页所属页面或分块祖先"})
		} else if ancestorMatches > 1 {
			// 页面和分块标识处于不同命名空间，裸 ID 同时命中时不能猜测容器类型。
			*issues = append(*issues, ValidationIssue{Path: path + ".containerId", Reason: "同时匹配多个祖先类型，容器引用存在歧义"})
		}
	}
	if sticky.ZIndex < 1 {
		*issues = append(*issues, ValidationIssue{Path: path + ".zIndex", Reason: "启用冻结时必须大于等于 1"})
	} else if sticky.ZIndex > MaxStickyZIndex {
		*issues = append(*issues, ValidationIssue{Path: path + ".zIndex", Reason: fmt.Sprintf("不能大于 %d", MaxStickyZIndex)})
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
