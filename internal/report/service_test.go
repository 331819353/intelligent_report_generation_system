package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/reportjson"
)

type memoryStore struct {
	record     DraftRecord
	replays    map[string]memoryReplay
	lastCreate CreatePlan
	lastUpdate UpdatePlan
	revisions  []RevisionRecord
}

type memoryReplay struct {
	hash   string
	actor  string
	record DraftRecord
}

func newMemoryStore() *memoryStore { return &memoryStore{replays: map[string]memoryReplay{}} }

func (s *memoryStore) Replay(_ context.Context, _, actorID, reportID, scope, key, requestHash string) (DraftRecord, bool, error) {
	item, exists := s.replays[scope+":"+reportID+":"+key]
	if !exists && scope == "CREATE" {
		item, exists = s.replays[scope+"::"+key]
	}
	if !exists {
		return DraftRecord{}, false, nil
	}
	if item.actor != actorID || item.hash != requestHash {
		return DraftRecord{}, false, ErrIdempotencyConflict
	}
	return item.record, true, nil
}

func (s *memoryStore) Create(_ context.Context, _, actorID string, plan CreatePlan) (DraftRecord, error) {
	s.lastCreate = plan
	report := plan.Prepared.Document.Report
	s.record = DraftRecord{
		ID: plan.ID, Code: report.Code, Name: report.Name, Description: report.Description, Type: report.Type, Status: "DRAFT",
		Revision: 1, DefinitionHash: plan.Prepared.Hash, Definition: plan.Prepared.JSON, EditorState: plan.EditorState,
	}
	s.replays["CREATE::"+plan.IdempotencyKey] = memoryReplay{hash: plan.RequestHash, actor: actorID, record: s.record}
	return s.record, nil
}

func (s *memoryStore) Get(_ context.Context, _, _, _, _ string) (DraftRecord, error) {
	if s.record.ID == "" {
		return DraftRecord{}, ErrNotFound
	}
	return s.record, nil
}

func (s *memoryStore) Update(_ context.Context, _, actorID, id string, plan UpdatePlan) (DraftRecord, error) {
	s.lastUpdate = plan
	if plan.ExpectedRevision != s.record.Revision {
		return DraftRecord{}, &ConflictError{Revision: s.record.Revision, Hash: s.record.DefinitionHash}
	}
	report := plan.Final.Document.Report
	s.record.Name, s.record.Description = report.Name, report.Description
	s.record.Revision += int64(len(plan.Changes))
	s.record.Definition, s.record.DefinitionHash, s.record.EditorState = plan.Final.JSON, plan.Final.Hash, plan.EditorState
	s.replays["UPDATE:"+id+":"+plan.IdempotencyKey] = memoryReplay{hash: plan.RequestHash, actor: actorID, record: s.record}
	return s.record, nil
}

func (s *memoryStore) ListRevisions(_ context.Context, _, _, _ string, _, _ int) ([]RevisionRecord, int, error) {
	return s.revisions, len(s.revisions), nil
}

func TestServiceCreateGeneratesServerIdentityAndReplaysExactly(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	input := CreateInput{Definition: reportExample(t)}
	created, err := service.Create(context.Background(), "tenant-1", "actor-1", "create-key", input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if uuid.Validate(created.ID) != nil || created.Revision != 1 || created.Status != "DRAFT" {
		t.Fatalf("created = %#v", created)
	}
	prepared, err := reportjson.Prepare(created.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Document.Report.ID != created.ID || prepared.Document.Report.ID == "report_enterprise_monthly" {
		t.Fatalf("server report id = %q", prepared.Document.Report.ID)
	}
	if created.EditorState.MinimumRowsByPage["page_overview"] != 14 {
		t.Fatalf("editor state = %#v", created.EditorState)
	}

	// 模拟首次响应后草稿继续变化；创建重放仍必须返回原始快照和原始 ID。
	original := created
	store.record.Revision = 9
	replayed, err := service.Create(context.Background(), "tenant-1", "actor-1", "create-key", input)
	if err != nil || replayed.ID != original.ID || replayed.Revision != original.Revision {
		t.Fatalf("replayed = %#v, err = %v", replayed, err)
	}
	if _, err := service.Create(context.Background(), "tenant-1", "actor-2", "create-key", input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-actor replay error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestServiceRejectsDefinitionThatExceedsLimitAfterCanonicalization(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(reportExample(t), &document); err != nil {
		t.Fatal(err)
	}
	// 原始 JSON 可用未转义的 HTML 字符保持在上限内，规范编码后仍必须再次检查实际持久化字节数。
	document["report"].(map[string]any)["description"] = strings.Repeat("<", 400_000)
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		t.Fatal(err)
	}
	if raw.Len() >= MaxDefinitionBytes {
		t.Fatalf("test input unexpectedly exceeds raw limit: %d", raw.Len())
	}
	_, err := NewService(newMemoryStore()).Create(context.Background(), "tenant-1", "actor-1", "canonical-size", CreateInput{Definition: raw.Bytes()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("canonical size error = %v, want ErrInvalidRequest", err)
	}
}

func TestServiceUpdateAppliesSemanticChangeAndRejectsMismatchedDefinition(t *testing.T) {
	store, service, created := createReport(t)
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		component(document, 0, 0, 1)["sticky"].(map[string]any)["top"] = float64(12)
	})
	change := DraftChange{
		ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER",
		Target: ChangeTarget{PageID: "page_overview", BlockID: "block_overview", ComponentID: "filter_stat_month"},
		Patch:  []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/1/sticky/top", Value: json.RawMessage(`12`)}},
	}
	updated, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "save-1", UpdateInput{ExpectedRevision: 1, Definition: definition, Changes: []DraftChange{change}})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision != 2 || len(store.lastUpdate.Changes) != 1 || store.lastUpdate.Changes[0].OperationType != "COMPONENT_STICKY_UPDATE" || store.lastUpdate.Changes[0].PatchHash == "" {
		t.Fatalf("updated=%#v plan=%#v", updated, store.lastUpdate)
	}

	store.record = created
	_, err = service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "save-mismatch", UpdateInput{ExpectedRevision: 1, Definition: created.Definition, Changes: []DraftChange{{
		ClientOperationID: uuid.NewString(), OperationType: change.OperationType, Source: "USER", Target: change.Target, Patch: change.Patch,
	}}})
	if !errors.Is(err, ErrPatchMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestServiceUpdateAppliesBlockConfigChange(t *testing.T) {
	store, service, created := createReport(t)
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		document["pages"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["name"] = "经营概览内容区"
	})
	change := DraftChange{
		ClientOperationID: uuid.NewString(), OperationType: "BLOCK_CONFIG_UPDATE", Source: "USER",
		Target: ChangeTarget{PageID: "page_overview", BlockID: "block_overview"},
		Patch:  []PatchOperation{{Op: "add", Path: "/pages/0/blocks/0/name", Value: json.RawMessage(`"经营概览内容区"`)}},
	}
	updated, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "block-config", UpdateInput{
		ExpectedRevision: 1,
		Definition:       definition,
		Changes:          []DraftChange{change},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision != 2 || len(store.lastUpdate.Changes) != 1 || store.lastUpdate.Changes[0].OperationType != "BLOCK_CONFIG_UPDATE" {
		t.Fatalf("updated=%#v plan=%#v", updated, store.lastUpdate)
	}
}

func TestServiceRejectsManualLockAndUnlockThenModifyBatch(t *testing.T) {
	_, service, created := createReport(t)
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		component(document, 0, 0, 0)["sticky"].(map[string]any)["top"] = float64(8)
	})
	_, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "locked", UpdateInput{ExpectedRevision: 1, Definition: definition, Changes: []DraftChange{{
		ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER",
		Target: ChangeTarget{PageID: "page_overview", BlockID: "block_overview", ComponentID: "title_main"},
		Patch:  []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/0/sticky/top", Value: json.RawMessage(`8`)}},
	}}})
	if !errors.Is(err, ErrEditLocked) {
		t.Fatalf("manual lock error = %v", err)
	}

	// 同一批次先解除 manualLocked 再修改仍以请求开始时的旧文档为锁事实。
	definition = mutateDocument(t, created.Definition, func(document map[string]any) {
		item := component(document, 0, 0, 0)
		item["manualLocked"] = false
		item["sticky"].(map[string]any)["top"] = float64(8)
	})
	_, err = service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "unlock-bypass", UpdateInput{ExpectedRevision: 1, Definition: definition, Changes: []DraftChange{
		{
			ClientOperationID: uuid.NewString(), OperationType: "UNDO", Source: "USER", Target: ChangeTarget{ReferencedOperationID: uuid.NewString()},
			Patch: []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/0/manualLocked", Value: json.RawMessage(`false`)}},
		},
		{
			ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER", Target: ChangeTarget{PageID: "page_overview", BlockID: "block_overview", ComponentID: "title_main"},
			Patch: []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/0/sticky/top", Value: json.RawMessage(`8`)}},
		},
	}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("unlock bypass error = %v", err)
	}
}

func TestServiceRejectsUndoUsedAsGenericReportMutation(t *testing.T) {
	_, service, created := createReport(t)
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		document["report"].(map[string]any)["name"] = "伪造撤销后的名称"
	})
	_, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "undo-generic-mutation", UpdateInput{
		ExpectedRevision: 1,
		Definition:       definition,
		Changes: []DraftChange{{
			ClientOperationID: uuid.NewString(), OperationType: "UNDO", Source: "USER",
			Target: ChangeTarget{ReferencedOperationID: uuid.NewString()},
			Patch:  []PatchOperation{{Op: "replace", Path: "/report/name", Value: json.RawMessage(`"伪造撤销后的名称"`)}},
		}},
	})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("generic undo mutation error = %v", err)
	}
}

func TestServiceAllowsExplicitLegacyDraftRecovery(t *testing.T) {
	_, service, created := createReport(t)
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		document["report"].(map[string]any)["name"] = "恢复的旧会话标题"
	})
	updated, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "legacy-recovery", UpdateInput{
		ExpectedRevision: 1,
		Definition:       definition,
		Changes: []DraftChange{{
			ClientOperationID: uuid.NewString(), OperationType: "LEGACY_DRAFT_RECOVERY", Source: "USER",
			Patch: []PatchOperation{{Op: "replace", Path: "/report/name", Value: json.RawMessage(`"恢复的旧会话标题"`)}},
		}},
	})
	if err != nil || updated.Name != "恢复的旧会话标题" || updated.Revision != 2 {
		t.Fatalf("legacy recovery updated=%#v err=%v", updated, err)
	}
}

func TestServiceValidatesComponentCopyIdentity(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	base := mutateDocument(t, reportExample(t), func(document map[string]any) {
		component(document, 0, 1, 0)["grid"].(map[string]any)["w"] = float64(24)
	})
	created, err := service.Create(context.Background(), "tenant-1", "actor-1", "copy-create", CreateInput{Definition: base})
	if err != nil {
		t.Fatal(err)
	}
	createdID := "source_note_copy"
	definition := mutateDocument(t, created.Definition, func(document map[string]any) {
		source := component(document, 0, 1, 0)
		copyBytes, _ := json.Marshal(source)
		var copied map[string]any
		_ = json.Unmarshal(copyBytes, &copied)
		copied["id"], copied["name"], copied["manualLocked"] = createdID, "数据来源说明 副本", false
		copied["grid"].(map[string]any)["x"] = float64(24)
		document["pages"].([]any)[0].(map[string]any)["blocks"].([]any)[1].(map[string]any)["components"] = append(
			document["pages"].([]any)[0].(map[string]any)["blocks"].([]any)[1].(map[string]any)["components"].([]any), copied,
		)
	})
	var final map[string]any
	_ = json.Unmarshal(definition, &final)
	createdComponent := component(final, 0, 1, 1)
	value, _ := json.Marshal(createdComponent)
	change := DraftChange{
		ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_COPY", Source: "USER",
		Target: ChangeTarget{PageID: "page_overview", BlockID: "block_source_note", SourceComponentID: "source_note", CreatedComponentID: createdID},
		Patch:  []PatchOperation{{Op: "add", Path: "/pages/0/blocks/1/components/-", Value: value}},
	}
	updated, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "copy-save", UpdateInput{ExpectedRevision: 1, Definition: definition, Changes: []DraftChange{change}})
	if err != nil || updated.Revision != 2 || store.lastUpdate.Changes[0].OperationType != "COMPONENT_COPY" {
		t.Fatalf("copy updated=%#v err=%v", updated, err)
	}

	change.Target.CreatedComponentID = "wrong-id"
	change.ClientOperationID = uuid.NewString()
	store.record = created
	_, err = service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "copy-invalid", UpdateInput{ExpectedRevision: 1, Definition: definition, Changes: []DraftChange{change}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("invalid copy error = %v", err)
	}
}

func TestNormalizeChangesRejectsUnsupportedAndExcessivePatch(t *testing.T) {
	operations := make([]PatchOperation, MaxPatchOperations+1)
	for index := range operations {
		operations[index] = PatchOperation{Op: "replace", Path: "/report/name", Value: json.RawMessage(`"x"`)}
	}
	_, _, err := normalizeChanges([]DraftChange{{ClientOperationID: uuid.NewString(), OperationType: "DOCUMENT_UPDATE", Source: "USER", Patch: operations}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsupported error = %v", err)
	}
	_, _, err = normalizeChanges([]DraftChange{{ClientOperationID: uuid.NewString(), OperationType: "UNDO", Source: "USER", Target: ChangeTarget{ReferencedOperationID: uuid.NewString()}, Patch: operations}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("excessive patch error = %v", err)
	}
	_, _, err = normalizeChanges([]DraftChange{{
		ClientOperationID: uuid.NewString(), OperationType: "BLOCK_MOVE", Source: "USER",
		Target: ChangeTarget{ReferencedOperationID: uuid.NewString()}, Patch: []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/grid/x", Value: json.RawMessage(`1`)}},
	}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unexpected reference error = %v", err)
	}
	largeValue := json.RawMessage(`"` + strings.Repeat("a", MaxPatchBytes) + `"`)
	_, _, err = normalizeChanges([]DraftChange{{
		ClientOperationID: uuid.NewString(), OperationType: "UNDO", Source: "USER",
		Target: ChangeTarget{ReferencedOperationID: uuid.NewString()}, Patch: []PatchOperation{{Op: "replace", Path: "/report/name", Value: largeValue}},
	}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("oversized patch error = %v", err)
	}
}

func TestApplyChangeRejectsTrailingJSONValue(t *testing.T) {
	prepared, err := reportjson.Prepare(reportExample(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = applyChange(prepared, DraftChange{Patch: []PatchOperation{{
		Op: "replace", Path: "/report/name", Value: json.RawMessage(`"新名称" true`),
	}}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestApplyChangeRejectsAmbiguousArrayIndex(t *testing.T) {
	prepared, err := reportjson.Prepare(reportExample(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = applyChange(prepared, DraftChange{Patch: []PatchOperation{{
		Op: "replace", Path: "/pages/00/name", Value: json.RawMessage(`"概览"`),
	}}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("ambiguous array index error = %v", err)
	}
}

func TestApplyChangeRejectsTooManyPointerSegments(t *testing.T) {
	prepared, err := reportjson.Prepare(reportExample(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = applyChange(prepared, DraftChange{Patch: []PatchOperation{{
		Op: "replace", Path: strings.Repeat("/x", MaxPatchPathSegments+1), Value: json.RawMessage(`1`),
	}}})
	if !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("pointer segment error = %v", err)
	}
}

func TestServiceTracksBlocksTouchedByNetZeroBatch(t *testing.T) {
	store, service, created := createReport(t)
	target := ChangeTarget{PageID: "page_overview", BlockID: "block_overview", ComponentID: "filter_stat_month"}
	changes := []DraftChange{
		{ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER", Target: target, Patch: []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/1/sticky/top", Value: json.RawMessage(`7`)}}},
		{ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER", Target: target, Patch: []PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/1/sticky/top", Value: json.RawMessage(`0`)}}},
	}
	_, err := service.Update(context.Background(), "tenant-1", "actor-1", created.ID, "net-zero", UpdateInput{
		ExpectedRevision: created.Revision, Definition: created.Definition, EditorState: created.EditorState, Changes: changes,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(store.lastUpdate.AffectedBlockIDs) != 1 || store.lastUpdate.AffectedBlockIDs[0] != "block_overview" {
		t.Fatalf("affected blocks = %#v", store.lastUpdate.AffectedBlockIDs)
	}
}

func createReport(t *testing.T) (*memoryStore, *Service, DraftRecord) {
	t.Helper()
	store := newMemoryStore()
	service := NewService(store)
	created, err := service.Create(context.Background(), "tenant-1", "actor-1", "create", CreateInput{Definition: reportExample(t)})
	if err != nil {
		t.Fatal(err)
	}
	return store, service, created
}

func reportExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "examples", "report-json-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutateDocument(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	result, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func component(document map[string]any, page, block, index int) map[string]any {
	return document["pages"].([]any)[page].(map[string]any)["blocks"].([]any)[block].(map[string]any)["components"].([]any)[index].(map[string]any)
}
