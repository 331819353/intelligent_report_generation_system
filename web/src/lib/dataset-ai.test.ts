import assert from 'node:assert/strict'
import test from 'node:test'
import { datasetAICanvasMode, datasetAIRequestContext } from './dataset-ai-context.ts'

const plan = (name: string) => ({
  dataset: { name, description: `${name} description` },
  nodes: [{ id: `${name}-node`, tableId: `${name}-table`, alias: name, selectedColumns: ['id'] }],
  joins: [],
  groups: [],
  transforms: [],
  end: {
    name: `${name} output`,
    input: { kind: 'NODE' as const, id: `${name}-node` },
    outputs: [{ nodeId: `${name}-node`, column: 'id', name: 'ID', code: 'id' }],
  },
})

test('only a completely empty canvas uses create intake', () => {
  assert.equal(datasetAICanvasMode({ nodes: 0, joins: 0, groups: 0, transforms: 0, hasEnd: false }), 'CREATE')
  assert.equal(datasetAICanvasMode({ nodes: 1, joins: 0, groups: 0, transforms: 0, hasEnd: false }), 'MODIFY')
  assert.equal(datasetAICanvasMode({ nodes: 0, joins: 0, groups: 1, transforms: 0, hasEnd: false }), 'MODIFY')
  assert.equal(datasetAICanvasMode({ nodes: 0, joins: 0, groups: 0, transforms: 0, hasEnd: true }), 'MODIFY')
})

test('continues a conversation from the latest staged proposal', () => {
  const liveCanvas = plan('live')
  const stagedProposal = plan('staged')
  assert.equal(datasetAIRequestContext(liveCanvas, stagedProposal, {
    forceLiveCanvas: false,
    stagedProposalApplied: false,
    preferStagedProposal: true,
  }), stagedProposal)
})

test('uses the live canvas when a manual edit invalidates the staged baseline', () => {
  const liveCanvas = plan('live')
  const stagedProposal = plan('staged')
  assert.equal(datasetAIRequestContext(liveCanvas, stagedProposal, {
    forceLiveCanvas: true,
    stagedProposalApplied: false,
    preferStagedProposal: true,
  }), liveCanvas)
})

test('uses the live canvas after the staged proposal has been applied', () => {
  const liveCanvas = plan('live')
  const stagedProposal = plan('staged')
  assert.equal(datasetAIRequestContext(liveCanvas, stagedProposal, {
    forceLiveCanvas: false,
    stagedProposalApplied: true,
    preferStagedProposal: true,
  }), liveCanvas)
})

test('uses a staged proposal when the canvas is still empty', () => {
  const stagedProposal = plan('staged')
  assert.equal(datasetAIRequestContext(undefined, stagedProposal, {
    forceLiveCanvas: false,
    stagedProposalApplied: false,
  }), stagedProposal)
})
