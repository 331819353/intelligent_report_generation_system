import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { SemanticChatPage } from './SemanticChatPage'

beforeEach(() => sessionStorage.clear())
afterEach(() => vi.unstubAllGlobals())

test('keeps every verified metric plan as context for the next turn', async () => {
  const plannedBodies: Array<Record<string, unknown>> = []
  let planIndex = 0
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/semantic-qa/graph/status')) {
      return json({ status: 'READY', currentGeneration: 7, requestedEventVersion: 11, appliedEventVersion: 11 })
    }
    if (url.endsWith('/semantic-qa/golden-question-sets')) return json([])
    if (url.endsWith('/semantic-qa/query-turns')) {
      plannedBodies.push(JSON.parse(String(init?.body)))
      planIndex += 1
      return json({
        questionHash: 'a'.repeat(64),
        intent: 'METRIC',
        metricCodes: ['sales_amount'],
        contextQueryPlanIds: planIndex > 1 ? ['00000000-0000-4000-8000-000000000001'] : [],
        contextInherited: planIndex > 1,
        plans: [plan(planIndex)],
        trace: mockTrace(
          planIndex > 1 ? ['本月销售额是多少？', '那上个月呢？'] : ['本月销售额是多少？'],
          [{ code: 'sales_amount', label: '销售额' }],
        ),
      }, 201)
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
  expect(await screen.findByText('查询结果：region为 华东，销售额为 128,000。')).toBeInTheDocument()
  expect(screen.getByRole('columnheader', { name: '销售额' })).toBeInTheDocument()

  await userEvent.type(composer, '那上个月呢？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))
  await waitFor(() => expect(plannedBodies).toHaveLength(2))

  expect(plannedBodies[0]).not.toHaveProperty('contextQueryPlanIds')
  expect(plannedBodies[1]).toMatchObject({
    question: '那上个月呢？',
    contextQueryPlanIds: ['00000000-0000-4000-8000-000000000001'],
  })
  expect(await screen.findByText('2 轮对话')).toBeInTheDocument()
})

test('shows the retrieval process and executes every metric in one question', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.endsWith('/semantic-qa/graph/status')) {
      return json({ status: 'READY', currentGeneration: 7, requestedEventVersion: 11, appliedEventVersion: 11 })
    }
    if (url.endsWith('/semantic-qa/golden-question-sets')) return json([])
    if (url.endsWith('/semantic-qa/query-turns')) {
      const requestedPlans = [
        plan(1),
        {
          ...plan(2),
          selectedMetricId: 'metric-2',
          selectedMetricVersionId: 'metric-version-2',
          conditions: {
            domain: 'sales',
            metricCode: 'order_count',
            metricVersionId: 'metric-version-2',
            datasetVersionId: 'dataset-version-1',
            dimensions: [],
          },
          evidence: [{
            ...plan(2).evidence[0],
            nodeKey: 'metric:order_count',
            subjectRef: 'metric-version-2',
            label: '订单量',
          }],
        },
      ]
      return json({
        questionHash: 'a'.repeat(64),
        intent: 'METRIC',
        metricCodes: ['sales_amount', 'order_count'],
        contextQueryPlanIds: [],
        contextInherited: false,
        plans: requestedPlans,
        trace: mockTrace(
          ['本月销售额和订单量分别是多少？'],
          [
            { code: 'sales_amount', label: '销售额' },
            { code: 'order_count', label: '订单量' },
          ],
        ),
      }, 201)
    }
    if (url.includes('/semantic-qa/query-plans/') && url.endsWith('/execute')) {
      const second = url.includes('000000000002')
      const selectedPlan = second
        ? {
            ...plan(2),
            status: 'EXECUTED',
            selectedMetricId: 'metric-2',
            selectedMetricVersionId: 'metric-version-2',
            evidence: [{
              ...plan(2).evidence[0],
              nodeKey: 'metric:order_count',
              subjectRef: 'metric-version-2',
              label: '订单量',
            }],
          }
        : { ...plan(1), status: 'EXECUTED' }
      return json({
        queryPlan: selectedPlan,
        result: {
          queryId: second ? 'query-2' : 'query-1',
          columns: [second ? 'order_count' : 'sales_amount'],
          rows: [[second ? 36 : 128000]],
          rowCount: 1,
          durationMs: second ? 12 : 18,
        },
        evidence: {
          graphGenerationId: '00000000-0000-4000-8000-000000000100',
          graphGeneration: 7,
          pathHash: 'b'.repeat(64),
          metricId: second ? 'metric-2' : 'metric-1',
          metricVersionId: second ? 'metric-version-2' : 'metric-version-1',
          datasetVersionId: 'dataset-version-1',
          materializationId: 'materialization-1',
          lineage: selectedPlan.evidence,
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
  await userEvent.type(composer, '本月销售额和订单量分别是多少？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))

  expect(await screen.findByText('已完成 2 个指标的可信查询：销售额为 128,000；订单量为 36。')).toBeInTheDocument()
  expect(screen.getByText('查看检索结果的过程')).toBeInTheDocument()
  expect(screen.getByText('销售额、订单量')).toBeInTheDocument()
  expect(screen.getByText('审查 2 个真实候选，选中 2 个已发布指标')).toBeInTheDocument()
  expect(screen.getAllByText(/权限\/版本\/兼容性已复核/)).toHaveLength(2)
})

test('answers the governed 80s xiaowei active workforce scenario from a semantic set', async () => {
  const governedPlan = workforcePlan()
  const followUpPlan = keyTalentFollowUpPlan()
  const plannedBodies: Array<Record<string, unknown>> = []
  let turnIndex = 0
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/semantic-qa/graph/status')) {
      return json({ status: 'READY', currentGeneration: 7, requestedEventVersion: 11, appliedEventVersion: 11 })
    }
    if (url.endsWith('/semantic-qa/golden-question-sets')) return json([])
    if (url.endsWith('/semantic-qa/query-turns')) {
      plannedBodies.push(JSON.parse(String(init?.body)))
      turnIndex += 1
      const followUp = turnIndex > 1
      return json({
        questionHash: 'a'.repeat(64),
        intent: 'METRIC',
        metricCodes: ['metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441'],
        contextQueryPlanIds: followUp ? [governedPlan.id] : [],
        contextInherited: followUp,
        plans: [followUp ? followUpPlan : governedPlan],
        trace: workforceTrace(followUp),
      }, 201)
    }
    if (url.includes('/semantic-qa/query-plans/') && url.endsWith('/execute')) {
      const isFollowUp = url.includes(followUpPlan.id)
      const selectedPlan = isFollowUp ? followUpPlan : governedPlan
      return json({
        queryPlan: { ...selectedPlan, status: 'EXECUTED' },
        result: {
          queryId: isFollowUp ? 'query-key-talent' : 'query-workforce',
          columns: ['metric_dws_employee_profile_regenerated_20260727_e_e1d100dce525'],
          rows: [[isFollowUp ? 5093 : 35386]],
          rowCount: 1,
          durationMs: 16,
        },
        evidence: {
          graphGenerationId: selectedPlan.graphGenerationId,
          graphGeneration: selectedPlan.graphGeneration,
          pathHash: selectedPlan.pathHash,
          metricId: selectedPlan.selectedMetricId,
          metricVersionId: selectedPlan.selectedMetricVersionId,
          datasetVersionId: selectedPlan.selectedDatasetVersionId,
          materializationId: selectedPlan.selectedMaterializationId,
          lineage: selectedPlan.evidence,
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
  await userEvent.type(composer, '80后小微在职人员有多少人？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))

  const metricHeader = await screen.findByRole('columnheader', { name: '员工总人数' })
  expect(metricHeader.closest('.semantic-chat-answer')?.querySelector(':scope > p')).toHaveTextContent(
    '查询结果：在小微人员范围内，按出生年代段为 80后（80-85、85-90）、人员状态为 在职（映射为在岗）的口径，员工总人数为 35,386。',
  )
  expect(screen.getByText(
    '在小微人员范围内，查询出生年代段=80后（映射：80-85、85-90）、人员状态=在职（映射：在岗）条件下的员工总人数。',
  )).toBeInTheDocument()

  await userEvent.type(composer, '其中的关键人才有多少？')
  await userEvent.click(screen.getByRole('button', { name: '发送问题' }))

  await waitFor(() => expect(plannedBodies).toHaveLength(2))
  expect(plannedBodies[1]).toMatchObject({
    question: '其中的关键人才有多少？',
    priorQuestions: ['80后小微在职人员有多少人？'],
    contextQueryPlanIds: [governedPlan.id],
  })
  expect(await screen.findByText(/员工总人数为 5,093。/)).toBeInTheDocument()
  expect(screen.getByText(
    '在小微人员范围内，查询出生年代段=80后（映射：80-85、85-90）、人员状态=在职（映射：在岗）、关键人才=关键人才（映射：54 个已治理标准值）条件下的员工总人数。',
  )).toBeInTheDocument()
  expect(screen.getAllByText('80-85、85-90（共 2 个）')).toHaveLength(2)
  expect(screen.getAllByText(/关键人才,已治理组合1、关键人才,已治理组合2/).length).toBeGreaterThan(0)
  expect(screen.getByText(/54 个已治理标准值/)).toBeInTheDocument()
  expect(screen.getByText('key_talent：关键人才')).toBeInTheDocument()
  expect(screen.getByText("key_talent LIKE '%关键人才%'")).toBeInTheDocument()
  expect(screen.getByText('field_key_talent IN (:key_talent_1 … :key_talent_54)')).toBeInTheDocument()
  expect(screen.getByText(/标识员工是否被评定为关键人才:关键人才/)).toBeInTheDocument()
  expect(screen.getByText(/复用已验证决策图 WHERE · CONTAINS/)).toBeInTheDocument()
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
    conditions: {
      domain: 'sales',
      metricCode: 'sales_amount',
      metricVersionId: 'metric-version-1',
      datasetVersionId: 'dataset-version-1',
      dimensions: [],
    },
    resolution: [
      { stage: 'INTENT_RECOGNITION', status: 'RESOLVED', selectedCode: 'METRIC' },
      { stage: 'METRIC_CATALOG', status: 'RESOLVED', candidateCount: 2, selectedCode: 'sales_amount' },
      { stage: 'DIMENSION_MEMBER', status: 'RESOLVED', candidateCount: 1, selectedCode: 'region' },
      { stage: 'DATASET_LOCK', status: 'RESOLVED', candidateCount: 1, selectedCode: 'dataset-version-1' },
    ],
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

function workforcePlan() {
  return {
    ...plan(3),
    selectedMetricId: 'metric-workforce',
    selectedMetricVersionId: 'metric-version-workforce',
    selectedDimensionId: undefined,
    selectedDatasetVersionId: 'dataset-version-workforce',
    selectedMaterializationId: 'materialization-workforce',
    conditions: {
      domain: '企业',
      metricCode: 'metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441',
      metricVersionId: 'metric-version-workforce',
      datasetVersionId: 'dataset-version-workforce',
      dimensions: [
        { dimensionCode: 'birth_cohort', dimensionId: 'dimension-generation', memberKeys: ['80-85', '85-90'] },
        { dimensionCode: 'employee_status', dimensionId: 'dimension-employment-status', memberKey: '在岗' },
      ],
    },
    evidence: [
      {
        ...plan(3).evidence[0],
        nodeKey: 'metric:employee_total_count',
        subjectRef: 'metric-version-workforce',
        label: '员工总人数',
      },
      {
        ...plan(3).evidence[0],
        index: 1,
        nodeKey: 'dimension:generation',
        subjectType: 'DIMENSION',
        subjectRef: 'dimension-generation',
        label: '出生年代段',
      },
      {
        ...plan(3).evidence[0],
        index: 2,
        nodeKey: 'dimension:employment-status',
        subjectType: 'DIMENSION',
        subjectRef: 'dimension-employment-status',
        label: '人员状态',
      },
      {
        ...plan(3).evidence[0],
        index: 3,
        nodeKey: 'dataset_version:workforce',
        subjectType: 'DATASET_VERSION',
        subjectRef: 'dataset-version-workforce',
        label: '员工画像属性聚合统计表',
      },
      {
        ...plan(3).evidence[0],
        index: 4,
        nodeKey: 'source:hr',
        subjectType: 'SOURCE',
        subjectRef: 'source-hr',
        label: '人力资源数据源',
      },
    ],
  }
}

function keyTalentFollowUpPlan() {
  const previous = workforcePlan()
  return {
    ...previous,
    id: '00000000-0000-4000-8000-000000000004',
    conditions: {
      ...previous.conditions,
      dimensions: [
        ...previous.conditions.dimensions,
        {
          dimensionCode: 'key_talent',
          dimensionId: 'dimension-key-talent',
          memberKeys: Array.from(
            { length: 54 },
            (_, index) => `关键人才,已治理组合${index + 1}`,
          ),
        },
      ],
    },
    evidence: [
      ...previous.evidence,
      {
        ...plan(4).evidence[0],
        index: previous.evidence.length,
        nodeKey: 'dimension:key-talent',
        subjectType: 'DIMENSION',
        subjectRef: 'dimension-key-talent',
        label: '关键人才',
      },
    ],
  }
}

function mockAssessments(metricCount: number, lookupCount = 0) {
  return [
    { step: 'CONTEXT_SYNTHESIS', status: 'PASS', decision: 'CURRENT_TURN_OVERRIDES_SAME_DIMENSION_THEN_LATEST_VERIFIED_PLAN', detail: '已组合最近对话并按当前轮优先解析' },
    { step: 'INTENT_EXTRACTION', status: 'PASS', decision: 'METRIC', detail: `提取 ${metricCount} 个指标、${lookupCount} 个维度值词` },
    { step: 'METRIC_RETRIEVAL', status: 'PASS', decision: 'PUBLISHED_METRIC_CATALOG', detail: `审查 ${metricCount} 个真实候选，选中 ${metricCount} 个已发布指标` },
    { step: 'DIMENSION_VALUE_RETRIEVAL', status: 'PASS', decision: lookupCount ? 'METRIC_SCOPED_MEMBER_MAPPING' : 'NO_DIMENSION_VALUE_REQUEST', detail: `形成 ${lookupCount} 组指标范围内的维度值候选与选择记录` },
    { step: 'FINAL_PLAN', status: 'PASS', decision: 'GOVERNED_QUERY_PLAN', detail: `${metricCount}/${metricCount} 个计划通过权限、兼容性、版本和血缘门禁` },
  ]
}

function mockTrace(
  questions: string[],
  metrics: Array<{ code: string; label: string }>,
) {
  return {
    conversationQuestions: questions,
    contextPolicy: 'CURRENT_TURN_OVERRIDES_SAME_DIMENSION_THEN_LATEST_VERIFIED_PLAN',
    standaloneQuestion: `查询${metrics.map(metric => metric.label).join('、')}。`,
    extraction: {
      intent: 'METRIC',
      metricTerms: metrics.map(metric => metric.label),
      dimensionValueTerms: [],
    },
    metricCandidates: metrics.map(metric => ({
      ...metric,
      matchedTerm: metric.label,
      matchMethod: 'EXACT_CATALOG',
      score: 1,
      selected: true,
      source: 'CURRENT_TURN',
    })),
    dimensionValueLookups: [],
    finalSelections: metrics.map((metric, index) => ({
      metricCode: metric.code,
      metricName: metric.label,
      metricVersionId: `metric-version-${index + 1}`,
      datasetVersionId: 'dataset-version-1',
      dimensions: [],
      planId: `plan-${index + 1}`,
      planStatus: 'READY',
    })),
    assessments: mockAssessments(metrics.length),
  }
}

function workforceTrace(followUp: boolean) {
  const metricCode = 'metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441'
  const keyTalentValues = Array.from(
    { length: 54 },
    (_, index) => `关键人才,已治理组合${index + 1}`,
  )
  const lookups = [
    {
      term: '80后',
      canonicalValue: '80后',
      aliasValues: ['80后'],
      metricCode,
      metricFieldId: 'field_employee_total_count',
      dimensionCode: 'birth_cohort',
      dimensionName: '出生年代段',
      dimensionFieldId: 'field_birth_cohort',
      dimensionFieldName: 'birth_cohort',
      dimensionFieldDescription: '按出生年份划分的员工代际区间',
      vectorQuery: '按出生年份划分的员工代际区间:80后',
      vectorModel: 'test-embedding-model',
      vectorDimensions: 2560,
      vectorSearchStatus: 'SUCCEEDED',
      vectorCandidateCount: 2,
      vectorCandidateMemberKeys: ['80-85', '85-90'],
      whereDesignStatus: 'SUCCEEDED',
      whereDesignOperator: 'IN',
      whereDesignReason: '用户值映射为两个受治理成员，应使用集合条件',
      whereDesignModel: 'test-model',
      matchMethod: 'SEMANTIC_SET',
      candidateCount: 2,
      candidateMemberKeys: ['80-85', '85-90'],
      selectedMemberKeys: ['80-85', '85-90'],
      whereCondition: "birth_cohort IN ('80-85', '85-90')",
      compiledCondition: 'field_birth_cohort IN (:birth_cohort_1 … :birth_cohort_2)',
      candidateFilter: { inputCount: 2, acceptedCount: 2, rejectedCount: 0, status: 'PASS', rules: [] },
      selected: true,
      source: followUp ? 'CONTEXT_PLAN' : 'CURRENT_TURN',
      sensitive: false,
    },
    {
      term: '在职',
      canonicalValue: '在岗',
      aliasValues: ['在职'],
      metricCode,
      metricFieldId: 'field_employee_total_count',
      dimensionCode: 'employee_status',
      dimensionName: '人员状态',
      dimensionFieldId: 'field_employee_status',
      dimensionFieldName: 'employee_status',
      dimensionFieldDescription: '员工在组织内的当前状态',
      vectorQuery: '员工在组织内的当前状态:在岗',
      vectorModel: 'test-embedding-model',
      vectorDimensions: 2560,
      vectorSearchStatus: 'SUCCEEDED',
      vectorCandidateCount: 1,
      vectorCandidateMemberKeys: ['在岗'],
      whereDesignStatus: 'SUCCEEDED',
      whereDesignOperator: 'EQUALS',
      whereDesignReason: '用户值映射为一个受治理成员，应使用等值条件',
      whereDesignModel: 'test-model',
      matchMethod: 'SEMANTIC_MAPPING',
      candidateCount: 1,
      candidateMemberKeys: ['在岗'],
      selectedMemberKeys: ['在岗'],
      whereCondition: "employee_status = '在岗'",
      compiledCondition: 'field_employee_status = :employee_status_1',
      candidateFilter: { inputCount: 1, acceptedCount: 1, rejectedCount: 0, status: 'PASS', rules: [] },
      selected: true,
      source: followUp ? 'CONTEXT_PLAN' : 'CURRENT_TURN',
      sensitive: false,
    },
    ...(followUp ? [{
      term: '关键人才',
      canonicalValue: '关键人才',
      aliasValues: ['关键人才'],
      metricCode,
      metricFieldId: 'field_employee_total_count',
      dimensionCode: 'key_talent',
      dimensionName: '关键人才',
      dimensionFieldId: 'field_key_talent',
      dimensionFieldName: 'key_talent',
      dimensionFieldDescription: '标识员工是否被评定为关键人才',
      vectorQuery: '标识员工是否被评定为关键人才:关键人才',
      vectorModel: 'test-embedding-model',
      vectorDimensions: 2560,
      vectorSearchStatus: 'SUCCEEDED',
      vectorCandidateCount: 54,
      vectorCandidateMemberKeys: keyTalentValues,
      whereDesignStatus: 'REUSED_DECISION_GRAPH',
      whereDesignOperator: 'CONTAINS',
      whereDesignReason: '用户按关键人才标签筛选，应使用包含条件',
      whereDesignModel: 'test-model',
      decisionId: 'decision-key-talent',
      matchMethod: 'SEMANTIC_TAG',
      candidateCount: 54,
      candidateMemberKeys: keyTalentValues,
      selectedMemberKeys: keyTalentValues,
      whereCondition: "key_talent LIKE '%关键人才%'",
      compiledCondition: 'field_key_talent IN (:key_talent_1 … :key_talent_54)',
      candidateFilter: { inputCount: 54, acceptedCount: 54, rejectedCount: 0, status: 'PASS', rules: [] },
      selected: true,
      source: 'CURRENT_TURN',
      sensitive: false,
    }] : []),
  ]
  const dimensions = [
    { dimensionCode: 'birth_cohort', dimensionName: '出生年代段', memberKeys: ['80-85', '85-90'] },
    { dimensionCode: 'employee_status', dimensionName: '人员状态', memberKeys: ['在岗'] },
    ...(followUp ? [{ dimensionCode: 'key_talent', dimensionName: '关键人才', memberKeys: keyTalentValues }] : []),
  ]
  return {
    conversationQuestions: followUp
      ? ['80后小微在职人员有多少人？', '其中的关键人才有多少？']
      : ['80后小微在职人员有多少人？'],
    contextPolicy: 'CURRENT_TURN_OVERRIDES_SAME_DIMENSION_THEN_LATEST_VERIFIED_PLAN',
    standaloneQuestion: followUp
      ? '在小微人员范围内，查询出生年代段=80后（映射：80-85、85-90）、人员状态=在职（映射：在岗）、关键人才=关键人才（映射：54 个已治理标准值）条件下的员工总人数。'
      : '在小微人员范围内，查询出生年代段=80后（映射：80-85、85-90）、人员状态=在职（映射：在岗）条件下的员工总人数。',
    extraction: {
      intent: 'METRIC',
      metricTerms: ['员工总人数'],
      dimensionValueTerms: followUp ? ['80后', '在职', '关键人才'] : ['80后', '在职'],
    },
    metricCandidates: [{
      code: metricCode,
      label: '员工总人数',
      matchedTerm: '员工总人数',
      matchMethod: followUp ? 'CONTEXT_PLAN' : 'EXACT_CATALOG',
      score: 1,
      selected: true,
      source: followUp ? 'CONTEXT_PLAN' : 'CURRENT_TURN',
    }],
    dimensionValueLookups: lookups,
    finalSelections: [{
      metricCode,
      metricName: '员工总人数',
      metricFieldId: 'field_employee_total_count',
      metricVersionId: 'metric-version-workforce',
      datasetVersionId: 'dataset-version-workforce',
      dimensions,
      whereCondition: followUp
        ? "birth_cohort IN ('80-85', '85-90') AND employee_status = '在岗' AND key_talent LIKE '%关键人才%'"
        : "birth_cohort IN ('80-85', '85-90') AND employee_status = '在岗'",
      compiledCondition: followUp
        ? 'field_birth_cohort IN (:birth_cohort_1 … :birth_cohort_2) AND field_employee_status = :employee_status_1 AND field_key_talent IN (:key_talent_1 … :key_talent_54)'
        : 'field_birth_cohort IN (:birth_cohort_1 … :birth_cohort_2) AND field_employee_status = :employee_status_1',
      planId: followUp
        ? '00000000-0000-4000-8000-000000000004'
        : '00000000-0000-4000-8000-000000000003',
      planStatus: 'READY',
    }],
    assessments: mockAssessments(1, lookups.length),
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
