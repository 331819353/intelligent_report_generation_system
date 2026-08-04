import {
  ArrowCounterClockwise,
  CheckCircle,
  ChatCircleDots,
  FileArrowUp,
  PaperPlaneRight,
  PlugsConnected,
  Sparkle,
  SpinnerGap,
  X,
} from '@phosphor-icons/react'
import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import { RequestError } from '../lib/api'
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
  database: '', oracleConnectMode: 'SERVICE_NAME', username: '', visibility: 'PRIVATE', sharingScope: 'PRIVATE',
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
  oracleConnectMode: textConfig(source, 'oracleConnectMode') === 'SID' ? 'SID' : 'SERVICE_NAME',
  username: textConfig(source, 'username'),
  visibility: source.visibility || 'PRIVATE',
  sharingScope: source.sharingScope || 'PRIVATE',
})

const fieldLabels: Record<string, string> = {
  code: '编码', name: '名称', type: '类型', host: 'Host', port: '端口',
  database: '数据库 / 服务名', username: '用户名', password: '密码', file: 'Excel / CSV 文件',
}

const publicationFailureChecks = (code: string) => {
  switch (code) {
    case 'DATA_SOURCE_TEST_PENDING':
      return ['等待当前连接测试完成后再次点击发布']
    case 'DATA_SOURCE_TEST_REQUIRED':
    case 'DATA_SOURCE_TEST_EXPIRED':
      return ['重新测试当前配置', '测试通过后自动重新提交发布']
    case 'DATA_SOURCE_VERSION_CHANGED':
      return ['已重新加载服务端最新配置', '确认配置后重新测试并发布']
    case 'DATA_SOURCE_REVIEW_PENDING':
      return ['当前发布申请已在审批队列中，无需重复提交']
    default:
      return ['刷新数据源状态后重试', '若仍失败，请根据错误码检查发布权限和审核状态']
  }
}

const welcomeMessage: DataSourceAIMessage = {
  role: 'assistant',
  content: '你好，我是数据源配置助手。告诉我你想新建数据源，还是选择一个现有数据源进行修改；之后我们会通过对话逐步补齐信息。',
}

const prepareChatInstruction = (input: string) => {
  let detectedPassword = ''
  const hidePassword = (prefix: string, value: string, suffix: string) => {
    const candidate = value.trim()
    if (!detectedPassword && candidate && !candidate.startsWith('[已')) detectedPassword = candidate
    return `${prefix}[已转入安全输入]${suffix}`
  }
  const markdownPassword = /(\|\s*(?:密码|口令|password|passwd)\s*\|\s*`?)([^`|\r\n]+)(`?\s*\|)/gi
  const inlinePassword = /((?:密码|口令|password|passwd)\s*[:=：]\s*["'`]?)([^"'`\r\n|]+)(["'`]?)/gi
  const text = input
    .replace(markdownPassword, (_match, prefix: string, value: string, suffix: string) => hidePassword(prefix, value, suffix))
    .replace(inlinePassword, (_match, prefix: string, value: string, suffix: string) => hidePassword(prefix, value, suffix))
  return { text, password: detectedPassword }
}

export function DataSourceAIAssistant({ sources, onSourceChanged, onReload, onNotice }: Props) {
  const [open, setOpen] = useState(false)
  const [modeChosen, setModeChosen] = useState(false)
  const [sourceId, setSourceId] = useState('')
  const [workingSource, setWorkingSource] = useState<DataSourceRecord | null>(null)
  const [draft, setDraft] = useState<DataSourceAIDraft>(blankDraft)
  const [messages, setMessages] = useState<DataSourceAIMessage[]>([welcomeMessage])
  const [instruction, setInstruction] = useState('')
  const [password, setPassword] = useState('')
  const [savedPasswordRejected, setSavedPasswordRejected] = useState(false)
  const [file, setFile] = useState<File | null>(null)
  const [missingFields, setMissingFields] = useState<string[]>([])
  const [checks, setChecks] = useState<string[]>([])
  const [fixes, setFixes] = useState<string[]>([])
  const [assistantTurnCompleted, setAssistantTurnCompleted] = useState(false)
  const [busy, setBusy] = useState<'chat' | 'test' | 'publish' | ''>('')
  const [testedSource, setTestedSource] = useState<DataSourceRecord | null>(null)
  const [testPassed, setTestPassed] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const logRef = useRef<HTMLDivElement | null>(null)
  const listedSource = useMemo(() => sources.find(source => source.id === sourceId), [sourceId, sources])
  const activeSource = workingSource || listedSource
  const effectiveMissingFields = useMemo(() => {
    const satisfied: Record<string, boolean> = {
      code: Boolean(draft.code.trim()),
      name: Boolean(draft.name.trim()),
      type: Boolean(draft.type),
      host: Boolean(draft.host.trim()),
      port: draft.port > 0 && draft.port <= 65535,
      database: Boolean(draft.database.trim()),
      username: Boolean(draft.username.trim()),
      password: Boolean(password || (activeSource && !savedPasswordRejected)),
      file: Boolean(file || activeSource?.fileAssetId),
    }
    const required = ['type']
    if (draft.type === 'EXCEL') required.push('code', 'name', 'file')
    else required.push('host', 'port', 'database', 'username', 'password')
    return [...new Set([...missingFields, ...required])].filter(field => !satisfied[field])
  }, [activeSource, draft, file, missingFields, password, savedPasswordRejected])
  const recognized = Boolean(draft.code || draft.name || draft.host || draft.database || activeSource)

  useEffect(() => {
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }, [busy, checks.length, effectiveMissingFields.length, fixes.length, messages, submitted, testPassed])

  const resetConversation = (nextSourceId = '') => {
    const source = sources.find(item => item.id === nextSourceId)
    setModeChosen(true)
    setSourceId(nextSourceId)
    setWorkingSource(source || null)
    setDraft(source ? draftFromSource(source) : blankDraft())
    setMessages([{
      role: 'assistant',
      content: source
        ? `好的，我们来修改“${source.name}”。请直接告诉我需要变更什么，未提到的现有配置会保留。`
        : '好的，我们来新建数据源。请直接描述你已有的信息，例如：“新建 MySQL，地址 db.internal:3306，库名 sales”。不完整也没关系，我会继续追问。',
    }])
    setInstruction('')
    setPassword('')
    setSavedPasswordRejected(false)
    setFile(null)
    setMissingFields([])
    setChecks([])
    setFixes([])
    setAssistantTurnCompleted(false)
    setTestedSource(null)
    setTestPassed(false)
    setSubmitted(false)
  }

  const restart = () => {
    setModeChosen(false)
    setSourceId('')
    setWorkingSource(null)
    setDraft(blankDraft())
    setMessages([welcomeMessage])
    setInstruction('')
    setPassword('')
    setSavedPasswordRejected(false)
    setFile(null)
    setMissingFields([])
    setChecks([])
    setFixes([])
    setAssistantTurnCompleted(false)
    setTestedSource(null)
    setTestPassed(false)
    setSubmitted(false)
  }

  const runTurn = async (
    text: string,
    failure?: DataSourceAITestFailure,
    manageBusy = true,
    sourceIDOverride?: string,
    passwordProvidedOverride?: boolean,
  ) => {
    const userMessage: DataSourceAIMessage = { role: 'user', content: text }
    const history = [...messages, userMessage].slice(-16)
    setMessages(history)
    setModeChosen(true)
    if (manageBusy) setBusy('chat')
    try {
      const result = await dataSourceAPI.aiTurn(sourceIDOverride || activeSource?.id || null, {
        instruction: text,
        history,
        draft,
        passwordProvided: passwordProvidedOverride ?? Boolean(password || (activeSource && !savedPasswordRejected)),
        fileProvided: Boolean(file || activeSource?.fileAssetId),
        ...(failure ? { testFailure: failure } : {}),
      })
      setDraft(result.draft)
      setMissingFields(result.missingFields)
      setChecks(result.suggestedChecks)
      setFixes(result.autoFixes)
      setAssistantTurnCompleted(true)
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
    const prepared = prepareChatInstruction(instruction.trim())
    if (!prepared.text || busy) return
    setInstruction('')
    if (prepared.password) setPassword(prepared.password)
    setTestPassed(false)
    setSubmitted(false)
    void runTurn(prepared.text, undefined, true, undefined, prepared.password ? true : undefined)
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
        code: value.code,
        name: value.name,
        description: value.description,
        visibility: value.visibility,
        sharingScope: value.sharingScope,
        type: 'EXCEL',
        fileAssetId,
      }
      return liveCurrent
        ? dataSourceAPI.update(liveCurrent.id, { ...input, expectedVersion: liveCurrent.version })
        : dataSourceAPI.create(input)
    }
    const input = {
      code: value.code,
      name: value.name,
      description: value.description,
      visibility: value.visibility,
      sharingScope: value.sharingScope,
      type: value.type as Exclude<DataSourceType, 'EXCEL'>,
      host: value.host,
      port: value.port,
      database: value.database,
      oracleConnectMode: value.type === 'ORACLE' ? value.oracleConnectMode : undefined,
      username: value.username,
      password,
    }
    return current
      ? dataSourceAPI.update(current.id, { ...input, expectedVersion: current.version })
      : dataSourceAPI.create(input)
  }

  const saveAndTest = async () => {
    if (busy) return
    if (effectiveMissingFields.length) {
      setMessages(current => [...current, {
        role: 'assistant',
        content: `还缺少：${effectiveMissingFields.map(item => fieldLabels[item] || item).join('、')}。请继续在对话中告诉我，密码或文件请使用下面的安全区域。`,
      }])
      return
    }
    setBusy('test')
    setTestPassed(false)
    setSubmitted(false)
    try {
      let saved = await persistDraft(draft, activeSource || undefined)
      const wasAlreadyListed = sources.some(source => source.id === saved.id)
      setWorkingSource(saved)
      setSourceId(saved.id)
      if (wasAlreadyListed) onSourceChanged(saved)
      if (draft.type === 'EXCEL') setFile(null)
      let retried = false
      while (true) {
        try {
          const test = await dataSourceAPI.test(saved.id)
          const latest = await dataSourceAPI.get(saved.id)
          setWorkingSource(latest)
          if (wasAlreadyListed) onSourceChanged(latest)
          setTestedSource(latest)
          setTestPassed(true)
          setPassword('')
          setSavedPasswordRejected(false)
          setMessages(current => [...current, {
            role: 'assistant',
            content: `连接成功：地址、端口、数据库/服务名和账号认证均已通过${test.serverVersion ? `，服务版本为 ${test.serverVersion}` : ''}${test.latencyMs ? `，总耗时 ${test.latencyMs}ms` : ''}。点击下方“发布”即可完成提交。`,
          }])
          onNotice({ tone: 'success', message: `“${latest.name}”连接成功，等待提交审核` })
          break
        } catch (cause) {
          if (!(cause instanceof DataSourceConnectionTestError)) throw cause
          const passwordRejected = cause.code === 'CONNECTION_AUTH_FAILED'
          if (passwordRejected) {
            setPassword('')
            setSavedPasswordRejected(true)
          }
          const result = await runTurn('连接测试失败。请根据错误诊断原因，并且只修复能够安全确定的参数。', {
            code: cause.code,
            message: cause.message,
          }, false, saved.id, passwordRejected ? false : undefined)
          if (!passwordRejected && !retried && result?.autoRetry) {
            retried = true
            saved = await persistDraft(result.draft, saved)
            setWorkingSource(saved)
            if (wasAlreadyListed) onSourceChanged(saved)
            setMessages(current => [...current, {
              role: 'assistant',
              content: '我已应用可以确定的参数修复，正在自动重试连接。',
            }])
            continue
          }
          setTestedSource(saved)
          setMessages(current => [...current, {
            role: 'assistant',
            content: passwordRejected
              ? `密码校验失败：${cause.message}。当前密码已标记为无效，请在安全区域重新输入；其他连接配置会继续保留。`
              : `仍未连接成功，暂时无法自动解决。具体原因：${cause.message}。当前密码会继续保留并用于下次测试，请核对网络连通性、防火墙白名单、数据库监听地址及账号权限后再试。`,
          }])
          onNotice({ tone: 'error', message: cause.message })
          break
        }
      }
      if (wasAlreadyListed) await onReload()
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '保存或测试数据源失败'
      setMessages(current => [...current, { role: 'assistant', content: `未能完成连接测试：${message}` }])
      onNotice({ tone: 'error', message })
    } finally {
      setBusy('')
    }
  }

  const publish = async () => {
    if (!testedSource || busy) return
    setBusy('publish')
    try {
      let publicationSource = testedSource
      let testRepairAttempted = false
      while (true) {
        try {
          await dataSourceAPI.submitPublicationRequest(
            publicationSource.id,
            '由数据源 AI 助手完成配置与连接测试，提交发布审核',
          )
          const latest = await dataSourceAPI.get(publicationSource.id)
          setWorkingSource(latest)
          setTestedSource(latest)
          setTestPassed(false)
          setSubmitted(true)
          setChecks([])
          setMessages(current => [...current, {
            role: 'assistant',
            content: '发布已完成提交。该数据源当前为“待审批”，审批通过后正式生效。',
          }])
          onSourceChanged(latest)
          await onReload()
          onNotice({ tone: 'success', message: `“${latest.name}”发布已完成提交` })
          return
        } catch (cause) {
          const code = cause instanceof RequestError ? cause.detail.code : 'DATA_SOURCE_REVIEW_FAILED'
          const message = cause instanceof Error ? cause.message : '发布失败'

          if (code === 'DATA_SOURCE_REVIEW_PENDING') {
            const latest = await dataSourceAPI.get(publicationSource.id)
            setWorkingSource(latest)
            setTestedSource(latest)
            setTestPassed(false)
            setSubmitted(true)
            setChecks(publicationFailureChecks(code))
            setMessages(current => [...current, {
              role: 'assistant',
              content: '检测到该数据源已有待审批的发布申请，已同步为提交完成状态，无需重复发布。',
            }])
            onSourceChanged(latest)
            await onReload()
            onNotice({ tone: 'success', message: `“${latest.name}”已在审核队列中` })
            return
          }

          if (!testRepairAttempted && (code === 'DATA_SOURCE_TEST_EXPIRED' || code === 'DATA_SOURCE_TEST_REQUIRED')) {
            testRepairAttempted = true
            setMessages(current => [...current, {
              role: 'assistant',
              content: `发布校验失败（${code}）：${message}。正在重新测试当前配置，测试通过后会自动再次发布。`,
            }])
            try {
              await dataSourceAPI.test(publicationSource.id)
              const latest = await dataSourceAPI.get(publicationSource.id)
              publicationSource = latest
              setWorkingSource(latest)
              setTestedSource(latest)
              setTestPassed(true)
              setChecks([])
              setMessages(current => [...current, {
                role: 'assistant',
                content: '当前配置已重新通过连接测试，正在自动重新发布。',
              }])
              continue
            } catch (testCause) {
              if (testCause instanceof DataSourceConnectionTestError) {
                const passwordRejected = testCause.code === 'CONNECTION_AUTH_FAILED'
                if (passwordRejected) {
                  setPassword('')
                  setSavedPasswordRejected(true)
                }
                setTestPassed(false)
                await runTurn('发布前重新测试连接失败。请根据错误诊断原因，并且只修复能够安全确定的参数。', {
                  code: testCause.code,
                  message: testCause.message,
                }, false, publicationSource.id, passwordRejected ? false : undefined)
                setMessages(current => [...current, {
                  role: 'assistant',
                  content: `发布修复未完成：重新测试失败，原因是 ${testCause.message}。请按诊断结果修复后再次测试并发布。`,
                }])
                onNotice({ tone: 'error', message: testCause.message })
                return
              }
              throw testCause
            }
          }

          if (code === 'DATA_SOURCE_VERSION_CHANGED') {
            const latest = await dataSourceAPI.get(publicationSource.id)
            setWorkingSource(latest)
            setTestedSource(latest)
            setDraft(draftFromSource(latest))
            setTestPassed(false)
            setSubmitted(false)
            setChecks(publicationFailureChecks(code))
            setMessages(current => [...current, {
              role: 'assistant',
              content: `发布失败（${code}）：${message}。配置已在测试后发生变化，我已加载最新版本；请确认后重新测试，避免发布未经确认的配置。`,
            }])
            onSourceChanged(latest)
            onNotice({ tone: 'error', message: '配置已变化，请重新测试后发布' })
            return
          }

          setChecks(publicationFailureChecks(code))
          setMessages(current => [...current, {
            role: 'assistant',
            content: `发布失败（${code}）：${message}。已根据错误保留当前配置并给出处理建议，请修复后再次点击发布。`,
          }])
          onNotice({ tone: 'error', message })
          return
        }
      }
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '提交发布申请失败'
      setChecks(publicationFailureChecks('DATA_SOURCE_REVIEW_FAILED'))
      setMessages(current => [...current, { role: 'assistant', content: `发布修复失败：${message}。当前配置已保留，请刷新状态后重试。` }])
      onNotice({ tone: 'error', message })
    } finally {
      setBusy('')
    }
  }

  return <aside className={`data-source-ai${open ? ' open' : ''}`} aria-label="数据源 AI 助手">
    <div className="data-source-ai-launcher-wrap" onMouseEnter={() => setOpen(true)}>
      <span className="data-source-ai-launcher-tip">用 AI 配置数据源</span>
      <button className="data-source-ai-launcher" type="button" onClick={() => setOpen(current => !current)} aria-expanded={open} aria-label="打开数据源 AI 助手">
        <Sparkle className="launcher-sparkle" size={17} weight="fill" />
        <ChatCircleDots size={25} weight="fill" />
        <span aria-hidden="true" />
      </button>
    </div>
    {open && <section className="data-source-ai-panel" role="dialog" aria-label="数据源 AI 配置对话">
      <header>
        <div className="data-source-ai-agent"><span><Sparkle size={18} weight="fill" /></span><div><small>AI DATA COPILOT</small><strong>数据源助手</strong></div></div>
        <div className="data-source-ai-header-actions">
          <button type="button" disabled={Boolean(busy)} aria-label="重新开始对话" title="重新开始" onClick={restart}><ArrowCounterClockwise size={17} /></button>
          <button type="button" aria-label="关闭数据源 AI 助手" onClick={() => setOpen(false)}><X size={18} /></button>
        </div>
      </header>
      <div className="data-source-ai-context"><span className="online-dot" />{modeChosen ? activeSource ? `正在配置：${activeSource.name}` : '正在新建数据源' : '在线 · 等待你的指令'}</div>
      <div className="data-source-ai-log" ref={logRef} aria-live="polite">
        {modeChosen && recognized && <div className="data-source-ai-inline-card configuration pinned">
          <header><span><Sparkle size={14} weight="fill" />当前识别配置</span><small>{activeSource ? '修改' : '新建'}</small></header>
          <dl>
            <div><dt>名称</dt><dd>{draft.name || '待补充'}</dd></div>
            <div><dt>类型</dt><dd>{draft.type}</dd></div>
            {draft.type !== 'EXCEL' && <><div><dt>地址</dt><dd>{draft.host ? `${draft.host}:${draft.port || '—'}` : '待补充'}</dd></div><div><dt>{draft.type === 'ORACLE' ? (draft.oracleConnectMode === 'SID' ? 'Oracle SID' : 'Oracle Service Name') : '数据库'}</dt><dd>{draft.database || '待补充'}</dd></div></>}
          </dl>
          {effectiveMissingFields.length > 0 && <p>还需要：{effectiveMissingFields.map(item => fieldLabels[item] || item).join('、')}</p>}
        </div>}

        {messages.map((message, index) => <div className={`data-source-ai-message ${message.role}`} key={`${message.role}-${index}`}>
          {message.role === 'assistant' && <span className="data-source-ai-avatar"><Sparkle size={13} weight="fill" /></span>}
          <div><small>{message.role === 'assistant' ? 'AI 助手' : '你'}</small><p>{message.content}</p></div>
        </div>)}

        {!modeChosen && <div className="data-source-ai-choices">
          <button type="button" onClick={() => resetConversation('')}><Sparkle size={15} weight="fill" /><span><strong>新建数据源</strong><small>从一段描述开始</small></span></button>
          {sources.map(source => <button type="button" key={source.id} onClick={() => resetConversation(source.id)}><PlugsConnected size={15} /><span><strong>修改 {source.name}</strong><small>{source.type} · {source.code}</small></span></button>)}
        </div>}

        {modeChosen && assistantTurnCompleted && (draft.type === 'EXCEL'
          ? <label className="data-source-ai-inline-card secret">
            <span><FileArrowUp size={16} />安全上传数据文件</span>
            <input type="file" accept=".xlsx,.xls,.csv" disabled={Boolean(busy)} onChange={event => { setFile(event.target.files?.[0] || null); setTestPassed(false) }} />
            <small>{file?.name || activeSource?.fileAssetId ? file?.name || '沿用已上传文件' : '文件内容不会发送给配置模型'}</small>
          </label>
          : <label className="data-source-ai-inline-card secret">
            <span><PlugsConnected size={16} />安全填写数据库密码</span>
            <input type="password" autoComplete="new-password" disabled={Boolean(busy)} value={password} onChange={event => { setPassword(event.target.value); setTestPassed(false) }} placeholder={savedPasswordRejected ? '原密码不正确，请重新输入' : activeSource ? '留空则沿用已保存密码' : '密码不会进入对话'} />
            <small>{savedPasswordRejected ? '原密码认证失败；重新输入后将替换并继续测试' : '凭据会持续用于后续测试，只有认证失败时才要求重新输入'}</small>
          </label>)}

        {fixes.length > 0 && <div className="data-source-ai-inline-card diagnostic success"><strong><CheckCircle size={15} weight="fill" />已安全修复</strong>{fixes.map(item => <span key={item}>· {item}</span>)}</div>}
        {checks.length > 0 && <div className="data-source-ai-inline-card diagnostic"><strong>建议检查</strong>{checks.map(item => <span key={item}>· {item}</span>)}</div>}

        {modeChosen && recognized && effectiveMissingFields.length === 0 && !testPassed && !submitted && <div className="data-source-ai-inline-card action">
          <span className="action-icon"><PlugsConnected size={20} weight="fill" /></span>
          <div><strong>信息已齐全</strong><p>我会保存当前配置并测试连接；若失败，将自动诊断并尝试一次安全修复。</p><button type="button" disabled={Boolean(busy)} onClick={() => void saveAndTest()}>{busy === 'test' ? <><SpinnerGap className="spin" size={15} />正在连接…</> : <><PlugsConnected size={15} />测试连接</>}</button></div>
        </div>}

        {busy === 'test' && <div className="data-source-ai-inline-card diagnostic">
          <strong><SpinnerGap className="spin" size={15} />正在按顺序执行分层连接检测</strong>
          <span>① 地址解析与 Ping</span><span>② TCP 端口</span><span>③ 数据库名 / Oracle Service Name 或 SID</span><span>④ 用户名和密码认证</span>
        </div>}

        {testPassed && testedSource && <div className="data-source-ai-inline-card action success">
          <span className="action-icon"><CheckCircle size={21} weight="fill" /></span>
          <div><strong>连接成功</strong><p>“{testedSource.name}”已通过连接校验。点击发布即可完成提交；审批通过前不会生效。</p><button type="button" disabled={Boolean(busy)} onClick={() => void publish()}>{busy === 'publish' ? <><SpinnerGap className="spin" size={15} />发布中…</> : <><PaperPlaneRight size={15} weight="fill" />发布</>}</button></div>
        </div>}

        {submitted && <div className="data-source-ai-inline-card action pending">
          <span className="action-icon"><CheckCircle size={21} weight="fill" /></span>
          <div><strong>已进入审核</strong><p>数据源列表已生成“待审批”记录。你可以关闭助手，等待领域管理员处理。</p></div>
        </div>}
        {busy === 'chat' && <div className="data-source-ai-thinking"><span /><span /><span />正在理解并整理参数</div>}
      </div>
      <form className="data-source-ai-composer" onSubmit={submitMessage}>
        <textarea rows={2} value={instruction} disabled={Boolean(busy)} onChange={event => setInstruction(event.target.value)} placeholder="直接描述需求或继续补充信息…" />
        <button type="submit" disabled={Boolean(busy) || !instruction.trim()} aria-label="发送消息"><PaperPlaneRight size={19} weight="fill" /></button>
        <small>Enter 换行 · 粘贴的密码会自动转入安全区域，不会发送给模型</small>
      </form>
    </section>}
  </aside>
}
