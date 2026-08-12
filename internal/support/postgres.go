package support

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const ticketColumns = `ticket.id::text,ticket.category,ticket.priority,ticket.subject,ticket.description,
	ticket.page_url,ticket.error_code,ticket.status,ticket.resolution_note,ticket.reporter_user_id::text,
	reporter.display_name,COALESCE(ticket.assignee_user_id::text,''),COALESCE(assignee.display_name,''),
	ticket.record_version,ticket.created_at,ticket.updated_at,ticket.resolved_at`

func (repository *Repository) Create(ctx context.Context, identity Identity, input CreateInput) (Ticket, error) {
	if repository == nil || repository.pool == nil || !identity.Valid() || input.normalize() != nil {
		return Ticket{}, ErrInvalid
	}
	var result Ticket
	err := database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH inserted AS (
			INSERT INTO platform.support_tickets(tenant_id,domain_id,reporter_user_id,client_request_id,category,priority,subject,description,page_url,error_code)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(tenant_id,reporter_user_id,client_request_id) DO NOTHING
			RETURNING *
		), selected AS (
			SELECT * FROM inserted
			UNION ALL
			SELECT * FROM platform.support_tickets WHERE tenant_id=$1 AND reporter_user_id=$3 AND client_request_id=$4
			LIMIT 1
		) SELECT `+ticketColumns+` FROM selected AS ticket
		JOIN platform.users AS reporter ON reporter.tenant_id=ticket.tenant_id AND reporter.id=ticket.reporter_user_id
		LEFT JOIN platform.users AS assignee ON assignee.tenant_id=ticket.tenant_id AND assignee.id=ticket.assignee_user_id`,
			identity.TenantID, identity.DomainID, identity.ActorID, input.ClientRequestID, input.Category, input.Priority,
			input.Subject, input.Description, input.PageURL, input.ErrorCode)
		return scanTicket(row, &result)
	})
	return result, mapError(err)
}

func (repository *Repository) List(ctx context.Context, identity Identity, queue bool, limit int) ([]Ticket, error) {
	if repository == nil || repository.pool == nil || !identity.Valid() || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	items := []Ticket{}
	err := database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		filter := "AND ticket.reporter_user_id=$3"
		limitPlaceholder := "$4"
		arguments := []any{identity.TenantID, identity.DomainID, identity.ActorID, limit}
		if queue {
			filter = ""
			limitPlaceholder = "$3"
			arguments = []any{identity.TenantID, identity.DomainID, limit}
		}
		rows, err := tx.Query(ctx, `SELECT `+ticketColumns+` FROM platform.support_tickets AS ticket
			JOIN platform.users AS reporter ON reporter.tenant_id=ticket.tenant_id AND reporter.id=ticket.reporter_user_id
			LEFT JOIN platform.users AS assignee ON assignee.tenant_id=ticket.tenant_id AND assignee.id=ticket.assignee_user_id
			WHERE ticket.tenant_id=$1 AND ticket.domain_id=$2 `+filter+`
			ORDER BY CASE ticket.status WHEN 'OPEN' THEN 0 WHEN 'IN_PROGRESS' THEN 1 WHEN 'RESOLVED' THEN 2 ELSE 3 END,
			CASE ticket.priority WHEN 'URGENT' THEN 0 WHEN 'HIGH' THEN 1 ELSE 2 END,ticket.updated_at DESC LIMIT `+limitPlaceholder,
			arguments...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Ticket
			if err := scanTicket(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, mapError(err)
}

func (repository *Repository) Transition(ctx context.Context, identity Identity, id string, input TransitionInput) (Ticket, error) {
	if repository == nil || repository.pool == nil || !identity.Valid() || input.normalize() != nil {
		return Ticket{}, ErrInvalid
	}
	var result Ticket
	err := database.WithTenantTx(ctx, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH selected AS (
			UPDATE platform.support_tickets SET status=$1,resolution_note=$2,
			resolved_at=CASE WHEN $1 IN('RESOLVED','CLOSED') THEN now() ELSE NULL END,
			record_version=record_version+1,updated_at=now()
			WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND record_version=$6
			RETURNING *
		) SELECT `+ticketColumns+` FROM selected AS ticket
		JOIN platform.users AS reporter ON reporter.tenant_id=ticket.tenant_id AND reporter.id=ticket.reporter_user_id
		LEFT JOIN platform.users AS assignee ON assignee.tenant_id=ticket.tenant_id AND assignee.id=ticket.assignee_user_id`,
			input.Status, input.ResolutionNote, identity.TenantID, identity.DomainID, id, input.RecordVersion)
		if err := scanTicket(row, &result); errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		} else {
			return err
		}
	})
	return result, mapError(err)
}

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner, value *Ticket) error {
	return row.Scan(&value.ID, &value.Category, &value.Priority, &value.Subject, &value.Description,
		&value.PageURL, &value.ErrorCode, &value.Status, &value.ResolutionNote, &value.ReporterUserID,
		&value.ReporterName, &value.AssigneeUserID, &value.AssigneeName, &value.RecordVersion,
		&value.CreatedAt, &value.UpdatedAt, &value.ResolvedAt)
}

func mapError(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23514", "22P02", "22001":
			return ErrInvalid
		case "40001", "23505":
			return ErrConflict
		case "42501":
			return ErrForbidden
		}
	}
	return err
}
