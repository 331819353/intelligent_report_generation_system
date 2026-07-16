import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import reportExample from '../../../api/examples/report-json-v1.json'
import { RequestError } from './api'
import type { ReportDocument } from './report-contract'
import {
  createIdempotencyKey,
  createReportDraft,
  getReportDraft,
  listReportRevisions,
  saveReportDraft,
  type ReportDraftRecord,
  type ReportRevisionRecord,
} from './report-drafts'

const definition = reportExample as ReportDocument

beforeEach(() => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access-token', refreshToken: 'refresh-token' }))
})

afterEach(() => {
  sessionStorage.clear()
  vi.unstubAllGlobals()
})

describe('report draft API', () => {
  test('creates a server draft with an idempotency key', async () => {
    const record = draftRecord()
    const fetchMock = vi.fn(async () => jsonResponse(record, 201))
    vi.stubGlobal('fetch', fetchMock)

    await expect(createReportDraft({ definition, editorState: { minimumRowsByPage: { page_overview: 14 } } }, 'create-key')).resolves.toEqual(record)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/reports')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('create-key')
    expect(JSON.parse(String(init.body))).toEqual({ definition, editorState: { minimumRowsByPage: { page_overview: 14 } } })
  })

  test('loads a draft using an encoded report id', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(draftRecord()))
    vi.stubGlobal('fetch', fetchMock)

    await getReportDraft('report/id')

    const [url] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/reports/report%2Fid/draft')
  })

  test('saves the frozen full definition and semantic changes together', async () => {
    const record = draftRecord({ revision: 2 })
    const fetchMock = vi.fn(async () => jsonResponse(record))
    vi.stubGlobal('fetch', fetchMock)
    const change = {
      clientOperationId: 'operation-1',
      operationType: 'COMPONENT_MOVE' as const,
      source: 'USER' as const,
      target: { pageId: 'page_overview', blockId: 'block_overview', componentId: 'chart_revenue_trend' },
      patch: [{ op: 'replace' as const, path: '/pages/0/blocks/0/components/2/grid/x', value: 1 }],
    }

    await saveReportDraft(record.id, {
      expectedRevision: 1,
      definition,
      editorState: { minimumRowsByPage: { page_overview: 14 } },
      changes: [change],
    }, 'save-key')

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(init.method).toBe('PUT')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('save-key')
    expect(JSON.parse(String(init.body))).toMatchObject({ expectedRevision: 1, definition, changes: [change] })
  })

  test('keeps conflict metadata for an explicit recovery choice', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      code: 'REPORT_DRAFT_CONFLICT', message: '报告草稿已被其他请求修改', currentRevision: 7, currentHash: 'f'.repeat(64),
    }, 409)))

    const error = await getReportDraft('00000000-0000-0000-0000-000000000001').catch(value => value)

    expect(error).toBeInstanceOf(RequestError)
    expect((error as RequestError).detail.currentRevision).toBe(7)
  })

  test('lists immutable revisions with bounded pagination', async () => {
    const createRevision: ReportRevisionRecord = {
      id: '00000000-0000-0000-0000-000000000002',
      baseRevision: 0,
      revision: 1,
      operationType: 'REPORT_CREATE',
      source: 'USER',
      patch: [{ op: 'add', path: '/report', value: definition.report }],
      patchHash: 'b'.repeat(64),
      beforeHash: '0'.repeat(64),
      afterHash: 'a'.repeat(64),
      actorUserId: '00000000-0000-0000-0000-000000000003',
      createdAt: '2026-07-16T10:00:00Z',
    }
    const fetchMock = vi.fn(async () => jsonResponse({ items: [createRevision], total: 1, limit: 20, offset: 10 }))
    vi.stubGlobal('fetch', fetchMock)

    const page = await listReportRevisions('00000000-0000-0000-0000-000000000001', 20, 10)

    const [url] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/reports/00000000-0000-0000-0000-000000000001/revisions?limit=20&offset=10')
    expect(page.items[0].operationType).toBe('REPORT_CREATE')
  })

  test('creates UUID-shaped retry keys', () => {
    expect(createIdempotencyKey()).toMatch(/^[0-9a-f-]{36}$/i)
  })
})

function draftRecord(overrides: Partial<ReportDraftRecord> = {}): ReportDraftRecord {
  return {
    id: '00000000-0000-0000-0000-000000000001',
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
    ...overrides,
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
