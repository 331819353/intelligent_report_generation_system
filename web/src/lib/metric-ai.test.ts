import { afterEach, describe, expect, test, vi } from 'vitest'
import { metricAIAPI } from './metric-ai'

afterEach(() => vi.restoreAllMocks())

describe('metricAIAPI', () => {
  test('只向审核提案端点提交最小指标意图', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      requestId: 'request-1', retrievalContextHash: 'a'.repeat(64),
      proposal: {
        schemaVersion: '1.0', strategy: 'NEEDS_CLARIFICATION', summary: '需要确认统计时点',
        targetDatasetId: '', targetDatasetVersionId: '', reuseMetricVersionId: '', retrievalEvidence: [],
        candidateMetricDefinition: null, datasetInstruction: '',
        clarificationQuestions: ['按支付时间还是下单时间？'], assumptions: [], warnings: [],
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))

    const request = { requirement: '统计已支付订单销售额，按支付月份汇总。' }
    const result = await metricAIAPI.propose(request)

    expect(result.proposal.strategy).toBe('NEEDS_CLARIFICATION')
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/metrics/ai/proposals')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual(request)
  })
})
