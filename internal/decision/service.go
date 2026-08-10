package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

type ApprovalPolicy struct {
	ID                string
	RequiredApprovals int
	ApproverUserIDs   []askdata.ID
}

type Repository interface {
	ResolveApprovalPolicy(context.Context, Identity, string) (ApprovalPolicy, error)
	Create(context.Context, Identity, CreateInput, ApprovalPolicy, []Evidence, time.Time) (Aggregate, error)
	List(context.Context, Identity, string, int, string) ([]Decision, string, error)
	Get(context.Context, Identity, askdata.ID, bool) (Aggregate, error)
	Update(context.Context, Identity, askdata.ID, UpdateInput, time.Time) (Decision, error)
	Submit(context.Context, Identity, askdata.ID, int64, ApprovalPolicy, time.Time) (Aggregate, error)
	DecideApproval(context.Context, Identity, askdata.ID, int64, bool, string, time.Time) (Aggregate, error)
	CreateAction(context.Context, Identity, askdata.ID, CreateActionInput, time.Time) (Action, error)
	TransitionAction(context.Context, Identity, askdata.ID, askdata.ID, TransitionActionInput, time.Time) (Action, error)
	AddOutcomeMetric(context.Context, Identity, askdata.ID, AddMetricInput, time.Time) (OutcomeMetric, error)
	SaveOutcomeRefresh(context.Context, Identity, askdata.ID, askdata.ID, OutcomeRefresh, time.Time) (OutcomeMetric, error)
	ConfirmOutcome(context.Context, Identity, askdata.ID, ConfirmOutcomeInput, time.Time) (OutcomeReview, error)
	TransitionDecision(context.Context, Identity, askdata.ID, int64, Status, string, time.Time) (Aggregate, error)
	MarkReviewDue(context.Context, time.Time, int) (int, error)
	EscalateActions(context.Context, time.Time, int) (int, error)
}

type EvidenceVerifier interface {
	Verify(context.Context, Identity, EvidenceInput) (Evidence, error)
}

type EvidenceResolver interface {
	Resolve(context.Context, Identity, SourceType, askdata.ID) (EvidenceInput, error)
}

type OutcomeRunner interface {
	Refresh(context.Context, Identity, OutcomeMetric) (OutcomeRefresh, error)
}

type OutcomeRefresh struct {
	Value           string
	ResultHash      askdata.ContentHash
	PolicyScopeHash askdata.ContentHash
	AsOf            time.Time
	Drifted         bool
	Status          string
}

type Service struct {
	repository Repository
	evidence   EvidenceVerifier
	outcomes   OutcomeRunner
	now        func() time.Time
}

func NewService(repository Repository, evidence EvidenceVerifier, outcomes OutcomeRunner) (*Service, error) {
	if repository == nil || evidence == nil {
		return nil, errors.New("decision service dependencies are incomplete")
	}
	return &Service{repository: repository, evidence: evidence, outcomes: outcomes, now: time.Now}, nil
}

func (service *Service) Create(ctx context.Context, identity Identity, input CreateInput) (Aggregate, error) {
	now := service.clock()
	if identity.Validate() != nil || input.Validate(now) != nil {
		return Aggregate{}, ErrInvalid
	}
	policy, err := service.repository.ResolveApprovalPolicy(ctx, identity, input.ApprovalPolicyID)
	if err != nil {
		return Aggregate{}, err
	}
	if policy.ID != input.ApprovalPolicyID || policy.RequiredApprovals < 1 || policy.RequiredApprovals > len(policy.ApproverUserIDs) {
		return Aggregate{}, ErrPolicyUnavailable
	}
	verified := make([]Evidence, 0, len(input.Evidence))
	for _, item := range input.Evidence {
		value, verifyErr := service.evidence.Verify(ctx, identity, item)
		if verifyErr != nil || !value.Verified || value.SourceHash != item.SourceHash || value.SemanticReleaseID != item.SemanticReleaseID {
			return Aggregate{}, ErrEvidenceInvalid
		}
		verified = append(verified, value)
	}
	return service.repository.Create(ctx, identity, input, policy, verified, now)
}

func (service *Service) List(ctx context.Context, identity Identity, scope string, limit int, cursor string) ([]Decision, string, error) {
	if identity.Validate() != nil || limit < 1 || limit > 200 {
		return nil, "", ErrInvalid
	}
	scope = strings.ToUpper(strings.TrimSpace(scope))
	if scope != "" && scope != "MINE" && scope != "APPROVALS" && scope != "ACTIONS" && scope != "REVIEWS" {
		return nil, "", ErrInvalid
	}
	return service.repository.List(ctx, identity, scope, limit, strings.TrimSpace(cursor))
}

func (service *Service) Get(ctx context.Context, identity Identity, id askdata.ID, events bool) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(id) {
		return Aggregate{}, ErrInvalid
	}
	return service.repository.Get(ctx, identity, id, events)
}

func (service *Service) PrefillEvidence(ctx context.Context, identity Identity, sourceType SourceType, sourceID askdata.ID) (EvidenceInput, error) {
	if identity.Validate() != nil || !validUUID(sourceID) {
		return EvidenceInput{}, ErrInvalid
	}
	resolver, ok := service.evidence.(EvidenceResolver)
	if !ok {
		return EvidenceInput{}, ErrEvidenceInvalid
	}
	return resolver.Resolve(ctx, identity, sourceType, sourceID)
}

func (service *Service) Update(ctx context.Context, identity Identity, id askdata.ID, input UpdateInput) (Decision, error) {
	if identity.Validate() != nil || !validUUID(id) || input.ExpectedVersion < 1 ||
		!validText(input.Title, 1, 256) || !validText(input.Question, 1, 4096) || len(input.Decision) > 8192 ||
		len(input.ExpectedEffect) > 4096 || input.ReviewAt.IsZero() || len(input.Risks) > 64 {
		return Decision{}, ErrInvalid
	}
	return service.repository.Update(ctx, identity, id, input, service.clock())
}

func (service *Service) Submit(ctx context.Context, identity Identity, id askdata.ID, expectedVersion int64) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(id) || expectedVersion < 1 {
		return Aggregate{}, ErrInvalid
	}
	current, err := service.repository.Get(ctx, identity, id, false)
	if err != nil {
		return Aggregate{}, err
	}
	if current.Decision.Status != StatusDraft && current.Decision.Status != StatusReopened {
		return Aggregate{}, ErrIllegalTransition
	}
	if current.Decision.EvidenceMode == EvidencePlatformVerified && len(current.Evidence) == 0 {
		return Aggregate{}, ErrEvidenceInvalid
	}
	policy, err := service.repository.ResolveApprovalPolicy(ctx, identity, current.Decision.ApprovalPolicyID)
	if err != nil {
		return Aggregate{}, err
	}
	return service.repository.Submit(ctx, identity, id, expectedVersion, policy, service.clock())
}

func (service *Service) DecideApproval(ctx context.Context, identity Identity, id askdata.ID, expectedVersion int64, approve bool, comment string) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(id) || expectedVersion < 1 || len(comment) > 4096 || strings.TrimSpace(comment) != comment || (!approve && !validText(comment, 1, 4096)) {
		return Aggregate{}, ErrInvalid
	}
	return service.repository.DecideApproval(ctx, identity, id, expectedVersion, approve, comment, service.clock())
}

func (service *Service) CreateAction(ctx context.Context, identity Identity, decisionID askdata.ID, input CreateActionInput) (Action, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || !validUUID(input.AssigneeUserID) ||
		!validText(input.Title, 1, 256) || len(input.Description) > 4096 || input.DueAt.IsZero() || input.DueAt.Before(service.clock()) || len(input.DeliverableRefs) > 32 {
		return Action{}, ErrInvalid
	}
	for _, ref := range input.DeliverableRefs {
		if !validText(ref, 1, 512) {
			return Action{}, ErrInvalid
		}
	}
	return service.repository.CreateAction(ctx, identity, decisionID, input, service.clock())
}

func (service *Service) TransitionAction(ctx context.Context, identity Identity, decisionID, actionID askdata.ID, input TransitionActionInput) (Action, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || !validUUID(actionID) || input.ExpectedVersion < 1 ||
		len(input.Reason) > 4096 || len(input.CompletionEvidence) > 2048 {
		return Action{}, ErrInvalid
	}
	if input.Target == ActionBlocked && !validText(input.Reason, 1, 4096) || input.Target == ActionDone && !validText(input.CompletionEvidence, 1, 2048) ||
		(input.Target == ActionDoing && input.Reason == "" && input.CompletionEvidence != "") {
		return Action{}, ErrInvalid
	}
	return service.repository.TransitionAction(ctx, identity, decisionID, actionID, input, service.clock())
}

func (service *Service) AddOutcomeMetric(ctx context.Context, identity Identity, decisionID askdata.ID, input AddMetricInput) (OutcomeMetric, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || !validUUID(askdata.ID(input.MetricVersionID)) ||
		input.SemanticIRHash.Validate() != nil || !validUUID(input.SemanticReleaseID) || input.SemanticReleaseHash.Validate() != nil ||
		len(input.SemanticIR) == 0 || validateNoForbiddenJSON(input.SemanticIR) != nil || input.ReviewAt.IsZero() || len(input.AttributionNote) > 4096 ||
		!validDecimal(input.BaselineValue) || !validMetricTarget(input) {
		return OutcomeMetric{}, ErrInvalid
	}
	semanticIR, err := ir.Decode(input.SemanticIR)
	if err != nil || semanticIR.DomainID != identity.DomainID || semanticIR.SemanticReleaseID != input.SemanticReleaseID ||
		semanticIR.SemanticContentHash != input.SemanticReleaseHash || len(semanticIR.Metrics) != 1 ||
		string(semanticIR.Metrics[0].MetricVersionID) != input.MetricVersionID || len(semanticIR.GroupBy) != 0 || semanticIR.Limit != 1 {
		return OutcomeMetric{}, ErrInvalid
	}
	_, canonical, hash, err := ir.Canonicalize(semanticIR)
	if err != nil || hash != input.SemanticIRHash {
		return OutcomeMetric{}, ErrInvalid
	}
	input.SemanticIR = canonical
	return service.repository.AddOutcomeMetric(ctx, identity, decisionID, input, service.clock())
}

func (service *Service) RefreshOutcome(ctx context.Context, identity Identity, decisionID askdata.ID) ([]OutcomeMetric, error) {
	if identity.Validate() != nil || !validUUID(decisionID) {
		return nil, ErrInvalid
	}
	if service.outcomes == nil {
		return nil, ErrOutcomeBlocked
	}
	aggregate, err := service.repository.Get(ctx, identity, decisionID, false)
	if err != nil {
		return nil, err
	}
	if aggregate.Decision.Status != StatusInExecution && aggregate.Decision.Status != StatusReviewDue && aggregate.Decision.Status != StatusReopened {
		return nil, ErrIllegalTransition
	}
	results := make([]OutcomeMetric, 0, len(aggregate.Metrics))
	for _, metric := range aggregate.Metrics {
		refresh, runErr := service.outcomes.Refresh(ctx, identity, metric)
		if runErr != nil {
			return nil, runErr
		}
		stored, saveErr := service.repository.SaveOutcomeRefresh(ctx, identity, decisionID, metric.ID, refresh, service.clock())
		if saveErr != nil {
			return nil, saveErr
		}
		results = append(results, stored)
	}
	if len(results) == 0 {
		return nil, ErrOutcomeBlocked
	}
	return results, nil
}

func (service *Service) ConfirmOutcome(ctx context.Context, identity Identity, decisionID askdata.ID, input ConfirmOutcomeInput) (OutcomeReview, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || input.ExpectedVersion < 1 || len(input.Notes) > 4096 {
		return OutcomeReview{}, ErrInvalid
	}
	if input.Conclusion != ConclusionAchieved && input.Conclusion != ConclusionPartial && input.Conclusion != ConclusionNotAchieved && input.Conclusion != ConclusionInconclusive {
		return OutcomeReview{}, ErrInvalid
	}
	return service.repository.ConfirmOutcome(ctx, identity, decisionID, input, service.clock())
}

func (service *Service) Close(ctx context.Context, identity Identity, decisionID askdata.ID, expectedVersion int64, reason string) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || expectedVersion < 1 || len(reason) > 4096 {
		return Aggregate{}, ErrInvalid
	}
	current, err := service.repository.Get(ctx, identity, decisionID, false)
	if err != nil {
		return Aggregate{}, err
	}
	if current.Decision.Status != StatusReviewDue {
		return Aggregate{}, ErrIllegalTransition
	}
	allTerminal := len(current.Actions) > 0
	for _, action := range current.Actions {
		if action.Status != ActionDone && action.Status != ActionCanceled {
			allTerminal = false
		}
	}
	reviewed := current.Review != nil && (current.Review.Status == ReviewConfirmed || current.Review.Status == ReviewInconclusive)
	if !allTerminal || !reviewed {
		if !validText(reason, 1, 4096) {
			return Aggregate{}, ErrOutcomeBlocked
		}
	}
	return service.repository.TransitionDecision(ctx, identity, decisionID, expectedVersion, StatusClosed, reason, service.clock())
}

func (service *Service) Reopen(ctx context.Context, identity Identity, decisionID askdata.ID, expectedVersion int64, reason string) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || expectedVersion < 1 || !validText(reason, 1, 4096) {
		return Aggregate{}, ErrInvalid
	}
	return service.repository.TransitionDecision(ctx, identity, decisionID, expectedVersion, StatusReopened, reason, service.clock())
}

func (service *Service) Cancel(ctx context.Context, identity Identity, decisionID askdata.ID, expectedVersion int64, reason string) (Aggregate, error) {
	if identity.Validate() != nil || !validUUID(decisionID) || expectedVersion < 1 || !validText(reason, 1, 4096) {
		return Aggregate{}, ErrInvalid
	}
	return service.repository.TransitionDecision(ctx, identity, decisionID, expectedVersion, StatusCanceled, reason, service.clock())
}

func (service *Service) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	now := service.clock()
	marked, err := service.repository.MarkReviewDue(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	escalated, err := service.repository.EscalateActions(ctx, now, limit)
	return marked + escalated, err
}

func (service *Service) clock() time.Time {
	if service.now == nil {
		return time.Now().UTC()
	}
	return service.now().UTC()
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, askdata.ContentHash, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, "", ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return canonical, askdata.HashBytes(canonical), nil
}

func validDecimal(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}

func validMetricTarget(input AddMetricInput) bool {
	valid := func(value *string) bool { return value != nil && validDecimal(*value) }
	switch input.TargetDirection {
	case DirectionIncrease, DirectionDecrease:
		return input.TargetValue == nil && input.TargetUpperValue == nil
	case DirectionAtLeast, DirectionAtMost:
		return valid(input.TargetValue) && input.TargetUpperValue == nil
	case DirectionRange:
		if !valid(input.TargetValue) || !valid(input.TargetUpperValue) {
			return false
		}
		lower, _ := new(big.Rat).SetString(*input.TargetValue)
		upper, _ := new(big.Rat).SetString(*input.TargetUpperValue)
		return lower.Cmp(upper) <= 0
	default:
		return false
	}
}
