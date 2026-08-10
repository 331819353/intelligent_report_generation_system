package datarequest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestClusterRequestsIsOrderInsensitiveAndTriggersAtThirdRequest(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	tenantID, domainID := uuid.NewString(), uuid.NewString()
	metricA, metricB, dimension := uuid.NewString(), uuid.NewString(), uuid.NewString()
	observations := []ClusterObservation{
		clusterObservation(tenantID, domainID, uuid.NewString(), []string{metricA, metricB}, []string{dimension}, "month", "经营复盘", now.Add(-20*24*time.Hour)),
		clusterObservation(tenantID, domainID, uuid.NewString(), []string{metricB, metricA}, []string{dimension}, "MONTH", "临时分析", now.Add(-10*24*time.Hour)),
		clusterObservation(tenantID, domainID, uuid.NewString(), []string{metricA, metricB}, []string{dimension}, "month", "经营复盘", now.Add(-time.Hour)),
	}
	candidates, err := ClusterRequests(now, observations[:2], nil)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("two requests candidates=%#v err=%v", candidates, err)
	}
	candidates, err = ClusterRequests(now, observations, nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("three requests candidates=%#v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if candidate.RequestCount != 3 || candidate.DistinctRequesterCount != 3 ||
		len(candidate.TypicalPurposes) != 2 || candidate.TypicalPurposes[0] != "经营复盘" {
		t.Fatalf("candidate=%#v", candidate)
	}
	existing := map[askdata.ContentHash]struct{}{candidate.KeyHash: {}}
	if repeated, err := ClusterRequests(now, observations, existing); err != nil || len(repeated) != 0 {
		t.Fatalf("repeated candidates=%#v err=%v", repeated, err)
	}
}

func TestClusterRequestsExcludesWindowBoundaryPredecessor(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	tenantID, domainID, requesterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	metricID := uuid.NewString()
	observations := []ClusterObservation{
		clusterObservation(tenantID, domainID, requesterID, []string{metricID}, nil, "DAY", "A", now.Add(-ClusterWindow-time.Nanosecond)),
		clusterObservation(tenantID, domainID, requesterID, []string{metricID}, nil, "DAY", "A", now.Add(-2*time.Hour)),
		clusterObservation(tenantID, domainID, requesterID, []string{metricID}, nil, "DAY", "A", now.Add(-time.Hour)),
	}
	candidates, err := ClusterRequests(now, observations, nil)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("outside window counted: %#v err=%v", candidates, err)
	}
}

func clusterObservation(
	tenantID, domainID, requesterID string,
	metrics, dimensions []string,
	grain, purpose string,
	createdAt time.Time,
) ClusterObservation {
	return ClusterObservation{
		RequestID: uuid.NewString(), TenantID: tenantID, DomainID: domainID,
		RequesterUserID: requesterID, MetricIDs: metrics, DimensionIDs: dimensions,
		Grain: grain, BusinessPurpose: purpose, CreatedAt: createdAt,
	}
}
