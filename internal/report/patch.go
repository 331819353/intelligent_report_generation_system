package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/reportjson"
)

// applyChange 按顺序应用一次语义操作中的 RFC 6902 子操作；中间树不对外暴露，
// 整个 change 应用完成后必须重新通过报告合同校验。
func applyChange(current reportjson.Prepared, change DraftChange) (reportjson.Prepared, json.RawMessage, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(current.JSON))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return reportjson.Prepared{}, nil, "", err
	}
	canonicalOperations := make([]map[string]any, 0, len(change.Patch))
	for index, operation := range change.Patch {
		op := strings.ToLower(strings.TrimSpace(operation.Op))
		if op != "add" && op != "remove" && op != "replace" {
			return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: changes patch[%d].op 不受支持", ErrInvalidPatch, index)
		}
		if len(operation.Path) == 0 || len(operation.Path) > MaxPatchPathLength || operation.Path[0] != '/' {
			return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: changes patch[%d].path 无效", ErrInvalidPatch, index)
		}
		tokens, err := decodeJSONPointer(operation.Path)
		if err != nil || len(tokens) > MaxPatchPathSegments {
			return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: changes patch[%d].path 无效", ErrInvalidPatch, index)
		}
		var value any
		canonical := map[string]any{"op": op, "path": operation.Path}
		if op == "remove" {
			if operation.Value != nil {
				return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: remove 不能携带 value", ErrInvalidPatch)
			}
		} else {
			if operation.Value == nil {
				return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: %s 必须携带 value", ErrInvalidPatch, op)
			}
			valueDecoder := json.NewDecoder(bytes.NewReader(operation.Value))
			valueDecoder.UseNumber()
			if err := valueDecoder.Decode(&value); err != nil {
				return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: value 不是单一 JSON 值", ErrInvalidPatch)
			}
			if err := valueDecoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: value 不是单一 JSON 值", ErrInvalidPatch)
			}
			canonical["value"] = value
		}
		root, err = applyAt(root, tokens, op, value)
		if err != nil {
			return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: patch[%d]: %v", ErrInvalidPatch, index, err)
		}
		canonicalOperations = append(canonicalOperations, canonical)
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return reportjson.Prepared{}, nil, "", err
	}
	prepared, err := reportjson.Prepare(raw)
	if err != nil {
		return reportjson.Prepared{}, nil, "", err
	}
	if len(prepared.JSON) > MaxDefinitionBytes {
		return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: Patch 结果规范 JSON 不能超过 %d 字节", ErrInvalidPatch, MaxDefinitionBytes)
	}
	if prepared.Hash == current.Hash {
		return reportjson.Prepared{}, nil, "", fmt.Errorf("%w: change 没有产生实际修改", ErrInvalidPatch)
	}
	patchJSON, err := json.Marshal(canonicalOperations)
	if err != nil {
		return reportjson.Prepared{}, nil, "", err
	}
	sum := sha256.Sum256(patchJSON)
	return prepared, patchJSON, hex.EncodeToString(sum[:]), nil
}

func decodeJSONPointer(path string) ([]string, error) {
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		var decoded strings.Builder
		for cursor := 0; cursor < len(part); cursor++ {
			if part[cursor] != '~' {
				decoded.WriteByte(part[cursor])
				continue
			}
			if cursor+1 >= len(part) {
				return nil, errorsNewPointer()
			}
			cursor++
			switch part[cursor] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, errorsNewPointer()
			}
		}
		parts[index] = decoded.String()
	}
	return parts, nil
}

func errorsNewPointer() error { return fmt.Errorf("JSON Pointer 转义无效") }

func applyAt(node any, tokens []string, op string, value any) (any, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("不允许替换文档根节点")
	}
	key := tokens[0]
	last := len(tokens) == 1
	switch current := node.(type) {
	case map[string]any:
		if last {
			_, exists := current[key]
			switch op {
			case "add":
				current[key] = value
			case "replace":
				if !exists {
					return nil, fmt.Errorf("replace 目标不存在")
				}
				current[key] = value
			case "remove":
				if !exists {
					return nil, fmt.Errorf("remove 目标不存在")
				}
				delete(current, key)
			}
			return current, nil
		}
		child, exists := current[key]
		if !exists {
			return nil, fmt.Errorf("父路径不存在")
		}
		next, err := applyAt(child, tokens[1:], op, value)
		if err != nil {
			return nil, err
		}
		current[key] = next
		return current, nil
	case []any:
		if last && op == "add" && key == "-" {
			return append(current, value), nil
		}
		position, err := parseArrayIndex(key)
		if err != nil {
			return nil, fmt.Errorf("数组下标无效")
		}
		if last {
			switch op {
			case "add":
				if position > len(current) {
					return nil, fmt.Errorf("add 数组下标越界")
				}
				current = append(current, nil)
				copy(current[position+1:], current[position:])
				current[position] = value
			case "replace":
				if position >= len(current) {
					return nil, fmt.Errorf("replace 数组下标越界")
				}
				current[position] = value
			case "remove":
				if position >= len(current) {
					return nil, fmt.Errorf("remove 数组下标越界")
				}
				current = append(current[:position], current[position+1:]...)
			}
			return current, nil
		}
		if position >= len(current) {
			return nil, fmt.Errorf("父数组下标越界")
		}
		next, err := applyAt(current[position], tokens[1:], op, value)
		if err != nil {
			return nil, err
		}
		current[position] = next
		return current, nil
	default:
		return nil, fmt.Errorf("父路径不是对象或数组")
	}
}

// parseArrayIndex 遵循 JSON Patch 的数组下标语法，拒绝 +1、-0 和 01 等歧义写法。
func parseArrayIndex(value string) (int, error) {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return 0, fmt.Errorf("数组下标无效")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("数组下标无效")
		}
	}
	return strconv.Atoi(value)
}

type documentDelta struct {
	Other             bool
	AddedBlocks       map[string]bool
	RemovedBlocks     map[string]bool
	ChangedBlocks     map[string]bool
	AddedComponents   map[string]bool
	RemovedComponents map[string]bool
	ChangedComponents map[string]bool
	ComponentBlocks   map[string]string
}

func calculateDelta(before, after reportjson.Document) documentDelta {
	delta := documentDelta{
		AddedBlocks: map[string]bool{}, RemovedBlocks: map[string]bool{}, ChangedBlocks: map[string]bool{},
		AddedComponents: map[string]bool{}, RemovedComponents: map[string]bool{}, ChangedComponents: map[string]bool{}, ComponentBlocks: map[string]string{},
	}
	beforeShell, afterShell := before, after
	beforeShell.Pages, afterShell.Pages = nil, nil
	if !reflect.DeepEqual(beforeShell, afterShell) {
		delta.Other = true
	}
	beforePages := pageMap(before.Pages)
	afterPages := pageMap(after.Pages)
	if len(beforePages) != len(afterPages) {
		delta.Other = true
	}
	for pageID, beforePage := range beforePages {
		afterPage, exists := afterPages[pageID]
		if !exists {
			delta.Other = true
			continue
		}
		beforePage.Blocks, afterPage.Blocks = nil, nil
		beforePage.ContentGridRows, afterPage.ContentGridRows = 0, 0
		if !reflect.DeepEqual(beforePage, afterPage) {
			delta.Other = true
		}
	}
	beforeBlocks := blockMap(before)
	afterBlocks := blockMap(after)
	for id, located := range beforeBlocks {
		next, exists := afterBlocks[id]
		if !exists {
			delta.RemovedBlocks[id] = true
		} else if located.PageID != next.PageID || !reflect.DeepEqual(located.Block, next.Block) {
			delta.ChangedBlocks[id] = true
		}
	}
	for id := range afterBlocks {
		if _, exists := beforeBlocks[id]; !exists {
			delta.AddedBlocks[id] = true
		}
	}
	beforeComponents := componentMap(before)
	afterComponents := componentMap(after)
	for id, located := range beforeComponents {
		delta.ComponentBlocks[id] = located.BlockID
		next, exists := afterComponents[id]
		if !exists {
			delta.RemovedComponents[id] = true
		} else if located.PageID != next.PageID || located.BlockID != next.BlockID || !reflect.DeepEqual(located.Component, next.Component) {
			delta.ChangedComponents[id] = true
		}
	}
	for id, located := range afterComponents {
		delta.ComponentBlocks[id] = located.BlockID
		if _, exists := beforeComponents[id]; !exists {
			delta.AddedComponents[id] = true
		}
	}
	return delta
}

type locatedBlock struct {
	PageID string
	Index  int
	Block  reportjson.Block
}

type locatedComponent struct {
	PageID, BlockID string
	Index           int
	Component       reportjson.Component
}

func pageMap(pages []reportjson.Page) map[string]reportjson.Page {
	result := make(map[string]reportjson.Page, len(pages))
	for _, page := range pages {
		result[page.ID] = page
	}
	return result
}

func blockMap(document reportjson.Document) map[string]locatedBlock {
	result := map[string]locatedBlock{}
	for _, page := range document.Pages {
		for index, block := range page.Blocks {
			result[block.ID] = locatedBlock{PageID: page.ID, Index: index, Block: block}
		}
	}
	return result
}

func componentMap(document reportjson.Document) map[string]locatedComponent {
	result := map[string]locatedComponent{}
	for _, page := range document.Pages {
		for _, block := range page.Blocks {
			for index, component := range block.Components {
				result[component.ID] = locatedComponent{PageID: page.ID, BlockID: block.ID, Index: index, Component: component}
			}
		}
	}
	return result
}

func touchedBlocks(delta documentDelta) []string {
	set := map[string]bool{}
	for id := range delta.AddedBlocks {
		set[id] = true
	}
	for id := range delta.RemovedBlocks {
		set[id] = true
	}
	for id := range delta.ChangedBlocks {
		set[id] = true
	}
	for id := range delta.AddedComponents {
		set[delta.ComponentBlocks[id]] = true
	}
	for id := range delta.RemovedComponents {
		set[delta.ComponentBlocks[id]] = true
	}
	for id := range delta.ChangedComponents {
		set[delta.ComponentBlocks[id]] = true
	}
	result := make([]string, 0, len(set))
	for id := range set {
		if id != "" {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// validateChangeSemantics 让审计操作类型与真实前后差异一致，禁止用宽泛操作名包裹无关修改。
func validateChangeSemantics(before, after reportjson.Document, change DraftChange) error {
	delta := calculateDelta(before, after)
	if delta.Other && change.OperationType != "LEGACY_DRAFT_RECOVERY" {
		return fmt.Errorf("%w: 语义操作包含报告或页面级无关修改", ErrInvalidPatch)
	}
	beforeBlocks, afterBlocks := blockMap(before), blockMap(after)
	beforeComponents, afterComponents := componentMap(before), componentMap(after)
	target := change.Target
	switch change.OperationType {
	case "BLOCK_MOVE", "BLOCK_RESIZE":
		old, oldOK := beforeBlocks[target.BlockID]
		next, nextOK := afterBlocks[target.BlockID]
		if blockTargetHasComponentFields(target) || !oldOK || !nextOK || old.PageID != target.PageID || next.PageID != target.PageID || !onlyTouchedBlock(delta, target.BlockID) || len(delta.AddedComponents)+len(delta.RemovedComponents) > 0 {
			return semanticTargetError(change.OperationType)
		}
		oldShell, nextShell := old.Block, next.Block
		oldShell.Components, nextShell.Components = nil, nil
		oldGrid, nextGrid := oldShell.Grid, nextShell.Grid
		oldInner, nextInner := oldShell.InnerGrid, nextShell.InnerGrid
		oldShell.Grid, nextShell.Grid = reportjson.Grid{}, reportjson.Grid{}
		oldShell.InnerGrid, nextShell.InnerGrid = reportjson.InnerGrid{}, reportjson.InnerGrid{}
		if !reflect.DeepEqual(oldShell, nextShell) {
			return semanticTargetError(change.OperationType)
		}
		if change.OperationType == "BLOCK_MOVE" {
			if oldGrid.W != nextGrid.W || oldGrid.H != nextGrid.H || oldInner != nextInner || (oldGrid.X == nextGrid.X && oldGrid.Y == nextGrid.Y) || !componentsEqualExceptGrid(old.Block.Components, next.Block.Components, false) {
				return semanticTargetError(change.OperationType)
			}
		} else if oldGrid.W == nextGrid.W && oldGrid.H == nextGrid.H || !componentsEqualExceptGrid(old.Block.Components, next.Block.Components, true) {
			return semanticTargetError(change.OperationType)
		}
	case "BLOCK_CREATE":
		created, ok := afterBlocks[target.BlockID]
		createdComponentID := firstNonBlank(target.CreatedComponentID, target.ComponentID)
		componentTargetInvalid := target.SourceComponentID != "" || target.ReferencedOperationID != "" || target.ComponentID != "" && target.CreatedComponentID != "" && target.ComponentID != target.CreatedComponentID || createdComponentID != "" && !delta.AddedComponents[createdComponentID]
		if target.PageID == "" || target.BlockID == "" || componentTargetInvalid || !ok || created.PageID != target.PageID || beforeBlocks[target.BlockID].Block.ID != "" || !onlyAddedBlock(delta, target.BlockID) {
			return semanticTargetError(change.OperationType)
		}
	case "BLOCK_CLEAR", "BLOCK_DELETE":
		removed, ok := beforeBlocks[target.BlockID]
		if blockTargetHasComponentFields(target) || !ok || removed.PageID != target.PageID || afterBlocks[target.BlockID].Block.ID != "" || !onlyRemovedBlock(delta, target.BlockID) {
			return semanticTargetError(change.OperationType)
		}
	case "BLOCK_STICKY_UPDATE":
		old, oldOK := beforeBlocks[target.BlockID]
		next, nextOK := afterBlocks[target.BlockID]
		if blockTargetHasComponentFields(target) || !oldOK || !nextOK || old.PageID != target.PageID || next.PageID != target.PageID || !onlyTouchedBlock(delta, target.BlockID) || !blockOnlyStickyChanged(old.Block, next.Block) {
			return semanticTargetError(change.OperationType)
		}
	case "COMPONENT_MOVE", "COMPONENT_RESIZE":
		old, oldOK := beforeComponents[target.ComponentID]
		next, nextOK := afterComponents[target.ComponentID]
		if componentTargetHasExtraFields(target) || !componentTargetMatches(target, old, next, oldOK, nextOK) || !onlyTouchedComponent(delta, target.ComponentID, target.BlockID) {
			return semanticTargetError(change.OperationType)
		}
		oldComponent, nextComponent := old.Component, next.Component
		oldGrid, nextGrid := oldComponent.Grid, nextComponent.Grid
		oldComponent.Grid, nextComponent.Grid = reportjson.Grid{}, reportjson.Grid{}
		if !reflect.DeepEqual(oldComponent, nextComponent) {
			return semanticTargetError(change.OperationType)
		}
		if change.OperationType == "COMPONENT_MOVE" {
			if oldGrid.W != nextGrid.W || oldGrid.H != nextGrid.H || oldGrid.X == nextGrid.X && oldGrid.Y == nextGrid.Y {
				return semanticTargetError(change.OperationType)
			}
		} else if oldGrid.W == nextGrid.W && oldGrid.H == nextGrid.H {
			return semanticTargetError(change.OperationType)
		}
	case "COMPONENT_CREATE":
		createdID := firstNonBlank(change.Target.CreatedComponentID, change.Target.ComponentID)
		created, ok := afterComponents[createdID]
		if createdID == "" || target.SourceComponentID != "" || target.ReferencedOperationID != "" || target.ComponentID != "" && target.CreatedComponentID != "" && target.ComponentID != target.CreatedComponentID || !ok || beforeComponents[createdID].Component.ID != "" || target.PageID != created.PageID || target.BlockID != created.BlockID || !onlyAddedComponent(delta, createdID, target.BlockID) {
			return semanticTargetError(change.OperationType)
		}
	case "COMPONENT_COPY":
		sourceID, createdID := target.SourceComponentID, target.CreatedComponentID
		sourceBefore, sourceOK := beforeComponents[sourceID]
		sourceAfter, sourceAfterOK := afterComponents[sourceID]
		created, createdOK := afterComponents[createdID]
		if sourceID == "" || createdID == "" || sourceID == createdID || target.ReferencedOperationID != "" || target.ComponentID != "" && target.ComponentID != createdID || !sourceOK || !sourceAfterOK || !createdOK || beforeComponents[createdID].Component.ID != "" || target.PageID != created.PageID || target.BlockID != created.BlockID || sourceBefore.PageID != target.PageID || sourceBefore.BlockID != target.BlockID || !reflect.DeepEqual(sourceBefore.Component, sourceAfter.Component) || !onlyAddedComponent(delta, createdID, target.BlockID) || !componentIsCopy(sourceBefore.Component, created.Component) {
			return semanticTargetError(change.OperationType)
		}
	case "COMPONENT_DELETE":
		removed, ok := beforeComponents[target.ComponentID]
		if componentTargetHasExtraFields(target) || !ok || afterComponents[target.ComponentID].Component.ID != "" || target.PageID != removed.PageID || target.BlockID != removed.BlockID || !onlyRemovedComponent(delta, target.ComponentID, target.BlockID) {
			return semanticTargetError(change.OperationType)
		}
	case "COMPONENT_STICKY_UPDATE":
		old, oldOK := beforeComponents[target.ComponentID]
		next, nextOK := afterComponents[target.ComponentID]
		if componentTargetHasExtraFields(target) || !componentTargetMatches(target, old, next, oldOK, nextOK) || !onlyTouchedComponent(delta, target.ComponentID, target.BlockID) || !componentOnlyStickyChanged(old.Component, next.Component) {
			return semanticTargetError(change.OperationType)
		}
	case "UNDO", "REDO":
		if target.ReferencedOperationID == "" {
			return semanticTargetError(change.OperationType)
		}
		// 撤销/重做不能成为通用修改后门：去掉引用字段后，真实差异仍须匹配一种正式编辑语义。
		if err := validateCompensatingSemantics(before, after, change, delta); err != nil {
			return err
		}
	case "LEGACY_DRAFT_RECOVERY":
		// 旧会话副本只能通过显式恢复进入服务端，且不能伪装成某个实体级操作。
		if target.PageID != "" || target.BlockID != "" || target.ComponentID != "" || target.SourceComponentID != "" || target.CreatedComponentID != "" || target.ReferencedOperationID != "" {
			return semanticTargetError(change.OperationType)
		}
	default:
		return fmt.Errorf("%w: operationType 不受支持", ErrInvalidRequest)
	}
	return nil
}

func validateCompensatingSemantics(before, after reportjson.Document, change DraftChange, delta documentDelta) error {
	target := change.Target
	target.ReferencedOperationID = ""
	candidates := []struct {
		operationType string
		target        ChangeTarget
	}{}
	beforeBlocks, afterBlocks := blockMap(before), blockMap(after)
	beforeComponents, afterComponents := componentMap(before), componentMap(after)
	for blockID := range delta.AddedBlocks {
		located := afterBlocks[blockID]
		candidate := target
		candidate.PageID, candidate.BlockID = located.PageID, blockID
		candidates = append(candidates, struct {
			operationType string
			target        ChangeTarget
		}{"BLOCK_CREATE", candidate})
	}
	for blockID := range delta.RemovedBlocks {
		located := beforeBlocks[blockID]
		candidates = append(candidates, struct {
			operationType string
			target        ChangeTarget
		}{"BLOCK_DELETE", ChangeTarget{PageID: located.PageID, BlockID: blockID}})
	}
	for blockID := range delta.ChangedBlocks {
		located := afterBlocks[blockID]
		for _, operationType := range []string{"BLOCK_MOVE", "BLOCK_RESIZE", "BLOCK_STICKY_UPDATE"} {
			candidates = append(candidates, struct {
				operationType string
				target        ChangeTarget
			}{operationType, ChangeTarget{PageID: located.PageID, BlockID: blockID}})
		}
	}
	for componentID := range delta.AddedComponents {
		located := afterComponents[componentID]
		candidate := ChangeTarget{PageID: located.PageID, BlockID: located.BlockID, ComponentID: componentID, CreatedComponentID: componentID}
		candidates = append(candidates, struct {
			operationType string
			target        ChangeTarget
		}{"COMPONENT_CREATE", candidate})
		if target.SourceComponentID != "" && target.CreatedComponentID == componentID {
			candidates = append(candidates, struct {
				operationType string
				target        ChangeTarget
			}{"COMPONENT_COPY", target})
		}
	}
	for componentID := range delta.RemovedComponents {
		located := beforeComponents[componentID]
		candidates = append(candidates, struct {
			operationType string
			target        ChangeTarget
		}{"COMPONENT_DELETE", ChangeTarget{PageID: located.PageID, BlockID: located.BlockID, ComponentID: componentID}})
	}
	for componentID := range delta.ChangedComponents {
		located := afterComponents[componentID]
		for _, operationType := range []string{"COMPONENT_MOVE", "COMPONENT_RESIZE", "COMPONENT_STICKY_UPDATE"} {
			candidates = append(candidates, struct {
				operationType string
				target        ChangeTarget
			}{operationType, ChangeTarget{PageID: located.PageID, BlockID: located.BlockID, ComponentID: componentID}})
		}
	}
	for _, candidate := range candidates {
		if validateChangeSemantics(before, after, DraftChange{OperationType: candidate.operationType, Target: candidate.target}) == nil {
			return nil
		}
	}
	return semanticTargetError(change.OperationType)
}

func semanticTargetError(operationType string) error {
	return fmt.Errorf("%w: %s 的 target 与实际变更不一致", ErrInvalidPatch, operationType)
}

func blockTargetHasComponentFields(target ChangeTarget) bool {
	return target.PageID == "" || target.BlockID == "" || target.ComponentID != "" || target.SourceComponentID != "" || target.CreatedComponentID != "" || target.ReferencedOperationID != ""
}

func componentTargetHasExtraFields(target ChangeTarget) bool {
	return target.SourceComponentID != "" || target.CreatedComponentID != "" || target.ReferencedOperationID != ""
}

func onlyTouchedBlock(delta documentDelta, blockID string) bool {
	return len(delta.AddedBlocks) == 0 && len(delta.RemovedBlocks) == 0 && len(delta.ChangedBlocks) == 1 && delta.ChangedBlocks[blockID] && touchedComponentsInside(delta, blockID)
}

func onlyAddedBlock(delta documentDelta, blockID string) bool {
	return len(delta.AddedBlocks) == 1 && delta.AddedBlocks[blockID] && len(delta.RemovedBlocks) == 0 && len(delta.ChangedBlocks) == 0 && touchedComponentsInside(delta, blockID)
}

func onlyRemovedBlock(delta documentDelta, blockID string) bool {
	return len(delta.RemovedBlocks) == 1 && delta.RemovedBlocks[blockID] && len(delta.AddedBlocks) == 0 && len(delta.ChangedBlocks) == 0 && touchedComponentsInside(delta, blockID)
}

func touchedComponentsInside(delta documentDelta, blockID string) bool {
	for id := range delta.AddedComponents {
		if delta.ComponentBlocks[id] != blockID {
			return false
		}
	}
	for id := range delta.RemovedComponents {
		if delta.ComponentBlocks[id] != blockID {
			return false
		}
	}
	for id := range delta.ChangedComponents {
		if delta.ComponentBlocks[id] != blockID {
			return false
		}
	}
	return true
}

func onlyTouchedComponent(delta documentDelta, componentID, blockID string) bool {
	return len(delta.AddedBlocks) == 0 && len(delta.RemovedBlocks) == 0 && len(delta.AddedComponents) == 0 && len(delta.RemovedComponents) == 0 && len(delta.ChangedComponents) == 1 && delta.ChangedComponents[componentID] && len(delta.ChangedBlocks) == 1 && delta.ChangedBlocks[blockID]
}

func onlyAddedComponent(delta documentDelta, componentID, blockID string) bool {
	return len(delta.AddedBlocks) == 0 && len(delta.RemovedBlocks) == 0 && len(delta.AddedComponents) == 1 && delta.AddedComponents[componentID] && len(delta.RemovedComponents) == 0 && len(delta.ChangedComponents) == 0 && len(delta.ChangedBlocks) == 1 && delta.ChangedBlocks[blockID]
}

func onlyRemovedComponent(delta documentDelta, componentID, blockID string) bool {
	return len(delta.AddedBlocks) == 0 && len(delta.RemovedBlocks) == 0 && len(delta.RemovedComponents) == 1 && delta.RemovedComponents[componentID] && len(delta.AddedComponents) == 0 && len(delta.ChangedComponents) == 0 && len(delta.ChangedBlocks) == 1 && delta.ChangedBlocks[blockID]
}

func componentTargetMatches(target ChangeTarget, old, next locatedComponent, oldOK, nextOK bool) bool {
	return target.PageID != "" && target.BlockID != "" && target.ComponentID != "" && oldOK && nextOK && old.PageID == target.PageID && next.PageID == target.PageID && old.BlockID == target.BlockID && next.BlockID == target.BlockID
}

func blockOnlyStickyChanged(old, next reportjson.Block) bool {
	oldSticky, nextSticky := old.Sticky, next.Sticky
	old.Sticky, next.Sticky = nil, nil
	return !reflect.DeepEqual(oldSticky, nextSticky) && reflect.DeepEqual(old, next)
}

func componentOnlyStickyChanged(old, next reportjson.Component) bool {
	oldSticky, nextSticky := old.Sticky, next.Sticky
	old.Sticky, next.Sticky = nil, nil
	return !reflect.DeepEqual(oldSticky, nextSticky) && reflect.DeepEqual(old, next)
}

func componentsEqualExceptGrid(old, next []reportjson.Component, allowGridChanges bool) bool {
	if len(old) != len(next) {
		return false
	}
	for index := range old {
		if old[index].ID != next[index].ID {
			return false
		}
		left, right := old[index], next[index]
		oldGrid, nextGrid := left.Grid, right.Grid
		left.Grid, right.Grid = reportjson.Grid{}, reportjson.Grid{}
		if !reflect.DeepEqual(left, right) || !allowGridChanges && oldGrid != nextGrid {
			return false
		}
	}
	return true
}

func componentIsCopy(source, created reportjson.Component) bool {
	if source.ID == created.ID {
		return false
	}
	source.ID, created.ID = "", ""
	source.Name, created.Name = "", ""
	source.Grid, created.Grid = reportjson.Grid{}, reportjson.Grid{}
	source.ManualLocked, created.ManualLocked = false, false
	return reflect.DeepEqual(source, created)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// validateLockedMutation 始终以请求开始时的旧文档为锁事实，因而同一批次不能先解锁再修改。
func validateLockedMutation(baseline, candidate reportjson.Document) error {
	currentBlocks := blockMap(candidate)
	currentComponents := componentMap(candidate)
	paths := []string{}
	for pageIndex, page := range baseline.Pages {
		for blockIndex, block := range page.Blocks {
			path := fmt.Sprintf("pages[%d].blocks[%d]", pageIndex, blockIndex)
			next, exists := currentBlocks[block.ID]
			if !exists {
				if block.Locks.Layout || block.Locks.Config || block.Locks.DataSnapshot {
					paths = append(paths, path+".locks")
				}
				for componentIndex, component := range block.Components {
					if component.ManualLocked {
						paths = append(paths, fmt.Sprintf("%s.components[%d].manualLocked", path, componentIndex))
					}
				}
				continue
			}
			if block.Locks.Layout && !reflect.DeepEqual(blockLayoutProjection(page.ID, block), blockLayoutProjection(next.PageID, next.Block)) {
				paths = append(paths, path+".locks.layout")
			}
			if block.Locks.Config && !reflect.DeepEqual(blockConfigProjection(block), blockConfigProjection(next.Block)) {
				paths = append(paths, path+".locks.config")
			}
			if block.Locks.DataSnapshot && !reflect.DeepEqual(blockDataProjection(block), blockDataProjection(next.Block)) {
				paths = append(paths, path+".locks.dataSnapshot")
			}
			for componentIndex, component := range block.Components {
				if !component.ManualLocked {
					continue
				}
				nextComponent, exists := currentComponents[component.ID]
				if !exists || !reflect.DeepEqual(componentManualProjection(component), componentManualProjection(nextComponent.Component)) {
					paths = append(paths, fmt.Sprintf("%s.components[%d].manualLocked", path, componentIndex))
				}
			}
		}
	}
	if len(paths) > 0 {
		sort.Strings(paths)
		return &LockedError{Paths: paths}
	}
	return nil
}

func blockLayoutProjection(pageID string, block reportjson.Block) any {
	components := make([]any, 0, len(block.Components))
	for _, component := range block.Components {
		components = append(components, struct {
			ID   string
			Grid reportjson.Grid
		}{component.ID, component.Grid})
	}
	return struct {
		PageID     string
		Grid       reportjson.Grid
		InnerGrid  reportjson.InnerGrid
		Components []any
	}{pageID, block.Grid, block.InnerGrid, components}
}

func blockConfigProjection(block reportjson.Block) any {
	components := make([]reportjson.Component, len(block.Components))
	copy(components, block.Components)
	for index := range components {
		components[index].Grid = reportjson.Grid{}
		components[index].ManualLocked = false
		components[index].Binding = nil
		components[index].RefreshPolicy = nil
		components[index].SourceTrace = nil
		components[index].Conclusion = nil
	}
	return struct {
		ZIndex           int
		Sticky           *reportjson.Sticky
		Style            map[string]any
		PermissionPolicy *reportjson.PermissionPolicy
		Components       []reportjson.Component
	}{block.ZIndex, block.Sticky, block.Style, block.PermissionPolicy, components}
}

func blockDataProjection(block reportjson.Block) any {
	result := make([]any, 0, len(block.Components))
	for _, component := range block.Components {
		result = append(result, struct {
			ID            string
			Binding       map[string]any
			RefreshPolicy map[string]any
			SourceTrace   []reportjson.SourceTrace
			Conclusion    map[string]any
		}{component.ID, component.Binding, component.RefreshPolicy, component.SourceTrace, component.Conclusion})
	}
	return result
}

func componentManualProjection(component reportjson.Component) reportjson.Component {
	component.ManualLocked = false
	return component
}
