package registry

import (
	"testing"

	"github.com/google/uuid"
)

func TestGeneratedKnowledgeContractsCanEnterRelease(t *testing.T) {
	tenantID, domainID, ownerID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	compatibility := MetricDimension{
		VersionIdentity: VersionIdentity{
			ID: uuid.NewString(), ObjectID: uuid.NewString(), VersionNo: 1,
			TenantID: tenantID, DomainID: domainID, OwnerID: ownerID,
			Status: VersionStatusCertified,
		},
		MetricVersionID: uuid.NewString(), DimensionVersionID: uuid.NewString(),
		Compatible: true, Role: "GROUP_BY",
	}
	compatibility.ContentHash = metricDimensionContentHash(compatibility)
	if _, err := MetricDimensionReleaseObject(compatibility); err != nil {
		t.Fatalf("metric dimension release object: %v", err)
	}

	term := BusinessTerm{
		VersionIdentity: VersionIdentity{
			ID: uuid.NewString(), ObjectID: uuid.NewString(), VersionNo: 1,
			TenantID: tenantID, DomainID: domainID, OwnerID: ownerID,
			Status: VersionStatusCertified,
		},
		Term: "销售金额", TermType: "METRIC", TargetObjectType: "METRIC",
		TargetVersionID: uuid.NewString(), TargetCode: "sales_amount",
		MatchMode: "EXACT", Priority: 100, NegativeContexts: []string{},
		ApplicableRoleIDs: []string{}, Source: "MANUAL", ReviewStatus: "APPROVED", ReviewedBy: ownerID,
		Code: "sales_amount_term",
		Name: "销售金额业务术语", Definition: "销售金额的业务口径。", Aliases: []string{"sales_amount"},
	}
	term.ContentHash = businessTermContentHash(term)
	if _, err := BusinessTermReleaseObject(term); err != nil {
		t.Fatalf("business term release object: %v", err)
	}
}
