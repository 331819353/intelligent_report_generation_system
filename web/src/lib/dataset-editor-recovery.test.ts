import assert from 'node:assert/strict'
import test from 'node:test'
import { loadDatasetEditorRecovery, removeDatasetEditorRecovery, saveDatasetEditorRecovery, type DatasetEditorRecovery } from './dataset-editor-recovery.ts'

const memoryStorage = (): Storage => {
  const values = new Map<string, string>()
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: key => values.get(key) ?? null,
    key: index => [...values.keys()][index] ?? null,
    removeItem: key => { values.delete(key) },
    setItem: (key, value) => { values.set(key, value) },
  }
}

const recovery = (): DatasetEditorRecovery => ({
  schemaVersion: 1,
  datasetID: 'dataset-1',
  datasetVersion: 3,
  generatedCode: 'dataset_orders',
  savedAt: '2026-08-14T10:00:00.000Z',
  snapshot: {
    draft: { code: '', name: '', description: '', nodes: [], fields: [], joins: [], filters: [], parameters: [], calculations: [], sorts: [], grainDescription: '', grainKeys: [] },
    relationBoxes: [], groupBoxes: [], transformBoxes: [], endBox: null, nodePositions: {},
    metadata: { name: '订单草稿', description: '', domain: '经营', subject: '' },
  },
})

test('dataset editor recoveries are isolated by domain and dataset', () => {
  const storage = memoryStorage()
  assert.equal(saveDatasetEditorRecovery('domain-a', recovery(), storage), true)
  assert.equal(loadDatasetEditorRecovery('domain-a', 'dataset-1', storage)?.generatedCode, 'dataset_orders')
  assert.equal(loadDatasetEditorRecovery('domain-b', 'dataset-1', storage), null)
})

test('dataset editor recovery can be removed after publication', () => {
  const storage = memoryStorage()
  saveDatasetEditorRecovery('domain-a', recovery(), storage)
  removeDatasetEditorRecovery('domain-a', 'dataset-1', storage)
  assert.equal(loadDatasetEditorRecovery('domain-a', 'dataset-1', storage), null)
})
