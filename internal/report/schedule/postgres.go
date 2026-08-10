package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Create(ctx context.Context, i Identity, reportID askdata.ID, input CreateInput, next, now time.Time) (Schedule, error) {
	var result Schedule
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		return scanSchedule(tx.QueryRow(ctx, `INSERT INTO platform.report_schedules(id,tenant_id,domain_id,report_id,report_version_id,name,schedule_kind,local_time,weekdays,day_of_month,timezone,business_calendar,next_run_at,max_consecutive_failures,miss_after_seconds,owner_user_id,created_by,created_at,updated_at)
 SELECT $1,$2,$3,report.id,version.id,$6,$7,$8::time,$9,$10,$11,$12,$13,$14,$15,$16,$16,$17,$17
 FROM platform.reports report JOIN platform.report_versions version ON version.tenant_id=report.tenant_id AND version.report_id=report.id
	 WHERE report.tenant_id=$2 AND report.domain_id=$3 AND report.id=$4 AND report.status='ACTIVE' AND version.id=$5 AND version.artifact_state='READY'
	 RETURNING id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at`, uuid.NewString(), i.TenantID, i.DomainID, reportID, input.ReportVersionID, input.Name, input.ScheduleKind, input.LocalTime, smallWeekdays(input.Weekdays), input.DayOfMonth, input.Timezone, input.BusinessCalendar, next, input.MaxConsecutiveFailures, input.MissAfterSeconds, i.ActorID, now), &result)
	})
	return result, mapError(err)
}
func (s *PostgresStore) List(ctx context.Context, i Identity, reportID askdata.ID, limit int) ([]Schedule, error) {
	items := []Schedule{}
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at FROM platform.report_schedules WHERE tenant_id=$1 AND domain_id=$2 AND report_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4`, i.TenantID, i.DomainID, reportID, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var value Schedule
			if e = scanSchedule(rows, &value); e != nil {
				return e
			}
			items = append(items, value)
		}
		return rows.Err()
	})
	return items, mapError(err)
}
func (s *PostgresStore) Get(ctx context.Context, i Identity, id askdata.ID) (Schedule, []Subscription, error) {
	var result Schedule
	subscriptions := []Subscription{}
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		if e := scanSchedule(tx.QueryRow(ctx, `SELECT id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at FROM platform.report_schedules WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`, i.TenantID, i.DomainID, id), &result); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id::text,schedule_id::text,recipient_user_id::text,channel,state,record_version,created_at,updated_at FROM platform.report_subscriptions WHERE tenant_id=$1 AND schedule_id=$2 AND state<>'REVOKED' ORDER BY created_at,id`, i.TenantID, id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Subscription
			if e = rows.Scan(&v.ID, &v.ScheduleID, &v.RecipientUserID, &v.Channel, &v.State, &v.RecordVersion, &v.CreatedAt, &v.UpdatedAt); e != nil {
				return e
			}
			subscriptions = append(subscriptions, v)
		}
		return rows.Err()
	})
	return result, subscriptions, mapError(err)
}
func (s *PostgresStore) SetState(ctx context.Context, i Identity, id askdata.ID, expected int64, state State, now time.Time) (Schedule, error) {
	var result Schedule
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		var next any
		if state == StateActive {
			current, _, e := s.scheduleTx(ctx, tx, i, id)
			if e != nil {
				return e
			}
			input, e := createInputFromSchedule(current)
			if e != nil {
				return e
			}
			computed, e := NextOccurrence(input, now)
			if e != nil {
				return e
			}
			next = computed
		}
		return scanSchedule(tx.QueryRow(ctx, `UPDATE platform.report_schedules SET state=$1,next_run_at=COALESCE($2::timestamptz,next_run_at),lease_token=NULL,lease_expires_at=NULL,record_version=record_version+1,updated_at=$3 WHERE tenant_id=$4 AND domain_id=$5 AND id=$6 AND record_version=$7 AND state<>'DISABLED' RETURNING id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at`, state, next, now, i.TenantID, i.DomainID, id, expected), &result)
	})
	return result, mapConcurrent(err)
}
func (s *PostgresStore) AddSubscription(ctx context.Context, i Identity, id askdata.ID, input SubscriptionInput, now time.Time) (Subscription, error) {
	var result Subscription
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.report_subscriptions(id,tenant_id,domain_id,schedule_id,recipient_user_id,channel,state,created_by,created_at,updated_at) SELECT $1,$2,$3,schedule.id,$5,$6,'ACTIVE',$7,$8,$8 FROM platform.report_schedules schedule JOIN platform.users recipient ON recipient.tenant_id=schedule.tenant_id AND recipient.id=$5 JOIN platform.domain_memberships membership ON membership.tenant_id=schedule.tenant_id AND membership.domain_id=schedule.domain_id AND membership.user_id=recipient.id WHERE schedule.tenant_id=$2 AND schedule.domain_id=$3 AND schedule.id=$4 AND schedule.state<>'DISABLED' AND recipient.status='ACTIVE' AND recipient.deleted_at IS NULL AND membership.status='ACTIVE' ON CONFLICT(tenant_id,schedule_id,recipient_user_id) DO UPDATE SET state='ACTIVE',record_version=platform.report_subscriptions.record_version+1,updated_at=EXCLUDED.updated_at RETURNING id::text,schedule_id::text,recipient_user_id::text,channel,state,record_version,created_at,updated_at`, uuid.NewString(), i.TenantID, i.DomainID, id, input.RecipientUserID, input.Channel, i.ActorID, now).Scan(&result.ID, &result.ScheduleID, &result.RecipientUserID, &result.Channel, &result.State, &result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
	})
	return result, mapError(err)
}
func (s *PostgresStore) RevokeSubscription(ctx context.Context, i Identity, id, subscriptionID askdata.ID, now time.Time) error {
	return mapError(database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE platform.report_subscriptions SET state='REVOKED',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND domain_id=$3 AND schedule_id=$4 AND id=$5 AND (recipient_user_id=$6 OR EXISTS(SELECT 1 FROM platform.report_schedules schedule WHERE schedule.tenant_id=$2 AND schedule.id=$4 AND platform.report_v2_can_access(schedule.report_id,ARRAY['EDIT']::text[]))) AND state<>'REVOKED'`, now, i.TenantID, i.DomainID, id, subscriptionID, i.ActorID)
		if e == nil && tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return e
	}))
}
func (s *PostgresStore) ListDeliveries(ctx context.Context, i Identity, limit int) ([]Delivery, error) {
	items := []Delivery{}
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,schedule_id::text,subscription_id::text,report_id::text,report_version_id::text,recipient_user_id::text,scheduled_for,channel,state,attempt,report_link,failure_code,access_checked_at,read_at,created_at FROM platform.report_deliveries WHERE tenant_id=$1 AND domain_id=$2 AND recipient_user_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4`, i.TenantID, i.DomainID, i.ActorID, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Delivery
			if e = scanDelivery(rows, &v); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return items, mapError(err)
}
func (s *PostgresStore) MarkDeliveryRead(ctx context.Context, i Identity, id askdata.ID, now time.Time) (Delivery, error) {
	var result Delivery
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		return scanDelivery(tx.QueryRow(ctx, `UPDATE platform.report_deliveries SET read_at=COALESCE(read_at,$1),updated_at=$1 WHERE tenant_id=$2 AND domain_id=$3 AND id=$4 AND recipient_user_id=$5 AND state='READY' RETURNING id::text,schedule_id::text,subscription_id::text,report_id::text,report_version_id::text,recipient_user_id::text,scheduled_for,channel,state,attempt,report_link,failure_code,access_checked_at,read_at,created_at`, now, i.TenantID, i.DomainID, id, i.ActorID), &result)
	})
	return result, mapError(err)
}
func (s *PostgresStore) Backfill(ctx context.Context, i Identity, id askdata.ID, scheduledFor, now time.Time) (int, error) {
	count := 0
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `INSERT INTO platform.report_deliveries(id,tenant_id,domain_id,schedule_id,subscription_id,report_id,report_version_id,recipient_user_id,scheduled_for,channel,state,next_attempt_at,created_at,updated_at) SELECT gen_random_uuid(),schedule.tenant_id,schedule.domain_id,schedule.id,subscription.id,schedule.report_id,schedule.report_version_id,subscription.recipient_user_id,$4,subscription.channel,'PENDING',$5,$5,$5 FROM platform.report_schedules schedule JOIN platform.report_subscriptions subscription ON subscription.tenant_id=schedule.tenant_id AND subscription.schedule_id=schedule.id AND subscription.state='ACTIVE' WHERE schedule.tenant_id=$1 AND schedule.domain_id=$2 AND schedule.id=$3 AND schedule.state<>'DISABLED' ON CONFLICT(tenant_id,subscription_id,scheduled_for) DO NOTHING`, i.TenantID, i.DomainID, id, scheduledFor, now)
		if e != nil {
			return e
		}
		count = int(tag.RowsAffected())
		_, e = tx.Exec(ctx, `INSERT INTO platform.report_delivery_events(id,tenant_id,domain_id,schedule_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,$4,'BACKFILL_REQUESTED',$5,jsonb_build_object('scheduledFor',$6::timestamptz),$7)`, uuid.NewString(), i.TenantID, i.DomainID, id, i.ActorID, scheduledFor, now)
		return e
	})
	return count, mapError(err)
}

func (s *PostgresStore) scheduleTx(ctx context.Context, tx pgx.Tx, i Identity, id askdata.ID) (Schedule, []Subscription, error) {
	var schedule Schedule
	if e := scanSchedule(tx.QueryRow(ctx, `SELECT id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at FROM platform.report_schedules WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`, i.TenantID, i.DomainID, id), &schedule); e != nil {
		return Schedule{}, nil, e
	}
	return schedule, nil, nil
}

type scanner interface{ Scan(...any) error }

func scanSchedule(row scanner, v *Schedule) error {
	var weekdays []int16
	var dayOfMonth *int16
	if err := row.Scan(&v.ID, &v.ReportID, &v.ReportVersionID, &v.Name, &v.Kind, &v.LocalTime, &weekdays, &dayOfMonth, &v.Timezone, &v.BusinessCalendar, &v.State, &v.NextRunAt, &v.ConsecutiveFailures, &v.MaxConsecutiveFailures, &v.MissAfterSeconds, &v.OwnerUserID, &v.RecordVersion, &v.LastFailureCode, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return err
	}
	v.Weekdays = make([]int, len(weekdays))
	for index, weekday := range weekdays {
		v.Weekdays[index] = int(weekday)
	}
	if dayOfMonth == nil {
		v.DayOfMonth = nil
	} else {
		value := int(*dayOfMonth)
		v.DayOfMonth = &value
	}
	return nil
}
func scanDelivery(row scanner, v *Delivery) error {
	return row.Scan(&v.ID, &v.ScheduleID, &v.SubscriptionID, &v.ReportID, &v.ReportVersionID, &v.RecipientUserID, &v.ScheduledFor, &v.Channel, &v.State, &v.Attempt, &v.ReportLink, &v.FailureCode, &v.AccessCheckedAt, &v.ReadAt, &v.CreatedAt)
}
func createInputFromSchedule(v Schedule) (CreateInput, error) {
	input := CreateInput{ReportVersionID: v.ReportVersionID, Name: v.Name, ScheduleKind: v.Kind, LocalTime: v.LocalTime, Weekdays: v.Weekdays, DayOfMonth: v.DayOfMonth, Timezone: v.Timezone, BusinessCalendar: v.BusinessCalendar, MaxConsecutiveFailures: v.MaxConsecutiveFailures, MissAfterSeconds: v.MissAfterSeconds}
	return input, nil
}
func smallWeekdays(values []int) []int16 {
	result := make([]int16, len(values))
	for index, value := range values {
		result[index] = int16(value)
	}
	return result
}
func mapConcurrent(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	return mapError(err)
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001":
			return ErrConflict
		case "23503", "42501":
			return ErrForbidden
		case "23514", "22P02", "22023":
			return ErrInvalid
		}
	}
	return fmt.Errorf("report schedule store: %w", err)
}
