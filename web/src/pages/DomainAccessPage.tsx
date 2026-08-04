import { CheckCircle, Clock, GlobeHemisphereWest, PaperPlaneTilt, SpinnerGap } from '@phosphor-icons/react'
import { FormEvent, useCallback, useEffect, useState } from 'react'
import { AppShell } from '../components/AppShell'
import {
  administrationAPI,
  type DomainApplication,
  type DomainCatalogItem,
} from '../lib/administration'
import { bindBusinessDomain } from '../lib/auth'
import { notifyDomainCatalogChanged, selectDomain } from '../lib/domain-context'

const accessLabels: Record<DomainCatalogItem['accessStatus'], string> = {
  AVAILABLE: '可申请',
  PENDING: '审批中',
  APPROVED: '已通过',
  REJECTED: '已驳回，可重新申请',
  CANCELLED: '可申请',
  MEMBER: '已加入',
  DOMAIN_ADMIN: '领域管理员',
}

/** 为无领域用户提供可登录、可申请、可在审批后进入领域的控制面。 */
export function DomainAccessPage() {
  const [items, setItems] = useState<DomainCatalogItem[]>([])
  const [selected, setSelected] = useState<DomainCatalogItem | null>(null)
  const [applications, setApplications] = useState<DomainApplication[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const fetchCatalog = useCallback(async () => {
    try {
      const catalog = await administrationAPI.listDomainCatalog()
      setItems(catalog)
      const queues = await Promise.all(catalog
        .filter(item => item.accessStatus === 'DOMAIN_ADMIN')
        .map(item => administrationAPI.listPendingDomainApplications(item.id)))
      setApplications(queues.flat())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域目录加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    await fetchCatalog()
  }, [fetchCatalog])

  useEffect(() => {
    let cancelled = false
    queueMicrotask(() => {
      if (!cancelled) void fetchCatalog()
    })
    return () => { cancelled = true }
  }, [fetchCatalog])

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!selected) return
    const reason = String(new FormData(event.currentTarget).get('reason') ?? '').trim()
    setBusy(true)
    setError('')
    try {
      await administrationAPI.applyDomain(selected.id, reason)
      setSelected(null)
      await load()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域申请提交失败')
    } finally {
      setBusy(false)
    }
  }

  const enter = async (domain: DomainCatalogItem) => {
    setBusy(true)
    setError('')
    try {
      await bindBusinessDomain(domain.id)
      const memberships = await administrationAPI.listDomains()
      selectDomain(memberships.find(item => item.id === domain.id) ?? domain)
      notifyDomainCatalogChanged()
      window.location.assign('/data-sources')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '进入领域失败')
      await load()
    } finally {
      setBusy(false)
    }
  }

  const review = async (application: DomainApplication, decision: 'APPROVED' | 'REJECTED') => {
    setBusy(true)
    setError('')
    try {
      await administrationAPI.reviewDomainApplication(application.id, decision)
      await load()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '审批失败')
    } finally {
      setBusy(false)
    }
  }

  return <AppShell title="领域权限" eyebrow="访问申请" className="domain-access-shell">
    <section className="domain-access-page">
      <header>
        <GlobeHemisphereWest size={44} weight="duotone" />
        <div><h2>选择需要加入的业务领域</h2><p>领域之间的权限、数据源和数据集相互隔离。申请由目标领域管理员审批。</p></div>
      </header>
      {error && <p className="form-error" role="alert">{error}</p>}
      {loading
        ? <div className="administration-empty"><SpinnerGap className="spin" size={30} /><strong>正在加载领域目录…</strong></div>
        : <div className="domain-access-grid">
          {items.map(domain => {
            const joined = domain.accessStatus === 'MEMBER' || domain.accessStatus === 'DOMAIN_ADMIN'
            const pending = domain.accessStatus === 'PENDING'
            return <article key={domain.id}>
              <div className="domain-management-avatar">{domain.name.slice(0, 1)}</div>
              <div><h3>{domain.name}</h3><code>{domain.code}</code></div>
              <p>{domain.description || '暂无领域说明'}</p>
              <span className={`domain-access-status ${domain.accessStatus.toLowerCase()}`}>
                {pending ? <Clock size={15} /> : joined ? <CheckCircle size={15} /> : null}
                {accessLabels[domain.accessStatus]}
              </span>
              {joined
                ? <button className="primary-button" type="button" disabled={busy} onClick={() => void enter(domain)}>进入领域</button>
                : <button className="quiet-button" type="button" disabled={busy || pending} onClick={() => setSelected(domain)}>{pending ? '等待审批' : '申请加入'}</button>}
            </article>
          })}
        </div>}
      {applications.length > 0 && <section className="domain-review-queue">
        <header><h2>待审批申请</h2><p>仅展示你担任领域管理员的领域。</p></header>
        {applications.map(application => <article key={application.id}>
          <div><strong>{application.applicantDisplayName}</strong><small>{application.applicantEmail}</small></div>
          <div><strong>{application.domainName}</strong><small>{application.reason || '未填写申请说明'}</small></div>
          <div className="domain-review-actions">
            <button className="quiet-button" type="button" disabled={busy} onClick={() => void review(application, 'REJECTED')}>驳回</button>
            <button className="primary-button" type="button" disabled={busy} onClick={() => void review(application, 'APPROVED')}>通过</button>
          </div>
        </article>)}
      </section>}
    </section>
    {selected && <div className="administration-dialog-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget && !busy) setSelected(null)
    }}>
      <section className="administration-dialog" role="dialog" aria-modal="true" aria-labelledby="domain-application-title">
        <header><div><span className="eyebrow">DOMAIN ACCESS</span><h2 id="domain-application-title">申请加入“{selected.name}”</h2><p>提交后由该领域管理员审批。</p></div></header>
        <form onSubmit={submit}>
          <label>申请说明<textarea name="reason" autoFocus maxLength={1000} placeholder="请简要说明需要访问该领域的工作原因" /></label>
          <footer><button className="quiet-button" type="button" disabled={busy} onClick={() => setSelected(null)}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <PaperPlaneTilt size={16} />}{busy ? '提交中…' : '提交申请'}</button></footer>
        </form>
      </section>
    </div>}
  </AppShell>
}
