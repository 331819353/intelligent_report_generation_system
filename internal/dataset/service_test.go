package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type memoryStore struct {
	record                     Record
	revisions                  []RevisionRecord
	disabledFromStatus         string
	disabledPublishedVersionID string
	publishCalls               int
	usage                      VersionUsage
	usageCalls                 int
	usageTenant                string
	usageDataset               string
	usageVersion               string
	sourceRevision             RevisionRecord
	sourceRevisionErr          error
	resolveSourceCalls         int
	resolveSourceTenant        string
	resolveSourceDataset       string
	resolveSourceVersion       string
	rollbackCalls              int
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
	s.record.Name, s.record.Description, s.record.Type, s.record.Version = input.Name, input.Description, prepared.Document.Dataset.Type, s.record.Version+1
	s.record.DSL, s.record.LogicalPlan, s.record.DSLHash, s.record.PlanHash = prepared.DSLJSON, prepared.LogicalPlanJSON, prepared.DSLHash, prepared.PlanHash
	return s.record, nil
}
func (s *memoryStore) Disable(_ context.Context, _, _, _ string, input LifecycleInput) (Record, error) {
	if input.ExpectedVersion != s.record.Version {
		return Record{}, ErrConflict
	}
	if s.record.Status != "DRAFT" && s.record.Status != "PUBLISHED" && s.record.Status != "STALE" {
		return Record{}, ErrInvalidTransition
	}
	s.disabledFromStatus, s.disabledPublishedVersionID = s.record.Status, s.record.CurrentPublishedVersionID
	s.record.Status, s.record.CurrentPublishedVersionID, s.record.Version = "DISABLED", "", s.record.Version+1
	return s.record, nil
}
func (s *memoryStore) Restore(_ context.Context, _, _, _ string, input LifecycleInput) (Record, error) {
	if input.ExpectedVersion != s.record.Version {
		return Record{}, ErrConflict
	}
	if s.record.Status != "DISABLED" {
		return Record{}, ErrInvalidTransition
	}
	targetStatus := s.disabledFromStatus
	if targetStatus == "" {
		targetStatus = "DRAFT"
	}
	s.record.Status, s.record.Version = targetStatus, s.record.Version+1
	if targetStatus == "PUBLISHED" {
		s.record.CurrentPublishedVersionID = s.disabledPublishedVersionID
	}
	s.disabledFromStatus, s.disabledPublishedVersionID = "", ""
	return s.record, nil
}
func (s *memoryStore) Delete(_ context.Context, _, _, _ string, input LifecycleInput) error {
	if input.ExpectedVersion != s.record.Version {
		return ErrConflict
	}
	s.record = Record{}
	return nil
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
func (s *memoryStore) ResolveVersionSourceRevision(_ context.Context, tenantID, datasetID, versionID string) (RevisionRecord, error) {
	s.resolveSourceCalls++
	s.resolveSourceTenant, s.resolveSourceDataset, s.resolveSourceVersion = tenantID, datasetID, versionID
	return s.sourceRevision, s.sourceRevisionErr
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
func (s *memoryStore) GetRevision(_ context.Context, _, datasetID, revisionID string) (RevisionRecord, error) {
	for _, revision := range s.revisions {
		if revision.DatasetID == datasetID && revision.ID == revisionID {
			return revision, nil
		}
	}
	return RevisionRecord{}, ErrRevisionNotFound
}
func (s *memoryStore) ListRevisions(_ context.Context, _, datasetID string, limit, offset int) ([]RevisionSummary, int, error) {
	items := []RevisionSummary{}
	for _, revision := range s.revisions {
		if revision.DatasetID == datasetID {
			items = append(items, revision.RevisionSummary)
		}
	}
	total := len(items)
	if offset >= total {
		return []RevisionSummary{}, total, nil
	}
	items = items[offset:]
	if len(items) > limit {
		items = items[:limit]
	}
	return items, total, nil
}
func (s *memoryStore) RollbackRevision(_ context.Context, _, _ string, datasetID string, input RollbackRevisionInput, revision RevisionRecord, prepared Prepared) (Record, error) {
	s.rollbackCalls++
	if datasetID != s.record.ID {
		return Record{}, ErrNotFound
	}
	if input.ExpectedVersion != s.record.Version {
		return Record{}, ErrConflict
	}
	s.record.Version++
	s.record.DraftRecordVersion++
	s.record.Name = prepared.Document.Dataset.Name
	s.record.Description = prepared.Document.Dataset.Description
	s.record.Type = prepared.Document.Dataset.Type
	s.record.DSL = prepared.DSLJSON
	s.record.LogicalPlan = prepared.LogicalPlanJSON
	s.record.DSLHash = prepared.DSLHash
	s.record.PlanHash = prepared.PlanHash
	s.revisions = append([]RevisionRecord{{RevisionSummary: RevisionSummary{
		ID: "44444444-4444-4444-8444-444444444444", DatasetID: datasetID, VersionNo: s.record.Version,
		OperationType: "ROLLBACK", SourceRevisionID: revision.ID, Name: s.record.Name,
		Description: s.record.Description, Type: s.record.Type, DraftVersionID: s.record.DraftVersionID,
		DraftRecordVersion: s.record.DraftRecordVersion, DSLVersion: DSLVersion,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}}, s.revisions...)
	return s.record, nil
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

func TestServiceListsLoadsAndRollsBackDraftRevisions(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	const datasetID = "11111111-1111-4111-8111-111111111111"
	const revisionID = "55555555-5555-4555-8555-555555555555"
	store := &memoryStore{record: Record{
		ID: datasetID, Code: prepared.Document.Dataset.Code, Name: "当前名称", Description: "当前说明",
		Type: "SINGLE_SOURCE", Status: "PUBLISHED", Version: 7,
		DraftVersionID: "33333333-3333-4333-8333-333333333333", DraftRecordVersion: 4,
		CurrentPublishedVersionID: "22222222-2222-4222-8222-222222222222",
	}, revisions: []RevisionRecord{{RevisionSummary: RevisionSummary{
		ID: revisionID, DatasetID: datasetID, VersionNo: 2, OperationType: "SAVE",
		Name: prepared.Document.Dataset.Name, Description: prepared.Document.Dataset.Description,
		Type: prepared.Document.Dataset.Type, DraftVersionID: "33333333-3333-4333-8333-333333333333",
		DraftRecordVersion: 2, DSLVersion: DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}}}
	service := NewService(store)

	items, total, err := service.ListRevisions(context.Background(), "tenant-1", datasetID, 20, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != revisionID {
		t.Fatalf("ListRevisions() items=%#v total=%d err=%v", items, total, err)
	}
	loaded, err := service.GetRevision(context.Background(), "tenant-1", datasetID, revisionID)
	if err != nil || loaded.ID != revisionID || loaded.DSLHash != prepared.DSLHash {
		t.Fatalf("GetRevision() record=%#v err=%v", loaded, err)
	}
	rolledBack, err := service.RollbackRevision(context.Background(), "tenant-1", "actor-1", datasetID, revisionID, RollbackRevisionInput{ExpectedVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Version != 8 || rolledBack.DraftRecordVersion != 5 || rolledBack.DSLHash != prepared.DSLHash ||
		rolledBack.CurrentPublishedVersionID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("RollbackRevision() record=%#v", rolledBack)
	}
	if len(store.revisions) != 2 || store.revisions[0].OperationType != "ROLLBACK" || store.revisions[0].SourceRevisionID != revisionID {
		t.Fatalf("revisions=%#v", store.revisions)
	}
	if _, err := service.RollbackRevision(context.Background(), "tenant-1", "actor-1", datasetID, revisionID, RollbackRevisionInput{ExpectedVersion: 7}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rollback error=%v", err)
	}
	if _, err := service.GetRevision(context.Background(), "tenant-1", datasetID, "invalid"); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("invalid revision error=%v", err)
	}
}

func TestServiceRollsBackPublishedVersionThroughExactSourceRevision(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	const datasetID = "11111111-1111-4111-8111-111111111111"
	const versionID = "22222222-2222-4222-8222-222222222222"
	const revisionID = "55555555-5555-4555-8555-555555555555"
	revision := RevisionRecord{RevisionSummary: RevisionSummary{
		ID: revisionID, DatasetID: datasetID, VersionNo: 2, OperationType: "SAVE",
		Name: prepared.Document.Dataset.Name, Description: prepared.Document.Dataset.Description,
		Type: prepared.Document.Dataset.Type, DraftVersionID: "33333333-3333-4333-8333-333333333333",
		DraftRecordVersion: 2, DSLVersion: DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}
	store := &memoryStore{record: Record{
		ID: datasetID, Status: "PUBLISHED", Version: 7,
		DraftVersionID: revision.DraftVersionID, DraftRecordVersion: 4,
		CurrentPublishedVersionID: versionID,
	}, sourceRevision: revision, revisions: []RevisionRecord{revision}}
	service := NewService(store)

	rolledBack, err := service.RollbackVersion(context.Background(), "tenant-1", "actor-1", datasetID, versionID, RollbackRevisionInput{ExpectedVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Version != 8 || rolledBack.DraftRecordVersion != 5 || rolledBack.DSLHash != prepared.DSLHash ||
		rolledBack.CurrentPublishedVersionID != versionID {
		t.Fatalf("RollbackVersion() record=%#v", rolledBack)
	}
	if store.resolveSourceCalls != 1 || store.resolveSourceTenant != "tenant-1" ||
		store.resolveSourceDataset != datasetID || store.resolveSourceVersion != versionID || store.rollbackCalls != 1 {
		t.Fatalf("store scope=%#v", store)
	}
	if len(store.revisions) != 2 || store.revisions[0].OperationType != "ROLLBACK" || store.revisions[0].SourceRevisionID != revisionID {
		t.Fatalf("revisions=%#v", store.revisions)
	}
}

func TestServicePublishedVersionRollbackFailsClosed(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	const datasetID = "11111111-1111-4111-8111-111111111111"
	const versionID = "22222222-2222-4222-8222-222222222222"
	revision := RevisionRecord{RevisionSummary: RevisionSummary{
		ID: "55555555-5555-4555-8555-555555555555", DatasetID: datasetID,
		Name: prepared.Document.Dataset.Name, Description: prepared.Document.Dataset.Description,
		Type: prepared.Document.Dataset.Type, DSLVersion: DSLVersion,
		DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
	}, DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON}

	tests := []struct {
		name       string
		revision   RevisionRecord
		resolveErr error
	}{
		{name: "缺少或重复来源", resolveErr: ErrVersionRollbackUnavailable},
		{name: "来源内容摘要损坏", revision: func() RevisionRecord {
			corrupted := revision
			corrupted.DSLHash = "corrupted"
			return corrupted
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{record: Record{ID: datasetID, Version: 7}, sourceRevision: test.revision, sourceRevisionErr: test.resolveErr}
			service := NewService(store)
			_, err := service.RollbackVersion(context.Background(), "tenant-1", "actor-1", datasetID, versionID, RollbackRevisionInput{ExpectedVersion: 7})
			if !errors.Is(err, ErrVersionRollbackUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if store.rollbackCalls != 0 || store.record.Version != 7 {
				t.Fatalf("失败关闭后仍修改草稿：store=%#v", store)
			}
		})
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

func TestServiceAllowsDerivedDatasetTypeToChangeWithDraftNodes(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store)
	created, err := service.Create(context.Background(), "tenant-1", "user-1", CreateInput{Code: "monthly_orders", Name: "月度订单数据集", Description: "按月份汇总有效订单金额", Type: "SINGLE_SOURCE", DSL: readExample(t)})
	if err != nil {
		t.Fatal(err)
	}
	var document Document
	if err := json.Unmarshal(created.DSL, &document); err != nil {
		t.Fatal(err)
	}
	document.Dataset.Type = "CROSS_SOURCE"
	document.Nodes = append(document.Nodes, Node{
		ID: "customers", Type: "TABLE", DataSourceID: "33333333-3333-4333-8333-333333333333",
		TableID: "44444444-4444-4444-8444-444444444444", Alias: "c",
		Projection: []string{"customer_id"}, SourceFilters: []SourceFilter{},
	})
	document.Joins = []Join{{
		ID: "orders_customers", LeftNodeID: "orders", RightNodeID: "customers", JoinType: "LEFT", Cardinality: "UNKNOWN", ManualConfirmed: true,
		Conditions: []JoinCondition{{
			LeftExpression:  Expression{Type: "FIELD_REF", NodeID: "orders", Field: "order_status"},
			Operator:        "EQUALS",
			RightExpression: Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"},
		}},
	}}
	crossSourceDSL, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(context.Background(), "tenant-1", "user-1", created.ID, UpdateInput{
		Name: created.Name, Description: created.Description, ExpectedVersion: created.Version, DSL: crossSourceDSL,
	})
	if err != nil {
		t.Fatalf("Update() should allow derived type change: %v", err)
	}
	if updated.Type != "CROSS_SOURCE" || updated.Version != created.Version+1 {
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

func TestServiceDisablesRestoresAndDeletesWithOptimisticVersion(t *testing.T) {
	store := &memoryStore{record: Record{ID: "11111111-1111-4111-8111-111111111111", Version: 3, Status: "PUBLISHED", CurrentPublishedVersionID: "22222222-2222-4222-8222-222222222222"}}
	service := NewService(store)
	disabled, err := service.Disable(context.Background(), "tenant-1", "actor-1", store.record.ID, LifecycleInput{ExpectedVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != "DISABLED" || disabled.Version != 4 || disabled.CurrentPublishedVersionID != "" {
		t.Fatalf("disabled=%#v", disabled)
	}
	restored, err := service.Restore(context.Background(), "tenant-1", "actor-1", disabled.ID, LifecycleInput{ExpectedVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "PUBLISHED" || restored.Version != 5 || restored.CurrentPublishedVersionID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("restored=%#v", restored)
	}
	if _, err := service.Restore(context.Background(), "tenant-1", "actor-1", restored.ID, LifecycleInput{ExpectedVersion: 5}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restore active dataset error=%v", err)
	}
	disabled, err = service.Disable(context.Background(), "tenant-1", "actor-1", restored.ID, LifecycleInput{ExpectedVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "tenant-1", "actor-1", disabled.ID, LifecycleInput{ExpectedVersion: 6}); err != nil {
		t.Fatal(err)
	}
	if store.record.ID != "" {
		t.Fatalf("record was not deleted: %#v", store.record)
	}
	if _, err := service.Disable(context.Background(), "tenant-1", "actor-1", "invalid", LifecycleInput{ExpectedVersion: 1}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("invalid id error=%v", err)
	}
	if _, err := service.Restore(context.Background(), "tenant-1", "actor-1", "invalid", LifecycleInput{ExpectedVersion: 1}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("invalid restore id error=%v", err)
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
