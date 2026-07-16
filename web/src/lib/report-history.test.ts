import reportExample from '../../../api/examples/report-json-v1.json'
import { expect, test } from 'vitest'
import type { ReportDocument } from './report-contract'
import { acknowledgeReportEditorChanges, commitReportEditorHistory, createReportEditorHistory, MAX_EDITOR_HISTORY_ENTRIES, redoReportEditorHistory, undoReportEditorHistory } from './report-history'

function snapshot(operation: string, x = 0, minimumRows = 14) {
  const document = structuredClone(reportExample) as unknown as ReportDocument
  document.pages[0].blocks[0].grid.x = x
  return { document, minimumRowsByPage: { page_overview: minimumRows }, operation }
}

test('统一历史用正逆 Patch 撤销重做并在新分支清空未来记录', () => {
  const initial = createReportEditorHistory(snapshot('初始状态'))
  const changed = commitReportEditorHistory(initial, snapshot('移动分块', 1), {
    operationType: 'BLOCK_MOVE', summary: '移动分块', target: { pageId: 'page_overview', blockId: 'block_overview' }, clientOperationId: 'move-1',
  })
  expect(changed.past[0]).not.toHaveProperty('document')
  expect(changed.past[0].forwardPatch).toEqual([{ op: 'replace', path: '/pages/0/blocks/0/grid/x', value: 1 }])
  expect(initial.present.document.pages[0].blocks[0].grid.x).toBe(0)

  const undone = undoReportEditorHistory(changed)
  expect(undone.present.document.pages[0].blocks[0].grid.x).toBe(0)
  expect(undone.pendingChanges.at(-1)).toMatchObject({ operationType: 'UNDO', target: { referencedOperationId: 'move-1' } })

  const redone = redoReportEditorHistory(undone)
  expect(redone.present.document.pages[0].blocks[0].grid.x).toBe(1)
  expect(redone.pendingChanges.at(-1)).toMatchObject({ operationType: 'REDO', target: { referencedOperationId: 'move-1' } })
  expect(commitReportEditorHistory(undone, snapshot('删除分块', 2), { operationType: 'BLOCK_DELETE', summary: '删除分块' }).future).toEqual([])
})

test('历史只保留最多 100 条 Patch，但未保存审计变化不会随之丢失', () => {
  let history = createReportEditorHistory(snapshot('初始状态'))
  for (let index = 0; index < MAX_EDITOR_HISTORY_ENTRIES + 5; index += 1) {
    history = commitReportEditorHistory(history, snapshot(`操作 ${index}`, index + 1), {
      operationType: 'BLOCK_MOVE', summary: `操作 ${index}`, clientOperationId: `operation-${index}`,
    })
  }
  expect(history.past).toHaveLength(MAX_EDITOR_HISTORY_ENTRIES)
  expect(history.pendingChanges).toHaveLength(MAX_EDITOR_HISTORY_ENTRIES + 5)
})

test('编辑态最小行数进入撤销状态但不进入服务端 Patch', () => {
  const initial = createReportEditorHistory(snapshot('初始状态'))
  const changed = commitReportEditorHistory(initial, snapshot('扩展空白区域', 0, 20), {
    operationType: 'BLOCK_MOVE', summary: '扩展空白区域', clientOperationId: 'ui-only',
  })
  expect(changed.past).toHaveLength(1)
  expect(changed.past[0].forwardPatch).toEqual([])
  expect(changed.pendingChanges).toEqual([])
  expect(undoReportEditorHistory(changed).present.minimumRowsByPage.page_overview).toBe(14)
})

test('保存确认只移除请求中已落库的变化', () => {
  let history = createReportEditorHistory(snapshot('初始状态'))
  history = commitReportEditorHistory(history, snapshot('第一次', 1), { operationType: 'BLOCK_MOVE', summary: '第一次', clientOperationId: 'first' })
  history = commitReportEditorHistory(history, snapshot('第二次', 2), { operationType: 'BLOCK_MOVE', summary: '第二次', clientOperationId: 'second' })
  const acknowledged = acknowledgeReportEditorChanges(history, ['first'])
  expect(acknowledged.pendingChanges.map(change => change.clientOperationId)).toEqual(['second'])
})

test('提交会深拷贝文档、目标与 Patch 值', () => {
  const initialSnapshot = snapshot('初始状态')
  const nextSnapshot = snapshot('移动', 1)
  const target = { pageId: 'page_overview', blockId: 'block_overview' }
  const history = commitReportEditorHistory(createReportEditorHistory(initialSnapshot), nextSnapshot, {
    operationType: 'BLOCK_MOVE', summary: '移动', target, clientOperationId: 'immutable',
  })
  nextSnapshot.document.pages[0].blocks[0].grid.x = 9
  target.blockId = 'changed'
  expect(history.present.document.pages[0].blocks[0].grid.x).toBe(1)
  expect(history.pendingChanges[0].target?.blockId).toBe('block_overview')
})
