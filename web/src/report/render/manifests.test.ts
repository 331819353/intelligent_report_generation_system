import assert from 'node:assert/strict'
import test from 'node:test'
import { editorBindingsValid, latestComponentManifests, type ComponentManifest } from './manifests.ts'

function manifest(type: string, version: string): ComponentManifest {
  return {
    type, version, renderer: 'REACT', displayName: type, category: 'CHART',
    minSize: { w: 2, h: 2 }, recommendedSize: { w: 6, h: 3 },
    dataContract: {
      dimensions: { min: 0, max: 1 }, measures: { min: 1, max: 9 },
      roles: ['LABEL', 'COLOR', 'VALUE', 'TOOLTIP'],
    },
    optionSchema: { type: 'object', additionalProperties: false, required: [], properties: {} },
    defaultOptions: {},
    mobilePolicy: { supported: true, defaultLegendMode: 'HIDDEN', labelDegradation: 'ELLIPSIS' },
    supportedInteractions: [],
    editorProfile: {
      componentType: type, componentVersion: version,
      example: { title: '多指标卡', description: '按组展示', items: [] },
      bindingGroups: [
        { id: 'label', label: '范围', description: '可选', kind: 'DIMENSION', roles: ['LABEL', 'COLOR'], min: 0, max: 1, addLabel: '添加范围' },
        { id: 'primary', label: '核心数值', description: '主值', kind: 'MEASURE', roles: ['VALUE'], min: 1, max: 3, addLabel: '添加核心数值' },
        { id: 'comparison', label: '说明指标', description: '同比环比', kind: 'MEASURE', roles: ['TOOLTIP'], min: 0, max: 6, addLabel: '添加说明', nestedUnder: 'primary', maxPerParent: 2 },
      ],
    },
  }
}

test('component library keeps only the newest exact version per type', () => {
  const oldMetric = manifest('metric-card', '1.0.0')
  const newMetric = manifest('metric-card', '1.1.0')
  const bar = manifest('bar-comparison', '1.0.0')
  assert.deepEqual(latestComponentManifests([oldMetric, bar, newMetric]).map(item => `${item.type}@${item.version}`).sort(), [
    'bar-comparison@1.0.0', 'metric-card@1.1.0',
  ])
})

test('KPI companion metrics must follow their primary metric and stay within each group', () => {
  const metric = manifest('metric-card', '1.1.0')
  assert.equal(editorBindingsValid(metric, [], [
    { role: 'VALUE', field: 'revenue' }, { role: 'TOOLTIP', field: 'revenue_yoy' },
    { role: 'VALUE', field: 'profit' }, { role: 'TOOLTIP', field: 'profit_mom' },
  ]), true)
  assert.equal(editorBindingsValid(metric, [], [
    { role: 'TOOLTIP', field: 'revenue_yoy' }, { role: 'VALUE', field: 'revenue' },
  ]), false)
  assert.equal(editorBindingsValid(metric, [], [
    { role: 'VALUE', field: 'revenue' },
    { role: 'TOOLTIP', field: 'revenue_yoy' }, { role: 'TOOLTIP', field: 'revenue_mom' }, { role: 'TOOLTIP', field: 'revenue_target' },
  ]), false)
})
