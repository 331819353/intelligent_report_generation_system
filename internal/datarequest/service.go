package datarequest

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Store interface {
	Create(context.Context, Identity, CreateCommand) (Request, error)
	List(context.Context, Identity, int) ([]Request, error)
	Get(context.Context, Identity, string) (Request, error)
	Submit(context.Context, Identity, string, int64, time.Time) (Request, error)
	Transition(context.Context, Identity, string, TransitionInput, time.Time) (Request, error)
}

type Service struct {
	store        Store
	exportBridge *ExportBridge
	now          func() time.Time
}

func NewService(store Store) *Service {
	service := &Service{store: store, now: time.Now}
	if queue, ok := store.(ControlledExportQueue); ok {
		service.exportBridge, _ = NewExportBridge(queue)
	}
	return service
}

func (service *Service) Create(
	ctx context.Context, identity Identity, input CreateInput,
) (Request, error) {
	if service == nil || service.store == nil || !identity.Valid() {
		return Request{}, ErrInvalidRequest
	}
	now := service.now().UTC()
	input.SourceQuestionRunID = strings.TrimSpace(input.SourceQuestionRunID)
	input.RequestText = strings.TrimSpace(input.RequestText)
	input.BusinessPurpose = strings.TrimSpace(input.BusinessPurpose)
	if (input.SourceQuestionRunID != "" && uuid.Validate(input.SourceQuestionRunID) != nil) ||
		!boundedText(input.RequestText, 1, 4096) ||
		!boundedText(input.BusinessPurpose, 1, 2000) || input.SLADueAt.IsZero() {
		return Request{}, ErrInvalidRequest
	}
	contextValue, err := input.ParsedContext.Normalize()
	if err != nil || input.SourceQuestionRunID == "" && !contextValue.Empty() {
		return Request{}, ErrInvalidRequest
	}
	fields, err := normalizeFields(input.RequiredFields)
	if err != nil {
		return Request{}, err
	}
	dueAt := input.SLADueAt.UTC()
	if !dueAt.After(now.Add(time.Hour)) || dueAt.After(now.Add(90*24*time.Hour)) {
		return Request{}, ErrInvalidRequest
	}
	return service.store.Create(ctx, identity, CreateCommand{
		ID: uuid.NewString(), SourceQuestionRunID: input.SourceQuestionRunID,
		RequestText: input.RequestText, ParsedContext: contextValue,
		BusinessPurpose: input.BusinessPurpose, RequiredFields: fields,
		SLADueAt: dueAt, CreatedAt: now,
	})
}

func (service *Service) List(
	ctx context.Context, identity Identity, limit int,
) ([]Request, error) {
	if service == nil || service.store == nil || !identity.Valid() || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	return service.store.List(ctx, identity, limit)
}

func (service *Service) Get(
	ctx context.Context, identity Identity, requestID string,
) (Request, error) {
	if service == nil || service.store == nil || !identity.Valid() || uuid.Validate(requestID) != nil {
		return Request{}, ErrInvalidRequest
	}
	return service.store.Get(ctx, identity, requestID)
}

func (service *Service) Submit(
	ctx context.Context, identity Identity, requestID string, recordVersion int64,
) (Request, error) {
	if service == nil || service.store == nil || !identity.Valid() ||
		uuid.Validate(requestID) != nil || recordVersion < 1 {
		return Request{}, ErrInvalidRequest
	}
	return service.store.Submit(ctx, identity, requestID, recordVersion, service.now().UTC())
}

func (service *Service) Transition(
	ctx context.Context, identity Identity, requestID string, input TransitionInput,
) (Request, error) {
	if service == nil || service.store == nil || !identity.Valid() ||
		uuid.Validate(requestID) != nil || input.RecordVersion < 1 {
		return Request{}, ErrInvalidRequest
	}
	input.Note = strings.TrimSpace(input.Note)
	input.SecurityCosignUserID = strings.TrimSpace(input.SecurityCosignUserID)
	input.AssigneeUserID = strings.TrimSpace(input.AssigneeUserID)
	input.DeliveryRef = strings.TrimSpace(input.DeliveryRef)
	if !boundedText(input.Note, 0, 2000) ||
		(input.SecurityCosignUserID != "" && uuid.Validate(input.SecurityCosignUserID) != nil) ||
		(input.AssigneeUserID != "" && uuid.Validate(input.AssigneeUserID) != nil) ||
		!boundedText(input.DeliveryRef, 0, 500) {
		return Request{}, ErrInvalidRequest
	}
	if input.ToState == StateRejected && input.Note == "" ||
		input.ToState == StateDelivered && (!validDeliveryType(input.DeliveryType) || input.DeliveryRef == "") ||
		input.ToState == StateDelivered && input.DeliveryType == DeliveryOneTimeExport &&
			uuid.Validate(input.DeliveryRef) != nil ||
		input.ToState != StateDelivered && (input.DeliveryType != "" || input.DeliveryRef != "") ||
		input.ToState != StateApproved && input.SecurityCosignUserID != "" ||
		input.ToState != StateInProgress && input.AssigneeUserID != "" {
		return Request{}, ErrInvalidRequest
	}
	return service.store.Transition(ctx, identity, requestID, input, service.now().UTC())
}

func (service *Service) EnqueueExport(
	ctx context.Context, identity Identity, requestID string, recordVersion int64,
) (ControlledExportJob, error) {
	if service == nil || service.store == nil || service.exportBridge == nil || !identity.Valid() ||
		uuid.Validate(requestID) != nil || recordVersion < 1 {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	request, err := service.store.Get(ctx, identity, requestID)
	if err != nil {
		return ControlledExportJob{}, err
	}
	if request.RecordVersion != recordVersion {
		return ControlledExportJob{}, ErrVersionConflict
	}
	return service.exportBridge.Enqueue(ctx, identity, request)
}

func validDeliveryType(value DeliveryType) bool {
	return value == DeliveryExistingReport || value == DeliveryNewDataset || value == DeliveryOneTimeExport
}
