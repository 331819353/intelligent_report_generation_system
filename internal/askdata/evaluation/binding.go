package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/calibration"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

const (
	BindingEvaluationVersion = "mention-binding-evaluation-v1"
	MaxBindingCases          = 10_000
	MaxCalibrationExamples   = calibration.MaxCalibrationExamples
	MaxBindingReportBytes    = 64 << 20
	MaxCalibrationRank       = calibration.MaxCalibrationRank
)

var (
	ErrInvalidBindingEvaluation = errors.New("mention/binding evaluation is invalid")
	ErrDuplicateEvaluationCase  = errors.New("mention/binding evaluation contains a duplicate case")
)

// DatasetSplit keeps calibration inputs separate from sealed and production
// regression cases. Only TRAIN and VALIDATION predictions are exported as
// calibration examples.
type DatasetSplit string

const (
	DatasetSplitTrain                DatasetSplit = "TRAIN"
	DatasetSplitValidation           DatasetSplit = "VALIDATION"
	DatasetSplitSealed               DatasetSplit = "SEALED"
	DatasetSplitProductionRegression DatasetSplit = "PRODUCTION_REGRESSION"
)

// ComplexityClass is a mutually exclusive evaluation stratum. It describes
// the gold case, not a model prediction.
type ComplexityClass = calibration.ComplexityClass

const (
	ComplexitySimple     = calibration.ComplexitySimple
	ComplexityComposite  = calibration.ComplexityComposite
	ComplexityContextual = calibration.ComplexityContextual
	ComplexityRelational = calibration.ComplexityRelational
)

// AmbiguityClass identifies the dominant gold ambiguity. MULTIPLE is used
// when no single ambiguity family explains the case.
type AmbiguityClass = calibration.AmbiguityClass

const (
	AmbiguityNone        = calibration.AmbiguityNone
	AmbiguityMetric      = calibration.AmbiguityMetric
	AmbiguityDimension   = calibration.AmbiguityDimension
	AmbiguityMember      = calibration.AmbiguityMember
	AmbiguityCrossDomain = calibration.AmbiguityCrossDomain
	AmbiguityMultiple    = calibration.AmbiguityMultiple
)

type MentionKind = calibration.MentionKind

const (
	MentionMetric    = calibration.MentionMetric
	MentionDimension = calibration.MentionDimension
	MentionMember    = calibration.MentionMember
)

// Binding identifies the selected immutable semantic object for one mention.
// ObjectVersionID is the metric, dimension or member version. Members also
// carry their parent dimension, and dimensions carry their semantic role.
type Binding struct {
	MentionKind              MentionKind                  `json:"mentionKind"`
	MentionIndex             int                          `json:"mentionIndex"`
	ObjectVersionID          askdata.ID                   `json:"objectVersionId"`
	ParentDimensionVersionID *askdata.ID                  `json:"parentDimensionVersionId"`
	Role                     *understanding.DimensionRole `json:"role"`
}

// CalibrationFeatures are trusted, normalized system features. There is no
// LLM-reported confidence field: NLU-006 must fit and validate its own
// calibrated probability from these features and held-out labels.
type CalibrationFeatures = calibration.CalibrationFeatures

type BindingPrediction struct {
	Binding  Binding             `json:"binding"`
	Features CalibrationFeatures `json:"features"`
}

// BindingEvaluationCase compares a gold and predicted understanding of the
// same exact question. Gold metadata determines all report strata.
type BindingEvaluationCase struct {
	SchemaVersion          string                              `json:"schemaVersion"`
	CaseID                 askdata.ID                          `json:"caseId"`
	Split                  DatasetSplit                        `json:"split"`
	DomainID               askdata.ID                          `json:"domainId"`
	Complexity             ComplexityClass                     `json:"complexity"`
	Ambiguity              AmbiguityClass                      `json:"ambiguity"`
	GoldUnderstanding      understanding.QuestionUnderstanding `json:"goldUnderstanding"`
	PredictedUnderstanding understanding.QuestionUnderstanding `json:"predictedUnderstanding"`
	GoldBindings           []Binding                           `json:"goldBindings"`
	PredictedBindings      []BindingPrediction                 `json:"predictedBindings"`
}

// PRFScore reports micro counts and their derived precision, recall and F1.
// A metric with a zero denominator is represented as 0 rather than NaN; the
// counts make the absence of gold or predicted items explicit.
type PRFScore struct {
	TruePositive  int     `json:"truePositive"`
	FalsePositive int     `json:"falsePositive"`
	FalseNegative int     `json:"falseNegative"`
	Gold          int     `json:"gold"`
	Predicted     int     `json:"predicted"`
	Precision     float64 `json:"precision"`
	Recall        float64 `json:"recall"`
	F1            float64 `json:"f1"`
}

type KindMetrics struct {
	Metric    PRFScore `json:"metric"`
	Dimension PRFScore `json:"dimension"`
	Member    PRFScore `json:"member"`
	Overall   PRFScore `json:"overall"`
}

type SliceSummary struct {
	CaseCount int         `json:"caseCount"`
	Mention   KindMetrics `json:"mention"`
	Binding   KindMetrics `json:"binding"`
}

type DomainSlice struct {
	DomainID askdata.ID   `json:"domainId"`
	Summary  SliceSummary `json:"summary"`
}

type ComplexitySlice struct {
	Complexity ComplexityClass `json:"complexity"`
	Summary    SliceSummary    `json:"summary"`
}

type AmbiguitySlice struct {
	Ambiguity AmbiguityClass `json:"ambiguity"`
	Summary   SliceSummary   `json:"summary"`
}

// CalibrationExample intentionally omits the question text. The stable case,
// object and original span are sufficient to audit labels without copying
// potentially sensitive prompts into model-training artifacts.
type CalibrationExample = calibration.CalibrationExample

type CalibrationInputs = calibration.CalibrationInputs

type BindingEvaluationReport struct {
	SchemaVersion string              `json:"schemaVersion"`
	CaseCount     int                 `json:"caseCount"`
	Overall       SliceSummary        `json:"overall"`
	ByDomain      []DomainSlice       `json:"byDomain"`
	ByComplexity  []ComplexitySlice   `json:"byComplexity"`
	ByAmbiguity   []AmbiguitySlice    `json:"byAmbiguity"`
	Calibration   CalibrationInputs   `json:"calibration"`
	ContentHash   askdata.ContentHash `json:"contentHash"`
}

type mentionKey struct {
	Kind  MentionKind
	Start int
	End   int
}

type bindingKey struct {
	MentionKey               mentionKey
	ObjectVersionID          askdata.ID
	ParentDimensionVersionID askdata.ID
	Role                     understanding.DimensionRole
}

type countTriple struct {
	TruePositive  int
	FalsePositive int
	FalseNegative int
}

type kindCounts struct {
	Metric    countTriple
	Dimension countTriple
	Member    countTriple
}

type summaryAccumulator struct {
	CaseCount int
	Mention   kindCounts
	Binding   kindCounts
}

type evaluatedCase struct {
	Mention     kindCounts
	Binding     kindCounts
	Calibration []CalibrationExample
}

func (evaluationCase BindingEvaluationCase) Validate() error {
	if evaluationCase.SchemaVersion != BindingEvaluationVersion {
		return fmt.Errorf("schemaVersion must be %q", BindingEvaluationVersion)
	}
	if err := evaluationCase.CaseID.Validate(); err != nil {
		return fmt.Errorf("caseId: %w", err)
	}
	if !validDatasetSplit(evaluationCase.Split) {
		return errors.New("split is invalid")
	}
	if err := evaluationCase.DomainID.Validate(); err != nil {
		return fmt.Errorf("domainId: %w", err)
	}
	if !validComplexity(evaluationCase.Complexity) {
		return errors.New("complexity is invalid")
	}
	if !validAmbiguity(evaluationCase.Ambiguity) {
		return errors.New("ambiguity is invalid")
	}
	if err := evaluationCase.GoldUnderstanding.Validate(); err != nil {
		return fmt.Errorf("goldUnderstanding: %w", err)
	}
	if err := evaluationCase.PredictedUnderstanding.Validate(); err != nil {
		return fmt.Errorf("predictedUnderstanding: %w", err)
	}
	if evaluationCase.GoldUnderstanding.Question != evaluationCase.PredictedUnderstanding.Question {
		return errors.New("gold and predicted understanding must reference the exact same question")
	}
	if err := validateUniqueMentions(evaluationCase.GoldUnderstanding); err != nil {
		return fmt.Errorf("goldUnderstanding: %w", err)
	}
	if err := validateUniqueMentions(evaluationCase.PredictedUnderstanding); err != nil {
		return fmt.Errorf("predictedUnderstanding: %w", err)
	}
	if err := validateBindings(evaluationCase.GoldUnderstanding, evaluationCase.GoldBindings, "goldBindings"); err != nil {
		return err
	}
	predicted := make([]Binding, len(evaluationCase.PredictedBindings))
	for index, prediction := range evaluationCase.PredictedBindings {
		predicted[index] = prediction.Binding
		if err := prediction.Features.Validate(); err != nil {
			return fmt.Errorf("predictedBindings[%d].features: %w", index, err)
		}
	}
	if err := validateBindings(evaluationCase.PredictedUnderstanding, predicted, "predictedBindings"); err != nil {
		return err
	}
	return nil
}

// EvaluateBindings computes exact Unicode-span mention metrics and stable-ID
// binding metrics, then micro-aggregates them globally and by the requested
// gold strata. Input order never changes the report or its hash.
func EvaluateBindings(cases []BindingEvaluationCase) (BindingEvaluationReport, error) {
	if cases == nil || len(cases) == 0 || len(cases) > MaxBindingCases {
		return BindingEvaluationReport{}, fmt.Errorf("%w: case count must be between 1 and %d", ErrInvalidBindingEvaluation, MaxBindingCases)
	}
	normalizedCases := append([]BindingEvaluationCase(nil), cases...)
	sort.Slice(normalizedCases, func(i, j int) bool { return normalizedCases[i].CaseID < normalizedCases[j].CaseID })
	for index, evaluationCase := range normalizedCases {
		if err := evaluationCase.Validate(); err != nil {
			return BindingEvaluationReport{}, fmt.Errorf("%w: cases[%d]: %v", ErrInvalidBindingEvaluation, index, err)
		}
		if index > 0 && normalizedCases[index-1].CaseID == evaluationCase.CaseID {
			return BindingEvaluationReport{}, fmt.Errorf("%w: %s", ErrDuplicateEvaluationCase, evaluationCase.CaseID)
		}
	}

	overall := &summaryAccumulator{}
	byDomain := make(map[askdata.ID]*summaryAccumulator)
	byComplexity := make(map[ComplexityClass]*summaryAccumulator)
	byAmbiguity := make(map[AmbiguityClass]*summaryAccumulator)
	training := make([]CalibrationExample, 0)
	validation := make([]CalibrationExample, 0)
	for _, evaluationCase := range normalizedCases {
		evaluated, err := evaluateCase(evaluationCase)
		if err != nil {
			return BindingEvaluationReport{}, fmt.Errorf("%w: case %s: %v", ErrInvalidBindingEvaluation, evaluationCase.CaseID, err)
		}
		addEvaluatedCase(overall, evaluated)
		addEvaluatedCase(accumulatorFor(byDomain, evaluationCase.DomainID), evaluated)
		addEvaluatedCase(accumulatorFor(byComplexity, evaluationCase.Complexity), evaluated)
		addEvaluatedCase(accumulatorFor(byAmbiguity, evaluationCase.Ambiguity), evaluated)
		switch evaluationCase.Split {
		case DatasetSplitTrain:
			training = append(training, evaluated.Calibration...)
		case DatasetSplitValidation:
			validation = append(validation, evaluated.Calibration...)
		}
	}
	if len(training)+len(validation) > MaxCalibrationExamples {
		return BindingEvaluationReport{}, fmt.Errorf("%w: calibration examples exceed %d", ErrInvalidBindingEvaluation, MaxCalibrationExamples)
	}
	sort.Slice(training, func(i, j int) bool { return calibrationExampleLess(training[i], training[j]) })
	sort.Slice(validation, func(i, j int) bool { return calibrationExampleLess(validation[i], validation[j]) })

	report := BindingEvaluationReport{
		SchemaVersion: BindingEvaluationVersion,
		CaseCount:     len(normalizedCases),
		Overall:       overall.summary(),
		ByDomain:      domainSlices(byDomain),
		ByComplexity:  complexitySlices(byComplexity),
		ByAmbiguity:   ambiguitySlices(byAmbiguity),
		Calibration: CalibrationInputs{
			Training:   training,
			Validation: validation,
		},
	}
	payload, err := bindingReportPayload(report)
	if err != nil {
		return BindingEvaluationReport{}, err
	}
	if len(payload) > MaxBindingReportBytes {
		return BindingEvaluationReport{}, fmt.Errorf("%w: report exceeds %d bytes", ErrInvalidBindingEvaluation, MaxBindingReportBytes)
	}
	report.ContentHash = askdata.HashBytes(payload)
	if err := report.Validate(); err != nil {
		return BindingEvaluationReport{}, err
	}
	return report, nil
}

func (report BindingEvaluationReport) Validate() error {
	if report.SchemaVersion != BindingEvaluationVersion || report.CaseCount < 1 || report.CaseCount > MaxBindingCases || report.Overall.CaseCount != report.CaseCount {
		return ErrInvalidBindingEvaluation
	}
	if err := report.Overall.validate(); err != nil {
		return fmt.Errorf("%w: overall: %v", ErrInvalidBindingEvaluation, err)
	}
	if err := validateDomainSlices(report.ByDomain, report.Overall); err != nil {
		return fmt.Errorf("%w: byDomain: %v", ErrInvalidBindingEvaluation, err)
	}
	if err := validateComplexitySlices(report.ByComplexity, report.Overall); err != nil {
		return fmt.Errorf("%w: byComplexity: %v", ErrInvalidBindingEvaluation, err)
	}
	if err := validateAmbiguitySlices(report.ByAmbiguity, report.Overall); err != nil {
		return fmt.Errorf("%w: byAmbiguity: %v", ErrInvalidBindingEvaluation, err)
	}
	if report.Calibration.Training == nil || report.Calibration.Validation == nil || len(report.Calibration.Training)+len(report.Calibration.Validation) > MaxCalibrationExamples {
		return fmt.Errorf("%w: calibration inputs are invalid", ErrInvalidBindingEvaluation)
	}
	if err := validateCalibrationExamples(report.Calibration.Training); err != nil {
		return fmt.Errorf("%w: calibration.training: %v", ErrInvalidBindingEvaluation, err)
	}
	if err := validateCalibrationExamples(report.Calibration.Validation); err != nil {
		return fmt.Errorf("%w: calibration.validation: %v", ErrInvalidBindingEvaluation, err)
	}
	if err := report.ContentHash.Validate(); err != nil {
		return fmt.Errorf("%w: contentHash: %v", ErrInvalidBindingEvaluation, err)
	}
	payload, err := bindingReportPayload(report)
	if err != nil {
		return err
	}
	if len(payload) > MaxBindingReportBytes || report.ContentHash != askdata.HashBytes(payload) {
		return fmt.Errorf("%w: contentHash does not match report", ErrInvalidBindingEvaluation)
	}
	return nil
}

// ValidateAgainst deterministically replays the evaluation, detecting a
// self-consistent but incorrect report as well as ordinary hash tampering.
func (report BindingEvaluationReport) ValidateAgainst(cases []BindingEvaluationCase) error {
	if err := report.Validate(); err != nil {
		return err
	}
	expected, err := EvaluateBindings(cases)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report, expected) {
		return fmt.Errorf("%w: report does not match replayed cases", ErrInvalidBindingEvaluation)
	}
	return nil
}

func evaluateCase(evaluationCase BindingEvaluationCase) (evaluatedCase, error) {
	goldMentions := mentionSets(evaluationCase.GoldUnderstanding)
	predictedMentions := mentionSets(evaluationCase.PredictedUnderstanding)
	result := evaluatedCase{}
	for _, kind := range []MentionKind{MentionMetric, MentionDimension, MentionMember} {
		result.Mention.add(kind, compareSets(goldMentions[kind], predictedMentions[kind]))
	}

	goldBindings := make(map[bindingKey]struct{}, len(evaluationCase.GoldBindings))
	for _, binding := range evaluationCase.GoldBindings {
		key, err := resolveBinding(evaluationCase.GoldUnderstanding, binding)
		if err != nil {
			return evaluatedCase{}, err
		}
		goldBindings[key] = struct{}{}
	}
	predictedBindings := make(map[bindingKey]struct{}, len(evaluationCase.PredictedBindings))
	calibration := make([]CalibrationExample, 0, len(evaluationCase.PredictedBindings))
	for _, prediction := range evaluationCase.PredictedBindings {
		key, err := resolveBinding(evaluationCase.PredictedUnderstanding, prediction.Binding)
		if err != nil {
			return evaluatedCase{}, err
		}
		predictedBindings[key] = struct{}{}
		_, correct := goldBindings[key]
		calibration = append(calibration, calibrationExample(evaluationCase, prediction, key, correct))
	}
	for _, kind := range []MentionKind{MentionMetric, MentionDimension, MentionMember} {
		result.Binding.add(kind, compareBindingSets(kind, goldBindings, predictedBindings))
	}
	result.Calibration = calibration
	return result, nil
}

func validateUniqueMentions(value understanding.QuestionUnderstanding) error {
	sets := mentionSets(value)
	if len(sets[MentionMetric]) != len(value.MetricMentions) {
		return errors.New("metricMentions contain a duplicate span")
	}
	if len(sets[MentionDimension]) != len(value.DimensionMentions) {
		return errors.New("dimensionMentions contain a duplicate span")
	}
	if len(sets[MentionMember]) != len(value.ValueMentions) {
		return errors.New("valueMentions contain a duplicate span")
	}
	return nil
}

func validateBindings(value understanding.QuestionUnderstanding, bindings []Binding, path string) error {
	if len(bindings) > understanding.MaxMetricMentions+understanding.MaxDimensionMentions+understanding.MaxValueMentions {
		return fmt.Errorf("%s contains too many bindings", path)
	}
	seen := make(map[struct {
		kind  MentionKind
		index int
	}]struct{}, len(bindings))
	for index, binding := range bindings {
		identity := struct {
			kind  MentionKind
			index int
		}{binding.MentionKind, binding.MentionIndex}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%s[%d] duplicates a mention binding", path, index)
		}
		seen[identity] = struct{}{}
		if _, err := resolveBinding(value, binding); err != nil {
			return fmt.Errorf("%s[%d]: %w", path, index, err)
		}
	}
	return nil
}

func resolveBinding(value understanding.QuestionUnderstanding, binding Binding) (bindingKey, error) {
	if err := binding.ObjectVersionID.Validate(); err != nil {
		return bindingKey{}, fmt.Errorf("objectVersionId: %w", err)
	}
	if binding.MentionIndex < 0 {
		return bindingKey{}, errors.New("mentionIndex is invalid")
	}
	key := bindingKey{ObjectVersionID: binding.ObjectVersionID}
	switch binding.MentionKind {
	case MentionMetric:
		if binding.MentionIndex >= len(value.MetricMentions) {
			return bindingKey{}, errors.New("mentionIndex does not reference a metric mention")
		}
		if binding.ParentDimensionVersionID != nil || binding.Role != nil {
			return bindingKey{}, errors.New("metric binding cannot carry a parent dimension or role")
		}
		span := value.MetricMentions[binding.MentionIndex].Span
		key.MentionKey = mentionKey{MentionMetric, span.Start, span.End}
	case MentionDimension:
		if binding.MentionIndex >= len(value.DimensionMentions) {
			return bindingKey{}, errors.New("mentionIndex does not reference a dimension mention")
		}
		if binding.ParentDimensionVersionID != nil || binding.Role == nil {
			return bindingKey{}, errors.New("dimension binding requires a role and cannot carry a parent dimension")
		}
		mention := value.DimensionMentions[binding.MentionIndex]
		if *binding.Role != mention.Role {
			return bindingKey{}, errors.New("dimension binding role must match its mention role")
		}
		span := mention.Span
		key.MentionKey = mentionKey{MentionDimension, span.Start, span.End}
		key.Role = *binding.Role
	case MentionMember:
		if binding.MentionIndex >= len(value.ValueMentions) {
			return bindingKey{}, errors.New("mentionIndex does not reference a member mention")
		}
		if binding.ParentDimensionVersionID == nil || binding.Role != nil {
			return bindingKey{}, errors.New("member binding requires a parent dimension and cannot carry a role")
		}
		if err := binding.ParentDimensionVersionID.Validate(); err != nil {
			return bindingKey{}, fmt.Errorf("parentDimensionVersionId: %w", err)
		}
		span := value.ValueMentions[binding.MentionIndex].Span
		key.MentionKey = mentionKey{MentionMember, span.Start, span.End}
		key.ParentDimensionVersionID = *binding.ParentDimensionVersionID
	default:
		return bindingKey{}, errors.New("mentionKind is invalid")
	}
	return key, nil
}

func mentionSets(value understanding.QuestionUnderstanding) map[MentionKind]map[mentionKey]struct{} {
	sets := map[MentionKind]map[mentionKey]struct{}{
		MentionMetric: {}, MentionDimension: {}, MentionMember: {},
	}
	for _, mention := range value.MetricMentions {
		sets[MentionMetric][mentionKey{MentionMetric, mention.Span.Start, mention.Span.End}] = struct{}{}
	}
	for _, mention := range value.DimensionMentions {
		sets[MentionDimension][mentionKey{MentionDimension, mention.Span.Start, mention.Span.End}] = struct{}{}
	}
	for _, mention := range value.ValueMentions {
		sets[MentionMember][mentionKey{MentionMember, mention.Span.Start, mention.Span.End}] = struct{}{}
	}
	return sets
}

func compareSets[T comparable](gold, predicted map[T]struct{}) countTriple {
	counts := countTriple{}
	for key := range predicted {
		if _, exists := gold[key]; exists {
			counts.TruePositive++
		} else {
			counts.FalsePositive++
		}
	}
	for key := range gold {
		if _, exists := predicted[key]; !exists {
			counts.FalseNegative++
		}
	}
	return counts
}

func compareBindingSets(kind MentionKind, gold, predicted map[bindingKey]struct{}) countTriple {
	goldForKind := make(map[bindingKey]struct{})
	predictedForKind := make(map[bindingKey]struct{})
	for key := range gold {
		if key.MentionKey.Kind == kind {
			goldForKind[key] = struct{}{}
		}
	}
	for key := range predicted {
		if key.MentionKey.Kind == kind {
			predictedForKind[key] = struct{}{}
		}
	}
	return compareSets(goldForKind, predictedForKind)
}

func calibrationExample(evaluationCase BindingEvaluationCase, prediction BindingPrediction, key bindingKey, correct bool) CalibrationExample {
	example := CalibrationExample{
		CaseID: evaluationCase.CaseID, DomainID: evaluationCase.DomainID,
		Complexity: evaluationCase.Complexity, Ambiguity: evaluationCase.Ambiguity,
		MentionKind:     key.MentionKey.Kind,
		MentionSpan:     understanding.Span{Start: key.MentionKey.Start, End: key.MentionKey.End},
		ObjectVersionID: key.ObjectVersionID, Features: prediction.Features, Correct: correct,
	}
	if key.ParentDimensionVersionID != "" {
		parent := key.ParentDimensionVersionID
		example.ParentDimensionVersionID = &parent
	}
	if key.Role != "" {
		role := key.Role
		example.Role = &role
	}
	return example
}

func (counts *kindCounts) add(kind MentionKind, delta countTriple) {
	switch kind {
	case MentionMetric:
		counts.Metric.add(delta)
	case MentionDimension:
		counts.Dimension.add(delta)
	case MentionMember:
		counts.Member.add(delta)
	}
}

func (counts *countTriple) add(delta countTriple) {
	counts.TruePositive += delta.TruePositive
	counts.FalsePositive += delta.FalsePositive
	counts.FalseNegative += delta.FalseNegative
}

func (counts kindCounts) metrics() KindMetrics {
	overall := counts.Metric
	overall.add(counts.Dimension)
	overall.add(counts.Member)
	return KindMetrics{
		Metric: counts.Metric.score(), Dimension: counts.Dimension.score(),
		Member: counts.Member.score(), Overall: overall.score(),
	}
}

func (counts countTriple) score() PRFScore {
	precision := ratio(counts.TruePositive, counts.TruePositive+counts.FalsePositive)
	recall := ratio(counts.TruePositive, counts.TruePositive+counts.FalseNegative)
	f1 := ratio(2*counts.TruePositive, 2*counts.TruePositive+counts.FalsePositive+counts.FalseNegative)
	return PRFScore{
		TruePositive: counts.TruePositive, FalsePositive: counts.FalsePositive, FalseNegative: counts.FalseNegative,
		Gold: counts.TruePositive + counts.FalseNegative, Predicted: counts.TruePositive + counts.FalsePositive,
		Precision: precision, Recall: recall, F1: f1,
	}
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func addEvaluatedCase(accumulator *summaryAccumulator, evaluated evaluatedCase) {
	accumulator.CaseCount++
	accumulator.Mention.addAll(evaluated.Mention)
	accumulator.Binding.addAll(evaluated.Binding)
}

func (counts *kindCounts) addAll(delta kindCounts) {
	counts.Metric.add(delta.Metric)
	counts.Dimension.add(delta.Dimension)
	counts.Member.add(delta.Member)
}

func (accumulator summaryAccumulator) summary() SliceSummary {
	return SliceSummary{CaseCount: accumulator.CaseCount, Mention: accumulator.Mention.metrics(), Binding: accumulator.Binding.metrics()}
}

func accumulatorFor[K comparable](values map[K]*summaryAccumulator, key K) *summaryAccumulator {
	value := values[key]
	if value == nil {
		value = &summaryAccumulator{}
		values[key] = value
	}
	return value
}

func domainSlices(values map[askdata.ID]*summaryAccumulator) []DomainSlice {
	keys := make([]askdata.ID, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]DomainSlice, len(keys))
	for index, key := range keys {
		result[index] = DomainSlice{DomainID: key, Summary: values[key].summary()}
	}
	return result
}

func complexitySlices(values map[ComplexityClass]*summaryAccumulator) []ComplexitySlice {
	keys := make([]ComplexityClass, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]ComplexitySlice, len(keys))
	for index, key := range keys {
		result[index] = ComplexitySlice{Complexity: key, Summary: values[key].summary()}
	}
	return result
}

func ambiguitySlices(values map[AmbiguityClass]*summaryAccumulator) []AmbiguitySlice {
	keys := make([]AmbiguityClass, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]AmbiguitySlice, len(keys))
	for index, key := range keys {
		result[index] = AmbiguitySlice{Ambiguity: key, Summary: values[key].summary()}
	}
	return result
}

func (summary SliceSummary) validate() error {
	if summary.CaseCount < 1 {
		return errors.New("caseCount must be positive")
	}
	if err := summary.Mention.validate(); err != nil {
		return fmt.Errorf("mention: %w", err)
	}
	if err := summary.Binding.validate(); err != nil {
		return fmt.Errorf("binding: %w", err)
	}
	return nil
}

func (metrics KindMetrics) validate() error {
	for name, score := range map[string]PRFScore{
		"metric": metrics.Metric, "dimension": metrics.Dimension, "member": metrics.Member, "overall": metrics.Overall,
	} {
		if err := score.validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	expected := countTriple{
		TruePositive:  metrics.Metric.TruePositive + metrics.Dimension.TruePositive + metrics.Member.TruePositive,
		FalsePositive: metrics.Metric.FalsePositive + metrics.Dimension.FalsePositive + metrics.Member.FalsePositive,
		FalseNegative: metrics.Metric.FalseNegative + metrics.Dimension.FalseNegative + metrics.Member.FalseNegative,
	}.score()
	if !reflect.DeepEqual(metrics.Overall, expected) {
		return errors.New("overall score does not equal the sum of kind counts")
	}
	return nil
}

func (score PRFScore) validate() error {
	if score.TruePositive < 0 || score.FalsePositive < 0 || score.FalseNegative < 0 || score.Gold < 0 || score.Predicted < 0 {
		return errors.New("counts cannot be negative")
	}
	expected := countTriple{score.TruePositive, score.FalsePositive, score.FalseNegative}.score()
	if !reflect.DeepEqual(score, expected) {
		return errors.New("precision, recall or F1 does not match counts")
	}
	return nil
}

func validateDomainSlices(slices []DomainSlice, overall SliceSummary) error {
	if slices == nil || len(slices) == 0 {
		return errors.New("at least one domain slice is required")
	}
	summaries := make([]SliceSummary, len(slices))
	previous := askdata.ID("")
	for index, slice := range slices {
		if err := slice.DomainID.Validate(); err != nil {
			return fmt.Errorf("[%d].domainId: %w", index, err)
		}
		if index > 0 && slice.DomainID <= previous {
			return errors.New("domain slices must be sorted and unique")
		}
		previous = slice.DomainID
		summaries[index] = slice.Summary
	}
	return validatePartition(summaries, overall)
}

func validateComplexitySlices(slices []ComplexitySlice, overall SliceSummary) error {
	if slices == nil || len(slices) == 0 {
		return errors.New("at least one complexity slice is required")
	}
	summaries := make([]SliceSummary, len(slices))
	previous := ComplexityClass("")
	for index, slice := range slices {
		if !validComplexity(slice.Complexity) {
			return fmt.Errorf("[%d].complexity is invalid", index)
		}
		if index > 0 && slice.Complexity <= previous {
			return errors.New("complexity slices must be sorted and unique")
		}
		previous = slice.Complexity
		summaries[index] = slice.Summary
	}
	return validatePartition(summaries, overall)
}

func validateAmbiguitySlices(slices []AmbiguitySlice, overall SliceSummary) error {
	if slices == nil || len(slices) == 0 {
		return errors.New("at least one ambiguity slice is required")
	}
	summaries := make([]SliceSummary, len(slices))
	previous := AmbiguityClass("")
	for index, slice := range slices {
		if !validAmbiguity(slice.Ambiguity) {
			return fmt.Errorf("[%d].ambiguity is invalid", index)
		}
		if index > 0 && slice.Ambiguity <= previous {
			return errors.New("ambiguity slices must be sorted and unique")
		}
		previous = slice.Ambiguity
		summaries[index] = slice.Summary
	}
	return validatePartition(summaries, overall)
}

func validatePartition(summaries []SliceSummary, overall SliceSummary) error {
	accumulator := summaryAccumulator{}
	for _, summary := range summaries {
		if err := summary.validate(); err != nil {
			return err
		}
		accumulator.CaseCount += summary.CaseCount
		accumulator.Mention.addAll(countsFromMetrics(summary.Mention))
		accumulator.Binding.addAll(countsFromMetrics(summary.Binding))
	}
	if !reflect.DeepEqual(accumulator.summary(), overall) {
		return errors.New("slice totals do not match overall totals")
	}
	return nil
}

func countsFromMetrics(metrics KindMetrics) kindCounts {
	return kindCounts{
		Metric:    countTriple{metrics.Metric.TruePositive, metrics.Metric.FalsePositive, metrics.Metric.FalseNegative},
		Dimension: countTriple{metrics.Dimension.TruePositive, metrics.Dimension.FalsePositive, metrics.Dimension.FalseNegative},
		Member:    countTriple{metrics.Member.TruePositive, metrics.Member.FalsePositive, metrics.Member.FalseNegative},
	}
}

func validateCalibrationExamples(examples []CalibrationExample) error {
	for index, example := range examples {
		if index > 0 && !calibrationExampleLess(examples[index-1], example) {
			return errors.New("examples must be sorted and unique")
		}
		if err := example.CaseID.Validate(); err != nil {
			return fmt.Errorf("[%d].caseId: %w", index, err)
		}
		if err := example.DomainID.Validate(); err != nil {
			return fmt.Errorf("[%d].domainId: %w", index, err)
		}
		if !validComplexity(example.Complexity) || !validAmbiguity(example.Ambiguity) || !validMentionKind(example.MentionKind) {
			return fmt.Errorf("[%d] has invalid classification", index)
		}
		if example.MentionSpan.Start < 0 || example.MentionSpan.End <= example.MentionSpan.Start || example.MentionSpan.End > understanding.MaxQuestionRunes {
			return fmt.Errorf("[%d].mentionSpan is invalid", index)
		}
		binding := Binding{
			MentionKind: example.MentionKind, MentionIndex: 0, ObjectVersionID: example.ObjectVersionID,
			ParentDimensionVersionID: example.ParentDimensionVersionID, Role: example.Role,
		}
		if err := validateBindingShape(binding); err != nil {
			return fmt.Errorf("[%d]: %w", index, err)
		}
		if err := example.Features.Validate(); err != nil {
			return fmt.Errorf("[%d].features: %w", index, err)
		}
	}
	return nil
}

func validateBindingShape(binding Binding) error {
	if err := binding.ObjectVersionID.Validate(); err != nil {
		return fmt.Errorf("objectVersionId: %w", err)
	}
	switch binding.MentionKind {
	case MentionMetric:
		if binding.ParentDimensionVersionID != nil || binding.Role != nil {
			return errors.New("metric binding shape is invalid")
		}
	case MentionDimension:
		if binding.ParentDimensionVersionID != nil || binding.Role == nil || !validDimensionRole(*binding.Role) {
			return errors.New("dimension binding shape is invalid")
		}
	case MentionMember:
		if binding.ParentDimensionVersionID == nil || binding.Role != nil {
			return errors.New("member binding shape is invalid")
		}
		if err := binding.ParentDimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("parentDimensionVersionId: %w", err)
		}
	default:
		return errors.New("mentionKind is invalid")
	}
	return nil
}

func calibrationExampleLess(left, right CalibrationExample) bool {
	leftKey := fmt.Sprintf("%s\x00%s\x00%08d\x00%08d\x00%s\x00%s\x00%s", left.CaseID, left.MentionKind, left.MentionSpan.Start, left.MentionSpan.End, left.ObjectVersionID, optionalID(left.ParentDimensionVersionID), optionalRole(left.Role))
	rightKey := fmt.Sprintf("%s\x00%s\x00%08d\x00%08d\x00%s\x00%s\x00%s", right.CaseID, right.MentionKind, right.MentionSpan.Start, right.MentionSpan.End, right.ObjectVersionID, optionalID(right.ParentDimensionVersionID), optionalRole(right.Role))
	return leftKey < rightKey
}

func optionalID(value *askdata.ID) askdata.ID {
	if value == nil {
		return ""
	}
	return *value
}

func optionalRole(value *understanding.DimensionRole) understanding.DimensionRole {
	if value == nil {
		return ""
	}
	return *value
}

func bindingReportPayload(report BindingEvaluationReport) ([]byte, error) {
	payload := struct {
		SchemaVersion string            `json:"schemaVersion"`
		CaseCount     int               `json:"caseCount"`
		Overall       SliceSummary      `json:"overall"`
		ByDomain      []DomainSlice     `json:"byDomain"`
		ByComplexity  []ComplexitySlice `json:"byComplexity"`
		ByAmbiguity   []AmbiguitySlice  `json:"byAmbiguity"`
		Calibration   CalibrationInputs `json:"calibration"`
	}{report.SchemaVersion, report.CaseCount, report.Overall, report.ByDomain, report.ByComplexity, report.ByAmbiguity, report.Calibration}
	return json.Marshal(payload)
}

func validDatasetSplit(value DatasetSplit) bool {
	return value == DatasetSplitTrain || value == DatasetSplitValidation || value == DatasetSplitSealed || value == DatasetSplitProductionRegression
}

func validComplexity(value ComplexityClass) bool {
	return value == ComplexitySimple || value == ComplexityComposite || value == ComplexityContextual || value == ComplexityRelational
}

func validAmbiguity(value AmbiguityClass) bool {
	return value == AmbiguityNone || value == AmbiguityMetric || value == AmbiguityDimension || value == AmbiguityMember || value == AmbiguityCrossDomain || value == AmbiguityMultiple
}

func validMentionKind(value MentionKind) bool {
	return value == MentionMetric || value == MentionDimension || value == MentionMember
}

func validDimensionRole(value understanding.DimensionRole) bool {
	return value == understanding.DimensionRoleGroupBy || value == understanding.DimensionRoleFilter || value == understanding.DimensionRoleTime || value == understanding.DimensionRoleSort
}
