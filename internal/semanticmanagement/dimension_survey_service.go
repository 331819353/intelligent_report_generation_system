package semanticmanagement

import (
	"context"
	"strings"
)

var dimensionSurveyStatuses = values("SUGGESTED", "ACCEPTED", "REJECTED", "STALE")
var dimensionSurveyFieldRoles = values("DIMENSION", "ATTRIBUTE", "TIME", "IDENTIFIER")

func (s *DimensionService) ListDimensionSurveyCandidates(
	ctx context.Context,
	tenantID string,
	filter DimensionSurveyFilter,
) ([]DimensionSurveyCandidate, int, error) {
	if s == nil || s.survey == nil || !validTenant(tenantID) ||
		!normalizePage(&filter.Page) {
		return nil, 0, ErrInvalidRequest
	}
	filter.DatasetID = strings.TrimSpace(filter.DatasetID)
	filter.DatasetVersionID = strings.TrimSpace(filter.DatasetVersionID)
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	filter.FieldRole = strings.ToUpper(strings.TrimSpace(filter.FieldRole))
	if (filter.DatasetID != "" && !validUUID(filter.DatasetID)) ||
		(filter.DatasetVersionID != "" && !validUUID(filter.DatasetVersionID)) ||
		!validOptionalEnum(filter.Status, dimensionSurveyStatuses) ||
		!validOptionalEnum(filter.FieldRole, dimensionSurveyFieldRoles) {
		return nil, 0, ErrInvalidRequest
	}
	return s.survey.ListDimensionSurveyCandidates(ctx, tenantID, filter)
}

func (s *DimensionService) GetDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, id string,
) (DimensionSurveyCandidate, error) {
	if s == nil || s.survey == nil || !validTenant(tenantID) || !validUUID(id) {
		return DimensionSurveyCandidate{}, ErrInvalidRequest
	}
	return s.survey.GetDimensionSurveyCandidate(ctx, tenantID, id)
}

func (s *DimensionService) UpdateDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	input UpdateDimensionSurveyCandidateInput,
) (DimensionSurveyCandidate, error) {
	if s == nil || s.survey == nil || !validActor(tenantID, actorID) ||
		!validUUID(id) || input.ExpectedVersion < 1 {
		return DimensionSurveyCandidate{}, ErrInvalidRequest
	}
	current, err := s.survey.GetDimensionSurveyCandidate(ctx, tenantID, id)
	if err != nil {
		return DimensionSurveyCandidate{}, err
	}
	if current.Status != "SUGGESTED" ||
		(current.ProposedHighCardinality && !input.HighCardinality) ||
		(current.ProposedSensitive && !input.Sensitive) ||
		indexPolicyRank(input.MemberIndexPolicy) <
			indexPolicyRank(current.ProposedMemberIndexPolicy) {
		return DimensionSurveyCandidate{}, ErrConflict
	}
	prepared, err := prepareSurveyDimension(current, input)
	if err != nil {
		return DimensionSurveyCandidate{}, err
	}
	return s.survey.UpdateDimensionSurveyCandidate(
		ctx, tenantID, actorID, id, input.ExpectedVersion, prepared,
	)
}

func (s *DimensionService) AcceptDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
) (DimensionSurveyAcceptance, error) {
	if s == nil || s.survey == nil || !validActor(tenantID, actorID) ||
		!validUUID(id) || expectedVersion < 1 {
		return DimensionSurveyAcceptance{}, ErrInvalidRequest
	}
	current, err := s.survey.GetDimensionSurveyCandidate(ctx, tenantID, id)
	if err != nil {
		return DimensionSurveyAcceptance{}, err
	}
	if current.Status != "SUGGESTED" {
		return DimensionSurveyAcceptance{}, ErrConflict
	}
	prepared, err := prepareSurveyDimension(current, UpdateDimensionSurveyCandidateInput{
		Code: current.ProposedCode, Name: current.ProposedName,
		Description:       current.ProposedDescription,
		DimensionType:     current.ProposedDimensionType,
		MemberIndexPolicy: current.ProposedMemberIndexPolicy,
		HighCardinality:   current.ProposedHighCardinality,
		Sensitive:         current.ProposedSensitive,
	})
	if err != nil {
		return DimensionSurveyAcceptance{}, err
	}
	return s.survey.AcceptDimensionSurveyCandidate(
		ctx, tenantID, actorID, id, expectedVersion, prepared,
	)
}

func (s *DimensionService) RejectDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	reason string,
) (DimensionSurveyCandidate, error) {
	reason = strings.TrimSpace(reason)
	if s == nil || s.survey == nil || !validActor(tenantID, actorID) ||
		!validUUID(id) || expectedVersion < 1 ||
		!validText(reason, 1, 2000) {
		return DimensionSurveyCandidate{}, ErrInvalidRequest
	}
	return s.survey.RejectDimensionSurveyCandidate(
		ctx, tenantID, actorID, id, expectedVersion, reason,
	)
}

func prepareSurveyDimension(
	candidate DimensionSurveyCandidate,
	input UpdateDimensionSurveyCandidateInput,
) (PreparedDimension, error) {
	normalized := normalizeDimension(CreateDimensionInput{
		DatasetID: candidate.DatasetID, DatasetVersionID: candidate.DatasetVersionID,
		FieldID: candidate.FieldID, Code: input.Code, Name: input.Name,
		Description: input.Description, DimensionType: input.DimensionType,
		MemberIndexPolicy: input.MemberIndexPolicy,
		HighCardinality:   input.HighCardinality,
		Sensitive:         input.Sensitive,
		Status:            "PUBLISHED",
	})
	if !validDimension(normalized) ||
		(candidate.RiskHighCardinality && !normalized.HighCardinality) ||
		(candidate.RiskSensitive && !normalized.Sensitive) {
		return PreparedDimension{}, ErrInvalidRequest
	}
	return prepareDimension(normalized)
}

func indexPolicyRank(value string) int {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "FULL":
		return 1
	case "EXACT_ONLY":
		return 2
	case "NONE":
		return 3
	default:
		return -1
	}
}
