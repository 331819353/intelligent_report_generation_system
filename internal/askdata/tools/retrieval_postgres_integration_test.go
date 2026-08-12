package tools

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/platform/database"
)

// This regression test can be pointed at a populated local release to verify
// the query reader, contract projection and Tool Host result closure together.
// It is opt-in because normal unit suites do not own a published release.
func TestPublishedReleaseContractsSatisfyQuestionToolContract(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	tenantID := askdata.ID(os.Getenv("ASKDATA_INTEGRATION_TENANT_ID"))
	actorID := askdata.ID(os.Getenv("ASKDATA_INTEGRATION_ACTOR_ID"))
	domainID := askdata.ID(os.Getenv("ASKDATA_INTEGRATION_DOMAIN_ID"))
	roleID := askdata.ID(os.Getenv("ASKDATA_INTEGRATION_ROLE_ID"))
	releaseID := askdata.ID(os.Getenv("ASKDATA_INTEGRATION_RELEASE_ID"))
	releaseHash := askdata.ContentHash(os.Getenv("ASKDATA_INTEGRATION_RELEASE_HASH"))
	if appURL == "" || tenantID == "" || actorID == "" || domainID == "" || roleID == "" || releaseID == "" || releaseHash == "" {
		t.Skip("set AskData integration release environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope, err := askdata.NewPolicyScope(
		tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{roleID},
		askdata.ReleaseRef{ReleaseID: releaseID, ContentHash: releaseHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx = database.WithAccessContext(ctx, string(actorID), string(domainID))
	reader := registry.NewQueryReader(pool)
	rows, err := reader.Contracts(ctx, scope, domainID, []string{
		"00134b0f-dc1a-583d-8e33-2eb97347f9b3",
		"434baa1a-ae3b-52f4-affd-1e06e4b94a33",
		"32a8761c-5da9-587d-ad5b-9b2a2da05112",
		"561151a0-0b6b-582a-9915-b57aa2e685e8",
		"8c3fb8f9-d087-5a1c-b342-ffaf35b7e4b7",
		"921109cc-4b38-5886-a93c-ad55ec4f6beb",
	})
	if err != nil {
		t.Fatal(err)
	}
	contracts := make([]toolhost.SemanticContractSummary, 0, len(rows))
	refs := make([]askdata.EvidenceRef, 0, len(rows))
	for _, row := range rows {
		summary, ok := contractSummary(row)
		if !ok {
			continue
		}
		summary.ContentHash = row.ContentHash
		contracts = append(contracts, summary)
		contentHash := askdata.HashBytes(row.Contract)
		refs = append(refs, askdata.EvidenceRef{
			EvidenceID: askdata.ID(contentHash), Kind: askdata.EvidenceKindSemanticContract,
			SourceID: summary.ObjectVersionID, ContentHash: contentHash,
		})
	}
	known := make(map[askdata.ID]askdata.EvidenceRef, len(refs))
	for _, ref := range refs {
		known[ref.EvidenceID] = ref
	}
	result := toolhost.GetSemanticContractsResult{
		Contracts: contracts, EvidenceIDs: sortedEvidenceIDs(refs),
	}
	if err := result.ValidateResult(known); err != nil {
		t.Fatalf("published contract result: %v; contracts=%+v", err, contracts)
	}

	canonical, err := reader.CanonicalMetricVersions(ctx, scope, domainID, []string{
		"561151a0-0b6b-582a-9915-b57aa2e685e8",
	})
	if err != nil || canonical["561151a0-0b6b-582a-9915-b57aa2e685e8"] != "fd437750-150d-592f-9db6-9eb42f766ccf" {
		t.Fatalf("legacy measure canonicalization = %#v, %v", canonical, err)
	}
	metricRefs, err := reader.ReleasedVersionRefs(ctx, scope, domainID, "METRIC", []string{
		"fd437750-150d-592f-9db6-9eb42f766ccf",
	})
	if err != nil || len(metricRefs) != 1 || metricRefs[0].Version != 1 || metricRefs[0].ObjectID == "" {
		t.Fatalf("metric graph refs = %#v, %v", metricRefs, err)
	}
	modelRefs, err := reader.ReleasedVersionRefs(ctx, scope, domainID, "MODEL", []string{
		"434baa1a-ae3b-52f4-affd-1e06e4b94a33",
	})
	if err != nil || len(modelRefs) != 1 || modelRefs[0].Version != 1 || modelRefs[0].ObjectID == "" {
		t.Fatalf("model graph refs = %#v, %v", modelRefs, err)
	}
	metricContracts, err := reader.Contracts(ctx, scope, domainID, []string{
		"fd437750-150d-592f-9db6-9eb42f766ccf",
	})
	if err != nil || len(metricContracts) != 1 {
		t.Fatalf("metric contracts = %#v, %v", metricContracts, err)
	}
	metricSummary, ok := contractSummary(metricContracts[0])
	if !ok || metricSummary.Name != "销售金额" || metricSummary.Definition != "销售金额" {
		t.Fatalf("enriched metric contract = %#v, mapped=%t", metricSummary, ok)
	}
}
