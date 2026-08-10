package schedule

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

type Repository interface {
	Create(context.Context, Identity, askdata.ID, CreateInput, time.Time, time.Time) (Schedule, error)
	List(context.Context, Identity, askdata.ID, int) ([]Schedule, error)
	Get(context.Context, Identity, askdata.ID) (Schedule, []Subscription, error)
	SetState(context.Context, Identity, askdata.ID, int64, State, time.Time) (Schedule, error)
	AddSubscription(context.Context, Identity, askdata.ID, SubscriptionInput, time.Time) (Subscription, error)
	RevokeSubscription(context.Context, Identity, askdata.ID, askdata.ID, time.Time) error
	ListDeliveries(context.Context, Identity, int) ([]Delivery, error)
	MarkDeliveryRead(context.Context, Identity, askdata.ID, time.Time) (Delivery, error)
	Backfill(context.Context, Identity, askdata.ID, time.Time, time.Time) (int, error)
}
type Authorizer interface {
	CheckReportView(context.Context, reportstore.Identity, askdata.ID) error
	CheckReportEdit(context.Context, reportstore.Identity, askdata.ID) error
}
type Service struct {
	repository Repository
	authorizer Authorizer
	now        func() time.Time
}

func NewService(repository Repository, authorizer Authorizer) (*Service, error) {
	if repository == nil || authorizer == nil {
		return nil, errors.New("report schedule dependencies are incomplete")
	}
	return &Service{repository: repository, authorizer: authorizer, now: time.Now}, nil
}
func (s *Service) Create(ctx context.Context, i Identity, reportID askdata.ID, input CreateInput) (Schedule, error) {
	if i.Validate() != nil || reportID.Validate() != nil {
		return Schedule{}, ErrInvalid
	}
	if err := s.authorizer.CheckReportEdit(ctx, toReportIdentity(i), reportID); err != nil {
		return Schedule{}, ErrForbidden
	}
	next, err := input.Normalize(s.clock())
	if err != nil {
		return Schedule{}, err
	}
	return s.repository.Create(ctx, i, reportID, input, next, s.clock())
}
func (s *Service) List(ctx context.Context, i Identity, reportID askdata.ID, limit int) ([]Schedule, error) {
	if i.Validate() != nil || reportID.Validate() != nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	if s.authorizer.CheckReportView(ctx, toReportIdentity(i), reportID) != nil {
		return nil, ErrForbidden
	}
	return s.repository.List(ctx, i, reportID, limit)
}
func (s *Service) Get(ctx context.Context, i Identity, id askdata.ID) (Schedule, []Subscription, error) {
	if i.Validate() != nil || id.Validate() != nil {
		return Schedule{}, nil, ErrInvalid
	}
	return s.repository.Get(ctx, i, id)
}
func (s *Service) SetState(ctx context.Context, i Identity, id askdata.ID, input VersionInput, state State) (Schedule, error) {
	if i.Validate() != nil || id.Validate() != nil || input.ExpectedVersion < 1 || (state != StateActive && state != StatePaused) {
		return Schedule{}, ErrInvalid
	}
	current, _, err := s.repository.Get(ctx, i, id)
	if err != nil {
		return Schedule{}, err
	}
	if s.authorizer.CheckReportEdit(ctx, toReportIdentity(i), current.ReportID) != nil {
		return Schedule{}, ErrForbidden
	}
	return s.repository.SetState(ctx, i, id, input.ExpectedVersion, state, s.clock())
}
func (s *Service) Subscribe(ctx context.Context, i Identity, id askdata.ID, input SubscriptionInput) (Subscription, error) {
	if i.Validate() != nil || id.Validate() != nil || input.RecipientUserID.Validate() != nil {
		return Subscription{}, ErrInvalid
	}
	if input.Channel == "" {
		input.Channel = "IN_APP"
	}
	if input.Channel != "IN_APP" {
		return Subscription{}, ErrInvalid
	}
	schedule, _, err := s.repository.Get(ctx, i, id)
	if err != nil {
		return Subscription{}, err
	}
	if s.authorizer.CheckReportEdit(ctx, toReportIdentity(i), schedule.ReportID) != nil && input.RecipientUserID != i.ActorID {
		return Subscription{}, ErrForbidden
	}
	recipient := reportstore.Identity{TenantID: i.TenantID, DomainID: i.DomainID, ActorID: input.RecipientUserID}
	if s.authorizer.CheckReportView(ctx, recipient, schedule.ReportID) != nil {
		return Subscription{}, ErrForbidden
	}
	return s.repository.AddSubscription(ctx, i, id, input, s.clock())
}
func (s *Service) Unsubscribe(ctx context.Context, i Identity, id, subscriptionID askdata.ID) error {
	if i.Validate() != nil || id.Validate() != nil || subscriptionID.Validate() != nil {
		return ErrInvalid
	}
	return s.repository.RevokeSubscription(ctx, i, id, subscriptionID, s.clock())
}
func (s *Service) Deliveries(ctx context.Context, i Identity, limit int) ([]Delivery, error) {
	if i.Validate() != nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	return s.repository.ListDeliveries(ctx, i, limit)
}
func (s *Service) MarkDeliveryRead(ctx context.Context, i Identity, id askdata.ID) (Delivery, error) {
	if i.Validate() != nil || id.Validate() != nil {
		return Delivery{}, ErrInvalid
	}
	return s.repository.MarkDeliveryRead(ctx, i, id, s.clock())
}
func (s *Service) Backfill(ctx context.Context, i Identity, id askdata.ID, scheduledFor time.Time) (int, error) {
	if i.Validate() != nil || id.Validate() != nil || scheduledFor.IsZero() || scheduledFor.After(s.clock()) {
		return 0, ErrInvalid
	}
	current, _, err := s.repository.Get(ctx, i, id)
	if err != nil {
		return 0, err
	}
	if s.authorizer.CheckReportEdit(ctx, toReportIdentity(i), current.ReportID) != nil {
		return 0, ErrForbidden
	}
	// Delivery rows are worker-owned and their RLS write policy is SYSTEM-only.
	// The edit authorization above is the control-plane boundary for this
	// explicit manual materialization.
	return s.repository.Backfill(database.WithoutAccessContext(ctx), i, id, scheduledFor, s.clock())
}
func (s *Service) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}
func toReportIdentity(i Identity) reportstore.Identity {
	return reportstore.Identity{TenantID: i.TenantID, DomainID: i.DomainID, ActorID: i.ActorID}
}
