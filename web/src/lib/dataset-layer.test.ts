import assert from 'node:assert/strict'
import test from 'node:test'
import { chooseDatasetLayers, datasetLineage, type LayerChoice, type LayerChoiceDraft } from './dataset-layer.ts'

const draftWithLayers = (
  layers: Array<LayerChoice | 'PHYSICAL'>,
  joins: unknown[] = [],
): LayerChoiceDraft => ({
  joins,
  nodes: layers.map(layer => ({
    table: {
      ...(layer === 'PHYSICAL'
        ? { sourceKind: 'TABLE' as const }
        : { sourceKind: 'DATASET' as const, datasetLayer: layer }),
    },
  })),
})

test('a single physical table is SOURCE lineage and may declare any layer, defaulting to ODS', () => {
  const draft = draftWithLayers(['PHYSICAL'])
  assert.equal(datasetLineage(draft), 'SOURCE')
  assert.deepEqual(chooseDatasetLayers(draft), ['ODS', 'DIM', 'DWD', 'DWS', 'ADS'])
})

test('a physical table with a governed layer tag defaults to that layer', () => {
  const draft: LayerChoiceDraft = { nodes: [{ table: { sourceKind: 'TABLE', tags: ['主题:经营分析', '层级:ADS'] } }] }
  assert.deepEqual(chooseDatasetLayers(draft), ['ADS', 'ODS', 'DIM', 'DWD', 'DWS'])
})

test('joined physical tables are MODELED lineage and cannot pick a layer directly', () => {
  const draft = draftWithLayers(['PHYSICAL', 'PHYSICAL'], [{ id: 'join_1' }])
  assert.equal(datasetLineage(draft), 'MODELED')
  assert.throws(() => chooseDatasetLayers(draft), /分层建模只能引用已发布数据集版本/)
})

test('modeled lineage still follows ODS to DIM/DWD to DWS to ADS', () => {
  assert.equal(datasetLineage(draftWithLayers(['ODS'])), 'MODELED')
  assert.deepEqual(chooseDatasetLayers(draftWithLayers(['ODS'])), ['DWD', 'DIM'])
  assert.deepEqual(chooseDatasetLayers(draftWithLayers(['ODS', 'DIM'])), ['DWD'])
  assert.deepEqual(chooseDatasetLayers(draftWithLayers(['DWD'])), ['DWS'])
  assert.deepEqual(chooseDatasetLayers(draftWithLayers(['DWD', 'DIM'])), ['DWS'])
  assert.deepEqual(chooseDatasetLayers(draftWithLayers(['DWS'])), ['ADS'])
  assert.throws(() => chooseDatasetLayers(draftWithLayers(['ADS'])), /不符合/)
})
