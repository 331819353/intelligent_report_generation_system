package semanticgraph

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestNebulaProjectionAndBoundedPathIntegration(t *testing.T) {
	if os.Getenv("NEBULA_INTEGRATION") != "1" {
		t.Skip("set NEBULA_INTEGRATION=1 to run against the local NebulaGraph")
	}
	client, err := NewNebulaClient(NebulaConfig{
		Addresses: []string{"127.0.0.1:9669"}, Username: "root", Password: "nebula",
		Space: "smart_query_dev", Timeout: 5 * time.Second, IdleTimeout: time.Minute,
		MinimumPoolSize: 1, MaximumPoolSize: 4,
	})
	if err != nil {
		t.Fatalf("connect to NebulaGraph: %v", err)
	}
	defer client.Close()

	validFrom := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	manifest := ReleaseManifest{
		TenantID:        "11111111-1111-4111-8111-111111111111",
		ReleaseID:       "22222222-2222-4222-8222-222222222222",
		SemanticVersion: "integration-2026-08-03.1", ContentHash: strings64("integration-manifest"),
		Objects: []ReleaseObject{
			{ObjectType: "DATASET", ObjectID: "integration_fact", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("fact"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{"title":"Integration Fact"}`)},
			{ObjectType: "DATASET", ObjectID: "integration_dimension", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("dimension"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{"title":"Integration Dimension"}`)},
			{ObjectType: "METRIC", ObjectID: "integration_gmv", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("metric"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{
					"title":"Integration GMV","sourceDatasetIds":["integration_fact"],
					"groupableDimensionIds":["integration_region"],
					"permissionPolicyIds":["integration_analyst"]
				}`)},
			{ObjectType: "DIMENSION", ObjectID: "integration_region", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("region"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{"title":"Integration Region"}`)},
			{ObjectType: "DIMENSION_VALUE", ObjectID: "integration_north", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("north"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{
					"title":"Integration North","dimensionId":"integration_region",
					"hierarchyLevel":"region"
				}`)},
			{ObjectType: "POLICY", ObjectID: "integration_analyst", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("policy"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{
					"title":"Integration Analyst","accessibleObjectIds":["integration_gmv"]
				}`)},
			{ObjectType: "RELATION", ObjectID: "integration_join", ObjectVersion: "1",
				DomainID: "integration", ContentHash: strings64("join"), Certification: "CERTIFIED",
				Sensitivity: "INTERNAL", ValidFrom: validFrom,
				Contract: json.RawMessage(`{
					"title":"Integration Join","relationType":"joins_to",
					"fromId":"integration_fact","fromType":"DATASET",
					"toId":"integration_dimension","toType":"DATASET",
					"certified":true,"allowedForQuery":true,
					"cardinality":"many_to_one","baseCost":1,"fanoutPolicy":"SAFE"
				}`)},
		},
	}
	verification, err := NewProjector(client).Project(context.Background(), manifest)
	if err != nil {
		t.Fatalf("project integration graph: %v", err)
	}
	if verification.VertexCount != 6 || verification.EdgeCount != 5 || verification.OrphanCount != 0 {
		t.Fatalf("unexpected projection verification: %#v", verification)
	}
	factVID := StableVID(manifest.TenantID, "dataset", "integration_fact", "1")
	dimensionVID := StableVID(manifest.TenantID, "dataset", "integration_dimension", "1")
	metricVID := StableVID(manifest.TenantID, "metric", "integration_gmv", "1")
	regionVID := StableVID(manifest.TenantID, "dimension", "integration_region", "1")
	northVID := StableVID(manifest.TenantID, "dimension_value", "integration_north", "1")
	roleVID := StableVID(manifest.TenantID, "role", "integration_analyst", "1")
	runtime := NewRuntime(client)
	scope := Scope{TenantID: manifest.TenantID, SemanticVersion: manifest.SemanticVersion,
		ContentHash: manifest.ContentHash, EffectiveAt: time.Now().UTC()}

	candidates, _, err := runtime.ExpandCandidates(context.Background(), scope, []string{metricVID})
	if err != nil || !hasCandidate(candidates, factVID) || !hasCandidate(candidates, regionVID) {
		t.Fatalf("candidate extension failed: candidates=%#v error=%v", candidates, err)
	}
	ownership, _, err := runtime.ValidateValueOwnership(context.Background(), scope, ValueOwnershipRequest{
		DimensionVID: regionVID, ValueVID: northVID,
	})
	if err != nil || !ownership.Certified {
		t.Fatalf("value ownership failed: ownership=%#v error=%v", ownership, err)
	}
	bundle, _, err := runtime.ValidateBundle(context.Background(), scope, Bundle{
		MetricVIDs: []string{metricVID}, DimensionVIDs: []string{regionVID},
		Values: []ValueBinding{{DimensionVID: regionVID, ValueVID: northVID}},
	})
	if err != nil || !bundle.Valid {
		t.Fatalf("bundle validation failed: validation=%#v error=%v", bundle, err)
	}
	paths, evidence, err := NewRuntime(client).FindJoinPaths(
		context.Background(),
		scope,
		JoinPathRequest{FactDatasetVID: factVID, DimensionDatasetVID: dimensionVID, MaxHops: 1, Limit: 5},
	)
	if err != nil || len(paths) != 1 || paths[0].Edges[0].RelationID != "integration_join" {
		t.Fatalf("bounded path query failed: paths=%#v evidence=%#v error=%v", paths, evidence, err)
	}
	authorized, _, err := runtime.FilterAuthorized(context.Background(), scope, AuthorizationRequest{
		RoleVIDs: []string{roleVID}, CandidateVIDs: []string{metricVID, regionVID},
	})
	if err != nil || len(authorized) != 1 || authorized[0] != metricVID {
		t.Fatalf("permission propagation failed: authorized=%#v error=%v", authorized, err)
	}
	impacted, _, err := runtime.ImpactAnalysis(context.Background(), scope, ImpactRequest{
		ChangedVIDs: []string{factVID}, MaxHops: 2, Limit: 20,
	})
	if err != nil || !hasImpact(impacted, metricVID) {
		t.Fatalf("impact analysis failed: impacted=%#v error=%v", impacted, err)
	}
}

func hasCandidate(items []Candidate, vid string) bool {
	for _, item := range items {
		if item.VID == vid {
			return true
		}
	}
	return false
}

func hasImpact(items []ImpactedObject, vid string) bool {
	for _, item := range items {
		if item.VID == vid {
			return true
		}
	}
	return false
}
