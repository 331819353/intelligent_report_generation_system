import { afterEach, expect, test, vi } from 'vitest'
import { semanticChatAPI, type SemanticQueryPlan } from './semantic-chat'

afterEach(() => vi.unstubAllGlobals())

test('plans a turn with every prior verified metric context', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
    questionHash: 'a'.repeat(64),
    intent: 'METRIC',
    metricCodes: ['sales_amount', 'order_count'],
    contextQueryPlanIds: ['plan-1', 'plan-2'],
    contextInherited: true,
    plans: [],
  }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)

  await semanticChatAPI.planTurn({
    question: '那上个月呢？',
    priorQuestions: ['本月销售额是多少？', '其中华东是多少？'],
    contextQueryPlanIds: ['plan-1', 'plan-2'],
  })

  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/api/v1/semantic-qa/query-turns')
  expect(JSON.parse(String(init?.body))).toEqual({
    question: '那上个月呢？',
    maximumPathHops: 8,
    priorQuestions: ['本月销售额是多少？', '其中华东是多少？'],
    contextQueryPlanIds: ['plan-1', 'plan-2'],
  })
})

test('plans a follow-up question against the previous governed query plan', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
    id: 'plan-2',
    graphGenerationId: 'generation-1',
    graphGeneration: 4,
    questionHash: 'a'.repeat(64),
    intent: 'METRIC',
    status: 'READY',
    confidence: 0.98,
    pathHash: 'b'.repeat(64),
    resolution: [],
    evidence: [],
    createdAt: '2026-07-26T00:00:00Z',
  }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)

  await semanticChatAPI.planQuestion({
    question: '那上个月呢？',
    contextQueryPlanId: '00000000-0000-4000-8000-000000000010',
  })

  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/api/v1/semantic-qa/query-plans')
  expect(JSON.parse(String(init?.body))).toEqual({
    question: '那上个月呢？',
    intent: 'UNKNOWN',
    metricCode: '',
    maximumPathHops: 8,
    contextQueryPlanId: '00000000-0000-4000-8000-000000000010',
  })
})

test('executes only the frozen generation and path returned by planning', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({}), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }))
  vi.stubGlobal('fetch', fetchMock)
  const plan = {
    id: '00000000-0000-4000-8000-000000000020',
    graphGenerationId: '00000000-0000-4000-8000-000000000021',
    graphGeneration: 4,
    questionHash: 'a'.repeat(64),
    intent: 'METRIC',
    status: 'READY',
    confidence: 0.98,
    pathHash: 'b'.repeat(64),
    resolution: [],
    evidence: [],
    createdAt: '2026-07-26T00:00:00Z',
  } satisfies SemanticQueryPlan

  await semanticChatAPI.executePlan(plan)

  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe(`/api/v1/semantic-qa/query-plans/${plan.id}/execute`)
  expect(JSON.parse(String(init?.body))).toMatchObject({
    expectedGraphGenerationId: plan.graphGenerationId,
    expectedPathHash: plan.pathHash,
    parameters: {},
    maxRows: 100,
  })
})
