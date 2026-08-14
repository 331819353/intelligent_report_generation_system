import {
  ArrowCounterClockwise,
  CaretDown,
  CheckCircle,
  ChatCircleDots,
  FileArrowUp,
  Key,
  PaperPlaneRight,
  PlugsConnected,
  ShieldCheck,
  Sparkle,
  SpinnerGap,
  X,
} from '@phosphor-icons/react'
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AppButton } from './AppButton'
import { RequestError } from '../lib/api'
import { prepareDataSourceChatInstruction } from '../lib/data-source-ai-input'
import { md5Hex } from '../lib/md5'
import {
  dataSourceAPI,
  defaultDatabasePort,
  DataSourceConnectionTestError,
  type DataSourceAIDraft,
  type DataSourceAIMessage,
  type DataSourceAITestFailure,
  type ConnectionTestJob,
  type ConnectionTestStage,
  type DataSourceRecord,
  type DataSourceType,
  type ExcelFileAsset,
  type ExcelWorkbookInspection,
  type ExcelDataSourceInput,
} from '../lib/data-sources'

type Notice = { tone: 'success' | 'error'; message: string }
type PendingInstruction = { text: string; passwordProvided: boolean }
type Props = {
  sources: DataSourceRecord[]
  onSourceChanged: (source: DataSourceRecord) => void
  onReload: () => Promise<unknown>
  onNotice: (notice: Notice) => void
  open?: boolean
  onOpenChange?: (open: boolean) => void
  hideLauncher?: boolean
  previewMode?: 'success'
}

const blankDraft = (): DataSourceAIDraft => ({
  code: '', name: '', description: '', type: '', host: '', port: 0,
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
  port: Number(textConfig(source, 'port')) || defaultDatabasePort(source.type) || 3306,
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

const connectionTestStages: { key: Exclude<ConnectionTestStage, 'QUEUED'>; label: string }[] = [
  { key: 'ADDRESS', label: '地址解析与 Ping' },
  { key: 'PORT', label: 'TCP 端口' },
  { key: 'DATABASE', label: '数据库名 / Oracle Service Name 或 SID' },
  { key: 'AUTHENTICATION', label: '用户名和密码认证' },
]

const connectionTestFailureStage = (code = ''): Exclude<ConnectionTestStage, 'QUEUED'> => {
  if (['ADDRESS_RESOLUTION_FAILED', 'ADDRESS_UNREACHABLE', 'CONNECTION_DNS_FAILED', 'NETWORK_UNREACHABLE'].includes(code)) return 'ADDRESS'
  if (['PORT_REFUSED', 'PORT_TIMEOUT', 'CONNECTION_REFUSED'].includes(code)) return 'PORT'
  if (code === 'CONNECTION_AUTH_FAILED') return 'AUTHENTICATION'
  return 'DATABASE'
}

type ConnectionTestStageState = 'pending' | 'running' | 'passed' | 'failed'

const connectionTestStageState = (
  job: ConnectionTestJob | null,
  stage: Exclude<ConnectionTestStage, 'QUEUED'>,
): ConnectionTestStageState => {
  if (!job || job.status === 'QUEUED') return 'pending'
  if (job.status === 'SUCCEEDED') return 'passed'
  const index = connectionTestStages.findIndex(item => item.key === stage)
  if (job.status === 'FAILED' || job.status === 'CANCELLED') {
    const failedIndex = connectionTestStages.findIndex(item => item.key === connectionTestFailureStage(job.errorCode))
    if (index < failedIndex) return 'passed'
    return index === failedIndex ? 'failed' : 'pending'
  }
  const currentIndex = connectionTestStages.findIndex(item => item.key === job.stage)
  if (currentIndex < 0) return 'pending'
  if (index < currentIndex) return 'passed'
  return index === currentIndex ? 'running' : 'pending'
}

const dataSourceCodePattern = /^[A-Za-z][A-Za-z0-9_]{0,127}$/

const excelAttachmentIdentity = (
  filename: string,
  checksum: string,
  occupiedCodes: Set<string>,
) => {
  const extensionMatch = filename.match(/\.([^.]+)$/)
  const extension = extensionMatch?.[1]?.toLocaleLowerCase() || 'file'
  const stem = filename.slice(0, extensionMatch?.index ?? filename.length).trim() || '文件数据源'
  const normalizedStem = stem.normalize('NFKC').toLocaleLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '_').replace(/^_+|_+$/g, '') || 'file'
  const readableCode = `${normalizedStem}_${extension}`
  let code = dataSourceCodePattern.test(readableCode) ? readableCode : `file_${md5Hex(readableCode)}`
  if (occupiedCodes.has(code.toLocaleLowerCase())) {
    const suffix = (checksum || md5Hex(`${filename}:${readableCode}`)).slice(0, 10).toLocaleLowerCase()
    code = `${code.slice(0, Math.max(1, 127 - suffix.length))}_${suffix}`
  }
  return { name: stem, code }
}

const excelInspectionDescription = (inspection: ExcelWorkbookInspection) => {
  const columns = inspection.sheets.reduce((total, sheet) => total + sheet.columns.length, 0)
  const sheets = inspection.sheets.slice(0, 6).map(sheet => `${sheet.name}（${sheet.columns.length} 列）`).join('、')
  return `AI 已解析 ${inspection.sheets.length} 个工作表、${columns} 个字段${sheets ? `：${sheets}` : ''}`
}

const publicationFailureChecks = (code: string) => {
  switch (code) {
    case 'DATA_SOURCE_TEST_PENDING':
      return ['等待当前连接测试完成后再次点击发布']
    case 'DATA_SOURCE_TEST_REQUIRED':
      return ['测试当前配置', '测试通过后自动重新提交发布']
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

export function DataSourceAIAssistant({ sources, onSourceChanged, onReload, onNotice, open: controlledOpen, onOpenChange, hideLauncher = false, previewMode }: Props) {
  const previewSource = previewMode === 'success' ? sources.find(source => source.type === 'ORACLE') || null : null
  const [internalOpen, setInternalOpen] = useState(false)
  const open = controlledOpen ?? internalOpen
  const setOpen = (next: boolean | ((current: boolean) => boolean)) => {
    const value = typeof next === 'function' ? next(open) : next
    if (controlledOpen === undefined) setInternalOpen(value)
    onOpenChange?.(value)
  }
  const [modeChosen, setModeChosen] = useState(Boolean(previewSource))
  const [sourceId, setSourceId] = useState(previewSource?.id || '')
  const [workingSource, setWorkingSource] = useState<DataSourceRecord | null>(previewSource)
  const [draft, setDraft] = useState<DataSourceAIDraft>(() => previewSource ? draftFromSource(previewSource) : blankDraft())
  const [messages, setMessages] = useState<DataSourceAIMessage[]>(previewSource ? [
    { role: 'assistant', content: `已识别“${previewSource.name}”的 Oracle 连接参数，名称、地址、端口、服务名和账号信息均已齐全。` },
    { role: 'assistant', content: '现有凭据会在安全通道中复用；连接测试已通过，可以提交发布审核。' },
  ] : [welcomeMessage])
  const [instruction, setInstruction] = useState('')
  const [pendingInstructions, setPendingInstructions] = useState<PendingInstruction[]>([])
  const [password, setPassword] = useState('')
  const [savedPasswordRejected, setSavedPasswordRejected] = useState(false)
  const [file, setFile] = useState<File | null>(null)
  const [uploadedFileAsset, setUploadedFileAsset] = useState<ExcelFileAsset | null>(null)
  const [fileInspection, setFileInspection] = useState<ExcelWorkbookInspection | null>(null)
  const [missingFields, setMissingFields] = useState<string[]>([])
  const [checks, setChecks] = useState<string[]>([])
  const [fixes, setFixes] = useState<string[]>(previewSource ? ['已规范为连接器可访问的数据库地址', '已根据类型与连接参数确认数据源标识'] : [])
  const [testFailureCode, setTestFailureCode] = useState('')
  const [connectionTestJob, setConnectionTestJob] = useState<ConnectionTestJob | null>(null)
  const [assistantTurnCompleted, setAssistantTurnCompleted] = useState(Boolean(previewSource))
  const [busy, setBusy] = useState<'chat' | 'file' | 'test' | 'publish' | ''>('')
  const [testedSource, setTestedSource] = useState<DataSourceRecord | null>(previewSource)
  const [testPassed, setTestPassed] = useState(Boolean(previewSource))
  const [submitted, setSubmitted] = useState(false)
  const logRef = useRef<HTMLDivElement | null>(null)
  const attachmentInputRef = useRef<HTMLInputElement | null>(null)
  const connectionTestAbortRef = useRef<AbortController | null>(null)
  const connectionTestRevisionRef = useRef(0)
  const listedSource = useMemo(() => sources.find(source => source.id === sourceId), [sourceId, sources])
  const activeSource = workingSource || listedSource
  const passwordFailure = testFailureCode === 'CONNECTION_AUTH_FAILED'
  const passwordStatus = savedPasswordRejected
    ? '认证失败，待重新输入'
    : password
      ? '已安全填写'
      : activeSource
        ? '沿用已保存密码'
        : '待填写'
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
      file: Boolean(uploadedFileAsset || file || activeSource?.fileAssetId),
    }
    const required = ['type']
    if (draft.type === 'EXCEL') required.push('code', 'name', 'file')
    else required.push('host', 'port', 'database', 'username', 'password')
    return [...new Set([...missingFields, ...required])].filter(field => !satisfied[field])
  }, [activeSource, draft, file, missingFields, password, savedPasswordRejected, uploadedFileAsset])
  const recognized = Boolean(draft.code || draft.name || draft.host || draft.database || draft.username || activeSource || fileInspection)
  const workflowSteps = [
    { label: '识别配置', hint: '提取连接参数', icon: <Sparkle size={15} weight="fill" />, complete: recognized },
    { label: '补齐信息', hint: draft.type === 'EXCEL' ? '确认文件与名称' : '确认账号与凭据', icon: <Key size={15} weight="duotone" />, complete: recognized && effectiveMissingFields.length === 0 },
    { label: '连接验证', hint: draft.type === 'EXCEL' ? '检查文件可用性' : '分层测试连接', icon: <ShieldCheck size={15} weight="duotone" />, complete: testPassed },
    { label: '提交发布', hint: '进入审批流程', icon: <PaperPlaneRight size={15} weight="fill" />, complete: submitted },
  ]
  const currentWorkflowStep = Math.max(0, workflowSteps.findIndex(step => !step.complete))
  const workflowStatus = submitted
    ? '已提交审核'
    : testPassed
      ? '验证通过，等待发布'
      : busy === 'test'
        ? '正在验证连接'
        : recognized && effectiveMissingFields.length === 0
          ? '信息完整，可以测试'
          : recognized
            ? `待补充 ${effectiveMissingFields.length} 项`
            : modeChosen
              ? '等待识别配置'
              : '等待开始'
  const draftSummary = draft.type === 'EXCEL'
    ? [draft.type, uploadedFileAsset?.filename || file?.name].filter(Boolean).join(' · ')
    : [draft.type, draft.host ? `${draft.host}${draft.port ? `:${draft.port}` : ''}` : ''].filter(Boolean).join(' · ')

  useEffect(() => {
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }, [busy, checks.length, effectiveMissingFields.length, fixes.length, messages, pendingInstructions, submitted, testPassed])

  useEffect(() => () => connectionTestAbortRef.current?.abort(), [])

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
    setPendingInstructions([])
    setPassword('')
    setSavedPasswordRejected(false)
    setFile(null)
    setUploadedFileAsset(null)
    setFileInspection(null)
    setMissingFields([])
    setChecks([])
    setFixes([])
    setTestFailureCode('')
    setConnectionTestJob(null)
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
    setPendingInstructions([])
    setPassword('')
    setSavedPasswordRejected(false)
    setFile(null)
    setUploadedFileAsset(null)
    setFileInspection(null)
    setMissingFields([])
    setChecks([])
    setFixes([])
    setTestFailureCode('')
    setConnectionTestJob(null)
    setAssistantTurnCompleted(false)
    setTestedSource(null)
    setTestPassed(false)
    setSubmitted(false)
  }

  const runTurn = useCallback(async (
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
        fileProvided: Boolean(uploadedFileAsset || file || activeSource?.fileAssetId),
        ...(failure ? { testFailure: failure } : {}),
      })
      setDraft(result.draft)
      setMissingFields(result.missingFields)
      setChecks(result.suggestedChecks)
      setFixes(result.autoFixes)
      if (!failure) setTestFailureCode('')
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
  }, [activeSource, draft, file, messages, password, savedPasswordRejected, uploadedFileAsset])

  const submitMessage = (event: FormEvent) => {
    event.preventDefault()
    const prepared = prepareDataSourceChatInstruction(instruction.trim())
    if (!prepared.text || busy === 'publish') return
    setInstruction('')
    if (prepared.password) setPassword(prepared.password)
    setTestPassed(false)
    setSubmitted(false)
    if (busy || pendingInstructions.length > 0) {
      setPendingInstructions(current => [...current, {
        text: prepared.text,
        passwordProvided: Boolean(prepared.password),
      }])
      if (busy === 'test') {
        connectionTestRevisionRef.current += 1
        connectionTestAbortRef.current?.abort()
      }
      return
    }
    void runTurn(prepared.text, undefined, true, undefined, prepared.password ? true : undefined)
  }

  useEffect(() => {
    if (busy || pendingInstructions.length === 0) return
    const [next, ...remaining] = pendingInstructions
    setPendingInstructions(remaining)
    setTestPassed(false)
    setSubmitted(false)
    setConnectionTestJob(null)
    void runTurn(next.text, undefined, true, undefined, next.passwordProvided ? true : undefined)
  }, [busy, pendingInstructions, runTurn])

  const persistDraft = async (value: DataSourceAIDraft, current?: DataSourceRecord) => {
    if (!value.type) throw new Error('请先确认数据库类型')
    if (value.type === 'EXCEL') {
      let liveCurrent = current
      let fileAssetId = uploadedFileAsset?.id || liveCurrent?.fileAssetId || ''
      if (file && !uploadedFileAsset) {
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

  const attachAndCreateExcelSource = async (selectedFile: File) => {
    if (busy) return
    const editingID = activeSource?.type === 'EXCEL' ? activeSource.id : ''
    setModeChosen(true)
    setBusy('file')
    setFile(selectedFile)
    setUploadedFileAsset(null)
    setFileInspection(null)
    setTestPassed(false)
    setSubmitted(false)
    setChecks([])
    setFixes([])
    setTestFailureCode('')
    setConnectionTestJob(null)
    setAssistantTurnCompleted(true)
    if (!editingID) {
      setSourceId('')
      setWorkingSource(null)
    }
    setMessages(current => [...current, {
      role: 'user',
      content: `已添加 Excel 附件：${selectedFile.name}`,
    } as DataSourceAIMessage].slice(-18))
    try {
      const liveCurrent = editingID ? await dataSourceAPI.get(editingID) : null
      const asset = liveCurrent?.fileAssetId
        ? await dataSourceAPI.uploadExcelVersion(liveCurrent.fileAssetId, selectedFile)
        : await dataSourceAPI.uploadExcel(selectedFile)
      setUploadedFileAsset(asset)

      const inspection = await dataSourceAPI.inspectExcelAsset(asset.id)
      setFileInspection(inspection)
      const occupiedCodes = new Set(
        sources
          .filter(source => source.id !== liveCurrent?.id)
          .map(source => source.code.toLocaleLowerCase()),
      )
      const identity = liveCurrent
        ? { name: liveCurrent.name, code: liveCurrent.code }
        : excelAttachmentIdentity(selectedFile.name, asset.sha256, occupiedCodes)
      const summary = excelInspectionDescription(inspection)
      const nextDraft: DataSourceAIDraft = {
        ...blankDraft(),
        code: identity.code,
        name: identity.name,
        description: liveCurrent?.description || summary,
        type: 'EXCEL',
        visibility: liveCurrent?.visibility || draft.visibility || 'PRIVATE',
        sharingScope: liveCurrent?.sharingScope || draft.sharingScope || 'PRIVATE',
      }
      setDraft(nextDraft)
      setMissingFields([])

      const input: ExcelDataSourceInput = {
        code: nextDraft.code,
        name: nextDraft.name,
        description: nextDraft.description,
        visibility: nextDraft.visibility,
        sharingScope: nextDraft.sharingScope,
        type: 'EXCEL',
        fileAssetId: asset.id,
      }
      const saved = liveCurrent
        ? await dataSourceAPI.update(liveCurrent.id, { ...input, expectedVersion: liveCurrent.version })
        : await dataSourceAPI.create(input)
      setWorkingSource(saved)
      setSourceId(saved.id)
      onSourceChanged(saved)

      const test = await dataSourceAPI.test(saved.id)
      const latest = await dataSourceAPI.get(saved.id)
      setWorkingSource(latest)
      setTestedSource(latest)
      setTestPassed(true)
      setTestFailureCode('')
      setFile(null)
      onSourceChanged(latest)
      await onReload()
      setMessages(current => [...current, {
        role: 'assistant',
        content: `${summary}。附件已保存为数据源“${latest.name}”，文件可用性测试已通过${test.latencyMs ? `，耗时 ${test.latencyMs}ms` : ''}；确认解析结果后可直接点击“发布”。`,
      } as DataSourceAIMessage].slice(-18))
      onNotice({ tone: 'success', message: `“${latest.name}”已从附件解析创建并通过测试` })
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : 'Excel 附件解析或数据源创建失败'
      setTestFailureCode(cause instanceof DataSourceConnectionTestError ? cause.code : 'EXCEL_ATTACHMENT_FAILED')
      setChecks(current => current.length > 0 ? current : ['确认文件格式、表头和工作表结构有效', '修正附件后重新上传'])
      setMessages(current => [...current, {
        role: 'assistant',
        content: `附件未能完成解析创建：${message}。已识别的信息会保留，你可以修正文件后重新上传。`,
      } as DataSourceAIMessage].slice(-18))
      onNotice({ tone: 'error', message })
    } finally {
      setBusy('')
      if (attachmentInputRef.current) attachmentInputRef.current.value = ''
    }
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
    setTestFailureCode('')
    setConnectionTestJob(null)
    const testRevision = connectionTestRevisionRef.current
    const testController = new AbortController()
    connectionTestAbortRef.current = testController
    try {
      let saved = await persistDraft(draft, activeSource || undefined)
      const wasAlreadyListed = sources.some(source => source.id === saved.id)
      setWorkingSource(saved)
      setSourceId(saved.id)
      if (wasAlreadyListed) onSourceChanged(saved)
      if (draft.type === 'EXCEL') setFile(null)
      if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current) return
      let retried = false
      while (true) {
        try {
          const test = await dataSourceAPI.test(
            saved.id,
            draft.type === 'EXCEL'
              ? { signal: testController.signal }
              : { signal: testController.signal, onProgress: setConnectionTestJob },
          )
          if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current) return
          const latest = await dataSourceAPI.get(saved.id)
          if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current) return
          setWorkingSource(latest)
          if (wasAlreadyListed) onSourceChanged(latest)
          setTestedSource(latest)
          setTestPassed(true)
          setTestFailureCode('')
          setPassword('')
          setSavedPasswordRejected(false)
          setMessages(current => [...current, {
            role: 'assistant',
            content: draft.type === 'EXCEL'
              ? `附件文件版本、校验和与对象可读性均已通过${test.serverVersion ? `，格式为 ${test.serverVersion}` : ''}${test.latencyMs ? `，总耗时 ${test.latencyMs}ms` : ''}。点击下方“发布”即可完成提交。`
              : `连接成功：地址、端口、数据库/服务名和账号认证均已通过${test.serverVersion ? `，服务版本为 ${test.serverVersion}` : ''}${test.latencyMs ? `，总耗时 ${test.latencyMs}ms` : ''}。点击下方“发布”即可完成提交。`,
          }])
          onNotice({ tone: 'success', message: `“${latest.name}”连接成功，等待提交审核` })
          break
        } catch (cause) {
          if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current) return
          if (!(cause instanceof DataSourceConnectionTestError)) throw cause
          const passwordRejected = cause.code === 'CONNECTION_AUTH_FAILED'
          setTestFailureCode(cause.code)
          if (passwordRejected) {
            setPassword('')
            setSavedPasswordRejected(true)
          }
          const result = await runTurn('连接测试失败。请根据错误诊断原因，并且只修复能够安全确定的参数。', {
            code: cause.code,
            message: cause.message,
          }, false, saved.id, passwordRejected ? false : undefined)
          if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current) return
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
      if (testController.signal.aborted || testRevision !== connectionTestRevisionRef.current || (cause instanceof Error && cause.name === 'AbortError')) return
      const message = cause instanceof Error ? cause.message : '保存或测试数据源失败'
      setTestFailureCode('DATA_SOURCE_TEST_FAILED')
      setChecks(current => current.length > 0 ? current : ['根据错误信息检查当前配置', '修正配置后通过对话重新确认'])
      setMessages(current => [...current, { role: 'assistant', content: `未能完成连接测试：${message}` }])
      onNotice({ tone: 'error', message })
    } finally {
      if (connectionTestAbortRef.current === testController) connectionTestAbortRef.current = null
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

          if (!testRepairAttempted && code === 'DATA_SOURCE_TEST_REQUIRED') {
            testRepairAttempted = true
            setMessages(current => [...current, {
              role: 'assistant',
              content: `发布校验失败（${code}）：${message}。正在测试当前配置，测试通过后会自动再次发布。`,
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

  return <aside className={`data-source-ai${open ? ' open' : ''}${hideLauncher ? ' without-launcher' : ''}`} aria-label="数据源 AI 助手">
    {!hideLauncher && <div className="data-source-ai-launcher-wrap" onMouseEnter={() => setOpen(true)}>
      <span className="data-source-ai-launcher-tip">用 AI 配置数据源</span>
      <AppButton variant="primary" className="data-source-ai-launcher" type="button" onClick={() => setOpen(current => !current)} aria-expanded={open} aria-label="打开数据源 AI 助手">
        <Sparkle className="launcher-sparkle" size={17} weight="fill" />
        <ChatCircleDots size={25} weight="fill" />
        <span aria-hidden="true" />
      </AppButton>
    </div>}
    {open && <section className="data-source-ai-panel" role="dialog" aria-label="数据源 AI 配置对话">
      <header>
        <div className="data-source-ai-agent"><span><Sparkle size={18} weight="fill" /></span><div><small>AI CONFIGURATION</small><strong>数据源智能配置</strong></div></div>
        <div className="data-source-ai-header-actions">
          <AppButton text circle type="button" disabled={Boolean(busy)} aria-label="重新开始对话" title="重新开始" onClick={restart}><ArrowCounterClockwise size={17} /></AppButton>
          <AppButton text circle type="button" aria-label="关闭数据源 AI 助手" onClick={() => setOpen(false)}><X size={18} /></AppButton>
        </div>
      </header>
      <div className="data-source-ai-context">
        <span className="data-source-ai-session"><i className="online-dot" /><span title={activeSource?.name}>{modeChosen ? activeSource ? `正在配置：${activeSource.name}` : '正在新建数据源' : '在线 · 等待你的指令'}</span></span>
        <em className={submitted ? 'is-success' : testPassed ? 'is-ready' : ''}>{workflowStatus}</em>
      </div>
      <ol className="data-source-ai-workflow" aria-label="智能配置流程">
        {workflowSteps.map((step, index) => <li className={`${step.complete ? 'is-complete' : ''}${!step.complete && index === currentWorkflowStep ? ' is-current' : ''}`} key={step.label}>
          <span>{step.complete ? <CheckCircle size={16} weight="fill" /> : step.icon}</span>
          <div><strong>{step.label}</strong><small>{step.hint}</small></div>
        </li>)}
      </ol>
      <div className="data-source-ai-conversation">
        {modeChosen && recognized && <details className="data-source-ai-inline-card configuration pinned">
          <summary>
            <span><Sparkle size={14} weight="fill" /><span><strong>当前识别配置</strong><small title={draftSummary}>{draftSummary || '等待识别参数'}</small></span></span>
            <em>{effectiveMissingFields.length > 0 ? `待补 ${effectiveMissingFields.length} 项` : activeSource ? '修改模式' : '新建模式'}</em>
            <CaretDown size={15} weight="bold" />
          </summary>
          <dl>
            <div className="wide"><dt>名称</dt><dd title={draft.name || undefined}>{draft.name || '待补充'}</dd></div>
            <div className="wide"><dt>编码</dt><dd title={draft.code || undefined}>{draft.code || '待生成'}</dd></div>
            <div><dt>类型</dt><dd>{draft.type || '待识别'}</dd></div>
            <div><dt>可见性 / 范围</dt><dd>{draft.visibility || 'PRIVATE'} / {draft.sharingScope || 'PRIVATE'}</dd></div>
            {draft.type !== 'EXCEL' && <>
              <div className="wide"><dt>Host</dt><dd title={draft.host || undefined}>{draft.host || '待补充'}</dd></div>
              <div><dt>端口</dt><dd>{draft.port || '待补充'}</dd></div>
              <div><dt>{draft.type === 'ORACLE' ? (draft.oracleConnectMode === 'SID' ? 'Oracle SID' : 'Oracle Service Name') : '数据库'}</dt><dd title={draft.database || undefined}>{draft.database || '待补充'}</dd></div>
              {draft.type === 'ORACLE' && <div><dt>Oracle 连接模式</dt><dd>{draft.oracleConnectMode === 'SID' ? 'SID' : 'SERVICE_NAME'}</dd></div>}
              <div className={draft.type === 'ORACLE' ? '' : 'wide'}><dt>用户名</dt><dd title={draft.username || undefined}>{draft.username || '待补充'}</dd></div>
              <div className="wide"><dt>密码状态</dt><dd className="secret-status">{passwordStatus}</dd></div>
            </>}
            {draft.type === 'EXCEL' && <><div><dt>附件</dt><dd>{uploadedFileAsset?.filename || activeSource?.name || file?.name || '待上传'}</dd></div><div><dt>解析结果</dt><dd>{fileInspection ? `${fileInspection.sheets.length} 个工作表` : '等待解析'}</dd></div></>}
          </dl>
          {effectiveMissingFields.length > 0 && <p>还需要：{effectiveMissingFields.map(item => fieldLabels[item] || item).join('、')}</p>}
        </details>}
        <div className="data-source-ai-log" ref={logRef} aria-live="polite">
        {messages.map((message, index) => <div className={`data-source-ai-message ${message.role}`} key={`${message.role}-${index}`}>
          {message.role === 'assistant' && <span className="data-source-ai-avatar"><Sparkle size={13} weight="fill" /></span>}
          <div><small>{message.role === 'assistant' ? 'AI 助手' : '你'}</small><p>{message.content}</p></div>
        </div>)}
        {pendingInstructions.map((item, index) => <div className="data-source-ai-message user queued" key={`queued-${index}`}>
          <div><small>你 · 已收到，待优先处理</small><p>{item.text}</p></div>
        </div>)}

        {!modeChosen && <div className="data-source-ai-choices">
          <AppButton type="button" onClick={() => resetConversation('')}><Sparkle size={15} weight="fill" /><span><strong>新建数据源</strong><small>从一段描述开始</small></span></AppButton>
          {sources.map(source => <AppButton type="button" key={source.id} onClick={() => resetConversation(source.id)}><PlugsConnected size={15} /><span><strong title={source.name}>修改 {source.name}</strong><small title={`${source.type} · ${source.code}`}>{source.type} · {source.code}</small></span></AppButton>)}
        </div>}

        {modeChosen && assistantTurnCompleted && (!testFailureCode || passwordFailure) && (draft.type === 'EXCEL'
          ? <label className="data-source-ai-inline-card secret">
            <span><FileArrowUp size={16} />安全上传数据文件</span>
            <input type="file" accept=".xlsx,.xls,.csv" disabled={Boolean(busy)} onChange={event => { const selected = event.target.files?.[0]; if (selected) void attachAndCreateExcelSource(selected) }} />
            <small>{file?.name || uploadedFileAsset?.filename || activeSource?.fileAssetId ? file?.name || uploadedFileAsset?.filename || '沿用已上传文件' : '附件在服务端安全解析，原始单元格不会发送给配置模型'}</small>
          </label>
          : <label className="data-source-ai-inline-card secret">
            <span><PlugsConnected size={16} />安全填写数据库密码</span>
            <input type="password" autoComplete="new-password" disabled={Boolean(busy)} value={password} onChange={event => {
              const value = event.target.value
              setPassword(value)
              setTestPassed(false)
              if (passwordFailure && value) {
                setSavedPasswordRejected(false)
                setTestFailureCode('')
                setChecks([])
                setFixes([])
              }
            }} placeholder={savedPasswordRejected ? '原密码不正确，请重新输入' : activeSource ? '留空则沿用已保存密码' : '密码不会进入对话'} />
            <small>{savedPasswordRejected ? '原密码认证失败；重新输入后将替换并继续测试' : '凭据会持续用于后续测试，只有认证失败时才要求重新输入'}</small>
          </label>)}

        {!testFailureCode && fixes.length > 0 && <div className="data-source-ai-inline-card diagnostic success"><strong><CheckCircle size={15} weight="fill" />已安全修复</strong>{fixes.map(item => <span key={item}>· {item}</span>)}</div>}
        {!testFailureCode && fileInspection && <div className="data-source-ai-inline-card diagnostic success excel-inspection">
          <strong><CheckCircle size={15} weight="fill" />附件结构已解析</strong>
          {fileInspection.sheets.map(sheet => <span key={sheet.name}>· {sheet.name}：{sheet.columns.slice(0, 8).map(column => `${column.name}(${column.canonicalType})`).join('、')}{sheet.columns.length > 8 ? ` 等 ${sheet.columns.length} 列` : ''}</span>)}
          <small>仅在页面展示解析后的结构摘要；原始单元格内容不会进入配置模型。</small>
        </div>}
        {checks.length > 0 && <div className="data-source-ai-inline-card diagnostic"><strong>建议检查</strong>{checks.map(item => <span key={item}>· {item}</span>)}</div>}

        {modeChosen && recognized && effectiveMissingFields.length === 0 && !testPassed && !submitted && !testFailureCode && <div className="data-source-ai-inline-card action">
          <span className="action-icon"><PlugsConnected size={20} weight="fill" /></span>
          <div><strong>信息已齐全</strong><p>{draft.type === 'EXCEL' ? '我会保存当前附件并验证文件版本可用性。' : '我会保存当前配置并测试连接；若失败，将自动诊断并尝试一次安全修复。'}</p><AppButton type="button" disabled={Boolean(busy)} onClick={() => void saveAndTest()}>{busy === 'test' ? <><SpinnerGap className="spin" size={15} />正在验证…</> : <><PlugsConnected size={15} />{draft.type === 'EXCEL' ? '验证附件' : '测试连接'}</>}</AppButton></div>
        </div>}

        {busy === 'test' && draft.type !== 'EXCEL' && <div className="data-source-ai-inline-card diagnostic test-progress">
          <strong><SpinnerGap className="spin" size={15} />{connectionTestJob?.status === 'QUEUED'
            ? connectionTestJob.attempt > 0 ? `等待第 ${connectionTestJob.attempt + 1} 次检测` : '等待连接测试 Worker'
            : connectionTestJob?.status === 'FAILED' || connectionTestJob?.status === 'CANCELLED'
              ? '连接检测已在当前阶段停止'
              : connectionTestJob?.stage && connectionTestJob.stage !== 'QUEUED'
                ? `正在检测：${connectionTestStages.find(item => item.key === connectionTestJob.stage)?.label || connectionTestJob.stage}`
                : '正在准备分层连接检测'}</strong>
          {connectionTestStages.map((item, index) => {
              const state = connectionTestStageState(connectionTestJob, item.key)
              return <span className={`test-progress-stage ${state}`} key={item.key}>
                <i>{state === 'passed'
                  ? <CheckCircle size={13} weight="fill" />
                  : state === 'running'
                    ? <SpinnerGap className="spin" size={13} />
                    : state === 'failed'
                      ? <X size={13} weight="bold" />
                      : index + 1}</i>
                <span>{item.label}</span>
                <em>{state === 'passed' ? '已通过' : state === 'running' ? '检测中' : state === 'failed' ? '未通过' : '等待中'}</em>
              </span>
            })}
        </div>}

        {testPassed && testedSource && <div className="data-source-ai-inline-card action success">
          <span className="action-icon"><CheckCircle size={21} weight="fill" /></span>
          <div><strong>{testedSource.type === 'EXCEL' ? '附件解析与验证成功' : '连接成功'}</strong><p>“{testedSource.name}”已通过{testedSource.type === 'EXCEL' ? '文件可用性校验' : '连接校验'}。点击发布即可完成提交；审批通过前不会生效。</p><AppButton type="button" disabled={Boolean(busy)} onClick={() => void publish()}>{busy === 'publish' ? <><SpinnerGap className="spin" size={15} />发布中…</> : <><PaperPlaneRight size={15} weight="fill" />发布</>}</AppButton></div>
        </div>}

        {submitted && <div className="data-source-ai-inline-card action pending">
          <span className="action-icon"><CheckCircle size={21} weight="fill" /></span>
          <div><strong>已进入审核</strong><p>数据源列表已生成“待审批”记录。你可以关闭助手，等待领域管理员处理。</p></div>
        </div>}
        {(busy === 'chat' || busy === 'file') && <div className="data-source-ai-thinking"><span /><span /><span />{busy === 'file' ? '正在上传、解析附件并创建数据源' : '正在理解并整理参数'}</div>}
        </div>
      </div>
      <form className="data-source-ai-composer" onSubmit={submitMessage}>
        <AppButton text circle className="data-source-ai-attachment" type="button" disabled={Boolean(busy)} aria-label="添加 Excel 附件" title="添加 Excel / CSV 附件" onClick={() => attachmentInputRef.current?.click()}><FileArrowUp size={18} /></AppButton>
        <input ref={attachmentInputRef} className="data-source-ai-attachment-input" type="file" accept=".xlsx,.xls,.csv" disabled={Boolean(busy)} onChange={event => { const selected = event.target.files?.[0]; if (selected) void attachAndCreateExcelSource(selected) }} />
        <textarea rows={2} value={instruction} onChange={event => setInstruction(event.target.value)} placeholder={busy ? '可随时补充或更正信息…' : '直接描述需求或继续补充信息…'} />
        <AppButton variant="primary" circle className="data-source-ai-send" type="submit" disabled={busy === 'publish' || !instruction.trim()} aria-label={busy ? '发送补充信息' : '发送消息'}><PaperPlaneRight size={19} weight="fill" /></AppButton>
        <small>{pendingInstructions.length > 0
          ? `已收到 ${pendingInstructions.length} 条补充信息，正在按顺序处理`
          : busy === 'test'
            ? '可随时补充更正；发送后会停止等待本轮测试并优先处理新信息'
            : busy
              ? busy === 'publish' ? '正在提交发布；你可以先编辑补充内容，完成后发送' : '可继续补充；发送后会在当前步骤结束后自动处理'
              : '可直接添加 Excel / CSV 附件 · 原始内容不会发送给配置模型'}</small>
      </form>
    </section>}
  </aside>
}
