import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import { AssetSharingSelect } from '../components/AssetSharingSelect'
import { RequestError } from '../lib/api'
import {
  semanticAssetAPI,
  type SemanticAsset,
  type SemanticAssetEmbeddingStatus,
  type SemanticAssetInput,
  type SemanticAssetStatus,
} from '../lib/semantic-assets'

type Notice = { tone: 'success' | 'error' | 'info'; message: string }
type SemanticGroup = 'DIMENSION' | 'METRIC'
type EditorState = {
  asset?: SemanticAsset
  group: SemanticGroup
  draft: SemanticAssetInput
}

const embeddingLabels: Record<SemanticAssetEmbeddingStatus, string> = {
  PENDING: '待向量化',
  SUCCEEDED: '已向量化',
  FAILED: '向量化失败',
  SKIPPED: '已跳过',
}

const statusLabels: Record<SemanticAssetStatus, string> = {
  ACTIVE: '生效',
  DEPRECATED: '已停用',
}

const semanticGroupLabels: Record<SemanticGroup, { label: string; type: string }> = {
  DIMENSION: {
    label: '维度语义',
    type: 'DIMENSION_SEMANTIC',
  },
  METRIC: {
    label: '指标语义',
    type: 'METRIC_SEMANTIC',
  },
}

const semanticGroupOf = (knowledgeType: string): SemanticGroup => {
  const normalized = knowledgeType.trim().toLocaleUpperCase('zh-CN')
  return normalized.includes('METRIC') ||
    normalized.includes('MEASURE') ||
    normalized.includes('指标')
    ? 'METRIC'
    : 'DIMENSION'
}

const emptyDraft = (group: SemanticGroup): SemanticAssetInput => ({
  commonTerm: '',
  mappingValue: '',
  knowledgeType: semanticGroupLabels[group].type,
})

const errorMessage = (cause: unknown) =>
  cause instanceof RequestError || cause instanceof Error
    ? cause.message
    : '操作失败，请稍后重试。'

export function SemanticAssetPage() {
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [canRead, setCanRead] = useState(false)
  const [canManage, setCanManage] = useState(false)
  const [assets, setAssets] = useState<SemanticAsset[]>([])
  const [total, setTotal] = useState(0)
  const [knowledgeTypes, setKnowledgeTypes] = useState<string[]>([])
  const [semanticGroup, setSemanticGroup] = useState<SemanticGroup>('DIMENSION')
  const [query, setQuery] = useState('')
  const [knowledgeType, setKnowledgeType] = useState('')
  const [status, setStatus] = useState<SemanticAssetStatus | ''>('ACTIVE')
  const [embeddingStatus, setEmbeddingStatus] =
    useState<SemanticAssetEmbeddingStatus | ''>('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [editor, setEditor] = useState<EditorState | null>(null)
  const [deprecating, setDeprecating] = useState<SemanticAsset | null>(null)

  useEffect(() => {
    let active = true
    Promise.all([
      semanticAssetAPI.evaluatePermission('READ'),
      semanticAssetAPI.evaluatePermission('MANAGE'),
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

  const loadAssets = useCallback(async () => {
    if (!permissionsReady || !canRead) {
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const [page, types] = await Promise.all([
        semanticAssetAPI.list({
          q: query.trim(), knowledgeType, status, embeddingStatus,
        }),
        semanticAssetAPI.listKnowledgeTypes(),
      ])
      setAssets(page.items)
      setTotal(page.total)
      setKnowledgeTypes(types.items)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setLoading(false)
    }
  }, [
    canRead, embeddingStatus, knowledgeType,
    permissionsReady, query, status,
  ])

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadAssets() }, 0)
    return () => window.clearTimeout(timer)
  }, [loadAssets])

  const embeddedCount = useMemo(
    () => assets.filter(item => item.embeddingStatus === 'SUCCEEDED').length,
    [assets],
  )
  const pendingCount = useMemo(
    () => assets.filter(item => item.embeddingStatus === 'PENDING').length,
    [assets],
  )
  const groupedAssets = useMemo(
    () => assets.filter(item => semanticGroupOf(item.knowledgeType) === semanticGroup),
    [assets, semanticGroup],
  )
  const groupCounts = useMemo(() => ({
    DIMENSION: assets.filter(item => semanticGroupOf(item.knowledgeType) === 'DIMENSION').length,
    METRIC: assets.filter(item => semanticGroupOf(item.knowledgeType) === 'METRIC').length,
  }), [assets])

  const submitEditor = async (event: FormEvent) => {
    event.preventDefault()
    if (!editor) return
    const input = {
      commonTerm: editor.draft.commonTerm.trim(),
      mappingValue: editor.draft.mappingValue.trim(),
      knowledgeType: editor.draft.knowledgeType.trim(),
    }
    if (!input.commonTerm || !input.mappingValue || !input.knowledgeType) {
      setNotice({ tone: 'error', message: '用户表达、标准语义和细分类型均不能为空。' })
      return
    }
    setBusy(true)
    try {
      if (editor.asset) {
        await semanticAssetAPI.update(
          editor.asset.id, editor.asset.version, input,
        )
        setNotice({ tone: 'success', message: '语义资产已更新。' })
      } else {
        await semanticAssetAPI.create(input)
        setNotice({
          tone: 'success',
          message: `${semanticGroupLabels[editor.group].label}已创建，用户表达已进入独立向量化队列。`,
        })
      }
      setEditor(null)
      await loadAssets()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) {
        await loadAssets()
      }
    } finally {
      setBusy(false)
    }
  }

  const confirmDeprecate = async () => {
    if (!deprecating) return
    setBusy(true)
    try {
      await semanticAssetAPI.deprecate(
        deprecating.id, deprecating.version,
      )
      setNotice({ tone: 'success', message: '语义资产已停用。' })
      setDeprecating(null)
      await loadAssets()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) {
        await loadAssets()
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <AppShell title="资产管理中心" eyebrow="数据资产 · 语义资产">
      <AssetManagementTabs />
      <section className="semantic-asset-page">
        {notice && (
          <div
            className={`semantic-asset-notice ${notice.tone}`}
            role={notice.tone === 'error' ? 'alert' : 'status'}
          >
            <span>{notice.message}</span>
            <button
              type="button"
              aria-label="关闭提示"
              onClick={() => setNotice(null)}
            >
              ×
            </button>
          </div>
        )}

        {!permissionsReady ? (
          <div className="semantic-asset-empty">正在检查语义资产权限…</div>
        ) : !canRead ? (
          <div className="semantic-asset-empty denied">
            当前账号没有语义资产读取权限。
          </div>
        ) : (
          <>
            {!canManage && (
              <div className="semantic-asset-readonly" role="note">
                当前为只读模式；新增、编辑和停用需要 DATASET:MANAGE。
              </div>
            )}
            <header className="semantic-asset-hero">
              <div>
                <span className="eyebrow">语义资产</span>
                <h2>把用户表达纠正为标准业务语义</h2>
                <p>
                  维度语义处理实体歧义，指标语义纠正问题表达；两类资产分别存放、
                  独立向量化，命中后返回确定的标准语义。
                </p>
              </div>
              <button
                className="primary-button"
                type="button"
                disabled={!canManage}
                onClick={() => setEditor({
                  group: semanticGroup,
                  draft: emptyDraft(semanticGroup),
                })}
              >
                新增{semanticGroupLabels[semanticGroup].label}
              </button>
            </header>

            <div className="semantic-asset-summary" aria-label="语义资产概览">
              <article>
                <span>维度语义</span><strong>{groupCounts.DIMENSION}</strong>
                <small>解决实体名称、简称和歧义</small>
              </article>
              <article>
                <span>指标语义</span><strong>{groupCounts.METRIC}</strong>
                <small>纠正问法并映射标准指标</small>
              </article>
              <article>
                <span>当前结果</span><strong>{total}</strong>
                <small>{pendingCount} 条等待向量化</small>
              </article>
              <article>
                <span>已向量化</span><strong>{embeddedCount}</strong>
                <small>当前页可参与语义匹配</small>
              </article>
            </div>

            <div className="semantic-group-switch" role="tablist" aria-label="语义资产分类">
              {(Object.keys(semanticGroupLabels) as SemanticGroup[]).map(group => (
                <button
                  key={group}
                  type="button"
                  role="tab"
                  aria-selected={semanticGroup === group}
                  onClick={() => setSemanticGroup(group)}
                >
                  {semanticGroupLabels[group].label}
                </button>
              ))}
            </div>

            <div className="semantic-asset-filters">
              <label>
                搜索
                <input
                  aria-label="搜索用户表达、标准语义或类型"
                  value={query}
                  placeholder="输入用户表达、标准语义或类型"
                  onChange={event => setQuery(event.target.value)}
                />
              </label>
              <label>
                类型
                <select
                  aria-label="语义资产类型"
                  value={knowledgeType}
                  onChange={event => {
                    const value = event.target.value
                    setKnowledgeType(value)
                    if (value) setSemanticGroup(semanticGroupOf(value))
                  }}
                >
                  <option value="">全部类型</option>
                  {knowledgeTypes.map(value => (
                    <option key={value} value={value}>{value}</option>
                  ))}
                </select>
              </label>
              <label>
                状态
                <select
                  aria-label="语义资产状态"
                  value={status}
                  onChange={event =>
                    setStatus(event.target.value as SemanticAssetStatus | '')}
                >
                  <option value="">全部状态</option>
                  {Object.entries(statusLabels).map(([value, label]) => (
                    <option key={value} value={value}>{label}</option>
                  ))}
                </select>
              </label>
              <label>
                向量状态
                <select
                  aria-label="语义资产向量状态"
                  value={embeddingStatus}
                  onChange={event => setEmbeddingStatus(
                    event.target.value as SemanticAssetEmbeddingStatus | '',
                  )}
                >
                  <option value="">全部向量状态</option>
                  {Object.entries(embeddingLabels).map(([value, label]) => (
                    <option key={value} value={value}>{label}</option>
                  ))}
                </select>
              </label>
              <button
                className="quiet-button"
                type="button"
                disabled={loading}
                onClick={() => void loadAssets()}
              >
                重新加载
              </button>
            </div>

            {loading ? (
              <div className="semantic-asset-empty">正在加载语义资产…</div>
            ) : groupedAssets.length === 0 ? (
              <div className="semantic-asset-empty">
                <strong>当前筛选下没有{semanticGroupLabels[semanticGroup].label}</strong>
                <span>新增表达映射后会自动进入独立向量化队列。</span>
              </div>
            ) : (
              <div className="semantic-asset-table-wrap">
                <table className="semantic-asset-table">
                  <thead>
                    <tr>
                      <th>用户表达</th>
                      <th>标准语义</th>
                      <th>语义分类</th>
                      <th>向量状态</th>
                      <th>资产状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groupedAssets.map(asset => (
                      <tr key={asset.id}>
                        <td><strong>{asset.commonTerm}</strong></td>
                        <td>{asset.mappingValue}</td>
                        <td><span className={`semantic-type-chip ${semanticGroupOf(asset.knowledgeType).toLowerCase()}`}>{semanticGroupLabels[semanticGroupOf(asset.knowledgeType)].label}</span><small className="semantic-subtype">{asset.knowledgeType}</small></td>
                        <td>
                          <span className={`embedding-status ${asset.embeddingStatus.toLowerCase()}`}>
                            {embeddingLabels[asset.embeddingStatus]}
                          </span>
                          {asset.embeddingErrorCode && (
                            <small>{asset.embeddingErrorCode}</small>
                          )}
                        </td>
                        <td>{statusLabels[asset.status]}</td>
                        <td>
                          <div className="semantic-asset-actions">
                            <AssetSharingSelect
                              resourceType="SEMANTIC_ASSET"
                              resourceID={asset.id}
                              value={asset.sharingScope || 'PRIVATE'}
                              ownerUserID={asset.createdBy}
                              assetDomainID={asset.domainId}
                              disabled={!canManage}
                              onChange={sharingScope => setAssets(current => current.map(item =>
                                item.id === asset.id ? { ...item, sharingScope } : item
                              ))}
                            />
                            <button
                              type="button"
                              disabled={!canManage || asset.status !== 'ACTIVE'}
                              onClick={() => setEditor({
                                asset,
                                group: semanticGroupOf(asset.knowledgeType),
                                draft: {
                                  commonTerm: asset.commonTerm,
                                  mappingValue: asset.mappingValue,
                                  knowledgeType: asset.knowledgeType,
                                },
                              })}
                            >
                              编辑
                            </button>
                            <button
                              type="button"
                              disabled={!canManage || asset.status !== 'ACTIVE'}
                              onClick={() => setDeprecating(asset)}
                            >
                              停用
                            </button>
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
            className="semantic-asset-dialog"
            role="dialog"
            aria-modal="true"
            aria-label={editor.asset ? '编辑语义资产' : '新增语义资产'}
            onSubmit={event => void submitEditor(event)}
          >
            <header>
              <div>
                <span className="eyebrow">{semanticGroupLabels[editor.group].label}</span>
                <h2>{editor.asset ? '编辑语义资产' : `新增${semanticGroupLabels[editor.group].label}`}</h2>
              </div>
              <button
                type="button"
                aria-label="关闭语义资产编辑器"
                onClick={() => setEditor(null)}
              >
                ×
              </button>
            </header>
            <label>
              语义分类
              <select
                aria-label="语义分类"
                value={editor.group}
                disabled={Boolean(editor.asset)}
                onChange={event => {
                  const group = event.target.value as SemanticGroup
                  setEditor({
                    ...editor,
                    group,
                    draft: {
                      ...editor.draft,
                      knowledgeType: semanticGroupLabels[group].type,
                    },
                  })
                }}
              >
                <option value="DIMENSION">维度语义 · 解决实体歧义</option>
                <option value="METRIC">指标语义 · 纠正问题表达</option>
              </select>
            </label>
            <label>
              用户表达
              <input
                aria-label="用户表达"
                value={editor.draft.commonTerm}
                placeholder={editor.group === 'DIMENSION' ? '例如：智家' : '例如：有多少人'}
                onChange={event => setEditor({
                  ...editor,
                  draft: { ...editor.draft, commonTerm: event.target.value },
                })}
              />
            </label>
            <label>
              标准语义
              <input
                aria-label="标准语义"
                value={editor.draft.mappingValue}
                placeholder={editor.group === 'DIMENSION' ? '例如：智家生态圈' : '例如：员工人数'}
                onChange={event => setEditor({
                  ...editor,
                  draft: { ...editor.draft, mappingValue: event.target.value },
                })}
              />
            </label>
            <label>
              细分类型
              <input
                aria-label="类型"
                list="semantic-asset-type-options"
                value={editor.draft.knowledgeType}
                onChange={event => setEditor({
                  ...editor,
                  draft: { ...editor.draft, knowledgeType: event.target.value },
                })}
              />
              <datalist id="semantic-asset-type-options">
                {knowledgeTypes.map(value => (
                  <option key={value} value={value} />
                ))}
              </datalist>
            </label>
            <footer>
              <button
                className="quiet-button"
                type="button"
                onClick={() => setEditor(null)}
              >
                取消
              </button>
              <button
                className="primary-button"
                type="submit"
                disabled={busy}
              >
                {busy ? '正在保存…' : '保存'}
              </button>
            </footer>
          </form>
        </div>
      )}

      {deprecating && (
        <div className="semantic-asset-dialog-backdrop" role="presentation">
          <section
            className="semantic-asset-dialog compact"
            role="dialog"
            aria-modal="true"
            aria-label="停用语义资产"
          >
            <header>
              <div>
                <span className="eyebrow">Deprecate</span>
                <h2>停用语义资产</h2>
              </div>
            </header>
            <p>
              确认停用“{deprecating.commonTerm}”？停用后该词不再参与语义匹配。
            </p>
            <footer>
              <button
                className="quiet-button"
                type="button"
                onClick={() => setDeprecating(null)}
              >
                取消
              </button>
              <button
                className="danger-button"
                type="button"
                disabled={busy}
                onClick={() => void confirmDeprecate()}
              >
                确认停用
              </button>
            </footer>
          </section>
        </div>
      )}
    </AppShell>
  )
}
