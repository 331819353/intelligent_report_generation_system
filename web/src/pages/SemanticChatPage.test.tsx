import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { SemanticChatPage } from './SemanticChatPage'

beforeEach(() => sessionStorage.clear())
afterEach(() => vi.unstubAllGlobals())

test('keeps a verified plan as context for the next turn', async () => {
  const plannedBodies: Array<Record<string, unknown>> = []
  let planIndex = 0
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/semantic-qa/graph/status')) {
      return json({ status: 'READY', currentGeneration: 7, requestedEventVersion: 11, appliedEventVersion: 11 })
    }
    if (url.endsWith('/semantic-qa/golden-question-sets')) return json([])
    if (url.endsWith('/semantic-qa/query-plans')) {
      plannedBodies.push(JSON.parse(String(init?.body)))
      planIndex += 1
      return json(plan(planIndex), 201)
    }
    if (url.includes('/semantic-qa/query-plans/') && url.endsWith('/execute')) {
      return json({
        queryPlan: { ...plan(planIndex), status: 'EXECUTED', executionRowCount: 1, executionDurationMs: 18 },
        result: { queryId: `query-${planIndex}`, columns: ['region', 'sales_amount'], rows: [['华东', 128000]], rowCount: 1, durationMs: 18 },
        evidence: {
          graphGenerationId: '00000000-0000-4000-8000-000000000100',
          graphGeneration: 7,
          pathHash: 'b'.repeat(64),
          metricId: 'metric-1',
          metricVersionId: 'metric-version-1',
          dimensionId: 'dimension-1',
          datasetVersionId: 'dataset-version-1',
          materializationId: 'materialization-1',
          lineage: plan(planIndex).evidence,
          permissionDecision: 'REVALIDATED_BY_METRIC_RUNTIME',
          freshnessDecision: 'ACTIVE_MATERIALIZATION_EXACT_VERSION',
          compatibilityDecision: 'VERIFIED_NON_UNSAFE',
          executionRevalidated: true,
        },
      })
    }
    return json({ code: 'NOT_FOUND', message: 'not found' }, 404)
  }))

  render(<MemoryRouter><SemanticChatPage /></MemoryRouter>)
  const composer = screen.getByLabelText('输入分析问题')
  await userEvent.type(composer, '本月销售额是多少？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))
  expect(await screen.findByText('查询结果：region为 华东，sales_amount为 128,000。')).toBeInTheDocument()

  await userEvent.type(composer, '那上个月呢？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))
  await waitFor(() => expect(plannedBodies).toHaveLength(2))

  expect(plannedBodies[0]).not.toHaveProperty('contextQueryPlanId')
  expect(plannedBodies[1]).toMatchObject({
    question: '那上个月呢？',
    contextQueryPlanId: '00000000-0000-4000-8000-000000000001',
  })
  expect(await screen.findByText('2 轮对话')).toBeInTheDocument()
})

function plan(index: number) {
  return {
    id: `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
    graphGenerationId: '00000000-0000-4000-8000-000000000100',
    graphGeneration: 7,
    questionHash: 'a'.repeat(64),
    intent: 'METRIC',
    status: 'READY',
    confidence: 0.98,
    selectedMetricId: 'metric-1',
    selectedMetricVersionId: 'metric-version-1',
    selectedDimensionId: 'dimension-1',
    selectedDatasetVersionId: 'dataset-version-1',
    selectedMaterializationId: 'materialization-1',
    pathHash: 'b'.repeat(64),
    evidence: [{
      index: 0,
      nodeKey: 'metric:sales_amount',
      subjectType: 'METRIC',
      subjectRef: 'metric-1',
      label: '销售额',
      authority: 'CONTROL_PLANE',
      confidence: 1,
      evidenceHash: 'c'.repeat(64),
    }],
    createdAt: '2026-07-26T00:00:00Z',
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
