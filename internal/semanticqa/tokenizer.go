package semanticqa

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const queryTokenizationStrategy = "JIEBA_HMM_POS_SEMANTIC_CATALOG_V1"

type querySemanticMatch struct {
	Text       string
	EntityType string
	EntityName string
	EntityCode string
	Source     string
	Confidence float64
	Priority   int
}

type queryTokenSpan struct {
	start      int
	end        int
	text       string
	entityType string
	entityName string
	entityCode string
	source     string
	confidence float64
	priority   int
}

var queryTokenRules = []struct {
	pattern    *regexp.Regexp
	entityType string
	entityName string
	entityCode string
	priority   int
	confidence float64
}{
	{
		pattern: regexp.MustCompile(
			`(?:[0-9]{4}年(?:1[0-2]|0?[1-9])月(?:3[01]|[12][0-9]|0?[1-9])日|` +
				`[0-9]{4}[-/.](?:1[0-2]|0?[1-9])[-/.](?:3[01]|[12][0-9]|0?[1-9])|` +
				`[0-9]{4}年(?:1[0-2]|0?[1-9])月|` +
				`今天|今日|昨日|昨天|前天|本周|上周|本月|上月|本季度|上季度|今年|去年|` +
				`(?:最近|近)\s*[0-9一二三四五六七八九十百两]+\s*(?:天|周|个月|月|年))`,
		),
		entityType: "TIME", entityName: "时间范围",
		entityCode: "RULE_TIME", priority: 88, confidence: 0.96,
	},
	{
		pattern:    regexp.MustCompile(`(?i)top\s*[0-9]+`),
		entityType: "ANALYSIS_WORD", entityName: "排名范围",
		entityCode: "RULE_TOP_N", priority: 82, confidence: 0.96,
	},
	{
		pattern: regexp.MustCompile(
			`[0-9]+(?:\.[0-9]+)?(?:%|个|条|人|家|元|万元|亿元|万|亿|天|周|月|年|次|名)?`,
		),
		entityType: "NUMBER", entityName: "数值",
		entityCode: "RULE_NUMBER", priority: 52, confidence: 0.94,
	},
}

var queryTokenKeywords = []querySemanticMatch{
	{Text: "同比", EntityType: "COMPARISON_WORD", EntityName: "同比", EntityCode: "RULE_YOY", Source: "RULE", Confidence: 0.98, Priority: 78},
	{Text: "环比", EntityType: "COMPARISON_WORD", EntityName: "环比", EntityCode: "RULE_MOM", Source: "RULE", Confidence: 0.98, Priority: 78},
	{Text: "对比", EntityType: "COMPARISON_WORD", EntityName: "对比", EntityCode: "RULE_COMPARE", Source: "RULE", Confidence: 0.92, Priority: 72},
	{Text: "比较", EntityType: "COMPARISON_WORD", EntityName: "比较", EntityCode: "RULE_COMPARE", Source: "RULE", Confidence: 0.92, Priority: 72},
	{Text: "趋势", EntityType: "ANALYSIS_WORD", EntityName: "趋势分析", EntityCode: "RULE_TREND", Source: "RULE", Confidence: 0.96, Priority: 74},
	{Text: "排名", EntityType: "ANALYSIS_WORD", EntityName: "排名分析", EntityCode: "RULE_RANKING", Source: "RULE", Confidence: 0.96, Priority: 74},
	{Text: "排行", EntityType: "ANALYSIS_WORD", EntityName: "排名分析", EntityCode: "RULE_RANKING", Source: "RULE", Confidence: 0.94, Priority: 74},
	{Text: "最高", EntityType: "ANALYSIS_WORD", EntityName: "最大值分析", EntityCode: "RULE_MAX", Source: "RULE", Confidence: 0.88, Priority: 68},
	{Text: "最低", EntityType: "ANALYSIS_WORD", EntityName: "最小值分析", EntityCode: "RULE_MIN", Source: "RULE", Confidence: 0.88, Priority: 68},
	{Text: "增长", EntityType: "ANALYSIS_WORD", EntityName: "增长分析", EntityCode: "RULE_GROWTH", Source: "RULE", Confidence: 0.9, Priority: 68},
	{Text: "下降", EntityType: "ANALYSIS_WORD", EntityName: "下降分析", EntityCode: "RULE_DECLINE", Source: "RULE", Confidence: 0.9, Priority: 68},
	{Text: "多少", EntityType: "QUERY_WORD", EntityName: "数量询问", EntityCode: "RULE_HOW_MANY", Source: "RULE", Confidence: 0.96, Priority: 64},
	{Text: "几个", EntityType: "QUERY_WORD", EntityName: "数量询问", EntityCode: "RULE_HOW_MANY", Source: "RULE", Confidence: 0.94, Priority: 64},
	{Text: "查询", EntityType: "QUERY_WORD", EntityName: "查询动作", EntityCode: "RULE_QUERY", Source: "RULE", Confidence: 0.92, Priority: 62},
	{Text: "统计", EntityType: "QUERY_WORD", EntityName: "统计动作", EntityCode: "RULE_AGGREGATE", Source: "RULE", Confidence: 0.92, Priority: 62},
	{Text: "列出", EntityType: "QUERY_WORD", EntityName: "列举动作", EntityCode: "RULE_LIST", Source: "RULE", Confidence: 0.92, Priority: 62},
}

type QueryTokenizer interface {
	Tokenize(
		context.Context, string, string, string, string,
	) (QueryTokenization, error)
}

type QueryTurnTokenizer interface {
	TokenizeQueryTurn(
		context.Context, string, string, string, string, bool,
	) (QueryTokenization, error)
}

// Tokenize combines general-purpose Jieba/HMM segmentation and POS tagging
// with governed semantic-catalog labels. The catalog only annotates exact
// published names and aliases; it does not determine how all remaining Chinese
// text is split. This keeps previously unseen wording observable without
// inventing example-specific business mappings.
func (interpreter *SemanticInterpreter) Tokenize(
	ctx context.Context,
	tenantID, actorID string,
	question, timezone string,
) (QueryTokenization, error) {
	return interpreter.tokenize(
		ctx, tenantID, actorID, question, timezone, false,
	)
}

func (interpreter *SemanticInterpreter) TokenizeQueryTurn(
	ctx context.Context,
	tenantID, actorID string,
	question, timezone string,
	trustedMetricAnchorAvailable bool,
) (QueryTokenization, error) {
	return interpreter.tokenize(
		ctx, tenantID, actorID, question, timezone,
		trustedMetricAnchorAvailable,
	)
}

func (interpreter *SemanticInterpreter) tokenize(
	ctx context.Context,
	tenantID, actorID string,
	question, timezone string,
	allowTrustedMetricAnchorCompletion bool,
) (QueryTokenization, error) {
	question = strings.TrimSpace(question)
	if interpreter == nil || interpreter.store == nil || question == "" {
		return QueryTokenization{}, ErrInvalidRequest
	}
	parsingRules, err := interpreter.store.semanticParsingRules(ctx, tenantID)
	if err != nil {
		return QueryTokenization{}, err
	}
	candidates, err := interpreter.store.tokenizationMetricCandidates(
		ctx, tenantID, 512,
	)
	if err != nil {
		return QueryTokenization{}, err
	}
	matches := []querySemanticMatch{}
	matchedMetricCodes := []string{}
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" {
			continue
		}
		for _, term := range metricMatchTerms(candidate) {
			if !containsNormalizedFold(question, term.Text) {
				continue
			}
			matches = append(matches, querySemanticMatch{
				Text: term.Text, EntityType: "METRIC",
				EntityName: candidate.Label, EntityCode: candidate.Code,
				Source: term.Source, Confidence: term.Confidence,
				Priority: term.Priority,
			})
			matchedMetricCodes = appendUniqueString(
				matchedMetricCodes, candidate.Code,
			)
		}
	}
	for _, match := range distinctiveMetricStemMatches(
		question, candidates, parsingRules,
	) {
		matches = append(matches, match)
		matchedMetricCodes = appendUniqueString(
			matchedMetricCodes, match.EntityCode,
		)
	}
	for _, metricCode := range matchedMetricCodes {
		lookups, lookupErr := interpreter.store.PreviewMetricDimensionLookups(
			ctx, tenantID, metricCode, question,
		)
		if lookupErr != nil {
			continue
		}
		for _, lookup := range lookups {
			if term := strings.TrimSpace(lookup.Term); term != "" &&
				containsNormalizedFold(question, term) {
				matches = append(matches, querySemanticMatch{
					Text: term, EntityType: "DIMENSION_VALUE",
					EntityName: lookup.DimensionName,
					EntityCode: lookup.DimensionCode,
					Source:     lookup.MatchMethod, Confidence: 1,
					Priority: 105,
				})
			}
			for _, dimensionTerm := range []string{
				lookup.DimensionName,
				lookup.DimensionFieldName,
				lookup.DimensionCode,
			} {
				dimensionTerm = strings.TrimSpace(dimensionTerm)
				if dimensionTerm == "" ||
					!containsNormalizedFold(question, dimensionTerm) {
					continue
				}
				matches = append(matches, querySemanticMatch{
					Text: dimensionTerm, EntityType: "DIMENSION",
					EntityName: lookup.DimensionName,
					EntityCode: lookup.DimensionCode,
					Source:     "GOVERNED_DIMENSION",
					Confidence: 1, Priority: 100,
				})
			}
		}
	}
	result := tokenizeQueryWithRules(question, matches, parsingRules)
	return interpreter.enrichTokenSemantics(
		ctx, tenantID, actorID, question, timezone, result,
		allowTrustedMetricAnchorCompletion, parsingRules,
	), nil
}

func distinctiveMetricStemMatches(
	question string,
	candidates []recallCandidate,
	rules semanticParsingRules,
) []querySemanticMatch {
	type owner struct {
		code, label string
		count       int
	}
	owners := map[string]owner{}
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" ||
			strings.TrimSpace(candidate.Code) == "" {
			continue
		}
		baseTerms := map[string]bool{}
		for _, term := range append(
			[]string{candidate.Label, candidate.Code},
			candidate.Aliases...,
		) {
			baseTerms[strings.ToLower(strings.TrimSpace(term))] = true
		}
		seenForMetric := map[string]bool{}
		for _, term := range rules.metricTerms(candidate) {
			normalized := strings.ToLower(strings.TrimSpace(term))
			if normalized == "" || baseTerms[normalized] ||
				seenForMetric[normalized] {
				continue
			}
			seenForMetric[normalized] = true
			item := owners[normalized]
			if item.count == 0 {
				item.code, item.label = candidate.Code, candidate.Label
			}
			if !strings.EqualFold(item.code, candidate.Code) {
				item.count++
			} else if item.count == 0 {
				item.count = 1
			}
			owners[normalized] = item
		}
	}
	result := []querySemanticMatch{}
	for term, item := range owners {
		if item.count != 1 || !containsNormalizedFold(question, term) {
			continue
		}
		result = append(result, querySemanticMatch{
			Text: term, EntityType: "METRIC",
			EntityName: item.label, EntityCode: item.code,
			Source: "METRIC_DISTINCTIVE_STEM", Confidence: 0.98,
			Priority: 113,
		})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if len([]rune(result[left].Text)) != len([]rune(result[right].Text)) {
			return len([]rune(result[left].Text)) >
				len([]rune(result[right].Text))
		}
		return result[left].Text < result[right].Text
	})
	return result
}

type metricMatchTerm struct {
	Text       string
	Source     string
	Confidence float64
	Priority   int
}

func metricMatchTerms(candidate recallCandidate) []metricMatchTerm {
	result := []metricMatchTerm{}
	appendTerm := func(text, source string, confidence float64, priority int) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, item := range result {
			if strings.EqualFold(item.Text, text) {
				return
			}
		}
		result = append(result, metricMatchTerm{
			Text: text, Source: source, Confidence: confidence,
			Priority: priority,
		})
	}
	appendTerm(candidate.Label, "METRIC_CATALOG", 1, 115)
	appendTerm(candidate.Code, "METRIC_CATALOG", 1, 115)
	for _, alias := range candidate.Aliases {
		appendTerm(alias, "SEMANTIC_ASSET_ALIAS", 1, 114)
	}
	return result
}

func (store *PostgresStore) tokenizationMetricCandidates(
	ctx context.Context,
	tenantID string,
	limit int,
) (items []recallCandidate, err error) {
	items = []recallCandidate{}
	if store == nil || limit < 1 {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT 'METRIC'::text,
				platform.dataset_version_effective_domain(dataset_version.id),
				metric.code::text,''::text,metric.name,
				COALESCE(semantic_alias.aliases,'{}'::text[]),
				''::text,1.0::float8
			FROM platform.metric_versions AS metric_version
			JOIN platform.metrics AS metric
			  ON metric.id=metric_version.metric_id
			 AND metric.current_published_version_id=metric_version.id
			 AND metric.status='PUBLISHED'
			 AND metric.deleted_at IS NULL
			JOIN platform.dataset_versions AS dataset_version
			  ON dataset_version.id=metric_version.dataset_version_id
			 AND dataset_version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.id=dataset_version.dataset_id
			 AND dataset.current_published_version_id=dataset_version.id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			LEFT JOIN LATERAL (
			  SELECT array_agg(asset.common_term::text ORDER BY
			    char_length(asset.common_term::text) DESC,
			    lower(asset.common_term::text)
			  ) AS aliases
			  FROM platform.semantic_term_assets AS asset
			  WHERE asset.status='ACTIVE'
			    AND lower(asset.knowledge_type) IN (
			      'metric','metric_alias','指标','指标别名'
			    )
			    AND lower(asset.mapping_value) IN (
			      lower(metric.code::text),lower(metric.name)
			    )
			) AS semantic_alias ON true
			WHERE metric_version.status='PUBLISHED'
			ORDER BY metric.name,metric.code
			LIMIT $1`, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item recallCandidate
			if scanErr := rows.Scan(
				&item.SubjectType, &item.Domain, &item.Code,
				&item.DimensionCode, &item.Label, &item.Aliases,
				&item.MemberValue, &item.Score,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func tokenizeQuery(
	question string,
	semanticMatches []querySemanticMatch,
) QueryTokenization {
	return tokenizeQueryWithRules(
		question, semanticMatches, semanticParsingRules{
			queryResidualTerms: map[string]bool{},
		},
	)
}

func tokenizeQueryWithRules(
	question string,
	semanticMatches []querySemanticMatch,
	rules semanticParsingRules,
) QueryTokenization {
	result := QueryTokenization{
		QuestionHash: hashText(question),
		Strategy:     queryTokenizationStrategy,
		Tokens:       []QueryToken{},
	}
	spans := []queryTokenSpan{}
	for _, match := range append(
		append([]querySemanticMatch(nil), semanticMatches...),
		queryTokenKeywords...,
	) {
		source := match.Source
		if source == "" {
			source = "SEMANTIC_DICTIONARY"
		}
		for _, position := range normalizedRuneSubstringSpans(
			question, match.Text,
		) {
			spans = append(spans, queryTokenSpan{
				start: position[0], end: position[1],
				text: match.Text, entityType: match.EntityType,
				entityName: match.EntityName, entityCode: match.EntityCode,
				source: source, confidence: match.Confidence,
				priority: match.Priority,
			})
		}
	}
	for _, rule := range queryTokenRules {
		for _, position := range rule.pattern.FindAllStringIndex(question, -1) {
			start := utf8.RuneCountInString(question[:position[0]])
			end := utf8.RuneCountInString(question[:position[1]])
			spans = append(spans, queryTokenSpan{
				start: start, end: end, text: question[position[0]:position[1]],
				entityType: rule.entityType, entityName: rule.entityName,
				entityCode: rule.entityCode, source: "RULE",
				confidence: rule.confidence, priority: rule.priority,
			})
		}
	}
	accepted := selectNonOverlappingTokenSpans(spans)
	questionRunes := []rune(question)
	cursor := 0
	for _, span := range accepted {
		if cursor < span.start {
			result.Tokens = appendJiebaQueryTokens(
				result.Tokens, questionRunes, cursor, span.start,
			)
		}
		text := string(questionRunes[span.start:span.end])
		result.Tokens = append(result.Tokens, QueryToken{
			Text: text, Normalized: strings.ToLower(strings.TrimSpace(text)),
			EntityType: span.entityType, EntityName: span.entityName,
			EntityCode: span.entityCode, Start: span.start, End: span.end,
			Source: span.source, Confidence: span.confidence,
		})
		cursor = span.end
	}
	if cursor < len(questionRunes) {
		result.Tokens = appendJiebaQueryTokens(
			result.Tokens, questionRunes, cursor, len(questionRunes),
		)
	}
	result.Tokens = coalesceAdministrativeLocationTokens(result.Tokens, rules)
	for index := range result.Tokens {
		value, name, code, found := rules.administrativeLocation(
			result.Tokens[index].Text,
		)
		if !found {
			continue
		}
		result.Tokens[index].Normalized = value
		result.Tokens[index].EntityType = "LOCATION"
		result.Tokens[index].EntityName = name
		result.Tokens[index].EntityCode = code
		result.Tokens[index].Confidence = max(
			result.Tokens[index].Confidence, 0.9,
		)
		result.Tokens[index].Source = "SEMANTIC_PARSING_RULE"
	}
	for _, token := range result.Tokens {
		if token.EntityType == "TEXT" || token.EntityType == "PUNCTUATION" {
			continue
		}
		result.EntityCount++
		if isDictionaryTokenSource(token.Source) {
			result.DictionaryEntityCount++
		}
	}
	return result
}

func coalesceAdministrativeLocationTokens(
	tokens []QueryToken,
	rules semanticParsingRules,
) []QueryToken {
	working := append([]QueryToken(nil), tokens...)
	result := make([]QueryToken, 0, len(working))
	for index := 0; index < len(working); index++ {
		current := working[index]
		if index+1 < len(tokens) &&
			oneOf(current.EntityType, "LOCATION", "PROPER_NOUN",
				"NOUN_CANDIDATE") {
			next := working[index+1]
			suffix := rules.administrativeSuffixAtStart(next.Text)
			combined := strings.TrimSpace(current.Text + suffix)
			if suffix != "" {
				if value, name, code, found :=
					rules.administrativeLocation(combined); found {
					current.Text = combined
					current.Normalized = value
					current.End = next.Start + len([]rune(suffix))
					current.EntityType = "LOCATION"
					current.EntityName = name
					current.EntityCode = code
					current.Confidence = max(current.Confidence, 0.9)
					current.Source = "SEMANTIC_PARSING_RULE"
					remainder := strings.TrimPrefix(next.Text, suffix)
					if remainder == "" {
						index++
					} else {
						entityType, entityName, entityCode, confidence :=
							jiebaEntityMetadata(remainder, next.PartOfSpeech)
						next.Text = remainder
						next.Normalized = strings.ToLower(remainder)
						next.Start = current.End
						next.EntityType = entityType
						next.EntityName = entityName
						next.EntityCode = entityCode
						next.Confidence = confidence
						working[index+1] = next
					}
				}
			}
		}
		result = append(result, current)
	}
	return result
}

func isDictionaryTokenSource(source string) bool {
	return source != "" &&
		source != "RULE" &&
		source != "JIEBA_HMM_POS" &&
		source != "FALLBACK"
}

func selectNonOverlappingTokenSpans(
	spans []queryTokenSpan,
) []queryTokenSpan {
	sort.SliceStable(spans, func(left, right int) bool {
		if spans[left].priority != spans[right].priority {
			return spans[left].priority > spans[right].priority
		}
		leftLength := spans[left].end - spans[left].start
		rightLength := spans[right].end - spans[right].start
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		return spans[left].start < spans[right].start
	})
	accepted := []queryTokenSpan{}
	for _, candidate := range spans {
		if candidate.start < 0 || candidate.end <= candidate.start {
			continue
		}
		overlaps := false
		for _, selected := range accepted {
			if candidate.start < selected.end && candidate.end > selected.start {
				overlaps = true
				break
			}
		}
		if !overlaps {
			accepted = append(accepted, candidate)
		}
	}
	sort.SliceStable(accepted, func(left, right int) bool {
		return accepted[left].start < accepted[right].start
	})
	return accepted
}

func normalizedRuneSubstringSpans(question, term string) [][2]int {
	questionRunes := []rune(question)
	normalizedQuestion := []rune{}
	originalIndexes := []int{}
	for index, value := range questionRunes {
		if unicode.IsSpace(value) {
			continue
		}
		normalizedQuestion = append(normalizedQuestion, unicode.ToLower(value))
		originalIndexes = append(originalIndexes, index)
	}
	normalizedTerm := []rune{}
	for _, value := range []rune(strings.TrimSpace(term)) {
		if !unicode.IsSpace(value) {
			normalizedTerm = append(normalizedTerm, unicode.ToLower(value))
		}
	}
	if len(normalizedTerm) == 0 ||
		len(normalizedTerm) > len(normalizedQuestion) {
		return nil
	}
	result := [][2]int{}
	for index := 0; index+len(normalizedTerm) <= len(normalizedQuestion); index++ {
		matches := true
		for offset := range normalizedTerm {
			if normalizedQuestion[index+offset] != normalizedTerm[offset] {
				matches = false
				break
			}
		}
		if matches {
			result = append(result, [2]int{
				originalIndexes[index],
				originalIndexes[index+len(normalizedTerm)-1] + 1,
			})
		}
	}
	return result
}

func containsNormalizedFold(value, part string) bool {
	return len(normalizedRuneSubstringSpans(value, part)) > 0
}
