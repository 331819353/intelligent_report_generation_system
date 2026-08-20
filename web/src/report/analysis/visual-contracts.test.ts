import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { analysisCardVisualContract, analysisCardVisualContracts } from './visual-contracts.ts'

const catalog = JSON.parse(readFileSync(new URL('./analysis-card-catalog.json', import.meta.url), 'utf8')) as Array<{ id: number; name: string }>

test('visual contracts cover every category and all three source variants', () => {
  assert.equal(analysisCardVisualContracts.length, 108)
  for (const item of catalog) {
    for (const variant of ['01', '02', '03'] as const) {
      assert.ok(analysisCardVisualContract(item.id, variant), `${item.id}-${variant}`)
    }
  }
})

test('visual contracts keep the three source motifs distinct inside every category', () => {
  for (const item of catalog) {
    const motifs = (['01', '02', '03'] as const).map(variant => analysisCardVisualContract(item.id, variant)?.motif)
    assert.equal(new Set(motifs).size, 3, item.name)
  }
})
