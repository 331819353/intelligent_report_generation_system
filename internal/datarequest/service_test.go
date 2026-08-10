package datarequest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	createdCommand  CreateCommand
	createdIdentity Identity
	createCalls     int
	items           map[string]Request
	err             error
}

type exportStoreFixture struct {
	fakeStore
	command ControlledExportCommand
}

func (store *exportStoreFixture) EnqueueControlledExport(
	_ context.Context, command ControlledExportCommand,
) (ControlledExportJob, error) {
	store.command = command
	return ControlledExportJob{
		JobID: uuid.NewString(), DataRequestID: command.DataRequestID,
		State: ControlledExportPending, ExpiresAt: command.ExpiresAt,
		MaxDownloads: command.MaxDownloads,
	}, nil
}

func (store *fakeStore) Create(_ context.Context, identity Identity, command CreateCommand) (Request, error) {
	store.createCalls++
	store.createdIdentity = identity
	store.createdCommand = command
	if store.err != nil {
		return Request{}, store.err
	}
	result := Request{
		ID: command.ID, TenantID: identity.TenantID, DomainID: identity.DomainID,
		RequesterUserID: identity.ActorID, SourceQuestionRunID: command.SourceQuestionRunID,
		RequestText: command.RequestText, ParsedContext: command.ParsedContext,
		BusinessPurpose: command.BusinessPurpose, RequiredFields: command.RequiredFields,
		SensitivityLevel: SensitivityInternal, State: StateDraft, RecordVersion: 1,
		SLADueAt: command.SLADueAt, CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	}
	if store.items == nil {
		store.items = map[string]Request{}
	}
	store.items[result.ID] = result
	return result, nil
}

func (store *fakeStore) List(_ context.Context, identity Identity, limit int) ([]Request, error) {
	if store.err != nil {
		return nil, store.err
	}
	result := []Request{}
	for _, item := range store.items {
		if item.TenantID == identity.TenantID && item.DomainID == identity.DomainID &&
			item.RequesterUserID == identity.ActorID && len(result) < limit {
			result = append(result, item)
		}
	}
	return result, nil
}

func (store *fakeStore) Get(_ context.Context, _ Identity, requestID string) (Request, error) {
	if store.err != nil {
		return Request{}, store.err
	}
	result, ok := store.items[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	return result, nil
}

func (store *fakeStore) Submit(
	_ context.Context, _ Identity, requestID string, recordVersion int64, now time.Time,
) (Request, error) {
	result, ok := store.items[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	if result.RecordVersion != recordVersion {
		return Request{}, ErrVersionConflict
	}
	result.State, result.RecordVersion, result.UpdatedAt = StateSubmitted, result.RecordVersion+1, now
	result.SubmittedAt = &now
	store.items[requestID] = result
	return result, nil
}

func (store *fakeStore) Transition(
	_ context.Context, _ Identity, requestID string, input TransitionInput, now time.Time,
) (Request, error) {
	result, ok := store.items[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	if result.RecordVersion != input.RecordVersion {
		return Request{}, ErrVersionConflict
	}
	result.State, result.RecordVersion, result.UpdatedAt = input.ToState, result.RecordVersion+1, now
	store.items[requestID] = result
	return result, nil
}

func TestCreateNormalizesGovernedContextAndSLA(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	metricA, metricB := uuid.NewString(), uuid.NewString()
	sourceRunID := uuid.NewString()
	store := &fakeStore{}
	service := NewService(store)
	service.now = func() time.Time { return now }
	identity := testIdentity()
	result, err := service.Create(context.Background(), identity, CreateInput{
		SourceQuestionRunID: sourceRunID,
		RequestText:         "  导出本月订单明细  ",
		ParsedContext: ParsedContext{
			MetricIDs: []string{metricB, metricA},
			TimeRange: &TimeRange{
				Start: now.Add(-7 * 24 * time.Hour), EndExclusive: now,
				Timezone: "Asia/Shanghai", Grain: "day",
			},
		},
		BusinessPurpose: "  月度经营复盘  ",
		RequiredFields:  []FieldRef{{DatasetVersionID: uuid.NewString(), FieldID: "order_id"}},
		SLADueAt:        now.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.createCalls != 1 || store.createdIdentity != identity || result.State != StateDraft ||
		store.createdCommand.RequestText != "导出本月订单明细" ||
		store.createdCommand.BusinessPurpose != "月度经营复盘" ||
		store.createdCommand.ParsedContext.MetricIDs[0] > store.createdCommand.ParsedContext.MetricIDs[1] ||
		store.createdCommand.ParsedContext.TimeRange.Grain != "DAY" {
		t.Fatalf("created request = %#v, command = %#v", result, store.createdCommand)
	}
}

func TestCreateRejectsClientContextWithoutSourceRunAndUnsafeSLA(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	service := NewService(&fakeStore{})
	service.now = func() time.Time { return now }
	base := CreateInput{
		RequestText: "主动申请", BusinessPurpose: "经营复盘",
		RequiredFields: []FieldRef{{DatasetVersionID: uuid.NewString(), FieldID: "order_id"}},
		SLADueAt:       now.Add(48 * time.Hour),
	}
	base.ParsedContext.MetricIDs = []string{uuid.NewString()}
	if _, err := service.Create(context.Background(), testIdentity(), base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("context without run error = %v", err)
	}
	base.ParsedContext = ParsedContext{}
	base.SLADueAt = now.Add(30 * time.Minute)
	if _, err := service.Create(context.Background(), testIdentity(), base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("short SLA error = %v", err)
	}
}

func TestStateMachineAllowsOnlyDocumentedTransitions(t *testing.T) {
	allowed := map[[2]State]bool{
		{StateDraft, StateSubmitted}:      true,
		{StateSubmitted, StateApproved}:   true,
		{StateSubmitted, StateRejected}:   true,
		{StateApproved, StateInProgress}:  true,
		{StateInProgress, StateDelivered}: true,
		{StateDelivered, StateClosed}:     true,
	}
	states := []State{
		StateDraft, StateSubmitted, StateApproved, StateRejected,
		StateInProgress, StateDelivered, StateClosed,
	}
	for _, from := range states {
		for _, to := range states {
			if ValidTransition(from, to) != allowed[[2]State{from, to}] {
				t.Fatalf("transition %s -> %s = %v", from, to, ValidTransition(from, to))
			}
		}
	}
}

func TestServiceEnqueuesOnlyCurrentApprovedControlledExport(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	identity := testIdentity()
	requestID := uuid.NewString()
	store := &exportStoreFixture{}
	store.items = map[string]Request{requestID: {
		ID: requestID, TenantID: identity.TenantID, DomainID: identity.DomainID,
		State: StateApproved, RecordVersion: 3, ApproverUserIDs: []string{identity.ActorID},
		SensitivityLevel: SensitivityInternal,
		RequiredFields:   []FieldRef{{DatasetVersionID: uuid.NewString(), FieldID: "order_id"}},
	}}
	service := NewService(store)
	service.exportBridge.now = func() time.Time { return now }
	job, err := service.EnqueueExport(context.Background(), identity, requestID, 3)
	if err != nil || job.State != ControlledExportPending ||
		store.command.MaxDownloads != DefaultExportDownloads ||
		!store.command.ExpiresAt.Equal(now.Add(DefaultExportTTL)) {
		t.Fatalf("job=%#v command=%#v err=%v", job, store.command, err)
	}
	if _, err := service.EnqueueExport(context.Background(), identity, requestID, 2); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale export err=%v", err)
	}
}

func TestTransitionShapeRequiresRejectionReasonAndDeliveryReference(t *testing.T) {
	service := NewService(&fakeStore{})
	identity := testIdentity()
	requestID := uuid.NewString()
	for _, input := range []TransitionInput{
		{ToState: StateRejected, RecordVersion: 2},
		{ToState: StateDelivered, RecordVersion: 5, DeliveryType: DeliveryOneTimeExport},
		{ToState: StateApproved, RecordVersion: 2, DeliveryType: DeliveryNewDataset, DeliveryRef: "dataset"},
		{ToState: StateInProgress, RecordVersion: 3, SecurityCosignUserID: uuid.NewString()},
	} {
		if _, err := service.Transition(context.Background(), identity, requestID, input); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("input %#v error = %v", input, err)
		}
	}
}

func testIdentity() Identity {
	return Identity{TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString()}
}
