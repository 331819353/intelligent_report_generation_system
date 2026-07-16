package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type memoryStore struct {
	record       Record
	publishCalls int
	usage        VersionUsage
	usageCalls   int
	usageTenant  string
	usageDataset string
	usageVersion string
}

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
func (s *memoryStore) ReplayPublication(context.Context, string, string, string, string, string) (VersionRecord, bool, error) {
	return VersionRecord{}, false, nil
}
func (s *memoryStore) Publish(_ context.Context, _, _, datasetID string, plan PublishPlan) (VersionRecord, error) {
	s.publishCalls++
	s.record.Version++
	return VersionRecord{ID: "22222222-2222-4222-8222-222222222222", DatasetID: datasetID, DatasetRecordVersion: s.record.Version,
		DraftVersionID: plan.DraftVersionID, DraftRecordVersion: plan.ExpectedDraftRecordVersion,
		VersionNo: 1, Status: "PUBLISHED", DSLVersion: DSLVersion, DSLHash: plan.Prepared.DSLHash, PlanHash: plan.Prepared.PlanHash,
		DSL: plan.Prepared.DSLJSON, LogicalPlan: plan.Prepared.LogicalPlanJSON}, nil
}

type memoryPublicationValidator struct {
	candidate PublicationCandidate
	result    PreviewResult
	err       error
}

func (v *memoryPublicationValidator) ValidatePublication(_ context.Context, _, _ string, candidate PublicationCandidate) (PreviewResult, error) {
	v.candidate = candidate
	return v.result, v.err
}
func (s *memoryStore) GetVersion(context.Context, string, string, string) (VersionRecord, error) {
	return VersionRecord{}, ErrVersionNotFound
}
func (s *memoryStore) ListVersions(context.Context, string, string, int, int) ([]VersionSummary, int, error) {
	return []VersionSummary{}, 0, nil
}
func (s *memoryStore) GetVersionUsage(_ context.Context, tenantID, datasetID, versionID string) (VersionUsage, error) {
	s.usageCalls++
	s.usageTenant, s.usageDataset, s.usageVersion = tenantID, datasetID, versionID
	return s.usage, nil
}
func (s *memoryStore) TransitionVersion(context.Context, string, string, string, string, VersionTransitionInput) (VersionRecord, error) {
	return VersionRecord{}, ErrVersionNotFound
}

func TestServiceLoadsExactVersionUsage(t *testing.T) {
	store := &memoryStore{usage: VersionUsage{
		ReportDraftReferences: 2, DownstreamDraftReferences: 1,
		DownstreamPublishedReferences: 3, ActiveQueryRuns: 4,
	}}
	service := NewService(store)
	usage, err := service.GetVersionUsage(context.Background(), "tenant-1", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if usage != store.usage || store.usageCalls != 1 || store.usageTenant != "tenant-1" ||
		store.usageDataset != "11111111-1111-4111-8111-111111111111" || store.usageVersion != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("usage=%#v store=%#v", usage, store)
	}
	if _, err := service.GetVersionUsage(context.Background(), "tenant-1", "invalid", "22222222-2222-4222-8222-222222222222"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("invalid identifier error=%v", err)
	}
	if store.usageCalls != 1 {
		t.Fatalf("无效标识仍访问了仓储：calls=%d", store.usageCalls)
	}
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

func TestServicePublishesExactDraftAfterValidation(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{record: Record{
		ID: "11111111-1111-4111-8111-111111111111", Version: 3,
		DraftVersionID: "33333333-3333-4333-8333-333333333333", DraftRecordVersion: 2,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
	}}
	validator := &memoryPublicationValidator{}
	service := NewService(store, validator)
	published, err := service.Publish(context.Background(), "tenant-1", "actor-1", store.record.ID, "publish-key-1", PublishInput{
		DraftVersionID: store.record.DraftVersionID, ExpectedVersion: 3, ExpectedDraftRecordVersion: 2,
		ExpectedDSLHash: prepared.DSLHash, ValidationParameters: map[string]any{"start_date": "2026-01-01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.VersionNo != 1 || published.DSLHash != prepared.DSLHash || store.publishCalls != 1 {
		t.Fatalf("published=%#v calls=%d", published, store.publishCalls)
	}
	if validator.candidate.DraftVersionID != store.record.DraftVersionID || validator.candidate.Parameters["start_date"] != "2026-01-01" {
		t.Fatalf("candidate=%#v", validator.candidate)
	}
}

func TestServiceBlocksPublicationWarningsAndStaleEnvelope(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{record: Record{
		ID: "11111111-1111-4111-8111-111111111111", Version: 3,
		DraftVersionID: "33333333-3333-4333-8333-333333333333", DraftRecordVersion: 2,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
	}}
	validator := &memoryPublicationValidator{result: PreviewResult{Warnings: []PreviewWarning{{Code: "JOIN_FANOUT_RISK"}}}}
	service := NewService(store, validator)
	input := PublishInput{DraftVersionID: store.record.DraftVersionID, ExpectedVersion: 3, ExpectedDraftRecordVersion: 2, ExpectedDSLHash: prepared.DSLHash}
	_, err = service.Publish(context.Background(), "tenant-1", "actor-1", store.record.ID, "publish-key-1", input)
	var validation *PublicationValidationError
	if !errors.As(err, &validation) || len(validation.Issues) != 1 || validation.Issues[0].Code != "JOIN_FANOUT_RISK" || store.publishCalls != 0 {
		t.Fatalf("err=%v validation=%#v calls=%d", err, validation, store.publishCalls)
	}
	input.ExpectedVersion = 2
	if _, err := service.Publish(context.Background(), "tenant-1", "actor-1", store.record.ID, "publish-key-2", input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publish error=%v", err)
	}
}

func TestValidIdempotencyKeyRejectsWhitespaceAndControlCharacters(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "普通键", value: "publish-key-1", valid: true},
		{name: "首部空格", value: " publish-key-1", valid: false},
		{name: "尾部空格", value: "publish-key-1 ", valid: false},
		{name: "中间换行", value: "publish\nkey", valid: false},
		{name: "中间制表符", value: "publish\tkey", valid: false},
		{name: "空字符串", value: "", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := validIdempotencyKey(test.value); actual != test.valid {
				t.Fatalf("validIdempotencyKey(%q)=%v, want %v", test.value, actual, test.valid)
			}
		})
	}
}
