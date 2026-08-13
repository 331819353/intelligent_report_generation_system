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
	validIdentity := identity.Valid() || queue && identity.DomainID == "" && identity.tenantActorValid()
	if repository == nil || repository.pool == nil || !validIdentity || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	items := []Ticket{}
	transactionContext := ctx
	if queue && identity.DomainID == "" {
		transactionContext = database.WithoutAccessContext(ctx)
	}
	err := database.WithTenantTx(transactionContext, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		if queue && identity.DomainID == "" {
			rows, err := tx.Query(ctx, `SELECT `+ticketColumns+` FROM platform.support_tickets AS ticket
				JOIN platform.users AS reporter ON reporter.tenant_id=ticket.tenant_id AND reporter.id=ticket.reporter_user_id
				LEFT JOIN platform.users AS assignee ON assignee.tenant_id=ticket.tenant_id AND assignee.id=ticket.assignee_user_id
				WHERE ticket.tenant_id=$1
				ORDER BY CASE ticket.status WHEN 'OPEN' THEN 0 WHEN 'IN_PROGRESS' THEN 1 WHEN 'RESOLVED' THEN 2 ELSE 3 END,
				CASE ticket.priority WHEN 'URGENT' THEN 0 WHEN 'HIGH' THEN 1 ELSE 2 END,ticket.updated_at DESC LIMIT $2`,
				identity.TenantID, limit)
			return collectTickets(rows, err, &items)
		}
		statement, args := domainTicketQuery(identity, queue, limit)
		rows, err := tx.Query(ctx, statement, args...)
		return collectTickets(rows, err, &items)
	})
	return items, mapError(err)
}

// domainTicketQuery 构造领域范围内的工单查询。
//
// 领域受理队列不按提交人过滤，因此比「我的工单」少一个占位符。占位符编号必须
// 连续：一旦去掉 $3 却仍然写死 LIMIT $4，Postgres 无法推断 $3 的类型，整个领域
// 受理队列都会返回 500。查询在这里单独构造，便于用测试守住这条约束。
func domainTicketQuery(identity Identity, queue bool, limit int) (string, []any) {
	filter, limitPlaceholder := "AND ticket.reporter_user_id=$3", "$4"
	args := []any{identity.TenantID, identity.DomainID, identity.ActorID, limit}
	if queue {
		filter, limitPlaceholder = "", "$3"
		args = []any{identity.TenantID, identity.DomainID, limit}
	}
	return `SELECT ` + ticketColumns + ` FROM platform.support_tickets AS ticket
		JOIN platform.users AS reporter ON reporter.tenant_id=ticket.tenant_id AND reporter.id=ticket.reporter_user_id
		LEFT JOIN platform.users AS assignee ON assignee.tenant_id=ticket.tenant_id AND assignee.id=ticket.assignee_user_id
		WHERE ticket.tenant_id=$1 AND ticket.domain_id=$2 ` + filter + `
		ORDER BY CASE ticket.status WHEN 'OPEN' THEN 0 WHEN 'IN_PROGRESS' THEN 1 WHEN 'RESOLVED' THEN 2 ELSE 3 END,
		CASE ticket.priority WHEN 'URGENT' THEN 0 WHEN 'HIGH' THEN 1 ELSE 2 END,ticket.updated_at DESC LIMIT ` + limitPlaceholder, args
}

func collectTickets(rows pgx.Rows, err error, items *[]Ticket) error {
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Ticket
		if err := scanTicket(rows, &item); err != nil {
			return err
		}
		*items = append(*items, item)
	}
	return rows.Err()
}

func (repository *Repository) Transition(ctx context.Context, identity Identity, id string, input TransitionInput) (Ticket, error) {
	validIdentity := identity.Valid() || identity.DomainID == "" && identity.tenantActorValid()
	if repository == nil || repository.pool == nil || !validIdentity || input.normalize() != nil {
		return Ticket{}, ErrInvalid
	}
	var result Ticket
	transactionContext := ctx
	if identity.DomainID == "" {
		transactionContext = database.WithoutAccessContext(ctx)
	}
	err := database.WithTenantTx(transactionContext, repository.pool, identity.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH selected AS (
			UPDATE platform.support_tickets SET status=$1,resolution_note=$2,
			resolved_at=CASE WHEN $1 IN('RESOLVED','CLOSED') THEN now() ELSE NULL END,
			record_version=record_version+1,updated_at=now()
			WHERE tenant_id=$3 AND ($4::text='' OR domain_id=$4::uuid) AND id=$5 AND record_version=$6
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
