import { type FormEvent, useEffect, useState } from 'react'
import { AppShell } from '../components/AppShell'
import {
  dataSourceAPI,
  type CreateDataSourceInput,
  type DataSourceRecord,
  type DataSourceStatus,
  type DataSourceType,
} from '../lib/data-sources'

const statusLabels: Record<DataSourceStatus, string> = {
  DRAFT: '待验证',
  ACTIVE: '运行中',
  DISABLED: '已停用',
  SYNCING: '同步中',
  ERROR: '异常',
  DELETING: '删除中',
}

const typeLabels: Record<DataSourceType, string> = {
  MYSQL: 'MySQL',
  ORACLE: 'Oracle',
  EXCEL: 'Excel / CSV',
}

const emptyDraft = (): CreateDataSourceInput => ({
  code: '',
  name: '',
  type: 'MYSQL',
  config: {},
  secretRef: '',
})

/** 展示租户数据源目录，并通过受控表单创建数据库数据源草稿。 */
export function DataSourceCenterPage() {
  const [sources, setSources] = useState<DataSourceRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [draft, setDraft] = useState<CreateDataSourceInput>(emptyDraft)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState('')

  useEffect(() => {
    let active = true
    dataSourceAPI.list().then(page => {
      if (!active) return
      setSources(page.items)
      setLoadError('')
    }).catch(cause => {
      if (active) setLoadError(cause instanceof Error ? cause.message : '加载数据源失败')
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [])

  const openCreate = () => {
    setDraft(emptyDraft())
    setFormError('')
    setShowCreate(true)
  }

  const closeCreate = () => {
    if (saving) return
    setShowCreate(false)
    setFormError('')
  }

  const submitCreate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const input = {
      ...draft,
      code: draft.code.trim(),
      name: draft.name.trim(),
      secretRef: draft.secretRef.trim(),
    }
    if (!input.code || !input.name || !input.secretRef) {
      setFormError('请完整填写数据源名称、编码和凭证引用')
      return
    }
    setSaving(true)
    setFormError('')
    try {
      const created = await dataSourceAPI.create(input)
      // 创建响应是服务端最终事实，直接加入目录可避免紧接着的列表读取被代理延迟。
      setSources(current => [created, ...current.filter(source => source.id !== created.id)])
      setShowCreate(false)
      setDraft(emptyDraft())
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '新建数据源失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <AppShell
      title="数据源配置中心"
      eyebrow="工作栏"
      actions={<button className="primary-button" type="button" onClick={openCreate}>新建数据源</button>}
    >
      <section className="data-source-center" aria-label="数据源配置中心内容">
        <header className="data-source-summary">
          <div><span className="eyebrow">数据源清单</span><h2>已有的数据源</h2><p>统一查看当前租户可用的数据库与文件数据源。</p></div>
          <strong aria-label={`${sources.length} 个数据源`}>{sources.length}<small> 个数据源</small></strong>
        </header>

        {loadError && <div className="data-source-feedback error" role="alert">{loadError}</div>}
        {loading ? <div className="data-source-empty">正在加载数据源…</div> : sources.length === 0
          ? <div className="data-source-empty"><strong>还没有数据源</strong><span>点击右上角“新建数据源”开始配置。</span></div>
          : <div className="data-source-list" role="list" aria-label="已有数据源清单">
              {sources.map(source => <article className="data-source-card" role="listitem" key={source.id}>
                <div className={`data-source-icon ${source.type.toLowerCase()}`}>{source.type === 'EXCEL' ? 'XL' : 'DB'}</div>
                <div className="data-source-main">
                  <div><h3>{source.name}</h3><span className={`data-source-status ${source.status.toLowerCase()}`}>{statusLabels[source.status]}</span></div>
                  <p>{typeLabels[source.type]} · {source.code}</p>
                </div>
                <dl>
                  <div><dt>类型</dt><dd>{typeLabels[source.type]}</dd></div>
                  <div><dt>配置版本</dt><dd>V{source.version}</dd></div>
                </dl>
              </article>)}
            </div>}
      </section>

      {showCreate && <div className="data-source-dialog-backdrop" role="presentation" onMouseDown={event => {
        if (event.target === event.currentTarget) closeCreate()
      }}>
        <section className="data-source-dialog" role="dialog" aria-modal="true" aria-labelledby="create-data-source-title">
          <header><div><span className="eyebrow">创建配置</span><h2 id="create-data-source-title">新建数据源</h2></div><button type="button" aria-label="关闭新建数据源" onClick={closeCreate}>×</button></header>
          <form onSubmit={submitCreate}>
            <label>数据源名称<input autoFocus value={draft.name} onChange={event => setDraft(current => ({ ...current, name: event.target.value }))} placeholder="例如：销售业务库" /></label>
            <label>数据源编码<input value={draft.code} onChange={event => setDraft(current => ({ ...current, code: event.target.value }))} placeholder="例如：sales_mysql" /></label>
            <label>数据源类型<select value={draft.type} onChange={event => setDraft(current => ({ ...current, type: event.target.value as CreateDataSourceInput['type'] }))}><option value="MYSQL">MySQL</option><option value="ORACLE">Oracle</option></select></label>
            <label>凭证引用<input aria-label="凭证引用" value={draft.secretRef} onChange={event => setDraft(current => ({ ...current, secretRef: event.target.value }))} placeholder={draft.type === 'ORACLE' ? 'env://ORACLE_SOURCE_SECRET' : 'env://MYSQL_SOURCE_SECRET'} /><small>仅填写密钥引用，不在页面中录入或保存明文密码。</small></label>
            {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
            <footer><button className="quiet-button" type="button" disabled={saving} onClick={closeCreate}>取消</button><button className="primary-button" type="submit" disabled={saving}>{saving ? '正在创建…' : '创建数据源'}</button></footer>
          </form>
        </section>
      </div>}
    </AppShell>
  )
}
