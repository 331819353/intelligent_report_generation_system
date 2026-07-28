package metriccandidate

import (
	"context"
	"errors"
)

const automaticApprovalBatchSize = 200

// AutomaticApprover moves rule-only DWS results through the existing atomic
// candidate acceptance boundary. Acceptance creates or updates a metric draft in
// the asset catalog and preserves the normal reviewer/audit fields.
type AutomaticApprover struct {
	source  AutomaticApprovalStore
	service *Service
}

func NewAutomaticApprover(
	source AutomaticApprovalStore,
	service *Service,
) *AutomaticApprover {
	return &AutomaticApprover{source: source, service: service}
}

func (approver *AutomaticApprover) ProcessPending(
	ctx context.Context,
	tenantID string,
) (bool, error) {
	if approver == nil || approver.source == nil || approver.service == nil ||
		tenantID == "" {
		return false, ErrInvalidRequest
	}
	items, err := approver.source.ListAutomaticApprovalCandidates(
		ctx, tenantID, automaticApprovalBatchSize,
	)
	if err != nil || len(items) == 0 {
		return false, err
	}
	processed := false
	var approvalErr error
	for _, item := range items {
		_, err := approver.service.Accept(
			ctx, tenantID, item.ActorID, item.Candidate.ID,
			AcceptInput{ExpectedVersion: item.Candidate.Version},
		)
		if err != nil {
			approvalErr = errors.Join(approvalErr, err)
			continue
		}
		processed = true
	}
	return processed, approvalErr
}
