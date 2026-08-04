import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import {
  dataSourceAPI,
  DataSourceConnectionTestError,
  type DataSourceAIDraft,
  type DataSourceAIMessage,
  type DataSourceAITestFailure,
  type DataSourceRecord,
  type DataSourceType,
  type ExcelDataSourceInput,
} from '../lib/data-sources'

type Notice = { tone: 'success' | 'error'; message: string }
type Props = {
  sources: DataSourceRecord[]
  onSourceChanged: (source: DataSourceRecord) => void
  onReload: () => Promise<unknown>
  onNotice: (notice: Notice) => void
}

const blankDraft = (): DataSourceAIDraft => ({
  code: '', name: '', description: '', type: 'MYSQL', host: '', port: 3306,
  database: '', username: '', visibility: 'PRIVATE', sharingScope: 'PRIVATE',
})

const textConfig = (source: DataSourceRecord, key: string) => {
  const value = source.config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}

const draftFromSource = (source: DataSourceRecord): DataSourceAIDraft => ({
  code: source.code,
  name: source.name,
  description: source.description || '',
  type: source.type,
  host: textConfig(source, 'host'),
  port: Number(textConfig(source, 'port')) || (source.type === 'ORACLE' ? 1521 : 3306),
  database: textConfig(source, 'database'),
  username: textConfig(source, 'username'),
  visibility: source.visibility || 'PRIVATE',
  sharingScope: source.sharingScope || 'PRIVATE',
})

const fieldLabels: Record<string, string> = {
  code: '编码', name: '名称', type: '类型', host: 'Host', port: '端口',
  database: '数据库 / 服务名', username: '用户名', password: '密码', file: 'Excel / CSV 文件',
}

const initialMessage: DataSourceAIMessage = {
  role: 'assistant',
  content: '告诉我数据源类型和你已有的信息。我会逐项补齐、保存草稿并测试；测试通过后，仍由你点击发布。密码只在安全输入框填写，不会发送给模型。',
}

export function DataSourceAIAssistant({ sources, onSourceChanged, onReload, onNotice }: Props) {
  const [open, setOpen] = useState(false)
  const [sourceId, setSourceId] = useState('')
  const [draft, setDraft] = useState<DataSourceAIDraft>(blankDraft)
  const [messages, setMessages] = useState<DataSourceAIMessage[]>([initialMessage])
  const [instruction, setInstruction] = useState('')
  const [password, setPassword] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [missingFields, setMissingFields] = useState<string[]>([])
  const [checks, setChecks] = useState<string[]>([])
  const [fixes, setFixes] = useState<string[]>([])
  const [busy, setBusy] = useState<'chat' | 'test' | 'publish' | ''>('')
  const [testedSource, setTestedSource] = useState<DataSourceRecord | null>(null)
  const [testPassed, setTestPassed] = useState(false)
  const logRef = useRef<HTMLDivElement | null>(null)
  const selectedSource = useMemo(() => sources.find(source => source.id === sourceId), [sourceId, sources])
  const effectiveMissingFields = useMemo(() => {
    const satisfied: Record<string, boolean> = {
      code: Boolean(draft.code.trim()), name: Boolean(draft.name.trim()), type: Boolean(draft.type),
      host: Boolean(draft.host.trim()), port: draft.port > 0 && draft.port <= 65535,
      database: Boolean(draft.database.trim()), username: Boolean(draft.username.trim()),
      password: Boolean(password || selectedSource), file: Boolean(file || selectedSource?.fileAssetId),
    }
    const required = ['code', 'name', 'type']
    if (draft.type === 'EXCEL') required.push('file')
    else required.push('host', 'port', 'database', 'username', 'password')
    return [...new Set([...missingFields, ...required])].filter(field => !satisfied[field])
  }, [draft, file, missingFields, password, selectedSource])

  useEffect(() => {
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }, [messages, busy])

  const resetConversation = (nextSourceId: string) => {
    const source = sources.find(item => item.id === nextSourceId)
    setSourceId(nextSourceId)
    setDraft(source ? draftFromSource(source) : blankDraft())
    setMessages([{
      role: 'assistant',
      content: source
        ? `正在修改“${source.name}”。请告诉我需要变更什么；未提到的现有配置会保留。`
        : initialMessage.content,
    }])
    setInstruction('')
    setPassword('')
    setFile(null)
    setMissingFields([])
    setChecks([])
    setFixes([])
    setTestedSource(null)
    setTestPassed(false)
  }

  const runTurn = async (
    text: string,
    failure?: DataSourceAITestFailure,
    manageBusy = true,
    sourceIDOverride?: string,
  ) => {
    const userMessage: DataSourceAIMessage = { role: 'user', content: text }
    const history = [...messages, userMessage].slice(-16)
    setMessages(history)
    if (manageBusy) setBusy('chat')
    try {
      const result = await dataSourceAPI.aiTurn(sourceIDOverride || sourceId || null, {
        instruction: text,
        history,
        draft,
        passwordProvided: Boolean(password),
        fileProvided: Boolean(file || selectedSource?.fileAssetId),
        ...(failure ? { testFailure: failure } : {}),
      })
      setDraft(result.draft)
      setMissingFields(result.missingFields)
      setChecks(result.suggestedChecks)
      setFixes(result.autoFixes)
      setMessages(current => [...current, { role: 'assistant', content: result.reply } as DataSourceAIMessage].slice(-18))
      return result
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : 'AI 助手响应失败'
      setMessages(current => [...current, { role: 'assistant', content: `暂时无法处理：${message}` } as DataSourceAIMessage].slice(-18))
      return null
    } finally {
      if (manageBusy) setBusy('')
    }
  }

  const submitMessage = (event: FormEvent) => {
    event.preventDefault()
    const text = instruction.trim()
    if (!text || busy) return
    setInstruction('')
    setTestPassed(false)
    void runTurn(text)
  }

  const persistDraft = async (value: DataSourceAIDraft, current?: DataSourceRecord) => {
    if (value.type === 'EXCEL') {
      let liveCurrent = current
      let fileAssetId = liveCurrent?.fileAssetId || ''
      if (file) {
        const asset = liveCurrent?.fileAssetId
          ? await dataSourceAPI.uploadExcelVersion(liveCurrent.fileAssetId, file)
          : await dataSourceAPI.uploadExcel(file)
        fileAssetId = asset.id
        if (liveCurrent) liveCurrent = await dataSourceAPI.get(liveCurrent.id)
      }
      if (!fileAssetId) throw new Error('请先选择 Excel 或 CSV 文件')
      const input: ExcelDataSourceInput = {
        code: value.code, name: value.name, description: value.description,
        visibility: value.visibility, sharingScope: value.sharingScope,
        type: 'EXCEL', fileAssetId,
      }
      return liveCurrent
        ? dataSourceAPI.update(liveCurrent.id, { ...input, expectedVersion: liveCurrent.version })
        : dataSourceAPI.create(input)
    }
    const input = {
      code: value.code, name: value.name, description: value.description,
      visibility: value.visibility, sharingScope: value.sharingScope,
      type: value.type as Exclude<DataSourceType, 'EXCEL'>,
      host: value.host, port: value.port, database: value.database,
      username: value.username, password,
    }
    return current
      ? dataSourceAPI.update(current.id, { ...input, expectedVersion: current.version })
      : dataSourceAPI.create(input)
  }

  const saveAndTest = async () => {
    if (busy) return
    if (effectiveMissingFields.length) {
      setMessages(current => [...current, { role: 'assistant', content: `还缺少：${effectiveMissingFields.map(item => fieldLabels[item] || item).join('、')}` }])
      return
    }
    setBusy('test')
    setTestPassed(false)
    try {
      let saved = await persistDraft(draft, selectedSource)
      onSourceChanged(saved)
      if (!selectedSource) setSourceId(saved.id)
      if (draft.type === 'EXCEL') setFile(null)
      let retried = false
      while (true) {
        try {
          const test = await dataSourceAPI.test(saved.id)
          const latest = await dataSourceAPI.get(saved.id)
          onSourceChanged(latest)
          setTestedSource(latest)
          setTestPassed(true)
          setPassword('')
          setMessages(current => [...current, {
            role: 'assistant',
            content: `连接测试通过${test.serverVersion ? `，服务版本 ${test.serverVersion}` : ''}。请检查配置摘要，然后由你点击“提交发布”。`,
          }])
          onNotice({ tone: 'success', message: `“${latest.name}”连接测试通过，等待你提交发布` })
          break
        } catch (cause) {
          if (!(cause instanceof DataSourceConnectionTestError)) throw cause
          const result = await runTurn(`连接测试失败，请诊断并仅修复可以安全确定的问题。错误代码：${cause.code}`, {
            code: cause.code, message: cause.message,
          }, false, saved.id)
          if (!retried && result?.autoRetry) {
            retried = true
            saved = await persistDraft(result.draft, saved)
            onSourceChanged(saved)
            setMessages(current => [...current, { role: 'assistant', content: '已应用确定性的格式修复，正在自动重试一次。' }])
            continue
          }
          setTestedSource(saved)
          onNotice({ tone: 'error', message: cause.message })
          break
        }
      }
      await onReload()
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '保存或测试数据源失败'
      setMessages(current => [...current, { role: 'assistant', content: `未能完成保存和测试：${message}` }])
      onNotice({ tone: 'error', message })
    } finally {
      setBusy('')
    }
  }

  const publish = async () => {
    if (!testedSource || busy) return
    setBusy('publish')
    try {
      await dataSourceAPI.submitPublicationRequest(testedSource.id, '由数据源 AI 助手完成配置和连接测试，申请发布')
      setMessages(current => [...current, { role: 'assistant', content: '发布申请已提交，配置会在现有权限审批通过后生效。' }])
      setTestPassed(false)
      await onReload()
      onNotice({ tone: 'success', message: `“${testedSource.name}”发布申请已提交` })
    } catch (cause) {
      onNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交发布申请失败' })
    } finally {
      setBusy('')
    }
  }

  return <aside className={`data-source-ai${open ? ' open' : ''}`} onMouseEnter={() => setOpen(true)} aria-label="数据源 AI 助手">
    <button className="data-source-ai-rail" type="button" onClick={() => setOpen(current => !current)} aria-expanded={open}>
      <span aria-hidden="true">AI</span><strong>数据源助手</strong>
    </button>
    {open && <section className="data-source-ai-panel" role="dialog" aria-label="数据源 AI 配置对话">
      <header><div><small>AI ASSISTANT</small><strong>数据源配置助手</strong></div><button type="button" aria-label="关闭数据源 AI 助手" onClick={() => setOpen(false)}>×</button></header>
      <label className="data-source-ai-target">操作对象<select value={sourceId} disabled={Boolean(busy)} onChange={event => resetConversation(event.target.value)}><option value="">新建数据源</option>{sources.map(source => <option key={source.id} value={source.id}>修改：{source.name}</option>)}</select></label>
      <div className="data-source-ai-log" ref={logRef} aria-live="polite">{messages.map((message, index) => <div className={`data-source-ai-message ${message.role}`} key={`${message.role}-${index}`}><small>{message.role === 'assistant' ? 'AI 助手' : '你'}</small><p>{message.content}</p></div>)}{busy === 'chat' && <div className="data-source-ai-thinking">正在理解并补全配置…</div>}</div>
      <div className="data-source-ai-summary">
        <div><span>名称</span><strong>{draft.name || '待补充'}</strong></div><div><span>类型</span><strong>{draft.type}</strong></div>
        {draft.type !== 'EXCEL' && <><div><span>地址</span><strong>{draft.host ? `${draft.host}:${draft.port || '—'}` : '待补充'}</strong></div><div><span>数据库</span><strong>{draft.database || '待补充'}</strong></div></>}
      </div>
      {effectiveMissingFields.length > 0 && <p className="data-source-ai-missing">待补充：{effectiveMissingFields.map(item => fieldLabels[item] || item).join('、')}</p>}
      {fixes.length > 0 && <div className="data-source-ai-advice success"><strong>已安全规范化</strong>{fixes.map(item => <span key={item}>• {item}</span>)}</div>}
      {checks.length > 0 && <div className="data-source-ai-advice"><strong>建议检查</strong>{checks.map(item => <span key={item}>• {item}</span>)}</div>}
      {draft.type === 'EXCEL' ? <label className="data-source-ai-secret">数据文件<input type="file" accept=".xlsx,.xls,.csv" onChange={event => setFile(event.target.files?.[0] || null)} /><small>{file?.name || selectedSource?.fileAssetId ? file?.name || '沿用已上传文件' : '文件内容不会发送到配置模型'}</small></label> : <label className="data-source-ai-secret">数据库密码<input type="password" autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} placeholder={selectedSource ? '留空则沿用已保存密码' : '仅在安全输入框填写'} /><small>密码不进入对话或模型请求</small></label>}
      <form className="data-source-ai-composer" onSubmit={submitMessage}><textarea rows={3} value={instruction} disabled={Boolean(busy)} onChange={event => setInstruction(event.target.value)} placeholder="例如：新建 MySQL，地址 db.internal:3306，库名 sales…" /><button type="submit" disabled={Boolean(busy) || !instruction.trim()}>发送</button></form>
      <footer><button className="quiet-button" type="button" disabled={Boolean(busy)} onClick={() => resetConversation(sourceId)}>重置对话</button><button className="primary-button" type="button" disabled={Boolean(busy)} onClick={() => void saveAndTest()}>{busy === 'test' ? '保存并测试中…' : selectedSource ? '保存修改并测试' : '创建草稿并测试'}</button>{testPassed && <button className="data-source-ai-publish" type="button" disabled={Boolean(busy)} onClick={() => void publish()}>{busy === 'publish' ? '提交中…' : '提交发布'}</button>}</footer>
    </section>}
  </aside>
}
