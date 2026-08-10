// Package inbound is the Report V2 side of the AskData add-to-report
// boundary. It deliberately authorizes again instead of trusting the source
// bounded context.
package inbound

import (
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/reportasset"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/store"
)

var ErrUnauthorized = errors.New("report inbound operation is unauthorized")

type Authorizer interface {
	AuthorizeReportEdit(context.Context, store.Identity, askdata.ID) error
	AuthorizeSemanticBinding(context.Context, store.Identity, report.SemanticQueryRef) error
}

type AtomicStore interface {
	ApplyInbound(context.Context, store.Identity, store.InboundInput) (store.InboundResult, error)
}

type Service struct {
	authorizer Authorizer
	store      AtomicStore
}

func NewService(authorizer Authorizer, atomicStore AtomicStore) (*Service, error) {
	if authorizer == nil || atomicStore == nil {
		return nil, errors.New("inbound authorizer and store are required")
	}
	return &Service{authorizer: authorizer, store: atomicStore}, nil
}

func (service *Service) ApplyIntent(ctx context.Context, claim reportasset.IntentDeliveryClaim) (int64, error) {
	identity := store.Identity{TenantID: claim.TenantID, ActorID: claim.ActorID, DomainID: claim.DomainID}
	if identity.Validate() != nil || claim.IntentID.Validate() != nil || claim.IdempotencyKeyHash.Validate() != nil ||
		claim.ReportID != claim.Bundle.ReportID || claim.Bundle.Source != operation.SourceSystem || claim.Bundle.Validate() != nil {
		return 0, &reportasset.DeliveryFailure{Code: "REPORT_INBOUND_CONTRACT_INVALID", Retryable: false, Cause: errors.New("invalid inbound claim")}
	}
	if err := service.authorizer.AuthorizeReportEdit(ctx, identity, claim.ReportID); err != nil {
		return 0, &reportasset.DeliveryFailure{Code: "REPORT_EDIT_FORBIDDEN", Retryable: false, Cause: errors.Join(ErrUnauthorized, err)}
	}
	bindings, err := semanticBindings(claim.Bundle.Operations)
	if err != nil {
		return 0, &reportasset.DeliveryFailure{Code: "REPORT_INBOUND_BINDING_INVALID", Retryable: false, Cause: err}
	}
	for _, binding := range bindings {
		// This call must verify both semantic-object access and all underlying
		// dataset/data-context access for the actor at delivery time.
		if err := service.authorizer.AuthorizeSemanticBinding(ctx, identity, binding); err != nil {
			return 0, &reportasset.DeliveryFailure{Code: "REPORT_DATA_ACCESS_FORBIDDEN", Retryable: false, Cause: errors.Join(ErrUnauthorized, err)}
		}
	}
	result, err := service.store.ApplyInbound(ctx, identity, store.InboundInput{
		IntentID: claim.IntentID, IdempotencyKeyHash: claim.IdempotencyKeyHash, Bundle: claim.Bundle,
	})
	if err != nil {
		retryable := !errors.Is(err, store.ErrInboundConflict) && !errors.Is(err, store.ErrRevisionConflict)
		code := "REPORT_INBOUND_APPLY_FAILED"
		if errors.Is(err, store.ErrRevisionConflict) {
			code = "REPORT_REVISION_CONFLICT"
		}
		return 0, &reportasset.DeliveryFailure{Code: code, Retryable: retryable, Cause: err}
	}
	if result.RevisionNo < 1 {
		return 0, &reportasset.DeliveryFailure{Code: "REPORT_INBOUND_RESULT_INVALID", Retryable: false}
	}
	return result.RevisionNo, nil
}

func semanticBindings(operations []operation.Operation) ([]report.SemanticQueryRef, error) {
	result := []report.SemanticQueryRef{}
	appendBinding := func(binding *report.DataBinding) error {
		if binding == nil || binding.BindingMode != report.BindingSemanticIR || binding.SemanticQueryRef == nil {
			return errors.New("add-to-report accepts only SEMANTIC_IR bindings")
		}
		result = append(result, *binding.SemanticQueryRef)
		return nil
	}
	for index, item := range operations {
		var binding *report.DataBinding
		switch item.Op {
		case operation.ComponentCreate:
			binding = item.Payload.(*operation.ComponentCreatePayload).Component.DataBinding
		case operation.ComponentReplace:
			binding = item.Payload.(*operation.ComponentReplacePayload).Component.DataBinding
		case operation.DataBindingUpdate:
			payload := item.Payload.(*operation.DataBindingUpdatePayload)
			if payload.Mode != operation.DataBindingSet {
				return nil, fmt.Errorf("operations[%d] clears a binding", index)
			}
			binding = payload.DataBinding
		default:
			continue
		}
		if err := appendBinding(binding); err != nil {
			return nil, fmt.Errorf("operations[%d]: %w", index, err)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("operation bundle contains no semantic binding")
	}
	return result, nil
}

var _ reportasset.IntentApplier = (*Service)(nil)
