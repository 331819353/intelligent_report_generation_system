import { Check, Plus, ShieldWarning, SpinnerGap, Trash, X } from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { AppButton } from './AppButton'
import { administrationAPI, type SubjectAttribute } from '../lib/administration'

type Props = {
  userID: string
  disabled?: boolean
  onNotice: (tone: 'success' | 'error', message: string) => void
}

/**
 * Administered business attributes of one reader.
 *
 * Governed row access policies match a model field against these values, so
 * they are granted by an administrator and never asserted by the person they
 * describe — a value someone could set for themselves would make the policy
 * reading it worthless.
 *
 * The panel states the consequence in both directions, because both are easy to
 * get wrong: granting widens what this person can read, and revoking makes every
 * policy referencing the attribute deny them every row.
 */
export function SubjectAttributesPanel({ userID, disabled = false, onNotice }: Props) {
  const [items, setItems] = useState<SubjectAttribute[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [adding, setAdding] = useState(false)
  const [draftKey, setDraftKey] = useState('')
  const [draftValues, setDraftValues] = useState('')

  const load = useCallback(async (id: string) => {
    if (!id) { setItems([]); setLoading(false); return }
    setLoading(true); setError('')
    try {
      setItems(await administrationAPI.listSubjectAttributes(id))
    } catch (cause) {
      setItems([])
      setError(cause instanceof Error ? cause.message : '主体属性读取失败')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load(userID) }, 0)
    return () => window.clearTimeout(timer)
  }, [load, userID])

  // Values are comma or newline separated; blanks are dropped and duplicates
  // collapse server-side, so the same grant always stores the same bytes.
  const parseValues = (raw: string) =>
    raw.split(/[,，\n]/).map(value => value.trim()).filter(Boolean)

  const save = async (key: string, raw: string) => {
    const values = parseValues(raw)
    if (!/^[a-z][a-z0-9_]{0,63}$/.test(key)) {
      setError('属性键需为小写字母开头，仅使用小写字母、数字和下划线。')
      return
    }
    if (!values.length) {
      setError('至少需要一个取值；如需收回授权请使用撤销。')
      return
    }
    setBusy(key); setError('')
    try {
      await administrationAPI.setSubjectAttribute(userID, key, values)
      await load(userID)
      setAdding(false); setDraftKey(''); setDraftValues('')
      onNotice('success', `已授予「${key}」共 ${values.length} 个取值；引用该属性的行级策略将据此放行对应行。`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '主体属性保存失败')
    } finally { setBusy('') }
  }

  const revoke = async (key: string) => {
    setBusy(key); setError('')
    try {
      await administrationAPI.deleteSubjectAttribute(userID, key)
      await load(userID)
      onNotice('success', `已撤销「${key}」；引用该属性的行级策略将对该成员拒绝全部行。`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '主体属性撤销失败')
    } finally { setBusy('') }
  }

  return <section className="subject-attributes">
    <header>
      <h3>数据范围属性 <span>（{items.length} 项）</span></h3>
      <AppButton plain type="button" disabled={disabled || adding} onClick={() => setAdding(true)}>
        <Plus size={14} />授予属性
      </AppButton>
    </header>
    <p className="subject-attributes-hint">
      行级访问策略用这些属性裁剪该成员能读到的行。
      <strong>没有被授予策略所引用的属性时，该成员在该策略下读不到任何行。</strong>
      属性由管理员授予，成员不能自述。
    </p>

    {loading && <div className="subject-attributes-state"><SpinnerGap className="spin" size={16} />正在读取</div>}

    {!loading && error && <div className="subject-attributes-state is-error">
      <ShieldWarning size={16} />{error}
    </div>}

    {!loading && items.length === 0 && !adding && <div className="subject-attributes-state">
      该成员尚未被授予任何数据范围属性
    </div>}

    {!loading && items.length > 0 && <ul className="subject-attributes-list">
      {items.map(item => <li key={item.attributeKey}>
        <code>{item.attributeKey}</code>
        <span>{item.attributeValues.join('、')}</span>
        <AppButton
          text
          circle
          type="button"
          aria-label={`撤销${item.attributeKey}`}
          disabled={disabled || busy !== ''}
          onClick={() => void revoke(item.attributeKey)}
        >{busy === item.attributeKey ? <SpinnerGap className="spin" size={14} /> : <Trash size={14} />}</AppButton>
      </li>)}
    </ul>}

    {adding && <div className="subject-attributes-form">
      <label>
        <span>属性键</span>
        <input
          value={draftKey}
          placeholder="region_code"
          onChange={event => setDraftKey(event.target.value.trim().toLowerCase())}
        />
      </label>
      <label>
        <span>取值（逗号或换行分隔）</span>
        <input
          value={draftValues}
          placeholder="EAST, NORTH"
          onChange={event => setDraftValues(event.target.value)}
        />
      </label>
      <div>
        <AppButton type="button" disabled={busy !== ''} onClick={() => { setAdding(false); setError('') }}>
          <X size={14} />取消
        </AppButton>
        <AppButton
          type="button"
          variant="primary"
          disabled={busy !== '' || !draftKey || !draftValues.trim()}
          onClick={() => void save(draftKey, draftValues)}
        ><Check size={14} />授予</AppButton>
      </div>
    </div>}
  </section>
}
