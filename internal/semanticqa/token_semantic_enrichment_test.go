package semanticqa

import "testing"

func TestTokenSemanticSearchTargetsReserveMetricRecallForWholeQuestion(
	t *testing.T,
) {
	tests := []struct {
		name           string
		entityType     string
		wantDimensions bool
	}{
		{name: "metric", entityType: "METRIC", wantDimensions: false},
		{name: "query word", entityType: "QUERY_WORD", wantDimensions: false},
		{name: "analysis word", entityType: "ANALYSIS_WORD", wantDimensions: false},
		{name: "time", entityType: "TIME", wantDimensions: true},
		{name: "dimension value", entityType: "DIMENSION_VALUE", wantDimensions: true},
		{name: "noun candidate", entityType: "NOUN_CANDIDATE", wantDimensions: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics, dimensions := tokenSemanticSearchTargets(QueryToken{
				Text: "测试", EntityType: test.entityType,
			})
			if metrics || dimensions != test.wantDimensions {
				t.Fatalf(
					"unexpected targets for %s: metrics=%v dimensions=%v",
					test.entityType, metrics, dimensions,
				)
			}
		})
	}
}

func TestRankTokenSemanticCorpusReturnsTopThreePerToken(t *testing.T) {
	token := QueryToken{
		Text: "销售额", PartOfSpeech: "n", EntityType: "NOUN_CANDIDATE",
	}
	corpus := []tokenSemanticCorpusItem{
		{
			SemanticType: "METRIC", Name: "订单数量", Code: "order_count",
			SearchText: "订单数量 下单笔数",
		},
		{
			SemanticType: "METRIC", Name: "商品总价", Code: "gross_sales_amount",
			SearchText: "商品总价 原始销售额 销售总额",
		},
		{
			SemanticType: "METRIC", Name: "净收入", Code: "net_revenue",
			SearchText: "净收入 销售收入 收入金额",
		},
		{
			SemanticType: "METRIC", Name: "毛收入", Code: "gross_revenue",
			SearchText: "毛收入 销售收入 收入金额",
		},
	}

	candidates := rankTokenSemanticCorpus(token, corpus, 3)
	if len(candidates) != 3 {
		t.Fatalf("expected exactly three candidates, got %#v", candidates)
	}
	if candidates[0].Name != "商品总价" {
		t.Fatalf("expected semantic-document match first, got %#v", candidates)
	}
	for _, candidate := range candidates {
		if candidate.MatchMethod != "LEXICAL_SEMANTIC_DOCUMENT" {
			t.Fatalf("unexpected match method: %#v", candidate)
		}
	}
}

func TestResolveTokenSemanticLLMOutputMapsOnlyRetrievedRanks(t *testing.T) {
	question := "本月销售额是多少？"
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "本月", Start: 0, End: 2, EntityType: "TIME",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType: "DIMENSION", Name: "统计日期",
					Code: "stat_date", DimensionName: "统计日期",
					DimensionCode: "stat_date",
					DimensionType: "TIME", ValueType: "DATE",
				},
			},
		},
		{
			Token: "销售额", Start: 2, End: 5,
			EntityType: "NOUN_CANDIDATE",
			MetricCandidates: []QueryTokenSemanticCandidate{
				{Name: "商品总价", Code: "gross_sales_amount"},
			},
		},
	}
	valid := tokenSemanticLLMOutput{
		Intent: "METRIC",
		MetricSelections: []tokenSemanticMetricSelection{
			{TokenStart: 2, CandidateRank: 1},
		},
		DimensionSelections: []tokenSemanticDimensionSelection{
			{
				SourceTokenStart: 0, CandidateTokenStart: 0,
				CandidateRank:   1,
				NormalizedValue: "2026-07-01 至 2026-07-31",
				TimeRangeStart:  "2026-07-01",
				TimeRangeEnd:    "2026-08-01",
				Confidence:      0.91,
			},
		},
		Confidence: 0.88,
	}
	resolved, ok := resolveTokenSemanticLLMOutput(
		question, nil, retrievals, valid,
	)
	if !ok || len(resolved.MetricNames) != 1 ||
		resolved.MetricNames[0] != "商品总价" ||
		len(resolved.DimensionValues) != 1 ||
		resolved.DimensionValues[0].DimensionCode != "stat_date" ||
		resolved.DimensionValues[0].Value !=
			"2026-07-01 至 2026-07-31" ||
		resolved.DimensionValues[0].TimeRange == nil ||
		resolved.DimensionValues[0].TimeRange.Start != "2026-07-01" ||
		resolved.DimensionValues[0].TimeRange.EndExclusive != "2026-08-01" {
		t.Fatalf("expected ranks to map to retrieved semantics: %#v", resolved)
	}

	inventedMetric := valid
	inventedMetric.MetricSelections = []tokenSemanticMetricSelection{
		{TokenStart: 2, CandidateRank: 2},
	}
	if _, ok := resolveTokenSemanticLLMOutput(
		question, nil, retrievals, inventedMetric,
	); ok {
		t.Fatal("rank outside retrieved metric candidates must be rejected")
	}

	inventedDimension := valid
	inventedDimension.DimensionSelections = []tokenSemanticDimensionSelection{
		{
			SourceTokenStart: 0, CandidateTokenStart: 2,
			CandidateRank: 1, Confidence: 0.9,
		},
	}
	if _, ok := resolveTokenSemanticLLMOutput(
		question, nil, retrievals, inventedDimension,
	); ok {
		t.Fatal("rank outside retrieved dimension candidates must be rejected")
	}
}

func TestResolveTokenSemanticLLMOutputRejectsNounAsDimensionValue(t *testing.T) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "华东", Start: 0, End: 2, EntityType: "LOCATION",
		},
		{
			Token: "区域", Start: 2, End: 4,
			EntityType: "NOUN_CANDIDATE",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType:  "DIMENSION",
					DimensionName: "配送区域名称",
					DimensionCode: "zone_name",
					Geographic:    true,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent: "METRIC",
		DimensionSelections: []tokenSemanticDimensionSelection{
			{
				SourceTokenStart: 2, CandidateTokenStart: 2,
				CandidateRank: 1, Confidence: 0.8,
			},
		},
		Confidence: 0.8,
	}
	if _, ok := resolveTokenSemanticLLMOutput(
		"华东区域", nil, retrievals, output,
	); ok {
		t.Fatal("generic noun candidate must not be emitted as a dimension value")
	}
	output.DimensionSelections[0].SourceTokenStart = 0
	resolved, ok := resolveTokenSemanticLLMOutput(
		"华东区域", nil, retrievals, output,
	)
	if !ok || len(resolved.DimensionValues) != 1 ||
		resolved.DimensionValues[0].SourceToken != "华东" ||
		resolved.DimensionValues[0].DimensionCode != "zone_name" {
		t.Fatal("location may use a neighboring token's retrieved dimension")
	}
}

func TestResolveTokenSemanticLLMOutputKeepsBestDimensionPerSource(t *testing.T) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "华东", Start: 0, End: 2, EntityType: "LOCATION",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType: "DIMENSION", DimensionName: "城市",
					DimensionCode: "city", Geographic: true, Score: 0.62,
				},
			},
		},
		{
			Token: "区域", Start: 2, End: 4,
			EntityType: "NOUN_CANDIDATE",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType:  "DIMENSION",
					DimensionName: "配送区域名称",
					DimensionCode: "zone_name", Geographic: true,
					Score: 0.79,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent: "METRIC",
		DimensionSelections: []tokenSemanticDimensionSelection{
			{
				SourceTokenStart: 0, CandidateTokenStart: 0,
				CandidateRank: 1, Confidence: 0.62,
			},
			{
				SourceTokenStart: 0, CandidateTokenStart: 2,
				CandidateRank: 1, Confidence: 0.79,
			},
		},
		Confidence: 0.8,
	}
	resolved, ok := resolveTokenSemanticLLMOutput(
		"华东区域", nil, retrievals, output,
	)
	if !ok || len(resolved.DimensionValues) != 1 ||
		resolved.DimensionValues[0].DimensionCode != "zone_name" {
		t.Fatalf("expected the higher-scoring dimension only: %#v", resolved)
	}
}

func TestResolveTokenSemanticLLMOutputKeepsLLMSelectedPublishedSemantic(t *testing.T) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "华东", Start: 0, End: 2, EntityType: "LOCATION",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType: "DIMENSION", DimensionName: "城市",
					DimensionCode: "city", Geographic: true, Score: 0.62,
				},
			},
		},
		{
			Token: "区域", Start: 2, End: 4,
			EntityType: "NOUN_CANDIDATE",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType:  "DIMENSION",
					DimensionName: "配送区域名称",
					DimensionCode: "zone_name", Geographic: true,
					Score: 0.79,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent: "METRIC",
		DimensionSelections: []tokenSemanticDimensionSelection{
			{
				SourceTokenStart: 0, CandidateTokenStart: 0,
				CandidateRank: 1, Confidence: 0.62,
			},
		},
		Confidence: 0.8,
	}
	resolved, ok := resolveTokenSemanticLLMOutput(
		"华东区域", nil, retrievals, output,
	)
	if !ok || len(resolved.DimensionValues) != 1 ||
		resolved.DimensionValues[0].DimensionCode != "city" {
		t.Fatalf("service must not override the selected semantic with code rules: %#v",
			resolved)
	}
}

func TestResolveTokenSemanticLLMOutputRequiresExplicitTime(t *testing.T) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "本月", Start: 0, End: 2, EntityType: "TIME",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType:  "DIMENSION",
					DimensionName: "统计日期",
					DimensionCode: "stat_date", DimensionType: "TIME",
					ValueType: "DATE", Score: 0.72,
				},
			},
		},
	}
	if _, ok := resolveTokenSemanticLLMOutput(
		"本月订单量", nil, retrievals,
		tokenSemanticLLMOutput{Intent: "METRIC", Confidence: 0.8},
	); ok {
		t.Fatal("time token with a retrieved time dimension must be selected")
	}
}

func TestResolveTokenSemanticLLMOutputDoesNotForceTimeToNonTimeDimension(
	t *testing.T,
) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "骑手", Start: 3, End: 5,
			EntityType: "NOUN_CANDIDATE",
			MetricCandidates: []QueryTokenSemanticCandidate{
				{Name: "骑手实体数量", Code: "courier_count"},
			},
		},
		{
			Token: "当前", Start: 5, End: 7, EntityType: "TIME",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType: "DIMENSION", DimensionName: "城市",
					DimensionCode: "city", DimensionType: "STANDARD",
					Geographic: true,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent: "METRIC",
		MetricSelections: []tokenSemanticMetricSelection{
			{TokenStart: 3, CandidateRank: 1},
		},
		Confidence: 0.86,
	}
	resolved, ok := resolveTokenSemanticLLMOutput(
		"北京的骑手当前有多少人？", nil, retrievals, output,
	)
	if !ok || len(resolved.MetricNames) != 1 ||
		resolved.MetricNames[0] != "骑手实体数量" ||
		len(resolved.DimensionValues) != 0 {
		t.Fatalf("non-time candidates must not block completion: %#v", resolved)
	}
}

func TestResolveTokenSemanticLLMOutputRejectsTimeMappedToNonTimeDimension(
	t *testing.T,
) {
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "当前", Start: 0, End: 2, EntityType: "TIME",
			DimensionCandidates: []QueryTokenSemanticCandidate{
				{
					SemanticType: "DIMENSION", DimensionName: "城市",
					DimensionCode: "city", DimensionType: "STANDARD",
					Geographic: true,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent: "METRIC",
		DimensionSelections: []tokenSemanticDimensionSelection{
			{
				SourceTokenStart: 0, CandidateTokenStart: 0,
				CandidateRank: 1, Confidence: 0.8,
			},
		},
		Confidence: 0.8,
	}
	if _, ok := resolveTokenSemanticLLMOutput(
		"当前骑手数", nil, retrievals, output,
	); ok {
		t.Fatal("time source must not select a non-time dimension")
	}
}

func TestCurateVectorTokenSemanticCandidatesCalibratesEntityTypes(
	t *testing.T,
) {
	candidates := []QueryTokenSemanticCandidate{
		{
			SemanticType: "DIMENSION", DimensionName: "城市",
			DimensionCode: "city", FieldID: "field_city",
			DimensionType: "STANDARD", Geographic: true, Score: 0.8,
		},
		{
			SemanticType: "DIMENSION", DimensionName: "城市",
			DimensionCode: "city", FieldID: "field_city",
			DimensionType: "STANDARD", Geographic: true, Score: 0.79,
		},
		{
			SemanticType: "DIMENSION_VALUE", Name: "杭州", Value: "杭州",
			DimensionName: "城市", DimensionCode: "city",
			FieldID: "field_city", DimensionType: "STANDARD",
			Geographic: true, Score: 0.78,
		},
		{
			SemanticType: "DIMENSION_VALUE", Name: "北京", Value: "北京",
			DimensionName: "城市", DimensionCode: "city",
			FieldID: "field_city", DimensionType: "STANDARD",
			Geographic: true, Score: 0.77,
		},
		{
			SemanticType: "DIMENSION", DimensionName: "统计日期",
			DimensionCode: "stat_date", FieldID: "field_stat_date",
			DimensionType: "TIME", Score: 0.7,
		},
	}
	_, locationDimensions := curateVectorTokenSemanticCandidates(
		QueryToken{Text: "北京", EntityType: "LOCATION"}, candidates, 5,
	)
	if len(locationDimensions) != 3 ||
		locationDimensions[0].DimensionCode != "city" ||
		locationDimensions[1].Value != "北京" ||
		locationDimensions[2].DimensionCode != "stat_date" {
		t.Fatalf(
			"candidates must be deduplicated without business-specific ranking: %#v",
			locationDimensions,
		)
	}
	_, timeDimensions := curateVectorTokenSemanticCandidates(
		QueryToken{Text: "当前", EntityType: "TIME"}, candidates, 5,
	)
	if len(timeDimensions) != 1 ||
		timeDimensions[0].DimensionType != "TIME" ||
		timeDimensions[0].SemanticType != "DIMENSION" {
		t.Fatalf("time candidates must contain time definitions only: %#v",
			timeDimensions)
	}
}

func TestCurateVectorTokenSemanticCandidatesKeepsPublishedVectorOrder(
	t *testing.T,
) {
	candidates := []QueryTokenSemanticCandidate{
		{
			SemanticType: "DIMENSION", DimensionName: "常驻配送区域ID",
			DimensionCode: "zone_id", FieldID: "field_zone_id",
			DimensionType: "STANDARD", Geographic: true, Score: 0.9,
		},
		{
			SemanticType: "DIMENSION", DimensionName: "配送区域名称",
			DimensionCode: "zone_name", FieldID: "field_zone_name",
			Description: "配送区域的业务名称", DimensionType: "STANDARD",
			Geographic: true, Score: 0.9,
		},
		{
			SemanticType: "DIMENSION", DimensionName: "配送区域城市",
			DimensionCode: "zone_city", FieldID: "field_zone_city",
			Description: "配送区域所在城市", DimensionType: "STANDARD",
			Geographic: true, Score: 0.9,
		},
		{
			SemanticType: "DIMENSION", DimensionName: "配送区域行政区",
			DimensionCode: "zone_district", FieldID: "field_zone_district",
			Description:   "行政或运营区划，用于区域经营分析",
			DimensionType: "STANDARD", Geographic: true, Score: 0.9,
		},
	}
	_, dimensions := curateVectorTokenSemanticCandidates(
		QueryToken{Text: "区域", EntityType: "NOUN_CANDIDATE"},
		candidates, 5,
	)
	if len(dimensions) != 4 ||
		dimensions[0].DimensionCode != "zone_id" ||
		dimensions[3].DimensionCode != "zone_district" {
		t.Fatalf("code must not infer a business hierarchy outside semantic assets: %#v",
			dimensions)
	}
}

func TestResolveTokenSemanticLLMOutputCollapsesSingleMetricExpression(
	t *testing.T,
) {
	questionMetrics := []QueryTokenSemanticCandidate{
		{Name: "总订单数", Code: "order_count", Score: 0.4453},
		{
			Name: "订单商品数量合计",
			Code: "order_item_quantity", Score: 0.421,
		},
	}
	retrievals := []QueryTokenSemanticRetrieval{
		{
			Token: "订单量", Start: 11, End: 14,
			EntityType: "NOUN_CANDIDATE",
			MetricCandidates: []QueryTokenSemanticCandidate{
				{
					Name: "订单商品数量合计",
					Code: "order_item_quantity", Score: 0.4891,
				},
			},
		},
	}
	output := tokenSemanticLLMOutput{
		Intent:              "METRIC",
		QuestionMetricRanks: []int{1},
		MetricSelections: []tokenSemanticMetricSelection{
			{TokenStart: 11, CandidateRank: 1},
		},
		Confidence: 0.85,
	}
	resolved, ok := resolveTokenSemanticLLMOutput(
		"华东区域最近 30 天订单量", questionMetrics, retrievals, output,
	)
	if !ok || len(resolved.MetricNames) != 1 ||
		resolved.MetricNames[0] != "总订单数" {
		t.Fatalf("single metric wording must resolve once: %#v", resolved)
	}
	multipleOutput := output
	multipleOutput.QuestionMetricRanks = []int{1, 2}
	resolved, ok = resolveTokenSemanticLLMOutput(
		"订单量和商品数量", questionMetrics, retrievals, multipleOutput,
	)
	if !ok || len(resolved.MetricNames) != 2 {
		t.Fatalf("explicit multi-metric wording must remain plural: %#v",
			resolved)
	}
}

func TestRankTokenSemanticCorpusDoesNotTurnSingleCharacterIntoContainsMatch(t *testing.T) {
	token := QueryToken{
		Text: "是", PartOfSpeech: "v", EntityType: "TEXT",
	}
	corpus := []tokenSemanticCorpusItem{
		{
			SemanticType: "DIMENSION", Name: "统计日期",
			Code: "stat_date", SearchText: "是否为统计日期",
		},
	}
	if candidates := rankTokenSemanticCorpus(token, corpus, 3); len(candidates) != 0 {
		t.Fatalf("single-character function word must not produce candidates: %#v", candidates)
	}
}

func TestRankTokenSemanticCorpusTimeBoostsDimensionNotArbitraryMembers(t *testing.T) {
	token := QueryToken{Text: "本月", EntityType: "TIME"}
	corpus := []tokenSemanticCorpusItem{
		{
			SemanticType: "DIMENSION", Name: "统计日期", Code: "stat_date",
			DimensionName: "统计日期", DimensionCode: "stat_date",
			DimensionType: "TIME", SearchText: "统计日期 时间",
		},
		{
			SemanticType: "DIMENSION_VALUE", Name: "2026-06-01",
			Code: "stat_date", DimensionName: "统计日期",
			DimensionCode: "stat_date", DimensionType: "TIME",
			Value: "2026-06-01", SearchText: "统计日期 2026-06-01",
		},
		{
			SemanticType: "DIMENSION", Name: "地理纬度",
			Code: "latitude", DimensionName: "地理纬度",
			DimensionCode: "latitude", DimensionType: "STANDARD",
			SearchText: "地理纬度",
		},
	}
	candidates := rankTokenSemanticCorpus(token, corpus, 3)
	if len(candidates) != 1 ||
		candidates[0].SemanticType != "DIMENSION" ||
		candidates[0].DimensionCode != "stat_date" {
		t.Fatalf("expected only the time dimension definition: %#v", candidates)
	}
}
