import {
  ArrowRight,
  BookOpenText,
  ChartLineUp,
  ChatCircleDots,
  CheckCircle,
  Clock,
  Database,
  FileText,
  Lifebuoy,
  MagnifyingGlass,
  PaperPlaneRight,
  Path,
  ShieldCheck,
  WarningCircle,
} from '@phosphor-icons/react'
import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { supportAPI, type SupportTicket } from '../lib/support'

const guides = [
  { title: '开始业务分析', description: '从首页进入问数，用自然语言获得可信分析并保留证据。', to: '/ask-data', icon: ChartLineUp, steps: ['提出业务问题', '复核口径与范围', '保存或进入报告'] },
  { title: '接入与治理数据', description: '建立连接、发布数据资产，再完成数据集和语义口径。', to: '/data-sources', icon: Database, steps: ['配置并测试连接', '完善资产元数据', '构建可复用数据集'] },
  { title: '设计并发布报告', description: '使用模板或空白画布，将分析编排为可分享的管理报告。', to: '/reports', icon: FileText, steps: ['选择模板或空白报告', '绑定已治理数据', '评审、发布与订阅'] },
  { title: '推动决策落地', description: '把结论转成审批、决策与行动项，持续跟踪业务结果。', to: '/decisions', icon: Path, steps: ['创建决策记录', '完成必要审批', '跟踪行动与复盘'] },
]

const faqs = [
  ['为什么看不到某个领域或资产？', '平台仅展示当前账号已获授权的领域和资产。先在左侧“领域访问”确认成员资格；资产仍不可见时，请联系领域管理员检查共享范围。'],
  ['数据源测试通过后为什么还不能使用？', '测试只验证连接可达。数据源还需要完成发布审批、元数据发现和业务信息完善，之后才能进入数据集建模。'],
  ['问数结果为什么要求澄清？', '当指标、时间或维度存在多种有效解释时，系统会先让你确认口径，避免用错误假设生成答案。'],
  ['报告发布后还能修改吗？', '已发布版本保持不变。继续编辑会形成新草稿，完成评审后发布新版本，历史版本仍可查看和回滚。'],
  ['如何反馈接口错误或页面异常？', '在本页提交支持工单，系统会自动记录当前页面。若页面显示稳定错误码，也可以一并填写，便于快速定位。'],
] as const

const categoryLabel: Record<SupportTicket['category'], string> = {
  QUESTION: '问数分析', DATA: '数据资产', REPORT: '报告中心', ACCESS: '账号权限', SYSTEM: '平台运行', OTHER: '其他问题',
}
const statusLabel: Record<SupportTicket['status'], string> = {
  OPEN: '待受理', IN_PROGRESS: '处理中', RESOLVED: '已解决', CLOSED: '已关闭',
}

export function HelpPage() {
  const navigate = useNavigate()
  const supportRef = useRef<HTMLElement>(null)
  const [query, setQuery] = useState('')
  const [tickets, setTickets] = useState<SupportTicket[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    let active = true
    void supportAPI.list().then(items => active && setTickets(items)).catch(cause => active && setError(cause instanceof Error ? cause.message : '支持工单加载失败')).finally(() => active && setLoading(false))
    if (new URLSearchParams(window.location.search).get('support') === '1') {
      window.setTimeout(() => supportRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 160)
    }
    return () => { active = false }
  }, [])

  const filteredFAQs = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase('zh-CN')
    return needle ? faqs.filter(item => item.join(' ').toLocaleLowerCase('zh-CN').includes(needle)) : faqs
  }, [query])

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy) return
    const formElement = event.currentTarget
    const form = new FormData(formElement)
    setBusy(true); setError(''); setNotice('')
    try {
      const created = await supportAPI.create({
        clientRequestId: crypto.randomUUID(),
        category: String(form.get('category')) as SupportTicket['category'],
        priority: String(form.get('priority')) as SupportTicket['priority'],
        subject: String(form.get('subject') ?? '').trim(),
        description: String(form.get('description') ?? '').trim(),
        pageUrl: String(form.get('pageUrl') ?? '').trim(),
        errorCode: String(form.get('errorCode') ?? '').trim(),
      })
      setTickets(current => [created, ...current])
      formElement.reset()
      setNotice(`支持工单 ${created.id.slice(0, 8).toUpperCase()} 已提交，可在下方跟踪处理状态`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '支持工单提交失败')
    } finally { setBusy(false) }
  }

  return <AppShell className="help-shell" title="帮助与支持" eyebrow="使用指南" titleMeta={<span>快速完成核心任务，遇到问题可直接提交支持工单</span>}>
    <main className="help-page">
      {(error || notice) && <div className={`help-feedback ${error ? 'is-error' : 'is-success'}`} role={error ? 'alert' : 'status'}>{error ? <WarningCircle size={18} weight="fill" /> : <CheckCircle size={18} weight="fill" />}{error || notice}<button type="button" onClick={() => { setError(''); setNotice('') }}>关闭</button></div>}

      <section className="help-hero">
        <div><span className="help-kicker"><BookOpenText size={16} />产品知识库</span><h2>今天想完成什么？</h2><p>按业务链路找到入口，或搜索常见问题。所有操作都会遵循当前领域权限。</p></div>
        <label className="help-search"><MagnifyingGlass size={20} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索权限、数据源、问数或报告问题" /></label>
      </section>

      <section className="help-guide-grid" aria-label="核心任务指南">
        {guides.map(guide => <article key={guide.title}>
          <span className="help-guide-icon"><guide.icon size={25} weight="duotone" /></span>
          <h3>{guide.title}</h3><p>{guide.description}</p>
          <ol>{guide.steps.map(step => <li key={step}>{step}</li>)}</ol>
          <AppButton text onClick={() => navigate(guide.to)}>进入工作台<ArrowRight size={15} /></AppButton>
        </article>)}
      </section>

      <div className="help-content-grid">
        <section className="help-faq-panel">
          <header><div><h2>常见问题</h2><p>{query ? `找到 ${filteredFAQs.length} 个相关答案` : '核心流程的高频问题与处理建议'}</p></div><ShieldCheck size={25} weight="duotone" /></header>
          <div className="help-faq-list">
            {filteredFAQs.map(([question, answer]) => <details key={question}><summary>{question}<span>+</span></summary><p>{answer}</p></details>)}
            {filteredFAQs.length === 0 && <div className="help-empty"><MagnifyingGlass size={30} /><strong>没有匹配的问题</strong><p>可以调整关键词，或直接提交支持工单。</p></div>}
          </div>
        </section>

        <section className="help-support-panel" ref={supportRef}>
          <header><span><Lifebuoy size={24} weight="duotone" /></span><div><h2>联系平台支持</h2><p>问题会进入当前领域的受理队列</p></div></header>
          <form onSubmit={submit}>
            <div className="help-form-row"><label>问题类型<select name="category" defaultValue="SYSTEM"><option value="SYSTEM">平台运行</option><option value="DATA">数据资产</option><option value="QUESTION">问数分析</option><option value="REPORT">报告中心</option><option value="ACCESS">账号权限</option><option value="OTHER">其他问题</option></select></label><label>优先级<select name="priority" defaultValue="NORMAL"><option value="NORMAL">普通</option><option value="HIGH">较高</option><option value="URGENT">紧急阻断</option></select></label></div>
            <label>问题标题<input name="subject" minLength={4} maxLength={120} required placeholder="简要描述遇到的问题" /></label>
            <label>问题详情<textarea name="description" minLength={10} maxLength={4000} required rows={5} placeholder="请说明操作步骤、预期结果与实际表现；请勿填写密码或连接凭据。" /></label>
            <div className="help-form-row"><label>发生页面<input name="pageUrl" defaultValue={`${window.location.pathname}${window.location.search}`} maxLength={1000} /></label><label>错误码（可选）<input name="errorCode" maxLength={127} placeholder="例如 REQUEST_FAILED" /></label></div>
            <footer><span><ShieldCheck size={15} />工单仅对本人及领域/平台管理员可见</span><AppButton variant="primary" type="submit" disabled={busy}><PaperPlaneRight size={17} />{busy ? '提交中…' : '提交工单'}</AppButton></footer>
          </form>
        </section>
      </div>

      <section className="help-ticket-panel">
        <header><div><h2>我的支持工单</h2><p>跟踪最近提交的问题与处理结果</p></div><span>{tickets.length} 条</span></header>
        {loading ? <div className="help-empty" role="status"><Clock size={30} /><strong>正在同步工单…</strong></div> : tickets.length === 0 ? <div className="help-empty"><ChatCircleDots size={30} /><strong>还没有支持工单</strong><p>遇到操作阻断时，可在上方提交并持续跟踪。</p></div> : <div className="help-ticket-list">{tickets.map(ticket => <article key={ticket.id}>
          <span className={`help-status is-${ticket.status.toLowerCase()}`}>{statusLabel[ticket.status]}</span>
          <div><strong>{ticket.subject}</strong><p>{categoryLabel[ticket.category]} · {new Date(ticket.createdAt).toLocaleString('zh-CN', { hour12: false })} · {ticket.id.slice(0, 8).toUpperCase()}</p>{ticket.resolutionNote && <small><CheckCircle size={14} />{ticket.resolutionNote}</small>}</div>
          <em>{ticket.priority === 'URGENT' ? '紧急' : ticket.priority === 'HIGH' ? '较高' : '普通'}</em>
        </article>)}</div>}
      </section>
    </main>
  </AppShell>
}
