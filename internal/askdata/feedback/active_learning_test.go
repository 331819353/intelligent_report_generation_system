package feedback

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type learningSource struct{ calls []LearningTask }

func (source *learningSource) TenantDomains(context.Context) ([][2]string, error) {
	return [][2]string{{"tenant", "domain"}}, nil
}
func (source *learningSource) Mine(_ context.Context, tenant, domain string, task LearningTask, _ int) ([]LearningSignal, error) {
	source.calls = append(source.calls, task)
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	return []LearningSignal{{TenantID: askdata.ID(tenant), DomainID: askdata.ID(domain), Task: task, KeyHash: askdata.HashBytes([]byte(task)), Summary: json.RawMessage(`{"objectIds":["stable-id"]}`), Evidence: json.RawMessage(`{"count":1}`), OccurrenceCount: 1, FirstSeenAt: now, LastSeenAt: now}}, nil
}

type learningStore struct {
	items      []Candidate
	rejectedAt *time.Time
}

func (store *learningStore) UpsertSignal(_ context.Context, signal LearningSignal, now time.Time) (Candidate, error) {
	suppressed := store.rejectedAt != nil && store.rejectedAt.After(now.Add(-90*24*time.Hour))
	candidate := Candidate{Task: signal.Task, Type: candidateTypeForTask[signal.Task], State: "DRAFT", ReviewStatus: "PENDING", Suppressed: suppressed}
	if !suppressed {
		store.items = append(store.items, candidate)
	}
	return candidate, nil
}

func TestActiveLearningRunsAllEightTasksAsDraftPending(t *testing.T) {
	source, store := &learningSource{}, &learningStore{}
	worker, err := NewActiveLearningWorker(source, store, 10)
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.ProcessDomain(context.Background(), "tenant", "domain", time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if count != 8 || len(source.calls) != 8 || len(store.items) != 8 {
		t.Fatalf("count=%d calls=%d items=%d", count, len(source.calls), len(store.items))
	}
	for _, candidate := range store.items {
		if candidate.State != "DRAFT" || candidate.ReviewStatus != "PENDING" || candidate.Type == "" {
			t.Fatalf("candidate=%+v", candidate)
		}
	}
}

func TestDataRequestClusterQueryIsWindowedAndThresholded(t *testing.T) {
	query := learningQuery(TaskDataRequestCluster)
	for _, required := range []string{
		"interval '30 days'", "HAVING count(*)>=3",
		"count(DISTINCT requester_user_id)", "purpose_rank<=5",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("data request query missing %q", required)
		}
	}
}

func TestActiveLearningSuppressesRecentRejection(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	rejected := now.Add(-89 * 24 * time.Hour)
	source, store := &learningSource{}, &learningStore{rejectedAt: &rejected}
	worker, _ := NewActiveLearningWorker(source, store, 10)
	count, err := worker.ProcessDomain(context.Background(), "tenant", "domain", now)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(store.items) != 0 {
		t.Fatalf("recent rejection repeated: count=%d", count)
	}
}

func TestSensitiveMemberRawValueIsRejected(t *testing.T) {
	now := time.Now().UTC()
	signal := LearningSignal{TenantID: "tenant", DomainID: "domain", Task: TaskConfusableMember, KeyHash: askdata.HashBytes([]byte("member")), Summary: json.RawMessage(`{"rawMemberValue":"secret customer"}`), Evidence: json.RawMessage(`{"count":1}`), OccurrenceCount: 1, FirstSeenAt: now, LastSeenAt: now}
	if signal.Validate() == nil {
		t.Fatal("raw member value accepted")
	}
}
