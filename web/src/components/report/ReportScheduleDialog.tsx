import { ArrowClockwise, CalendarDots, CheckCircle, Clock, EnvelopeOpen, Pause, Play, Plus, SpinnerGap, WarningCircle, X } from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { currentSubject } from '../../lib/auth'
import { reportRuntimeAPI, type ReportDelivery, type ReportSchedule, type ReportSubscription } from '../../report/api/runtime'

type Props = {
  open: boolean
  reportId: string
  reportVersionId: string
  reportName: string
  timezone: string
  canEdit: boolean
  onClose: () => void
  onOpenReport: (href: string) => void
}

type ScheduleDetail = { schedule: ReportSchedule; subscriptions: ReportSubscription[] }

const kindLabel = { DAILY: '每天', WEEKLY: '每周', MONTHLY: '每月' } satisfies Record<ReportSchedule['scheduleKind'], string>
const weekdayLabel = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

function formatDateTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
}

function scheduleRule(item: ReportSchedule) {
  if (item.scheduleKind === 'WEEKLY') return `${item.weekdays.map(day => weekdayLabel[day]).join('、')} ${item.localTime.slice(0, 5)}`
  if (item.scheduleKind === 'MONTHLY') return `每月 ${item.dayOfMonth ?? 1} 日 ${item.localTime.slice(0, 5)}`
  return `${item.businessCalendar === 'WEEKDAYS' ? '每个工作日' : '每天'} ${item.localTime.slice(0, 5)}`
}

export function ReportScheduleDialog({ open, reportId, reportVersionId, reportName, timezone, canEdit, onClose, onOpenReport }: Props) {
  const [tab, setTab] = useState<'schedules' | 'deliveries'>('schedules')
  const [schedules, setSchedules] = useState<ReportSchedule[]>([])
  const [details, setDetails] = useState<Record<string, ScheduleDetail>>({})
  const [deliveries, setDeliveries] = useState<ReportDelivery[]>([])
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState(() => `${reportName} 定时分发`)
  const [kind, setKind] = useState<ReportSchedule['scheduleKind']>('MONTHLY')
  const [localTime, setLocalTime] = useState('09:00')
  const [weekday, setWeekday] = useState(1)
  const [dayOfMonth, setDayOfMonth] = useState(1)
  const [businessCalendar, setBusinessCalendar] = useState<ReportSchedule['businessCalendar']>('WEEKDAYS')
  const actorId = currentSubject()

  const load = useCallback(async () => {
    if (!open) return
    setLoading(true); setError('')
    try {
      const [schedulePage, deliveryPage] = await Promise.all([
        reportRuntimeAPI.listSchedules(reportId),
        reportRuntimeAPI.listDeliveries(),
      ])
      setSchedules(schedulePage.items)
      setDeliveries(deliveryPage.items.filter(item => item.reportId === reportId))
      if (deliveryPage.items.some(item => item.reportId === reportId && item.state === 'READY')) {
        setNotice(current => current.includes('后台正在安全校验') ? '报告已送达，可在“我的收件箱”中打开。' : current)
      }
      const loadedDetails = await Promise.all(schedulePage.items.map(item => reportRuntimeAPI.getSchedule(item.id)))
      setDetails(Object.fromEntries(loadedDetails.map(item => [item.schedule.id, item])))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '定时分发暂时无法加载')
    } finally { setLoading(false) }
  }, [open, reportId])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const unread = useMemo(() => deliveries.filter(item => item.state === 'READY' && !item.readAt).length, [deliveries])

  const create = async () => {
    setBusy('create'); setError(''); setNotice('')
    try {
      const created = await reportRuntimeAPI.createSchedule(reportId, {
        reportVersionId, name: name.trim(), scheduleKind: kind, localTime,
        weekdays: kind === 'WEEKLY' ? [weekday] : [],
        ...(kind === 'MONTHLY' ? { dayOfMonth } : {}),
        timezone, businessCalendar, maxConsecutiveFailures: 3, missAfterSeconds: 86400,
      })
      if (actorId) await reportRuntimeAPI.subscribeSchedule(created.id, actorId)
      setCreating(false); setNotice('分发计划已创建，并已订阅给当前账号。')
      await load()
    } catch (cause) { setError(cause instanceof Error ? cause.message : '分发计划创建失败') }
    finally { setBusy('') }
  }

  const toggle = async (item: ReportSchedule) => {
    setBusy(`state:${item.id}`); setError(''); setNotice('')
    try {
      await reportRuntimeAPI.setScheduleState(item.id, item.state === 'ACTIVE' ? 'pause' : 'resume', item.recordVersion)
      setNotice(item.state === 'ACTIVE' ? '分发计划已暂停。' : '分发计划已恢复。')
      await load()
    } catch (cause) { setError(cause instanceof Error ? cause.message : '计划状态更新失败') }
    finally { setBusy('') }
  }

  const toggleSelfSubscription = async (item: ReportSchedule) => {
    const mine = details[item.id]?.subscriptions.find(value => value.recipientUserId === actorId && value.state === 'ACTIVE')
    if (!actorId) return
    setBusy(`subscribe:${item.id}`); setError(''); setNotice('')
    try {
      if (mine) await reportRuntimeAPI.unsubscribeSchedule(item.id, mine.id)
      else await reportRuntimeAPI.subscribeSchedule(item.id, actorId)
      setNotice(mine ? '已取消当前账号订阅。' : '当前账号已订阅该计划。')
      await load()
    } catch (cause) { setError(cause instanceof Error ? cause.message : '订阅状态更新失败') }
    finally { setBusy('') }
  }

  const backfill = async (item: ReportSchedule) => {
    setBusy(`backfill:${item.id}`); setError(''); setNotice('')
    try {
      const scheduledFor = new Date(Date.now() - 60_000).toISOString()
      const result = await reportRuntimeAPI.backfillSchedule(item.id, scheduledFor)
      setNotice(result.deliveryCount > 0 ? `已生成 ${result.deliveryCount} 条补发任务，后台正在安全校验。` : '当前时间点已有分发记录，无需重复补发。')
      window.setTimeout(() => { void load() }, 1200)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '补发任务创建失败') }
    finally { setBusy('') }
  }

  const openDelivery = async (item: ReportDelivery) => {
    if (item.state !== 'READY') return
    try {
      if (!item.readAt) await reportRuntimeAPI.markDeliveryRead(item.id)
    } finally { onOpenReport(item.reportLink || `/reports/${item.reportId}`) }
  }

  if (!open) return null
  return <div className="report-schedule-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target && !busy) onClose() }}>
    <section className="report-schedule-dialog" role="dialog" aria-modal="true" aria-labelledby="report-schedule-title">
      <header><div><span><CalendarDots size={19} /></span><div><h2 id="report-schedule-title">订阅与定时分发</h2><p>固定当前发布版本，按计划在站内安全分发。</p></div></div><button type="button" aria-label="关闭定时分发" onClick={onClose}><X size={18} /></button></header>
      <nav aria-label="分发管理视图"><button className={tab === 'schedules' ? 'is-active' : ''} type="button" onClick={() => setTab('schedules')}>分发计划 <span>{schedules.length}</span></button><button className={tab === 'deliveries' ? 'is-active' : ''} type="button" onClick={() => setTab('deliveries')}>我的收件箱 {unread > 0 && <span>{unread}</span>}</button><button type="button" aria-label="刷新定时分发" onClick={() => void load()}><ArrowClockwise size={16} /></button></nav>
      {loading && <div className="report-schedule-state"><SpinnerGap className="spin" size={22} />正在读取分发状态…</div>}
      {!loading && tab === 'schedules' && <div className="report-schedule-content">
        {canEdit && !creating && <button className="report-schedule-create-trigger" type="button" onClick={() => setCreating(true)}><Plus size={17} /><span><strong>新建分发计划</strong><small>选择周期、时间并自动订阅当前账号</small></span></button>}
        {creating && <section className="report-schedule-form"><div><strong>新建分发计划</strong><button type="button" aria-label="取消新建" onClick={() => setCreating(false)}><X size={15} /></button></div><label><span>计划名称</span><input value={name} maxLength={256} onChange={event => setName(event.target.value)} /></label><div className="report-schedule-form-grid"><label><span>周期</span><select value={kind} onChange={event => setKind(event.target.value as ReportSchedule['scheduleKind'])}><option value="DAILY">每天</option><option value="WEEKLY">每周</option><option value="MONTHLY">每月</option></select></label>{kind === 'WEEKLY' && <label><span>星期</span><select value={weekday} onChange={event => setWeekday(Number(event.target.value))}>{weekdayLabel.map((label, index) => <option value={index} key={label}>{label}</option>)}</select></label>}{kind === 'MONTHLY' && <label><span>日期</span><input type="number" min={1} max={31} value={dayOfMonth} onChange={event => setDayOfMonth(Number(event.target.value))} /></label>}<label><span>执行时间</span><input type="time" value={localTime} onChange={event => setLocalTime(event.target.value)} /></label><label><span>日历策略</span><select value={businessCalendar} onChange={event => setBusinessCalendar(event.target.value as ReportSchedule['businessCalendar'])}><option value="WEEKDAYS">工作日</option><option value="CALENDAR_DAYS">自然日</option></select></label></div><p><Clock size={15} />时区 {timezone}；月末不存在的日期自动落在当月最后一个有效日。</p><button className="primary-button" type="button" disabled={busy === 'create' || !name.trim() || !localTime} onClick={() => void create()}>{busy === 'create' ? '正在创建…' : '创建并订阅'}</button></section>}
        {schedules.map(item => { const mine = details[item.id]?.subscriptions.some(value => value.recipientUserId === actorId && value.state === 'ACTIVE'); return <article className="report-schedule-card" key={item.id}><div className={`report-schedule-icon is-${item.state.toLowerCase()}`}><CalendarDots size={21} /></div><div className="report-schedule-copy"><div><strong>{item.name}</strong><span className={`is-${item.state.toLowerCase()}`}>{item.state === 'ACTIVE' ? '运行中' : item.state === 'PAUSED' ? '已暂停' : '已停用'}</span></div><p>{kindLabel[item.scheduleKind]} · {scheduleRule(item)} · {item.timezone}</p><small>下次执行 {formatDateTime(item.nextRunAt)} · 当前订阅 {details[item.id]?.subscriptions.filter(value => value.state === 'ACTIVE').length ?? 0} 人{item.lastFailureCode ? ` · 最近失败 ${item.lastFailureCode}` : ''}</small></div><div className="report-schedule-actions"><button type="button" disabled={Boolean(busy) || item.state === 'DISABLED'} onClick={() => void toggleSelfSubscription(item)}>{mine ? '取消订阅' : '订阅自己'}</button>{canEdit && <button type="button" disabled={Boolean(busy) || item.state === 'DISABLED'} onClick={() => void backfill(item)}>{busy === `backfill:${item.id}` ? <SpinnerGap className="spin" size={14} /> : <ArrowClockwise size={14} />}补发一次</button>}{canEdit && <button type="button" disabled={Boolean(busy) || item.state === 'DISABLED'} onClick={() => void toggle(item)}>{item.state === 'ACTIVE' ? <Pause size={14} /> : <Play size={14} />}{item.state === 'ACTIVE' ? '暂停' : '恢复'}</button>}</div></article> })}
        {schedules.length === 0 && !creating && <div className="report-schedule-empty"><CalendarDots size={27} /><strong>还没有分发计划</strong><p>{canEdit ? '为当前发布版本创建第一个计划。' : '报告编辑者创建计划后可在这里订阅。'}</p></div>}
      </div>}
      {!loading && tab === 'deliveries' && <div className="report-delivery-list">{deliveries.map(item => <button type="button" disabled={item.state !== 'READY'} onClick={() => void openDelivery(item)} key={item.id}><span className={`is-${item.state.toLowerCase()}`}>{item.state === 'READY' ? item.readAt ? <CheckCircle size={19} /> : <EnvelopeOpen size={19} /> : item.state === 'FAILED' || item.state === 'SKIPPED' || item.state === 'MISSED' ? <WarningCircle size={19} /> : <Clock size={19} />}</span><span><strong>{item.state === 'READY' ? '报告已送达' : item.state === 'FAILED' ? '分发失败' : item.state === 'SKIPPED' ? '无权分发' : item.state === 'MISSED' ? '错过执行窗口' : '正在生成分发'}</strong><small>{formatDateTime(item.scheduledFor)} · {item.failureCode || '站内安全分发'}</small></span>{item.state === 'READY' && <em>{item.readAt ? '再次打开' : '打开报告'}</em>}</button>)}{deliveries.length === 0 && <div className="report-schedule-empty"><EnvelopeOpen size={27} /><strong>还没有分发记录</strong><p>订阅计划执行后，报告会安全送达到这里。</p></div>}</div>}
      {(error || notice) && <p className={`report-schedule-message ${error ? 'is-error' : 'is-success'}`} role={error ? 'alert' : 'status'}>{error ? <WarningCircle size={16} /> : <CheckCircle size={16} />}{error || notice}</p>}
      <footer><span>所有分发均在打开时重新校验报告权限，不附带离线数据副本。</span><button type="button" onClick={onClose}>完成</button></footer>
    </section>
  </div>
}
