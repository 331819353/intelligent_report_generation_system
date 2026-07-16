import reportExample from '../../../../api/examples/report-json-v1.json'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, test, vi } from 'vitest'
import { demoReportRuntime } from '../../lib/demo-report-runtime'
import type { ReportDocument } from '../../lib/report-contract'
import { REPORT_COMPONENT_DRAG_MIME } from './componentDrag'
import { ReportDesignerCanvas } from './ReportDesignerCanvas'

beforeEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

test('键盘移动遵循网格吸附并拒绝碰撞', () => {
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} />)
  const block = screen.getByLabelText('分块 block_overview，位置 1 列 1 行，尺寸 12 × 10')
  fireEvent.keyDown(block, { key: 'ArrowDown' })
  expect(screen.getByLabelText('分块 block_overview，位置 1 列 2 行，尺寸 12 × 10')).toBeInTheDocument()
  fireEvent.keyDown(block, { key: 'ArrowDown' })
  expect(screen.getByRole('alert')).toHaveTextContent('分块与 block_source_note 重叠')
  expect(screen.getByLabelText('分块 block_overview，位置 1 列 2 行，尺寸 12 × 10')).toBeInTheDocument()
})

test('键盘缩放更新草稿并通知调用方', () => {
  const onChange = vi.fn()
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} onChange={onChange} />)
  const block = screen.getByLabelText('分块 block_overview，位置 1 列 1 行，尺寸 12 × 10')
  fireEvent.keyDown(block, { key: 'ArrowLeft', shiftKey: true })
  expect(screen.getByLabelText('分块 block_overview，位置 1 列 1 行，尺寸 11 × 10')).toBeInTheDocument()
  expect(onChange).toHaveBeenCalledTimes(1)
})

test('移动长页面分块会同步扩展内容行数', () => {
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} />)
  const block = screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3')
  fireEvent.keyDown(block, { key: 'ArrowDown' })
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 13 行，尺寸 12 × 3')).toBeInTheDocument()
  expect(document.querySelector('.report-page-canvas')).toHaveAttribute('data-content-rows', '15')
})

test('只有 loadGeneration 变化才用新的服务端草稿重建内存历史', () => {
  const remote = structuredClone(reportExample) as unknown as ReportDocument
  remote.pages[0].blocks[1].grid.y = 15
  remote.pages[0].contentGridRows = 18
  const first = render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} loadGeneration={1} />)
  fireEvent.keyDown(screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3'), { key: 'ArrowDown' })
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 13 行，尺寸 12 × 3')).toBeInTheDocument()

  first.rerender(<ReportDesignerCanvas source={remote} runtime={demoReportRuntime} loadGeneration={1} />)
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 13 行，尺寸 12 × 3')).toBeInTheDocument()

  first.rerender(<ReportDesignerCanvas source={remote} runtime={demoReportRuntime} loadGeneration={2} />)
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 16 行，尺寸 12 × 3')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '撤销' })).toBeDisabled()
})

test('组件支持键盘移动并拒绝与同分块组件碰撞', () => {
  const source = editableComponentSource()
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} />)
  const chart = screen.getByLabelText('组件 营业收入趋势，位置 1 列 5 行，尺寸 12 × 8')
  fireEvent.keyDown(chart, { key: 'ArrowRight' })
  expect(screen.getByLabelText('组件 营业收入趋势，位置 2 列 5 行，尺寸 12 × 8')).toBeInTheDocument()
  fireEvent.keyDown(chart, { key: 'ArrowUp' })
  expect(screen.getByRole('alert')).toHaveTextContent('组件与 title_main 重叠')
})

test('组件可以复制和删除', () => {
  render(<ReportDesignerCanvas source={editableComponentSource()} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '复制组件 营业收入趋势' }))
  expect(document.querySelector('[data-component-id="component_chart_1"]')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '删除组件 营业收入趋势 副本' }))
  expect(document.querySelector('[data-component-id="component_chart_1"]')).not.toBeInTheDocument()
})

test('从组件面板拖入的类型在目标内网格创建组件', () => {
  const source = editableComponentSource()
  source.pages[0].blocks[0].components = []
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} />)
  const block = screen.getByLabelText('分块 block_overview，位置 1 列 1 行，尺寸 12 × 10')
  vi.spyOn(block, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, right: 960, bottom: 540, width: 960, height: 540, x: 0, y: 0, toJSON: () => ({}) })
  fireEvent.drop(block, {
    clientX: 40,
    clientY: 27,
    dataTransfer: { getData: (type: string) => type === REPORT_COMPONENT_DRAG_MIME ? 'KPI' : '' },
  })
  expect(screen.getByLabelText('组件 指标卡，位置 1 列 1 行，尺寸 12 × 8')).toBeInTheDocument()
})

test('键盘或触屏选择组件后可点击分块空白处插入', () => {
  const source = editableComponentSource()
  source.pages[0].blocks[0].components = []
  const consumed = vi.fn()
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} pendingComponentType="FILTER" onPendingComponentConsumed={consumed} />)
  fireEvent.click(screen.getByLabelText('分块 block_overview，位置 1 列 1 行，尺寸 12 × 10'), { clientX: 0, clientY: 0 })
  expect(screen.getByLabelText('组件 筛选器，位置 1 列 1 行，尺寸 12 × 4')).toBeInTheDocument()
  expect(consumed).toHaveBeenCalledTimes(1)
})

test('非空分块清空前确认组件数量并可撤销重做', () => {
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '清空分块 block_source_note' }))
  expect(screen.getByRole('dialog', { name: '清空分块' })).toHaveTextContent('将移除该分块中的 1 个组件')
  fireEvent.click(screen.getByRole('button', { name: '确认清空' }))
  expect(document.querySelector('[data-block-id="block_source_note"]')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '空白单元，第 1 列，第 12 行' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '撤销' }))
  expect(document.querySelector('[data-block-id="block_source_note"]')).toBeInTheDocument()
  expect(document.querySelector('[data-component-id="source_note"]')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '重做' }))
  expect(document.querySelector('[data-block-id="block_source_note"]')).not.toBeInTheDocument()
})

test('取消确认不会修改非空分块', () => {
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '删除分块 block_overview' }))
  const dialog = screen.getByRole('dialog', { name: '删除分块' })
  expect(dialog).toHaveTextContent('将移除该分块中的 4 个组件')
  expect(screen.getByRole('button', { name: '取消' })).toHaveFocus()
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(document.querySelector('[data-block-id="block_overview"]')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '撤销' })).toBeDisabled()
})

test('空分块删除无需确认且空白单元可重新生成正式分块', () => {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  source.pages[0].blocks[1].components = []
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '删除分块 block_source_note' }))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '空白单元，第 4 列，第 13 行' }))
  expect(screen.getByLabelText('分块 block_empty_1，位置 4 列 13 行，尺寸 1 × 1')).toBeInTheDocument()
})

test('组件落入隐式空白单元会原子创建分块和组件', () => {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  source.pages[0].blocks[1].components = []
  const consumed = vi.fn()
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} pendingComponentType="KPI" onPendingComponentConsumed={consumed} />)
  fireEvent.click(screen.getByRole('button', { name: '删除分块 block_source_note' }))
  fireEvent.click(screen.getByRole('button', { name: '空白单元，第 2 列，第 12 行' }))
  expect(screen.getByLabelText('分块 block_empty_1，位置 2 列 12 行，尺寸 1 × 1')).toBeInTheDocument()
  expect(screen.getByLabelText('组件 指标卡，位置 1 列 1 行，尺寸 4 × 4')).toBeInTheDocument()
  expect(consumed).toHaveBeenCalledTimes(1)
})

test('锁定或无编辑权限时禁止重置分块', () => {
  const locked = structuredClone(reportExample) as unknown as ReportDocument
  locked.pages[0].blocks[0].locks.config = true
  const first = render(<ReportDesignerCanvas source={locked} runtime={demoReportRuntime} />)
  expect(screen.queryByRole('button', { name: '删除分块 block_overview' })).not.toBeInTheDocument()
  first.unmount()
  render(<ReportDesignerCanvas source={reportExample} runtime={{ ...demoReportRuntime, permissions: ['report:view'] }} />)
  expect(screen.getByText('当前账号仅可查看报告，没有编辑权限。')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /清空分块/ })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '撤销' })).toBeDisabled()
})

test('统一历史栈可以撤销组件删除', () => {
  render(<ReportDesignerCanvas source={editableComponentSource()} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '删除组件 营业收入趋势' }))
  expect(document.querySelector('[data-component-id="chart_revenue_trend"]')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '撤销' }))
  expect(document.querySelector('[data-component-id="chart_revenue_trend"]')).toBeInTheDocument()
})

test('快捷键可以撤销和重做组件操作', () => {
  render(<ReportDesignerCanvas source={editableComponentSource()} runtime={demoReportRuntime} />)
  fireEvent.click(screen.getByRole('button', { name: '删除组件 营业收入趋势' }))
  fireEvent.keyDown(window, { key: 'z', ctrlKey: true })
  expect(document.querySelector('[data-component-id="chart_revenue_trend"]')).toBeInTheDocument()
  fireEvent.keyDown(window, { key: 'z', ctrlKey: true, shiftKey: true })
  expect(document.querySelector('[data-component-id="chart_revenue_trend"]')).not.toBeInTheDocument()
})

test('服务端 editorState 可在重新加载后恢复已释放区域的编辑态最小行数', () => {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  source.pages[0].blocks[1].components = []
  const onTransition = vi.fn()
  const first = render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} onTransition={onTransition} loadGeneration={1} />)
  fireEvent.click(screen.getByRole('button', { name: '删除分块 block_source_note' }))
  const saved = onTransition.mock.calls.at(-1)?.[0]
  first.unmount()
  render(<ReportDesignerCanvas source={saved.document} initialEditorState={saved.editorState} runtime={demoReportRuntime} loadGeneration={2} />)
  expect(document.querySelector('[data-block-id="block_source_note"]')).not.toBeInTheDocument()
  expect(document.querySelector('.report-page-canvas')).toHaveAttribute('data-content-rows', '14')
  expect(screen.getByRole('button', { name: '空白单元，第 12 列，第 14 行' })).toBeInTheDocument()
})

test('复制组件上报服务端合同一致的 Patch、来源和新旧审计身份', () => {
  const onTransition = vi.fn()
  render(<ReportDesignerCanvas source={editableComponentSource()} runtime={demoReportRuntime} onTransition={onTransition} />)
  fireEvent.click(screen.getByRole('button', { name: '复制组件 营业收入趋势' }))

  const transition = onTransition.mock.calls.at(-1)?.[0]
  expect(transition.document.pages[0].blocks[0].components.at(-1)?.id).toBe('component_chart_1')
  expect(transition.editorState.minimumRowsByPage.page_overview).toBeGreaterThanOrEqual(10)
  expect(transition.pendingChanges).toHaveLength(1)
  expect(transition.pendingChanges[0]).toMatchObject({
    operationType: 'COMPONENT_COPY', source: 'USER',
    target: {
      pageId: 'page_overview', blockId: 'block_overview', componentId: 'component_chart_1',
      sourceComponentId: 'chart_revenue_trend', createdComponentId: 'component_chart_1',
    },
  })
  expect(transition.pendingChanges[0].clientOperationId).toMatch(/^[0-9a-f-]{36}$/)
  expect(transition.pendingChanges[0].patch).toEqual(expect.arrayContaining([
    expect.objectContaining({ op: 'add', path: '/pages/0/blocks/0/components/2' }),
  ]))
})

test('长距离指针拖拽只形成一条布局审计变化', () => {
  const onTransition = vi.fn()
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} onTransition={onTransition} />)
  const block = screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3')

  fireEvent.pointerDown(block, { pointerId: 7, clientX: 0, clientY: 0 })
  fireEvent.pointerMove(block, { pointerId: 7, clientX: 0, clientY: 60 })
  fireEvent.pointerMove(block, { pointerId: 7, clientX: 0, clientY: 110 })
  expect(onTransition).not.toHaveBeenCalled()
  fireEvent.pointerUp(block, { pointerId: 7, clientX: 0, clientY: 110 })

  expect(onTransition).toHaveBeenCalledTimes(1)
  const pending = onTransition.mock.calls[0][0].pendingChanges
  expect(pending).toHaveLength(1)
  expect(pending[0]).toMatchObject({
    operationType: 'BLOCK_MOVE',
    target: { pageId: 'page_overview', blockId: 'block_source_note' },
  })
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 14 行，尺寸 12 × 3')).toBeInTheDocument()
})

test('确认已保存操作只清理 pending，保留撤销并为撤销创建新审计变化', async () => {
  const onTransition = vi.fn()
  const view = render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} onTransition={onTransition} />)
  fireEvent.keyDown(screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3'), { key: 'ArrowDown' })
  const operationID = onTransition.mock.calls.at(-1)?.[0].pendingChanges[0].clientOperationId as string

  view.rerender(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} onTransition={onTransition} acknowledgedClientOperationIds={[operationID]} />)
  await waitFor(() => expect(onTransition.mock.calls.at(-1)?.[0].pendingChanges).toEqual([]))
  expect(screen.getByRole('button', { name: '撤销' })).toBeEnabled()

  fireEvent.click(screen.getByRole('button', { name: '撤销' }))
  const undo = onTransition.mock.calls.at(-1)?.[0].pendingChanges[0]
  expect(undo).toMatchObject({ operationType: 'UNDO', target: { referencedOperationId: operationID } })
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3')).toBeInTheDocument()
})

test('旧 sessionStorage 草稿不会覆盖传入的服务端事实', () => {
  const legacy = structuredClone(reportExample) as unknown as ReportDocument
  legacy.pages[0].blocks[1].grid.y = 20
  sessionStorage.setItem('report-layout-draft:test', JSON.stringify({ kind: 'report-designer-session-v1', document: legacy }))

  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} />)
  expect(screen.getByLabelText('分块 block_source_note，位置 1 列 12 行，尺寸 12 × 3')).toBeInTheDocument()
  expect(screen.queryByLabelText('分块 block_source_note，位置 1 列 21 行，尺寸 12 × 3')).not.toBeInTheDocument()
})

test('分块和组件冻结设置写入统一历史并规范化禁用态', () => {
  const onChange = vi.fn()
  render(<ReportDesignerCanvas source={reportExample} runtime={demoReportRuntime} onChange={onChange} />)

  expect(screen.getByText('分块 block_overview')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('checkbox', { name: '启用浏览态冻结' }))
  let changed = onChange.mock.calls.at(-1)?.[0] as ReportDocument
  expect(changed.pages[0].blocks[0].sticky).toEqual({ enabled: true, top: 0, scope: 'PAGE', zIndex: 100 })

  fireEvent.focus(screen.getByLabelText('组件 营业收入趋势，位置 1 列 5 行，尺寸 32 × 36'))
  expect(screen.getByText('组件 营业收入趋势')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('checkbox', { name: '启用浏览态冻结' }))
  fireEvent.change(screen.getByRole('combobox', { name: '冻结作用域' }), { target: { value: 'CONTAINER' } })
  changed = onChange.mock.calls.at(-1)?.[0] as ReportDocument
  expect(changed.pages[0].blocks[0].components[2].sticky).toEqual({ enabled: true, top: 0, scope: 'CONTAINER', containerId: 'block_overview', zIndex: 100 })

  fireEvent.click(screen.getByRole('checkbox', { name: '启用浏览态冻结' }))
  changed = onChange.mock.calls.at(-1)?.[0] as ReportDocument
  expect(changed.pages[0].blocks[0].components[2].sticky).toEqual({ enabled: false })
  fireEvent.click(screen.getByRole('button', { name: '撤销' }))
  expect(screen.getByRole('checkbox', { name: '启用浏览态冻结' })).toBeChecked()
})

test('页面与所属分块同名时不提供歧义容器选项', () => {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  source.pages[0].id = source.pages[0].blocks[0].id
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} />)

  fireEvent.focus(screen.getByLabelText('组件 营业收入趋势，位置 1 列 5 行，尺寸 32 × 36'))
  fireEvent.click(screen.getByRole('checkbox', { name: '启用浏览态冻结' }))

  expect(screen.getByRole('combobox', { name: '冻结作用域' })).toHaveValue('BLOCK')
  expect(screen.queryByRole('option', { name: '指定祖先容器' })).not.toBeInTheDocument()
})

test('配置锁定时仍可选择目标但冻结表单只读', () => {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  source.pages[0].blocks[0].locks.config = true
  render(<ReportDesignerCanvas source={source} runtime={demoReportRuntime} />)
  expect(screen.getByText('配置已锁定')).toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: '启用浏览态冻结' })).toBeDisabled()
})

function editableComponentSource(): ReportDocument {
  const source = structuredClone(reportExample) as unknown as ReportDocument
  const block = source.pages[0].blocks[0]
  block.components = [structuredClone(block.components[0]), structuredClone(block.components[2])]
  block.components[0].manualLocked = true
  block.components[0].grid = { x: 0, y: 0, w: 12, h: 4 }
  block.components[1].grid = { x: 0, y: 4, w: 12, h: 8 }
  return source
}
