import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { Link } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import { RequestError } from '../lib/api'
import {
  semanticParsingRuleAPI,
  type SemanticParsingRule,
  type SemanticParsingRuleInput,
  type SemanticParsingRuleStatus,
  type SemanticParsingRuleType,
} from '../lib/semantic-parsing-rules'

type Notice = { tone: 'success' | 'error' | 'info'; message: string }
type EditorState = {
  editing?: SemanticParsingRule
  source?: SemanticParsingRule
  draft: SemanticParsingRuleInput
}

type ScenarioDefinition = {
  number: string
  title: string
  shortTitle: string
  problem: string
  result: string
  example: string
  fieldLabel: string
  fieldHelp: string
  placeholder: string
  matchMode: SemanticParsingRuleInput['matchMode']
  action: SemanticParsingRuleInput['action']
  minimumLength: number
  maximumLength: number
}

const scenarioDefinitions: Record<SemanticParsingRuleType, ScenarioDefinition> = {
  METRIC_NAME_SUFFIX: {
    number: '01',
    title: '识别指标简称',
    shortTitle: '指标简称',
    problem: '指标名称很长，用户习惯只说前面的业务词。',
    result: '从正式指标名中提取可识别的简称。',
    example: '“投诉数量”可提取“投诉”，从而识别“投诉总量”。',
    fieldLabel: '指标名称中可以省略的结尾词',
    fieldHelp: '填写指标正式名称里经常出现、但用户提问时可能省略的结尾部分。',
    placeholder: '例如：数量、金额合计、实体数量',
    matchMode: 'SUFFIX', action: 'STRIP_SUFFIX',
    minimumLength: 2, maximumLength: 0,
  },
  ADMIN_REGION_SUFFIX: {
    number: '02',
    title: '识别行政区域',
    shortTitle: '行政区域',
    problem: '用户说的是“北京市”，数据中保存的是“北京”。',
    result: '拆分地域名称，并确定它属于城市、省份还是区县。',
    example: '“北京市”会被理解为“城市 = 北京”。',
    fieldLabel: '地域名称的结尾词',
    fieldHelp: '填写地域名称末尾用来表示行政级别的文字。',
    placeholder: '例如：市、开发区、自治州',
    matchMode: 'SUFFIX', action: 'MAP_ADMIN_REGION',
    minimumLength: 2, maximumLength: 12,
  },
  QUERY_RESIDUAL_TERM: {
    number: '03',
    title: '忽略口语表达',
    shortTitle: '口语表达',
    problem: '“帮我、请问、看一下”等词不改变查询含义。',
    result: '忽略不影响指标和筛选条件的口语词。',
    example: '“帮我查投诉量”会忽略“帮我”，继续识别投诉量。',
    fieldLabel: '可以忽略的口语表达',
    fieldHelp: '只有完全不影响业务含义的词才能放在这里。',
    placeholder: '例如：帮我、请问、看一下',
    matchMode: 'EXACT', action: 'ALLOW_DETERMINISTIC',
    minimumLength: 0, maximumLength: 0,
  },
  BROAD_METRIC_PHRASE: {
    number: '04',
    title: '拦截过于宽泛的问题',
    shortTitle: '宽泛问题',
    problem: '“经营情况怎么样”无法判断用户真正想看哪个指标。',
    result: '先展示相关指标，让用户确认后再查询。',
    example: '“经营情况怎么样”不会擅自查询，而是先请用户选择指标。',
    fieldLabel: '需要用户进一步确认的问法',
    fieldHelp: '填写无法直接对应到一个具体指标的宽泛问题。',
    placeholder: '例如：经营情况怎么样、整体表现如何',
    matchMode: 'CONTAINS', action: 'REQUIRE_METRIC_CONFIRMATION',
    minimumLength: 0, maximumLength: 0,
  },
}

const scenarioOrder = Object.keys(scenarioDefinitions) as SemanticParsingRuleType[]

const adminDimensions = [
  { code: 'city', name: '城市', label: '城市', example: '北京市 → 城市 = 北京' },
  { code: 'province', name: '省份', label: '省份', example: '浙江省 → 省份 = 浙江' },
  { code: 'district', name: '行政区', label: '区 / 县', example: '海淀区 → 行政区 = 海淀' },
]

const emptyDraft = (
  ruleType: SemanticParsingRuleType = 'METRIC_NAME_SUFFIX',
): SemanticParsingRuleInput => {
  const definition = scenarioDefinitions[ruleType]
  return {
    ruleType,
    pattern: '',
    matchMode: definition.matchMode,
    action: definition.action,
    outputName: ruleType === 'ADMIN_REGION_SUFFIX' ? '城市' : '',
    outputCode: ruleType === 'ADMIN_REGION_SUFFIX' ? 'city' : '',
    minimumLength: definition.minimumLength,
    maximumLength: definition.maximumLength,
    priority: 100,
  }
}

const draftOf = (rule: SemanticParsingRule): SemanticParsingRuleInput => ({
  ruleType: rule.ruleType,
  pattern: rule.pattern,
  matchMode: rule.matchMode,
  action: rule.action,
  outputName: rule.outputName || '',
  outputCode: rule.outputCode || '',
  minimumLength: rule.minimumLength,
  maximumLength: rule.maximumLength,
  priority: rule.priority,
})

const errorMessage = (cause: unknown) =>
  cause instanceof RequestError || cause instanceof Error
    ? cause.message
    : '操作失败，请稍后重试。'

const ruleExplanation = (rule: Pick<SemanticParsingRuleInput, 'ruleType' | 'pattern' | 'outputName'>) => {
  const pattern = rule.pattern.trim()
  if (!pattern) {
    return {
      result: '填写上方内容后，这里会展示系统如何理解这条设置。',
      example: scenarioDefinitions[rule.ruleType].example,
    }
  }
  switch (rule.ruleType) {
  case 'METRIC_NAME_SUFFIX':
    return {
      result: `正式指标名以“${pattern}”结尾时，系统会提取前面的业务词作为识别线索。`,
      example: `“业务词 + ${pattern}” → 额外识别“业务词”`,
    }
  case 'ADMIN_REGION_SUFFIX':
    return {
      result: `地域名称以“${pattern}”结尾时，系统会去掉结尾词并识别为${rule.outputName || '所选地域类型'}。`,
      example: `“北京${pattern}” → ${rule.outputName || '地域'} = 北京`,
    }
  case 'QUERY_RESIDUAL_TERM':
    return {
      result: `问题中的“${pattern}”不会参与指标和维度判断。`,
      example: `“${pattern}，查询投诉量” → 忽略“${pattern}”后继续解析`,
    }
  case 'BROAD_METRIC_PHRASE':
    return {
      result: `问题中出现“${pattern}”时，系统不会擅自挑选指标。`,
      example: `“${pattern}” → 展示相关指标，请用户确认`,
    }
  }
}

export function SemanticParsingRulePage() {
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [canRead, setCanRead] = useState(false)
  const [canManage, setCanManage] = useState(false)
  const [rules, setRules] = useState<SemanticParsingRule[]>([])
  const [total, setTotal] = useState(0)
  const [query, setQuery] = useState('')
  const [ruleType, setRuleType] = useState<SemanticParsingRuleType | ''>('')
  const [status, setStatus] = useState<SemanticParsingRuleStatus | ''>('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [editor, setEditor] = useState<EditorState | null>(null)
  const [deprecating, setDeprecating] = useState<SemanticParsingRule | null>(null)
  const [systemDefaultsOpen, setSystemDefaultsOpen] = useState(false)
  const resultsRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    Promise.all([
      semanticParsingRuleAPI.evaluatePermission('READ'),
      semanticParsingRuleAPI.evaluatePermission('MANAGE'),
    ]).then(([read, manage]) => {
      if (!active) return
      setCanRead(read.allowed)
      setCanManage(manage.allowed)
    }).catch(() => {
      if (active) setCanRead(false)
    }).finally(() => {
      if (active) setPermissionsReady(true)
    })
    return () => { active = false }
  }, [])

  const loadRules = useCallback(async () => {
    if (!permissionsReady || !canRead) {
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const page = await semanticParsingRuleAPI.list({ status: '' })
      setRules(page.items)
      setTotal(page.total)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setLoading(false)
    }
  }, [canRead, permissionsReady])

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadRules() }, 0)
    return () => window.clearTimeout(timer)
  }, [loadRules])

  const counts = useMemo(() => ({
    platform: rules.filter(rule => rule.scope === 'PLATFORM').length,
    tenant: rules.filter(rule => rule.scope === 'TENANT').length,
    active: rules.filter(rule => rule.status === 'ACTIVE').length,
  }), [rules])

  const visibleRules = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
    return rules.filter(rule => {
      if (ruleType && rule.ruleType !== ruleType) return false
      if (status && rule.status !== status) return false
      if (!normalizedQuery) return true
      const searchable = [
        rule.pattern,
        rule.outputName || '',
        scenarioDefinitions[rule.ruleType].title,
        scenarioDefinitions[rule.ruleType].problem,
      ].join(' ').toLocaleLowerCase('zh-CN')
      return searchable.includes(normalizedQuery)
    })
  }, [query, ruleType, rules, status])

  const tenantRules = visibleRules.filter(rule => rule.scope === 'TENANT')
  const platformRules = visibleRules.filter(rule => rule.scope === 'PLATFORM')

  const showRuleList = (nextType: SemanticParsingRuleType | '') => {
    setRuleType(nextType)
    setQuery('')
    setStatus(nextType ? 'ACTIVE' : '')
    setSystemDefaultsOpen(true)
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        resultsRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      })
    })
  }

  const changeRuleType = (nextType: SemanticParsingRuleType) => {
    if (!editor || editor.editing || editor.source) return
    setEditor({ draft: emptyDraft(nextType) })
  }

  const openNewRule = (nextType: SemanticParsingRuleType = 'METRIC_NAME_SUFFIX') => {
    setEditor({ draft: emptyDraft(nextType) })
  }

  const submitEditor = async (event: FormEvent) => {
    event.preventDefault()
    if (!editor) return
    const input: SemanticParsingRuleInput = {
      ...editor.draft,
      pattern: editor.draft.pattern.trim(),
      outputName: editor.draft.outputName?.trim() || '',
      outputCode: editor.draft.outputCode?.trim() || '',
    }
    if (!input.pattern) {
      setNotice({ tone: 'error', message: '请填写需要系统识别的内容。' })
      return
    }
    if (input.ruleType === 'ADMIN_REGION_SUFFIX' &&
      (!input.outputName || !input.outputCode)) {
      setNotice({ tone: 'error', message: '请选择这类地域应该识别成什么。' })
      return
    }
    setBusy(true)
    try {
      if (editor.editing) {
        await semanticParsingRuleAPI.update(
          editor.editing.id, editor.editing.version, input,
        )
        setNotice({ tone: 'success', message: '问法设置已更新，下一次提问立即生效。' })
      } else {
        await semanticParsingRuleAPI.create(input)
        setNotice({
          tone: 'success',
          message: editor.source?.status === 'DEPRECATED'
            ? '这条问法设置已重新启用。'
            : '问法设置已保存，下一次提问立即生效。',
        })
      }
      setEditor(null)
      await loadRules()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) {
        await loadRules()
      }
    } finally {
      setBusy(false)
    }
  }

  const confirmDeprecate = async () => {
    if (!deprecating) return
    setBusy(true)
    try {
      await semanticParsingRuleAPI.deprecate(
        deprecating.id, deprecating.version,
      )
      setNotice({ tone: 'success', message: '这条问法设置已停用。' })
      setDeprecating(null)
      await loadRules()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) {
        await loadRules()
      }
    } finally {
      setBusy(false)
    }
  }

  const hasPlatformEquivalent = deprecating && rules.some(rule =>
    rule.scope === 'PLATFORM' &&
    rule.ruleType === deprecating.ruleType &&
    rule.pattern.toLocaleLowerCase('zh-CN') ===
      deprecating.pattern.toLocaleLowerCase('zh-CN'),
  )

  const renderRule = (rule: SemanticParsingRule) => {
    const definition = scenarioDefinitions[rule.ruleType]
    const explanation = ruleExplanation(rule)
    return (
      <article className="semantic-rule-record" key={rule.id}>
        <header>
          <div>
            <span className="semantic-rule-kind">{definition.shortTitle}</span>
            <strong>{rule.pattern}</strong>
          </div>
          <span className={`semantic-rule-scope ${rule.scope.toLowerCase()}`}>
            {rule.scope === 'PLATFORM' ? '系统内置' : '我的设置'}
          </span>
        </header>
        <p>{explanation.result}</p>
        <div className="semantic-rule-example">
          <span>示例</span>
          <strong>{explanation.example}</strong>
        </div>
        <footer>
          <span className={`semantic-rule-readable-status ${rule.status.toLowerCase()}`}>
            {rule.status === 'ACTIVE' ? '正在使用' : '已停用'}
          </span>
          <div className="semantic-asset-actions">
            {rule.scope === 'PLATFORM' ? (
              <button
                type="button"
                disabled={!canManage}
                onClick={() => setEditor({ source: rule, draft: draftOf(rule) })}
              >按我的业务调整</button>
            ) : rule.status === 'ACTIVE' ? (
              <>
                <button
                  type="button"
                  disabled={!canManage}
                  onClick={() => setEditor({ editing: rule, draft: draftOf(rule) })}
                >修改</button>
                <button type="button" disabled={!canManage} onClick={() => setDeprecating(rule)}>停用</button>
              </>
            ) : (
              <button
                type="button"
                disabled={!canManage}
                onClick={() => setEditor({ source: rule, draft: draftOf(rule) })}
              >重新启用</button>
            )}
          </div>
        </footer>
      </article>
    )
  }

  const editorExplanation = editor ? ruleExplanation(editor.draft) : null

  return (
    <AppShell title="资产管理中心" eyebrow="数据资产 · 问句解析设置">
      <AssetManagementTabs />
      <section className="semantic-asset-page semantic-rule-page">
        {notice && (
          <div
            className={`semantic-asset-notice ${notice.tone}`}
            role={notice.tone === 'error' ? 'alert' : 'status'}
          >
            <span>{notice.message}</span>
            <button type="button" aria-label="关闭提示" onClick={() => setNotice(null)}>×</button>
          </div>
        )}

        {!permissionsReady ? (
          <div className="semantic-asset-empty">正在检查问句解析设置权限…</div>
        ) : !canRead ? (
          <div className="semantic-asset-empty denied">当前账号没有问句解析设置读取权限。</div>
        ) : (
          <>
            {!canManage && (
              <div className="semantic-asset-readonly" role="note">
                当前为只读模式；新增、修改和停用需要 DATASET:MANAGE。
              </div>
            )}
            <header className="semantic-asset-hero semantic-rule-hero">
              <div>
                <span className="eyebrow">问句解析设置</span>
                <h2>告诉系统，遇到固定问法时应该怎么理解</h2>
                <p>
                  这里只处理指标简称、地域结尾、口语词和过于宽泛的问题。
                  选择你遇到的场景，填写一项内容即可；保存后下一次提问立即生效。
                </p>
              </div>
              <button
                className="primary-button"
                type="button"
                disabled={!canManage}
                onClick={() => openNewRule()}
              >新增问法设置</button>
            </header>

            <section className="semantic-rule-boundary" aria-label="配置边界说明">
              <div>
                <strong>先确认是不是这里要解决的问题</strong>
                <p>
                  “客诉量 = 投诉数量”“帝都 = 北京”属于业务词义，不属于固定问句结构。
                </p>
              </div>
              <Link to="/assets/semantics">业务词义请到“语义资产”配置 →</Link>
            </section>

            <div className="semantic-rule-flow" aria-label="问句解析所处流程">
              <article><span>1</span><div><strong>用户提问</strong><small>北京市投诉总量是多少</small></div></article>
              <b aria-hidden="true">→</b>
              <article><span>2</span><div><strong>固定问法处理</strong><small>城市 = 北京，指标词 = 投诉</small></div></article>
              <b aria-hidden="true">→</b>
              <article><span>3</span><div><strong>语义检索与查询</strong><small>匹配已发布指标和维度</small></div></article>
            </div>

            <section className="semantic-rule-scenarios">
              <header>
                <div><span className="eyebrow">按问题选择</span><h3>你想让系统解决哪类问法？</h3></div>
                <button
                  type="button"
                  className={ruleType === '' ? 'active' : ''}
                  onClick={() => showRuleList('')}
                >查看全部</button>
              </header>
              <div>
                {scenarioOrder.map(type => {
                  const definition = scenarioDefinitions[type]
                  const count = rules.filter(rule => rule.ruleType === type && rule.status === 'ACTIVE').length
                  return (
                    <article className={ruleType === type ? 'selected' : ''} key={type}>
                      <span className="semantic-rule-number">{definition.number}</span>
                      <h4>{definition.title}</h4>
                      <p>{definition.problem}</p>
                      <strong>{definition.result}</strong>
                      <small>{definition.example}</small>
                      <footer>
                        <button type="button" onClick={() => showRuleList(type)}>查看 {count} 条设置</button>
                        <button type="button" disabled={!canManage} onClick={() => openNewRule(type)}>新增</button>
                      </footer>
                    </article>
                  )
                })}
              </div>
            </section>

            <div ref={resultsRef} className="semantic-asset-filters semantic-rule-filters">
              <label>
                搜索设置内容
                <input
                  aria-label="搜索问句解析设置"
                  value={query}
                  placeholder="例如：数量、市、帮我、经营情况"
                  onChange={event => setQuery(event.target.value)}
                />
              </label>
              <label>
                使用状态
                <select
                  aria-label="问句解析设置状态"
                  value={status}
                  onChange={event => setStatus(
                    event.target.value as SemanticParsingRuleStatus | '',
                  )}
                >
                  <option value="">全部状态</option>
                  <option value="ACTIVE">正在使用</option>
                  <option value="DEPRECATED">已停用</option>
                </select>
              </label>
              <div className="semantic-rule-filter-result">
                <strong>{visibleRules.length}</strong>
                <span>条符合条件</span>
              </div>
              <button className="quiet-button" type="button" disabled={loading} onClick={() => void loadRules()}>
                重新加载
              </button>
            </div>

            {loading ? (
              <div className="semantic-asset-empty">正在加载问句解析设置…</div>
            ) : (
              <>
                <section className="semantic-rule-list-section">
                  <header>
                    <div><span className="eyebrow">当前租户</span><h3>我的问法设置</h3></div>
                    <span>{tenantRules.length} 条</span>
                  </header>
                  {tenantRules.length ? (
                    <div className="semantic-rule-record-grid">{tenantRules.map(renderRule)}</div>
                  ) : (
                    <div className="semantic-rule-friendly-empty">
                      <strong>当前没有符合条件的自定义设置</strong>
                      <p>{counts.tenant ? '可以调整筛选条件查看其他设置。' : '常见问法已经由系统内置规则处理，只有遇到本企业特有说法时才需要新增。'}</p>
                      {!counts.tenant && <button type="button" disabled={!canManage} onClick={() => openNewRule()}>新增第一条问法设置</button>}
                    </div>
                  )}
                </section>

                <details
                  className="semantic-rule-system-defaults"
                  open={systemDefaultsOpen}
                  onToggle={event => setSystemDefaultsOpen(event.currentTarget.open)}
                >
                  <summary>
                    <div><strong>系统内置设置</strong><small>覆盖常见中文问法，通常无需修改</small></div>
                    <span>{platformRules.length} 条</span>
                  </summary>
                  {platformRules.length ? (
                    <div className="semantic-rule-record-grid">{platformRules.map(renderRule)}</div>
                  ) : (
                    <div className="semantic-rule-friendly-empty"><p>当前筛选条件下没有系统内置设置。</p></div>
                  )}
                </details>

                <div className="semantic-rule-health-note">
                  当前共 {total} 条设置，其中 {counts.platform} 条由系统维护、
                  {counts.tenant} 条由当前租户维护、{counts.active} 条正在使用。
                </div>
              </>
            )}
          </>
        )}
      </section>

      {editor && editorExplanation && (
        <div className="semantic-asset-dialog-backdrop" role="presentation">
          <form
            className="semantic-asset-dialog semantic-rule-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="配置问句解析设置"
            onSubmit={event => void submitEditor(event)}
          >
            <header>
              <div>
                <span className="eyebrow">按业务场景配置</span>
                <h2>{editor.editing ? '修改问法设置' : editor.source?.scope === 'PLATFORM' ? '按我的业务调整' : editor.source ? '重新启用问法设置' : '新增问法设置'}</h2>
              </div>
              <button type="button" aria-label="关闭问句解析设置" onClick={() => setEditor(null)}>×</button>
            </header>

            <fieldset className="semantic-rule-scenario-picker" disabled={Boolean(editor.editing || editor.source)}>
              <legend>1. 选择你遇到的问题</legend>
              <div>
                {scenarioOrder.map(type => (
                  <button
                    className={editor.draft.ruleType === type ? 'selected' : ''}
                    key={type}
                    type="button"
                    onClick={() => changeRuleType(type)}
                  >
                    <span>{scenarioDefinitions[type].number}</span>
                    <strong>{scenarioDefinitions[type].title}</strong>
                  </button>
                ))}
              </div>
            </fieldset>

            <section className="semantic-rule-editor-context">
              <span>这个场景解决什么</span>
              <strong>{scenarioDefinitions[editor.draft.ruleType].problem}</strong>
              <p>{scenarioDefinitions[editor.draft.ruleType].result}</p>
            </section>

            <label className="semantic-rule-main-field">
              <span>2. {scenarioDefinitions[editor.draft.ruleType].fieldLabel}</span>
              <input
                aria-label={scenarioDefinitions[editor.draft.ruleType].fieldLabel}
                value={editor.draft.pattern}
                disabled={Boolean(editor.source)}
                placeholder={scenarioDefinitions[editor.draft.ruleType].placeholder}
                onChange={event => setEditor({ ...editor, draft: { ...editor.draft, pattern: event.target.value } })}
              />
              <small>{scenarioDefinitions[editor.draft.ruleType].fieldHelp}</small>
            </label>

            {editor.draft.ruleType === 'ADMIN_REGION_SUFFIX' && (
              <label className="semantic-rule-main-field">
                <span>3. 这类地域应该识别成什么</span>
                <select
                  aria-label="地域类型"
                  value={editor.draft.outputCode || 'city'}
                  onChange={event => {
                    const selected = adminDimensions.find(item => item.code === event.target.value)
                    if (!selected) return
                    setEditor({
                      ...editor,
                      draft: {
                        ...editor.draft,
                        outputCode: selected.code,
                        outputName: selected.name,
                      },
                    })
                  }}
                >
                  {!adminDimensions.some(item => item.code === editor.draft.outputCode) && editor.draft.outputCode && (
                    <option value={editor.draft.outputCode}>{editor.draft.outputName || '现有地域类型'}</option>
                  )}
                  {adminDimensions.map(item => (
                    <option key={item.code} value={item.code}>{item.label}（{item.example}）</option>
                  ))}
                </select>
              </label>
            )}

            <section className="semantic-rule-preview" aria-label="设置效果预览">
              <header><span>{editor.draft.ruleType === 'ADMIN_REGION_SUFFIX' ? '4' : '3'}. 效果预览</span><strong>保存后系统会这样处理</strong></header>
              <p>{editorExplanation.result}</p>
              <div><span>示例</span><strong>{editorExplanation.example}</strong></div>
              <small>系统仍会通过已发布指标、维度和决策图校验最终查询，不会由这条设置直接生成 SQL。</small>
            </section>

            <footer>
              <button className="quiet-button" type="button" onClick={() => setEditor(null)}>取消</button>
              <button className="primary-button" type="submit" disabled={busy}>{busy ? '正在保存…' : '保存并立即生效'}</button>
            </footer>
          </form>
        </div>
      )}

      {deprecating && (
        <div className="semantic-asset-dialog-backdrop" role="presentation">
          <section className="semantic-asset-dialog compact" role="dialog" aria-modal="true" aria-label="停用问句解析设置">
            <header><div><span className="eyebrow">使用范围：当前租户</span><h2>停用这条问法设置？</h2></div></header>
            <p>
              停用“{deprecating.pattern}”后，当前租户的新问题将不再使用这条识别方式。
              {hasPlatformEquivalent && ' 这条设置与系统内置规则相同，停用后也不会自动恢复系统内置行为。'}
            </p>
            <footer>
              <button className="quiet-button" type="button" onClick={() => setDeprecating(null)}>取消</button>
              <button className="danger-button" type="button" disabled={busy} onClick={() => void confirmDeprecate()}>确认停用</button>
            </footer>
          </section>
        </div>
      )}
    </AppShell>
  )
}
