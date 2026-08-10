package binding

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/calibration"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

func TestCalibrationModelUsesTrainAndHeldOutValidationDeterministically(t *testing.T) {
	inputs := calibrationInputsForTest()
	config := directFitConfig()
	first, err := FitCalibrationModel(inputs, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !first.DirectEnabled || first.ValidationPrecision != 1 || first.ValidationDirect < 2 ||
		first.TrainingHash == first.ValidationHash {
		t.Fatalf("held-out direct gate was not proven: %#v", first)
	}
	permuted := inputs
	permuted.Training = reverseCalibrationExamples(inputs.Training)
	permuted.Validation = reverseCalibrationExamples(inputs.Validation)
	second, err := FitCalibrationModel(permuted, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("calibration changed with input order:\nfirst=%#v\nsecond=%#v", first, second)
	}
	high, err := first.Probability(highCalibrationFeatures())
	if err != nil {
		t.Fatal(err)
	}
	low, err := first.Probability(lowCalibrationFeatures())
	if err != nil {
		t.Fatal(err)
	}
	if high <= low || high < first.DirectThreshold {
		t.Fatalf("probabilities high=%v low=%v threshold=%v", high, low, first.DirectThreshold)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCalibrationModel(raw)
	if err != nil || !reflect.DeepEqual(decoded, first) {
		t.Fatalf("DecodeCalibrationModel() = %#v, %v", decoded, err)
	}
	unknown := strings.Replace(string(raw), `"version":`, `"llmConfidence":0.99,"version":`, 1)
	if _, err := DecodeCalibrationModel([]byte(unknown)); err == nil {
		t.Fatal("calibration model accepted an LLM confidence field")
	}

	tampered := first
	tampered.Coefficients = append([]float64(nil), first.Coefficients...)
	tampered.Coefficients[0]++
	if _, err := NewCalibrator(tampered); !errors.Is(err, ErrInvalidCalibration) {
		t.Fatalf("tampered model error = %v", err)
	}
}

func TestCalibrationRejectsMissingLabelsAndInvalidFeatures(t *testing.T) {
	inputs := calibrationInputsForTest()
	for index := range inputs.Validation {
		inputs.Validation[index].Correct = true
	}
	if _, err := FitCalibrationModel(inputs, directFitConfig()); !errors.Is(err, ErrInvalidCalibration) {
		t.Fatalf("single-label validation error = %v", err)
	}
	inputs = calibrationInputsForTest()
	inputs.Training[0].Features.VectorScore = 2
	if _, err := FitCalibrationModel(inputs, directFitConfig()); !errors.Is(err, ErrInvalidCalibration) {
		t.Fatalf("invalid feature error = %v", err)
	}
	inputs = calibrationInputsForTest()
	inputs.Validation[0] = inputs.Training[0]
	if _, err := FitCalibrationModel(inputs, directFitConfig()); !errors.Is(err, ErrInvalidCalibration) {
		t.Fatalf("train/validation leakage error = %v", err)
	}
}

func TestCalibratorAllowsOnlyHeldOutProvenDirectBundle(t *testing.T) {
	calibrator, err := FitCalibrator(calibrationInputsForTest(), directFitConfig())
	if err != nil {
		t.Fatal(err)
	}
	bindingRequest := jointBindingFixture(t)
	bindingResult, err := Bind(bindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := DecisionRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		Presentations: presentationsForResult(bindingResult),
	}
	decision, err := calibrator.Decide(request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Disposition != DispositionDirect || decision.SelectedBundleHash == nil ||
		*decision.SelectedBundleHash != bindingResult.Bundles[0].BundleHash ||
		decision.Clarification != nil || decision.Confidence.Score < calibrator.model.DirectThreshold {
		t.Fatalf("unexpected direct decision: %#v model=%#v", decision, calibrator.model)
	}
	if err := decision.ValidateAgainst(calibrator, request); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
}

func TestLowMarginProducesTwoToThreeEvidenceBackedOptions(t *testing.T) {
	config := directFitConfig()
	config.MinDirectMargin = 0.9
	calibrator, err := FitCalibrator(calibrationInputsForTest(), config)
	if err != nil {
		t.Fatal(err)
	}
	bindingRequest := jointBindingFixture(t)
	bindingResult, err := Bind(bindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := DecisionRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		Presentations: presentationsForResult(bindingResult),
	}
	decision, err := calibrator.Decide(request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Disposition != DispositionClarify || decision.Clarification == nil ||
		len(decision.Clarification.Options) < 2 || len(decision.Clarification.Options) > 3 ||
		decision.Clarification.Question == "问题不清楚，请重新输入" {
		t.Fatalf("unexpected clarification: %#v", decision)
	}
	for _, option := range decision.Clarification.Options {
		if option.Label == "" || option.Difference == "" || len(option.EvidenceRefs) == 0 ||
			option.BundleHash.Validate() != nil {
			t.Fatalf("option is not explainable and evidence-backed: %#v", option)
		}
	}
	arguments, err := decision.Clarification.ToolArguments(bindingResult.Scope.Release)
	if err != nil || len(arguments.ClarificationOptions) != len(decision.Clarification.Options) {
		t.Fatalf("typed clarification tool arguments = %#v, %v", arguments, err)
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeConfidenceDecision(raw, calibrator, request)
	if err != nil || !reflect.DeepEqual(decoded, decision) {
		t.Fatalf("DecodeConfidenceDecision() = %#v, %v", decoded, err)
	}
	unknown := strings.Replace(string(raw), `"version":`, `"confidenceFromLlm":0.99,"version":`, 1)
	if _, err := DecodeConfidenceDecision([]byte(unknown), calibrator, request); err == nil {
		t.Fatal("strict decoder accepted an LLM-provided confidence field")
	}
}

func TestNoMatchAndSingleBundleNeverInventClarificationOptions(t *testing.T) {
	config := directFitConfig()
	config.MinDirectMargin = 0.9
	calibrator, err := FitCalibrator(calibrationInputsForTest(), config)
	if err != nil {
		t.Fatal(err)
	}

	noMatchRequest := jointBindingFixture(t)
	metricSet := candidateSetIndex(noMatchRequest.CandidateSets, MentionMetric)
	noMatchRequest.CandidateSets[metricSet].Candidates = []CandidateOption{}
	noMatchResult, err := Bind(noMatchRequest)
	if err != nil {
		t.Fatal(err)
	}
	noMatch, err := calibrator.Decide(DecisionRequest{
		BindingRequest: noMatchRequest, BindingResult: noMatchResult,
		Presentations: []BundlePresentation{},
	})
	if err != nil || noMatch.Disposition != DispositionNoMatch || noMatch.Clarification != nil {
		t.Fatalf("no-match decision = %#v, %v", noMatch, err)
	}

	singleRequest := jointBindingFixture(t)
	singleRequest.Config.TopBundles = 1
	singleResult, err := Bind(singleRequest)
	if err != nil {
		t.Fatal(err)
	}
	single, err := calibrator.Decide(DecisionRequest{
		BindingRequest: singleRequest, BindingResult: singleResult,
		Presentations: presentationsForResult(singleResult),
	})
	if err != nil || single.Disposition != DispositionEvidenceRequired || single.Clarification != nil {
		t.Fatalf("single low-confidence decision = %#v, %v", single, err)
	}
}

func TestTopNComparisonWithoutRankByAlwaysProducesThreeChoiceClarification(t *testing.T) {
	calibrator, err := FitCalibrator(calibrationInputsForTest(), directFitConfig())
	if err != nil {
		t.Fatal(err)
	}
	bindingRequest := jointBindingFixture(t)
	understandingRequest, understandingResult, _ := understandingFixture(
		t, "销售额地区华东同比前10", []string{"销售额"}, []string{"地区"}, []string{"华东"},
	)
	bindingRequest.UnderstandingRequest = understandingRequest
	bindingRequest.UnderstandingResult = understandingResult
	bindingRequest.Config.TopBundles = 1
	bindingResult, err := Bind(bindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := calibrator.Decide(DecisionRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		Presentations: presentationsForResult(bindingResult),
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Disposition != DispositionClarify || decision.Clarification == nil ||
		decision.Clarification.ConflictCode != "TOPN_COMPARISON_RANK_BY_REQUIRED" ||
		len(decision.Clarification.Options) != 3 {
		t.Fatalf("unexpected rankBy clarification: %#v", decision)
	}
	wantLabels := []string{"按当期值", "按增长额", "按增长率"}
	for index, option := range decision.Clarification.Options {
		if option.Label != wantLabels[index] || option.BundleHash != bindingResult.Bundles[0].BundleHash ||
			len(option.EvidenceRefs) == 0 {
			t.Fatalf("rankBy option[%d] = %#v", index, option)
		}
	}
}

func TestClarificationRejectsInventedBundleUnsafeTextAndCrossBundleEvidence(t *testing.T) {
	config := directFitConfig()
	config.MinDirectMargin = 0.9
	calibrator, err := FitCalibrator(calibrationInputsForTest(), config)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]BundlePresentation)
	}{
		{name: "invented bundle", mutate: func(values []BundlePresentation) {
			values[0].BundleHash = askdata.HashBytes([]byte("invented"))
		}},
		{name: "physical query text", mutate: func(values []BundlePresentation) {
			values[0].Difference = "select secret from physical_table"
		}},
		{name: "cross bundle evidence", mutate: func(values []BundlePresentation) {
			values[0].EvidenceRefs = []askdata.EvidenceRef{values[1].EvidenceRefs[0]}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindingRequest := jointBindingFixture(t)
			bindingResult, bindErr := Bind(bindingRequest)
			if bindErr != nil {
				t.Fatal(bindErr)
			}
			presentations := presentationsForResult(bindingResult)
			test.mutate(presentations)
			_, decideErr := calibrator.Decide(DecisionRequest{
				BindingRequest: bindingRequest, BindingResult: bindingResult, Presentations: presentations,
			})
			if !errors.Is(decideErr, ErrInvalidConfidenceDecision) {
				t.Fatalf("Decide() error = %v", decideErr)
			}
		})
	}
}

func calibrationInputsForTest() calibration.CalibrationInputs {
	training := make([]calibration.CalibrationExample, 0, 8)
	validation := make([]calibration.CalibrationExample, 0, 8)
	for index := 0; index < 4; index++ {
		training = append(training,
			calibrationExampleForTest(askdata.ID("train-positive-"+string(rune('a'+index))), highCalibrationFeatures(), true),
			calibrationExampleForTest(askdata.ID("train-negative-"+string(rune('a'+index))), lowCalibrationFeatures(), false),
		)
		validation = append(validation,
			calibrationExampleForTest(askdata.ID("validation-positive-"+string(rune('a'+index))), highCalibrationFeatures(), true),
			calibrationExampleForTest(askdata.ID("validation-negative-"+string(rune('a'+index))), lowCalibrationFeatures(), false),
		)
	}
	return calibration.CalibrationInputs{Training: training, Validation: validation}
}

func calibrationExampleForTest(
	caseID askdata.ID,
	features calibration.CalibrationFeatures,
	correct bool,
) calibration.CalibrationExample {
	return calibration.CalibrationExample{
		CaseID: caseID, DomainID: "sales", Complexity: calibration.ComplexitySimple,
		Ambiguity: calibration.AmbiguityMetric, MentionKind: calibration.MentionMetric,
		MentionSpan: understanding.Span{Start: 0, End: 3}, ObjectVersionID: caseID + "-metric-v1",
		Features: features, Correct: correct,
	}
}

func highCalibrationFeatures() calibration.CalibrationFeatures {
	return calibration.CalibrationFeatures{
		CandidateScore: 0.85, CandidateMargin: 0.30, ExactScore: 0.70,
		LexicalScore: 0.80, VectorScore: 0.75, GraphScore: 1, RuleScore: 1,
		RetrievalRank: 1,
	}
}

func lowCalibrationFeatures() calibration.CalibrationFeatures {
	return calibration.CalibrationFeatures{
		CandidateScore: 0.20, CandidateMargin: 0.01, ExactScore: 0,
		LexicalScore: 0.15, VectorScore: 0.10, GraphScore: 0.30, RuleScore: 0.20,
		RetrievalRank: 4,
	}
}

func directFitConfig() FitConfig {
	return FitConfig{
		Iterations: 800, LearningRate: 0.2, L2: 0.01,
		MinDirectConfidence: 0.60, MinDirectMargin: 0.01,
		MinDirectPrecision: 0.95, MinDirectExamples: 2,
	}
}

func reverseCalibrationExamples(values []calibration.CalibrationExample) []calibration.CalibrationExample {
	result := append([]calibration.CalibrationExample(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func presentationsForResult(result Result) []BundlePresentation {
	values := make([]BundlePresentation, len(result.Bundles))
	for index, bundle := range result.Bundles {
		values[index] = BundlePresentation{
			BundleHash:   bundle.BundleHash,
			Label:        "候选口径 " + string(rune('A'+index)),
			Difference:   "使用不同的认证指标、维度或成员组合。",
			EvidenceRefs: []askdata.EvidenceRef{optionSpecificEvidence(result.Bundles, index)},
		}
	}
	return values
}

func optionSpecificEvidence(bundles []Bundle, index int) askdata.EvidenceRef {
	for _, candidate := range bundles[index].EvidenceRefs {
		shared := false
		for otherIndex, other := range bundles {
			if otherIndex == index {
				continue
			}
			for _, evidence := range other.EvidenceRefs {
				if evidence == candidate {
					shared = true
					break
				}
			}
			if shared {
				break
			}
		}
		if !shared {
			return candidate
		}
	}
	return bundles[index].EvidenceRefs[0]
}
