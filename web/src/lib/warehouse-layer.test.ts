import assert from 'node:assert/strict'
import test from 'node:test'
import { layerFromTags, replaceLayerTag } from './warehouse-layer.ts'

test('layerFromTags reads the governed layer tag case-insensitively', () => {
  assert.equal(layerFromTags(['作用:事实表', '层级:dws']), 'DWS')
  assert.equal(layerFromTags(['作用:事实表']), undefined)
  assert.equal(layerFromTags(['层级:XYZ']), undefined)
  assert.equal(layerFromTags(undefined), undefined)
})

test('replaceLayerTag keeps other tags and enforces a single layer tag', () => {
  assert.deepEqual(replaceLayerTag(['层级:ODS', '主题:订单', '层级:DWS'], 'ADS'), ['主题:订单', '层级:ADS'])
  assert.deepEqual(replaceLayerTag(['层级:ODS', '主题:订单'], ''), ['主题:订单'])
})
