package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

const maxDeliveryAttempts = 5

type Worker struct {
	store      *PostgresStore
	authorizer Authorizer
	now        func() time.Time
}

func NewWorker(store *PostgresStore, authorizer Authorizer) (*Worker, error) {
	if store == nil || store.pool == nil || authorizer == nil {
		return nil, errors.New("report schedule worker dependencies are incomplete")
	}
	return &Worker{store: store, authorizer: authorizer, now: time.Now}, nil
}
func (w *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	rows, err := w.store.pool.Query(ctx, `SELECT tenant_id::text FROM platform.report_schedule_work_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		values = append(values, id)
	}
	return values, rows.Err()
}
func (w *Worker) ProcessTenant(ctx context.Context, tenantID string, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, ErrInvalid
	}
	now := w.clock()
	created, err := w.materializeDue(ctx, tenantID, now, limit)
	if err != nil {
		return created, err
	}
	for index := 0; index < limit; index++ {
		delivery, token, found, e := w.claimDelivery(ctx, tenantID, now)
		if e != nil {
			return created, e
		}
		if !found {
			break
		}
		state, code := w.evaluate(ctx, tenantID, delivery)
		if e = w.finishDelivery(ctx, tenantID, delivery, token, state, code, now); e != nil {
			return created, e
		}
		created++
	}
	return created, nil
}
func (w *Worker) materializeDue(ctx context.Context, tenantID string, now time.Time, limit int) (int, error) {
	count := 0
	for index := 0; index < limit; index++ {
		processed := false
		err := database.WithTenantTx(ctx, w.store.pool, tenantID, func(tx pgx.Tx) error {
			var schedule Schedule
			var domainID askdata.ID
			var weekdays []int16
			var dayOfMonth *int16
			row := tx.QueryRow(ctx, `SELECT id::text,report_id::text,report_version_id::text,name,schedule_kind,local_time::text,weekdays,day_of_month,timezone,business_calendar,state,next_run_at,consecutive_failures,max_consecutive_failures,miss_after_seconds,owner_user_id::text,record_version,last_failure_code,created_at,updated_at,domain_id::text FROM platform.report_schedules WHERE tenant_id=$1 AND state='ACTIVE' AND next_run_at<=$2 AND (lease_expires_at IS NULL OR lease_expires_at<=$2) ORDER BY next_run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, tenantID, now)
			if e := row.Scan(&schedule.ID, &schedule.ReportID, &schedule.ReportVersionID, &schedule.Name, &schedule.Kind, &schedule.LocalTime, &weekdays, &dayOfMonth, &schedule.Timezone, &schedule.BusinessCalendar, &schedule.State, &schedule.NextRunAt, &schedule.ConsecutiveFailures, &schedule.MaxConsecutiveFailures, &schedule.MissAfterSeconds, &schedule.OwnerUserID, &schedule.RecordVersion, &schedule.LastFailureCode, &schedule.CreatedAt, &schedule.UpdatedAt, &domainID); errors.Is(e, pgx.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			}
			schedule.Weekdays = make([]int, len(weekdays))
			for index, weekday := range weekdays {
				schedule.Weekdays[index] = int(weekday)
			}
			if dayOfMonth != nil {
				value := int(*dayOfMonth)
				schedule.DayOfMonth = &value
			}
			processed = true
			input, _ := createInputFromSchedule(schedule)
			next, e := NextOccurrence(input, schedule.NextRunAt)
			if e != nil {
				return e
			}
			missed := now.Sub(schedule.NextRunAt) > time.Duration(schedule.MissAfterSeconds)*time.Second
			state := "PENDING"
			failure := ""
			checkedAt := any(nil)
			if missed {
				state = "MISSED"
				failure = "MISSED_WINDOW"
				checkedAt = now
			}
			tag, e := tx.Exec(ctx, `INSERT INTO platform.report_deliveries(id,tenant_id,domain_id,schedule_id,subscription_id,report_id,report_version_id,recipient_user_id,scheduled_for,channel,state,next_attempt_at,failure_code,access_checked_at,created_at,updated_at) SELECT gen_random_uuid(),schedule.tenant_id,schedule.domain_id,schedule.id,subscription.id,schedule.report_id,schedule.report_version_id,subscription.recipient_user_id,schedule.next_run_at,subscription.channel,$4,$2,$5,$6,$2,$2 FROM platform.report_schedules schedule JOIN platform.report_subscriptions subscription ON subscription.tenant_id=schedule.tenant_id AND subscription.schedule_id=schedule.id AND subscription.state='ACTIVE' WHERE schedule.tenant_id=$1 AND schedule.id=$3 ON CONFLICT(tenant_id,subscription_id,scheduled_for) DO NOTHING`, tenantID, now, schedule.ID, state, failure, checkedAt)
			if e != nil {
				return e
			}
			count += int(tag.RowsAffected())
			_, e = tx.Exec(ctx, `UPDATE platform.report_schedules SET next_run_at=$1,lease_token=NULL,lease_expires_at=NULL,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4`, next, now, tenantID, schedule.ID)
			if e != nil {
				return e
			}
			event := "DELIVERIES_CREATED"
			if missed {
				event = "WINDOW_MISSED"
			}
			_, e = tx.Exec(ctx, `INSERT INTO platform.report_delivery_events(id,tenant_id,domain_id,schedule_id,event_type,failure_code,details_json,created_at) VALUES($1,$2,$3,$4,$5,$6,jsonb_build_object('scheduledFor',$7::timestamptz,'deliveryCount',$8::bigint),$9)`, uuid.NewString(), tenantID, domainID, schedule.ID, event, failure, schedule.NextRunAt, tag.RowsAffected(), now)
			return e
		})
		if err != nil {
			return count, err
		}
		if !processed {
			break
		}
	}
	return count, nil
}
func (w *Worker) claimDelivery(ctx context.Context, tenantID string, now time.Time) (Delivery, string, bool, error) {
	var result Delivery
	token := uuid.NewString()
	found := false
	err := database.WithTenantTx(ctx, w.store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH candidate AS(SELECT id FROM platform.report_deliveries WHERE tenant_id=$1 AND ((state IN ('PENDING','FAILED') AND next_attempt_at<=$2 AND attempt<$3) OR (state='RUNNING' AND lease_expires_at<=$2)) ORDER BY next_attempt_at,created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE platform.report_deliveries delivery SET state='RUNNING',attempt=attempt+1,lease_token=$4,lease_expires_at=$2+interval '2 minutes',failure_code='',access_checked_at=NULL,updated_at=$2 FROM candidate WHERE delivery.tenant_id=$1 AND delivery.id=candidate.id RETURNING delivery.id::text,delivery.schedule_id::text,delivery.subscription_id::text,delivery.report_id::text,delivery.report_version_id::text,delivery.recipient_user_id::text,delivery.scheduled_for,delivery.channel,delivery.state,delivery.attempt,delivery.report_link,delivery.failure_code,delivery.access_checked_at,delivery.read_at,delivery.created_at`, tenantID, now, maxDeliveryAttempts, token).Scan(&result.ID, &result.ScheduleID, &result.SubscriptionID, &result.ReportID, &result.ReportVersionID, &result.RecipientUserID, &result.ScheduledFor, &result.Channel, &result.State, &result.Attempt, &result.ReportLink, &result.FailureCode, &result.AccessCheckedAt, &result.ReadAt, &result.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, "", false, nil
	}
	if err == nil {
		found = true
	}
	return result, token, found, err
}
func (w *Worker) evaluate(ctx context.Context, tenantID string, delivery Delivery) (string, string) {
	identity := reportstore.Identity{TenantID: askdata.ID(tenantID), DomainID: "", ActorID: delivery.RecipientUserID}
	var domain askdata.ID
	available := false
	err := database.WithTenantTx(ctx, w.store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT schedule.domain_id::text,(report.status='ACTIVE' AND version.artifact_state='READY') FROM platform.report_schedules schedule JOIN platform.reports report ON report.tenant_id=schedule.tenant_id AND report.id=schedule.report_id JOIN platform.report_versions version ON version.tenant_id=schedule.tenant_id AND version.id=schedule.report_version_id AND version.report_id=report.id WHERE schedule.tenant_id=$1 AND schedule.id=$2`, tenantID, delivery.ScheduleID).Scan(&domain, &available)
	})
	if err != nil {
		return "FAILED", "INTERNAL_UNAVAILABLE"
	}
	if !available {
		return "FAILED", "REPORT_VERSION_UNAVAILABLE"
	}
	identity.DomainID = domain
	executionContext := database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	if w.authorizer.CheckReportView(executionContext, identity, delivery.ReportID) != nil {
		return "SKIPPED", "NO_PERMISSION"
	}
	return "READY", ""
}
func (w *Worker) finishDelivery(ctx context.Context, tenantID string, delivery Delivery, token, state, code string, now time.Time) error {
	return database.WithTenantTx(ctx, w.store.pool, tenantID, func(tx pgx.Tx) error {
		retry := state == "FAILED" && delivery.Attempt < maxDeliveryAttempts
		next := now.Add(time.Duration(1<<min(delivery.Attempt, 6)) * time.Minute)
		finalState := state
		if retry {
			finalState = "FAILED"
		}
		link := ""
		if state == "READY" {
			link = fmt.Sprintf("/reports/%s?versionId=%s", delivery.ReportID, delivery.ReportVersionID)
		}
		tag, e := tx.Exec(ctx, `UPDATE platform.report_deliveries SET state=$1,report_link=$2,failure_code=$3,access_checked_at=$4,next_attempt_at=$5,lease_token=NULL,lease_expires_at=NULL,updated_at=$4 WHERE tenant_id=$6 AND id=$7 AND state='RUNNING' AND lease_token=$8`, finalState, link, code, now, next, tenantID, delivery.ID, token)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		event := "DELIVERY_" + finalState
		if retry {
			event = "DELIVERY_RETRY_SCHEDULED"
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform.report_delivery_events(id,tenant_id,domain_id,schedule_id,delivery_id,event_type,failure_code,details_json,created_at) SELECT $1,delivery.tenant_id,delivery.domain_id,delivery.schedule_id,delivery.id,$2,$3,jsonb_build_object('attempt',delivery.attempt,'nextAttemptAt',$4::timestamptz),$5 FROM platform.report_deliveries delivery WHERE delivery.tenant_id=$6 AND delivery.id=$7`, uuid.NewString(), event, code, next, now, tenantID, delivery.ID)
		if e != nil {
			return e
		}
		if finalState == "READY" {
			_, e = tx.Exec(ctx, `UPDATE platform.report_schedules SET consecutive_failures=0,last_failure_code='',updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, delivery.ScheduleID)
		} else if finalState == "FAILED" && !retry {
			_, e = tx.Exec(ctx, `UPDATE platform.report_schedules SET consecutive_failures=consecutive_failures+1,last_failure_code=$1,state=CASE WHEN consecutive_failures+1>=max_consecutive_failures THEN 'PAUSED' ELSE state END,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4`, code, now, tenantID, delivery.ScheduleID)
		}
		return e
	})
}
func (w *Worker) clock() time.Time {
	if w.now == nil {
		return time.Now().UTC()
	}
	return w.now().UTC()
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
