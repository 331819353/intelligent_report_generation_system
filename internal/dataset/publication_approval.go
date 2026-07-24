package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

var (
	ErrPublicationRequestNotFound   = errors.New("dataset publication request not found")
	ErrPublicationRequestConflict   = errors.New("dataset publication request version conflict")
	ErrPublicationRequestNotPending = errors.New("dataset publication request is not pending")
	ErrPublicationCandidatesFailed  = errors.New("dataset publication metric candidate generation failed")
	ErrPublicationCandidatesPending = errors.New("dataset publication metric candidates are not ready")
)

const (
	PublicationRequestPending  = "PENDING"
	PublicationRequestApproved = "APPROVED"
	PublicationRequestRejected = "REJECTED"
)

// PublicationRequest freezes one saved draft revision for human review. Validation parameters
// are stored server-side but deliberately omitted from the response because they may contain
// business filter values; reviewers see the exact hashes and draft identity instead.
type PublicationRequest struct {
	ID                         string          `json:"id"`
	DatasetID                  string          `json:"datasetId"`
	Status                     string          `json:"status"`
	Version                    int64           `json:"version"`
	DraftVersionID             string          `json:"draftVersionId"`
	ExpectedDatasetVersion     int64           `json:"expectedDatasetVersion"`
	ExpectedDraftRecordVersion int64           `json:"expectedDraftRecordVersion"`
	ExpectedDSLHash            string          `json:"expectedDslHash"`
	ExpectedPlanHash           string          `json:"expectedPlanHash"`
	RequesterID                string          `json:"requesterId"`
	RequestNote                string          `json:"requestNote"`
	ReviewerID                 string          `json:"reviewerId,omitempty"`
	ReviewNote                 string          `json:"reviewNote,omitempty"`
	PublishedVersionID         string          `json:"publishedVersionId,omitempty"`
	SubmittedAt                string          `json:"submittedAt"`
	ReviewedAt                 string          `json:"reviewedAt,omitempty"`
	UpdatedAt                  string          `json:"updatedAt"`
	ValidationParameters       map[string]any  `json:"-"`
	ReservedPublishedVersionID string          `json:"-"`
	MetricCandidateResult      json.RawMessage `json:"-"`
	MetricCandidateStatus      string          `json:"metricCandidateStatus"`
	MetricCandidateTotal       int             `json:"metricCandidateTotal"`
	MetricCandidateReady       int             `json:"metricCandidateReady"`
	MetricCandidateReview      int             `json:"metricCandidateReview"`
	MetricCandidateBlocked     int             `json:"metricCandidateBlocked"`
	MetricCandidateWarning     string          `json:"metricCandidateWarning,omitempty"`
	MetricCandidateErrorCode   string          `json:"metricCandidateErrorCode,omitempty"`
	MetricCandidateGeneratedAt string          `json:"metricCandidateGeneratedAt,omitempty"`
}

type PublicationRequestPage struct {
	Items  []PublicationRequest `json:"items"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

// SubmitPublicationInput has the same frozen snapshot fields as the former direct publish
// request, plus an optional human-readable note for the approver.
type SubmitPublicationInput struct {
	DraftVersionID             string         `json:"draftVersionId"`
	ExpectedVersion            int64          `json:"expectedVersion"`
	ExpectedDraftRecordVersion int64          `json:"expectedDraftRecordVersion"`
	ExpectedDSLHash            string         `json:"expectedDslHash"`
	ValidationParameters       map[string]any `json:"validationParameters"`
	Note                       string         `json:"note"`
}

type ApprovePublicationInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Note            string `json:"note"`
}

type RejectPublicationInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

type PublicationApprovalResult struct {
	Request          PublicationRequest `json:"request"`
	PublishedVersion VersionRecord      `json:"publishedVersion"`
}

type SubmitPublicationPlan struct {
	Input            SubmitPublicationInput
	ExpectedPlanHash string
	ParametersJSON   json.RawMessage
}

const (
	PublicationCandidateLegacy    = "LEGACY"
	PublicationCandidatePending   = "PENDING"
	PublicationCandidateSucceeded = "SUCCEEDED"
	PublicationCandidatePartial   = "PARTIAL"
	PublicationCandidateFailed    = "FAILED"
)

type PublicationCandidatePreparation struct {
	Status    string
	Result    json.RawMessage
	Total     int
	Ready     int
	Review    int
	Blocked   int
	Warning   string
	ErrorCode string
}

// PublicationApprovalStore keeps request state and the final publication commit in one
// transaction. ApproveAndPublish is the only approval path allowed to move the published pointer.
type PublicationApprovalStore interface {
	SubmitPublicationRequest(context.Context, string, string, string, SubmitPublicationPlan) (PublicationRequest, error)
	ListPublicationRequests(context.Context, string, string, int, int) ([]PublicationRequest, int, error)
	GetPublicationRequest(context.Context, string, string, string) (PublicationRequest, error)
	SavePublicationCandidatePreparation(context.Context, string, string, PublicationRequest, PublicationCandidatePreparation) (PublicationRequest, error)
	ApproveAndPublish(context.Context, string, string, string, string, int64, string, PublishPlan) (PublicationRequest, VersionRecord, error)
	RejectPublicationRequest(context.Context, string, string, string, string, RejectPublicationInput) (PublicationRequest, error)
}

// PublicationApprovalService separates drafting from approval while reusing the authoritative
// publication validator. MANAGE submits; PUBLISH approves or rejects at the HTTP boundary.
type PublicationApprovalService struct {
	store    PublicationApprovalStore
	datasets *Service
}

func NewPublicationApprovalService(store PublicationApprovalStore, datasets *Service) *PublicationApprovalService {
	return &PublicationApprovalService{store: store, datasets: datasets}
}

func (s *PublicationApprovalService) Submit(
	ctx context.Context,
	tenantID, actorID, datasetID string,
	input SubmitPublicationInput,
) (PublicationRequest, error) {
	input.Note = strings.TrimSpace(input.Note)
	if s == nil || s.store == nil || s.datasets == nil || tenantID == "" || actorID == "" ||
		!canonicalUUID(datasetID) || !canonicalUUID(input.DraftVersionID) || input.ExpectedVersion < 1 ||
		input.ExpectedDraftRecordVersion < 1 || !validHash(input.ExpectedDSLHash) || !validApprovalText(input.Note, false) {
		return PublicationRequest{}, ErrInvalidDocument
	}
	if input.ValidationParameters == nil {
		input.ValidationParameters = map[string]any{}
	}
	parametersJSON, err := json.Marshal(input.ValidationParameters)
	if err != nil || len(parametersJSON) > 1<<20 {
		return PublicationRequest{}, ErrInvalidDocument
	}
	current, err := s.datasets.Get(ctx, tenantID, datasetID)
	if err != nil {
		return PublicationRequest{}, err
	}
	if current.Status == "DISABLED" {
		return PublicationRequest{}, ErrInvalidTransition
	}
	if current.Version != input.ExpectedVersion || current.DraftVersionID != input.DraftVersionID ||
		current.DraftRecordVersion != input.ExpectedDraftRecordVersion || current.DSLHash != input.ExpectedDSLHash {
		return PublicationRequest{}, ErrConflict
	}
	return s.store.SubmitPublicationRequest(ctx, tenantID, actorID, datasetID, SubmitPublicationPlan{
		Input: input, ExpectedPlanHash: current.PlanHash, ParametersJSON: parametersJSON,
	})
}

func (s *PublicationApprovalService) List(
	ctx context.Context,
	tenantID, datasetID string,
	limit, offset int,
) ([]PublicationRequest, int, error) {
	if s == nil || s.store == nil || tenantID == "" || !canonicalUUID(datasetID) || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidDocument
	}
	return s.store.ListPublicationRequests(ctx, tenantID, datasetID, limit, offset)
}

func (s *PublicationApprovalService) Approve(
	ctx context.Context,
	tenantID, actorID, datasetID, requestID string,
	input ApprovePublicationInput,
) (PublicationApprovalResult, error) {
	input.Note = strings.TrimSpace(input.Note)
	if s == nil || s.store == nil || s.datasets == nil || tenantID == "" || actorID == "" ||
		!canonicalUUID(datasetID) || !canonicalUUID(requestID) || input.ExpectedVersion < 1 || !validApprovalText(input.Note, false) {
		return PublicationApprovalResult{}, ErrInvalidDocument
	}
	request, err := s.store.GetPublicationRequest(ctx, tenantID, datasetID, requestID)
	if err != nil {
		return PublicationApprovalResult{}, err
	}
	if request.Status == PublicationRequestApproved {
		version, versionErr := s.datasets.GetVersion(ctx, tenantID, datasetID, request.PublishedVersionID)
		return PublicationApprovalResult{Request: request, PublishedVersion: version}, versionErr
	}
	if request.Status != PublicationRequestPending {
		return PublicationApprovalResult{}, ErrPublicationRequestNotPending
	}
	if request.MetricCandidateStatus != PublicationCandidateSucceeded &&
		request.MetricCandidateStatus != PublicationCandidatePartial &&
		request.MetricCandidateStatus != PublicationCandidateLegacy {
		return PublicationApprovalResult{}, ErrPublicationCandidatesPending
	}
	if request.Version != input.ExpectedVersion {
		return PublicationApprovalResult{}, ErrPublicationRequestConflict
	}
	publishInput := PublishInput{
		DraftVersionID: request.DraftVersionID, ExpectedVersion: request.ExpectedDatasetVersion,
		ExpectedDraftRecordVersion: request.ExpectedDraftRecordVersion, ExpectedDSLHash: request.ExpectedDSLHash,
		ValidationParameters: request.ValidationParameters,
	}
	plan, err := s.datasets.preparePublication(ctx, tenantID, actorID, datasetID, request.ID, publishInput)
	if err != nil {
		return PublicationApprovalResult{}, err
	}
	plan.ReservedPublishedVersionID = request.ReservedPublishedVersionID
	approved, published, err := s.store.ApproveAndPublish(
		ctx, tenantID, actorID, datasetID, requestID, input.ExpectedVersion, input.Note, plan,
	)
	if err != nil {
		return PublicationApprovalResult{}, err
	}
	return PublicationApprovalResult{Request: approved, PublishedVersion: published}, nil
}

func (s *PublicationApprovalService) Reject(
	ctx context.Context,
	tenantID, actorID, datasetID, requestID string,
	input RejectPublicationInput,
) (PublicationRequest, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if s == nil || s.store == nil || tenantID == "" || actorID == "" || !canonicalUUID(datasetID) ||
		!canonicalUUID(requestID) || input.ExpectedVersion < 1 || !validApprovalText(input.Reason, true) {
		return PublicationRequest{}, ErrInvalidDocument
	}
	return s.store.RejectPublicationRequest(ctx, tenantID, actorID, datasetID, requestID, input)
}

func validApprovalText(value string, required bool) bool {
	length := len([]rune(value))
	if length > 1000 || required && length == 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return false
		}
	}
	return true
}
