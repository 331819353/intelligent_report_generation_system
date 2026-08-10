package understanding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/datarequest"
)

const ScopeVerdictSchemaVersion = "question-scope-verdict-v1"

var (
	ErrInvalidScopeVerdict = errors.New("question scope verdict is invalid")
	ErrInvalidScopeContext = errors.New("question scope parsed context is invalid")
)

type QuestionType string

const (
	QuestionTypeMetricLookup     QuestionType = "METRIC_LOOKUP"
	QuestionTypeGroupedAnalysis  QuestionType = "GROUPED_ANALYSIS"
	QuestionTypeFilteredAnalysis QuestionType = "FILTERED_ANALYSIS"
	QuestionTypeRanking          QuestionType = "RANKING"
	QuestionTypeComparison       QuestionType = "COMPARISON"
	QuestionTypeMultiMetric      QuestionType = "MULTI_METRIC"
	QuestionTypeRatioTarget      QuestionType = "RATIO_TARGET"
	QuestionTypeDefinition       QuestionType = "DEFINITION"
	QuestionTypeBundle           QuestionType = "BUNDLE"
	QuestionTypeDetailList       QuestionType = "DETAIL_LIST"
	QuestionTypeForecast         QuestionType = "FORECAST"
	QuestionTypeAdHocFormula     QuestionType = "AD_HOC_FORMULA"
	QuestionTypeCausal           QuestionType = "CAUSAL"
	QuestionTypeCrossDomain      QuestionType = "CROSS_DOMAIN"
	QuestionTypeUngovernedSource QuestionType = "UNGOVERNED_SOURCE"
)

var questionTypeWhitelist = []QuestionType{
	QuestionTypeMetricLookup,
	QuestionTypeGroupedAnalysis,
	QuestionTypeFilteredAnalysis,
	QuestionTypeRanking,
	QuestionTypeComparison,
	QuestionTypeMultiMetric,
	QuestionTypeRatioTarget,
	QuestionTypeDefinition,
	QuestionTypeBundle,
	QuestionTypeDetailList,
	QuestionTypeForecast,
	QuestionTypeAdHocFormula,
	QuestionTypeCausal,
	QuestionTypeCrossDomain,
	QuestionTypeUngovernedSource,
}

type ScopeOutcome string

const (
	ScopeOutcomeExecute    ScopeOutcome = "EXECUTE"
	ScopeOutcomeDefinition ScopeOutcome = "DEFINITION"
	ScopeOutcomeBundle     ScopeOutcome = "BUNDLE"
	ScopeOutcomeOutOfScope ScopeOutcome = "OUT_OF_SCOPE"
	ScopeOutcomeBlocked    ScopeOutcome = "BLOCKED"
)

type ScopeReason string

const (
	ScopeReasonSupported          ScopeReason = "SCOPE_SUPPORTED"
	ScopeReasonDefinition         ScopeReason = "SCOPE_DEFINITION"
	ScopeReasonBundle             ScopeReason = "SCOPE_BUNDLE"
	ScopeReasonDetailList         ScopeReason = "SCOPE_DETAIL_LIST"
	ScopeReasonForecast           ScopeReason = "SCOPE_FORECAST"
	ScopeReasonAdHocFormula       ScopeReason = "SCOPE_AD_HOC_FORMULA"
	ScopeReasonCausalUnsupported  ScopeReason = "SCOPE_CAUSAL_UNSUPPORTED"
	ScopeReasonCausalContribution ScopeReason = "SCOPE_CAUSAL_CONTRIBUTION"
	ScopeReasonCrossDomain        ScopeReason = "SCOPE_CROSS_DOMAIN"
	ScopeReasonUngovernedSource   ScopeReason = "SCOPE_UNGOVERNED_SOURCE"
)

type ClassificationSource string

const (
	ClassificationSourceRule             ClassificationSource = "RULE"
	ClassificationSourceLLMFallback      ClassificationSource = "LLM_FALLBACK"
	ClassificationSourceFallbackRejected ClassificationSource = "RULE_FALLBACK_REJECTED"
)

type NextActionKind string

const (
	NextActionDataRequest   NextActionKind = "DATA_REQUEST"
	NextActionMetricRequest NextActionKind = "METRIC_REQUEST"
	NextActionRephrase      NextActionKind = "REPHRASE"
)

type NextActionTarget string

const (
	NextActionTargetDataRequestDialog NextActionTarget = "DATA_REQUEST_DIALOG"
	NextActionTargetMetricRequestForm NextActionTarget = "METRIC_REQUEST_FORM"
	NextActionTargetAskDataComposer   NextActionTarget = "ASK_DATA_COMPOSER"
)

type NextActionPrefill string

const NextActionPrefillCurrentQuestion NextActionPrefill = "CURRENT_QUESTION"

// NextActionPayload is deliberately closed. It may prefill the already
// approved request/composer entry points, but cannot carry SQL or result rows.
type NextActionPayload struct {
	Target  NextActionTarget  `json:"target"`
	Prefill NextActionPrefill `json:"prefill"`
}

type NextAction struct {
	Kind    NextActionKind    `json:"kind"`
	Label   string            `json:"label"`
	Payload NextActionPayload `json:"payload"`
}

// ParsedContext aliases the DR-001 whitelist. Its only possible values are
// bound metric/dimension/member UUIDs and a normalized time range.
type ParsedContext = datarequest.ParsedContext

type ScopeVerdict struct {
	SchemaVersion        string               `json:"schemaVersion"`
	Type                 QuestionType         `json:"type"`
	Outcome              ScopeOutcome         `json:"outcome"`
	Reason               ScopeReason          `json:"reason"`
	UserMessage          string               `json:"userMessage,omitempty"`
	NextActions          []NextAction         `json:"nextActions"`
	ParsedContext        *ParsedContext       `json:"parsedContext,omitempty"`
	LexiconVersion       string               `json:"lexiconVersion"`
	LexiconHash          askdata.ContentHash  `json:"lexiconHash"`
	ClassificationSource ClassificationSource `json:"classificationSource"`
}

type ScopeFallbackInput struct {
	Question       string              `json:"question"`
	AllowedTypes   []QuestionType      `json:"allowedTypes"`
	LexiconVersion string              `json:"lexiconVersion"`
	LexiconHash    askdata.ContentHash `json:"lexiconHash"`
}

// ScopeFallback returns a raw enum string so the trusted boundary can reject
// invented values before they become a QuestionType.
type ScopeFallback interface {
	ClassifyQuestionType(context.Context, ScopeFallbackInput) (string, error)
}

type ScopeClassifier struct {
	lexicon             ScopeLexicon
	lexiconHash         askdata.ContentHash
	fallback            ScopeFallback
	contributionEnabled bool
}

func NewScopeClassifier(lexicon ScopeLexicon, fallback ScopeFallback, contributionEnabled bool) (*ScopeClassifier, error) {
	if err := lexicon.Validate(); err != nil {
		return nil, err
	}
	cloned := cloneScopeLexicon(lexicon)
	hash, err := cloned.ContentHash()
	if err != nil {
		return nil, err
	}
	return &ScopeClassifier{
		lexicon: cloned, lexiconHash: hash, fallback: fallback, contributionEnabled: contributionEnabled,
	}, nil
}

// Classify is the deterministic default requested by the public NLU contract.
// Unknown lexical shapes fall back to a conservative Bundle candidate; the
// configurable classifier below may ask an LLM only for that unresolved case.
func Classify(understanding QuestionUnderstanding) (QuestionType, ScopeVerdict) {
	classifier, err := NewScopeClassifier(DefaultScopeLexicon(), nil, false)
	if err != nil {
		panic(err)
	}
	return classifier.Classify(context.Background(), understanding)
}

func (classifier *ScopeClassifier) Classify(ctx context.Context, understanding QuestionUnderstanding) (QuestionType, ScopeVerdict) {
	candidate, confident := classifier.ruleCandidate(understanding)
	source := ClassificationSourceRule
	if !confident && classifier.fallback != nil {
		raw, err := classifier.fallback.ClassifyQuestionType(ctx, ScopeFallbackInput{
			Question: understanding.Question, AllowedTypes: AllowedQuestionTypes(),
			LexiconVersion: classifier.lexicon.Version, LexiconHash: classifier.lexiconHash,
		})
		if err == nil && validQuestionType(QuestionType(raw)) {
			candidate = QuestionType(raw)
			source = ClassificationSourceLLMFallback
		} else {
			source = ClassificationSourceFallbackRejected
		}
	}
	verdict := classifier.verdict(candidate)
	verdict.ClassificationSource = source
	return candidate, verdict
}

func (classifier *ScopeClassifier) LexiconVersion() string {
	return classifier.lexicon.Version
}

func AllowedQuestionTypes() []QuestionType {
	return append([]QuestionType(nil), questionTypeWhitelist...)
}

func validQuestionType(value QuestionType) bool {
	for _, allowed := range questionTypeWhitelist {
		if value == allowed {
			return true
		}
	}
	return false
}

func (classifier *ScopeClassifier) ruleCandidate(understanding QuestionUnderstanding) (QuestionType, bool) {
	question := normalizedScopeQuestion(understanding.Question)
	lexicon := classifier.lexicon
	metricCount := len(understanding.MetricMentions)
	groupedCount := 0
	for _, dimension := range understanding.DimensionMentions {
		if dimension.Role == DimensionRoleGroupBy {
			groupedCount++
		}
	}

	switch {
	case containsScopeTerm(question, lexicon.UngovernedSourceTerms):
		return QuestionTypeUngovernedSource, true
	case containsScopeTerm(question, lexicon.CrossDomainTerms):
		return QuestionTypeCrossDomain, true
	case containsScopeTerm(question, lexicon.ForecastTerms):
		return QuestionTypeForecast, true
	case containsScopeTerm(question, lexicon.AdHocFormulaTerms):
		return QuestionTypeAdHocFormula, true
	case containsScopeTerm(question, lexicon.DefinitionTerms):
		return QuestionTypeDefinition, true
	case containsScopeTerm(question, lexicon.CausalTerms):
		return QuestionTypeCausal, true
	case containsScopeTerm(question, lexicon.StrongDetailTerms):
		return QuestionTypeDetailList, true
	case len(understanding.Ordering) > 0 || containsScopeTerm(question, lexicon.RankingTerms):
		return QuestionTypeRanking, true
	case len(understanding.Comparisons) > 0 || containsScopeTerm(question, lexicon.ComparisonTerms):
		return QuestionTypeComparison, true
	case containsScopeTerm(question, lexicon.RatioTerms):
		return QuestionTypeRatioTarget, true
	case metricCount >= lexicon.Thresholds.MultiMetricCount:
		return QuestionTypeMultiMetric, true
	case metricCount > 0 && groupedCount >= lexicon.Thresholds.GroupedDimensionMin:
		// A weak verb such as “列出” does not turn a governed aggregate grouped
		// by region into a row-level detail request.
		return QuestionTypeGroupedAnalysis, true
	case metricCount > 0 && len(understanding.ValueMentions) >= lexicon.Thresholds.FilterValueMin:
		return QuestionTypeFilteredAnalysis, true
	case containsScopeTerm(question, lexicon.BundleTerms):
		return QuestionTypeBundle, true
	case containsScopeTerm(question, lexicon.WeakDetailTerms):
		return QuestionTypeDetailList, true
	case metricCount > 0:
		return QuestionTypeMetricLookup, true
	default:
		return QuestionTypeBundle, false
	}
}

func normalizedScopeQuestion(question string) string {
	normalized, err := NormalizeQuestion(question)
	if err == nil {
		return strings.ToLower(normalized.Normalized)
	}
	return strings.ToLower(strings.TrimSpace(question))
}

func (classifier *ScopeClassifier) verdict(questionType QuestionType) ScopeVerdict {
	verdict := ScopeVerdict{
		SchemaVersion: ScopeVerdictSchemaVersion,
		Type:          questionType, Outcome: ScopeOutcomeExecute, Reason: ScopeReasonSupported,
		NextActions: []NextAction{}, LexiconVersion: classifier.lexicon.Version,
		LexiconHash:          classifier.lexiconHash,
		ClassificationSource: ClassificationSourceRule,
	}
	action := func(kind NextActionKind, label string, target NextActionTarget) NextAction {
		return NextAction{Kind: kind, Label: label, Payload: NextActionPayload{Target: target, Prefill: NextActionPrefillCurrentQuestion}}
	}
	switch questionType {
	case QuestionTypeDefinition:
		verdict.Outcome, verdict.Reason = ScopeOutcomeDefinition, ScopeReasonDefinition
		verdict.UserMessage = "这是指标口径问题，将直接展示已发布的指标定义，不查询业务数据。"
	case QuestionTypeBundle:
		verdict.Outcome, verdict.Reason = ScopeOutcomeBundle, ScopeReasonBundle
		verdict.UserMessage = "这是宽泛经营问题，将按当前领域的已发布 KPI 组合回答。"
	case QuestionTypeDetailList:
		verdict.Outcome, verdict.Reason = ScopeOutcomeOutOfScope, ScopeReasonDetailList
		verdict.UserMessage = "智能问数仅返回受治理的汇总分析，明细数据请提交取数申请。"
		verdict.NextActions = []NextAction{action(NextActionDataRequest, "发起明细取数申请", NextActionTargetDataRequestDialog)}
	case QuestionTypeForecast:
		verdict.Outcome, verdict.Reason = ScopeOutcomeOutOfScope, ScopeReasonForecast
		verdict.UserMessage = "当前领域尚未发布可用于预测的受治理模型，不能生成预测结果。"
		verdict.NextActions = []NextAction{action(NextActionRephrase, "改问历史表现或变化", NextActionTargetAskDataComposer)}
	case QuestionTypeAdHocFormula:
		verdict.Outcome, verdict.Reason = ScopeOutcomeOutOfScope, ScopeReasonAdHocFormula
		verdict.UserMessage = "临时公式不属于已治理指标，需先提交指标建设需求。"
		verdict.NextActions = []NextAction{action(NextActionMetricRequest, "提交指标建设需求", NextActionTargetMetricRequestForm)}
	case QuestionTypeCausal:
		if classifier.contributionEnabled {
			verdict.Reason = ScopeReasonCausalContribution
			verdict.UserMessage = "可提供贡献度分解，但结果只描述关联与贡献，不证明因果关系。"
		} else {
			verdict.Outcome, verdict.Reason = ScopeOutcomeOutOfScope, ScopeReasonCausalUnsupported
			verdict.UserMessage = "当前领域未启用贡献度分解，无法回答原因或因果问题。"
			verdict.NextActions = []NextAction{action(NextActionRephrase, "改问可观测的变化或对比", NextActionTargetAskDataComposer)}
		}
	case QuestionTypeCrossDomain:
		verdict.Outcome, verdict.Reason = ScopeOutcomeOutOfScope, ScopeReasonCrossDomain
		verdict.UserMessage = "问数固定在登录后选定的业务领域，不能在一次问题中组合其他领域数据。请先切换领域，再分别提问。"
		verdict.NextActions = []NextAction{action(NextActionRephrase, "在当前领域重新表述", NextActionTargetAskDataComposer)}
	case QuestionTypeUngovernedSource:
		verdict.Outcome, verdict.Reason = ScopeOutcomeBlocked, ScopeReasonUngovernedSource
		verdict.UserMessage = "所引用的数据源尚未接入当前领域的语义层，不能用于可信问数。"
		verdict.NextActions = []NextAction{action(NextActionMetricRequest, "提交数据治理需求", NextActionTargetMetricRequestForm)}
	}
	return verdict
}

// WithParsedContext adds only DR-001's normalized bound-object whitelist. It
// is intentionally limited to refusal exits and cannot hold rows by type.
func WithParsedContext(verdict ScopeVerdict, parsed ParsedContext) (ScopeVerdict, error) {
	if verdict.Outcome != ScopeOutcomeOutOfScope {
		return ScopeVerdict{}, fmt.Errorf("%w: context is only valid for out-of-scope exits", ErrInvalidScopeContext)
	}
	normalized, err := parsed.Normalize()
	if err != nil {
		return ScopeVerdict{}, fmt.Errorf("%w: %v", ErrInvalidScopeContext, err)
	}
	if normalized.Empty() {
		verdict.ParsedContext = nil
		return verdict, nil
	}
	verdict.ParsedContext = &normalized
	return verdict, nil
}

func (verdict ScopeVerdict) Validate() error {
	if verdict.SchemaVersion != ScopeVerdictSchemaVersion || !validQuestionType(verdict.Type) ||
		strings.TrimSpace(verdict.LexiconVersion) == "" || verdict.LexiconHash.Validate() != nil || !validScopeOutcome(verdict.Outcome) ||
		!validClassificationSource(verdict.ClassificationSource) {
		return ErrInvalidScopeVerdict
	}
	if !utf8.ValidString(verdict.UserMessage) || verdict.UserMessage != strings.TrimSpace(verdict.UserMessage) ||
		len(verdict.NextActions) > 3 || utf8.RuneCountInString(verdict.UserMessage) > 1_000 ||
		!validVerdictShape(verdict) {
		return ErrInvalidScopeVerdict
	}
	if verdict.Outcome == ScopeOutcomeOutOfScope && len(verdict.NextActions) == 0 {
		return fmt.Errorf("%w: out-of-scope verdict requires a next action", ErrInvalidScopeVerdict)
	}
	for _, action := range verdict.NextActions {
		if err := validateNextAction(action); err != nil {
			return err
		}
	}
	if verdict.ParsedContext != nil {
		if verdict.Outcome != ScopeOutcomeOutOfScope {
			return fmt.Errorf("%w: parsed context outside refusal", ErrInvalidScopeVerdict)
		}
		normalized, err := verdict.ParsedContext.Normalize()
		if err != nil || normalized.Empty() {
			return fmt.Errorf("%w: parsed context", ErrInvalidScopeVerdict)
		}
	}
	return nil
}

func validateNextAction(action NextAction) error {
	validPair := action.Kind == NextActionDataRequest && action.Payload.Target == NextActionTargetDataRequestDialog ||
		action.Kind == NextActionMetricRequest && action.Payload.Target == NextActionTargetMetricRequestForm ||
		action.Kind == NextActionRephrase && action.Payload.Target == NextActionTargetAskDataComposer
	if !validPair || !utf8.ValidString(action.Label) || action.Label != strings.TrimSpace(action.Label) ||
		strings.TrimSpace(action.Label) == "" || utf8.RuneCountInString(action.Label) > 128 ||
		action.Payload.Prefill != NextActionPrefillCurrentQuestion {
		return fmt.Errorf("%w: next action", ErrInvalidScopeVerdict)
	}
	return nil
}

func validVerdictShape(verdict ScopeVerdict) bool {
	switch verdict.Type {
	case QuestionTypeMetricLookup, QuestionTypeGroupedAnalysis, QuestionTypeFilteredAnalysis,
		QuestionTypeRanking, QuestionTypeComparison, QuestionTypeMultiMetric, QuestionTypeRatioTarget:
		return verdict.Outcome == ScopeOutcomeExecute && verdict.Reason == ScopeReasonSupported && len(verdict.NextActions) == 0
	case QuestionTypeDefinition:
		return verdict.Outcome == ScopeOutcomeDefinition && verdict.Reason == ScopeReasonDefinition && len(verdict.NextActions) == 0
	case QuestionTypeBundle:
		return verdict.Outcome == ScopeOutcomeBundle && verdict.Reason == ScopeReasonBundle && len(verdict.NextActions) == 0
	case QuestionTypeDetailList:
		return verdict.Outcome == ScopeOutcomeOutOfScope && verdict.Reason == ScopeReasonDetailList
	case QuestionTypeForecast:
		return verdict.Outcome == ScopeOutcomeOutOfScope && verdict.Reason == ScopeReasonForecast
	case QuestionTypeAdHocFormula:
		return verdict.Outcome == ScopeOutcomeOutOfScope && verdict.Reason == ScopeReasonAdHocFormula
	case QuestionTypeCausal:
		return verdict.Outcome == ScopeOutcomeExecute && verdict.Reason == ScopeReasonCausalContribution && len(verdict.NextActions) == 0 ||
			verdict.Outcome == ScopeOutcomeOutOfScope && verdict.Reason == ScopeReasonCausalUnsupported
	case QuestionTypeCrossDomain:
		return verdict.Outcome == ScopeOutcomeOutOfScope && verdict.Reason == ScopeReasonCrossDomain
	case QuestionTypeUngovernedSource:
		return verdict.Outcome == ScopeOutcomeBlocked && verdict.Reason == ScopeReasonUngovernedSource
	default:
		return false
	}
}

func validScopeOutcome(value ScopeOutcome) bool {
	switch value {
	case ScopeOutcomeExecute, ScopeOutcomeDefinition, ScopeOutcomeBundle, ScopeOutcomeOutOfScope, ScopeOutcomeBlocked:
		return true
	default:
		return false
	}
}

func validClassificationSource(value ClassificationSource) bool {
	switch value {
	case ClassificationSourceRule, ClassificationSourceLLMFallback, ClassificationSourceFallbackRejected:
		return true
	default:
		return false
	}
}

// ScopeEvaluation keeps correct refusals separate from false refusals so an
// honest out-of-scope answer improves, rather than harms, the quality score.
type ScopeEvaluation struct {
	Total           int `json:"total"`
	Correct         int `json:"correct"`
	CorrectRefusals int `json:"correctRefusals"`
	FalseRefusals   int `json:"falseRefusals"`
}

func (evaluation *ScopeEvaluation) Add(expected QuestionType, verdict ScopeVerdict) {
	if evaluation == nil {
		return
	}
	evaluation.Total++
	if verdict.Type == expected {
		evaluation.Correct++
		if verdict.Outcome == ScopeOutcomeOutOfScope {
			evaluation.CorrectRefusals++
		}
		return
	}
	if verdict.Outcome == ScopeOutcomeOutOfScope {
		evaluation.FalseRefusals++
	}
}

func IsCorrectRefusal(expected QuestionType, verdict ScopeVerdict) bool {
	return verdict.Type == expected && verdict.Outcome == ScopeOutcomeOutOfScope
}
