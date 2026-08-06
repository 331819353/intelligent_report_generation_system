package dimension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	GenerationReviewSchemaVersion = "1.1"
	MaxGenerationReviewMembers    = 64
	MaxGenerationProposals        = 64
)

var (
	ErrInvalidGenerationReview   = errors.New("dimension profile generation review is invalid")
	ErrSensitiveGenerationReview = errors.New("sensitive dimension members cannot enter LLM review")
)

type ProposalSource string
type AnomalyType string
type MergeRisk string

const (
	ProposalDeterministic ProposalSource = "DETERMINISTIC_EQUIVALENCE"
	ProposalLLM           ProposalSource = "LLM_REVIEW"

	AnomalyAliasCandidate   AnomalyType = "ALIAS_CANDIDATE"
	AnomalyClusterCandidate AnomalyType = "CLUSTER_CANDIDATE"
	AnomalyHierarchy        AnomalyType = "HIERARCHY_ANOMALY"
	AnomalySentinel         AnomalyType = "SENTINEL_CANDIDATE"

	RiskLow    MergeRisk = "LOW"
	RiskMedium MergeRisk = "MEDIUM"
	RiskHigh   MergeRisk = "HIGH"
)

// GenerationMemberEvidence is the bounded, public/internal view of one
// append-only member observation. MemberEvidenceID is derived from the
// dimension-bound member key hash, so it is stable across profile generations
// without pretending that an observed value is already a certified member.
type GenerationMemberEvidence struct {
	MemberEvidenceID       askdata.ID          `json:"memberEvidenceId"`
	MemberKeyHash          askdata.ContentHash `json:"memberKeyHash"`
	CanonicalLabel         string              `json:"canonicalLabel"`
	NormalizedValue        string              `json:"normalizedValue"`
	ObservedAliases        []string            `json:"observedAliases"`
	ObservedCount          int64               `json:"observedCount"`
	ObservationContentHash askdata.ContentHash `json:"observationContentHash"`
}

// GenerationReviewEvidence is local, policy-bound review state. It may retain
// PUBLIC/INTERNAL FULL member labels for deterministic checks, but PromptFact
// deliberately emits a separate aggregate-only payload with no member label,
// normalized value, alias, member hash or hash-derived member ID.
type GenerationReviewEvidence struct {
	SchemaVersion         string                     `json:"schemaVersion"`
	TenantID              askdata.ID                 `json:"tenantId"`
	DomainID              askdata.ID                 `json:"domainId"`
	DimensionVersionID    askdata.ID                 `json:"dimensionVersionId"`
	Generation            int64                      `json:"generation"`
	SourceSnapshotHash    askdata.ContentHash        `json:"sourceSnapshotHash"`
	ProfileHash           askdata.ContentHash        `json:"profileHash"`
	Sensitivity           registry.Sensitivity       `json:"sensitivity"`
	MemberIndexPolicy     registry.MemberIndexPolicy `json:"memberIndexPolicy"`
	HighCardinality       bool                       `json:"highCardinality"`
	ScanComplete          bool                       `json:"scanComplete"`
	CapturedDistinctCount int64                      `json:"capturedDistinctCount"`
	OmittedMemberCount    int                        `json:"omittedMemberCount"`
	Members               []GenerationMemberEvidence `json:"members"`
	ReservedValues        []ReservedValueObservation `json:"reservedValues"`
}

// NewGenerationReviewEvidence connects DIM-002's persisted profile generation
// and member observations to a bounded DIMENSION_PROFILE cognition fact.
func NewGenerationReviewEvidence(
	claim ScanClaim,
	profile Profile,
	observations []MemberObservation,
) (GenerationReviewEvidence, error) {
	if err := profile.Validate(); err != nil {
		return GenerationReviewEvidence{}, fmt.Errorf("%w: profile: %v", ErrInvalidGenerationReview, err)
	}
	if err := generationReviewClaimMatchesProfile(claim, profile); err != nil {
		return GenerationReviewEvidence{}, err
	}
	effectiveHighCardinality := claim.HighCardinalityHint || profile.HighCardinalityHint
	if !memberLabelsEligibleForLLM(
		profile.Sensitivity,
		claim.MemberIndexPolicy,
		effectiveHighCardinality,
	) {
		return GenerationReviewEvidence{}, ErrSensitiveGenerationReview
	}
	if err := validateMemberObservations(claim, profile, observations); err != nil {
		return GenerationReviewEvidence{}, fmt.Errorf("%w: observations", ErrInvalidGenerationReview)
	}
	if int64(len(observations)+len(profile.ReservedValues)) != profile.DistinctCount {
		return GenerationReviewEvidence{}, fmt.Errorf("%w: captured distinct evidence is incomplete", ErrInvalidGenerationReview)
	}

	members := make([]GenerationMemberEvidence, 0, len(observations))
	for _, observation := range observations {
		if !observation.EligibleForLLM {
			return GenerationReviewEvidence{}, ErrSensitiveGenerationReview
		}
		members = append(members, GenerationMemberEvidence{
			MemberEvidenceID:       memberEvidenceID(observation.MemberKeyHash),
			MemberKeyHash:          observation.MemberKeyHash,
			CanonicalLabel:         observation.CanonicalLabel,
			NormalizedValue:        observation.NormalizedValue,
			ObservedAliases:        append([]string{}, observation.ObservedAliases...),
			ObservedCount:          observation.ObservedCount,
			ObservationContentHash: observation.ContentHash,
		})
	}
	// Select the most frequently observed bounded set, with a stable ID
	// tie-break, then canonicalize the selected payload by stable evidence ID.
	sort.Slice(members, func(i, j int) bool {
		if members[i].ObservedCount == members[j].ObservedCount {
			return members[i].MemberEvidenceID < members[j].MemberEvidenceID
		}
		return members[i].ObservedCount > members[j].ObservedCount
	})
	omitted := 0
	if len(members) > MaxGenerationReviewMembers {
		omitted = len(members) - MaxGenerationReviewMembers
		members = members[:MaxGenerationReviewMembers]
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].MemberEvidenceID < members[j].MemberEvidenceID
	})
	evidence := GenerationReviewEvidence{
		SchemaVersion:         GenerationReviewSchemaVersion,
		TenantID:              profile.TenantID,
		DomainID:              profile.DomainID,
		DimensionVersionID:    profile.DimensionVersionID,
		Generation:            profile.Generation,
		SourceSnapshotHash:    profile.SourceSnapshotHash,
		ProfileHash:           profile.ProfileHash,
		Sensitivity:           profile.Sensitivity,
		MemberIndexPolicy:     claim.MemberIndexPolicy,
		HighCardinality:       effectiveHighCardinality,
		ScanComplete:          !profile.Usage.Truncated && !profile.Usage.TimedOut,
		CapturedDistinctCount: profile.DistinctCount,
		OmittedMemberCount:    omitted,
		Members:               members,
		ReservedValues:        append([]ReservedValueObservation(nil), profile.ReservedValues...),
	}
	if err := evidence.Validate(); err != nil {
		return GenerationReviewEvidence{}, err
	}
	return evidence, nil
}

func generationReviewClaimMatchesProfile(claim ScanClaim, profile Profile) error {
	if askdata.ContentHash(claim.InputHash).Validate() != nil ||
		claim.TenantID != string(profile.TenantID) ||
		claim.DomainID != string(profile.DomainID) ||
		claim.DimensionVersionID != string(profile.DimensionVersionID) ||
		claim.Generation != profile.Generation ||
		claim.SourceSnapshotHash != string(profile.SourceSnapshotHash) ||
		claim.Sensitivity != profile.Sensitivity ||
		(claim.MemberIndexPolicy != registry.MemberIndexFull &&
			claim.MemberIndexPolicy != registry.MemberIndexExactOnly &&
			claim.MemberIndexPolicy != registry.MemberIndexOnDemand &&
			claim.MemberIndexPolicy != registry.MemberIndexNone) ||
		(claim.HighCardinalityHint && !profile.HighCardinalityHint) {
		return fmt.Errorf("%w: claim/profile policy binding", ErrInvalidGenerationReview)
	}
	return nil
}

func (evidence GenerationReviewEvidence) Validate() error {
	if evidence.SchemaVersion != GenerationReviewSchemaVersion {
		return fmt.Errorf("%w: schemaVersion", ErrInvalidGenerationReview)
	}
	for _, id := range []askdata.ID{evidence.TenantID, evidence.DomainID, evidence.DimensionVersionID} {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%w: identity", ErrInvalidGenerationReview)
		}
	}
	if evidence.Generation < 1 || evidence.CapturedDistinctCount < 0 ||
		evidence.OmittedMemberCount < 0 || len(evidence.Members) > MaxGenerationReviewMembers ||
		!memberLabelsEligibleForLLM(
			evidence.Sensitivity,
			evidence.MemberIndexPolicy,
			evidence.HighCardinality,
		) {
		return ErrInvalidGenerationReview
	}
	if err := evidence.SourceSnapshotHash.Validate(); err != nil {
		return fmt.Errorf("%w: sourceSnapshotHash", ErrInvalidGenerationReview)
	}
	if err := evidence.ProfileHash.Validate(); err != nil {
		return fmt.Errorf("%w: profileHash", ErrInvalidGenerationReview)
	}
	if int64(len(evidence.Members)+evidence.OmittedMemberCount+len(evidence.ReservedValues)) !=
		evidence.CapturedDistinctCount {
		return fmt.Errorf("%w: capturedDistinctCount", ErrInvalidGenerationReview)
	}
	previousMemberID := askdata.ID("")
	for index, member := range evidence.Members {
		if err := validateGenerationMember(member, evidence); err != nil {
			return fmt.Errorf("%w: members[%d]: %v", ErrInvalidGenerationReview, index, err)
		}
		if previousMemberID != "" && member.MemberEvidenceID <= previousMemberID {
			return fmt.Errorf("%w: members must be sorted and unique", ErrInvalidGenerationReview)
		}
		previousMemberID = member.MemberEvidenceID
	}
	previousReserved := ""
	reservedCatalogVersion := ""
	for index, reserved := range evidence.ReservedValues {
		if !stableCode(reserved.Code) || reserved.Count < 1 || reserved.NormalizedValueHash.Validate() != nil ||
			strings.TrimSpace(reserved.CatalogVersion) == "" || len(reserved.CatalogVersion) > 64 ||
			strings.TrimSpace(reserved.CatalogVersion) != reserved.CatalogVersion {
			return fmt.Errorf("%w: reservedValues[%d]", ErrInvalidGenerationReview, index)
		}
		identity := reserved.Code + "\x00" + string(reserved.NormalizedValueHash)
		if previousReserved != "" && identity <= previousReserved {
			return fmt.Errorf("%w: reservedValues must be sorted and unique", ErrInvalidGenerationReview)
		}
		if reservedCatalogVersion == "" {
			reservedCatalogVersion = reserved.CatalogVersion
		} else if reserved.CatalogVersion != reservedCatalogVersion {
			return fmt.Errorf("%w: reservedValues catalog version mismatch", ErrInvalidGenerationReview)
		}
		previousReserved = identity
	}
	return nil
}

// PromptFact returns the exact AI-003 fact boundary used for asset review. The
// payload is intentionally aggregate-only. Until member observations can be
// reloaded from the authoritative profile generation in PostgreSQL, no local
// member label or deterministic/hash-derived member identity crosses this
// boundary.
func (evidence GenerationReviewEvidence) PromptFact() (askdatacognition.PromptFact, error) {
	if err := evidence.Validate(); err != nil {
		return askdatacognition.PromptFact{}, err
	}
	payload, err := json.Marshal(struct {
		SchemaVersion         string                     `json:"schemaVersion"`
		TenantID              askdata.ID                 `json:"tenantId"`
		DomainID              askdata.ID                 `json:"domainId"`
		DimensionVersionID    askdata.ID                 `json:"dimensionVersionId"`
		Generation            int64                      `json:"generation"`
		SourceSnapshotHash    askdata.ContentHash        `json:"sourceSnapshotHash"`
		ProfileHash           askdata.ContentHash        `json:"profileHash"`
		Sensitivity           registry.Sensitivity       `json:"sensitivity"`
		MemberIndexPolicy     registry.MemberIndexPolicy `json:"memberIndexPolicy"`
		HighCardinality       bool                       `json:"highCardinality"`
		ScanComplete          bool                       `json:"scanComplete"`
		CapturedDistinctCount int64                      `json:"capturedDistinctCount"`
		LocalMemberCount      int                        `json:"localMemberCount"`
		OmittedMemberCount    int                        `json:"omittedMemberCount"`
		ReservedDistinctCount int                        `json:"reservedDistinctCount"`
	}{
		SchemaVersion:         evidence.SchemaVersion,
		TenantID:              evidence.TenantID,
		DomainID:              evidence.DomainID,
		DimensionVersionID:    evidence.DimensionVersionID,
		Generation:            evidence.Generation,
		SourceSnapshotHash:    evidence.SourceSnapshotHash,
		ProfileHash:           evidence.ProfileHash,
		Sensitivity:           evidence.Sensitivity,
		MemberIndexPolicy:     evidence.MemberIndexPolicy,
		HighCardinality:       evidence.HighCardinality,
		ScanComplete:          evidence.ScanComplete,
		CapturedDistinctCount: evidence.CapturedDistinctCount,
		LocalMemberCount:      len(evidence.Members),
		OmittedMemberCount:    evidence.OmittedMemberCount,
		ReservedDistinctCount: len(evidence.ReservedValues),
	})
	if err != nil {
		return askdatacognition.PromptFact{}, err
	}
	return askdatacognition.NewPromptFact(
		askdata.ID("dimension-profile:"+string(evidence.ProfileHash)),
		askdatacognition.FactDimensionProfile,
		payload,
	)
}

func (evidence GenerationReviewEvidence) EvidenceRef() (askdata.EvidenceRef, error) {
	fact, err := evidence.PromptFact()
	if err != nil {
		return askdata.EvidenceRef{}, err
	}
	ref := askdata.EvidenceRef{
		EvidenceID:  fact.EvidenceID,
		Kind:        askdata.EvidenceKindDimensionProfile,
		SourceID:    askdata.ID("profile:" + string(evidence.ProfileHash)),
		ContentHash: fact.ContentHash,
	}
	return ref, ref.Validate()
}

func validateGenerationMember(member GenerationMemberEvidence, evidence GenerationReviewEvidence) error {
	if err := member.MemberEvidenceID.Validate(); err != nil {
		return err
	}
	if err := member.MemberKeyHash.Validate(); err != nil {
		return err
	}
	if err := member.ObservationContentHash.Validate(); err != nil {
		return err
	}
	if member.MemberEvidenceID != memberEvidenceID(member.MemberKeyHash) || member.ObservedCount < 1 ||
		len(member.ObservedAliases) > 64 ||
		normalizeMemberKey(member.CanonicalLabel) != member.NormalizedValue ||
		askdata.HashBytes([]byte(string(evidence.DimensionVersionID)+"\x00"+member.NormalizedValue)) != member.MemberKeyHash {
		return ErrInvalidGenerationReview
	}
	previousAlias := ""
	for _, alias := range member.ObservedAliases {
		display, err := normalizeMemberDisplay(alias)
		if err != nil || display != alias || normalizeMemberKey(alias) != member.NormalizedValue ||
			alias == member.CanonicalLabel || (previousAlias != "" && alias <= previousAlias) {
			return ErrInvalidGenerationReview
		}
		previousAlias = alias
	}
	payload, err := memberObservationPayload(MemberObservation{
		DimensionVersionID: evidence.DimensionVersionID,
		Generation:         evidence.Generation,
		MemberKeyHash:      member.MemberKeyHash,
		CanonicalLabel:     member.CanonicalLabel,
		NormalizedValue:    member.NormalizedValue,
		ObservedAliases:    member.ObservedAliases,
		ObservedCount:      member.ObservedCount,
		Sensitivity:        evidence.Sensitivity,
		EligibleForLLM: memberLabelsEligibleForLLM(
			evidence.Sensitivity,
			evidence.MemberIndexPolicy,
			evidence.HighCardinality,
		),
	})
	if err != nil || askdata.HashBytes(payload) != member.ObservationContentHash {
		return ErrInvalidGenerationReview
	}
	return nil
}

func memberEvidenceID(hash askdata.ContentHash) askdata.ID {
	return askdata.ID("profile-member:" + string(hash))
}

// AnomalyProposal contains only stable generation member evidence IDs and a
// reference to the exact DIMENSION_PROFILE fact. It is a review candidate,
// never a mutation of askdata.dimension_members.
type AnomalyProposal struct {
	Source                       ProposalSource        `json:"source"`
	Type                         AnomalyType           `json:"type"`
	DimensionVersionID           askdata.ID            `json:"dimensionVersionId"`
	Generation                   int64                 `json:"generation"`
	MemberEvidenceIDs            []askdata.ID          `json:"memberEvidenceIds"`
	SuggestedCanonicalEvidenceID askdata.ID            `json:"suggestedCanonicalEvidenceId,omitempty"`
	SuggestedAliases             []string              `json:"suggestedAliases"`
	Risk                         MergeRisk             `json:"risk"`
	Summary                      string                `json:"summary"`
	EvidenceRefs                 []askdata.EvidenceRef `json:"evidenceRefs"`
	Sensitivity                  registry.Sensitivity  `json:"sensitivity"`
}

func (proposal AnomalyProposal) Validate() error {
	if proposal.Source != ProposalDeterministic && proposal.Source != ProposalLLM {
		return errors.New("proposal source is invalid")
	}
	if proposal.Type != AnomalyAliasCandidate && proposal.Type != AnomalyClusterCandidate &&
		proposal.Type != AnomalyHierarchy && proposal.Type != AnomalySentinel {
		return errors.New("anomaly type is invalid")
	}
	if proposal.Source == ProposalDeterministic &&
		(proposal.Type != AnomalyAliasCandidate || proposal.Risk != RiskLow) {
		return errors.New("deterministic proposals are limited to low-risk alias equivalence")
	}
	if proposal.Risk != RiskLow && proposal.Risk != RiskMedium && proposal.Risk != RiskHigh {
		return errors.New("merge risk is invalid")
	}
	if err := proposal.DimensionVersionID.Validate(); err != nil {
		return fmt.Errorf("dimensionVersionId: %w", err)
	}
	if proposal.Generation < 1 || len(proposal.MemberEvidenceIDs) < 1 ||
		len(proposal.MemberEvidenceIDs) > MaxGenerationReviewMembers {
		return errors.New("generation or memberEvidenceIds count is invalid")
	}
	previousID := askdata.ID("")
	seen := map[askdata.ID]struct{}{}
	for index, memberID := range proposal.MemberEvidenceIDs {
		if err := memberID.Validate(); err != nil {
			return fmt.Errorf("memberEvidenceIds[%d]: %w", index, err)
		}
		if _, duplicate := seen[memberID]; duplicate {
			return fmt.Errorf("memberEvidenceIds[%d] is duplicated", index)
		}
		if previousID != "" && memberID < previousID {
			return errors.New("memberEvidenceIds must be sorted")
		}
		seen[memberID] = struct{}{}
		previousID = memberID
	}
	minimumMembers := 1
	if proposal.Type == AnomalyClusterCandidate || proposal.Type == AnomalyHierarchy {
		minimumMembers = 2
	}
	if len(proposal.MemberEvidenceIDs) < minimumMembers {
		return fmt.Errorf("%s requires at least %d member evidence IDs", proposal.Type, minimumMembers)
	}
	if proposal.Type == AnomalyAliasCandidate || proposal.Type == AnomalyClusterCandidate {
		if err := proposal.SuggestedCanonicalEvidenceID.Validate(); err != nil {
			return fmt.Errorf("suggestedCanonicalEvidenceId: %w", err)
		}
		if _, included := seen[proposal.SuggestedCanonicalEvidenceID]; !included {
			return errors.New("suggestedCanonicalEvidenceId must be one of memberEvidenceIds")
		}
	} else if proposal.SuggestedCanonicalEvidenceID != "" {
		return errors.New("suggestedCanonicalEvidenceId is not allowed for this anomaly type")
	}
	if proposal.Type == AnomalyAliasCandidate {
		if len(proposal.SuggestedAliases) < 1 || len(proposal.SuggestedAliases) > 64 {
			return errors.New("alias candidates require suggestedAliases")
		}
	} else if len(proposal.SuggestedAliases) != 0 {
		return errors.New("suggestedAliases is only allowed for alias candidates")
	}
	previousAlias := ""
	catalog := DefaultReservedValueCatalog()
	for index, alias := range proposal.SuggestedAliases {
		display, err := normalizeMemberDisplay(alias)
		if err != nil || display != alias || (previousAlias != "" && alias <= previousAlias) {
			return fmt.Errorf("suggestedAliases[%d] is invalid or unsorted", index)
		}
		if _, reserved := catalog.Values[normalizeMemberKey(alias)]; reserved {
			return fmt.Errorf("suggestedAliases[%d] is reserved", index)
		}
		previousAlias = alias
	}
	if strings.TrimSpace(proposal.Summary) == "" || !utf8.ValidString(proposal.Summary) ||
		utf8.RuneCountInString(proposal.Summary) > 1_000 {
		return errors.New("summary is invalid")
	}
	if len(proposal.EvidenceRefs) < 1 || len(proposal.EvidenceRefs) > 64 {
		return errors.New("evidenceRefs count is invalid")
	}
	previousEvidenceID := askdata.ID("")
	for index, evidence := range proposal.EvidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
		if previousEvidenceID != "" && evidence.EvidenceID <= previousEvidenceID {
			return errors.New("evidenceRefs must be sorted and unique")
		}
		previousEvidenceID = evidence.EvidenceID
	}
	if !validSensitivity(proposal.Sensitivity) {
		return errors.New("sensitivity is invalid")
	}
	if proposal.Source == ProposalLLM &&
		(proposal.Sensitivity == registry.SensitivityConfidential ||
			proposal.Sensitivity == registry.SensitivityRestricted) {
		return ErrSensitiveGenerationReview
	}
	return nil
}

// ValidateAgainstGeneration prevents a model from inventing a member, citing
// another profile generation or proposing an alias that was not observed in
// the bounded evidence supplied to it.
func (proposal AnomalyProposal) ValidateAgainstGeneration(evidence GenerationReviewEvidence) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if err := proposal.Validate(); err != nil {
		return err
	}
	if proposal.DimensionVersionID != evidence.DimensionVersionID ||
		proposal.Generation != evidence.Generation || proposal.Sensitivity != evidence.Sensitivity {
		return errors.New("proposal does not belong to the supplied profile generation")
	}
	members := make(map[askdata.ID]GenerationMemberEvidence, len(evidence.Members))
	for _, member := range evidence.Members {
		members[member.MemberEvidenceID] = member
	}
	allowedAliases := map[string]struct{}{}
	for _, memberID := range proposal.MemberEvidenceIDs {
		member, ok := members[memberID]
		if !ok {
			return fmt.Errorf("member evidence %q is not in the supplied generation", memberID)
		}
		allowedAliases[member.CanonicalLabel] = struct{}{}
		for _, alias := range member.ObservedAliases {
			allowedAliases[alias] = struct{}{}
		}
	}
	for _, alias := range proposal.SuggestedAliases {
		if _, observed := allowedAliases[alias]; !observed {
			return fmt.Errorf("suggested alias %q was not observed in the supplied generation", alias)
		}
	}
	ref, err := evidence.EvidenceRef()
	if err != nil {
		return err
	}
	if len(proposal.EvidenceRefs) != 1 || proposal.EvidenceRefs[0] != ref {
		return errors.New("proposal must cite only the exact supplied generation evidence")
	}
	return nil
}

// CanAutoApply is deliberately narrow. It expresses policy capability only;
// ReviewProfileGeneration never writes or certifies a semantic member.
func (proposal AnomalyProposal) CanAutoApply() bool {
	return proposal.Validate() == nil && proposal.Source == ProposalDeterministic &&
		proposal.Type == AnomalyAliasCandidate && proposal.Risk == RiskLow &&
		(proposal.Sensitivity == registry.SensitivityPublic ||
			proposal.Sensitivity == registry.SensitivityInternal)
}

func (proposal AnomalyProposal) CanAutoApplyAgainst(evidence GenerationReviewEvidence) bool {
	return proposal.CanAutoApply() && evidence.ScanComplete && evidence.OmittedMemberCount == 0 &&
		proposal.ValidateAgainstGeneration(evidence) == nil
}

// GenerationReviewer is the LLM adapter boundary. Implementations receive a
// sanitized, hashed DIMENSION_PROFILE fact and return candidates only; the
// local validator remains authoritative.
type GenerationReviewer interface {
	ReviewGeneration(context.Context, askdatacognition.PromptFact) ([]AnomalyProposal, error)
}

type GenerationReviewResult struct {
	Evidence               GenerationReviewEvidence `json:"evidence"`
	DeterministicProposals []AnomalyProposal        `json:"deterministicProposals"`
	LLMProposals           []AnomalyProposal        `json:"llmProposals"`
	ResultHash             askdata.ContentHash      `json:"resultHash"`
}

// ReviewProfileGeneration builds deterministic aliases, invokes the bounded
// reviewer, validates every returned ID/reference against the same generation
// and returns candidates without applying them.
func ReviewProfileGeneration(
	_ context.Context,
	claim ScanClaim,
	profile Profile,
	observations []MemberObservation,
	reviewer GenerationReviewer,
) (GenerationReviewResult, error) {
	if reviewer == nil {
		return GenerationReviewResult{}, ErrInvalidGenerationReview
	}
	evidence, err := NewGenerationReviewEvidence(claim, profile, observations)
	if err != nil {
		return GenerationReviewResult{}, err
	}
	deterministic, err := deterministicAliasProposals(evidence)
	if err != nil {
		return GenerationReviewResult{}, err
	}
	// Member-aware LLM review remains disabled until observations are loaded
	// from the persisted profile generation through an authoritative store.
	// The supplied reviewer is deliberately never invoked here.
	llmProposals := []AnomalyProposal{}
	if len(llmProposals) > MaxGenerationProposals {
		return GenerationReviewResult{}, fmt.Errorf("%w: too many LLM proposals", ErrInvalidGenerationReview)
	}
	seen := map[askdata.ContentHash]struct{}{}
	for index := range llmProposals {
		llmProposals[index] = normalizeProposal(llmProposals[index])
		if llmProposals[index].Source != ProposalLLM {
			return GenerationReviewResult{}, fmt.Errorf("llmProposals[%d] is not LLM sourced", index)
		}
		if err := llmProposals[index].ValidateAgainstGeneration(evidence); err != nil {
			return GenerationReviewResult{}, fmt.Errorf("llmProposals[%d]: %w", index, err)
		}
		hash, err := proposalHash(llmProposals[index])
		if err != nil {
			return GenerationReviewResult{}, err
		}
		if _, duplicate := seen[hash]; duplicate {
			return GenerationReviewResult{}, fmt.Errorf("llmProposals[%d] is duplicated", index)
		}
		seen[hash] = struct{}{}
	}
	sortProposals(llmProposals)
	result := GenerationReviewResult{
		Evidence:               evidence,
		DeterministicProposals: deterministic,
		LLMProposals:           llmProposals,
	}
	payload, err := generationReviewResultPayload(result)
	if err != nil {
		return GenerationReviewResult{}, err
	}
	result.ResultHash = askdata.HashBytes(payload)
	return result, result.Validate()
}

func (result GenerationReviewResult) Validate() error {
	if err := result.Evidence.Validate(); err != nil {
		return err
	}
	for index, proposal := range result.DeterministicProposals {
		if proposal.Source != ProposalDeterministic || proposal.ValidateAgainstGeneration(result.Evidence) != nil {
			return fmt.Errorf("deterministicProposals[%d] is invalid", index)
		}
	}
	for index, proposal := range result.LLMProposals {
		if proposal.Source != ProposalLLM || proposal.ValidateAgainstGeneration(result.Evidence) != nil {
			return fmt.Errorf("llmProposals[%d] is invalid", index)
		}
	}
	if err := result.ResultHash.Validate(); err != nil {
		return err
	}
	payload, err := generationReviewResultPayload(result)
	if err != nil {
		return err
	}
	if askdata.HashBytes(payload) != result.ResultHash {
		return errors.New("resultHash does not match generation review content")
	}
	return nil
}

func deterministicAliasProposals(evidence GenerationReviewEvidence) ([]AnomalyProposal, error) {
	ref, err := evidence.EvidenceRef()
	if err != nil {
		return nil, err
	}
	result := []AnomalyProposal{}
	for _, member := range evidence.Members {
		if len(member.ObservedAliases) == 0 {
			continue
		}
		proposal := AnomalyProposal{
			Source:                       ProposalDeterministic,
			Type:                         AnomalyAliasCandidate,
			DimensionVersionID:           evidence.DimensionVersionID,
			Generation:                   evidence.Generation,
			MemberEvidenceIDs:            []askdata.ID{member.MemberEvidenceID},
			SuggestedCanonicalEvidenceID: member.MemberEvidenceID,
			SuggestedAliases:             append([]string(nil), member.ObservedAliases...),
			Risk:                         RiskLow,
			Summary:                      "画像观测到仅由 Unicode、大小写或空白规范化形成的别名候选。",
			EvidenceRefs:                 []askdata.EvidenceRef{ref},
			Sensitivity:                  evidence.Sensitivity,
		}
		if err := proposal.ValidateAgainstGeneration(evidence); err != nil {
			return nil, err
		}
		result = append(result, proposal)
	}
	sortProposals(result)
	return result, nil
}

func normalizeProposal(proposal AnomalyProposal) AnomalyProposal {
	proposal.MemberEvidenceIDs = append([]askdata.ID(nil), proposal.MemberEvidenceIDs...)
	proposal.SuggestedAliases = append([]string(nil), proposal.SuggestedAliases...)
	proposal.EvidenceRefs = append([]askdata.EvidenceRef(nil), proposal.EvidenceRefs...)
	sort.Slice(proposal.MemberEvidenceIDs, func(i, j int) bool {
		return proposal.MemberEvidenceIDs[i] < proposal.MemberEvidenceIDs[j]
	})
	sort.Strings(proposal.SuggestedAliases)
	sort.Slice(proposal.EvidenceRefs, func(i, j int) bool {
		return proposal.EvidenceRefs[i].EvidenceID < proposal.EvidenceRefs[j].EvidenceID
	})
	return proposal
}

func sortProposals(proposals []AnomalyProposal) {
	sort.Slice(proposals, func(i, j int) bool {
		left, _ := proposalHash(proposals[i])
		right, _ := proposalHash(proposals[j])
		return left < right
	})
}

func proposalHash(proposal AnomalyProposal) (askdata.ContentHash, error) {
	payload, err := json.Marshal(proposal)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func generationReviewResultPayload(result GenerationReviewResult) ([]byte, error) {
	type resultWithoutHash GenerationReviewResult
	payload := resultWithoutHash(result)
	payload.ResultHash = ""
	return json.Marshal(payload)
}
