import {
  ArrowRight,
  CaretDown,
  ChartBarHorizontal,
  ChatCircleDots,
  CheckCircle,
  ClockCounterClockwise,
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

type RunPhase = 'idle' | 'loading' | 'complete'
type Feedback = 'helpful' | 'unhelpful' | 'report' | null
type Session = { id: string; title: string; meta: string; status: 'complete' | 'attention' }
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

const DEFAULT_QUESTION = '哪些渠道导致本月毛利率下降？'

const SESSION_GROUPS: SessionGroup[] = [
  {
    label: '今天',
    items: [
      { id: 'margin', title: '本月毛利率下降原因', meta: '刚刚 · 已完成', status: 'complete' },
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

/** WEB-001 typed mock：在真实工作台壳内验证问数信息结构与关键交互。 */
export function AskDataPage() {
  const [question, setQuestion] = useState(DEFAULT_QUESTION)
  const [phase, setPhase] = useState<RunPhase>('complete')
  const [sessionQuery, setSessionQuery] = useState('')
  const [selectedSession, setSelectedSession] = useState('margin')
  const [expandedResult, setExpandedResult] = useState(false)
  const [feedback, setFeedback] = useState<Feedback>(null)
  const [openEvidence, setOpenEvidence] = useState<Record<string, boolean>>(() => Object.fromEntries(EVIDENCE_SECTIONS.map(id => [id, true])))
  const sessionSearchRef = useRef<HTMLInputElement>(null)
  const runTimerRef = useRef<number | null>(null)

  useEffect(() => () => {
    if (runTimerRef.current !== null) window.clearTimeout(runTimerRef.current)
  }, [])

  const filteredGroups = useMemo(() => {
    const normalized = sessionQuery.trim().toLocaleLowerCase()
    if (!normalized) return SESSION_GROUPS
    return SESSION_GROUPS
      .map(group => ({ ...group, items: group.items.filter(item => item.title.toLocaleLowerCase().includes(normalized)) }))
      .filter(group => group.items.length > 0)
  }, [sessionQuery])

  const submitQuestion = (event: FormEvent) => {
    event.preventDefault()
    if (!question.trim() || phase === 'loading') return
    if (runTimerRef.current !== null) window.clearTimeout(runTimerRef.current)
    setPhase('loading')
    setFeedback(null)
    setExpandedResult(false)
    runTimerRef.current = window.setTimeout(() => {
      setPhase('complete')
      setSelectedSession('margin')
      runTimerRef.current = null
    }, 650)
  }

  const startNewQuestion = () => {
    if (runTimerRef.current !== null) window.clearTimeout(runTimerRef.current)
    runTimerRef.current = null
    setQuestion('')
    setPhase('idle')
    setSelectedSession('')
    setFeedback(null)
    setExpandedResult(false)
  }

  const choosePrompt = (prompt: string) => {
    setQuestion(prompt)
    setPhase('idle')
  }

  const chooseSession = (session: Session) => {
    setSelectedSession(session.id)
    setQuestion(session.id === 'margin' ? DEFAULT_QUESTION : session.title)
    setPhase(session.status === 'attention' ? 'idle' : 'complete')
    setFeedback(null)
  }

  const toggleEvidence = (id: string) => {
    setOpenEvidence(current => ({ ...current, [id]: !current[id] }))
  }

  return (
    <AppShell
      className="ask-data-shell"
      eyebrow="可信问数"
      title="问数工作台"
      titleMeta={<span className="ask-release-badge"><span />企业经营 · Release 2026.08</span>}
      actions={<>
        <button className="quiet-button ask-topbar-button" type="button" onClick={() => sessionSearchRef.current?.focus()}><ClockCounterClockwise size={15} aria-hidden="true" />历史会话</button>
        <button className="primary-button ask-topbar-button" type="button" onClick={startNewQuestion}><Plus size={15} aria-hidden="true" />开始新问题</button>
      </>}
    >
      <div className="ask-workbench">
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
            <header><strong>用业务语言提问</strong><small>智能问数</small></header>
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
                <button type="button" onClick={() => choosePrompt('按渠道查看本月毛利率变化')}>按渠道查看毛利率变化</button>
                <button type="button" onClick={() => choosePrompt('本月毛利率和上月相比如何？')}>本月毛利率同比环比</button>
                <button type="button" onClick={() => choosePrompt('毛利率下降的主要原因是什么？')}>毛利率下降 TOP 原因</button>
              </div>
              <button type="submit" aria-label="发送问题" disabled={!question.trim() || phase === 'loading'}><PaperPlaneRight size={18} weight="fill" /></button>
            </div>
          </form>

          {phase !== 'idle' && <section className="ask-dialogue-context" aria-label="本轮分析进度">
            <div className="ask-user-message"><small>我 · 现在</small><p>{question}</p></div>
            <div className="ask-assistant-message">
              <span><ChatCircleDots size={17} weight="duotone" aria-hidden="true" /></span>
              <div><small>智能助手 · 现在</small><p>{phase === 'loading' ? '已理解你的问题，正在核验毛利率口径与渠道数据。' : '已识别到本月毛利率环比下降，正在展示主要影响渠道与已验证证据。'}</p></div>
            </div>
            <div className="ask-run-status" role="status" aria-live="polite">
              {['理解问题', '确认口径', '验证数据'].map((label, index) => <div className={phase === 'loading' && index === 2 ? '' : 'is-complete'} key={label}>
                {phase === 'loading' && index === 2 ? <span className="ask-status-spinner" /> : <CheckCircle size={15} weight="fill" aria-hidden="true" />}
                <span><strong>{label}</strong><small>{index === 0 ? '识别指标与维度' : index === 1 ? '毛利率、本月 vs 上月' : phase === 'loading' ? '正在完成核验' : '数据校验完成'}</small></span>
              </div>)}
            </div>
          </section>}

          {phase === 'idle' && <section className="ask-empty-state">
            <div><Sparkle size={22} weight="duotone" /></div>
            <h2>准备好分析这个问题</h2>
            <p>发送后将先确认业务口径，再查询已发布的数据资产。</p>
            <button className="primary-button" type="button" onClick={() => document.querySelector<HTMLFormElement>('.ask-question-composer')?.requestSubmit()} disabled={!question.trim()}>开始分析</button>
          </section>}

          {phase === 'loading' && <section className="ask-loading-state" aria-label="正在分析">
            <span className="ask-loader-ring" />
            <div><strong>正在验证数据</strong><p>已匹配“渠道毛利分析”口径，正在检查数据新鲜度。</p></div>
          </section>}

          {phase === 'complete' && <div className="ask-result-stream">
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
          <header className="ask-evidence-heading">
            <div><span className="ask-live-dot" /><span><strong>理解与证据驾驶舱</strong><small>答案可追溯、口径可核验</small></span></div>
            <span className="ask-trust-score">96</span>
          </header>

          <div className="ask-evidence-sections">
            <EvidenceSection id="intent" title="问题理解" icon={<Sparkle size={14} weight="fill" />} open={openEvidence.intent} onToggle={toggleEvidence}>
              <dl className="ask-evidence-grid">
                <div><dt>意图</dt><dd>归因分析</dd></div>
                <div><dt>指标</dt><dd>综合毛利率</dd></div>
                <div><dt>维度</dt><dd>销售渠道</dd></div>
                <div><dt>时间</dt><dd>本月环比</dd></div>
              </dl>
              <p className="ask-confidence-note"><CheckCircle size={13} weight="fill" />语义绑定置信度 98%</p>
            </EvidenceSection>

            <EvidenceSection id="policy" title="口径与权限" icon={<ShieldCheck size={14} weight="fill" />} open={openEvidence.policy} onToggle={toggleEvidence}>
              <div className="ask-source-card">
                <span className="ask-source-icon blue"><Database size={14} /></span>
                <span><strong>综合毛利率</strong><small>财务经营口径 · v3.2</small></span>
                <span className="ask-source-status">已发布</span>
              </div>
              <p className="ask-policy-note"><ShieldCheck size={12} weight="fill" />已按“家电经营分析”领域权限过滤</p>
            </EvidenceSection>

            <EvidenceSection id="lineage" title="数据链路" icon={<LinkSimple size={14} />} open={openEvidence.lineage} onToggle={toggleEvidence}>
              <ol className="ask-lineage-list">
                <li><span>1</span><div><strong>DWS 渠道经营日汇总</strong><small>dws_channel_operation_daily</small></div></li>
                <li><span>2</span><div><strong>DWD 销售订单明细</strong><small>dwd_sales_order_detail</small></div></li>
                <li><span>3</span><div><strong>ERP 销售与返利</strong><small>两张受控源表</small></div></li>
              </ol>
            </EvidenceSection>

            <EvidenceSection id="quality" title="质量与新鲜度" icon={<Pulse size={14} weight="bold" />} open={openEvidence.quality} onToggle={toggleEvidence}>
              <div className="ask-quality-score"><strong>98.7</strong><span>质量分<small>通过 12 项规则</small></span></div>
              <dl className="ask-freshness-list"><div><dt>数据截至</dt><dd>08-05 23:00</dd></div><div><dt>最近刷新</dt><dd>38 分钟前</dd></div></dl>
            </EvidenceSection>
          </div>

          <section className="ask-feedback-card">
            <h2>这个答案有帮助吗？</h2>
            <div>
              <button type="button" aria-label="答案有帮助" aria-pressed={feedback === 'helpful'} onClick={() => setFeedback(feedback === 'helpful' ? null : 'helpful')}><ThumbsUp size={15} /></button>
              <button type="button" aria-label="答案没有帮助" aria-pressed={feedback === 'unhelpful'} onClick={() => setFeedback(feedback === 'unhelpful' ? null : 'unhelpful')}><ThumbsDown size={15} /></button>
              <button type="button" aria-pressed={feedback === 'report'} onClick={() => setFeedback(feedback === 'report' ? null : 'report')}><WarningCircle size={14} />报告问题</button>
            </div>
            {feedback && <p role="status">{feedback === 'helpful' ? '感谢反馈，我们会继续保持。' : feedback === 'unhelpful' ? '已记录，后续会用于改进回答。' : '问题已标记，提交会进入人工复核。'}</p>}
          </section>
        </aside>
      </div>
    </AppShell>
  )
}
