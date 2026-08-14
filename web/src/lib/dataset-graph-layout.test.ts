import assert from 'node:assert/strict'
import test from 'node:test'

import { graphDownstreamTargets, type DesignerGraphV1 } from './dataset-graph.ts'

const graph: DesignerGraphV1 = {
  version: '1.0',
  nodePositions: {
    node_1: { x: 42, y: 48 },
    node_2: { x: 42, y: 198 },
  },
  nodeNames: { node_1: '订单', node_2: '客户' },
  joins: [{
    id: 'join_1', name: '订单关联客户',
    left: { kind: 'TRANSFORM', id: 'transform_1' },
    right: { kind: 'NODE', id: 'node_2' },
    position: { x: 642, y: 123 }, outputKeys: [],
  }],
  groups: [{
    id: 'group_1', name: '按区域汇总', input: { kind: 'JOIN', id: 'join_1' },
    position: { x: 942, y: 123 }, dimensions: [], metrics: [],
  }],
  transforms: [
    { id: 'transform_1', name: '订单过滤', family: 'CONDITION', input: { kind: 'NODE', id: 'node_1' }, position: { x: 342, y: 48 }, rules: [] },
    { id: 'transform_unrelated', name: '无关分支', family: 'TEXT', input: { kind: 'NODE', id: 'node_2' }, position: { x: 342, y: 348 }, rules: [] },
  ],
  end: { id: 'end_1', name: '最终输出', input: { kind: 'GROUP', id: 'group_1' }, position: { x: 1242, y: 123 }, outputs: [] },
}

test('downstream layout movement follows only the inserted edge branch', () => {
  assert.deepEqual(graphDownstreamTargets({ kind: 'JOIN', id: 'join_1' }, graph), [
    { kind: 'JOIN', id: 'join_1' },
    { kind: 'GROUP', id: 'group_1' },
    { kind: 'OUTPUT', id: 'end_1' },
  ])
})

test('inserting before the first transform moves the complete remaining chain', () => {
  assert.deepEqual(graphDownstreamTargets({ kind: 'TRANSFORM', id: 'transform_1' }, graph), [
    { kind: 'TRANSFORM', id: 'transform_1' },
    { kind: 'JOIN', id: 'join_1' },
    { kind: 'GROUP', id: 'group_1' },
    { kind: 'OUTPUT', id: 'end_1' },
  ])
})

test('output insertion moves only the output component', () => {
  assert.deepEqual(graphDownstreamTargets({ kind: 'OUTPUT', id: 'end_1' }, graph), [
    { kind: 'OUTPUT', id: 'end_1' },
  ])
})
