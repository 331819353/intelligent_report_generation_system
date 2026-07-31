import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react'
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

const ruleDefinitions: Record<SemanticParsingRuleType, {
  label: string
  description: string
  matchMode: SemanticParsingRuleInput['matchMode']
  action: SemanticParsingRuleInput['action']
  minimumLength: number
  maximumLength: number
}> = {
  METRIC_NAME_SUFFIX: {
    label: '指标名称后缀',
    description: '从已发布指标名称或别名中移除后缀，生成可精确识别的指标词干。',
    matchMode: 'SUFFIX', action: 'STRIP_SUFFIX',
    minimumLength: 2, maximumLength: 0,
  },
  ADMIN_REGION_SUFFIX: {
    label: '行政区划后缀',
    description: '将“北京 + 市”等表达归一为维度名称、维度编码与维度值。',
    matchMode: 'SUFFIX', action: 'MAP_ADMIN_REGION',
    minimumLength: 2, maximumLength: 12,
  },
  QUERY_RESIDUAL_TERM: {
    label: '问句剩余词',
    description: '允许这些功能性词语通过确定性解析，不触发额外模型补全。',
    matchMode: 'EXACT', action: 'ALLOW_DETERMINISTIC',
    minimumLength: 0, maximumLength: 0,
  },
  BROAD_METRIC_PHRASE: {
    label: '宽泛指标问法',
    description: '命中后必须由用户确认指标，避免模型擅自选择一组经营指标。',
    matchMode: 'CONTAINS', action: 'REQUIRE_METRIC_CONFIRMATION',
    minimumLength: 0, maximumLength: 0,
  },
}

const ruleTypeOrder = Object.keys(ruleDefinitions) as SemanticParsingRuleType[]

const emptyDraft = (
  ruleType: SemanticParsingRuleType = 'METRIC_NAME_SUFFIX',
): SemanticParsingRuleInput => {
  const definition = ruleDefinitions[ruleType]
  return {
    ruleType,
    pattern: '',
    matchMode: definition.matchMode,
    action: definition.action,
    outputName: '',
    outputCode: '',
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
      const page = await semanticParsingRuleAPI.list({
        q: query.trim(), ruleType, status,
      })
      setRules(page.items)
      setTotal(page.total)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setLoading(false)
    }
  }, [canRead, permissionsReady, query, ruleType, status])

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadRules() }, 0)
    return () => window.clearTimeout(timer)
  }, [loadRules])

  const counts = useMemo(() => ({
    platform: rules.filter(rule => rule.scope === 'PLATFORM').length,
    tenant: rules.filter(rule => rule.scope === 'TENANT').length,
    active: rules.filter(rule => rule.status === 'ACTIVE').length,
  }), [rules])

  const changeRuleType = (nextType: SemanticParsingRuleType) => {
    if (!editor) return
    const next = emptyDraft(nextType)
    setEditor({
      ...editor,
      draft: {
        ...next,
        pattern: editor.draft.pattern,
        priority: editor.draft.priority,
      },
    })
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
      setNotice({ tone: 'error', message: '匹配表达不能为空。' })
      return
    }
    if (input.ruleType === 'ADMIN_REGION_SUFFIX' &&
      (!input.outputName || !input.outputCode)) {
      setNotice({ tone: 'error', message: '行政区划规则必须配置输出维度名称和编码。' })
      return
    }
    setBusy(true)
    try {
      if (editor.editing) {
        await semanticParsingRuleAPI.update(
          editor.editing.id, editor.editing.version, input,
        )
        setNotice({ tone: 'success', message: '解析规则已更新，下一次问答立即生效。' })
      } else {
        await semanticParsingRuleAPI.create(input)
        setNotice({
          tone: 'success',
          message: editor.source?.status === 'DEPRECATED'
            ? '租户规则已重新启用，下一次问答立即生效。'
            : '租户解析规则已保存，下一次问答立即生效。',
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
      setNotice({
        tone: 'success',
        message: '租户规则已停用；若其覆盖平台规则，平台规则也会被屏蔽。',
      })
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

  return (
    <AppShell title="资产管理中心" eyebrow="数据资产 · 解析规则">
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
          <div className="semantic-asset-empty">正在检查解析规则权限…</div>
        ) : !canRead ? (
          <div className="semantic-asset-empty denied">当前账号没有解析规则读取权限。</div>
        ) : (
          <>
            {!canManage && (
              <div className="semantic-asset-readonly" role="note">
                当前为只读模式；新增、覆盖、编辑和停用需要 DATASET:MANAGE。
              </div>
            )}
            <header className="semantic-asset-hero">
              <div>
                <span className="eyebrow">语义解析规则</span>
                <h2>管理确定性解析规则，不再通过发版修改词表</h2>
                <p>
                  规则存储于平台基础 PostgreSQL，不参与向量化。平台规则提供默认值；
                  租户可按“类型 + 表达”覆盖，保存后下一次智能问答立即生效。
                </p>
              </div>
              <button
                className="primary-button"
                type="button"
                disabled={!canManage}
                onClick={() => setEditor({ draft: emptyDraft() })}
              >
                新增租户规则
              </button>
            </header>

            <div className="semantic-asset-summary" aria-label="解析规则概览">
              <article><span>平台默认</span><strong>{counts.platform}</strong><small>跨租户只读基线</small></article>
              <article><span>租户配置</span><strong>{counts.tenant}</strong><small>同类型同表达优先</small></article>
              <article><span>当前生效</span><strong>{counts.active}</strong><small>保存后按请求热加载</small></article>
              <article><span>筛选结果</span><strong>{total}</strong><small>精确规则，不向量化</small></article>
            </div>

            <div className="semantic-asset-filters semantic-rule-filters">
              <label>
                搜索
                <input
                  aria-label="搜索解析规则"
                  value={query}
                  placeholder="表达、输出维度名称或编码"
                  onChange={event => setQuery(event.target.value)}
                />
              </label>
              <label>
                规则类型
                <select
                  aria-label="解析规则类型"
                  value={ruleType}
                  onChange={event => setRuleType(
                    event.target.value as SemanticParsingRuleType | '',
                  )}
                >
                  <option value="">全部类型</option>
                  {ruleTypeOrder.map(value => (
                    <option key={value} value={value}>{ruleDefinitions[value].label}</option>
                  ))}
                </select>
              </label>
              <label>
                状态
                <select
                  aria-label="解析规则状态"
                  value={status}
                  onChange={event => setStatus(
                    event.target.value as SemanticParsingRuleStatus | '',
                  )}
                >
                  <option value="">全部状态</option>
                  <option value="ACTIVE">生效</option>
                  <option value="DEPRECATED">已停用</option>
                </select>
              </label>
              <button className="quiet-button" type="button" disabled={loading} onClick={() => void loadRules()}>
                重新加载
              </button>
            </div>

            {loading ? (
              <div className="semantic-asset-empty">正在加载解析规则…</div>
            ) : rules.length === 0 ? (
              <div className="semantic-asset-empty">
                <strong>当前筛选下没有解析规则</strong>
                <span>新增租户规则后，下一次智能问答会直接读取。</span>
              </div>
            ) : (
              <div className="semantic-asset-table-wrap">
                <table className="semantic-asset-table semantic-rule-table">
                  <thead>
                    <tr>
                      <th>规则类型</th><th>匹配表达</th><th>解析动作</th>
                      <th>输出 / 约束</th><th>范围 / 状态</th><th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rules.map(rule => (
                      <tr key={rule.id}>
                        <td>
                          <span className="semantic-type-chip">{ruleDefinitions[rule.ruleType].label}</span>
                          <small className="semantic-subtype">{rule.ruleType}</small>
                        </td>
                        <td><strong>{rule.pattern}</strong><small className="semantic-rule-priority">优先级 {rule.priority}</small></td>
                        <td>{rule.matchMode}<small className="semantic-subtype">{rule.action}</small></td>
                        <td>
                          {rule.ruleType === 'ADMIN_REGION_SUFFIX'
                            ? `${rule.outputName} · ${rule.outputCode}`
                            : ruleDefinitions[rule.ruleType].description}
                          {(rule.minimumLength > 0 || rule.maximumLength > 0) && (
                            <small className="semantic-rule-limit">
                              值长度 {rule.minimumLength}–{rule.maximumLength || '不限'} 字
                            </small>
                          )}
                        </td>
                        <td>
                          <span className={`semantic-rule-scope ${rule.scope.toLowerCase()}`}>
                            {rule.scope === 'PLATFORM' ? '平台默认' : '当前租户'}
                          </span>
                          <small className={`semantic-rule-status ${rule.status.toLowerCase()}`}>
                            {rule.status === 'ACTIVE' ? '生效' : '已停用'} · v{rule.version}
                          </small>
                        </td>
                        <td>
                          <div className="semantic-asset-actions">
                            {rule.scope === 'PLATFORM' ? (
                              <button
                                type="button"
                                disabled={!canManage}
                                onClick={() => setEditor({ source: rule, draft: draftOf(rule) })}
                              >
                                创建租户覆盖
                              </button>
                            ) : rule.status === 'ACTIVE' ? (
                              <>
                                <button
                                  type="button"
                                  disabled={!canManage}
                                  onClick={() => setEditor({ editing: rule, draft: draftOf(rule) })}
                                >编辑</button>
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
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </section>

      {editor && (
        <div className="semantic-asset-dialog-backdrop" role="presentation">
          <form
            className="semantic-asset-dialog semantic-rule-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="配置语义解析规则"
            onSubmit={event => void submitEditor(event)}
          >
            <header>
              <div>
                <span className="eyebrow">下一次问答立即生效</span>
                <h2>{editor.editing ? '编辑租户规则' : editor.source?.scope === 'PLATFORM' ? '创建租户覆盖' : editor.source ? '重新启用租户规则' : '新增租户规则'}</h2>
              </div>
              <button type="button" aria-label="关闭解析规则编辑器" onClick={() => setEditor(null)}>×</button>
            </header>
            <p className="semantic-rule-help">{ruleDefinitions[editor.draft.ruleType].description}</p>
            <label>
              规则类型
              <select
                aria-label="编辑规则类型"
                value={editor.draft.ruleType}
                disabled={Boolean(editor.editing || editor.source)}
                onChange={event => changeRuleType(event.target.value as SemanticParsingRuleType)}
              >
                {ruleTypeOrder.map(value => (
                  <option key={value} value={value}>{ruleDefinitions[value].label}</option>
                ))}
              </select>
            </label>
            <label>
              匹配表达
              <input
                aria-label="匹配表达"
                value={editor.draft.pattern}
                disabled={Boolean(editor.source)}
                placeholder="例如：总量、市、经营情况"
                onChange={event => setEditor({ ...editor, draft: { ...editor.draft, pattern: event.target.value } })}
              />
            </label>
            <div className="semantic-rule-form-grid">
              <label>匹配方式<input value={editor.draft.matchMode} disabled /></label>
              <label>解析动作<input value={editor.draft.action} disabled /></label>
            </div>
            {editor.draft.ruleType === 'ADMIN_REGION_SUFFIX' && (
              <div className="semantic-rule-form-grid">
                <label>
                  输出维度名称
                  <input
                    aria-label="输出维度名称"
                    value={editor.draft.outputName || ''}
                    placeholder="例如：城市"
                    onChange={event => setEditor({ ...editor, draft: { ...editor.draft, outputName: event.target.value } })}
                  />
                </label>
                <label>
                  输出维度编码
                  <input
                    aria-label="输出维度编码"
                    value={editor.draft.outputCode || ''}
                    placeholder="例如：city"
                    onChange={event => setEditor({ ...editor, draft: { ...editor.draft, outputCode: event.target.value } })}
                  />
                </label>
              </div>
            )}
            <div className="semantic-rule-form-grid three">
              <label>
                优先级
                <input type="number" min="0" max="1000" value={editor.draft.priority} onChange={event => setEditor({ ...editor, draft: { ...editor.draft, priority: Number(event.target.value) } })} />
              </label>
              <label>
                最小值长度
                <input type="number" min="0" max="256" disabled={editor.draft.minimumLength === 0} value={editor.draft.minimumLength} onChange={event => setEditor({ ...editor, draft: { ...editor.draft, minimumLength: Number(event.target.value) } })} />
              </label>
              <label>
                最大值长度
                <input type="number" min="0" max="256" disabled={editor.draft.ruleType !== 'ADMIN_REGION_SUFFIX'} value={editor.draft.maximumLength} onChange={event => setEditor({ ...editor, draft: { ...editor.draft, maximumLength: Number(event.target.value) } })} />
              </label>
            </div>
            <footer>
              <button className="quiet-button" type="button" onClick={() => setEditor(null)}>取消</button>
              <button className="primary-button" type="submit" disabled={busy}>{busy ? '正在保存…' : '保存规则'}</button>
            </footer>
          </form>
        </div>
      )}

      {deprecating && (
        <div className="semantic-asset-dialog-backdrop" role="presentation">
          <section className="semantic-asset-dialog compact" role="dialog" aria-modal="true" aria-label="停用解析规则">
            <header><div><span className="eyebrow">热更新规则</span><h2>停用租户规则</h2></div></header>
            <p>确认停用“{deprecating.pattern}”？若它覆盖同名平台规则，平台默认规则也将被屏蔽。</p>
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
