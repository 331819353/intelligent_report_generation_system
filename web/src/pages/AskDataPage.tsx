import {
  ArrowRight,
  CaretDown,
  ChartBarHorizontal,
  ChatCircleDots,
  CheckCircle,
  ClipboardText,
  Database,
  LinkSimple,
  MagnifyingGlass,
  PaperPlaneRight,
  Plus,
  Pulse,
  ShieldCheck,
  Sparkle,
  ThumbsDown,
  ThumbsUp,
  WarningCircle,
} from '@phosphor-icons/react'
import { BarChart } from 'echarts/charts'
import { AriaComponent, GridComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { AppShell } from '../components/AppShell'
import { ClarificationCard } from '../components/ask-data/ClarificationCard'
import { ConversationOutcome } from '../components/ask-data/ConversationOutcome'
import { ConversationProgress } from '../components/ask-data/ConversationProgress'
import { EvidencePanel } from '../components/ask-data/EvidencePanel'
import { FeedbackDialog } from '../components/ask-data/FeedbackDialog'
import { ResultWorkspace } from '../components/ask-data/ResultWorkspace'
import { ReleaseDriftCard } from '../components/ask-data/ReleaseDriftCard'
import { ReleaseDriftEvidencePanel } from '../components/ask-data/ReleaseDriftEvidencePanel'
import { latestQuestionState } from '../components/ask-data/conversation-progress'
import { questionResultReady } from '../components/ask-data/result-presentation'
import { useAskDataQuestion } from '../hooks/use-ask-data-question'
import { MyDataRequests } from '../askdata/datarequest/MyDataRequests'
import type { DataRequestPrefill } from '../askdata/datarequest/DataRequestDialog'
import {
  mapAskDataError,
  questionAPI,
  type ClarificationOption,
  type QuestionFeedbackSubmission,
  type QuestionResult,
  type QuestionRun,
  type QuestionRunEvent,
  type QuestionRunState,
  type ReleaseDrift,
} from '../lib/ask-data-api'

type WorkbenchMode = 'idle' | 'snapshot-release-drift' | 'snapshot-clarification' | 'snapshot-running' | 'snapshot-complete' | 'snapshot-result' | 'snapshot-answer-degraded' | 'snapshot-scope-detail' | 'live'
type WorkspaceView = 'ask' | 'requests'
type Feedback = 'helpful' | 'report' | null
type Session = { id: string; title: string; meta: string; status: 'release-drift' | 'clarification' | 'running' | 'complete' | 'attention' }
type SessionGroup = { label: string; items: Session[] }
type Contribution = { channel: string; delta: number; impact: number }
type EvidenceSectionProps = {
  id: string
  title: string
  icon: ReactNode
  open: boolean
  onToggle: (id: string) => void
  children: ReactNode
}

const DEFAULT_QUESTION = '本月销售额是多少？'
const MARGIN_QUESTION = '哪些渠道导致本月毛利率下降？'

const SESSION_GROUPS: SessionGroup[] = [
  {
    label: '今天',
    items: [
      { id: 'sales', title: DEFAULT_QUESTION, meta: '10:38 · 待确认', status: 'release-drift' },
      { id: 'margin', title: MARGIN_QUESTION, meta: '10:35 · 已完成', status: 'complete' },
      { id: 'revenue', title: '华东区收入同比变化', meta: '10:32 · 已完成', status: 'complete' },
      { id: 'inventory', title: '冰箱库存周转异常', meta: '09:18 · 待确认', status: 'attention' },
    ],
  },
  {
    label: '昨天',
    items: [
      { id: 'product', title: '高端产品销售贡献', meta: '16:44 · 已完成', status: 'complete' },
      { id: 'region', title: '区域目标达成排名', meta: '14:06 · 已完成', status: 'complete' },
    ],
  },
]

const COMMON_QUESTIONS = [
  '本月销售额较上月有何变化？',
  '哪些区域未完成收入目标？',
  '高端产品的毛利贡献是多少？',
]

const CONTRIBUTIONS: Contribution[] = [
  { channel: '直营网', delta: -2.35, impact: -1256 },
  { channel: '经销', delta: -1.48, impact: -842 },
  { channel: '电商', delta: -0.28, impact: -156 },
  { channel: '海外', delta: 0.32, impact: 184 },
  { channel: '工程', delta: 0.41, impact: 236 },
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
}

const DEMO_SCOPE_DETAIL_RUN: QuestionRun = {
  ...DEMO_CLARIFICATION_RUN,
  runId: '00000000-0000-4000-8000-000000000131',
  state: 'BLOCKED',
  disposition: 'REFUSE',
  completion: {
    code: 'SCOPE_DETAIL_LIST', artifactType: 'BLOCK', artifactHash: 'd'.repeat(64), evidenceIds: [],
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
  ],
  views: [
    { id: 'view:trend', type: 'LINE', label: '趋势', datasetId: 'dataset:sales-trend', dimensionKeys: ['day'], measureKeys: ['sales'] },
    { id: 'view:channel', type: 'BAR', label: '渠道', datasetId: 'dataset:channel-contribution', dimensionKeys: ['channel'], measureKeys: ['sales'] },
    { id: 'view:detail', type: 'TABLE', label: '明细', datasetId: 'dataset:channel-detail', dimensionKeys: ['rank', 'channel'], measureKeys: ['sales', 'share', 'orders', 'average_order_value'] },
  ],
  defaultViewId: 'view:trend',
  recommendedViewId: 'view:trend',
}

const DEMO_RESULT_RUN: QuestionRun = {
  ...DEMO_CLARIFICATION_RUN,
  runId: '00000000-0000-4000-8000-000000000130',
  parentRunId: DEMO_CLARIFICATION_RUN.runId,
  state: 'ANSWERED',
  disposition: 'DIRECT',
  completion: {
    code: 'ANSWER_READY', artifactType: 'ANSWER', artifactHash: 'c'.repeat(64),
    evidenceIds: DEMO_RESULT.evidenceIds, result: DEMO_RESULT,
  },
  recordVersion: 18,
  lastEventId: 18,
  updatedAt: '2026-08-07T10:38:31+08:00',
  completedAt: '2026-08-07T10:38:31+08:00',
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
      grid: [
        { left: 48, right: '55%', top: 26, bottom: 25, containLabel: true },
        { left: '58%', right: 16, top: 26, bottom: 25, containLabel: true },
      ],
      title: [
        { text: '毛利率变化（百分点）', left: 8, top: 2, textStyle: { color: '#7c8491', fontSize: 10, fontWeight: 600 } },
        { text: '毛利额影响（万元）', left: '58%', top: 2, textStyle: { color: '#7c8491', fontSize: 10, fontWeight: 600 } },
      ],
      tooltip: {
        trigger: 'axis',
        axisPointer: { type: 'shadow' },
        backgroundColor: '#ffffff',
        borderColor: '#dfe4ea',
        borderWidth: 1,
        textStyle: { color: '#273142', fontSize: 11 },
      },
      xAxis: [
        { type: 'value', gridIndex: 0, min: -2.6, max: 0.8, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#9299a4', fontSize: 9 }, axisLine: { show: false }, axisTick: { show: false } },
        { type: 'value', gridIndex: 1, min: -1400, max: 400, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#9299a4', fontSize: 9 }, axisLine: { show: false }, axisTick: { show: false } },
      ],
      yAxis: [
        { type: 'category', gridIndex: 0, inverse: true, data: CONTRIBUTIONS.map(item => item.channel), axisLabel: { color: '#5d6572', fontSize: 10 }, axisLine: { show: false }, axisTick: { show: false } },
        { type: 'category', gridIndex: 1, inverse: true, data: CONTRIBUTIONS.map(item => item.channel), axisLabel: { show: false }, axisLine: { show: false }, axisTick: { show: false } },
      ],
      series: [
        {
          name: '毛利率变化',
          type: 'bar',
          xAxisIndex: 0,
          yAxisIndex: 0,
          barWidth: 11,
          data: CONTRIBUTIONS.map(item => ({ value: item.delta, itemStyle: { color: item.delta < 0 ? '#2864dc' : '#8db0ee', borderRadius: item.delta < 0 ? [4, 0, 0, 4] : [0, 4, 4, 0] } })),
          label: { show: true, position: 'right', color: '#5b6472', fontSize: 9, formatter: ({ value }: { value: number }) => `${value > 0 ? '+' : ''}${value.toFixed(2)}` },
        },
        {
          name: '毛利额影响',
          type: 'bar',
          xAxisIndex: 1,
          yAxisIndex: 1,
          barWidth: 11,
          data: CONTRIBUTIONS.map(item => ({ value: item.impact, itemStyle: { color: item.impact < 0 ? '#0e9f8a' : '#95d4ca', borderRadius: item.impact < 0 ? [4, 0, 0, 4] : [0, 4, 4, 0] } })),
          label: { show: true, position: 'right', color: '#5b6472', fontSize: 9, formatter: ({ value }: { value: number }) => `${value > 0 ? '+' : ''}${value}` },
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

/** WEB-003：真实 Question API/SSE 生命周期 + 受控事件进度；历史会话仍沿用 WEB-001 快照。 */
export function AskDataPage() {
  const snapshot = new URLSearchParams(window.location.search).get('snapshot')
  const dataRequestSnapshot = snapshot === 'data-requests'
  const scopeDetailSnapshot = snapshot === 'scope-detail'
  const answerDegradedSnapshot = snapshot === 'answer-degraded'
  const designSnapshot = dataRequestSnapshot || scopeDetailSnapshot || answerDegradedSnapshot
  const [question, setQuestion] = useState(scopeDetailSnapshot ? '导出本月订单明细' : DEFAULT_QUESTION)
  const [mode, setMode] = useState<WorkbenchMode>(scopeDetailSnapshot
    ? 'snapshot-scope-detail'
    : answerDegradedSnapshot ? 'snapshot-answer-degraded' : 'snapshot-release-drift')
  const [workspaceView, setWorkspaceView] = useState<WorkspaceView>(dataRequestSnapshot ? 'requests' : 'ask')
  const [dataRequestDialogOpen, setDataRequestDialogOpen] = useState(false)
  const [dataRequestPrefill, setDataRequestPrefill] = useState<DataRequestPrefill | undefined>()
  const [sessionQuery, setSessionQuery] = useState('')
  const [selectedSession, setSelectedSession] = useState('sales')
  const [selectedClarificationOption, setSelectedClarificationOption] = useState<ClarificationOption | undefined>(
    DEMO_CLARIFICATION_RUN.completion?.clarification?.options[0],
  )
  const [expandedResult, setExpandedResult] = useState(false)
  const [feedback, setFeedback] = useState<Feedback>(null)
  const [feedbackDialogOpen, setFeedbackDialogOpen] = useState(false)
  const [feedbackBusy, setFeedbackBusy] = useState(false)
  const [feedbackError, setFeedbackError] = useState('')
  const [openEvidence, setOpenEvidence] = useState<Record<string, boolean>>(() => Object.fromEntries(EVIDENCE_SECTIONS.map(id => [id, true])))
  const sessionSearchRef = useRef<HTMLInputElement>(null)
  const {
    state: questionState,
    createQuestion,
    submitClarification,
    confirmReleaseDrift,
    cancel: cancelQuestion,
    reset: resetQuestion,
  } = useAskDataQuestion()

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
  const showProgress = mode === 'snapshot-running' || mode === 'snapshot-complete' || mode === 'live' && questionState.phase !== 'IDLE'

  const filteredGroups = useMemo(() => {
    const normalized = sessionQuery.trim().toLocaleLowerCase()
    if (!normalized) return SESSION_GROUPS
    return SESSION_GROUPS
      .map(group => ({ ...group, items: group.items.filter(item => item.title.toLocaleLowerCase().includes(normalized)) }))
      .filter(group => group.items.length > 0)
  }, [sessionQuery])

  const submitQuestion = (event: FormEvent) => {
    event.preventDefault()
    if (!question.trim() || liveActive) return
    setMode('live')
    setSelectedSession('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    setExpandedResult(false)
    void createQuestion(question)
  }

  const startNewQuestion = () => {
    setWorkspaceView('ask')
    resetQuestion()
    setQuestion('')
    setMode('idle')
    setSelectedSession('')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    setExpandedResult(false)
  }

  const choosePrompt = (prompt: string) => {
    resetQuestion()
    setQuestion(prompt)
    setMode('idle')
    setSelectedSession('')
    setFeedbackDialogOpen(false)
  }

  const chooseSession = (session: Session) => {
    resetQuestion()
    setSelectedSession(session.id)
    setQuestion(session.id === 'margin' ? MARGIN_QUESTION : session.title)
    setMode(session.id === 'sales'
      ? 'snapshot-release-drift'
      : session.status === 'clarification'
      ? 'snapshot-clarification'
      : session.status === 'running' ? 'snapshot-running' : session.status === 'attention' ? 'idle' : 'snapshot-complete')
    if (session.status === 'clarification') {
      setSelectedClarificationOption(DEMO_CLARIFICATION_RUN.completion?.clarification?.options[0])
    }
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
  }

  const retryQuestion = () => {
    if (!question.trim()) return
    setMode('live')
    setFeedback(null)
    setFeedbackDialogOpen(false)
    setFeedbackError('')
    void createQuestion(question)
  }

  const retryNarrative = () => {
    if (mode === 'snapshot-answer-degraded') {
      setMode('snapshot-running')
      setSelectedSession('')
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
    setSelectedSession('')
  }

  const cancelClarification = () => {
    resetQuestion()
    setMode('idle')
    setSelectedSession('')
    setSelectedClarificationOption(undefined)
  }

  const continueClarification = (optionId: string) => {
    if (mode === 'snapshot-clarification') {
      setMode('snapshot-running')
      setSelectedSession('')
      return
    }
    void submitClarification(optionId)
  }

  const switchReleaseAndAnalyze = () => {
    if (!activeReleaseDrift) return
    if (mode === 'snapshot-release-drift') {
      setMode('snapshot-running')
      setSelectedSession('')
      return
    }
    void confirmReleaseDrift(question, activeReleaseDrift)
  }

  const viewHistoricalResult = () => {
    resetQuestion()
    setMode('snapshot-result')
    setSelectedSession('sales')
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

  return (
    <AppShell
      className={`ask-data-shell ${workspaceView === 'requests' ? 'data-request-mode-shell' : ''} ${workspaceView === 'ask' && activeResultRun ? 'ask-data-result-shell' : ''} ${workspaceView === 'ask' && activeReleaseDrift ? 'ask-data-drift-shell' : ''}`.trim()}
      eyebrow="可信问数"
      title="问数工作台"
      titleMeta={<span className="ask-release-badge"><span />企业经营 · Release {designSnapshot ? '2026.08.1' : activeReleaseDrift?.active.semanticVersion ?? '2026.08'}</span>}
      lockBusinessDomain
      actions={<>
        <div className="ask-workspace-tabs" role="tablist" aria-label="问数工作台视图">
          <button type="button" role="tab" aria-selected={workspaceView === 'ask'} onClick={() => setWorkspaceView('ask')}><ChatCircleDots size={15} weight={workspaceView === 'ask' ? 'fill' : 'regular'} aria-hidden="true" />问数</button>
          <button type="button" role="tab" aria-selected={workspaceView === 'requests'} onClick={() => setWorkspaceView('requests')}><ClipboardText size={15} weight={workspaceView === 'requests' ? 'fill' : 'regular'} aria-hidden="true" />我的申请</button>
        </div>
        {workspaceView === 'ask'
          ? <button className="primary-button ask-topbar-button" type="button" onClick={startNewQuestion}><Plus size={15} aria-hidden="true" />开始新问题</button>
          : <button className="primary-button ask-topbar-button" type="button" onClick={openManualDataRequest}><Plus size={15} aria-hidden="true" />新建取数申请</button>}
      </>}
    >
      {workspaceView === 'requests' ? <MyDataRequests
        snapshot={designSnapshot}
        dialogOpen={dataRequestDialogOpen}
        dialogPrefill={dataRequestPrefill}
        onDialogOpenChange={setDataRequestDialogOpen}
      /> : <div className="ask-workbench">
        <aside className="ask-session-rail" aria-label="问数会话">
          <div className="ask-rail-heading">
            <span>会话</span>
            <button type="button" aria-label="新建问题" onClick={startNewQuestion}><Plus size={15} /></button>
          </div>
          <label className="ask-session-search">
            <span className="sr-only">搜索会话</span>
            <MagnifyingGlass size={14} aria-hidden="true" />
            <input ref={sessionSearchRef} value={sessionQuery} onChange={event => setSessionQuery(event.target.value)} placeholder="搜索会话" />
          </label>
          <div className="ask-session-list">
            {filteredGroups.map(group => <section key={group.label}>
              <h2>{group.label}</h2>
              {group.items.map(session => <button
                className={selectedSession === session.id ? 'is-active' : ''}
                type="button"
                key={session.id}
                onClick={() => chooseSession(session)}
              >
                <span>{session.title}</span>
                <small className={session.status === 'attention' ? 'needs-attention' : ''}>{session.meta}</small>
              </button>)}
            </section>)}
            {filteredGroups.length === 0 && <p className="ask-empty-search">没有匹配的会话</p>}
          </div>
          <section className="ask-common-questions">
            <h2><Sparkle size={13} weight="fill" aria-hidden="true" />常用问题</h2>
            {COMMON_QUESTIONS.map(prompt => <button type="button" key={prompt} onClick={() => choosePrompt(prompt)}>{prompt}<ArrowRight size={12} aria-hidden="true" /></button>)}
          </section>
        </aside>

        <section className="ask-conversation" aria-label="问数对话与结果">
          <form className="ask-question-composer" onSubmit={submitQuestion}>
            <header><strong>用业务语言提问</strong></header>
            <label className="ask-composer-field">
              <span className="sr-only">输入业务问题</span>
              <textarea
                value={question}
                maxLength={500}
                rows={2}
                onChange={event => setQuestion(event.target.value)}
                placeholder="试着问：本月哪些渠道影响了毛利率？"
              />
              <small>{question.length}/500</small>
            </label>
            <div className="ask-composer-footer">
              <div className="ask-smart-suggestions">
                <span><Sparkle size={11} weight="fill" aria-hidden="true" />智能建议</span>
                <button type="button" onClick={() => choosePrompt('本月销售额按什么口径统计？')}>本月销售额按什么口径统计</button>
                <button type="button" onClick={() => choosePrompt('销售额是否包含退款？')}>销售额包含退款吗</button>
                <button type="button" onClick={() => choosePrompt('查看销售额口径说明')}>查看口径说明</button>
              </div>
              <button type="submit" aria-label="发送问题" disabled={!question.trim() || liveActive}><PaperPlaneRight size={18} weight="fill" /></button>
            </div>
          </form>

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
              <p><strong>本月毛利率下降主要由以下渠道贡献：</strong><em>直营网渠道</em>和<em>经销渠道</em>，两者合计解释本次下降的 93%。</p>
            </section>

            <section className="ask-result-card">
              <header>
                <div><ChartBarHorizontal size={15} weight="duotone" aria-hidden="true" /><span><strong>渠道贡献拆解</strong><small>本月 vs 上月</small></span></div>
                <span className="ask-verified-chip"><ShieldCheck size={13} weight="fill" aria-hidden="true" />已验证</span>
              </header>
              <ContributionChart />
              <div className="ask-result-table-wrap">
                <table>
                  <caption className="sr-only">各渠道本月毛利率及环比贡献</caption>
                  <thead><tr><th>渠道</th><th>本月毛利率</th><th>环比</th><th>毛利额影响</th></tr></thead>
                  <tbody>
                    {(expandedResult ? CONTRIBUTIONS : CONTRIBUTIONS.slice(0, 3)).map(item => <tr key={item.channel}>
                      <td>{item.channel}</td>
                      <td>{item.channel === '直营网' ? '18.42%' : item.channel === '经销' ? '20.18%' : item.channel === '电商' ? '24.09%' : item.channel === '海外' ? '27.31%' : '26.48%'}</td>
                      <td className={item.delta < 0 ? 'negative' : 'positive'}>{item.delta > 0 ? '+' : ''}{item.delta.toFixed(2)}pp</td>
                      <td className={item.impact < 0 ? 'negative' : 'positive'}>{item.impact > 0 ? '+' : ''}{item.impact.toLocaleString()} 万</td>
                    </tr>)}
                  </tbody>
                </table>
              </div>
              <button className="ask-expand-result" type="button" onClick={() => setExpandedResult(expanded => !expanded)}>{expandedResult ? '收起明细' : '查看全部 5 个渠道'}<CaretDown className={expandedResult ? 'is-open' : ''} size={13} aria-hidden="true" /></button>
            </section>
          </div>}
        </section>

        <aside className="ask-evidence-panel" aria-label="理解与证据驾驶舱">
          {activeReleaseDrift ? <ReleaseDriftEvidencePanel question={question} drift={activeReleaseDrift} /> : activeClarificationRun ? <EvidencePanel
            question={question}
            run={activeClarificationRun}
            option={selectedClarificationOption}
          /> : activeResultRun?.completion?.result ? <EvidencePanel
            question={question}
            run={activeResultRun}
            result={activeResultRun.completion.result}
            graphDegraded={graphDegraded}
            answer={activeResultRun.completion.answer}
          /> : <>
          <header className="ask-evidence-heading">
            <div><span className={`ask-live-dot ${mode === 'live' ? 'is-pending' : ''}`.trim()} /><span><strong>理解与证据驾驶舱</strong><small>{mode === 'live' ? '仅展示已确认的受控证据' : '答案可追溯、口径可核验'}</small></span></div>
            <span className={`ask-trust-score ${mode === 'live' ? 'is-pending' : ''}`.trim()}>{mode === 'live' ? '—' : mode === 'snapshot-scope-detail' ? '100' : '96'}</span>
          </header>

          {mode === 'live' ? <div className="ask-live-evidence-state" role="status">
            <span><ShieldCheck size={22} weight="duotone" aria-hidden="true" /></span>
            <strong>等待受控证据</strong>
            <p>指标、口径、数据链路与质量信息通过校验后才会显示。</p>
          </div> : <div className="ask-evidence-sections">
            <EvidenceSection id="intent" title="问题理解" icon={<Sparkle size={14} weight="fill" />} open={openEvidence.intent} onToggle={toggleEvidence}>
              <dl className="ask-evidence-grid">
                <div><dt>意图</dt><dd>{mode === 'snapshot-scope-detail' ? '明细取数' : '归因分析'}</dd></div>
                <div><dt>指标</dt><dd>{mode === 'snapshot-scope-detail' ? '订单' : '综合毛利率'}</dd></div>
                <div><dt>范围</dt><dd>{mode === 'snapshot-scope-detail' ? '逐行明细' : '销售渠道'}</dd></div>
                <div><dt>时间</dt><dd>{mode === 'snapshot-scope-detail' ? '本月' : '本月环比'}</dd></div>
              </dl>
              <p className="ask-confidence-note"><CheckCircle size={13} weight="fill" />{mode === 'snapshot-scope-detail' ? '确定性范围分类已验证' : '语义绑定置信度 98%'}</p>
            </EvidenceSection>

            <EvidenceSection id="policy" title="口径与权限" icon={<ShieldCheck size={14} weight="fill" />} open={openEvidence.policy} onToggle={toggleEvidence}>
              <div className="ask-source-card">
                <span className="ask-source-icon blue"><Database size={14} /></span>
                <span><strong>{mode === 'snapshot-scope-detail' ? '可信问数范围门禁' : '综合毛利率'}</strong><small>{mode === 'snapshot-scope-detail' ? 'SCOPE_DETAIL_LIST · 规则固定' : '财务经营口径 · v3.2'}</small></span>
                <span className="ask-source-status">{mode === 'snapshot-scope-detail' ? '已阻断查询' : '已发布'}</span>
              </div>
              <p className="ask-policy-note"><ShieldCheck size={12} weight="fill" />{mode === 'snapshot-scope-detail' ? '领域固定为“企业经营”，拒答不返回任何结果行' : '已按“家电经营分析”领域权限过滤'}</p>
            </EvidenceSection>

            {mode !== 'snapshot-scope-detail' && <EvidenceSection id="lineage" title="数据链路" icon={<LinkSimple size={14} />} open={openEvidence.lineage} onToggle={toggleEvidence}>
              <ol className="ask-lineage-list">
                <li><span>1</span><div><strong>DWS 渠道经营日汇总</strong><small>dws_channel_operation_daily</small></div></li>
                <li><span>2</span><div><strong>DWD 销售订单明细</strong><small>dwd_sales_order_detail</small></div></li>
                <li><span>3</span><div><strong>ERP 销售与返利</strong><small>两张受控源表</small></div></li>
              </ol>
            </EvidenceSection>}

            {mode !== 'snapshot-scope-detail' && <EvidenceSection id="quality" title="质量与新鲜度" icon={<Pulse size={14} weight="bold" />} open={openEvidence.quality} onToggle={toggleEvidence}>
              <div className="ask-quality-score"><strong>98.7</strong><span>质量分<small>通过 12 项规则</small></span></div>
              <dl className="ask-freshness-list"><div><dt>数据截至</dt><dd>08-05 23:00</dd></div><div><dt>最近刷新</dt><dd>38 分钟前</dd></div></dl>
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
    </AppShell>
  )
}
