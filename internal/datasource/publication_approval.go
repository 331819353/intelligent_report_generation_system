package datasource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrReviewRequestNotFound   = errors.New("data source publication review request not found")
	ErrReviewRequestConflict   = errors.New("data source publication review request version conflict")
	ErrReviewRequestNotPending = errors.New("data source publication review request is not pending")
	ErrReviewWithdrawForbidden = errors.New("only the requester can withdraw the review request")
	ErrReviewSelfApproval      = errors.New("requester cannot review their own publication request")
)

// PublicationRequest freezes one exact, successfully tested data-source draft for human review.
type PublicationRequest struct {
	ID                 string       `json:"id"`
	DataSourceID       string       `json:"dataSourceId"`
	ConfigVersionID    string       `json:"configVersionId"`
	ConfigHash         string       `json:"configHash"`
	Status             ReviewStatus `json:"status"`
	Version            int64        `json:"version"`
	RequesterUserID    string       `json:"requesterUserId"`
	RequestNote        string       `json:"requestNote"`
	ReviewerUserID     string       `json:"reviewerUserId,omitempty"`
	ReviewNote         string       `json:"reviewNote,omitempty"`
	SubmittedAt        time.Time    `json:"submittedAt"`
	ReviewedAt         *time.Time   `json:"reviewedAt,omitempty"`
	UpdatedAt          time.Time    `json:"updatedAt"`
	PublishedVersionID string       `json:"publishedVersionId,omitempty"`
}

type SubmitPublicationInput struct {
	Note string `json:"note"`
}

type ReviewPublicationInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

type PublicationRequestPage struct {
	Items []PublicationRequest `json:"items"`
	Total int                  `json:"total"`
}

type PublicationApprovalStore interface {
	SubmitPublicationRequest(context.Context, string, string, Source, string) (PublicationRequest, error)
	ListPublicationRequests(context.Context, string, string) ([]PublicationRequest, error)
	LatestPublicationRequest(context.Context, string, string) (*PublicationRequest, error)
	GetPublicationRequest(context.Context, string, string, string) (PublicationRequest, error)
	WithdrawPublicationRequest(context.Context, string, string, string, string, ReviewPublicationInput) (PublicationRequest, error)
	ApproveAndPublish(context.Context, string, string, string, string, ReviewPublicationInput) (PublicationRequest, Source, error)
	RejectPublicationRequest(context.Context, string, string, string, string, ReviewPublicationInput) (PublicationRequest, error)
}

type PublicationApprovalService struct {
	store   PublicationApprovalStore
	sources *Service
}

func NewPublicationApprovalService(store PublicationApprovalStore, sources *Service) *PublicationApprovalService {
	return &PublicationApprovalService{store: store, sources: sources}
}

func (s *PublicationApprovalService) Submit(
	ctx context.Context,
	tenantID, actorID, sourceID string,
	input SubmitPublicationInput,
) (PublicationRequest, error) {
	if len([]rune(input.Note)) > 1000 {
		return PublicationRequest{}, ErrInvalidConfiguration
	}
	versioned, ok := s.sources.repo.(VersionedRepository)
	if !ok {
		return PublicationRequest{}, ErrVersioningRequired
	}
	draft, err := versioned.GetDraft(ctx, tenantID, sourceID)
	if err != nil {
		return PublicationRequest{}, err
	}
	if draft.ConfigVersionID == "" || draft.ConfigHash == "" {
		return PublicationRequest{}, ErrSourceVersionChanged
	}
	if draft.ValidationStatus != ValidationPassed {
		return PublicationRequest{}, ErrTestRequired
	}
	if draft.TestExpiresAt == nil || !draft.TestExpiresAt.After(s.sources.now().UTC()) {
		return PublicationRequest{}, ErrTestExpired
	}
	if s.sources.connectionTests != nil {
		latest, latestErr := s.sources.connectionTests.LatestConnectionTest(
			ctx, tenantID, sourceID, draft.ConfigVersionID, draft.ConfigHash,
		)
		if latestErr != nil {
			return PublicationRequest{}, latestErr
		}
		if latest != nil {
			switch latest.Status {
			case ConnectionTestQueued, ConnectionTestRunning:
				return PublicationRequest{}, ErrConnectionTestPending
			case ConnectionTestFailed:
				return PublicationRequest{}, ErrConnectionTestFailed
			case ConnectionTestCancelled:
				return PublicationRequest{}, ErrSourceVersionChanged
			}
		}
	}
	return s.store.SubmitPublicationRequest(ctx, tenantID, actorID, draft, strings.TrimSpace(input.Note))
}

func (s *PublicationApprovalService) List(
	ctx context.Context,
	tenantID, sourceID string,
) ([]PublicationRequest, error) {
	return s.store.ListPublicationRequests(ctx, tenantID, sourceID)
}

func (s *PublicationApprovalService) Withdraw(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (PublicationRequest, error) {
	if input.ExpectedVersion < 1 {
		return PublicationRequest{}, ErrReviewRequestConflict
	}
	return s.store.WithdrawPublicationRequest(ctx, tenantID, actorID, sourceID, requestID, input)
}

func (s *PublicationApprovalService) Approve(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (PublicationRequest, Source, error) {
	if input.ExpectedVersion < 1 {
		return PublicationRequest{}, Source{}, ErrReviewRequestConflict
	}
	request, err := s.store.GetPublicationRequest(ctx, tenantID, sourceID, requestID)
	if err != nil {
		return PublicationRequest{}, Source{}, err
	}
	if request.Status != ReviewPending {
		return PublicationRequest{}, Source{}, ErrReviewRequestNotPending
	}
	if request.Version != input.ExpectedVersion {
		return PublicationRequest{}, Source{}, ErrReviewRequestConflict
	}
	if request.RequesterUserID == actorID {
		return PublicationRequest{}, Source{}, ErrReviewSelfApproval
	}
	// Close the old runtime pool before the atomic pointer switch. If the transaction loses
	// a race, the next runtime request safely recreates the old pool from its immutable snapshot.
	current, err := s.sources.repo.Get(ctx, tenantID, sourceID)
	if err != nil {
		return PublicationRequest{}, Source{}, err
	}
	if current.PublishedVersionID != "" && current.PublishedVersionID != request.ConfigVersionID {
		if connector := s.sources.connectors[current.Type]; connector != nil {
			if err := connector.Close(ctx, current); err != nil {
				return PublicationRequest{}, Source{}, fmt.Errorf("close published source connection: %w", err)
			}
		}
	}
	return s.store.ApproveAndPublish(ctx, tenantID, actorID, sourceID, requestID, input)
}

func (s *PublicationApprovalService) Reject(
	ctx context.Context,
	tenantID, actorID, sourceID, requestID string,
	input ReviewPublicationInput,
) (PublicationRequest, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if input.ExpectedVersion < 1 {
		return PublicationRequest{}, ErrReviewRequestConflict
	}
	if input.Reason == "" || len([]rune(input.Reason)) > 1000 {
		return PublicationRequest{}, ErrInvalidConfiguration
	}
	request, err := s.store.GetPublicationRequest(ctx, tenantID, sourceID, requestID)
	if err != nil {
		return PublicationRequest{}, err
	}
	if request.RequesterUserID == actorID {
		return PublicationRequest{}, ErrReviewSelfApproval
	}
	return s.store.RejectPublicationRequest(ctx, tenantID, actorID, sourceID, requestID, input)
}

func applyPublicationReview(source Source, request *PublicationRequest) Source {
	source.ReviewStatus = ReviewNotSubmitted
	if request == nil || request.ConfigVersionID != source.ConfigVersionID || request.ConfigHash != source.ConfigHash {
		return source
	}
	source.ReviewStatus = request.Status
	source.ReviewRequestID = request.ID
	source.ReviewRequestVersion = request.Version
	source.ReviewNote = request.ReviewNote
	source.ReviewRequesterID = request.RequesterUserID
	source.ReviewReviewerID = request.ReviewerUserID
	source.ReviewSubmittedAt = &request.SubmittedAt
	source.ReviewReviewedAt = request.ReviewedAt
	return source
}
