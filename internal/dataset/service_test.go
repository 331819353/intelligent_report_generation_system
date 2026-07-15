package dataset

import (
	"context"
	"encoding/json"
	"testing"
)

type memoryStore struct{ record Record }

func (s *memoryStore) Create(_ context.Context, _, _ string, input CreateInput, prepared Prepared) (Record, error) {
	s.record = Record{ID: "dataset-1", Code: input.Code, Name: input.Name, Description: input.Description, Type: input.Type, Status: "DRAFT", Version: 1, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash}
	return s.record, nil
}
func (s *memoryStore) Get(_ context.Context, _, _ string) (Record, error) { return s.record, nil }
func (s *memoryStore) List(_ context.Context, _ string, _, _ int) ([]Summary, int, error) {
	items := []Summary{{ID: s.record.ID, Code: s.record.Code, Name: s.record.Name}}
	return items, len(items), nil
}
func (s *memoryStore) Update(_ context.Context, _, _, _ string, input UpdateInput, prepared Prepared) (Record, error) {
	if input.ExpectedVersion != s.record.Version {
		return Record{}, ErrConflict
	}
	s.record.Name, s.record.Description, s.record.Version = input.Name, input.Description, s.record.Version+1
	s.record.DSL, s.record.LogicalPlan, s.record.DSLHash, s.record.PlanHash = prepared.DSLJSON, prepared.LogicalPlanJSON, prepared.DSLHash, prepared.PlanHash
	return s.record, nil
}

func TestServiceCreateAndUpdate(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store)
	raw := readExample(t)
	created, err := service.Create(context.Background(), "tenant-1", "user-1", CreateInput{Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额", Type: "single_source", DSL: raw})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Version != 1 || created.DSLHash == "" || created.PlanHash == "" {
		t.Fatalf("created record = %#v", created)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["dataset"].(map[string]any)["name"] = "月度有效订单数据集"
	document["dataset"].(map[string]any)["description"] = "新说明"
	updatedDSL, _ := json.Marshal(document)
	updated, err := service.Update(context.Background(), "tenant-1", "user-1", created.ID, UpdateInput{Name: "月度有效订单数据集", Description: "新说明", ExpectedVersion: 1, DSL: updatedDSL})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Version != 2 || updated.Name != "月度有效订单数据集" {
		t.Fatalf("updated record = %#v", updated)
	}
}

func TestServiceRejectsMetadataDrift(t *testing.T) {
	service := NewService(&memoryStore{})
	_, err := service.Create(context.Background(), "tenant-1", "user-1", CreateInput{Code: "another_code", Name: "月度订单数据集", Type: "SINGLE_SOURCE", DSL: readExample(t)})
	if err == nil {
		t.Fatal("Create() accepted metadata that differs from the DSL")
	}
}
