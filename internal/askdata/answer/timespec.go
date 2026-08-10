// Package answer contains deterministic, evidence-backed presentation
// contracts. It never composes prose from unverified model output.
package answer

import (
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type RenderOptions struct {
	Locale string `json:"locale,omitempty"`
}

type TimeSpecView struct {
	RangeLabel      string `json:"rangeLabel"`
	AsOfLabel       string `json:"asOfLabel"`
	PolicyLabel     string `json:"policyLabel"`
	ComparisonLabel string `json:"comparisonLabel"`
	TruncatedHint   string `json:"truncatedHint"`
}

// RenderTimeSpec is the only user-facing time renderer. Answer composition,
// evidence, report runtime and exports must consume this exact view.
func RenderTimeSpec(spec compiler.ResolvedTimeSpec, options RenderOptions) TimeSpecView {
	locale := options.Locale
	if locale == "" {
		locale = "zh-CN"
	}
	if locale != "zh-CN" || compiler.ValidateResolvedTimeSpec(spec) != nil {
		return TimeSpecView{}
	}
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return TimeSpecView{}
	}
	start := dateLabel(spec.ResolvedStart, location)
	end := dateLabel(spec.ResolvedEndExclusive.In(location).AddDate(0, 0, -1), location)
	asOf := dateLabel(spec.DataAvailableThrough, location)
	view := TimeSpecView{
		RangeLabel:  start + " 至 " + end,
		AsOfLabel:   "数据截止 " + asOf,
		PolicyLabel: policyLabel(spec),
	}
	if spec.Comparison != nil {
		comparisonStart := dateLabel(spec.Comparison.ResolvedStart, location)
		comparisonEnd := dateLabel(spec.Comparison.ResolvedEndExclusive.In(location).AddDate(0, 0, -1), location)
		alignment := "按相同自然日期对齐"
		if spec.Comparison.Alignment == string(registry.ComparisonSameDayCount) {
			alignment = "按相同天数对齐"
		}
		view.ComparisonLabel = "对比期 " + comparisonStart + " 至 " + comparisonEnd + "，" + alignment
		if spec.Comparison.OverflowApplied {
			view.ComparisonLabel += "，月末已对齐至最后一天"
		}
	}
	if spec.TruncatedByDataAvailability {
		view.TruncatedHint = "数据仅更新至 " + asOf + "，结果已按可用范围裁剪"
	}
	return view
}

func dateLabel(value time.Time, location *time.Location) string {
	return value.In(location).Format("2006-01-02")
}

func policyLabel(spec compiler.ResolvedTimeSpec) string {
	period := requestedPeriodLabel(spec.RequestedPeriod)
	switch registry.IncompletePeriodPolicy(spec.PolicyApplied) {
	case registry.IncompletePeriodMTD:
		if spec.RequestedPeriod == "TODAY" {
			return "今日（MTD）"
		}
		return period + "至今（MTD）"
	case registry.IncompletePeriodFull:
		return period + "完整周期（FULL_PERIOD）"
	case registry.IncompletePeriodLastComplete:
		if spec.PeriodFallbackApplied {
			return "已回退至上一完整" + grainLabel(spec.Grain) + "（LAST_COMPLETE）"
		}
		return period + "上一完整周期（LAST_COMPLETE）"
	default:
		return ""
	}
}

func requestedPeriodLabel(value string) string {
	switch value {
	case "TODAY":
		return "今日"
	case "YESTERDAY":
		return "昨日"
	case "CURRENT_WEEK":
		return "本周"
	case "PREVIOUS_WEEK":
		return "上周"
	case "CURRENT_MONTH":
		return "本月"
	case "PREVIOUS_MONTH":
		return "上月"
	case "CURRENT_QUARTER":
		return "本季度"
	case "PREVIOUS_QUARTER":
		return "上季度"
	case "CURRENT_YEAR":
		return "本年"
	case "PREVIOUS_YEAR":
		return "上年"
	case "CURRENT_FISCAL_MONTH":
		return "本财月"
	case "PREVIOUS_FISCAL_MONTH":
		return "上财月"
	case "CURRENT_FISCAL_QUARTER":
		return "本财季"
	case "PREVIOUS_FISCAL_QUARTER":
		return "上财季"
	case "CURRENT_FISCAL_YEAR":
		return "本财年"
	case "PREVIOUS_FISCAL_YEAR":
		return "上财年"
	case "EXPLICIT_DAY":
		return "指定日期"
	case "EXPLICIT_MONTH":
		return "指定月份"
	case "EXPLICIT_YEAR":
		return "指定年份"
	case "LAST_12_MONTHS":
		return "近 12 个月"
	case "ABSOLUTE", "EXPLICIT_RANGE":
		return "指定区间"
	default:
		return "请求周期"
	}
}

func grainLabel(value string) string {
	switch strings.TrimPrefix(value, "FISCAL_") {
	case "DAY":
		return "日"
	case "WEEK":
		return "周"
	case "MONTH":
		return "月"
	case "QUARTER":
		return "季度"
	case "YEAR":
		return "年"
	default:
		return "周期"
	}
}
