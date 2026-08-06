package binding

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

const (
	ConfidenceDecisionVersion = "binding-confidence-decision-v1"
	MaxClarificationOptions   = 3
)

var (
	ErrInvalidConfidenceDecision = errors.New("binding confidence decision is invalid")
	unsafeClarificationText      = regexp.MustCompile(`(?is)\b(select\s+.+\s+from|insert\s+into|update\s+.+\s+set|delete\s+from|match\s*\(.+\)\s*(return|where))\b`)
)

type Disposition string

const (
	DispositionDirect           Disposition = "DIRECT"
	DispositionClarify          Disposition = "CLARIFY"
	DispositionEvidenceRequired Disposition = "EVIDENCE_REQUIRED"
	DispositionNoMatch          Disposition = "NO_MATCH"
)

// BundlePresentation is a sanitized, evidence-backed registry projection. It
// is not model-authored free text and cannot introduce another bundle or cite
// evidence absent from that bundle.
type BundlePresentation struct {
	BundleHash   askdata.ContentHash   `json:"bundleHash"`
	Label        string                `json:"label"`
	Difference   string                `json:"difference"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type DecisionRequest struct {
	BindingRequest Request              `json:"bindingRequest"`
	BindingResult  Result               `json:"bindingResult"`
	Presentations  []BundlePresentation `json:"presentations"`
}

type CalibratedBundle struct {
	BundleHash  askdata.ContentHash `json:"bundleHash"`
	Rank        int                 `json:"rank"`
	Probability float64             `json:"probability"`
	Margin      float64             `json:"margin"`
}

type ClarificationOption struct {
	OptionID            askdata.ID            `json:"optionId"`
	BundleHash          askdata.ContentHash   `json:"bundleHash"`
	Label               string                `json:"label"`
	Difference          string                `json:"difference"`
	MetricVersionIDs    []askdata.ID          `json:"metricVersionIds"`
	DimensionVersionIDs []askdata.ID          `json:"dimensionVersionIds"`
	MemberVersionIDs    []askdata.ID          `json:"memberVersionIds"`
	ModelVersionIDs     []askdata.ID          `json:"modelVersionIds"`
	EvidenceRefs        []askdata.EvidenceRef `json:"evidenceRefs"`
}

type Clarification struct {
	ConflictCode string                `json:"conflictCode"`
	Question     string                `json:"question"`
	Options      []ClarificationOption `json:"options"`
}

func (clarification Clarification) ToolArguments(
	release askdata.ReleaseRef,
) (toolhost.ToolArguments, error) {
	arguments := toolhost.NewArguments(release)
	arguments.ConflictCode = &clarification.ConflictCode
	arguments.ClarificationQuestion = &clarification.Question
	arguments.ClarificationOptions = make([]toolhost.ClarificationOption, len(clarification.Options))
	for index, option := range clarification.Options {
		arguments.ClarificationOptions[index] = toolhost.ClarificationOption{
			OptionID: option.OptionID, Label: option.Label,
			EvidenceRefs: append([]askdata.EvidenceRef(nil), option.EvidenceRefs...),
		}
	}
	if err := arguments.ValidateFor(toolhost.ToolRequestClarification); err != nil {
		return toolhost.ToolArguments{}, err
	}
	return arguments, nil
}

type ConfidenceDecision struct {
	Version            string                     `json:"version"`
	BindingResultHash  askdata.ContentHash        `json:"bindingResultHash"`
	CalibrationHash    askdata.ContentHash        `json:"calibrationHash"`
	Disposition        Disposition                `json:"disposition"`
	Confidence         askdata.ConfidenceEvidence `json:"confidence"`
	CalibratedBundles  []CalibratedBundle         `json:"calibratedBundles"`
	SelectedBundleHash *askdata.ContentHash       `json:"selectedBundleHash,omitempty"`
	Clarification      *Clarification             `json:"clarification,omitempty"`
	DecisionHash       askdata.ContentHash        `json:"decisionHash"`
}

func (calibrator *Calibrator) Decide(request DecisionRequest) (ConfidenceDecision, error) {
	if calibrator == nil || calibrator.model.Validate() != nil {
		return ConfidenceDecision{}, ErrInvalidConfidenceDecision
	}
	if err := request.BindingResult.ValidateAgainst(request.BindingRequest); err != nil {
		return ConfidenceDecision{}, fmt.Errorf("%w: binding replay: %v", ErrInvalidConfidenceDecision, err)
	}
	presentations, err := normalizePresentations(request.BindingResult, request.Presentations)
	if err != nil {
		return ConfidenceDecision{}, err
	}
	request.Presentations = presentations
	modelEvidence, err := calibrator.model.EvidenceRef()
	if err != nil {
		return ConfidenceDecision{}, err
	}
	decision := ConfidenceDecision{
		Version: ConfidenceDecisionVersion, BindingResultHash: request.BindingResult.ResultHash,
		CalibrationHash: calibrator.model.ContentHash, CalibratedBundles: []CalibratedBundle{},
	}
	if request.BindingResult.NoMatch {
		decision.Disposition = DispositionNoMatch
		decision.Confidence = askdata.ConfidenceEvidence{
			Score: 0, Margin: 0, Evidence: []askdata.EvidenceRef{modelEvidence},
			ReasonCodes: []string{"NO_BINDING_BUNDLE"},
		}
		return finalizeConfidenceDecision(decision)
	}

	for index, bundle := range request.BindingResult.Bundles {
		margin := bundleMargin(request.BindingResult.Bundles, index)
		probability, probabilityErr := calibrator.model.Probability(runtimeFeatures(bundle, margin, index+1))
		if probabilityErr != nil {
			return ConfidenceDecision{}, probabilityErr
		}
		decision.CalibratedBundles = append(decision.CalibratedBundles, CalibratedBundle{
			BundleHash: bundle.BundleHash, Rank: index + 1,
			Probability: probability, Margin: margin,
		})
	}
	top := decision.CalibratedBundles[0]
	direct := calibrator.model.DirectEnabled && top.Probability >= calibrator.model.DirectThreshold &&
		top.Margin >= calibrator.model.MinDirectMargin
	reasonCodes := []string{}
	if direct {
		reasonCodes = append(reasonCodes, "CALIBRATED_DIRECT", "CANDIDATE_MARGIN_ACCEPTED")
	} else {
		if !calibrator.model.DirectEnabled {
			reasonCodes = append(reasonCodes, "VALIDATION_DIRECT_GATE_DISABLED")
		}
		if top.Probability < calibrator.model.DirectThreshold {
			reasonCodes = append(reasonCodes, "CALIBRATED_LOW_CONFIDENCE")
		}
		if top.Margin < calibrator.model.MinDirectMargin {
			reasonCodes = append(reasonCodes, "CANDIDATE_MARGIN_LOW")
		}
	}
	evidenceBundles := 1
	if !direct && len(request.BindingResult.Bundles) > 1 {
		evidenceBundles = minInt(MaxClarificationOptions, len(request.BindingResult.Bundles))
	}
	confidenceEvidence, err := boundedConfidenceEvidence(
		request.BindingResult.Bundles[:evidenceBundles], modelEvidence,
	)
	if err != nil {
		return ConfidenceDecision{}, err
	}
	decision.Confidence = askdata.ConfidenceEvidence{
		Score: top.Probability, Margin: top.Margin, Evidence: confidenceEvidence,
		ReasonCodes: normalizedReasonCodes(reasonCodes),
	}
	if direct {
		decision.Disposition = DispositionDirect
		selected := top.BundleHash
		decision.SelectedBundleHash = &selected
		return finalizeConfidenceDecision(decision)
	}
	if len(request.BindingResult.Bundles) < 2 {
		decision.Disposition = DispositionEvidenceRequired
		decision.Confidence.ReasonCodes = normalizedReasonCodes(append(
			decision.Confidence.ReasonCodes, "INSUFFICIENT_CLARIFICATION_OPTIONS",
		))
		return finalizeConfidenceDecision(decision)
	}
	decision.Disposition = DispositionClarify
	clarification, err := buildClarification(
		request.BindingResult.Bundles[:evidenceBundles], presentations,
	)
	if err != nil {
		return ConfidenceDecision{}, err
	}
	decision.Clarification = &clarification
	return finalizeConfidenceDecision(decision)
}

func (decision ConfidenceDecision) ValidateAgainst(
	calibrator *Calibrator,
	request DecisionRequest,
) error {
	expected, err := calibrator.Decide(request)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(decision, expected) {
		return ErrInvalidConfidenceDecision
	}
	return nil
}

func DecodeConfidenceDecision(
	raw []byte,
	calibrator *Calibrator,
	request DecisionRequest,
) (ConfidenceDecision, error) {
	var decision ConfidenceDecision
	if err := askdata.DecodeStrictJSON(raw, &decision); err != nil {
		return ConfidenceDecision{}, err
	}
	if err := decision.ValidateAgainst(calibrator, request); err != nil {
		return ConfidenceDecision{}, err
	}
	return decision, nil
}

func normalizePresentations(
	result Result,
	values []BundlePresentation,
) ([]BundlePresentation, error) {
	if result.NoMatch {
		if len(values) != 0 {
			return nil, fmt.Errorf("%w: no-match cannot have presentations", ErrInvalidConfidenceDecision)
		}
		return []BundlePresentation{}, nil
	}
	if len(values) != len(result.Bundles) {
		return nil, fmt.Errorf("%w: every bundle requires one presentation", ErrInvalidConfidenceDecision)
	}
	bundles := make(map[askdata.ContentHash]Bundle, len(result.Bundles))
	evidenceOccurrences := map[askdata.EvidenceRef]int{}
	for _, bundle := range result.Bundles {
		bundles[bundle.BundleHash] = bundle
		for _, evidence := range bundle.EvidenceRefs {
			evidenceOccurrences[evidence]++
		}
	}
	resultValues := append([]BundlePresentation(nil), values...)
	for index := range resultValues {
		resultValues[index].Label = strings.TrimSpace(resultValues[index].Label)
		resultValues[index].Difference = strings.TrimSpace(resultValues[index].Difference)
		resultValues[index].EvidenceRefs = normalizeEvidenceRefs(resultValues[index].EvidenceRefs)
	}
	sort.Slice(resultValues, func(i, j int) bool { return resultValues[i].BundleHash < resultValues[j].BundleHash })
	seenLabels := map[string]struct{}{}
	for index, presentation := range resultValues {
		bundle, exists := bundles[presentation.BundleHash]
		if !exists || presentation.BundleHash.Validate() != nil {
			return nil, fmt.Errorf("%w: presentations[%d] references another bundle", ErrInvalidConfidenceDecision, index)
		}
		if !safePresentationText(presentation.Label, 256) || !safePresentationText(presentation.Difference, 512) {
			return nil, fmt.Errorf("%w: presentations[%d] text", ErrInvalidConfidenceDecision, index)
		}
		labelKey := strings.ToLower(presentation.Label)
		if _, duplicate := seenLabels[labelKey]; duplicate {
			return nil, fmt.Errorf("%w: presentation labels must distinguish options", ErrInvalidConfidenceDecision)
		}
		seenLabels[labelKey] = struct{}{}
		if len(presentation.EvidenceRefs) < 1 || len(presentation.EvidenceRefs) > 16 ||
			!evidenceSubset(presentation.EvidenceRefs, bundle.EvidenceRefs) {
			return nil, fmt.Errorf("%w: presentations[%d] evidence", ErrInvalidConfidenceDecision, index)
		}
		if len(result.Bundles) > 1 {
			specific := false
			for _, evidence := range presentation.EvidenceRefs {
				if evidenceOccurrences[evidence] < len(result.Bundles) {
					specific = true
					break
				}
			}
			if !specific {
				return nil, fmt.Errorf("%w: presentations[%d] lacks option-specific evidence", ErrInvalidConfidenceDecision, index)
			}
		}
	}
	return resultValues, nil
}

func buildClarification(
	bundles []Bundle,
	presentations []BundlePresentation,
) (Clarification, error) {
	presentationByHash := make(map[askdata.ContentHash]BundlePresentation, len(presentations))
	for _, presentation := range presentations {
		presentationByHash[presentation.BundleHash] = presentation
	}
	code, question := bundleConflict(bundles[0], bundles[1])
	clarification := Clarification{ConflictCode: code, Question: question, Options: []ClarificationOption{}}
	for _, bundle := range bundles {
		presentation := presentationByHash[bundle.BundleHash]
		option := ClarificationOption{
			OptionID:   askdata.ID("clarification-option:" + string(bundle.BundleHash)),
			BundleHash: bundle.BundleHash, Label: presentation.Label, Difference: presentation.Difference,
			MetricVersionIDs: metricVersionIDs(bundle), DimensionVersionIDs: dimensionVersionIDs(bundle),
			MemberVersionIDs: memberVersionIDs(bundle), ModelVersionIDs: append([]askdata.ID(nil), bundle.ModelVersionIDs...),
			EvidenceRefs: append([]askdata.EvidenceRef(nil), presentation.EvidenceRefs...),
		}
		clarification.Options = append(clarification.Options, option)
	}
	if _, err := clarification.ToolArguments(askdata.ReleaseRef{
		ReleaseID: "validation-only", ContentHash: askdata.HashBytes([]byte("validation-only")),
	}); err != nil {
		return Clarification{}, err
	}
	return clarification, nil
}

func bundleConflict(left, right Bundle) (string, string) {
	if !reflect.DeepEqual(metricVersionIDs(left), metricVersionIDs(right)) {
		return "METRIC_DEFINITION_AMBIGUOUS", "检测到多个可用指标口径，请选择本次要使用的口径。"
	}
	if !reflect.DeepEqual(dimensionVersionIDs(left), dimensionVersionIDs(right)) {
		return "DIMENSION_ROLE_AMBIGUOUS", "检测到多个可用维度口径，请选择本次要使用的维度。"
	}
	if !reflect.DeepEqual(memberVersionIDs(left), memberVersionIDs(right)) {
		return "MEMBER_OWNERSHIP_AMBIGUOUS", "检测到同名成员的多个有效归属，请选择本次筛选口径。"
	}
	if !reflect.DeepEqual(left.ModelVersionIDs, right.ModelVersionIDs) ||
		!reflect.DeepEqual(left.GraphPath, right.GraphPath) {
		return "RELATIONSHIP_PATH_AMBIGUOUS", "检测到多个认证关系路径，请选择符合本次分析的关系口径。"
	}
	return "BINDING_BUNDLE_AMBIGUOUS", "存在多个证据接近的语义方案，请选择最符合本次问题的口径。"
}

func boundedConfidenceEvidence(
	bundles []Bundle,
	model askdata.EvidenceRef,
) ([]askdata.EvidenceRef, error) {
	values := []askdata.EvidenceRef{}
	for _, bundle := range bundles {
		values = append(values, bundle.EvidenceRefs...)
	}
	values = normalizeEvidenceRefs(values)
	if len(values) > askdata.MaxConfidenceProofs-1 {
		values = values[:askdata.MaxConfidenceProofs-1]
	}
	values = normalizeEvidenceRefs(append(values, model))
	seen := map[askdata.ID]askdata.EvidenceRef{}
	for _, value := range values {
		if previous, duplicate := seen[value.EvidenceID]; duplicate && previous != value {
			return nil, fmt.Errorf("%w: conflicting evidence identity", ErrInvalidConfidenceDecision)
		}
		seen[value.EvidenceID] = value
	}
	if len(values) > askdata.MaxConfidenceProofs {
		return nil, ErrInvalidConfidenceDecision
	}
	return values, nil
}

func evidenceSubset(values, allowed []askdata.EvidenceRef) bool {
	set := make(map[askdata.EvidenceRef]struct{}, len(allowed))
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if value.Validate() != nil {
			return false
		}
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func safePresentationText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum &&
		!unsafeClarificationText.MatchString(value) && !ai.ContainsSensitiveText(value)
}

func bundleMargin(values []Bundle, index int) float64 {
	if index+1 >= len(values) {
		return 0
	}
	margin := values[index].Score.Total - values[index+1].Score.Total
	if margin < 0 {
		return 0
	}
	if margin > 1 {
		return 1
	}
	return roundScore(margin)
}

func metricVersionIDs(bundle Bundle) []askdata.ID {
	values := make([]askdata.ID, len(bundle.MetricBindings))
	for index, binding := range bundle.MetricBindings {
		values[index] = binding.MetricVersionID
	}
	return normalizeIDs(values)
}

func dimensionVersionIDs(bundle Bundle) []askdata.ID {
	values := make([]askdata.ID, len(bundle.DimensionBindings))
	for index, binding := range bundle.DimensionBindings {
		values[index] = binding.DimensionVersionID
	}
	return normalizeIDs(values)
}

func memberVersionIDs(bundle Bundle) []askdata.ID {
	values := make([]askdata.ID, len(bundle.MemberBindings))
	for index, binding := range bundle.MemberBindings {
		values[index] = binding.MemberVersionID
	}
	return normalizeIDs(values)
}

func normalizedReasonCodes(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return []string{"CALIBRATION_DECISION_UNAVAILABLE"}
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func finalizeConfidenceDecision(decision ConfidenceDecision) (ConfidenceDecision, error) {
	if err := decision.Confidence.Validate(); err != nil {
		return ConfidenceDecision{}, err
	}
	decision.DecisionHash = ""
	payload, err := json.Marshal(decision)
	if err != nil {
		return ConfidenceDecision{}, err
	}
	decision.DecisionHash = askdata.HashBytes(payload)
	return decision, nil
}
