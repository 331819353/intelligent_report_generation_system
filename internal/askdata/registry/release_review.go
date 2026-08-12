package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/security/promptguard"
)

const (
	ReleaseReviewSchemaVersion = "release-review-report-v1"
	MaxReleaseReviewEvidence   = 128
)

//go:embed schemas/release-review-report-v1.schema.json
var releaseReviewSchemaJSON []byte

var ErrReleaseReviewInvalid = errors.New("semantic release review is invalid")

type ReleaseReviewEvidence struct {
	EvidenceID  askdata.ID          `json:"evidenceId"`
	Kind        string              `json:"kind"`
	ContentHash askdata.ContentHash `json:"contentHash"`
	Payload     json.RawMessage     `json:"payload"`
}

type ReleaseReviewRisk struct {
	Code        string       `json:"code"`
	Severity    string       `json:"severity"`
	EvidenceIDs []askdata.ID `json:"evidenceIds"`
}

type ReleaseReviewEnvelope struct {
	SchemaVersion       string              `json:"schemaVersion"`
	Recommendation      string              `json:"recommendation"`
	ImpactCodes         []string            `json:"impactCodes"`
	FailureClusterCodes []string            `json:"failureClusterCodes"`
	Risks               []ReleaseReviewRisk `json:"risks"`
	EvidenceIDs         []askdata.ID        `json:"evidenceIds"`
}

type ReleaseReviewLLMRequest struct {
	TenantID       string
	DomainID       string
	ActorID        string
	ReleaseID      string
	PromptVersion  string
	PreferredModel string
	GatePassed     bool
	Evidence       []ReleaseReviewEvidence
}

type ReleaseReviewLLMResult struct {
	Report         ReleaseReviewEnvelope `json:"report"`
	ReportHash     askdata.ContentHash   `json:"reportHash"`
	AIRequestID    string                `json:"aiRequestId"`
	ProviderModel  string                `json:"providerModel"`
	Attempts       int                   `json:"attempts"`
	CostMicros     int64                 `json:"costMicros"`
	RedactionCount int                   `json:"redactionCount"`
}

type releaseReviewInvoker interface {
	Invoke(context.Context, ai.Invocation) (ai.InvocationResult, error)
}

type ReleaseReviewer struct {
	invoker releaseReviewInvoker
	schema  ai.JSONSchema
}

func NewReleaseReviewer(invoker releaseReviewInvoker) (*ReleaseReviewer, error) {
	if invoker == nil {
		return nil, ErrReleaseReviewInvalid
	}
	schema := ai.JSONSchema{
		Name: "release_review_report_v1", Description: "evidence-bound advisory semantic release review",
		Schema: append([]byte(nil), releaseReviewSchemaJSON...),
	}
	if err := ai.ValidateProviderRequest(ai.ProviderRequest{
		Messages:       []ai.Message{{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "validate release review schema"}}}},
		ResponseSchema: schema,
	}); err != nil {
		return nil, fmt.Errorf("%w: schema: %v", ErrReleaseReviewInvalid, err)
	}
	return &ReleaseReviewer{invoker: invoker, schema: schema}, nil
}

func (reviewer *ReleaseReviewer) Review(
	ctx context.Context,
	request ReleaseReviewLLMRequest,
) (ReleaseReviewLLMResult, error) {
	if reviewer == nil || reviewer.invoker == nil || ctx == nil ||
		!canonicalAdminUUID(request.TenantID) || !canonicalAdminUUID(request.DomainID) ||
		!canonicalAdminUUID(request.ActorID) || !canonicalAdminUUID(request.ReleaseID) ||
		strings.TrimSpace(request.PromptVersion) == "" {
		return ReleaseReviewLLMResult{}, ErrReleaseReviewInvalid
	}
	messages, evidence, err := releaseReviewMessages(request)
	if err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	invoked, err := reviewer.invoker.Invoke(ctx, ai.Invocation{
		TenantID: request.TenantID, ActorID: request.ActorID,
		Purpose: ai.PurposeSemanticQuestion, PromptVersion: strings.TrimSpace(request.PromptVersion),
		ResourceType: "SEMANTIC_RELEASE_REVIEW", ResourceID: request.ReleaseID,
		PreferredModel: strings.TrimSpace(request.PreferredModel),
		Request:        ai.ProviderRequest{Messages: messages, ResponseSchema: reviewer.schema, MaxOutputTokens: 4096},
	})
	if err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	var envelope ReleaseReviewEnvelope
	if err := askdata.DecodeStrictJSON(invoked.ProviderResult.Content, &envelope); err != nil {
		return ReleaseReviewLLMResult{}, fmt.Errorf("%w: output: %v", ErrReleaseReviewInvalid, err)
	}
	if err := validateReleaseReviewEnvelope(envelope, request.GatePassed, evidence); err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	return ReleaseReviewLLMResult{
		Report: envelope, ReportHash: askdata.HashBytes(canonical),
		AIRequestID: invoked.RequestID, ProviderModel: invoked.ProviderResult.Model,
		Attempts: invoked.Attempts, CostMicros: invoked.CostMicros,
		RedactionCount: invoked.RedactionCount,
	}, nil
}

func releaseReviewMessages(
	request ReleaseReviewLLMRequest,
) ([]ai.Message, map[askdata.ID]askdata.ContentHash, error) {
	if len(request.Evidence) < 1 || len(request.Evidence) > MaxReleaseReviewEvidence {
		return nil, nil, ErrReleaseReviewInvalid
	}
	type promptEvidence struct {
		EvidenceID  askdata.ID                   `json:"evidenceId"`
		Kind        string                       `json:"kind"`
		TrustLabel  promptguard.PromptTrustLabel `json:"trustLabel"`
		Executable  bool                         `json:"executable"`
		ContentHash askdata.ContentHash          `json:"contentHash"`
		Payload     json.RawMessage              `json:"payload"`
	}
	items := make([]promptEvidence, len(request.Evidence))
	known := make(map[askdata.ID]askdata.ContentHash, len(items))
	for index, evidence := range request.Evidence {
		if evidence.EvidenceID.Validate() != nil || evidence.ContentHash.Validate() != nil ||
			!stableUpperCode(evidence.Kind) || len(evidence.Payload) < 2 || len(evidence.Payload) > 131072 {
			return nil, nil, ErrReleaseReviewInvalid
		}
		var payload map[string]any
		if err := askdata.DecodeStrictJSON(evidence.Payload, &payload); err != nil {
			return nil, nil, ErrReleaseReviewInvalid
		}
		canonical, err := json.Marshal(payload)
		if err != nil || askdata.HashBytes(canonical) != evidence.ContentHash {
			return nil, nil, ErrReleaseReviewInvalid
		}
		if _, duplicate := known[evidence.EvidenceID]; duplicate {
			return nil, nil, ErrReleaseReviewInvalid
		}
		assessment, err := promptguard.AssessUntrustedPromptData(evidence.Kind, canonical)
		if err != nil {
			return nil, nil, err
		}
		known[evidence.EvidenceID] = evidence.ContentHash
		items[index] = promptEvidence{
			EvidenceID: evidence.EvidenceID, Kind: evidence.Kind,
			TrustLabel: assessment.TrustLabel, Executable: false,
			ContentHash: evidence.ContentHash, Payload: canonical,
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].EvidenceID < items[j].EvidenceID })
	payload, err := json.Marshal(map[string]any{
		"gatePassed": request.GatePassed, "evidence": items,
	})
	if err != nil {
		return nil, nil, err
	}
	return []ai.Message{
		{Role: ai.MessageRoleSystem, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: "你是语义发布评审器。只输出结构化风险代码和证据 ID；不得编造数字、SQL 或业务事实，不得改变门禁；门禁失败时不得建议 APPROVE。"}}},
		{Role: ai.MessageRoleUser, Parts: []ai.ContentPart{{Type: ai.ContentTypeText, Text: string(payload)}}},
	}, known, nil
}

func validateReleaseReviewEnvelope(
	envelope ReleaseReviewEnvelope,
	gatePassed bool,
	known map[askdata.ID]askdata.ContentHash,
) error {
	if envelope.SchemaVersion != ReleaseReviewSchemaVersion ||
		(envelope.Recommendation != "APPROVE" && envelope.Recommendation != "CONDITIONAL" && envelope.Recommendation != "REJECT") ||
		(!gatePassed && envelope.Recommendation == "APPROVE") || len(envelope.ImpactCodes) > 64 ||
		len(envelope.FailureClusterCodes) > 64 || len(envelope.Risks) > 64 ||
		len(envelope.EvidenceIDs) < 1 || len(envelope.EvidenceIDs) > MaxReleaseReviewEvidence {
		return ErrReleaseReviewInvalid
	}
	for _, codes := range [][]string{envelope.ImpactCodes, envelope.FailureClusterCodes} {
		seen := map[string]bool{}
		for _, code := range codes {
			if !stableUpperCode(code) || seen[code] {
				return ErrReleaseReviewInvalid
			}
			seen[code] = true
		}
	}
	cited := map[askdata.ID]bool{}
	for _, evidenceID := range envelope.EvidenceIDs {
		if _, exists := known[evidenceID]; !exists || cited[evidenceID] {
			return ErrReleaseReviewInvalid
		}
		cited[evidenceID] = true
	}
	for _, risk := range envelope.Risks {
		if !stableUpperCode(risk.Code) ||
			(risk.Severity != "LOW" && risk.Severity != "MEDIUM" && risk.Severity != "HIGH" && risk.Severity != "CRITICAL") ||
			len(risk.EvidenceIDs) < 1 || len(risk.EvidenceIDs) > 32 {
			return ErrReleaseReviewInvalid
		}
		seen := map[askdata.ID]bool{}
		for _, evidenceID := range risk.EvidenceIDs {
			if _, exists := known[evidenceID]; !exists || !cited[evidenceID] || seen[evidenceID] {
				return ErrReleaseReviewInvalid
			}
			seen[evidenceID] = true
		}
	}
	return nil
}

type ReleaseReviewRecorder interface {
	RecordReleaseReviewReport(context.Context, AdminScope, string, ReleaseReviewReportInput) (string, error)
}

type GenerateReleaseReviewRequest struct {
	Scope             AdminScope
	ReleaseID         string
	EvaluationSetID   string
	EvaluationBatchID string
	Gate              ReleaseGateResult
	PromptVersion     string
	PreferredModel    string
	Evidence          []ReleaseReviewEvidence
}

type GenerateReleaseReviewResult struct {
	Review              ReleaseReviewLLMResult `json:"review"`
	PersistedReportHash string                 `json:"persistedReportHash"`
}

type ReleaseReviewService struct {
	reviewer *ReleaseReviewer
	recorder ReleaseReviewRecorder
}

func NewReleaseReviewService(reviewer *ReleaseReviewer, recorder ReleaseReviewRecorder) (*ReleaseReviewService, error) {
	if reviewer == nil || recorder == nil {
		return nil, ErrReleaseReviewInvalid
	}
	return &ReleaseReviewService{reviewer: reviewer, recorder: recorder}, nil
}

func (service *ReleaseReviewService) GenerateAndRecord(
	ctx context.Context,
	request GenerateReleaseReviewRequest,
) (GenerateReleaseReviewResult, error) {
	if service == nil || service.reviewer == nil || service.recorder == nil ||
		request.Scope.Validate(ctx) != nil || !canonicalAdminUUID(request.ReleaseID) ||
		!canonicalAdminUUID(request.EvaluationSetID) || !canonicalAdminUUID(request.EvaluationBatchID) ||
		!validLifecycleHash(request.Gate.ReceiptHash) {
		return GenerateReleaseReviewResult{}, ErrReleaseReviewInvalid
	}
	review, err := service.reviewer.Review(ctx, ReleaseReviewLLMRequest{
		TenantID: request.Scope.TenantID, DomainID: request.Scope.DomainID,
		ActorID: request.Scope.ActorID, ReleaseID: request.ReleaseID,
		PromptVersion: request.PromptVersion, PreferredModel: request.PreferredModel,
		GatePassed: request.Gate.Passed, Evidence: request.Evidence,
	})
	if err != nil {
		review, err = deterministicReleaseReview(ReleaseReviewLLMRequest{
			TenantID: request.Scope.TenantID, DomainID: request.Scope.DomainID,
			ActorID: request.Scope.ActorID, ReleaseID: request.ReleaseID,
			PromptVersion: request.PromptVersion, PreferredModel: request.PreferredModel,
			GatePassed: request.Gate.Passed, Evidence: request.Evidence,
		}, request.Gate.Failures)
		if err != nil {
			return GenerateReleaseReviewResult{}, err
		}
	}
	evidence := make([]map[string]any, len(request.Evidence))
	for index, item := range request.Evidence {
		evidence[index] = map[string]any{"evidenceId": item.EvidenceID, "contentHash": item.ContentHash, "kind": item.Kind}
	}
	report, err := json.Marshal(map[string]any{
		"schemaVersion": ReleaseReviewSchemaVersion,
		"review":        review.Report, "reportHash": review.ReportHash,
		"providerAudit": map[string]any{
			"aiRequestId": review.AIRequestID, "providerModel": review.ProviderModel,
			"attempts": review.Attempts, "redactionCount": review.RedactionCount,
		},
		"evidence": evidence,
	})
	if err != nil {
		return GenerateReleaseReviewResult{}, err
	}
	persistedHash, err := service.recorder.RecordReleaseReviewReport(ctx, request.Scope, request.ReleaseID, ReleaseReviewReportInput{
		EvaluationSetID: request.EvaluationSetID, EvaluationBatchID: request.EvaluationBatchID,
		GateReceiptHash: request.Gate.ReceiptHash, Recommendation: review.Report.Recommendation,
		Report: report,
	})
	if err != nil {
		return GenerateReleaseReviewResult{}, err
	}
	return GenerateReleaseReviewResult{Review: review, PersistedReportHash: persistedHash}, nil
}

func deterministicReleaseReview(
	request ReleaseReviewLLMRequest,
	gateFailures []string,
) (ReleaseReviewLLMResult, error) {
	_, known, err := releaseReviewMessages(request)
	if err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	evidenceIDs := make([]askdata.ID, 0, len(known))
	for evidenceID := range known {
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	sort.Slice(evidenceIDs, func(i, j int) bool { return evidenceIDs[i] < evidenceIDs[j] })
	envelope := ReleaseReviewEnvelope{
		SchemaVersion:       ReleaseReviewSchemaVersion,
		Recommendation:      "APPROVE",
		ImpactCodes:         []string{"GATE_FACTS_VERIFIED"},
		FailureClusterCodes: []string{},
		Risks:               []ReleaseReviewRisk{},
		EvidenceIDs:         evidenceIDs,
	}
	if !request.GatePassed {
		failures := make([]string, 0, len(gateFailures))
		seen := map[string]bool{}
		for _, failure := range gateFailures {
			failure = strings.TrimSpace(failure)
			if stableUpperCode(failure) && !seen[failure] {
				seen[failure] = true
				failures = append(failures, failure)
			}
		}
		sort.Strings(failures)
		if len(failures) == 0 {
			failures = []string{"RELEASE_GATE_FAILED"}
		}
		envelope.Recommendation = "REJECT"
		envelope.ImpactCodes = []string{}
		envelope.FailureClusterCodes = failures
		envelope.Risks = []ReleaseReviewRisk{{
			Code: "RELEASE_GATE_FAILED", Severity: "CRITICAL", EvidenceIDs: evidenceIDs,
		}}
	}
	if err := validateReleaseReviewEnvelope(envelope, request.GatePassed, known); err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return ReleaseReviewLLMResult{}, err
	}
	return ReleaseReviewLLMResult{
		Report: envelope, ReportHash: askdata.HashBytes(canonical),
		ProviderModel: "deterministic-gate-review-v1",
	}, nil
}
