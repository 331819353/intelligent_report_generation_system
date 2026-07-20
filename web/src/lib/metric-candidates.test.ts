import { afterEach, describe, expect, test, vi } from 'vitest'
import { metricCandidateAPI } from './metric-candidates'

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

describe('候选指标 API 合同', () => {
  test('目录传递服务端筛选和分页并禁用缓存', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [], total: 0, limit: 30, offset: 60 }))
    vi.stubGlobal('fetch', fetchMock)

    await metricCandidateAPI.list({ status: 'NEEDS_REVIEW', datasetId: 'dataset/id', limit: 30, offset: 60 })

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/metric-candidates?limit=30&offset=60&status=NEEDS_REVIEW&datasetId=dataset%2Fid')
    expect(init.cache).toBe('no-store')
  })

  test('接受和拒绝都携带乐观锁版本，拒绝额外提交人工原因', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: 'candidate-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await metricCandidateAPI.accept('candidate/id', 4)
    await metricCandidateAPI.reject('candidate/id', 5, '业务口径不成立')

    const [acceptURL, acceptInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const [rejectURL, rejectInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit]
    expect(acceptURL).toBe('/api/v1/metric-candidates/candidate%2Fid/accept')
    expect(acceptInit.method).toBe('POST')
    expect(JSON.parse(String(acceptInit.body))).toEqual({ expectedVersion: 4 })
    expect(rejectURL).toBe('/api/v1/metric-candidates/candidate%2Fid/reject')
    expect(rejectInit.method).toBe('POST')
    expect(JSON.parse(String(rejectInit.body))).toEqual({ expectedVersion: 5, reason: '业务口径不成立' })
  })
})

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
