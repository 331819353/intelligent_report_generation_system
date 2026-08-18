// Package cardkind owns the closed, versioned semantic card vocabulary used by
// both the manual report builder and the report AI blueprint contract.
package cardkind

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/template"
)

// Kind describes the question a report card answers. It deliberately does not
// describe a renderer; Resolve chooses a component from Candidates only after
// the data shape has passed Contract validation.
type Kind string

const (
	KPI          Kind = "KPI"
	Trend        Kind = "TREND"
	Compare      Kind = "COMPARE"
	Rank         Kind = "RANK"
	Composition  Kind = "COMPOSITION"
	Distribution Kind = "DISTRIBUTION"
	Funnel       Kind = "FUNNEL"
	Target       Kind = "TARGET"
	Detail       Kind = "DETAIL"
	Insight      Kind = "INSIGHT"
	Summary      Kind = "SUMMARY"
	Recommend    Kind = "RECOMMEND"
	Text         Kind = "TEXT"
	Image        Kind = "IMAGE"
	Divider      Kind = "DIVIDER"
	Filter       Kind = "FILTER"
)

const Version = "1.0.0"

type Cardinality struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type Contract struct {
	Metrics    Cardinality `json:"metrics"`
	Dimensions Cardinality `json:"dimensions"`
	TopN       bool        `json:"topN"`
}

type LayoutIntent struct {
	Span            int    `json:"span"`
	MinRows         int    `json:"minRows"`
	NarrativeAttach string `json:"narrativeAttach"`
	MobileSlotMode  string `json:"mobileSlotMode"`
}

type Manifest struct {
	Kind           Kind                     `json:"kind"`
	Version        string                   `json:"version"`
	DisplayName    string                   `json:"displayName"`
	Question       string                   `json:"question"`
	Contract       Contract                 `json:"contract"`
	Candidates     []string                 `json:"candidates"`
	DefaultMethods []insight.AnalysisMethod `json:"defaultMethods"`
	LayoutIntent   LayoutIntent             `json:"layoutIntent"`
}

type Registry struct {
	items map[Kind]Manifest
}

// NewDefaultRegistry validates the semantic vocabulary against the exact
// component and method registries used by the compiler and runtime.
func NewDefaultRegistry(components *template.Registry, methods *insight.Registry) (*Registry, error) {
	if components == nil || methods == nil {
		return nil, errors.New("card kind dependencies are required")
	}
	items := []Manifest{
		manifest(KPI, "指标卡", "关键指标本期多少、变化多少", 1, 3, 0, 1, false, []string{"metric-card"}, []insight.AnalysisMethod{insight.AnalysisCurrentValue, insight.AnalysisPeriodComparison}, 6, 3),
		manifest(Trend, "趋势卡", "指标随时间如何变化", 1, 3, 1, 2, false, []string{"line-trend", "area-stacked"}, []insight.AnalysisMethod{insight.AnalysisTrend, insight.AnalysisAnomalyPoint}, 16, 5),
		manifest(Compare, "对比卡", "分组之间谁高谁低", 1, 2, 1, 2, false, []string{"bar-comparison"}, []insight.AnalysisMethod{insight.AnalysisGroupDifference}, 12, 5),
		manifest(Rank, "排名卡", "前后 N 名是谁", 1, 1, 1, 1, true, []string{"bar-horizontal", "data-table"}, []insight.AnalysisMethod{insight.AnalysisTopN}, 12, 6),
		manifest(Composition, "构成卡", "各对象的占比与贡献如何", 1, 1, 1, 1, false, []string{"pie-donut", "area-stacked"}, []insight.AnalysisMethod{insight.AnalysisShareOfTotal, insight.AnalysisContribution}, 8, 5),
		manifest(Distribution, "分布卡", "指标分布与离群情况如何", 2, 3, 0, 2, false, []string{"scatter"}, []insight.AnalysisMethod{insight.AnalysisAnomalyPoint}, 12, 6),
		manifest(Funnel, "漏斗卡", "各阶段转化与流失如何", 1, 1, 1, 1, false, []string{"funnel"}, []insight.AnalysisMethod{insight.AnalysisCurrentValue}, 12, 6),
		manifest(Target, "目标卡", "目标完成进度如何", 1, 3, 0, 1, false, []string{"metric-card"}, []insight.AnalysisMethod{insight.AnalysisTargetAchievement}, 8, 3),
		manifest(Detail, "明细卡", "底层记录是什么", 1, 16, 0, 16, false, []string{"data-table"}, []insight.AnalysisMethod{insight.AnalysisDataCompleteness}, 24, 8),
		manifest(Insight, "结论卡", "数据证据说明了什么", 1, 8, 0, 8, false, []string{"insight-text"}, []insight.AnalysisMethod{insight.AnalysisCurrentValue}, 8, 3),
		manifest(Summary, "摘要卡", "本节或整份报告的关键结论是什么", 0, 0, 0, 0, false, []string{"rich-text"}, nil, 24, 3),
		manifest(Recommend, "建议卡", "基于已验证结论建议关注什么", 0, 0, 0, 0, false, []string{"rich-text"}, nil, 24, 3),
		manifest(Text, "文本卡", "需要补充什么说明", 0, 0, 0, 0, false, []string{"rich-text"}, nil, 24, 3),
		manifest(Image, "图片卡", "需要展示什么图片", 0, 0, 0, 0, false, []string{"image"}, nil, 12, 4),
		manifest(Divider, "分隔卡", "如何分隔内容", 0, 0, 0, 0, false, []string{"rich-text"}, nil, 24, 1),
		manifest(Filter, "筛选卡", "使用什么维度筛选", 0, 0, 1, 1, false, []string{"filter-control"}, nil, 6, 2),
	}
	registry := &Registry{items: make(map[Kind]Manifest, len(items))}
	for index, item := range items {
		if err := validateManifest(item, components, methods); err != nil {
			return nil, fmt.Errorf("card kinds[%d]: %w", index, err)
		}
		if _, exists := registry.items[item.Kind]; exists {
			return nil, fmt.Errorf("card kind %q is duplicated", item.Kind)
		}
		registry.items[item.Kind] = clone(item)
	}
	return registry, nil
}

func manifest(kind Kind, name, question string, metricMin, metricMax, dimensionMin, dimensionMax int, topN bool, candidates []string, methods []insight.AnalysisMethod, span, rows int) Manifest {
	return Manifest{
		Kind: kind, Version: Version, DisplayName: name, Question: question,
		Contract:   Contract{Metrics: Cardinality{Min: metricMin, Max: metricMax}, Dimensions: Cardinality{Min: dimensionMin, Max: dimensionMax}, TopN: topN},
		Candidates: append([]string(nil), candidates...), DefaultMethods: append([]insight.AnalysisMethod(nil), methods...),
		LayoutIntent: LayoutIntent{Span: span, MinRows: rows, NarrativeAttach: "BELOW", MobileSlotMode: "STACK"},
	}
}

func validateManifest(item Manifest, components *template.Registry, methods *insight.Registry) error {
	if strings.TrimSpace(string(item.Kind)) == "" || item.Version != Version || strings.TrimSpace(item.DisplayName) == "" || strings.TrimSpace(item.Question) == "" {
		return errors.New("identity is invalid")
	}
	if item.Contract.Metrics.Min < 0 || item.Contract.Metrics.Max < item.Contract.Metrics.Min || item.Contract.Metrics.Max > 16 ||
		item.Contract.Dimensions.Min < 0 || item.Contract.Dimensions.Max < item.Contract.Dimensions.Min || item.Contract.Dimensions.Max > 16 {
		return errors.New("contract cardinality is invalid")
	}
	if len(item.Candidates) == 0 || item.LayoutIntent.Span < 1 || item.LayoutIntent.Span > 24 || item.LayoutIntent.MinRows < 1 {
		return errors.New("component candidates or layout intent is invalid")
	}
	for _, candidate := range item.Candidates {
		found := false
		for _, component := range components.List() {
			if component.Type == candidate {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("component candidate %q is not registered", candidate)
		}
	}
	for _, method := range item.DefaultMethods {
		if _, found := methods.Get(method); !found {
			return fmt.Errorf("analysis method %q is not registered", method)
		}
	}
	return nil
}

func (registry *Registry) Get(kind Kind) (Manifest, bool) {
	if registry == nil {
		return Manifest{}, false
	}
	item, ok := registry.items[kind]
	return clone(item), ok
}

func (registry *Registry) List() []Manifest {
	if registry == nil {
		return []Manifest{}
	}
	items := make([]Manifest, 0, len(registry.items))
	for _, item := range registry.items {
		items = append(items, clone(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Kind < items[j].Kind })
	return items
}

// Resolve returns the first registered component candidate whose immutable
// data contract can represent the blueprint card shape.
func (registry *Registry) Resolve(kind Kind, metricCount, dimensionCount int, components *template.Registry) (Manifest, template.Manifest, error) {
	item, ok := registry.Get(kind)
	if !ok {
		return Manifest{}, template.Manifest{}, fmt.Errorf("card kind %q is not registered", kind)
	}
	if metricCount < item.Contract.Metrics.Min || metricCount > item.Contract.Metrics.Max ||
		dimensionCount < item.Contract.Dimensions.Min || dimensionCount > item.Contract.Dimensions.Max {
		return Manifest{}, template.Manifest{}, fmt.Errorf("card kind %s requires metrics %d..%d and dimensions %d..%d", item.Kind,
			item.Contract.Metrics.Min, item.Contract.Metrics.Max, item.Contract.Dimensions.Min, item.Contract.Dimensions.Max)
	}
	for _, candidate := range item.Candidates {
		var versions []template.Manifest
		for _, component := range components.List() {
			if component.Type == candidate && metricCount >= component.DataContract.Measures.Min && metricCount <= component.DataContract.Measures.Max &&
				dimensionCount >= component.DataContract.Dimensions.Min && dimensionCount <= component.DataContract.Dimensions.Max {
				versions = append(versions, component)
			}
		}
		if len(versions) != 0 {
			sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
			return item, versions[len(versions)-1], nil
		}
	}
	return Manifest{}, template.Manifest{}, fmt.Errorf("card kind %s has no component for metrics=%d dimensions=%d", kind, metricCount, dimensionCount)
}

func clone(item Manifest) Manifest {
	item.Candidates = append([]string(nil), item.Candidates...)
	item.DefaultMethods = append([]insight.AnalysisMethod(nil), item.DefaultMethods...)
	return item
}
