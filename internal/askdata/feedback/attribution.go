package feedback

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

// SuggestStage derives an auditable suggestion only from typed artifact facts;
// it never reads prompts, result rows, SQL, or free-form user text.
func SuggestStage(issue IssueType, snapshot orchestrator.ReplaySnapshot) Stage {
	has := func(kind orchestrator.ArtifactType) bool {
		for _, artifact := range snapshot.Artifacts {
			if artifact.Type == kind {
				return true
			}
		}
		return false
	}
	switch issue {
	case IssueNarrative:
		return StageNarrative
	case IssuePermission, IssueUnderstanding:
		return StageUnderstanding
	case IssueMember:
		if !has(orchestrator.ArtifactCandidateSet) {
			return StageRetrieval
		}
		return StageBinding
	case IssueMetric, IssueDimension:
		return StageBinding
	case IssueComparison:
		if !has(orchestrator.ArtifactGraphPlan) {
			return StageGraph
		}
		return StageCompile
	case IssueTime:
		if has(orchestrator.ArtifactSemanticIR) {
			return StageCompile
		}
		return StageUnderstanding
	case IssueResult:
		if has(orchestrator.ArtifactResultVerification) {
			return StageData
		}
		return StageExecution
	default:
		if has(orchestrator.ArtifactQueryPlan) {
			return StageExecution
		}
		return StageUnderstanding
	}
}

func FromLegacyIssue(issue string) IssueType {
	switch issue {
	case "METRIC":
		return IssueMetric
	case "DIMENSION":
		return IssueDimension
	case "MEMBER":
		return IssueMember
	case "TIME":
		return IssueTime
	case "RELATIONSHIP":
		return IssueComparison
	case "DATA":
		return IssueResult
	case "PERMISSION":
		return IssuePermission
	case "EXPRESSION":
		return IssueUnderstanding
	default:
		return IssueOther
	}
}

const (
	AttributionSchemaVersion  = "feedback-attribution-v1"
	AttributionStateSuggested = "SUGGESTED"
)

//go:embed schemas/feedback-attribution-v1.schema.json
var attributionSchemaJSON []byte

type AttributionCategory string

const (
	AttributionMetric       AttributionCategory = "METRIC"
	AttributionDimension    AttributionCategory = "DIMENSION"
	AttributionMember       AttributionCategory = "MEMBER"
	AttributionTime         AttributionCategory = "TIME"
	AttributionRelationship AttributionCategory = "RELATIONSHIP"
	AttributionData         AttributionCategory = "DATA"
	AttributionPermission   AttributionCategory = "PERMISSION"
	AttributionExpression   AttributionCategory = "EXPRESSION"
)

type ChangeCandidateType string

const (
	ChangeMetricDefinition    ChangeCandidateType = "METRIC_DEFINITION"
	ChangeDimensionDefinition ChangeCandidateType = "DIMENSION_DEFINITION"
	ChangeMemberAlias         ChangeCandidateType = "MEMBER_ALIAS"
	ChangeTimeContract        ChangeCandidateType = "TIME_CONTRACT"
	ChangeRelationship        ChangeCandidateType = "RELATIONSHIP"
	ChangeDataQualityRule     ChangeCandidateType = "DATA_QUALITY_RULE"
	ChangePermissionReview    ChangeCandidateType = "PERMISSION_POLICY_REVIEW"
	ChangeExpressionPolicy    ChangeCandidateType = "EXPRESSION_POLICY"
)

var allowedCandidateForCategory = map[AttributionCategory]ChangeCandidateType{
	AttributionMetric: ChangeMetricDefinition, AttributionDimension: ChangeDimensionDefinition,
	AttributionMember: ChangeMemberAlias, AttributionTime: ChangeTimeContract,
	AttributionRelationship: ChangeRelationship, AttributionData: ChangeDataQualityRule,
	AttributionPermission: ChangePermissionReview, AttributionExpression: ChangeExpressionPolicy,
}

type AttributionFact struct {
	EvidenceID  askdata.ID          `json:"evidenceId"`
	Kind        string              `json:"kind"`
	ContentHash askdata.ContentHash `json:"contentHash"`
	Summary     json.RawMessage     `json:"summary"`
}

type AttributionEvidenceRef struct {
	EvidenceID  askdata.ID          `json:"evidenceId"`
	ContentHash askdata.ContentHash `json:"contentHash"`
}

type AttributionProposal struct {
	Category        AttributionCategory      `json:"category"`
	CandidateType   ChangeCandidateType      `json:"candidateType"`
	TargetObjectIDs []askdata.ID             `json:"targetObjectIds"`
	Evidence        []AttributionEvidenceRef `json:"evidence"`
	Confidence      float64                  `json:"confidence"`
	RationaleCode   string                   `json:"rationaleCode"`
}

type AttributionEnvelope struct {
	SchemaVersion string                `json:"schemaVersion"`
	Proposals     []AttributionProposal `json:"proposals"`
}

type SuggestedChange struct {
	AttributionProposal
	State       string              `json:"state"`
	ContentHash askdata.ContentHash `json:"contentHash"`
}

type AttributionRequest struct {
	TenantID, ActorID, DomainID string
	TicketID, RunID             askdata.ID
	PromptVersion               string
	PreferredModel              string
	Issue                       IssueType
	AllowedObjectIDs            []askdata.ID
	Facts                       []AttributionFact
}

type AttributionResult struct {
	Suggestions    []SuggestedChange
	AIRequestID    string
	ProviderModel  string
	Attempts       int
	CostMicros     int64
	RedactionCount int
}

type attributionInvoker interface {
	Invoke(context.Context, ai.Invocation) (ai.InvocationResult, error)
}

// Attributor has no registry repository by design. Its only output is a
// SUGGESTED candidate that must enter the existing human review lifecycle.
type Attributor struct {
	invoker attributionInvoker
	schema  ai.JSONSchema
}

func NewAttributor(invoker attributionInvoker) (*Attributor, error) {
	if invoker == nil {
		return nil, errors.New("feedback attribution invoker is required")
	}
	schema := ai.JSONSchema{Name: "feedback_attribution_v1", Description: "bounded feedback attribution suggestions", Schema: append([]byte(nil), attributionSchemaJSON...)}
	if err := ai.ValidateProviderRequest(ai.ProviderRequest{
		Messages:       []ai.Message{{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "validate feedback attribution schema"}}}},
		ResponseSchema: schema,
	}); err != nil {
		return nil, err
	}
	return &Attributor{invoker: invoker, schema: schema}, nil
}

func (attributor *Attributor) Attribute(ctx context.Context, request AttributionRequest) (AttributionResult, error) {
	if attributor == nil || attributor.invoker == nil || ctx == nil {
		return AttributionResult{}, ErrInvalid
	}
	for _, value := range []string{request.TenantID, request.ActorID, request.DomainID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != strings.ToLower(value) {
			return AttributionResult{}, ErrInvalid
		}
	}
	if request.TicketID.Validate() != nil || request.RunID.Validate() != nil || request.PromptVersion == "" ||
		len(request.Facts) < 1 || len(request.Facts) > 64 || len(request.AllowedObjectIDs) > 128 {
		return AttributionResult{}, ErrInvalid
	}
	messages, err := buildAttributionMessages(request)
	if err != nil {
		return AttributionResult{}, err
	}
	invocation, err := attributor.invoker.Invoke(ctx, ai.Invocation{
		TenantID: request.TenantID, ActorID: request.ActorID, Purpose: ai.PurposeSemanticQuestion,
		PromptVersion: request.PromptVersion, ResourceType: "FEEDBACK_ATTRIBUTION", ResourceID: string(request.TicketID),
		PreferredModel: request.PreferredModel,
		Request:        ai.ProviderRequest{Messages: messages, ResponseSchema: attributor.schema, MaxOutputTokens: 4096},
	})
	if err != nil {
		return AttributionResult{}, err
	}
	var envelope AttributionEnvelope
	if err := askdata.DecodeStrictJSON(invocation.ProviderResult.Content, &envelope); err != nil {
		return AttributionResult{}, err
	}
	suggestions, err := ValidateAttribution(envelope, request)
	if err != nil {
		return AttributionResult{}, err
	}
	return AttributionResult{
		Suggestions: suggestions, AIRequestID: invocation.RequestID, ProviderModel: invocation.ProviderResult.Model,
		Attempts: invocation.Attempts, CostMicros: invocation.CostMicros, RedactionCount: invocation.RedactionCount,
	}, nil
}

func ValidateAttribution(envelope AttributionEnvelope, request AttributionRequest) ([]SuggestedChange, error) {
	if envelope.SchemaVersion != AttributionSchemaVersion || len(envelope.Proposals) < 1 || len(envelope.Proposals) > 16 {
		return nil, ErrInvalid
	}
	allowedEvidence := map[askdata.ID]askdata.ContentHash{}
	for _, fact := range request.Facts {
		allowedEvidence[fact.EvidenceID] = fact.ContentHash
	}
	allowedObjects := map[askdata.ID]struct{}{}
	for _, id := range request.AllowedObjectIDs {
		if id.Validate() != nil {
			return nil, ErrInvalid
		}
		allowedObjects[id] = struct{}{}
	}
	result := make([]SuggestedChange, len(envelope.Proposals))
	for index, proposal := range envelope.Proposals {
		if allowedCandidateForCategory[proposal.Category] != proposal.CandidateType || proposal.Confidence < 0 || proposal.Confidence > 1 ||
			!stableAttributionCode(proposal.RationaleCode) || len(proposal.TargetObjectIDs) > 32 || len(proposal.Evidence) < 1 || len(proposal.Evidence) > 32 {
			return nil, ErrInvalid
		}
		seenObjects := map[askdata.ID]struct{}{}
		for _, id := range proposal.TargetObjectIDs {
			if _, allowed := allowedObjects[id]; !allowed {
				return nil, fmt.Errorf("%w: proposal %d invented an object", ErrInvalid, index)
			}
			if _, duplicate := seenObjects[id]; duplicate {
				return nil, ErrInvalid
			}
			seenObjects[id] = struct{}{}
		}
		seenEvidence := map[askdata.ID]struct{}{}
		for _, evidence := range proposal.Evidence {
			if allowedEvidence[evidence.EvidenceID] != evidence.ContentHash {
				return nil, fmt.Errorf("%w: proposal %d invented evidence", ErrInvalid, index)
			}
			if _, duplicate := seenEvidence[evidence.EvidenceID]; duplicate {
				return nil, ErrInvalid
			}
			seenEvidence[evidence.EvidenceID] = struct{}{}
		}
		proposal.TargetObjectIDs = append([]askdata.ID(nil), proposal.TargetObjectIDs...)
		sort.Slice(proposal.TargetObjectIDs, func(i, j int) bool { return proposal.TargetObjectIDs[i] < proposal.TargetObjectIDs[j] })
		proposal.Evidence = append([]AttributionEvidenceRef(nil), proposal.Evidence...)
		sort.Slice(proposal.Evidence, func(i, j int) bool { return proposal.Evidence[i].EvidenceID < proposal.Evidence[j].EvidenceID })
		payload, err := json.Marshal(proposal)
		if err != nil {
			return nil, err
		}
		result[index] = SuggestedChange{AttributionProposal: proposal, State: AttributionStateSuggested, ContentHash: askdata.HashBytes(payload)}
	}
	return result, nil
}

func BuildAttributionFacts(snapshot orchestrator.ReplaySnapshot) ([]AttributionFact, []askdata.ID, error) {
	if snapshot.Run.Validate() != nil || len(snapshot.Artifacts) == 0 {
		return nil, nil, ErrInvalid
	}
	facts := make([]AttributionFact, 0, len(snapshot.Artifacts))
	objectIDs := map[askdata.ID]struct{}{}
	for _, artifact := range snapshot.Artifacts {
		summary := struct {
			ArtifactType  string       `json:"artifactType"`
			SchemaVersion string       `json:"schemaVersion"`
			EvidenceIDs   []askdata.ID `json:"evidenceIds"`
		}{string(artifact.Type), artifact.SchemaVersion, append([]askdata.ID(nil), artifact.EvidenceIDs...)}
		payload, err := json.Marshal(summary)
		if err != nil {
			return nil, nil, err
		}
		facts = append(facts, AttributionFact{EvidenceID: artifact.ID, Kind: string(artifact.Type), ContentHash: artifact.Hash, Summary: payload})
		for _, id := range artifact.EvidenceIDs {
			objectIDs[id] = struct{}{}
		}
	}
	ids := make([]askdata.ID, 0, len(objectIDs))
	for id := range objectIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return facts, ids, nil
}

func buildAttributionMessages(request AttributionRequest) ([]ai.Message, error) {
	type safeFact struct {
		EvidenceID  askdata.ID          `json:"evidenceId"`
		Kind        string              `json:"kind"`
		ContentHash askdata.ContentHash `json:"contentHash"`
		Summary     json.RawMessage     `json:"summary"`
	}
	envelope := struct {
		Stage            string       `json:"stage"`
		Issue            IssueType    `json:"issueType"`
		RunID            askdata.ID   `json:"runId"`
		AllowedObjectIDs []askdata.ID `json:"allowedObjectIds"`
		Facts            []safeFact   `json:"facts"`
	}{Stage: "FEEDBACK_ATTRIBUTION", Issue: request.Issue, RunID: request.RunID, AllowedObjectIDs: request.AllowedObjectIDs, Facts: make([]safeFact, len(request.Facts))}
	seen := map[askdata.ID]struct{}{}
	for index, fact := range request.Facts {
		if fact.EvidenceID.Validate() != nil || fact.ContentHash.Validate() != nil || !stableAttributionCode(fact.Kind) || !safeLearningJSON(fact.Summary) {
			return nil, ErrInvalid
		}
		if _, duplicate := seen[fact.EvidenceID]; duplicate {
			return nil, ErrInvalid
		}
		seen[fact.EvidenceID] = struct{}{}
		var value any
		if err := askdata.DecodeStrictJSON(fact.Summary, &value); err != nil {
			return nil, ErrInvalid
		}
		canonical, _ := json.Marshal(value)
		envelope.Facts[index] = safeFact{fact.EvidenceID, fact.Kind, fact.ContentHash, canonical}
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return []ai.Message{
		{Role: ai.MessageRoleSystem, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "Attribute feedback only to the closed categories. Return review candidates; never mutate or publish semantic assets."}}},
		{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(payload)}}},
	}, nil
}

func stableAttributionCode(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
