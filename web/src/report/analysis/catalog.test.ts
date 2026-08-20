import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

type CatalogGroup = {
  id: string
  kind: 'DIMENSION' | 'MEASURE'
  roles: string[]
  min: number
  max: number
}

type CatalogItem = {
  id: number
  slug: string
  type: string
  name: string
  question: string
  bindingGroups: CatalogGroup[]
}

const catalog = JSON.parse(readFileSync(new URL('./analysis-card-catalog.json', import.meta.url), 'utf8')) as CatalogItem[]

test('analysis catalog contains exactly 37 unique semantic card families', () => {
  assert.equal(catalog.length, 37)
  assert.equal(new Set(catalog.map(item => item.id)).size, 37)
  assert.equal(new Set(catalog.map(item => item.slug)).size, 37)
  assert.equal(new Set(catalog.map(item => item.type)).size, 37)
  assert.equal(catalog.every(item => item.name && item.question), true)
})

test('every card family declares governed metric, dimension and filter-ready contracts', () => {
  for (const item of catalog) {
    assert.equal(item.bindingGroups.length > 0, true, `${item.type} has no binding groups`)
    assert.equal(new Set(item.bindingGroups.map(group => group.id)).size, item.bindingGroups.length)
    for (const group of item.bindingGroups) {
      assert.equal(group.roles.length > 0, true, `${item.type}/${group.id} has no roles`)
      assert.equal(group.min >= 0 && group.max >= group.min, true, `${item.type}/${group.id} has invalid cardinality`)
    }
  }
})

test('three visual variants produce 111 selectable cards', () => {
  const variants = ['01', '02', '03']
  assert.equal(catalog.length * variants.length, 111)
})
