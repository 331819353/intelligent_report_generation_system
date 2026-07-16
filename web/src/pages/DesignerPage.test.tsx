import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import reportExample from '../../../api/examples/report-json-v1.json'
import type { ReportDocument } from '../lib/report-contract'
import type { ReportDraftRecord, SaveReportDraftInput } from '../lib/report-drafts'
import { DesignerPage } from './DesignerPage'

const reportId = '4ea8375a-3f96-4ff0-8c5e-f0b132c1e842'

beforeEach(() => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
})

afterEach(() => {
  sessionStorage.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test('treats a new template as unsaved until the server creates it', async () => {
  const created = draftRecord()
  vi.stubGlobal('confirm', vi.fn(() => false))
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const input = JSON.parse(String(init?.body)) as { definition: ReportDocument }
    const definition = structuredClone(input.definition)
    definition.report.id = created.id
    return jsonResponse({ ...created, code: definition.report.code, definition })
  }))
  renderDesigner('draft')

  expect(screen.getByText('未保存')).toBeInTheDocument()
  const unloadWhileNew = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(unloadWhileNew)
  expect(unloadWhileNew.defaultPrevented).toBe(true)

  await userEvent.click(screen.getByRole('link', { name: '工作台' }))
  expect(window.confirm).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: '创建草稿' })).toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: '创建草稿' }))
  expect(await screen.findByText('已保存')).toBeInTheDocument()
  await waitFor(() => {
    const unloadAfterCreate = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(unloadAfterCreate)
    expect(unloadAfterCreate.defaultPrevented).toBe(false)
  })
})

test('loads and saves a semantic report draft change', async () => {
  const initial = draftRecord()
  let savedInput: SaveReportDraftInput | undefined
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      savedInput = JSON.parse(String(init.body)) as SaveReportDraftInput
      return jsonResponse({ ...initial, revision: 2, definition: savedInput.definition, editorState: savedInput.editorState })
    }
    return jsonResponse(initial)
  })
  vi.stubGlobal('fetch', fetchMock)
  renderDesigner()

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用浏览态冻结' }))
  expect(screen.getByText('未保存')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))

  await waitFor(() => expect(savedInput).toBeDefined())
  expect(savedInput?.expectedRevision).toBe(1)
  expect(savedInput?.changes).toHaveLength(1)
  expect(savedInput?.changes[0]).toMatchObject({
    operationType: 'BLOCK_STICKY_UPDATE', source: 'USER', target: { pageId: 'page_overview', blockId: 'block_overview' },
  })
  expect(await screen.findByText('已保存')).toBeInTheDocument()
})

test('does not acknowledge edits created while a save is in flight', async () => {
  const initial = draftRecord()
  let resolveSave: ((response: Response) => void) | undefined
  let savedInput: SaveReportDraftInput | undefined
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method !== 'PUT') return jsonResponse(initial)
    savedInput = JSON.parse(String(init.body)) as SaveReportDraftInput
    return new Promise<Response>(resolve => { resolveSave = resolve })
  }))
  renderDesigner()

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用浏览态冻结' }))
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
  await screen.findByText('保存中')
  fireEvent.change(screen.getByRole('spinbutton', { name: '顶部偏移' }), { target: { value: '7' } })
  expect(savedInput).toBeDefined()
  resolveSave?.(jsonResponse({ ...initial, revision: 2, definition: savedInput!.definition, editorState: savedInput!.editorState }))

  expect(await screen.findByText('未保存')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '保存草稿' })).toBeEnabled()
})

test('keeps local content visible when the server reports a revision conflict', async () => {
  const initial = draftRecord()
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => init?.method === 'PUT'
    ? jsonResponse({ code: 'REPORT_DRAFT_CONFLICT', message: '报告草稿已更新', currentRevision: 8, currentHash: 'b'.repeat(64) }, 409)
    : jsonResponse(initial)))
  renderDesigner()

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用浏览态冻结' }))
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('修订 8')
  expect(screen.getByRole('region', { name: '草稿冲突处理' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '导出本地 JSON' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '载入服务端版本' })).toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: '启用浏览态冻结' })).toBeChecked()
})

test('exports edits created while a conflicting save is in flight', async () => {
  const initial = draftRecord()
  let resolveSave: ((response: Response) => void) | undefined
  let exportedBlob: Blob | undefined
  const NativeURL = URL
  class ExportURL extends NativeURL {
    static createObjectURL(blob: Blob) {
      exportedBlob = blob
      return 'blob:conflict-local'
    }

    static revokeObjectURL() {}
  }
  vi.stubGlobal('URL', ExportURL)
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method !== 'PUT') return jsonResponse(initial)
    return new Promise<Response>(resolve => { resolveSave = resolve })
  }))
  renderDesigner()

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用浏览态冻结' }))
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
  await screen.findByText('保存中')
  fireEvent.change(screen.getByRole('spinbutton', { name: '顶部偏移' }), { target: { value: '7' } })
  resolveSave?.(jsonResponse({
    code: 'REPORT_DRAFT_CONFLICT', message: '报告草稿已更新', currentRevision: 8, currentHash: 'b'.repeat(64),
  }, 409))

  await userEvent.click(await screen.findByRole('button', { name: '导出本地 JSON' }))
  expect(exportedBlob).toBeInstanceOf(Blob)
  if (!exportedBlob) throw new Error('未捕获冲突导出 Blob')
  const exported = JSON.parse(await exportedBlob.text()) as ReportDocument
  expect(exported.pages[0].blocks[0].sticky).toMatchObject({ enabled: true, top: 7 })
})

test('removes only a corrupt legacy draft key after the server draft loads', async () => {
  const legacyKey = `report-layout-draft:${reportId}`
  sessionStorage.setItem(legacyKey, '{broken')
  vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(draftRecord())))
  renderDesigner()

  await screen.findByRole('checkbox', { name: '启用浏览态冻结' })

  expect(sessionStorage.getItem(legacyKey)).toBeNull()
  expect(sessionStorage.getItem('intelligent-report-auth')).not.toBeNull()
})

test('migrates a confirmed legacy session and clears its exact key only after save', async () => {
  const initial = draftRecord()
  const legacy = structuredClone(initial.definition)
  legacy.report.name = '旧会话恢复标题'
  const legacyKey = `report-layout-draft:${reportId}`
  sessionStorage.setItem(legacyKey, JSON.stringify({
    kind: 'report-designer-session-v1', document: legacy, minimumRowsByPage: { page_overview: 16 },
  }))
  let savedInput: SaveReportDraftInput | undefined
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method !== 'PUT') return jsonResponse(initial)
    savedInput = JSON.parse(String(init.body)) as SaveReportDraftInput
    return jsonResponse({ ...initial, name: '旧会话恢复标题', revision: 2, definition: savedInput.definition, editorState: savedInput.editorState })
  }))
  renderDesigner()

  await screen.findByRole('region', { name: '旧会话草稿迁移' })
  expect(sessionStorage.getItem(legacyKey)).not.toBeNull()
  await userEvent.click(screen.getByRole('button', { name: '恢复为未保存修改' }))
  expect(await screen.findByRole('heading', { level: 1, name: '旧会话恢复标题' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))

  await waitFor(() => expect(savedInput?.changes[0]?.operationType).toBe('LEGACY_DRAFT_RECOVERY'))
  await waitFor(() => expect(sessionStorage.getItem(legacyKey)).toBeNull())
  expect(sessionStorage.getItem('intelligent-report-auth')).not.toBeNull()
})

test('migrates a bare legacy V1 document without a session envelope', async () => {
  const initial = draftRecord()
  const legacy = structuredClone(initial.definition)
  legacy.report.name = '裸 V1 旧草稿'
  const legacyKey = `report-layout-draft:${reportId}`
  sessionStorage.setItem(legacyKey, JSON.stringify(legacy))
  let savedInput: SaveReportDraftInput | undefined
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method !== 'PUT') return jsonResponse(initial)
    savedInput = JSON.parse(String(init.body)) as SaveReportDraftInput
    return jsonResponse({ ...initial, revision: 2, definition: savedInput.definition, editorState: savedInput.editorState })
  }))
  renderDesigner()

  await screen.findByRole('region', { name: '旧会话草稿迁移' })
  await userEvent.click(screen.getByRole('button', { name: '恢复为未保存修改' }))
  expect(await screen.findByRole('heading', { level: 1, name: '裸 V1 旧草稿' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))

  await waitFor(() => expect(savedInput?.changes[0]?.operationType).toBe('LEGACY_DRAFT_RECOVERY'))
  await waitFor(() => expect(sessionStorage.getItem(legacyKey)).toBeNull())
})

test('uses server capabilities only to close the editing UI', async () => {
  const initial = draftRecord()
  initial.capabilities.edit = false
  vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(initial)))
  renderDesigner()

  expect(await screen.findByText('只读')).toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: '启用浏览态冻结' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled()
})

function renderDesigner(id = reportId) {
  return render(
    <MemoryRouter initialEntries={[`/designer/${id}`]}>
      <Routes><Route path="/designer/:reportId" element={<DesignerPage />} /></Routes>
    </MemoryRouter>,
  )
}

function draftRecord(): ReportDraftRecord {
  const definition = structuredClone(reportExample) as ReportDocument
  definition.report.id = reportId
  return {
    id: reportId,
    code: definition.report.code,
    name: definition.report.name,
    description: definition.report.description ?? '',
    type: definition.report.type,
    status: 'DRAFT',
    revision: 1,
    definitionHash: 'a'.repeat(64),
    definition,
    editorState: { minimumRowsByPage: { page_overview: 14 } },
    createdAt: '2026-07-16T10:00:00Z',
    updatedAt: '2026-07-16T10:00:00Z',
    capabilities: { edit: true },
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
