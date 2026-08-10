package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

type CreateFromFeedbackInput struct {
	Identity        Identity
	QueryFeedbackID askdata.ID
	Snapshot        orchestrator.ReplaySnapshot
	IssueType       IssueType
	Severity        Severity
	Now             time.Time
}

func CreateTicketTx(ctx context.Context, tx pgx.Tx, input CreateFromFeedbackInput) (Ticket, error) {
	if tx == nil || input.Identity.Validate() != nil || input.QueryFeedbackID.Validate() != nil ||
		input.Snapshot.Run.ID.Validate() != nil || input.Snapshot.Run.ActorID != input.Identity.ActorID ||
		input.Snapshot.Run.DomainID != input.Identity.DomainID {
		return Ticket{}, ErrInvalid
	}
	if input.Severity == "" {
		input.Severity = SeverityP1
	}
	due, err := SLADueAt(input.Now, input.Severity, nil)
	if err != nil {
		return Ticket{}, err
	}
	id := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("askdata-feedback-ticket-v1\x00"+string(input.Identity.TenantID)+"\x00"+string(input.QueryFeedbackID))).String())
	suggested := SuggestStage(input.IssueType, input.Snapshot)
	row := tx.QueryRow(ctx, `INSERT INTO askdata.feedback_tickets(
		id,tenant_id,domain_id,query_feedback_id,question_run_id,reporter_user_id,
		issue_type,severity,suggested_stage,sla_due_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	ON CONFLICT(tenant_id,query_feedback_id) DO UPDATE SET
		severity=askdata.feedback_tickets.severity
	RETURNING `+ticketColumns, id, input.Identity.TenantID, input.Identity.DomainID,
		input.QueryFeedbackID, input.Snapshot.Run.ID, input.Identity.ActorID,
		input.IssueType, input.Severity, suggested, due)
	ticket, err := scanTicket(row)
	if err != nil {
		return Ticket{}, err
	}
	var eventCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM askdata.feedback_ticket_events
		WHERE tenant_id=$1 AND ticket_id=$2`, input.Identity.TenantID, ticket.ID).Scan(&eventCount); err != nil {
		return Ticket{}, err
	}
	if eventCount == 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.feedback_ticket_events(
			tenant_id,ticket_id,event_no,from_status,to_status,actor_user_id,details_json
		) VALUES($1,$2,1,NULL,'NEW',$3,$4)`, input.Identity.TenantID, ticket.ID,
			input.Identity.ActorID, []byte(`{"source":"QUERY_FEEDBACK"}`)); err != nil {
			return Ticket{}, err
		}
	}
	return ticket, nil
}

func (repository *PostgresRepository) List(ctx context.Context, identity Identity) ([]Ticket, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil {
		return nil, ErrInvalid
	}
	result := []Ticket{}
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+ticketColumns+` FROM askdata.feedback_tickets
			WHERE tenant_id=$1 AND domain_id=$2 ORDER BY
			CASE severity WHEN 'P0' THEN 0 WHEN 'P1' THEN 1 ELSE 2 END,sla_due_at,id`, identity.TenantID, identity.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanTicket(rows)
			if scanErr != nil {
				return scanErr
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (repository *PostgresRepository) Get(ctx context.Context, identity Identity, id askdata.ID) (Ticket, []Event, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return Ticket{}, nil, ErrInvalid
	}
	var ticket Ticket
	events := []Event{}
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var err error
		ticket, err = scanTicket(tx.QueryRow(ctx, `SELECT `+ticketColumns+` FROM askdata.feedback_tickets
			WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`, identity.TenantID, identity.DomainID, id))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,ticket_id::text,event_no,COALESCE(from_status,''),to_status,
			actor_user_id::text,details_json,created_at FROM askdata.feedback_ticket_events
			WHERE tenant_id=$1 AND ticket_id=$2 ORDER BY event_no`, identity.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Event
			var raw []byte
			if err := rows.Scan(&item.ID, &item.TicketID, &item.EventNo, &item.FromStatus, &item.ToStatus, &item.ActorUserID, &raw, &item.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &item.Details); err != nil {
				return err
			}
			events = append(events, item)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, nil, ErrNotFound
	}
	return ticket, events, err
}

func (repository *PostgresRepository) Transition(ctx context.Context, identity Identity, id askdata.ID, input TransitionInput) (Ticket, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return Ticket{}, ErrInvalid
	}
	var result Ticket
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := scanTicket(tx.QueryRow(ctx, `SELECT `+ticketColumns+` FROM askdata.feedback_tickets
			WHERE tenant_id=$1 AND domain_id=$2 AND id=$3
			  AND (owner_user_id=$4 OR platform.user_is_domain_administrator(domain_id)
			    OR platform.user_is_platform_administrator() OR platform.is_system_access())
			FOR UPDATE`, identity.TenantID, identity.DomainID, id, identity.ActorID))
		if err != nil {
			return err
		}
		if err := input.Validate(current); err != nil {
			return err
		}
		severity := input.Severity
		if severity == "" {
			severity = current.Severity
		}
		stage := input.AttributedStage
		if stage == "" {
			stage = current.AttributedStage
		}
		owner := firstID(input.OwnerUserID, current.OwnerUserID)
		resolution := firstText(input.ResolutionNote, current.ResolutionNote)
		response := firstText(input.UserResponse, current.UserResponse)
		fixType := firstText(input.FixCandidateType, current.FixCandidateType)
		fixID := firstID(input.FixCandidateID, current.FixCandidateID)
		releaseID := firstID(input.LinkedReleaseID, current.LinkedReleaseID)
		caseID := firstID(input.LinkedEvaluationCaseID, current.LinkedEvaluationCaseID)
		if input.TargetStatus == StatusVerified && releaseID == "" {
			return fmt.Errorf("%w: release is required", ErrInvalid)
		}
		if input.TargetStatus == StatusClosed && (releaseID == "" || caseID == "") {
			return fmt.Errorf("%w: verified release and case are required", ErrInvalid)
		}
		row := tx.QueryRow(ctx, `UPDATE askdata.feedback_tickets SET status=$1,severity=$2,attributed_stage=NULLIF($3,''),
			owner_user_id=NULLIF($4,'')::uuid,resolution_note=$5,user_response=$6,
			fix_candidate_type=NULLIF($7,''),fix_candidate_id=NULLIF($8,'')::uuid,
			fix_candidate_state=CASE WHEN NULLIF($8,'') IS NULL THEN NULL ELSE 'DRAFT' END,
			linked_release_id=NULLIF($9,'')::uuid,linked_evaluation_case_id=NULLIF($10,'')::uuid,
			record_version=record_version+1 WHERE id=$11 AND record_version=$12 RETURNING `+ticketColumns,
			input.TargetStatus, severity, stage, owner, resolution, response, fixType, fixID, releaseID, caseID, id, input.ExpectedVersion)
		result, err = scanTicket(row)
		if err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"suggestedStage": current.SuggestedStage, "attributedStage": result.AttributedStage, "severity": result.Severity})
		_, err = tx.Exec(ctx, `INSERT INTO askdata.feedback_ticket_events(tenant_id,ticket_id,event_no,from_status,to_status,actor_user_id,details_json)
			SELECT $1,$2,COALESCE(max(event_no),0)+1,$3,$4,$5,$6 FROM askdata.feedback_ticket_events WHERE tenant_id=$1 AND ticket_id=$2`,
			identity.TenantID, id, current.Status, result.Status, identity.ActorID, details)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrConflict
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && (pgError.Code == "40001" || pgError.Code == "23514") {
		return Ticket{}, errors.Join(ErrConflict, err)
	}
	return result, err
}

type Metrics struct {
	Total, Rejected, Closed, Overdue int64
	ClosureRate                      float64
}

func (repository *PostgresRepository) Metrics(ctx context.Context, identity Identity, now time.Time) (Metrics, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil {
		return Metrics{}, ErrInvalid
	}
	var result Metrics
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='REJECTED'),count(*) FILTER(WHERE status='CLOSED'),
		count(*) FILTER(WHERE status NOT IN('REJECTED','CLOSED') AND sla_due_at<$3),
		COALESCE(count(*) FILTER(WHERE status='CLOSED')::double precision/NULLIF(count(*) FILTER(WHERE status<>'REJECTED'),0),0)
		FROM askdata.feedback_tickets WHERE tenant_id=$1 AND domain_id=$2 AND created_at>=$3-interval '30 days'`, identity.TenantID, identity.DomainID, now).Scan(&result.Total, &result.Rejected, &result.Closed, &result.Overdue, &result.ClosureRate)
	})
	return result, err
}

const ticketColumns = `id::text,tenant_id::text,domain_id::text,query_feedback_id::text,question_run_id::text,
	reporter_user_id::text,issue_type,severity,suggested_stage,COALESCE(attributed_stage,''),status,
	COALESCE(owner_user_id::text,''),sla_due_at,COALESCE(linked_release_id::text,''),
	COALESCE(linked_evaluation_case_id::text,''),resolution_note,user_response,COALESCE(fix_candidate_type,''),
	COALESCE(fix_candidate_id::text,''),record_version,created_at,updated_at,closed_at`

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner) (Ticket, error) {
	var value Ticket
	err := row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.QueryFeedbackID, &value.QuestionRunID, &value.ReporterUserID, &value.IssueType, &value.Severity, &value.SuggestedStage, &value.AttributedStage, &value.Status, &value.OwnerUserID, &value.SLADueAt, &value.LinkedReleaseID, &value.LinkedEvaluationCaseID, &value.ResolutionNote, &value.UserResponse, &value.FixCandidateType, &value.FixCandidateID, &value.RecordVersion, &value.CreatedAt, &value.UpdatedAt, &value.ClosedAt)
	return value, err
}
func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
