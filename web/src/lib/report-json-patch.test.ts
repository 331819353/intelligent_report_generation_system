import { describe, expect, test } from 'vitest'
import { applyReportJSONPatch, createReportJSONPatch, MAX_REPORT_JSON_PATCH_OPERATIONS, ReportJSONPatchError, type ReportJSONPatchOperation } from './report-json-patch'

describe('报告 JSON Patch', () => {
  test('正向和逆向补丁可完整往返且不会修改输入', () => {
    const before = { pages: [{ id: 'page/1', blocks: [{ id: 'a', grid: { x: 0 } }, { id: 'b', grid: { x: 1 } }] }], theme: { color: 'blue' } }
    const after = { pages: [{ id: 'page/1', blocks: [{ id: 'a', grid: { x: 2 } }, { id: 'c', grid: { x: 3 } }] }], theme: { color: 'red' } }
    const untouched = structuredClone(before)
    const patches = createReportJSONPatch(before, after)

    const applied = applyReportJSONPatch(before, patches.forward)
    expect(applied).toEqual(after)
    expect(applyReportJSONPatch(applied, patches.inverse)).toEqual(before)
    expect(before).toEqual(untouched)
    expect(applied).not.toBe(after)
  })

  test('数组删除保留公共首尾并从尾部安全删除', () => {
    const { forward } = createReportJSONPatch(
      { components: [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }] },
      { components: [{ id: 'a' }, { id: 'd' }] },
    )
    expect(forward).toEqual([
      { op: 'remove', path: '/components/2' },
      { op: 'remove', path: '/components/1' },
    ])
  })

  test('严格解析 JSON Pointer 转义并支持数组追加', () => {
    const source = { 'a/b': { '~key': [1] } }
    const result = applyReportJSONPatch(source, [
      { op: 'replace', path: '/a~1b/~0key/0', value: 2 },
      { op: 'add', path: '/a~1b/~0key/-', value: 3 },
    ])
    expect(result).toEqual({ 'a/b': { '~key': [2, 3] } })
    expect(source).toEqual({ 'a/b': { '~key': [1] } })
  })

  test.each([
    [{ op: 'replace', path: 'missing-slash', value: 1 }],
    [{ op: 'replace', path: '/bad~2escape', value: 1 }],
    [{ op: 'replace', path: '/items/01', value: 1 }],
    [{ op: 'remove', path: '' }],
    [{ op: 'move', path: '/value', from: '/old' }],
    [{ op: 'add', path: '/value' }],
    [{ op: 'remove', path: '/value', value: 1 }],
  ])('拒绝非法操作 %#', patch => {
    expect(() => applyReportJSONPatch({ items: [1], value: 1 }, patch as unknown as ReportJSONPatchOperation[])).toThrow(ReportJSONPatchError)
  })

  test('操作失败保持输入和值对象不变', () => {
    const source = { nested: { value: 1 } }
    const added = { deep: { value: 2 } }
    expect(() => applyReportJSONPatch(source, [
      { op: 'add', path: '/copy', value: added },
      { op: 'replace', path: '/missing', value: 3 },
    ])).toThrow('目标属性不存在')
    expect(source).toEqual({ nested: { value: 1 } })
    expect(added).toEqual({ deep: { value: 2 } })
  })

  test('拒绝非 JSON 数值和循环引用', () => {
    expect(() => createReportJSONPatch({ value: 1 }, { value: Number.NaN })).toThrow('有限值')
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    expect(() => createReportJSONPatch({}, cyclic)).toThrow('循环引用')
  })

  test('对象键 __proto__ 不会污染目标原型', () => {
    const value = JSON.parse('{"polluted":true}') as Record<string, unknown>
    const result = applyReportJSONPatch({}, [{ op: 'add', path: '/__proto__', value }]) as Record<string, unknown>
    expect(Object.hasOwn(result, '__proto__')).toBe(true)
    expect(({} as Record<string, unknown>).polluted).toBeUndefined()
  })

  test('过多原子变化会压缩为最近子树替换并保持可逆', () => {
    const before = { config: Object.fromEntries(Array.from({ length: MAX_REPORT_JSON_PATCH_OPERATIONS + 1 }, (_, index) => [`key_${index}`, index])) }
    const after = { config: Object.fromEntries(Array.from({ length: MAX_REPORT_JSON_PATCH_OPERATIONS + 1 }, (_, index) => [`key_${index}`, index + 1])) }
    const patches = createReportJSONPatch(before, after)

    expect(patches.forward).toEqual([{ op: 'replace', path: '/config', value: after.config }])
    expect(applyReportJSONPatch(before, patches.forward)).toEqual(after)
    expect(applyReportJSONPatch(after, patches.inverse)).toEqual(before)
  })
})
