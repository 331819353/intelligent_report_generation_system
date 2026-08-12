import assert from 'node:assert/strict'
import test from 'node:test'

import { describeSelections, type ReportSelection } from './runtime-state.ts'

// useReportRuntimeState 是 React Hook，这里覆盖它导出的纯函数与其状态规则的
// 核心不变量；Hook 的组合行为由运行页的类型检查与端到端使用覆盖。

test('selection descriptions state only the values actually sent to the server', () => {
  const selections: ReportSelection[] = [
    { componentId: 'a', values: { channel: '线上' } },
    { componentId: 'b', values: { region: ['华东', '华南'] } },
  ]
  const text = describeSelections(selections)
  assert.equal(text, 'channel = 线上；region = 华东、华南')
  // 描述里不应出现组件 ID 之类的内部标识。
  assert.equal(text.includes('componentId'), false)
})

test('an empty selection list describes nothing', () => {
  assert.equal(describeSelections([]), '')
})
