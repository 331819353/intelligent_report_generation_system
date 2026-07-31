package semanticqa

import "testing"

func TestTokenizeQueryPrefersSemanticEntitiesAndKeepsRuneOffsets(t *testing.T) {
	question := "最近30天华东区域的总订单数同比增长多少？"
	result := tokenizeQuery(question, []querySemanticMatch{
		{
			Text: "总订单数", EntityType: "METRIC",
			EntityName: "总订单数", EntityCode: "metric_orders",
			Source: "METRIC_CATALOG", Confidence: 1, Priority: 110,
		},
		{
			Text: "华东区域", EntityType: "DIMENSION_VALUE",
			EntityName: "销售区域", EntityCode: "region",
			Source: "EXACT_MEMBER", Confidence: 1, Priority: 105,
		},
	})
	expected := map[string]string{
		"最近30天": "TIME",
		"华东区域":  "DIMENSION_VALUE",
		"总订单数":  "METRIC",
		"同比":    "COMPARISON_WORD",
		"增长":    "ANALYSIS_WORD",
		"多少":    "QUERY_WORD",
	}
	for text, entityType := range expected {
		found := false
		for _, token := range result.Tokens {
			if token.Text != text || token.EntityType != entityType {
				continue
			}
			runes := []rune(question)
			if string(runes[token.Start:token.End]) != token.Text {
				t.Fatalf("offsets for %q do not select original text", token.Text)
			}
			found = true
			break
		}
		if !found {
			t.Errorf("missing token %q with type %s: %#v", text, entityType, result.Tokens)
		}
	}
	if result.EntityCount != len(expected) {
		t.Fatalf("expected %d entities, got %d", len(expected), result.EntityCount)
	}
	if result.DictionaryEntityCount != 2 {
		t.Fatalf("expected 2 dictionary entities, got %d", result.DictionaryEntityCount)
	}
}

func TestTokenizeQueryUsesJiebaAroundSpacedTime(t *testing.T) {
	question := "华东区域最近 30 天订单量"
	result := tokenizeQuery(question, nil)
	foundTime := false
	jiebaTokenCount := 0
	for _, token := range result.Tokens {
		if token.Text == "最近 30 天" && token.EntityType == "TIME" {
			foundTime = true
		}
		if token.Text == "30" {
			t.Fatalf("time value must not leak as a standalone number: %#v", result.Tokens)
		}
		if token.Source == "JIEBA_HMM_POS" {
			jiebaTokenCount++
			if token.PartOfSpeech == "" {
				t.Fatalf("Jieba token %q is missing POS", token.Text)
			}
		}
	}
	if !foundTime {
		t.Fatalf("expected complete spaced time token, got %#v", result.Tokens)
	}
	if jiebaTokenCount < 2 {
		t.Fatalf("expected general Jieba tokens around time, got %#v", result.Tokens)
	}
}

func TestTokenizeQueryExtractsPreviouslyUnseenJiebaEntities(t *testing.T) {
	result := tokenizeQuery("王小明去上海浦东调研新能源门店客流", nil)
	jiebaCount := 0
	candidateCount := 0
	foundPerson := false
	for _, token := range result.Tokens {
		if token.Source != "JIEBA_HMM_POS" {
			continue
		}
		jiebaCount++
		if token.Text == "王小明" && token.EntityType == "PERSON" {
			foundPerson = true
		}
		if token.EntityType != "TEXT" && token.EntityType != "PUNCTUATION" {
			candidateCount++
		}
	}
	if jiebaCount < 4 || candidateCount == 0 || !foundPerson {
		t.Fatalf("expected unseen wording to be segmented and tagged: %#v", result.Tokens)
	}
}

func TestCoalesceJiebaEntityWordsRepairsGenericBoundaries(t *testing.T) {
	words := coalesceJiebaEntityWords([]jiebaWord{
		{Text: "深圳", PartOfSpeech: "ns"},
		{Text: "南", PartOfSpeech: "ns"},
		{Text: "山区", PartOfSpeech: "ns"},
		{Text: "客", PartOfSpeech: "n"},
		{Text: "单价", PartOfSpeech: "n"},
		{Text: "订单", PartOfSpeech: "n"},
		{Text: "量", PartOfSpeech: "n"},
		{Text: "，", PartOfSpeech: "x"},
		{Text: "销售", PartOfSpeech: "vn"},
		{Text: "额", PartOfSpeech: "n"},
	})
	if len(words) != 5 ||
		words[0].Text != "深圳南山区" ||
		words[1].Text != "客单价" ||
		words[2].Text != "订单量" ||
		words[4].Text != "销售额" {
		t.Fatalf("unexpected coalesced words: %#v", words)
	}
}

func TestTokenizeQueryKeepsVerbalNounMetricPhraseTogether(t *testing.T) {
	result := tokenizeQuery("本月销售额是多少？", nil)
	found := false
	for _, token := range result.Tokens {
		if token.Text == "销售额" &&
			token.EntityType == "NOUN_CANDIDATE" &&
			token.Source == "JIEBA_HMM_POS" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 销售额 noun candidate, got %#v", result.Tokens)
	}
}

func TestNormalizedRuneSubstringSpansIgnoreWhitespace(t *testing.T) {
	question := "查看 SKU实体数量"
	spans := normalizedRuneSubstringSpans(question, "SKU 实体数量")
	if len(spans) != 1 {
		t.Fatalf("expected one normalized match, got %#v", spans)
	}
	runes := []rune(question)
	if text := string(runes[spans[0][0]:spans[0][1]]); text != "SKU实体数量" {
		t.Fatalf("unexpected original span %q", text)
	}
}

func TestDistinctiveMetricStemMatchesOnlyUniquePublishedMetric(t *testing.T) {
	candidates := []recallCandidate{
		{
			SubjectType: "METRIC", Code: "complaint_count",
			Label: "投诉数量",
		},
		{
			SubjectType: "METRIC", Code: "order_count",
			Label: "总订单数",
		},
		{
			SubjectType: "METRIC", Code: "returned_order_count",
			Label: "退货订单数量",
		},
	}
	matches := distinctiveMetricStemMatches(
		"投诉总量和订单总量", candidates, testSemanticParsingRules(),
	)
	if len(matches) != 1 ||
		matches[0].EntityCode != "complaint_count" ||
		matches[0].Text != "投诉" {
		t.Fatalf("only the unique complaint stem should match: %#v", matches)
	}
}

func TestTokenizeQueryCoalescesAdministrativeLocationSuffix(t *testing.T) {
	result := tokenizeQueryWithRules(
		"帮我查询北京市的投诉总量是什么？",
		[]querySemanticMatch{
			{
				Text: "投诉", EntityType: "METRIC",
				EntityName: "投诉数量", EntityCode: "complaint_count",
				Source:     "METRIC_DISTINCTIVE_STEM",
				Confidence: 0.98, Priority: 113,
			},
		}, testSemanticParsingRules(),
	)
	for _, token := range result.Tokens {
		if token.Text == "北京市" &&
			token.EntityType == "LOCATION" &&
			token.EntityCode == "city" &&
			token.Normalized == "北京" &&
			token.Source == "SEMANTIC_PARSING_RULE" {
			return
		}
	}
	t.Fatalf("expected 北京 + 市 to be one location token: %#v", result.Tokens)
}
