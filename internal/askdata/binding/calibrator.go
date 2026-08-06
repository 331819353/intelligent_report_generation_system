package binding

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/calibration"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

const (
	CalibrationModelVersion = "binding-calibration-model-v1"
	calibrationFeatureCount = 9
	defaultFitIterations    = 1_200
	defaultFitLearningRate  = 0.15
	defaultFitL2            = 0.01
)

var ErrInvalidCalibration = errors.New("binding calibration is invalid")

type FitConfig struct {
	Iterations          int     `json:"iterations"`
	LearningRate        float64 `json:"learningRate"`
	L2                  float64 `json:"l2"`
	MinDirectConfidence float64 `json:"minDirectConfidence"`
	MinDirectMargin     float64 `json:"minDirectMargin"`
	MinDirectPrecision  float64 `json:"minDirectPrecision"`
	MinDirectExamples   int     `json:"minDirectExamples"`
}

func (config FitConfig) normalize() (FitConfig, error) {
	if config.Iterations == 0 {
		config.Iterations = defaultFitIterations
	}
	if config.LearningRate == 0 {
		config.LearningRate = defaultFitLearningRate
	}
	if config.L2 == 0 {
		config.L2 = defaultFitL2
	}
	if config.MinDirectConfidence == 0 {
		config.MinDirectConfidence = 0.80
	}
	if config.MinDirectMargin == 0 {
		config.MinDirectMargin = 0.10
	}
	if config.MinDirectPrecision == 0 {
		config.MinDirectPrecision = 0.95
	}
	if config.MinDirectExamples == 0 {
		config.MinDirectExamples = 2
	}
	if config.Iterations < 100 || config.Iterations > 20_000 ||
		!positiveBounded(config.LearningRate, 1) || !positiveBounded(config.L2, 10) ||
		!unitScore(config.MinDirectConfidence) || !unitScore(config.MinDirectMargin) ||
		!unitScore(config.MinDirectPrecision) || config.MinDirectPrecision == 0 ||
		config.MinDirectExamples < 1 || config.MinDirectExamples > 100_000 {
		return FitConfig{}, fmt.Errorf("%w: fit configuration", ErrInvalidCalibration)
	}
	return config, nil
}

type CalibrationBin struct {
	UpperBound  float64 `json:"upperBound"`
	Probability float64 `json:"probability"`
	Count       int     `json:"count"`
	Correct     int     `json:"correct"`
}

type CalibrationModel struct {
	Version             string              `json:"version"`
	Coefficients        []float64           `json:"coefficients"`
	Bins                []CalibrationBin    `json:"bins"`
	DirectEnabled       bool                `json:"directEnabled"`
	DirectThreshold     float64             `json:"directThreshold"`
	MinDirectMargin     float64             `json:"minDirectMargin"`
	MinDirectPrecision  float64             `json:"minDirectPrecision"`
	MinDirectExamples   int                 `json:"minDirectExamples"`
	ValidationPrecision float64             `json:"validationPrecision"`
	ValidationCoverage  float64             `json:"validationCoverage"`
	ValidationDirect    int                 `json:"validationDirect"`
	TrainingCount       int                 `json:"trainingCount"`
	ValidationCount     int                 `json:"validationCount"`
	TrainingHash        askdata.ContentHash `json:"trainingHash"`
	ValidationHash      askdata.ContentHash `json:"validationHash"`
	ContentHash         askdata.ContentHash `json:"contentHash"`
}

type Calibrator struct{ model CalibrationModel }

func FitCalibrator(
	inputs calibration.CalibrationInputs,
	config FitConfig,
) (*Calibrator, error) {
	model, err := FitCalibrationModel(inputs, config)
	if err != nil {
		return nil, err
	}
	return &Calibrator{model: model}, nil
}

func NewCalibrator(model CalibrationModel) (*Calibrator, error) {
	if err := model.Validate(); err != nil {
		return nil, err
	}
	return &Calibrator{model: model}, nil
}

func DecodeCalibrationModel(raw []byte) (CalibrationModel, error) {
	var model CalibrationModel
	if err := askdata.DecodeStrictJSON(raw, &model); err != nil {
		return CalibrationModel{}, err
	}
	if err := model.Validate(); err != nil {
		return CalibrationModel{}, err
	}
	return model, nil
}

func (calibrator *Calibrator) Model() CalibrationModel {
	if calibrator == nil {
		return CalibrationModel{}
	}
	result := calibrator.model
	result.Coefficients = append([]float64(nil), result.Coefficients...)
	result.Bins = append([]CalibrationBin(nil), result.Bins...)
	return result
}

func FitCalibrationModel(
	inputs calibration.CalibrationInputs,
	config FitConfig,
) (CalibrationModel, error) {
	config, err := config.normalize()
	if err != nil {
		return CalibrationModel{}, err
	}
	training, err := normalizeCalibrationExamples(inputs.Training, "training")
	if err != nil {
		return CalibrationModel{}, err
	}
	validation, err := normalizeCalibrationExamples(inputs.Validation, "validation")
	if err != nil {
		return CalibrationModel{}, err
	}
	trainingIdentities := make(map[string]struct{}, len(training))
	for _, example := range training {
		trainingIdentities[calibrationExampleIdentity(example)] = struct{}{}
	}
	for _, example := range validation {
		if _, leaked := trainingIdentities[calibrationExampleIdentity(example)]; leaked {
			return CalibrationModel{}, fmt.Errorf("%w: training example leaked into validation", ErrInvalidCalibration)
		}
	}
	if !hasBothLabels(training) || !hasBothLabels(validation) {
		return CalibrationModel{}, fmt.Errorf("%w: training and validation require positive and negative labels", ErrInvalidCalibration)
	}
	trainingHash, _, err := registry.CanonicalContentHash(training)
	if err != nil {
		return CalibrationModel{}, err
	}
	validationHash, _, err := registry.CanonicalContentHash(validation)
	if err != nil {
		return CalibrationModel{}, err
	}
	coefficients := fitLogistic(training, config)
	bins := fitIsotonic(validation, coefficients)
	directEnabled, threshold, precision, coverage, directCount := selectDirectThreshold(
		validation, coefficients, bins, config,
	)
	model := CalibrationModel{
		Version: CalibrationModelVersion, Coefficients: coefficients, Bins: bins,
		DirectEnabled: directEnabled, DirectThreshold: threshold,
		MinDirectMargin: config.MinDirectMargin, MinDirectPrecision: config.MinDirectPrecision,
		MinDirectExamples: config.MinDirectExamples, ValidationPrecision: precision,
		ValidationCoverage: coverage, ValidationDirect: directCount,
		TrainingCount: len(training), ValidationCount: len(validation),
		TrainingHash: trainingHash, ValidationHash: validationHash,
	}
	model.ContentHash, err = calibrationModelHash(model)
	if err != nil {
		return CalibrationModel{}, err
	}
	if err := model.Validate(); err != nil {
		return CalibrationModel{}, err
	}
	return model, nil
}

func (model CalibrationModel) Validate() error {
	if model.Version != CalibrationModelVersion || len(model.Coefficients) != calibrationFeatureCount ||
		len(model.Bins) == 0 || model.TrainingCount < 2 || model.ValidationCount < 2 ||
		model.TrainingHash.Validate() != nil || model.ValidationHash.Validate() != nil ||
		model.TrainingHash == model.ValidationHash ||
		model.MinDirectExamples < 1 || !unitScore(model.MinDirectMargin) ||
		!unitScore(model.MinDirectPrecision) || !unitScore(model.DirectThreshold) ||
		!unitScore(model.ValidationPrecision) || !unitScore(model.ValidationCoverage) ||
		model.ValidationDirect < 0 || model.ValidationDirect > model.ValidationCount {
		return ErrInvalidCalibration
	}
	for _, coefficient := range model.Coefficients {
		if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) || math.Abs(coefficient) > 100 {
			return ErrInvalidCalibration
		}
	}
	count := 0
	previousUpper, previousProbability := -1.0, -1.0
	for _, bin := range model.Bins {
		if !unitScore(bin.UpperBound) || !unitScore(bin.Probability) ||
			bin.UpperBound <= previousUpper || bin.Probability < previousProbability ||
			bin.Count < 1 || bin.Correct < 0 || bin.Correct > bin.Count {
			return ErrInvalidCalibration
		}
		previousUpper, previousProbability = bin.UpperBound, bin.Probability
		count += bin.Count
	}
	if count != model.ValidationCount {
		return ErrInvalidCalibration
	}
	if model.DirectEnabled {
		if model.ValidationDirect < model.MinDirectExamples ||
			model.ValidationPrecision < model.MinDirectPrecision || model.DirectThreshold == 0 {
			return ErrInvalidCalibration
		}
	} else if model.ValidationDirect != 0 || model.ValidationPrecision != 0 || model.ValidationCoverage != 0 {
		return ErrInvalidCalibration
	}
	expected, err := calibrationModelHash(model)
	if err != nil || model.ContentHash.Validate() != nil || model.ContentHash != expected {
		return ErrInvalidCalibration
	}
	return nil
}

func (model CalibrationModel) EvidenceRef() (askdata.EvidenceRef, error) {
	if err := model.Validate(); err != nil {
		return askdata.EvidenceRef{}, err
	}
	return askdata.EvidenceRef{
		EvidenceID: askdata.ID("calibration-model:" + string(model.ContentHash)),
		Kind:       askdata.EvidenceKindDataQuality, SourceID: askdata.ID(CalibrationModelVersion),
		ContentHash: model.ContentHash,
	}, nil
}

func (model CalibrationModel) Probability(features calibration.CalibrationFeatures) (float64, error) {
	if err := model.Validate(); err != nil {
		return 0, err
	}
	if err := validateCalibrationFeatures(features); err != nil {
		return 0, err
	}
	raw := logistic(dot(model.Coefficients, featureVector(features)))
	return applyIsotonic(model.Bins, raw), nil
}

func fitLogistic(examples []calibration.CalibrationExample, config FitConfig) []float64 {
	weights := make([]float64, calibrationFeatureCount)
	for iteration := 0; iteration < config.Iterations; iteration++ {
		gradient := make([]float64, calibrationFeatureCount)
		for _, example := range examples {
			features := featureVector(example.Features)
			prediction := logistic(dot(weights, features))
			target := 0.0
			if example.Correct {
				target = 1
			}
			for index := range gradient {
				gradient[index] += (prediction - target) * features[index]
			}
		}
		count := float64(len(examples))
		for index := range weights {
			regularization := 0.0
			if index > 0 {
				regularization = config.L2 * weights[index]
			}
			weights[index] -= config.LearningRate * (gradient[index]/count + regularization)
			weights[index] = math.Max(-100, math.Min(100, weights[index]))
		}
	}
	for index := range weights {
		weights[index] = roundScore(weights[index])
	}
	return weights
}

type isotonicWorkBlock struct {
	upper       float64
	probability float64
	count       int
	correct     int
}

func fitIsotonic(
	examples []calibration.CalibrationExample,
	coefficients []float64,
) []CalibrationBin {
	type observation struct {
		raw     float64
		correct bool
	}
	observations := make([]observation, len(examples))
	for index, example := range examples {
		observations[index] = observation{
			raw: logistic(dot(coefficients, featureVector(example.Features))), correct: example.Correct,
		}
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].raw != observations[j].raw {
			return observations[i].raw < observations[j].raw
		}
		return !observations[i].correct && observations[j].correct
	})
	blocks := []isotonicWorkBlock{}
	for index := 0; index < len(observations); {
		end, correct := index+1, 0
		if observations[index].correct {
			correct++
		}
		for end < len(observations) && observations[end].raw == observations[index].raw {
			if observations[end].correct {
				correct++
			}
			end++
		}
		count := end - index
		blocks = append(blocks, isotonicWorkBlock{
			upper: observations[end-1].raw, probability: float64(correct+1) / float64(count+2),
			count: count, correct: correct,
		})
		for len(blocks) >= 2 && blocks[len(blocks)-2].probability > blocks[len(blocks)-1].probability {
			right, left := blocks[len(blocks)-1], blocks[len(blocks)-2]
			mergedCount := left.count + right.count
			blocks = append(blocks[:len(blocks)-2], isotonicWorkBlock{
				upper: right.upper,
				probability: (left.probability*float64(left.count) + right.probability*float64(right.count)) /
					float64(mergedCount),
				count: mergedCount, correct: left.correct + right.correct,
			})
		}
		index = end
	}
	result := make([]CalibrationBin, len(blocks))
	for index, block := range blocks {
		result[index] = CalibrationBin{
			UpperBound: block.upper, Probability: roundScore(block.probability),
			Count: block.count, Correct: block.correct,
		}
	}
	return result
}

func selectDirectThreshold(
	examples []calibration.CalibrationExample,
	coefficients []float64,
	bins []CalibrationBin,
	config FitConfig,
) (bool, float64, float64, float64, int) {
	probabilities := make([]float64, len(examples))
	candidates := []float64{}
	for index, example := range examples {
		raw := logistic(dot(coefficients, featureVector(example.Features)))
		probabilities[index] = applyIsotonic(bins, raw)
		if probabilities[index] >= config.MinDirectConfidence {
			candidates = append(candidates, probabilities[index])
		}
	}
	sort.Float64s(candidates)
	candidates = uniqueFloats(candidates)
	bestThreshold, bestPrecision, bestCount := 1.0, 0.0, 0
	for _, threshold := range candidates {
		count, correct := 0, 0
		for index, example := range examples {
			if probabilities[index] < threshold || example.Features.CandidateMargin < config.MinDirectMargin {
				continue
			}
			count++
			if example.Correct {
				correct++
			}
		}
		if count < config.MinDirectExamples {
			continue
		}
		precision := float64(correct) / float64(count)
		if precision >= config.MinDirectPrecision && count > bestCount {
			bestThreshold, bestPrecision, bestCount = threshold, precision, count
		}
	}
	if bestCount == 0 {
		return false, 1, 0, 0, 0
	}
	return true, roundScore(bestThreshold), roundScore(bestPrecision),
		roundScore(float64(bestCount) / float64(len(examples))), bestCount
}

func normalizeCalibrationExamples(
	values []calibration.CalibrationExample,
	name string,
) ([]calibration.CalibrationExample, error) {
	if len(values) < 2 || len(values) > calibration.MaxCalibrationExamples {
		return nil, fmt.Errorf("%w: %s example count", ErrInvalidCalibration, name)
	}
	result := append([]calibration.CalibrationExample(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		return calibrationExampleIdentity(result[i]) < calibrationExampleIdentity(result[j])
	})
	for index, example := range result {
		if err := validateCalibrationExample(example); err != nil {
			return nil, fmt.Errorf("%w: %s[%d]: %v", ErrInvalidCalibration, name, index, err)
		}
		if index > 0 && calibrationExampleIdentity(result[index-1]) == calibrationExampleIdentity(example) {
			return nil, fmt.Errorf("%w: duplicate %s example", ErrInvalidCalibration, name)
		}
	}
	return result, nil
}

func validateCalibrationExample(example calibration.CalibrationExample) error {
	if example.CaseID.Validate() != nil || example.DomainID.Validate() != nil ||
		example.ObjectVersionID.Validate() != nil || example.MentionSpan.Start < 0 ||
		example.MentionSpan.End <= example.MentionSpan.Start ||
		validateCalibrationFeatures(example.Features) != nil {
		return ErrInvalidCalibration
	}
	switch example.Complexity {
	case calibration.ComplexitySimple, calibration.ComplexityComposite,
		calibration.ComplexityContextual, calibration.ComplexityRelational:
	default:
		return ErrInvalidCalibration
	}
	switch example.Ambiguity {
	case calibration.AmbiguityNone, calibration.AmbiguityMetric, calibration.AmbiguityDimension,
		calibration.AmbiguityMember, calibration.AmbiguityCrossDomain, calibration.AmbiguityMultiple:
	default:
		return ErrInvalidCalibration
	}
	switch example.MentionKind {
	case calibration.MentionMetric:
		if example.ParentDimensionVersionID != nil || example.Role != nil {
			return ErrInvalidCalibration
		}
	case calibration.MentionDimension:
		if example.ParentDimensionVersionID != nil || example.Role == nil || !validCalibrationRole(*example.Role) {
			return ErrInvalidCalibration
		}
	case calibration.MentionMember:
		if example.ParentDimensionVersionID == nil || example.ParentDimensionVersionID.Validate() != nil || example.Role != nil {
			return ErrInvalidCalibration
		}
	default:
		return ErrInvalidCalibration
	}
	return nil
}

func validateCalibrationFeatures(features calibration.CalibrationFeatures) error {
	for _, value := range []float64{
		features.CandidateScore, features.CandidateMargin, features.ExactScore,
		features.LexicalScore, features.VectorScore, features.GraphScore, features.RuleScore,
	} {
		if !unitScore(value) {
			return ErrInvalidCalibration
		}
	}
	if features.RetrievalRank < 1 || features.RetrievalRank > calibration.MaxCalibrationRank {
		return ErrInvalidCalibration
	}
	return nil
}

func runtimeFeatures(bundle Bundle, margin float64, rank int) calibration.CalibrationFeatures {
	return calibration.CalibrationFeatures{
		CandidateScore: bundle.Score.Total, CandidateMargin: margin,
		ExactScore: bundle.Score.Exact, LexicalScore: bundle.Score.Lexical,
		VectorScore: bundle.Score.Vector, GraphScore: bundle.Score.Graph,
		RuleScore: bundle.Score.Rule, RetrievalRank: rank,
	}
}

func featureVector(features calibration.CalibrationFeatures) []float64 {
	return []float64{
		1, features.CandidateScore, features.CandidateMargin, features.ExactScore,
		features.LexicalScore, features.VectorScore, features.GraphScore,
		features.RuleScore, 1 / float64(features.RetrievalRank),
	}
}

func calibrationExampleIdentity(example calibration.CalibrationExample) string {
	parent, role := "", ""
	if example.ParentDimensionVersionID != nil {
		parent = string(*example.ParentDimensionVersionID)
	}
	if example.Role != nil {
		role = string(*example.Role)
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s",
		example.CaseID, example.DomainID, example.MentionKind, example.MentionSpan.Start,
		example.MentionSpan.End, example.ObjectVersionID, parent, role)
}

func hasBothLabels(values []calibration.CalibrationExample) bool {
	positive, negative := false, false
	for _, value := range values {
		if value.Correct {
			positive = true
		} else {
			negative = true
		}
	}
	return positive && negative
}

func validCalibrationRole(value understanding.DimensionRole) bool {
	return value == understanding.DimensionRoleGroupBy || value == understanding.DimensionRoleFilter ||
		value == understanding.DimensionRoleTime || value == understanding.DimensionRoleSort
}

func calibrationModelHash(model CalibrationModel) (askdata.ContentHash, error) {
	payload := model
	payload.ContentHash = ""
	hash, _, err := registry.CanonicalContentHash(payload)
	return hash, err
}

func dot(left, right []float64) float64 {
	result := 0.0
	for index := range left {
		result += left[index] * right[index]
	}
	return result
}

func logistic(value float64) float64 {
	if value >= 0 {
		exponential := math.Exp(-value)
		return 1 / (1 + exponential)
	}
	exponential := math.Exp(value)
	return exponential / (1 + exponential)
}

func applyIsotonic(bins []CalibrationBin, raw float64) float64 {
	for _, bin := range bins {
		if raw <= bin.UpperBound {
			return bin.Probability
		}
	}
	return bins[len(bins)-1].Probability
}

func uniqueFloats(values []float64) []float64 {
	if len(values) == 0 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] != values[write-1] {
			values[write] = values[read]
			write++
		}
	}
	return values[:write]
}

func positiveBounded(value, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= maximum
}
