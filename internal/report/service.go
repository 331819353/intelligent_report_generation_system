package report

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/reportjson"
)

type Service struct{ store Store }

// NewService 创建报告草稿服务；发布版本和对象存储编排由 T0601 负责。
func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Create(ctx context.Context, tenantID, actorID, idempotencyKey string, input CreateInput) (DraftRecord, error) {
	if tenantID == "" || actorID == "" || !validIdempotencyKey(idempotencyKey) || len(input.Definition) > MaxDefinitionBytes {
		return DraftRecord{}, ErrInvalidRequest
	}
	preparedWithoutServerID, err := reportjson.Prepare(input.Definition)
	if err != nil {
		return DraftRecord{}, err
	}
	if len(preparedWithoutServerID.JSON) > MaxDefinitionBytes {
		return DraftRecord{}, fmt.Errorf("%w: 规范报告 JSON 不能超过 %d 字节", ErrInvalidRequest, MaxDefinitionBytes)
	}
	if preparedWithoutServerID.Document.Report.Status != "DRAFT" {
		return DraftRecord{}, fmt.Errorf("%w: 草稿状态必须为 DRAFT", ErrIdentityInvalid)
	}
	// 客户端报告 ID 不参与创建幂等；服务端生成的 UUID 才是权限与持久化身份。
	requestDocument := preparedWithoutServerID.Document
	requestDocument.Report.ID = ""
	requestRaw, err := json.Marshal(requestDocument)
	if err != nil {
		return DraftRecord{}, err
	}
	requestPrepared, err := reportjson.Prepare(requestRaw)
	if err != nil {
		return DraftRecord{}, err
	}
	if len(requestPrepared.JSON) > MaxDefinitionBytes {
		return DraftRecord{}, fmt.Errorf("%w: 规范报告 JSON 不能超过 %d 字节", ErrInvalidRequest, MaxDefinitionBytes)
	}
	editorState, err := normalizeEditorState(input.EditorState, requestPrepared.Document)
	if err != nil {
		return DraftRecord{}, err
	}
	requestHash, err := hashRequest(struct {
		Definition  json.RawMessage `json:"definition"`
		EditorState EditorState     `json:"editorState"`
	}{requestPrepared.JSON, editorState})
	if err != nil {
		return DraftRecord{}, err
	}
	if replay, ok, err := s.store.Replay(ctx, tenantID, actorID, "", "CREATE", idempotencyKey, requestHash); err != nil || ok {
		return replay, err
	}

	document := requestPrepared.Document
	document.Report.ID = uuid.NewString()
	storedRaw, err := json.Marshal(document)
	if err != nil {
		return DraftRecord{}, err
	}
	prepared, err := reportjson.Prepare(storedRaw)
	if err != nil {
		return DraftRecord{}, err
	}
	if len(prepared.JSON) > MaxDefinitionBytes {
		return DraftRecord{}, fmt.Errorf("%w: 规范报告 JSON 不能超过 %d 字节", ErrInvalidRequest, MaxDefinitionBytes)
	}
	components, dependencies := deriveIndexes(prepared.Document)
	return s.store.Create(ctx, tenantID, actorID, CreatePlan{
		ID: document.Report.ID, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		Prepared: prepared, EditorState: editorState, Components: components, Dependencies: dependencies,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, actorID, id string) (DraftRecord, error) {
	if tenantID == "" || actorID == "" || uuid.Validate(id) != nil {
		return DraftRecord{}, ErrNotFound
	}
	return s.store.Get(ctx, tenantID, actorID, id, "READ")
}

func (s *Service) Update(ctx context.Context, tenantID, actorID, id, idempotencyKey string, input UpdateInput) (DraftRecord, error) {
	if tenantID == "" || actorID == "" || uuid.Validate(id) != nil || !validIdempotencyKey(idempotencyKey) || input.ExpectedRevision < 1 || len(input.Definition) > MaxDefinitionBytes {
		return DraftRecord{}, ErrInvalidRequest
	}
	finalProvided, err := reportjson.Prepare(input.Definition)
	if err != nil {
		return DraftRecord{}, err
	}
	if len(finalProvided.JSON) > MaxDefinitionBytes {
		return DraftRecord{}, fmt.Errorf("%w: 规范报告 JSON 不能超过 %d 字节", ErrInvalidRequest, MaxDefinitionBytes)
	}
	if finalProvided.Document.Report.ID != id || finalProvided.Document.Report.Status != "DRAFT" {
		return DraftRecord{}, fmt.Errorf("%w: 报告 ID 或草稿状态无效", ErrIdentityInvalid)
	}
	editorState, err := normalizeEditorState(input.EditorState, finalProvided.Document)
	if err != nil {
		return DraftRecord{}, err
	}
	normalizedChanges, totalOperations, err := normalizeChanges(input.Changes)
	if err != nil {
		return DraftRecord{}, err
	}
	requestHash, err := hashRequest(struct {
		ExpectedRevision int64           `json:"expectedRevision"`
		Definition       json.RawMessage `json:"definition"`
		EditorState      EditorState     `json:"editorState"`
		Changes          []DraftChange   `json:"changes"`
	}{input.ExpectedRevision, finalProvided.JSON, editorState, normalizedChanges})
	if err != nil {
		return DraftRecord{}, err
	}
	// 幂等记录必须先于 expectedRevision 和 Patch 应用读取，才能在后续保存发生后精确重放旧响应。
	if replay, ok, err := s.store.Replay(ctx, tenantID, actorID, id, "UPDATE", idempotencyKey, requestHash); err != nil || ok {
		return replay, err
	}

	currentRecord, err := s.store.Get(ctx, tenantID, actorID, id, "UPDATE")
	if err != nil {
		return DraftRecord{}, err
	}
	if currentRecord.Revision != input.ExpectedRevision {
		return DraftRecord{}, &ConflictError{Revision: currentRecord.Revision, Hash: currentRecord.DefinitionHash}
	}
	baseline, err := reportjson.Prepare(currentRecord.Definition)
	if err != nil {
		return DraftRecord{}, err
	}
	if baseline.Document.Report.ID != id || baseline.Document.Report.Status != "DRAFT" {
		return DraftRecord{}, ErrIdentityInvalid
	}
	current := baseline
	preparedChanges := make([]PreparedChange, 0, len(normalizedChanges))
	affectedBlockSet := map[string]bool{}
	for _, change := range normalizedChanges {
		next, patchJSON, patchHash, err := applyChange(current, change)
		if err != nil {
			return DraftRecord{}, err
		}
		if err := validatePersistentIdentity(baseline.Document, next.Document); err != nil {
			return DraftRecord{}, err
		}
		if err := validateLockedMutation(baseline.Document, next.Document); err != nil {
			return DraftRecord{}, err
		}
		if err := validateChangeSemantics(current.Document, next.Document, change); err != nil {
			return DraftRecord{}, err
		}
		// 占用校验必须覆盖每个中间变更；若只比较批次首尾，移动后撤销会把真实触碰过的分块抵消掉。
		for _, blockID := range touchedBlocks(calculateDelta(current.Document, next.Document)) {
			affectedBlockSet[blockID] = true
		}
		targetJSON, err := json.Marshal(change.Target)
		if err != nil {
			return DraftRecord{}, err
		}
		preparedChanges = append(preparedChanges, PreparedChange{
			ClientOperationID: change.ClientOperationID, OperationType: change.OperationType, Source: "USER",
			ReferencedOperationID: change.Target.ReferencedOperationID,
			TargetJSON:            targetJSON, PatchJSON: patchJSON, PatchHash: patchHash, BeforeHash: current.Hash, After: next,
		})
		current = next
	}
	if !bytes.Equal(current.JSON, finalProvided.JSON) {
		return DraftRecord{}, ErrPatchMismatch
	}
	components, dependencies := deriveIndexes(current.Document)
	affectedBlockIDs := make([]string, 0, len(affectedBlockSet))
	for blockID := range affectedBlockSet {
		affectedBlockIDs = append(affectedBlockIDs, blockID)
	}
	sort.Strings(affectedBlockIDs)
	_ = totalOperations // 已在 normalizeChanges 中验证总上限，保留变量便于审计调试。
	return s.store.Update(ctx, tenantID, actorID, id, UpdatePlan{
		ExpectedRevision: input.ExpectedRevision, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		Final: current, EditorState: editorState, Changes: preparedChanges, AffectedBlockIDs: affectedBlockIDs,
		Components: components, Dependencies: dependencies,
	})
}

func (s *Service) ListRevisions(ctx context.Context, tenantID, actorID, id string, limit, offset int) ([]RevisionRecord, int, error) {
	if tenantID == "" || actorID == "" || uuid.Validate(id) != nil || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListRevisions(ctx, tenantID, actorID, id, limit, offset)
}

func normalizeChanges(changes []DraftChange) ([]DraftChange, int, error) {
	if len(changes) == 0 || len(changes) > MaxChanges {
		return nil, 0, fmt.Errorf("%w: changes 数量必须在 1 到 %d 之间", ErrInvalidRequest, MaxChanges)
	}
	result := make([]DraftChange, len(changes))
	patches := make([][]PatchOperation, len(changes))
	seenIDs := map[string]bool{}
	total := 0
	for index, change := range changes {
		change.OperationType = strings.ToUpper(strings.TrimSpace(change.OperationType))
		if !allowedOperationTypes[change.OperationType] || change.Source != "USER" || uuid.Validate(change.ClientOperationID) != nil || seenIDs[change.ClientOperationID] {
			return nil, 0, fmt.Errorf("%w: changes[%d] 的操作身份、类型或来源无效", ErrInvalidRequest, index)
		}
		seenIDs[change.ClientOperationID] = true
		if len(change.Patch) == 0 {
			return nil, 0, fmt.Errorf("%w: changes[%d].patch 不能为空", ErrInvalidPatch, index)
		}
		total += len(change.Patch)
		if total > MaxPatchOperations {
			return nil, 0, fmt.Errorf("%w: 单请求 Patch 操作不能超过 %d 条", ErrInvalidPatch, MaxPatchOperations)
		}
		for patchIndex := range change.Patch {
			change.Patch[patchIndex].Op = strings.ToLower(strings.TrimSpace(change.Patch[patchIndex].Op))
		}
		if change.OperationType == "UNDO" || change.OperationType == "REDO" {
			if uuid.Validate(change.Target.ReferencedOperationID) != nil {
				return nil, 0, fmt.Errorf("%w: 撤销或重做必须引用有效的历史操作 UUID", ErrInvalidRequest)
			}
		} else if change.Target.ReferencedOperationID != "" {
			return nil, 0, fmt.Errorf("%w: 仅撤销或重做可以携带 referencedOperationId", ErrInvalidRequest)
		}
		result[index] = change
		patches[index] = change.Patch
	}
	patchJSON, err := json.Marshal(patches)
	if err != nil || len(patchJSON) > MaxPatchBytes {
		return nil, 0, fmt.Errorf("%w: 单请求 Patch 正文不能超过 %d 字节", ErrInvalidPatch, MaxPatchBytes)
	}
	return result, total, nil
}

func validatePersistentIdentity(baseline, candidate reportjson.Document) error {
	if candidate.Report.ID != baseline.Report.ID || candidate.Report.Code != baseline.Report.Code || candidate.Report.Type != baseline.Report.Type || candidate.Report.Status != "DRAFT" {
		return fmt.Errorf("%w: report.id、code、type 不可修改且草稿必须保持 DRAFT", ErrIdentityInvalid)
	}
	return nil
}

func normalizeEditorState(input EditorState, document reportjson.Document) (EditorState, error) {
	provided := input.MinimumRowsByPage
	if provided == nil {
		provided = map[string]int{}
	}
	pages := make(map[string]reportjson.Page, len(document.Pages))
	for _, page := range document.Pages {
		pages[page.ID] = page
	}
	for pageID := range provided {
		if _, exists := pages[pageID]; !exists {
			return EditorState{}, fmt.Errorf("%w: editorState 引用了未知页面", ErrInvalidRequest)
		}
	}
	result := EditorState{MinimumRowsByPage: make(map[string]int, len(pages))}
	for pageID, page := range pages {
		rows := provided[pageID]
		if rows == 0 {
			rows = page.ContentGridRows
		}
		if rows < page.ContentGridRows || rows < 10 || rows > MaxEditorRows {
			return EditorState{}, fmt.Errorf("%w: editorState 页面行数无效", ErrInvalidRequest)
		}
		result.MinimumRowsByPage[pageID] = rows
	}
	return result, nil
}

func deriveIndexes(document reportjson.Document) ([]ComponentIndex, []DependencyIndex) {
	components := []ComponentIndex{}
	dependencies := []DependencyIndex{}
	seenDependencies := map[string]bool{}
	addDependency := func(kind, id, path string) {
		if strings.TrimSpace(id) == "" {
			return
		}
		key := kind + "\x00" + id + "\x00" + path
		if !seenDependencies[key] {
			seenDependencies[key] = true
			dependencies = append(dependencies, DependencyIndex{Type: kind, ID: id, Path: path})
		}
	}
	for requirementIndex, requirement := range document.DataRequirements {
		path := fmt.Sprintf("dataRequirements[%d]", requirementIndex)
		addDependency("DATASET_VERSION", requirement.ResolvedDatasetVersionID, path+".resolvedDatasetVersionId")
		for metricIndex, metricID := range requirement.ResolvedMetricIDs {
			addDependency("METRIC", metricID, fmt.Sprintf("%s.resolvedMetricIds[%d]", path, metricIndex))
		}
	}
	for parameterIndex, parameter := range document.Parameters {
		if parameter.SemanticBinding == nil {
			continue
		}
		for fieldIndex, field := range parameter.SemanticBinding.DatasetFields {
			addDependency("DATASET_VERSION", field.DatasetVersionID, fmt.Sprintf("parameters[%d].semanticBinding.datasetFields[%d].datasetVersionId", parameterIndex, fieldIndex))
		}
	}
	for pageIndex, page := range document.Pages {
		for blockIndex, block := range page.Blocks {
			for componentIndex, component := range block.Components {
				components = append(components, ComponentIndex{PageID: page.ID, BlockID: block.ID, ComponentID: component.ID, ComponentType: component.Type})
				path := fmt.Sprintf("pages[%d].blocks[%d].components[%d]", pageIndex, blockIndex, componentIndex)
				if id, ok := component.Binding["datasetVersionId"].(string); ok {
					addDependency("DATASET_VERSION", id, path+".binding.datasetVersionId")
				}
				for index, id := range stringValues(component.Binding["datasetVersionIds"]) {
					addDependency("DATASET_VERSION", id, fmt.Sprintf("%s.binding.datasetVersionIds[%d]", path, index))
				}
				if id, ok := component.Binding["metricId"].(string); ok {
					addDependency("METRIC", id, path+".binding.metricId")
				}
				for index, id := range stringValues(component.Binding["metricIds"]) {
					addDependency("METRIC", id, fmt.Sprintf("%s.binding.metricIds[%d]", path, index))
				}
				for traceIndex, trace := range component.SourceTrace {
					addDependency("SOURCE_TRACE", trace.SourceID, fmt.Sprintf("%s.sourceTrace[%d]", path, traceIndex))
				}
			}
		}
	}
	sort.Slice(components, func(i, j int) bool { return components[i].ComponentID < components[j].ComponentID })
	sort.Slice(dependencies, func(i, j int) bool {
		left, right := dependencies[i], dependencies[j]
		return left.Type+"\x00"+left.ID+"\x00"+left.Path < right.Type+"\x00"+right.ID+"\x00"+right.Path
	})
	return components, dependencies
}

func stringValues(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func validIdempotencyKey(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && strings.TrimSpace(value) == value
}

func hashRequest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
