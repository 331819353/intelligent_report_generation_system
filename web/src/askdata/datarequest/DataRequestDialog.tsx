import {
  CalendarBlank,
  CheckCircle,
  Database,
  Info,
  LockSimple,
  ShieldCheck,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState } from 'react'

import type {
  CreateDataRequestInput,
  DataRequestFieldOption,
  DataRequestFieldRef,
  DataRequestParsedContext,
} from './model.ts'
import {
  dataRequestSensitivityLabels,
  deriveDataRequestSensitivity,
  sanitizeDataRequestContext,
  validateDataRequestDraft,
} from './model.ts'

export type DataRequestPrefill = {
  sourceQuestionRunId?: string
  requestText?: string
  parsedContext?: unknown
}

type DataRequestDialogProps = {
  open: boolean
  domainName: string
  fieldOptions: DataRequestFieldOption[]
  fieldsLoading: boolean
  prefill?: DataRequestPrefill
  onClose: () => void
  onCreate: (input: CreateDataRequestInput, submitImmediately: boolean) => Promise<void>
}

function defaultDueAt() {
  const value = new Date(Date.now() + 4 * 24 * 60 * 60 * 1000)
  const local = new Date(value.getTime() - value.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 16)
}

export function DataRequestDialog({
  open,
  domainName,
  fieldOptions,
  fieldsLoading,
  prefill,
  onClose,
  onCreate,
}: DataRequestDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [requestText, setRequestText] = useState('')
  const [businessPurpose, setBusinessPurpose] = useState('')
  const [slaDueAt, setSlaDueAt] = useState(defaultDueAt)
  const [selectedFields, setSelectedFields] = useState<DataRequestFieldRef[]>([])
  const [parsedContext, setParsedContext] = useState<DataRequestParsedContext>({})
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const sensitivity = deriveDataRequestSensitivity(selectedFields, fieldOptions)

  const fieldGroups = useMemo(() => {
    const groups = new Map<string, DataRequestFieldOption[]>()
    fieldOptions.forEach(option => groups.set(option.datasetName, [...(groups.get(option.datasetName) ?? []), option]))
    return Array.from(groups.entries())
  }, [fieldOptions])

  useEffect(() => {
    const dialog = dialogRef.current
    if (!dialog) return
    if (open && !dialog.open) {
      setRequestText(prefill?.requestText?.trim() ?? '')
      setBusinessPurpose('')
      setSlaDueAt(defaultDueAt())
      setSelectedFields([])
      setParsedContext(sanitizeDataRequestContext(prefill?.parsedContext))
      setError('')
      setSubmitting(false)
      dialog.showModal()
      requestAnimationFrame(() => dialog.querySelector<HTMLTextAreaElement>('[name="requestText"]')?.focus({ preventScroll: true }))
    } else if (!open && dialog.open) {
      dialog.close()
    }
  }, [open, prefill])

  const close = () => {
    if (submitting) return
    dialogRef.current?.close()
    onClose()
  }

  const toggleField = (option: DataRequestFieldOption) => {
    const key = `${option.datasetVersionId}:${option.fieldId}`
    setSelectedFields(current => current.some(item => `${item.datasetVersionId}:${item.fieldId}` === key)
      ? current.filter(item => `${item.datasetVersionId}:${item.fieldId}` !== key)
      : [...current, { datasetVersionId: option.datasetVersionId, fieldId: option.fieldId }])
  }

  const create = async (submitImmediately: boolean) => {
    if (submitting) return
    const dueAt = new Date(slaDueAt)
    const input: CreateDataRequestInput = {
      ...(prefill?.sourceQuestionRunId ? { sourceQuestionRunId: prefill.sourceQuestionRunId } : {}),
      requestText: requestText.trim(),
      parsedContext: prefill?.sourceQuestionRunId ? parsedContext : {},
      businessPurpose: businessPurpose.trim(),
      requiredFields: selectedFields,
      slaDueAt: Number.isNaN(dueAt.getTime()) ? '' : dueAt.toISOString(),
    }
    const issue = validateDataRequestDraft(input)
    if (issue) {
      setError(issue)
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await onCreate(input, submitImmediately)
      setSubmitting(false)
      dialogRef.current?.close()
      onClose()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '申请保存失败，请稍后重试。')
    } finally {
      setSubmitting(false)
    }
  }

  const selectedKeys = new Set(selectedFields.map(field => `${field.datasetVersionId}:${field.fieldId}`))

  return <dialog
    className="data-request-dialog"
    ref={dialogRef}
    aria-labelledby="data-request-dialog-title"
    onCancel={event => { event.preventDefault(); close() }}
    onClose={() => { if (open) onClose() }}
  >
    <form onSubmit={event => { event.preventDefault(); void create(true) }}>
      <header className="data-request-dialog-heading">
        <div>
          <span><Database size={17} weight="duotone" aria-hidden="true" /></span>
          <div><h2 id="data-request-dialog-title">新建取数申请</h2><p>提交后进入领域审批与交付流程，不会直接返回明细行。</p></div>
        </div>
        <button type="button" aria-label="关闭取数申请" disabled={submitting} onClick={close}><X size={19} /></button>
      </header>

      <div className="data-request-dialog-body">
        <section className="data-request-fixed-domain" aria-label="固定业务领域">
          <LockSimple size={17} weight="duotone" aria-hidden="true" />
          <span><small>所属领域 · 登录会话已锁定</small><strong>{domainName}</strong></span>
          <em>不可切换</em>
        </section>

        <div className="data-request-form-grid">
          <label className="is-wide"><span>取数需求 <b>*</b></span><textarea name="requestText" rows={3} maxLength={4096} value={requestText} onChange={event => setRequestText(event.target.value)} placeholder="例如：导出本月已支付订单明细，按订单维度提供交付字段" /></label>
          <label className="is-wide"><span>业务用途 <b>*</b></span><textarea rows={2} maxLength={2000} value={businessPurpose} onChange={event => setBusinessPurpose(event.target.value)} placeholder="说明使用场景、决策目的和使用人群" /></label>
          <label><span>期望交付时间 <b>*</b></span><span className="data-request-input-icon"><CalendarBlank size={15} aria-hidden="true" /><input type="datetime-local" value={slaDueAt} onChange={event => setSlaDueAt(event.target.value)} /></span></label>
          <div className="data-request-sensitivity-readonly"><span>敏感级别</span><output className={`is-${sensitivity.toLocaleLowerCase()}`}><ShieldCheck size={15} weight="fill" aria-hidden="true" />{selectedFields.length ? dataRequestSensitivityLabels[sensitivity] : '选择字段后推导'}</output></div>
        </div>

        <fieldset className="data-request-field-picker">
          <legend>需要的字段 <b>*</b><small>仅展示当前领域可见的已发布数据集字段</small></legend>
          {fieldsLoading ? <div className="data-request-field-state" role="status"><span className="data-request-inline-spinner" />正在读取字段目录…</div>
            : fieldGroups.length === 0 ? <div className="data-request-field-state is-warning"><Info size={18} aria-hidden="true" /><span><strong>暂无可申请字段</strong><small>请先在当前领域发布包含可见输出字段的数据集。</small></span></div>
              : <div className="data-request-field-groups">{fieldGroups.map(([datasetName, fields]) => <section key={datasetName}>
                <header><strong>{datasetName}</strong><small>{fields.length} 个可见字段</small></header>
                <div>{fields.map(field => {
                  const key = `${field.datasetVersionId}:${field.fieldId}`
                  const selected = selectedKeys.has(key)
                  return <label className={selected ? 'is-selected' : ''} key={key}>
                    <input type="checkbox" checked={selected} onChange={() => toggleField(field)} />
                    <span><strong>{field.fieldName}</strong><small>{field.fieldCode}</small></span>
                    {selected && <CheckCircle size={15} weight="fill" aria-hidden="true" />}
                  </label>
                })}</div>
              </section>)}</div>}
        </fieldset>

        {sensitivity === 'CONFIDENTIAL' || sensitivity === 'RESTRICTED'
          ? <p className="data-request-cosign-note"><ShieldCheck size={15} weight="fill" aria-hidden="true" />该字段组合需要安全会签，审批时间可能延长。</p>
          : <p className="data-request-form-note"><Info size={14} aria-hidden="true" />平台会按所选字段自动推导最高敏感级；用户不能手动修改。</p>}
        {error && <p className="data-request-form-error" role="alert">{error}</p>}
      </div>

      <footer>
        <button className="quiet-button" type="button" onClick={close} disabled={submitting}>取消</button>
        <button className="quiet-button" type="button" onClick={() => void create(false)} disabled={submitting || fieldsLoading || fieldOptions.length === 0}>{submitting ? '保存中…' : '保存草稿'}</button>
        <button className="primary-button" type="submit" disabled={submitting || fieldsLoading || fieldOptions.length === 0}>{submitting ? '提交中…' : '创建并提交'}</button>
      </footer>
    </form>
  </dialog>
}
