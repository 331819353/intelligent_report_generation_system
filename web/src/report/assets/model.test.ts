import assert from 'node:assert/strict'
import test from 'node:test'
import { reportAssetFixtures } from './fixtures.ts'
import { canRun, filterAssets, lifecycleLabels } from './model.ts'

test('asset center filters fixed-domain visible reports without inventing actions', () => {
  assert.deepEqual(filterAssets(reportAssetFixtures, 'mine', 'ALL', '').map(item => item.name), ['供应链月度经营报告'])
  assert.deepEqual(filterAssets(reportAssetFixtures, 'all', 'OFFLINE', '').map(item => item.name), ['渠道复盘报告'])
  assert.deepEqual(filterAssets(reportAssetFixtures, 'all', 'ALL', 'cost').map(item => item.name), ['质量成本分析'])
  assert.equal(canRun(reportAssetFixtures[1], 'PUBLISH'), false)
  assert.equal(lifecycleLabels.CHANGED, '有未发布修改')
})
