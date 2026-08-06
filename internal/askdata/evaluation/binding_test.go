package evaluation

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/testfixture"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

func TestEvaluateBindingsComputesMetricsSlicesAndCalibrationInputs(t *testing.T) {
	cases := standardBindingEvaluationCases(t)
	report, err := EvaluateBindings(cases)
	if err != nil {
		t.Fatalf("EvaluateBindings() error = %v", err)
	}
	if err := report.ValidateAgainst(cases); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
	if report.CaseCount != 4 || report.Overall.CaseCount != 4 {
		t.Fatalf("case counts = %d/%d, want 4/4", report.CaseCount, report.Overall.CaseCount)
	}

	assertScore(t, "mention.metric", report.Overall.Mention.Metric, 3, 0, 1, 1, 0.75, 6.0/7.0)
	assertScore(t, "mention.dimension", report.Overall.Mention.Dimension, 1, 1, 1, 0.5, 0.5, 0.5)
	assertScore(t, "mention.member", report.Overall.Mention.Member, 2, 0, 0, 1, 1, 1)
	assertScore(t, "mention.overall", report.Overall.Mention.Overall, 6, 1, 2, 6.0/7.0, 0.75, 0.8)

	assertScore(t, "binding.metric", report.Overall.Binding.Metric, 3, 0, 1, 1, 0.75, 6.0/7.0)
	assertScore(t, "binding.dimension", report.Overall.Binding.Dimension, 1, 1, 1, 0.5, 0.5, 0.5)
	assertScore(t, "binding.member", report.Overall.Binding.Member, 1, 1, 1, 0.5, 0.5, 0.5)
	assertScore(t, "binding.overall", report.Overall.Binding.Overall, 5, 2, 3, 5.0/7.0, 0.625, 2.0/3.0)

	if got := domainIDs(report.ByDomain); !reflect.DeepEqual(got, []askdata.ID{"finance", "sales"}) {
		t.Fatalf("domain slices = %v", got)
	}
	if got := complexities(report.ByComplexity); !reflect.DeepEqual(got, []ComplexityClass{ComplexityComposite, ComplexitySimple}) {
		t.Fatalf("complexity slices = %v", got)
	}
	if got := ambiguities(report.ByAmbiguity); !reflect.DeepEqual(got, []AmbiguityClass{AmbiguityCrossDomain, AmbiguityDimension, AmbiguityMember, AmbiguityNone}) {
		t.Fatalf("ambiguity slices = %v", got)
	}

	if len(report.Calibration.Training) != 3 || len(report.Calibration.Validation) != 2 {
		t.Fatalf("calibration sizes = %d/%d, want 3/2", len(report.Calibration.Training), len(report.Calibration.Validation))
	}
	for _, example := range report.Calibration.Training {
		if example.CaseID != "case-direct" || !example.Correct {
			t.Fatalf("unexpected training example: %+v", example)
		}
	}
	correctValidation := 0
	incorrectValidation := 0
	for _, example := range report.Calibration.Validation {
		if example.CaseID != "case-member-ambiguous" {
			t.Fatalf("sealed or production case leaked into validation: %+v", example)
		}
		if example.Correct {
			correctValidation++
		} else {
			incorrectValidation++
		}
	}
	if correctValidation != 1 || incorrectValidation != 1 {
		t.Fatalf("validation labels = correct %d/incorrect %d", correctValidation, incorrectValidation)
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"今年华东区按月的销售额", "华东的销售额", "财务入账销售额", "按地区看订单数"} {
		if strings.Contains(string(raw), question) {
			t.Fatalf("report leaked raw question %q", question)
		}
	}
}

func TestEvaluateBindingsIsStableAcrossCaseAndBindingOrder(t *testing.T) {
	cases := standardBindingEvaluationCases(t)
	first, err := EvaluateBindings(cases)
	if err != nil {
		t.Fatal(err)
	}
	reordered := []BindingEvaluationCase{
		cloneBindingCase(t, cases[2]), cloneBindingCase(t, cases[0]),
		cloneBindingCase(t, cases[3]), cloneBindingCase(t, cases[1]),
	}
	reverseBindings(reordered[1].GoldBindings)
	reversePredictions(reordered[1].PredictedBindings)
	second, err := EvaluateBindings(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("reports differ across input order:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestDimensionRoleIsBindingSemanticsNotMentionIdentity(t *testing.T) {
	roleGroup := understanding.DimensionRoleGroupBy
	roleFilter := understanding.DimensionRoleFilter
	question := "按地区看销售额"
	gold := emptyUnderstanding(question)
	gold.MetricMentions = []understanding.MetricMention{{Text: "销售额", Span: understanding.Span{Start: 4, End: 7}, AggregationHint: understanding.AggregationDefault}}
	gold.DimensionMentions = []understanding.DimensionMention{{Text: "地区", Span: understanding.Span{Start: 1, End: 3}, Role: roleGroup}}
	predicted := gold
	predicted.DimensionMentions = append([]understanding.DimensionMention(nil), gold.DimensionMentions...)
	predicted.DimensionMentions[0].Role = roleFilter
	caseValue := BindingEvaluationCase{
		SchemaVersion: BindingEvaluationVersion, CaseID: "case-role", Split: DatasetSplitValidation,
		DomainID: "sales", Complexity: ComplexitySimple, Ambiguity: AmbiguityDimension,
		GoldUnderstanding: gold, PredictedUnderstanding: predicted,
		GoldBindings: []Binding{
			metricBinding(0, "sales-net-amount@v1"),
			dimensionBinding(0, "sales-region@v1", roleGroup),
		},
		PredictedBindings: []BindingPrediction{
			prediction(metricBinding(0, "sales-net-amount@v1"), 0.9, 0.3, 1),
			prediction(dimensionBinding(0, "sales-region@v1", roleFilter), 0.7, 0.05, 1),
		},
	}
	report, err := EvaluateBindings([]BindingEvaluationCase{caseValue})
	if err != nil {
		t.Fatal(err)
	}
	assertScore(t, "dimension mention", report.Overall.Mention.Dimension, 1, 0, 0, 1, 1, 1)
	assertScore(t, "dimension binding", report.Overall.Binding.Dimension, 0, 1, 1, 0, 0, 0)
	if report.Calibration.Validation[0].MentionKind != MentionDimension || report.Calibration.Validation[0].Correct {
		t.Fatalf("wrong role should produce a negative calibration label: %+v", report.Calibration.Validation[0])
	}
}

func TestBindingEvaluationRejectsInvalidInputs(t *testing.T) {
	base := standardBindingEvaluationCases(t)[3]
	tests := []struct {
		name  string
		cases func() []BindingEvaluationCase
		is    error
	}{
		{name: "nil cases", cases: func() []BindingEvaluationCase { return nil }, is: ErrInvalidBindingEvaluation},
		{name: "duplicate case", cases: func() []BindingEvaluationCase {
			return []BindingEvaluationCase{cloneBindingCase(t, base), cloneBindingCase(t, base)}
		}, is: ErrDuplicateEvaluationCase},
		{name: "unknown split", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.Split = "DEV"
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "question mismatch", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.PredictedUnderstanding = emptyUnderstanding("另一个问题")
			value.PredictedBindings = nil
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "duplicate mention span", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.GoldUnderstanding.MetricMentions = append(value.GoldUnderstanding.MetricMentions, value.GoldUnderstanding.MetricMentions[0])
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "binding index", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.GoldBindings[0].MentionIndex = 99
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "duplicate binding", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.GoldBindings = append(value.GoldBindings, value.GoldBindings[0])
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "metric role", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			role := understanding.DimensionRoleGroupBy
			value.GoldBindings[0].Role = &role
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "dimension role mismatch", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			role := understanding.DimensionRoleFilter
			value.GoldBindings[1].Role = &role
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "member parent missing", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.GoldBindings[2].ParentDimensionVersionID = nil
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "nan feature", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.PredictedBindings[0].Features.VectorScore = math.NaN()
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
		{name: "zero retrieval rank", cases: func() []BindingEvaluationCase {
			value := cloneBindingCase(t, base)
			value.PredictedBindings[0].Features.RetrievalRank = 0
			return []BindingEvaluationCase{value}
		}, is: ErrInvalidBindingEvaluation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := EvaluateBindings(test.cases())
			if !errors.Is(err, test.is) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.is)
			}
		})
	}
}

func TestBindingEvaluationReportDetectsTamperingAndReplayMismatch(t *testing.T) {
	cases := standardBindingEvaluationCases(t)
	report, err := EvaluateBindings(cases)
	if err != nil {
		t.Fatal(err)
	}
	tampered := cloneBindingReport(t, report)
	tampered.Overall.Mention.Metric.TruePositive++
	if err := tampered.Validate(); !errors.Is(err, ErrInvalidBindingEvaluation) {
		t.Fatalf("Validate() error = %v", err)
	}

	selfConsistent := cloneBindingReport(t, report)
	selfConsistent.Calibration.Validation[0].Correct = !selfConsistent.Calibration.Validation[0].Correct
	payload, err := bindingReportPayload(selfConsistent)
	if err != nil {
		t.Fatal(err)
	}
	selfConsistent.ContentHash = askdata.HashBytes(payload)
	if err := selfConsistent.Validate(); err != nil {
		t.Fatalf("self-consistent report should pass structural validation: %v", err)
	}
	if err := selfConsistent.ValidateAgainst(cases); !errors.Is(err, ErrInvalidBindingEvaluation) {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
}

func TestZeroDenominatorScoresRemainFinite(t *testing.T) {
	caseValue := BindingEvaluationCase{
		SchemaVersion: BindingEvaluationVersion, CaseID: "case-empty", Split: DatasetSplitSealed,
		DomainID: "sales", Complexity: ComplexitySimple, Ambiguity: AmbiguityNone,
		GoldUnderstanding: emptyUnderstanding("没有绑定"), PredictedUnderstanding: emptyUnderstanding("没有绑定"),
		GoldBindings: []Binding{}, PredictedBindings: []BindingPrediction{},
	}
	report, err := EvaluateBindings([]BindingEvaluationCase{caseValue})
	if err != nil {
		t.Fatal(err)
	}
	for _, score := range []PRFScore{
		report.Overall.Mention.Metric, report.Overall.Mention.Dimension, report.Overall.Mention.Member,
		report.Overall.Binding.Metric, report.Overall.Binding.Dimension, report.Overall.Binding.Member,
	} {
		if score.Precision != 0 || score.Recall != 0 || score.F1 != 0 || math.IsNaN(score.F1) {
			t.Fatalf("zero-denominator score = %+v", score)
		}
	}
}

func standardBindingEvaluationCases(t *testing.T) []BindingEvaluationCase {
	t.Helper()
	fixture := testfixture.Standard()
	if err := fixture.Validate(); err != nil {
		t.Fatalf("synthetic fixture: %v", err)
	}
	groupBy := understanding.DimensionRoleGroupBy

	direct := emptyUnderstanding("今年华东区按月的销售额")
	month := understanding.TimeGrainMonth
	direct.MetricMentions = []understanding.MetricMention{{Text: "销售额", Span: understanding.Span{Start: 8, End: 11}, AggregationHint: understanding.AggregationDefault}}
	direct.DimensionMentions = []understanding.DimensionMention{{Text: "月", Span: understanding.Span{Start: 6, End: 7}, Role: groupBy, Grain: &month}}
	dimensionHint := "地区"
	direct.ValueMentions = []understanding.ValueMention{{Text: "华东区", Span: understanding.Span{Start: 2, End: 5}, DimensionHint: &dimensionHint, OperatorHint: understanding.ValueOperatorDefault}}
	directBindings := []Binding{
		metricBinding(0, fixture.Metrics[0].Version.VersionID),
		dimensionBinding(0, fixture.Dimensions[2].Version.VersionID, groupBy),
		memberBinding(0, fixture.Dimensions[0].Version.VersionID, fixture.Members[0].Version.VersionID),
	}
	directPredictions := []BindingPrediction{
		prediction(directBindings[0], 0.97, 0.42, 1),
		prediction(directBindings[1], 0.94, 0.31, 1),
		prediction(directBindings[2], 0.92, 0.23, 1),
	}

	ambiguous := emptyUnderstanding("华东的销售额")
	ambiguous.MetricMentions = []understanding.MetricMention{{Text: "销售额", Span: understanding.Span{Start: 3, End: 6}, AggregationHint: understanding.AggregationDefault}}
	ambiguous.ValueMentions = []understanding.ValueMention{{Text: "华东", Span: understanding.Span{Start: 0, End: 2}, DimensionHint: &dimensionHint, OperatorHint: understanding.ValueOperatorDefault}}
	ambiguousGold := []Binding{
		metricBinding(0, fixture.Metrics[0].Version.VersionID),
		memberBinding(0, fixture.Dimensions[0].Version.VersionID, fixture.Members[0].Version.VersionID),
	}
	ambiguousPredicted := []BindingPrediction{
		prediction(ambiguousGold[0], 0.88, 0.18, 1),
		prediction(memberBinding(0, fixture.Dimensions[1].Version.VersionID, fixture.Members[1].Version.VersionID), 0.51, 0.01, 2),
	}

	finance := emptyUnderstanding("财务入账销售额")
	finance.MetricMentions = []understanding.MetricMention{{Text: "财务入账销售额", Span: understanding.Span{Start: 0, End: 7}, AggregationHint: understanding.AggregationDefault}}
	financePredicted := emptyUnderstanding(finance.Question)

	boundaryGold := emptyUnderstanding("按地区看订单数")
	boundaryGold.MetricMentions = []understanding.MetricMention{{Text: "订单数", Span: understanding.Span{Start: 4, End: 7}, AggregationHint: understanding.AggregationDefault}}
	boundaryGold.DimensionMentions = []understanding.DimensionMention{{Text: "地区", Span: understanding.Span{Start: 1, End: 3}, Role: groupBy}}
	boundaryPredicted := emptyUnderstanding(boundaryGold.Question)
	boundaryPredicted.MetricMentions = append([]understanding.MetricMention(nil), boundaryGold.MetricMentions...)
	boundaryPredicted.DimensionMentions = []understanding.DimensionMention{{Text: "区", Span: understanding.Span{Start: 2, End: 3}, Role: groupBy}}
	boundaryGoldBindings := []Binding{
		metricBinding(0, fixture.Metrics[2].Version.VersionID),
		dimensionBinding(0, fixture.Dimensions[0].Version.VersionID, groupBy),
	}
	boundaryPredictions := []BindingPrediction{
		prediction(boundaryGoldBindings[0], 0.9, 0.2, 1),
		prediction(boundaryGoldBindings[1], 0.7, 0.07, 1),
	}

	return []BindingEvaluationCase{
		{
			SchemaVersion: BindingEvaluationVersion, CaseID: "case-boundary", Split: DatasetSplitProductionRegression,
			DomainID: "sales", Complexity: ComplexityComposite, Ambiguity: AmbiguityDimension,
			GoldUnderstanding: boundaryGold, PredictedUnderstanding: boundaryPredicted,
			GoldBindings: boundaryGoldBindings, PredictedBindings: boundaryPredictions,
		},
		{
			SchemaVersion: BindingEvaluationVersion, CaseID: "case-member-ambiguous", Split: DatasetSplitValidation,
			DomainID: "sales", Complexity: ComplexitySimple, Ambiguity: AmbiguityMember,
			GoldUnderstanding: ambiguous, PredictedUnderstanding: ambiguous,
			GoldBindings: ambiguousGold, PredictedBindings: ambiguousPredicted,
		},
		{
			SchemaVersion: BindingEvaluationVersion, CaseID: "case-finance", Split: DatasetSplitSealed,
			DomainID: "finance", Complexity: ComplexitySimple, Ambiguity: AmbiguityCrossDomain,
			GoldUnderstanding: finance, PredictedUnderstanding: financePredicted,
			GoldBindings: []Binding{metricBinding(0, fixture.Metrics[1].Version.VersionID)}, PredictedBindings: []BindingPrediction{},
		},
		{
			SchemaVersion: BindingEvaluationVersion, CaseID: "case-direct", Split: DatasetSplitTrain,
			DomainID: "sales", Complexity: ComplexityComposite, Ambiguity: AmbiguityNone,
			GoldUnderstanding: direct, PredictedUnderstanding: direct,
			GoldBindings: directBindings, PredictedBindings: directPredictions,
		},
	}
}

func emptyUnderstanding(question string) understanding.QuestionUnderstanding {
	return understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: question,
		DomainHypotheses: []understanding.DomainHypothesis{}, MetricMentions: []understanding.MetricMention{},
		DimensionMentions: []understanding.DimensionMention{}, ValueMentions: []understanding.ValueMention{},
		Comparisons: []understanding.ComparisonMention{}, Ordering: []understanding.OrderingMention{},
		UnresolvedSpans: []understanding.UnresolvedSpan{},
	}
}

func metricBinding(index int, objectID askdata.ID) Binding {
	return Binding{MentionKind: MentionMetric, MentionIndex: index, ObjectVersionID: objectID}
}

func dimensionBinding(index int, objectID askdata.ID, value understanding.DimensionRole) Binding {
	role := value
	return Binding{MentionKind: MentionDimension, MentionIndex: index, ObjectVersionID: objectID, Role: &role}
}

func memberBinding(index int, parentID, objectID askdata.ID) Binding {
	parent := parentID
	return Binding{MentionKind: MentionMember, MentionIndex: index, ObjectVersionID: objectID, ParentDimensionVersionID: &parent}
}

func prediction(binding Binding, score, margin float64, rank int) BindingPrediction {
	return BindingPrediction{Binding: binding, Features: CalibrationFeatures{
		CandidateScore: score, CandidateMargin: margin, ExactScore: 0.8,
		LexicalScore: 0.7, VectorScore: 0.6, GraphScore: 1, RuleScore: 1, RetrievalRank: rank,
	}}
}

func assertScore(t *testing.T, name string, got PRFScore, tp, fp, fn int, precision, recall, f1 float64) {
	t.Helper()
	want := PRFScore{
		TruePositive: tp, FalsePositive: fp, FalseNegative: fn,
		Gold: tp + fn, Predicted: tp + fp, Precision: precision, Recall: recall, F1: f1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %+v, want %+v", name, got, want)
	}
}

func domainIDs(slices []DomainSlice) []askdata.ID {
	result := make([]askdata.ID, len(slices))
	for index, slice := range slices {
		result[index] = slice.DomainID
	}
	return result
}

func complexities(slices []ComplexitySlice) []ComplexityClass {
	result := make([]ComplexityClass, len(slices))
	for index, slice := range slices {
		result[index] = slice.Complexity
	}
	return result
}

func ambiguities(slices []AmbiguitySlice) []AmbiguityClass {
	result := make([]AmbiguityClass, len(slices))
	for index, slice := range slices {
		result[index] = slice.Ambiguity
	}
	return result
}

func cloneBindingCase(t *testing.T, value BindingEvaluationCase) BindingEvaluationCase {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned BindingEvaluationCase
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func cloneBindingReport(t *testing.T, value BindingEvaluationReport) BindingEvaluationReport {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned BindingEvaluationReport
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func reverseBindings(values []Binding) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reversePredictions(values []BindingPrediction) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
