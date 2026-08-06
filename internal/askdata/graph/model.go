package graph

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	PlanVersion            = "1.0"
	MaxVIDBytes            = 256
	MaxMetricCandidates    = 16
	MaxModelCandidates     = 16
	MaxDimensionCandidates = 32
	MaxMemberCandidates    = 64
	MaxJoinHops            = 4
	MaxJoinPaths           = 32
	DefaultJoinHops        = 3
	DefaultJoinPaths       = 16
)

var (
	ErrInvalidPlanRequest = errors.New("invalid graph plan request")
	ErrInvalidGraphResult = errors.New("invalid graph result")
	ErrGraphQueryFailed   = errors.New("graph query failed")
)

type ObjectType string

const (
	ObjectTypeSemanticModel ObjectType = "semantic_model"
	ObjectTypeMetric        ObjectType = "metric"
	ObjectTypeDimension     ObjectType = "dimension"
	ObjectTypeMember        ObjectType = "member"
)

func (objectType ObjectType) Validate() error {
	switch objectType {
	case ObjectTypeSemanticModel, ObjectTypeMetric, ObjectTypeDimension, ObjectTypeMember:
		return nil
	default:
		return fmt.Errorf("unsupported graph object type %q", objectType)
	}
}

// ObjectVersionRef contains only immutable semantic identifiers. Version is
// the registry version number used by the shared-space VID contract; it is not
// a caller-controlled graph label or expression.
type ObjectVersionRef struct {
	ObjectID  askdata.ID `json:"objectId"`
	VersionID askdata.ID `json:"versionId"`
	Version   int        `json:"version"`
}

func (ref ObjectVersionRef) Validate() error {
	if err := ref.ObjectID.Validate(); err != nil {
		return fmt.Errorf("objectId: %w", err)
	}
	if err := ref.VersionID.Validate(); err != nil {
		return fmt.Errorf("versionId: %w", err)
	}
	if ref.Version < 1 || ref.Version > 1_000_000_000 {
		return errors.New("version must be between 1 and 1000000000")
	}
	return nil
}

// BuildVID implements the frozen shared-Space identity contract. Release
// isolation is additionally enforced on every vertex and edge property in the
// generated queries; a VID by itself is never accepted as authorization.
func BuildVID(tenantID askdata.ID, objectType ObjectType, ref ObjectVersionRef) (string, error) {
	if err := tenantID.Validate(); err != nil {
		return "", fmt.Errorf("tenantId: %w", err)
	}
	if err := objectType.Validate(); err != nil {
		return "", err
	}
	if err := ref.Validate(); err != nil {
		return "", err
	}
	vid := strings.Join([]string{
		string(tenantID), string(objectType), string(ref.ObjectID), strconv.Itoa(ref.Version),
	}, ":")
	if len([]byte(vid)) > MaxVIDBytes {
		return "", fmt.Errorf("VID exceeds %d bytes", MaxVIDBytes)
	}
	return vid, nil
}

// PlanRequest has no raw text, graph identifier or nGQL field. All candidates
// must already have passed registry/retrieval authorization for Scope.
type PlanRequest struct {
	Scope         askdata.PolicyScope `json:"scope"`
	DomainID      askdata.ID          `json:"domainId"`
	MetricRefs    []ObjectVersionRef  `json:"metricRefs"`
	ModelRefs     []ObjectVersionRef  `json:"modelRefs"`
	DimensionRefs []ObjectVersionRef  `json:"dimensionRefs"`
	MemberRefs    []ObjectVersionRef  `json:"memberRefs"`
	MaxJoinHops   int                 `json:"maxJoinHops"`
	MaxPaths      int                 `json:"maxPaths"`
}

// Normalize returns a canonical request without mutating the caller's slices.
func (request PlanRequest) Normalize() (PlanRequest, error) {
	normalized := request
	normalized.MetricRefs = normalizedRefs(request.MetricRefs)
	normalized.ModelRefs = normalizedRefs(request.ModelRefs)
	normalized.DimensionRefs = normalizedRefs(request.DimensionRefs)
	normalized.MemberRefs = normalizedRefs(request.MemberRefs)
	if normalized.MaxJoinHops == 0 {
		normalized.MaxJoinHops = DefaultJoinHops
	}
	if normalized.MaxPaths == 0 {
		normalized.MaxPaths = DefaultJoinPaths
	}
	if err := normalized.Validate(); err != nil {
		return PlanRequest{}, err
	}
	return normalized, nil
}

func (request PlanRequest) Validate() error {
	if err := request.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: scope: %v", ErrInvalidPlanRequest, err)
	}
	if err := request.DomainID.Validate(); err != nil {
		return fmt.Errorf("%w: domainId: %v", ErrInvalidPlanRequest, err)
	}
	if !containsID(request.Scope.DomainIDs, request.DomainID) {
		return fmt.Errorf("%w: domainId is outside policy scope", ErrInvalidPlanRequest)
	}
	if err := validateRefSet(request.Scope.TenantID, ObjectTypeMetric, "metricRefs", request.MetricRefs, 1, MaxMetricCandidates); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	if err := validateRefSet(request.Scope.TenantID, ObjectTypeSemanticModel, "modelRefs", request.ModelRefs, 1, MaxModelCandidates); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	if err := validateRefSet(request.Scope.TenantID, ObjectTypeDimension, "dimensionRefs", request.DimensionRefs, 0, MaxDimensionCandidates); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	if err := validateRefSet(request.Scope.TenantID, ObjectTypeMember, "memberRefs", request.MemberRefs, 0, MaxMemberCandidates); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	if len(request.MemberRefs) > 0 && len(request.DimensionRefs) == 0 {
		return fmt.Errorf("%w: memberRefs require dimensionRefs", ErrInvalidPlanRequest)
	}
	if request.MaxJoinHops < 1 || request.MaxJoinHops > MaxJoinHops {
		return fmt.Errorf("%w: maxJoinHops must be between 1 and %d", ErrInvalidPlanRequest, MaxJoinHops)
	}
	if request.MaxPaths < 1 || request.MaxPaths > MaxJoinPaths {
		return fmt.Errorf("%w: maxPaths must be between 1 and %d", ErrInvalidPlanRequest, MaxJoinPaths)
	}
	return nil
}

type MetricModelBinding struct {
	MetricVersionID askdata.ID `json:"metricVersionId"`
	ModelVersionID  askdata.ID `json:"modelVersionId"`
}

type DimensionCompatibility struct {
	ModelVersionID     askdata.ID `json:"modelVersionId"`
	DimensionVersionID askdata.ID `json:"dimensionVersionId"`
}

type MemberStatus string

const (
	MemberStatusActive  MemberStatus = "ACTIVE"
	MemberStatusExpired MemberStatus = "EXPIRED"
)

type MemberOwnership struct {
	MemberVersionID    askdata.ID   `json:"memberVersionId"`
	DimensionVersionID askdata.ID   `json:"dimensionVersionId"`
	Status             MemberStatus `json:"status"`
}

type TraversalDirection string

const (
	TraversalForward TraversalDirection = "FORWARD"
	TraversalReverse TraversalDirection = "REVERSE"
)

type JoinStep struct {
	Hop                   int                   `json:"hop"`
	RelationshipVersionID askdata.ID            `json:"relationshipVersionId"`
	FromModelVersionID    askdata.ID            `json:"fromModelVersionId"`
	ToModelVersionID      askdata.ID            `json:"toModelVersionId"`
	Direction             TraversalDirection    `json:"direction"`
	JoinType              registry.JoinType     `json:"joinType"`
	Cardinality           registry.Cardinality  `json:"cardinality"`
	FanoutPolicy          registry.FanoutPolicy `json:"fanoutPolicy"`
}

type JoinRiskCode string

const (
	JoinRiskOneToMany      JoinRiskCode = "JOIN_ONE_TO_MANY"
	JoinRiskManyToMany     JoinRiskCode = "JOIN_MANY_TO_MANY"
	JoinRiskPreaggregation JoinRiskCode = "JOIN_PREAGG_REQUIRED"
	JoinRiskFanoutBlocked  JoinRiskCode = "JOIN_FANOUT_BLOCKED"
)

type JoinPath struct {
	PathID             askdata.ID     `json:"pathId"`
	FromModelVersionID askdata.ID     `json:"fromModelVersionId"`
	ToModelVersionID   askdata.ID     `json:"toModelVersionId"`
	Steps              []JoinStep     `json:"steps"`
	Allowed            bool           `json:"allowed"`
	RiskCodes          []JoinRiskCode `json:"riskCodes"`
}

func NewJoinPath(steps []JoinStep) (JoinPath, error) {
	path := JoinPath{Steps: append([]JoinStep(nil), steps...)}
	if len(path.Steps) > 0 {
		path.FromModelVersionID = path.Steps[0].FromModelVersionID
		path.ToModelVersionID = path.Steps[len(path.Steps)-1].ToModelVersionID
	}
	path.Allowed, path.RiskCodes = derivePathRisk(path.Steps)
	hash, _, err := registry.CanonicalContentHash(struct {
		From  askdata.ID `json:"from"`
		To    askdata.ID `json:"to"`
		Steps []JoinStep `json:"steps"`
	}{path.FromModelVersionID, path.ToModelVersionID, path.Steps})
	if err != nil {
		return JoinPath{}, err
	}
	path.PathID = askdata.ID("graph-path:" + string(hash))
	if err := path.Validate(); err != nil {
		return JoinPath{}, err
	}
	return path, nil
}

func (path JoinPath) Validate() error {
	if err := path.PathID.Validate(); err != nil {
		return fmt.Errorf("pathId: %w", err)
	}
	if len(path.Steps) < 1 || len(path.Steps) > MaxJoinHops {
		return fmt.Errorf("steps count must be between 1 and %d", MaxJoinHops)
	}
	if path.FromModelVersionID != path.Steps[0].FromModelVersionID ||
		path.ToModelVersionID != path.Steps[len(path.Steps)-1].ToModelVersionID {
		return errors.New("path endpoints do not match steps")
	}
	seenRelationships := make(map[askdata.ID]struct{}, len(path.Steps))
	for index, step := range path.Steps {
		if err := validateJoinStep(step, index); err != nil {
			return err
		}
		if index > 0 && path.Steps[index-1].ToModelVersionID != step.FromModelVersionID {
			return fmt.Errorf("steps[%d] is disconnected", index)
		}
		if _, exists := seenRelationships[step.RelationshipVersionID]; exists {
			return fmt.Errorf("steps[%d] repeats a relationship", index)
		}
		seenRelationships[step.RelationshipVersionID] = struct{}{}
	}
	allowed, riskCodes := derivePathRisk(path.Steps)
	if allowed != path.Allowed || !equalRiskCodes(riskCodes, path.RiskCodes) {
		return errors.New("path risk summary does not match steps")
	}
	expected, err := newJoinPathWithoutValidation(path.Steps)
	if err != nil {
		return err
	}
	if path.PathID != expected.PathID {
		return errors.New("pathId does not match path content")
	}
	return nil
}

// newJoinPathWithoutValidation computes the identity used by Validate without
// recursively calling Validate.
func newJoinPathWithoutValidation(steps []JoinStep) (JoinPath, error) {
	path := JoinPath{Steps: append([]JoinStep(nil), steps...)}
	if len(path.Steps) == 0 {
		return JoinPath{}, errors.New("join path is empty")
	}
	path.FromModelVersionID = path.Steps[0].FromModelVersionID
	path.ToModelVersionID = path.Steps[len(path.Steps)-1].ToModelVersionID
	path.Allowed, path.RiskCodes = derivePathRisk(path.Steps)
	hash, _, err := registry.CanonicalContentHash(struct {
		From  askdata.ID `json:"from"`
		To    askdata.ID `json:"to"`
		Steps []JoinStep `json:"steps"`
	}{path.FromModelVersionID, path.ToModelVersionID, path.Steps})
	if err != nil {
		return JoinPath{}, err
	}
	path.PathID = askdata.ID("graph-path:" + string(hash))
	return path, nil
}

type GraphPlan struct {
	PlanVersion          string                   `json:"planVersion"`
	Scope                askdata.PolicyScope      `json:"scope"`
	DomainID             askdata.ID               `json:"domainId"`
	RequestHash          askdata.ContentHash      `json:"requestHash"`
	Models               []ObjectVersionRef       `json:"models"`
	MetricModels         []MetricModelBinding     `json:"metricModels"`
	CompatibleDimensions []DimensionCompatibility `json:"compatibleDimensions"`
	MemberOwnerships     []MemberOwnership        `json:"memberOwnerships"`
	JoinPaths            []JoinPath               `json:"joinPaths"`
	PlanHash             askdata.ContentHash      `json:"planHash"`
}

func NewGraphPlan(
	request PlanRequest,
	models []ObjectVersionRef,
	metricModels []MetricModelBinding,
	dimensions []DimensionCompatibility,
	members []MemberOwnership,
	paths []JoinPath,
) (GraphPlan, error) {
	normalizedRequest, err := request.Normalize()
	if err != nil {
		return GraphPlan{}, err
	}
	if err := validatePlanResultsAgainstRequest(
		normalizedRequest, models, metricModels, dimensions, members, paths,
	); err != nil {
		return GraphPlan{}, fmt.Errorf("%w: %v", ErrInvalidGraphResult, err)
	}
	requestHash, _, err := registry.CanonicalContentHash(normalizedRequest)
	if err != nil {
		return GraphPlan{}, err
	}
	plan := GraphPlan{
		PlanVersion: PlanVersion, Scope: normalizedRequest.Scope, DomainID: normalizedRequest.DomainID,
		RequestHash: requestHash, Models: normalizedRefs(models),
		MetricModels:         append([]MetricModelBinding(nil), metricModels...),
		CompatibleDimensions: append([]DimensionCompatibility(nil), dimensions...),
		MemberOwnerships:     append([]MemberOwnership(nil), members...),
		JoinPaths:            append([]JoinPath(nil), paths...),
	}
	plan.normalizeCollections()
	plan.PlanHash, err = plan.contentHash()
	if err != nil {
		return GraphPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return GraphPlan{}, err
	}
	return plan, nil
}

func (plan GraphPlan) Validate() error {
	if plan.PlanVersion != PlanVersion {
		return errors.New("unsupported graph plan version")
	}
	if err := plan.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if err := plan.DomainID.Validate(); err != nil || !containsID(plan.Scope.DomainIDs, plan.DomainID) {
		return errors.New("domainId is invalid or outside policy scope")
	}
	if err := plan.RequestHash.Validate(); err != nil {
		return fmt.Errorf("requestHash: %w", err)
	}
	if err := validateRefSet(plan.Scope.TenantID, ObjectTypeSemanticModel, "models", plan.Models, 0, MaxModelCandidates); err != nil {
		return err
	}
	modelIDs := refIndex(plan.Models)
	seenMetricModels := make(map[string]struct{}, len(plan.MetricModels))
	for index, binding := range plan.MetricModels {
		if err := binding.MetricVersionID.Validate(); err != nil {
			return fmt.Errorf("metricModels[%d].metricVersionId: %w", index, err)
		}
		if _, exists := modelIDs[binding.ModelVersionID]; !exists {
			return fmt.Errorf("metricModels[%d] references an unknown model", index)
		}
		key := string(binding.MetricVersionID) + "\x00" + string(binding.ModelVersionID)
		if _, exists := seenMetricModels[key]; exists {
			return fmt.Errorf("metricModels[%d] is duplicated", index)
		}
		seenMetricModels[key] = struct{}{}
	}
	seenDimensions := make(map[string]struct{}, len(plan.CompatibleDimensions))
	for index, compatible := range plan.CompatibleDimensions {
		if _, exists := modelIDs[compatible.ModelVersionID]; !exists {
			return fmt.Errorf("compatibleDimensions[%d] references an unknown model", index)
		}
		if err := compatible.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("compatibleDimensions[%d].dimensionVersionId: %w", index, err)
		}
		key := string(compatible.ModelVersionID) + "\x00" + string(compatible.DimensionVersionID)
		if _, exists := seenDimensions[key]; exists {
			return fmt.Errorf("compatibleDimensions[%d] is duplicated", index)
		}
		seenDimensions[key] = struct{}{}
	}
	seenMembers := make(map[askdata.ID]struct{}, len(plan.MemberOwnerships))
	for index, ownership := range plan.MemberOwnerships {
		if err := ownership.MemberVersionID.Validate(); err != nil {
			return fmt.Errorf("memberOwnerships[%d].memberVersionId: %w", index, err)
		}
		if err := ownership.DimensionVersionID.Validate(); err != nil {
			return fmt.Errorf("memberOwnerships[%d].dimensionVersionId: %w", index, err)
		}
		if ownership.Status != MemberStatusActive && ownership.Status != MemberStatusExpired {
			return fmt.Errorf("memberOwnerships[%d].status is invalid", index)
		}
		if _, exists := seenMembers[ownership.MemberVersionID]; exists {
			return fmt.Errorf("memberOwnerships[%d] is duplicated", index)
		}
		seenMembers[ownership.MemberVersionID] = struct{}{}
	}
	seenPaths := make(map[askdata.ID]struct{}, len(plan.JoinPaths))
	for index, path := range plan.JoinPaths {
		if err := path.Validate(); err != nil {
			return fmt.Errorf("joinPaths[%d]: %w", index, err)
		}
		if _, exists := modelIDs[path.FromModelVersionID]; !exists {
			return fmt.Errorf("joinPaths[%d] starts outside models", index)
		}
		if _, exists := modelIDs[path.ToModelVersionID]; !exists {
			return fmt.Errorf("joinPaths[%d] ends outside models", index)
		}
		for stepIndex, step := range path.Steps {
			if _, exists := modelIDs[step.FromModelVersionID]; !exists {
				return fmt.Errorf("joinPaths[%d].steps[%d] starts outside models", index, stepIndex)
			}
			if _, exists := modelIDs[step.ToModelVersionID]; !exists {
				return fmt.Errorf("joinPaths[%d].steps[%d] ends outside models", index, stepIndex)
			}
		}
		if _, exists := seenPaths[path.PathID]; exists {
			return fmt.Errorf("joinPaths[%d] is duplicated", index)
		}
		seenPaths[path.PathID] = struct{}{}
	}
	expected, err := plan.contentHash()
	if err != nil {
		return err
	}
	if err := plan.PlanHash.Validate(); err != nil || plan.PlanHash != expected {
		return errors.New("planHash does not match graph plan content")
	}
	canonical := plan
	canonical.normalizeCollections()
	if !equalPlanCollections(plan, canonical) {
		return errors.New("graph plan collections must be sorted and unique")
	}
	return nil
}

func (plan GraphPlan) EvidenceRef() (askdata.EvidenceRef, error) {
	if err := plan.Validate(); err != nil {
		return askdata.EvidenceRef{}, err
	}
	return askdata.EvidenceRef{
		EvidenceID: askdata.ID("graph-plan:" + string(plan.PlanHash)),
		Kind:       askdata.EvidenceKindGraphPath, SourceID: plan.Scope.Release.ReleaseID,
		ContentHash: plan.PlanHash,
	}, nil
}

func (plan GraphPlan) contentHash() (askdata.ContentHash, error) {
	payload := struct {
		PlanVersion  string                   `json:"planVersion"`
		Scope        askdata.PolicyScope      `json:"scope"`
		DomainID     askdata.ID               `json:"domainId"`
		RequestHash  askdata.ContentHash      `json:"requestHash"`
		Models       []ObjectVersionRef       `json:"models"`
		MetricModels []MetricModelBinding     `json:"metricModels"`
		Dimensions   []DimensionCompatibility `json:"compatibleDimensions"`
		Members      []MemberOwnership        `json:"memberOwnerships"`
		Paths        []JoinPath               `json:"joinPaths"`
	}{
		plan.PlanVersion, plan.Scope, plan.DomainID, plan.RequestHash, plan.Models,
		plan.MetricModels, plan.CompatibleDimensions, plan.MemberOwnerships, plan.JoinPaths,
	}
	hash, _, err := registry.CanonicalContentHash(payload)
	return hash, err
}

func (plan *GraphPlan) normalizeCollections() {
	sort.Slice(plan.MetricModels, func(i, j int) bool {
		if plan.MetricModels[i].MetricVersionID != plan.MetricModels[j].MetricVersionID {
			return plan.MetricModels[i].MetricVersionID < plan.MetricModels[j].MetricVersionID
		}
		return plan.MetricModels[i].ModelVersionID < plan.MetricModels[j].ModelVersionID
	})
	plan.MetricModels = deduplicateMetricModels(plan.MetricModels)
	sort.Slice(plan.CompatibleDimensions, func(i, j int) bool {
		if plan.CompatibleDimensions[i].ModelVersionID != plan.CompatibleDimensions[j].ModelVersionID {
			return plan.CompatibleDimensions[i].ModelVersionID < plan.CompatibleDimensions[j].ModelVersionID
		}
		return plan.CompatibleDimensions[i].DimensionVersionID < plan.CompatibleDimensions[j].DimensionVersionID
	})
	plan.CompatibleDimensions = deduplicateDimensions(plan.CompatibleDimensions)
	sort.Slice(plan.MemberOwnerships, func(i, j int) bool {
		return plan.MemberOwnerships[i].MemberVersionID < plan.MemberOwnerships[j].MemberVersionID
	})
	plan.MemberOwnerships = deduplicateMembers(plan.MemberOwnerships)
	sort.Slice(plan.JoinPaths, func(i, j int) bool {
		if len(plan.JoinPaths[i].Steps) != len(plan.JoinPaths[j].Steps) {
			return len(plan.JoinPaths[i].Steps) < len(plan.JoinPaths[j].Steps)
		}
		return plan.JoinPaths[i].PathID < plan.JoinPaths[j].PathID
	})
	plan.JoinPaths = deduplicatePaths(plan.JoinPaths)
}

func validateJoinStep(step JoinStep, index int) error {
	if step.Hop != index+1 {
		return fmt.Errorf("steps[%d].hop must equal %d", index, index+1)
	}
	for name, id := range map[string]askdata.ID{
		"relationshipVersionId": step.RelationshipVersionID,
		"fromModelVersionId":    step.FromModelVersionID,
		"toModelVersionId":      step.ToModelVersionID,
	} {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("steps[%d].%s: %w", index, name, err)
		}
	}
	if step.FromModelVersionID == step.ToModelVersionID {
		return fmt.Errorf("steps[%d] cannot self-join", index)
	}
	if step.Direction != TraversalForward && step.Direction != TraversalReverse {
		return fmt.Errorf("steps[%d].direction is invalid", index)
	}
	switch step.JoinType {
	case registry.JoinInner, registry.JoinLeft, registry.JoinRight, registry.JoinFull:
	default:
		return fmt.Errorf("steps[%d].joinType is invalid", index)
	}
	switch step.Cardinality {
	case registry.CardinalityOneToOne, registry.CardinalityManyToOne,
		registry.CardinalityOneToMany, registry.CardinalityManyToMany:
	default:
		return fmt.Errorf("steps[%d].cardinality is invalid", index)
	}
	switch step.FanoutPolicy {
	case registry.FanoutBlock, registry.FanoutCertifiedPre, registry.FanoutSafe:
	default:
		return fmt.Errorf("steps[%d].fanoutPolicy is invalid", index)
	}
	if step.Cardinality == registry.CardinalityManyToMany && step.FanoutPolicy == registry.FanoutSafe {
		return fmt.Errorf("steps[%d] declares MANY_TO_MANY as SAFE", index)
	}
	return nil
}

func derivePathRisk(steps []JoinStep) (bool, []JoinRiskCode) {
	allowed := true
	seen := make(map[JoinRiskCode]struct{})
	for _, step := range steps {
		switch step.Cardinality {
		case registry.CardinalityManyToOne, registry.CardinalityOneToMany:
			// A path is canonicalized by stable endpoint ID rather than by a
			// fact-to-dimension orientation. Either directional spelling therefore
			// represents a to-many boundary and must surface the same fanout risk.
			seen[JoinRiskOneToMany] = struct{}{}
		case registry.CardinalityManyToMany:
			seen[JoinRiskManyToMany] = struct{}{}
		}
		switch step.FanoutPolicy {
		case registry.FanoutBlock:
			allowed = false
			seen[JoinRiskFanoutBlocked] = struct{}{}
		case registry.FanoutCertifiedPre:
			seen[JoinRiskPreaggregation] = struct{}{}
		}
	}
	codes := make([]JoinRiskCode, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return allowed, codes
}

func validateRefSet(tenantID askdata.ID, objectType ObjectType, name string, refs []ObjectVersionRef, minimum, maximum int) error {
	if len(refs) < minimum || len(refs) > maximum {
		return fmt.Errorf("%s count must be between %d and %d", name, minimum, maximum)
	}
	seenVersionIDs := make(map[askdata.ID]struct{}, len(refs))
	seenVIDs := make(map[string]struct{}, len(refs))
	for index, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%s[%d]: %w", name, index, err)
		}
		vid, err := BuildVID(tenantID, objectType, ref)
		if err != nil {
			return fmt.Errorf("%s[%d]: %w", name, index, err)
		}
		if _, exists := seenVersionIDs[ref.VersionID]; exists {
			return fmt.Errorf("%s contains conflicting or duplicate versionId %q", name, ref.VersionID)
		}
		seenVersionIDs[ref.VersionID] = struct{}{}
		if _, exists := seenVIDs[vid]; exists {
			return fmt.Errorf("%s contains references that resolve to duplicate VID %q", name, vid)
		}
		seenVIDs[vid] = struct{}{}
		if index > 0 && compareRefs(refs[index-1], ref) >= 0 {
			return fmt.Errorf("%s must be sorted and unique", name)
		}
	}
	return nil
}

func normalizedRefs(refs []ObjectVersionRef) []ObjectVersionRef {
	result := append([]ObjectVersionRef(nil), refs...)
	sort.Slice(result, func(i, j int) bool { return compareRefs(result[i], result[j]) < 0 })
	if len(result) < 2 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if compareRefs(result[read-1], result[read]) == 0 {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func compareRefs(left, right ObjectVersionRef) int {
	if left.VersionID != right.VersionID {
		return strings.Compare(string(left.VersionID), string(right.VersionID))
	}
	if left.ObjectID != right.ObjectID {
		return strings.Compare(string(left.ObjectID), string(right.ObjectID))
	}
	return left.Version - right.Version
}

func containsID(values []askdata.ID, target askdata.ID) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= target })
	return index < len(values) && values[index] == target
}

func refIndex(refs []ObjectVersionRef) map[askdata.ID]ObjectVersionRef {
	result := make(map[askdata.ID]ObjectVersionRef, len(refs))
	for _, ref := range refs {
		result[ref.VersionID] = ref
	}
	return result
}

func equalRiskCodes(left, right []JoinRiskCode) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func deduplicateMetricModels(values []MetricModelBinding) []MetricModelBinding {
	return deduplicateSorted(values, func(left, right MetricModelBinding) bool {
		return left == right
	})
}

func deduplicateDimensions(values []DimensionCompatibility) []DimensionCompatibility {
	return deduplicateSorted(values, func(left, right DimensionCompatibility) bool {
		return left == right
	})
}

func deduplicateMembers(values []MemberOwnership) []MemberOwnership {
	return deduplicateSorted(values, func(left, right MemberOwnership) bool {
		return left == right
	})
}

func deduplicatePaths(values []JoinPath) []JoinPath {
	return deduplicateSorted(values, func(left, right JoinPath) bool {
		return left.PathID == right.PathID
	})
}

func deduplicateSorted[T any](values []T, equal func(T, T) bool) []T {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if equal(values[write-1], values[read]) {
			continue
		}
		values[write] = values[read]
		write++
	}
	return values[:write]
}

func equalPlanCollections(left, right GraphPlan) bool {
	leftHash, _, leftErr := registry.CanonicalContentHash(struct {
		Models       []ObjectVersionRef       `json:"models"`
		MetricModels []MetricModelBinding     `json:"metricModels"`
		Dimensions   []DimensionCompatibility `json:"dimensions"`
		Members      []MemberOwnership        `json:"members"`
		Paths        []JoinPath               `json:"paths"`
	}{left.Models, left.MetricModels, left.CompatibleDimensions, left.MemberOwnerships, left.JoinPaths})
	rightHash, _, rightErr := registry.CanonicalContentHash(struct {
		Models       []ObjectVersionRef       `json:"models"`
		MetricModels []MetricModelBinding     `json:"metricModels"`
		Dimensions   []DimensionCompatibility `json:"dimensions"`
		Members      []MemberOwnership        `json:"members"`
		Paths        []JoinPath               `json:"paths"`
	}{right.Models, right.MetricModels, right.CompatibleDimensions, right.MemberOwnerships, right.JoinPaths})
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func validatePlanResultsAgainstRequest(
	request PlanRequest,
	models []ObjectVersionRef,
	metricModels []MetricModelBinding,
	dimensions []DimensionCompatibility,
	members []MemberOwnership,
	paths []JoinPath,
) error {
	requestedMetrics := refIndex(request.MetricRefs)
	requestedModels := refIndex(request.ModelRefs)
	requestedDimensions := refIndex(request.DimensionRefs)
	requestedMembers := refIndex(request.MemberRefs)
	resolvedModels := make(map[askdata.ID]ObjectVersionRef, len(models))
	for index, model := range models {
		if !sameRef(requestedModels[model.VersionID], model) {
			return fmt.Errorf("models[%d] is outside requested model candidates", index)
		}
		if existing, exists := resolvedModels[model.VersionID]; exists && existing != model {
			return fmt.Errorf("models[%d] conflicts with the same versionId", index)
		}
		resolvedModels[model.VersionID] = model
	}
	metricBoundModels := make(map[askdata.ID]struct{}, len(metricModels))
	for index, binding := range metricModels {
		if _, exists := requestedMetrics[binding.MetricVersionID]; !exists {
			return fmt.Errorf("metricModels[%d] references an unrequested metric", index)
		}
		if _, exists := resolvedModels[binding.ModelVersionID]; !exists {
			return fmt.Errorf("metricModels[%d] references an unresolved model", index)
		}
		metricBoundModels[binding.ModelVersionID] = struct{}{}
	}
	for index, compatible := range dimensions {
		if _, exists := resolvedModels[compatible.ModelVersionID]; !exists {
			return fmt.Errorf("compatibleDimensions[%d] references an unresolved model", index)
		}
		if _, exists := requestedDimensions[compatible.DimensionVersionID]; !exists {
			return fmt.Errorf("compatibleDimensions[%d] references an unrequested dimension", index)
		}
	}
	memberOwners := make(map[askdata.ID]MemberOwnership, len(members))
	for index, ownership := range members {
		if _, exists := requestedMembers[ownership.MemberVersionID]; !exists {
			return fmt.Errorf("memberOwnerships[%d] references an unrequested member", index)
		}
		if _, exists := requestedDimensions[ownership.DimensionVersionID]; !exists {
			return fmt.Errorf("memberOwnerships[%d] references an unrequested dimension", index)
		}
		if existing, exists := memberOwners[ownership.MemberVersionID]; exists && existing != ownership {
			return fmt.Errorf("memberOwnerships[%d] conflicts with the same member", index)
		}
		memberOwners[ownership.MemberVersionID] = ownership
	}
	if len(paths) > request.MaxPaths {
		return errors.New("joinPaths exceeds requested path limit")
	}
	for index, path := range paths {
		if err := path.Validate(); err != nil {
			return fmt.Errorf("joinPaths[%d]: %w", index, err)
		}
		if len(path.Steps) > request.MaxJoinHops {
			return fmt.Errorf("joinPaths[%d] exceeds requested hop limit", index)
		}
		if _, exists := metricBoundModels[path.FromModelVersionID]; !exists {
			return fmt.Errorf("joinPaths[%d] starts outside metric-bound models", index)
		}
		if _, exists := metricBoundModels[path.ToModelVersionID]; !exists {
			return fmt.Errorf("joinPaths[%d] ends outside metric-bound models", index)
		}
		for stepIndex, step := range path.Steps {
			if _, exists := requestedModels[step.FromModelVersionID]; !exists {
				return fmt.Errorf("joinPaths[%d].steps[%d] starts outside requested models", index, stepIndex)
			}
			if _, exists := requestedModels[step.ToModelVersionID]; !exists {
				return fmt.Errorf("joinPaths[%d].steps[%d] ends outside requested models", index, stepIndex)
			}
		}
	}
	return nil
}
