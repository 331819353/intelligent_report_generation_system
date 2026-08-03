package reportjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const cardSchemaURL = "https://schemas.intelligent-report.local/report-1.0.schema.json"

var semanticVersionPattern = regexp.MustCompile(`^1\.[0-9]+\.[0-9]+$`)

var cardTypes = map[string]bool{
	"TITLE": true, "CONCLUSION": true, "CHART": true,
	"COMPARISON": true, "RANKING": true, "TABLE": true,
}

// MarshalJSON ensures the current card DSL never leaks legacy canvas/page fields.
func (document Document) MarshalJSON() ([]byte, error) {
	type documentAlias Document
	if !document.IsCardDSL() {
		return json.Marshal(documentAlias(document))
	}
	return json.Marshal(struct {
		SchemaURL     string           `json:"$schema"`
		SchemaVersion string           `json:"schemaVersion"`
		Report        Report           `json:"report"`
		Layout        ResponsiveLayout `json:"layout"`
		GlobalFilters []GlobalFilter   `json:"globalFilters"`
		Cards         []Card           `json:"cards"`
		Extensions    map[string]any   `json:"extensions,omitempty"`
	}{
		SchemaURL: document.SchemaURL, SchemaVersion: document.SchemaVersion,
		Report: document.Report, Layout: valueOrZero(document.Layout),
		GlobalFilters: document.GlobalFilters, Cards: document.Cards, Extensions: document.Extensions,
	})
}

func valueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func decodeAndNormalizeCardDocument(raw []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("解析卡片式报告 JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Document{}, err
	}
	document.SchemaURL = cardSchemaURL
	if document.Report.Title == "" {
		document.Report.Title = document.Report.Name
	}
	if document.Report.Name == "" {
		document.Report.Name = document.Report.Title
	}
	if document.GlobalFilters == nil {
		document.GlobalFilters = []GlobalFilter{}
	}
	if document.Cards == nil {
		document.Cards = []Card{}
	}
	if document.Layout != nil {
		if document.Layout.Margin == 0 {
			document.Layout.Margin = 12
		}
		if document.Layout.Breakpoints == nil {
			document.Layout.Breakpoints = map[string]int{}
		}
	}
	for index := range document.GlobalFilters {
		if document.GlobalFilters[index].Options == nil {
			document.GlobalFilters[index].Options = []FilterOption{}
		}
	}
	for index := range document.Cards {
		card := &document.Cards[index]
		if card.Layout == nil {
			card.Layout = map[string]Grid{}
		}
		if card.Config == nil {
			card.Config = map[string]any{}
		}
		if card.Binding.Metrics == nil {
			card.Binding.Metrics = []MetricBinding{}
		}
		if card.Binding.Dimensions == nil {
			card.Binding.Dimensions = []DimensionBinding{}
		}
		if card.Binding.GlobalFilterBindings == nil {
			card.Binding.GlobalFilterBindings = []GlobalFilterBinding{}
		}
		if card.Binding.Filters == nil {
			card.Binding.Filters = []CardFilter{}
		}
		if card.Binding.Sort == nil {
			card.Binding.Sort = []CardSort{}
		}
		if card.Interactions == nil {
			card.Interactions = []CardInteraction{}
		}
	}
	return document, nil
}

func validateCardDocument(document Document) error {
	issues := make([]ValidationIssue, 0)
	add := func(path, reason string) { issues = append(issues, ValidationIssue{Path: path, Reason: reason}) }
	if document.SchemaVersion != CardSchemaVersion {
		add("schemaVersion", "必须为 1.0.0")
	}
	if document.SchemaURL != cardSchemaURL {
		add("$schema", "必须使用平台固定的 Report DSL Schema")
	}
	validateCardReport(&issues, document.Report)
	validateResponsiveLayout(&issues, document.Layout)
	filterIDs := validateGlobalFilters(&issues, document.GlobalFilters)
	if len(document.Cards) == 0 {
		add("cards", "至少需要一张卡片")
	}
	if len(document.Cards) > 200 {
		add("cards", "卡片数量不能超过 200")
	}
	cardIDs := map[string]bool{}
	for index, card := range document.Cards {
		path := fmt.Sprintf("cards[%d]", index)
		if strings.TrimSpace(card.ID) == "" || len(card.ID) > 128 {
			add(path+".id", "卡片标识不能为空且不能超过 128 个字符")
		} else if cardIDs[card.ID] {
			add(path+".id", "卡片标识重复")
		}
		cardIDs[card.ID] = true
		validateCard(&issues, path, card, filterIDs, document.Report.Status == "PUBLISHED")
	}
	validateCardInteractionReferences(&issues, document.Cards, cardIDs)
	validateBreakpointCollisions(&issues, document.Cards)
	if len(issues) > 0 {
		sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
		return &ValidationError{Issues: issues}
	}
	return nil
}

func validateCardInteractionReferences(issues *[]ValidationIssue, cards []Card, cardIDs map[string]bool) {
	for cardIndex, card := range cards {
		for interactionIndex, interaction := range card.Interactions {
			path := fmt.Sprintf("cards[%d].interactions[%d].action", cardIndex, interactionIndex)
			if interaction.Action.Type != "crossFilter" {
				continue
			}
			if !cardIDs[interaction.Action.TargetCardID] {
				*issues = append(*issues, ValidationIssue{Path: path + ".targetCardId", Reason: "跨卡筛选的目标卡片不存在"})
			}
		}
	}
}

func validateCardReport(issues *[]ValidationIssue, report Report) {
	add := func(path, reason string) { *issues = append(*issues, ValidationIssue{Path: path, Reason: reason}) }
	if report.ID != "" && len(report.ID) > 128 {
		add("report.id", "长度不能超过 128")
	}
	if !codePattern.MatchString(report.Code) {
		add("report.code", "必须是字母开头的稳定技术编码")
	}
	if strings.TrimSpace(report.Title) == "" || len(report.Title) > 200 {
		add("report.title", "标题不能为空且不能超过 200 个字符")
	}
	if report.Type != "DASHBOARD" && report.Type != "REPORT" {
		add("report.type", "必须为 DASHBOARD 或 REPORT")
	}
	if report.Status != "DRAFT" && report.Status != "PUBLISHED" && report.Status != "ARCHIVED" {
		add("report.status", "报告状态无效")
	}
	if report.ThemeID == "" {
		add("report.themeId", "必须选择主题")
	}
	if report.Timezone == "" {
		add("report.timezone", "必须声明时区")
	}
}

func validateResponsiveLayout(issues *[]ValidationIssue, layout *ResponsiveLayout) {
	add := func(path, reason string) { *issues = append(*issues, ValidationIssue{Path: path, Reason: reason}) }
	if layout == nil {
		add("layout", "必须声明响应式布局")
		return
	}
	if layout.Columns != 12 {
		add("layout.columns", "第一版固定为 12 列")
	}
	if layout.RowHeight < 4 || layout.RowHeight > 80 {
		add("layout.rowHeight", "必须在 4 到 80 之间")
	}
	if layout.Margin < 0 || layout.Margin > 48 {
		add("layout.margin", "必须在 0 到 48 之间")
	}
	lg, lgOK := layout.Breakpoints["lg"]
	md, mdOK := layout.Breakpoints["md"]
	sm, smOK := layout.Breakpoints["sm"]
	if !lgOK || !mdOK || !smOK || !(lg > md && md > sm && sm >= 0) {
		add("layout.breakpoints", "必须声明递减的 lg、md、sm 断点")
	}
}

func validateGlobalFilters(issues *[]ValidationIssue, filters []GlobalFilter) map[string]bool {
	ids := map[string]bool{}
	for index, filter := range filters {
		path := fmt.Sprintf("globalFilters[%d]", index)
		if filter.ID == "" || ids[filter.ID] {
			*issues = append(*issues, ValidationIssue{Path: path + ".id", Reason: "筛选标识不能为空且不能重复"})
		}
		ids[filter.ID] = true
		if filter.Label == "" {
			*issues = append(*issues, ValidationIssue{Path: path + ".label", Reason: "筛选名称不能为空"})
		}
		if !oneOf(filter.Type, "DATE_RANGE", "DATE", "SELECT", "MULTI_SELECT", "NUMBER_RANGE", "TEXT") {
			*issues = append(*issues, ValidationIssue{Path: path + ".type", Reason: "不支持的筛选类型"})
		}
		if filter.Source.SemanticModelID == "" || filter.Source.DimensionID == "" {
			*issues = append(*issues, ValidationIssue{Path: path + ".source", Reason: "必须绑定语义模型和维度"})
		}
		if !allowedFilterOperator(filter.Operator) {
			*issues = append(*issues, ValidationIssue{Path: path + ".operator", Reason: "不支持的筛选操作符"})
		}
	}
	return ids
}

func validateCard(issues *[]ValidationIssue, path string, card Card, filterIDs map[string]bool, requireCompleteBinding bool) {
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	if !cardTypes[card.Type] {
		add(".type", "不支持的卡片类型")
	}
	if !semanticVersionPattern.MatchString(card.CardVersion) {
		add(".cardVersion", "必须为 1.x.x 语义版本")
	}
	if strings.TrimSpace(card.Appearance.Title) == "" {
		add(".appearance.title", "卡片标题不能为空")
	}
	for _, breakpoint := range []string{"lg", "md", "sm"} {
		grid, ok := card.Layout[breakpoint]
		if !ok {
			add(".layout."+breakpoint, "必须持久化该断点布局")
			continue
		}
		if grid.X < 0 || grid.Y < 0 || grid.W < 1 || grid.W > 12 || grid.H < 2 || grid.X+grid.W > 12 {
			add(".layout."+breakpoint, "布局必须位于 12 列网格内且高度至少为 2")
		}
	}
	metricIDs := map[string]bool{}
	for index, metric := range card.Binding.Metrics {
		if metric.ID == "" || metricIDs[metric.ID] {
			add(fmt.Sprintf(".binding.metrics[%d].id", index), "指标标识不能为空且不能重复")
		}
		metricIDs[metric.ID] = true
		if !oneOf(metric.Role, "value", "baseline", "target", "numerator", "denominator", "series") {
			add(fmt.Sprintf(".binding.metrics[%d].role", index), "指标视觉角色无效")
		}
	}
	dimensionIDs := map[string]bool{}
	for index, dimension := range card.Binding.Dimensions {
		if dimension.ID == "" || dimensionIDs[dimension.ID] {
			add(fmt.Sprintf(".binding.dimensions[%d].id", index), "维度标识不能为空且不能重复")
		}
		dimensionIDs[dimension.ID] = true
		if !oneOf(dimension.Role, "category", "series", "group", "time", "column") {
			add(fmt.Sprintf(".binding.dimensions[%d].role", index), "维度视觉角色无效")
		}
	}
	if requireCompleteBinding {
		validateCardBindingShape(issues, path, card)
	}
	for index, binding := range card.Binding.GlobalFilterBindings {
		if !filterIDs[binding.FilterID] {
			add(fmt.Sprintf(".binding.globalFilterBindings[%d].filterId", index), "引用的全局筛选不存在")
		}
		if binding.TargetDimensionID == "" {
			add(fmt.Sprintf(".binding.globalFilterBindings[%d].targetDimensionId", index), "必须声明卡片语义模型中的目标筛选维度")
		}
	}
	for index, filter := range card.Binding.Filters {
		if filter.DimensionID == "" {
			add(fmt.Sprintf(".binding.filters[%d].dimensionId", index), "静态筛选必须声明目标维度")
		}
		if !allowedFilterOperator(filter.Operator) {
			add(fmt.Sprintf(".binding.filters[%d].operator", index), "静态筛选操作符无效")
		}
	}
	for index, sortRule := range card.Binding.Sort {
		if !dimensionIDs[sortRule.Field] && !metricIDs[sortRule.Field] {
			add(fmt.Sprintf(".binding.sort[%d].field", index), "排序字段必须绑定到本卡片")
		}
		if sortRule.Direction != "asc" && sortRule.Direction != "desc" {
			add(fmt.Sprintf(".binding.sort[%d].direction", index), "排序方向必须为 asc 或 desc")
		}
	}
	for index, interaction := range card.Interactions {
		validateCardInteraction(issues, fmt.Sprintf("%s.interactions[%d]", path, index), interaction)
	}
}

func validateCardBindingShape(issues *[]ValidationIssue, path string, card Card) {
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	metrics, dimensions := len(card.Binding.Metrics), len(card.Binding.Dimensions)
	switch card.Type {
	case "TITLE":
		if metrics != 0 || dimensions != 0 {
			add(".binding", "标题卡不能绑定指标或维度")
		}
	case "CONCLUSION":
		if metrics != 1 {
			add(".binding.metrics", "结论卡必须绑定一个主指标")
		}
	case "CHART":
		if metrics < 1 {
			add(".binding.metrics", "图形卡至少绑定一个指标")
		}
	case "COMPARISON":
		if metrics < 1 || metrics > 2 {
			add(".binding.metrics", "对比卡需要一个当前指标和可选基线指标")
		}
	case "RANKING":
		if metrics < 1 || dimensions < 1 {
			add(".binding", "排序卡至少需要一个指标和一个维度")
		}
		if len(card.Binding.Sort) == 0 || card.Binding.Limit < 1 || card.Binding.Limit > 100 {
			add(".binding", "排序卡必须设置 Sort 和 1～100 的 TopN")
		}
	case "TABLE":
		if metrics+dimensions < 1 {
			add(".binding", "表格卡至少绑定一个字段")
		}
	}
	if card.Type != "TITLE" && card.Binding.SemanticModelID == "" {
		add(".binding.semanticModelId", "数据卡片必须绑定语义模型")
	}
	if card.Binding.Limit < 0 || card.Binding.Limit > 1000 {
		add(".binding.limit", "查询行数必须在 0 到 1000 之间")
	}
}

func validateCardInteraction(issues *[]ValidationIssue, path string, interaction CardInteraction) {
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	if interaction.ID == "" {
		add(".id", "交互标识不能为空")
	}
	if !oneOf(interaction.Event, "data.click", "card.click", "table.row.click") {
		add(".event", "不支持的交互事件")
	}
	switch interaction.Action.Type {
	case "drillDown":
		if interaction.Action.PathID == "" || interaction.Action.ToDimension == "" {
			add(".action", "下钻必须声明 pathId 和 toDimension")
		}
	case "crossFilter":
		if interaction.Action.TargetCardID == "" {
			add(".action.targetCardId", "跨卡筛选必须声明目标卡片")
		}
	case "navigate", "openModal":
		if interaction.Action.TargetReportID == "" {
			add(".action.targetReportId", "跳转或弹窗必须声明目标报表")
		}
	case "openUrl":
		parsed, err := url.Parse(interaction.Action.URL)
		if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") {
			add(".action.url", "第一版仅允许站内绝对路径，外部域名必须由服务端白名单能力开放")
		}
	default:
		add(".action.type", "不支持的交互动作")
	}
}

func validateBreakpointCollisions(issues *[]ValidationIssue, cards []Card) {
	for _, breakpoint := range []string{"lg", "md", "sm"} {
		for current := range cards {
			left, ok := cards[current].Layout[breakpoint]
			if !ok {
				continue
			}
			for previous := 0; previous < current; previous++ {
				right, exists := cards[previous].Layout[breakpoint]
				if exists && overlaps(left, right) {
					*issues = append(*issues, ValidationIssue{
						Path:   fmt.Sprintf("cards[%d].layout.%s", current, breakpoint),
						Reason: fmt.Sprintf("与 cards[%d] 在 %s 断点发生碰撞", previous, breakpoint),
					})
				}
			}
		}
	}
}

func allowedFilterOperator(value string) bool {
	return oneOf(value, "equals", "notEquals", "in", "notIn", "between", "gte", "lt")
}
