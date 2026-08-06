// Package binding jointly binds understood mentions to immutable semantic
// object versions. It consumes only replay-validated understanding, search
// evidence and GraphPlan contracts; it never accepts names or physical query
// fragments as executable identities.
package binding

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

const (
	BindingResultVersion    = "binding-bundle-result-v1"
	MaxCandidatesPerMention = 30
	DefaultBeamWidth        = 64
	DefaultTopBundles       = 10
	MaxBeamWidth            = 256
	MaxTopBundles           = 30
)

var (
	ErrInvalidBindingRequest = errors.New("joint binding request is invalid")
	ErrInvalidBindingResult  = errors.New("joint binding result is invalid")
)

type MentionKind string

const (
	MentionMetric    MentionKind = "METRIC"
	MentionDimension MentionKind = "DIMENSION"
	MentionMember    MentionKind = "MEMBER"
)

type CandidateSelectionSource string

const (
	SelectionLLMRerank          CandidateSelectionSource = "LLM_RERANK"
	SelectionDeterministicExact CandidateSelectionSource = "DETERMINISTIC_EXACT"
)

type MentionRef struct {
	Origin understanding.EvidenceOrigin `json:"origin"`
	Kind   MentionKind                  `json:"kind"`
	Index  int                          `json:"index"`
}

type CandidateOption struct {
	Candidate                search.Candidate         `json:"candidate"`
	ParentDimensionVersionID *askdata.ID              `json:"parentDimensionVersionId,omitempty"`
	SelectionSource          CandidateSelectionSource `json:"selectionSource"`
	ReviewerRank             int                      `json:"reviewerRank"`
	Gate                     search.DeterministicGate `json:"gate"`
	RuleScore                float64                  `json:"ruleScore"`
	QualityScore             float64                  `json:"qualityScore"`
	CostScore                float64                  `json:"costScore"`
	FeatureEvidenceRefs      []askdata.EvidenceRef    `json:"featureEvidenceRefs"`
}

type MentionCandidateSet struct {
	Mention    MentionRef          `json:"mention"`
	Evidence   askdata.EvidenceRef `json:"evidence"`
	Candidates []CandidateOption   `json:"candidates"`
}

type Config struct {
	BeamWidth  int `json:"beamWidth"`
	TopBundles int `json:"topBundles"`
}

func (config Config) normalize() (Config, error) {
	if config.BeamWidth == 0 {
		config.BeamWidth = DefaultBeamWidth
	}
	if config.TopBundles == 0 {
		config.TopBundles = DefaultTopBundles
	}
	if config.BeamWidth < 1 || config.BeamWidth > MaxBeamWidth ||
		config.TopBundles < 1 || config.TopBundles > MaxTopBundles ||
		config.TopBundles > config.BeamWidth {
		return Config{}, fmt.Errorf("%w: beam configuration", ErrInvalidBindingRequest)
	}
	return config, nil
}

type Request struct {
	UnderstandingRequest understanding.UnderstandingRequest `json:"understandingRequest"`
	UnderstandingResult  understanding.UnderstandingResult  `json:"understandingResult"`
	GraphRequest         graph.PlanRequest                  `json:"graphRequest"`
	GraphResolution      graph.Resolution                   `json:"graphResolution"`
	CandidateSets        []MentionCandidateSet              `json:"candidateSets"`
	Config               Config                             `json:"config"`
}

type MetricBinding struct {
	Mention         MentionRef                    `json:"mention"`
	MetricVersionID askdata.ID                    `json:"metricVersionId"`
	ModelVersionID  askdata.ID                    `json:"modelVersionId"`
	AggregationHint understanding.AggregationHint `json:"aggregationHint"`
	EvidenceRefs    []askdata.EvidenceRef         `json:"evidenceRefs"`
}

// Mention is nil only for a FILTER dimension implied and proven by a member
// ownership edge. It is never a model-invented semantic object.
type DimensionBinding struct {
	Mention            *MentionRef                 `json:"mention,omitempty"`
	DimensionVersionID askdata.ID                  `json:"dimensionVersionId"`
	Role               understanding.DimensionRole `json:"role"`
	Grain              *understanding.TimeGrain    `json:"grain,omitempty"`
	EvidenceRefs       []askdata.EvidenceRef       `json:"evidenceRefs"`
}

type MemberBinding struct {
	Mention            MentionRef                      `json:"mention"`
	MemberVersionID    askdata.ID                      `json:"memberVersionId"`
	DimensionVersionID askdata.ID                      `json:"dimensionVersionId"`
	OperatorHint       understanding.ValueOperatorHint `json:"operatorHint"`
	EvidenceRefs       []askdata.EvidenceRef           `json:"evidenceRefs"`
}

type TimeBinding struct {
	Origin understanding.EvidenceOrigin    `json:"origin"`
	Value  understanding.TimeUnderstanding `json:"value"`
}

type Bundle struct {
	MetricBindings         []MetricBinding        `json:"metricBindings"`
	DimensionBindings      []DimensionBinding     `json:"dimensionBindings"`
	MemberBindings         []MemberBinding        `json:"memberBindings"`
	Time                   *TimeBinding           `json:"time,omitempty"`
	ModelVersionIDs        []askdata.ID           `json:"modelVersionIds"`
	GraphPath              *graph.JoinPath        `json:"graphPath,omitempty"`
	GraphSource            graph.ResolutionSource `json:"graphSource"`
	GraphDegraded          bool                   `json:"graphDegraded"`
	GraphDegradationReason string                 `json:"graphDegradationReason,omitempty"`
	Score                  Score                  `json:"score"`
	EvidenceRefs           []askdata.EvidenceRef  `json:"evidenceRefs"`
	BundleHash             askdata.ContentHash    `json:"bundleHash"`
}

type BlockedCandidate struct {
	Mention         MentionRef            `json:"mention"`
	ObjectVersionID askdata.ID            `json:"objectVersionId"`
	ReasonCodes     []string              `json:"reasonCodes"`
	EvidenceRefs    []askdata.EvidenceRef `json:"evidenceRefs"`
}

type Result struct {
	Version           string              `json:"version"`
	Scope             askdata.PolicyScope `json:"scope"`
	DomainID          askdata.ID          `json:"domainId"`
	UnderstandingHash askdata.ContentHash `json:"understandingHash"`
	GraphPlanHash     askdata.ContentHash `json:"graphPlanHash"`
	Bundles           []Bundle            `json:"bundles"`
	BlockedCandidates []BlockedCandidate  `json:"blockedCandidates"`
	NoMatch           bool                `json:"noMatch"`
	ResultHash        askdata.ContentHash `json:"resultHash"`
}

func Bind(request Request) (Result, error) {
	normalized, state, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	return bindNormalized(normalized, state)
}

// DecodeResult is the persistence/replay boundary for BINDING_BUNDLE
// artifacts. Unknown and duplicate JSON fields fail before the artifact is
// recomputed against its exact upstream request.
func DecodeResult(raw []byte, request Request) (Result, error) {
	var result Result
	if err := askdata.DecodeStrictJSON(raw, &result); err != nil {
		return Result{}, err
	}
	if err := result.ValidateAgainst(request); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (result Result) ValidateAgainst(request Request) error {
	expected, err := Bind(request)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(result, expected) {
		return ErrInvalidBindingResult
	}
	return nil
}

type requestState struct {
	mentions      map[MentionRef]mentionValue
	sets          map[MentionRef]MentionCandidateSet
	graphEvidence askdata.EvidenceRef
	time          *TimeBinding
	blocked       []BlockedCandidate
	metricRefs    map[askdata.ID]graph.ObjectVersionRef
	dimensionRefs map[askdata.ID]graph.ObjectVersionRef
	memberRefs    map[askdata.ID]graph.ObjectVersionRef
}

type mentionValue struct {
	metric    *understanding.MetricMention
	dimension *understanding.DimensionMention
	member    *understanding.ValueMention
}

func normalizeRequest(request Request) (Request, requestState, error) {
	if err := request.UnderstandingResult.Validate(request.UnderstandingRequest); err != nil {
		return Request{}, requestState{}, fmt.Errorf("%w: understanding replay: %v", ErrInvalidBindingRequest, err)
	}
	graphRequest, err := request.GraphRequest.Normalize()
	if err != nil {
		return Request{}, requestState{}, fmt.Errorf("%w: graph request: %v", ErrInvalidBindingRequest, err)
	}
	if err := request.GraphResolution.Validate(graphRequest); err != nil {
		return Request{}, requestState{}, fmt.Errorf("%w: graph replay: %v", ErrInvalidBindingRequest, err)
	}
	if !reflect.DeepEqual(request.UnderstandingRequest.ContextRequest.Scope, graphRequest.Scope) {
		return Request{}, requestState{}, fmt.Errorf("%w: understanding and graph scope mismatch", ErrInvalidBindingRequest)
	}
	if !effectiveDomainContains(request.UnderstandingResult, graphRequest.DomainID) {
		return Request{}, requestState{}, fmt.Errorf("%w: graph domain is not an understanding hypothesis", ErrInvalidBindingRequest)
	}
	config, err := request.Config.normalize()
	if err != nil {
		return Request{}, requestState{}, err
	}
	request.Config = config
	request.GraphRequest = graphRequest
	state := requestState{
		mentions: make(map[MentionRef]mentionValue), sets: make(map[MentionRef]MentionCandidateSet),
		metricRefs:    versionRefIndex(graphRequest.MetricRefs),
		dimensionRefs: versionRefIndex(graphRequest.DimensionRefs),
		memberRefs:    versionRefIndex(graphRequest.MemberRefs),
	}
	addUnderstandingMentions(state.mentions, understanding.EvidenceOriginCurrent, request.UnderstandingResult.Current)
	if inherited := request.UnderstandingResult.Context.Inherited; inherited != nil {
		addUnderstandingMentions(state.mentions, understanding.EvidenceOriginInherited, *inherited)
	}
	if countMentionKind(state.mentions, MentionMetric) == 0 {
		return Request{}, requestState{}, fmt.Errorf("%w: at least one metric mention is required", ErrInvalidBindingRequest)
	}
	state.time, err = effectiveTime(request.UnderstandingResult)
	if err != nil {
		return Request{}, requestState{}, err
	}
	state.graphEvidence, err = request.GraphResolution.Plan.EvidenceRef()
	if err != nil {
		return Request{}, requestState{}, fmt.Errorf("%w: graph evidence", ErrInvalidBindingRequest)
	}

	sets := append([]MentionCandidateSet(nil), request.CandidateSets...)
	for index := range sets {
		if sets[index].Candidates != nil {
			candidates := make([]CandidateOption, len(sets[index].Candidates))
			copy(candidates, sets[index].Candidates)
			sets[index].Candidates = candidates
		}
		for candidateIndex := range sets[index].Candidates {
			sets[index].Candidates[candidateIndex].FeatureEvidenceRefs = normalizeEvidenceRefs(
				sets[index].Candidates[candidateIndex].FeatureEvidenceRefs,
			)
		}
		sort.Slice(sets[index].Candidates, func(i, j int) bool {
			return candidateOptionLess(sets[index].Candidates[i], sets[index].Candidates[j])
		})
	}
	sort.Slice(sets, func(i, j int) bool { return mentionRefLess(sets[i].Mention, sets[j].Mention) })
	request.CandidateSets = sets
	if len(sets) != len(state.mentions) {
		return Request{}, requestState{}, fmt.Errorf("%w: every mention requires exactly one candidate set", ErrInvalidBindingRequest)
	}
	for index, set := range sets {
		value, exists := state.mentions[set.Mention]
		if !exists {
			return Request{}, requestState{}, fmt.Errorf("%w: candidateSets[%d] references an unknown mention", ErrInvalidBindingRequest, index)
		}
		if _, duplicate := state.sets[set.Mention]; duplicate {
			return Request{}, requestState{}, fmt.Errorf("%w: duplicate mention candidate set", ErrInvalidBindingRequest)
		}
		if err := validateCandidateSet(set, value, graphRequest, state); err != nil {
			return Request{}, requestState{}, fmt.Errorf("%w: candidateSets[%d]: %v", ErrInvalidBindingRequest, index, err)
		}
		state.sets[set.Mention] = set
		for _, option := range set.Candidates {
			if option.Gate.Verdict == search.GateBlock {
				state.blocked = append(state.blocked, blockedCandidate(set, option))
			}
		}
	}
	sort.Slice(state.blocked, func(i, j int) bool {
		if state.blocked[i].Mention != state.blocked[j].Mention {
			return mentionRefLess(state.blocked[i].Mention, state.blocked[j].Mention)
		}
		return state.blocked[i].ObjectVersionID < state.blocked[j].ObjectVersionID
	})
	return request, state, nil
}

func validateCandidateSet(
	set MentionCandidateSet,
	value mentionValue,
	graphRequest graph.PlanRequest,
	state requestState,
) error {
	if set.Evidence.Validate() != nil || set.Evidence.Kind != askdata.EvidenceKindCandidateSet ||
		set.Evidence.SourceID != graphRequest.Scope.Release.ReleaseID {
		return errors.New("candidate-set evidence is invalid or belongs to another release")
	}
	if set.Candidates == nil || len(set.Candidates) > MaxCandidatesPerMention {
		return fmt.Errorf("candidate collection must be non-null with at most %d items", MaxCandidatesPerMention)
	}
	seen := map[askdata.ID]struct{}{}
	reviewerRanks := map[int]struct{}{}
	for index, option := range set.Candidates {
		if err := validateCandidateOption(set.Mention, value, option, state); err != nil {
			return fmt.Errorf("candidates[%d]: %w", index, err)
		}
		if _, duplicate := seen[option.Candidate.ObjectVersionID]; duplicate {
			return errors.New("candidate objectVersionId is duplicated")
		}
		seen[option.Candidate.ObjectVersionID] = struct{}{}
		if option.ReviewerRank > 0 {
			if _, duplicate := reviewerRanks[option.ReviewerRank]; duplicate {
				return errors.New("reviewerRank is duplicated")
			}
			reviewerRanks[option.ReviewerRank] = struct{}{}
		}
	}
	return nil
}

func validateCandidateOption(
	mention MentionRef,
	value mentionValue,
	option CandidateOption,
	state requestState,
) error {
	expectedType := search.ObjectMetric
	refs := state.metricRefs
	switch mention.Kind {
	case MentionMetric:
		if value.metric == nil {
			return errors.New("mention kind does not match the understanding")
		}
	case MentionDimension:
		expectedType, refs = search.ObjectDimension, state.dimensionRefs
		if value.dimension == nil {
			return errors.New("mention kind does not match the understanding")
		}
	case MentionMember:
		expectedType, refs = search.ObjectMember, state.memberRefs
		if value.member == nil {
			return errors.New("mention kind does not match the understanding")
		}
	default:
		return errors.New("mention kind is invalid")
	}
	if option.Candidate.ObjectType != expectedType {
		return errors.New("candidate objectType does not match mention kind")
	}
	if _, exists := refs[option.Candidate.ObjectVersionID]; !exists {
		return errors.New("candidate is outside the graph request")
	}
	if err := validateSearchCandidate(option.Candidate); err != nil {
		return err
	}
	if err := option.Gate.Validate(); err != nil {
		return fmt.Errorf("deterministic gate: %w", err)
	}
	for name, score := range map[string]float64{
		"ruleScore": option.RuleScore, "qualityScore": option.QualityScore, "costScore": option.CostScore,
	} {
		if !unitScore(score) {
			return fmt.Errorf("%s must be between 0 and 1", name)
		}
	}
	if err := validateFeatureEvidence(option.FeatureEvidenceRefs); err != nil {
		return err
	}
	switch option.SelectionSource {
	case SelectionLLMRerank:
		if option.ReviewerRank < 1 || option.ReviewerRank > MaxCandidatesPerMention {
			return errors.New("LLM rerank candidate requires a bounded reviewerRank")
		}
		if mention.Kind == MentionMember {
			return errors.New("member candidate cannot be selected by LLM reranking")
		}
	case SelectionDeterministicExact:
		if option.ReviewerRank != 0 || !hasRetrievalSource(option.Candidate, search.SourceExact) {
			return errors.New("deterministic candidate requires an exact hit and no reviewerRank")
		}
	default:
		return errors.New("selectionSource is invalid")
	}
	if mention.Kind == MentionMember {
		if option.ParentDimensionVersionID == nil || option.ParentDimensionVersionID.Validate() != nil {
			return errors.New("member candidate requires parentDimensionVersionId")
		}
		if _, exists := state.dimensionRefs[*option.ParentDimensionVersionID]; !exists {
			return errors.New("member parent dimension is outside the graph request")
		}
	} else if option.ParentDimensionVersionID != nil {
		return errors.New("only a member candidate can carry parentDimensionVersionId")
	}
	return nil
}

func validateSearchCandidate(candidate search.Candidate) error {
	if candidate.ObjectVersionID.Validate() != nil || math.IsNaN(candidate.Score) ||
		math.IsInf(candidate.Score, 0) || candidate.Score < 0 ||
		len(candidate.Evidence) < 1 || len(candidate.Evidence) > 3 {
		return errors.New("search candidate identity, score or evidence is invalid")
	}
	seen := map[search.RetrievalSource]struct{}{}
	previous := search.RetrievalSource("")
	for _, source := range candidate.Evidence {
		if source.Source != search.SourceExact && source.Source != search.SourceLexical && source.Source != search.SourceVector {
			return errors.New("retrieval source is invalid")
		}
		if _, duplicate := seen[source.Source]; duplicate || (previous != "" && source.Source < previous) {
			return errors.New("retrieval sources must be sorted and unique")
		}
		seen[source.Source] = struct{}{}
		previous = source.Source
		if source.Rank < 1 || math.IsNaN(source.SourceScore) || math.IsInf(source.SourceScore, 0) ||
			source.SourceScore < 0 || source.Evidence.Validate() != nil ||
			source.Evidence.SourceID != candidate.ObjectVersionID || source.Evidence.Kind != evidenceKindForSource(source.Source) {
			return errors.New("retrieval source evidence is invalid")
		}
	}
	return nil
}

func evidenceKindForSource(source search.RetrievalSource) askdata.EvidenceKind {
	switch source {
	case search.SourceExact:
		return askdata.EvidenceKindExactAlias
	case search.SourceLexical:
		return askdata.EvidenceKindLexicalMatch
	default:
		return askdata.EvidenceKindVectorMatch
	}
}

func addUnderstandingMentions(
	result map[MentionRef]mentionValue,
	origin understanding.EvidenceOrigin,
	value understanding.QuestionUnderstanding,
) {
	for index := range value.MetricMentions {
		mention := value.MetricMentions[index]
		result[MentionRef{origin, MentionMetric, index}] = mentionValue{metric: &mention}
	}
	for index := range value.DimensionMentions {
		mention := value.DimensionMentions[index]
		result[MentionRef{origin, MentionDimension, index}] = mentionValue{dimension: &mention}
	}
	for index := range value.ValueMentions {
		mention := value.ValueMentions[index]
		result[MentionRef{origin, MentionMember, index}] = mentionValue{member: &mention}
	}
}

func effectiveDomainContains(result understanding.UnderstandingResult, domainID askdata.ID) bool {
	values := result.Current.DomainHypotheses
	if len(values) == 0 && result.Context.Inherited != nil {
		values = result.Context.Inherited.DomainHypotheses
	}
	for _, value := range values {
		if value.DomainID == domainID {
			return true
		}
	}
	return false
}

func effectiveTime(result understanding.UnderstandingResult) (*TimeBinding, error) {
	var values []TimeBinding
	if result.Current.Time != nil {
		values = append(values, TimeBinding{understanding.EvidenceOriginCurrent, *result.Current.Time})
	}
	if result.Context.Inherited != nil && result.Context.Inherited.Time != nil {
		values = append(values, TimeBinding{understanding.EvidenceOriginInherited, *result.Context.Inherited.Time})
	}
	if len(values) > 1 {
		return nil, fmt.Errorf("%w: current and inherited time both survived context precedence", ErrInvalidBindingRequest)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return &values[0], nil
}

func countMentionKind(values map[MentionRef]mentionValue, kind MentionKind) int {
	count := 0
	for mention := range values {
		if mention.Kind == kind {
			count++
		}
	}
	return count
}

func versionRefIndex(values []graph.ObjectVersionRef) map[askdata.ID]graph.ObjectVersionRef {
	result := make(map[askdata.ID]graph.ObjectVersionRef, len(values))
	for _, value := range values {
		result[value.VersionID] = value
	}
	return result
}

func candidateOptionLess(left, right CandidateOption) bool {
	if left.Candidate.Score != right.Candidate.Score {
		return left.Candidate.Score > right.Candidate.Score
	}
	if left.ReviewerRank != right.ReviewerRank {
		if left.ReviewerRank == 0 {
			return false
		}
		if right.ReviewerRank == 0 {
			return true
		}
		return left.ReviewerRank < right.ReviewerRank
	}
	return left.Candidate.ObjectVersionID < right.Candidate.ObjectVersionID
}

func mentionRefLess(left, right MentionRef) bool {
	if left.Origin != right.Origin {
		return left.Origin < right.Origin
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.Index < right.Index
}

func blockedCandidate(set MentionCandidateSet, option CandidateOption) BlockedCandidate {
	refs := []askdata.EvidenceRef{set.Evidence}
	for _, source := range option.Candidate.Evidence {
		refs = append(refs, source.Evidence)
	}
	refs = append(refs, option.Gate.EvidenceRefs...)
	refs = append(refs, option.FeatureEvidenceRefs...)
	return BlockedCandidate{
		Mention: set.Mention, ObjectVersionID: option.Candidate.ObjectVersionID,
		ReasonCodes: append([]string(nil), option.Gate.ReasonCodes...), EvidenceRefs: normalizeEvidenceRefs(refs),
	}
}

func hasRetrievalSource(candidate search.Candidate, source search.RetrievalSource) bool {
	for _, evidence := range candidate.Evidence {
		if evidence.Source == source {
			return true
		}
	}
	return false
}

func unitScore(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validateFeatureEvidence(values []askdata.EvidenceRef) error {
	if len(values) < 2 || len(values) > 8 || !reflect.DeepEqual(values, normalizeEvidenceRefs(values)) {
		return errors.New("feature evidence must be canonical and bounded")
	}
	hasQuality, hasContract := false, false
	for _, value := range values {
		if value.Validate() != nil {
			return errors.New("feature evidence is invalid")
		}
		switch value.Kind {
		case askdata.EvidenceKindDataQuality:
			hasQuality = true
		case askdata.EvidenceKindSemanticContract:
			hasContract = true
		default:
			return errors.New("feature evidence kind is invalid")
		}
	}
	if !hasQuality || !hasContract {
		return errors.New("quality and semantic contract evidence are required")
	}
	return nil
}

func normalizeEvidenceRefs(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EvidenceID != result[j].EvidenceID {
			return result[i].EvidenceID < result[j].EvidenceID
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].SourceID != result[j].SourceID {
			return result[i].SourceID < result[j].SourceID
		}
		return result[i].ContentHash < result[j].ContentHash
	})
	if len(result) == 0 {
		return []askdata.EvidenceRef{}
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

func finalizeBundle(bundle Bundle) (Bundle, error) {
	bundle.BundleHash = ""
	hash, _, err := registry.CanonicalContentHash(bundle)
	if err != nil {
		return Bundle{}, err
	}
	bundle.BundleHash = hash
	return bundle, nil
}

func finalizeResult(result Result) (Result, error) {
	result.ResultHash = ""
	payload, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	result.ResultHash = askdata.HashBytes(payload)
	return result, nil
}
