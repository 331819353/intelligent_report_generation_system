import {
  BookmarkSimple,
  CaretDown,
  ChartBarHorizontal,
  CheckCircle,
  Database,
  DownloadSimple,
  FileText,
  LinkSimple,
  PaperPlaneRight,
  Path,
  Plus,
  Pulse,
  ShareNetwork,
  ShieldCheck,
  Sparkle,
  ThumbsDown,
  ThumbsUp,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { BarChart } from 'echarts/charts'
import { AriaComponent, GridComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { useEffect, useRef, useState, useSyncExternalStore, type FormEvent, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AddToReportDialog } from '../components/ask-data/AddToReportDialog'
import { CreateDecisionDialog } from '../components/decision/CreateDecisionDialog'
import { ClarificationCard } from '../components/ask-data/ClarificationCard'
import { ConversationRail } from '../components/ask-data/ConversationRail'
import { ConversationOutcome } from '../components/ask-data/ConversationOutcome'
import { ConversationProgress } from '../components/ask-data/ConversationProgress'
import { EvidencePanel } from '../components/ask-data/EvidencePanel'
import { FeedbackDialog } from '../components/ask-data/FeedbackDialog'
import { ResultWorkspace } from '../components/ask-data/ResultWorkspace'
import { SaveQuestionDialog } from '../components/ask-data/SaveQuestionDialog'
import { ReleaseDriftCard } from '../components/ask-data/ReleaseDriftCard'
import { ReleaseDriftEvidencePanel } from '../components/ask-data/ReleaseDriftEvidencePanel'
import { latestQuestionState } from '../components/ask-data/conversation-progress'
import { questionResultReady } from '../components/ask-data/result-presentation'
import { renderTimeSpec } from '../askdata/format/timespec'
import { useAskDataQuestion } from '../hooks/use-ask-data-question'
import { MyDataRequests } from '../askdata/datarequest/MyDataRequests'
import type { DataRequestPrefill } from '../askdata/datarequest/DataRequestDialog'
import '../styles/ask-data.css'
import '../styles/data-request.css'
import {
  mapAskDataError,
  questionAPI,
  type ClarificationOption,
  type ConversationSummary,
  type QuestionFeedbackSubmission,
  type QuestionResult,
  type QuestionRun,
  type QuestionRunEvent,
  type QuestionRunState,
  type ReleaseDrift,
} from '../lib/ask-data-api'
import { currentDomain, subscribeDomainChange } from '../lib/domain-context'
import {
  buildAskDataAttachmentContext,
  clearAskDataAttachmentDraft,
  readAskDataAttachmentDraft,
  saveAskDataAttachmentDraft,
  type AskDataAttachmentDraftItem,
} from '../lib/ask-data-attachments'

type WorkbenchMode = 'idle' | 'snapshot-release-drift' | 'snapshot-clarification' | 'snapshot-running' | 'snapshot-complete' | 'snapshot-result' | 'snapshot-answer-degraded' | 'snapshot-scope-detail' | 'live'
type WorkspaceView = 'ask' | 'requests'
type Feedback = 'helpful' | 'report' | null
type Contribution = { channel: string; delta: number; margin: string; monthOverMonth: string; reason: string }
type EvidenceSectionProps = {
  id: string
  title: string
  icon: ReactNode
  open: boolean
  onToggle: (id: string) => void
  children: ReactNode
}

const MARGIN_QUESTION = '哪些渠道导致本月毛利率下降？'

const CONTRIBUTIONS: Contribution[] = [
  { channel: '线上分销', delta: -0.72, margin: '1.48%', monthOverMonth: '-2.31%', reason: '价格竞争加剧，促销力度提升导致毛利下滑' },
  { channel: '电商平台', delta: -0.36, margin: '2.25%', monthOverMonth: '-1.42%', reason: '平台补贴增加，佣金费率上调' },
  { channel: '线下经销', delta: -0.08, margin: '4.31%', monthOverMonth: '-0.29%', reason: '部分区域串货影响价格体系' },
  { channel: 'KA卖场', delta: 0.06, margin: '5.18%', monthOverMonth: '+0.18%', reason: '高毛利新品占比提升' },
  { channel: '直营门店', delta: 0.03, margin: '6.02%', monthOverMonth: '+0.12%', reason: '高端产品销售结构改善' },
  { channel: '其他渠道', delta: 0.02, margin: '4.76%', monthOverMonth: '+0.08%', reason: '费用投放保持稳定' },
]

const EVIDENCE_SECTIONS = ['intent', 'policy', 'lineage', 'quality'] as const

const DEMO_RELEASE_DRIFT: ReleaseDrift = {
  conversationId: '00000000-0000-4000-8000-000000000121',
  pinnedAt: '2026-08-08T10:15:00+08:00',
  previous: {
    releaseId: '00000000-0000-4000-8000-000000000123', contentHash: 'b'.repeat(64),
    semanticVersion: '2026.08', status: 'SUPERSEDED',
  },
  active: {
    releaseId: '00000000-0000-4000-8000-000000000124', contentHash: 'c'.repeat(64),
    semanticVersion: '2026.08.1', status: 'ACTIVE',
  },
  changes: [
    {
      objectType: 'METRIC', objectId: '00000000-0000-4000-8000-000000000125', name: '已支付订单销售额',
      fromVersion: 'v3.2', toVersion: 'v3.3', changeKind: 'UPDATED', summary: '退款扣除规则更新',
    },
    {
      objectType: 'DIMENSION', objectId: '00000000-0000-4000-8000-000000000126', name: '销售渠道',
      fromVersion: 'v2.8', toVersion: 'v2.9', changeKind: 'UPDATED', summary: '渠道归类规则更新',
    },
  ],
}

const DEMO_PROGRESS_STATES: QuestionRunState[] = [
  'RECEIVED',
  'AUTHORIZED',
  'UNDERSTANDING',
  'RETRIEVING',
  'BINDING',
  'GRAPH_VALIDATING',
  'IR_READY',
  'EXECUTING',
  'RESULT_VERIFYING',
]

const DEMO_RUNNING_EVENTS: QuestionRunEvent[] = DEMO_PROGRESS_STATES.map((state, index) => ({
  eventId: `00000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`,
  eventIndex: index + 1,
  runVersion: index + 1,
  state,
  type: 'STATE_TRANSITION',
  status: state === 'RESULT_VERIFYING' ? 'STARTED' : 'SUCCEEDED',
  evidenceIds: [],
	graphDegraded: false,
  createdAt: `2026-08-06T10:38:${String(21 + index).padStart(2, '0')}+08:00`,
}))

const DEMO_COMPLETE_EVENTS: QuestionRunEvent[] = [
  ...DEMO_RUNNING_EVENTS.map(event => event.state === 'RESULT_VERIFYING' ? { ...event, status: 'SUCCEEDED' as const } : event),
  {
    eventId: '00000000-0000-4000-8000-000000000010',
    eventIndex: 10,
    runVersion: 10,
    state: 'ANSWERED',
    type: 'STATE_TRANSITION',
    status: 'SUCCEEDED',
    evidenceIds: [],
		graphDegraded: false,
    createdAt: '2026-08-06T10:38:31+08:00',
  },
]

const DEMO_CLARIFICATION_RUN: QuestionRun = {
  runId: '00000000-0000-4000-8000-000000000120',
  conversationId: '00000000-0000-4000-8000-000000000121',
  state: 'CLARIFICATION_REQUIRED',
  disposition: 'CLARIFY',
  completion: {
    code: 'METRIC_DEFINITION_AMBIGUOUS',
    artifactId: '00000000-0000-4000-8000-000000000122',
    artifactType: 'CLARIFICATION',
    artifactHash: 'a'.repeat(64),
    evidenceIds: ['evidence:paid-sales', 'evidence:net-sales'],
    clarification: {
      clarificationId: '00000000-0000-4000-8000-000000000122',
      conflictCode: 'METRIC_DEFINITION_AMBIGUOUS',
      message: '检测到 2 个可用口径，请选择本次分析使用的定义。',
      options: [
        {
          optionId: 'clarification-option:paid-sales',
          label: '已支付订单销售额',
          difference: '是否扣除已确认退款',
          evidenceIds: ['evidence:paid-sales'],
          evidence: {
            definition: '已支付订单金额，扣除取消订单，不扣除后续退款。',
            owner: { id: 'owner:finance-data', displayName: '王敏 · 财务数据组' },
            semanticVersion: 'v3.2',
            semanticStatus: 'CERTIFIED',
            time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
            quality: { status: 'PASS', scorePermillion: 987000, dataAsOf: '2026-08-06T10:30:00+08:00', rulesPassed: 12, rulesTotal: 12 },
          },
        },
        {
          optionId: 'clarification-option:net-sales',
          label: '净销售额',
          difference: '是否扣除已确认退款',
          evidenceIds: ['evidence:net-sales'],
          evidence: {
            definition: '已支付订单金额，扣除取消订单和已确认退款。',
            owner: { id: 'owner:business-analysis', displayName: '李楠 · 经营分析组' },
            semanticVersion: 'v2.8',
            semanticStatus: 'CERTIFIED',
            time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
            quality: { status: 'PASS', scorePermillion: 979000, dataAsOf: '2026-08-06T10:26:00+08:00', rulesPassed: 11, rulesTotal: 11 },
          },
        },
      ],
    },
  },
  release: { releaseId: '00000000-0000-4000-8000-000000000123', contentHash: 'b'.repeat(64) },
  hashes: {},
  budget: {
    limits: { maxSteps: 16, maxLlmCalls: 4, maxToolCalls: 8, maxFormalQueries: 2, maxValidationQueries: 3, maxDurationMs: 25_000 },
    usage: { stepCount: 7, llmCallsUsed: 2, toolCallsUsed: 4, formalQueriesUsed: 0, validationQueriesUsed: 0, elapsedMs: 1_860, exhausted: false },
  },
  recordVersion: 12,
  lastEventId: 12,
  createdAt: '2026-08-06T10:38:00+08:00',
  updatedAt: '2026-08-06T10:38:06+08:00',
  completedAt: '2026-08-06T10:38:06+08:00',
  allowedActions: [],
}

const DEMO_SCOPE_DETAIL_RUN: QuestionRun = {
  ...DEMO_CLARIFICATION_RUN,
  runId: '00000000-0000-4000-8000-000000000131',
  state: 'BLOCKED',
  disposition: 'REFUSE',
  completion: {
    code: 'SCOPE_DETAIL_LIST', artifactId: '00000000-0000-4000-8000-000000000134', artifactType: 'BLOCK', artifactHash: 'd'.repeat(64), evidenceIds: [],
    scopeVerdict: {
      schemaVersion: 'question-scope-verdict-v1', type: 'DETAIL_LIST', outcome: 'OUT_OF_SCOPE',
      reason: 'SCOPE_DETAIL_LIST',
      userMessage: '智能问数仅返回受治理的汇总分析，明细数据请提交取数申请。',
      nextActions: [{
        kind: 'DATA_REQUEST', label: '发起明细取数申请',
        payload: { target: 'DATA_REQUEST_DIALOG', prefill: 'CURRENT_QUESTION' },
      }],
      parsedContext: {
        metricIds: ['00000000-0000-4000-8000-000000000132'],
        dimensionIds: ['00000000-0000-4000-8000-000000000133'],
        timeRange: {
          start: '2026-08-01T00:00:00+08:00', endExclusive: '2026-09-01T00:00:00+08:00',
          timezone: 'Asia/Shanghai', grain: 'MONTH',
        },
      },
      lexiconVersion: 'askdata-scope-lexicon-2026.08', lexiconHash: 'e'.repeat(64), classificationSource: 'RULE',
    },
  },
  recordVersion: 8,
  lastEventId: 8,
  completedAt: '2026-08-08T10:38:06+08:00',
}

const DEMO_RESULT_DETAIL_ROWS: QuestionResult['datasets'][number]['rows'] = [
  { rank: '1', channel: '电商渠道', sales: '6102430', share: '47.5', orders: '12358', average_order_value: '494.05' },
  { rank: '2', channel: '线下门店', sales: '3248760', share: '25.3', orders: '8721', average_order_value: '372.33' },
  { rank: '3', channel: '经销商渠道', sales: '2156890', share: '16.8', orders: '6142', average_order_value: '351.45' },
  { rank: '4', channel: '企业客户', sales: '921430', share: '7.2', orders: '1258', average_order_value: '732.46' },
  { rank: '5', channel: '其他渠道', sales: '416810', share: '3.2', orders: '980', average_order_value: '424.29' },
  ...Array.from({ length: 43 }, (_, index) => {
    const rank = index + 6
    const sales = Math.max(18_000, 398_000 - index * 8_100)
    const orders = Math.max(42, 920 - index * 17)
    return {
      rank: String(rank), channel: `渠道明细 ${String(rank).padStart(2, '0')}`,
      sales: String(sales), share: (sales / 12_846_320 * 100).toFixed(1),
      orders: String(orders), average_order_value: (sales / orders).toFixed(2),
    }
  }),
]

const DEMO_RESULT: QuestionResult = {
  schemaVersion: 'question-result-v1',
  title: '本月已支付订单销售额',
  resolvedTimeSpec: {
    requestedPeriod: 'CURRENT_MONTH', grain: 'MONTH', policyApplied: 'MTD', policySource: 'TIME_CONTRACT',
    resolvedStart: '2026-08-01T00:00:00+08:00', resolvedEndExclusive: '2026-08-07T00:00:00+08:00',
    dataAvailableThrough: '2026-08-06T10:30:00+08:00', truncatedByDataAvailability: true,
    periodFallbackApplied: false, timezone: 'Asia/Shanghai',
    comparison: {
      type: 'YEAR_OVER_YEAR', periods: 1, alignment: 'SAME_DAY_COUNT',
      resolvedStart: '2025-08-01T00:00:00+08:00', resolvedEndExclusive: '2025-08-07T00:00:00+08:00', overflowApplied: false,
    },
  },
  timeSpec: {
    rangeLabel: '2026-08-01 至 2026-08-06', asOfLabel: '数据截止 2026-08-06', policyLabel: '本月至今（MTD）',
    comparisonLabel: '对比期 2025-08-01 至 2025-08-06，按相同天数对齐',
    truncatedHint: '数据仅更新至 2026-08-06，结果已按可用范围裁剪',
  },
  summary: {
    metricLabel: '已支付订单销售额', value: '12846320', formattedValue: '¥12,846,320', unit: 'CNY',
    comparison: {
      label: '较上年同期', direction: 'UP', changePermillion: 86000, formattedChange: '+8.6%',
      baselineStart: '2025-08-01', baselineEnd: '2025-08-06',
    },
    time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
  },
  evidenceIds: ['evidence:paid-sales'],
  evidence: DEMO_CLARIFICATION_RUN.completion?.clarification?.options[0].evidence,
  datasets: [
    {
      id: 'dataset:sales-trend', label: '每日销售额趋势', page: 1, pageSize: 6, totalRows: 6,
      columns: [
        { key: 'day', label: '日期', type: 'DATE', role: 'DIMENSION' },
        { key: 'sales', label: '销售额（元）', type: 'DECIMAL', role: 'MEASURE' },
      ],
      rows: [
        { day: '2026-08-01', sales: '1976540' }, { day: '2026-08-02', sales: '1982310' },
        { day: '2026-08-03', sales: '2067890' }, { day: '2026-08-04', sales: '2128650' },
        { day: '2026-08-05', sales: '2324110' }, { day: '2026-08-06', sales: '2366820' },
      ],
    },
    {
      id: 'dataset:channel-contribution', label: '渠道销售额贡献', page: 1, pageSize: 5, totalRows: 5,
      columns: [
        { key: 'channel', label: '渠道', type: 'STRING', role: 'DIMENSION' },
        { key: 'sales', label: '销售额（元）', type: 'DECIMAL', role: 'MEASURE' },
      ],
      rows: [
        { channel: '电商渠道', sales: '6102430' }, { channel: '线下门店', sales: '3248760' },
        { channel: '经销商渠道', sales: '2156890' }, { channel: '企业客户', sales: '921430' },
        { channel: '其他渠道', sales: '416810' },
      ],
    },
    {
      id: 'dataset:channel-detail', label: '渠道销售额明细', page: 1, pageSize: 5, totalRows: 48,
      columns: [
        { key: 'rank', label: '排名', type: 'INTEGER', role: 'DIMENSION' },
        { key: 'channel', label: '渠道', type: 'STRING', role: 'DIMENSION' },
        { key: 'sales', label: '销售额（元）', type: 'DECIMAL', role: 'MEASURE' },
        { key: 'share', label: '占比', type: 'DECIMAL', role: 'MEASURE' },
        { key: 'orders', label: '订单数', type: 'INTEGER', role: 'MEASURE' },
        { key: 'average_order_value', label: '客单价（元）', type: 'DECIMAL', role: 'MEASURE' },
      ],
      rows: DEMO_RESULT_DETAIL_ROWS,
    },
    {
      id: 'dataset:operations-bundle', label: '核心经营指标组', page: 1, pageSize: 1, totalRows: 1,
      columns: [
        { key: 'sales', label: '销售额（元）', type: 'DECIMAL', role: 'MEASURE' },
        { key: 'orders', label: '已支付订单数', type: 'INTEGER', role: 'MEASURE' },
        { key: 'average_order_value', label: '客单价（元）', type: 'DECIMAL', role: 'MEASURE' },
        { key: 'active_customers', label: '活跃客户数', type: 'INTEGER', role: 'MEASURE' },
      ],
      rows: [{ sales: '12846320', orders: '29459', average_order_value: '436.07', active_customers: '18632' }],
    },
  ],
  views: [
    { id: 'view:trend', type: 'LINE', label: '趋势', datasetId: 'dataset:sales-trend', dimensionKeys: ['day'], measureKeys: ['sales'] },
    { id: 'view:channel', type: 'BAR', label: '渠道', datasetId: 'dataset:channel-contribution', dimensionKeys: ['channel'], measureKeys: ['sales'] },
    { id: 'view:bundle', type: 'KPI_BUNDLE', label: '指标组', datasetId: 'dataset:operations-bundle', dimensionKeys: [], measureKeys: ['sales', 'orders', 'average_order_value', 'active_customers'] },
    { id: 'view:detail', type: 'TABLE', label: '明细', datasetId: 'dataset:channel-detail', dimensionKeys: ['rank', 'channel'], measureKeys: ['sales', 'share', 'orders', 'average_order_value'] },
  ],
  defaultViewId: 'view:trend',
  recommendedViewId: 'view:trend',
  reportSources: [{
    reportId: '00000000-0000-4000-8000-000000000211',
    reportVersionId: '00000000-0000-4000-8000-000000000212',
    componentId: '00000000-0000-4000-8000-000000000213',
    reportTitle: '月度经营分析报告', componentTitle: '核心经营指标概览', componentType: 'METRIC_CARD_GROUP',
    componentVersion: '3', semanticReleaseId: '00000000-0000-4000-8000-000000000101',
    componentHash: 'f'.repeat(64), citationStatus: 'CITED', accessStatus: 'AUTHORIZED_AT_RUN',
    openPath: '/reports/00000000-0000-4000-8000-000000000211?versionId=00000000-0000-4000-8000-000000000212',
  }],
}

const DEMO_RESULT_RUN: QuestionRun = {
  ...DEMO_CLARIFICATION_RUN,
  runId: '00000000-0000-4000-8000-000000000130',
  parentRunId: DEMO_CLARIFICATION_RUN.runId,
  state: 'ANSWERED',
  disposition: 'DIRECT',
  completion: {
    code: 'ANSWER_READY', artifactId: '00000000-0000-4000-8000-000000000135', artifactType: 'ANSWER', artifactHash: 'c'.repeat(64),
    evidenceIds: DEMO_RESULT.evidenceIds, result: DEMO_RESULT,
  },
  recordVersion: 18,
  lastEventId: 18,
  updatedAt: '2026-08-07T10:38:31+08:00',
  completedAt: '2026-08-07T10:38:31+08:00',
  allowedActions: ['SAVE', 'SHARE', 'EXPORT', 'CREATE_DECISION', 'ADD_TO_REPORT'],
}

const DEMO_DEGRADED_RUN: QuestionRun = {
  ...DEMO_RESULT_RUN,
  completion: {
    ...DEMO_RESULT_RUN.completion!,
    code: 'ANSWER_DEGRADED',
    answer: {
      schemaVersion: '1.0', narrativeDegraded: true,
      hint: '本次未生成文字结论，请查看数据与口径。',
      verification: { attempts: 2, passed: false },
    },
  },
}

registerEChartsComponents([BarChart, GridComponent, TitleComponent, TooltipComponent, AriaComponent, CanvasRenderer])

function ContributionChart() {
  const chartRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!chartRef.current) return undefined
    const chart = init(chartRef.current)
    chart.setOption({
      animationDuration: 420,
      aria: { enabled: true, description: '各渠道毛利率贡献对比。直营网和经销渠道为主要负向贡献。' },
      grid: { left: 16, right: 70, top: 24, bottom: 22, containLabel: true },
      title: { text: '对毛利率变化的影响', right: 18, top: 72, textStyle: { color: '#7c8491', fontSize: 10, fontWeight: 500 } },
      tooltip: {
        trigger: 'axis',
        axisPointer: { type: 'shadow' },
        backgroundColor: '#ffffff',
        borderColor: '#dfe4ea',
        borderWidth: 1,
        textStyle: { color: '#273142', fontSize: 11 },
      },
      xAxis: { type: 'value', min: -1.5, max: 1, position: 'top', splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#9299a4', fontSize: 9, formatter: (value: number) => value.toFixed(2) }, axisLine: { lineStyle: { color: '#bfc7d2' } }, axisTick: { show: false } },
      yAxis: { type: 'category', inverse: true, data: CONTRIBUTIONS.map(item => item.channel), axisLabel: { color: '#4b5565', fontSize: 10, margin: 16 }, axisLine: { show: false }, axisTick: { show: false } },
      series: [
        {
          name: '毛利率变化',
          type: 'bar',
          barWidth: 10,
          data: CONTRIBUTIONS.map(item => ({ value: item.delta, itemStyle: { color: item.delta < 0 ? '#1769e8' : '#85b5f6', borderRadius: item.delta < 0 ? [3, 0, 0, 3] : [0, 3, 3, 0] } })),
          label: { show: true, position: ({ value }: { value: number }) => value < 0 ? 'left' : 'right', color: ({ value }: { value: number }) => value < 0 ? '#ef4444' : '#12a16f', fontSize: 9, formatter: ({ value }: { value: number }) => `${value > 0 ? '+' : ''}${value.toFixed(2)}` },
        },
      ],
    })
    const resize = () => chart.resize()
    const observer = new ResizeObserver(resize)
    observer.observe(chartRef.current)
    return () => {
      observer.disconnect()
      chart.dispose()
    }
  }, [])

  return <div className="ask-contribution-chart" ref={chartRef} role="img" aria-label="各渠道毛利率和毛利额贡献对比图" />
}

function EvidenceSection({ id, title, icon, open, onToggle, children }: EvidenceSectionProps) {
  return (
    <section className="ask-evidence-section">
      <button type="button" aria-expanded={open} aria-controls={`evidence-${id}`} onClick={() => onToggle(id)}>
        <span className="ask-evidence-title-icon">{icon}</span>
        <span>{title}</span>
        <CaretDown className={open ? 'is-open' : ''} size={14} aria-hidden="true" />
      </button>
      {open && <div id={`evidence-${id}`} className="ask-evidence-body">{children}</div>}
    </section>
  )
}

/** WEB-003：真实 Question API/SSE 生命周期、会话历史与受控证据呈现。 */
export function AskDataPage() {
  const navigate = useNavigate()
  const { conversationId: routeConversationId } = useParams()
  const searchParams = new URLSearchParams(window.location.search)
  // 设计走查快照只在开发构建中可用；生产构建永远从真实空态开始，
  // 不得把演示用的澄清、漂移与结果数据当作用户的分析结论展示。
  const snapshot = import.meta.env.DEV ? searchParams.get('snapshot') : null
  const incomingQuestion = searchParams.get('q')?.trim() || ''
  const incomingRunId = searchParams.get('runId')?.trim() || ''
  const incomingAttachmentsEnabled = searchParams.get('attachments') === '1'
  const incomingAutoSubmit = searchParams.get('autoSubmit') === '1'
  const dataRequestSnapshot = snapshot === 'data-requests'
  const dataRequestWorkspace = dataRequestSnapshot || searchParams.get('workspace') === 'data-requests'
  const scopeDetailSnapshot = snapshot === 'scope-detail'
  const answerDegradedSnapshot = snapshot === 'answer-degraded'
  const resultSnapshot = snapshot === 'result'
  const designSnapshot = dataRequestSnapshot || scopeDetailSnapshot || answerDegradedSnapshot || resultSnapshot
  const liveDomainName = useSyncExternalStore(
    subscribeDomainChange,
    () => currentDomain()?.name ?? '当前业务领域',
    () => '企业经营',
  )
  const domainName = designSnapshot ? '企业经营' : liveDomainName
  const [question, setQuestion] = useState(scopeDetailSnapshot ? '导出本月订单明细' : resultSnapshot ? MARGIN_QUESTION : incomingQuestion)
  const [incomingAttachments, setIncomingAttachments] = useState<AskDataAttachmentDraftItem[]>(() => designSnapshot || !incomingAttachmentsEnabled ? [] : readAskDataAttachmentDraft())
  const autoSubmitRef = useRef('')
  const [followUp, setFollowUp] = useState('')
  const [mode, setMode] = useState<WorkbenchMode>(() => {
    if (incomingRunId) return 'live'
    if (scopeDetailSnapshot) return 'snapshot-scope-detail'
    if (answerDegradedSnapshot) return 'snapshot-answer-degraded'
    if (resultSnapshot) return 'snapshot-complete'
    return 'idle'
  })
  const [workspaceView, setWorkspaceView] = useState<WorkspaceView>(dataRequestWorkspace ? 'requests' : 'ask')
  const [dataRequestDialogOpen, setDataRequestDialogOpen] = useState(false)
  const [dataRequestPrefill, setDataRequestPrefill] = useState<DataRequestPrefill | undefined>()
  const [activeConversationID, setActiveConversationID] = useState(resultSnapshot ? '00000000-0000-4000-8000-000000000100' : routeConversationId ?? '')
  const [activeConversationLabel, setActiveConversationLabel] = useState(resultSnapshot ? MARGIN_QUESTION : '')
  const [historyRefreshKey, setHistoryRefreshKey] = useState(0)
  const [addToReportOpen, setAddToReportOpen] = useState(false)
  const [createDecisionOpen, setCreateDecisionOpen] = useState(false)
  const [saveQuestionOpen, setSaveQuestionOpen] = useState(false)
  const [toast, setToast] = useState('')
  const [selectedClarificationOption, setSelectedClarificationOption] = useState<ClarificationOption | undefined>(
    DEMO_CLARIFICATION_RUN.completion?.clarification?.options[0],
  )
  const [expandedResult, setExpandedResult] = useState(false)
  const [feedback, setFeedback] = useState<Feedback>(null)
  const [feedbackDialogOpen, setFeedbackDialogOpen] = useState(false)
  const [feedbackBusy, setFeedbackBusy] = useState(false)
  const [feedbackError, setFeedbackError] = useState('')
  const [openEvidence, setOpenEvidence] = useState<Record<string, boolean>>(() => Object.fromEntries(EVIDENCE_SECTIONS.map(id => [id, true])))
  const {
    state: questionState,
    createQuestion,
    resumeQuestion,
    submitClarification,
    confirmReleaseDrift,
    cancel: cancelQuestion,
    reset: resetQuestion,
  } = useAskDataQuestion()

  useEffect(() => {
    if (!incomingRunId) return undefined
    void resumeQuestion(incomingRunId)
    return () => { cancelQuestion() }
  }, [cancelQuestion, incomingRunId, resumeQuestion])

  useEffect(() => {
    if (!routeConversationId || designSnapshot || incomingRunId) return undefined
    let cancelled = false
    void questionAPI.getConversation(routeConversationId, { runLimit: 1 }).then(detail => {
      if (cancelled) return undefined
		setToast('')
      setActiveConversationID(detail.conversation.conversationId)
      setActiveConversationLabel(current => detail.conversation.label === '分析会话' && current
        ? current
        : detail.conversation.label)
      // A conversation title is navigation metadata, not the next question.
      // Using the fallback title ("分析会话") as composer content caused an
      // accidental follow-up whenever users reopened a blocked conversation.
      setQuestion('')
      setMode('live')
      return resumeQuestion(detail.conversation.latestRunId)
    }).catch(cause => {
      if (!cancelled) {
        setMode('idle')
        setToast(mapAskDataError(cause).message)
      }
    })
    return () => { cancelled = true }
  }, [designSnapshot, incomingRunId, resumeQuestion, routeConversationId])

  useEffect(() => {
    if (!incomingAutoSubmit || !incomingQuestion || incomingRunId || routeConversationId || dataRequestWorkspace) return
    const requestKey = `${incomingQuestion}:${incomingAttachments.map(item => item.id).join(',')}`
    if (autoSubmitRef.current === requestKey) return
    autoSubmitRef.current = requestKey

    const attachmentContext = buildAskDataAttachmentContext(incomingAttachments)
    const submittedPayload = attachmentContext ? `${incomingQuestion}\n\n${attachmentContext}` : incomingQuestion
    const consumedParams = new URLSearchParams(window.location.search)
    consumedParams.delete('autoSubmit')
    navigate(`/ask-data?${consumedParams.toString()}`, { replace: true })

    setMode('live')
    setToast('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    setExpandedResult(false)
    setActiveConversationLabel(incomingQuestion)
    setQuestion('')

    void createQuestion(submittedPayload).then(run => {
      if (!run) return
      if (attachmentContext) {
        clearAskDataAttachmentDraft()
        setIncomingAttachments([])
      }
      setActiveConversationID(run.conversationId)
      setHistoryRefreshKey(value => value + 1)
      if (!designSnapshot) navigate(`/ask-data/conversations/${run.conversationId}`)
    })
  }, [
    createQuestion,
    dataRequestWorkspace,
    designSnapshot,
    incomingAttachments,
    incomingAutoSubmit,
    incomingQuestion,
    incomingRunId,
    navigate,
    routeConversationId,
  ])

  const activeReleaseDrift = mode === 'snapshot-release-drift'
    ? DEMO_RELEASE_DRIFT
    : mode === 'live' && questionState.error?.kind === 'RELEASE_DRIFT'
      ? questionState.error.releaseDrift
      : undefined

  const liveClarificationRun = mode === 'live' && questionState.run?.state === 'CLARIFICATION_REQUIRED'
    ? questionState.run
    : undefined
  const activeClarificationRun = mode === 'snapshot-clarification' ? DEMO_CLARIFICATION_RUN : liveClarificationRun
  const liveResultRun = mode === 'live' && questionState.run?.state === 'ANSWERED' && questionResultReady(questionState.run.completion?.result)
    ? questionState.run
    : undefined
  const activeResultRun = mode === 'snapshot-answer-degraded' ? DEMO_DEGRADED_RUN : mode === 'snapshot-result' ? DEMO_RESULT_RUN : liveResultRun
  const graphDegraded = mode === 'snapshot-result' || mode === 'snapshot-answer-degraded' || questionState.events.some(event => event.graphDegraded)

  const liveActive = mode === 'live' && ['CREATING', 'CONNECTING', 'STREAMING', 'RECONNECTING'].includes(questionState.phase)
  const progressEvents = mode === 'snapshot-running'
    ? DEMO_RUNNING_EVENTS
    : mode === 'snapshot-complete' ? DEMO_COMPLETE_EVENTS : questionState.events
  const progressState: QuestionRunState = mode === 'snapshot-running'
    ? 'RESULT_VERIFYING'
    : mode === 'snapshot-complete'
      ? 'ANSWERED'
      : questionState.run?.state
        ?? latestQuestionState(questionState.operation?.state ?? 'RECEIVED', questionState.events)
  const progressPhase = mode === 'snapshot-running'
    ? 'PREVIEW' as const
    : mode === 'snapshot-complete' ? 'TERMINAL' as const : questionState.phase
  const showProgress = mode === 'snapshot-running' || mode === 'live' && questionState.phase !== 'IDLE'
  // 只有设计走查快照才允许展示演示证据；真实模式（idle/live）一律展示受控空态，
  // 避免把示例指标口径、可信度分与血缘当成用户本次分析的事实。
  const demoEvidence = mode !== 'live' && mode !== 'idle'

  const submitQuestion = (event: FormEvent) => {
    event.preventDefault()
    const submittedQuestion = verifiedAnswerVisible ? followUp.trim() : question.trim()
    if (!submittedQuestion || liveActive) return
    const attachmentContext = verifiedAnswerVisible ? '' : buildAskDataAttachmentContext(incomingAttachments)
    const submittedPayload = attachmentContext ? `${submittedQuestion}\n\n${attachmentContext}` : submittedQuestion
    setMode('live')
		setToast('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    setExpandedResult(false)
    if (!activeConversationID || !activeConversationLabel || activeConversationLabel === '分析会话') {
      setActiveConversationLabel(submittedQuestion)
    }
    if (verifiedAnswerVisible) setFollowUp('')
    else setQuestion('')
    void createQuestion(submittedPayload, activeConversationID || undefined).then(run => {
      if (!run) return
      if (attachmentContext) {
        clearAskDataAttachmentDraft()
        setIncomingAttachments([])
      }
      setActiveConversationID(run.conversationId)
      setHistoryRefreshKey(value => value + 1)
      if (!designSnapshot) navigate(`/ask-data/conversations/${run.conversationId}`)
    })
  }

  const startNewQuestion = () => {
    setWorkspaceView('ask')
		setToast('')
    resetQuestion()
    setQuestion('')
    setIncomingAttachments([])
    clearAskDataAttachmentDraft()
    setFollowUp('')
    setMode('idle')
    setActiveConversationID('')
    setActiveConversationLabel('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    setExpandedResult(false)
    if (!designSnapshot) navigate('/ask-data')
  }

  const choosePrompt = (prompt: string) => {
    resetQuestion()
    if (verifiedAnswerVisible) setFollowUp(prompt)
    else {
      setQuestion(prompt)
      setMode('idle')
    }
    setFeedbackDialogOpen(false)
  }

  const chooseConversation = (conversation: ConversationSummary) => {
    resetQuestion()
		setToast('')
    setActiveConversationID(conversation.conversationId)
    setActiveConversationLabel(conversation.label)
    setQuestion('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    if (designSnapshot) {
      setMode(conversation.conversationId.endsWith('100') ? 'snapshot-complete' : 'snapshot-result')
      return
    }
    setMode('live')
    navigate(`/ask-data/conversations/${conversation.conversationId}`)
    void resumeQuestion(conversation.latestRunId)
  }

  const retryQuestion = () => {
    if (!question.trim()) return
    setMode('live')
		setToast('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    void createQuestion(question)
  }

  const retryNarrative = () => {
    if (mode === 'snapshot-answer-degraded') {
      setMode('snapshot-running')
      return
    }
    retryQuestion()
  }

  const cancelVisibleRun = () => {
    if (mode === 'live') {
      cancelQuestion()
      return
    }
    setMode('idle')
  }

  const cancelClarification = () => {
    resetQuestion()
    setMode('idle')
    setSelectedClarificationOption(undefined)
  }

  const continueClarification = (optionId: string) => {
    if (mode === 'snapshot-clarification') {
      setMode('snapshot-running')
      return
    }
    void submitClarification(optionId)
  }

  const switchReleaseAndAnalyze = () => {
    if (!activeReleaseDrift) return
    if (mode === 'snapshot-release-drift') {
      setMode('snapshot-running')
      return
    }
    void confirmReleaseDrift(question, activeReleaseDrift)
  }

  const viewHistoricalResult = () => {
    resetQuestion()
    setMode('snapshot-result')
  }

  const toggleEvidence = (id: string) => {
    setOpenEvidence(current => ({ ...current, [id]: !current[id] }))
  }

  const submitStructuredFeedback = async (submission: QuestionFeedbackSubmission) => {
    try {
      if (mode === 'live') await questionAPI.feedback(submission)
      setFeedback('report')
      setFeedbackError('')
    } catch (error) {
      throw mapAskDataError(error)
    }
  }

  const submitHelpfulFeedback = async () => {
    if (!activeResultRun || feedbackBusy || feedback === 'helpful') return
    setFeedbackBusy(true)
    setFeedbackError('')
    try {
      if (mode === 'live') {
        await questionAPI.feedback({
          runId: activeResultRun.runId,
          runVersion: activeResultRun.recordVersion,
          rating: 'ACCURATE',
          issueType: 'NONE',
          comment: '',
        })
      }
      setFeedback('helpful')
    } catch (error) {
      setFeedbackError(mapAskDataError(error).message)
    } finally {
      setFeedbackBusy(false)
    }
  }

  const openStructuredFeedback = () => {
    setFeedbackError('')
    setFeedbackDialogOpen(true)
  }

  const openManualDataRequest = () => {
    setDataRequestPrefill(undefined)
    setDataRequestDialogOpen(true)
  }

  const openScopeDataRequest = (verdict: NonNullable<QuestionRun['completion']>['scopeVerdict']) => {
    if (!verdict || verdict.reason !== 'SCOPE_DETAIL_LIST') return
    setDataRequestPrefill({
      ...(mode === 'snapshot-scope-detail'
        ? { sourceQuestionRunId: DEMO_SCOPE_DETAIL_RUN.runId }
        : questionState.run?.runId ? { sourceQuestionRunId: questionState.run.runId } : {}),
      requestText: question,
      parsedContext: verdict.parsedContext,
    })
    setWorkspaceView('requests')
    setDataRequestDialogOpen(true)
  }

	const presentedRun = mode === 'snapshot-complete' ? DEMO_RESULT_RUN : activeResultRun ?? questionState.run
	const verifiedAnswerVisible = mode === 'snapshot-complete' || Boolean(activeResultRun)
	const activeResultTime = activeResultRun?.completion?.result?.resolvedTimeSpec
		? renderTimeSpec(activeResultRun.completion.result.resolvedTimeSpec)
		: undefined
	const conversationContextLabel = verifiedAnswerVisible
		? activeResultTime
			? `${activeResultTime.asOfLabel} · ${activeResultTime.rangeLabel}`
			: '已通过受控结果校验'
		: `当前领域：${domainName} · 仅使用已发布语义口径`
  const evidenceQuestion = activeConversationLabel || question
  const notify = (message: string) => {
    setToast(message)
    window.setTimeout(() => setToast(''), 2600)
  }
  const exportVisibleResult = () => {
    const existingExport = document.querySelector<HTMLButtonElement>('.ask-result-detail header button')
    if (existingExport) {
      existingExport.click()
      notify('已导出当前可见结果，并附带时间口径')
      return
    }
    const rows = [['渠道', '影响（百分点）', '本月毛利率', '环比变化', '主要原因'], ...CONTRIBUTIONS.map(item => [item.channel, item.delta.toFixed(2), item.margin, item.monthOverMonth, item.reason])]
    const csv = rows.map(row => row.map(value => `"${value.replaceAll('"', '""')}"`).join(',')).join('\n')
    const url = URL.createObjectURL(new Blob([`\uFEFF${csv}`], { type: 'text/csv;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url; link.download = '渠道毛利率贡献分析.csv'; link.click(); URL.revokeObjectURL(url)
    notify('已导出当前结果')
  }
  const shareConversation = async () => {
    const href = activeConversationID
      ? `${window.location.origin}/ask-data/conversations/${activeConversationID}`
      : window.location.href
    try {
      await navigator.clipboard.writeText(href)
      notify('会话链接已复制，打开时仍会校验当前用户权限')
    } catch {
      notify('浏览器未允许复制，请从地址栏复制链接')
    }
  }

  return (
    <AppShell
      className={`ask-data-shell ${workspaceView === 'requests' ? 'data-request-mode-shell' : ''} ${workspaceView === 'ask' && verifiedAnswerVisible ? 'ask-data-result-shell' : ''} ${workspaceView === 'ask' && activeReleaseDrift ? 'ask-data-drift-shell' : ''}`.trim()}
      eyebrow="可信问数"
      title="问数工作台"
      titleMeta={<span className="ask-release-badge"><span />{domainName} · Release {designSnapshot ? '2026.08.1' : activeReleaseDrift?.active.semanticVersion ?? '2026.08'}</span>}
      lockBusinessDomain
      actions={workspaceView === 'requests' ? <button className="primary-button ask-topbar-button" type="button" onClick={openManualDataRequest}><Plus size={15} aria-hidden="true" />新建取数申请</button> : undefined}
    >
      {workspaceView === 'requests' ? <MyDataRequests
        snapshot={designSnapshot}
        dialogOpen={dataRequestDialogOpen}
        dialogPrefill={dataRequestPrefill}
        onDialogOpenChange={setDataRequestDialogOpen}
      /> : <div className="ask-workbench">
        <ConversationRail snapshot={designSnapshot} activeConversationId={activeConversationID} refreshKey={historyRefreshKey} onNew={startNewQuestion} onSelect={chooseConversation} onActiveChange={conversation => setActiveConversationLabel(conversation.label)} />

        <section className="ask-conversation" aria-label="问数对话与结果">
          <header className="ask-conversation-heading">
            <div><div className="ask-conversation-title-row"><h2>{activeConversationLabel || question || '开始一个新问题'}</h2>{verifiedAnswerVisible && <span><CheckCircle size={13} weight="fill" />已验证</span>}</div><p>{conversationContextLabel}</p></div>
            <div className="ask-answer-actions">
              <button type="button" title={presentedRun && !presentedRun.allowedActions.includes('SAVE') ? '当前结果尚未形成可收藏的语义快照' : undefined} disabled={!verifiedAnswerVisible || !presentedRun?.allowedActions.includes('SAVE')} onClick={() => setSaveQuestionOpen(true)}><BookmarkSimple size={16} />收藏</button>
              <button type="button" title={presentedRun && !presentedRun.allowedActions.includes('ADD_TO_REPORT') ? '该历史结果未保留完整报告快照，可重新提问后加入报告' : undefined} disabled={!verifiedAnswerVisible || !presentedRun?.allowedActions.includes('ADD_TO_REPORT')} onClick={() => setAddToReportOpen(true)}><FileText size={16} />加入报告</button>
              <button type="button" title={presentedRun && !presentedRun.allowedActions.includes('CREATE_DECISION') ? '当前结果尚未形成可固定的验证证据' : undefined} disabled={!verifiedAnswerVisible || !presentedRun?.allowedActions.includes('CREATE_DECISION')} onClick={() => designSnapshot ? notify('设计快照不写入业务数据；真实答案会固定证据并创建决策') : setCreateDecisionOpen(true)}><Path size={16} />形成决策</button>
              <button type="button" disabled={!verifiedAnswerVisible} onClick={exportVisibleResult}><DownloadSimple size={16} />导出<CaretDown size={12} /></button>
              <button type="button" disabled={!activeConversationID} onClick={() => void shareConversation()}><ShareNetwork size={16} />分享</button>
            </div>
          </header>

          <div className="ask-conversation-scroll">
          {showProgress && <ConversationProgress
            phase={progressPhase}
            currentState={progressState}
            events={progressEvents}
            onCancel={liveActive || mode === 'snapshot-running' ? cancelVisibleRun : undefined}
          />}

          {activeClarificationRun && <ClarificationCard
            run={activeClarificationRun}
            submitting={mode === 'live' && questionState.phase === 'CREATING'}
            error={mode === 'live' ? questionState.error : undefined}
            onSubmit={continueClarification}
            onCancel={cancelClarification}
            onSelectionChange={setSelectedClarificationOption}
          />}

          {activeReleaseDrift && <ReleaseDriftCard
            drift={activeReleaseDrift}
            busy={mode === 'live' && questionState.phase === 'CREATING'}
            error={mode === 'live' && questionState.error?.kind !== 'RELEASE_DRIFT' ? questionState.error?.message : undefined}
            onConfirm={switchReleaseAndAnalyze}
            onHistory={viewHistoricalResult}
          />}

          {activeResultRun?.completion?.result && <ResultWorkspace
            result={activeResultRun.completion.result}
            answer={activeResultRun.completion.answer}
            onRetryNarrative={activeResultRun.completion.answer?.narrativeDegraded ? retryNarrative : undefined}
          />}

          {mode === 'live' && !activeReleaseDrift && !activeClarificationRun && !activeResultRun && <ConversationOutcome state={questionState} onRetry={retryQuestion} onDataRequest={openScopeDataRequest} />}

          {mode === 'snapshot-scope-detail' && <ConversationOutcome
            state={{ phase: 'TERMINAL', run: DEMO_SCOPE_DETAIL_RUN, events: [], lastEventId: DEMO_SCOPE_DETAIL_RUN.lastEventId }}
            onRetry={() => setMode('idle')}
            onDataRequest={openScopeDataRequest}
          />}

          {mode === 'idle' && <section className="ask-empty-state">
            <div><Sparkle size={22} weight="duotone" /></div>
            <h2>准备好分析这个问题</h2>
            <p>发送后将先确认业务口径，再查询已发布的数据资产。</p>
            <button className="primary-button" type="button" onClick={() => document.querySelector<HTMLFormElement>('.ask-question-composer')?.requestSubmit()} disabled={!question.trim()}>开始分析</button>
          </section>}

          {mode === 'snapshot-complete' && <div className="ask-result-stream">
            <section className="ask-answer-card">
              <p>本月毛利率较上月<strong>下降 1.35 个百分点</strong>，主要受渠道<em>线上分销</em>和<em>电商平台</em>毛利率下降影响，两者合计拉低 1.08 个百分点，占总降幅的 <strong>80%</strong>。</p>
              <dl><div><dt>毛利率环比变化</dt><dd>−1.35 <small>百分点</small></dd><span>4.82% → 3.47%</span></div><div><dt>本月毛利率</dt><dd>3.47<small>%</small></dd><span>2026-08 · 数据截至 08-09</span></div></dl>
            </section>

            <section className="ask-result-card">
              <header>
                <div><ChartBarHorizontal size={16} weight="duotone" aria-hidden="true" /><span><strong>渠道毛利率环比变化贡献（百分点）</strong><small>本月 vs 上月</small></span></div>
                <span className="ask-verified-chip"><ShieldCheck size={13} weight="fill" aria-hidden="true" />已验证</span>
              </header>
              <ContributionChart />
              <div className="ask-findings-heading"><strong>关键发现</strong><WarningCircle size={12} aria-hidden="true" /></div>
              <div className="ask-result-table-wrap">
                <table>
                  <caption className="sr-only">各渠道本月毛利率及环比贡献</caption>
                  <thead><tr><th>#</th><th>发现</th><th>影响（百分点）</th><th>本月毛利率</th><th>环比变化</th><th>主要原因</th></tr></thead>
                  <tbody>
                    {(expandedResult ? CONTRIBUTIONS : CONTRIBUTIONS.slice(0, 3)).map((item, index) => <tr key={item.channel}>
                      <td>{index + 1}</td><td><strong>{item.channel}</strong></td>
                      <td className={item.delta < 0 ? 'negative' : 'positive'}>{item.delta > 0 ? '+' : ''}{item.delta.toFixed(2)}pp</td>
                      <td>{item.margin}</td><td className={item.monthOverMonth.startsWith('-') ? 'negative' : 'positive'}>{item.monthOverMonth}</td><td>{item.reason}</td>
                    </tr>)}
                  </tbody>
                </table>
              </div>
              <button className="ask-expand-result" type="button" onClick={() => setExpandedResult(expanded => !expanded)}>{expandedResult ? '收起明细' : '查看全部发现（6 条）'}<CaretDown className={expandedResult ? 'is-open' : ''} size={13} aria-hidden="true" /></button>
            </section>
          </div>}
          </div>

          <form className="ask-question-composer" onSubmit={submitQuestion}>
            {!verifiedAnswerVisible && incomingAttachments.length > 0 && <div className="ask-composer-attachments" aria-label="本次问数附件">
              {incomingAttachments.map(item => <span key={item.id}><FileText size={14} /><strong>{item.name}</strong><button type="button" aria-label={`移除附件 ${item.name}`} onClick={() => setIncomingAttachments(current => {
                const next = current.filter(attachment => attachment.id !== item.id)
                saveAskDataAttachmentDraft(next)
                return next
              })}><X size={12} /></button></span>)}
            </div>}
            <label className="ask-composer-field">
              <span className="sr-only">输入业务问题</span>
              <textarea value={verifiedAnswerVisible ? followUp : question} maxLength={500} rows={2} onChange={event => verifiedAnswerVisible ? setFollowUp(event.target.value) : setQuestion(event.target.value)} placeholder={verifiedAnswerVisible ? '继续追问，深入分析或对比其他维度…' : '试着问：本月哪些渠道影响了毛利率？'} />
              <small>{verifiedAnswerVisible ? followUp.length : question.length}/500</small>
            </label>
            <div className="ask-composer-footer">
              <div className="ask-smart-suggestions"><span><Sparkle size={12} weight="fill" />推荐追问</span><button type="button" onClick={() => choosePrompt('线上分销中，哪些区域下降最多？')}>线上分销哪些区域下降最多</button><button type="button" onClick={() => choosePrompt('电商平台按产品线拆分对比')}>电商平台按产品线对比</button></div>
              <button type="submit" disabled={!(verifiedAnswerVisible ? followUp.trim() : question.trim()) || liveActive}><PaperPlaneRight size={16} weight="fill" />发送</button>
            </div>
          </form>
          <p className="ask-ai-disclaimer">内容由 AI 生成，请结合业务实际审慎参考 <WarningCircle size={11} aria-hidden="true" /></p>
        </section>

        <aside className="ask-evidence-panel" aria-label="证据与可信度">
          {activeReleaseDrift ? <ReleaseDriftEvidencePanel question={evidenceQuestion} drift={activeReleaseDrift} /> : activeClarificationRun ? <EvidencePanel
            question={evidenceQuestion}
            run={activeClarificationRun}
            option={selectedClarificationOption}
          /> : activeResultRun?.completion?.result ? <EvidencePanel
            question={evidenceQuestion}
            run={activeResultRun}
            result={activeResultRun.completion.result}
            graphDegraded={graphDegraded}
            answer={activeResultRun.completion.answer}
          /> : <>
          <header className="ask-evidence-heading">
            <div><span className={`ask-live-dot ${demoEvidence ? '' : 'is-pending'}`.trim()} /><span><strong>证据与可信度</strong><small>{demoEvidence ? '数据完整、口径一致、校验通过' : '仅展示已确认的受控证据'}</small></span></div>
            <span className={`ask-trust-score ${demoEvidence ? '' : 'is-pending'}`.trim()}>{!demoEvidence ? '—' : mode === 'snapshot-scope-detail' ? '100' : '92'}</span>
          </header>

          {!demoEvidence ? <div className="ask-live-evidence-state" role="status">
            <span><ShieldCheck size={22} weight="duotone" aria-hidden="true" /></span>
            <strong>等待受控证据</strong>
            <p>指标、口径、数据链路与质量信息通过校验后才会显示。</p>
          </div> : <div className="ask-evidence-sections">
            <EvidenceSection id="intent" title="指标口径（已绑定）" icon={<Sparkle size={14} weight="fill" />} open={openEvidence.intent} onToggle={toggleEvidence}>
              <dl className="ask-evidence-grid">
                <div className="is-wide"><dt>指标</dt><dd>{mode === 'snapshot-scope-detail' ? '订单' : '毛利率（%）'}</dd></div>
                <div className="is-wide"><dt>定义</dt><dd>{mode === 'snapshot-scope-detail' ? '逐行明细' : '（营业收入 − 营业成本）/ 营业收入 × 100%'}</dd></div>
                <div><dt>管理方</dt><dd>财务管理部</dd></div>
                <div><dt>负责人</dt><dd>张磊</dd></div>
              </dl>
              <p className="ask-confidence-note"><CheckCircle size={13} weight="fill" />{mode === 'snapshot-scope-detail' ? '确定性范围分类已验证' : '语义绑定置信度 98%'}</p>
            </EvidenceSection>

            <EvidenceSection id="policy" title="时间范围" icon={<ShieldCheck size={14} weight="fill" />} open={openEvidence.policy} onToggle={toggleEvidence}>
              <div className="ask-source-card">
                <span className="ask-source-icon blue"><Database size={14} /></span>
                <span><strong>{mode === 'snapshot-scope-detail' ? '可信问数范围门禁' : '本月（自然月）'}</strong><small>{mode === 'snapshot-scope-detail' ? 'SCOPE_DETAIL_LIST · 规则固定' : '2026-08-01 ～ 2026-08-09'}</small></span>
                <span className="ask-source-status">{mode === 'snapshot-scope-detail' ? '已阻断查询' : '已确认'}</span>
              </div>
              <p className="ask-policy-note"><ShieldCheck size={12} weight="fill" />{mode === 'snapshot-scope-detail' ? '领域固定为“企业经营”，拒答不返回任何结果行' : '已按“家电经营分析”领域权限过滤'}</p>
            </EvidenceSection>

            {mode !== 'snapshot-scope-detail' && <EvidenceSection id="lineage" title="数据来源（5）" icon={<LinkSimple size={14} />} open={openEvidence.lineage} onToggle={toggleEvidence}>
              <ol className="ask-lineage-list">
                <li><span>1</span><div><strong>ERP－订单事实表</strong><small>运行正常</small></div></li>
                <li><span>2</span><div><strong>ERP－成本事实表</strong><small>运行正常</small></div></li>
                <li><span>3</span><div><strong>CRM－渠道维度表</strong><small>另有 2 个已验证数据源</small></div></li>
              </ol>
            </EvidenceSection>}

            {mode !== 'snapshot-scope-detail' && <EvidenceSection id="quality" title="数据时效" icon={<Pulse size={14} weight="bold" />} open={openEvidence.quality} onToggle={toggleEvidence}>
              <div className="ask-quality-score"><strong>92</strong><span>可信度<small>口径一致、校验通过</small></span></div>
              <dl className="ask-freshness-list"><div><dt>数据截至</dt><dd>2026-08-09 23:59:59</dd></div><div><dt>更新频率</dt><dd>每日 08:10</dd></div></dl>
            </EvidenceSection>}
          </div>}

          </>}

          {activeResultRun && <section className="ask-feedback-card">
            <h2>这个答案有帮助吗？</h2>
            <div>
              <button type="button" aria-label="答案有帮助" aria-pressed={feedback === 'helpful'} disabled={feedbackBusy} onClick={() => void submitHelpfulFeedback()}><ThumbsUp size={15} /></button>
              <button type="button" aria-label="答案没有帮助" aria-pressed={feedback === 'report'} disabled={feedbackBusy} onClick={openStructuredFeedback}><ThumbsDown size={15} /></button>
              <button type="button" aria-pressed={feedback === 'report'} disabled={feedbackBusy} onClick={openStructuredFeedback}><WarningCircle size={14} />报告问题</button>
            </div>
            {feedback && <p role="status">{feedback === 'helpful' ? '感谢反馈，我们会继续保持。' : '反馈已进入人工复核，不会直接修改答案。'}</p>}
            {feedbackError && <p className="is-error" role="alert">{feedbackError}</p>}
          </section>}
        </aside>
      </div>}

      {workspaceView === 'ask' && activeResultRun && <FeedbackDialog
        open={feedbackDialogOpen}
        run={activeResultRun}
        onClose={() => setFeedbackDialogOpen(false)}
        onSubmit={submitStructuredFeedback}
      />}
      {workspaceView === 'ask' && <AddToReportDialog
        open={addToReportOpen}
        snapshot={designSnapshot}
        run={presentedRun}
        onClose={() => setAddToReportOpen(false)}
        onApplied={() => notify('已加入报告草稿，证据与当前问数运行已固定')}
      />}
      {workspaceView === 'ask' && saveQuestionOpen && presentedRun && <SaveQuestionDialog
        open={saveQuestionOpen}
        run={presentedRun}
        question={activeConversationLabel || question || presentedRun.completion?.result?.title || '经营分析'}
        snapshot={designSnapshot}
        onClose={() => setSaveQuestionOpen(false)}
        onSaved={() => notify('已收藏，可从首页或常用问题再次运行')}
      />}
      {workspaceView === 'ask' && createDecisionOpen && presentedRun?.completion?.artifactId && <CreateDecisionDialog
        open={createDecisionOpen}
        source={{
          type: 'ANSWER_ARTIFACT', id: presentedRun.completion.artifactId, label: '智能问数已验证答案',
          title: `${presentedRun.completion.result?.summary.metricLabel || activeConversationLabel || '经营分析'}决策`,
          question: activeConversationLabel || question || presentedRun.completion.result?.title || '',
          decision: presentedRun.completion.answer?.narrative?.summary || '',
          expectedEffect: `围绕“${presentedRun.completion.result?.summary.metricLabel || '关键经营指标'}”形成可执行方案，并在复盘日验证实际结果。`,
        }}
        onClose={() => setCreateDecisionOpen(false)}
        onCreated={decisionId => { setCreateDecisionOpen(false); navigate(`/decisions?decisionId=${encodeURIComponent(decisionId)}`) }}
      />}
      {toast && <div className="ask-toast" role="status"><CheckCircle size={16} weight="fill" />{toast}</div>}
    </AppShell>
  )
}
