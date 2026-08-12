import { BookmarkSimple, CheckCircle, ShieldCheck, WarningCircle, X } from '@phosphor-icons/react'
import { useEffect, useState, type FormEvent } from 'react'
import { administrationAPI, type ShareTarget } from '../../lib/administration'
import { mapAskDataError, questionAPI, type QuestionRun, type SavedQuestionVisibility } from '../../lib/ask-data-api'

type SaveQuestionDialogProps = {
  open: boolean
  run: QuestionRun
  question: string
  snapshot?: boolean
  onClose: () => void
  onSaved: () => void
}

export function SaveQuestionDialog({ open, run, question, snapshot = false, onClose, onSaved }: SaveQuestionDialogProps) {
  const [name, setName] = useState(question.slice(0, 200))
  const [visibility, setVisibility] = useState<SavedQuestionVisibility>('PRIVATE')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [targets, setTargets] = useState<ShareTarget[]>([])
  const [principalId, setPrincipalId] = useState('')

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    void administrationAPI.listShareTargets()
      .then(items => { if (!cancelled) setTargets(items) })
      .catch(() => { if (!cancelled) setTargets([]) })
    return () => { cancelled = true }
  }, [snapshot])

  if (!open) return null
  const close = () => { if (!busy) onClose() }
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!name.trim() || busy) return
    setBusy(true); setError('')
    try {
      if (!snapshot) {
        const target = targets.find(candidate => candidate.id === principalId)
        if (visibility === 'TEAM' && !target) throw new Error('分享对象已失效，请重新选择。')
        await questionAPI.saveQuestionFromRun({
          runId: run.runId, name: name.trim(), questionText: question, visibility,
          ...(target ? { shareTarget: { type: target.type, id: target.id } } : {}),
        })
      }
      setSaved(true)
      onSaved()
    } catch (cause) {
      setError(mapAskDataError(cause).message)
    } finally {
      setBusy(false)
    }
  }

  return <div className="ask-dialog-overlay" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) close() }}>
    <section className="ask-save-question-dialog" role="dialog" aria-modal="true" aria-labelledby="save-question-title">
      {saved ? <div className="ask-save-question-success">
        <span><CheckCircle size={42} weight="duotone" aria-hidden="true" /></span>
        <h2 id="save-question-title">已收藏为常用问题</h2>
        <p>可从首页或问数工作台的“常用问题”再次运行，系统会固定当前已发布语义口径。</p>
        <button className="primary-button" type="button" onClick={close}>完成</button>
      </div> : <form onSubmit={submit}>
        <header>
          <div><span><BookmarkSimple size={18} weight="duotone" /></span><div><h2 id="save-question-title">收藏这次分析</h2><p>保存问题和受控语义口径，方便以后直接运行。</p></div></div>
          <button type="button" aria-label="关闭" onClick={close} disabled={busy}><X size={18} /></button>
        </header>
        <div className="ask-save-question-body">
          <label>收藏名称<input autoFocus maxLength={200} value={name} onChange={event => setName(event.target.value)} /></label>
          <label>可见范围<select value={visibility} onChange={event => setVisibility(event.target.value as SavedQuestionVisibility)}>
            <option value="PRIVATE">仅自己</option>
            <option value="TEAM">团队成员（需授权）</option>
            <option value="CERTIFIED_CANDIDATE">提交为认证问法候选</option>
          </select></label>
          {visibility === 'TEAM' && <label>授权团队成员<select value={principalId} onChange={event => setPrincipalId(event.target.value)} required>
            <option value="">请选择用户或角色</option>
            {targets.map(target => <option value={target.id} key={`${target.type}:${target.id}`}>{target.name} · {target.type === 'ROLE' ? '角色' : '用户'} · {target.detail}</option>)}
          </select></label>}
          <div className="ask-save-question-source"><strong>原始问题</strong><p>{question}</p><small>Run v{run.recordVersion} · Release {run.release.releaseId.slice(0, 8)}</small></div>
          <p className="ask-save-question-trust"><ShieldCheck size={16} weight="fill" />服务端会从本次已验证运行提取语义 IR，浏览器不能替换指标、维度或过滤条件。</p>
        </div>
        {error && <p className="ask-dialog-error" role="alert"><WarningCircle size={15} />{error}</p>}
        <footer><button type="button" onClick={close} disabled={busy}>取消</button><button className="primary-button" type="submit" disabled={busy || !name.trim() || visibility === 'TEAM' && !principalId}>{busy ? '正在收藏…' : '确认收藏'}</button></footer>
      </form>}
    </section>
  </div>
}
