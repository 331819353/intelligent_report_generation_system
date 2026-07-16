import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import reportExample from '../../../api/examples/report-json-v1.json'
import { AppShell } from '../components/AppShell'
import { ComponentPalette } from '../components/report/ComponentPalette'
import { ReportDesignerCanvas, type ReportDesignerTransition } from '../components/report/ReportDesignerCanvas'
import { RequestError } from '../lib/api'
import type { ComponentType, ReportDocument, ReportRuntimeContext } from '../lib/report-contract'
import {
  createIdempotencyKey,
  createReportDraft,
  getReportDraft,
  saveReportDraft,
  type ReportDraftChange,
  type ReportDraftRecord,
  type ReportEditorState,
} from '../lib/report-drafts'
import { applyReportJSONPatch, createReportJSONPatch } from '../lib/report-json-patch'
import { validateReportDocument } from '../lib/report-schema'
import { demoReportRuntime } from '../lib/demo-report-runtime'

type DesignerPhase = 'LOADING' | 'READY' | 'SAVING' | 'CONFLICT' | 'ERROR' | 'READ_ONLY'

type CanvasSeed = {
  definition: ReportDocument
  editorState: ReportEditorState
  pendingChanges: ReportDraftChange[]
  generation: number
}

type SaveCandidate =
  | {
      kind: 'CREATE'
      idempotencyKey: string
      definition: ReportDocument
      editorState: ReportEditorState
      submittedOperationIds: string[]
    }
  | {
      kind: 'UPDATE'
      reportId: string
      idempotencyKey: string
      expectedRevision: number
      definition: ReportDocument
      editorState: ReportEditorState
      changes: ReportDraftChange[]
      submittedOperationIds: string[]
    }

type LegacyDraft = {
  key: string
  definition: ReportDocument
  editorState: ReportEditorState
  recoveryChange?: ReportDraftChange
  reason?: string
}

const NEW_REPORT_IDS = new Set(['draft', 'new', 'demo'])
const LEGACY_SESSION_KIND = 'report-designer-session-v1'
const MAX_SAVE_PATCH_OPERATIONS = 100

/** 设计器页面以服务端草稿为事实来源，并显式管理保存、冲突和旧会话迁移。 */
export function DesignerPage() {
  const { reportId = 'draft' } = useParams()
  const navigate = useNavigate()
  const startsNew = NEW_REPORT_IDS.has(reportId)
  const [initialSeed] = useState<CanvasSeed | undefined>(() => startsNew ? createNewReportSeed() : undefined)
  const [seed, setSeed] = useState<CanvasSeed | undefined>(initialSeed)
  const [baseline, setBaseline] = useState<ReportDraftRecord>()
  const [transition, setTransition] = useState<ReportDesignerTransition | undefined>(() => initialSeed ? transitionFromSeed(initialSeed) : undefined)
  const [phase, setPhase] = useState<DesignerPhase>(startsNew ? 'READY' : 'LOADING')
  const [message, setMessage] = useState('')
  const [pendingComponentType, setPendingComponentType] = useState<ComponentType>()
  const [acknowledgedOperationIds, setAcknowledgedOperationIds] = useState<string[]>([])
  const [retryCandidate, setRetryCandidate] = useState<SaveCandidate>()
  const [conflictSnapshot, setConflictSnapshot] = useState<ReportDesignerTransition>()
  const [legacyDraft, setLegacyDraft] = useState<LegacyDraft>()
  const loadSequence = useRef(0)
  const adoptedReportId = useRef<string | undefined>(undefined)
  const initializedRouteId = useRef(startsNew ? reportId : '')
  const transitionRef = useRef(transition)
  const seedRef = useRef(seed)

  useEffect(() => { transitionRef.current = transition }, [transition])
  useEffect(() => { seedRef.current = seed }, [seed])

  const acknowledged = useMemo(() => new Set(acknowledgedOperationIds), [acknowledgedOperationIds])
  const pendingChanges = useMemo(
    () => transition?.pendingChanges.filter(change => !acknowledged.has(change.clientOperationId)) ?? [],
    [acknowledged, transition],
  )
  // 尚未取得服务端基线的新建模板本身就是未保存工作，不能等到首次画布编辑后才启用离开保护。
  const hasUnpersistedTemplate = Boolean(transition && !baseline)
  const hasUnsavedWork = hasUnpersistedTemplate || pendingChanges.length > 0 || phase === 'SAVING'
  const readOnly = phase === 'READ_ONLY'
  const runtime = useMemo<ReportRuntimeContext>(() => readOnly
    ? { ...demoReportRuntime, permissions: demoReportRuntime.permissions?.filter(permission => permission !== 'report:edit') ?? [] }
    : demoReportRuntime, [readOnly])

  const installRecord = useCallback((record: ReportDraftRecord, inspectLegacy = true) => {
    const validation = validateReportDocument(record.definition)
    if (!validation.document) {
      setSeed(undefined)
      setTransition(undefined)
      setPhase('ERROR')
      setMessage('服务端报告草稿不符合当前报告合同，已停止进入编辑态。')
      return false
    }
    const nextSeed: CanvasSeed = {
      definition: validation.document,
      editorState: normalizeEditorState(record.editorState, validation.document),
      pendingChanges: [],
      generation: Date.now() + Math.random(),
    }
    setBaseline({ ...record, definition: validation.document, editorState: nextSeed.editorState })
    setSeed(nextSeed)
    setTransition(transitionFromSeed(nextSeed))
    setAcknowledgedOperationIds([])
    setRetryCandidate(undefined)
    setConflictSnapshot(undefined)
    setPhase(record.capabilities.edit ? 'READY' : 'READ_ONLY')
    setMessage(record.capabilities.edit ? '' : '当前账号可以查看草稿，但没有服务端编辑权限。')
    if (inspectLegacy) setLegacyDraft(readLegacyDraft(record, nextSeed.editorState))
    return true
  }, [])

  const loadExisting = useCallback(async (id: string, inspectLegacy = true) => {
    const sequence = ++loadSequence.current
    setPhase('LOADING')
    setMessage('正在加载服务端草稿…')
    try {
      const record = await getReportDraft(id)
      if (sequence !== loadSequence.current) return
      installRecord(record, inspectLegacy)
    } catch (error) {
      if (sequence !== loadSequence.current) return
      setSeed(undefined)
      setTransition(undefined)
      setPhase(error instanceof RequestError && error.status === 403 ? 'READ_ONLY' : 'ERROR')
      setMessage(describeLoadError(error))
    }
  }, [installRecord])

  /* eslint-disable react-hooks/set-state-in-effect -- 路由身份变化必须原子清空上一报告的本地状态。 */
  useEffect(() => {
    if (adoptedReportId.current === reportId) {
      adoptedReportId.current = undefined
      initializedRouteId.current = reportId
      return
    }
    if (initializedRouteId.current === reportId) return
    initializedRouteId.current = reportId
    loadSequence.current += 1
    setLegacyDraft(undefined)
    setRetryCandidate(undefined)
    setConflictSnapshot(undefined)
    setAcknowledgedOperationIds([])
    if (NEW_REPORT_IDS.has(reportId)) {
      const nextSeed = createNewReportSeed()
      setBaseline(undefined)
      setSeed(nextSeed)
      setTransition(transitionFromSeed(nextSeed))
      setPhase('READY')
      setMessage('')
      return
    }
    void loadExisting(reportId)
  }, [loadExisting, reportId])
  /* eslint-enable react-hooks/set-state-in-effect */

  useEffect(() => {
    if (!hasUnsavedWork) return
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    const handleLinkClick = (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target.closest('a[href]') : null
      if (!target || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
      const href = target.getAttribute('href')
      if (!href || href.startsWith('#')) return
      // beforeunload 只保护整页离开；普通链接在此确认，SPA 前进/后退（popstate）仍需后续路由阻断能力。
      if (!window.confirm('当前报告有未保存修改，确定离开吗？')) {
        event.preventDefault()
        event.stopPropagation()
      }
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    document.addEventListener('click', handleLinkClick, true)
    return () => {
      window.removeEventListener('beforeunload', handleBeforeUnload)
      document.removeEventListener('click', handleLinkClick, true)
    }
  }, [hasUnsavedWork])

  function handleTransition(next: ReportDesignerTransition) {
    transitionRef.current = next
    setTransition(next)
    if (phase === 'ERROR' && retryCandidate) return
    if (phase !== 'SAVING' && phase !== 'CONFLICT' && phase !== 'READ_ONLY') {
      setPhase('READY')
      setMessage('')
    }
  }

  async function handleSave() {
    if (!transition || phase === 'SAVING' || readOnly) return
    let candidate = retryCandidate
    if (!candidate) {
      try {
        candidate = baseline ? buildUpdateCandidate(baseline, transition, pendingChanges) : buildCreateCandidate(transition)
      } catch (error) {
        setPhase('ERROR')
        setMessage(error instanceof Error ? error.message : '无法生成安全的草稿保存批次。')
        return
      }
    }
    if (!candidate) return
    await persistCandidate(candidate)
  }

  async function persistCandidate(candidate: SaveCandidate) {
    setPhase('SAVING')
    setMessage('正在保存服务端草稿…')
    try {
      const record = candidate.kind === 'CREATE'
        ? await createReportDraft({ definition: candidate.definition, editorState: candidate.editorState }, candidate.idempotencyKey)
        : await saveReportDraft(candidate.reportId, {
            expectedRevision: candidate.expectedRevision,
            definition: candidate.definition,
            editorState: candidate.editorState,
            changes: candidate.changes,
          }, candidate.idempotencyKey)

      setRetryCandidate(undefined)
      setBaseline(record)
      if (candidate.kind === 'CREATE') {
        // 创建响应会注入服务端 UUID；重建一次内存基线，同时保留请求发出后产生的新操作。
        const submitted = new Set(candidate.submittedOperationIds)
        const latestTransition = transitionRef.current
        const remaining = latestTransition?.pendingChanges.filter(change => !submitted.has(change.clientOperationId)) ?? []
        const localDefinition = structuredClone(latestTransition?.document ?? record.definition)
        localDefinition.report.id = record.id
        const nextSeed: CanvasSeed = {
          definition: localDefinition,
          editorState: latestTransition?.editorState ?? record.editorState,
          pendingChanges: remaining,
          generation: (seedRef.current?.generation ?? 0) + 1,
        }
        seedRef.current = nextSeed
        transitionRef.current = transitionFromSeed(nextSeed)
        setSeed(nextSeed)
        setTransition(transitionRef.current)
        setAcknowledgedOperationIds([])
        adoptedReportId.current = record.id
        navigate(`/designer/${record.id}`, { replace: true })
        setPhase('READY')
        setMessage(remaining.length ? '草稿已创建，保存期间的新修改仍待保存。' : '草稿已创建并保存。')
      } else {
        setAcknowledgedOperationIds(candidate.submittedOperationIds)
        const submitted = new Set(candidate.submittedOperationIds)
        const hasLaterChanges = transitionRef.current?.pendingChanges.some(change => !submitted.has(change.clientOperationId)) === true
        setPhase('READY')
        setMessage(hasLaterChanges ? '本批修改已保存，保存期间的新修改仍待保存。' : '草稿已保存。')
        if (candidate.changes.some(change => change.operationType === 'LEGACY_DRAFT_RECOVERY') && legacyDraft) {
          sessionStorage.removeItem(legacyDraft.key)
          setLegacyDraft(undefined)
        }
      }
    } catch (error) {
      handleSaveFailure(error, candidate)
    }
  }

  function handleSaveFailure(error: unknown, candidate: SaveCandidate) {
    if (error instanceof RequestError && error.status === 409 && error.detail.code === 'REPORT_DRAFT_CONFLICT') {
      setRetryCandidate(undefined)
      // 冲突导出必须读取最新引用，确保保存请求发出后产生的编辑也包含在本地恢复快照中。
      const latestTransition = transitionRef.current
      setConflictSnapshot(latestTransition ? structuredClone(latestTransition) : undefined)
      setPhase('CONFLICT')
      setMessage(`服务端草稿已更新到修订 ${error.detail.currentRevision ?? '未知'}，本地修改尚未覆盖远端。`)
      return
    }
    if (error instanceof RequestError && error.status === 403) {
      setRetryCandidate(undefined)
      setPhase('READ_ONLY')
      setMessage('服务端已拒绝编辑权限，本地内容仍可导出，但不能继续保存。')
      return
    }
    const retryable = !(error instanceof RequestError) || error.status >= 500
    setRetryCandidate(retryable ? candidate : undefined)
    setPhase('ERROR')
    setMessage(describeSaveError(error, retryable))
  }

  function keepServerAndDiscardLegacy() {
    if (!legacyDraft) return
    sessionStorage.removeItem(legacyDraft.key)
    setLegacyDraft(undefined)
    setMessage('已保留服务端版本并清除旧会话副本。')
  }

  function recoverLegacy() {
    if (!legacyDraft?.recoveryChange || !baseline) return
    const nextSeed: CanvasSeed = {
      definition: structuredClone(legacyDraft.definition),
      editorState: legacyDraft.editorState,
      pendingChanges: [legacyDraft.recoveryChange],
      generation: (seed?.generation ?? 0) + 1,
    }
    setSeed(nextSeed)
    setTransition(transitionFromSeed(nextSeed))
    setAcknowledgedOperationIds([])
    setRetryCandidate(undefined)
    setPhase('READY')
    setMessage('旧会话草稿已载入为未保存修改，服务端确认保存后才会删除旧副本。')
  }

  const title = transition?.document.report.name ?? baseline?.name ?? '报告设计器'
  const dirty = hasUnpersistedTemplate || pendingChanges.length > 0
  const saveDisabled = !transition || phase === 'LOADING' || phase === 'SAVING' || phase === 'CONFLICT' || readOnly || Boolean(baseline && !dirty && !retryCandidate)
  const statusLabel = phase === 'LOADING' ? '加载中'
    : phase === 'SAVING' ? '保存中'
      : phase === 'CONFLICT' ? '冲突'
        : phase === 'READ_ONLY' ? '只读'
          : phase === 'ERROR' ? '保存失败'
            : dirty ? '未保存' : '已保存'

  const actions = <>
    <span className={`report-draft-status report-draft-status--${statusLabel}`}>{statusLabel}</span>
    <button type="button" className="quiet-button" disabled>✦ AI 修改</button>
    <button type="button" className="quiet-button" disabled={!seed}>预览</button>
    <button type="button" className="primary-button" disabled={saveDisabled} onClick={handleSave}>{retryCandidate ? '重试保存' : baseline ? '保存草稿' : '创建草稿'}</button>
    <button type="button" className="quiet-button" disabled title="不可变发布版本将在 T0601 接入">发布</button>
  </>

  return (
    <AppShell title={title} eyebrow="报告设计器" actions={actions}>
      {message && <div className={`report-draft-message report-draft-message--${phase.toLowerCase()}`} role={phase === 'ERROR' || phase === 'CONFLICT' ? 'alert' : 'status'}>{message}</div>}
      {phase === 'CONFLICT' && conflictSnapshot && (
        <section className="report-draft-recovery" aria-label="草稿冲突处理">
          <strong>本地修改未被覆盖</strong><span>请选择载入服务端最新版本，或先导出本地 JSON 留存。</span>
          <button type="button" onClick={() => exportReportJSON(conflictSnapshot.document, 'conflict-local')}>导出本地 JSON</button>
          <button type="button" onClick={() => void loadExisting(baseline?.id ?? reportId, false)}>载入服务端版本</button>
        </section>
      )}
      {phase === 'READ_ONLY' && transition && (
        <section className="report-draft-recovery" aria-label="只读草稿处理">
          <strong>当前草稿已切换为只读</strong><span>本地未保存内容不会自动上传，可先导出留存。</span>
          <button type="button" onClick={() => exportReportJSON(transition.document, 'read-only-local')}>导出本地 JSON</button>
        </section>
      )}
      {legacyDraft && (
        <section className="report-draft-recovery" aria-label="旧会话草稿迁移">
          <strong>检测到旧会话草稿</strong><span>{legacyDraft.reason ?? '旧副本与服务端不同，不会自动覆盖服务端事实。'}</span>
          <button type="button" onClick={() => exportReportJSON(legacyDraft.definition, 'legacy-local')}>导出旧副本</button>
          {legacyDraft.recoveryChange && <button type="button" onClick={recoverLegacy}>恢复为未保存修改</button>}
          <button type="button" onClick={keepServerAndDiscardLegacy}>保留服务端并清除旧副本</button>
        </section>
      )}
      {!seed && phase !== 'LOADING' && <section className="panel"><p>{message || '无法加载报告草稿。'}</p><button type="button" onClick={() => void loadExisting(reportId)}>重新加载</button></section>}
      {phase === 'LOADING' && !seed && <section className="panel" role="status">正在加载报告草稿…</section>}
      {seed && (
        <div className="designer-layout">
          <aside className="tool-panel"><span className="eyebrow">组件</span><h2>内容组件</h2><p className="component-palette-help">拖入分块，或选择后点击空白处</p><ComponentPalette selectedType={pendingComponentType} onSelect={setPendingComponentType} /></aside>
          <section className="canvas-wrap"><div className="canvas-toolbar"><span>1920 × 1080 首屏基准 · 方向键移动 · Shift + 方向键缩放</span><strong>自动适应宽度</strong></div><div className="canvas"><ReportDesignerCanvas source={seed.definition} runtime={runtime} initialEditorState={seed.editorState} initialPendingChanges={seed.pendingChanges} loadGeneration={seed.generation} acknowledgedClientOperationIds={acknowledgedOperationIds} onTransition={handleTransition} pendingComponentType={pendingComponentType} onPendingComponentConsumed={() => setPendingComponentType(undefined)} /></div></section>
          <aside className="property-panel"><span className="eyebrow">属性</span><h2>选择与设置</h2><p className="component-palette-help">点击或聚焦画布中的分块、组件，可在画布上方配置浏览态冻结；修改会进入统一撤销历史。</p><p className="component-palette-help">分块配置锁开启时，冻结设置只读；空白基础单元仍可直接选择。</p></aside>
        </div>
      )}
    </AppShell>
  )
}

function buildCreateCandidate(transition: ReportDesignerTransition): SaveCandidate {
  return {
    kind: 'CREATE',
    idempotencyKey: createIdempotencyKey(),
    definition: structuredClone(transition.document),
    editorState: structuredClone(transition.editorState),
    submittedOperationIds: transition.pendingChanges.map(change => change.clientOperationId),
  }
}

function buildUpdateCandidate(baseline: ReportDraftRecord, transition: ReportDesignerTransition, pending: ReportDraftChange[]): SaveCandidate | undefined {
  if (pending.length === 0) return undefined
  let definition = structuredClone(baseline.definition)
  let operationCount = 0
  const changes: ReportDraftChange[] = []
  for (const change of pending) {
    if (change.patch.length > MAX_SAVE_PATCH_OPERATIONS) throw new Error('单次语义操作超过 100 条 Patch，已停止保存并保留本地修改。')
    if (changes.length >= MAX_SAVE_PATCH_OPERATIONS || operationCount + change.patch.length > MAX_SAVE_PATCH_OPERATIONS) break
    definition = applyReportJSONPatch(definition, change.patch)
    changes.push(structuredClone(change))
    operationCount += change.patch.length
  }
  if (changes.length === 0) throw new Error('待保存 Patch 超过服务端上限，已保留本地修改。')
  return {
    kind: 'UPDATE',
    reportId: baseline.id,
    idempotencyKey: createIdempotencyKey(),
    expectedRevision: baseline.revision,
    definition,
    editorState: structuredClone(changes.length === pending.length ? transition.editorState : baseline.editorState),
    changes,
    submittedOperationIds: changes.map(change => change.clientOperationId),
  }
}

function createNewReportSeed(): CanvasSeed {
  const definition = structuredClone(reportExample) as ReportDocument
  delete definition.report.id
  definition.report.status = 'DRAFT'
  definition.report.code = `${definition.report.code}_${createIdempotencyKey().slice(0, 8)}`
  const editorState = normalizeEditorState(undefined, definition)
  return { definition, editorState, pendingChanges: [], generation: 1 }
}

function transitionFromSeed(seed: CanvasSeed): ReportDesignerTransition {
  return {
    document: structuredClone(seed.definition),
    editorState: structuredClone(seed.editorState),
    pendingChanges: structuredClone(seed.pendingChanges),
  }
}

function normalizeEditorState(editorState: ReportEditorState | undefined, document: ReportDocument): ReportEditorState {
  const rows = editorState?.minimumRowsByPage ?? {}
  return {
    minimumRowsByPage: Object.fromEntries(document.pages.map(page => {
      const candidate = rows[page.id]
      const value = typeof candidate === 'number' && Number.isFinite(candidate) ? Math.round(candidate) : page.contentGridRows
      return [page.id, Math.min(1000, Math.max(10, page.contentGridRows, value))]
    })),
  }
}

function readLegacyDraft(record: ReportDraftRecord, fallbackEditorState: ReportEditorState): LegacyDraft | undefined {
  const key = `report-layout-draft:${record.id}`
  const raw = sessionStorage.getItem(key)
  if (!raw) return undefined
  try {
    const parsed = JSON.parse(raw) as unknown
    const envelope = isRecord(parsed) && parsed.kind === LEGACY_SESSION_KIND ? parsed : undefined
    const validation = validateReportDocument(envelope ? envelope.document : parsed)
    if (!validation.document) {
      sessionStorage.removeItem(key)
      return undefined
    }
    const definition = structuredClone(validation.document)
    const idLooksForeign = typeof definition.report.id === 'string' && isUUID(definition.report.id) && definition.report.id !== record.id
    if (idLooksForeign || definition.report.code !== record.code || definition.report.type !== record.type || definition.report.status !== 'DRAFT') {
      return { key, definition, editorState: fallbackEditorState, reason: '旧副本的报告身份与服务端不一致，只能导出或清除。' }
    }
    definition.report.id = record.id
    const editorState = normalizeEditorState(envelope && isRecord(envelope.minimumRowsByPage)
      ? { minimumRowsByPage: envelope.minimumRowsByPage as Record<string, number> }
      : fallbackEditorState, definition)
    const patch = createReportJSONPatch(record.definition, definition).forward
    if (patch.length === 0) {
      sessionStorage.removeItem(key)
      return undefined
    }
    const recoverable = patch.length <= MAX_SAVE_PATCH_OPERATIONS && patch.every(operation => operation.path !== '')
    return {
      key,
      definition,
      editorState,
      ...(recoverable ? { recoveryChange: {
        clientOperationId: createIdempotencyKey(),
        operationType: 'LEGACY_DRAFT_RECOVERY',
        source: 'USER',
        patch,
      } } : {}),
      ...(!recoverable ? { reason: '旧副本差异超过安全 Patch 上限，只能导出或清除。' } : {}),
    }
  } catch {
    // 仅删除精确的旧布局键，绝不清空同一 sessionStorage 中的登录令牌。
    sessionStorage.removeItem(key)
    return undefined
  }
}

function describeLoadError(error: unknown) {
  if (error instanceof RequestError && error.status === 403) return '当前账号没有读取该报告草稿的权限。'
  if (error instanceof RequestError && error.status === 404) return '报告草稿不存在或已删除。'
  return error instanceof Error ? `报告草稿加载失败：${error.message}` : '报告草稿加载失败，请稍后重试。'
}

function describeSaveError(error: unknown, retryable: boolean) {
  if (error instanceof RequestError) {
    if (error.detail.code === 'REPORT_RESOURCE_OCCUPIED') return '报告或相关分块正被任务占用，当前修改未保存。'
    if (error.detail.code === 'REPORT_EDIT_LOCKED') return `服务端锁定内容拒绝修改：${error.message}`
    if (error.detail.code === 'REPORT_JSON_VALIDATION_FAILED') return `报告合同校验失败：${error.message}`
    if (error.detail.code === 'REPORT_CODE_CONFLICT') return '报告编码已存在，请调整报告编码后重新创建。'
    if (error.detail.code === 'REPORT_IDEMPOTENCY_CONFLICT') return '保存幂等键与已有请求冲突，已停止自动重试。'
  }
  const detail = error instanceof Error ? error.message : '未知错误'
  return retryable ? `草稿保存失败，可使用同一请求安全重试：${detail}` : `草稿保存失败：${detail}`
}

function exportReportJSON(document: ReportDocument, suffix: string) {
  const blob = new Blob([JSON.stringify(document, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const anchor = globalThis.document.createElement('a')
  anchor.href = url
  anchor.download = `${document.report.code}-${suffix}.json`
  anchor.click()
  URL.revokeObjectURL(url)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function isUUID(value: string) {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value)
}
