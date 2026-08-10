import {
  ArrowClockwise,
  ArrowSquareOut,
  CalendarBlank,
  Check,
  CheckCircle,
  ClipboardText,
  Database,
  FileArrowDown,
  FileText,
  Info,
  MagnifyingGlass,
  Package,
  Plus,
  ShieldCheck,
  UserCircle,
  WarningCircle,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'

import { currentSubject } from '../../lib/auth.ts'
import { currentDomain } from '../../lib/domain-context.ts'
import { dataRequestAPI, loadDataRequestFieldOptions, type DataRequestTransitionInput } from '../api/dataRequest.ts'
import { DataRequestDialog } from './DataRequestDialog.tsx'
import type { DataRequestPrefill } from './DataRequestDialog.tsx'
import type {
  CreateDataRequestInput,
  DataRequest,
  DataRequestDeliveryType,
  DataRequestEvent,
  DataRequestFieldOption,
  DataRequestState,
} from './model.ts'
import {
  dataRequestDeliveryLabels,
  dataRequestSensitivityLabels,
  dataRequestStateLabels,
  dataRequestStepStatus,
  dataRequestTimeline,
  deriveDataRequestSensitivity,
} from './model.ts'

type MyDataRequestsProps = {
  snapshot?: boolean
  dialogOpen: boolean
  dialogPrefill?: DataRequestPrefill
  onDialogOpenChange: (open: boolean) => void
}

const SNAPSHOT_FIELDS: DataRequestFieldOption[] = [
  { datasetId: 'orders', datasetName: '订单经营明细', datasetVersionId: '00000000-0000-4000-8000-000000000201', fieldId: 'order_no', fieldCode: 'order_no', fieldName: '订单编号', sensitivityLevel: 'INTERNAL' },
  { datasetId: 'orders', datasetName: '订单经营明细', datasetVersionId: '00000000-0000-4000-8000-000000000201', fieldId: 'paid_at', fieldCode: 'paid_at', fieldName: '支付时间', sensitivityLevel: 'INTERNAL' },
  { datasetId: 'orders', datasetName: '订单经营明细', datasetVersionId: '00000000-0000-4000-8000-000000000201', fieldId: 'sales_channel', fieldCode: 'sales_channel', fieldName: '销售渠道', sensitivityLevel: 'INTERNAL' },
  { datasetId: 'orders', datasetName: '订单经营明细', datasetVersionId: '00000000-0000-4000-8000-000000000201', fieldId: 'paid_amount', fieldCode: 'paid_amount', fieldName: '实付金额', sensitivityLevel: 'INTERNAL' },
  { datasetId: 'customers', datasetName: '客户主数据', datasetVersionId: '00000000-0000-4000-8000-000000000202', fieldId: 'customer_name', fieldCode: 'customer_name', fieldName: '客户名称', sensitivityLevel: 'CONFIDENTIAL' },
  { datasetId: 'customers', datasetName: '客户主数据', datasetVersionId: '00000000-0000-4000-8000-000000000202', fieldId: 'customer_region', fieldCode: 'customer_region', fieldName: '所属区域', sensitivityLevel: 'INTERNAL' },
]

function snapshotEvents(requestId: string, states: DataRequestState[], start: string, note = ''): DataRequestEvent[] {
  return states.map((state, index) => ({
    id: `${requestId}-event-${index + 1}`,
    requestId,
    sequenceNo: index + 1,
    ...(index > 0 ? { fromState: states[index - 1] } : {}),
    toState: state,
    actorUserId: index < 2 ? '申请人' : '领域管理员',
    ...(index === states.length - 1 && note ? { note } : {}),
    createdAt: new Date(new Date(start).getTime() + index * 55 * 60 * 1000).toISOString(),
  }))
}

const SNAPSHOT_REQUESTS: DataRequest[] = [
  {
    id: 'DR-20260808-001', domainId: 'snapshot-domain', requesterUserId: 'snapshot-user', requestText: '导出本月订单明细',
    parsedContext: {}, businessPurpose: '月度经营复盘',
    requiredFields: SNAPSHOT_FIELDS.slice(0, 4).map(({ datasetVersionId, fieldId }) => ({ datasetVersionId, fieldId })),
    sensitivityLevel: 'INTERNAL', state: 'SUBMITTED', approverUserIds: ['snapshot-approver'],
    slaDueAt: '2026-08-12T10:00:00+08:00', recordVersion: 2,
    createdAt: '2026-08-08T10:38:00+08:00', updatedAt: '2026-08-08T10:42:00+08:00', submittedAt: '2026-08-08T10:42:00+08:00',
    events: snapshotEvents('DR-20260808-001', ['DRAFT', 'SUBMITTED'], '2026-08-08T02:38:00.000Z'),
  },
  {
    id: 'DR-20260807-002', domainId: 'snapshot-domain', requesterUserId: 'snapshot-user', requestText: '渠道退款明细',
    parsedContext: {}, businessPurpose: '渠道退款异常复核',
    requiredFields: SNAPSHOT_FIELDS.slice(0, 3).map(({ datasetVersionId, fieldId }) => ({ datasetVersionId, fieldId })),
    sensitivityLevel: 'INTERNAL', state: 'IN_PROGRESS', approverUserIds: ['snapshot-approver'], assigneeUserId: 'snapshot-assignee',
    slaDueAt: '2026-08-10T18:00:00+08:00', recordVersion: 4,
    createdAt: '2026-08-07T15:22:00+08:00', updatedAt: '2026-08-08T09:16:00+08:00',
    submittedAt: '2026-08-07T15:28:00+08:00', approvedAt: '2026-08-07T17:02:00+08:00', startedAt: '2026-08-08T09:16:00+08:00',
    events: snapshotEvents('DR-20260807-002', ['DRAFT', 'SUBMITTED', 'APPROVED', 'IN_PROGRESS'], '2026-08-07T07:22:00.000Z'),
  },
  {
    id: 'DR-20260805-003', domainId: 'snapshot-domain', requesterUserId: 'snapshot-user', requestText: '华东客户名单',
    parsedContext: {}, businessPurpose: '重点客户经营回访',
    requiredFields: SNAPSHOT_FIELDS.slice(4).map(({ datasetVersionId, fieldId }) => ({ datasetVersionId, fieldId })),
    sensitivityLevel: 'CONFIDENTIAL', state: 'DELIVERED', approverUserIds: ['snapshot-approver'], assigneeUserId: 'snapshot-assignee', securityCosignUserId: 'snapshot-security',
    slaDueAt: '2026-08-08T18:00:00+08:00', deliveryType: 'EXISTING_REPORT', deliveryRef: '/reports/customer-east', recordVersion: 5,
    createdAt: '2026-08-05T09:41:00+08:00', updatedAt: '2026-08-08T08:30:00+08:00',
    submittedAt: '2026-08-05T09:48:00+08:00', approvedAt: '2026-08-05T16:20:00+08:00', startedAt: '2026-08-06T09:10:00+08:00', deliveredAt: '2026-08-08T08:30:00+08:00',
    events: snapshotEvents('DR-20260805-003', ['DRAFT', 'SUBMITTED', 'APPROVED', 'IN_PROGRESS', 'DELIVERED'], '2026-08-05T01:41:00.000Z'),
  },
  {
    id: 'DR-20260801-004', domainId: 'snapshot-domain', requesterUserId: 'snapshot-user', requestText: '库存异常订单',
    parsedContext: {}, businessPurpose: '库存异常专项核查',
    requiredFields: SNAPSHOT_FIELDS.slice(0, 2).map(({ datasetVersionId, fieldId }) => ({ datasetVersionId, fieldId })),
    sensitivityLevel: 'INTERNAL', state: 'CLOSED', approverUserIds: ['snapshot-approver'], assigneeUserId: 'snapshot-assignee',
    slaDueAt: '2026-08-04T18:00:00+08:00', deliveryType: 'ONE_TIME_EXPORT', deliveryRef: 'EXPORT-20260804-018', recordVersion: 6,
    createdAt: '2026-08-01T18:05:00+08:00', updatedAt: '2026-08-05T11:40:00+08:00',
    submittedAt: '2026-08-01T18:12:00+08:00', approvedAt: '2026-08-02T10:14:00+08:00', startedAt: '2026-08-02T10:20:00+08:00', deliveredAt: '2026-08-04T16:30:00+08:00', closedAt: '2026-08-05T11:40:00+08:00',
    events: snapshotEvents('DR-20260801-004', ['DRAFT', 'SUBMITTED', 'APPROVED', 'IN_PROGRESS', 'DELIVERED', 'CLOSED'], '2026-08-01T10:05:00.000Z'),
  },
]

const formatter = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
const dateFormatter = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })

function formatTimestamp(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : formatter.format(date).replaceAll('/', '-')
}

function formatDate(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : dateFormatter.format(date).replaceAll('/', '-')
}

function shortRequestNumber(request: DataRequest) {
  if (request.id.startsWith('DR-')) return request.id
  const date = request.createdAt.slice(0, 10).replaceAll('-', '')
  return `DR-${date}-${request.id.slice(-6).toLocaleUpperCase()}`
}

function eventTitle(event: DataRequestEvent) {
  if (event.toState === 'DRAFT') return '已创建申请草稿'
  if (event.toState === 'SUBMITTED') return '申请已提交，等待领域审批'
  if (event.toState === 'APPROVED') return '领域审批已通过'
  if (event.toState === 'REJECTED') return '申请已驳回'
  if (event.toState === 'IN_PROGRESS') return '申请已进入处理'
  if (event.toState === 'DELIVERED') return '交付物已就绪'
  return '申请已确认关闭'
}

export function MyDataRequests({ snapshot = false, dialogOpen, dialogPrefill, onDialogOpenChange }: MyDataRequestsProps) {
  const [items, setItems] = useState<DataRequest[]>(snapshot ? SNAPSHOT_REQUESTS : [])
  const [selectedId, setSelectedId] = useState(snapshot ? SNAPSHOT_REQUESTS[0].id : '')
  const [detail, setDetail] = useState<DataRequest | null>(snapshot ? SNAPSHOT_REQUESTS[0] : null)
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(!snapshot)
  const [fieldsLoading, setFieldsLoading] = useState(!snapshot)
  const [fieldOptions, setFieldOptions] = useState<DataRequestFieldOption[]>(snapshot ? SNAPSHOT_FIELDS : [])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [transitionMode, setTransitionMode] = useState<'REJECT' | 'DELIVER' | null>(null)
  const [transitionNote, setTransitionNote] = useState('')
  const [deliveryType, setDeliveryType] = useState<DataRequestDeliveryType>('EXISTING_REPORT')
  const [deliveryRef, setDeliveryRef] = useState('')
  const subject = currentSubject()
  const domainName = currentDomain()?.name || (snapshot ? '企业经营' : '当前业务领域')

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    Promise.allSettled([dataRequestAPI.list(100), loadDataRequestFieldOptions()]).then(results => {
      if (cancelled) return
      const [requestResult, fieldResult] = results
      if (requestResult.status === 'fulfilled') {
        setItems(requestResult.value.items)
        const first = requestResult.value.items[0]
        setSelectedId(first?.id ?? '')
        setDetail(first ?? null)
      } else {
        setError(requestResult.reason instanceof Error ? requestResult.reason.message : '申请列表读取失败。')
      }
      if (fieldResult.status === 'fulfilled') setFieldOptions(fieldResult.value)
      setLoading(false)
      setFieldsLoading(false)
    })
    return () => { cancelled = true }
  }, [snapshot])

  useEffect(() => {
    if (snapshot || !selectedId) return undefined
    let cancelled = false
    dataRequestAPI.get(selectedId).then(value => {
      if (!cancelled) setDetail(value)
    }).catch(cause => {
      if (!cancelled) setError(cause instanceof Error ? cause.message : '申请详情读取失败。')
    }).finally(() => {
      if (!cancelled) setLoading(false)
    })
    return () => { cancelled = true }
  }, [selectedId, snapshot])

  const filteredItems = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    if (!normalized) return items
    return items.filter(item => item.requestText.toLocaleLowerCase().includes(normalized) || shortRequestNumber(item).toLocaleLowerCase().includes(normalized))
  }, [items, query])

  const fieldMap = useMemo(() => new Map(fieldOptions.map(field => [`${field.datasetVersionId}:${field.fieldId}`, field])), [fieldOptions])

  const replaceRequest = (next: DataRequest) => {
    setItems(current => [next, ...current.filter(item => item.id !== next.id)].sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt)))
    setSelectedId(next.id)
    setDetail(next)
  }

  const selectRequest = (request: DataRequest) => {
    setTransitionMode(null)
    setTransitionNote('')
    setDeliveryRef('')
    setError('')
    setDetail(request)
    if (!snapshot) setLoading(true)
    setSelectedId(request.id)
  }

  const retryDetail = async () => {
    if (!detail || snapshot) {
      setError('')
      return
    }
    setError('')
    setLoading(true)
    try {
      setDetail(await dataRequestAPI.get(detail.id))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '申请详情读取失败。')
    } finally {
      setLoading(false)
    }
  }

  const snapshotTransition = (request: DataRequest, input: DataRequestTransitionInput) => {
    const now = new Date().toISOString()
    const event: DataRequestEvent = {
      id: `${request.id}-event-${request.recordVersion + 1}`,
      requestId: request.id,
      sequenceNo: request.recordVersion + 1,
      fromState: request.state,
      toState: input.toState,
      actorUserId: '当前用户',
      ...(input.note ? { note: input.note } : {}),
      createdAt: now,
    }
    return {
      ...request,
      state: input.toState,
      statusNote: input.note,
      recordVersion: request.recordVersion + 1,
      updatedAt: now,
      ...(input.toState === 'SUBMITTED' ? { submittedAt: now } : {}),
      ...(input.toState === 'APPROVED' ? { approvedAt: now } : {}),
      ...(input.toState === 'REJECTED' ? { rejectedAt: now } : {}),
      ...(input.toState === 'IN_PROGRESS' ? { startedAt: now, assigneeUserId: '当前用户' } : {}),
      ...(input.toState === 'DELIVERED' ? { deliveredAt: now, deliveryType: input.deliveryType, deliveryRef: input.deliveryRef } : {}),
      ...(input.toState === 'CLOSED' ? { closedAt: now } : {}),
      events: [...(request.events ?? []), event],
    }
  }

  const transition = async (toState: DataRequestState, input: Partial<DataRequestTransitionInput> = {}) => {
    if (!detail || busy) return
    setBusy(true)
    setError('')
    try {
      const transitionInput: DataRequestTransitionInput = {
        toState,
        note: input.note ?? '',
        recordVersion: detail.recordVersion,
        ...(input.assigneeUserId ? { assigneeUserId: input.assigneeUserId } : {}),
        ...(input.deliveryType ? { deliveryType: input.deliveryType } : {}),
        ...(input.deliveryRef ? { deliveryRef: input.deliveryRef } : {}),
      }
      const next = snapshot
        ? snapshotTransition(detail, transitionInput)
        : toState === 'SUBMITTED'
          ? await dataRequestAPI.submit(detail.id, detail.recordVersion)
          : await dataRequestAPI.transition(detail.id, transitionInput)
      replaceRequest(next)
      setTransitionMode(null)
      setTransitionNote('')
      setDeliveryRef('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '状态更新失败，请刷新后重试。')
    } finally {
      setBusy(false)
    }
  }

  const createRequest = async (input: CreateDataRequestInput, submitImmediately: boolean) => {
    let created: DataRequest
    if (snapshot) {
      const now = new Date().toISOString()
      const index = String(items.length + 1).padStart(3, '0')
      created = {
        id: `DR-${now.slice(0, 10).replaceAll('-', '')}-${index}`,
        domainId: 'snapshot-domain', requesterUserId: 'snapshot-user', requestText: input.requestText,
        parsedContext: input.parsedContext, businessPurpose: input.businessPurpose, requiredFields: input.requiredFields,
        sensitivityLevel: deriveDataRequestSensitivity(input.requiredFields, fieldOptions), state: 'DRAFT', approverUserIds: ['snapshot-approver'],
        slaDueAt: input.slaDueAt, recordVersion: 1, createdAt: now, updatedAt: now,
        events: snapshotEvents(`DR-${now.slice(0, 10).replaceAll('-', '')}-${index}`, ['DRAFT'], now),
      }
      if (submitImmediately) created = snapshotTransition(created, { toState: 'SUBMITTED', note: '', recordVersion: 1 })
    } else {
      created = await dataRequestAPI.create(input)
      if (submitImmediately) created = await dataRequestAPI.submit(created.id, created.recordVersion)
    }
    replaceRequest(created)
  }

  const canApprove = Boolean(detail && (snapshot || detail.approverUserIds.includes(subject)))
  const canDeliver = Boolean(detail && (snapshot || canApprove || detail.assigneeUserId === subject))
  const canClose = Boolean(detail && (snapshot || canApprove || detail.requesterUserId === subject))

  return <div className="data-request-workbench">
    <aside className="data-request-master" aria-label="我的取数申请列表">
      <header><div><strong>我的申请</strong><small>{items.length} 项</small></div><button type="button" aria-label="新建取数申请" onClick={() => onDialogOpenChange(true)}><Plus size={15} /></button></header>
      <label className="data-request-search"><MagnifyingGlass size={14} aria-hidden="true" /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索申请或编号" /></label>
      <div className="data-request-list">
        {filteredItems.map(item => <button type="button" key={item.id} className={selectedId === item.id ? 'is-active' : ''} onClick={() => selectRequest(item)}>
          <span><strong>{item.requestText}</strong><em className={`is-${item.state.toLocaleLowerCase()}`}>{dataRequestStateLabels[item.state]}</em></span>
          <small><b>{shortRequestNumber(item)}</b><time>{formatTimestamp(item.updatedAt)}</time></small>
        </button>)}
        {!loading && filteredItems.length === 0 && <div className="data-request-list-empty"><ClipboardText size={23} weight="duotone" /><strong>{query ? '没有匹配的申请' : '还没有取数申请'}</strong><small>{query ? '尝试更换搜索关键词' : '创建申请后可在这里跟踪审批与交付'}</small>{!query && <button type="button" onClick={() => onDialogOpenChange(true)}>新建申请</button>}</div>}
      </div>
      <footer><ShieldCheck size={14} weight="fill" aria-hidden="true" /><span>当前领域已锁定<strong>{domainName}</strong></span></footer>
    </aside>

    <section className="data-request-detail" aria-label="取数申请详情">
      {loading && !detail ? <div className="data-request-detail-state" role="status"><span className="data-request-inline-spinner" /><strong>正在读取申请…</strong></div>
        : detail ? <>
          <header className="data-request-detail-heading">
            <div><span className="data-request-heading-icon"><FileText size={19} weight="duotone" /></span><span><h2>{detail.requestText}</h2><small>{shortRequestNumber(detail)} · 创建于 {formatTimestamp(detail.createdAt)}</small></span></div>
            <div className="data-request-detail-actions">
              {detail.state === 'DRAFT' && <button className="primary-button" type="button" disabled={busy} onClick={() => void transition('SUBMITTED')}>提交审批</button>}
              {detail.state === 'SUBMITTED' && canApprove && <><button className="quiet-button" type="button" disabled={busy} onClick={() => setTransitionMode('REJECT')}>驳回</button><button className="primary-button" type="button" disabled={busy} onClick={() => void transition('APPROVED')}>批准申请</button></>}
              {detail.state === 'APPROVED' && canApprove && <button className="primary-button" type="button" disabled={busy} onClick={() => void transition('IN_PROGRESS')}>开始处理</button>}
              {detail.state === 'IN_PROGRESS' && canDeliver && <button className="primary-button" type="button" disabled={busy} onClick={() => setTransitionMode('DELIVER')}>登记交付</button>}
              {detail.state === 'DELIVERED' && canClose && <button className="primary-button" type="button" disabled={busy} onClick={() => void transition('CLOSED')}>确认关闭</button>}
              {['REJECTED', 'CLOSED'].includes(detail.state) && <span className={`data-request-status-pill is-${detail.state.toLocaleLowerCase()}`}>{dataRequestStateLabels[detail.state]}</span>}
            </div>
          </header>

          {transitionMode === 'REJECT' && <form className="data-request-transition-panel" onSubmit={event => { event.preventDefault(); if (transitionNote.trim()) void transition('REJECTED', { note: transitionNote.trim() }) }}>
            <WarningCircle size={18} weight="duotone" aria-hidden="true" /><label><span>驳回原因</span><input autoFocus value={transitionNote} onChange={event => setTransitionNote(event.target.value)} placeholder="请说明申请需补充或调整的内容" /></label><button className="quiet-button" type="button" onClick={() => setTransitionMode(null)}>取消</button><button className="primary-button" type="submit" disabled={!transitionNote.trim() || busy}>确认驳回</button>
          </form>}

          {transitionMode === 'DELIVER' && <form className="data-request-transition-panel is-delivery" onSubmit={event => { event.preventDefault(); if (deliveryRef.trim()) void transition('DELIVERED', { deliveryType, deliveryRef: deliveryRef.trim(), note: transitionNote.trim() }) }}>
            <Package size={18} weight="duotone" aria-hidden="true" />
            <label><span>交付方式</span><select value={deliveryType} onChange={event => setDeliveryType(event.target.value as DataRequestDeliveryType)}>{Object.entries(dataRequestDeliveryLabels).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
            <label><span>交付物引用</span><input value={deliveryRef} onChange={event => setDeliveryRef(event.target.value)} placeholder="报告链接、数据集版本或导出任务 ID" /></label>
            <button className="quiet-button" type="button" onClick={() => setTransitionMode(null)}>取消</button><button className="primary-button" type="submit" disabled={!deliveryRef.trim() || busy}>确认交付</button>
          </form>}

          {error && <p className="data-request-page-error" role="alert"><WarningCircle size={15} />{error}<button type="button" onClick={() => void retryDetail()}><ArrowClockwise size={13} />重试</button></p>}

          <div className="data-request-detail-scroll">
            <section className="data-request-summary-card">
              <header><strong>申请概要</strong><span className={`data-request-status-pill is-${detail.state.toLocaleLowerCase()}`}>{dataRequestStateLabels[detail.state]}</span></header>
              <dl>
                <div className="is-wide"><dt>业务用途</dt><dd>{detail.businessPurpose}</dd></div>
                <div className="is-wide"><dt>需要字段</dt><dd className="data-request-field-chips">{detail.requiredFields.map(field => {
                  const option = fieldMap.get(`${field.datasetVersionId}:${field.fieldId}`)
                  return <span key={`${field.datasetVersionId}:${field.fieldId}`}>{option?.fieldName || field.fieldId}</span>
                })}</dd></div>
                <div><dt>敏感级别</dt><dd><span className={`data-request-sensitivity-chip is-${detail.sensitivityLevel.toLocaleLowerCase()}`}><ShieldCheck size={13} weight="fill" />{dataRequestSensitivityLabels[detail.sensitivityLevel]}</span></dd></div>
                <div><dt>期望交付</dt><dd><CalendarBlank size={13} />{formatDate(detail.slaDueAt)}</dd></div>
              </dl>
              {(detail.statusNote || detail.state === 'REJECTED') && <p className={detail.state === 'REJECTED' ? 'is-rejected' : ''}><Info size={14} />{detail.statusNote || '审批未通过，请根据意见调整后重新申请。'}</p>}
              {detail.deliveryRef && <p className="data-request-delivery-ref"><Package size={14} /><span><small>{detail.deliveryType ? dataRequestDeliveryLabels[detail.deliveryType] : '交付物'}</small><strong>{detail.deliveryRef}</strong></span>{/^https?:\/\//.test(detail.deliveryRef) && <a href={detail.deliveryRef} target="_blank" rel="noreferrer">打开<ArrowSquareOut size={12} /></a>}</p>}
            </section>

            <section className="data-request-progress-card">
              <header><strong>处理进度</strong><small>状态由审批与交付动作自动更新</small></header>
              <ol>{dataRequestTimeline.map((step, index) => {
                const status = dataRequestStepStatus(detail.state, step.state)
                return <li className={`is-${status}`} key={step.state}>
                  <span>{status === 'complete' ? <Check size={13} weight="bold" /> : status === 'rejected' ? <WarningCircle size={13} weight="fill" /> : index + 1}</span>
                  <strong>{step.label}</strong>
                  {index < dataRequestTimeline.length - 1 && <i />}
                </li>
              })}</ol>
            </section>

            <section className="data-request-audit-card">
              <header><strong>流转记录</strong><small>{detail.events?.length ?? 0} 条审计事件</small></header>
              <ol>{[...(detail.events ?? [])].reverse().map(event => <li key={event.id}>
                <span><CheckCircle size={15} weight="fill" /></span>
                <div><strong>{eventTitle(event)}</strong><small><UserCircle size={12} />{event.actorUserId === subject ? '当前用户' : event.actorUserId}</small>{event.note && <p>{event.note}</p>}</div>
                <time>{formatTimestamp(event.createdAt)}</time>
              </li>)}</ol>
            </section>

            <section className="data-request-fulfillment-guide">
              <header><Database size={17} weight="duotone" /><span><strong>交付优先级</strong><small>优先复用可信资产，减少重复取数</small></span></header>
              <ol>
                <li><span>1</span><div><strong>现有报告优先</strong><small>已有可信报告可满足时直接交付引用</small></div></li>
                <li><span>2</span><div><strong>新建 ADS 并纳入语义层</strong><small>复用价值高的需求沉淀为受控数据资产</small></div></li>
                <li><span>3</span><div><strong>一次性导出</strong><small>仅在前两种方式不适用时使用受控导出</small></div></li>
              </ol>
            </section>
          </div>
        </> : <div className="data-request-detail-state"><FileArrowDown size={34} weight="duotone" /><strong>选择一项申请查看详情</strong><small>状态、交付物与完整流转记录会显示在这里。</small></div>}
    </section>

    <DataRequestDialog
      open={dialogOpen}
      prefill={dialogPrefill}
      domainName={domainName}
      fieldOptions={fieldOptions}
      fieldsLoading={fieldsLoading}
      onClose={() => onDialogOpenChange(false)}
      onCreate={createRequest}
    />
  </div>
}
