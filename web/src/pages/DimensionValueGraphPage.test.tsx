import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  semanticGovernanceAPI,
  type DimensionWhereDecision,
  type DimensionWhereDecisionGroup,
} from '../lib/semantic-governance'
import { DimensionValueGraphPage } from './DimensionValueGraphPage'

afterEach(() => vi.restoreAllMocks())

const createdAt = '2026-07-28T00:00:00Z'

const decision = (
  id: string,
  overrides: Partial<DimensionWhereDecision> = {},
): DimensionWhereDecision => ({
  id,
  vectorKey: '员工在组织内的当前状态:在岗',
  vectorKeyHash: 'a'.repeat(64),
  embeddingModel: 'Qwen3-Embedding-4B',
  dimensionId: 'dimension-status',
  dimensionName: '人员状态',
  dimensionFieldId: 'field-status',
  dimensionFieldName: 'employee_status',
  dimensionDescription: '员工在组织内的当前状态',
  canonicalValue: '在岗',
  aliases: ['在职'],
  selectedMemberCount: 1,
  metricId: 'metric-1',
  metricVersionId: 'metric-version-1',
  datasetVersionId: 'dataset-version-1',
  metricCode: 'employee_total',
  metricName: '员工总人数',
  metricFieldId: 'field_employee_total',
  materializationId: 'materialization-1',
  tableSchema: 'warehouse_published',
  tableName: 'dws_employee',
  predicateOperator: 'EQUALS',
  whereCondition: "employee_status = '在岗'",
  compiledCondition: 'employee_status = :employee_status_1',
  llmModel: 'MiniMax-M2',
  llmPromptVersion: 'dws-dimension-where-policy-v1',
  llmReason: '标准状态单值匹配',
  latestQueryPlanId: '',
  dimensionMemberId: 'member-active',
  sourceType: 'DWS_PRECOMPUTED',
  sourceInputHash: 'b'.repeat(64),
  observationCount: 1,
  firstSeenAt: createdAt,
  lastSeenAt: createdAt,
  ...overrides,
})

const group = (
  dimensionId: string,
  overrides: Partial<DimensionWhereDecisionGroup> = {},
): DimensionWhereDecisionGroup => ({
  dimensionId,
  dimensionName: '人员状态',
  dimensionFieldName: 'employee_status',
  dimensionDescription: '员工在组织内的当前状态',
  memberIndexPolicy: 'FULL',
  memberCount: 4,
  decisionCount: 4,
  pendingVectorCount: 0,
  metricCount: 1,
  tableCount: 1,
  buildStatus: 'READY',
  lastBuiltAt: createdAt,
  ...overrides,
})

describe('DimensionValueGraphPage', () => {
  test('lists every DWS dimension and lazily loads persisted decisions', async () => {
    vi.spyOn(semanticGovernanceAPI, 'evaluatePermission')
      .mockResolvedValue({ allowed: true })
    vi.spyOn(semanticGovernanceAPI, 'listWhereDecisionGroups')
      .mockResolvedValue({
        items: [
          group('dimension-status'),
          group('dimension-talent', {
            dimensionName: '关键人才',
            dimensionFieldName: 'key_talent',
            dimensionDescription: '标识员工是否被评定为关键人才',
            memberCount: 71,
            decisionCount: 71,
          }),
          group('dimension-position', {
            dimensionName: '岗位编码',
            dimensionFieldName: 'position_code',
            dimensionDescription: '员工岗位编码',
            memberIndexPolicy: 'EXACT_ONLY',
            memberCount: 0,
            decisionCount: 0,
            buildStatus: 'EXACT_ONLY',
          }),
        ],
        total: 3,
      })
    const decisions = vi.spyOn(
      semanticGovernanceAPI, 'listWhereDecisions',
    ).mockImplementation(async (_q, _table, dimensionId) => ({
      items: dimensionId === 'dimension-talent'
        ? [decision('decision-talent', {
          vectorKey: '标识员工是否被评定为关键人才:关键人才',
          dimensionId: 'dimension-talent',
          dimensionName: '关键人才',
          dimensionFieldId: 'field-talent',
          dimensionFieldName: 'key_talent',
          dimensionDescription: '标识员工是否被评定为关键人才',
          canonicalValue: '关键人才',
          aliases: ['核心人才'],
          selectedMemberCount: 54,
          predicateOperator: 'CONTAINS',
          whereCondition: "key_talent LIKE '%关键人才%'",
          sourceType: 'QUERY_OBSERVED',
        })]
        : [decision('decision-status-active')],
      total: dimensionId === 'dimension-talent' ? 71 : 4,
      limit: 50,
      offset: 0,
    }))

    render(<MemoryRouter><DimensionValueGraphPage /></MemoryRouter>)

    expect(await screen.findByRole('heading', {
      name: '按维度查看“维度字段：维度值 → 指标字段 → WHERE”',
    })).toBeInTheDocument()
    expect(await screen.findByText('人员状态')).toBeInTheDocument()
    expect(screen.getByText('关键人才')).toBeInTheDocument()
    expect(screen.getByText('岗位编码')).toBeInTheDocument()
    expect(screen.getAllByText('75')).toHaveLength(2)
    expect(await screen.findByText('employee_status：在岗'))
      .toBeInTheDocument()
    expect(screen.getByText(/DWS全量预计算/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', {
      name: /关键人才.*key_talent/,
    }))
    expect(await screen.findByText("key_talent LIKE '%关键人才%'"))
      .toBeInTheDocument()
    expect(decisions).toHaveBeenCalledWith(
      '', '', 'dimension-talent', 50, 0,
    )
  })

  test('does not request the removed legacy relationship canvas data', async () => {
    vi.spyOn(semanticGovernanceAPI, 'evaluatePermission')
      .mockResolvedValue({ allowed: true })
    vi.spyOn(semanticGovernanceAPI, 'listWhereDecisionGroups')
      .mockResolvedValue({
        items: [group('dimension-status')],
        total: 1,
      })
    vi.spyOn(semanticGovernanceAPI, 'listWhereDecisions').mockResolvedValue({
      items: [decision('decision-status-active')],
      total: 1,
      limit: 50,
      offset: 0,
    })
    const dimensions = vi.spyOn(semanticGovernanceAPI, 'listDimensions')
    const members = vi.spyOn(semanticGovernanceAPI, 'listMembers')
    const compatibilities = vi.spyOn(
      semanticGovernanceAPI, 'listCompatibilities',
    )

    render(<MemoryRouter><DimensionValueGraphPage /></MemoryRouter>)

    expect(await screen.findByText("employee_status = '在岗'"))
      .toBeInTheDocument()
    expect(screen.queryByText('决策入口')).not.toBeInTheDocument()
    expect(screen.queryByText('维度值 → 维度名称 → 指标名称'))
      .not.toBeInTheDocument()
    expect(screen.queryByLabelText('搜索决策图成员')).not.toBeInTheDocument()
    expect(dimensions).not.toHaveBeenCalled()
    expect(members).not.toHaveBeenCalled()
    expect(compatibilities).not.toHaveBeenCalled()
  })

  test('shows all DWS dimensions even when decisions are still building', async () => {
    vi.spyOn(semanticGovernanceAPI, 'evaluatePermission')
      .mockResolvedValue({ allowed: true })
    vi.spyOn(semanticGovernanceAPI, 'listWhereDecisionGroups')
      .mockResolvedValue({
        items: [group('dimension-status', {
          memberCount: 4,
          decisionCount: 0,
          pendingVectorCount: 4,
          buildStatus: 'BUILDING',
        })],
        total: 1,
      })
    const listDecisions = vi.spyOn(
      semanticGovernanceAPI, 'listWhereDecisions',
    )

    render(<MemoryRouter><DimensionValueGraphPage /></MemoryRouter>)

    expect(await screen.findByText('人员状态')).toBeInTheDocument()
    expect(screen.getAllByText('4')).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', {
      name: /人员状态.*employee_status/,
    }))
    expect(await screen.findByText(
      '该维度正在等待 LLM 策略或向量构建。',
    )).toBeInTheDocument()
    await waitFor(() => expect(listDecisions).not.toHaveBeenCalled())
  })
})
