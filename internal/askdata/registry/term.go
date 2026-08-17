package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	TermTypeMetric    = "METRIC"
	TermTypeDimension = "DIMENSION"
	TermTypeMember    = "MEMBER"
	TermTypeTime      = "TIME"
	TermTypeOperator  = "OPERATOR"

	TermTargetMetric       = "METRIC"
	TermTargetDimension    = "DIMENSION"
	TermTargetMember       = "MEMBER"
	TermTargetTimeContract = "TIME_CONTRACT"
	TermTargetOperator     = "OPERATOR"
	TermTargetLegacy       = "LEGACY"

	TermMatchExact      = "EXACT"
	TermMatchPrefix     = "PREFIX"
	TermMatchSuffix     = "SUFFIX"
	TermMatchRegexpSafe = "REGEX_SAFE"
	TermMatchVector     = "VECTOR"

	TermSourceManual            = "MANUAL"
	TermSourceImport            = "IMPORT"
	TermSourceFeedback          = "FEEDBACK"
	TermSourceActiveLearning    = "ACTIVE_LEARNING"
	TermSourceReportAsset       = "REPORT_ASSET"
	TermSourceCertifiedExample  = "CERTIFIED_EXAMPLE"
	TermSourceFeedbackCandidate = "FEEDBACK_CANDIDATE"

	TermReviewPending  = "PENDING"
	TermReviewApproved = "APPROVED"
	TermReviewRejected = "REJECTED"
)

func applyBusinessTermDefaults(term *BusinessTerm) {
	if term.NegativeContexts == nil {
		term.NegativeContexts = []string{}
	}
	if term.ApplicableRoleIDs == nil {
		term.ApplicableRoleIDs = []string{}
	}
	if term.Aliases == nil {
		term.Aliases = []string{}
	}
	if term.Term == "" {
		term.Term = term.Code
	}
	if term.Code == "" {
		term.Code = term.Term
	}
	if term.TermType == "" {
		term.TermType = TermTypeOperator
	}
	if term.TargetObjectType == "" {
		term.TargetObjectType = TermTargetLegacy
	}
	if term.TargetVersionID == "" {
		if term.TargetObjectType == TermTargetOperator {
			term.TargetVersionID = uuid.NewSHA1(
				uuid.NameSpaceOID, []byte("askdata/operator/"+term.TargetCode),
			).String()
		} else if term.TargetObjectType == TermTargetLegacy {
			term.TargetVersionID = term.ID
		}
	}
	if term.TargetCode == "" {
		term.TargetCode = term.Code
	}
	if term.MatchMode == "" {
		term.MatchMode = TermMatchExact
	}
	if term.Priority == 0 {
		term.Priority = 100
	}
	if term.Source == "" {
		term.Source = TermSourceManual
	}
	term.ReviewStatus = TermReviewPending
	term.ReviewedBy, term.ReviewedAt = "", nil
	if term.MatchMode == TermMatchRegexpSafe && term.MatchPattern == "" {
		term.MatchPattern = term.Term
	}
}

func validateGovernedBusinessTerm(validation *validator, term BusinessTerm) {
	if strings.TrimSpace(term.Term) != term.Term || len(term.Term) < 1 || len(term.Term) > 512 {
		validation.add(validationCodeRequired, "term", "must contain 1 to 512 trimmed characters")
	}
	if !oneOf(term.TermType, TermTypeMetric, TermTypeDimension, TermTypeMember, TermTypeTime, TermTypeOperator) {
		validation.add(validationCodeInvalidEnum, "termType", "unsupported governed term type")
	}
	if !oneOf(term.TargetObjectType, TermTargetMetric, TermTargetDimension, TermTargetMember,
		TermTargetTimeContract, TermTargetOperator, TermTargetLegacy) {
		validation.add(validationCodeInvalidEnum, "targetObjectType", "unsupported governed term target")
	}
	validateUUID(validation, "targetVersionId", term.TargetVersionID, true)
	if strings.TrimSpace(term.TargetCode) != term.TargetCode || len(term.TargetCode) > 512 {
		validation.add(validationCodeRequired, "targetCode", "must be trimmed and no longer than 512 characters")
	}
	if !oneOf(term.MatchMode, TermMatchExact, TermMatchPrefix, TermMatchSuffix,
		TermMatchRegexpSafe, TermMatchVector) {
		validation.add(validationCodeInvalidEnum, "matchMode", "unsupported governed match mode")
	}
	if term.MatchMode == TermMatchRegexpSafe {
		if _, err := CompileSafeTermRegexp(term.MatchPattern); err != nil {
			validation.add(validationCodeInvalidDependency, "matchPattern", ErrTermRegexUnsafe.Error())
		}
	} else if term.MatchPattern != "" {
		validation.add(validationCodeInvalidDependency, "matchPattern", "only REGEX_SAFE accepts a match pattern")
	}
	if term.Priority < 0 || term.Priority > 1000 {
		validation.add(validationCodeInvalidDependency, "priority", "must be between 0 and 1000")
	}
	if len(term.NegativeContexts) > 64 {
		validation.add(validationCodeInvalidDependency, "negativeContexts", "cannot contain more than 64 values")
	}
	seenNegatives := map[string]struct{}{}
	for index, negative := range term.NegativeContexts {
		normalized := strings.ToLower(strings.TrimSpace(negative))
		if normalized == "" || strings.Contains(strings.ToLower(term.Term), normalized) ||
			strings.Contains(normalized, strings.ToLower(term.Term)) {
			validation.add(validationCodeInvalidDependency, fmt.Sprintf("negativeContexts[%d]", index),
				"TERM_NEGATIVE_CONTEXT_CONTRADICTION")
		}
		if _, duplicate := seenNegatives[normalized]; duplicate {
			validation.add(validationCodeDuplicate, fmt.Sprintf("negativeContexts[%d]", index),
				"negative context is duplicated after normalization")
		}
		seenNegatives[normalized] = struct{}{}
	}
	if len(term.ApplicableRoleIDs) > 64 {
		validation.add(validationCodeInvalidDependency, "applicableRoleIds", "cannot contain more than 64 roles")
	}
	seenRoles := map[string]struct{}{}
	for index, roleID := range term.ApplicableRoleIDs {
		validateUUID(validation, fmt.Sprintf("applicableRoleIds[%d]", index), roleID, true)
		if _, duplicate := seenRoles[roleID]; duplicate {
			validation.add(validationCodeDuplicate, fmt.Sprintf("applicableRoleIds[%d]", index), "role is duplicated")
		}
		seenRoles[roleID] = struct{}{}
	}
	if term.ValidFrom != nil && term.ValidTo != nil && !term.ValidTo.After(*term.ValidFrom) {
		validation.add(validationCodeInvalidDependency, "validTo", "must be after validFrom")
	}
	if !oneOf(term.Source, TermSourceManual, TermSourceImport, TermSourceFeedback,
		TermSourceActiveLearning, TermSourceReportAsset, TermSourceCertifiedExample,
		TermSourceFeedbackCandidate) {
		validation.add(validationCodeInvalidEnum, "source", "unsupported governed term source")
	}
	if term.ReviewStatus != TermReviewPending || term.ReviewedBy != "" || term.ReviewedAt != nil {
		validation.add(validationCodeInvalidDependency, "reviewStatus", "new and edited drafts must remain PENDING")
	}
}

func validateBusinessTermReferencesTx(ctx context.Context, tx pgx.Tx, term BusinessTerm) error {
	var targetValid bool
	if err := tx.QueryRow(ctx, `SELECT CASE $1::text
		WHEN 'METRIC' THEN EXISTS(
			SELECT 1 FROM askdata.metric_versions
			WHERE id=$2 AND domain_id=$3 AND tenant_id=$4 AND status='CERTIFIED'
		)
		WHEN 'DIMENSION' THEN EXISTS(
			SELECT 1 FROM askdata.dimensions
			WHERE id=$2 AND domain_id=$3 AND tenant_id=$4 AND status='CERTIFIED'
		)
		WHEN 'MEMBER' THEN EXISTS(
			SELECT 1 FROM askdata.dimension_members
			WHERE id=$2 AND domain_id=$3 AND tenant_id=$4 AND status='CERTIFIED'
		)
		WHEN 'TIME_CONTRACT' THEN EXISTS(
			SELECT 1 FROM askdata.time_contract_versions
			WHERE id=$2 AND domain_id=$3 AND tenant_id=$4 AND status='CERTIFIED'
		)
		WHEN 'OPERATOR' THEN true
		WHEN 'LEGACY' THEN true
		WHEN 'CONCEPT' THEN true
		ELSE false END`, term.TargetObjectType, term.TargetVersionID,
		term.DomainID, term.TenantID).Scan(&targetValid); err != nil {
		return err
	}
	if !targetValid {
		return fmt.Errorf("%w: TERM_TARGET_INVALID: target must be certified in the same domain",
			ErrRegistryInvalidRequest)
	}
	if len(term.ApplicableRoleIDs) == 0 {
		return nil
	}
	var validRoleCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.roles
		WHERE tenant_id=$1 AND id=ANY($2::uuid[])
		  AND status='ACTIVE' AND deleted_at IS NULL`,
		term.TenantID, term.ApplicableRoleIDs).Scan(&validRoleCount); err != nil {
		return err
	}
	if validRoleCount != len(term.ApplicableRoleIDs) {
		return fmt.Errorf("%w: TERM_ROLE_INVALID: applicable roles must be active in the same tenant",
			ErrRegistryInvalidRequest)
	}
	return nil
}
