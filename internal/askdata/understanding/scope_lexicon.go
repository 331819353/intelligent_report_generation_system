package understanding

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	DefaultScopeLexiconVersion = "askdata-scope-lexicon-2026.08"
	MaxScopeLexiconTerms       = 128
	MaxScopeLexiconTermRunes   = 64
)

var ErrInvalidScopeLexicon = errors.New("question scope lexicon is invalid")

// ScopeThresholds keeps structural classification thresholds versioned with
// the lexical rules that a semantic Release pins.
type ScopeThresholds struct {
	MultiMetricCount    int `json:"multiMetricCount"`
	GroupedDimensionMin int `json:"groupedDimensionMin"`
	FilterValueMin      int `json:"filterValueMin"`
}

// ScopeLexicon is a release-pinned, deterministic vocabulary. Callers may add
// current-domain external system names to CrossDomainTerms and ungoverned
// source names to UngovernedSourceTerms before constructing a Classifier.
type ScopeLexicon struct {
	Version               string          `json:"version"`
	Thresholds            ScopeThresholds `json:"thresholds"`
	DefinitionTerms       []string        `json:"definitionTerms"`
	BundleTerms           []string        `json:"bundleTerms"`
	StrongDetailTerms     []string        `json:"strongDetailTerms"`
	WeakDetailTerms       []string        `json:"weakDetailTerms"`
	ForecastTerms         []string        `json:"forecastTerms"`
	AdHocFormulaTerms     []string        `json:"adHocFormulaTerms"`
	CausalTerms           []string        `json:"causalTerms"`
	CrossDomainTerms      []string        `json:"crossDomainTerms"`
	UngovernedSourceTerms []string        `json:"ungovernedSourceTerms"`
	RatioTerms            []string        `json:"ratioTerms"`
	RankingTerms          []string        `json:"rankingTerms"`
	ComparisonTerms       []string        `json:"comparisonTerms"`
}

func DefaultScopeLexicon() ScopeLexicon {
	return ScopeLexicon{
		Version: DefaultScopeLexiconVersion,
		Thresholds: ScopeThresholds{
			MultiMetricCount: 2, GroupedDimensionMin: 1, FilterValueMin: 1,
		},
		DefinitionTerms: []string{
			"怎么算", "如何计算", "计算口径", "口径是什么", "什么口径", "口径说明",
			"怎么定义", "定义是什么", "是什么意思", "包含哪些", "是否包含", "怎么统计",
		},
		BundleTerms: []string{
			"怎么样", "什么情况", "情况如何", "概况", "总体表现", "整体表现", "经营情况",
		},
		StrongDetailTerms: []string{
			"明细", "清单", "名单", "逐笔", "每一笔", "所有订单", "全部订单", "导出全部",
			"导出所有", "原始记录", "逐条记录",
		},
		WeakDetailTerms: []string{"列出", "列一下", "给我所有", "全部给我"},
		ForecastTerms: []string{
			"预测", "预计", "将会", "会不会", "下个月会", "未来", "趋势预估", "forecast",
		},
		AdHocFormulaTerms: []string{
			"自定义公式", "临时公式", "帮我算", "除以", "乘以", "加上", "减去", "自行计算",
		},
		CausalTerms: []string{"为什么", "什么原因", "原因是", "导致", "因为什么", "根因", "因果"},
		CrossDomainTerms: []string{
			"跨领域", "跨域", "其他领域", "另一个领域", "当前领域之外", "切到其他领域",
			"结合销售和供应链", "结合财务和供应链",
		},
		UngovernedSourceTerms: []string{
			"未接入系统", "未接入的数据", "本地excel", "个人excel", "微信群", "临时表",
			"原始数据库", "数据库原表", "erp原表", "oa系统", "个人网盘",
		},
		RatioTerms:      []string{"占比", "完成率", "达成率", "转化率", "渗透率", "比例"},
		RankingTerms:    []string{"top", "前几", "前十", "排名", "排行", "最高", "最低", "倒数"},
		ComparisonTerms: []string{"同比", "环比", "对比", "比较", "较上", "较去", "差异"},
	}
}

func (lexicon ScopeLexicon) Validate() error {
	if strings.TrimSpace(lexicon.Version) == "" || utf8.RuneCountInString(lexicon.Version) > 128 {
		return fmt.Errorf("%w: version", ErrInvalidScopeLexicon)
	}
	if lexicon.Thresholds.MultiMetricCount < 2 || lexicon.Thresholds.MultiMetricCount > MaxMetricMentions ||
		lexicon.Thresholds.GroupedDimensionMin < 1 || lexicon.Thresholds.GroupedDimensionMin > MaxDimensionMentions ||
		lexicon.Thresholds.FilterValueMin < 1 || lexicon.Thresholds.FilterValueMin > MaxValueMentions {
		return fmt.Errorf("%w: thresholds", ErrInvalidScopeLexicon)
	}
	groups := [][]string{
		lexicon.DefinitionTerms, lexicon.BundleTerms, lexicon.StrongDetailTerms, lexicon.WeakDetailTerms,
		lexicon.ForecastTerms, lexicon.AdHocFormulaTerms, lexicon.CausalTerms, lexicon.CrossDomainTerms,
		lexicon.UngovernedSourceTerms, lexicon.RatioTerms, lexicon.RankingTerms, lexicon.ComparisonTerms,
	}
	for groupIndex, terms := range groups {
		if len(terms) == 0 || len(terms) > MaxScopeLexiconTerms {
			return fmt.Errorf("%w: term group %d size", ErrInvalidScopeLexicon, groupIndex)
		}
		seen := map[string]struct{}{}
		for termIndex, term := range terms {
			normalized := strings.TrimSpace(strings.ToLower(term))
			if normalized == "" || utf8.RuneCountInString(normalized) > MaxScopeLexiconTermRunes {
				return fmt.Errorf("%w: term group %d item %d", ErrInvalidScopeLexicon, groupIndex, termIndex)
			}
			if _, exists := seen[normalized]; exists {
				return fmt.Errorf("%w: duplicate term %q", ErrInvalidScopeLexicon, normalized)
			}
			seen[normalized] = struct{}{}
		}
	}
	return nil
}

func (lexicon ScopeLexicon) ContentHash() (askdata.ContentHash, error) {
	if err := lexicon.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(cloneScopeLexicon(lexicon))
	if err != nil {
		return "", fmt.Errorf("%w: encode: %v", ErrInvalidScopeLexicon, err)
	}
	return askdata.HashBytes(canonical), nil
}

func cloneScopeLexicon(source ScopeLexicon) ScopeLexicon {
	result := source
	result.DefinitionTerms = normalizedScopeTerms(source.DefinitionTerms)
	result.BundleTerms = normalizedScopeTerms(source.BundleTerms)
	result.StrongDetailTerms = normalizedScopeTerms(source.StrongDetailTerms)
	result.WeakDetailTerms = normalizedScopeTerms(source.WeakDetailTerms)
	result.ForecastTerms = normalizedScopeTerms(source.ForecastTerms)
	result.AdHocFormulaTerms = normalizedScopeTerms(source.AdHocFormulaTerms)
	result.CausalTerms = normalizedScopeTerms(source.CausalTerms)
	result.CrossDomainTerms = normalizedScopeTerms(source.CrossDomainTerms)
	result.UngovernedSourceTerms = normalizedScopeTerms(source.UngovernedSourceTerms)
	result.RatioTerms = normalizedScopeTerms(source.RatioTerms)
	result.RankingTerms = normalizedScopeTerms(source.RankingTerms)
	result.ComparisonTerms = normalizedScopeTerms(source.ComparisonTerms)
	return result
}

func normalizedScopeTerms(source []string) []string {
	result := make([]string, len(source))
	for index, value := range source {
		result[index] = strings.TrimSpace(strings.ToLower(value))
	}
	sort.Strings(result)
	return result
}

func containsScopeTerm(question string, terms []string) bool {
	compactQuestion := compactScopeText(question)
	for _, term := range terms {
		if strings.Contains(question, term) || strings.Contains(compactQuestion, compactScopeText(term)) {
			return true
		}
	}
	return false
}

func compactScopeText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, value)
}
