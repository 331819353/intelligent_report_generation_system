// Package reportasset implements the fail-closed Report V2 to AskData asset
// boundary. Report assets are retrieval priors only; the current semantic
// release and viewer policy remain authoritative during binding.
package reportasset

import (
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/report"
)

type RejectionCode string

const (
	CodeSemanticBindingRequired RejectionCode = "REPORT_ASSET_SEMANTIC_BINDING_REQUIRED"
	CodeObjectNotCertified      RejectionCode = "REPORT_ASSET_OBJECT_NOT_CERTIFIED"
	CodeVersionNotPublished     RejectionCode = "REPORT_ASSET_VERSION_NOT_PUBLISHED"
	CodeApprovalRequired        RejectionCode = "REPORT_ASSET_APPROVAL_REQUIRED"
	CodeFreeTextUncertified     RejectionCode = "REPORT_ASSET_FREE_TEXT_UNCERTIFIED"
	CodeContractInvalid         RejectionCode = "REPORT_ASSET_CONTRACT_INVALID"
)

type ObjectKind string

const (
	ObjectMetric    ObjectKind = "METRIC"
	ObjectDimension ObjectKind = "DIMENSION"
	ObjectMember    ObjectKind = "MEMBER"
)

type ObjectCertification struct {
	Kind      ObjectKind
	VersionID askdata.ID
	Status    registry.VersionStatus
}

type Approval struct {
	ApproverUserID askdata.ID
	Role           string
	ContentHash    askdata.ContentHash
}

type Candidate struct {
	ID, TenantID, DomainID, ReportID, ReportVersionID askdata.ID
	PageID, SectionID, BlockID, ComponentID           askdata.ID
	BindingMode                                       report.BindingMode
	SemanticIR                                        ircontract.SemanticIR
	SemanticIRHash                                    askdata.ContentHash
	QueryPlanHash                                     askdata.ContentHash
	ReportPublished, ReportVersionImmutable           bool
	Certifications                                    []ObjectCertification
	Approvals                                         []Approval
	ComponentContentHash                              askdata.ContentHash
	ContainsUncertifiedFreeText                       bool
	ReportTitle, ReportDescription                    string
	SectionPurpose, BlockTitle                        string
	ComponentType, ComponentVersion                   string
	NarrativeRole                                     string
	Sensitivity                                       registry.Sensitivity
}

type Rejection struct {
	Code   RejectionCode `json:"code"`
	Detail string        `json:"detail,omitempty"`
}

type Validation struct {
	Eligible   bool        `json:"eligible"`
	Rejections []Rejection `json:"rejections"`
}

func Validate(candidate Candidate) Validation {
	// DATASET_FIELD is a hard short-circuit: callers must not even construct a
	// search document or graph mutation for it.
	if candidate.BindingMode != report.BindingSemanticIR {
		return rejected(CodeSemanticBindingRequired, "component is not bound through SEMANTIC_IR")
	}
	result := Validation{Eligible: true, Rejections: []Rejection{}}
	if err := validateContract(candidate); err != nil {
		result.Rejections = append(result.Rejections, Rejection{Code: CodeContractInvalid, Detail: err.Error()})
	}
	if missing := uncertifiedReferences(candidate); len(missing) > 0 {
		result.Rejections = append(result.Rejections, Rejection{
			Code: CodeObjectNotCertified, Detail: fmt.Sprintf("uncertified or missing objects: %v", missing),
		})
	}
	if !candidate.ReportPublished || !candidate.ReportVersionImmutable {
		result.Rejections = append(result.Rejections, Rejection{Code: CodeVersionNotPublished})
	}
	if !hasCurrentApproval(candidate) {
		result.Rejections = append(result.Rejections, Rejection{Code: CodeApprovalRequired})
	}
	if candidate.ContainsUncertifiedFreeText {
		result.Rejections = append(result.Rejections, Rejection{Code: CodeFreeTextUncertified})
	}
	result.Eligible = len(result.Rejections) == 0
	return result
}

func rejected(code RejectionCode, detail string) Validation {
	return Validation{Eligible: false, Rejections: []Rejection{{Code: code, Detail: detail}}}
}

func validateContract(candidate Candidate) error {
	for name, id := range map[string]askdata.ID{
		"id": candidate.ID, "tenantId": candidate.TenantID, "domainId": candidate.DomainID,
		"reportId": candidate.ReportID, "reportVersionId": candidate.ReportVersionID,
		"componentId": candidate.ComponentID,
	} {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if candidate.ComponentContentHash.Validate() != nil || candidate.SemanticIRHash.Validate() != nil ||
		candidate.QueryPlanHash.Validate() != nil {
		return errors.New("content, semantic IR, or query plan hash is invalid")
	}
	normalized, _, hash, err := ircontract.Canonicalize(candidate.SemanticIR)
	if err != nil || hash != candidate.SemanticIRHash || normalized.SemanticReleaseID != candidate.SemanticIR.SemanticReleaseID ||
		normalized.DomainID != candidate.DomainID {
		return errors.New("semantic IR is invalid, noncanonical, cross-domain, or hash-mismatched")
	}
	if candidate.ComponentType == "" || candidate.ComponentVersion == "" {
		return errors.New("component type and version are required")
	}
	return nil
}

func referencedObjects(candidate Candidate) map[string]ObjectCertification {
	result := map[string]ObjectCertification{}
	for _, item := range candidate.Certifications {
		result[string(item.Kind)+"\x00"+string(item.VersionID)] = item
	}
	return result
}

func uncertifiedReferences(candidate Candidate) []string {
	available := referencedObjects(candidate)
	required := map[string]struct{}{}
	add := func(kind ObjectKind, id askdata.ID) {
		required[string(kind)+"\x00"+string(id)] = struct{}{}
	}
	for _, metric := range candidate.SemanticIR.Metrics {
		add(ObjectMetric, metric.MetricVersionID)
	}
	for _, group := range candidate.SemanticIR.GroupBy {
		add(ObjectDimension, group.DimensionVersionID)
	}
	for _, filter := range candidate.SemanticIR.Filters {
		add(ObjectDimension, filter.DimensionVersionID)
		for _, memberID := range filter.MemberVersionIDs {
			add(ObjectMember, memberID)
		}
	}
	if candidate.SemanticIR.TimeRange != nil {
		add(ObjectDimension, candidate.SemanticIR.TimeRange.DimensionVersionID)
	}
	missing := []string{}
	for key := range required {
		item, ok := available[key]
		if !ok || item.Status != registry.VersionStatusCertified || item.VersionID.Validate() != nil {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func hasCurrentApproval(candidate Candidate) bool {
	for _, approval := range candidate.Approvals {
		if approval.ApproverUserID.Validate() == nil && approval.ContentHash == candidate.ComponentContentHash &&
			(approval.Role == "REPORT_OWNER" || approval.Role == "SEMANTIC_OWNER") {
			return true
		}
	}
	return false
}
