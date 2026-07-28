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
  semanticAssetAPI,
  type SemanticAsset,
  type SemanticAssetEmbeddingStatus,
  type SemanticAssetInput,
  type SemanticAssetStatus,
} from '../lib/semantic-assets'

type Notice = { tone: 'success' | 'error' | 'info'; message: string }
type EditorState = {
  asset?: SemanticAsset
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

const emptyDraft = (): SemanticAssetInput => ({
  commonTerm: '',
  mappingValue: '',
  knowledgeType: '',
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
    void loadAssets()
  }, [loadAssets])

  const embeddedCount = useMemo(
    () => assets.filter(item => item.embeddingStatus === 'SUCCEEDED').length,
    [assets],
  )
  const pendingCount = useMemo(
    () => assets.filter(item => item.embeddingStatus === 'PENDING').length,
    [assets],
  )

  const submitEditor = async (event: FormEvent) => {
    event.preventDefault()
    if (!editor) return
    const input = {
      commonTerm: editor.draft.commonTerm.trim(),
      mappingValue: editor.draft.mappingValue.trim(),
      knowledgeType: editor.draft.knowledgeType.trim(),
    }
    if (!input.commonTerm || !input.mappingValue || !input.knowledgeType) {
      setNotice({ tone: 'error', message: '常用词、映射值和类型均不能为空。' })
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
          message: '语义资产已创建，常用词已进入向量化队列。',
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
    <AppShell title="资产管理中心" eyebrow="指标 · 语义 · 维度">
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
                <span className="eyebrow">Semantic Dictionary</span>
                <h2>常用词语义映射</h2>
                <p>
                  常用词单独向量化；匹配后返回确定的映射值与知识类型。
                  本目录与 DWS 维度资产相互独立。
                </p>
              </div>
              <button
                className="primary-button"
                type="button"
                disabled={!canManage}
                onClick={() => setEditor({ draft: emptyDraft() })}
              >
                新增语义资产
              </button>
            </header>

            <div className="semantic-asset-summary" aria-label="语义资产概览">
              <article>
                <span>当前结果</span><strong>{total}</strong>
                <small>符合筛选的语义资产</small>
              </article>
              <article>
                <span>已向量化</span><strong>{embeddedCount}</strong>
                <small>当前页向量可用</small>
              </article>
              <article>
                <span>待向量化</span><strong>{pendingCount}</strong>
                <small>当前页异步处理中</small>
              </article>
              <article>
                <span>知识类型</span><strong>{knowledgeTypes.length}</strong>
                <small>租户内受控类型</small>
              </article>
            </div>

            <div className="semantic-asset-filters">
              <label>
                搜索
                <input
                  aria-label="搜索常用词、映射值或类型"
                  value={query}
                  placeholder="输入常用词、映射值或类型"
                  onChange={event => setQuery(event.target.value)}
                />
              </label>
              <label>
                类型
                <select
                  aria-label="语义资产类型"
                  value={knowledgeType}
                  onChange={event => setKnowledgeType(event.target.value)}
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
            ) : assets.length === 0 ? (
              <div className="semantic-asset-empty">
                <strong>当前筛选下没有语义资产</strong>
                <span>新增常用词映射后会自动进入向量化队列。</span>
              </div>
            ) : (
              <div className="semantic-asset-table-wrap">
                <table className="semantic-asset-table">
                  <thead>
                    <tr>
                      <th>常用词</th>
                      <th>映射值</th>
                      <th>类型</th>
                      <th>向量状态</th>
                      <th>资产状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {assets.map(asset => (
                      <tr key={asset.id}>
                        <td><strong>{asset.commonTerm}</strong></td>
                        <td>{asset.mappingValue}</td>
                        <td><span className="semantic-type-chip">{asset.knowledgeType}</span></td>
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
                            <button
                              type="button"
                              disabled={!canManage || asset.status !== 'ACTIVE'}
                              onClick={() => setEditor({
                                asset,
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
                <span className="eyebrow">Semantic Asset</span>
                <h2>{editor.asset ? '编辑语义资产' : '新增语义资产'}</h2>
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
              常用词
              <input
                aria-label="常用词"
                value={editor.draft.commonTerm}
                onChange={event => setEditor({
                  ...editor,
                  draft: { ...editor.draft, commonTerm: event.target.value },
                })}
              />
            </label>
            <label>
              映射值
              <input
                aria-label="映射值"
                value={editor.draft.mappingValue}
                onChange={event => setEditor({
                  ...editor,
                  draft: { ...editor.draft, mappingValue: event.target.value },
                })}
              />
            </label>
            <label>
              类型
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
