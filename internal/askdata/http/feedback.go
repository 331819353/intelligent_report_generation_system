package askdatahttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/askdata"
	feedbackticket "intelligent-report-generation-system/internal/askdata/feedback"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	maxFeedbackCommentRunes = 2000
	feedbackIDDomain        = "askdata-query-feedback-v1\x00"
)

type FeedbackRating string

const (
	FeedbackAccurate   FeedbackRating = "ACCURATE"
	FeedbackInaccurate FeedbackRating = "INACCURATE"
)

type FeedbackIssueType string

const (
	FeedbackIssueNone         FeedbackIssueType = "NONE"
	FeedbackIssueMetric       FeedbackIssueType = "METRIC"
	FeedbackIssueDimension    FeedbackIssueType = "DIMENSION"
	FeedbackIssueMember       FeedbackIssueType = "MEMBER"
	FeedbackIssueTime         FeedbackIssueType = "TIME"
	FeedbackIssueRelationship FeedbackIssueType = "RELATIONSHIP"
	FeedbackIssueData         FeedbackIssueType = "DATA"
	FeedbackIssuePermission   FeedbackIssueType = "PERMISSION"
	FeedbackIssueExpression   FeedbackIssueType = "EXPRESSION"
	FeedbackIssueOther        FeedbackIssueType = "OTHER"
)

var validFeedbackIssues = map[FeedbackIssueType]bool{
	FeedbackIssueNone: true, FeedbackIssueMetric: true, FeedbackIssueDimension: true,
	FeedbackIssueMember: true, FeedbackIssueTime: true, FeedbackIssueRelationship: true,
	FeedbackIssueData: true, FeedbackIssuePermission: true, FeedbackIssueExpression: true,
	FeedbackIssueOther: true,
}

type SubmitFeedbackInput struct {
	RunID      askdata.ID
	RunVersion int64
	Rating     FeedbackRating
	IssueType  FeedbackIssueType
	Comment    string
}

type FeedbackResult struct {
	FeedbackID    askdata.ID
	RunID         askdata.ID
	Rating        FeedbackRating
	IssueType     FeedbackIssueType
	RecordVersion int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Replayed      bool
}

type FeedbackView struct {
	FeedbackID    askdata.ID        `json:"feedbackId"`
	RunID         askdata.ID        `json:"runId"`
	Rating        FeedbackRating    `json:"rating"`
	IssueType     FeedbackIssueType `json:"issueType"`
	RecordVersion int64             `json:"recordVersion"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Replayed      bool              `json:"replayed"`
}

func (handler *Handler) submitFeedback(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "QUESTION_INVALID_REQUEST", "feedback endpoint does not accept query parameters")
		return
	}
	runID, err := parseRunID(request.PathValue("runId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	var body struct {
		Rating     FeedbackRating    `json:"rating"`
		IssueType  FeedbackIssueType `json:"issueType"`
		Comment    string            `json:"comment"`
		RunVersion int64             `json:"runVersion"`
	}
	if err := decodeStrictJSON(writer, request, &body); err != nil {
		writeServiceError(writer, err)
		return
	}
	comment, err := normalizeFeedbackComment(body.Comment)
	if err != nil || !validFeedbackShape(body.Rating, body.IssueType) || body.RunVersion < 1 {
		writeServiceError(writer, ErrInvalidRequest)
		return
	}
	result, err := handler.backend.SubmitFeedback(request.Context(), identity, SubmitFeedbackInput{
		RunID: runID, RunVersion: body.RunVersion, Rating: body.Rating,
		IssueType: body.IssueType, Comment: comment,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, FeedbackView{
		FeedbackID: result.FeedbackID, RunID: result.RunID, Rating: result.Rating,
		IssueType: result.IssueType, RecordVersion: result.RecordVersion,
		CreatedAt: result.CreatedAt, UpdatedAt: result.UpdatedAt, Replayed: result.Replayed,
	})
}

func validFeedbackShape(rating FeedbackRating, issue FeedbackIssueType) bool {
	if !validFeedbackIssues[issue] {
		return false
	}
	return rating == FeedbackAccurate && issue == FeedbackIssueNone ||
		rating == FeedbackInaccurate && issue != FeedbackIssueNone
}

func normalizeFeedbackComment(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidRequest
	}
	value := strings.TrimSpace(raw)
	if utf8.RuneCountInString(value) > maxFeedbackCommentRunes {
		return "", ErrInvalidRequest
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalidRequest
		}
	}
	return value, nil
}

func (service *PostgresService) SubmitFeedback(
	ctx context.Context,
	identity RequestIdentity,
	input SubmitFeedbackInput,
) (FeedbackResult, error) {
	comment, err := normalizeFeedbackComment(input.Comment)
	if service == nil || service.pool == nil || service.runs == nil || identity.validate() != nil ||
		!canonicalUUID(input.RunID) || input.RunVersion < 1 || !validFeedbackShape(input.Rating, input.IssueType) || err != nil {
		return FeedbackResult{}, ErrInvalidRequest
	}
	scope, err := service.resolveRunScope(ctx, identity, input.RunID)
	if err != nil {
		return FeedbackResult{}, err
	}
	snapshot, err := service.runs.Resume(ctx, orchestrator.ResumeRequest{
		Scope: scope, DomainID: identity.DomainID, RunID: input.RunID,
	})
	if err != nil {
		return FeedbackResult{}, err
	}
	if !snapshot.Run.Terminal() {
		return FeedbackResult{}, ErrFeedbackNotAccepted
	}
	if snapshot.Run.RecordVersion != input.RunVersion {
		return FeedbackResult{}, orchestrator.ErrVersionConflict
	}
	input.Comment = comment
	return service.persistFeedback(ctx, identity, snapshot, input)
}

func (service *PostgresService) persistFeedback(
	ctx context.Context,
	identity RequestIdentity,
	snapshot orchestrator.ReplaySnapshot,
	input SubmitFeedbackInput,
) (FeedbackResult, error) {
	run := snapshot.Run
	var result FeedbackResult
	runner := service.scopeRunner
	if runner == nil {
		runner = func(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
			return database.WithTenantTx(ctx, service.pool, tenantID, fn)
		}
	}
	err := runner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		lockKey := string(identity.TenantID) + ":" + string(identity.ActorID) + ":" + string(run.ID)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
			return err
		}
		ensureTicket := func() error {
			if input.Rating != FeedbackInaccurate {
				return nil
			}
			_, err := feedbackticket.CreateTicketTx(ctx, tx, feedbackticket.CreateFromFeedbackInput{
				Identity: feedbackticket.Identity{
					TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
				},
				QueryFeedbackID: result.FeedbackID,
				Snapshot:        snapshot,
				IssueType:       feedbackticket.FromLegacyIssue(string(input.IssueType)),
				Severity:        feedbackticket.SeverityP1,
				Now:             result.CreatedAt,
			})
			return err
		}
		var existingComment string
		err := tx.QueryRow(ctx, `SELECT id::text,rating,issue_type,comment,record_version,created_at,updated_at
			FROM askdata.query_feedback
			WHERE tenant_id=$1 AND question_run_id=$2 AND actor_id=$3
			FOR UPDATE`, identity.TenantID, run.ID, identity.ActorID,
		).Scan(&result.FeedbackID, &result.Rating, &result.IssueType, &existingComment,
			&result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
		if err == nil {
			result.RunID = run.ID
			if result.Rating == input.Rating && result.IssueType == input.IssueType && existingComment == input.Comment {
				result.Replayed = true
				return ensureTicket()
			}
			err = tx.QueryRow(ctx, `UPDATE askdata.query_feedback
				SET rating=$4,issue_type=$5,comment=$6,record_version=record_version+1
				WHERE tenant_id=$1 AND question_run_id=$2 AND actor_id=$3
				RETURNING id::text,rating,issue_type,record_version,created_at,updated_at`,
				identity.TenantID, run.ID, identity.ActorID, input.Rating, input.IssueType, input.Comment,
			).Scan(&result.FeedbackID, &result.Rating, &result.IssueType,
				&result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
			if err != nil {
				return err
			}
			return ensureTicket()
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		feedbackID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(
			feedbackIDDomain+string(identity.TenantID)+"\x00"+string(identity.ActorID)+"\x00"+string(run.ID),
		)).String())
		err = tx.QueryRow(ctx, `INSERT INTO askdata.query_feedback(
			id,tenant_id,domain_id,actor_id,question_run_id,release_id,
			release_content_hash,policy_scope_hash,rating,issue_type,comment,feedback_hash
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id::text,rating,issue_type,record_version,created_at,updated_at`,
			feedbackID, identity.TenantID, identity.DomainID, identity.ActorID, run.ID,
			run.Release.ReleaseID, run.Release.ContentHash, run.PolicyScopeHash,
			input.Rating, input.IssueType, input.Comment, strings.Repeat("0", 64),
		).Scan(&result.FeedbackID, &result.Rating, &result.IssueType,
			&result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
		if err != nil {
			return err
		}
		return ensureTicket()
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && (pgError.Code == "23505" || pgError.Code == "40001") {
			return FeedbackResult{}, ErrFeedbackConflict
		}
		return FeedbackResult{}, fmt.Errorf("%w: persist feedback", ErrQuestionServiceFailure)
	}
	result.RunID = run.ID
	return result, nil
}
